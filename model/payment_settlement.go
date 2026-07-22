package model

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxNormalizedPaymentPayloadBytes = 16 << 10

var (
	ErrPaymentEventInvalid          = errors.New("invalid normalized payment event")
	errPaymentTerminalAdminDecision = errors.New("payment was rejected or voided by an administrator")
)

// PaymentEventInput contains only verified provider facts. Callers must verify
// the provider signature before constructing this value.
type PaymentEventInput struct {
	Provider                     string
	EventKey                     string
	EventType                    string
	TradeNo                      string
	ProviderOrderKey             string
	ProviderPaymentKey           string
	ProviderResourceKey          string
	ProviderCredentialGeneration int64
	ProviderLivemode             *bool
	ProviderCreatedAt            int64
	ProviderState                string
	CustomerID                   string
	PaidAmountMinor              int64
	RefundedAmountMinor          int64
	DisputedAmountMinor          int64
	Currency                     string
	PaymentMethod                string
	Paid                         bool
	Failed                       bool
	Expired                      bool
	Refunded                     bool
	Disputed                     bool
	DisputeResolved              bool
	DisputeWon                   bool
	PermanentFailure             bool
	ManualReview                 bool
	NormalizedPayload            string
}

type PaymentSettlementResult struct {
	Order            *PaymentOrder
	Duplicate        bool
	ManualReview     bool
	UserID           int
	QuotaDelta       int
	AffiliateUserID  int
	AffiliateReward  int
	UserCacheChanged bool
	GroupCacheValue  string
	CacheUserIDs     []int
}

func (in *PaymentEventInput) normalizeIdentity() {
	if in == nil {
		return
	}
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.EventKey = strings.TrimSpace(in.EventKey)
	in.EventType = strings.TrimSpace(in.EventType)
	in.TradeNo = strings.TrimSpace(in.TradeNo)
	in.ProviderOrderKey = strings.TrimSpace(in.ProviderOrderKey)
	in.ProviderPaymentKey = strings.TrimSpace(in.ProviderPaymentKey)
	in.ProviderResourceKey = strings.TrimSpace(in.ProviderResourceKey)
	in.ProviderState = strings.TrimSpace(in.ProviderState)
	in.CustomerID = strings.TrimSpace(in.CustomerID)
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	in.PaymentMethod = strings.TrimSpace(in.PaymentMethod)
	if in.Provider != PaymentProviderEpay {
		in.PaymentMethod = strings.ToLower(in.PaymentMethod)
	}
}

func (in PaymentEventInput) validate() error {
	if err := in.validateIdentity(); err != nil {
		return err
	}
	if in.PaidAmountMinor < 0 || in.RefundedAmountMinor < 0 || in.DisputedAmountMinor < 0 {
		return fmt.Errorf("%w: invalid payment amount", ErrPaymentEventInvalid)
	}
	if in.Paid && (in.Failed || in.Expired || in.Refunded || in.Disputed || in.DisputeResolved) {
		return fmt.Errorf("%w: contradictory transition", ErrPaymentEventInvalid)
	}
	if in.Failed && in.Expired || in.Refunded && (in.Disputed || in.DisputeResolved) {
		return fmt.Errorf("%w: contradictory transition", ErrPaymentEventInvalid)
	}
	if in.DisputeWon && !in.DisputeResolved {
		return fmt.Errorf("%w: invalid dispute resolution", ErrPaymentEventInvalid)
	}
	if in.Paid && in.PaidAmountMinor <= 0 || in.Refunded && in.RefundedAmountMinor <= 0 || in.Disputed && in.DisputedAmountMinor <= 0 {
		return fmt.Errorf("%w: invalid payment amount", ErrPaymentEventInvalid)
	}
	if in.PermanentFailure && !in.Failed && !in.Expired {
		return fmt.Errorf("%w: invalid permanent failure", ErrPaymentEventInvalid)
	}
	if in.ManualReview && (in.Paid || in.Failed || in.Expired || in.Refunded || in.Disputed || in.DisputeResolved) {
		return fmt.Errorf("%w: contradictory manual-review transition", ErrPaymentEventInvalid)
	}
	return nil
}

func (in PaymentEventInput) validateIdentity() error {
	if in.Provider == "" || len(in.Provider) > 32 || in.EventKey == "" || in.EventType == "" || len(in.EventKey) > 255 || len(in.EventType) > 128 ||
		len(in.TradeNo) > 128 || len(in.ProviderOrderKey) > PaymentProviderAuthorityKeyMaxLength ||
		len(in.ProviderPaymentKey) > PaymentProviderAuthorityKeyMaxLength ||
		len(in.ProviderResourceKey) > PaymentProviderAuthorityKeyMaxLength ||
		in.ProviderCredentialGeneration < 0 || in.ProviderCreatedAt < 0 || len(in.ProviderState) > 64 || len(in.CustomerID) > 255 || len(in.Currency) > 8 || len(in.PaymentMethod) > 64 {
		return errors.New("invalid normalized payment event identity")
	}
	if len(in.NormalizedPayload) > maxNormalizedPaymentPayloadBytes {
		return errors.New("normalized payment event is too large")
	}
	if !paymentProviderUsesEnvironmentBinding(in.Provider) && in.ProviderLivemode != nil {
		return errors.New("provider livemode is only valid for environment-bound events")
	}
	return nil
}

func stripePaidEventModeAllowed(input PaymentEventInput) bool {
	if input.Provider != PaymentProviderStripe || !input.Paid {
		return true
	}
	if input.ProviderLivemode == nil {
		return false
	}
	return *input.ProviderLivemode || setting.StripeTestModeEnabled()
}

func stripeOrderPaidFulfillmentAllowed(order *PaymentOrder, input PaymentEventInput) bool {
	if input.Provider != PaymentProviderStripe || !input.Paid {
		return true
	}
	if order == nil || order.ProviderLivemode == nil {
		return false
	}
	if input.ProviderLivemode != nil && !paymentLivemodeEqual(order.ProviderLivemode, input.ProviderLivemode) {
		return false
	}
	return *order.ProviderLivemode || setting.StripeTestModeEnabled()
}

// ProcessPaymentEvent durably records and applies one verified provider event.
// Permanent contract mismatches are committed as manual-review state and
// returned with ErrPaymentManualReview so webhook handlers can acknowledge the
// provider without repeatedly applying an unsafe event. Transient failures are
// returned normally so the provider retries.
func ProcessPaymentEvent(input PaymentEventInput) (*PaymentSettlementResult, error) {
	return processPaymentEvent(input, 0)
}

// ProcessPaymentEventForTask applies a provider event only while the durable
// background task still owns its current lease and fencing token. The task row
// remains locked in the same transaction as event persistence, settlement and
// ledger writes, so a reclaimed stale worker cannot mutate the order.
func ProcessPaymentEventForTask(input PaymentEventInput, task *PaymentTask, runnerID string) (*PaymentSettlementResult, error) {
	return processPaymentEventWithTaskLease(input, 0, nil, task, runnerID)
}

func paymentEventFromInput(input PaymentEventInput, payloadDigest string) *PaymentEvent {
	return &PaymentEvent{
		Provider: input.Provider, EventKey: input.EventKey, EventType: input.EventType,
		TradeNo: input.TradeNo, ProviderOrderKey: input.ProviderOrderKey, ProviderPaymentKey: input.ProviderPaymentKey,
		ProviderResourceKey: input.ProviderResourceKey, ProviderCredentialGeneration: input.ProviderCredentialGeneration,
		ProviderLivemode:  input.ProviderLivemode,
		CustomerID:        input.CustomerID,
		ProviderCreatedAt: input.ProviderCreatedAt, ProviderState: input.ProviderState,
		PaidAmountMinor: input.PaidAmountMinor, RefundedAmountMinor: input.RefundedAmountMinor,
		DisputedAmountMinor: input.DisputedAmountMinor, Currency: input.Currency, PaymentMethod: input.PaymentMethod,
		Paid: input.Paid, Failed: input.Failed, Expired: input.Expired, Refunded: input.Refunded,
		Disputed: input.Disputed, DisputeResolved: input.DisputeResolved, DisputeWon: input.DisputeWon,
		PermanentFailure: input.PermanentFailure, ManualReview: input.ManualReview,
		PayloadDigest: payloadDigest, NormalizedPayload: input.NormalizedPayload,
	}
}

func processPaymentEvent(input PaymentEventInput, manualReplayEventID int64) (*PaymentSettlementResult, error) {
	return processPaymentEventWithReplayAttempts(input, manualReplayEventID, nil)
}

func processPaymentEventWithReplayAttempts(input PaymentEventInput, manualReplayEventID int64, expectedAttempts *int) (*PaymentSettlementResult, error) {
	return processPaymentEventWithTaskLease(input, manualReplayEventID, expectedAttempts, nil, "")
}

func processPaymentEventWithTaskLease(input PaymentEventInput, manualReplayEventID int64, expectedAttempts *int,
	task *PaymentTask, runnerID string) (*PaymentSettlementResult, error) {
	input.normalizeIdentity()
	if err := input.validate(); err != nil {
		return nil, err
	}
	result := &PaymentSettlementResult{}
	var postCommitErr error
	payloadDigest := PaymentPayloadDigest(input.NormalizedPayload)

	err := DB.Transaction(func(tx *gorm.DB) error {
		if task != nil {
			if err := assertPaymentTaskLeaseTx(tx, task, runnerID); err != nil {
				return err
			}
		}
		stripeCredentialTransition := input.Provider == PaymentProviderStripe &&
			(input.Paid || input.Failed || input.Expired || input.Refunded || input.Disputed || input.DisputeResolved || input.ManualReview)
		credentialFenced := input.Paid && (input.Provider == PaymentProviderEpay || input.Provider == PaymentProviderXorPay) ||
			stripeCredentialTransition
		if credentialFenced {
			if _, err := lockPaymentConfigurationFenceTx(tx); err != nil {
				return err
			}
		}
		event, created, err := UpsertPaymentEvent(tx, paymentEventFromInput(input, payloadDigest))
		if err != nil {
			return err
		}
		if !created {
			providerLivemodeConflict := event.ProviderLivemode != nil && input.ProviderLivemode != nil &&
				*event.ProviderLivemode != *input.ProviderLivemode
			if event.PayloadDigest != payloadDigest || providerLivemodeConflict {
				if event.PaymentOrderID > 0 {
					if err := tx.Model(&PaymentOrder{}).Where("id = ?", event.PaymentOrderID).Updates(map[string]interface{}{
						"status": PaymentOrderStatusManualReview, "status_reason": "event_key_payload_conflict",
						"updated_at": common.GetTimestamp(), "version": gorm.Expr("version + ?", 1),
					}).Error; err != nil {
						return err
					}
				}
				if err := finishPaymentEventWithReviewCodeTx(tx, event.ID, PaymentEventStatusManualReview,
					PaymentReviewCodeEventKeyPayloadConflict, "event key was reused with a different payload", event.PaymentOrderID); err != nil {
					return err
				}
				result.ManualReview = true
				postCommitErr = ErrPaymentEventConflict
				return nil
			}
			if event.ProviderLivemode == nil && input.ProviderLivemode != nil {
				if err := tx.Model(&PaymentEvent{}).Where("id = ? AND provider_livemode IS NULL", event.ID).
					Update("provider_livemode", *input.ProviderLivemode).Error; err != nil {
					return err
				}
				providerLivemode := *input.ProviderLivemode
				event.ProviderLivemode = &providerLivemode
			}
			if manualReplayEventID > 0 && event.ID == manualReplayEventID && expectedAttempts != nil {
				if event.Attempts == *expectedAttempts+1 {
					result.Duplicate = true
					result.ManualReview = event.Status == PaymentEventStatusManualReview
					if event.PaymentOrderID > 0 {
						var order PaymentOrder
						if err := tx.First(&order, event.PaymentOrderID).Error; err != nil {
							return err
						}
						result.Order = &order
						result.UserID = order.UserID
					}
					if result.ManualReview {
						postCommitErr = ErrPaymentManualReview
					}
					return nil
				}
				if event.Attempts != *expectedAttempts {
					return fmt.Errorf("%w: payment event attempts changed", ErrPaymentAuditConflict)
				}
			}
			manualReplay := manualReplayEventID > 0 && event.ID == manualReplayEventID &&
				event.Status == PaymentEventStatusManualReview && event.PaymentOrderID == 0
			if !manualReplay && (event.Status == PaymentEventStatusProcessed || event.Status == PaymentEventStatusManualReview ||
				event.Status == PaymentEventStatusDismissed || event.Status == PaymentEventStatusCredentialRevoked) {
				result.Duplicate = true
				result.ManualReview = event.Status == PaymentEventStatusManualReview
				if event.PaymentOrderID > 0 {
					var order PaymentOrder
					if err := tx.First(&order, event.PaymentOrderID).Error; err == nil {
						result.Order = &order
						result.UserID = order.UserID
					}
				}
				return nil
			}
		}
		if err := tx.Model(&PaymentEvent{}).Where("id = ?", event.ID).Updates(map[string]interface{}{
			"status":      PaymentEventStatusProcessing,
			"attempts":    gorm.Expr("attempts + ?", 1),
			"last_error":  "",
			"review_code": "",
			"updated_at":  common.GetTimestamp(),
		}).Error; err != nil {
			return err
		}

		// Unsupported Stripe event types are still acknowledged after signature
		// verification and inbox persistence; they carry no state transition.
		if !input.Paid && !input.Failed && !input.Expired && !input.Refunded && !input.Disputed && !input.DisputeResolved && !input.ManualReview {
			return finishPaymentEventTx(tx, event.ID, PaymentEventStatusProcessed, "", 0)
		}

		providerPaymentAuthority := paymentEventRequiresProviderPaymentAuthority(input)
		order, err := lockPaymentOrderForEventTx(tx, input)
		if !stripePaidEventModeAllowed(input) {
			reason := "stripe_test_mode_disabled"
			if input.ProviderLivemode == nil {
				reason = "stripe_payment_mode_unverified"
			}
			if err == nil {
				result.Order = order
				result.UserID = order.UserID
				return markPaymentManualReviewForOrderTx(tx, event, order, reason, ErrPaymentManualReview, result, &postCommitErr)
			}
			if errors.Is(err, ErrPaymentOrderNotFound) {
				if finishErr := finishPaymentEventTx(tx, event.ID, PaymentEventStatusManualReview, reason, 0); finishErr != nil {
					return finishErr
				}
				result.ManualReview = true
				postCommitErr = fmt.Errorf("%w: %s", ErrPaymentManualReview, reason)
				return nil
			}
			return err
		}
		if errors.Is(err, ErrPaymentOrderNotFound) && input.Paid && input.TradeNo != "" {
			adopted, adoptErr := adoptLegacyPaymentOrderTx(tx, input)
			if adoptErr == nil {
				order = &adopted
				err = nil
			} else if legacyPaymentAdoptionRequiresManualReview(adoptErr) {
				reason := "legacy payment adoption requires manual review: " + adoptErr.Error()
				reviewCode := ""
				if errors.Is(adoptErr, ErrLegacyTopUpQuotaSnapshotUnavailable) {
					reviewCode = PaymentReviewCodeLegacyTopUpQuotaSnapshotMissing
				} else if errors.Is(adoptErr, ErrLegacySubscriptionContractUnavailable) {
					reviewCode = PaymentReviewCodeLegacySubscriptionContractUnavailable
				}
				if finishErr := finishPaymentEventWithReviewCodeTx(tx, event.ID, PaymentEventStatusManualReview,
					reviewCode, reason, 0); finishErr != nil {
					return finishErr
				}
				result.ManualReview = true
				postCommitErr = fmt.Errorf("%w: %v", ErrPaymentManualReview, adoptErr)
				return nil
			} else {
				err = adoptErr
			}
		}
		if err != nil {
			if errors.Is(err, ErrPaymentOrderNotFound) {
				if providerPaymentAuthority && input.ProviderPaymentKey != "" {
					reason := "provider payment identity is not bound to an order yet"
					if finishErr := finishPaymentEventTx(tx, event.ID, PaymentEventStatusManualReview, reason, 0); finishErr != nil {
						return finishErr
					}
					result.ManualReview = true
					postCommitErr = fmt.Errorf("%w: %s", ErrPaymentManualReview, reason)
					return nil
				}
				if finishErr := finishPaymentEventTx(tx, event.ID, PaymentEventStatusFailed, "payment order not found", 0); finishErr != nil {
					return finishErr
				}
				postCommitErr = err
				return nil
			}
			return err
		}
		result.Order = order
		result.UserID = order.UserID
		if order.Provider != input.Provider {
			return markPaymentManualReviewForOrderTx(tx, event, order, "provider_mismatch", ErrPaymentProviderMismatch, result, &postCommitErr)
		}
		if providerPaymentAuthority && input.ProviderPaymentKey != "" && input.TradeNo != "" && input.TradeNo != order.TradeNo {
			// Refund and dispute metadata can be edited after the original payment.
			// The immutable provider payment identity selects the authoritative
			// order; conflicting metadata must never redirect a reversal to a
			// different trade number.
			return markPaymentManualReviewForOrderTx(tx, event, order, "provider_trade_identity_mismatch", ErrPaymentProviderMismatch, result, &postCommitErr)
		}
		environmentBoundStateTransition := input.Paid || input.Failed || input.Expired || input.Refunded ||
			input.Disputed || input.DisputeResolved || input.ManualReview
		if (order.Provider == PaymentProviderCreem || order.Provider == PaymentProviderWaffoPancake) && environmentBoundStateTransition {
			reasonPrefix := "creem"
			if order.Provider == PaymentProviderWaffoPancake {
				reasonPrefix = "waffo_pancake"
			}
			reason := ""
			switch {
			case order.ProviderLivemode == nil:
				reason = reasonPrefix + "_order_environment_unbound"
			case input.ProviderLivemode == nil:
				reason = reasonPrefix + "_payment_environment_unverified"
			case !paymentLivemodeEqual(order.ProviderLivemode, input.ProviderLivemode):
				reason = reasonPrefix + "_order_event_environment_mismatch"
			}
			if reason != "" {
				return markPaymentManualReviewForOrderTx(tx, event, order, reason, ErrPaymentManualReview, result, &postCommitErr)
			}
		}
		stripeEconomicReversal := order.Provider == PaymentProviderStripe && providerPaymentAuthority
		if stripeEconomicReversal && input.ProviderLivemode != nil {
			if order.ProviderLivemode == nil {
				if err := tx.Model(&PaymentOrder{}).Where("id = ? AND provider_livemode IS NULL", order.ID).
					Update("provider_livemode", *input.ProviderLivemode).Error; err != nil {
					return err
				}
				order.ProviderLivemode = copyPaymentLivemode(input.ProviderLivemode)
			} else if !paymentLivemodeEqual(order.ProviderLivemode, input.ProviderLivemode) {
				return markPaymentManualReviewForOrderTx(tx, event, order, "stripe_order_event_mode_mismatch", ErrPaymentManualReview, result, &postCommitErr)
			}
		}
		if !stripeOrderPaidFulfillmentAllowed(order, input) {
			return markPaymentManualReviewForOrderTx(tx, event, order, "stripe_order_event_mode_mismatch", ErrPaymentManualReview, result, &postCommitErr)
		}
		if credentialFenced {
			if input.ProviderCredentialGeneration <= 0 {
				return markPaymentManualReviewForOrderTx(tx, event, order, "payment_credential_generation_missing", ErrPaymentManualReview, result, &postCommitErr)
			}
			if order.ProviderCredentialGeneration == 0 {
				if err := tx.Model(&PaymentOrder{}).Where("id = ? AND provider_credential_generation = 0", order.ID).
					Update("provider_credential_generation", input.ProviderCredentialGeneration).Error; err != nil {
					return err
				}
				order.ProviderCredentialGeneration = input.ProviderCredentialGeneration
			}
			generationForAvailability := order.ProviderCredentialGeneration
			if order.Provider == PaymentProviderStripe {
				generationForAvailability = input.ProviderCredentialGeneration
			}
			available, err := paymentCredentialGenerationAvailableTx(tx, order.Provider,
				generationForAvailability, order.CreatedAt, common.GetTimestamp())
			if err != nil {
				return err
			}
			generationMatches := order.ProviderCredentialGeneration == input.ProviderCredentialGeneration
			if order.Provider == PaymentProviderStripe {
				// Stripe's current endpoint secret signs lifecycle events for both
				// old and new Checkout orders. The previous secret is constrained by
				// the order creation cutoff inside paymentCredentialGenerationAvailableTx.
				generationMatches = true
			}
			if !generationMatches || !available {
				return markPaymentManualReviewForOrderTx(tx, event, order, "payment_credential_generation_revoked", ErrPaymentManualReview, result, &postCommitErr)
			}
		}
		if err := bindAndValidateProviderIdentityTx(tx, order, input); err != nil {
			return markPaymentManualReviewForOrderTx(tx, event, order, "provider_order_identity_mismatch", err, result, &postCommitErr)
		}
		if input.ManualReview {
			return markPaymentManualReviewForOrderTx(tx, event, order, "provider_reported_manual_review", ErrPaymentManualReview, result, &postCommitErr)
		}
		staleProviderState, providerStateConflict, err := evaluateProviderStateOrderTx(tx, event, order, input)
		if err != nil {
			return err
		}
		if providerStateConflict {
			return markPaymentManualReviewForOrderTx(tx, event, order, "provider_state_order_conflict", ErrPaymentManualReview, result, &postCommitErr)
		}
		if staleProviderState {
			// A stale provider state is terminal but must not join the set of
			// economically applied events. This is especially important when two
			// Stripe dispute states share the same second-level timestamp: a later
			// inbox ID must never make the ignored state win aggregation.
			return finishPaymentEventTx(tx, event.ID, PaymentEventStatusDismissed, "stale provider state ignored", order.ID)
		}

		if input.Paid {
			terminalAdminDecision, err := paymentOrderHasTerminalAdminDecisionTx(tx, order.ID)
			if err != nil {
				return err
			}
			if terminalAdminDecision {
				return markPaymentManualReviewPreservingProjectionTx(tx, event, order,
					"paid_after_admin_reject_or_void", errPaymentTerminalAdminDecision, result, &postCommitErr)
			}
			if input.PaidAmountMinor != order.ExpectedAmountMinor {
				return markPaymentManualReviewForOrderTx(tx, event, order, "paid_amount_mismatch", ErrPaymentAmountMismatch, result, &postCommitErr)
			}
			if !strings.EqualFold(strings.TrimSpace(input.Currency), strings.TrimSpace(order.Currency)) {
				return markPaymentManualReviewForOrderTx(tx, event, order, "payment_currency_mismatch", ErrPaymentCurrencyMismatch, result, &postCommitErr)
			}
			if input.PaymentMethod != "" && order.PaymentMethod != input.PaymentMethod {
				return markPaymentManualReviewForOrderTx(tx, event, order, "payment_method_mismatch", ErrPaymentProviderMismatch, result, &postCommitErr)
			}
			customerBound, err := bindStripeCustomerTx(tx, order, input.CustomerID)
			if err != nil {
				return markPaymentManualReviewForOrderTx(tx, event, order, "stripe_customer_identity_mismatch", err, result, &postCommitErr)
			}
			result.UserCacheChanged = result.UserCacheChanged || customerBound
			orderBeforeFulfillment := *order
			resultBeforeFulfillment := clonePaymentSettlementResult(result)
			const fulfillmentSavepoint = "payment_entitlement_fulfillment"
			if err := tx.SavePoint(fulfillmentSavepoint).Error; err != nil {
				return err
			}
			if err := fulfillPaymentOrderTx(tx, event, order, input, result); err != nil {
				if errors.Is(err, ErrSubscriptionPurchaseLimit) || errors.Is(err, ErrSubscriptionOrderSnapshotMissing) ||
					errors.Is(err, ErrPaymentManualReview) || errors.Is(err, ErrTopUpStatusInvalid) ||
					errors.Is(err, ErrPaymentProviderMismatch) || errors.Is(err, ErrPaymentMethodMismatch) ||
					errors.Is(err, ErrQuotaOverflow) || errors.Is(err, ErrBillingAccountNotFound) ||
					errors.Is(err, gorm.ErrRecordNotFound) {
					if rollbackErr := rollbackPaymentSettlementSavepoint(tx, fulfillmentSavepoint, order, orderBeforeFulfillment,
						result, resultBeforeFulfillment); rollbackErr != nil {
						return rollbackErr
					}
					return markPaymentManualReviewForOrderTx(tx, event, order, "entitlement_fulfillment_conflict", err, result, &postCommitErr)
				}
				return err
			}
		}

		if input.Refunded || input.Disputed || input.DisputeResolved {
			orderBeforeReversal := *order
			resultBeforeReversal := clonePaymentSettlementResult(result)
			const reversalSavepoint = "payment_entitlement_reversal"
			if err := tx.SavePoint(reversalSavepoint).Error; err != nil {
				return err
			}
			if err := reconcilePaymentReversalTx(tx, event, order, input, result); err != nil {
				if errors.Is(err, ErrPaymentAmountMismatch) || errors.Is(err, ErrPaymentCurrencyMismatch) ||
					errors.Is(err, ErrPaymentManualReview) || errors.Is(err, ErrQuotaOverflow) ||
					errors.Is(err, ErrBillingAccountNotFound) || errors.Is(err, ErrTopUpStatusInvalid) ||
					errors.Is(err, ErrPaymentProviderMismatch) || errors.Is(err, gorm.ErrRecordNotFound) {
					if rollbackErr := rollbackPaymentSettlementSavepoint(tx, reversalSavepoint, order, orderBeforeReversal,
						result, resultBeforeReversal); rollbackErr != nil {
						return rollbackErr
					}
					return markPaymentManualReviewForOrderTx(tx, event, order, "reversal_contract_mismatch", err, result, &postCommitErr)
				}
				return err
			}
		}

		if input.Failed || input.Expired {
			if err := failOrExpirePaymentOrderTx(tx, order, input); err != nil {
				return err
			}
		}
		return finishPaymentEventTx(tx, event.ID, PaymentEventStatusProcessed, "", order.ID)
	})
	if err != nil {
		return nil, err
	}
	if result.UserCacheChanged && result.UserID > 0 {
		if err := InvalidateUserCache(result.UserID); err != nil {
			common.SysLog("failed to invalidate payment user cache: " + err.Error())
		}
	}
	for _, userID := range result.CacheUserIDs {
		if userID <= 0 || userID == result.UserID {
			continue
		}
		if err := InvalidateUserCache(userID); err != nil {
			common.SysLog("failed to invalidate payment-related user cache: " + err.Error())
		}
	}
	if result.GroupCacheValue != "" && result.UserID > 0 {
		_ = UpdateUserGroupCache(result.UserID, result.GroupCacheValue)
	}
	if result.AffiliateUserID > 0 && result.AffiliateReward > 0 {
		recordAffiliateTopUpRewardLog(result.AffiliateUserID, result.AffiliateReward)
	}
	if postCommitErr == nil && input.Paid && strings.TrimSpace(input.ProviderPaymentKey) != "" {
		if err := replayUnmatchedPaymentReversals(input.Provider, input.ProviderPaymentKey); err != nil {
			return result, err
		}
	}
	return result, postCommitErr
}

func legacyPaymentAdoptionRequiresManualReview(err error) bool {
	return errors.Is(err, ErrLegacyPaymentContractUnavailable) || errors.Is(err, ErrPaymentManualReview) ||
		errors.Is(err, ErrPaymentAmountMismatch) || errors.Is(err, ErrPaymentCurrencyMismatch) ||
		errors.Is(err, ErrPaymentProviderMismatch) || errors.Is(err, ErrPaymentMethodMismatch)
}

func clonePaymentSettlementResult(result *PaymentSettlementResult) PaymentSettlementResult {
	if result == nil {
		return PaymentSettlementResult{}
	}
	cloned := *result
	cloned.CacheUserIDs = append([]int(nil), result.CacheUserIDs...)
	return cloned
}

func rollbackPaymentSettlementSavepoint(tx *gorm.DB, savepoint string, order *PaymentOrder, orderBefore PaymentOrder,
	result *PaymentSettlementResult, resultBefore PaymentSettlementResult) error {
	if err := tx.RollbackTo(savepoint).Error; err != nil {
		return fmt.Errorf("rollback payment settlement savepoint %s: %w", savepoint, err)
	}
	*order = orderBefore
	*result = resultBefore
	result.Order = order
	return nil
}

// RecordPaymentEventReceived persists the normalized facts immediately after
// signature verification and before any provider API confirmation. It checks
// only bounded identity fields so even a contradictory but signed event has a
// durable inbox record for later manual review.
func RecordPaymentEventReceived(input PaymentEventInput) error {
	input.normalizeIdentity()
	if err := input.validateIdentity(); err != nil {
		return err
	}
	payloadDigest := PaymentPayloadDigest(input.NormalizedPayload)
	return DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := UpsertPaymentEvent(tx, paymentEventFromInput(input, payloadDigest))
		return err
	})
}

// MarkPaymentEventValidationFailed keeps a retryable inbox item when a
// provider authority confirmation cannot complete. Terminal events are never
// downgraded by a delayed duplicate delivery.
func MarkPaymentEventValidationFailed(provider, eventKey, reasonCode string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	eventKey = strings.TrimSpace(eventKey)
	reasonCode = strings.TrimSpace(reasonCode)
	if provider == "" || len(provider) > 32 || eventKey == "" || len(eventKey) > 255 || reasonCode == "" || len(reasonCode) > 1024 {
		return errors.New("invalid payment event validation failure")
	}
	result := DB.Model(&PaymentEvent{}).
		Where("provider = ? AND event_key = ? AND status IN ?", provider, eventKey,
			[]string{PaymentEventStatusReceived, PaymentEventStatusProcessing, PaymentEventStatusFailed}).
		Updates(map[string]interface{}{
			"status": PaymentEventStatusFailed, "review_code": "", "last_error": reasonCode,
			"attempts": gorm.Expr("attempts + ?", 1), "updated_at": common.GetTimestamp(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	var count int64
	if err := DB.Model(&PaymentEvent{}).Where("provider = ? AND event_key = ?", provider, eventKey).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return errors.New("payment event not found")
	}
	return nil
}

// RecordPaymentEventManualReview durably retains a verified provider event
// that cannot be safely mapped to an order. Webhook handlers may acknowledge
// the provider only after this function commits.
func RecordPaymentEventManualReview(input PaymentEventInput, reason string) error {
	input.normalizeIdentity()
	if err := input.validateIdentity(); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "verified payment event could not be mapped safely"
	}
	if len(reason) > 1024 {
		reason = reason[:1024]
	}
	payloadDigest := PaymentPayloadDigest(input.NormalizedPayload)
	return DB.Transaction(func(tx *gorm.DB) error {
		event, created, err := UpsertPaymentEvent(tx, paymentEventFromInput(input, payloadDigest))
		if err != nil {
			return err
		}
		if !created && (event.Status == PaymentEventStatusProcessed || event.Status == PaymentEventStatusDismissed ||
			event.Status == PaymentEventStatusCredentialRevoked) {
			return nil
		}
		reviewCode := event.ReviewCode
		if created {
			reviewCode = ""
		}
		if !created && event.ReviewCode == PaymentReviewCodeEventKeyPayloadConflict {
			if strings.TrimSpace(event.LastError) != "" {
				reason = event.LastError
			} else {
				reason = "event key was reused with a different payload"
			}
		} else if !created && (event.PayloadDigest != payloadDigest ||
			event.ProviderLivemode != nil && input.ProviderLivemode != nil && *event.ProviderLivemode != *input.ProviderLivemode) {
			reason = "event key was reused with a different payload"
			reviewCode = PaymentReviewCodeEventKeyPayloadConflict
		}
		updates := map[string]interface{}{
			"status": PaymentEventStatusManualReview, "review_code": reviewCode, "last_error": reason,
			"attempts": gorm.Expr("attempts + ?", 1), "updated_at": common.GetTimestamp(),
		}
		if event.ProviderLivemode == nil && input.ProviderLivemode != nil {
			updates["provider_livemode"] = *input.ProviderLivemode
		}
		return tx.Model(&PaymentEvent{}).Where("id = ?", event.ID).Updates(updates).Error
	})
}

func replayUnmatchedPaymentReversals(provider, providerPaymentKey string) error {
	provider = strings.TrimSpace(provider)
	providerPaymentKey = strings.TrimSpace(providerPaymentKey)
	if provider == "" || providerPaymentKey == "" {
		return nil
	}
	var events []PaymentEvent
	if err := DB.Where("provider = ? AND provider_payment_key = ? AND payment_order_id = ? AND status = ?",
		provider, providerPaymentKey, 0, PaymentEventStatusManualReview).
		Where("refunded = ? OR disputed = ? OR dispute_resolved = ?", true, true, true).
		Order("created_at asc, id asc").Find(&events).Error; err != nil {
		return err
	}
	for _, event := range events {
		_, err := processPaymentEvent(PaymentEventInput{
			Provider: event.Provider, EventKey: event.EventKey, EventType: event.EventType, TradeNo: event.TradeNo,
			ProviderOrderKey: event.ProviderOrderKey, ProviderPaymentKey: event.ProviderPaymentKey,
			ProviderResourceKey: event.ProviderResourceKey, ProviderCredentialGeneration: event.ProviderCredentialGeneration,
			ProviderLivemode:  event.ProviderLivemode,
			ProviderCreatedAt: event.ProviderCreatedAt, ProviderState: event.ProviderState,
			CustomerID:      event.CustomerID,
			PaidAmountMinor: event.PaidAmountMinor, RefundedAmountMinor: event.RefundedAmountMinor,
			DisputedAmountMinor: event.DisputedAmountMinor, Currency: event.Currency, PaymentMethod: event.PaymentMethod,
			Paid: event.Paid, Failed: event.Failed, Expired: event.Expired, Refunded: event.Refunded,
			Disputed: event.Disputed, DisputeResolved: event.DisputeResolved, DisputeWon: event.DisputeWon,
			PermanentFailure: event.PermanentFailure, ManualReview: event.ManualReview, NormalizedPayload: event.NormalizedPayload,
		}, event.ID)
		if err != nil && !errors.Is(err, ErrPaymentManualReview) && !errors.Is(err, ErrPaymentEventConflict) {
			return err
		}
	}
	return nil
}

func lockPaymentOrderForEventTx(tx *gorm.DB, input PaymentEventInput) (*PaymentOrder, error) {
	var order PaymentOrder
	query := lockForUpdate(tx)
	var err error
	providerPaymentKey := strings.TrimSpace(input.ProviderPaymentKey)
	if paymentEventRequiresProviderPaymentAuthority(input) && providerPaymentKey != "" {
		err = query.Where("provider = ? AND provider_payment_key = ?", input.Provider, providerPaymentKey).First(&order).Error
	} else if strings.TrimSpace(input.TradeNo) != "" {
		err = query.Where("trade_no = ?", strings.TrimSpace(input.TradeNo)).First(&order).Error
	} else if providerPaymentKey != "" {
		err = query.Where("provider = ? AND provider_payment_key = ?", input.Provider, providerPaymentKey).First(&order).Error
	} else {
		return nil, ErrPaymentOrderNotFound
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPaymentOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func paymentEventRequiresProviderPaymentAuthority(input PaymentEventInput) bool {
	if input.Refunded || input.Disputed || input.DisputeResolved {
		return true
	}
	if input.Provider != PaymentProviderStripe {
		return false
	}
	switch input.EventType {
	case "charge.refunded", "charge.dispute.created", "charge.dispute.closed":
		// Incompatible Stripe API-version events deliberately clear their
		// economic flags and enter manual review. They still carry a trustworthy
		// PaymentIntent/Charge identity and untrusted mutable metadata, so order
		// selection must retain the same authority rule as compatible events.
		return true
	default:
		return false
	}
}

func bindAndValidateProviderIdentityTx(tx *gorm.DB, order *PaymentOrder, input PaymentEventInput) error {
	if tx == nil || order == nil || order.ID <= 0 {
		return ErrPaymentProviderMismatch
	}
	updates := map[string]interface{}{}
	providerOrderKey := strings.TrimSpace(input.ProviderOrderKey)
	if providerOrderKey != "" {
		if order.ProviderOrderKey != nil && *order.ProviderOrderKey != providerOrderKey {
			return ErrPaymentProviderMismatch
		}
		if order.ProviderOrderKey == nil {
			updates["provider_order_key"] = providerOrderKey
		}
	}
	providerPaymentKey := strings.TrimSpace(input.ProviderPaymentKey)
	if providerPaymentKey != "" {
		if order.ProviderPaymentKey != nil && *order.ProviderPaymentKey != providerPaymentKey {
			return ErrPaymentProviderMismatch
		}
		if order.ProviderPaymentKey == nil {
			updates["provider_payment_key"] = providerPaymentKey
		}
	}
	if len(updates) == 0 {
		return nil
	}
	conflictQuery := tx.Model(&PaymentOrder{}).Select("id").Where("id <> ?", order.ID)
	switch {
	case providerOrderKey != "" && providerPaymentKey != "":
		conflictQuery = conflictQuery.Where("provider_order_key = ? OR provider_payment_key = ?", providerOrderKey, providerPaymentKey)
	case providerOrderKey != "":
		conflictQuery = conflictQuery.Where("provider_order_key = ?", providerOrderKey)
	case providerPaymentKey != "":
		conflictQuery = conflictQuery.Where("provider_payment_key = ?", providerPaymentKey)
	}
	var conflictingOrder PaymentOrder
	conflictResult := conflictQuery.Limit(1).Find(&conflictingOrder)
	if conflictResult.Error != nil {
		return conflictResult.Error
	}
	if conflictResult.RowsAffected > 0 {
		return ErrPaymentProviderMismatch
	}

	// PostgreSQL marks a transaction failed after a unique-index violation.
	// Isolate the authority binding behind a savepoint so a concurrent claim by
	// another order can be normalized into a durable manual-review outcome
	// instead of forcing the webhook into an endless 500 retry loop.
	const savepoint = "payment_provider_identity_binding"
	if err := tx.SavePoint(savepoint).Error; err != nil {
		return err
	}
	updates["updated_at"] = common.GetTimestamp()
	if err := tx.Model(&PaymentOrder{}).Where("id = ?", order.ID).Updates(updates).Error; err != nil {
		if rollbackErr := tx.RollbackTo(savepoint).Error; rollbackErr != nil {
			return fmt.Errorf("bind provider identity: %w; rollback savepoint: %v", err, rollbackErr)
		}
		conflictResult = conflictQuery.Limit(1).Find(&conflictingOrder)
		if conflictResult.Error != nil {
			return conflictResult.Error
		}
		if conflictResult.RowsAffected > 0 {
			return ErrPaymentProviderMismatch
		}
		return err
	}
	if _, exists := updates["provider_order_key"]; exists {
		order.ProviderOrderKey = &providerOrderKey
	}
	if _, exists := updates["provider_payment_key"]; exists {
		order.ProviderPaymentKey = &providerPaymentKey
	}
	return nil
}

func bindStripeCustomerTx(tx *gorm.DB, order *PaymentOrder, customerID string) (bool, error) {
	customerID = strings.TrimSpace(customerID)
	if tx == nil || order == nil || order.Provider != PaymentProviderStripe || customerID == "" {
		return false, nil
	}
	if !strings.HasPrefix(customerID, "cus_") || len(customerID) > 64 {
		return false, ErrPaymentProviderMismatch
	}
	var user User
	if err := lockForUpdate(tx).Select("id", "stripe_customer").Where("id = ?", order.UserID).First(&user).Error; err != nil {
		return false, err
	}
	retiredCustomer, err := retiredStripeCustomerAllowedForOrderTx(tx, order, customerID)
	if err != nil {
		return false, err
	}
	if retiredCustomer {
		// A retired binding remains valid evidence for payment intents created
		// before retirement, but it must never become the user's active checkout
		// customer again.
		return false, nil
	}
	current := strings.TrimSpace(user.StripeCustomer)
	if current != "" && current != customerID {
		return false, ErrPaymentProviderMismatch
	}

	// Preserve compatibility with users bound before the ownership table was
	// introduced. This read is intentionally not FOR UPDATE; concurrent new
	// bindings are serialized by PaymentCustomerBinding's unique constraints.
	var otherUser User
	otherQuery := tx.Select("id").
		Where("stripe_customer = ? AND id <> ?", customerID, order.UserID).
		Limit(1).
		Find(&otherUser)
	if otherQuery.Error != nil {
		return false, otherQuery.Error
	}
	if otherQuery.RowsAffected > 0 {
		return false, ErrPaymentProviderMismatch
	}

	binding := PaymentCustomerBinding{
		Provider:    PaymentProviderStripe,
		CustomerKey: customerID,
		UserID:      order.UserID,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&binding).Error; err != nil {
		return false, err
	}

	var userBinding PaymentCustomerBinding
	userBindingQuery := lockForUpdate(tx).
		Where("provider = ? AND user_id = ?", PaymentProviderStripe, order.UserID).
		Limit(1).
		Find(&userBinding)
	if userBindingQuery.Error != nil {
		return false, userBindingQuery.Error
	}
	if userBindingQuery.RowsAffected > 0 && userBinding.CustomerKey != customerID {
		return false, ErrPaymentProviderMismatch
	}
	var customerBinding PaymentCustomerBinding
	customerBindingQuery := lockForUpdate(tx).
		Where("provider = ? AND customer_key = ?", PaymentProviderStripe, customerID).
		Limit(1).
		Find(&customerBinding)
	if customerBindingQuery.Error != nil {
		return false, customerBindingQuery.Error
	}
	if customerBindingQuery.RowsAffected > 0 && customerBinding.UserID != order.UserID {
		return false, ErrPaymentProviderMismatch
	}
	if userBindingQuery.RowsAffected == 0 || customerBindingQuery.RowsAffected == 0 {
		return false, errors.New("stripe customer ownership binding was not persisted")
	}
	if current == customerID {
		return false, nil
	}
	if err := tx.Model(&User{}).Where("id = ? AND (stripe_customer = ? OR stripe_customer IS NULL)", order.UserID, "").
		Update("stripe_customer", customerID).Error; err != nil {
		return false, err
	}
	return true, nil
}

func retiredStripeCustomerAllowedForOrderTx(tx *gorm.DB, order *PaymentOrder, customerID string) (bool, error) {
	if tx == nil || order == nil || customerID == "" || !tx.Migrator().HasTable(&PaymentCustomerBindingRetirement{}) {
		return false, nil
	}
	var conflictingCount int64
	if err := tx.Model(&PaymentCustomerBindingRetirement{}).
		Where("provider = ? AND customer_key = ? AND user_id <> ?", PaymentProviderStripe, customerID, order.UserID).
		Count(&conflictingCount).Error; err != nil {
		return false, err
	}
	if conflictingCount > 0 {
		return false, ErrPaymentProviderMismatch
	}
	baseQuery := tx.Model(&PaymentCustomerBindingRetirement{}).
		Where("provider = ? AND customer_key = ? AND user_id = ?", PaymentProviderStripe, customerID, order.UserID)
	var retiredCount int64
	if err := baseQuery.Count(&retiredCount).Error; err != nil {
		return false, err
	}
	if retiredCount == 0 {
		return false, nil
	}
	if order.CreatedAt <= 0 {
		return false, ErrPaymentProviderMismatch
	}
	var historicalCount int64
	if err := baseQuery.Where("retired_at >= ?", order.CreatedAt).Count(&historicalCount).Error; err != nil {
		return false, err
	}
	if historicalCount == 0 {
		// Retirement is terminal for the customer key. A later order must never
		// reactivate it merely because the active-binding slot is currently empty.
		return false, ErrPaymentProviderMismatch
	}
	return true, nil
}

func fulfillPaymentOrderTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder, input PaymentEventInput, result *PaymentSettlementResult) error {
	if !stripeOrderPaidFulfillmentAllowed(order, input) {
		return ErrPaymentManualReview
	}
	if order.Status == PaymentOrderStatusFulfilled || order.Status == PaymentOrderStatusRefunded || order.Status == PaymentOrderStatusDisputed || order.Status == PaymentOrderStatusDebt {
		return nil
	}
	if order.Status == PaymentOrderStatusManualReview {
		return ErrPaymentManualReview
	}
	if event.Provider != "admin" {
		terminalAdminDecision, err := paymentOrderHasTerminalAdminDecisionTx(tx, order.ID)
		if err != nil {
			return err
		}
		if terminalAdminDecision {
			return ErrPaymentManualReview
		}
	}
	if order.CreditQuota < 0 || order.CreditQuota > math.MaxInt32 {
		return errors.New("payment credit quota is outside the supported range")
	}
	now := common.GetTimestamp()
	switch order.OrderKind {
	case PaymentOrderKindTopUp:
		if order.CreditQuota <= 0 {
			return errors.New("payment top-up quota is invalid")
		}
		var topUp TopUp
		if err := lockForUpdate(tx).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).First(&topUp).Error; err != nil {
			return err
		}
		if topUp.PaymentProvider != order.Provider || topUp.UserId != order.UserID {
			return ErrPaymentProviderMismatch
		}
		if topUp.Status != common.TopUpStatusPending && topUp.Status != common.TopUpStatusManualReview &&
			topUp.Status != common.TopUpStatusFailed && topUp.Status != common.TopUpStatusExpired &&
			topUp.Status != common.TopUpStatusSuccess {
			return ErrTopUpStatusInvalid
		}
		if topUp.Status == common.TopUpStatusSuccess {
			return ErrPaymentManualReview
		}
		if topUp.Status == common.TopUpStatusPending || topUp.Status == common.TopUpStatusManualReview ||
			topUp.Status == common.TopUpStatusFailed || topUp.Status == common.TopUpStatusExpired {
			quota := int(order.CreditQuota)
			inviterID, reward, err := applyAffiliateTopUpRewardTx(tx, &topUp, quota)
			if err != nil {
				return err
			}
			if err := applyUserQuotaDeltaTx(tx, order.UserID, quota); err != nil {
				return err
			}
			topUp.Status = common.TopUpStatusSuccess
			topUp.CompleteTime = now
			topUp.PaymentOrderId = &order.ID
			if err := tx.Save(&topUp).Error; err != nil {
				return err
			}
			result.QuotaDelta += quota
			result.AffiliateUserID = inviterID
			result.AffiliateReward = reward
			result.UserCacheChanged = true
		}
		if err := createPaymentLedgerEntryTx(tx, event, order, PaymentLedgerEntryCredit, order.ExpectedAmountMinor, order.CreditQuota, "top-up credit"); err != nil {
			return err
		}
	case PaymentOrderKindSubscription:
		var snapshot SubscriptionPlanSnapshot
		if err := common.UnmarshalJsonStr(order.ProductSnapshot, &snapshot); err != nil {
			return ErrSubscriptionOrderSnapshotMissing
		}
		plan, err := snapshot.SubscriptionPlan()
		if err != nil {
			return err
		}
		subscription, err := CreateUserSubscriptionFromPaymentOrderTx(tx, order.UserID, plan, "payment_order", order.ID)
		if err != nil {
			return err
		}
		subscription.PaymentOrderId = &order.ID
		if err := tx.Model(subscription).Update("payment_order_id", order.ID).Error; err != nil {
			return err
		}
		legacyOrder, err := ensureSubscriptionPaymentProjectionTx(tx, order)
		if err != nil {
			return err
		}
		legacyOrder.Status = common.TopUpStatusSuccess
		legacyOrder.CompleteTime = now
		legacyOrder.ProviderOrderId = input.ProviderOrderKey
		legacyOrder.PaymentOrderId = &order.ID
		if err := tx.Save(legacyOrder).Error; err != nil {
			return err
		}
		if err := upsertSubscriptionTopUpTx(tx, legacyOrder); err != nil {
			return err
		}
		if err := createPaymentLedgerEntryTx(tx, event, order, PaymentLedgerEntrySubscriptionGranted, order.ExpectedAmountMinor, 0, "subscription entitlement granted"); err != nil {
			return err
		}
		result.GroupCacheValue = strings.TrimSpace(plan.UpgradeGroup)
	default:
		return errors.New("unsupported payment order kind")
	}

	order.Status = PaymentOrderStatusFulfilled
	order.StatusReason = ""
	order.PaidAmountMinor = input.PaidAmountMinor
	order.SettledAt = now
	order.UpdatedAt = now
	order.Version++
	if err := tx.Model(&PaymentOrder{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
		"status":                           order.Status,
		"status_reason":                    "",
		"paid_amount_minor":                order.PaidAmountMinor,
		"settled_at":                       order.SettledAt,
		"provider_order_key":               order.ProviderOrderKey,
		"provider_payment_key":             order.ProviderPaymentKey,
		"start_payload":                    "",
		"browser_authorization_digest":     nil,
		"browser_authorization_payload":    "",
		"browser_authorization_expires_at": 0,
		"browser_authorized_at":            0,
		"updated_at":                       order.UpdatedAt,
		"version":                          order.Version,
	}).Error; err != nil {
		return err
	}
	if err := settlePaymentLimitReservationAtTx(tx, order, paymentLimitPaidAtForEvent(event, order, now), now); err != nil {
		return err
	}
	return tx.Model(&PaymentEvent{}).Where("id = ?", event.ID).Update("payment_order_id", order.ID).Error
}

func paymentOrderHasTerminalAdminDecisionTx(tx *gorm.DB, orderID int64) (bool, error) {
	if tx == nil || orderID <= 0 {
		return false, errors.New("invalid payment administrator decision lookup")
	}
	var count int64
	err := tx.Model(&PaymentEvent{}).
		Where("payment_order_id = ? AND provider = ? AND status = ? AND event_type IN ?", orderID, "admin", PaymentEventStatusProcessed,
			[]string{"payment.admin_reject", "payment.admin_void"}).
		Count(&count).Error
	return count > 0, err
}

func ensureSubscriptionPaymentProjectionTx(tx *gorm.DB, order *PaymentOrder) (*SubscriptionOrder, error) {
	var legacy SubscriptionOrder
	err := lockForUpdate(tx).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).First(&legacy).Error
	if err == nil {
		return &legacy, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var snapshot SubscriptionPlanSnapshot
	if err := common.UnmarshalJsonStr(order.ProductSnapshot, &snapshot); err != nil {
		return nil, ErrSubscriptionOrderSnapshotMissing
	}
	legacy = SubscriptionOrder{
		PaymentOrderId:      &order.ID,
		UserId:              order.UserID,
		PlanId:              snapshot.PlanId,
		Money:               float64(order.ExpectedAmountMinor) / float64(common.PaymentProviderCurrencyMinorUnit(order.Provider, order.Currency)),
		PlanSnapshot:        order.ProductSnapshot,
		ExpectedAmountMinor: order.ExpectedAmountMinor,
		PaymentCurrency:     order.Currency,
		ReserveUntil:        order.ExpiresAt,
		TradeNo:             order.TradeNo,
		PaymentMethod:       order.PaymentMethod,
		PaymentProvider:     order.Provider,
		Status:              common.TopUpStatusPending,
		CreateTime:          order.CreatedAt,
	}
	if err := tx.Create(&legacy).Error; err != nil {
		return nil, err
	}
	return &legacy, nil
}

func failOrExpirePaymentOrderTx(tx *gorm.DB, order *PaymentOrder, input PaymentEventInput) error {
	if order.Status == PaymentOrderStatusFulfilled || order.Status == PaymentOrderStatusRefunded || order.Status == PaymentOrderStatusDisputed || order.Status == PaymentOrderStatusDebt || order.Status == PaymentOrderStatusManualReview {
		return nil
	}
	status := PaymentOrderStatusFailed
	legacyStatus := common.TopUpStatusFailed
	reason := "provider reported payment failure"
	if input.Expired {
		status = PaymentOrderStatusExpired
		legacyStatus = common.TopUpStatusExpired
		reason = "provider payment session expired"
	}
	now := common.GetTimestamp()
	if err := tx.Model(&PaymentOrder{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
		"status": status, "status_reason": reason,
		"start_flow": "", "start_payload": "", "browser_authorization_digest": nil,
		"browser_authorization_payload": "", "browser_authorization_expires_at": 0,
		"browser_authorized_at": 0, "updated_at": now, "version": gorm.Expr("version + ?", 1),
	}).Error; err != nil {
		return err
	}
	order.Status = status
	order.StatusReason = reason
	order.StartFlow = ""
	order.StartPayload = ""
	order.BrowserAuthorizationDigest = nil
	order.BrowserAuthorizationPayload = ""
	order.BrowserAuthorizationExpiresAt = 0
	order.BrowserAuthorizedAt = 0
	order.UpdatedAt = now
	order.Version++
	if err := releasePaymentLimitReservationTx(tx, order, now); err != nil {
		return err
	}
	if order.OrderKind == PaymentOrderKindTopUp {
		return tx.Model(&TopUp{}).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
			Where("status = ?", common.TopUpStatusPending).Updates(map[string]interface{}{"status": legacyStatus, "complete_time": now}).Error
	}
	return tx.Model(&SubscriptionOrder{}).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
		Where("status = ?", common.TopUpStatusPending).Updates(map[string]interface{}{"status": legacyStatus, "complete_time": now}).Error
}

// evaluateProviderStateOrderTx prevents a delayed non-terminal provider event
// from rolling back a newer terminal decision for the same provider object.
// Equal-timestamp terminal conflicts are escalated instead of guessing an
// ordering that the provider did not supply.
func evaluateProviderStateOrderTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder, input PaymentEventInput) (bool, bool, error) {
	if tx == nil || event == nil || order == nil || (!input.Disputed && !input.DisputeResolved) ||
		strings.TrimSpace(input.ProviderResourceKey) == "" || input.ProviderCreatedAt <= 0 {
		return false, false, nil
	}
	var latest PaymentEvent
	err := tx.Where("provider = ? AND provider_resource_key = ? AND payment_order_id = ? AND id <> ? AND status = ?",
		input.Provider, input.ProviderResourceKey, order.ID, event.ID, PaymentEventStatusProcessed).
		Order("provider_created_at desc, id desc").First(&latest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if latest.ProviderCreatedAt > input.ProviderCreatedAt {
		return true, false, nil
	}
	if latest.ProviderCreatedAt < input.ProviderCreatedAt {
		return false, false, nil
	}
	if latest.ProviderState == input.ProviderState {
		return true, false, nil
	}
	if latest.DisputeResolved && !input.DisputeResolved {
		return true, false, nil
	}
	if !latest.DisputeResolved && input.DisputeResolved {
		return false, false, nil
	}
	if latest.DisputeResolved && input.DisputeResolved {
		return false, true, nil
	}
	return true, false, nil
}

func reconcilePaymentReversalTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder, input PaymentEventInput, result *PaymentSettlementResult) error {
	if order.Status == PaymentOrderStatusPending || order.Status == PaymentOrderStatusProcessing || order.Status == PaymentOrderStatusFailed || order.Status == PaymentOrderStatusExpired {
		return ErrPaymentManualReview
	}
	if input.Currency != "" && !strings.EqualFold(strings.TrimSpace(input.Currency), strings.TrimSpace(order.Currency)) {
		return ErrPaymentCurrencyMismatch
	}
	granted, err := paymentEntitlementGrantedTx(tx, order)
	if err != nil {
		return fmt.Errorf("%w: payment entitlement grant cannot be verified: %v", ErrPaymentManualReview, err)
	}
	if !granted {
		resolved, err := legacyExternalRefundWithoutEntitlementResolvedTx(tx, order)
		if err != nil {
			return err
		}
		if resolved && input.Refunded && input.RefundedAmountMinor == order.ExpectedAmountMinor {
			return nil
		}
		return fmt.Errorf("%w: payment entitlement was never granted", ErrPaymentManualReview)
	}
	if input.Refunded {
		if input.RefundedAmountMinor < order.RefundedAmountMinor {
			return nil
		}
		if input.RefundedAmountMinor > order.ExpectedAmountMinor {
			return ErrPaymentAmountMismatch
		}
		order.RefundedAmountMinor = input.RefundedAmountMinor
	}
	if input.Disputed || input.DisputeResolved {
		disputedAmount, err := paymentDisputedAmountTx(tx, event, order, input)
		if err != nil {
			return err
		}
		order.DisputedAmountMinor = disputedAmount
	}
	targetAmount := order.RefundedAmountMinor
	if order.DisputedAmountMinor > order.ExpectedAmountMinor-targetAmount {
		targetAmount = order.ExpectedAmountMinor
	} else {
		targetAmount += order.DisputedAmountMinor
	}
	if targetAmount < 0 || targetAmount > order.ExpectedAmountMinor {
		return ErrPaymentAmountMismatch
	}

	if order.OrderKind == PaymentOrderKindSubscription {
		return reconcileSubscriptionReversalTx(tx, event, order, input, targetAmount, result)
	}
	targetQuota, clamp := common.QuotaFromDecimalChecked(decimal.NewFromInt(order.CreditQuota).
		Mul(decimal.NewFromInt(targetAmount)).Div(decimal.NewFromInt(order.ExpectedAmountMinor)))
	if clamp != nil || targetQuota < 0 || int64(targetQuota) > order.CreditQuota {
		return errors.New("payment reversal quota is outside the supported range")
	}
	delta := int64(targetQuota) - order.ReversedQuota
	ledgerType := PaymentLedgerEntryRefundReversal
	if order.DisputedAmountMinor > 0 {
		ledgerType = PaymentLedgerEntryDisputeReversal
	}
	if delta > 0 {
		recovered, debt, err := reversePaymentQuotaTx(tx, order, int(delta), PaymentDebtKindBuyer)
		if err != nil {
			return err
		}
		result.QuotaDelta -= recovered
		result.UserCacheChanged = recovered > 0 || debt > 0
		if err := createPaymentLedgerEntryTx(tx, event, order, ledgerType, targetAmount-order.ReversedAmountMinor, -delta, "payment quota reversed"); err != nil {
			return err
		}
	} else if delta < 0 {
		restored, err := restorePaymentQuotaTx(tx, order, int(-delta), PaymentDebtKindBuyer)
		if err != nil {
			return err
		}
		result.QuotaDelta += restored
		result.UserCacheChanged = true
		if err := createPaymentLedgerEntryTx(tx, event, order, PaymentLedgerEntryReversalRestored, targetAmount-order.ReversedAmountMinor, -delta, "payment reversal restored"); err != nil {
			return err
		}
	}
	if err := reconcileAffiliateRewardReversalTx(tx, event, order, targetAmount, result); err != nil {
		return err
	}
	order.ReversedQuota = int64(targetQuota)
	order.ReversedAmountMinor = targetAmount
	status := PaymentOrderStatusFulfilled
	if targetAmount > 0 {
		hasOpenDebt, err := hasAnyOpenPaymentDebtTx(tx, order.ID)
		if err != nil {
			return err
		}
		if hasOpenDebt {
			status = PaymentOrderStatusDebt
		} else if order.DisputedAmountMinor > 0 {
			status = PaymentOrderStatusDisputed
		} else if order.RefundedAmountMinor >= order.ExpectedAmountMinor {
			status = PaymentOrderStatusRefunded
		} else {
			status = PaymentOrderStatusRefundPending
		}
	}
	order.Status = status
	order.UpdatedAt = common.GetTimestamp()
	order.Version++
	if err := tx.Model(&PaymentOrder{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
		"status": order.Status, "refunded_amount_minor": order.RefundedAmountMinor,
		"disputed_amount_minor": order.DisputedAmountMinor, "reversed_amount_minor": order.ReversedAmountMinor,
		"reversed_quota": order.ReversedQuota, "updated_at": order.UpdatedAt, "version": order.Version,
	}).Error; err != nil {
		return err
	}
	return syncPaymentProjectionStatusTx(tx, order)
}

// paymentDisputedAmountTx derives the current disputed exposure from the
// latest processed state of every provider dispute resource. Stripe documents
// that one payment can receive more than one dispute, so a single latest event
// must not overwrite or clear the amount still held by another dispute.
func paymentDisputedAmountTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder, input PaymentEventInput) (int64, error) {
	if tx == nil || event == nil || order == nil {
		return 0, ErrPaymentEventInvalid
	}
	resourceKey := strings.TrimSpace(input.ProviderResourceKey)
	if resourceKey == "" {
		if input.Disputed {
			if input.DisputedAmountMinor <= 0 || input.DisputedAmountMinor > order.ExpectedAmountMinor {
				return 0, ErrPaymentAmountMismatch
			}
			return input.DisputedAmountMinor, nil
		}
		if input.DisputeResolved && input.DisputeWon {
			return 0, nil
		}
		return order.DisputedAmountMinor, nil
	}

	var history []PaymentEvent
	if err := tx.Select(
		"id", "provider_resource_key", "provider_created_at", "disputed_amount_minor", "disputed", "dispute_resolved", "dispute_won",
	).Where(
		"provider = ? AND payment_order_id = ? AND id <> ? AND status = ? AND provider_resource_key <> ?",
		input.Provider, order.ID, event.ID, PaymentEventStatusProcessed, "",
	).Where("disputed = ? OR dispute_resolved = ?", true, true).
		Order("provider_resource_key asc, provider_created_at desc, id desc").Find(&history).Error; err != nil {
		return 0, err
	}

	latest := make(map[string]PaymentEvent, len(history)+1)
	for _, candidate := range history {
		key := strings.TrimSpace(candidate.ProviderResourceKey)
		if key == "" {
			continue
		}
		if _, exists := latest[key]; !exists {
			latest[key] = candidate
		}
	}
	latest[resourceKey] = PaymentEvent{
		ProviderResourceKey: resourceKey,
		DisputedAmountMinor: input.DisputedAmountMinor,
		Disputed:            input.Disputed,
		DisputeResolved:     input.DisputeResolved,
		DisputeWon:          input.DisputeWon,
	}

	var total int64
	for _, state := range latest {
		if state.DisputeResolved && state.DisputeWon {
			continue
		}
		if !state.Disputed {
			if state.DisputeResolved {
				return 0, ErrPaymentManualReview
			}
			continue
		}
		if state.DisputedAmountMinor <= 0 || state.DisputedAmountMinor > order.ExpectedAmountMinor-total {
			return 0, ErrPaymentAmountMismatch
		}
		total += state.DisputedAmountMinor
	}
	return total, nil
}

func legacyExternalRefundWithoutEntitlementResolvedTx(tx *gorm.DB, order *PaymentOrder) (bool, error) {
	if tx == nil || order == nil || order.ExpectedAmountMinor <= 0 || order.Status != PaymentOrderStatusRefunded ||
		order.PaidAmountMinor != 0 || order.RefundedAmountMinor != order.ExpectedAmountMinor || order.SettledAt != 0 {
		return false, nil
	}
	var snapshot struct {
		Source     string `json:"source"`
		Resolution string `json:"resolution"`
	}
	if err := common.UnmarshalJsonStr(order.PricingSnapshot, &snapshot); err != nil ||
		(snapshot.Source != "admin_legacy_topup_resolution" && snapshot.Source != "admin_legacy_subscription_resolution") ||
		snapshot.Resolution != PaymentLegacyTopUpResolutionExternalRefund {
		return false, nil
	}
	var receipts []PaymentLedgerEntry
	if err := tx.Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryLegacyPaymentReceived).
		Find(&receipts).Error; err != nil {
		return false, err
	}
	var refunds []PaymentLedgerEntry
	if err := tx.Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryAdminExternalRefund).
		Find(&refunds).Error; err != nil {
		return false, err
	}
	if len(receipts) != 1 || len(refunds) != 1 || receipts[0].AmountMinor != order.ExpectedAmountMinor ||
		refunds[0].AmountMinor != order.ExpectedAmountMinor || receipts[0].QuotaDelta != 0 || refunds[0].QuotaDelta != 0 {
		return false, nil
	}
	var receiptEvent PaymentEvent
	if err := tx.First(&receiptEvent, receipts[0].PaymentEventID).Error; err != nil {
		return false, err
	}
	var refundEvent PaymentEvent
	if err := tx.First(&refundEvent, refunds[0].PaymentEventID).Error; err != nil {
		return false, err
	}
	if receiptEvent.Provider != order.Provider || !receiptEvent.Paid || receiptEvent.PaidAmountMinor != order.ExpectedAmountMinor ||
		receiptEvent.PaymentOrderID != order.ID || refundEvent.Provider != "admin" || !refundEvent.Refunded ||
		refundEvent.RefundedAmountMinor != order.ExpectedAmountMinor || refundEvent.Status != PaymentEventStatusProcessed ||
		refundEvent.PaymentOrderID != order.ID {
		return false, nil
	}
	return true, nil
}

func reconcileSubscriptionReversalTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder, input PaymentEventInput, targetAmount int64, result *PaymentSettlementResult) error {
	if targetAmount > 0 && targetAmount < order.ExpectedAmountMinor {
		return ErrPaymentAmountMismatch
	}
	var subscription UserSubscription
	if err := lockForUpdate(tx).Where("payment_order_id = ?", order.ID).First(&subscription).Error; err != nil {
		return err
	}
	now := common.GetTimestamp()
	if targetAmount == 0 {
		// A won dispute restores the original paid state. The entitlement was not
		// cancelled while the dispute was open, so only the monetary hold clears.
		if order.ReversedQuota != 0 {
			return ErrPaymentManualReview
		}
		restoredAmount, err := restorePaymentDebtAmountTx(tx, order, PaymentDebtKindBuyer, order.ExpectedAmountMinor)
		if err != nil {
			return err
		}
		if restoredAmount > 0 {
			result.UserCacheChanged = true
		}
		order.Status = PaymentOrderStatusFulfilled
		order.DisputedAmountMinor = 0
		order.ReversedAmountMinor = 0
		result.UserCacheChanged = true
	} else if input.Disputed && !input.DisputeResolved {
		if err := addPaymentDebtTx(tx, order, order.UserID, PaymentDebtKindBuyer, 0, targetAmount, true); err != nil {
			return err
		}
		order.Status = PaymentOrderStatusDisputed
		order.ReversedAmountMinor = targetAmount
		result.UserCacheChanged = true
	} else {
		restoredAmount, err := restorePaymentDebtAmountTx(tx, order, PaymentDebtKindBuyer, order.ExpectedAmountMinor)
		if err != nil {
			return err
		}
		if restoredAmount > 0 {
			result.UserCacheChanged = true
		}
		usageTotal := subscription.AmountUsedTotal
		if usageTotal < subscription.AmountUsed {
			usageTotal = subscription.AmountUsed
		}
		if subscription.UsageAccountingVersion == 0 && subscription.LastResetTime > subscription.StartTime {
			return ErrPaymentManualReview
		}
		if usageTotal < order.ReversedQuota {
			return ErrPaymentManualReview
		}
		if subscription.Status == "active" {
			subscription.Status = "cancelled"
			subscription.EndTime = now
			if err := tx.Save(&subscription).Error; err != nil {
				return err
			}
			targetGroup, err := downgradeUserGroupForSubscriptionTx(tx, &subscription, now)
			if err != nil {
				return err
			}
			result.GroupCacheValue = targetGroup
		}
		usageDelta := usageTotal - order.ReversedQuota
		if usageDelta > math.MaxInt32 {
			return errors.New("subscription reversal debt exceeds supported range")
		}
		if usageDelta > 0 {
			if err := addPaymentDebtTx(tx, order, order.UserID, PaymentDebtKindBuyer, int(usageDelta), 0, true); err != nil {
				return err
			}
			result.UserCacheChanged = true
		}
		order.Status = PaymentOrderStatusRefunded
		hasOpenDebt, err := hasAnyOpenPaymentDebtTx(tx, order.ID)
		if err != nil {
			return err
		}
		if hasOpenDebt {
			order.Status = PaymentOrderStatusDebt
		}
		order.ReversedAmountMinor = targetAmount
		order.ReversedQuota = usageTotal
		if err := createPaymentLedgerEntryTx(tx, event, order, PaymentLedgerEntrySubscriptionCanceled, targetAmount, -usageDelta, "subscription entitlement cancelled"); err != nil {
			return err
		}
	}
	order.UpdatedAt = now
	order.Version++
	if err := tx.Model(&PaymentOrder{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
		"status": order.Status, "refunded_amount_minor": order.RefundedAmountMinor,
		"disputed_amount_minor": order.DisputedAmountMinor, "reversed_amount_minor": order.ReversedAmountMinor,
		"reversed_quota": order.ReversedQuota, "updated_at": order.UpdatedAt, "version": order.Version,
	}).Error; err != nil {
		return err
	}
	return syncPaymentProjectionStatusTx(tx, order)
}

func reversePaymentQuotaTx(tx *gorm.DB, order *PaymentOrder, quota int, debtKind string) (int, int, error) {
	return reverseUserPaymentQuotaTx(tx, order, order.UserID, quota, debtKind)
}

func reverseUserPaymentQuotaTx(tx *gorm.DB, order *PaymentOrder, userID int, quota int, debtKind string) (int, int, error) {
	if quota <= 0 {
		return 0, 0, nil
	}
	var user User
	if err := lockForUpdate(tx).Select("id", "quota", "status").Where("id = ?", userID).First(&user).Error; err != nil {
		return 0, 0, err
	}
	recovered := quota
	if user.Quota < recovered {
		recovered = user.Quota
	}
	if recovered < 0 {
		return 0, 0, errors.New("user quota is negative")
	}
	if recovered > 0 {
		if err := applyUserQuotaDeltaTx(tx, userID, -recovered); err != nil {
			return 0, 0, err
		}
	}
	debt := quota - recovered
	if debt > 0 {
		if err := addPaymentDebtTx(tx, order, userID, debtKind, debt, 0, true); err != nil {
			return 0, 0, err
		}
	}
	return recovered, debt, nil
}

func restorePaymentQuotaTx(tx *gorm.DB, order *PaymentOrder, quota int, debtKind string) (int, error) {
	return restoreUserPaymentQuotaTx(tx, order, order.UserID, quota, debtKind)
}

func restoreUserPaymentQuotaTx(tx *gorm.DB, order *PaymentOrder, userID int, quota int, debtKind string) (int, error) {
	if quota <= 0 {
		return 0, nil
	}
	resolved, err := resolvePaymentDebtQuotaTx(tx, order, userID, debtKind, quota)
	if err != nil {
		return 0, err
	}
	credit := quota - resolved
	if credit > 0 {
		if err := applyUserQuotaDeltaTx(tx, userID, credit); err != nil {
			return 0, err
		}
	}
	return credit, nil
}

func reconcileAffiliateRewardReversalTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder, targetAmount int64, result *PaymentSettlementResult) error {
	if order == nil || event == nil || order.ExpectedAmountMinor <= 0 || targetAmount < 0 || targetAmount > order.ExpectedAmountMinor {
		return ErrPaymentAmountMismatch
	}
	var topUp TopUp
	if err := lockForUpdate(tx).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).First(&topUp).Error; err != nil {
		return err
	}
	var records []AffiliateRewardRecord
	if err := lockForUpdate(tx).Where("top_up_id = ?", topUp.Id).Order("id asc").Find(&records).Error; err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}

	totalDelta := 0
	inviterID := 0
	now := common.GetTimestamp()
	for index := range records {
		record := &records[index]
		if record.RewardQuota < 0 || record.ReversedQuota < 0 || record.ReversedQuota > record.RewardQuota || record.TransferredQuota < 0 || record.TransferredQuota > record.RewardQuota {
			return ErrPaymentManualReview
		}
		if inviterID == 0 {
			inviterID = record.InviterId
		} else if inviterID != record.InviterId {
			return ErrPaymentManualReview
		}
		targetQuota, clamp := common.QuotaFromDecimalChecked(decimal.NewFromInt(int64(record.RewardQuota)).
			Mul(decimal.NewFromInt(targetAmount)).Div(decimal.NewFromInt(order.ExpectedAmountMinor)))
		if clamp != nil || targetQuota < 0 || targetQuota > record.RewardQuota {
			return ErrPaymentManualReview
		}
		if targetQuota == record.ReversedQuota {
			continue
		}

		originalStatus := record.Status
		if originalStatus == AffiliateRewardStatusCanceled && record.PreReversalStatus != "" {
			originalStatus = record.PreReversalStatus
		}
		if originalStatus == "" {
			originalStatus = AffiliateRewardStatusAvailable
		}
		activeUntransferred := record.RewardQuota - record.TransferredQuota
		oldWalletReversal := record.ReversedQuota - activeUntransferred
		if oldWalletReversal < 0 {
			oldWalletReversal = 0
		}
		newWalletReversal := targetQuota - activeUntransferred
		if newWalletReversal < 0 {
			newWalletReversal = 0
		}
		oldAvailableReversal := record.ReversedQuota - oldWalletReversal
		newAvailableReversal := targetQuota - newWalletReversal

		walletDelta := newWalletReversal - oldWalletReversal
		availableDelta := newAvailableReversal - oldAvailableReversal
		historyDelta := targetQuota - record.ReversedQuota
		balanceWasGranted := originalStatus == AffiliateRewardStatusAvailable || originalStatus == AffiliateRewardStatusTransferred
		if record.Status == AffiliateRewardStatusCanceled && record.PreReversalStatus == "" {
			// A separately cancelled reward already had its untransferred balance
			// removed. Only the historically transferred portion remains subject
			// to payment reversal.
			availableDelta = 0
			historyDelta = walletDelta
			balanceWasGranted = record.TransferredQuota > 0
		}

		if balanceWasGranted && (availableDelta != 0 || historyDelta != 0) {
			var inviter User
			if err := lockForUpdate(tx).Select("id", "aff_quota", "aff_history").Where("id = ?", record.InviterId).First(&inviter).Error; err != nil {
				return err
			}
			if availableDelta > 0 && inviter.AffQuota < availableDelta {
				return ErrPaymentManualReview
			}
			if historyDelta > 0 && inviter.AffHistoryQuota < historyDelta {
				return ErrPaymentManualReview
			}
			newAffQuota := int64(inviter.AffQuota) - int64(availableDelta)
			newAffHistory := int64(inviter.AffHistoryQuota) - int64(historyDelta)
			if newAffQuota < 0 || newAffQuota > int64(common.MaxQuota) || newAffHistory < 0 || newAffHistory > int64(common.MaxQuota) {
				return ErrPaymentManualReview
			}
			updates := map[string]interface{}{}
			if availableDelta != 0 {
				updates["aff_quota"] = gorm.Expr("aff_quota - ?", availableDelta)
			}
			if historyDelta != 0 {
				updates["aff_history"] = gorm.Expr("aff_history - ?", historyDelta)
			}
			if len(updates) > 0 {
				if err := tx.Model(&User{}).Where("id = ?", record.InviterId).Updates(updates).Error; err != nil {
					return err
				}
				result.CacheUserIDs = appendUniqueUserID(result.CacheUserIDs, record.InviterId)
			}
		}
		if walletDelta > 0 {
			if _, _, err := reverseUserPaymentQuotaTx(tx, order, record.InviterId, walletDelta, PaymentDebtKindAffiliate); err != nil {
				return err
			}
			result.CacheUserIDs = appendUniqueUserID(result.CacheUserIDs, record.InviterId)
		} else if walletDelta < 0 {
			if _, err := restoreUserPaymentQuotaTx(tx, order, record.InviterId, -walletDelta, PaymentDebtKindAffiliate); err != nil {
				return err
			}
			result.CacheUserIDs = appendUniqueUserID(result.CacheUserIDs, record.InviterId)
		}

		updates := map[string]interface{}{"reversed_quota": targetQuota}
		if targetQuota >= record.RewardQuota && record.Status != AffiliateRewardStatusCanceled {
			updates["pre_reversal_status"] = originalStatus
			updates["status"] = AffiliateRewardStatusCanceled
			updates["canceled_at"] = now
		} else if record.PreReversalStatus != "" {
			updates["status"] = originalStatus
			updates["pre_reversal_status"] = ""
			updates["canceled_at"] = int64(0)
		}
		if err := tx.Model(&AffiliateRewardRecord{}).Where("id = ?", record.Id).Updates(updates).Error; err != nil {
			return err
		}
		totalDelta += targetQuota - record.ReversedQuota
	}
	if totalDelta == 0 {
		return nil
	}
	entry := &PaymentLedgerEntry{
		PaymentOrderID: order.ID,
		PaymentEventID: event.ID,
		UserID:         inviterID,
		EntryType:      PaymentLedgerEntryAffiliateReversal,
		AmountMinor:    targetAmount - order.ReversedAmountMinor,
		QuotaDelta:     int64(-totalDelta),
		Currency:       order.Currency,
		Description:    "affiliate reward reconciled after payment reversal",
		CreatedAt:      now,
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "payment_order_id"}, {Name: "payment_event_id"}, {Name: "entry_type"}},
		DoNothing: true,
	}).Create(entry).Error
}

func appendUniqueUserID(userIDs []int, userID int) []int {
	if userID <= 0 {
		return userIDs
	}
	for _, existing := range userIDs {
		if existing == userID {
			return userIDs
		}
	}
	return append(userIDs, userID)
}

func addPaymentDebtTx(tx *gorm.DB, order *PaymentOrder, userID int, debtKind string, quota int, amountMinor int64, freeze bool) error {
	if quota < 0 || amountMinor < 0 || (quota == 0 && amountMinor == 0) {
		return errors.New("invalid payment debt")
	}
	var debt PaymentDebt
	err := lockForUpdate(tx).Where("payment_order_id = ? AND user_id = ? AND debt_kind = ?", order.ID, userID, debtKind).First(&debt).Error
	now := common.GetTimestamp()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var user User
		if err := lockForUpdate(tx).Select("id", "status", "payment_frozen").Where("id = ?", userID).First(&user).Error; err != nil {
			return err
		}
		previousUserStatus, previousStatusErr := paymentFreezePreviousUserStatusTx(tx, &user)
		if previousStatusErr != nil {
			return previousStatusErr
		}
		debt = PaymentDebt{
			PaymentOrderID:         order.ID,
			UserID:                 userID,
			DebtKind:               debtKind,
			Currency:               order.Currency,
			OriginalAmountMinor:    amountMinor,
			OutstandingAmountMinor: amountMinor,
			OriginalQuota:          int64(quota),
			OutstandingQuota:       int64(quota),
			PreviousUserStatus:     previousUserStatus,
			FreezeApplied:          freeze,
			Status:                 PaymentDebtStatusOpen,
			CreatedAt:              now,
			UpdatedAt:              now,
		}
		if err := tx.Create(&debt).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if freeze && debt.Status != PaymentDebtStatusOpen {
			var user User
			if err := lockForUpdate(tx).Select("id", "status", "payment_frozen").Where("id = ?", userID).First(&user).Error; err != nil {
				return err
			}
			previousUserStatus, previousStatusErr := paymentFreezePreviousUserStatusTx(tx, &user)
			if previousStatusErr != nil {
				return previousStatusErr
			}
			debt.PreviousUserStatus = previousUserStatus
		}
		debt.OriginalQuota += int64(quota)
		debt.OutstandingQuota += int64(quota)
		debt.OriginalAmountMinor += amountMinor
		debt.OutstandingAmountMinor += amountMinor
		debt.Status = PaymentDebtStatusOpen
		debt.FreezeApplied = debt.FreezeApplied || freeze
		debt.ResolvedAt = 0
		debt.Resolution = ""
		debt.ResolutionNote = ""
		debt.ResolvedBy = 0
		debt.UpdatedAt = now
		if err := tx.Save(&debt).Error; err != nil {
			return err
		}
	}
	if freeze {
		return tx.Model(&User{}).Where("id = ?", userID).Updates(map[string]interface{}{
			"status":         common.UserStatusDisabled,
			"payment_frozen": true,
		}).Error
	}
	return nil
}

func resolvePaymentDebtQuotaTx(tx *gorm.DB, order *PaymentOrder, userID int, debtKind string, quota int) (int, error) {
	var debt PaymentDebt
	err := lockForUpdate(tx).Where("payment_order_id = ? AND user_id = ? AND debt_kind = ?", order.ID, userID, debtKind).First(&debt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	resolved := quota
	if int64(resolved) > debt.OutstandingQuota {
		resolved = int(debt.OutstandingQuota)
	}
	debt.OutstandingQuota -= int64(resolved)
	debt.RecoveredQuota += int64(resolved)
	debt.UpdatedAt = common.GetTimestamp()
	if debt.OutstandingQuota == 0 && debt.OutstandingAmountMinor == 0 {
		debt.Status = PaymentDebtStatusResolved
		debt.ResolvedAt = debt.UpdatedAt
	}
	if err := tx.Save(&debt).Error; err != nil {
		return 0, err
	}
	if debt.Status == PaymentDebtStatusResolved {
		if err := releasePaymentFreezeTx(tx, &debt); err != nil {
			return 0, err
		}
	}
	return resolved, nil
}

func restorePaymentDebtAmountTx(tx *gorm.DB, order *PaymentOrder, debtKind string, amountMinor int64) (int64, error) {
	var debt PaymentDebt
	err := lockForUpdate(tx).Where("payment_order_id = ? AND user_id = ? AND debt_kind = ?", order.ID, order.UserID, debtKind).First(&debt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	resolved := amountMinor
	if resolved > debt.OutstandingAmountMinor {
		resolved = debt.OutstandingAmountMinor
	}
	debt.OutstandingAmountMinor -= resolved
	debt.UpdatedAt = common.GetTimestamp()
	if debt.OutstandingQuota == 0 && debt.OutstandingAmountMinor == 0 {
		debt.Status = PaymentDebtStatusResolved
		debt.ResolvedAt = debt.UpdatedAt
	}
	if err := tx.Save(&debt).Error; err != nil {
		return 0, err
	}
	if debt.Status == PaymentDebtStatusResolved {
		if err := releasePaymentFreezeTx(tx, &debt); err != nil {
			return 0, err
		}
	}
	return resolved, nil
}

func releasePaymentFreezeTx(tx *gorm.DB, debt *PaymentDebt) error {
	if debt == nil || !debt.FreezeApplied {
		return nil
	}
	_, err := releaseUserPaymentFreezeTx(tx, debt.UserID, debt.PreviousUserStatus)
	return err
}

func hasAnyOpenPaymentDebtTx(tx *gorm.DB, orderID int64) (bool, error) {
	var count int64
	if err := tx.Model(&PaymentDebt{}).Where("payment_order_id = ? AND status = ?", orderID, PaymentDebtStatusOpen).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func paymentOrderProjectionStatus(status string) string {
	switch status {
	case PaymentOrderStatusPaid, PaymentOrderStatusFulfilled:
		return common.TopUpStatusSuccess
	default:
		return status
	}
}

func syncPaymentProjectionStatusTx(tx *gorm.DB, order *PaymentOrder) error {
	if order == nil {
		return errors.New("payment order is required")
	}
	status := paymentOrderProjectionStatus(order.Status)
	if order.OrderKind == PaymentOrderKindTopUp {
		return tx.Model(&TopUp{}).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
			Update("status", status).Error
	}
	if order.OrderKind == PaymentOrderKindSubscription {
		updates := map[string]interface{}{"status": status}
		if order.Status == PaymentOrderStatusManualReview {
			updates["review_reason"] = order.StatusReason
		} else {
			updates["review_reason"] = ""
		}
		return tx.Model(&SubscriptionOrder{}).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
			Updates(updates).Error
	}
	return nil
}

func createPaymentLedgerEntryTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder, entryType string, amountMinor int64, quotaDelta int64, description string) error {
	entry := &PaymentLedgerEntry{
		PaymentOrderID: order.ID,
		PaymentEventID: event.ID,
		UserID:         order.UserID,
		EntryType:      entryType,
		AmountMinor:    amountMinor,
		QuotaDelta:     quotaDelta,
		Currency:       order.Currency,
		Description:    description,
		CreatedAt:      common.GetTimestamp(),
	}
	// Event processing can safely be retried after a crash between provider
	// delivery and acknowledgement. DoNothing is required on PostgreSQL because
	// catching a unique violation and querying in the same transaction leaves
	// that transaction permanently aborted.
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "payment_order_id"}, {Name: "payment_event_id"}, {Name: "entry_type"}},
		DoNothing: true,
	}).Create(entry).Error
}

func markPaymentManualReviewTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder, reason string, reviewErr error, result *PaymentSettlementResult, postCommitErr *error) error {
	now := common.GetTimestamp()
	if err := tx.Model(&PaymentOrder{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
		"status": PaymentOrderStatusManualReview, "status_reason": reason, "start_payload": "",
		"browser_authorization_digest": nil, "browser_authorization_payload": "",
		"browser_authorization_expires_at": 0, "browser_authorized_at": 0,
		"updated_at": now, "version": gorm.Expr("version + ?", 1),
	}).Error; err != nil {
		return err
	}
	if err := finishPaymentEventTx(tx, event.ID, PaymentEventStatusManualReview, reason, order.ID); err != nil {
		return err
	}
	if order.OrderKind == PaymentOrderKindTopUp {
		if err := tx.Model(&TopUp{}).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
			Update("status", common.TopUpStatusManualReview).Error; err != nil {
			return err
		}
	} else if order.OrderKind == PaymentOrderKindSubscription {
		if err := tx.Model(&SubscriptionOrder{}).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
			Updates(map[string]interface{}{"status": SubscriptionOrderStatusManualReview, "review_reason": reason}).Error; err != nil {
			return err
		}
	}
	order.Status = PaymentOrderStatusManualReview
	order.StatusReason = reason
	result.ManualReview = true
	*postCommitErr = fmt.Errorf("%w: %v", ErrPaymentManualReview, reviewErr)
	return nil
}

func markPaymentManualReviewForOrderTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder, reason string,
	reviewErr error, result *PaymentSettlementResult, postCommitErr *error) error {
	if order != nil && (order.SettledAt > 0 || order.PaidAmountMinor > 0 ||
		order.Status == PaymentOrderStatusFulfilled || order.Status == PaymentOrderStatusPaid ||
		order.Status == PaymentOrderStatusRefundPending || order.Status == PaymentOrderStatusRefunded ||
		order.Status == PaymentOrderStatusDisputed || order.Status == PaymentOrderStatusDebt) {
		return markPaymentManualReviewPreservingProjectionTx(tx, event, order, reason, reviewErr, result, postCommitErr)
	}
	return markPaymentManualReviewTx(tx, event, order, reason, reviewErr, result, postCommitErr)
}

// A late provider-paid event must remain visible to administrators after a
// deliberate reject/void, but the failed legacy projection must stay terminal
// so it does not silently reserve a subscription purchase slot again.
func markPaymentManualReviewPreservingProjectionTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder, reason string, reviewErr error, result *PaymentSettlementResult, postCommitErr *error) error {
	now := common.GetTimestamp()
	if err := tx.Model(&PaymentOrder{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
		"status": PaymentOrderStatusManualReview, "status_reason": reason, "start_payload": "",
		"browser_authorization_digest": nil, "browser_authorization_payload": "",
		"browser_authorization_expires_at": 0, "browser_authorized_at": 0,
		"updated_at": now, "version": gorm.Expr("version + ?", 1),
	}).Error; err != nil {
		return err
	}
	if err := finishPaymentEventTx(tx, event.ID, PaymentEventStatusManualReview, reason, order.ID); err != nil {
		return err
	}
	order.Status = PaymentOrderStatusManualReview
	order.StatusReason = reason
	order.UpdatedAt = now
	order.Version++
	result.ManualReview = true
	*postCommitErr = fmt.Errorf("%w: %v", ErrPaymentManualReview, reviewErr)
	return nil
}

func finishPaymentEventTx(tx *gorm.DB, eventID int64, status string, lastError string, orderID int64) error {
	return finishPaymentEventWithReviewCodeTx(tx, eventID, status, "", lastError, orderID)
}

func finishPaymentEventWithReviewCodeTx(tx *gorm.DB, eventID int64, status, reviewCode, lastError string, orderID int64) error {
	updates := map[string]interface{}{
		"status": status, "review_code": reviewCode, "last_error": lastError, "updated_at": common.GetTimestamp(),
	}
	if orderID > 0 {
		updates["payment_order_id"] = orderID
	}
	if status == PaymentEventStatusProcessed {
		updates["processed_at"] = common.GetTimestamp()
	}
	return tx.Model(&PaymentEvent{}).Where("id = ?", eventID).Updates(updates).Error
}

func LogPaymentSettlement(result *PaymentSettlementResult, provider string, clientIP string) {
	if result == nil || result.Order == nil || result.Duplicate || result.ManualReview {
		return
	}
	if result.Order.OrderKind == PaymentOrderKindTopUp && result.QuotaDelta > 0 {
		RecordTopupLog(result.Order.UserID,
			fmt.Sprintf("在线充值成功，到账额度: %s，支付金额: %s %s", logger.FormatQuota(result.QuotaDelta), formatPaymentAmountForLog(result.Order.PaidAmountMinor, result.Order.Provider, result.Order.Currency), result.Order.Currency),
			clientIP, result.Order.PaymentMethod, provider)
	}
}

func formatPaymentAmountForLog(amountMinor int64, provider, currency string) string {
	exponent := common.PaymentProviderCurrencyExponent(provider, currency)
	return decimal.New(amountMinor, -exponent).StringFixed(exponent)
}
