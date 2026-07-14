package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

const (
	maxMidjourneyPollingResponseBytes = 2 << 20
	midjourneySubmissionRecoveryDelay = 2 * time.Minute
	midjourneyPollingBatchSize        = 200
)

var errMidjourneyPollingResponseTooLarge = errors.New("Midjourney polling response is too large")

type midjourneyPollingTarget struct {
	ChannelID         int
	CredentialID      int
	ChannelGeneration string
}

type midjourneyPollingKey struct {
	Target         midjourneyPollingTarget
	UpstreamTaskID string
}

// midjourneyPollSummary is the result recorded on a midjourney_poll system task
// row, summarizing one polling pass.
type midjourneyPollSummary struct {
	UnfinishedTasks   int `json:"unfinished_tasks"`
	ChannelsScanned   int `json:"channels_scanned"`
	NullTasksFailed   int `json:"null_tasks_failed"`
	TimedOutTasks     int `json:"timed_out_tasks"`
	BillingOperations int `json:"billing_operations"`
}

// runMidjourneyTaskUpdateOnce performs one Midjourney polling pass synchronously.
// It honors ctx cancellation (the system-task runner cancels it when the lease
// is lost) and, when report is non-nil, reports progress as (processedChannels,
// totalChannels) so the system task surfaces a percentage.
func runMidjourneyTaskUpdateOnce(ctx context.Context, report func(processed, total int)) midjourneyPollSummary {
	summary := midjourneyPollSummary{}
	if ctx == nil {
		ctx = context.Background()
	}
	billingOwner := "midjourney-poll:" + common.GetUUID()
	summary.BillingOperations += drainMidjourneyBillingOperations(ctx, billingOwner, 100)
	if !constant.UpdateTask {
		return summary
	}
	tasks, err := model.FindUnfinishedMidjourneyTasks(5_000)
	if err != nil {
		logger.LogError(ctx, "load unfinished Midjourney tasks: "+err.Error())
		return summary
	}
	if len(tasks) == 0 {
		return summary
	}
	nowMs := time.Now().UnixMilli()
	activeTasks := make([]*model.Midjourney, 0, len(tasks))
	timedOutTasks := make([]*model.Midjourney, 0)
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if task.SubmitTime <= 0 || nowMs-task.SubmitTime > time.Hour.Milliseconds() {
			timedOutTasks = append(timedOutTasks, task)
			continue
		}
		activeTasks = append(activeTasks, task)
	}
	if len(timedOutTasks) > 0 {
		summary.TimedOutTasks = len(timedOutTasks)
		failMidjourneyPollingTasks(ctx, timedOutTasks, "Midjourney task timed out")
	}
	tasks = activeTasks
	if len(tasks) == 0 {
		summary.BillingOperations += drainMidjourneyBillingOperations(ctx, billingOwner, 100)
		return summary
	}
	summary.UnfinishedTasks = len(tasks)
	logger.LogInfo(ctx, fmt.Sprintf("检测到未完成的任务数有: %d", len(tasks)))

	targets, grouped, unrecoverable, ambiguous := groupMidjourneyPollingTasks(tasks, time.Now())
	if len(unrecoverable) > 0 {
		summary.NullTasksFailed += len(unrecoverable)
		failMidjourneyPollingTasks(ctx, unrecoverable, "Midjourney 上游任务身份缺失")
	}
	if len(ambiguous) > 0 {
		summary.NullTasksFailed += len(ambiguous)
		failMidjourneyPollingTasks(ctx, ambiguous, "Midjourney 上游任务身份冲突")
	}
	if len(targets) == 0 {
		return summary
	}

	for index, target := range targets {
		if ctx.Err() != nil {
			break
		}
		if report != nil {
			report(index, len(targets))
		}
		summary.ChannelsScanned++
		targetTasks := grouped[target]
		if len(targetTasks) == 0 {
			continue
		}
		logger.LogInfo(ctx, fmt.Sprintf(
			"渠道 #%d Credential #%d 未完成的 Midjourney 任务有: %d",
			target.ChannelID, target.CredentialID, len(targetTasks),
		))

		channel, err := model.CacheGetChannel(target.ChannelID)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("Midjourney CacheGetChannel: %v", err))
			continue
		}
		if target.ChannelGeneration != "" && target.ChannelGeneration != channel.RoutingGeneration {
			failMidjourneyPollingTasks(ctx, targetTasks, "原 Midjourney 渠道已被替换")
			continue
		}
		credential, err := resolveMidjourneyPollingCredential(ctx, channel, target.CredentialID)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf(
				"渠道 #%d Credential #%d Midjourney 轮询凭据不可用: %v",
				target.ChannelID, target.CredentialID, err,
			))
			if errors.Is(err, channelrouting.ErrPersistedCredentialUnavailable) {
				failMidjourneyPollingTasks(ctx, targetTasks, "原 Midjourney 渠道凭据已不可用")
			}
			continue
		}

		requestURL := strings.TrimRight(channel.GetBaseURL(), "/") + "/mj/task/list-by-condition"
		for start := 0; start < len(targetTasks); start += midjourneyPollingBatchSize {
			if ctx.Err() != nil {
				break
			}
			end := min(start+midjourneyPollingBatchSize, len(targetTasks))
			batch := targetTasks[start:end]
			taskIDs := make([]string, 0, len(batch))
			tasksByIdentity := make(map[midjourneyPollingKey][]*model.Midjourney, len(batch))
			for _, task := range batch {
				upstreamTaskID := task.GetUpstreamTaskID()
				key := midjourneyPollingKey{Target: target, UpstreamTaskID: upstreamTaskID}
				tasksByIdentity[key] = append(tasksByIdentity[key], task)
				taskIDs = append(taskIDs, upstreamTaskID)
			}
			body, err := common.Marshal(map[string]any{"ids": taskIDs})
			if err != nil {
				logger.LogError(ctx, fmt.Sprintf("Get Midjourney Task marshal body error: %v", err))
				continue
			}

			requestContext, cancel := context.WithTimeout(ctx, 15*time.Second)
			req, err := http.NewRequestWithContext(requestContext, http.MethodPost, requestURL, bytes.NewReader(body))
			if err != nil {
				cancel()
				logger.LogError(ctx, "Get Midjourney Task request could not be created")
				continue
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("mj-api-secret", credential)
			proxyURL := channel.GetSetting().Proxy
			httpClient, err := service.GetHttpClientWithProxy(proxyURL)
			if err != nil || httpClient == nil {
				cancel()
				logger.LogError(ctx, "Get Midjourney Task HTTP client is unavailable")
				continue
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				cancel()
				logger.LogError(ctx, "Get Midjourney Task request failed")
				continue
			}
			responseBody, readErr := readMidjourneyPollingResponse(resp.Body, maxMidjourneyPollingResponseBytes)
			resp.Body.Close()
			cancel()
			if readErr != nil {
				logger.LogError(ctx, fmt.Sprintf("Get Midjourney Task response error: %v", readErr))
				continue
			}
			if resp.StatusCode != http.StatusOK {
				logger.LogError(ctx, fmt.Sprintf(
					"Get Midjourney Task status=%d body_bytes=%d",
					resp.StatusCode, len(responseBody),
				))
				continue
			}

			var responseItems []dto.MidjourneyDto
			if err := common.Unmarshal(responseBody, &responseItems); err != nil {
				logger.LogError(ctx, fmt.Sprintf(
					"Get Midjourney Task decode error: %v body_bytes=%d",
					err, len(responseBody),
				))
				continue
			}
			for _, responseItem := range responseItems {
				key := midjourneyPollingKey{Target: target, UpstreamTaskID: responseItem.MjId}
				matchedTasks := tasksByIdentity[key]
				if len(matchedTasks) == 0 {
					logger.LogWarn(ctx, fmt.Sprintf(
						"Midjourney task response ignored: channel=%d credential=%d unknown_upstream_task_id_bytes=%d",
						target.ChannelID, target.CredentialID, len(responseItem.MjId),
					))
					continue
				}
				for _, task := range matchedTasks {
					applyMidjourneyPollingResult(ctx, task, responseItem)
				}
			}
		}
	}
	if report != nil && ctx.Err() == nil {
		report(len(targets), len(targets))
	}
	summary.BillingOperations += drainMidjourneyBillingOperations(ctx, billingOwner, 100)
	return summary
}

func drainMidjourneyBillingOperations(ctx context.Context, owner string, limit int) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		return 0
	}
	processed := 0
	for processed < limit && ctx.Err() == nil {
		operation, found, err := service.ProcessNextMidjourneyBillingOperation(ctx, owner, time.Minute)
		if err != nil {
			operationKey := ""
			if operation != nil {
				operationKey = operation.OperationKey
			}
			logger.LogWarn(ctx, fmt.Sprintf("process Midjourney billing operation failed: operation=%s error=%s",
				operationKey, common.SanitizeErrorMessage(err.Error())))
			break
		}
		if !found {
			break
		}
		processed++
	}
	return processed
}

func finalizeMidjourneyFailureAndQueueRefund(ctx context.Context, task *model.Midjourney, fromStatus string) (*model.MidjourneyFailureFinalization, error) {
	finalization, err := service.FinalizeMidjourneyFailure(ctx, task, fromStatus)
	if err != nil {
		return nil, err
	}
	if finalization.Operation.ID == 0 || finalization.Operation.Kind != model.TaskBillingOperationKindRefund {
		return finalization, nil
	}
	owner := "midjourney-inline:" + common.GetUUID()
	if _, processErr := service.ProcessMidjourneyBillingOperation(ctx, finalization.Operation.ID, owner, time.Minute); processErr != nil {
		logger.LogWarn(ctx, fmt.Sprintf("deferred Midjourney refund remains durable: operation=%s error=%s",
			finalization.Operation.OperationKey, common.SanitizeErrorMessage(processErr.Error())))
	}
	return finalization, nil
}

func groupMidjourneyPollingTasks(
	tasks []*model.Midjourney,
	now time.Time,
) (
	[]midjourneyPollingTarget,
	map[midjourneyPollingTarget][]*model.Midjourney,
	[]*model.Midjourney,
	[]*model.Midjourney,
) {
	groups := make(map[midjourneyPollingTarget][]*model.Midjourney)
	unrecoverable := make([]*model.Midjourney, 0)
	for _, task := range tasks {
		if task == nil || task.Id <= 0 {
			continue
		}
		if strings.TrimSpace(task.MjId) == "" {
			unrecoverable = append(unrecoverable, task)
			continue
		}
		if task.ChannelGeneration != "" && strings.TrimSpace(task.UpstreamTaskID) == "" {
			if now.UnixMilli()-task.SubmitTime >= midjourneySubmissionRecoveryDelay.Milliseconds() {
				unrecoverable = append(unrecoverable, task)
			}
			continue
		}
		target := midjourneyPollingTarget{
			ChannelID: task.ChannelId, CredentialID: task.RoutingCredentialID,
			ChannelGeneration: task.ChannelGeneration,
		}
		if target.ChannelID <= 0 || strings.TrimSpace(task.GetUpstreamTaskID()) == "" {
			unrecoverable = append(unrecoverable, task)
			continue
		}
		groups[target] = append(groups[target], task)
	}
	targets := make([]midjourneyPollingTarget, 0, len(groups))
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
		return targets[i].ChannelGeneration < targets[j].ChannelGeneration
	})

	filteredTargets := make([]midjourneyPollingTarget, 0, len(targets))
	ambiguous := make([]*model.Midjourney, 0)
	for _, target := range targets {
		targetTasks := groups[target]
		identityCounts := make(map[string]int, len(targetTasks))
		for _, task := range targetTasks {
			identityCounts[task.GetUpstreamTaskID()]++
		}
		filtered := make([]*model.Midjourney, 0, len(targetTasks))
		for _, task := range targetTasks {
			if identityCounts[task.GetUpstreamTaskID()] > 1 {
				ambiguous = append(ambiguous, task)
				continue
			}
			filtered = append(filtered, task)
		}
		if len(filtered) == 0 {
			delete(groups, target)
			continue
		}
		groups[target] = filtered
		filteredTargets = append(filteredTargets, target)
	}
	return filteredTargets, groups, unrecoverable, ambiguous
}

func resolveMidjourneyPollingCredential(
	ctx context.Context,
	channel *model.Channel,
	credentialID int,
) (string, error) {
	if channel == nil {
		return "", channelrouting.ErrPersistedCredentialUnavailable
	}
	if credentialID > 0 {
		key, _, err := channelrouting.ResolvePersistedCredentialKey(ctx, channel, credentialID)
		return key, err
	}
	keys := channel.GetKeys()
	if channel.ChannelInfo.IsMultiKey || len(keys) != 1 || strings.TrimSpace(keys[0]) == "" {
		return "", channelrouting.ErrPersistedCredentialUnavailable
	}
	return keys[0], nil
}

func readMidjourneyPollingResponse(body io.Reader, maxBytes int64) ([]byte, error) {
	if body == nil || maxBytes <= 0 {
		return nil, errors.New("Midjourney polling response reader is invalid")
	}
	responseBody, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(responseBody)) > maxBytes {
		return nil, errMidjourneyPollingResponseTooLarge
	}
	return responseBody, nil
}

func failMidjourneyPollingTasks(ctx context.Context, tasks []*model.Midjourney, reason string) {
	for _, task := range tasks {
		if task == nil || task.Id <= 0 || task.Progress == "100%" {
			continue
		}
		previousStatus := task.Status
		task.FailReason = reason
		task.Status = "FAILURE"
		task.Progress = "100%"
		_, err := finalizeMidjourneyFailureAndQueueRefund(ctx, task, previousStatus)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("fail Midjourney task %d: %v", task.Id, err))
		}
	}
}

func applyMidjourneyPollingResult(ctx context.Context, task *model.Midjourney, response dto.MidjourneyDto) {
	if task == nil {
		return
	}
	status := strings.ToUpper(strings.TrimSpace(response.Status))
	if response.Progress == "100%" && status != "SUCCESS" && status != "FAILURE" {
		response.FailReason = "invalid upstream terminal task status"
		response.Status = "FAILURE"
		status = "FAILURE"
	}
	if status == "SUCCESS" || status == "FAILURE" {
		response.Status = status
		response.Progress = "100%"
	}
	if !checkMjTaskNeedUpdate(task, response) {
		return
	}
	previousStatus := task.Status
	task.Code = 1
	if response.Progress != "" {
		task.Progress = response.Progress
	}
	if response.PromptEn != "" {
		task.PromptEn = response.PromptEn
	}
	if response.State != "" {
		task.State = response.State
	}
	if response.SubmitTime > 0 {
		task.SubmitTime = response.SubmitTime
	}
	if response.StartTime > 0 {
		task.StartTime = response.StartTime
	}
	if response.FinishTime > 0 {
		task.FinishTime = response.FinishTime
	}
	if response.ImageUrl != "" {
		task.ImageUrl = response.ImageUrl
	}
	if response.Status != "" {
		task.Status = response.Status
	}
	if response.FailReason != "" {
		task.FailReason = common.SanitizeErrorMessage(response.FailReason)
	}
	if response.Properties != nil {
		if value, err := common.Marshal(response.Properties); err == nil {
			task.Properties = string(value)
		}
	}
	if response.Buttons != nil {
		if value, err := common.Marshal(response.Buttons); err == nil {
			task.Buttons = string(value)
		}
	}
	if response.VideoUrl != "" {
		task.VideoUrl = response.VideoUrl
	}
	if len(response.VideoUrls) > 0 {
		if value, err := common.Marshal(response.VideoUrls); err == nil {
			task.VideoUrls = string(value)
		}
	}

	shouldRefund := false
	if (task.Progress != "100%" && task.FailReason != "") ||
		(task.Progress == "100%" && task.Status == "FAILURE") {
		task.Progress = "100%"
		task.Status = "FAILURE"
		shouldRefund = true
	}
	if shouldRefund {
		if _, err := finalizeMidjourneyFailureAndQueueRefund(ctx, task, previousStatus); err != nil {
			logger.LogError(ctx, "UpdateMidjourneyTask error: "+err.Error())
		}
		return
	}
	if task.Progress == "100%" && task.Status == "SUCCESS" {
		if _, err := service.FinalizeMidjourneySuccess(ctx, task, previousStatus); err != nil {
			logger.LogError(ctx, "FinalizeMidjourneySuccess error: "+err.Error())
		}
		return
	}
	if _, err := task.UpdateWithStatus(previousStatus); err != nil {
		logger.LogError(ctx, "UpdateMidjourneyTask error: "+err.Error())
	}
}
func checkMjTaskNeedUpdate(oldTask *model.Midjourney, newTask dto.MidjourneyDto) bool {
	if oldTask == nil {
		return false
	}
	if oldTask.Code != 1 {
		return true
	}
	if newTask.Progress != "" && oldTask.Progress != newTask.Progress {
		return true
	}
	if newTask.PromptEn != "" && oldTask.PromptEn != newTask.PromptEn {
		return true
	}
	if newTask.State != "" && oldTask.State != newTask.State {
		return true
	}
	if newTask.SubmitTime > 0 && oldTask.SubmitTime != newTask.SubmitTime {
		return true
	}
	if newTask.StartTime > 0 && oldTask.StartTime != newTask.StartTime {
		return true
	}
	if newTask.FinishTime > 0 && oldTask.FinishTime != newTask.FinishTime {
		return true
	}
	if newTask.ImageUrl != "" && oldTask.ImageUrl != newTask.ImageUrl {
		return true
	}
	if newTask.Status != "" && oldTask.Status != newTask.Status {
		return true
	}
	if newTask.FailReason != "" && oldTask.FailReason != newTask.FailReason {
		return true
	}
	if oldTask.Progress != "100%" && newTask.FailReason != "" {
		return true
	}
	if newTask.VideoUrl != "" && oldTask.VideoUrl != newTask.VideoUrl {
		return true
	}
	if len(newTask.VideoUrls) > 0 {
		newVideoUrlsStr, _ := common.Marshal(newTask.VideoUrls)
		if oldTask.VideoUrls != string(newVideoUrlsStr) {
			return true
		}
	}

	return false
}

func GetAllMidjourney(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)

	// 解析其他查询参数
	queryParams := model.TaskQueryParams{
		ChannelID:      c.Query("channel_id"),
		MjID:           c.Query("mj_id"),
		StartTimestamp: c.Query("start_timestamp"),
		EndTimestamp:   c.Query("end_timestamp"),
	}

	items := model.GetAllTasks(pageInfo.GetStartIdx(), pageInfo.GetPageSize(), queryParams)
	total := model.CountAllTasks(queryParams)

	for i, midjourney := range items {
		useLocalMidjourneyMediaURLs(midjourney)
		items[i] = midjourney
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func GetUserMidjourney(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)

	userId := c.GetInt("id")

	queryParams := model.TaskQueryParams{
		MjID:           c.Query("mj_id"),
		StartTimestamp: c.Query("start_timestamp"),
		EndTimestamp:   c.Query("end_timestamp"),
	}

	items := model.GetAllUserTask(userId, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), queryParams)
	total := model.CountAllUserTask(userId, queryParams)

	for i, midjourney := range items {
		useLocalMidjourneyMediaURLs(midjourney)
		items[i] = midjourney
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func useLocalMidjourneyMediaURLs(task *model.Midjourney) {
	if task == nil {
		return
	}
	task.Quota = task.EffectiveBillingQuota()
	baseURL := strings.TrimRight(system_setting.ServerAddress, "/")
	taskID := url.PathEscape(strings.TrimSpace(task.MjId))
	if task.ImageUrl != "" && setting.MjForwardUrlEnabled {
		task.ImageUrl = baseURL + "/mj/image/" + taskID
	} else {
		task.ImageUrl = ""
	}
	if task.VideoUrl != "" {
		task.VideoUrl = baseURL + "/mj/video/" + taskID
	}
	if task.VideoUrls == "" {
		return
	}
	var videoURLs []dto.ImgUrls
	if err := common.Unmarshal([]byte(task.VideoUrls), &videoURLs); err != nil {
		task.VideoUrls = ""
		return
	}
	for index := range videoURLs {
		if strings.TrimSpace(videoURLs[index].Url) != "" {
			videoURLs[index].Url = baseURL + "/mj/video/" + taskID + "/" + strconv.Itoa(index)
		}
	}
	encoded, err := common.Marshal(videoURLs)
	if err != nil {
		task.VideoUrls = ""
		return
	}
	task.VideoUrls = string(encoded)
}
