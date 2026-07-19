package model

import (
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func createCredentialIncidentTestOrder(t *testing.T, tradeNo string, userID int, status string, generation int64) *PaymentOrder {
	t.Helper()
	now := time.Now().Unix()
	order := &PaymentOrder{
		TradeNo: tradeNo, UserID: userID, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe,
		ProviderCredentialGeneration: generation, RequestID: "request_" + tradeNo,
		ExpectedAmountMinor: 1000, PaidAmountMinor: 1000, Currency: "USD",
		RequestedAmount: 10, CreditQuota: 5000, Status: status, StatusReason: "economic state retained",
		CreatedAt: now - 100, UpdatedAt: now - 100, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	require.NoError(t, DB.Create(&TopUp{
		PaymentOrderId: &order.ID, UserId: userID, Amount: 10, Money: 10,
		TradeNo: tradeNo, PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		Status: common.TopUpStatusSuccess, CreateTime: now - 100,
	}).Error)
	return order
}

func TestCredentialRevocationMarksAllCanonicalOrdersWithoutChangingTerminalEconomics(t *testing.T) {
	truncateTables(t)
	active := createCredentialIncidentTestOrder(t, "PO_INCIDENT_ACTIVE", 975101, PaymentOrderStatusProcessing, 5)
	terminal := createCredentialIncidentTestOrder(t, "PO_INCIDENT_FULFILLED", 975102, PaymentOrderStatusFulfilled, 6)
	terminal.RefundedAmountMinor = 200
	terminal.DisputedAmountMinor = 300
	terminal.ReversedAmountMinor = 100
	terminal.ReversedQuota = 500
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", terminal.ID).Updates(map[string]interface{}{
		"refunded_amount_minor": terminal.RefundedAmountMinor, "disputed_amount_minor": terminal.DisputedAmountMinor,
		"reversed_amount_minor": terminal.ReversedAmountMinor, "reversed_quota": terminal.ReversedQuota,
	}).Error)
	require.NoError(t, DB.Create(&PaymentEvent{
		Provider: PaymentProviderStripe, EventKey: "newer-signing-generation-event", EventType: "checkout.session.completed",
		TradeNo: terminal.TradeNo, PaymentOrderID: terminal.ID, ProviderCredentialGeneration: 7,
		Paid: true, PaidAmountMinor: terminal.PaidAmountMinor, Status: PaymentEventStatusProcessed,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), ProcessedAt: time.Now().Unix(),
	}).Error)

	affected := map[int64]struct{}{}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return markCanonicalPaymentCredentialIncidentsTx(tx, PaymentCredentialRevocation{
			Provider: PaymentProviderStripe, Generation: 7, ValidBefore: time.Now().Unix(), AllActiveOrders: true,
		}, time.Now().Unix(), affected)
	}))
	assert.Len(t, affected, 2)

	require.NoError(t, DB.First(active, active.ID).Error)
	assert.Equal(t, PaymentOrderStatusManualReview, active.Status)
	assert.True(t, active.CredentialIncident)
	assert.Equal(t, PaymentCredentialIncidentOpen, active.CredentialIncidentState)
	require.NoError(t, DB.First(terminal, terminal.ID).Error)
	assert.Equal(t, PaymentOrderStatusFulfilled, terminal.Status)
	assert.Equal(t, "economic state retained", terminal.StatusReason)
	assert.EqualValues(t, 200, terminal.RefundedAmountMinor)
	assert.EqualValues(t, 300, terminal.DisputedAmountMinor)
	assert.EqualValues(t, 100, terminal.ReversedAmountMinor)
	assert.EqualValues(t, 500, terminal.ReversedQuota)
	assert.True(t, terminal.CredentialIncident)
	assert.EqualValues(t, 7, terminal.CredentialIncidentGeneration)

	orders, total, err := ListPaymentAuditOrders([]string{
		PaymentOrderStatusManualReview, PaymentOrderStatusDebt, PaymentOrderStatusDisputed, PaymentOrderStatusRefundPending,
	}, true, "", "", 0, 20)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Len(t, orders, 2)
}

func TestCredentialIncidentAdminReviewIsAuditedAndEconomicallyNeutral(t *testing.T) {
	truncateTables(t)
	order := createCredentialIncidentTestOrder(t, "PO_INCIDENT_REVIEW", 975103, PaymentOrderStatusFulfilled, 9)
	affected := map[int64]struct{}{}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return markCanonicalPaymentCredentialIncidentsTx(tx, PaymentCredentialRevocation{
			Provider: PaymentProviderStripe, Generation: 9, ValidBefore: time.Now().Unix(),
		}, time.Now().Unix(), affected)
	}))
	require.NoError(t, DB.First(order, order.ID).Error)
	originalStatus, originalPaid, originalQuota := order.Status, order.PaidAmountMinor, order.CreditQuota

	acknowledged, err := ReviewPaymentCredentialIncidentByAdmin(PaymentCredentialIncidentActionInput{
		TradeNo: order.TradeNo, ExpectedVersion: order.Version, AdminID: 91, ActorIP: "192.0.2.91",
		Action: PaymentCredentialIncidentActionAcknowledge, Reason: "confirmed the revoked credential incident evidence",
	})
	require.NoError(t, err)
	assert.False(t, acknowledged.Duplicate)
	assert.True(t, acknowledged.Order.CredentialIncident)
	assert.Equal(t, PaymentCredentialIncidentAcknowledged, acknowledged.Order.CredentialIncidentState)
	assert.Equal(t, originalStatus, acknowledged.Order.Status)
	assert.Equal(t, originalPaid, acknowledged.Order.PaidAmountMinor)
	assert.Equal(t, originalQuota, acknowledged.Order.CreditQuota)

	resolved, err := ReviewPaymentCredentialIncidentByAdmin(PaymentCredentialIncidentActionInput{
		TradeNo: order.TradeNo, ExpectedVersion: acknowledged.Order.Version, AdminID: 91, ActorIP: "192.0.2.91",
		Action: PaymentCredentialIncidentActionResolve, Reason: "verified settlement evidence and cleared incident review",
	})
	require.NoError(t, err)
	assert.False(t, resolved.Order.CredentialIncident)
	assert.Equal(t, PaymentCredentialIncidentResolved, resolved.Order.CredentialIncidentState)
	assert.Equal(t, originalStatus, resolved.Order.Status)
	assert.Equal(t, originalPaid, resolved.Order.PaidAmountMinor)
	assert.Equal(t, originalQuota, resolved.Order.CreditQuota)

	var eventCount, auditCount, ledgerCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider = ? AND payment_order_id = ?", "admin", order.ID).Count(&eventCount).Error)
	require.NoError(t, DB.Model(&PaymentOperationsAudit{}).Where("payment_order_id = ?", order.ID).Count(&auditCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ?", order.ID).Count(&ledgerCount).Error)
	assert.EqualValues(t, 2, eventCount)
	assert.EqualValues(t, 2, auditCount)
	assert.Zero(t, ledgerCount)

	duplicate, err := ReviewPaymentCredentialIncidentByAdmin(PaymentCredentialIncidentActionInput{
		TradeNo: order.TradeNo, ExpectedVersion: acknowledged.Order.Version, AdminID: 91, ActorIP: "192.0.2.91",
		Action: PaymentCredentialIncidentActionResolve, Reason: "verified settlement evidence and cleared incident review",
	})
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)

	_, err = ReviewPaymentCredentialIncidentByAdmin(PaymentCredentialIncidentActionInput{
		TradeNo: order.TradeNo, ExpectedVersion: resolved.Order.Version, AdminID: 91,
		Action: PaymentCredentialIncidentActionResolve, Reason: "short",
	})
	assert.ErrorIs(t, err, ErrPaymentAuditInvalid)
}

func TestConfigurationRevocationAffectedOrderCountIsDistinct(t *testing.T) {
	truncateTables(t)
	common.OptionMapRWMutex.Lock()
	originalOptionMap := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalOptionMap
		common.OptionMapRWMutex.Unlock()
	})
	active := createCredentialIncidentTestOrder(t, "PO_CONFIG_INCIDENT_ACTIVE", 975104, PaymentOrderStatusPending, 11)
	terminal := createCredentialIncidentTestOrder(t, "PO_CONFIG_INCIDENT_TERMINAL", 975105, PaymentOrderStatusRefunded, 11)
	legacy := &TopUp{
		UserId: 975106, TradeNo: "LEGACY_CONFIG_INCIDENT", PaymentMethod: PaymentMethodStripe,
		PaymentProvider: PaymentProviderStripe, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix() - 10,
	}
	require.NoError(t, DB.Create(legacy).Error)
	require.NoError(t, DB.Create(&PaymentEvent{
		Provider: PaymentProviderStripe, EventKey: "failed-monetary-event-before-revocation", EventType: "checkout.session.completed",
		ProviderCredentialGeneration: 11, Paid: true, PaidAmountMinor: 1000,
		Status: PaymentEventStatusFailed, LastError: "transient database failure",
		CreatedAt: time.Now().Unix() - 10, UpdatedAt: time.Now().Unix() - 10,
	}).Error)
	require.NoError(t, DB.Create(&PaymentEvent{
		Provider: PaymentProviderStripe, EventKey: "failed-telemetry-event-before-revocation", EventType: "telemetry.ping",
		ProviderCredentialGeneration: 11, Status: PaymentEventStatusFailed,
		CreatedAt: time.Now().Unix() - 10, UpdatedAt: time.Now().Unix() - 10,
	}).Error)

	revocation := PaymentCredentialRevocation{Provider: PaymentProviderStripe, Generation: 11, ValidBefore: time.Now().Unix(), AllActiveOrders: true}
	_, err := UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
		map[string]string{"StripeCurrency": "USD"}, 1,
		[]PaymentCredentialRevocation{revocation, revocation}, nil,
		&PaymentConfigurationAuditInput{AdminID: 92, ActorIP: "192.0.2.92", Reason: "emergency duplicate generation revocation test"},
	)
	require.NoError(t, err)
	var audit PaymentConfigurationAudit
	require.NoError(t, DB.Order("id desc").First(&audit).Error)
	assert.EqualValues(t, 3, audit.AffectedOrders)
	assert.EqualValues(t, 1, audit.AffectedEvents)

	require.NoError(t, DB.First(active, active.ID).Error)
	require.NoError(t, DB.First(terminal, terminal.ID).Error)
	assert.Equal(t, PaymentOrderStatusManualReview, active.Status)
	assert.Equal(t, PaymentOrderStatusRefunded, terminal.Status)
	assert.True(t, terminal.CredentialIncident)
	var monetaryEvent, telemetryEvent PaymentEvent
	require.NoError(t, DB.Where("event_key = ?", "failed-monetary-event-before-revocation").First(&monetaryEvent).Error)
	require.NoError(t, DB.Where("event_key = ?", "failed-telemetry-event-before-revocation").First(&telemetryEvent).Error)
	assert.Equal(t, PaymentEventStatusCredentialRevoked, monetaryEvent.Status)
	assert.Equal(t, PaymentEventStatusFailed, telemetryEvent.Status)
}

func TestRetireStripeCustomerBindingPreservesHistoryAndOldWebhookOwnership(t *testing.T) {
	truncateTables(t)
	const userID = 975107
	seedPaymentUser(t, userID, 0)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", userID).Update("stripe_customer", "cus_retired_old").Error)
	binding := &PaymentCustomerBinding{Provider: PaymentProviderStripe, CustomerKey: "cus_retired_old", UserID: userID}
	require.NoError(t, DB.Create(binding).Error)

	result, err := RetireStripeCustomerBindingByAdmin(RetireStripeCustomerBindingInput{
		BindingID: binding.ID, UserID: userID, ExpectedVersion: 1, AdminID: 93,
		ActorIP: "192.0.2.93", Reason: "retire compromised Stripe customer binding safely",
	})
	require.NoError(t, err)
	assert.False(t, result.Duplicate)
	require.NotNil(t, result.Retirement)
	assert.Equal(t, "cus_retired_old", result.Retirement.CustomerKey)

	var activeCount, historyCount, auditCount int64
	require.NoError(t, DB.Model(&PaymentCustomerBinding{}).Where("id = ?", binding.ID).Count(&activeCount).Error)
	require.NoError(t, DB.Model(&PaymentCustomerBindingRetirement{}).Where("original_binding_id = ?", binding.ID).Count(&historyCount).Error)
	require.NoError(t, DB.Model(&PaymentOperationsAudit{}).Where("action = ? AND subject_id = ?", PaymentOperationsActionStripeBindingRetire, binding.ID).Count(&auditCount).Error)
	assert.Zero(t, activeCount)
	assert.EqualValues(t, 1, historyCount)
	assert.EqualValues(t, 1, auditCount)
	var user User
	require.NoError(t, DB.First(&user, userID).Error)
	assert.Empty(t, user.StripeCustomer)
	lateWithoutReplacement := &PaymentOrder{UserID: userID, Provider: PaymentProviderStripe, CreatedAt: result.Retirement.RetiredAt + 1}
	err = DB.Transaction(func(tx *gorm.DB) error {
		_, bindErr := bindStripeCustomerTx(tx, lateWithoutReplacement, "cus_retired_old")
		return bindErr
	})
	assert.ErrorIs(t, err, ErrPaymentProviderMismatch)
	unknownAge := &PaymentOrder{UserID: userID, Provider: PaymentProviderStripe}
	err = DB.Transaction(func(tx *gorm.DB) error {
		_, bindErr := bindStripeCustomerTx(tx, unknownAge, "cus_retired_old")
		return bindErr
	})
	assert.ErrorIs(t, err, ErrPaymentProviderMismatch)

	duplicate, err := RetireStripeCustomerBindingByAdmin(RetireStripeCustomerBindingInput{
		BindingID: binding.ID, UserID: userID, ExpectedVersion: 1, AdminID: 93,
		ActorIP: "192.0.2.93", Reason: "retire compromised Stripe customer binding safely",
	})
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)

	require.NoError(t, DB.Model(&User{}).Where("id = ?", userID).Update("stripe_customer", "cus_active_new").Error)
	require.NoError(t, DB.Create(&PaymentCustomerBinding{Provider: PaymentProviderStripe, CustomerKey: "cus_active_new", UserID: userID}).Error)
	oldOrder := &PaymentOrder{UserID: userID, Provider: PaymentProviderStripe, CreatedAt: result.Retirement.RetiredAt - 1}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		bound, err := bindStripeCustomerTx(tx, oldOrder, "cus_retired_old")
		assert.False(t, bound)
		return err
	}))
	require.NoError(t, DB.First(&user, userID).Error)
	assert.Equal(t, "cus_active_new", user.StripeCustomer)

	newOrder := &PaymentOrder{UserID: userID, Provider: PaymentProviderStripe, CreatedAt: result.Retirement.RetiredAt + 1}
	err = DB.Transaction(func(tx *gorm.DB) error {
		_, bindErr := bindStripeCustomerTx(tx, newOrder, "cus_retired_old")
		return bindErr
	})
	assert.ErrorIs(t, err, ErrPaymentProviderMismatch)

	updateErr := DB.Model(&PaymentCustomerBindingRetirement{}).Where("id = ?", result.Retirement.ID).Update("reason", "tampered").Error
	assert.ErrorIs(t, updateErr, ErrPaymentOperationsHistoryImmutable)
}

func TestRetireStripeCustomerBindingRejectsWrongUserAndVersion(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 975108, 0)
	seedPaymentUser(t, 975109, 0)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 975108).Update("stripe_customer", "cus_owner_guard").Error)
	binding := &PaymentCustomerBinding{Provider: PaymentProviderStripe, CustomerKey: "cus_owner_guard", UserID: 975108}
	require.NoError(t, DB.Create(binding).Error)

	_, err := RetireStripeCustomerBindingByAdmin(RetireStripeCustomerBindingInput{
		BindingID: binding.ID, UserID: 975109, ExpectedVersion: 1, AdminID: 94,
		Reason: "attempt to retire binding for the wrong user",
	})
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)
	_, err = RetireStripeCustomerBindingByAdmin(RetireStripeCustomerBindingInput{
		BindingID: binding.ID, UserID: 975108, ExpectedVersion: 2, AdminID: 94,
		Reason: "attempt to retire a stale binding version",
	})
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)

	var count int64
	require.NoError(t, DB.Model(&PaymentCustomerBinding{}).Where("id = ?", binding.ID).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestConcurrentStripeCustomerBindingRetirementCreatesOneHistory(t *testing.T) {
	truncateTables(t)
	const userID = 975116
	seedPaymentUser(t, userID, 0)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", userID).Update("stripe_customer", "cus_concurrent_retire").Error)
	binding := &PaymentCustomerBinding{Provider: PaymentProviderStripe, CustomerKey: "cus_concurrent_retire", UserID: userID}
	require.NoError(t, DB.Create(binding).Error)
	input := RetireStripeCustomerBindingInput{
		BindingID: binding.ID, UserID: userID, ExpectedVersion: 1, AdminID: 95,
		Reason: "concurrent retirement must preserve one immutable record",
	}
	results := make([]*RetireStripeCustomerBindingResult, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] = RetireStripeCustomerBindingByAdmin(input)
		}(index)
	}
	close(start)
	wait.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.NotEqual(t, results[0].Duplicate, results[1].Duplicate)

	var historyCount, auditCount int64
	require.NoError(t, DB.Model(&PaymentCustomerBindingRetirement{}).Where("original_binding_id = ?", binding.ID).Count(&historyCount).Error)
	require.NoError(t, DB.Model(&PaymentOperationsAudit{}).Where("action = ? AND subject_id = ?", PaymentOperationsActionStripeBindingRetire, binding.ID).Count(&auditCount).Error)
	assert.EqualValues(t, 1, historyCount)
	assert.EqualValues(t, 1, auditCount)
}

func TestUserDeletionRetainsStripeBindingsAndMappedLegacyInventory(t *testing.T) {
	t.Run("active customer binding", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 975110, 0)
		require.NoError(t, DB.Create(&PaymentCustomerBinding{
			Provider: PaymentProviderStripe, CustomerKey: "cus_delete_guard", UserID: 975110,
		}).Error)
		user := &User{Id: 975110}
		assert.ErrorIs(t, user.Delete(), ErrUserFinancialHistoryRequiresRetention)
	})

	t.Run("mapped legacy subscription", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 975111, 0)
		userID := 975111
		require.NoError(t, DB.Create(&StripeLegacySubscription{
			StripeSubscriptionID: "sub_delete_guard", StripeCustomerID: "cus_legacy_delete_guard",
			UserID: &userID, MappingStatus: StripeLegacyMappingMapped, Status: "active",
		}).Error)
		user := &User{Id: userID}
		assert.ErrorIs(t, user.Delete(), ErrUserFinancialHistoryRequiresRetention)
	})

	t.Run("unconsumed quote is removed", func(t *testing.T) {
		truncateTables(t)
		require.NoError(t, DB.AutoMigrate(&Midjourney{}))
		seedPaymentUser(t, 975112, 0)
		require.NoError(t, CreatePaymentQuote(&PaymentQuote{
			QuoteID: "Q_DELETE_DISPOSABLE", UserID: 975112, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderEpay, PaymentMethod: "alipay", ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}))
		user := &User{Id: 975112}
		require.NoError(t, user.Delete())
		var quoteCount int64
		require.NoError(t, DB.Model(&PaymentQuote{}).Where("user_id = ?", 975112).Count(&quoteCount).Error)
		assert.Zero(t, quoteCount)
		err := CreatePaymentQuote(&PaymentQuote{
			QuoteID: "Q_DELETE_BLOCKED", UserID: 975112, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderEpay, PaymentMethod: "alipay", ExpiresAt: time.Now().Add(time.Hour).Unix(),
		})
		assert.ErrorIs(t, err, ErrPaymentUserUnavailable)
	})
}

func TestGlobalPaymentQuoteCleanupDoesNotRequireAnotherUserRequest(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	cutoff := now - PaymentQuoteAuditRetentionSeconds
	quotes := []*PaymentQuote{
		{QuoteID: "Q_GLOBAL_CLEAN_OLD_EXPIRED", UserID: 975113, ExpiresAt: cutoff - 1, CreatedAt: cutoff - 100},
		{QuoteID: "Q_GLOBAL_CLEAN_OLD_CONSUMED", UserID: 975114, ConsumedAt: cutoff - 1, ExpiresAt: now + 100, CreatedAt: cutoff - 100},
		{QuoteID: "Q_GLOBAL_KEEP_ACTIVE", UserID: 975115, ExpiresAt: now + 100, CreatedAt: now},
	}
	for _, quote := range quotes {
		require.NoError(t, DB.Create(quote).Error)
	}
	first, err := CleanupPaymentQuotes(t.Context(), now, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, first.Scanned)
	assert.EqualValues(t, 1, first.Deleted)
	second, err := CleanupPaymentQuotes(t.Context(), now, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, second.Scanned)
	assert.EqualValues(t, 1, second.Deleted)

	var activeCount int64
	require.NoError(t, DB.Model(&PaymentQuote{}).Where("quote_id = ?", "Q_GLOBAL_KEEP_ACTIVE").Count(&activeCount).Error)
	assert.EqualValues(t, 1, activeCount)
}
