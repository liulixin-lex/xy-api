package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertStripeLegacySubscriptionMapsCustomerAndPrice(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 991001, Username: "stripe_inventory_user", StripeCustomer: "cus_inventory_user"}
	plan := &SubscriptionPlan{Id: 991002, Title: "Legacy plan", PriceAmount: 12, Currency: "USD", StripePriceId: "price_inventory_plan", Enabled: true}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, DB.Create(plan).Error)

	inventory, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_inventory_1",
		StripeCustomerID:     "cus_inventory_user",
		PriceIDs:             []string{"price_inventory_plan"},
		Status:               "active",
		Currency:             "USD",
		StateObservedAt:      100,
		FullState:            true,
		SyncSource:           StripeLegacySyncSourceAPI,
	})
	require.NoError(t, err)
	assert.Equal(t, StripeLegacyMappingMapped, inventory.MappingStatus)
	require.NotNil(t, inventory.UserID)
	require.NotNil(t, inventory.SubscriptionPlanID)
	assert.Equal(t, user.Id, *inventory.UserID)
	assert.Equal(t, plan.Id, *inventory.SubscriptionPlanID)
	assert.Equal(t, []string{"price_inventory_plan"}, inventory.PriceIDs())

	resynced, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_inventory_1",
		StripeCustomerID:     "cus_inventory_user",
		PriceIDs:             []string{"price_inventory_plan"},
		Status:               "active",
		Currency:             "USD",
		StateObservedAt:      200,
		FullState:            true,
		SyncSource:           StripeLegacySyncSourceAPI,
	})
	require.NoError(t, err)
	assert.Equal(t, inventory.ID, resynced.ID)
	var count int64
	require.NoError(t, DB.Model(&StripeLegacySubscription{}).Where("stripe_subscription_id = ?", "sub_inventory_1").Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestNormalizeStripePriceIDsRetainsHistoricalPlanIDs(t *testing.T) {
	assert.Equal(t, []string{"plan_legacy", "price_current"}, normalizeStripePriceIDs([]string{
		"price_current", "plan_legacy", "price_current", "invalid",
	}))
}

func TestStripeLegacyMappingPrefersCustomerAndPriceOverMetadataHints(t *testing.T) {
	truncateTables(t)
	customerUser := &User{Id: 991101, Username: "stripe_customer_owner", StripeCustomer: "cus_mapping_owner"}
	metadataUser := &User{Id: 991102, Username: "stripe_metadata_hint", AffCode: "stripe_metadata_hint_aff"}
	pricePlan := &SubscriptionPlan{Id: 991103, Title: "Price plan", Currency: "USD", StripePriceId: "price_mapping_owner", Enabled: true}
	metadataPlan := &SubscriptionPlan{Id: 991104, Title: "Metadata plan", Currency: "USD", Enabled: true}
	require.NoError(t, DB.Create(customerUser).Error)
	require.NoError(t, DB.Create(metadataUser).Error)
	require.NoError(t, DB.Create(pricePlan).Error)
	require.NoError(t, DB.Create(metadataPlan).Error)

	inventory, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_mapping_precedence",
		StripeCustomerID:     "cus_mapping_owner",
		MetadataUserID:       metadataUser.Id,
		MetadataPlanID:       metadataPlan.Id,
		PriceIDs:             []string{"price_mapping_owner"},
		Status:               "active",
		StateObservedAt:      100,
		FullState:            true,
	})
	require.NoError(t, err)
	require.NotNil(t, inventory.UserID)
	require.NotNil(t, inventory.SubscriptionPlanID)
	assert.Equal(t, customerUser.Id, *inventory.UserID)
	assert.Equal(t, pricePlan.Id, *inventory.SubscriptionPlanID)
}

func TestUpsertStripeLegacySubscriptionKeepsExplicitUnmappedStatus(t *testing.T) {
	truncateTables(t)
	inventory, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_inventory_unmapped",
		StripeCustomerID:     "cus_missing_user",
		PriceIDs:             []string{"price_missing_plan"},
		Status:               "past_due",
		StateObservedAt:      100,
		FullState:            true,
		SyncSource:           StripeLegacySyncSourceWebhook,
	})
	require.NoError(t, err)
	assert.Equal(t, StripeLegacyMappingUnmapped, inventory.MappingStatus)
	assert.Contains(t, inventory.MappingReason, "not mapped")
	assert.Nil(t, inventory.UserID)
	assert.Nil(t, inventory.SubscriptionPlanID)
}

func TestUpsertStripeLegacySubscriptionRejectsStaleLifecycleState(t *testing.T) {
	truncateTables(t)
	first, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_inventory_ordering",
		Status:               "active",
		StateObservedAt:      200,
		FullState:            true,
		LastStripeEventID:    "evt_newer",
		LastStripeEventType:  "customer.subscription.updated",
		SyncSource:           StripeLegacySyncSourceWebhook,
	})
	require.NoError(t, err)
	assert.Equal(t, "active", first.Status)

	stale, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_inventory_ordering",
		Status:               "canceled",
		StateObservedAt:      100,
		FullState:            true,
		LastStripeEventID:    "evt_old",
		LastStripeEventType:  "customer.subscription.deleted",
		SyncSource:           StripeLegacySyncSourceWebhook,
	})
	require.NoError(t, err)
	assert.Equal(t, "active", stale.Status)
	assert.Equal(t, "evt_newer", stale.LastStripeEventID)

	latest, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_inventory_ordering",
		Status:               "canceled",
		StateObservedAt:      300,
		FullState:            true,
		LastStripeEventID:    "evt_latest",
		LastStripeEventType:  "customer.subscription.deleted",
		SyncSource:           StripeLegacySyncSourceWebhook,
	})
	require.NoError(t, err)
	assert.Equal(t, "canceled", latest.Status)
	assert.Equal(t, "evt_latest", latest.LastStripeEventID)
}

func TestUpsertStripeLegacySubscriptionRetainsEventPayloadConflictForReview(t *testing.T) {
	truncateTables(t)
	first, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID:    "sub_inventory_conflict",
		Status:                  "active",
		StateObservedAt:         200,
		FullState:               true,
		LastStripeEventID:       "evt_conflict",
		LastStripePayloadDigest: "digest_a",
		SyncSource:              StripeLegacySyncSourceWebhook,
	})
	require.NoError(t, err)
	assert.Equal(t, "active", first.Status)

	conflict, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID:    "sub_inventory_conflict",
		Status:                  "canceled",
		StateObservedAt:         200,
		FullState:               true,
		LastStripeEventID:       "evt_conflict",
		LastStripePayloadDigest: "digest_b",
		SyncSource:              StripeLegacySyncSourceWebhook,
	})
	require.NoError(t, err)
	assert.Equal(t, "active", conflict.Status)
	assert.Equal(t, "stripe_event_payload_conflict", conflict.ReviewReason)
}

func TestUpsertStripeLegacySubscriptionPreservesPriceMappingWhenWebhookOmitsItems(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 991201, Username: "stripe_partial_user", StripeCustomer: "cus_partial_user"}
	plan := &SubscriptionPlan{Id: 991202, Title: "Partial plan", Currency: "USD", StripePriceId: "price_partial", Enabled: true}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, DB.Create(plan).Error)
	_, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_partial_items", StripeCustomerID: "cus_partial_user",
		PriceIDs: []string{"price_partial"}, Status: "active", StateObservedAt: 100, FullState: true,
	})
	require.NoError(t, err)

	inventory, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_partial_items", StripeCustomerID: "cus_partial_user",
		Status: "canceled", StateObservedAt: 200, FullState: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "canceled", inventory.Status)
	assert.Equal(t, []string{"price_partial"}, inventory.PriceIDs())
	assert.Equal(t, StripeLegacyMappingMapped, inventory.MappingStatus)
}

func TestUpsertStripeLegacyInvoiceUpdatesReadOnlyInventory(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 991003, Username: "stripe_invoice_user", StripeCustomer: "cus_invoice_user"}
	plan := &SubscriptionPlan{Id: 991004, Title: "Invoice plan", PriceAmount: 8, Currency: "USD", StripePriceId: "price_invoice_plan", Enabled: true}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, DB.Create(plan).Error)
	_, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_invoice_1", StripeCustomerID: "cus_invoice_user",
		PriceIDs: []string{"price_invoice_plan"}, Status: "active",
		StateObservedAt: 400, FullState: true, SyncSource: StripeLegacySyncSourceWebhook,
	})
	require.NoError(t, err)

	invoice, err := UpsertStripeLegacyInvoice(StripeLegacyInvoiceSnapshot{
		StripeInvoiceID:      "in_inventory_1",
		StripeSubscriptionID: "sub_invoice_1",
		StripeCustomerID:     "cus_invoice_user",
		Status:               "paid",
		Currency:             "USD",
		AmountPaid:           800,
		Paid:                 true,
		StateObservedAt:      500,
	})
	require.NoError(t, err)
	assert.True(t, invoice.Paid)
	require.NotNil(t, invoice.StripeLegacySubscriptionID)

	subscription, err := GetStripeLegacySubscriptionByStripeID("sub_invoice_1")
	require.NoError(t, err)
	assert.Equal(t, "in_inventory_1", subscription.LatestInvoiceID)
	assert.Equal(t, int64(800), subscription.LatestInvoiceAmountPaid)
	assert.True(t, subscription.LatestInvoicePaid)
	assert.Equal(t, StripeLegacyMappingMapped, subscription.MappingStatus)
}

func TestCheckoutPlaceholderUsesExistingSubscriptionOrderMapping(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 991005, Username: "stripe_checkout_user"}
	plan := &SubscriptionPlan{Id: 991006, Title: "Checkout plan", PriceAmount: 6, Currency: "USD", Enabled: true}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, DB.Create(plan).Error)
	require.NoError(t, DB.Create(&SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, TradeNo: "legacy-checkout-trade",
		Status: "pending", PaymentProvider: PaymentProviderStripe,
	}).Error)

	inventory, err := UpsertStripeLegacySubscription(StripeLegacySubscriptionSnapshot{
		StripeSubscriptionID: "sub_checkout_placeholder",
		TradeNo:              "legacy-checkout-trade",
		CheckoutSessionID:    "cs_checkout_placeholder",
		FullState:            false,
		SyncSource:           StripeLegacySyncSourceCheckout,
	})
	require.NoError(t, err)
	assert.Equal(t, StripeLegacyMappingMapped, inventory.MappingStatus)
	require.NotNil(t, inventory.UserID)
	require.NotNil(t, inventory.SubscriptionPlanID)
	assert.Equal(t, user.Id, *inventory.UserID)
	assert.Equal(t, plan.Id, *inventory.SubscriptionPlanID)
}
