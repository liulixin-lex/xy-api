package controller

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

type retainedPaymentWebhookFacts struct {
	EventID          string `json:"event_id,omitempty"`
	BusinessEventID  string `json:"business_event_id,omitempty"`
	EventType        string `json:"event_type"`
	TradeNo          string `json:"trade_no,omitempty"`
	ProviderOrderKey string `json:"provider_order_key,omitempty"`
	ProviderState    string `json:"provider_state,omitempty"`
	PaidAmountMinor  int64  `json:"paid_amount_minor,omitempty"`
	Currency         string `json:"currency,omitempty"`
	PaymentMethod    string `json:"payment_method,omitempty"`
	Environment      string `json:"environment,omitempty"`
	PayloadDigest    string `json:"payload_digest,omitempty"`
}

func retainedPaymentNormalizedPayload(facts retainedPaymentWebhookFacts) (string, error) {
	payload, err := common.Marshal(facts)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func retainedPaymentEventKey(provider, upstreamEventID, eventType, providerOrderKey, tradeNo string) string {
	upstreamEventID = strings.TrimSpace(upstreamEventID)
	if upstreamEventID != "" && len(upstreamEventID) <= 255 {
		return upstreamEventID
	}
	return model.PaymentEventKey(provider, eventType, providerOrderKey, tradeNo, "")
}

func retainedPaymentInboxStopsSettlement(err error) bool {
	return errors.Is(err, model.ErrPaymentEventDuplicate) || errors.Is(err, model.ErrPaymentEventConflict)
}

// processCanonicalRetainedPaymentEvent gives canonical PaymentOrder rows first
// ownership of every verified retained-provider callback. Historical rows that
// predate PaymentOrder deliberately fall through to the legacy settlement
// projection, preserving upgrade compatibility without double-processing a
// canonical order through both accounting paths.
func processCanonicalRetainedPaymentEvent(event *service.NormalizedPaymentEvent) (bool, error) {
	if event == nil || strings.TrimSpace(event.TradeNo) == "" {
		return false, nil
	}
	_, err := model.GetPaymentOrderByTradeNo(event.TradeNo)
	if errors.Is(err, model.ErrPaymentOrderNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if err := service.RecordVerifiedPaymentWebhookReceived(event); err != nil {
		if errors.Is(err, model.ErrPaymentEventDuplicate) || errors.Is(err, model.ErrPaymentEventConflict) {
			return true, nil
		}
		return true, err
	}
	_, err = service.ProcessNormalizedPaymentEvent(event)
	if err == nil || errors.Is(err, model.ErrPaymentManualReview) || errors.Is(err, model.ErrPaymentEventConflict) {
		return true, nil
	}
	if errors.Is(err, model.ErrPaymentAmountMismatch) || errors.Is(err, model.ErrPaymentCurrencyMismatch) ||
		errors.Is(err, model.ErrPaymentProviderMismatch) || errors.Is(err, model.ErrPaymentEventInvalid) {
		if recordErr := service.RecordUnmatchedPaymentEvent(event, "canonical retained payment callback requires manual review"); recordErr != nil &&
			!errors.Is(recordErr, model.ErrPaymentEventConflict) {
			return true, recordErr
		}
		return true, nil
	}
	return true, err
}
