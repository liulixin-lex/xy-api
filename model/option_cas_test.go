package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	paymentOptionCASTestFirst  = "payment_test.cas_01_first"
	paymentOptionCASTestSecond = "payment_test.cas_02_second"
)

func preparePaymentOptionCASTest(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&Option{}))
	keys := []string{
		PaymentConfigurationVersionOptionKey,
		paymentOptionCASTestFirst,
		paymentOptionCASTestSecond,
	}
	require.NoError(t, DB.Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), keys).Delete(&Option{}).Error)

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	previous := make(map[string]string, len(keys))
	existed := make(map[string]bool, len(keys))
	for _, key := range keys {
		previous[key], existed[key] = common.OptionMap[key]
		delete(common.OptionMap, key)
	}
	common.OptionMap[PaymentConfigurationVersionOptionKey] = "1"
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		DB.Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), keys).Delete(&Option{})
		common.OptionMapRWMutex.Lock()
		for _, key := range keys {
			if existed[key] {
				common.OptionMap[key] = previous[key]
			} else {
				delete(common.OptionMap, key)
			}
		}
		common.OptionMapRWMutex.Unlock()
	})
}

func TestUpdatePaymentOptionsBulkWithVersionLockHeldIncrementsVersion(t *testing.T) {
	preparePaymentOptionCASTest(t)

	nextVersion, err := UpdatePaymentOptionsBulkWithVersionLockHeld(map[string]string{
		paymentOptionCASTestFirst:  "first-value",
		paymentOptionCASTestSecond: "second-value",
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), nextVersion)

	var options []Option
	require.NoError(t, DB.Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), []string{
		PaymentConfigurationVersionOptionKey,
		paymentOptionCASTestFirst,
		paymentOptionCASTestSecond,
	}).Find(&options).Error)
	stored := make(map[string]string, len(options))
	for _, option := range options {
		stored[option.Key] = option.Value
	}
	assert.Equal(t, "2", stored[PaymentConfigurationVersionOptionKey])
	assert.Equal(t, "first-value", stored[paymentOptionCASTestFirst])
	assert.Equal(t, "second-value", stored[paymentOptionCASTestSecond])

	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "2", common.OptionMap[PaymentConfigurationVersionOptionKey])
	assert.Equal(t, "first-value", common.OptionMap[paymentOptionCASTestFirst])
	assert.Equal(t, "second-value", common.OptionMap[paymentOptionCASTestSecond])
	common.OptionMapRWMutex.RUnlock()
}

func TestUpdatePaymentOptionsBulkWithVersionLockHeldRejectsStaleVersion(t *testing.T) {
	preparePaymentOptionCASTest(t)

	nextVersion, err := UpdatePaymentOptionsBulkWithVersionLockHeld(map[string]string{
		paymentOptionCASTestFirst: "committed-value",
	}, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), nextVersion)

	currentVersion, err := UpdatePaymentOptionsBulkWithVersionLockHeld(map[string]string{
		paymentOptionCASTestFirst:  "stale-value",
		paymentOptionCASTestSecond: "must-not-exist",
	}, 1)
	assert.ErrorIs(t, err, ErrPaymentConfigurationVersionConflict)
	assert.Equal(t, int64(2), currentVersion)

	var first Option
	require.NoError(t, DB.First(&first, fmt.Sprintf("%s = ?", optionKeyColumn()), paymentOptionCASTestFirst).Error)
	assert.Equal(t, "committed-value", first.Value)
	var second Option
	assert.ErrorIs(t, DB.First(&second, fmt.Sprintf("%s = ?", optionKeyColumn()), paymentOptionCASTestSecond).Error, gorm.ErrRecordNotFound)
	var version Option
	require.NoError(t, DB.First(&version, fmt.Sprintf("%s = ?", optionKeyColumn()), PaymentConfigurationVersionOptionKey).Error)
	assert.Equal(t, "2", version.Value)

	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "2", common.OptionMap[PaymentConfigurationVersionOptionKey])
	assert.Equal(t, "committed-value", common.OptionMap[paymentOptionCASTestFirst])
	_, secondExists := common.OptionMap[paymentOptionCASTestSecond]
	common.OptionMapRWMutex.RUnlock()
	assert.False(t, secondExists)
}

func TestUpdatePaymentOptionsBulkWithVersionLockHeldRollsBackEveryWrite(t *testing.T) {
	preparePaymentOptionCASTest(t)
	require.NoError(t, DB.Create(&Option{
		Key:   PaymentConfigurationVersionOptionKey,
		Value: "1",
	}).Error)

	forcedErr := errors.New("forced option write failure")
	callbackName := "test:payment_option_cas_write_failure"
	triggered := false
	require.NoError(t, DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		option, ok := tx.Statement.Dest.(*Option)
		if !ok || option.Key != paymentOptionCASTestSecond {
			return
		}
		triggered = true
		tx.AddError(forcedErr)
	}))
	t.Cleanup(func() {
		require.NoError(t, DB.Callback().Create().Remove(callbackName))
	})

	currentVersion, err := UpdatePaymentOptionsBulkWithVersionLockHeld(map[string]string{
		paymentOptionCASTestFirst:  "must-roll-back",
		paymentOptionCASTestSecond: "must-fail",
	}, 1)
	assert.ErrorIs(t, err, forcedErr)
	assert.Equal(t, int64(1), currentVersion)
	assert.True(t, triggered)

	var version Option
	require.NoError(t, DB.First(&version, fmt.Sprintf("%s = ?", optionKeyColumn()), PaymentConfigurationVersionOptionKey).Error)
	assert.Equal(t, "1", version.Value)
	var count int64
	require.NoError(t, DB.Model(&Option{}).Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), []string{
		paymentOptionCASTestFirst,
		paymentOptionCASTestSecond,
	}).Count(&count).Error)
	assert.Zero(t, count)

	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "1", common.OptionMap[PaymentConfigurationVersionOptionKey])
	_, firstExists := common.OptionMap[paymentOptionCASTestFirst]
	_, secondExists := common.OptionMap[paymentOptionCASTestSecond]
	common.OptionMapRWMutex.RUnlock()
	assert.False(t, firstExists)
	assert.False(t, secondExists)
}

func TestUpdatePaymentOptionsAndRevokeCredentialsMovesDependentOrdersToReview(t *testing.T) {
	preparePaymentOptionCASTest(t)
	require.NoError(t, DB.AutoMigrate(&PaymentOrder{}, &TopUp{}, &SubscriptionOrder{}))
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "payment-credential-emergency-revoke", UserID: 991900,
		OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay, PaymentMethod: "alipay",
		RequestID: "payment-credential-emergency-revoke", ProviderCredentialGeneration: 3,
		ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 1,
		Status: PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	t.Cleanup(func() { DB.Delete(&PaymentOrder{}, order.ID) })

	nextVersion, err := UpdatePaymentOptionsAndRevokeCredentialsWithVersionLockHeld(
		map[string]string{paymentOptionCASTestFirst: "emergency-revoke"},
		1,
		[]PaymentCredentialRevocation{{Provider: PaymentProviderEpay, Generation: 3, ValidBefore: now}},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), nextVersion)
	require.NoError(t, DB.First(order, order.ID).Error)
	assert.Equal(t, PaymentOrderStatusManualReview, order.Status)
	assert.Contains(t, order.StatusReason, "credential generation revoked")
}

func TestBindPaymentOrderCredentialGenerationUsesConfigurationFence(t *testing.T) {
	preparePaymentOptionCASTest(t)
	require.NoError(t, DB.AutoMigrate(&PaymentOrder{}))
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "payment-credential-bind-fence", UserID: 991901,
		OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayNative,
		RequestID: "payment-credential-bind-fence", ExpectedAmountMinor: 100, Currency: "CNY",
		RequestedAmount: 1, CreditQuota: 1, Status: PaymentOrderStatusPending,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	t.Cleanup(func() { _ = DB.Delete(&PaymentOrder{}, order.ID).Error })

	nextVersion, err := UpdatePaymentOptionsBulkWithVersionLockHeld(map[string]string{
		paymentOptionCASTestFirst: "remote-payment-configuration-change",
	}, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), nextVersion)

	err = BindPaymentOrderCredentialGeneration(order.TradeNo, 7, 1)
	assert.ErrorIs(t, err, ErrPaymentConfigurationVersionConflict)
	require.NoError(t, DB.First(order, order.ID).Error)
	assert.Zero(t, order.ProviderCredentialGeneration)
	assert.Equal(t, PaymentOrderStatusPending, order.Status)

	require.NoError(t, BindPaymentOrderCredentialGeneration(order.TradeNo, 7, nextVersion))
	require.NoError(t, DB.First(order, order.ID).Error)
	assert.Equal(t, int64(7), order.ProviderCredentialGeneration)
}

func TestAuditedPaymentConfigurationUpdateRecordsAuthoritativeSecretFreeEvidence(t *testing.T) {
	preparePaymentOptionCASTest(t)
	require.NoError(t, DB.AutoMigrate(&PaymentConfigurationAudit{}, &PaymentOrder{}, &PaymentEvent{}, &TopUp{}, &SubscriptionOrder{}))
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true, SkipHooks: true}).Delete(&PaymentConfigurationAudit{}).Error)
	t.Cleanup(func() {
		_ = DB.Session(&gorm.Session{AllowGlobalUpdate: true, SkipHooks: true}).Delete(&PaymentConfigurationAudit{}).Error
	})

	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "payment-audit-evidence-order", UserID: 991902,
		OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay, PaymentMethod: "alipay",
		RequestID: "payment-audit-evidence-order", ProviderCredentialGeneration: 3,
		ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 1,
		Status: PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	event := &PaymentEvent{
		Provider: PaymentProviderEpay, ProviderCredentialGeneration: 3,
		EventKey: "payment-audit-evidence-event", EventType: "paid", Paid: true,
		PaidAmountMinor: 100, Currency: "CNY", Status: PaymentEventStatusManualReview,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, DB.Create(event).Error)
	t.Cleanup(func() {
		_ = DB.Delete(&PaymentOrder{}, order.ID).Error
		_ = DB.Delete(&PaymentEvent{}, event.ID).Error
	})

	const optionValue = "credential-value-that-must-never-enter-the-audit-record"
	nextVersion, err := UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
		map[string]string{paymentOptionCASTestFirst: optionValue},
		1,
		[]PaymentCredentialRevocation{{Provider: PaymentProviderEpay, Generation: 3, ValidBefore: now}},
		nil,
		&PaymentConfigurationAuditInput{
			AdminID: 42, ActorIP: "203.0.113.10",
			ChangedKeys: []string{"caller-supplied-wrong-key"}, RevokedProviders: []string{"caller-supplied-wrong-provider"},
			Reason: "emergency rotation after credential exposure",
		},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), nextVersion)

	var audit PaymentConfigurationAudit
	require.NoError(t, DB.Where("committed_version = ?", nextVersion).First(&audit).Error)
	assert.Equal(t, 42, audit.AdminID)
	assert.Equal(t, "203.0.113.10", audit.ActorIP)
	assert.True(t, audit.Emergency)
	assert.EqualValues(t, 1, audit.AffectedOrders)
	assert.EqualValues(t, 1, audit.AffectedEvents)
	assert.Equal(t, int64(1), audit.PreviousVersion)
	assert.Equal(t, int64(2), audit.CommittedVersion)
	var changedKeys []string
	require.NoError(t, common.UnmarshalJsonStr(audit.ChangedKeys, &changedKeys))
	assert.Equal(t, []string{paymentOptionCASTestFirst}, changedKeys)
	var revokedProviders []string
	require.NoError(t, common.UnmarshalJsonStr(audit.RevokedProviders, &revokedProviders))
	assert.Equal(t, []string{PaymentProviderEpay}, revokedProviders)
	serializedAudit, err := common.Marshal(audit)
	require.NoError(t, err)
	assert.NotContains(t, string(serializedAudit), optionValue)
}

func TestAuditedPaymentConfigurationPreconditionRollsBackMutationAndEvidence(t *testing.T) {
	preparePaymentOptionCASTest(t)
	require.NoError(t, DB.AutoMigrate(&PaymentConfigurationAudit{}, &PaymentOrder{}))
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true, SkipHooks: true}).Delete(&PaymentConfigurationAudit{}).Error)
	t.Cleanup(func() {
		_ = DB.Session(&gorm.Session{AllowGlobalUpdate: true, SkipHooks: true}).Delete(&PaymentConfigurationAudit{}).Error
	})

	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "payment-audit-precondition-order", UserID: 991903,
		OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay, PaymentMethod: "alipay",
		RequestID: "payment-audit-precondition-order", ProviderCredentialGeneration: 1,
		ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 1,
		Status: PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	t.Cleanup(func() { _ = DB.Delete(&PaymentOrder{}, order.ID).Error })

	currentVersion, err := UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
		map[string]string{paymentOptionCASTestFirst: "must-roll-back"},
		1, nil,
		&PaymentConfigurationPreconditions{RequireNoActiveEpayOrders: true},
		&PaymentConfigurationAuditInput{AdminID: 43, ActorIP: "203.0.113.11", Reason: "change payment endpoint"},
	)
	assert.ErrorIs(t, err, ErrPaymentConfigurationPrecondition)
	assert.Equal(t, int64(1), currentVersion)

	var optionCount int64
	require.NoError(t, DB.Model(&Option{}).Where(fmt.Sprintf("%s = ?", optionKeyColumn()), paymentOptionCASTestFirst).Count(&optionCount).Error)
	assert.Zero(t, optionCount)
	var auditCount int64
	require.NoError(t, DB.Model(&PaymentConfigurationAudit{}).Count(&auditCount).Error)
	assert.Zero(t, auditCount)
}

func TestPaymentConfigurationPreconditionsIncludeStandaloneLegacyOrders(t *testing.T) {
	for _, test := range []struct {
		name          string
		projection    interface{}
		preconditions *PaymentConfigurationPreconditions
		values        map[string]string
	}{
		{
			name: "epay top-up",
			projection: &TopUp{
				UserId: 991904, Amount: 1, Money: 1, TradeNo: "legacy-epay-precondition",
				PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay,
				CreateTime: common.GetTimestamp(), Status: common.TopUpStatusPending,
			},
			preconditions: &PaymentConfigurationPreconditions{RequireNoActiveEpayOrders: true},
			values:        map[string]string{paymentOptionCASTestFirst: "new-epay-endpoint"},
		},
		{
			name: "stripe subscription",
			projection: &SubscriptionOrder{
				UserId: 991905, PlanId: 1, TradeNo: "legacy-stripe-precondition",
				PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
				CreateTime: common.GetTimestamp(), Status: common.TopUpStatusPending,
			},
			preconditions: &PaymentConfigurationPreconditions{RequireStripeWebhookOverlap: true},
			values: map[string]string{
				"StripeWebhookSecret": "", "StripeWebhookSecretPrevious": "",
				"StripeWebhookSecretPreviousExpiresAt": "0",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			preparePaymentOptionCASTest(t)
			require.NoError(t, DB.AutoMigrate(&PaymentConfigurationAudit{}, &PaymentOrder{}, &TopUp{}, &SubscriptionOrder{}))
			require.NoError(t, DB.Create(test.projection).Error)
			t.Cleanup(func() {
				switch projection := test.projection.(type) {
				case *TopUp:
					_ = DB.Delete(projection).Error
				case *SubscriptionOrder:
					_ = DB.Delete(projection).Error
				}
				_ = DB.Session(&gorm.Session{AllowGlobalUpdate: true, SkipHooks: true}).Delete(&PaymentConfigurationAudit{}).Error
			})

			version, err := UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
				test.values, 1, nil, test.preconditions,
				&PaymentConfigurationAuditInput{AdminID: 45, ActorIP: "203.0.113.13", Reason: "legacy precondition test"},
			)
			assert.ErrorIs(t, err, ErrPaymentConfigurationPrecondition)
			assert.Equal(t, int64(1), version)
			var auditCount int64
			require.NoError(t, DB.Model(&PaymentConfigurationAudit{}).Count(&auditCount).Error)
			assert.Zero(t, auditCount)
		})
	}
}

func TestAuditedCredentialRevocationRequiresMeaningfulReason(t *testing.T) {
	preparePaymentOptionCASTest(t)
	require.NoError(t, DB.AutoMigrate(&PaymentConfigurationAudit{}))
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true, SkipHooks: true}).Delete(&PaymentConfigurationAudit{}).Error)
	t.Cleanup(func() {
		_ = DB.Session(&gorm.Session{AllowGlobalUpdate: true, SkipHooks: true}).Delete(&PaymentConfigurationAudit{}).Error
	})

	currentVersion, err := UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
		map[string]string{paymentOptionCASTestFirst: "must-not-commit"},
		1,
		[]PaymentCredentialRevocation{{Provider: PaymentProviderEpay, Generation: 1, ValidBefore: common.GetTimestamp()}},
		nil,
		&PaymentConfigurationAuditInput{AdminID: 44, ActorIP: "203.0.113.12", Reason: "short"},
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reason")
	assert.Zero(t, currentVersion)
	var auditCount int64
	require.NoError(t, DB.Model(&PaymentConfigurationAudit{}).Count(&auditCount).Error)
	assert.Zero(t, auditCount)
}

func TestStripeWebhookEmergencyRevocationIsAtomicAndMovesEveryActiveOrderToReview(t *testing.T) {
	preparePaymentOptionCASTest(t)
	t.Setenv("PAYMENT_SECRET_KEY", "test-payment-secret-key-at-least-32-bytes")
	require.NoError(t, DB.AutoMigrate(&PaymentConfigurationAudit{}, &PaymentOrder{}, &PaymentEvent{}, &TopUp{}, &SubscriptionOrder{}))
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Where("admin_id = ?", 46).Delete(&PaymentConfigurationAudit{}).Error)

	stripeOptionKeys := []string{
		"StripeWebhookSecret", "StripeWebhookSecretPrevious", "StripeWebhookSecretPreviousExpiresAt", "StripeWebhookCredentialLivemode",
		"StripeWebhookCredentialGeneration", "StripeWebhookPreviousCredentialGeneration", "StripeWebhookPreviousValidBefore",
	}
	common.OptionMapRWMutex.Lock()
	previousOptionValues := make(map[string]string, len(stripeOptionKeys))
	previousOptionExists := make(map[string]bool, len(stripeOptionKeys))
	for _, key := range stripeOptionKeys {
		previousOptionValues[key], previousOptionExists[key] = common.OptionMap[key]
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		_ = DB.Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), stripeOptionKeys).Delete(&Option{}).Error
		common.OptionMapRWMutex.Lock()
		for _, key := range stripeOptionKeys {
			if previousOptionExists[key] {
				common.OptionMap[key] = previousOptionValues[key]
			} else {
				delete(common.OptionMap, key)
			}
		}
		common.OptionMapRWMutex.Unlock()
	})

	originalAPISecret := setting.StripeApiSecret
	originalAccountID := setting.StripeCredentialAccountId
	originalCredentialMode := setting.StripeCredentialLivemode
	originalWebhookSecret := setting.StripeWebhookSecret
	originalPreviousSecret := setting.StripeWebhookSecretPrevious
	originalPreviousExpiry := setting.StripeWebhookSecretPreviousExpiresAt
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	originalGeneration := setting.StripeWebhookCredentialGeneration
	originalPreviousGeneration := setting.StripeWebhookPreviousCredentialGeneration
	originalPreviousValidBefore := setting.StripeWebhookPreviousValidBefore
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		setting.StripeCredentialAccountId = originalAccountID
		setting.StripeCredentialLivemode = originalCredentialMode
		setting.StripeWebhookSecret = originalWebhookSecret
		setting.StripeWebhookSecretPrevious = originalPreviousSecret
		setting.StripeWebhookSecretPreviousExpiresAt = originalPreviousExpiry
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
		setting.StripeWebhookCredentialGeneration = originalGeneration
		setting.StripeWebhookPreviousCredentialGeneration = originalPreviousGeneration
		setting.StripeWebhookPreviousValidBefore = originalPreviousValidBefore
	})
	setting.StripeApiSecret = "sk_test_preserved_api_credential"
	setting.StripeCredentialAccountId = "acct_preserved_binding"
	setting.StripeCredentialLivemode = "test"
	setting.StripeWebhookSecret = "whsec_compromised_current"
	setting.StripeWebhookSecretPrevious = "whsec_compromised_previous"
	setting.StripeWebhookSecretPreviousExpiresAt = common.GetTimestamp() + 3600
	setting.StripeWebhookCredentialLivemode = "test"
	setting.StripeWebhookCredentialGeneration = 1
	setting.StripeWebhookPreviousCredentialGeneration = 0
	setting.StripeWebhookPreviousValidBefore = 0

	now := common.GetTimestamp()
	newOrder := func(suffix, kind, provider, status string) *PaymentOrder {
		credentialGeneration := int64(0)
		if provider == PaymentProviderStripe {
			credentialGeneration = 1
		}
		return &PaymentOrder{
			TradeNo: "stripe-emergency-" + suffix, UserID: 992000,
			OrderKind: kind, Provider: provider, PaymentMethod: PaymentMethodStripe,
			RequestID: "stripe-emergency-" + suffix, ProviderCredentialGeneration: credentialGeneration, ExpectedAmountMinor: 100,
			Currency: "USD", RequestedAmount: 1, CreditQuota: 10,
			Status: status, CreatedAt: now, UpdatedAt: now, Version: 1,
		}
	}
	pendingTopUpOrder := newOrder("pending-topup", PaymentOrderKindTopUp, PaymentProviderStripe, PaymentOrderStatusPending)
	processingSubscriptionOrder := newOrder("processing-subscription", PaymentOrderKindSubscription, PaymentProviderStripe, PaymentOrderStatusProcessing)
	manualOrder := newOrder("manual", PaymentOrderKindTopUp, PaymentProviderStripe, PaymentOrderStatusManualReview)
	fulfilledOrder := newOrder("fulfilled", PaymentOrderKindTopUp, PaymentProviderStripe, PaymentOrderStatusFulfilled)
	otherProviderOrder := newOrder("other-provider", PaymentOrderKindTopUp, PaymentProviderEpay, PaymentOrderStatusPending)
	for _, order := range []*PaymentOrder{pendingTopUpOrder, processingSubscriptionOrder, manualOrder, fulfilledOrder, otherProviderOrder} {
		require.NoError(t, DB.Create(order).Error)
	}

	linkedTopUp := &TopUp{
		PaymentOrderId: &pendingTopUpOrder.ID, UserId: pendingTopUpOrder.UserID, Amount: 1, Money: 1,
		TradeNo: pendingTopUpOrder.TradeNo, PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		CreateTime: now, Status: common.TopUpStatusPending,
	}
	linkedSubscription := &SubscriptionOrder{
		PaymentOrderId: &processingSubscriptionOrder.ID, UserId: processingSubscriptionOrder.UserID, PlanId: 1,
		TradeNo: processingSubscriptionOrder.TradeNo, PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		CreateTime: now, Status: common.TopUpStatusPending,
	}
	standaloneTopUp := &TopUp{
		UserId: 992001, Amount: 1, Money: 1, TradeNo: "stripe-emergency-legacy-topup",
		PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe, CreateTime: now,
		Status: common.TopUpStatusPending,
	}
	standaloneSubscription := &SubscriptionOrder{
		UserId: 992002, PlanId: 1, TradeNo: "stripe-emergency-legacy-subscription",
		PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe, CreateTime: now,
		Status: common.TopUpStatusPending,
	}
	for _, projection := range []interface{}{linkedTopUp, linkedSubscription, standaloneTopUp, standaloneSubscription} {
		require.NoError(t, DB.Create(projection).Error)
	}
	unmatchedEvent := &PaymentEvent{
		Provider: PaymentProviderStripe, EventKey: "stripe-emergency-unmatched-event", EventType: "checkout.session.completed",
		ProviderCredentialGeneration: 1, PaidAmountMinor: 100, Currency: "USD", Paid: true,
		PayloadDigest: PaymentPayloadDigest(`{"paid":true}`), NormalizedPayload: `{"paid":true}`,
		Status: PaymentEventStatusManualReview, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, DB.Create(unmatchedEvent).Error)
	unmatchedReversal := &PaymentEvent{
		Provider: PaymentProviderStripe, EventKey: "stripe-emergency-unmatched-refund", EventType: "charge.refunded",
		ProviderCredentialGeneration: 1, ProviderPaymentKey: "stripe:pi_revoked_unmatched",
		RefundedAmountMinor: 100, Currency: "USD", Refunded: true,
		PayloadDigest: PaymentPayloadDigest(`{"refunded":true}`), NormalizedPayload: `{"refunded":true}`,
		Status: PaymentEventStatusManualReview, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, DB.Create(unmatchedReversal).Error)

	t.Cleanup(func() {
		_ = DB.Where("trade_no LIKE ?", "stripe-emergency-%").Delete(&TopUp{}).Error
		_ = DB.Where("trade_no LIKE ?", "stripe-emergency-%").Delete(&SubscriptionOrder{}).Error
		_ = DB.Where("trade_no LIKE ?", "stripe-emergency-%").Delete(&PaymentOrder{}).Error
		_ = DB.Delete(&PaymentEvent{}, unmatchedEvent.ID).Error
		_ = DB.Delete(&PaymentEvent{}, unmatchedReversal.ID).Error
		_ = DB.Session(&gorm.Session{SkipHooks: true}).Where("admin_id = ?", 46).Delete(&PaymentConfigurationAudit{}).Error
	})

	nextVersion, err := UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
		map[string]string{
			"StripeWebhookSecret":                       "",
			"StripeWebhookSecretPrevious":               "",
			"StripeWebhookSecretPreviousExpiresAt":      "0",
			"StripeWebhookCredentialLivemode":           "",
			"StripeWebhookCredentialGeneration":         "2",
			"StripeWebhookPreviousCredentialGeneration": "0",
			"StripeWebhookPreviousValidBefore":          "0",
		},
		1,
		[]PaymentCredentialRevocation{{Provider: PaymentProviderStripe, Generation: 1, ValidBefore: now, AllActiveOrders: true}},
		nil,
		&PaymentConfigurationAuditInput{
			AdminID: 46, ActorIP: "203.0.113.46", Reason: "emergency Stripe webhook credential shutdown after exposure",
		},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), nextVersion)
	var audit PaymentConfigurationAudit
	require.NoError(t, DB.Where("admin_id = ? AND committed_version = ?", 46, nextVersion).First(&audit).Error)
	assert.True(t, audit.Emergency)
	assert.EqualValues(t, 6, audit.AffectedOrders)
	assert.EqualValues(t, 2, audit.AffectedEvents)
	var revokedProviders []string
	require.NoError(t, common.UnmarshalJsonStr(audit.RevokedProviders, &revokedProviders))
	assert.Equal(t, []string{PaymentProviderStripe}, revokedProviders)

	for _, order := range []*PaymentOrder{pendingTopUpOrder, processingSubscriptionOrder, manualOrder} {
		require.NoError(t, DB.First(order, order.ID).Error)
		assert.Equal(t, PaymentOrderStatusManualReview, order.Status)
		assert.Contains(t, order.StatusReason, "webhook signing credential revoked")
	}
	require.NoError(t, DB.First(fulfilledOrder, fulfilledOrder.ID).Error)
	assert.Equal(t, PaymentOrderStatusFulfilled, fulfilledOrder.Status)
	assert.True(t, fulfilledOrder.CredentialIncident)
	assert.Equal(t, PaymentCredentialIncidentOpen, fulfilledOrder.CredentialIncidentState)
	assert.EqualValues(t, 1, fulfilledOrder.CredentialIncidentGeneration)
	require.NoError(t, DB.First(otherProviderOrder, otherProviderOrder.ID).Error)
	assert.Equal(t, PaymentOrderStatusPending, otherProviderOrder.Status)

	require.NoError(t, DB.First(linkedTopUp, linkedTopUp.Id).Error)
	assert.Equal(t, common.TopUpStatusManualReview, linkedTopUp.Status)
	require.NoError(t, DB.First(linkedSubscription, linkedSubscription.Id).Error)
	assert.Equal(t, SubscriptionOrderStatusManualReview, linkedSubscription.Status)
	require.NoError(t, DB.First(standaloneTopUp, standaloneTopUp.Id).Error)
	assert.Equal(t, common.TopUpStatusManualReview, standaloneTopUp.Status)
	require.NoError(t, DB.First(standaloneSubscription, standaloneSubscription.Id).Error)
	assert.Equal(t, SubscriptionOrderStatusManualReview, standaloneSubscription.Status)
	assert.Contains(t, standaloneSubscription.ReviewReason, "webhook signing credential revoked")

	assert.Empty(t, setting.StripeWebhookSecret)
	assert.Empty(t, setting.StripeWebhookSecretPrevious)
	assert.Zero(t, setting.StripeWebhookSecretPreviousExpiresAt)
	assert.Empty(t, setting.StripeWebhookCredentialLivemode)
	assert.Equal(t, int64(2), setting.StripeWebhookCredentialGeneration)
	assert.Zero(t, setting.StripeWebhookPreviousCredentialGeneration)
	assert.Zero(t, setting.StripeWebhookPreviousValidBefore)
	assert.Equal(t, "sk_test_preserved_api_credential", setting.StripeApiSecret)
	assert.Equal(t, "acct_preserved_binding", setting.StripeCredentialAccountId)
	assert.Equal(t, "test", setting.StripeCredentialLivemode)
	require.NoError(t, DB.First(unmatchedEvent, unmatchedEvent.ID).Error)
	assert.Equal(t, PaymentEventStatusCredentialRevoked, unmatchedEvent.Status)
	require.NoError(t, DB.First(unmatchedReversal, unmatchedReversal.ID).Error)
	assert.Equal(t, PaymentEventStatusCredentialRevoked, unmatchedReversal.Status)
	replayedInput := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1,
		EventKey: unmatchedEvent.EventKey, EventType: unmatchedEvent.EventType,
		PaidAmountMinor: 100, Currency: "USD", Paid: true, NormalizedPayload: `{"paid":true}`,
	}
	require.NoError(t, RecordPaymentEventManualReview(replayedInput, "replayed event must remain terminal after credential revocation"))
	require.NoError(t, DB.First(unmatchedEvent, unmatchedEvent.ID).Error)
	assert.Equal(t, PaymentEventStatusCredentialRevoked, unmatchedEvent.Status)
	duplicate, err := ProcessPaymentEvent(replayedInput)
	require.NoError(t, err)
	require.NotNil(t, duplicate)
	assert.True(t, duplicate.Duplicate)
	require.NoError(t, DB.First(unmatchedEvent, unmatchedEvent.ID).Error)
	assert.Equal(t, PaymentEventStatusCredentialRevoked, unmatchedEvent.Status)

	require.NoError(t, DB.First(pendingTopUpOrder, pendingTopUpOrder.ID).Error)
	_, err = ResolveUnmatchedPaymentEventByAdmin(PaymentUnmatchedEventActionInput{
		EventID: unmatchedEvent.ID, AdminID: 9, ActorIP: "192.0.2.9", Action: PaymentUnmatchedEventActionLink,
		Reason:        "credential-revoked events cannot be linked to payment orders",
		TargetTradeNo: pendingTopUpOrder.TradeNo, ExpectedOrderVersion: pendingTopUpOrder.Version,
	})
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)
	require.NoError(t, DB.First(unmatchedEvent, unmatchedEvent.ID).Error)
	assert.Equal(t, PaymentEventStatusCredentialRevoked, unmatchedEvent.Status)
	assert.Zero(t, unmatchedEvent.PaymentOrderID)
}
