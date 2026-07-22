package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stripe/stripe-go/v86"
)

type StripeLegacySyncResult struct {
	Seen     int `json:"seen"`
	Mapped   int `json:"mapped"`
	Unmapped int `json:"unmapped"`
}

type StripeLegacySubscriptionCancellationInput struct {
	InventoryID       int64
	ExpectedUpdatedAt int64
	AdminID           int
	ActorIP           string
	Reason            string
}

type StripeLegacySubscriptionCancellationResult struct {
	Subscription *model.StripeLegacySubscription `json:"subscription"`
	Duplicate    bool                            `json:"duplicate"`
}

var (
	ErrStripeLegacySyncNotConfigured            = errors.New("Stripe legacy inventory sync is not configured")
	ErrStripeLegacySyncModeMismatch             = errors.New("Stripe legacy inventory sync mode mismatch")
	ErrStripeLegacySyncUnavailable              = errors.New("Stripe legacy inventory sync is unavailable")
	ErrStripeLegacyCancellationNotConfigured    = errors.New("Stripe legacy subscription cancellation is not configured")
	ErrStripeLegacyCancellationIdentityMismatch = errors.New("Stripe legacy subscription cancellation identity mismatch")
	ErrStripeLegacyCancellationModeMismatch     = errors.New("Stripe legacy subscription cancellation mode mismatch")
	ErrStripeLegacyCancellationUnavailable      = errors.New("Stripe legacy subscription cancellation is unavailable")
)

type stripeLegacySubscriptionAPI interface {
	RetrieveAccount(ctx context.Context, accountID string) (*stripe.Account, error)
	UpdateSubscription(ctx context.Context, subscriptionID string, params *stripe.SubscriptionUpdateParams) (*stripe.Subscription, error)
}

type stripeLegacySubscriptionAPIFactory func(secret string) stripeLegacySubscriptionAPI

type stripeLegacySubscriptionClient struct {
	client *stripe.Client
}

func (client stripeLegacySubscriptionClient) RetrieveAccount(ctx context.Context, accountID string) (*stripe.Account, error) {
	params := &stripe.AccountRetrieveParams{}
	setStripeAccount(params, accountID)
	return client.client.V1Accounts.Retrieve(ctx, params)
}

func (client stripeLegacySubscriptionClient) UpdateSubscription(ctx context.Context, subscriptionID string, params *stripe.SubscriptionUpdateParams) (*stripe.Subscription, error) {
	return client.client.V1Subscriptions.Update(ctx, subscriptionID, params)
}

// ValidateVerifiedWebhook confirms Checkout Session payment evidence against
// the configured Stripe account without mutating local inventory.
func (p *stripePaymentProvider) ValidateVerifiedWebhook(ctx context.Context, event *NormalizedPaymentEvent) error {
	if event == nil || event.Provider != model.PaymentProviderStripe || len(event.VerifiedPayload) == 0 {
		return nil
	}
	if event.ProviderState == model.PaymentProviderStateStripeLegacyRecurringCheckoutPaid {
		unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
		err := p.confirmLegacyRecurringCheckoutAuthority(ctx, event)
		unlockPaymentConfiguration()
		return err
	}
	if event.Paid || event.Failed || event.Expired {
		unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
		err := p.confirmCheckoutSessionAuthority(ctx, event)
		unlockPaymentConfiguration()
		return err
	}
	return nil
}

// ProcessVerifiedWebhook updates recurring-subscription inventory only after
// the canonical event has been durably persisted or settled. It intentionally
// never invokes local subscription fulfillment or cancellation.
func (p *stripePaymentProvider) ProcessVerifiedWebhook(_ context.Context, event *NormalizedPaymentEvent) error {
	if event == nil || event.Provider != model.PaymentProviderStripe || len(event.VerifiedPayload) == 0 {
		return nil
	}
	var stripeEvent stripe.Event
	if err := common.Unmarshal(event.VerifiedPayload, &stripeEvent); err != nil {
		return err
	}
	switch stripeEvent.Type {
	case stripe.EventTypeCheckoutSessionCompleted,
		stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded,
		stripe.EventTypeCheckoutSessionAsyncPaymentFailed,
		stripe.EventTypeCheckoutSessionExpired:
		return processStripeLegacyCheckoutInventory(stripeEvent)
	case stripe.EventTypeCustomerSubscriptionCreated,
		stripe.EventTypeCustomerSubscriptionUpdated,
		stripe.EventTypeCustomerSubscriptionDeleted,
		stripe.EventTypeCustomerSubscriptionPaused,
		stripe.EventTypeCustomerSubscriptionResumed,
		stripe.EventTypeCustomerSubscriptionPendingUpdateApplied,
		stripe.EventTypeCustomerSubscriptionPendingUpdateExpired,
		stripe.EventTypeCustomerSubscriptionTrialWillEnd:
		var subscription stripe.Subscription
		if err := common.Unmarshal(stripeEvent.Data.Raw, &subscription); err != nil {
			return err
		}
		_, err := model.UpsertStripeLegacySubscription(stripeLegacySubscriptionSnapshot(&subscription, stripeEvent, model.StripeLegacySyncSourceWebhook, true))
		return err
	default:
		if !strings.HasPrefix(string(stripeEvent.Type), "invoice.") {
			return nil
		}
		var invoice stripe.Invoice
		if err := common.Unmarshal(stripeEvent.Data.Raw, &invoice); err != nil {
			return err
		}
		// invoice.upcoming previews can omit a persistent invoice ID. They are
		// transient forecasts, not durable inventory records.
		if !strings.HasPrefix(invoice.ID, "in_") {
			return nil
		}
		_, err := model.UpsertStripeLegacyInvoice(stripeLegacyInvoiceSnapshot(&invoice, stripeEvent))
		return err
	}
}

func processStripeLegacyCheckoutInventory(event stripe.Event) error {
	var session stripe.CheckoutSession
	if err := common.Unmarshal(event.Data.Raw, &session); err != nil {
		return err
	}
	if session.Subscription == nil || session.Subscription.ID == "" {
		return nil
	}
	snapshot := stripeLegacySubscriptionSnapshot(session.Subscription, event, model.StripeLegacySyncSourceCheckout, false)
	snapshot.CheckoutSessionID = session.ID
	snapshot.TradeNo = strings.TrimSpace(session.ClientReferenceID)
	if snapshot.TradeNo == "" {
		snapshot.TradeNo = strings.TrimSpace(session.Metadata["trade_no"])
	}
	if session.Customer != nil {
		snapshot.StripeCustomerID = session.Customer.ID
	}
	snapshot.MetadataUserID = model.ParseStripeLegacyMetadataID(session.Metadata["user_id"])
	snapshot.MetadataPlanID = model.ParseStripeLegacyMetadataID(session.Metadata["plan_id"])
	// Expanded subscription objects can be treated as a full Stripe snapshot;
	// the usual webhook object is ID-only and remains a mapped placeholder until
	// customer.subscription.* or an administrator API sync supplies full state.
	if session.Subscription.Status != "" || session.Subscription.Items != nil {
		snapshot.FullState = true
	}
	_, err := model.UpsertStripeLegacySubscription(snapshot)
	return err
}

func stripeLegacySubscriptionSnapshot(subscription *stripe.Subscription, event stripe.Event, source string, fullState bool) model.StripeLegacySubscriptionSnapshot {
	snapshot := model.StripeLegacySubscriptionSnapshot{
		SyncSource:          source,
		FullState:           fullState,
		LastStripeEventID:   event.ID,
		LastStripeEventType: string(event.Type),
		StateObservedAt:     event.Created,
		Livemode:            event.Livemode,
	}
	if event.Data != nil && len(event.Data.Raw) > 0 {
		snapshot.LastStripePayloadDigest = model.PaymentPayloadDigest(string(event.Data.Raw))
	}
	if subscription == nil {
		return snapshot
	}
	snapshot.StripeSubscriptionID = subscription.ID
	if subscription.Customer != nil {
		snapshot.StripeCustomerID = subscription.Customer.ID
	}
	snapshot.MetadataUserID = model.ParseStripeLegacyMetadataID(subscription.Metadata["user_id"])
	snapshot.MetadataPlanID = model.ParseStripeLegacyMetadataID(subscription.Metadata["plan_id"])
	snapshot.TradeNo = strings.TrimSpace(subscription.Metadata["trade_no"])
	if snapshot.TradeNo == "" {
		snapshot.TradeNo = strings.TrimSpace(subscription.Metadata["order_id"])
	}
	snapshot.Currency = string(subscription.Currency)
	snapshot.Status = string(subscription.Status)
	snapshot.CollectionMethod = string(subscription.CollectionMethod)
	snapshot.CancelAtPeriodEnd = subscription.CancelAtPeriodEnd
	snapshot.CancelAt = subscription.CancelAt
	snapshot.CanceledAt = subscription.CanceledAt
	snapshot.EndedAt = subscription.EndedAt
	snapshot.TrialStart = subscription.TrialStart
	snapshot.TrialEnd = subscription.TrialEnd
	snapshot.StripeCreatedAt = subscription.Created
	if subscription.LatestInvoice != nil {
		snapshot.LatestInvoiceID = subscription.LatestInvoice.ID
	}
	if subscription.Items != nil {
		snapshot.PriceIDs = make([]string, 0, len(subscription.Items.Data))
		var quantity int64
		for _, item := range subscription.Items.Data {
			if item == nil {
				continue
			}
			if item.CurrentPeriodStart > 0 && (snapshot.CurrentPeriodStart == 0 || item.CurrentPeriodStart < snapshot.CurrentPeriodStart) {
				snapshot.CurrentPeriodStart = item.CurrentPeriodStart
			}
			if item.CurrentPeriodEnd > snapshot.CurrentPeriodEnd {
				snapshot.CurrentPeriodEnd = item.CurrentPeriodEnd
			}
			if item.Quantity > 0 {
				if quantity > math.MaxInt64-item.Quantity {
					quantity = math.MaxInt64
				} else {
					quantity += item.Quantity
				}
			}
			if item.Price == nil {
				continue
			}
			snapshot.PriceIDs = append(snapshot.PriceIDs, item.Price.ID)
			if snapshot.Currency == "" {
				snapshot.Currency = string(item.Price.Currency)
			}
			if snapshot.ProductID == "" && item.Price.Product != nil {
				snapshot.ProductID = item.Price.Product.ID
			}
		}
		snapshot.Quantity = quantity
	}
	return snapshot
}

func stripeLegacyInvoiceSnapshot(invoice *stripe.Invoice, event stripe.Event) model.StripeLegacyInvoiceSnapshot {
	snapshot := model.StripeLegacyInvoiceSnapshot{
		LastStripeEventID:   event.ID,
		LastStripeEventType: string(event.Type),
		StateObservedAt:     event.Created,
		Livemode:            event.Livemode,
	}
	if event.Data != nil && len(event.Data.Raw) > 0 {
		snapshot.LastStripePayloadDigest = model.PaymentPayloadDigest(string(event.Data.Raw))
	}
	if invoice == nil {
		return snapshot
	}
	snapshot.StripeInvoiceID = invoice.ID
	if invoice.Customer != nil {
		snapshot.StripeCustomerID = invoice.Customer.ID
	}
	if invoice.Parent != nil && invoice.Parent.SubscriptionDetails != nil && invoice.Parent.SubscriptionDetails.Subscription != nil {
		snapshot.StripeSubscriptionID = invoice.Parent.SubscriptionDetails.Subscription.ID
	}
	// Stripe API versions before the invoice parent migration exposed a
	// top-level subscription field. The SDK's custom ID unmarshaler lets us
	// retain compatibility with those signed historical webhook payloads.
	if snapshot.StripeSubscriptionID == "" && event.Data != nil && len(event.Data.Raw) > 0 {
		var legacy struct {
			Subscription *stripe.Subscription `json:"subscription"`
		}
		if common.Unmarshal(event.Data.Raw, &legacy) == nil && legacy.Subscription != nil {
			snapshot.StripeSubscriptionID = legacy.Subscription.ID
		}
	}
	snapshot.Status = string(invoice.Status)
	snapshot.BillingReason = string(invoice.BillingReason)
	snapshot.Currency = string(invoice.Currency)
	snapshot.AmountDue = invoice.AmountDue
	snapshot.AmountPaid = invoice.AmountPaid
	snapshot.AmountRemaining = invoice.AmountRemaining
	snapshot.AttemptCount = invoice.AttemptCount
	snapshot.Paid = invoice.Status == stripe.InvoiceStatusPaid || event.Type == stripe.EventTypeInvoicePaid || event.Type == stripe.EventTypeInvoicePaymentSucceeded
	snapshot.PeriodStart = invoice.PeriodStart
	snapshot.PeriodEnd = invoice.PeriodEnd
	snapshot.StripeCreatedAt = invoice.Created
	return snapshot
}

// SyncStripeLegacySubscriptions performs a read-only Stripe API inventory sync.
// It writes only the local compatibility inventory and never changes Stripe or
// local user entitlements.
func SyncStripeLegacySubscriptions(ctx context.Context) (*StripeLegacySyncResult, error) {
	if err := model.EnsureStripeLegacyInventorySchema(); err != nil {
		return nil, err
	}
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		return nil, fmt.Errorf("%w: failed to synchronize payment configuration: %v", ErrStripeLegacySyncUnavailable, err)
	}
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	apiSecret := setting.StripeApiSecret
	accountID := strings.TrimSpace(setting.StripeAccountId)
	unlockPaymentConfiguration()
	if !validStripeSecret(apiSecret) {
		return nil, fmt.Errorf("%w: invalid Stripe API secret", ErrStripeLegacySyncNotConfigured)
	}
	client := stripe.NewClient(apiSecret)
	params := &stripe.SubscriptionListParams{
		Status: stripe.String("all"),
	}
	params.Limit = stripe.Int64(100)
	setStripeAccount(params, accountID)
	observedAt := time.Now().Unix()
	result := &StripeLegacySyncResult{}
	list := client.V1Subscriptions.List(ctx, params)
	for subscription, err := range list.All(ctx) {
		if err != nil {
			return result, fmt.Errorf("%w: Stripe subscription list failed: %v", ErrStripeLegacySyncUnavailable, err)
		}
		if subscription == nil {
			continue
		}
		if expected, known := stripeExpectedLiveMode(apiSecret); known && subscription.Livemode != expected {
			return result, fmt.Errorf("%w: Stripe subscription livemode mismatch", ErrStripeLegacySyncModeMismatch)
		}
		event := stripe.Event{Created: observedAt, Livemode: subscription.Livemode}
		inventory, err := model.UpsertStripeLegacySubscription(stripeLegacySubscriptionSnapshot(subscription, event, model.StripeLegacySyncSourceAPI, true))
		if err != nil {
			return result, fmt.Errorf("%w: failed to persist Stripe subscription inventory: %v", ErrStripeLegacySyncUnavailable, err)
		}
		result.Seen++
		if inventory.MappingStatus == model.StripeLegacyMappingMapped {
			result.Mapped++
		} else {
			result.Unmapped++
		}
	}
	return result, nil
}

// CancelStripeLegacySubscriptionAtPeriodEnd schedules a verified legacy
// recurring subscription to stop at the end of its current Stripe period. It
// does not refund, expire, extend, or otherwise mutate local entitlements.
func CancelStripeLegacySubscriptionAtPeriodEnd(ctx context.Context, input StripeLegacySubscriptionCancellationInput) (*StripeLegacySubscriptionCancellationResult, error) {
	return cancelStripeLegacySubscriptionAtPeriodEnd(ctx, input, func(secret string) stripeLegacySubscriptionAPI {
		return stripeLegacySubscriptionClient{client: stripe.NewClient(secret)}
	})
}

func cancelStripeLegacySubscriptionAtPeriodEnd(ctx context.Context, input StripeLegacySubscriptionCancellationInput, apiFactory stripeLegacySubscriptionAPIFactory) (*StripeLegacySubscriptionCancellationResult, error) {
	input.ActorIP = strings.TrimSpace(input.ActorIP)
	input.Reason = strings.TrimSpace(input.Reason)
	if apiFactory == nil || input.InventoryID <= 0 || input.ExpectedUpdatedAt <= 0 || input.AdminID <= 0 ||
		input.ActorIP == "" || len(input.ActorIP) > 64 || len(input.Reason) < 8 || len(input.Reason) > 512 {
		return nil, model.ErrStripeLegacySubscriptionCancelInvalid
	}
	if err := model.EnsureStripeLegacyInventorySchema(); err != nil {
		return nil, err
	}
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		return nil, fmt.Errorf("%w: payment configuration synchronization failed", ErrStripeLegacyCancellationUnavailable)
	}
	inventory, err := model.GetStripeLegacySubscriptionByID(input.InventoryID)
	if err != nil {
		return nil, err
	}
	retry, err := model.FindStripeLegacySubscriptionCancellationRetry(
		input.InventoryID, input.ExpectedUpdatedAt, input.AdminID, input.Reason,
	)
	if err != nil {
		return nil, err
	}
	if retry != nil {
		return &StripeLegacySubscriptionCancellationResult{
			Subscription: retry.Subscription,
			Duplicate:    true,
		}, nil
	}
	if inventory.UpdatedAt != input.ExpectedUpdatedAt {
		if !inventory.CancelAtPeriodEnd {
			return nil, fmt.Errorf("%w: inventory snapshot changed", model.ErrStripeLegacySubscriptionCancelConflict)
		}
		audited, auditErr := model.HasStripeLegacySubscriptionCancellationAudit(input.InventoryID)
		if auditErr != nil {
			return nil, auditErr
		}
		if audited {
			return nil, fmt.Errorf("%w: cancellation was already audited for another inventory snapshot", model.ErrStripeLegacySubscriptionCancelConflict)
		}
	}
	if inventory.Status == "canceled" || inventory.Status == "incomplete_expired" || inventory.EndedAt > 0 {
		return nil, fmt.Errorf("%w: subscription is already terminal", model.ErrStripeLegacySubscriptionCancelConflict)
	}
	if _, err := model.GetStripeLegacySubscriptionByStripeID(inventory.StripeSubscriptionID); err != nil {
		return nil, fmt.Errorf("%w: inventory subscription identity is invalid", model.ErrStripeLegacySubscriptionCancelConflict)
	}

	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	apiSecret := strings.TrimSpace(setting.StripeApiSecret)
	credentialAccountID := strings.TrimSpace(setting.StripeCredentialAccountId)
	connectedAccountID := strings.TrimSpace(setting.StripeAccountId)
	configuredMode := strings.ToLower(strings.TrimSpace(setting.StripeCredentialLivemode))
	unlockPaymentConfiguration()
	credentialMode, err := StripeCredentialMode(apiSecret)
	if err != nil || !setting.StripeCredentialModeAllowed(credentialMode) || configuredMode != credentialMode ||
		!stripeAccountIDPattern.MatchString(credentialAccountID) ||
		connectedAccountID != "" && !stripeAccountIDPattern.MatchString(connectedAccountID) {
		return nil, ErrStripeLegacyCancellationNotConfigured
	}
	expectedLivemode := credentialMode == "live"
	if inventory.Livemode != expectedLivemode {
		return nil, ErrStripeLegacyCancellationModeMismatch
	}
	api := apiFactory(apiSecret)
	if api == nil {
		return nil, ErrStripeLegacyCancellationUnavailable
	}

	expectedAccountID := credentialAccountID
	if connectedAccountID != "" {
		expectedAccountID = connectedAccountID
	}
	account, err := api.RetrieveAccount(ctx, connectedAccountID)
	if err != nil {
		return nil, fmt.Errorf("%w: Stripe account verification failed", ErrStripeLegacyCancellationUnavailable)
	}
	if account == nil || strings.TrimSpace(account.ID) != expectedAccountID {
		return nil, ErrStripeLegacyCancellationIdentityMismatch
	}

	params := &stripe.SubscriptionUpdateParams{CancelAtPeriodEnd: stripe.Bool(true)}
	requestDigest := model.PaymentPayloadDigest(fmt.Sprintf("%d\x00%s", input.AdminID, input.Reason))
	params.SetIdempotencyKey(fmt.Sprintf("legacy-sub-cancel:%d:%d:%s", inventory.ID, input.ExpectedUpdatedAt, requestDigest[:16]))
	setStripeAccount(params, connectedAccountID)
	updated, err := api.UpdateSubscription(ctx, inventory.StripeSubscriptionID, params)
	if err != nil {
		return nil, fmt.Errorf("%w: Stripe subscription update failed", ErrStripeLegacyCancellationUnavailable)
	}
	if updated == nil || strings.TrimSpace(updated.ID) != inventory.StripeSubscriptionID {
		return nil, ErrStripeLegacyCancellationIdentityMismatch
	}
	if updated.Livemode != expectedLivemode || updated.Livemode != inventory.Livemode {
		return nil, ErrStripeLegacyCancellationModeMismatch
	}
	if inventory.StripeCustomerID != "" && (updated.Customer == nil || strings.TrimSpace(updated.Customer.ID) != inventory.StripeCustomerID) {
		return nil, ErrStripeLegacyCancellationIdentityMismatch
	}
	if !updated.CancelAtPeriodEnd {
		return nil, fmt.Errorf("%w: Stripe did not schedule cancellation", ErrStripeLegacyCancellationUnavailable)
	}

	observedAt := time.Now().Unix()
	snapshot := stripeLegacySubscriptionSnapshot(updated, stripe.Event{Created: observedAt, Livemode: updated.Livemode}, model.StripeLegacySyncSourceAPI, true)
	persisted, err := model.PersistStripeLegacySubscriptionCancellation(model.StripeLegacySubscriptionCancellationInput{
		InventoryID: input.InventoryID, ExpectedUpdatedAt: input.ExpectedUpdatedAt,
		AdminID: input.AdminID, ActorIP: input.ActorIP, Reason: input.Reason,
		AccountID: expectedAccountID, CredentialMode: credentialMode, Snapshot: snapshot,
	})
	if err != nil {
		if errors.Is(err, model.ErrStripeLegacySubscriptionCancelInvalid) ||
			errors.Is(err, model.ErrStripeLegacySubscriptionNotFound) ||
			errors.Is(err, model.ErrStripeLegacySubscriptionCancelConflict) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: local cancellation snapshot persistence failed", ErrStripeLegacyCancellationUnavailable)
	}
	return &StripeLegacySubscriptionCancellationResult{
		Subscription: persisted.Subscription,
		Duplicate:    persisted.Duplicate,
	}, nil
}
