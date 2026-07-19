package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const (
	ViolationFeeCodePrefix     = "violation_fee."
	CSAMViolationMarker        = "Failed check: SAFETY_CHECK_TYPE"
	ContentViolatesUsageMarker = "Content violates usage guidelines"
)

func IsViolationFeeCode(code types.ErrorCode) bool {
	return strings.HasPrefix(string(code), ViolationFeeCodePrefix)
}

func HasCSAMViolationMarker(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), CSAMViolationMarker) || strings.Contains(err.Error(), ContentViolatesUsageMarker) {
		return true
	}
	msg := err.ToOpenAIError().Message
	return strings.Contains(msg, CSAMViolationMarker) || strings.Contains(err.Error(), ContentViolatesUsageMarker)
}

func WrapAsViolationFeeGrokCSAM(err *types.NewAPIError) *types.NewAPIError {
	if err == nil {
		return nil
	}
	oai := err.ToOpenAIError()
	oai.Type = string(types.ErrorCodeViolationFeeGrokCSAM)
	oai.Code = string(types.ErrorCodeViolationFeeGrokCSAM)
	return types.WithOpenAIError(oai, err.StatusCode, types.ErrOptionWithSkipRetry())
}

// NormalizeViolationFeeError ensures:
// - if the CSAM marker is present, error.code is set to a stable violation-fee code and skip-retry is enabled.
// - if error.code already has the violation-fee prefix, skip-retry is enabled.
//
// It must be called before retry decision logic.
func NormalizeViolationFeeError(err *types.NewAPIError) *types.NewAPIError {
	if err == nil {
		return nil
	}

	if HasCSAMViolationMarker(err) {
		return WrapAsViolationFeeGrokCSAM(err)
	}

	if IsViolationFeeCode(err.GetErrorCode()) {
		oai := err.ToOpenAIError()
		return types.WithOpenAIError(oai, err.StatusCode, types.ErrOptionWithSkipRetry())
	}

	return err
}

func shouldChargeViolationFee(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.GetErrorCode() == types.ErrorCodeViolationFeeGrokCSAM {
		return true
	}
	// In case some callers didn't normalize, keep a safety net.
	return HasCSAMViolationMarker(err)
}

func calcViolationFeeQuota(amount, groupRatio float64) (int, *common.QuotaClamp) {
	if amount <= 0 {
		return 0, nil
	}
	if groupRatio <= 0 {
		return 0, nil
	}
	quota, clamp := common.QuotaRoundChecked(amount * common.QuotaPerUnit * groupRatio)
	if quota <= 0 {
		return 0, clamp
	}
	return quota, clamp
}

// ChargeViolationFeeIfNeeded charges an additional fee after the normal flow finishes (including refund).
// It uses Grok fee settings as the fee policy.
func ChargeViolationFeeIfNeeded(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, apiErr *types.NewAPIError) bool {
	if ctx == nil || relayInfo == nil || apiErr == nil {
		return false
	}
	//if relayInfo.IsPlayground {
	//	return false
	//}
	if !shouldChargeViolationFee(apiErr) {
		return false
	}

	settings := model_setting.GetGrokSettings()
	if settings == nil || !settings.ViolationDeductionEnabled {
		return false
	}

	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio
	feeQuota, clamp := calcViolationFeeQuota(settings.ViolationDeductionAmount, groupRatio)
	noteQuotaClamp(relayInfo, clamp)
	if feeQuota <= 0 {
		return false
	}

	fundingSource := relayInfo.BillingSource
	if fundingSource != BillingSourceSubscription {
		fundingSource = BillingSourceWallet
	}
	requestID := "violation_" + common.Sha1([]byte(relayInfo.RequestId+":"+string(types.ErrorCodeViolationFeeGrokCSAM)))
	alreadySettled := false
	_, err := model.PreConsumeBillingReservation(model.BillingReservationInput{
		RequestId: requestID, UserId: relayInfo.UserId, TokenId: relayInfo.TokenId,
		FundingSource: fundingSource, Quota: feeQuota, SkipToken: relayInfo.IsPlayground,
	}, relayInfo.TokenKey)
	if err == nil {
		_, err = model.SettleBillingReservation(requestID, feeQuota, relayInfo.TokenKey)
	}
	if errors.Is(err, model.ErrBillingReservationFinalized) {
		var reservation *model.BillingReservation
		reservation, err = model.GetBillingReservation(requestID)
		if err == nil && (reservation.Status != model.BillingReservationStatusSettled || reservation.SettledQuota != feeQuota) {
			err = model.ErrBillingReservationConflict
		} else if err == nil {
			alreadySettled = true
		}
	}
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("failed to charge violation fee: %s", err.Error()))
		return false
	}
	if alreadySettled {
		return true
	}
	if fundingSource == BillingSourceSubscription {
		checkAndSendSubscriptionQuotaNotify(relayInfo)
	} else {
		checkAndSendQuotaNotify(relayInfo, feeQuota, 0)
	}

	model.UpdateUserUsedQuotaAndRequestCount(relayInfo.UserId, feeQuota)
	channelID := 0
	if relayInfo.ChannelMeta != nil {
		channelID = relayInfo.ChannelId
	}
	if channelID > 0 {
		model.UpdateChannelUsedQuota(channelID, feeQuota)
	}

	useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
	tokenName := ctx.GetString("token_name")
	oai := apiErr.ToOpenAIError()

	other := map[string]any{
		"violation_fee":        true,
		"violation_fee_code":   string(types.ErrorCodeViolationFeeGrokCSAM),
		"fee_quota":            feeQuota,
		"base_amount":          settings.ViolationDeductionAmount,
		"group_ratio":          groupRatio,
		"status_code":          apiErr.StatusCode,
		"upstream_error_type":  oai.Type,
		"upstream_error_code":  fmt.Sprintf("%v", oai.Code),
		"violation_fee_marker": CSAMViolationMarker,
	}

	model.RecordConsumeLog(ctx, relayInfo.UserId, model.RecordConsumeLogParams{
		ChannelId:      channelID,
		ModelName:      relayInfo.OriginModelName,
		TokenName:      tokenName,
		Quota:          feeQuota,
		Content:        "Violation fee charged",
		TokenId:        relayInfo.TokenId,
		UseTimeSeconds: int(useTimeSeconds),
		IsStream:       relayInfo.IsStream,
		Group:          relayInfo.UsingGroup,
		Other:          other,
	})

	return true
}
