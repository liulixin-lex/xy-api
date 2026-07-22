package model

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPaymentMySQL57IntegrationContracts(t *testing.T) {
	dsn := os.Getenv("PAYMENT_MYSQL57_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated MySQL 5.7 payment integration database is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{PrepareStmt: false})
	require.NoError(t, err)
	runPaymentDatabaseIntegrationContracts(t, db, common.DatabaseTypeMySQL)
}

func TestPaymentPostgreSQL96IntegrationContracts(t *testing.T) {
	dsn := os.Getenv("PAYMENT_POSTGRES96_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated PostgreSQL 9.6 payment integration database is not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{PrepareStmt: false})
	require.NoError(t, err)
	runPaymentDatabaseIntegrationContracts(t, db, common.DatabaseTypePostgreSQL)
}

func runPaymentDatabaseIntegrationContracts(t *testing.T, db *gorm.DB, databaseType common.DatabaseType) {
	t.Helper()
	requireDedicatedEmptyPaymentSchema(t, db, databaseType)

	previousDB := DB
	previousLogDB := LOG_DB
	previousMainType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	DB = db
	LOG_DB = db
	common.SetDatabaseTypes(databaseType, databaseType)
	initCol()
	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		initCol()
	})

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(24)
	sqlDB.SetMaxIdleConns(24)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	t.Setenv("PAYMENT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	migrationDB := db
	if databaseType == common.DatabaseTypeMySQL {
		migrationDB, err = prepareMySQLMigrationDB(db, 700, []string{"payment_orders"})
		require.NoError(t, err)
	}
	require.NoError(t, migrationDB.AutoMigrate(
		&User{},
		&TopUp{},
		&PaymentQuote{},
		&PaymentUserGuard{},
		&PaymentOrder{},
		&PaymentTask{},
		&PaymentEvent{},
		&PaymentLimitPolicy{},
		&PaymentLimitBucket{},
		&PaymentLimitReservation{},
	))
	for _, userID := range []int{985001, 985011, 985012, 985013, 985021} {
		require.NoError(t, db.Create(&User{
			Id: userID, Username: fmt.Sprintf("payment-db-user-%d", userID), Password: "integration-test-password",
			Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 1_000_000,
			AffCode: fmt.Sprintf("PAYDB%d", userID),
		}).Error)
	}
	require.NoError(t, migrationDB.AutoMigrate(
		&TopUp{},
		&PaymentOrder{},
		&PaymentTask{},
		&PaymentEvent{},
		&PaymentLimitPolicy{},
		&PaymentLimitBucket{},
		&PaymentLimitReservation{},
	), "payment migrations must be idempotent")
	for _, model := range []interface{}{
		&TopUp{},
		&PaymentOrder{},
		&PaymentTask{},
		&PaymentEvent{},
		&PaymentLimitPolicy{},
		&PaymentLimitBucket{},
		&PaymentLimitReservation{},
	} {
		assert.True(t, db.Migrator().HasTable(model), "%T", model)
	}
	for _, index := range []struct {
		model interface{}
		name  string
	}{
		{model: &TopUp{}, name: "idx_topup_provider_order_key"},
		{model: &PaymentTask{}, name: "idx_payment_task_subject"},
		{model: &PaymentLimitPolicy{}, name: "idx_payment_limit_policy_channel_v2"},
		{model: &PaymentLimitBucket{}, name: "idx_payment_limit_bucket_v2"},
		{model: &PaymentLimitReservation{}, name: "idx_payment_limit_reservations_payment_order_id"},
	} {
		assert.True(t, db.Migrator().HasIndex(index.model, index.name), index.name)
	}
	for _, column := range []string{
		"CreationFenceToken",
		"BrowserAuthorizationDigest",
		"BrowserAuthorizationPayload",
		"BrowserAuthorizationExpiresAt",
		"BrowserAuthorizedAt",
	} {
		assert.True(t, db.Migrator().HasColumn(&PaymentOrder{}, column), column)
	}
	for _, column := range []string{
		"Currency",
		"ExpectedAmountMinor",
		"CreditQuotaSnapshot",
		"ProviderOrderId",
		"ProviderOrderKey",
		"ReviewReason",
	} {
		assert.True(t, db.Migrator().HasColumn(&TopUp{}, column), column)
	}

	t.Run("concurrent order idempotency creates one order and one task", func(t *testing.T) {
		now := time.Now().Unix()
		quote := &PaymentQuote{
			QuoteID: "Q_DB_IDEMPOTENCY", UserID: 985001, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderEpay, PaymentMethod: "wechat", RequestedAmount: 5,
			CreditQuota: 500, ExpectedAmountMinor: 500, Currency: "CNY", PricingSnapshot: `{}`,
			ExpiresAt: now + 600,
		}
		require.NoError(t, CreatePaymentQuote(quote))

		start := make(chan struct{})
		results := make(chan *PaymentOrder, 8)
		errorsCh := make(chan error, 8)
		var wait sync.WaitGroup
		for range 8 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				order, err := CreatePaymentOrderFromQuote(quote.UserID, quote.QuoteID, "db-idempotency-request")
				results <- order
				errorsCh <- err
			}()
		}
		close(start)
		wait.Wait()
		close(results)
		close(errorsCh)

		for err := range errorsCh {
			require.NoError(t, err)
		}
		var orderID int64
		for order := range results {
			require.NotNil(t, order)
			if orderID == 0 {
				orderID = order.ID
			}
			assert.Equal(t, orderID, order.ID)
		}
		var orderCount int64
		require.NoError(t, db.Model(&PaymentOrder{}).
			Where("user_id = ? AND request_id = ?", quote.UserID, "db-idempotency-request").Count(&orderCount).Error)
		assert.EqualValues(t, 1, orderCount)
		var taskCount int64
		require.NoError(t, db.Model(&PaymentTask{}).
			Where("payment_order_id = ? AND operation = ?", orderID, PaymentTaskOperationCreate).Count(&taskCount).Error)
		assert.EqualValues(t, 1, taskCount)
	})

	t.Run("daily limit reservation is atomic and supports settle and release", func(t *testing.T) {
		require.NoError(t, UpsertPaymentLimitPolicy(&PaymentLimitPolicy{
			Provider: PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY",
			SingleLimitMinor: 700, DailyLimitMinor: 1000, Timezone: "UTC", Enabled: true,
		}))
		now := time.Now().Unix()
		quotes := []*PaymentQuote{
			{
				QuoteID: "Q_DB_LIMIT_A", UserID: 985011, OrderKind: PaymentOrderKindTopUp,
				Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestedAmount: 6,
				CreditQuota: 600, ExpectedAmountMinor: 600, Currency: "CNY", PricingSnapshot: `{}`, ExpiresAt: now + 600,
			},
			{
				QuoteID: "Q_DB_LIMIT_B", UserID: 985012, OrderKind: PaymentOrderKindTopUp,
				Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestedAmount: 6,
				CreditQuota: 600, ExpectedAmountMinor: 600, Currency: "CNY", PricingSnapshot: `{}`, ExpiresAt: now + 600,
			},
		}
		for _, quote := range quotes {
			require.NoError(t, CreatePaymentQuote(quote))
		}

		start := make(chan struct{})
		orders := make([]*PaymentOrder, len(quotes))
		errs := make([]error, len(quotes))
		var wait sync.WaitGroup
		for index := range quotes {
			index := index
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				orders[index], errs[index] = CreatePaymentOrderFromQuote(
					quotes[index].UserID, quotes[index].QuoteID, fmt.Sprintf("db-limit-%d", index),
				)
			}()
		}
		close(start)
		wait.Wait()

		winner := -1
		for index, err := range errs {
			if err == nil {
				require.Equal(t, -1, winner, "only one reservation may consume the remaining daily capacity")
				winner = index
				continue
			}
			assert.ErrorIs(t, err, ErrPaymentDailyLimitExceeded)
		}
		require.NotEqual(t, -1, winner)

		policy, err := GetPaymentLimitPolicy(PaymentProviderEpay, "alipay", "CNY")
		require.NoError(t, err)
		require.NotNil(t, policy)
		usage, err := CurrentPaymentLimitUsage(*policy, now)
		require.NoError(t, err)
		assert.Equal(t, int64(600), usage.ReservedMinor)
		assert.Zero(t, usage.PaidMinor)

		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return settlePaymentLimitReservationTx(tx, orders[winner], now)
		}))
		usage, err = CurrentPaymentLimitUsage(*policy, now)
		require.NoError(t, err)
		assert.Zero(t, usage.ReservedMinor)
		assert.Equal(t, int64(600), usage.PaidMinor)

		releaseQuote := &PaymentQuote{
			QuoteID: "Q_DB_LIMIT_RELEASE", UserID: 985013, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestedAmount: 4,
			CreditQuota: 400, ExpectedAmountMinor: 400, Currency: "CNY", PricingSnapshot: `{}`, ExpiresAt: now + 600,
		}
		require.NoError(t, CreatePaymentQuote(releaseQuote))
		releaseOrder, err := CreatePaymentOrderFromQuote(releaseQuote.UserID, releaseQuote.QuoteID, "db-limit-release")
		require.NoError(t, err)
		usage, err = CurrentPaymentLimitUsage(*policy, now)
		require.NoError(t, err)
		assert.Equal(t, int64(400), usage.ReservedMinor)
		assert.Equal(t, int64(600), usage.PaidMinor)

		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return releasePaymentLimitReservationTx(tx, releaseOrder, now)
		}))
		usage, err = CurrentPaymentLimitUsage(*policy, now)
		require.NoError(t, err)
		assert.Zero(t, usage.ReservedMinor)
		assert.Equal(t, int64(600), usage.PaidMinor)
	})

	t.Run("task leases and creation fencing reject stale workers", func(t *testing.T) {
		now := time.Now().Unix()
		require.NoError(t, db.Model(&PaymentTask{}).
			Where("status IN ?", []string{PaymentTaskStatusPending, PaymentTaskStatusRunning, PaymentTaskStatusRetryWait}).
			Updates(map[string]interface{}{
				"status":      PaymentTaskStatusSucceeded,
				"lease_owner": "",
				"lease_until": 0,
				"finished_at": now,
				"updated_at":  now,
			}).Error)
		order := &PaymentOrder{
			TradeNo: "PO_DB_FENCE", UserID: 985021, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayNative,
			RequestID: "db-fence", ExpectedAmountMinor: 100, Currency: "CNY",
			RequestedAmount: 1, CreditQuota: 100, Status: PaymentOrderStatusPending,
			ExpiresAt: now + 3600, CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		require.NoError(t, db.Create(order).Error)
		_, err := EnsurePaymentTask(order.ID, PaymentTaskOperationCreate, now)
		require.NoError(t, err)

		start := make(chan struct{})
		claims := make(chan []*PaymentTask, 12)
		errorsCh := make(chan error, 12)
		var wait sync.WaitGroup
		for index := range 12 {
			index := index
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				claimed, claimErr := ClaimDuePaymentTasks(t.Context(), fmt.Sprintf("db-runner-%d", index), now, time.Minute, 1)
				claims <- claimed
				errorsCh <- claimErr
			}()
		}
		close(start)
		wait.Wait()
		close(claims)
		close(errorsCh)
		for claimErr := range errorsCh {
			require.NoError(t, claimErr)
		}
		var first *PaymentTask
		claimCount := 0
		for claimed := range claims {
			claimCount += len(claimed)
			if len(claimed) == 1 {
				first = claimed[0]
			}
		}
		require.Equal(t, 1, claimCount)
		require.NotNil(t, first)

		require.NoError(t, db.Model(&PaymentTask{}).Where("id = ?", first.ID).Update("lease_until", now-1).Error)
		secondClaims, err := ClaimDuePaymentTasks(t.Context(), "db-successor", now, time.Minute, 1)
		require.NoError(t, err)
		require.Len(t, secondClaims, 1)
		second := secondClaims[0]
		assert.Greater(t, second.FenceToken, first.FenceToken)

		payload := `{"flow":"qr","trade_no":"PO_DB_FENCE","qr_content":"weixin://wxpay/db-fence","expires_at":9999999999}`
		err = SavePaymentOrderStartWithProviderIdentityFenced(order.TradeNo, "qr", payload, now+1800,
			"xorpay:stale", "", first.FenceToken)
		assert.ErrorIs(t, err, ErrPaymentTaskLeaseLost)
		require.NoError(t, SavePaymentOrderStartWithProviderIdentityFenced(order.TradeNo, "qr", payload, now+1800,
			"xorpay:current", "", second.FenceToken))
		assert.ErrorIs(t, FinishPaymentTask(first, first.LeaseOwner, PaymentTaskStatusSucceeded, "", ""), ErrPaymentTaskLeaseLost)

		stored, err := GetPaymentOrderByTradeNo(order.TradeNo)
		require.NoError(t, err)
		require.NotNil(t, stored.ProviderOrderKey)
		assert.Equal(t, "xorpay:current", *stored.ProviderOrderKey)
	})
}

func requireDedicatedEmptyPaymentSchema(t *testing.T, db *gorm.DB, databaseType common.DatabaseType) {
	t.Helper()
	var count int64
	switch databaseType {
	case common.DatabaseTypeMySQL:
		require.NoError(t, db.Raw(
			"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'",
		).Scan(&count).Error)
	case common.DatabaseTypePostgreSQL:
		require.NoError(t, db.Raw(
			"SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname = current_schema()",
		).Scan(&count).Error)
	default:
		require.FailNow(t, "unsupported payment integration database", databaseType)
	}
	require.Zero(t, count, "payment integration DSN must point to a dedicated empty schema")
}
