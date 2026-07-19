package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

func midjourneyBillingOther(task *model.Midjourney, reason string) map[string]interface{} {
	other := map[string]interface{}{
		"task_id": task.MjId,
		"reason":  reason,
	}
	if requestId := strings.TrimSpace(task.PrivateData.BillingRequestId); requestId != "" {
		other["billing_request_id"] = requestId
	}
	if task.PrivateData.BillingSource != "" {
		other["billing_source"] = task.PrivateData.BillingSource
	}
	if task.PrivateData.SubscriptionId > 0 {
		other["subscription_id"] = task.PrivateData.SubscriptionId
	}
	if billingContext := task.PrivateData.BillingContext; billingContext != nil {
		other["model_price"] = billingContext.ModelPrice
		other["group_ratio"] = billingContext.GroupRatio
		if billingContext.ModelRatio > 0 {
			other["model_ratio"] = billingContext.ModelRatio
		}
	}
	return other
}

// RefundMidjourneyTaskQuota reverses a provider-confirmed failure through the
// durable reservation. Missing/deleted tokens are handled by the model ledger
// without blocking the wallet or subscription refund.
func RefundMidjourneyTaskQuota(ctx context.Context, task *model.Midjourney, reason string) {
	if task == nil {
		return
	}
	requestId := strings.TrimSpace(task.PrivateData.BillingRequestId)
	if requestId == "" {
		return
	}
	tokenKey := ""
	if task.PrivateData.TokenId > 0 {
		tokenKey = resolveTokenKey(ctx, task.PrivateData.TokenId, task.MjId)
	}
	result, err := model.RefundMidjourneyBillingReservation(task.Id, requestId, tokenKey)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Midjourney 任务 %s 持久化退款失败: %s", task.MjId, err.Error()))
		return
	}
	if !result.Applied || result.Reservation.ReservedQuota <= 0 {
		return
	}
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   reason,
		ChannelId: task.ChannelId,
		ModelName: CovertMjpActionToModelName(task.Action),
		Quota:     result.Reservation.ReservedQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     midjourneyBillingOther(task, reason),
		NodeName:  task.PrivateData.NodeName,
	})
}

// SettleMidjourneyTaskQuota closes a provider-confirmed success exactly once.
// Midjourney is normally fixed-price, but the delta path remains complete for
// future adaptor adjustments and uses the same atomic funding/token ledger.
func SettleMidjourneyTaskQuota(ctx context.Context, task *model.Midjourney, actualQuota int, reason string) {
	if task == nil || actualQuota < 0 {
		return
	}
	requestId := strings.TrimSpace(task.PrivateData.BillingRequestId)
	if requestId == "" {
		return
	}
	tokenKey := ""
	if task.PrivateData.TokenId > 0 {
		tokenKey = resolveTokenKey(ctx, task.PrivateData.TokenId, task.MjId)
	}
	result, err := model.SettleMidjourneyBillingReservation(task.Id, requestId, actualQuota, tokenKey)
	if err != nil {
		markAsyncBillingSettlementShortfall(ctx, task.MjId, requestId, actualQuota, err)
		logger.LogError(ctx, fmt.Sprintf("Midjourney 任务 %s 持久化结算失败: %s", task.MjId, err.Error()))
		return
	}
	if !result.Applied {
		return
	}
	previousQuota := result.PreviousReservedQuota
	delta := actualQuota - previousQuota
	task.Quota = actualQuota
	if delta == 0 {
		logger.LogInfo(ctx, fmt.Sprintf("Midjourney 任务 %s 预扣费准确（%s）", task.MjId, reason))
		return
	}

	logType := model.LogTypeRefund
	logQuota := -delta
	if delta > 0 {
		logType = model.LogTypeConsume
		logQuota = delta
		model.UpdateUserUsedQuotaAndRequestCount(task.UserId, delta)
		model.UpdateChannelUsedQuota(task.ChannelId, delta)
	}
	other := midjourneyBillingOther(task, reason)
	other["pre_consumed_quota"] = previousQuota
	other["actual_quota"] = actualQuota
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   logType,
		Content:   reason,
		ChannelId: task.ChannelId,
		ModelName: CovertMjpActionToModelName(task.Action),
		Quota:     logQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	})
}
