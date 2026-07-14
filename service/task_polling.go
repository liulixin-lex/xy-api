package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

const (
	maxTaskPollingResponseBytes         int64 = 2 << 20
	maxTaskBillingOperationsPerPass           = 100
	taskBillingOperationLease                 = time.Minute
	historicalTaskAutomaticRefundCutoff       = int64(1740182400) // 2025-02-22 00:00:00 UTC
)

var (
	errTaskPollingCredentialIdentityMissing = errors.New("task credential identity is missing")
	errTaskPollingCredentialUnavailable     = errors.New("task credential is unavailable")
	errTaskPollingResponseTooLarge          = errors.New("task polling response is too large")
)

type taskPollingTarget struct {
	ChannelID              int
	CredentialID           int
	LegacyCredentialDigest string
}

type taskPollingKey struct {
	taskPollingTarget
	UpstreamTaskID string
}

func readTaskPollingResponse(body io.Reader, maxBytes int64) ([]byte, error) {
	if body == nil || maxBytes <= 0 {
		return nil, errors.New("task polling response reader is invalid")
	}
	responseBody, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(responseBody)) > maxBytes {
		return nil, fmt.Errorf("%w: limit=%d", errTaskPollingResponseTooLarge, maxBytes)
	}
	return responseBody, nil
}

// TaskPollingAdaptor 定义轮询所需的最小适配器接口，避免 service -> relay 的循环依赖
type TaskPollingAdaptor interface {
	Init(info *relaycommon.RelayInfo)
	// The worker context must reach every upstream request made by the poll,
	// including token refreshes and secondary result lookups.
	FetchTask(ctx context.Context, baseURL string, key string, body map[string]any, proxy string) (*http.Response, error)
	ParseTaskResult(ctx context.Context, body []byte) (*relaycommon.TaskInfo, error)
	// AdjustBillingOnComplete 在任务到达终态（成功/失败）时由轮询循环调用。
	// 返回正数触发差额结算（补扣/退还），返回 0 保持预扣费金额不变。
	AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int
}

// GetTaskAdaptorFunc 由 main 包注入，用于获取指定平台的任务适配器。
// 打破 service -> relay -> relay/channel -> service 的循环依赖。
var GetTaskAdaptorFunc func(platform constant.TaskPlatform) TaskPollingAdaptor

// sweepTimedOutTasks 在主轮询之前独立清理超时任务。
// 每次最多处理 100 条，剩余的下个周期继续处理。
// 使用 per-task CAS (UpdateWithStatus) 防止覆盖被正常轮询已推进的任务。
func sweepTimedOutTasks(ctx context.Context) {
	if constant.TaskTimeoutMinutes <= 0 {
		return
	}
	cutoff := time.Now().Unix() - int64(constant.TaskTimeoutMinutes)*60
	tasks := model.GetTimedOutUnfinishedTasks(cutoff, 100)
	if len(tasks) == 0 {
		return
	}

	reason := fmt.Sprintf("任务超时（%d分钟）", constant.TaskTimeoutMinutes)
	legacyReason := "任务超时（旧系统遗留任务，不进行退款，请联系管理员）"
	now := time.Now().Unix()
	timedOutCount := 0

	for _, task := range tasks {
		unsupportedProtocol := !model.SupportsDurableTaskBillingProtocol(task.PrivateData.BillingProtocolVersion)

		if unsupportedProtocol {
			oldStatus := task.Status
			task.Status = model.TaskStatusFailure
			task.Progress = "100%"
			task.FinishTime = now
			task.FailReason = legacyReason
			won, err := task.UpdateWithStatus(oldStatus)
			if err != nil {
				logger.LogError(ctx, fmt.Sprintf("sweepTimedOutTasks legacy CAS update error for task %s: %v", task.TaskID, err))
				continue
			}
			if !won {
				continue
			}
			// Historical rows intentionally retain their original charge. Calling
			// finalization after the CAS creates a completed/noop audit marker.
			_, _ = FinalizeTaskObservation(ctx, TaskFinalizationObservation{
				TaskID: task.ID, TerminalStatus: model.TaskStatusFailure,
				Progress: "100%", FinishTime: now, FailReason: legacyReason,
			})
			timedOutCount++
			continue
		}

		failReason := reason
		preserveChargeReason := ""
		if task.PrivateData.BillingProtocolVersion == model.TaskBillingHistoricalProtocolVersion &&
			(task.SubmitTime <= 0 || task.SubmitTime < historicalTaskAutomaticRefundCutoff) {
			failReason = legacyReason
			preserveChargeReason = "historical task predates the automatic timeout-refund cutoff; charge retained"
		}
		_, err := finalizePolledTask(ctx, TaskFinalizationObservation{
			TaskID: task.ID, TerminalStatus: model.TaskStatusFailure,
			Progress: "100%", FinishTime: now, FailReason: failReason,
			PreserveChargeReason: preserveChargeReason,
		})
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("sweepTimedOutTasks finalization error for task %s: %v", task.TaskID, err))
			continue
		}
		timedOutCount++
	}

	if timedOutCount > 0 {
		logger.LogInfo(ctx, fmt.Sprintf("sweepTimedOutTasks: timed out %d tasks", timedOutCount))
	}
}

// TaskPollSummary is the result recorded on an async_task_poll system task row,
// summarizing one polling pass.
type TaskPollSummary struct {
	UnfinishedTasks            int `json:"unfinished_tasks"`
	PlatformsScanned           int `json:"platforms_scanned"`
	NullTasksFailed            int `json:"null_tasks_failed"`
	BillingOperationsProcessed int `json:"billing_operations_processed"`
	BillingOperationErrors     int `json:"billing_operation_errors"`
}

// RunTaskPollingOnce performs one async-task (Suno/video) polling pass
// synchronously. It honors ctx cancellation (the system-task runner cancels it
// when the lease is lost) and, when report is non-nil, reports progress as
// (processedPlatforms, totalPlatforms). It returns immediately if the task
// adaptor factory has not been wired yet, to avoid a nil call during startup.
func RunTaskPollingOnce(ctx context.Context, report func(processed, total int)) TaskPollSummary {
	summary := TaskPollSummary{}
	if ctx == nil {
		ctx = context.Background()
	}
	processTaskBillingBacklog(ctx, &summary)
	if !constant.UpdateTask {
		return summary
	}
	if GetTaskAdaptorFunc == nil {
		return summary
	}

	common.SysLog("任务进度轮询开始")
	sweepTimedOutTasks(ctx)
	allTasks := model.GetAllUnFinishSyncTasks(constant.TaskQueryLimit)
	summary.UnfinishedTasks = len(allTasks)
	platformTask := make(map[constant.TaskPlatform][]*model.Task)
	for _, t := range allTasks {
		platformTask[t.Platform] = append(platformTask[t.Platform], t)
	}

	totalPlatforms := len(platformTask)
	processedPlatforms := 0
	for platform, tasks := range platformTask {
		if ctx.Err() != nil {
			break
		}
		if report != nil {
			report(processedPlatforms, totalPlatforms)
		}
		processedPlatforms++
		if len(tasks) == 0 {
			continue
		}
		summary.PlatformsScanned++
		validTasks := make([]*model.Task, 0, len(tasks))
		for _, task := range tasks {
			upstreamID := task.GetUpstreamTaskID()
			if upstreamID == "" {
				_, finalizeErr := finalizePolledTask(ctx, TaskFinalizationObservation{
					TaskID: task.ID, TerminalStatus: model.TaskStatusFailure,
					Progress:   taskcommon.ProgressComplete,
					FailReason: "upstream task identity is missing",
				})
				if finalizeErr != nil {
					logger.LogError(ctx, fmt.Sprintf("finalize task with missing upstream identity failed: task=%s error=%v", task.TaskID, finalizeErr))
				} else {
					summary.NullTasksFailed++
				}
				continue
			}
			validTasks = append(validTasks, task)
		}
		if len(validTasks) == 0 {
			continue
		}

		DispatchPlatformUpdate(ctx, platform, validTasks)
	}
	if report != nil && ctx.Err() == nil {
		report(totalPlatforms, totalPlatforms)
	}
	common.SysLog("任务进度轮询完成")
	return summary
}

func processTaskBillingBacklog(ctx context.Context, summary *TaskPollSummary) {
	if summary == nil || ctx.Err() != nil {
		return
	}
	owner := "task-billing-" + common.GetUUID()
	for processedCount := 0; processedCount < maxTaskBillingOperationsPerPass; processedCount++ {
		operation, processed, err := ProcessNextTaskBillingOperation(ctx, owner, taskBillingOperationLease)
		if !processed {
			if err != nil {
				summary.BillingOperationErrors++
				logger.LogError(ctx, fmt.Sprintf("claim task billing operation failed: %v", err))
			}
			return
		}
		summary.BillingOperationsProcessed++
		if err != nil {
			summary.BillingOperationErrors++
			operationKey := "unknown"
			if operation != nil && operation.OperationKey != "" {
				operationKey = operation.OperationKey
			}
			logger.LogWarn(ctx, fmt.Sprintf("task billing operation remains recoverable: operation=%s error=%s",
				operationKey, common.SanitizeErrorMessage(err.Error())))
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func finalizePolledTask(ctx context.Context, observation TaskFinalizationObservation) (*model.Task, error) {
	result, err := FinalizeTaskObservation(ctx, observation)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Task == nil {
		return nil, errors.New("task finalization returned an incomplete result")
	}
	if result.ManualReview {
		return result.Task, nil
	}
	if result.Operation == nil {
		return nil, errors.New("task finalization returned no billing operation")
	}
	_, processErr := ProcessTaskBillingOperation(
		ctx,
		result.Operation.ID,
		"task-billing-"+common.GetUUID(),
		taskBillingOperationLease,
	)
	if errors.Is(processErr, model.ErrTaskBillingOperationNotClaimed) {
		processErr = nil
	}
	if processErr != nil {
		logger.LogWarn(ctx, fmt.Sprintf("task billing operation deferred for recovery: operation=%s error=%s",
			result.Operation.OperationKey, common.SanitizeErrorMessage(processErr.Error())))
	}
	return result.Task, nil
}

// DispatchPlatformUpdate 按平台分发轮询更新
func DispatchPlatformUpdate(ctx context.Context, platform constant.TaskPlatform, tasks []*model.Task) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch platform {
	case constant.TaskPlatformMidjourney:
		// MJ 轮询由其自身处理，这里预留入口
	case constant.TaskPlatformSuno:
		_ = UpdateSunoTasks(ctx, tasks)
	default:
		if err := UpdateVideoTasks(ctx, platform, tasks); err != nil {
			common.SysLog(fmt.Sprintf("UpdateVideoTasks fail: %s", err))
		}
	}
}

func groupTaskPollingTargets(tasks []*model.Task) ([]taskPollingTarget, map[taskPollingTarget][]*model.Task) {
	groups := make(map[taskPollingTarget][]*model.Task)
	for _, task := range tasks {
		if task == nil || task.ChannelId <= 0 || task.GetUpstreamTaskID() == "" {
			continue
		}
		target := taskPollingTarget{
			ChannelID:    task.ChannelId,
			CredentialID: task.PrivateData.RoutingCredentialID,
		}
		if credential, ok := task.PrivateData.HistoricalPlaintextCredential(); ok {
			digest := sha256.Sum256([]byte(credential))
			target.LegacyCredentialDigest = hex.EncodeToString(digest[:])
		}
		groups[target] = append(groups[target], task)
	}
	targets := make([]taskPollingTarget, 0, len(groups))
	for target := range groups {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].ChannelID != targets[j].ChannelID {
			return targets[i].ChannelID < targets[j].ChannelID
		}
		if targets[i].CredentialID != targets[j].CredentialID {
			return targets[i].CredentialID < targets[j].CredentialID
		}
		return targets[i].LegacyCredentialDigest < targets[j].LegacyCredentialDigest
	})
	return targets, groups
}

func failTaskPollingTarget(ctx context.Context, tasks []*model.Task, reason string) error {
	var updateErrors []error
	for _, task := range tasks {
		if task == nil {
			continue
		}
		_, err := finalizePolledTask(ctx, TaskFinalizationObservation{
			TaskID: task.ID, TerminalStatus: model.TaskStatusFailure,
			Progress: taskcommon.ProgressComplete, FinishTime: time.Now().Unix(), FailReason: reason,
		})
		if err != nil {
			updateErrors = append(updateErrors, fmt.Errorf("fail task %s: %w", task.TaskID, err))
		}
	}
	return errors.Join(updateErrors...)
}

type taskPollingCredentialResolver func(context.Context, *model.Channel, int) (string, int, error)

func resolveTaskPollingCredential(
	ctx context.Context,
	channel *model.Channel,
	target taskPollingTarget,
	tasks []*model.Task,
) (string, error) {
	privateData := model.TaskPrivateData{RoutingCredentialID: target.CredentialID}
	if target.LegacyCredentialDigest != "" {
		for _, task := range tasks {
			if task == nil {
				continue
			}
			credential, ok := task.PrivateData.HistoricalPlaintextCredential()
			if !ok {
				return "", errTaskPollingCredentialIdentityMissing
			}
			digest := sha256.Sum256([]byte(credential))
			if hex.EncodeToString(digest[:]) != target.LegacyCredentialDigest {
				return "", errTaskPollingCredentialIdentityMissing
			}
			if privateData.Key != "" && privateData.Key != credential {
				return "", errTaskPollingCredentialIdentityMissing
			}
			privateData = task.PrivateData
		}
		if privateData.Key == "" {
			return "", errTaskPollingCredentialIdentityMissing
		}
	}
	return resolveTaskPollingCredentialWithResolver(ctx, channel, privateData, channelrouting.ResolvePersistedCredentialKey)
}

func resolveTaskPollingCredentialWithResolver(
	ctx context.Context,
	channel *model.Channel,
	privateData model.TaskPrivateData,
	resolve taskPollingCredentialResolver,
) (string, error) {
	if channel == nil {
		return "", errTaskPollingCredentialUnavailable
	}
	credentialID := privateData.RoutingCredentialID
	if credentialID > 0 {
		credential, _, err := resolve(ctx, channel, credentialID)
		if err != nil {
			if errors.Is(err, channelrouting.ErrPersistedCredentialUnavailable) {
				return "", fmt.Errorf("%w: channel=%d credential=%d", errTaskPollingCredentialUnavailable, channel.Id, credentialID)
			}
			return "", fmt.Errorf("resolve persisted task credential: %w", err)
		}
		if strings.TrimSpace(credential) == "" {
			return "", fmt.Errorf("%w: channel=%d credential=%d", errTaskPollingCredentialUnavailable, channel.Id, credentialID)
		}
		return credential, nil
	}
	if credential, ok := privateData.HistoricalPlaintextCredential(); ok {
		return credential, nil
	}
	if channel.ChannelInfo.IsMultiKey || len(channel.GetKeys()) > 1 {
		return "", fmt.Errorf("%w: channel=%d", errTaskPollingCredentialIdentityMissing, channel.Id)
	}
	if strings.TrimSpace(channel.Key) == "" {
		return "", fmt.Errorf("%w: channel=%d", errTaskPollingCredentialUnavailable, channel.Id)
	}
	return channel.Key, nil
}

func taskPollingCredentialFailureIsPermanent(err error) bool {
	return errors.Is(err, errTaskPollingCredentialIdentityMissing) ||
		errors.Is(err, errTaskPollingCredentialUnavailable)
}

// UpdateSunoTasks 按渠道和稳定 Credential 更新所有 Suno 任务。
func UpdateSunoTasks(ctx context.Context, tasks []*model.Task) error {
	if ctx == nil {
		ctx = context.Background()
	}
	targets, groups := groupTaskPollingTargets(tasks)
	var updateErrors []error
	for _, target := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := updateSunoTasks(ctx, target, groups[target])
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("渠道 #%d Credential #%d 更新异步任务失败: %s", target.ChannelID, target.CredentialID, err.Error()))
			updateErrors = append(updateErrors, err)
		}
	}
	return errors.Join(updateErrors...)
}

func updateSunoTasks(ctx context.Context, target taskPollingTarget, tasks []*model.Task) error {
	logger.LogInfo(ctx, fmt.Sprintf("渠道 #%d Credential #%d 未完成的任务有: %d", target.ChannelID, target.CredentialID, len(tasks)))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(tasks) == 0 {
		return nil
	}
	ch, err := model.GetChannelById(target.ChannelID, true)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			reason := fmt.Sprintf("渠道已不存在，渠道ID：%d", target.ChannelID)
			return errors.Join(err, failTaskPollingTarget(ctx, tasks, reason))
		}
		return fmt.Errorf("load Suno task channel %d: %w", target.ChannelID, err)
	}
	adaptor := GetTaskAdaptorFunc(constant.TaskPlatformSuno)
	if adaptor == nil {
		return errors.New("adaptor not found")
	}
	credential, err := resolveTaskPollingCredential(ctx, ch, target, tasks)
	if err != nil {
		if taskPollingCredentialFailureIsPermanent(err) {
			reason := "任务绑定的渠道凭据已永久不可用"
			return errors.Join(err, failTaskPollingTarget(ctx, tasks, reason))
		}
		return err
	}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelId:           ch.Id,
		ChannelBaseUrl:      ch.GetBaseURL(),
		ApiKey:              credential,
		RoutingCredentialID: target.CredentialID,
	}})
	baseURL := ch.GetBaseURL()
	if baseURL == "" {
		baseURL = constant.ChannelBaseURLs[ch.Type]
	}
	proxy := ch.GetSetting().Proxy
	taskIDs := make([]string, 0, len(tasks))
	taskM := make(map[taskPollingKey]*model.Task, len(tasks))
	for _, task := range tasks {
		key := taskPollingKey{taskPollingTarget: target, UpstreamTaskID: task.GetUpstreamTaskID()}
		if _, duplicated := taskM[key]; duplicated {
			return fmt.Errorf("duplicate task polling identity channel=%d credential=%d upstream_task_id=%s", target.ChannelID, target.CredentialID, key.UpstreamTaskID)
		}
		taskM[key] = task
		taskIDs = append(taskIDs, key.UpstreamTaskID)
	}
	resp, err := adaptor.FetchTask(ctx, baseURL, credential, map[string]any{
		"ids": taskIDs,
	}, proxy)
	if err != nil {
		common.SysLog("Get Task request failed: " + common.SanitizeErrorMessage(err.Error()))
		return err
	}
	if resp == nil {
		return errors.New("Get Suno Task returned an empty response")
	}
	defer resp.Body.Close()
	responseBody, err := readTaskPollingResponse(resp.Body, maxTaskPollingResponseBytes)
	if err != nil {
		common.SysLog(fmt.Sprintf("Get Suno Task parse body error: %v", err))
		return err
	}
	if resp.StatusCode != http.StatusOK {
		logger.LogError(ctx, fmt.Sprintf("Get Task status code: %d body_bytes=%d", resp.StatusCode, len(responseBody)))
		return fmt.Errorf("Get Task status code: %d", resp.StatusCode)
	}
	var responseItems dto.TaskResponse[[]dto.SunoDataResponse]
	err = common.Unmarshal(responseBody, &responseItems)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Get Suno Task response parse failed: %v body_bytes=%d", err, len(responseBody)))
		return err
	}
	if !responseItems.IsSuccess() {
		safeMessage := common.SanitizeErrorMessage(responseItems.Message)
		common.SysLog(fmt.Sprintf("渠道 #%d Credential #%d 任务查询失败: code=%s message=%s", target.ChannelID, target.CredentialID, responseItems.Code, safeMessage))
		return fmt.Errorf("Suno task query failed: code=%s message=%s", responseItems.Code, safeMessage)
	}

	var itemErrors []error
	for _, responseItem := range responseItems.Data {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		task := taskM[taskPollingKey{taskPollingTarget: target, UpstreamTaskID: responseItem.TaskID}]
		if task == nil {
			logger.LogWarn(ctx, fmt.Sprintf("Suno task response ignored: channel=%d credential=%d unknown task_id=%s", target.ChannelID, target.CredentialID, responseItem.TaskID))
			continue
		}
		if !taskNeedsUpdate(task, responseItem) {
			continue
		}

		status := lo.If(model.TaskStatus(responseItem.Status) != "", model.TaskStatus(responseItem.Status)).Else(task.Status)
		failReason := lo.If(responseItem.FailReason != "", responseItem.FailReason).Else(task.FailReason)
		submitTime := lo.If(responseItem.SubmitTime != 0, responseItem.SubmitTime).Else(task.SubmitTime)
		startTime := lo.If(responseItem.StartTime != 0, responseItem.StartTime).Else(task.StartTime)
		finishTime := lo.If(responseItem.FinishTime != 0, responseItem.FinishTime).Else(task.FinishTime)
		if responseItem.FailReason != "" {
			status = model.TaskStatusFailure
		}
		if status == model.TaskStatusSuccess || status == model.TaskStatusFailure {
			_, finalizeErr := finalizePolledTask(ctx, TaskFinalizationObservation{
				TaskID: task.ID, TerminalStatus: status, Progress: taskcommon.ProgressComplete,
				SubmitTime: submitTime, StartTime: startTime, FinishTime: finishTime,
				FailReason: failReason, Data: responseItem.Data,
			})
			if finalizeErr != nil {
				itemErrors = append(itemErrors, fmt.Errorf("finalize Suno task %s: %w", task.TaskID, finalizeErr))
			}
			continue
		}

		snap := task.Snapshot()
		task.Status = status
		task.FailReason = common.SanitizeErrorMessage(failReason)
		task.SubmitTime = submitTime
		task.StartTime = startTime
		task.FinishTime = finishTime
		task.Data = SanitizeTaskData(responseItem.Data)
		won, updateErr := task.UpdateWithStatus(snap.Status)
		if updateErr != nil {
			itemErrors = append(itemErrors, fmt.Errorf("update Suno task %s: %w", task.TaskID, updateErr))
			continue
		}
		if !won {
			logger.LogWarn(ctx, fmt.Sprintf("Suno task %s already changed by another poller", task.TaskID))
			continue
		}
	}
	return errors.Join(itemErrors...)
}

// taskNeedsUpdate 检查 Suno 任务是否需要更新
func taskNeedsUpdate(oldTask *model.Task, newTask dto.SunoDataResponse) bool {
	if oldTask.SubmitTime != newTask.SubmitTime {
		return true
	}
	if oldTask.StartTime != newTask.StartTime {
		return true
	}
	if oldTask.FinishTime != newTask.FinishTime {
		return true
	}
	if string(oldTask.Status) != newTask.Status {
		return true
	}
	if oldTask.FailReason != newTask.FailReason {
		return true
	}

	if (oldTask.Status == model.TaskStatusFailure || oldTask.Status == model.TaskStatusSuccess) && oldTask.Progress != "100%" {
		return true
	}

	oldData, _ := common.Marshal(oldTask.Data)
	newData, _ := common.Marshal(newTask.Data)

	sort.Slice(oldData, func(i, j int) bool {
		return oldData[i] < oldData[j]
	})
	sort.Slice(newData, func(i, j int) bool {
		return newData[i] < newData[j]
	})

	if string(oldData) != string(newData) {
		return true
	}
	return false
}

// UpdateVideoTasks 按渠道和稳定 Credential 并行更新视频任务。
func UpdateVideoTasks(ctx context.Context, platform constant.TaskPlatform, tasks []*model.Task) error {
	if ctx == nil {
		ctx = context.Background()
	}
	targets, groups := groupTaskPollingTargets(tasks)
	var wg sync.WaitGroup
	errCh := make(chan error, len(targets))
	for _, target := range targets {
		targetTasks := groups[target]
		if len(targetTasks) == 0 {
			continue
		}
		targetTasks = append([]*model.Task(nil), targetTasks...)

		wg.Add(1)
		gopool.Go(func() {
			defer wg.Done()
			if err := updateVideoTasks(ctx, platform, target, targetTasks); err != nil {
				logger.LogError(ctx, fmt.Sprintf("Channel #%d Credential #%d failed to update video async tasks: %s", target.ChannelID, target.CredentialID, err.Error()))
				errCh <- err
			}
		})
	}
	wg.Wait()
	close(errCh)
	var updateErrors []error
	for err := range errCh {
		updateErrors = append(updateErrors, err)
	}
	if ctx.Err() != nil {
		updateErrors = append(updateErrors, ctx.Err())
	}
	return errors.Join(updateErrors...)
}

func updateVideoTasks(ctx context.Context, platform constant.TaskPlatform, target taskPollingTarget, tasks []*model.Task) error {
	logger.LogInfo(ctx, fmt.Sprintf("Channel #%d Credential #%d pending video tasks: %d", target.ChannelID, target.CredentialID, len(tasks)))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(tasks) == 0 {
		return nil
	}
	cacheGetChannel, err := model.GetChannelById(target.ChannelID, true)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			reason := fmt.Sprintf("Channel no longer exists, channel ID: %d", target.ChannelID)
			return errors.Join(err, failTaskPollingTarget(ctx, tasks, reason))
		}
		return fmt.Errorf("load video task channel %d: %w", target.ChannelID, err)
	}
	adaptor := GetTaskAdaptorFunc(platform)
	if adaptor == nil {
		return fmt.Errorf("video adaptor not found")
	}
	credential, err := resolveTaskPollingCredential(ctx, cacheGetChannel, target, tasks)
	if err != nil {
		if taskPollingCredentialFailureIsPermanent(err) {
			reason := "Task credential is permanently unavailable"
			return errors.Join(err, failTaskPollingTarget(ctx, tasks, reason))
		}
		return err
	}
	info := &relaycommon.RelayInfo{}
	info.ChannelMeta = &relaycommon.ChannelMeta{
		ChannelId:           cacheGetChannel.Id,
		ChannelBaseUrl:      cacheGetChannel.GetBaseURL(),
		ApiKey:              credential,
		RoutingCredentialID: target.CredentialID,
	}
	adaptor.Init(info)
	disablePollingSleep := cacheGetChannel.GetOtherSettings().DisableTaskPollingSleep
	var updateErrors []error
	for i, task := range tasks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := updateVideoSingleTask(ctx, adaptor, cacheGetChannel, credential, task); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Failed to update video task %s: %s", task.GetUpstreamTaskID(), err.Error()))
			updateErrors = append(updateErrors, err)
		}
		if disablePollingSleep || i == len(tasks)-1 {
			continue
		}

		// sleep 1 second between tasks for this channel only.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return errors.Join(updateErrors...)
}

func updateVideoSingleTask(ctx context.Context, adaptor TaskPollingAdaptor, ch *model.Channel, credential string, task *model.Task) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if task == nil {
		return errors.New("video task is nil")
	}
	upstreamTaskID := task.GetUpstreamTaskID()
	if upstreamTaskID == "" {
		return fmt.Errorf("task %s has no upstream task id", task.TaskID)
	}
	baseURL := constant.ChannelBaseURLs[ch.Type]
	if ch.GetBaseURL() != "" {
		baseURL = ch.GetBaseURL()
	}
	proxy := ch.GetSetting().Proxy
	resp, err := adaptor.FetchTask(ctx, baseURL, credential, map[string]any{
		"task_id": upstreamTaskID,
		"action":  task.Action,
	}, proxy)
	if err != nil {
		return fmt.Errorf("fetchTask failed for task %s: %w", upstreamTaskID, err)
	}
	if resp == nil {
		return fmt.Errorf("fetchTask returned an empty response for task %s", upstreamTaskID)
	}
	defer resp.Body.Close()
	responseBody, err := readTaskPollingResponse(resp.Body, maxTaskPollingResponseBytes)
	if err != nil {
		return fmt.Errorf("readAll failed for task %s: %w", upstreamTaskID, err)
	}

	logger.LogDebug(ctx, "updateVideoSingleTask response: status=%d bytes=%d", resp.StatusCode, len(responseBody))

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("task polling returned transient status %d for task %s", resp.StatusCode, upstreamTaskID)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		reason := fmt.Sprintf("upstream task query returned status %d", resp.StatusCode)
		_, finalizeErr := finalizePolledTask(ctx, TaskFinalizationObservation{
			TaskID: task.ID, TerminalStatus: model.TaskStatusFailure,
			Progress: taskcommon.ProgressComplete, FinishTime: time.Now().Unix(), FailReason: reason,
		})
		return errors.Join(fmt.Errorf("%s", reason), finalizeErr)
	}

	taskResult := &relaycommon.TaskInfo{}
	sanitizedData := SanitizeTaskData(responseBody)
	var responseItems dto.TaskResponse[dto.TaskDto]
	if err = common.Unmarshal(responseBody, &responseItems); err == nil && responseItems.IsSuccess() {
		item := responseItems.Data
		logger.LogDebug(ctx, "updateVideoSingleTask parsed response: task=%s status=%s progress=%s", upstreamTaskID, item.Status, item.Progress)
		taskResult.TaskID = item.TaskID
		taskResult.Status = item.Status
		taskResult.Url = item.ResultURL
		taskResult.Progress = item.Progress
		taskResult.Reason = item.FailReason
		sanitizedData = SanitizeTaskData(item.Data)
	} else if taskResult, err = adaptor.ParseTaskResult(ctx, responseBody); err != nil {
		return fmt.Errorf("parseTaskResult failed for task %s: %w", upstreamTaskID, err)
	}

	logger.LogDebug(ctx, "updateVideoSingleTask taskResult: task=%s status=%s progress=%s", upstreamTaskID, taskResult.Status, taskResult.Progress)
	if taskResult.Status == "" {
		var errorResult dto.GeneralErrorResponse
		if unmarshalErr := common.Unmarshal(responseBody, &errorResult); unmarshalErr != nil {
			return UnparseableUpstreamResponseError(unmarshalErr, responseBody)
		}
		openAIError := errorResult.TryToOpenAIError()
		if openAIError == nil {
			return UnparseableUpstreamResponseError(errors.New("upstream task status is empty"), responseBody)
		}
		if openAIError.Code == "429" {
			return nil
		}
		taskResult = relaycommon.FailTaskInfo("upstream returned error")
	}

	now := time.Now().Unix()
	status := model.TaskStatus(taskResult.Status)
	progress := taskResult.Progress
	if progress == "" {
		switch status {
		case model.TaskStatusSubmitted:
			progress = taskcommon.ProgressSubmitted
		case model.TaskStatusQueued:
			progress = taskcommon.ProgressQueued
		case model.TaskStatusInProgress:
			progress = taskcommon.ProgressInProgress
		case model.TaskStatusSuccess, model.TaskStatusFailure:
			progress = taskcommon.ProgressComplete
		}
	}

	upstreamResultURL := strings.TrimSpace(taskResult.Url)
	if upstreamResultURL == "" {
		upstreamResultURL = strings.TrimSpace(taskResult.RemoteUrl)
	}
	if strings.HasPrefix(strings.ToLower(upstreamResultURL), "data:") {
		upstreamResultURL = ""
	}

	if status == model.TaskStatusSuccess || status == model.TaskStatusFailure {
		finishTime := task.FinishTime
		if finishTime == 0 {
			finishTime = now
		}
		startTime := task.StartTime
		if startTime == 0 && status == model.TaskStatusSuccess {
			startTime = now
		}
		var actualQuota *int
		if status == model.TaskStatusSuccess {
			if adjusted := adaptor.AdjustBillingOnComplete(task, taskResult); adjusted > 0 {
				actualQuota = &adjusted
			}
		}
		stored, finalizeErr := finalizePolledTask(ctx, TaskFinalizationObservation{
			TaskID: task.ID, TerminalStatus: status, Progress: progress,
			SubmitTime: task.SubmitTime, StartTime: startTime, FinishTime: finishTime,
			FailReason: taskResult.Reason, UpstreamResultURL: upstreamResultURL,
			Data: sanitizedData, ActualQuota: actualQuota, TotalTokens: taskResult.TotalTokens,
		})
		if finalizeErr != nil {
			return fmt.Errorf("finalize task %s: %w", task.TaskID, finalizeErr)
		}
		*task = *stored
		return nil
	}

	if status != model.TaskStatusSubmitted && status != model.TaskStatusQueued && status != model.TaskStatusInProgress {
		return fmt.Errorf("unknown task status %s for task %s", taskResult.Status, task.TaskID)
	}
	snap := task.Snapshot()
	task.Status = status
	task.Progress = progress
	if status == model.TaskStatusInProgress && task.StartTime == 0 {
		task.StartTime = now
	}
	if upstreamResultURL != "" {
		task.SetUpstreamResultURL(upstreamResultURL)
	}
	task.Data = sanitizedData
	if snap.Equal(task.Snapshot()) {
		return nil
	}
	won, updateErr := task.UpdateWithStatus(snap.Status)
	if updateErr != nil {
		return fmt.Errorf("update task %s: %w", task.TaskID, updateErr)
	}
	if !won {
		stored, exists, reloadErr := model.GetByTaskId(task.UserId, task.TaskID)
		if reloadErr != nil {
			return reloadErr
		}
		if exists && stored != nil {
			*task = *stored
		}
	}
	return nil
}
