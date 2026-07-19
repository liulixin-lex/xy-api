package service

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// LogTaskConsumption 记录任务提交日志和统计信息（仅记录，不涉及实际扣费）。
// 资金与令牌额度由 durable BillingReservation 预留，并在任务终态结算或退款。
func LogTaskConsumption(c *gin.Context, info *relaycommon.RelayInfo) {
	tokenName := c.GetString("token_name")
	logContent := fmt.Sprintf("操作 %s", info.Action)
	// 支持任务仅按次计费
	if common.StringsContains(constant.TaskPricePatches, info.OriginModelName) {
		logContent = fmt.Sprintf("%s，按次计费", logContent)
	} else {
		if otherRatios := info.PriceData.OtherRatios(); len(otherRatios) > 0 {
			var contents []string
			for key, ra := range otherRatios {
				if 1.0 != ra {
					contents = append(contents, fmt.Sprintf("%s: %.2f", key, ra))
				}
			}
			if len(contents) > 0 {
				logContent = fmt.Sprintf("%s, 计算参数：%s", logContent, strings.Join(contents, ", "))
			}
		}
	}
	other := make(map[string]interface{})
	other["is_task"] = true
	other["request_path"] = c.Request.URL.Path
	other["model_price"] = info.PriceData.ModelPrice
	if info.PriceData.ModelRatio > 0 {
		other["model_ratio"] = info.PriceData.ModelRatio
	}
	other["group_ratio"] = info.PriceData.GroupRatioInfo.GroupRatio
	if info.PriceData.GroupRatioInfo.HasSpecialRatio {
		other["user_group_ratio"] = info.PriceData.GroupRatioInfo.GroupSpecialRatio
	}
	if info.IsModelMapped {
		other["is_model_mapped"] = true
		other["upstream_model_name"] = info.UpstreamModelName
	}
	attachQuotaSaturation(c, info, other)
	model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{
		ChannelId: info.ChannelId,
		ModelName: info.OriginModelName,
		TokenName: tokenName,
		Quota:     info.PriceData.Quota,
		Content:   logContent,
		TokenId:   info.TokenId,
		Group:     info.UsingGroup,
		Other:     other,
	})
	model.UpdateUserUsedQuotaAndRequestCount(info.UserId, info.PriceData.Quota)
	model.UpdateChannelUsedQuota(info.ChannelId, info.PriceData.Quota)
}

// ---------------------------------------------------------------------------
// 异步任务计费辅助函数
// ---------------------------------------------------------------------------

// resolveTokenKey 通过 TokenId 运行时获取令牌 Key（用于 Redis 缓存操作）。
// 如果令牌已被删除或查询失败，返回空字符串。
func resolveTokenKey(ctx context.Context, tokenId int, taskID string) string {
	token, err := model.GetTokenById(tokenId)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("获取令牌 key 失败 (tokenId=%d, task=%s): %s", tokenId, taskID, err.Error()))
		return ""
	}
	return token.Key
}

// taskBillingOther 从 task 的 BillingContext 构建日志 Other 字段。
func taskBillingOther(task *model.Task) map[string]interface{} {
	other := make(map[string]interface{})
	if bc := task.PrivateData.BillingContext; bc != nil {
		other["model_price"] = bc.ModelPrice
		if bc.ModelRatio > 0 {
			other["model_ratio"] = bc.ModelRatio
		}
		other["group_ratio"] = bc.GroupRatio
		if priceData := taskBillingContextPriceData(bc); priceData != nil {
			for k, v := range priceData.OtherRatios() {
				other[k] = v
			}
		}
	}
	props := task.Properties
	if props.UpstreamModelName != "" && props.UpstreamModelName != props.OriginModelName {
		other["is_model_mapped"] = true
		other["upstream_model_name"] = props.UpstreamModelName
	}
	return other
}

func taskBillingContextPriceData(bc *model.TaskBillingContext) *types.PriceData {
	if bc == nil || len(bc.OtherRatios) == 0 {
		return nil
	}
	priceData := &types.PriceData{}
	if !priceData.ReplaceOtherRatios(bc.OtherRatios) {
		return nil
	}
	return priceData
}

// taskModelName 从 BillingContext 或 Properties 中获取模型名称。
func taskModelName(task *model.Task) string {
	if bc := task.PrivateData.BillingContext; bc != nil && bc.OriginModelName != "" {
		return bc.OriginModelName
	}
	return task.Properties.OriginModelName
}

// RefundTaskQuota 统一的任务失败退款逻辑。
// 当异步任务失败时，将预扣的 quota 退还给用户（支持钱包和订阅），并退还令牌额度。
func RefundTaskQuota(ctx context.Context, task *model.Task, reason string) {
	if task == nil {
		return
	}
	if requestId := strings.TrimSpace(task.PrivateData.BillingRequestId); requestId != "" {
		tokenKey := resolveTokenKey(ctx, task.PrivateData.TokenId, task.TaskID)
		result, err := model.RefundTaskBillingReservation(task.ID, requestId, tokenKey)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("任务 %s 持久化退款失败: %s", task.TaskID, err.Error()))
			return
		}
		if !result.Applied {
			return
		}
		quota := result.Reservation.ReservedQuota
		if quota <= 0 {
			return
		}
		other := taskBillingOther(task)
		other["task_id"] = task.TaskID
		other["reason"] = reason
		other["billing_request_id"] = requestId
		model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
			UserId:    task.UserId,
			LogType:   model.LogTypeRefund,
			Content:   "",
			ChannelId: task.ChannelId,
			ModelName: taskModelName(task),
			Quota:     quota,
			TokenId:   task.PrivateData.TokenId,
			Group:     task.Group,
			Other:     other,
			NodeName:  task.PrivateData.NodeName,
		})
		return
	}

	quota := task.Quota
	if quota == 0 {
		return
	}
	legacyResult, err := model.RefundLegacyTaskBilling(task.ID)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("任务 %s 兼容退款事务失败: %s", task.TaskID, err.Error()))
		return
	}
	if !legacyResult.Applied {
		return
	}
	quota = legacyResult.PreviousQuota

	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["reason"] = reason
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   "",
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     quota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
	})
}

func markAsyncBillingSettlementShortfall(ctx context.Context, resourceId string, requestId string, targetQuota int, settleErr error) {
	failureCode, deterministic := model.BillingSettlementFailureCode(settleErr)
	if !deterministic {
		return
	}
	if _, err := model.MarkBillingReservationSettlementShortfall(requestId, targetQuota, failureCode); err != nil {
		logger.LogError(ctx, fmt.Sprintf("异步任务 %s 结算欠款审计失败: %s", resourceId, err.Error()))
	}
}

// RecalculateTaskQuota 通用的异步差额结算。
// actualQuota 是任务完成后的实际应扣额度，与预扣额度 (task.Quota) 做差额结算。
// reason 用于日志记录（例如 "token重算" 或 "adaptor调整"）。
// clamps 可选：若计算 actualQuota 时发生额度饱和，将其记入日志 admin_info（仅管理员可见）。
func RecalculateTaskQuota(ctx context.Context, task *model.Task, actualQuota int, reason string, clamps ...*common.QuotaClamp) {
	if task == nil || actualQuota < 0 {
		return
	}
	if requestId := strings.TrimSpace(task.PrivateData.BillingRequestId); requestId != "" {
		tokenKey := resolveTokenKey(ctx, task.PrivateData.TokenId, task.TaskID)
		result, err := model.SettleTaskBillingReservation(task.ID, requestId, actualQuota, tokenKey)
		if err != nil {
			markAsyncBillingSettlementShortfall(ctx, task.TaskID, requestId, actualQuota, err)
			logger.LogError(ctx, fmt.Sprintf("任务 %s 持久化结算失败: %s", task.TaskID, err.Error()))
			return
		}
		if !result.Applied {
			return
		}
		preConsumedQuota := result.PreviousReservedQuota
		quotaDelta := actualQuota - preConsumedQuota
		task.Quota = actualQuota
		if quotaDelta == 0 {
			logger.LogInfo(ctx, fmt.Sprintf("任务 %s 预扣费准确（%s，%s）", task.TaskID, logger.LogQuota(actualQuota), reason))
			return
		}
		logger.LogInfo(ctx, fmt.Sprintf("任务 %s 差额结算：delta=%s（实际：%s，预扣：%s，%s）",
			task.TaskID,
			logger.LogQuota(quotaDelta),
			logger.LogQuota(actualQuota),
			logger.LogQuota(preConsumedQuota),
			reason,
		))
		var logType int
		var logQuota int
		if quotaDelta > 0 {
			logType = model.LogTypeConsume
			logQuota = quotaDelta
			model.UpdateUserUsedQuotaAndRequestCount(task.UserId, quotaDelta)
			model.UpdateChannelUsedQuota(task.ChannelId, quotaDelta)
		} else {
			logType = model.LogTypeRefund
			logQuota = -quotaDelta
		}
		other := taskBillingOther(task)
		other["task_id"] = task.TaskID
		other["pre_consumed_quota"] = preConsumedQuota
		other["actual_quota"] = actualQuota
		other["billing_request_id"] = requestId
		for _, clamp := range clamps {
			attachQuotaSaturationToOther(other, clamp)
		}
		model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
			UserId:    task.UserId,
			LogType:   logType,
			Content:   reason,
			ChannelId: task.ChannelId,
			ModelName: taskModelName(task),
			Quota:     logQuota,
			TokenId:   task.PrivateData.TokenId,
			Group:     task.Group,
			Other:     other,
			NodeName:  task.PrivateData.NodeName,
		})
		return
	}
	if actualQuota == 0 {
		return
	}
	legacyResult, err := model.SettleLegacyTaskBilling(task.ID, actualQuota)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("任务 %s 兼容结算事务失败: %s", task.TaskID, err.Error()))
		return
	}
	if !legacyResult.Applied {
		return
	}
	preConsumedQuota := legacyResult.PreviousQuota
	quotaDelta := actualQuota - preConsumedQuota

	if quotaDelta == 0 {
		logger.LogInfo(ctx, fmt.Sprintf("任务 %s 预扣费准确（%s，%s）",
			task.TaskID, logger.LogQuota(actualQuota), reason))
		return
	}

	logger.LogInfo(ctx, fmt.Sprintf("任务 %s 差额结算：delta=%s（实际：%s，预扣：%s，%s）",
		task.TaskID,
		logger.LogQuota(quotaDelta),
		logger.LogQuota(actualQuota),
		logger.LogQuota(preConsumedQuota),
		reason,
	))

	task.Quota = actualQuota

	var logType int
	var logQuota int
	if quotaDelta > 0 {
		logType = model.LogTypeConsume
		logQuota = quotaDelta
		model.UpdateUserUsedQuotaAndRequestCount(task.UserId, quotaDelta)
		model.UpdateChannelUsedQuota(task.ChannelId, quotaDelta)
	} else {
		logType = model.LogTypeRefund
		logQuota = -quotaDelta
	}
	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["pre_consumed_quota"] = preConsumedQuota
	other["actual_quota"] = actualQuota
	for _, clamp := range clamps {
		attachQuotaSaturationToOther(other, clamp)
	}
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   logType,
		Content:   reason,
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     logQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	})
}

// RecalculateTaskQuotaByTokens 根据实际 token 消耗重新计费（异步差额结算）。
// 当任务成功且返回了 totalTokens 时，根据模型倍率和分组倍率重新计算实际扣费额度，
// 与预扣费的差额进行补扣或退还。支持钱包和订阅计费来源。
func RecalculateTaskQuotaByTokens(ctx context.Context, task *model.Task, totalTokens int) {
	if task == nil || totalTokens <= 0 {
		return
	}
	actualQuota, clamp, ok := taskTokenQuotaFromSnapshot(task, totalTokens)
	if !ok {
		logger.LogWarn(ctx, fmt.Sprintf("任务 %s 缺少有效的提交时计费快照，按原预扣额度完成结算", task.TaskID))
		RecalculateTaskQuota(ctx, task, task.Quota, "缺少有效的提交时计费快照，保持预扣额度")
		return
	}
	bc := task.PrivateData.BillingContext
	otherMultiplier := 1.0
	if priceData := taskBillingContextPriceData(bc); priceData != nil {
		otherMultiplier = priceData.OtherRatioMultiplier()
	}
	reason := fmt.Sprintf("token重算：tokens=%d, modelRatio=%.2f, groupRatio=%.2f, otherMultiplier=%.4f", totalTokens, bc.ModelRatio, bc.GroupRatio, otherMultiplier)
	RecalculateTaskQuota(ctx, task, actualQuota, reason, clamp)
}

func taskTokenQuotaFromSnapshot(task *model.Task, totalTokens int) (int, *common.QuotaClamp, bool) {
	if task == nil || totalTokens <= 0 || task.PrivateData.BillingContext == nil {
		return 0, nil, false
	}
	bc := task.PrivateData.BillingContext
	if bc.ModelRatio <= 0 || bc.GroupRatio <= 0 ||
		math.IsNaN(bc.ModelRatio) || math.IsInf(bc.ModelRatio, 0) ||
		math.IsNaN(bc.GroupRatio) || math.IsInf(bc.GroupRatio, 0) {
		return 0, nil, false
	}
	otherMultiplier := 1.0
	if len(bc.OtherRatios) > 0 {
		priceData := taskBillingContextPriceData(bc)
		if priceData == nil {
			return 0, nil, false
		}
		otherMultiplier = priceData.OtherRatioMultiplier()
	}
	quota, clamp := common.QuotaFromFloatChecked(float64(totalTokens) * bc.ModelRatio * bc.GroupRatio * otherMultiplier)
	return quota, clamp, true
}
