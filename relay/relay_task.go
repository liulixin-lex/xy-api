package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const (
	maxTaskFetchResponseBytes     int64 = 2 << 20
	asyncBillingTaskSubmitTimeout       = 5 * time.Minute
	maxTaskContinuationIDBytes          = 4 << 10
)

type asyncSubmissionOutcome int

const (
	asyncSubmissionOutcomeAccepted asyncSubmissionOutcome = iota
	asyncSubmissionOutcomeRejected
	asyncSubmissionOutcomeAmbiguous
)

func classifyAsyncSubmissionHTTPStatus(statusCode int) asyncSubmissionOutcome {
	if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		return asyncSubmissionOutcomeAccepted
	}
	switch statusCode {
	case http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusNotAcceptable,
		http.StatusProxyAuthRequired,
		http.StatusGone,
		http.StatusLengthRequired,
		http.StatusPreconditionFailed,
		http.StatusRequestEntityTooLarge,
		http.StatusRequestURITooLong,
		http.StatusUnsupportedMediaType,
		http.StatusRequestedRangeNotSatisfiable,
		http.StatusExpectationFailed,
		http.StatusMisdirectedRequest,
		http.StatusUnprocessableEntity,
		http.StatusUpgradeRequired,
		http.StatusPreconditionRequired,
		http.StatusTooManyRequests,
		http.StatusRequestHeaderFieldsTooLarge,
		http.StatusUnavailableForLegalReasons:
		return asyncSubmissionOutcomeRejected
	}
	return asyncSubmissionOutcomeAmbiguous
}

var (
	errTaskCredentialIdentityMissing = errors.New("origin task credential identity is missing")
	errTaskCredentialUnavailable     = errors.New("origin task credential is unavailable")
	errTaskFetchResponseTooLarge     = errors.New("task fetch response is too large")
)

func readTaskFetchResponse(body io.Reader, maxBytes int64) ([]byte, error) {
	if body == nil || maxBytes <= 0 {
		return nil, errors.New("task fetch response reader is invalid")
	}
	responseBody, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(responseBody)) > maxBytes {
		return nil, fmt.Errorf("%w: limit=%d", errTaskFetchResponseTooLarge, maxBytes)
	}
	return responseBody, nil
}

type TaskSubmitResult struct {
	UpstreamTaskID   string
	TaskData         []byte
	Platform         constant.TaskPlatform
	Quota            int
	IdempotentReplay bool
	response         *taskSubmissionResponseWriter
	//PerCallPrice   types.PriceData
}

func (result *TaskSubmitResult) CommitResponse(c *gin.Context) error {
	if result == nil || result.response == nil || c == nil {
		return errors.New("task submission response is unavailable")
	}
	return result.response.CommitTo(c.Writer)
}

type PreparedTaskAttempt struct {
	adaptor     channel.TaskAdaptor
	platform    constant.TaskPlatform
	requestBody io.Reader
	replay      *model.AsyncBillingReplayResponse
}

func (prepared *PreparedTaskAttempt) IdempotentReplayResult(c *gin.Context) (*TaskSubmitResult, error) {
	if prepared == nil || prepared.replay == nil {
		return nil, nil
	}
	if c == nil {
		return nil, model.ErrAsyncBillingReplayUnavailable
	}
	headers, err := parseAsyncBillingReplayHeaders(prepared.replay.HeadersJSON)
	if err != nil {
		return nil, err
	}
	writer := newTaskSubmissionResponseWriter(c.Writer, maxTaskSubmissionResponseBytes)
	for key, values := range headers {
		writer.header[key] = append([]string(nil), values...)
	}
	writer.header.Set("Content-Type", prepared.replay.ContentType)
	writer.WriteHeader(prepared.replay.StatusCode)
	if _, err := writer.Write(prepared.replay.Body); err != nil {
		return nil, err
	}
	return &TaskSubmitResult{Platform: prepared.platform, response: writer, IdempotentReplay: true}, nil
}

// ResolveOriginTask 处理基于已有任务的提交（remix / continuation）：
// 查找原始任务、从中提取模型名称、锁定原始渠道及稳定 Credential，
// 以及提取 OtherRatios（时长、分辨率）。
// 该函数在控制器的重试循环之前调用一次，其结果通过 info 字段和上下文持久化。
func ResolveOriginTask(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	// 检测 remix action
	path := c.Request.URL.Path
	if strings.Contains(path, "/v1/videos/") && strings.HasSuffix(path, "/remix") {
		info.Action = constant.TaskActionRemix
	}

	// 提取 remix 任务的 video_id
	if info.Action == constant.TaskActionRemix {
		videoID := c.Param("video_id")
		if strings.TrimSpace(videoID) == "" {
			return service.TaskErrorWrapperLocal(fmt.Errorf("video_id is required"), "invalid_request", http.StatusBadRequest)
		}
		info.OriginTaskID = strings.TrimSpace(videoID)
	}

	if info.OriginTaskID == "" {
		return nil
	}

	// 查找原始任务
	originTask, exist, err := model.GetByTaskId(info.UserId, info.OriginTaskID)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "get_origin_task_failed", http.StatusInternalServerError)
	}
	if !exist {
		return service.TaskErrorWrapperLocal(errors.New("task_origin_not_exist"), "task_not_exist", http.StatusBadRequest)
	}
	upstreamTaskID := strings.TrimSpace(originTask.GetUpstreamTaskID())
	if upstreamTaskID == "" || len(upstreamTaskID) > maxTaskContinuationIDBytes ||
		!utf8.ValidString(upstreamTaskID) || strings.ContainsAny(upstreamTaskID, "\r\n\x00") {
		return service.TaskErrorWrapperLocal(
			errors.New("origin task upstream identity is invalid"),
			"task_upstream_identity_invalid",
			http.StatusConflict,
		)
	}
	info.OriginUpstreamTaskID = upstreamTaskID

	// 从原始任务推导模型名称
	if info.OriginModelName == "" {
		info.OriginModelName = originTask.GetOriginModelName()
	}

	// 锁定到原始任务的渠道和 Credential。
	ch, err := model.GetChannelById(originTask.ChannelId, true)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "channel_not_found", http.StatusBadRequest)
	}
	if ch.Status != common.ChannelStatusEnabled {
		return service.TaskErrorWrapperLocal(errors.New("the channel of the origin task is disabled"), "task_channel_disable", http.StatusBadRequest)
	}
	info.LockedRoutingGroup = strings.TrimSpace(originTask.Group)
	info.LockedRoutingCredentialID = originTask.PrivateData.RoutingCredentialID
	if !service.StatefulRoutingGroupAllowed(info.TokenGroup, info.UserGroup, info.LockedRoutingGroup) {
		return service.TaskErrorWrapperLocal(
			errors.New("origin task routing group is not available to the current token"),
			"task_routing_group_forbidden",
			http.StatusForbidden,
		)
	}
	credential, taskErr := lockOriginTaskCredential(c, info, ch, originTask.PrivateData)
	if taskErr != nil {
		return taskErr
	}
	if credential == "" {
		info.LockedChannel = ch
	} else {
		info.LockedChannel = pinTaskChannelCredential(ch, credential)
	}

	// 提取 remix 参数（时长、分辨率 → OtherRatios）
	if info.Action == constant.TaskActionRemix {
		if originTask.PrivateData.BillingContext != nil {
			// 新的 remix 逻辑：直接从原始任务的 BillingContext 中提取 OtherRatios（如果存在）
			for s, f := range originTask.PrivateData.BillingContext.OtherRatios {
				info.PriceData.AddOtherRatio(s, f)
			}
		} else {
			// 旧的 remix 逻辑：直接从 task data 解析 seconds 和 size（如果存在）
			var taskData map[string]interface{}
			_ = common.Unmarshal(originTask.Data, &taskData)
			secondsStr, _ := taskData["seconds"].(string)
			seconds, _ := strconv.Atoi(secondsStr)
			if seconds <= 0 {
				seconds = 4
			}
			// 历史任务数据可能包含未经校验的时长，作为计费乘数前必须钳制
			if seconds > relaycommon.MaxTaskDurationSeconds {
				seconds = relaycommon.MaxTaskDurationSeconds
			}
			sizeStr, _ := taskData["size"].(string)
			info.PriceData.AddOtherRatio("seconds", float64(seconds))
			info.PriceData.AddOtherRatio("size", 1)
			if sizeStr == "1792x1024" || sizeStr == "1024x1792" {
				info.PriceData.AddOtherRatio("size", 1.666667)
			}
		}
	}

	return nil
}

type taskCredentialKeyResolver func(*model.Channel, int) (string, int, bool)
type taskPersistedCredentialResolver func(context.Context, *model.Channel, int) (string, int, error)
type taskRoutingIdentityResolver func(group string, channelID int, credential string) (channelrouting.Identity, bool)

func pinTaskChannelCredential(channel *model.Channel, credential string) *model.Channel {
	if channel == nil || credential == "" {
		return channel
	}
	lockedChannel := *channel
	lockedChannel.Key = credential
	lockedChannel.Keys = nil
	if channel.ChannelInfo.IsMultiKey || len(channel.GetKeys()) > 1 {
		lockedChannel.ChannelInfo.IsMultiKey = true
		lockedChannel.ChannelInfo.MultiKeySize = 1
		lockedChannel.ChannelInfo.MultiKeyStatusList = map[int]int{0: common.ChannelStatusEnabled}
		lockedChannel.ChannelInfo.MultiKeyDisabledReason = nil
		lockedChannel.ChannelInfo.MultiKeyDisabledTime = nil
		lockedChannel.ChannelInfo.MultiKeyPollingIndex = 0
		lockedChannel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeRandom
	}
	return &lockedChannel
}

func lockOriginTaskCredential(c *gin.Context, info *relaycommon.RelayInfo, channel *model.Channel, privateData model.TaskPrivateData) (string, *dto.TaskError) {
	if info == nil {
		return "", service.TaskErrorWrapperLocal(errors.New("origin task relay info is missing"), "invalid_task_context", http.StatusInternalServerError)
	}
	if channel == nil {
		return "", service.TaskErrorWrapperLocal(errors.New("origin task channel is missing"), "channel_not_found", http.StatusBadRequest)
	}
	credentialID := privateData.RoutingCredentialID
	if credentialID <= 0 {
		if credential, ok := privateData.HistoricalPlaintextCredential(); ok {
			return credential, nil
		}
		if channel.ChannelInfo.IsMultiKey || len(channel.GetKeys()) > 1 {
			return "", service.TaskErrorWrapperLocal(errTaskCredentialIdentityMissing, "task_credential_identity_missing", http.StatusConflict)
		}
		return "", nil
	}
	requestContext := context.Background()
	if c != nil && c.Request != nil {
		requestContext = c.Request.Context()
	}
	credential, _, err := channelrouting.ResolvePersistedCredentialKey(requestContext, channel, credentialID)
	if err != nil {
		return "", service.TaskErrorWrapperLocal(errTaskCredentialUnavailable, "task_credential_unavailable", http.StatusServiceUnavailable)
	}
	group := ""
	if info.TaskRelayInfo != nil {
		group = info.LockedRoutingGroup
	}
	if group == "" {
		group = info.UsingGroup
	}
	if group == "" {
		group = common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	}
	if group == "auto" {
		if selectedGroup := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); selectedGroup != "" {
			group = selectedGroup
		}
	}
	identity, resolved := channelrouting.ResolveIdentity(group, channel.Id, credential)
	if resolved && identity.CredentialID != credentialID {
		return "", service.TaskErrorWrapperLocal(errTaskCredentialUnavailable, "task_credential_unavailable", http.StatusServiceUnavailable)
	}
	if stage, active := channelrouting.CurrentPoolDeploymentStage(group); active &&
		stage == model.RoutingDeploymentStageActive && !resolved {
		return "", service.TaskErrorWrapperLocal(errTaskCredentialUnavailable, "task_credential_unavailable", http.StatusServiceUnavailable)
	}
	if resolved {
		if accountID, known := channelrouting.ResolveUpstreamAccountID(group, channel.Id, info.OriginModelName); known {
			identity.UpstreamAccountID = accountID
		}
		service.SetSelectedRoutingIdentity(c, service.SelectedRoutingIdentity{
			ChannelID: channel.Id, SnapshotRevision: identity.SnapshotRevision,
			PoolID: identity.PoolID, MemberID: identity.MemberID,
			CredentialID: identity.CredentialID, UpstreamAccountID: identity.UpstreamAccountID,
		})
		if _, planned := service.GetSelectedRoutingIdentity(c, channel.Id); !planned {
			return "", service.TaskErrorWrapperLocal(errTaskCredentialUnavailable, "task_credential_unavailable", http.StatusServiceUnavailable)
		}
	}
	return credential, nil
}

func lockOriginTaskCredentialWithResolvers(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	channel *model.Channel,
	credentialID int,
	resolveCredential taskCredentialKeyResolver,
	resolveIdentity taskRoutingIdentityResolver,
) (string, *dto.TaskError) {
	if info == nil {
		return "", service.TaskErrorWrapperLocal(errors.New("origin task relay info is missing"), "invalid_task_context", http.StatusInternalServerError)
	}
	if channel == nil {
		return "", service.TaskErrorWrapperLocal(errors.New("origin task channel is missing"), "channel_not_found", http.StatusBadRequest)
	}
	if credentialID <= 0 {
		if channel.ChannelInfo.IsMultiKey || len(channel.GetKeys()) > 1 {
			return "", service.TaskErrorWrapperLocal(errTaskCredentialIdentityMissing, "task_credential_identity_missing", http.StatusConflict)
		}
		return "", nil
	}

	credential, _, resolved := resolveCredential(channel, credentialID)
	if !resolved {
		return "", service.TaskErrorWrapperLocal(errTaskCredentialUnavailable, "task_credential_unavailable", http.StatusServiceUnavailable)
	}
	group := ""
	if info.TaskRelayInfo != nil {
		group = info.LockedRoutingGroup
	}
	if group == "" {
		group = info.UsingGroup
	}
	if group == "" {
		group = common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	}
	if group == "auto" {
		if selectedGroup := common.GetContextKeyString(c, constant.ContextKeyAutoGroup); selectedGroup != "" {
			group = selectedGroup
		}
	}
	identity, resolved := resolveIdentity(group, channel.Id, credential)
	if !resolved || identity.CredentialID != credentialID {
		return "", service.TaskErrorWrapperLocal(errTaskCredentialUnavailable, "task_credential_unavailable", http.StatusServiceUnavailable)
	}
	if accountID, known := channelrouting.ResolveUpstreamAccountID(group, channel.Id, info.OriginModelName); known {
		identity.UpstreamAccountID = accountID
	}
	service.SetSelectedRoutingIdentity(c, service.SelectedRoutingIdentity{
		ChannelID:         channel.Id,
		SnapshotRevision:  identity.SnapshotRevision,
		PoolID:            identity.PoolID,
		MemberID:          identity.MemberID,
		CredentialID:      identity.CredentialID,
		UpstreamAccountID: identity.UpstreamAccountID,
	})
	if _, planned := service.GetSelectedRoutingIdentity(c, channel.Id); !planned {
		return "", service.TaskErrorWrapperLocal(errTaskCredentialUnavailable, "task_credential_unavailable", http.StatusServiceUnavailable)
	}
	return credential, nil
}

// RelayTaskSubmit 完成 task 提交的全部流程（每次尝试调用一次）：
// 刷新渠道元数据 → 确定 platform/adaptor → 验证请求 →
// 估算计费(EstimateBilling) → 计算价格 → 预扣费（仅首次）→
// 构建/发送/解析上游请求 → 提交后计费调整(AdjustBillingOnSubmit)。
// 控制器负责 defer Refund 和成功后 Settle。
func ValidateTaskRequestForRouting(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	platform := constant.TaskPlatform(c.GetString("platform"))
	if platform == constant.TaskPlatformSuno {
		adaptor := GetTaskAdaptor(platform)
		if adaptor == nil {
			return service.TaskErrorWrapperLocal(fmt.Errorf("invalid api platform: %s", platform), "invalid_api_platform", http.StatusBadRequest)
		}
		adaptor.Init(info)
		if taskErr := adaptor.ValidateRequestAndSetAction(c, info); taskErr != nil {
			taskErr.LocalError = true
			return taskErr
		}
		if info.OriginModelName == "" {
			info.OriginModelName = service.CoverTaskActionToModelName(platform, info.Action)
		}
		return nil
	}
	return relaycommon.ValidateTaskRequestForRouting(c, info)
}

func PrepareTaskAttempt(c *gin.Context, info *relaycommon.RelayInfo) (*PreparedTaskAttempt, *dto.TaskError) {
	info.InitChannelMeta(c)

	// 1. 确定 platform → 创建适配器 → 验证请求
	platform := constant.TaskPlatform(c.GetString("platform"))
	if platform == "" {
		platform = GetTaskPlatform(c)
	}
	adaptor := GetTaskAdaptor(platform)
	if adaptor == nil {
		return nil, service.TaskErrorWrapperLocal(fmt.Errorf("invalid api platform: %s", platform), "invalid_api_platform", http.StatusBadRequest)
	}
	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(c, info); taskErr != nil {
		taskErr.LocalError = true
		return nil, taskErr
	}

	// 2. 确定模型名称
	modelName := info.OriginModelName
	if modelName == "" {
		modelName = service.CoverTaskActionToModelName(platform, info.Action)
	}

	// 2.5 应用渠道的模型映射（与同步任务对齐）
	info.OriginModelName = modelName
	info.UpstreamModelName = modelName
	if err := helper.ModelMappedHelper(c, info, nil); err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_mapping_failed", http.StatusBadRequest)
	}
	if err := bindTaskAsyncBillingClientIdentity(c, info); err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "invalid_idempotency_key", http.StatusBadRequest)
	}

	// 3. 预生成公开 task ID（仅首次）
	if info.PublicTaskID == "" {
		info.PublicTaskID = model.GenerateTaskID()
	}

	// 4. 价格计算：基础模型价格
	info.OriginModelName = modelName
	preservedRatios := info.PriceData.OtherRatios()
	priceData, err := helper.ModelPriceHelperPerCall(c, info)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, string(types.ErrorCodeModelPriceError), http.StatusBadRequest)
	}
	info.PriceData = priceData
	for k, v := range preservedRatios {
		info.PriceData.AddOtherRatio(k, v)
	}

	// 5. 计费估算：让适配器根据用户请求提供 OtherRatios（时长、分辨率等）
	//    必须在 ModelPriceHelperPerCall 之后调用（它会重建 PriceData）。
	//    ResolveOriginTask 可能已在 remix 路径中预设了 OtherRatios，此处合并。
	if estimatedRatios := adaptor.EstimateBilling(c, info); len(estimatedRatios) > 0 {
		for k, v := range estimatedRatios {
			info.PriceData.AddOtherRatio(k, v)
		}
	}

	// 6. 将 OtherRatios 应用到基础额度（饱和转换，防止溢出成负数）
	if !common.StringsContains(constant.TaskPricePatches, modelName) {
		quotaWithRatios := info.PriceData.ApplyOtherRatiosToFloat(float64(info.PriceData.Quota))
		quota, clamp := common.QuotaFromFloatChecked(quotaWithRatios)
		info.PriceData.Quota = quota
		noteTaskQuotaClamp(info, clamp)
	}

	// 7. Fleet-gated durable reservation. A mixed-version fleet stays on the v1
	// BillingSession path until every live poller advertises protocol v2.
	if !info.AsyncBillingV2Decided {
		info.AsyncBillingV2Decided = true
		info.AsyncBillingV2Enabled = service.AsyncBillingV2FleetReady(c.Request.Context())
	}
	if info.AsyncBillingV2Enabled {
		if info.AsyncBillingReservationID == 0 {
			reservation, created, reserveErr := model.CreateAsyncBillingReservation(c.Request.Context(), model.AsyncBillingReservationSpec{
				ReservationKey:    asyncBillingReservationKey(info, model.AsyncBillingKindTask),
				ProtocolVersion:   model.TaskBillingProtocolVersion,
				Kind:              model.AsyncBillingKindTask,
				PublicTaskID:      info.PublicTaskID,
				UserID:            info.UserId,
				TokenID:           info.TokenId,
				IsPlayground:      info.IsPlayground,
				BillingPreference: info.UserSetting.BillingPreference,
				Quota:             info.PriceData.Quota,
				ClientKeyHash:     info.AsyncBillingClientKeyHash,
				ClientPayloadHash: info.AsyncBillingClientPayloadHash,
				ClientScope:       info.AsyncBillingClientScope,
			}, time.Now())
			if reserveErr != nil {
				statusCode := http.StatusInternalServerError
				if errors.Is(reserveErr, model.ErrAsyncBillingInsufficientQuota) ||
					errors.Is(reserveErr, model.ErrAsyncBillingSubscriptionExhausted) ||
					errors.Is(reserveErr, model.ErrAsyncBillingNoActiveSubscription) {
					statusCode = http.StatusForbidden
				}
				return nil, service.TaskErrorWrapperLocal(reserveErr, "pre_consume_task_quota_failed", statusCode)
			}
			info.AsyncBillingReservationID = reservation.ID
			info.PublicTaskID = reservation.PublicTaskID
			info.FinalPreConsumedQuota = reservation.CurrentQuota
			info.BillingSource = reservation.FundingSource
			info.SubscriptionId = reservation.SubscriptionID
			info.SubscriptionPreConsumed = int64(reservation.CurrentQuota)
			info.SubscriptionPlanId = reservation.SubscriptionPlanID
			info.SubscriptionPlanTitle = reservation.SubscriptionPlanName
			info.SubscriptionAmountTotal = reservation.SubscriptionTotal
			if info.AsyncBillingClientKeyPresent && !created {
				replay, replayErr := asyncBillingIdempotentReplay(reservation)
				if replayErr != nil {
					return nil, asyncBillingIdempotencyTaskError(replayErr)
				}
				return &PreparedTaskAttempt{platform: platform, replay: replay}, nil
			}
		} else if _, reserveErr := model.ReserveAsyncBillingQuota(
			c.Request.Context(), info.AsyncBillingReservationID, info.PriceData.Quota, time.Now(),
		); reserveErr != nil {
			return nil, service.TaskErrorWrapperLocal(reserveErr, "pre_consume_task_quota_failed", http.StatusForbidden)
		}
	} else {
		if info.Billing == nil && !info.PriceData.FreeModel {
			info.ForcePreConsume = true
			if apiErr := service.PreConsumeBilling(c, info.PriceData.Quota, info); apiErr != nil {
				return nil, service.TaskErrorFromAPIError(apiErr)
			}
		}
		if info.Billing != nil && !info.PriceData.FreeModel {
			if err := info.Billing.Reserve(info.PriceData.Quota); err != nil {
				return nil, service.TaskErrorWrapperLocal(err, "pre_consume_task_quota_failed", http.StatusForbidden)
			}
		}
	}
	if info.AsyncBillingReservationID > 0 {
		taskDraft := model.InitTask(platform, info)
		taskDraft.Action = info.Action
		taskDraft.Quota = info.PriceData.Quota
		taskDraft.PrivateData.BillingProtocolVersion = model.TaskBillingProtocolVersion
		taskDraft.PrivateData.AsyncBillingReservationID = info.AsyncBillingReservationID
		taskDraft.PrivateData.BillingSource = info.BillingSource
		taskDraft.PrivateData.SubscriptionId = info.SubscriptionId
		taskDraft.PrivateData.TokenId = info.TokenId
		taskDraft.PrivateData.NodeName = common.NodeName
		taskDraft.PrivateData.BillingContext = &model.TaskBillingContext{
			ModelPrice:      info.PriceData.ModelPrice,
			GroupRatio:      info.PriceData.GroupRatioInfo.GroupRatio,
			ModelRatio:      info.PriceData.ModelRatio,
			OtherRatios:     info.PriceData.OtherRatios(),
			OriginModelName: info.OriginModelName,
			PerCallBilling:  common.StringsContains(constant.TaskPricePatches, info.OriginModelName) || info.PriceData.UsePrice,
		}
		intent, intentErr := buildAsyncBillingAcceptanceIntent(c, info, taskDraft, nil)
		if intentErr != nil {
			return nil, service.TaskErrorWrapperLocal(intentErr, "freeze_task_acceptance_intent_failed", http.StatusInternalServerError)
		}
		bindAsyncBillingAcceptanceIntent(c, intent)
	}

	// 8. 构建请求体。此时只依赖渠道元数据，不读取或推进渠道凭证。
	requestBody, err := adaptor.BuildRequestBody(c, info)
	if err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "build_request_failed", http.StatusInternalServerError)
	}
	return &PreparedTaskAttempt{adaptor: adaptor, platform: platform, requestBody: requestBody}, nil
}

func RelayPreparedTaskSubmit(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	prepared *PreparedTaskAttempt,
) (*TaskSubmitResult, *dto.TaskError) {
	if c == nil || c.Request == nil || info == nil || prepared == nil || prepared.adaptor == nil || prepared.requestBody == nil {
		return nil, service.TaskErrorWrapperLocal(errors.New("prepared task attempt is invalid"), "invalid_task_attempt", http.StatusInternalServerError)
	}
	if info.AsyncBillingReservationID > 0 {
		timeout := asyncBillingTaskSubmitTimeout
		if common.RelayTimeout > 0 && time.Duration(common.RelayTimeout)*time.Second < timeout {
			timeout = time.Duration(common.RelayTimeout) * time.Second
		}
		submitContext, cancel := context.WithTimeout(c.Request.Context(), timeout)
		originalRequest := c.Request
		c.Request = originalRequest.WithContext(submitContext)
		if deadline, ok := submitContext.Deadline(); ok {
			info.AsyncBillingSendDeadlineMs = deadline.UnixMilli()
		}
		defer func() {
			c.Request = originalRequest
			cancel()
		}()
	}
	info.InitChannelMeta(c)
	if taskErr := validateTaskSubmitCredentialIdentity(info); taskErr != nil {
		return nil, taskErr
	}
	prepared.adaptor.Init(info)

	// 9. 发送请求
	resp, err := prepared.adaptor.DoRequest(c, info, prepared.requestBody)
	if err != nil {
		if sendState := relaycommon.RoutingUpstreamSendStateFromContext(c); info.AsyncBillingReservationID > 0 &&
			sendState != nil && sendState.Sent() {
			manualErr := markTaskReservationManualReview(c, info, "task_transport_outcome_ambiguous: "+err.Error(), "")
			return nil, service.TaskErrorWrapperLocal(errors.Join(err, manualErr), "task_submit_outcome_ambiguous", http.StatusBadGateway)
		}
		var transportErr *types.NewAPIError
		if !errors.As(err, &transportErr) || transportErr.GetErrorCode() != types.ErrorCodeDoRequestFailed {
			return nil, service.TaskErrorWrapperLocal(err, string(types.ErrorCodeDoRequestFailed), http.StatusInternalServerError)
		}
		return nil, service.TaskErrorWrapper(err, string(types.ErrorCodeDoRequestFailed), http.StatusBadGateway)
	}
	if resp == nil {
		if info.AsyncBillingReservationID > 0 {
			manualErr := markTaskReservationManualReview(c, info, "task_response_missing_after_send", "")
			return nil, service.TaskErrorWrapperLocal(manualErr, "task_submit_outcome_ambiguous", http.StatusBadGateway)
		}
		return nil, service.TaskErrorFromUpstreamResponse(nil, nil, time.Now())
	}
	if submissionOutcome := classifyAsyncSubmissionHTTPStatus(resp.StatusCode); submissionOutcome != asyncSubmissionOutcomeAccepted {
		if info.AsyncBillingReservationID > 0 && submissionOutcome == asyncSubmissionOutcomeAmbiguous {
			responseBody, readErr := service.ReadUpstreamResponseBody(resp.Body, service.DefaultMaxUpstreamResponseBytes)
			_ = resp.Body.Close()
			manualErr := markTaskReservationManualReview(
				c, info, fmt.Sprintf("task_upstream_status_outcome_ambiguous_%d", resp.StatusCode), "",
			)
			statusErr := service.UnparseableUpstreamResponseError(
				fmt.Errorf("upstream task returned ambiguous status %d", resp.StatusCode), responseBody,
			)
			return nil, service.TaskErrorWrapperLocal(
				errors.Join(statusErr, readErr, manualErr), "task_submit_outcome_ambiguous", http.StatusBadGateway,
			)
		}
		if info.AsyncBillingReservationID > 0 {
			persistContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
			rejectErr := model.RejectAsyncBillingAttempt(
				persistContext,
				info.AsyncBillingReservationID,
				info.RetryIndex,
				fmt.Sprintf("upstream_status_%d", resp.StatusCode),
				time.Now(),
			)
			cancel()
			if rejectErr != nil {
				_ = resp.Body.Close()
				return nil, service.TaskErrorWrapperLocal(
					rejectErr, "persist_task_rejection_failed", http.StatusInternalServerError,
				)
			}
		}
		responseBody, readErr := service.ReadUpstreamResponseBody(resp.Body, service.DefaultMaxUpstreamResponseBytes)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, service.TaskErrorWrapper(readErr, "read_response_body_failed", http.StatusBadGateway)
		}
		return nil, service.TaskErrorFromUpstreamResponse(
			resp,
			service.UnparseableUpstreamResponseError(
				fmt.Errorf("upstream task returned status %d", resp.StatusCode),
				responseBody,
			),
			time.Now(),
		)
	}

	// 10. 返回 OtherRatios 给下游（header 必须在 DoResponse 写 body 之前设置）
	otherRatios := info.PriceData.OtherRatios()
	if otherRatios == nil {
		otherRatios = map[string]float64{}
	}
	ratiosJSON, _ := common.Marshal(otherRatios)
	c.Header("X-New-Api-Other-Ratios", string(ratiosJSON))

	// 11. Parse into a bounded internal writer. The controller commits this
	// response only after the accepted task and billing state are durable.
	originalWriter := c.Writer
	responseWriter := newTaskSubmissionResponseWriter(originalWriter, maxTaskSubmissionResponseBytes)
	var upstreamTaskID string
	var taskData []byte
	var taskErr *dto.TaskError
	func() {
		c.Writer = responseWriter
		defer func() { c.Writer = originalWriter }()
		upstreamTaskID, taskData, taskErr = prepared.adaptor.DoResponse(c, resp, info)
	}()
	if taskErr != nil {
		if info.AsyncBillingReservationID <= 0 {
			taskErr.LocalError = false
			return nil, taskErr
		}
		if strings.TrimSpace(upstreamTaskID) != "" {
			controlled, controlledErr := controlledTaskSubmissionResponse(c, info, prepared.platform)
			if controlledErr == nil {
				responseWriter = controlled
				taskErr = nil
			} else {
				manualErr := markTaskReservationManualReview(c, info,
					"task_response_normalization_failed: "+controlledErr.Error(), upstreamTaskID)
				return nil, service.TaskErrorWrapperLocal(errors.Join(taskErr.Error, controlledErr, manualErr),
					"task_submit_outcome_ambiguous", http.StatusBadGateway)
			}
		} else {
			manualErr := markTaskReservationManualReview(c, info,
				"task_2xx_response_unparseable: "+taskErr.Message, "")
			return nil, service.TaskErrorWrapperLocal(errors.Join(taskErr.Error, manualErr),
				"task_submit_outcome_ambiguous", http.StatusBadGateway)
		}
	}
	if responseWriter.overflow {
		if strings.TrimSpace(upstreamTaskID) != "" {
			controlled, controlledErr := controlledTaskSubmissionResponse(c, info, prepared.platform)
			if controlledErr == nil {
				responseWriter = controlled
			} else {
				manualErr := markTaskReservationManualReview(c, info,
					"task_response_too_large_and_normalization_failed: "+controlledErr.Error(), upstreamTaskID)
				return nil, service.TaskErrorWrapperLocal(errors.Join(errTaskSubmissionResponseTooLarge, controlledErr, manualErr),
					"task_submit_outcome_ambiguous", http.StatusBadGateway)
			}
		} else {
			manualErr := markTaskReservationManualReview(c, info, "task_2xx_response_too_large", "")
			return nil, service.TaskErrorWrapperLocal(errors.Join(errTaskSubmissionResponseTooLarge, manualErr),
				"task_submit_outcome_ambiguous", http.StatusBadGateway)
		}
	}
	normalizedUpstreamTaskID, identityErr := normalizeAcceptedUpstreamTaskID(upstreamTaskID)
	if identityErr != nil {
		manualErr := markTaskReservationManualReview(c, info,
			"task_2xx_response_invalid_upstream_task_id: "+identityErr.Error(), "")
		return nil, service.TaskErrorWrapperLocal(errors.Join(identityErr, manualErr),
			"task_submit_outcome_ambiguous", http.StatusBadGateway)
	}
	upstreamTaskID = normalizedUpstreamTaskID
	if upstreamTaskID == "" {
		manualErr := markTaskReservationManualReview(c, info, "task_2xx_response_missing_upstream_task_id", "")
		return nil, service.TaskErrorWrapperLocal(errors.Join(errors.New("task_id is empty"), manualErr),
			"task_submit_outcome_ambiguous", http.StatusBadGateway)
	}

	// 11. 提交后计费调整：让适配器根据上游实际返回调整 OtherRatios
	finalQuota := info.PriceData.Quota
	if adjustedRatios := prepared.adaptor.AdjustBillingOnSubmit(info, taskData); len(adjustedRatios) > 0 {
		if adjustedQuota, ok := recalcQuotaFromRatios(info, adjustedRatios); ok {
			// 基于调整后的 ratios 重新计算 quota
			finalQuota = adjustedQuota
			info.PriceData.ReplaceOtherRatios(adjustedRatios)
			info.PriceData.Quota = finalQuota
		}
	}

	return &TaskSubmitResult{
		UpstreamTaskID: upstreamTaskID,
		TaskData:       taskData,
		Platform:       prepared.platform,
		Quota:          finalQuota,
		response:       responseWriter,
	}, nil
}

func normalizeAcceptedUpstreamTaskID(raw string) (string, error) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return "", nil
	}
	if len(normalized) <= 191 && utf8.ValidString(normalized) && !strings.ContainsAny(normalized, "\r\n\x00") {
		return normalized, nil
	}
	digest := sha256.Sum256([]byte(raw))
	return "", fmt.Errorf("invalid upstream task identity length=%d sha256=%s", len(raw), hex.EncodeToString(digest[:]))
}

func controlledTaskSubmissionResponse(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	platform constant.TaskPlatform,
) (*taskSubmissionResponseWriter, error) {
	if c == nil || info == nil || strings.TrimSpace(info.PublicTaskID) == "" {
		return nil, errors.New("public task identity is unavailable")
	}
	var response any
	if platform == constant.TaskPlatformSuno {
		response = dto.TaskResponse[string]{Code: "success", Data: info.PublicTaskID}
	} else {
		video := dto.NewOpenAIVideo()
		video.ID = info.PublicTaskID
		video.TaskID = info.PublicTaskID
		video.Model = info.OriginModelName
		video.CreatedAt = time.Now().Unix()
		response = video
	}
	body, err := common.Marshal(response)
	if err != nil {
		return nil, err
	}
	writer := newTaskSubmissionResponseWriter(c.Writer, maxTaskSubmissionResponseBytes)
	writer.header.Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	if _, err := writer.Write(body); err != nil {
		return nil, err
	}
	return writer, nil
}

func markTaskReservationManualReview(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	reason string,
	upstreamTaskID string,
) error {
	if info == nil || info.AsyncBillingReservationID <= 0 {
		return nil
	}
	persistContext := context.Background()
	if c != nil && c.Request != nil {
		persistContext = context.WithoutCancel(c.Request.Context())
	}
	persistContext, cancel := context.WithTimeout(persistContext, 5*time.Second)
	defer cancel()
	_, err := model.MarkAsyncBillingReservationManualReview(
		persistContext, info.AsyncBillingReservationID, reason, upstreamTaskID, time.Now(),
	)
	if err == nil {
		info.AsyncBillingManualReviewMarked = true
	}
	return err
}

func validateTaskSubmitCredentialIdentity(info *relaycommon.RelayInfo) *dto.TaskError {
	if info == nil || info.ChannelMeta == nil {
		return service.TaskErrorWrapperLocal(errors.New("task channel metadata is missing"), "invalid_task_attempt", http.StatusInternalServerError)
	}
	if info.ChannelIsMultiKey && info.RoutingCredentialID <= 0 {
		return service.TaskErrorWrapperLocal(errTaskCredentialIdentityMissing, "task_credential_identity_missing", http.StatusServiceUnavailable)
	}
	return nil
}

func RelayTaskSubmit(c *gin.Context, info *relaycommon.RelayInfo) (*TaskSubmitResult, *dto.TaskError) {
	prepared, taskErr := PrepareTaskAttempt(c, info)
	if taskErr != nil {
		return nil, taskErr
	}
	return RelayPreparedTaskSubmit(c, info, prepared)
}

// recalcQuotaFromRatios 根据 adjustedRatios 重新计算 quota。
// 公式: baseQuota × ∏(ratio) — 其中 baseQuota 是不含 OtherRatios 的基础额度。
func recalcQuotaFromRatios(info *relaycommon.RelayInfo, ratios map[string]float64) (int, bool) {
	// 从 PriceData 获取不含 OtherRatios 的基础价格
	baseQuota := info.PriceData.RemoveOtherRatiosFromFloat(float64(info.PriceData.Quota))
	priceData := info.PriceData
	if !priceData.ReplaceOtherRatios(ratios) {
		return 0, false
	}
	// 应用新的 ratios
	result := priceData.ApplyOtherRatiosToFloat(baseQuota)
	quota, clamp := common.QuotaFromFloatChecked(result)
	noteTaskQuotaClamp(info, clamp)
	return quota, true
}

// noteTaskQuotaClamp records the first quota saturation event onto the task's
// RelayInfo so LogTaskConsumption can surface it on the submit log's
// admin_info. First non-nil clamp wins.
func noteTaskQuotaClamp(info *relaycommon.RelayInfo, clamp *common.QuotaClamp) {
	if clamp == nil || info == nil {
		return
	}
	if info.QuotaClamp == nil {
		info.QuotaClamp = clamp
	}
}

var fetchRespBuilders = map[int]func(c *gin.Context) (respBody []byte, taskResp *dto.TaskError){
	relayconstant.RelayModeSunoFetchByID:  sunoFetchByIDRespBodyBuilder,
	relayconstant.RelayModeSunoFetch:      sunoFetchRespBodyBuilder,
	relayconstant.RelayModeVideoFetchByID: videoFetchByIDRespBodyBuilder,
}

func authorizeTaskFetchTarget(
	c *gin.Context,
	modelName string,
	channelID int,
	enforceSpecificChannel bool,
) *dto.TaskError {
	authorizationErr := middleware.AuthorizeTokenRoutingTarget(
		c, modelName, channelID, enforceSpecificChannel,
	)
	if authorizationErr == nil {
		return nil
	}
	return service.TaskErrorWrapperLocal(
		authorizationErr.Err,
		string(authorizationErr.GetErrorCode()),
		authorizationErr.StatusCode,
	)
}

func RelayTaskFetch(c *gin.Context, relayMode int) (taskResp *dto.TaskError) {
	respBuilder, ok := fetchRespBuilders[relayMode]
	if !ok {
		taskResp = service.TaskErrorWrapperLocal(errors.New("invalid_relay_mode"), "invalid_relay_mode", http.StatusBadRequest)
		return taskResp
	}

	respBody, taskErr := respBuilder(c)
	if taskErr != nil {
		return taskErr
	}
	if len(respBody) == 0 {
		respBody = []byte("{\"code\":\"success\",\"data\":null}")
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	_, err := io.Copy(c.Writer, bytes.NewBuffer(respBody))
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "copy_response_body_failed", http.StatusInternalServerError)
		return
	}
	return
}

func sunoFetchRespBodyBuilder(c *gin.Context) (respBody []byte, taskResp *dto.TaskError) {
	userId := c.GetInt("id")
	var condition = struct {
		IDs    []any  `json:"ids"`
		Action string `json:"action"`
	}{}
	err := c.BindJSON(&condition)
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "invalid_request", http.StatusBadRequest)
		return
	}
	var tasks []any
	if len(condition.IDs) > 0 {
		taskModels, err := model.GetByTaskIds(userId, condition.IDs)
		if err != nil {
			taskResp = service.TaskErrorWrapper(err, "get_tasks_failed", http.StatusInternalServerError)
			return
		}
		authorizedModels := make(map[string]struct{}, len(taskModels))
		for _, task := range taskModels {
			modelName := task.GetOriginModelName()
			if _, authorized := authorizedModels[modelName]; !authorized {
				if taskResp = authorizeTaskFetchTarget(c, modelName, 0, false); taskResp != nil {
					return
				}
				authorizedModels[modelName] = struct{}{}
			}
			tasks = append(tasks, TaskModel2Dto(task))
		}
	} else {
		tasks = make([]any, 0)
	}
	respBody, err = common.Marshal(dto.TaskResponse[[]any]{
		Code: "success",
		Data: tasks,
	})
	return
}

func sunoFetchByIDRespBodyBuilder(c *gin.Context) (respBody []byte, taskResp *dto.TaskError) {
	taskId := c.Param("id")
	userId := c.GetInt("id")

	originTask, exist, err := model.GetByTaskId(userId, taskId)
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "get_task_failed", http.StatusInternalServerError)
		return
	}
	if !exist {
		taskResp = service.TaskErrorWrapperLocal(errors.New("task_not_exist"), "task_not_exist", http.StatusBadRequest)
		return
	}
	if taskResp = authorizeTaskFetchTarget(c, originTask.GetOriginModelName(), 0, false); taskResp != nil {
		return
	}

	respBody, err = common.Marshal(dto.TaskResponse[any]{
		Code: "success",
		Data: TaskModel2Dto(originTask),
	})
	return
}

func videoFetchByIDRespBodyBuilder(c *gin.Context) (respBody []byte, taskResp *dto.TaskError) {
	taskId := c.Param("task_id")
	if taskId == "" {
		taskId = c.GetString("task_id")
	}
	userId := c.GetInt("id")

	originTask, exist, err := model.GetByTaskId(userId, taskId)
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "get_task_failed", http.StatusInternalServerError)
		return
	}
	if !exist {
		taskResp = service.TaskErrorWrapperLocal(errors.New("task_not_exist"), "task_not_exist", http.StatusBadRequest)
		return
	}
	if taskResp = authorizeTaskFetchTarget(c, originTask.GetOriginModelName(), 0, false); taskResp != nil {
		return
	}
	if originTask.Status != model.TaskStatusSuccess && originTask.Status != model.TaskStatusFailure {
		channelModel, channelErr := model.GetChannelById(originTask.ChannelId, true)
		if channelErr == nil && channelModel != nil &&
			(channelModel.Type == constant.ChannelTypeVertexAi || channelModel.Type == constant.ChannelTypeGemini) {
			if taskResp = authorizeTaskFetchTarget(
				c, originTask.GetOriginModelName(), originTask.ChannelId, true,
			); taskResp != nil {
				return
			}
		}
	}

	isOpenAIVideoAPI := strings.HasPrefix(c.Request.RequestURI, "/v1/videos/")

	// Gemini/Vertex 支持实时查询：用户 fetch 时直接从上游拉取最新状态
	realtimeResp, realtimeErr := tryRealtimeFetch(c.Request.Context(), originTask, isOpenAIVideoAPI)
	if realtimeErr != nil {
		statusCode := http.StatusBadGateway
		code := "fetch_task_failed"
		if errors.Is(realtimeErr, errTaskCredentialIdentityMissing) {
			statusCode = http.StatusConflict
			code = "task_credential_identity_missing"
		} else if errors.Is(realtimeErr, errTaskCredentialUnavailable) {
			statusCode = http.StatusServiceUnavailable
			code = "task_credential_unavailable"
		}
		taskResp = service.TaskErrorWrapperLocal(realtimeErr, code, statusCode)
		return
	}
	if len(realtimeResp) > 0 {
		respBody = realtimeResp
		return
	}

	// OpenAI Video API 格式: 走各 adaptor 的 ConvertToOpenAIVideo
	if isOpenAIVideoAPI {
		adaptor := GetTaskAdaptor(originTask.Platform)
		if adaptor == nil {
			taskResp = service.TaskErrorWrapperLocal(fmt.Errorf("invalid channel id: %d", originTask.ChannelId), "invalid_channel_id", http.StatusBadRequest)
			return
		}
		if converter, ok := adaptor.(channel.OpenAIVideoConverter); ok {
			openAIVideoData, err := converter.ConvertToOpenAIVideo(originTask)
			if err != nil {
				taskResp = service.TaskErrorWrapper(err, "convert_to_openai_video_failed", http.StatusInternalServerError)
				return
			}
			respBody = openAIVideoData
			return
		}
		taskResp = service.TaskErrorWrapperLocal(fmt.Errorf("not_implemented:%s", originTask.Platform), "not_implemented", http.StatusNotImplemented)
		return
	}

	// 通用 TaskDto 格式
	respBody, err = common.Marshal(dto.TaskResponse[any]{
		Code: "success",
		Data: TaskModel2Dto(originTask),
	})
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "marshal_response_failed", http.StatusInternalServerError)
	}
	return
}

// tryRealtimeFetch 尝试从上游实时拉取 Gemini/Vertex 任务状态。
// 仅当渠道类型为 Gemini 或 Vertex 时触发；其他渠道返回 nil。
// 当非 OpenAI Video API 时，还会构建自定义格式的响应体。
func tryRealtimeFetch(ctx context.Context, task *model.Task, isOpenAIVideoAPI bool) ([]byte, error) {
	if task == nil {
		return nil, errors.New("task is nil")
	}
	if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
		if isOpenAIVideoAPI {
			return nil, nil
		}
		return buildRealtimeTaskResponse(task, detectVideoFormat(task.Data))
	}

	channelModel, err := model.GetChannelById(task.ChannelId, true)
	if err != nil {
		return nil, fmt.Errorf("load task channel: %w", err)
	}
	if channelModel.Type != constant.ChannelTypeVertexAi && channelModel.Type != constant.ChannelTypeGemini {
		return nil, nil
	}

	baseURL := constant.ChannelBaseURLs[channelModel.Type]
	if channelModel.GetBaseURL() != "" {
		baseURL = channelModel.GetBaseURL()
	}
	proxy := channelModel.GetSetting().Proxy
	adaptor := GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(channelModel.Type)))
	if adaptor == nil {
		return nil, fmt.Errorf("task adaptor is unavailable for channel type %d", channelModel.Type)
	}
	credential, credentialErr := resolveTaskFetchCredential(ctx, channelModel, task.PrivateData)
	if credentialErr != nil {
		return nil, credentialErr
	}

	resp, err := adaptor.FetchTask(ctx, baseURL, credential, map[string]any{
		"task_id": task.GetUpstreamTaskID(),
		"action":  task.Action,
	}, proxy)
	if err != nil {
		return nil, fmt.Errorf("fetch upstream task: %w", err)
	}
	if resp == nil {
		return nil, errors.New("fetch upstream task returned an empty response")
	}
	defer resp.Body.Close()
	body, err := readTaskFetchResponse(resp.Body, maxTaskFetchResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read upstream task response: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
		return nil, fmt.Errorf("upstream task response status %d", resp.StatusCode)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		reason := fmt.Sprintf("upstream task query returned status %d", resp.StatusCode)
		finalized, finalizeErr := service.FinalizeTaskObservation(ctx, service.TaskFinalizationObservation{
			TaskID: task.ID, TerminalStatus: model.TaskStatusFailure,
			Progress: taskcommon.ProgressComplete, FinishTime: time.Now().Unix(), FailReason: reason,
		})
		if finalizeErr != nil {
			return nil, errors.Join(fmt.Errorf("%s", reason), finalizeErr)
		}
		if finalized == nil || finalized.Task == nil || (!finalized.ManualReview && finalized.Operation == nil) {
			return nil, errors.New("task finalization returned an incomplete result")
		}
		*task = *finalized.Task
		if finalized.Operation != nil {
			processRealtimeTaskBillingOperation(ctx, finalized.Operation)
		}
		if isOpenAIVideoAPI {
			return nil, nil
		}
		return buildRealtimeTaskResponse(task, detectVideoFormat(task.Data))
	}

	ti, err := adaptor.ParseTaskResult(ctx, body)
	if err != nil || ti == nil {
		if err == nil {
			err = errors.New("parsed task result is empty")
		}
		return nil, fmt.Errorf("parse upstream task response: %w", err)
	}

	adjustedQuota := 0
	if model.TaskStatus(ti.Status) == model.TaskStatusSuccess {
		adjustedQuota = adaptor.AdjustBillingOnComplete(task, ti)
	}
	if err := applyRealtimeTaskObservation(ctx, task, ti, body, adjustedQuota); err != nil {
		return nil, err
	}

	// OpenAI Video API 由调用者的 ConvertToOpenAIVideo 分支处理
	if isOpenAIVideoAPI {
		return nil, nil
	}

	// 非 OpenAI Video API: 构建自定义格式响应
	return buildRealtimeTaskResponse(task, detectVideoFormat(task.Data))
}

func applyRealtimeTaskObservation(
	ctx context.Context,
	task *model.Task,
	taskInfo *relaycommon.TaskInfo,
	responseBody []byte,
	adjustedQuota int,
) error {
	if task == nil || taskInfo == nil {
		return errors.New("realtime task observation is incomplete")
	}
	status := model.TaskStatus(taskInfo.Status)
	progress := taskInfo.Progress
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
	upstreamResultURL := strings.TrimSpace(taskInfo.Url)
	if upstreamResultURL == "" {
		upstreamResultURL = strings.TrimSpace(taskInfo.RemoteUrl)
	}
	if strings.HasPrefix(strings.ToLower(upstreamResultURL), "data:") {
		upstreamResultURL = ""
	}
	sanitizedData := service.SanitizeTaskData(responseBody)
	now := time.Now().Unix()

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
		if status == model.TaskStatusSuccess && adjustedQuota > 0 {
			actualQuota = &adjustedQuota
		}
		finalized, err := service.FinalizeTaskObservation(ctx, service.TaskFinalizationObservation{
			TaskID: task.ID, TerminalStatus: status, Progress: progress,
			SubmitTime: task.SubmitTime, StartTime: startTime, FinishTime: finishTime,
			FailReason: taskInfo.Reason, UpstreamResultURL: upstreamResultURL,
			Data: sanitizedData, ActualQuota: actualQuota, TotalTokens: taskInfo.TotalTokens,
		})
		if err != nil {
			return fmt.Errorf("finalize realtime task %s: %w", task.TaskID, err)
		}
		if finalized == nil || finalized.Task == nil || (!finalized.ManualReview && finalized.Operation == nil) {
			return errors.New("task finalization returned an incomplete result")
		}
		*task = *finalized.Task
		if finalized.Operation != nil {
			processRealtimeTaskBillingOperation(ctx, finalized.Operation)
		}
		return nil
	}

	if status != model.TaskStatusSubmitted && status != model.TaskStatusQueued && status != model.TaskStatusInProgress {
		return fmt.Errorf("unknown task status %s for task %s", taskInfo.Status, task.TaskID)
	}
	snapshot := task.Snapshot()
	task.Status = status
	task.Progress = progress
	task.FailReason = common.SanitizeErrorMessage(taskInfo.Reason)
	if status == model.TaskStatusInProgress && task.StartTime == 0 {
		task.StartTime = now
	}
	if upstreamResultURL != "" {
		task.SetUpstreamResultURL(upstreamResultURL)
	}
	task.Data = sanitizedData
	if snapshot.Equal(task.Snapshot()) {
		return nil
	}
	won, err := task.UpdateWithStatus(snapshot.Status)
	if err != nil {
		return fmt.Errorf("update realtime task %s: %w", task.TaskID, err)
	}
	if won {
		return nil
	}
	stored, exists, err := model.GetByTaskId(task.UserId, task.TaskID)
	if err != nil {
		return err
	}
	if !exists || stored == nil {
		return errors.New("task disappeared after concurrent update")
	}
	*task = *stored
	return nil
}

func processRealtimeTaskBillingOperation(ctx context.Context, operation *model.TaskBillingOperation) {
	if operation == nil {
		return
	}
	_, err := service.ProcessTaskBillingOperation(
		ctx,
		operation.ID,
		"task-realtime-billing-"+common.GetUUID(),
		time.Minute,
	)
	if err != nil && !errors.Is(err, model.ErrTaskBillingOperationNotClaimed) {
		logger.LogWarn(ctx, fmt.Sprintf(
			"realtime task billing operation deferred for recovery: operation=%s error=%s",
			operation.OperationKey,
			common.SanitizeErrorMessage(err.Error()),
		))
	}
}

func buildRealtimeTaskResponse(task *model.Task, format string) ([]byte, error) {
	if task == nil {
		return nil, errors.New("task is nil")
	}
	if format == "" {
		format = "mp4"
	}
	out := map[string]any{
		"error":    nil,
		"format":   format,
		"metadata": nil,
		"status":   mapTaskStatusToSimple(task.Status),
		"task_id":  task.TaskID,
		"url":      task.GetResultURL(),
	}
	respBody, err := common.Marshal(dto.TaskResponse[any]{
		Code: "success",
		Data: out,
	})
	return respBody, err
}

func resolveTaskFetchCredential(ctx context.Context, channel *model.Channel, privateData model.TaskPrivateData) (string, error) {
	return resolveTaskFetchCredentialWithResolver(ctx, channel, privateData, channelrouting.ResolvePersistedCredentialKey)
}

func resolveTaskFetchCredentialWithResolver(
	ctx context.Context,
	channel *model.Channel,
	privateData model.TaskPrivateData,
	resolve taskPersistedCredentialResolver,
) (string, error) {
	if channel == nil {
		return "", errTaskCredentialUnavailable
	}
	credentialID := privateData.RoutingCredentialID
	if credentialID > 0 {
		credential, _, err := resolve(ctx, channel, credentialID)
		if err != nil || strings.TrimSpace(credential) == "" {
			return "", errTaskCredentialUnavailable
		}
		return credential, nil
	}
	if credential, ok := privateData.HistoricalPlaintextCredential(); ok {
		return credential, nil
	}
	if channel.ChannelInfo.IsMultiKey || len(channel.GetKeys()) > 1 {
		return "", errTaskCredentialIdentityMissing
	}
	if strings.TrimSpace(channel.Key) == "" {
		return "", errTaskCredentialUnavailable
	}
	return channel.Key, nil
}

// detectVideoFormat 从 Gemini/Vertex 原始响应中探测视频格式
func detectVideoFormat(rawBody []byte) string {
	var raw map[string]any
	if err := common.Unmarshal(rawBody, &raw); err != nil {
		return "mp4"
	}
	respObj, ok := raw["response"].(map[string]any)
	if !ok {
		return "mp4"
	}
	vids, ok := respObj["videos"].([]any)
	if !ok || len(vids) == 0 {
		return "mp4"
	}
	v0, ok := vids[0].(map[string]any)
	if !ok {
		return "mp4"
	}
	mt, ok := v0["mimeType"].(string)
	if !ok || mt == "" || strings.Contains(mt, "mp4") {
		return "mp4"
	}
	return mt
}

// mapTaskStatusToSimple 将内部 TaskStatus 映射为简化状态字符串
func mapTaskStatusToSimple(status model.TaskStatus) string {
	switch status {
	case model.TaskStatusSuccess:
		return "succeeded"
	case model.TaskStatusFailure:
		return "failed"
	case model.TaskStatusQueued, model.TaskStatusSubmitted:
		return "queued"
	default:
		return "processing"
	}
}

func TaskModel2Dto(task *model.Task) *dto.TaskDto {
	return &dto.TaskDto{
		ID:         task.ID,
		CreatedAt:  task.CreatedAt,
		UpdatedAt:  task.UpdatedAt,
		TaskID:     task.TaskID,
		Platform:   string(task.Platform),
		UserId:     task.UserId,
		Group:      task.Group,
		ChannelId:  task.ChannelId,
		Quota:      task.EffectiveBillingQuota(),
		Action:     task.Action,
		Status:     string(task.Status),
		FailReason: common.SanitizeErrorMessage(task.FailReason),
		ResultURL:  task.GetResultURL(),
		SubmitTime: task.SubmitTime,
		StartTime:  task.StartTime,
		FinishTime: task.FinishTime,
		Progress:   task.Progress,
		Properties: task.Properties,
		Username:   task.Username,
		Data:       service.SanitizeTaskData(task.Data),
	}
}
