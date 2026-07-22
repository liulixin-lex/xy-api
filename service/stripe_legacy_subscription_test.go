package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v86"
	stripewebhook "github.com/stripe/stripe-go/v86/webhook"
)

var (
	stripeLegacyInventoryTestMigrateOnce sync.Once
	stripeLegacyInventoryTestMigrateErr  error
)

type stripeLegacyCancellationFakeAPI struct {
	account               *stripe.Account
	subscription          *stripe.Subscription
	accountErr            error
	updateErr             error
	retrievedAccountID    string
	updatedSubscriptionID string
	updateParams          *stripe.SubscriptionUpdateParams
	updateParamsHistory   []*stripe.SubscriptionUpdateParams
	updateFn              func(context.Context, string, *stripe.SubscriptionUpdateParams) (*stripe.Subscription, error)
	updateCalls           int
}

func (fake *stripeLegacyCancellationFakeAPI) RetrieveAccount(_ context.Context, accountID string) (*stripe.Account, error) {
	fake.retrievedAccountID = accountID
	return fake.account, fake.accountErr
}

func (fake *stripeLegacyCancellationFakeAPI) UpdateSubscription(ctx context.Context, subscriptionID string, params *stripe.SubscriptionUpdateParams) (*stripe.Subscription, error) {
	fake.updateCalls++
	fake.updatedSubscriptionID = subscriptionID
	fake.updateParams = params
	fake.updateParamsHistory = append(fake.updateParamsHistory, params)
	if fake.updateFn != nil {
		return fake.updateFn(ctx, subscriptionID, params)
	}
	return fake.subscription, fake.updateErr
}

func prepareStripeLegacyInventoryTest(t *testing.T) {
	t.Helper()
	stripeLegacyInventoryTestMigrateOnce.Do(func() {
		if !model.DB.Migrator().HasTable("subscription_plans") {
			stripeLegacyInventoryTestMigrateErr = model.DB.Exec(`CREATE TABLE subscription_plans (
id integer PRIMARY KEY,
title varchar(128) NOT NULL,
subtitle varchar(255) DEFAULT '',
price_amount numeric NOT NULL,
currency varchar(8) NOT NULL DEFAULT 'USD',
duration_unit varchar(16) NOT NULL DEFAULT 'month',
duration_value integer NOT NULL DEFAULT 1,
custom_seconds bigint NOT NULL DEFAULT 0,
enabled numeric DEFAULT 1,
sort_order integer DEFAULT 0,
allow_balance_pay numeric DEFAULT 1,
allow_wallet_overflow numeric DEFAULT 1,
stripe_price_id varchar(128) DEFAULT '',
creem_product_id varchar(128) DEFAULT '',
waffo_pancake_product_id varchar(128) DEFAULT '',
max_purchase_per_user integer DEFAULT 0,
upgrade_group varchar(64) DEFAULT '',
downgrade_group varchar(64) DEFAULT '',
total_amount bigint NOT NULL DEFAULT 0,
quota_reset_period varchar(16) DEFAULT 'never',
quota_reset_custom_seconds bigint DEFAULT 0,
created_at bigint,
updated_at bigint
)`).Error
			if stripeLegacyInventoryTestMigrateErr != nil {
				return
			}
		}
		stripeLegacyInventoryTestMigrateErr = model.DB.AutoMigrate(
			&model.Option{},
			&model.SubscriptionOrder{},
			&model.PaymentOrder{},
			&model.PaymentEvent{},
			&model.UserSubscription{},
			&model.StripeLegacySubscription{},
			&model.StripeLegacyInvoice{},
		)
	})
	require.NoError(t, stripeLegacyInventoryTestMigrateErr)
	require.NoError(t, model.DB.Exec("DELETE FROM stripe_legacy_invoices").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM stripe_legacy_subscriptions").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM subscription_orders").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM subscription_plans").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM user_subscriptions").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM payment_events").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM payment_orders").Error)
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM stripe_legacy_invoices")
		model.DB.Exec("DELETE FROM stripe_legacy_subscriptions")
		model.DB.Exec("DELETE FROM subscription_orders")
		model.DB.Exec("DELETE FROM subscription_plans")
		model.DB.Exec("DELETE FROM user_subscriptions")
		model.DB.Exec("DELETE FROM payment_events")
		model.DB.Exec("DELETE FROM payment_orders")
		model.DB.Exec("DELETE FROM users")
	})

	originalAPISecret := setting.StripeApiSecret
	originalWebhookSecret := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalPreviousExpiry := setting.StripeWebhookSecretPreviousExpiresAt
	originalAccount := setting.StripeAccountId
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		setting.StripeWebhookSecret = originalWebhookSecret
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeWebhookSecretPreviousExpiresAt = originalPreviousExpiry
		setting.StripeAccountId = originalAccount
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
	})
	setting.StripeApiSecret = "sk_test_inventory"
	setting.StripeWebhookSecret = "whsec_inventory"
	setting.StripeWebhookSecretPrevious = ""
	setting.StripeWebhookSecretPreviousExpiresAt = 0
	setting.StripeAccountId = ""
	setting.StripeWebhookCredentialLivemode = "test"
}

func verifiedStripeInventoryEvent(t *testing.T, payload []byte) *NormalizedPaymentEvent {
	t.Helper()
	signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{
		Payload: payload,
		Secret:  setting.StripeWebhookSecret,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(string(payload)))
	request.Header.Set("Stripe-Signature", signed.Header)
	event, err := VerifyPaymentWebhook(model.PaymentProviderStripe, request)
	require.NoError(t, err)
	return event
}

func TestStripeLegacySubscriptionSnapshotAllowsNilEventData(t *testing.T) {
	snapshot := stripeLegacySubscriptionSnapshot(&stripe.Subscription{
		ID:       "sub_api_snapshot",
		Livemode: false,
		Status:   stripe.SubscriptionStatusActive,
	}, stripe.Event{
		Created:  1_721_304_000,
		Livemode: false,
	}, model.StripeLegacySyncSourceAPI, true)

	assert.Equal(t, "sub_api_snapshot", snapshot.StripeSubscriptionID)
	assert.Equal(t, int64(1_721_304_000), snapshot.StateObservedAt)
	assert.Empty(t, snapshot.LastStripePayloadDigest)
}

func TestStripeLegacyInvoiceSnapshotAllowsNilEventData(t *testing.T) {
	snapshot := stripeLegacyInvoiceSnapshot(&stripe.Invoice{
		ID:       "in_api_snapshot",
		Livemode: false,
		Status:   stripe.InvoiceStatusOpen,
	}, stripe.Event{
		Created:  1_721_304_001,
		Livemode: false,
	})

	assert.Equal(t, "in_api_snapshot", snapshot.StripeInvoiceID)
	assert.Equal(t, int64(1_721_304_001), snapshot.StateObservedAt)
	assert.Empty(t, snapshot.LastStripePayloadDigest)
	assert.Empty(t, snapshot.StripeSubscriptionID)
}

func TestStripeRecurringCheckoutPaidIsAuthorityConfirmedAndQuarantined(t *testing.T) {
	requested := false
	configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, request *http.Request) {
		requested = true
		assert.Equal(t, "/v1/checkout/sessions/cs_recurring_checkout", request.URL.Path)
		assert.Empty(t, request.Header.Get("Stripe-Account"))
		assert.True(t, strings.HasPrefix(request.Header.Get("Authorization"), "Bearer sk_test_inventory"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"cs_recurring_checkout","object":"checkout.session","livemode":false,
			"mode":"subscription","subscription":"sub_recurring_checkout",
			"client_reference_id":"legacy-recurring-trade","customer":"cus_recurring_checkout",
			"amount_total":1000,"currency":"usd","payment_status":"paid","status":"complete",
			"metadata":{"trade_no":"legacy-recurring-trade"}}
		`))
	})
	prepareStripeLegacyInventoryTest(t)
	user := &model.User{Id: 992001, Username: "stripe_recurring_checkout", StripeCustomer: "cus_recurring_checkout"}
	plan := &model.SubscriptionPlan{Id: 992002, Title: "Recurring legacy", Currency: "USD", PriceAmount: 10, Enabled: true}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, model.DB.Create(plan).Error)
	require.NoError(t, model.DB.Create(&model.SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, TradeNo: "legacy-recurring-trade",
		Status: "pending", PaymentProvider: model.PaymentProviderStripe,
	}).Error)

	payload := []byte(fmt.Sprintf(`{
		"id":"evt_recurring_checkout","object":"event","api_version":%q,
		"created":1721304100,"livemode":false,"type":"checkout.session.completed",
		"data":{"object":{"id":"cs_recurring_checkout","object":"checkout.session",
			"mode":"subscription","subscription":"sub_recurring_checkout",
			"client_reference_id":"legacy-recurring-trade","customer":"cus_recurring_checkout",
			"amount_total":1000,"currency":"usd","payment_status":"paid","status":"complete",
			"metadata":{"trade_no":"legacy-recurring-trade"}}}
	}`, stripe.APIVersion))
	event := verifiedStripeInventoryEvent(t, payload)
	assert.False(t, event.Paid)
	assert.False(t, event.Failed)
	assert.False(t, event.Expired)
	assert.True(t, event.ManualReview)
	assert.Equal(t, model.PaymentProviderStateStripeLegacyRecurringCheckoutPaid, event.ProviderState)
	assert.Equal(t, "stripe:sub_recurring_checkout", event.ProviderResourceKey)
	require.NoError(t, RecordVerifiedPaymentWebhookReceived(event))
	require.NoError(t, ValidateVerifiedPaymentWebhook(t.Context(), model.PaymentProviderStripe, event))
	assert.True(t, requested)
	settlement, err := ProcessNormalizedPaymentEvent(event)
	require.ErrorIs(t, err, model.ErrPaymentManualReview)
	require.NotNil(t, settlement)
	assert.True(t, settlement.ManualReview)
	assert.Nil(t, settlement.Order)
	_, err = model.GetStripeLegacySubscriptionByStripeID("sub_recurring_checkout")
	assert.Error(t, err)
	require.NoError(t, ProcessVerifiedPaymentWebhook(t.Context(), model.PaymentProviderStripe, event))

	inventory, err := model.GetStripeLegacySubscriptionByStripeID("sub_recurring_checkout")
	require.NoError(t, err)
	assert.Equal(t, "cs_recurring_checkout", inventory.CheckoutSessionID)
	assert.Equal(t, model.StripeLegacyMappingMapped, inventory.MappingStatus)
	require.NotNil(t, inventory.UserID)
	require.NotNil(t, inventory.SubscriptionPlanID)
	assert.Equal(t, user.Id, *inventory.UserID)
	assert.Equal(t, plan.Id, *inventory.SubscriptionPlanID)
	var storedEvent model.PaymentEvent
	require.NoError(t, model.DB.Where("provider = ? AND event_key = ?", model.PaymentProviderStripe, "evt_recurring_checkout").First(&storedEvent).Error)
	assert.Equal(t, model.PaymentEventStatusManualReview, storedEvent.Status)
	assert.Equal(t, model.PaymentReviewCodeStripeLegacyRecurringCheckoutPaid, storedEvent.ReviewCode)
	assert.Zero(t, storedEvent.PaymentOrderID)
	var storedOrder model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", "legacy-recurring-trade").First(&storedOrder).Error)
	assert.Equal(t, common.TopUpStatusPending, storedOrder.Status)
	var entitlementCount int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&entitlementCount).Error)
	assert.Zero(t, entitlementCount)
}

func TestStripeZeroTotalRecurringCheckoutIsAuthorityConfirmedInventoryOnly(t *testing.T) {
	requested := false
	configureStripeAccountConfirmationTest(t, func(w http.ResponseWriter, request *http.Request) {
		requested = true
		assert.Equal(t, "/v1/checkout/sessions/cs_recurring_trial", request.URL.Path)
		assert.Empty(t, request.Header.Get("Stripe-Account"))
		assert.True(t, strings.HasPrefix(request.Header.Get("Authorization"), "Bearer sk_test_inventory"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"cs_recurring_trial","object":"checkout.session","livemode":false,
			"mode":"subscription","subscription":"sub_recurring_trial",
			"client_reference_id":"legacy-recurring-trial","customer":"cus_recurring_trial",
			"amount_total":0,"currency":"usd","payment_status":"paid","status":"complete",
			"metadata":{"trade_no":"legacy-recurring-trial"}}
		`))
	})
	prepareStripeLegacyInventoryTest(t)
	user := &model.User{Id: 992011, Username: "stripe_trial", StripeCustomer: "cus_recurring_trial"}
	plan := &model.SubscriptionPlan{Id: 992012, Title: "Recurring trial", Currency: "USD", PriceAmount: 0, Enabled: true}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, model.DB.Create(plan).Error)
	require.NoError(t, model.DB.Create(&model.SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, Money: 0, TradeNo: "legacy-recurring-trial",
		PaymentMethod: model.PaymentMethodStripe, PaymentProvider: model.PaymentProviderStripe,
		Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp() - 100,
	}).Error)

	payload := []byte(fmt.Sprintf(`{
		"id":"evt_recurring_trial","object":"event","api_version":%q,
		"created":1721304150,"livemode":false,"type":"checkout.session.completed",
		"data":{"object":{"id":"cs_recurring_trial","object":"checkout.session",
			"mode":"subscription","subscription":"sub_recurring_trial",
			"client_reference_id":"legacy-recurring-trial","customer":"cus_recurring_trial",
			"amount_total":0,"currency":"usd","payment_status":"paid","status":"complete",
			"metadata":{"trade_no":"legacy-recurring-trial"}}}
	}`, stripe.APIVersion))
	event := verifiedStripeInventoryEvent(t, payload)
	assert.False(t, event.Paid)
	assert.False(t, event.ManualReview)
	assert.Zero(t, event.PaidAmountMinor)
	assert.Equal(t, model.PaymentProviderStateStripeLegacyRecurringCheckoutPaid, event.ProviderState)
	assert.Equal(t, "stripe:sub_recurring_trial", event.ProviderResourceKey)
	require.NoError(t, RecordVerifiedPaymentWebhookReceived(event))
	require.NoError(t, ValidateVerifiedPaymentWebhook(t.Context(), model.PaymentProviderStripe, event))
	assert.True(t, requested)
	settlement, err := ProcessNormalizedPaymentEvent(event)
	require.NoError(t, err)
	require.NotNil(t, settlement)
	assert.False(t, settlement.ManualReview)
	assert.Nil(t, settlement.Order)
	require.NoError(t, ProcessVerifiedPaymentWebhook(t.Context(), model.PaymentProviderStripe, event))

	inventory, err := model.GetStripeLegacySubscriptionByStripeID("sub_recurring_trial")
	require.NoError(t, err)
	assert.Equal(t, "cs_recurring_trial", inventory.CheckoutSessionID)
	assert.Equal(t, "legacy-recurring-trial", inventory.TradeNo)
	assert.Equal(t, "cus_recurring_trial", inventory.StripeCustomerID)
	var storedEvent model.PaymentEvent
	require.NoError(t, model.DB.Where("provider = ? AND event_key = ?", model.PaymentProviderStripe,
		"evt_recurring_trial").First(&storedEvent).Error)
	assert.Equal(t, model.PaymentEventStatusProcessed, storedEvent.Status)
	assert.Empty(t, storedEvent.ReviewCode)
	assert.Zero(t, storedEvent.PaymentOrderID)
	var canonicalCount, entitlementCount int64
	require.NoError(t, model.DB.Model(&model.PaymentOrder{}).Where("trade_no = ?", "legacy-recurring-trial").Count(&canonicalCount).Error)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&entitlementCount).Error)
	assert.Zero(t, canonicalCount)
	assert.Zero(t, entitlementCount)
	var storedLegacy model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", "legacy-recurring-trial").First(&storedLegacy).Error)
	assert.Equal(t, common.TopUpStatusPending, storedLegacy.Status)
	assert.Nil(t, storedLegacy.PaymentOrderId)
}

func TestStripeSubscriptionAndInvoiceWebhooksUpdateInventoryOnly(t *testing.T) {
	prepareStripeLegacyInventoryTest(t)
	user := &model.User{Id: 992003, Username: "stripe_lifecycle_user", StripeCustomer: "cus_lifecycle_user"}
	plan := &model.SubscriptionPlan{
		Id: 992004, Title: "Lifecycle plan", Currency: "USD", PriceAmount: 20,
		StripePriceId: "price_lifecycle_plan", Enabled: true,
	}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, model.DB.Create(plan).Error)

	subscriptionPayload := []byte(fmt.Sprintf(`{
		"id":"evt_subscription_updated","object":"event","api_version":%q,
		"created":1721304200,"livemode":false,"type":"customer.subscription.updated",
		"data":{"object":{"id":"sub_lifecycle","object":"subscription","customer":"cus_lifecycle_user",
			"status":"active","currency":"usd","collection_method":"charge_automatically",
			"created":1721200000,"cancel_at_period_end":false,
			"items":{"object":"list","data":[{"id":"si_lifecycle","object":"subscription_item",
				"current_period_start":1721300000,"current_period_end":1723978400,"quantity":1,
				"price":{"id":"price_lifecycle_plan","object":"price","currency":"usd","product":"prod_lifecycle"}}]},
			"metadata":{}}}
	}`, stripe.APIVersion))
	event := verifiedStripeInventoryEvent(t, subscriptionPayload)
	assert.False(t, event.Paid)
	require.NoError(t, ProcessVerifiedPaymentWebhook(t.Context(), model.PaymentProviderStripe, event))
	require.NoError(t, ProcessVerifiedPaymentWebhook(t.Context(), model.PaymentProviderStripe, event))

	inventory, err := model.GetStripeLegacySubscriptionByStripeID("sub_lifecycle")
	require.NoError(t, err)
	assert.Equal(t, "active", inventory.Status)
	assert.Equal(t, model.StripeLegacyMappingMapped, inventory.MappingStatus)
	assert.Equal(t, []string{"price_lifecycle_plan"}, inventory.PriceIDs())

	invoicePayload := []byte(fmt.Sprintf(`{
		"id":"evt_invoice_paid","object":"event","api_version":%q,
		"created":1721304300,"livemode":false,"type":"invoice.paid",
		"data":{"object":{"id":"in_lifecycle","object":"invoice","customer":"cus_lifecycle_user",
			"status":"paid","billing_reason":"subscription_cycle","currency":"usd",
			"amount_due":2000,"amount_paid":2000,"amount_remaining":0,"attempt_count":1,
			"period_start":1721300000,"period_end":1723978400,"created":1721304200,
			"parent":{"type":"subscription_details","subscription_details":{"subscription":"sub_lifecycle","metadata":{}}}}}
	}`, stripe.APIVersion))
	invoiceEvent := verifiedStripeInventoryEvent(t, invoicePayload)
	require.NoError(t, ProcessVerifiedPaymentWebhook(t.Context(), model.PaymentProviderStripe, invoiceEvent))
	require.NoError(t, ProcessVerifiedPaymentWebhook(t.Context(), model.PaymentProviderStripe, invoiceEvent))

	inventory, err = model.GetStripeLegacySubscriptionByStripeID("sub_lifecycle")
	require.NoError(t, err)
	assert.Equal(t, "in_lifecycle", inventory.LatestInvoiceID)
	assert.Equal(t, "paid", inventory.LatestInvoiceStatus)
	assert.True(t, inventory.LatestInvoicePaid)
	assert.Equal(t, int64(2000), inventory.LatestInvoiceAmountDue)
	assert.Equal(t, int64(2000), inventory.LatestInvoiceAmountPaid)
	var subscriptionInventoryCount, invoiceInventoryCount int64
	require.NoError(t, model.DB.Model(&model.StripeLegacySubscription{}).Where("stripe_subscription_id = ?", "sub_lifecycle").Count(&subscriptionInventoryCount).Error)
	require.NoError(t, model.DB.Model(&model.StripeLegacyInvoice{}).Where("stripe_invoice_id = ?", "in_lifecycle").Count(&invoiceInventoryCount).Error)
	assert.EqualValues(t, 1, subscriptionInventoryCount)
	assert.EqualValues(t, 1, invoiceInventoryCount)

	var localEntitlementCount int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&localEntitlementCount).Error)
	assert.Zero(t, localEntitlementCount)
}

func TestStripeSubscriptionDeletionDoesNotCancelLocalEntitlement(t *testing.T) {
	prepareStripeLegacyInventoryTest(t)
	user := &model.User{Id: 992005, Username: "stripe_delete_user", StripeCustomer: "cus_delete_user"}
	plan := &model.SubscriptionPlan{
		Id: 992006, Title: "Delete compatibility plan", Currency: "USD", PriceAmount: 20,
		StripePriceId: "price_delete_plan", Enabled: true,
	}
	require.NoError(t, model.DB.Create(user).Error)
	require.NoError(t, model.DB.Create(plan).Error)
	entitlement := &model.UserSubscription{
		UserId: user.Id, PlanId: plan.Id, AmountTotal: 5000,
		StartTime: 1721200000, EndTime: 1821200000, Status: "active", Source: "order",
	}
	require.NoError(t, model.DB.Create(entitlement).Error)

	payload := []byte(fmt.Sprintf(`{
		"id":"evt_subscription_deleted","object":"event","api_version":%q,
		"created":1721304500,"livemode":false,"type":"customer.subscription.deleted",
		"data":{"object":{"id":"sub_deleted_compat","object":"subscription","customer":"cus_delete_user",
			"status":"canceled","currency":"usd","collection_method":"charge_automatically","created":1721200000,
			"canceled_at":1721304400,"ended_at":1721304400,
			"items":{"object":"list","data":[{"id":"si_delete","object":"subscription_item","quantity":1,
				"price":{"id":"price_delete_plan","object":"price","currency":"usd"}}]}}}
	}`, stripe.APIVersion))
	event := verifiedStripeInventoryEvent(t, payload)
	require.NoError(t, ProcessVerifiedPaymentWebhook(t.Context(), model.PaymentProviderStripe, event))

	var stored model.UserSubscription
	require.NoError(t, model.DB.First(&stored, entitlement.Id).Error)
	assert.Equal(t, "active", stored.Status)
	assert.Equal(t, int64(1821200000), stored.EndTime)
	assert.Equal(t, int64(5000), stored.AmountTotal)

	inventory, err := model.GetStripeLegacySubscriptionByStripeID("sub_deleted_compat")
	require.NoError(t, err)
	assert.Equal(t, "canceled", inventory.Status)
}

func TestStripeLegacyInvoicePayloadSupportsPreParentSubscriptionField(t *testing.T) {
	prepareStripeLegacyInventoryTest(t)
	payload := []byte(fmt.Sprintf(`{
		"id":"evt_invoice_legacy","object":"event","api_version":%q,
		"created":1721304400,"livemode":false,"type":"invoice.payment_failed",
		"data":{"object":{"id":"in_legacy_parent","object":"invoice","subscription":"sub_legacy_parent",
			"customer":"cus_legacy_parent","status":"open","currency":"usd","amount_due":900,"amount_paid":0}}
	}`, stripe.APIVersion))
	event := verifiedStripeInventoryEvent(t, payload)
	require.NoError(t, ProcessVerifiedPaymentWebhook(t.Context(), model.PaymentProviderStripe, event))

	inventory, err := model.GetStripeLegacySubscriptionByStripeID("sub_legacy_parent")
	require.NoError(t, err)
	assert.Equal(t, "in_legacy_parent", inventory.LatestInvoiceID)
}

func TestSyncStripeLegacySubscriptionsRefreshesRemotePaymentConfiguration(t *testing.T) {
	prepareStripeLegacyInventoryTest(t)

	keys := []string{model.PaymentConfigurationVersionOptionKey, "StripeApiSecret"}
	var previousOptions []model.Option
	for _, key := range keys {
		var option model.Option
		result := model.DB.Where(&model.Option{Key: key}).Limit(1).Find(&option)
		require.NoError(t, result.Error)
		if result.RowsAffected == 1 {
			previousOptions = append(previousOptions, option)
		}
		require.NoError(t, model.DB.Delete(&model.Option{Key: key}).Error)
	}

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	previousVersion, versionExisted := common.OptionMap[model.PaymentConfigurationVersionOptionKey]
	previousSecret, secretExisted := common.OptionMap["StripeApiSecret"]
	common.OptionMap[model.PaymentConfigurationVersionOptionKey] = "1"
	common.OptionMap["StripeApiSecret"] = "sk_test_stale_node"
	common.OptionMapRWMutex.Unlock()
	setting.StripeApiSecret = "sk_test_stale_node"

	require.NoError(t, model.DB.Create([]model.Option{
		{Key: model.PaymentConfigurationVersionOptionKey, Value: "2"},
		{Key: "StripeApiSecret", Value: ""},
	}).Error)
	t.Cleanup(func() {
		for _, key := range keys {
			_ = model.DB.Delete(&model.Option{Key: key}).Error
		}
		if len(previousOptions) > 0 {
			_ = model.DB.Create(&previousOptions).Error
		}
		common.OptionMapRWMutex.Lock()
		if versionExisted {
			common.OptionMap[model.PaymentConfigurationVersionOptionKey] = previousVersion
		} else {
			delete(common.OptionMap, model.PaymentConfigurationVersionOptionKey)
		}
		if secretExisted {
			common.OptionMap["StripeApiSecret"] = previousSecret
		} else {
			delete(common.OptionMap, "StripeApiSecret")
		}
		common.OptionMapRWMutex.Unlock()
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := SyncStripeLegacySubscriptions(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid Stripe API secret")
	assert.Empty(t, setting.StripeApiSecret)
	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "2", common.OptionMap[model.PaymentConfigurationVersionOptionKey])
	common.OptionMapRWMutex.RUnlock()
}

func TestCancelStripeLegacySubscriptionAtPeriodEndVerifiesIdentityAndPersistsAudit(t *testing.T) {
	prepareStripeLegacyInventoryTest(t)
	t.Setenv(setting.StripeTestModeEnabledEnv, "true")
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentOperationsAudit{}))
	require.NoError(t, model.DB.Exec(
		"DELETE FROM payment_operations_audits WHERE action = ?",
		model.PaymentOperationsActionStripeLegacySubscriptionCancel,
	).Error)
	t.Cleanup(func() {
		_ = model.DB.Exec(
			"DELETE FROM payment_operations_audits WHERE action = ?",
			model.PaymentOperationsActionStripeLegacySubscriptionCancel,
		).Error
	})
	originalCredentialAccountID := setting.StripeCredentialAccountId
	originalCredentialMode := setting.StripeCredentialLivemode
	t.Cleanup(func() {
		setting.StripeCredentialAccountId = originalCredentialAccountID
		setting.StripeCredentialLivemode = originalCredentialMode
	})
	setting.StripeCredentialAccountId = "acct_inventorydirect"
	setting.StripeCredentialLivemode = "test"

	now := common.GetTimestamp()
	inventory, err := model.UpsertStripeLegacySubscription(model.StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_service_cancel",
		StripeCustomerID:     "cus_service_cancel",
		Status:               "active",
		StateObservedAt:      now,
		FullState:            true,
		SyncSource:           model.StripeLegacySyncSourceAPI,
	})
	require.NoError(t, err)
	fake := &stripeLegacyCancellationFakeAPI{
		account: &stripe.Account{ID: "acct_inventorydirect"},
		subscription: &stripe.Subscription{
			ID:                "sub_service_cancel",
			Customer:          &stripe.Customer{ID: "cus_service_cancel"},
			Status:            stripe.SubscriptionStatusActive,
			CancelAtPeriodEnd: true,
			CancelAt:          now + 3600,
			Livemode:          false,
		},
	}
	result, err := cancelStripeLegacySubscriptionAtPeriodEnd(t.Context(), StripeLegacySubscriptionCancellationInput{
		InventoryID: inventory.ID, ExpectedUpdatedAt: inventory.UpdatedAt,
		AdminID: 992900, ActorIP: "127.0.0.1", Reason: "stop legacy automatic renewal",
	}, func(secret string) stripeLegacySubscriptionAPI {
		assert.Equal(t, "sk_test_inventory", secret)
		return fake
	})
	require.NoError(t, err)
	require.NotNil(t, result.Subscription)
	assert.True(t, result.Subscription.CancelAtPeriodEnd)
	assert.False(t, result.Duplicate)
	assert.Empty(t, fake.retrievedAccountID)
	assert.Equal(t, "sub_service_cancel", fake.updatedSubscriptionID)
	assert.Equal(t, 1, fake.updateCalls)
	require.NotNil(t, fake.updateParams)
	require.NotNil(t, fake.updateParams.CancelAtPeriodEnd)
	assert.True(t, *fake.updateParams.CancelAtPeriodEnd)
	require.NotNil(t, fake.updateParams.IdempotencyKey)
	assert.Contains(t, *fake.updateParams.IdempotencyKey, "legacy-sub-cancel:")
	assert.Nil(t, fake.updateParams.StripeAccount)

	var auditCount int64
	require.NoError(t, model.DB.Model(&model.PaymentOperationsAudit{}).Where(
		"action = ? AND subject_id = ?",
		model.PaymentOperationsActionStripeLegacySubscriptionCancel, inventory.ID,
	).Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)

	retry, err := cancelStripeLegacySubscriptionAtPeriodEnd(t.Context(), StripeLegacySubscriptionCancellationInput{
		InventoryID: inventory.ID, ExpectedUpdatedAt: inventory.UpdatedAt,
		AdminID: 992900, ActorIP: "127.0.0.1", Reason: "stop legacy automatic renewal",
	}, func(string) stripeLegacySubscriptionAPI { return fake })
	require.NoError(t, err)
	assert.True(t, retry.Duplicate)
	assert.Equal(t, 1, fake.updateCalls)
}

func TestCancelStripeLegacySubscriptionAtPeriodEndRecoversAfterLostResponseAndWebhook(t *testing.T) {
	prepareStripeLegacyInventoryTest(t)
	t.Setenv(setting.StripeTestModeEnabledEnv, "true")
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentOperationsAudit{}))
	require.NoError(t, model.DB.Exec(
		"DELETE FROM payment_operations_audits WHERE action = ?",
		model.PaymentOperationsActionStripeLegacySubscriptionCancel,
	).Error)
	t.Cleanup(func() {
		_ = model.DB.Exec(
			"DELETE FROM payment_operations_audits WHERE action = ?",
			model.PaymentOperationsActionStripeLegacySubscriptionCancel,
		).Error
	})
	originalCredentialAccountID := setting.StripeCredentialAccountId
	originalCredentialMode := setting.StripeCredentialLivemode
	t.Cleanup(func() {
		setting.StripeCredentialAccountId = originalCredentialAccountID
		setting.StripeCredentialLivemode = originalCredentialMode
	})
	setting.StripeCredentialAccountId = "acct_inventoryrecovery"
	setting.StripeCredentialLivemode = "test"

	now := common.GetTimestamp()
	inventory, err := model.UpsertStripeLegacySubscription(model.StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_service_cancel_recovery",
		StripeCustomerID:     "cus_service_cancel_recovery",
		Status:               "active",
		StateObservedAt:      now,
		FullState:            true,
		SyncSource:           model.StripeLegacySyncSourceAPI,
	})
	require.NoError(t, err)
	expectedUpdatedAt := inventory.UpdatedAt
	reason := strings.Repeat("x", 512)
	fake := &stripeLegacyCancellationFakeAPI{
		account: &stripe.Account{ID: "acct_inventoryrecovery"},
	}
	fake.updateFn = func(_ context.Context, _ string, _ *stripe.SubscriptionUpdateParams) (*stripe.Subscription, error) {
		if fake.updateCalls == 1 {
			_, upsertErr := model.UpsertStripeLegacySubscription(model.StripeLegacySubscriptionSnapshot{
				StripeSubscriptionID: "sub_service_cancel_recovery",
				StripeCustomerID:     "cus_service_cancel_recovery",
				Status:               "active",
				CancelAtPeriodEnd:    true,
				CancelAt:             now + 3600,
				StateObservedAt:      now + 1,
				FullState:            true,
				SyncSource:           model.StripeLegacySyncSourceWebhook,
			})
			if upsertErr != nil {
				return nil, upsertErr
			}
			return nil, errors.New("connection reset after Stripe accepted the update")
		}
		return &stripe.Subscription{
			ID:                "sub_service_cancel_recovery",
			Customer:          &stripe.Customer{ID: "cus_service_cancel_recovery"},
			Status:            stripe.SubscriptionStatusActive,
			CancelAtPeriodEnd: true,
			CancelAt:          now + 3600,
			Livemode:          false,
		}, nil
	}
	input := StripeLegacySubscriptionCancellationInput{
		InventoryID: inventory.ID, ExpectedUpdatedAt: expectedUpdatedAt,
		AdminID: 992902, ActorIP: "127.0.0.1", Reason: reason,
	}

	_, err = cancelStripeLegacySubscriptionAtPeriodEnd(t.Context(), input, func(string) stripeLegacySubscriptionAPI { return fake })
	assert.ErrorIs(t, err, ErrStripeLegacyCancellationUnavailable)
	var auditCount int64
	require.NoError(t, model.DB.Model(&model.PaymentOperationsAudit{}).Where(
		"action = ? AND subject_id = ?",
		model.PaymentOperationsActionStripeLegacySubscriptionCancel, inventory.ID,
	).Count(&auditCount).Error)
	assert.Zero(t, auditCount)
	recoveredInventory, err := model.GetStripeLegacySubscriptionByID(inventory.ID)
	require.NoError(t, err)
	assert.NotEqual(t, expectedUpdatedAt, recoveredInventory.UpdatedAt)
	assert.True(t, recoveredInventory.CancelAtPeriodEnd)

	result, err := cancelStripeLegacySubscriptionAtPeriodEnd(t.Context(), input, func(string) stripeLegacySubscriptionAPI { return fake })
	require.NoError(t, err)
	require.NotNil(t, result.Subscription)
	assert.False(t, result.Duplicate)
	assert.True(t, result.Subscription.CancelAtPeriodEnd)
	assert.Equal(t, 2, fake.updateCalls)
	require.Len(t, fake.updateParamsHistory, 2)
	require.NotNil(t, fake.updateParamsHistory[0].IdempotencyKey)
	require.NotNil(t, fake.updateParamsHistory[1].IdempotencyKey)
	assert.Equal(t, *fake.updateParamsHistory[0].IdempotencyKey, *fake.updateParamsHistory[1].IdempotencyKey)
	require.NoError(t, model.DB.Model(&model.PaymentOperationsAudit{}).Where(
		"action = ? AND subject_id = ?",
		model.PaymentOperationsActionStripeLegacySubscriptionCancel, inventory.ID,
	).Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)

	retry, err := cancelStripeLegacySubscriptionAtPeriodEnd(t.Context(), input, func(string) stripeLegacySubscriptionAPI { return fake })
	require.NoError(t, err)
	assert.True(t, retry.Duplicate)
	assert.Equal(t, 2, fake.updateCalls)
}

func TestCancelStripeLegacySubscriptionAtPeriodEndRejectsAccountMismatchBeforeUpdate(t *testing.T) {
	prepareStripeLegacyInventoryTest(t)
	t.Setenv(setting.StripeTestModeEnabledEnv, "true")
	originalCredentialAccountID := setting.StripeCredentialAccountId
	originalCredentialMode := setting.StripeCredentialLivemode
	t.Cleanup(func() {
		setting.StripeCredentialAccountId = originalCredentialAccountID
		setting.StripeCredentialLivemode = originalCredentialMode
	})
	setting.StripeCredentialAccountId = "acct_expectedinventory"
	setting.StripeCredentialLivemode = "test"

	inventory, err := model.UpsertStripeLegacySubscription(model.StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_service_identity_mismatch",
		StripeCustomerID:     "cus_service_identity_mismatch",
		Status:               "active",
		StateObservedAt:      common.GetTimestamp(),
		FullState:            true,
		SyncSource:           model.StripeLegacySyncSourceAPI,
	})
	require.NoError(t, err)
	fake := &stripeLegacyCancellationFakeAPI{account: &stripe.Account{ID: "acct_unexpectedinventory"}}
	_, err = cancelStripeLegacySubscriptionAtPeriodEnd(t.Context(), StripeLegacySubscriptionCancellationInput{
		InventoryID: inventory.ID, ExpectedUpdatedAt: inventory.UpdatedAt,
		AdminID: 992901, ActorIP: "127.0.0.1", Reason: "stop legacy automatic renewal",
	}, func(string) stripeLegacySubscriptionAPI { return fake })
	assert.ErrorIs(t, err, ErrStripeLegacyCancellationIdentityMismatch)
	assert.Empty(t, fake.updatedSubscriptionID)
	assert.Nil(t, fake.updateParams)
}
