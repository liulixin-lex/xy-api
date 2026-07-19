package service

import (
	"context"
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
	require.NoError(t, model.DB.Exec("DELETE FROM payment_orders").Error)
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM stripe_legacy_invoices")
		model.DB.Exec("DELETE FROM stripe_legacy_subscriptions")
		model.DB.Exec("DELETE FROM subscription_orders")
		model.DB.Exec("DELETE FROM subscription_plans")
		model.DB.Exec("DELETE FROM user_subscriptions")
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

func TestStripeRecurringCheckoutIsInventoryOnlyAndBindsLocalOrder(t *testing.T) {
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
	require.NoError(t, ProcessVerifiedPaymentWebhook(t.Context(), model.PaymentProviderStripe, event))

	inventory, err := model.GetStripeLegacySubscriptionByStripeID("sub_recurring_checkout")
	require.NoError(t, err)
	assert.Equal(t, "cs_recurring_checkout", inventory.CheckoutSessionID)
	assert.Equal(t, model.StripeLegacyMappingMapped, inventory.MappingStatus)
	require.NotNil(t, inventory.UserID)
	require.NotNil(t, inventory.SubscriptionPlanID)
	assert.Equal(t, user.Id, *inventory.UserID)
	assert.Equal(t, plan.Id, *inventory.SubscriptionPlanID)
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
