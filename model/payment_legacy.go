package model

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	ErrLegacyPaymentContractUnavailable      = errors.New("legacy payment contract cannot be reconstructed safely")
	ErrLegacySubscriptionContractUnavailable = errors.New("legacy subscription contract snapshot is unavailable")
	ErrLegacyTopUpQuotaSnapshotUnavailable   = errors.New("legacy top-up quota snapshot is unavailable")
)

// AdoptLegacyPaymentOrder creates a canonical accounting record for a pending
// order produced by an older frontend/API. Adoption is only allowed after a
// signed paid event and only when the signed amount matches the legacy order.
// This keeps classic clients compatible without preserving the old unverified
// settlement path. Legacy Stripe trade IDs derived from the protected
// `new-api-ref-` seed remain supported by this adoption and settlement path.
func AdoptLegacyPaymentOrder(input PaymentEventInput) (*PaymentOrder, error) {
	input.normalizeIdentity()
	if !stripePaidEventModeAllowed(input) {
		return nil, ErrPaymentManualReview
	}
	tradeNo := input.TradeNo
	if tradeNo == "" || !input.Paid || input.PaidAmountMinor <= 0 || input.Currency == "" {
		return nil, ErrPaymentOrderNotFound
	}
	var adopted PaymentOrder
	if err := DB.Where("trade_no = ?", tradeNo).First(&adopted).Error; err == nil {
		return &adopted, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		credentialFenced := input.Provider == PaymentProviderEpay || input.Provider == PaymentProviderStripe || input.Provider == PaymentProviderXorPay
		if credentialFenced {
			if input.ProviderCredentialGeneration <= 0 {
				return ErrPaymentManualReview
			}
			if _, err := lockPaymentConfigurationFenceTx(tx); err != nil {
				return err
			}
		}
		var err error
		adopted, err = adoptLegacyPaymentOrderTx(tx, input)
		return err
	})
	if err != nil {
		if lookupErr := DB.Where("trade_no = ?", tradeNo).First(&adopted).Error; lookupErr == nil {
			return &adopted, nil
		}
		return nil, err
	}
	return &adopted, nil
}

func adoptLegacyPaymentOrderTx(tx *gorm.DB, input PaymentEventInput) (PaymentOrder, error) {
	if tx == nil {
		return PaymentOrder{}, errors.New("legacy payment adoption transaction is required")
	}
	input.normalizeIdentity()
	if !stripePaidEventModeAllowed(input) {
		return PaymentOrder{}, ErrPaymentManualReview
	}
	tradeNo := input.TradeNo
	if tradeNo == "" || !input.Paid || input.PaidAmountMinor <= 0 || input.Currency == "" {
		return PaymentOrder{}, ErrPaymentOrderNotFound
	}
	var adopted PaymentOrder
	if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&adopted).Error; err == nil {
		return adopted, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return PaymentOrder{}, err
	}

	credentialFenced := input.Provider == PaymentProviderEpay || input.Provider == PaymentProviderStripe || input.Provider == PaymentProviderXorPay
	var subscriptionOrder SubscriptionOrder
	if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&subscriptionOrder).Error; err == nil {
		var plan *SubscriptionPlan
		if legacySubscriptionNeedsPlanReconstruction(&subscriptionOrder) {
			var storedPlan SubscriptionPlan
			if err := lockForUpdate(tx).Where("id = ?", subscriptionOrder.PlanId).First(&storedPlan).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return PaymentOrder{}, err
				}
			} else {
				plan = &storedPlan
			}
		}
		if _, err := prepareLegacySubscriptionAdoptionContract(input, &subscriptionOrder, plan); err != nil {
			return PaymentOrder{}, err
		}
		if credentialFenced {
			available, err := paymentCredentialGenerationAvailableTx(tx, input.Provider,
				input.ProviderCredentialGeneration, subscriptionOrder.CreateTime, common.GetTimestamp())
			if err != nil {
				return PaymentOrder{}, err
			}
			if !available {
				return PaymentOrder{}, ErrPaymentManualReview
			}
		}
		currency := strings.ToUpper(strings.TrimSpace(subscriptionOrder.PaymentCurrency))
		adopted = PaymentOrder{
			TradeNo:                      tradeNo,
			UserID:                       subscriptionOrder.UserId,
			OrderKind:                    PaymentOrderKindSubscription,
			Provider:                     input.Provider,
			ProviderCredentialGeneration: input.ProviderCredentialGeneration,
			ProviderLivemode:             copyPaymentLivemode(input.ProviderLivemode),
			PaymentMethod:                subscriptionOrder.PaymentMethod,
			RequestID:                    legacyPaymentRequestID(input.Provider, tradeNo),
			ExpectedAmountMinor:          subscriptionOrder.ExpectedAmountMinor,
			Currency:                     currency,
			RequestedAmount:              int64(subscriptionOrder.PlanId),
			ProductSnapshot:              subscriptionOrder.PlanSnapshot,
			LegacyRecordType:             PaymentOrderKindSubscription,
			LegacyRecordID:               subscriptionOrder.Id,
			Status:                       PaymentOrderStatusPending,
			ExpiresAt:                    subscriptionOrder.ReserveUntil,
			CreatedAt:                    subscriptionOrder.CreateTime,
			UpdatedAt:                    common.GetTimestamp(),
			Version:                      1,
		}
		if err := setAdoptedProviderKeys(&adopted, input); err != nil {
			return PaymentOrder{}, err
		}
		if err := tx.Create(&adopted).Error; err != nil {
			return PaymentOrder{}, err
		}
		updates := map[string]interface{}{
			"payment_order_id":      adopted.ID,
			"expected_amount_minor": subscriptionOrder.ExpectedAmountMinor,
			"payment_currency":      subscriptionOrder.PaymentCurrency,
			"plan_snapshot":         subscriptionOrder.PlanSnapshot,
			"reserve_until":         subscriptionOrder.ReserveUntil,
		}
		if strings.TrimSpace(subscriptionOrder.PaymentProvider) == "" {
			updates["payment_provider"] = PaymentProviderEpay
		}
		if err := tx.Model(&SubscriptionOrder{}).Where("id = ?", subscriptionOrder.Id).Updates(updates).Error; err != nil {
			return PaymentOrder{}, err
		}
		return adopted, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return PaymentOrder{}, err
	}

	var topUp TopUp
	if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&topUp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PaymentOrder{}, ErrPaymentOrderNotFound
		}
		return PaymentOrder{}, err
	}
	if err := validateLegacyPaymentProviderAndMethod(topUp.PaymentProvider, topUp.PaymentMethod, input); err != nil {
		return PaymentOrder{}, err
	}
	if topUp.Status != common.TopUpStatusPending {
		return PaymentOrder{}, ErrPaymentProviderMismatch
	}
	exponent, ok := common.PaymentProviderCurrencyExponentOK(input.Provider, input.Currency)
	if !ok {
		return PaymentOrder{}, ErrPaymentCurrencyMismatch
	}
	expectedMinor, err := legacyPaymentMoneyMinorExact(topUp.Money, exponent, math.MaxInt32)
	if err != nil || expectedMinor != input.PaidAmountMinor {
		return PaymentOrder{}, ErrPaymentAmountMismatch
	}
	return PaymentOrder{}, fmt.Errorf("%w: %w", ErrLegacyPaymentContractUnavailable, ErrLegacyTopUpQuotaSnapshotUnavailable)
}

func legacySubscriptionNeedsPlanReconstruction(order *SubscriptionOrder) bool {
	return order != nil && order.ExpectedAmountMinor == 0 && strings.TrimSpace(order.PaymentCurrency) == "" &&
		strings.TrimSpace(order.PlanSnapshot) == "" && order.ReserveUntil == 0
}

func prepareLegacySubscriptionAdoptionContract(input PaymentEventInput, order *SubscriptionOrder,
	currentPlan *SubscriptionPlan) (*SubscriptionPlan, error) {
	if order == nil || order.Id <= 0 || order.UserId <= 0 || order.PlanId <= 0 || order.CreateTime <= 0 {
		return nil, fmt.Errorf("%w: %w: legacy subscription identity is incomplete", ErrLegacyPaymentContractUnavailable, ErrLegacySubscriptionContractUnavailable)
	}
	if err := validateLegacyPaymentProviderAndMethod(order.PaymentProvider, order.PaymentMethod, input); err != nil {
		return nil, err
	}
	if order.Status != common.TopUpStatusPending {
		return nil, ErrPaymentProviderMismatch
	}
	if legacySubscriptionNeedsPlanReconstruction(order) {
		if input.Provider != PaymentProviderEpay {
			return nil, ErrPaymentProviderMismatch
		}
		if input.Currency != "CNY" {
			return nil, ErrPaymentCurrencyMismatch
		}
		legacyMinor, err := legacyPaymentMoneyMinorExact(order.Money, 2, math.MaxInt64)
		if err != nil || legacyMinor <= 0 || legacyMinor != input.PaidAmountMinor {
			return nil, ErrPaymentAmountMismatch
		}
		if currentPlan == nil {
			return nil, fmt.Errorf("%w: %w: subscription plan is missing", ErrLegacyPaymentContractUnavailable, ErrLegacySubscriptionContractUnavailable)
		}
		if currentPlan.CreatedAt <= 0 || currentPlan.UpdatedAt <= 0 || currentPlan.CreatedAt > order.CreateTime ||
			currentPlan.UpdatedAt >= order.CreateTime {
			return nil, fmt.Errorf("%w: %w: subscription plan changed after the legacy order was created", ErrLegacyPaymentContractUnavailable, ErrLegacySubscriptionContractUnavailable)
		}
		if err := ValidateSubscriptionPlanForExternalPayment(currentPlan); err != nil {
			return nil, fmt.Errorf("%w: %w: subscription plan is no longer a valid external-payment contract", ErrLegacyPaymentContractUnavailable, ErrLegacySubscriptionContractUnavailable)
		}
		planMinor, err := legacyPaymentMoneyMinorExact(currentPlan.PriceAmount, 2, math.MaxInt64)
		if err != nil || planMinor != legacyMinor {
			return nil, fmt.Errorf("%w: %w: subscription plan price no longer matches the legacy order", ErrLegacyPaymentContractUnavailable, ErrLegacySubscriptionContractUnavailable)
		}
		if err := order.SetPlanSnapshot(currentPlan); err != nil {
			return nil, fmt.Errorf("%w: %w: subscription plan snapshot is invalid", ErrLegacyPaymentContractUnavailable, ErrLegacySubscriptionContractUnavailable)
		}
		order.ExpectedAmountMinor = legacyMinor
		order.PaymentCurrency = "CNY"
		order.ReserveUntil = order.CreateTime + int64(defaultSubscriptionReservationTTL/time.Second)
	} else if order.ExpectedAmountMinor <= 0 || strings.TrimSpace(order.PaymentCurrency) == "" ||
		strings.TrimSpace(order.PlanSnapshot) == "" || order.ReserveUntil <= 0 {
		return nil, fmt.Errorf("%w: %w: legacy subscription payment fields are only partially initialized", ErrLegacyPaymentContractUnavailable, ErrLegacySubscriptionContractUnavailable)
	}
	if order.ExpectedAmountMinor != input.PaidAmountMinor {
		return nil, ErrPaymentAmountMismatch
	}
	currency := strings.ToUpper(strings.TrimSpace(order.PaymentCurrency))
	if !strings.EqualFold(currency, input.Currency) {
		return nil, ErrPaymentCurrencyMismatch
	}
	inputProviderOrderKey := strings.TrimSpace(input.ProviderOrderKey)
	if order.ProviderOrderKey != nil && strings.TrimSpace(*order.ProviderOrderKey) != inputProviderOrderKey {
		return nil, ErrPaymentProviderMismatch
	}
	if order.ProviderOrderId != "" {
		expectedProviderOrderKey := input.Provider + ":" + strings.TrimSpace(order.ProviderOrderId)
		if inputProviderOrderKey != expectedProviderOrderKey {
			return nil, ErrPaymentProviderMismatch
		}
	}
	snapshot, err := order.planSnapshot()
	if err != nil {
		return nil, fmt.Errorf("%w: %w: subscription plan snapshot is invalid", ErrLegacyPaymentContractUnavailable, ErrLegacySubscriptionContractUnavailable)
	}
	plan, err := snapshot.SubscriptionPlan()
	if err != nil {
		return nil, fmt.Errorf("%w: %w: subscription plan snapshot is invalid", ErrLegacyPaymentContractUnavailable, ErrLegacySubscriptionContractUnavailable)
	}
	return plan, nil
}

func validateLegacyPaymentProviderAndMethod(storedProvider, storedMethod string, input PaymentEventInput) error {
	storedProvider = strings.ToLower(strings.TrimSpace(storedProvider))
	if storedProvider == input.Provider {
		return nil
	}
	if storedProvider != "" || input.Provider != PaymentProviderEpay || storedMethod == "" || storedMethod != input.PaymentMethod ||
		isReservedNonEpayPaymentMethod(input.PaymentMethod) {
		return ErrPaymentProviderMismatch
	}
	return nil
}

func isReservedNonEpayPaymentMethod(method string) bool {
	switch method {
	case PaymentMethodStripe, PaymentMethodCreem, PaymentMethodWaffo, PaymentMethodWaffoPancake,
		PaymentMethodXorPayNative, PaymentMethodXorPayAlipay, PaymentMethodXorPayJSAPI, PaymentMethodBalance:
		return true
	default:
		return false
	}
}

func legacyPaymentMoneyMinorExact(amount float64, exponent int32, max int64) (int64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 || exponent < 0 || max <= 0 {
		return 0, ErrPaymentAmountMismatch
	}
	minor := decimal.NewFromFloat(amount).Mul(decimal.New(1, exponent))
	if !minor.Equal(minor.Round(0)) || minor.IsNegative() || minor.GreaterThan(decimal.NewFromInt(max)) {
		return 0, ErrPaymentAmountMismatch
	}
	return minor.IntPart(), nil
}

func setAdoptedProviderKeys(order *PaymentOrder, input PaymentEventInput) error {
	if order == nil {
		return errors.New("payment order is required")
	}
	if value := strings.TrimSpace(input.ProviderOrderKey); value != "" {
		order.ProviderOrderKey = &value
	}
	if value := strings.TrimSpace(input.ProviderPaymentKey); value != "" {
		order.ProviderPaymentKey = &value
	}
	return nil
}

func legacyPaymentRequestID(provider, tradeNo string) string {
	digest := PaymentPayloadDigest(fmt.Sprintf("%s:%s", provider, tradeNo))
	return "legacy_" + digest[:48]
}
