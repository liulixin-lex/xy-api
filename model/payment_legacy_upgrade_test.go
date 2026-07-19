package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyTopUpSchema struct {
	Id            int
	UserId        int
	Amount        int64
	Money         float64
	TradeNo       string `gorm:"unique;type:varchar(255);index"`
	PaymentMethod string `gorm:"type:varchar(50)"`
	CreateTime    int64
	CompleteTime  int64
	Status        string
}

func (legacyTopUpSchema) TableName() string { return "top_ups" }

type legacySubscriptionOrderSchema struct {
	Id              int
	UserId          int
	PlanId          int
	Money           float64
	TradeNo         string `gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string `gorm:"type:varchar(50)"`
	Status          string
	CreateTime      int64
	CompleteTime    int64
	ProviderPayload string `gorm:"type:text"`
}

func (legacySubscriptionOrderSchema) TableName() string { return "subscription_orders" }

type legacyUserSubscriptionSchema struct {
	Id     int
	UserId int
	PlanId int
}

func (legacyUserSubscriptionSchema) TableName() string { return "user_subscriptions" }

type legacyPaymentUserSchema struct {
	Id          int
	Username    string
	Password    string
	AccessToken *string `gorm:"type:char(32);column:access_token;uniqueIndex"`
	Status      int
	Quota       int
	Group       string
	AffCode     string
}

func (legacyPaymentUserSchema) TableName() string { return "users" }

func legacyEpayPaidInput(tradeNo string, amountMinor int64, method string) PaymentEventInput {
	payload := common.GetJsonString(map[string]interface{}{
		"out_trade_no": tradeNo, "trade_no": "gateway_" + tradeNo,
		"money_minor": amountMinor, "type": method, "trade_status": "TRADE_SUCCESS",
	})
	return PaymentEventInput{
		Provider: PaymentProviderEpay, ProviderCredentialGeneration: 1,
		EventKey: "legacy-epay-paid:" + tradeNo, EventType: "TRADE_SUCCESS", TradeNo: tradeNo,
		ProviderOrderKey: "epay:g1:gateway_" + tradeNo,
		PaidAmountMinor:  amountMinor, Currency: "CNY", PaymentMethod: method, Paid: true,
		NormalizedPayload: payload,
	}
}

func createLegacySubscriptionPlan(t *testing.T, id int, price float64, createdAt, updatedAt int64) *SubscriptionPlan {
	t.Helper()
	plan := &SubscriptionPlan{
		Id: id, Title: "Legacy subscription plan", PriceAmount: price, Currency: "USD",
		DurationUnit: SubscriptionDurationDay, DurationValue: 30, Enabled: true,
		TotalAmount: 12345, QuotaResetPeriod: SubscriptionResetNever,
	}
	require.NoError(t, DB.Create(plan).Error)
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", id).
		UpdateColumns(map[string]interface{}{"created_at": createdAt, "updated_at": updatedAt}).Error)
	plan.CreatedAt = createdAt
	plan.UpdatedAt = updatedAt
	return plan
}

func TestLegacyEpayZeroValueSubscriptionAdoptsAndSettlesAtomically(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 975101, 0)
	now := common.GetTimestamp()
	plan := createLegacySubscriptionPlan(t, 985101, 10.25, now-200, now-150)
	legacy := &SubscriptionOrder{
		UserId: 975101, PlanId: plan.Id, Money: 10.25, TradeNo: "LEGACY_EPAY_SUB_ZERO",
		PaymentMethod: "alipay", Status: common.TopUpStatusPending, CreateTime: now - 100,
	}
	require.NoError(t, DB.Create(legacy).Error)

	result, err := ProcessPaymentEvent(legacyEpayPaidInput(legacy.TradeNo, 1025, "alipay"))
	require.NoError(t, err)
	require.NotNil(t, result.Order)
	assert.Equal(t, PaymentOrderStatusFulfilled, result.Order.Status)
	assert.Equal(t, PaymentProviderEpay, result.Order.Provider)
	assert.Equal(t, "CNY", result.Order.Currency)
	assert.EqualValues(t, 1025, result.Order.ExpectedAmountMinor)
	assert.NotEmpty(t, result.Order.ProductSnapshot)
	assert.Equal(t, legacy.CreateTime+int64(defaultSubscriptionReservationTTL/time.Second), result.Order.ExpiresAt)

	var storedLegacy SubscriptionOrder
	require.NoError(t, DB.First(&storedLegacy, legacy.Id).Error)
	require.NotNil(t, storedLegacy.PaymentOrderId)
	assert.EqualValues(t, result.Order.ID, *storedLegacy.PaymentOrderId)
	assert.Equal(t, PaymentProviderEpay, storedLegacy.PaymentProvider)
	assert.EqualValues(t, 1025, storedLegacy.ExpectedAmountMinor)
	assert.Equal(t, "CNY", storedLegacy.PaymentCurrency)
	assert.Equal(t, result.Order.ProductSnapshot, storedLegacy.PlanSnapshot)
	assert.Equal(t, result.Order.ExpiresAt, storedLegacy.ReserveUntil)
	assert.Equal(t, common.TopUpStatusSuccess, storedLegacy.Status)

	var subscription UserSubscription
	require.NoError(t, DB.Where("payment_order_id = ?", result.Order.ID).First(&subscription).Error)
	assert.Equal(t, plan.Id, subscription.PlanId)
	assert.EqualValues(t, plan.TotalAmount, subscription.AmountTotal)
}

func TestLegacyStripeTestSubscriptionCannotAdoptWhenSandboxIsDisabled(t *testing.T) {
	t.Setenv(setting.StripeTestModeEnabledEnv, "false")
	truncateTables(t)
	seedPaymentUser(t, 975120, 0)
	now := common.GetTimestamp()
	legacy := &SubscriptionOrder{
		UserId: 975120, PlanId: 985120, Money: 10, TradeNo: "LEGACY_STRIPE_TEST_DISABLED",
		PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		Status: common.TopUpStatusPending, CreateTime: now - 100,
	}
	require.NoError(t, DB.Create(legacy).Error)
	testMode := false
	input := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: &testMode,
		EventKey: "legacy-stripe-test-disabled-paid", EventType: "checkout.session.completed",
		TradeNo: legacy.TradeNo, ProviderOrderKey: "stripe:cs_legacy_test_disabled",
		ProviderPaymentKey: "stripe:pi_legacy_test_disabled", PaidAmountMinor: 1000,
		Currency: "USD", PaymentMethod: PaymentMethodStripe, Paid: true,
		NormalizedPayload: `{"paid":true,"livemode":false}`,
	}

	result, err := ProcessPaymentEvent(input)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)
	assert.Nil(t, result.Order)

	var canonicalCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", legacy.TradeNo).Count(&canonicalCount).Error)
	assert.Zero(t, canonicalCount)
	var entitlementCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", legacy.UserId).Count(&entitlementCount).Error)
	assert.Zero(t, entitlementCount)
	var storedLegacy SubscriptionOrder
	require.NoError(t, DB.First(&storedLegacy, legacy.Id).Error)
	assert.Equal(t, common.TopUpStatusPending, storedLegacy.Status)
	assert.Nil(t, storedLegacy.PaymentOrderId)
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderStripe, input.EventKey).First(&event).Error)
	assert.Equal(t, PaymentEventStatusManualReview, event.Status)
	require.NotNil(t, event.ProviderLivemode)
	assert.False(t, *event.ProviderLivemode)
}

func TestLegacyEpayEmptyProviderNormalizationIsStrict(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 975102, 0)
	now := common.GetTimestamp()

	tests := []struct {
		name          string
		storedMethod  string
		inputProvider string
		inputMethod   string
		inputCurrency string
		generation    int64
	}{
		{name: "signed method mismatch", storedMethod: "alipay", inputProvider: PaymentProviderEpay, inputMethod: "wxpay", inputCurrency: "CNY", generation: 1},
		{name: "reserved Stripe method", storedMethod: PaymentMethodStripe, inputProvider: PaymentProviderEpay, inputMethod: PaymentMethodStripe, inputCurrency: "CNY", generation: 1},
		{name: "other provider remains strict", storedMethod: PaymentMethodStripe, inputProvider: PaymentProviderStripe, inputMethod: PaymentMethodStripe, inputCurrency: "USD", generation: 1},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tradeNo := fmt.Sprintf("LEGACY_EMPTY_PROVIDER_%d", index)
			legacy := &TopUp{
				UserId: 975102, Amount: 1, Money: 1, TradeNo: tradeNo,
				PaymentMethod: test.storedMethod, Status: common.TopUpStatusPending, CreateTime: now,
			}
			require.NoError(t, DB.Create(legacy).Error)
			input := legacyEpayPaidInput(tradeNo, 100, test.inputMethod)
			input.Provider = test.inputProvider
			input.Currency = test.inputCurrency
			input.ProviderCredentialGeneration = test.generation
			if test.inputProvider == PaymentProviderStripe {
				livemode := true
				input.ProviderLivemode = &livemode
			}
			_, err := AdoptLegacyPaymentOrder(input)
			assert.ErrorIs(t, err, ErrPaymentProviderMismatch)

			var stored TopUp
			require.NoError(t, DB.First(&stored, legacy.Id).Error)
			assert.Empty(t, stored.PaymentProvider)
			assert.Nil(t, stored.PaymentOrderId)
			var canonicalCount int64
			require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", tradeNo).Count(&canonicalCount).Error)
			assert.Zero(t, canonicalCount)
		})
	}
}

func TestLegacyEpayUnsafeSubscriptionEvidenceStaysDurableAndNeverGrants(t *testing.T) {
	truncateTables(t)
	now := common.GetTimestamp()

	tests := []struct {
		name        string
		userID      int
		planID      int
		createPlan  bool
		planPrice   float64
		planUpdated int64
		money       float64
		paidMinor   int64
	}{
		{name: "missing plan", userID: 975110, planID: 985110, money: 10, paidMinor: 1000},
		{name: "changed plan", userID: 975111, planID: 985111, createPlan: true, planPrice: 10, planUpdated: now - 49, money: 10, paidMinor: 1000},
		{name: "same second update is ambiguous", userID: 975112, planID: 985112, createPlan: true, planPrice: 10, planUpdated: now - 50, money: 10, paidMinor: 1000},
		{name: "price no longer matches", userID: 975113, planID: 985113, createPlan: true, planPrice: 11, planUpdated: now - 60, money: 10, paidMinor: 1000},
		{name: "legacy money has excess precision", userID: 975114, planID: 985114, createPlan: true, planPrice: 10, planUpdated: now - 60, money: 10.001, paidMinor: 1000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seedPaymentUser(t, test.userID, 0)
			orderCreatedAt := now - 50
			if test.createPlan {
				createLegacySubscriptionPlan(t, test.planID, test.planPrice, now-100, test.planUpdated)
			}
			legacy := &SubscriptionOrder{
				UserId: test.userID, PlanId: test.planID, Money: test.money,
				TradeNo: fmt.Sprintf("LEGACY_UNSAFE_%d", test.userID), PaymentMethod: "alipay",
				Status: common.TopUpStatusPending, CreateTime: orderCreatedAt,
			}
			require.NoError(t, DB.Create(legacy).Error)

			result, err := ProcessPaymentEvent(legacyEpayPaidInput(legacy.TradeNo, test.paidMinor, "alipay"))
			require.ErrorIs(t, err, ErrPaymentManualReview)
			require.NotNil(t, result)
			assert.True(t, result.ManualReview)
			assert.Nil(t, result.Order)

			var event PaymentEvent
			require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderEpay,
				"legacy-epay-paid:"+legacy.TradeNo).First(&event).Error)
			assert.Equal(t, PaymentEventStatusManualReview, event.Status)
			assert.Zero(t, event.PaymentOrderID)
			assert.Contains(t, event.LastError, "legacy payment adoption requires manual review")
			assert.Equal(t, 1, event.Attempts)

			var canonicalCount int64
			require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", legacy.TradeNo).Count(&canonicalCount).Error)
			assert.Zero(t, canonicalCount)
			var entitlementCount int64
			require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", test.userID).Count(&entitlementCount).Error)
			assert.Zero(t, entitlementCount)
			var storedLegacy SubscriptionOrder
			require.NoError(t, DB.First(&storedLegacy, legacy.Id).Error)
			assert.Equal(t, common.TopUpStatusPending, storedLegacy.Status)
			assert.Empty(t, storedLegacy.PaymentProvider)
			assert.Zero(t, storedLegacy.ExpectedAmountMinor)
			assert.Empty(t, storedLegacy.PlanSnapshot)
		})
	}
}

func TestLegacyTopUpWithoutQuotaSnapshotStaysInManualReviewAfterQuotaChange(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 975119, 0)
	originalQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })

	legacy := &TopUp{
		UserId: 975119, Amount: 2, Money: 14.60, TradeNo: "LEGACY_TOPUP_QPU_DRIFT",
		PaymentMethod: "alipay", Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp() - 100,
	}
	require.NoError(t, DB.Create(legacy).Error)
	common.QuotaPerUnit = 1_000_000

	result, err := ProcessPaymentEvent(legacyEpayPaidInput(legacy.TradeNo, 1460, "alipay"))
	require.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)
	assert.Nil(t, result.Order)

	var user User
	require.NoError(t, DB.First(&user, legacy.UserId).Error)
	assert.Zero(t, user.Quota)
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderEpay,
		"legacy-epay-paid:"+legacy.TradeNo).First(&event).Error)
	assert.Equal(t, PaymentEventStatusManualReview, event.Status)
	assert.Contains(t, event.LastError, "legacy top-up quota snapshot is unavailable")
	var canonicalCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", legacy.TradeNo).Count(&canonicalCount).Error)
	assert.Zero(t, canonicalCount)
}

func TestRetryLegacyEpayEventRecoversAfterHistoricalPlanIsRestored(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 975120, 0)
	now := common.GetTimestamp()
	legacy := &SubscriptionOrder{
		UserId: 975120, PlanId: 985120, Money: 12.34, TradeNo: "LEGACY_RETRY_RESTORED_PLAN",
		PaymentMethod: "alipay", Status: common.TopUpStatusPending, CreateTime: now - 100,
	}
	require.NoError(t, DB.Create(legacy).Error)
	paid := legacyEpayPaidInput(legacy.TradeNo, 1234, "alipay")
	_, err := ProcessPaymentEvent(paid)
	require.ErrorIs(t, err, ErrPaymentManualReview)

	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", paid.Provider, paid.EventKey).First(&event).Error)
	require.Equal(t, PaymentReviewCodeLegacySubscriptionContractUnavailable, event.ReviewCode)
	createLegacySubscriptionPlan(t, legacy.PlanId, 12.34, now-300, now-200)
	views, _, err := ListUnmatchedPaymentEventViewsPage(0, 50)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, []string{PaymentUnmatchedEventActionRetryLegacy}, views[0].AvailableActions)
	input := PaymentUnmatchedEventActionInput{
		EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 88, ActorIP: "127.0.0.1",
		Action: PaymentUnmatchedEventActionRetryLegacy,
		Reason: "historical plan record was restored and verified against provider evidence",
	}
	result, err := ResolveUnmatchedPaymentEventByAdmin(input)
	require.NoError(t, err)
	require.NotNil(t, result.Order)
	assert.False(t, result.Duplicate)
	assert.Equal(t, PaymentOrderStatusFulfilled, result.Order.Status)
	assert.Equal(t, PaymentEventStatusProcessed, result.Event.Status)

	duplicate, err := ResolveUnmatchedPaymentEventByAdmin(input)
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)
	assert.EqualValues(t, result.Order.ID, duplicate.Order.ID)

	var entitlementCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("payment_order_id = ?", result.Order.ID).Count(&entitlementCount).Error)
	assert.EqualValues(t, 1, entitlementCount)
	assertLegacyRetryAuditEvidence(t, event.ID, result.Order.ID, input.ExpectedEventAttempts)
}

func TestRetryLegacyEpayCrashAfterSettlementRecoversAuditWithoutGrantingTwice(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 975121, 0)
	now := common.GetTimestamp()
	legacy := &SubscriptionOrder{
		UserId: 975121, PlanId: 985121, Money: 9.99, TradeNo: "LEGACY_RETRY_CRASH_RECOVERY",
		PaymentMethod: "alipay", Status: common.TopUpStatusPending, CreateTime: now - 100,
	}
	require.NoError(t, DB.Create(legacy).Error)
	paid := legacyEpayPaidInput(legacy.TradeNo, 999, "alipay")
	_, err := ProcessPaymentEvent(paid)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	createLegacySubscriptionPlan(t, legacy.PlanId, 9.99, now-300, now-200)

	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", paid.Provider, paid.EventKey).First(&event).Error)
	input := PaymentUnmatchedEventActionInput{
		EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 89, ActorIP: "127.0.0.2",
		Action: PaymentUnmatchedEventActionRetryLegacy,
		Reason: "resume an interrupted administrator retry after validating durable evidence",
	}
	payload, err := legacyPaymentRetryAdminPayload(input, &event)
	require.NoError(t, err)
	adminEvent := &PaymentEvent{
		Provider: "admin", EventKey: legacyPaymentRetryAdminEventKey(event.ID, event.Attempts),
		EventType: paymentLegacyRetryAdminEventType, TradeNo: event.TradeNo,
		PayloadDigest: PaymentPayloadDigest(payload), NormalizedPayload: payload,
		Status: PaymentEventStatusProcessing, Attempts: 1, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, DB.Create(adminEvent).Error)

	expectedAttempts := event.Attempts
	settled, err := processPaymentEventWithReplayAttempts(paymentEventInputFromStoredEvent(&event), event.ID, &expectedAttempts)
	require.NoError(t, err)
	require.NotNil(t, settled.Order)
	assert.Equal(t, PaymentOrderStatusFulfilled, settled.Order.Status)

	recovered, err := ResolveUnmatchedPaymentEventByAdmin(input)
	require.NoError(t, err)
	require.NotNil(t, recovered.Order)
	assert.True(t, recovered.Duplicate)
	assert.EqualValues(t, settled.Order.ID, recovered.Order.ID)

	var entitlementCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("payment_order_id = ?", settled.Order.ID).Count(&entitlementCount).Error)
	assert.EqualValues(t, 1, entitlementCount)
	assertLegacyRetryAuditEvidence(t, event.ID, settled.Order.ID, input.ExpectedEventAttempts)
	var storedAdminEvent PaymentEvent
	require.NoError(t, DB.First(&storedAdminEvent, adminEvent.ID).Error)
	assert.Equal(t, PaymentEventStatusProcessed, storedAdminEvent.Status)
}

func assertLegacyRetryAuditEvidence(t *testing.T, providerEventID, orderID int64, expectedAttempts int) {
	t.Helper()
	var ledgerCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where(
		"payment_order_id = ? AND entry_type = ?", orderID, PaymentLedgerEntryAdminLegacyRetry,
	).Count(&ledgerCount).Error)
	assert.EqualValues(t, 1, ledgerCount)
	var auditCount int64
	require.NoError(t, DB.Model(&PaymentOperationsAudit{}).Where(
		"action = ? AND subject_id = ? AND expected_version = ?",
		PaymentOperationsActionLegacyEpayRetry, providerEventID, expectedAttempts,
	).Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)
}

func TestLegacyPaymentRowsFromPreCanonicalSchemaMigrateAndHandleUnsafeTopUp(t *testing.T) {
	dsn := fmt.Sprintf("file:legacy-payment-upgrade-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(
		&legacyTopUpSchema{}, &legacySubscriptionOrderSchema{}, &legacyUserSubscriptionSchema{}, &legacyPaymentUserSchema{},
	))
	now := common.GetTimestamp()
	require.NoError(t, db.Create(&legacyPaymentUserSchema{
		Id: 975129, Username: "legacy_payment_frozen_user", Password: "legacy-password", Status: common.UserStatusEnabled,
		Quota: 12345, Group: "default", AffCode: "legacy_payment_frozen_aff",
	}).Error)
	require.NoError(t, db.Create(&legacyTopUpSchema{
		UserId: 975130, Amount: 2, Money: 2.5, TradeNo: "LEGACY_SCHEMA_TOPUP",
		PaymentMethod: "alipay", CreateTime: now - 100, Status: common.TopUpStatusPending,
	}).Error)
	require.NoError(t, db.Create(&legacySubscriptionOrderSchema{
		UserId: 975131, PlanId: 985131, Money: 12.34, TradeNo: "LEGACY_SCHEMA_SUBSCRIPTION",
		PaymentMethod: "alipay", CreateTime: now - 100, Status: common.TopUpStatusPending,
	}).Error)

	originalDB, originalLogDB := DB, LOG_DB
	originalMainType, originalLogType := common.MainDatabaseType(), common.LogDatabaseType()
	originalRedisEnabled := common.RedisEnabled
	DB, LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	t.Cleanup(func() {
		DB, LOG_DB = originalDB, originalLogDB
		common.SetDatabaseTypes(originalMainType, originalLogType)
		common.RedisEnabled = originalRedisEnabled
		_ = sqlDB.Close()
	})
	require.NoError(t, ensurePaymentProjectionColumnsSQLite())
	require.NoError(t, ensureUserPaymentFrozenColumnOn(DB))
	assert.True(t, DB.Migrator().HasColumn("subscription_orders", "provider_order_key"))
	assert.True(t, DB.Migrator().HasColumn("user_subscriptions", "payment_order_id"))
	require.NoError(t, db.AutoMigrate(
		&Option{}, &User{}, &PaymentUserGuard{}, &PaymentOrder{}, &PaymentEvent{}, &PaymentLedgerEntry{},
		&PaymentDebt{}, &PaymentCustomerBinding{}, &PaymentOperationsAudit{}, &AffiliateRewardRecord{},
		&SubscriptionPlan{}, &UserSubscription{}, &TopUp{}, &SubscriptionOrder{},
	))
	var migratedLegacyUser User
	require.NoError(t, DB.First(&migratedLegacyUser, 975129).Error)
	assert.False(t, migratedLegacyUser.PaymentFrozen)
	var paymentFrozenColumn struct {
		NotNull int `gorm:"column:notnull"`
	}
	require.NoError(t, DB.Raw(
		"SELECT `notnull` FROM pragma_table_info('users') WHERE name = ?", "payment_frozen",
	).Scan(&paymentFrozenColumn).Error)
	assert.Equal(t, 1, paymentFrozenColumn.NotNull)

	for key, value := range map[string]string{
		PaymentConfigurationVersionOptionKey: "1", "EpayCredentialGeneration": "1",
		"EpayPreviousCredentialGeneration": "0", "EpayPreviousValidBefore": "0", "EpayPreviousExpiresAt": "0",
	} {
		require.NoError(t, DB.Create(&Option{Key: key, Value: value}).Error)
	}
	for _, userID := range []int{975130, 975131} {
		require.NoError(t, DB.Create(&User{
			Id: userID, Username: fmt.Sprintf("legacy_schema_user_%d", userID),
			AffCode: fmt.Sprintf("legacy_schema_aff_%d", userID), Status: common.UserStatusEnabled,
		}).Error)
	}
	createLegacySubscriptionPlan(t, 985131, 12.34, now-300, now-200)

	var migratedTopUp TopUp
	require.NoError(t, DB.Where("trade_no = ?", "LEGACY_SCHEMA_TOPUP").First(&migratedTopUp).Error)
	assert.Empty(t, migratedTopUp.PaymentProvider)
	assert.Nil(t, migratedTopUp.PaymentOrderId)
	var migratedSubscription SubscriptionOrder
	require.NoError(t, DB.Where("trade_no = ?", "LEGACY_SCHEMA_SUBSCRIPTION").First(&migratedSubscription).Error)
	assert.Empty(t, migratedSubscription.PaymentProvider)
	assert.Zero(t, migratedSubscription.ExpectedAmountMinor)
	assert.Empty(t, migratedSubscription.PaymentCurrency)
	assert.Empty(t, migratedSubscription.PlanSnapshot)
	assert.Zero(t, migratedSubscription.ReserveUntil)

	topUpResult, err := ProcessPaymentEvent(legacyEpayPaidInput("LEGACY_SCHEMA_TOPUP", 250, "alipay"))
	require.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, topUpResult)
	assert.True(t, topUpResult.ManualReview)
	assert.Nil(t, topUpResult.Order)
	subscriptionResult, err := ProcessPaymentEvent(legacyEpayPaidInput("LEGACY_SCHEMA_SUBSCRIPTION", 1234, "alipay"))
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusFulfilled, subscriptionResult.Order.Status)

	var topUpUser User
	require.NoError(t, DB.First(&topUpUser, 975130).Error)
	assert.Zero(t, topUpUser.Quota)
	var topUpEvent PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND trade_no = ?", PaymentProviderEpay, "LEGACY_SCHEMA_TOPUP").First(&topUpEvent).Error)
	assert.Equal(t, PaymentEventStatusManualReview, topUpEvent.Status)
	assert.Contains(t, topUpEvent.LastError, "legacy top-up quota snapshot is unavailable")
	var subscriptionCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("payment_order_id = ?", subscriptionResult.Order.ID).Count(&subscriptionCount).Error)
	assert.EqualValues(t, 1, subscriptionCount)

	var upgradedTopUp TopUp
	require.NoError(t, DB.First(&upgradedTopUp, migratedTopUp.Id).Error)
	assert.Empty(t, upgradedTopUp.PaymentProvider)
	assert.Nil(t, upgradedTopUp.PaymentOrderId)
	var upgradedSubscription SubscriptionOrder
	require.NoError(t, DB.First(&upgradedSubscription, migratedSubscription.Id).Error)
	assert.Equal(t, PaymentProviderEpay, upgradedSubscription.PaymentProvider)
	assert.EqualValues(t, 1234, upgradedSubscription.ExpectedAmountMinor)
	assert.Equal(t, "CNY", upgradedSubscription.PaymentCurrency)
	assert.NotEmpty(t, upgradedSubscription.PlanSnapshot)
	require.NotNil(t, upgradedSubscription.PaymentOrderId)
}

func TestLegacyPaymentExactMinorRejectsRoundedValues(t *testing.T) {
	_, err := legacyPaymentMoneyMinorExact(10.001, 2, 1<<31-1)
	assert.ErrorIs(t, err, ErrPaymentAmountMismatch)
	minor, err := legacyPaymentMoneyMinorExact(10.01, 2, 1<<31-1)
	require.NoError(t, err)
	assert.EqualValues(t, 1001, minor)
}
