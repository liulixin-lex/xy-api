package model

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRoutingChannelBindingCredentialsRoundTripAndMaskJSON(t *testing.T) {
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := RoutingChannelBinding{ChannelID: 1001, UpstreamType: RoutingUpstreamTypeNewAPI}
	creds := RoutingCredentials{
		NewAPIAccessToken: "newapi-token-secret",
		GatewayAPIKey:     "gateway-key-secret",
	}

	require.NoError(t, binding.SetCredentials(creds))
	require.NotNil(t, binding.EncCredentials)
	assert.NotContains(t, *binding.EncCredentials, "newapi-token-secret")
	assert.Equal(t, RoutingCredentialKeyVersion, binding.KeyVersion)

	decoded, err := binding.GetCredentials()
	require.NoError(t, err)
	assert.Equal(t, creds, decoded)

	jsonBytes, err := common.Marshal(binding)
	require.NoError(t, err)
	jsonText := string(jsonBytes)
	assert.NotContains(t, jsonText, "newapi-token-secret")
	assert.NotContains(t, jsonText, "gateway-key-secret")
	assert.NotContains(t, jsonText, "enc_credentials")

	credsJSON, err := common.Marshal(creds)
	require.NoError(t, err)
	assert.NotContains(t, string(credsJSON), "newapi-token-secret")
	assert.NotContains(t, string(credsJSON), "gateway-key-secret")
}

func TestRoutingChannelBindingCredentialsFailClosedWhenSecretUnstable(t *testing.T) {
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "runtime-random-secret"
	t.Setenv("CRYPTO_SECRET", "")
	t.Setenv("SESSION_SECRET", "")
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := RoutingChannelBinding{ChannelID: 1002}
	err := binding.SetCredentials(RoutingCredentials{Sub2APIToken: "jwt-secret"})
	require.ErrorIs(t, err, ErrCredentialSecretUnstable)
	assert.Nil(t, binding.EncCredentials)
}

func TestRoutingChannelBindingCredentialsDetectKeyMismatch(t *testing.T) {
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := RoutingChannelBinding{ChannelID: 1003}
	require.NoError(t, binding.SetCredentials(RoutingCredentials{Sub2APIPassword: "password-secret"}))

	common.CryptoSecret = "different-routing-secret"
	_, err := binding.GetCredentials()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialKeyMismatch))
}

func TestRoutingModelsAutoMigrateAndMetricUpsert(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingMigrationAndUpsertContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingModelsExternalDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "mysql", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.envKey)
			if dsn == "" {
				t.Skipf("%s is not set", test.envKey)
			}
			db := openRoutingExternalTestDB(t, test.dbType, dsn)
			runRoutingMigrationAndUpsertContract(t, db, test.dbType)
		})
	}
}

var routingMigrationModels = []interface{}{
	&RoutingChannelBinding{},
	&RoutingCostSnapshot{},
	&RoutingChannelMetric{},
	&RoutingBreakerState{},
	&RoutingChannelHealthState{},
	&RoutingAgentRecommendation{},
}

func runRoutingMigrationAndUpsertContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()

	withRoutingTestDB(t, db, dbType)
	require.NoError(t, DB.AutoMigrate(routingMigrationModels...))
	require.NoError(t, DB.AutoMigrate(routingMigrationModels...))

	for _, model := range routingMigrationModels {
		require.True(t, DB.Migrator().HasTable(model))
	}

	binding := RoutingChannelBinding{
		ChannelID:     1,
		UpstreamType:  RoutingUpstreamTypeNewAPI,
		BaseURL:       "https://newapi.example",
		UpstreamGroup: "vip",
	}
	require.NoError(t, DB.Create(&binding).Error)

	var savedBinding RoutingChannelBinding
	require.NoError(t, DB.Where("channel_id = ?", 1).First(&savedBinding).Error)
	assert.False(t, savedBinding.Enabled)
	assert.False(t, savedBinding.ServesClaudeCode)
	assert.NotZero(t, savedBinding.CreatedTime)
	assert.NotZero(t, savedBinding.UpdatedTime)

	initialTiersJSON := `{"type":"expr","expr":"input * 1"}`
	require.NoError(t, UpsertRoutingCostSnapshot(&RoutingCostSnapshot{
		ChannelID:       1,
		ModelName:       "gpt-test",
		QuotaType:       0,
		GroupRatio:      1,
		BaseRatio:       2,
		CompletionRatio: 3,
		ModelPrice:      4,
		BillingMode:     "tiered_expr",
		TiersJSON:       &initialTiersJSON,
		Confidence:      RoutingCostConfidenceUnknown,
		SnapshotTS:      100,
		PricingVersion:  "v1",
	}))
	replacementTiersJSON := `{"type":"expr","expr":"input * 2"}`
	extrasJSON := `{"source":"sync"}`
	require.NoError(t, UpsertRoutingCostSnapshot(&RoutingCostSnapshot{
		ChannelID:       1,
		ModelName:       "gpt-test",
		QuotaType:       0,
		GroupRatio:      10,
		BaseRatio:       20,
		CompletionRatio: 30,
		ModelPrice:      40,
		BillingMode:     "ratio",
		TiersJSON:       &replacementTiersJSON,
		ExtrasJSON:      &extrasJSON,
		Confidence:      RoutingCostConfidenceFull,
		SnapshotTS:      200,
		PricingVersion:  "v2",
	}))

	var costSnapshotCount int64
	require.NoError(t, DB.Model(&RoutingCostSnapshot{}).Where("channel_id = ? AND model_name = ?", 1, "gpt-test").Count(&costSnapshotCount).Error)
	assert.Equal(t, int64(1), costSnapshotCount)

	var savedCostSnapshot RoutingCostSnapshot
	require.NoError(t, DB.Where("channel_id = ? AND model_name = ?", 1, "gpt-test").First(&savedCostSnapshot).Error)
	assert.Equal(t, 10.0, savedCostSnapshot.GroupRatio)
	assert.Equal(t, 20.0, savedCostSnapshot.BaseRatio)
	assert.Equal(t, 30.0, savedCostSnapshot.CompletionRatio)
	assert.Equal(t, 40.0, savedCostSnapshot.ModelPrice)
	assert.Equal(t, "ratio", savedCostSnapshot.BillingMode)
	require.NotNil(t, savedCostSnapshot.TiersJSON)
	require.NotNil(t, savedCostSnapshot.ExtrasJSON)
	assert.Equal(t, replacementTiersJSON, *savedCostSnapshot.TiersJSON)
	assert.Equal(t, extrasJSON, *savedCostSnapshot.ExtrasJSON)
	assert.Equal(t, RoutingCostConfidenceFull, savedCostSnapshot.Confidence)
	assert.Equal(t, int64(200), savedCostSnapshot.SnapshotTS)
	assert.Equal(t, "v2", savedCostSnapshot.PricingVersion)

	metric := &RoutingChannelMetric{
		ChannelID:       1,
		APIKeyIndex:     RoutingMetricSingleKeyIndex,
		ModelName:       "gpt-test",
		Group:           "default",
		BucketTs:        60,
		RequestCount:    1,
		SuccessCount:    1,
		TotalLatencyMs:  100,
		TtftSumMs:       40,
		TtftCount:       1,
		OutputTokens:    20,
		GenerationMs:    90,
		Err5xx:          1,
		RetryAfterMaxMs: 250,
	}
	require.NoError(t, UpsertRoutingChannelMetric(metric))

	metric.RequestCount = 2
	metric.SuccessCount = 1
	metric.TotalLatencyMs = 300
	metric.TtftSumMs = 80
	metric.TtftCount = 2
	metric.OutputTokens = 30
	metric.GenerationMs = 270
	metric.Err5xx = 0
	metric.Err429 = 2
	metric.RetryAfterMaxMs = 150
	require.NoError(t, UpsertRoutingChannelMetric(metric))

	var saved RoutingChannelMetric
	require.NoError(t, DB.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ? AND bucket_ts = ?",
		1, RoutingMetricSingleKeyIndex, "gpt-test", "default", 60).First(&saved).Error)
	assert.Equal(t, int64(3), saved.RequestCount)
	assert.Equal(t, int64(2), saved.SuccessCount)
	assert.Equal(t, int64(400), saved.TotalLatencyMs)
	assert.Equal(t, int64(120), saved.TtftSumMs)
	assert.Equal(t, int64(3), saved.TtftCount)
	assert.Equal(t, int64(50), saved.OutputTokens)
	assert.Equal(t, int64(360), saved.GenerationMs)
	assert.Equal(t, int64(1), saved.Err5xx)
	assert.Equal(t, int64(2), saved.Err429)
	assert.Equal(t, int64(250), saved.RetryAfterMaxMs)

	require.NoError(t, DB.Delete(&saved).Error)
	retentionMetrics := []RoutingChannelMetric{
		{
			ChannelID:    2,
			APIKeyIndex:  RoutingMetricSingleKeyIndex,
			ModelName:    "retention-test",
			Group:        "retention",
			BucketTs:     100,
			RequestCount: 1,
		},
		{
			ChannelID:    2,
			APIKeyIndex:  RoutingMetricSingleKeyIndex,
			ModelName:    "retention-test",
			Group:        "retention",
			BucketTs:     200,
			RequestCount: 1,
		},
	}
	require.NoError(t, DB.Create(&retentionMetrics).Error)

	deleted, err := DeleteRoutingMetricsBefore(150)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	var retainedMetrics []RoutingChannelMetric
	require.NoError(t, DB.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
		2, RoutingMetricSingleKeyIndex, "retention-test", "retention").Order("bucket_ts asc").Find(&retainedMetrics).Error)
	require.Len(t, retainedMetrics, 1)
	assert.Equal(t, int64(200), retainedMetrics[0].BucketTs)

	require.NoError(t, UpsertRoutingBreakerState(&RoutingBreakerState{
		ChannelID:           1,
		APIKeyIndex:         2,
		ModelName:           "gpt-test",
		Group:               "default",
		State:               RoutingBreakerStateOpen,
		Reason:              "5xx",
		ConsecutiveFailures: 3,
		Consecutive5xx:      3,
		EjectionCount:       1,
		OpenedAt:            100,
		CooldownUntil:       500,
		HalfOpenInflight:    2,
		WindowRequests:      50,
		WindowFailures:      25,
		UpdatedTime:         1000,
	}))
	require.NoError(t, UpsertRoutingBreakerState(&RoutingBreakerState{
		ChannelID:           1,
		APIKeyIndex:         2,
		ModelName:           "gpt-test",
		Group:               "default",
		State:               RoutingBreakerStateHealthy,
		Reason:              "recovered",
		ConsecutiveFailures: 0,
		Consecutive5xx:      0,
		EjectionCount:       2,
		OpenedAt:            0,
		CooldownUntil:       0,
		HalfOpenInflight:    0,
		WindowRequests:      51,
		WindowFailures:      2,
		UpdatedTime:         2000,
	}))
	require.NoError(t, UpsertRoutingBreakerState(&RoutingBreakerState{
		ChannelID:           1,
		APIKeyIndex:         2,
		ModelName:           "gpt-test",
		Group:               "default",
		State:               RoutingBreakerStateOpen,
		Reason:              "stale",
		ConsecutiveFailures: 9,
		Consecutive5xx:      9,
		EjectionCount:       9,
		OpenedAt:            1500,
		CooldownUntil:       2500,
		HalfOpenInflight:    3,
		WindowRequests:      99,
		WindowFailures:      99,
		UpdatedTime:         1500,
	}))

	var breakerCount int64
	require.NoError(t, DB.Model(&RoutingBreakerState{}).Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
		1, 2, "gpt-test", "default").Count(&breakerCount).Error)
	assert.Equal(t, int64(1), breakerCount)

	var savedBreaker RoutingBreakerState
	require.NoError(t, DB.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
		1, 2, "gpt-test", "default").First(&savedBreaker).Error)
	assert.Equal(t, RoutingBreakerStateHealthy, savedBreaker.State)
	assert.Equal(t, "recovered", savedBreaker.Reason)
	assert.Equal(t, int64(0), savedBreaker.ConsecutiveFailures)
	assert.Equal(t, int64(0), savedBreaker.Consecutive5xx)
	assert.Equal(t, int64(2), savedBreaker.EjectionCount)
	assert.Equal(t, int64(0), savedBreaker.OpenedAt)
	assert.Equal(t, int64(0), savedBreaker.CooldownUntil)
	assert.Equal(t, int64(0), savedBreaker.HalfOpenInflight)
	assert.Equal(t, int64(51), savedBreaker.WindowRequests)
	assert.Equal(t, int64(2), savedBreaker.WindowFailures)
	assert.Equal(t, int64(2000), savedBreaker.UpdatedTime)

	require.NoError(t, UpsertRoutingChannelAuthFailure(1, true, "unauthorized", 3000))
	require.NoError(t, UpsertRoutingChannelBalance(1, 0.75, 3100))
	var health RoutingChannelHealthState
	require.NoError(t, DB.Where("channel_id = ?", 1).First(&health).Error)
	assert.True(t, health.AuthFailure)
	assert.Equal(t, "unauthorized", health.AuthFailureReason)
	assert.Equal(t, int64(3000), health.AuthFailureUntil)
	assert.True(t, health.BalanceKnown)
	assert.Equal(t, 0.75, health.Balance)
	assert.Equal(t, int64(3100), health.BalanceUpdatedTime)

	require.NoError(t, ClearRoutingChannelAuthFailure(1, 3200))
	require.NoError(t, DB.Where("channel_id = ?", 1).First(&health).Error)
	assert.False(t, health.AuthFailure)
	assert.True(t, health.BalanceKnown)
	assert.Equal(t, 0.75, health.Balance)
}

func openRoutingSQLiteTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	return db
}

func openRoutingExternalTestDB(t *testing.T, dbType common.DatabaseType, dsn string) *gorm.DB {
	t.Helper()

	var (
		db  *gorm.DB
		err error
	)
	switch dbType {
	case common.DatabaseTypeMySQL:
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
	case common.DatabaseTypePostgreSQL:
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	default:
		t.Fatalf("unsupported external routing test database type %q", dbType)
	}
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	t.Cleanup(func() { _ = sqlDB.Close() })

	for _, model := range routingMigrationModels {
		if db.Migrator().HasTable(model) {
			t.Skipf("refusing to run against external database because %s already exists", model.(interface{ TableName() string }).TableName())
		}
	}

	t.Cleanup(func() {
		for index := len(routingMigrationModels) - 1; index >= 0; index-- {
			_ = db.Migrator().DropTable(routingMigrationModels[index])
		}
	})

	return db
}

func withRoutingTestDB(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()

	previousDB := DB
	previousLOGDB := LOG_DB
	previousMainDBType := common.MainDatabaseType()
	previousLogDBType := common.LogDatabaseType()

	DB = db
	LOG_DB = db
	common.SetDatabaseTypes(dbType, dbType)
	initCol()

	t.Cleanup(func() {
		DB = previousDB
		LOG_DB = previousLOGDB
		common.SetDatabaseTypes(previousMainDBType, previousLogDBType)
		initCol()
	})
}
