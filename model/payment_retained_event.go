package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// RecordRetainedPaymentEventReceived is the durable inbox entry point for the
// signed Creem, Waffo and Waffo Pancake compatibility webhooks. These orders
// still settle through their legacy projections, so this function deliberately
// records verified facts without requiring a canonical PaymentOrder.
//
// A terminal event with the same payload is a duplicate delivery. Reusing the
// same stable provider event key with different facts is retained as a manual-
// review incident and must never proceed to settlement.
func RecordRetainedPaymentEventReceived(input PaymentEventInput) error {
	input.normalizeIdentity()
	if err := input.validateIdentity(); err != nil {
		return err
	}
	payloadDigest := PaymentPayloadDigest(input.NormalizedPayload)
	var postCommitErr error
	err := DB.Transaction(func(tx *gorm.DB) error {
		event, created, err := UpsertPaymentEvent(tx, paymentEventFromInput(input, payloadDigest))
		if err != nil || created {
			return err
		}
		if retainedPaymentEventPayloadConflict(event, input, payloadDigest) {
			if err := finishPaymentEventWithReviewCodeTx(tx, event.ID, PaymentEventStatusManualReview,
				PaymentReviewCodeEventKeyPayloadConflict, "event key was reused with a different payload", event.PaymentOrderID); err != nil {
				return err
			}
			if err := tx.Model(&PaymentEvent{}).Where("id = ?", event.ID).Updates(map[string]interface{}{
				"attempts":   gorm.Expr("attempts + ?", 1),
				"updated_at": common.GetTimestamp(),
			}).Error; err != nil {
				return err
			}
			postCommitErr = ErrPaymentEventConflict
			return nil
		}
		if event.ReviewCode == PaymentReviewCodeEventKeyPayloadConflict {
			postCommitErr = ErrPaymentEventConflict
			return nil
		}
		switch event.Status {
		case PaymentEventStatusProcessed, PaymentEventStatusManualReview, PaymentEventStatusDismissed, PaymentEventStatusCredentialRevoked:
			if err := tx.Model(&PaymentEvent{}).Where("id = ?", event.ID).Updates(map[string]interface{}{
				"attempts":   gorm.Expr("attempts + ?", 1),
				"updated_at": common.GetTimestamp(),
			}).Error; err != nil {
				return err
			}
			postCommitErr = ErrPaymentEventDuplicate
		}
		return nil
	})
	if err != nil {
		return err
	}
	return postCommitErr
}

// MarkRetainedPaymentEventProcessed closes a retained-provider inbox item only
// after its legacy settlement transaction has committed. If settlement fails,
// the event remains received/failed and a provider retry can safely resume it.
func MarkRetainedPaymentEventProcessed(input PaymentEventInput) error {
	input.normalizeIdentity()
	if err := input.validateIdentity(); err != nil {
		return err
	}
	payloadDigest := PaymentPayloadDigest(input.NormalizedPayload)
	var postCommitErr error
	err := DB.Transaction(func(tx *gorm.DB) error {
		var event PaymentEvent
		if err := lockForUpdate(tx).Where("provider = ? AND event_key = ?", input.Provider, input.EventKey).First(&event).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("payment event not found")
			}
			return err
		}
		if retainedPaymentEventPayloadConflict(&event, input, payloadDigest) {
			if err := finishPaymentEventWithReviewCodeTx(tx, event.ID, PaymentEventStatusManualReview,
				PaymentReviewCodeEventKeyPayloadConflict, "event key was reused with a different payload", event.PaymentOrderID); err != nil {
				return err
			}
			postCommitErr = ErrPaymentEventConflict
			return nil
		}
		if event.ReviewCode == PaymentReviewCodeEventKeyPayloadConflict {
			postCommitErr = ErrPaymentEventConflict
			return nil
		}
		if event.Status == PaymentEventStatusManualReview || event.Status == PaymentEventStatusDismissed || event.Status == PaymentEventStatusCredentialRevoked {
			postCommitErr = ErrPaymentManualReview
			return nil
		}
		now := common.GetTimestamp()
		updates := map[string]interface{}{
			"status":       PaymentEventStatusProcessed,
			"attempts":     gorm.Expr("attempts + ?", 1),
			"last_error":   "",
			"review_code":  "",
			"processed_at": now,
			"updated_at":   now,
		}
		return tx.Model(&PaymentEvent{}).Where("id = ?", event.ID).Updates(updates).Error
	})
	if err != nil {
		return err
	}
	return postCommitErr
}

func retainedPaymentEventPayloadConflict(event *PaymentEvent, input PaymentEventInput, payloadDigest string) bool {
	if event == nil || event.PayloadDigest != payloadDigest {
		return true
	}
	return event.ProviderLivemode != nil && input.ProviderLivemode != nil && *event.ProviderLivemode != *input.ProviderLivemode
}
