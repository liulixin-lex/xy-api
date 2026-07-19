package service

import (
	"fmt"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const (
	BillingSourceWallet       = "wallet"
	BillingSourceSubscription = "subscription"

	billingSettlementStatusSettled   = "settled"
	billingSettlementStatusPending   = "pending"
	billingSettlementStatusShortfall = "shortfall"
	billingSettlementStatusFailed    = "failed"
)

type billingSettlementLogState struct {
	Status           string
	TargetQuota      int
	ChargedQuota     int
	OutstandingQuota int
	FailureCode      string
}

// PreConsumeBilling 根据用户计费偏好创建 BillingSession 并执行预扣费。
// 会话存储在 relayInfo.Billing 上，供后续 Settle / Refund 使用。
func PreConsumeBilling(c *gin.Context, preConsumedQuota int, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	session, apiErr := NewBillingSession(c, relayInfo, preConsumedQuota)
	if apiErr != nil {
		return apiErr
	}
	relayInfo.Billing = session
	return nil
}

// ---------------------------------------------------------------------------
// SettleBilling — 后结算辅助函数
// ---------------------------------------------------------------------------

// SettleBilling 执行计费结算。所有路径都必须通过 durable BillingSession，
// 避免资金来源与 Token 分步更新形成半结算。
func SettleBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int) error {
	if relayInfo.Billing != nil {
		preConsumed := relayInfo.Billing.GetPreConsumedQuota()
		delta := actualQuota - preConsumed

		if delta > 0 {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费后补扣费：%s（实际消耗：%s，预扣费：%s）",
				logger.FormatQuota(delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else if delta < 0 {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费后返还扣费：%s（实际消耗：%s，预扣费：%s）",
				logger.FormatQuota(-delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费与实际消耗一致，无需调整：%s（按次计费）",
				logger.FormatQuota(actualQuota),
			))
		}

		if err := relayInfo.Billing.Settle(actualQuota); err != nil {
			return err
		}

		// 发送额度通知（订阅计费使用订阅剩余额度）
		if actualQuota != 0 {
			if relayInfo.BillingSource == BillingSourceSubscription {
				checkAndSendSubscriptionQuotaNotify(relayInfo)
			} else {
				checkAndSendQuotaNotify(relayInfo, actualQuota-preConsumed, preConsumed)
			}
		}
		return nil
	}

	// A missing session must never fall back to split funding/token updates.
	// Recreate a zero-history durable lifecycle only when no legacy pre-consume
	// was recorded; otherwise fail closed for reconciliation.
	if relayInfo.FinalPreConsumedQuota != 0 {
		return fmt.Errorf("billing session is missing for pre-consumed quota")
	}
	session, apiErr := NewBillingSession(ctx, relayInfo, actualQuota)
	if apiErr != nil {
		return apiErr
	}
	relayInfo.Billing = session
	return session.Settle(actualQuota)
}

func settleBillingForLog(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int) (billingSettlementLogState, error) {
	state := billingSettlementLogState{
		Status:       billingSettlementStatusSettled,
		TargetQuota:  actualQuota,
		ChargedQuota: actualQuota,
	}
	err := SettleBilling(ctx, relayInfo, actualQuota)
	if err == nil {
		return state, nil
	}

	state.Status = billingSettlementStatusFailed
	state.ChargedQuota = relayInfo.FinalPreConsumedQuota
	if relayInfo.Billing != nil {
		state.ChargedQuota = relayInfo.Billing.GetPreConsumedQuota()
	}
	if reservation, lookupErr := model.GetBillingReservation(relayInfo.RequestId); lookupErr == nil {
		state.ChargedQuota = reservation.ReservedQuota
		state.FailureCode = reservation.SettlementFailureCode
		switch {
		case reservation.Status == model.BillingReservationStatusSettled:
			state.Status = billingSettlementStatusSettled
			state.ChargedQuota = reservation.SettledQuota
		case reservation.Status == model.BillingReservationStatusReserved && reservation.ShortfallFreezeApplied:
			state.Status = billingSettlementStatusShortfall
		case reservation.Status == model.BillingReservationStatusReserved && reservation.SettlementPending:
			state.Status = billingSettlementStatusPending
		}
	}
	if state.FailureCode == "" {
		state.FailureCode, _ = model.BillingSettlementFailureCode(err)
	}
	if state.TargetQuota > state.ChargedQuota {
		state.OutstandingQuota = state.TargetQuota - state.ChargedQuota
	}
	return state, err
}

func attachBillingSettlementLogState(other map[string]interface{}, state billingSettlementLogState) {
	if other == nil || state.Status == billingSettlementStatusSettled {
		return
	}
	other["billing_settlement_status"] = state.Status
	other["billing_target_quota"] = state.TargetQuota
	other["billing_charged_quota"] = state.ChargedQuota
	other["billing_outstanding_quota"] = state.OutstandingQuota
	if state.FailureCode != "" {
		other["billing_failure_code"] = state.FailureCode
	}
}
