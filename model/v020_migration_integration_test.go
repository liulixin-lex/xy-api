package model

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// v020MigrationFixtureCommit is the immutable v0.2.0 tag source used to copy
// the versioned table contracts below. Keep these fixture structs independent
// from the current models: using current structs would silently turn this into
// a clean-install test whenever v0.2.1 adds a column.
const v020MigrationFixtureCommit = "ea3c0ef6cccddcf252f14f03abb11b2b74366233"

type v020UserFixture struct {
	Id                            int            `gorm:"primaryKey"`
	Username                      string         `gorm:"unique;index"`
	Password                      string         `gorm:"not null"`
	DisplayName                   string         `gorm:"index"`
	Role                          int            `gorm:"type:int;default:1"`
	Status                        int            `gorm:"type:int;default:1"`
	PaymentFrozen                 bool           `gorm:"not null;index"`
	Email                         string         `gorm:"index"`
	GitHubId                      string         `gorm:"column:github_id;index"`
	DiscordId                     string         `gorm:"column:discord_id;index"`
	OidcId                        string         `gorm:"column:oidc_id;index"`
	WeChatId                      string         `gorm:"column:wechat_id;index"`
	TelegramId                    string         `gorm:"column:telegram_id;index"`
	AccessToken                   *string        `gorm:"type:char(32);column:access_token;uniqueIndex"`
	Quota                         int            `gorm:"type:int;default:0"`
	UsedQuota                     int            `gorm:"type:int;default:0;column:used_quota"`
	RequestCount                  int            `gorm:"type:int;default:0"`
	Group                         string         `gorm:"type:varchar(64);default:'default'"`
	AffCode                       string         `gorm:"type:varchar(32);column:aff_code;uniqueIndex"`
	AffCount                      int            `gorm:"type:int;default:0;column:aff_count"`
	AffQuota                      int            `gorm:"type:int;default:0;column:aff_quota"`
	AffHistoryQuota               int            `gorm:"type:int;default:0;column:aff_history"`
	InviterId                     int            `gorm:"type:int;column:inviter_id;index"`
	InviteRewardRule              string         `gorm:"type:varchar(32);column:invite_reward_rule"`
	InviteRewardPercent           int            `gorm:"type:int;default:-1;column:invite_reward_percent"`
	InviteLinkBatchId             int            `gorm:"type:int;column:invite_link_batch_id;index"`
	InviteFirstTopupRewardPercent int            `gorm:"type:int;column:invite_first_topup_reward_percent"`
	InviteContinuousRewardPercent int            `gorm:"type:int;column:invite_continuous_reward_percent"`
	InviteRewardRulesSnapshot     string         `gorm:"type:text;column:invite_reward_rules_snapshot"`
	InviteFirstTopupRewardedAt    int64          `gorm:"column:invite_first_topup_rewarded_at;index"`
	InviteBoundAt                 int64          `gorm:"column:invite_bound_at"`
	DeletedAt                     gorm.DeletedAt `gorm:"index"`
	LinuxDOId                     string         `gorm:"column:linux_do_id;index"`
	Setting                       string         `gorm:"type:text;column:setting"`
	Remark                        string         `gorm:"type:varchar(255)"`
	StripeCustomer                string         `gorm:"type:varchar(64);column:stripe_customer;index"`
	CreatedAt                     int64          `gorm:"autoCreateTime;column:created_at"`
	LastLoginAt                   int64          `gorm:"default:0;column:last_login_at"`
}

func (v020UserFixture) TableName() string { return "users" }

type v020TopUpFixture struct {
	Id              int
	PaymentOrderId  *int64 `gorm:"index"`
	UserId          int    `gorm:"index"`
	Amount          int64
	Money           float64
	TradeNo         string `gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string `gorm:"type:varchar(50)"`
	PaymentProvider string `gorm:"type:varchar(50);default:''"`
	CreateTime      int64
	CompleteTime    int64
	Status          string
}

func (v020TopUpFixture) TableName() string { return "top_ups" }

type v020SubscriptionOrderFixture struct {
	Id                  int
	UserId              int     `gorm:"index;uniqueIndex:idx_subscription_balance_request,priority:1"`
	PlanId              int     `gorm:"index"`
	PaymentOrderId      *int64  `gorm:"index"`
	BalanceRequestId    *string `gorm:"type:varchar(128);uniqueIndex:idx_subscription_balance_request,priority:2"`
	Money               float64
	PlanSnapshot        string  `gorm:"type:text"`
	ExpectedAmountMinor int64   `gorm:"type:bigint;not null;default:0"`
	PaymentCurrency     string  `gorm:"type:varchar(8);not null;default:''"`
	ReserveUntil        int64   `gorm:"type:bigint;not null;default:0;index"`
	ProviderOrderId     string  `gorm:"type:varchar(255);default:'';index"`
	ProviderOrderKey    *string `gorm:"type:varchar(320);uniqueIndex"`
	ReviewReason        string  `gorm:"type:varchar(255);default:''"`
	TradeNo             string  `gorm:"unique;type:varchar(255);index"`
	PaymentMethod       string  `gorm:"type:varchar(50)"`
	PaymentProvider     string  `gorm:"type:varchar(50);default:''"`
	Status              string
	CreateTime          int64
	CompleteTime        int64
	ProviderPayload     string `gorm:"type:text"`
}

func (v020SubscriptionOrderFixture) TableName() string { return "subscription_orders" }

type v020UserSubscriptionFixture struct {
	Id                      int
	UserId                  int    `gorm:"index;index:idx_user_sub_active,priority:1"`
	PlanId                  int    `gorm:"index"`
	PaymentOrderId          *int64 `gorm:"uniqueIndex"`
	AmountTotal             int64  `gorm:"type:bigint;not null;default:0"`
	AmountUsed              int64  `gorm:"type:bigint;not null;default:0"`
	AmountUsedTotal         int64  `gorm:"type:bigint;not null;default:0"`
	UsageAccountingVersion  int    `gorm:"type:int;not null;default:0"`
	StartTime               int64  `gorm:"bigint"`
	EndTime                 int64  `gorm:"bigint;index;index:idx_user_sub_active,priority:3"`
	Status                  string `gorm:"type:varchar(32);index;index:idx_user_sub_active,priority:2"`
	Source                  string `gorm:"type:varchar(32);default:'order'"`
	LastResetTime           int64  `gorm:"type:bigint;default:0"`
	NextResetTime           int64  `gorm:"type:bigint;default:0;index"`
	QuotaResetVersion       int64  `gorm:"type:bigint;not null;default:0"`
	QuotaResetPeriod        string `gorm:"type:varchar(16);default:''"`
	QuotaResetCustomSeconds int64  `gorm:"type:bigint;default:0"`
	UpgradeGroup            string `gorm:"type:varchar(64);default:''"`
	PrevUserGroup           string `gorm:"type:varchar(64);default:''"`
	DowngradeGroup          string `gorm:"type:varchar(64);default:''"`
	AllowWalletOverflow     bool
	CreatedAt               int64 `gorm:"bigint"`
	UpdatedAt               int64 `gorm:"bigint"`
}

func (v020UserSubscriptionFixture) TableName() string { return "user_subscriptions" }

type v020PaymentQuoteFixture struct {
	ID                  int64  `gorm:"primaryKey"`
	QuoteID             string `gorm:"uniqueIndex;type:varchar(128)"`
	UserID              int    `gorm:"index"`
	OrderKind           string `gorm:"type:varchar(32)"`
	Provider            string `gorm:"type:varchar(32)"`
	PaymentMethod       string `gorm:"type:varchar(64)"`
	ProviderLivemode    *bool
	RequestedAmount     int64
	CreditQuota         int64
	ExpectedAmountMinor int64
	Currency            string `gorm:"type:varchar(8)"`
	PricingSnapshot     string `gorm:"type:text"`
	ProductSnapshot     string `gorm:"type:text"`
	ExpiresAt           int64  `gorm:"index"`
	ConsumedAt          int64
	CreatedAt           int64 `gorm:"index"`
}

func (v020PaymentQuoteFixture) TableName() string { return "payment_quotes" }

type v020PaymentUserGuardFixture struct {
	UserID    int `gorm:"primaryKey"`
	UpdatedAt int64
	Blocked   bool
}

func (v020PaymentUserGuardFixture) TableName() string { return "payment_user_guards" }

type v020PaymentOrderFixture struct {
	ID                           int64  `gorm:"primaryKey"`
	TradeNo                      string `gorm:"uniqueIndex;type:varchar(128)"`
	UserID                       int    `gorm:"index;uniqueIndex:idx_payment_user_request,priority:1"`
	OrderKind                    string `gorm:"type:varchar(32);index"`
	Provider                     string `gorm:"type:varchar(32);index"`
	PaymentMethod                string `gorm:"type:varchar(64)"`
	ProviderCredentialGeneration int64  `gorm:"index"`
	ProviderLivemode             *bool
	QuoteID                      string  `gorm:"type:varchar(128)"`
	RequestID                    string  `gorm:"type:varchar(128);uniqueIndex:idx_payment_user_request,priority:2"`
	ProviderOrderKey             *string `gorm:"type:varchar(320);uniqueIndex:idx_payment_orders_provider_order_key"`
	ProviderPaymentKey           *string `gorm:"type:varchar(320);uniqueIndex:idx_payment_orders_provider_payment_key"`
	ExpectedAmountMinor          int64
	PaidAmountMinor              int64
	Currency                     string `gorm:"type:varchar(8)"`
	RequestedAmount              int64
	CreditQuota                  int64
	PricingSnapshot              string `gorm:"type:text"`
	ProductSnapshot              string `gorm:"type:text"`
	StartFlow                    string `gorm:"type:varchar(32)"`
	StartPayload                 string `gorm:"type:text"`
	StartedAt                    int64
	ProviderCheckedAt            int64  `gorm:"index"`
	LegacyRecordType             string `gorm:"type:varchar(32)"`
	LegacyRecordID               int    `gorm:"index"`
	Status                       string `gorm:"type:varchar(32);index"`
	StatusReason                 string `gorm:"type:varchar(512)"`
	CredentialIncident           bool   `gorm:"index"`
	CredentialIncidentState      string `gorm:"type:varchar(32);index"`
	CredentialIncidentGeneration int64  `gorm:"index"`
	CredentialIncidentReason     string `gorm:"type:varchar(512)"`
	CredentialIncidentAt         int64  `gorm:"index"`
	CredentialIncidentReviewedAt int64
	CredentialIncidentReviewedBy int
	CredentialIncidentReviewNote string `gorm:"type:varchar(512)"`
	ExpiresAt                    int64  `gorm:"index"`
	SettledAt                    int64
	RefundedAmountMinor          int64
	DisputedAmountMinor          int64
	ReversedAmountMinor          int64
	ReversedQuota                int64
	CreatedAt                    int64 `gorm:"index"`
	UpdatedAt                    int64
	Version                      int64
}

func (v020PaymentOrderFixture) TableName() string { return "payment_orders" }

type v020PaymentEventFixture struct {
	ID                           int64  `gorm:"primaryKey"`
	Provider                     string `gorm:"type:varchar(32);index;uniqueIndex:idx_payment_event_key,priority:1"`
	EventKey                     string `gorm:"type:varchar(255);uniqueIndex:idx_payment_event_key,priority:2"`
	EventType                    string `gorm:"type:varchar(128)"`
	TradeNo                      string `gorm:"type:varchar(128);index"`
	PaymentOrderID               int64  `gorm:"index"`
	ProviderOrderKey             string `gorm:"type:varchar(320);index"`
	ProviderPaymentKey           string `gorm:"type:varchar(320);index"`
	ProviderResourceKey          string `gorm:"type:varchar(320);index"`
	ProviderCredentialGeneration int64  `gorm:"index"`
	ProviderLivemode             *bool  `gorm:"index"`
	CustomerID                   string `gorm:"type:varchar(255)"`
	ProviderCreatedAt            int64  `gorm:"index"`
	ProviderState                string `gorm:"type:varchar(64)"`
	PaidAmountMinor              int64
	RefundedAmountMinor          int64
	DisputedAmountMinor          int64
	Currency                     string `gorm:"type:varchar(8)"`
	PaymentMethod                string `gorm:"type:varchar(64)"`
	Paid                         bool
	Failed                       bool
	Expired                      bool
	Refunded                     bool
	Disputed                     bool
	DisputeResolved              bool
	DisputeWon                   bool
	PermanentFailure             bool
	ManualReview                 bool
	PayloadDigest                string `gorm:"type:varchar(128)"`
	NormalizedPayload            string `gorm:"type:text"`
	Status                       string `gorm:"type:varchar(32);index"`
	ReviewCode                   string `gorm:"type:varchar(64);index"`
	Attempts                     int
	LastError                    string `gorm:"type:varchar(1024)"`
	CreatedAt                    int64  `gorm:"index"`
	ProcessedAt                  int64
	UpdatedAt                    int64
}

func (v020PaymentEventFixture) TableName() string { return "payment_events" }

type v020PaymentLedgerEntryFixture struct {
	ID             int64  `gorm:"primaryKey"`
	PaymentOrderID int64  `gorm:"index;uniqueIndex:idx_payment_ledger_event,priority:1"`
	PaymentEventID int64  `gorm:"index;uniqueIndex:idx_payment_ledger_event,priority:2"`
	UserID         int    `gorm:"index"`
	EntryType      string `gorm:"type:varchar(48);index;uniqueIndex:idx_payment_ledger_event,priority:3"`
	AmountMinor    int64
	QuotaDelta     int64
	Currency       string `gorm:"type:varchar(8)"`
	Description    string `gorm:"type:varchar(255)"`
	CreatedAt      int64  `gorm:"index"`
}

func (v020PaymentLedgerEntryFixture) TableName() string { return "payment_ledger_entries" }

type v020BillingReservationFixture struct {
	Id                       int64  `gorm:"primaryKey"`
	RequestId                string `gorm:"type:varchar(64);uniqueIndex"`
	UserId                   int    `gorm:"index"`
	TokenId                  int    `gorm:"index"`
	FundingSource            string `gorm:"type:varchar(32);index"`
	SubscriptionId           int    `gorm:"index"`
	SubscriptionResetAt      int64  `gorm:"index"`
	SubscriptionResetVersion int64  `gorm:"index"`
	LegacyAdopted            bool   `gorm:"index"`
	ResourceType             string `gorm:"type:varchar(32);index"`
	ResourceId               string `gorm:"type:varchar(191);index"`
	InitialQuota             int
	ReservedQuota            int
	TokenReserved            int
	SettledQuota             int
	SettlementTarget         int
	SettlementPending        bool   `gorm:"index"`
	SettlementFailureCode    string `gorm:"type:varchar(64);index"`
	SettlementShortfallQuota int
	ShortfallFreezeApplied   bool `gorm:"index"`
	ShortfallPreviousStatus  int
	ShortfallDetectedAt      int64 `gorm:"index"`
	ShortfallResolvedAt      int64 `gorm:"index"`
	TokenMode                int
	Status                   string `gorm:"type:varchar(32);index"`
	Version                  int
	LastReconciledAt         int64  `gorm:"index"`
	ReconcileNote            string `gorm:"type:varchar(255)"`
	CreatedAt                int64  `gorm:"index"`
	UpdatedAt                int64  `gorm:"index"`
}

func (v020BillingReservationFixture) TableName() string { return "billing_reservations" }

type v020LogFixture struct {
	Id                int   `gorm:"index:idx_created_at_id,priority:2;index:idx_user_id_id,priority:2"`
	UserId            int   `gorm:"index;index:idx_user_id_id,priority:1"`
	CreatedAt         int64 `gorm:"bigint;index:idx_created_at_id,priority:1;index:idx_created_at_type"`
	Type              int   `gorm:"index:idx_created_at_type"`
	Content           string
	Username          string `gorm:"index;index:index_username_model_name,priority:2;default:''"`
	TokenName         string `gorm:"index;default:''"`
	ModelName         string `gorm:"index;index:index_username_model_name,priority:1;default:''"`
	Quota             int    `gorm:"default:0"`
	PromptTokens      int    `gorm:"default:0"`
	CompletionTokens  int    `gorm:"default:0"`
	UseTime           int    `gorm:"default:0"`
	IsStream          bool
	ChannelId         int    `gorm:"column:channel;index"`
	TokenId           int    `gorm:"default:0;index"`
	Group             string `gorm:"index"`
	Ip                string `gorm:"index;default:''"`
	RequestId         string `gorm:"type:varchar(64);index:idx_logs_request_id;default:''"`
	UpstreamRequestId string `gorm:"type:varchar(128);index:idx_logs_upstream_request_id;default:''"`
	Other             string
}

func (v020LogFixture) TableName() string { return "logs" }

type v020PaymentMigrationRows struct {
	user                v020UserFixture
	topUp               v020TopUpFixture
	subscriptionOrder   v020SubscriptionOrderFixture
	userSubscription    v020UserSubscriptionFixture
	quote               v020PaymentQuoteFixture
	guard               v020PaymentUserGuardFixture
	topUpOrder          v020PaymentOrderFixture
	subscriptionPayment v020PaymentOrderFixture
	event               v020PaymentEventFixture
	ledger              v020PaymentLedgerEntryFixture
	billingReservation  v020BillingReservationFixture
}

func TestV020ToV021SQLiteMigration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "v020-to-v021.db")), &gorm.Config{PrepareStmt: false})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	runV020ToV021MigrationContract(t, db, common.DatabaseTypeSQLite)
}

func TestV020ToV021MySQL57Migration(t *testing.T) {
	dsn := os.Getenv("MYSQL57_TEST_V020_UPGRADE_DSN")
	if dsn == "" {
		t.Skip("dedicated MySQL 5.7 v0.2.0 upgrade database is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{PrepareStmt: false})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	requireDedicatedEmptyPaymentSchema(t, db, common.DatabaseTypeMySQL)
	runV020ToV021MigrationContract(t, db, common.DatabaseTypeMySQL)
}

func TestV020ToV021PostgreSQL96Migration(t *testing.T) {
	dsn := os.Getenv("POSTGRES96_TEST_V020_UPGRADE_DSN")
	if dsn == "" {
		t.Skip("dedicated PostgreSQL 9.6 v0.2.0 upgrade database is not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{PrepareStmt: false})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	requireDedicatedEmptyPaymentSchema(t, db, common.DatabaseTypePostgreSQL)
	runV020ToV021MigrationContract(t, db, common.DatabaseTypePostgreSQL)
}

func runV020ToV021MigrationContract(t *testing.T, db *gorm.DB, databaseType common.DatabaseType) {
	t.Helper()

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

	fixtureDB := db
	if databaseType == common.DatabaseTypeMySQL {
		var err error
		fixtureDB, err = prepareMySQLMigrationDB(db, 700, nil)
		require.NoError(t, err)
	}
	require.NoError(t, fixtureDB.AutoMigrate(
		&v020UserFixture{},
		&v020TopUpFixture{},
		&v020SubscriptionOrderFixture{},
		&v020UserSubscriptionFixture{},
		&v020PaymentQuoteFixture{},
		&v020PaymentUserGuardFixture{},
		&v020PaymentOrderFixture{},
		&v020PaymentEventFixture{},
		&v020PaymentLedgerEntryFixture{},
		&v020BillingReservationFixture{},
	))

	rows := createV020PaymentMigrationRows()
	for _, row := range []interface{}{
		&rows.user,
		&rows.topUpOrder,
		&rows.subscriptionPayment,
		&rows.topUp,
		&rows.subscriptionOrder,
		&rows.userSubscription,
		&rows.quote,
		&rows.guard,
		&rows.event,
		&rows.ledger,
		&rows.billingReservation,
	} {
		require.NoError(t, db.Create(row).Error)
	}

	assert.Equal(t, v020MigrationFixtureCommit, "ea3c0ef6cccddcf252f14f03abb11b2b74366233")
	require.False(t, db.Migrator().HasColumn(&v020TopUpFixture{}, "CreditQuotaSnapshot"))
	require.False(t, db.Migrator().HasColumn(&v020PaymentOrderFixture{}, "ConfigurationVersion"))
	require.False(t, db.Migrator().HasColumn(&v020PaymentOrderFixture{}, "CreationFenceToken"))
	require.False(t, db.Migrator().HasColumn(&v020PaymentOrderFixture{}, "BrowserAuthorizationDigest"))
	require.False(t, db.Migrator().HasTable(&PaymentTask{}))
	require.False(t, db.Migrator().HasTable(&PaymentLimitPolicy{}))
	require.False(t, db.Migrator().HasTable(&PaymentLimitBucket{}))
	require.False(t, db.Migrator().HasTable(&PaymentLimitReservation{}))

	require.NoError(t, migrateDBSafely())
	assertV020PaymentMigrationRows(t, db, rows)
	assertV021PaymentMigrationSchema(t, db)

	limitPolicy := PaymentLimitPolicy{
		ID: 8101, Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayAlipay,
		Currency: "CNY", SingleLimitMinor: 199900, DailyLimitMinor: 1999900,
		Timezone: "Asia/Shanghai", Enabled: true, CreatedAt: 1711000000, UpdatedAt: 1711000100, Version: 4,
	}
	limitBucket := PaymentLimitBucket{
		ID: 8102, Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayAlipay,
		Currency: "CNY", DayKey: "2024-03-21", ReservedMinor: 1250, PaidMinor: 5000,
		CreatedAt: 1711000000, UpdatedAt: 1711000100, Version: 7,
	}
	limitReservation := PaymentLimitReservation{
		ID: 8103, PaymentOrderID: rows.topUpOrder.ID, Provider: PaymentProviderXorPay,
		PaymentMethod: PaymentMethodXorPayAlipay, Currency: "CNY", DayKey: "2024-03-21",
		AmountMinor: 1250, Status: PaymentLimitReservationActive, ExpiresAt: 1711003600,
		CreatedAt: 1711000000, UpdatedAt: 1711000100,
	}
	require.NoError(t, db.Create(&limitPolicy).Error)
	require.NoError(t, db.Create(&limitBucket).Error)
	require.NoError(t, db.Create(&limitReservation).Error)

	require.NoError(t, migrateDBSafely(), "v0.2.1 migration must be idempotent")
	assertV020PaymentMigrationRows(t, db, rows)
	assertV021PaymentMigrationSchema(t, db)
	var storedPolicy PaymentLimitPolicy
	var storedBucket PaymentLimitBucket
	var storedReservation PaymentLimitReservation
	require.NoError(t, db.First(&storedPolicy, limitPolicy.ID).Error)
	require.NoError(t, db.First(&storedBucket, limitBucket.ID).Error)
	require.NoError(t, db.First(&storedReservation, limitReservation.ID).Error)
	assert.Equal(t, limitPolicy, storedPolicy)
	assert.Equal(t, limitBucket, storedBucket)
	assert.Equal(t, limitReservation, storedReservation)
}

func assertV021PaymentMigrationSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, column := range []string{
		"Currency",
		"ExpectedAmountMinor",
		"CreditQuotaSnapshot",
		"ProviderOrderId",
		"ProviderOrderKey",
		"ReviewReason",
	} {
		assert.True(t, db.Migrator().HasColumn(&TopUp{}, column), "top_ups.%s", column)
	}
	for _, column := range []string{
		"ConfigurationVersion",
		"CreationFenceToken",
		"BrowserAuthorizationDigest",
		"BrowserAuthorizationPayload",
		"BrowserAuthorizationExpiresAt",
		"BrowserAuthorizedAt",
	} {
		assert.True(t, db.Migrator().HasColumn(&PaymentOrder{}, column), "payment_orders.%s", column)
	}

	indexContracts := []struct {
		model   interface{}
		indexes []string
	}{
		{model: &TopUp{}, indexes: []string{
			"idx_top_ups_provider_order_id",
			"idx_topup_provider_order_key",
		}},
		{model: &PaymentOrder{}, indexes: []string{
			"idx_payment_orders_configuration_version",
			"idx_payment_orders_browser_authorization_digest",
			"idx_payment_orders_browser_authorization_expires_at",
		}},
		{model: &PaymentTask{}, indexes: []string{
			"idx_payment_tasks_task_id",
			"idx_payment_tasks_payment_order_id",
			"idx_payment_tasks_operation",
			"idx_payment_task_subject",
			"idx_payment_tasks_status",
			"idx_payment_tasks_phase",
			"idx_payment_tasks_available_at",
			"idx_payment_tasks_lease_owner",
			"idx_payment_tasks_lease_until",
			"idx_payment_tasks_last_error_code",
			"idx_payment_tasks_created_at",
			"idx_payment_tasks_updated_at",
			"idx_payment_tasks_finished_at",
		}},
		{model: &PaymentLimitPolicy{}, indexes: []string{
			"idx_payment_limit_policy_channel_v2",
			"idx_payment_limit_policies_enabled",
			"idx_payment_limit_policies_created_at",
			"idx_payment_limit_policies_updated_at",
		}},
		{model: &PaymentLimitBucket{}, indexes: []string{
			"idx_payment_limit_bucket_v2",
			"idx_payment_limit_buckets_created_at",
			"idx_payment_limit_buckets_updated_at",
		}},
		{model: &PaymentLimitReservation{}, indexes: []string{
			"idx_payment_limit_reservations_payment_order_id",
			"idx_payment_limit_reservations_provider",
			"idx_payment_limit_reservations_payment_method",
			"idx_payment_limit_reservations_currency",
			"idx_payment_limit_reservations_day_key",
			"idx_payment_limit_reservations_paid_day_key",
			"idx_payment_limit_reservations_paid_at",
			"idx_payment_limit_reservations_status",
			"idx_payment_limit_reservations_expires_at",
			"idx_payment_limit_reservations_over_limit",
			"idx_payment_limit_reservations_created_at",
			"idx_payment_limit_reservations_updated_at",
		}},
	}
	for _, contract := range indexContracts {
		assert.True(t, db.Migrator().HasTable(contract.model), "%T", contract.model)
		for _, index := range contract.indexes {
			assert.True(t, db.Migrator().HasIndex(contract.model, index), "%T index %s", contract.model, index)
		}
	}
}

func createV020PaymentMigrationRows() v020PaymentMigrationRows {
	livemode := false
	accessToken := "fixture-access-token-v020-000001"
	topUpOrderID := int64(3001)
	subscriptionOrderID := int64(3002)
	providerOrderKey := "xorpay:v020-order-3001"
	providerPaymentKey := "xorpay:v020-payment-3001"
	subscriptionProviderKey := "stripe:v020-checkout-3002"
	balanceRequestID := "v020-balance-request"
	return v020PaymentMigrationRows{
		user: v020UserFixture{
			Id: 101, Username: "v020-user", Password: "v020-password-hash", DisplayName: "v0.2.0 User",
			Role: 1, Status: 1, Email: "v020@example.invalid", AccessToken: &accessToken,
			Quota: 987654, UsedQuota: 12345, RequestCount: 77, Group: "premium", AffCode: "V020MIGRATION",
			Setting: `{"notify_type":"email"}`, Remark: "v0.2.0 migration fixture", CreatedAt: 1711000000,
		},
		topUp: v020TopUpFixture{
			Id: 2001, PaymentOrderId: &topUpOrderID, UserId: 101, Amount: 125000, Money: 12.5,
			TradeNo: "TU_V020_PRESERVE", PaymentMethod: PaymentMethodXorPayAlipay,
			PaymentProvider: PaymentProviderXorPay, CreateTime: 1711000001, CompleteTime: 1711000010, Status: "success",
		},
		subscriptionOrder: v020SubscriptionOrderFixture{
			Id: 2101, UserId: 101, PlanId: 41, PaymentOrderId: &subscriptionOrderID,
			BalanceRequestId: &balanceRequestID, Money: 19.99,
			PlanSnapshot:        `{"version":1,"plan_id":41,"title":"v0.2.0 preserved plan"}`,
			ExpectedAmountMinor: 1999, PaymentCurrency: "USD", ReserveUntil: 1711003600,
			ProviderOrderId: "cs_v020_preserve", ProviderOrderKey: &subscriptionProviderKey,
			TradeNo: "SO_V020_PRESERVE", PaymentMethod: PaymentMethodStripe,
			PaymentProvider: PaymentProviderStripe, Status: "completed", CreateTime: 1711000002,
			CompleteTime: 1711000020, ProviderPayload: `{"checkout_session":"cs_v020_preserve"}`,
		},
		userSubscription: v020UserSubscriptionFixture{
			Id: 2201, UserId: 101, PlanId: 41, PaymentOrderId: &subscriptionOrderID,
			AmountTotal: 500000, AmountUsed: 5000, AmountUsedTotal: 7000, UsageAccountingVersion: 1,
			StartTime: 1711000020, EndTime: 1713592020, Status: "active", Source: "order",
			LastResetTime: 1711000020, NextResetTime: 1711604820, QuotaResetVersion: 3,
			QuotaResetPeriod: "weekly", UpgradeGroup: "premium", PrevUserGroup: "default",
			DowngradeGroup: "default", AllowWalletOverflow: true, CreatedAt: 1711000020, UpdatedAt: 1711000030,
		},
		quote: v020PaymentQuoteFixture{
			ID: 2901, QuoteID: "Q_V020_PRESERVE", UserID: 101, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayAlipay,
			ProviderLivemode: &livemode, RequestedAmount: 1250, CreditQuota: 125000,
			ExpectedAmountMinor: 1250, Currency: "CNY", PricingSnapshot: `{"quota":125000}`,
			ExpiresAt: 1711003600, ConsumedAt: 1711000001, CreatedAt: 1711000000,
		},
		guard: v020PaymentUserGuardFixture{UserID: 101, UpdatedAt: 1711000000},
		topUpOrder: v020PaymentOrderFixture{
			ID: topUpOrderID, TradeNo: "PO_V020_TOPUP", UserID: 101, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayAlipay,
			ProviderCredentialGeneration: 2, ProviderLivemode: &livemode, QuoteID: "Q_V020_PRESERVE",
			RequestID: "REQ_V020_TOPUP", ProviderOrderKey: &providerOrderKey,
			ProviderPaymentKey: &providerPaymentKey, ExpectedAmountMinor: 1250, PaidAmountMinor: 1250,
			Currency: "CNY", RequestedAmount: 1250, CreditQuota: 125000,
			PricingSnapshot: `{"quota":125000}`, StartFlow: "qr",
			StartPayload: `{"flow":"qr","trade_no":"PO_V020_TOPUP"}`, StartedAt: 1711000002,
			ProviderCheckedAt: 1711000010, LegacyRecordType: "topup", LegacyRecordID: 2001,
			Status: PaymentOrderStatusFulfilled, ExpiresAt: 1711003600, SettledAt: 1711000010,
			CreatedAt: 1711000001, UpdatedAt: 1711000010, Version: 5,
		},
		subscriptionPayment: v020PaymentOrderFixture{
			ID: subscriptionOrderID, TradeNo: "PO_V020_SUBSCRIPTION", UserID: 101,
			OrderKind: PaymentOrderKindSubscription, Provider: PaymentProviderStripe,
			PaymentMethod: PaymentMethodStripe, ProviderCredentialGeneration: 3,
			ProviderLivemode: &livemode, RequestID: "REQ_V020_SUBSCRIPTION",
			ProviderOrderKey: &subscriptionProviderKey, ExpectedAmountMinor: 1999, PaidAmountMinor: 1999,
			Currency: "USD", RequestedAmount: 1, ProductSnapshot: `{"plan_id":41}`,
			StartFlow: "redirect", StartPayload: `{"url":"https://checkout.stripe.com/c/pay/v020"}`,
			StartedAt: 1711000003, LegacyRecordType: "subscription", LegacyRecordID: 2101,
			Status: PaymentOrderStatusFulfilled, ExpiresAt: 1711003600, SettledAt: 1711000020,
			CreatedAt: 1711000002, UpdatedAt: 1711000020, Version: 4,
		},
		event: v020PaymentEventFixture{
			ID: 4001, Provider: PaymentProviderXorPay, EventKey: "xorpay:v020-event-4001",
			EventType: "payment.success", TradeNo: "PO_V020_TOPUP", PaymentOrderID: topUpOrderID,
			ProviderOrderKey: providerOrderKey, ProviderPaymentKey: providerPaymentKey,
			ProviderCredentialGeneration: 2, ProviderLivemode: &livemode, ProviderCreatedAt: 1711000009,
			ProviderState: "success", PaidAmountMinor: 1250, Currency: "CNY",
			PaymentMethod: PaymentMethodXorPayAlipay, Paid: true,
			PayloadDigest: "v020-payload-digest", NormalizedPayload: `{"status":"success"}`,
			Status: PaymentEventStatusProcessed, Attempts: 1, CreatedAt: 1711000009,
			ProcessedAt: 1711000010, UpdatedAt: 1711000010,
		},
		ledger: v020PaymentLedgerEntryFixture{
			ID: 5001, PaymentOrderID: topUpOrderID, PaymentEventID: 4001, UserID: 101,
			EntryType: PaymentLedgerEntryCredit, AmountMinor: 1250, QuotaDelta: 125000,
			Currency: "CNY", Description: "v0.2.0 fulfilled top-up", CreatedAt: 1711000010,
		},
		billingReservation: v020BillingReservationFixture{
			Id: 6001, RequestId: "req-v020-billing", UserId: 101, TokenId: 301,
			FundingSource: "wallet", ResourceType: "relay", ResourceId: "chatcmpl-v020",
			InitialQuota: 800, ReservedQuota: 800, TokenReserved: 800, SettledQuota: 750,
			SettlementTarget: 750, TokenMode: 1, Status: "settled", Version: 2,
			LastReconciledAt: 1711000040, ReconcileNote: "v0.2.0 preserved reservation",
			CreatedAt: 1711000030, UpdatedAt: 1711000040,
		},
	}
}

func assertV020PaymentMigrationRows(t *testing.T, db *gorm.DB, expected v020PaymentMigrationRows) {
	t.Helper()
	assertV020Row(t, db, expected.user.Id, expected.user)
	assertV020Row(t, db, expected.topUp.Id, expected.topUp)
	assertV020Row(t, db, expected.subscriptionOrder.Id, expected.subscriptionOrder)
	assertV020Row(t, db, expected.userSubscription.Id, expected.userSubscription)
	assertV020Row(t, db, expected.quote.ID, expected.quote)
	assertV020Row(t, db, expected.guard.UserID, expected.guard)
	assertV020Row(t, db, expected.topUpOrder.ID, expected.topUpOrder)
	assertV020Row(t, db, expected.subscriptionPayment.ID, expected.subscriptionPayment)
	assertV020Row(t, db, expected.event.ID, expected.event)
	assertV020Row(t, db, expected.ledger.ID, expected.ledger)
	assertV020Row(t, db, expected.billingReservation.Id, expected.billingReservation)
}

func assertV020Row[T any](t *testing.T, db *gorm.DB, primaryKey interface{}, expected T) {
	t.Helper()
	var actual T
	require.NoError(t, db.First(&actual, primaryKey).Error)
	assert.Equal(t, expected, actual)
}
