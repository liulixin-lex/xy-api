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

func TestRoutingPersistenceAcceptsOnlySingleKeyMinusOne(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "sqlite", dbType: common.DatabaseTypeSQLite},
		{name: "mysql", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var db *gorm.DB
			if test.dbType == common.DatabaseTypeSQLite {
				db = openRoutingSQLiteTestDB(t)
			} else {
				dsn := os.Getenv(test.envKey)
				if dsn == "" {
					t.Skipf("%s is not set", test.envKey)
				}
				db = openRoutingExternalTestDB(t, test.dbType, dsn)
			}
			if db.Migrator().HasTable(&Channel{}) {
				t.Skip("refusing to run against external database because channels already exists")
			}
			withRoutingTestDB(t, db, test.dbType)
			t.Cleanup(func() { _ = db.Migrator().DropTable(&Channel{}) })

			previousMemoryCache := common.MemoryCacheEnabled
			common.MemoryCacheEnabled = false
			t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })

			require.NoError(t, DB.AutoMigrate(&Channel{}, &RoutingChannelMetric{}, &RoutingBreakerState{}))
			require.NoError(t, DB.Create(&Channel{Id: 1001, Name: "single", Key: "single-key"}).Error)
			require.NoError(t, DB.Create(&Channel{Id: 1002, Name: "multi", Key: "key-0\nkey-1", ChannelInfo: ChannelInfo{IsMultiKey: true}}).Error)

			assert.True(t, SupportsLegacyRoutingState(1001, RoutingMetricSingleKeyIndex))
			assert.False(t, SupportsLegacyRoutingState(1001, 2))
			assert.False(t, SupportsLegacyRoutingState(1002, RoutingMetricSingleKeyIndex))
			assert.False(t, SupportsLegacyRoutingState(1002, 2))

			states := []struct {
				channelID   int
				apiKeyIndex int
				modelName   string
			}{
				{channelID: 1001, apiKeyIndex: RoutingMetricSingleKeyIndex, modelName: "single-minus-one"},
				{channelID: 1001, apiKeyIndex: 2, modelName: "single-positive"},
				{channelID: 1002, apiKeyIndex: RoutingMetricSingleKeyIndex, modelName: "multi-minus-one"},
				{channelID: 1002, apiKeyIndex: 2, modelName: "multi-positive"},
			}
			for _, state := range states {
				require.NoError(t, UpsertRoutingChannelMetric(&RoutingChannelMetric{
					ChannelID: state.channelID, APIKeyIndex: state.apiKeyIndex, ModelName: state.modelName,
					Group: "default", BucketTs: 60, RequestCount: 1,
				}))
				require.NoError(t, UpsertRoutingBreakerState(&RoutingBreakerState{
					ChannelID: state.channelID, APIKeyIndex: state.apiKeyIndex, ModelName: state.modelName,
					Group: "default", State: RoutingBreakerStateOpen, UpdatedTime: 100,
				}))
			}

			var metricCount int64
			require.NoError(t, DB.Model(&RoutingChannelMetric{}).Count(&metricCount).Error)
			assert.Equal(t, int64(1), metricCount)
			var savedMetric RoutingChannelMetric
			require.NoError(t, DB.First(&savedMetric).Error)
			assert.Equal(t, 1001, savedMetric.ChannelID)
			assert.Equal(t, RoutingMetricSingleKeyIndex, savedMetric.APIKeyIndex)
			var breakerCount int64
			require.NoError(t, DB.Model(&RoutingBreakerState{}).Count(&breakerCount).Error)
			assert.Equal(t, int64(1), breakerCount)
			var savedBreaker RoutingBreakerState
			require.NoError(t, DB.First(&savedBreaker).Error)
			assert.Equal(t, 1001, savedBreaker.ChannelID)
			assert.Equal(t, RoutingMetricSingleKeyIndex, savedBreaker.APIKeyIndex)
		})
	}
}

func TestResolveLegacyRoutingStateEligibilityFailsClosedWhenMemoryCacheMissesChannel(t *testing.T) {
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })

	channelID := int(^uint(0) >> 1)
	eligibility, err := ResolveLegacyRoutingStateEligibility(channelID, RoutingMetricSingleKeyIndex)
	require.NoError(t, err)
	assert.False(t, eligibility.Supported())
	assert.False(t, SupportsLegacyRoutingState(channelID, RoutingMetricSingleKeyIndex))
}

func TestResolveLegacyRoutingStateEligibilityTreatsRecordNotFoundAsUnsupported(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	require.NoError(t, DB.AutoMigrate(&Channel{}))

	eligibility, err := ResolveLegacyRoutingStateEligibility(404, RoutingMetricSingleKeyIndex)
	require.NoError(t, err)
	assert.False(t, eligibility.Supported())
	assert.False(t, SupportsLegacyRoutingState(404, RoutingMetricSingleKeyIndex))
}

func TestRoutingStateUpsertsPropagateEligibilityQueryErrors(t *testing.T) {
	tests := []struct {
		name   string
		models []interface{}
		upsert func() error
	}{
		{
			name:   "metric",
			models: []interface{}{&Channel{}, &RoutingChannelMetric{}},
			upsert: func() error {
				return UpsertRoutingChannelMetric(&RoutingChannelMetric{
					ChannelID: 1, APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "gpt-test",
					Group: "default", BucketTs: 60, RequestCount: 1,
				})
			},
		},
		{
			name:   "breaker",
			models: []interface{}{&Channel{}, &RoutingBreakerState{}},
			upsert: func() error {
				return UpsertRoutingBreakerState(&RoutingBreakerState{
					ChannelID: 1, APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "gpt-test",
					Group: "default", State: RoutingBreakerStateOpen, UpdatedTime: 100,
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openRoutingSQLiteTestDB(t)
			withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
			previousMemoryCache := common.MemoryCacheEnabled
			common.MemoryCacheEnabled = false
			t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })

			require.NoError(t, DB.AutoMigrate(test.models...))
			require.NoError(t, DB.Create(&Channel{Id: 1, Name: "single", Key: "single-key"}).Error)

			forcedErr := errors.New("forced channel eligibility query failure")
			callbackName := "test:fail_" + test.name + "_channel_eligibility_query"
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "channels" {
					tx.AddError(forcedErr)
				}
			}))
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

			require.ErrorIs(t, test.upsert(), forcedErr)
			require.NoError(t, db.Callback().Query().Remove(callbackName))
			var persistedCount int64
			require.NoError(t, DB.Model(test.models[1]).Count(&persistedCount).Error)
			assert.Zero(t, persistedCount)
		})
	}
}

func TestLegacyRoutingStateEligibilityRejectsMismatchedRecords(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	require.NoError(t, DB.AutoMigrate(&Channel{}, &RoutingChannelMetric{}, &RoutingBreakerState{}))
	require.NoError(t, DB.Create(&Channel{Id: 1, Name: "single", Key: "single-key"}).Error)

	eligibility, err := ResolveLegacyRoutingStateEligibility(1, RoutingMetricSingleKeyIndex)
	require.NoError(t, err)
	require.True(t, eligibility.Supported())

	require.ErrorIs(t, eligibility.UpsertRoutingChannelMetric(&RoutingChannelMetric{
		ChannelID: 2, APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "gpt-test",
		Group: "default", BucketTs: 60, RequestCount: 1,
	}), ErrLegacyRoutingStateEligibilityMismatch)
	require.ErrorIs(t, eligibility.UpsertRoutingBreakerState(&RoutingBreakerState{
		ChannelID: 2, APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "gpt-test",
		Group: "default", State: RoutingBreakerStateOpen, UpdatedTime: 100,
	}), ErrLegacyRoutingStateEligibilityMismatch)

	var metricCount int64
	require.NoError(t, DB.Model(&RoutingChannelMetric{}).Count(&metricCount).Error)
	assert.Zero(t, metricCount)
	var breakerCount int64
	require.NoError(t, DB.Model(&RoutingBreakerState{}).Count(&breakerCount).Error)
	assert.Zero(t, breakerCount)
}

var routingMigrationModels = []interface{}{
	&RoutingChannelBinding{},
	&RoutingCostSnapshot{},
	&RoutingChannelMetric{},
	&RoutingBreakerState{},
	&RoutingChannelHealthState{},
	&RoutingAgentRecommendation{},
}

type routingChannelMetricBeforeReliability struct {
	ID           int    `gorm:"primaryKey"`
	ChannelID    int    `gorm:"uniqueIndex:idx_routing_metric_key,priority:1"`
	APIKeyIndex  int    `gorm:"uniqueIndex:idx_routing_metric_key,priority:2"`
	ModelName    string `gorm:"type:varchar(128);uniqueIndex:idx_routing_metric_key,priority:3"`
	Group        string `gorm:"column:group;type:varchar(64);uniqueIndex:idx_routing_metric_key,priority:4"`
	BucketTs     int64  `gorm:"uniqueIndex:idx_routing_metric_key,priority:5"`
	RequestCount int64
	SuccessCount int64
}

func (routingChannelMetricBeforeReliability) TableName() string {
	return "routing_channel_metrics"
}

type routingBreakerStateBeforeSemanticVersion struct {
	ID                  int    `gorm:"primaryKey"`
	ChannelID           int    `gorm:"uniqueIndex:idx_routing_breaker_key,priority:1;index"`
	APIKeyIndex         int    `gorm:"uniqueIndex:idx_routing_breaker_key,priority:2"`
	ModelName           string `gorm:"type:varchar(128);uniqueIndex:idx_routing_breaker_key,priority:3"`
	Group               string `gorm:"column:group;type:varchar(64);uniqueIndex:idx_routing_breaker_key,priority:4"`
	State               string `gorm:"type:varchar(32);index"`
	Reason              string `gorm:"type:varchar(64);index"`
	ConsecutiveFailures int64
	Consecutive5xx      int64 `gorm:"column:consecutive_5xx"`
	EjectionCount       int64
	OpenedAt            int64 `gorm:"bigint"`
	CooldownUntil       int64 `gorm:"bigint;index"`
	HalfOpenInflight    int64
	WindowRequests      int64
	WindowFailures      int64
	LastProbeAt         int64 `gorm:"bigint"`
	UpdatedTime         int64 `gorm:"bigint;index"`
}

func (routingBreakerStateBeforeSemanticVersion) TableName() string {
	return "routing_breaker_states"
}

func runRoutingMigrationAndUpsertContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()

	withRoutingTestDB(t, db, dbType)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	require.NoError(t, DB.AutoMigrate(&routingChannelMetricBeforeReliability{}, &routingBreakerStateBeforeSemanticVersion{}))
	legacyMetric := routingChannelMetricBeforeReliability{
		ChannelID:    91,
		APIKeyIndex:  RoutingMetricSingleKeyIndex,
		ModelName:    "legacy-gpt-test",
		Group:        "legacy",
		BucketTs:     6000,
		RequestCount: 7,
		SuccessCount: 6,
	}
	require.NoError(t, DB.Create(&legacyMetric).Error)
	const legacyBreakerUpdatedTime int64 = 9_000_000_000
	legacyBreaker := routingBreakerStateBeforeSemanticVersion{
		ChannelID:           1,
		APIKeyIndex:         RoutingMetricSingleKeyIndex,
		ModelName:           "gpt-test",
		Group:               "default",
		State:               RoutingBreakerStateOpen,
		Reason:              "rate_limit",
		ConsecutiveFailures: 99,
		Consecutive5xx:      99,
		EjectionCount:       9,
		OpenedAt:            8000,
		CooldownUntil:       10_000,
		HalfOpenInflight:    4,
		WindowRequests:      999,
		WindowFailures:      998,
		LastProbeAt:         8500,
		UpdatedTime:         legacyBreakerUpdatedTime,
	}
	require.NoError(t, DB.Create(&legacyBreaker).Error)
	require.NoError(t, DB.AutoMigrate(routingMigrationModels...))
	require.NoError(t, DB.AutoMigrate(routingMigrationModels...))
	t.Cleanup(func() { _ = db.Migrator().DropTable(&Channel{}) })
	require.NoError(t, DB.AutoMigrate(&Channel{}))
	require.NoError(t, DB.Create(&[]Channel{
		{Id: 1, Name: "single-one", Key: "single-key-one"},
		{Id: 91, Name: "single-ninety-one", Key: "single-key-ninety-one"},
	}).Error)

	for _, model := range routingMigrationModels {
		require.True(t, DB.Migrator().HasTable(model))
	}
	require.True(t, DB.Migrator().HasColumn(&RoutingChannelMetric{}, "ReliabilityRequestCount"))
	require.True(t, DB.Migrator().HasColumn(&RoutingChannelMetric{}, "ReliabilityFailureCount"))
	require.True(t, DB.Migrator().HasColumn(&RoutingChannelMetric{}, "Err529"))
	require.True(t, DB.Migrator().HasColumn(&RoutingBreakerState{}, "SemanticVersion"))

	var migratedLegacyMetric RoutingChannelMetric
	require.NoError(t, DB.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ? AND bucket_ts = ?",
		91, RoutingMetricSingleKeyIndex, "legacy-gpt-test", "legacy", 6000).First(&migratedLegacyMetric).Error)
	assert.Equal(t, int64(7), migratedLegacyMetric.RequestCount)
	assert.Equal(t, int64(6), migratedLegacyMetric.SuccessCount)
	assert.Zero(t, migratedLegacyMetric.ReliabilityRequestCount)
	assert.Zero(t, migratedLegacyMetric.ReliabilityFailureCount)
	assert.Zero(t, migratedLegacyMetric.Err529)

	var migratedLegacyBreaker RoutingBreakerState
	require.NoError(t, DB.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
		1, RoutingMetricSingleKeyIndex, "gpt-test", "default").First(&migratedLegacyBreaker).Error)
	assert.Zero(t, migratedLegacyBreaker.SemanticVersion)
	assert.Equal(t, legacyBreakerUpdatedTime, migratedLegacyBreaker.UpdatedTime)
	assert.Equal(t, int64(999), migratedLegacyBreaker.WindowRequests)
	assert.Equal(t, int64(998), migratedLegacyBreaker.WindowFailures)

	hydrationStates, err := GetRoutingBreakerStatesForHydration(5000)
	require.NoError(t, err)
	assert.Empty(t, hydrationStates)

	require.NoError(t, UpsertRoutingChannelMetric(&RoutingChannelMetric{
		ChannelID:               91,
		APIKeyIndex:             RoutingMetricSingleKeyIndex,
		ModelName:               "legacy-gpt-test",
		Group:                   "legacy",
		BucketTs:                6000,
		RequestCount:            1,
		SuccessCount:            1,
		ReliabilityRequestCount: 2,
		ReliabilityFailureCount: 1,
		Err529:                  1,
	}))
	require.NoError(t, DB.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ? AND bucket_ts = ?",
		91, RoutingMetricSingleKeyIndex, "legacy-gpt-test", "legacy", 6000).First(&migratedLegacyMetric).Error)
	assert.Equal(t, int64(8), migratedLegacyMetric.RequestCount)
	assert.Equal(t, int64(7), migratedLegacyMetric.SuccessCount)
	assert.Equal(t, int64(2), migratedLegacyMetric.ReliabilityRequestCount)
	assert.Equal(t, int64(1), migratedLegacyMetric.ReliabilityFailureCount)
	assert.Equal(t, int64(1), migratedLegacyMetric.Err529)

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
		ChannelID:               1,
		APIKeyIndex:             RoutingMetricSingleKeyIndex,
		ModelName:               "gpt-test",
		Group:                   "default",
		BucketTs:                60,
		RequestCount:            1,
		SuccessCount:            1,
		ReliabilityRequestCount: 2,
		ReliabilityFailureCount: 1,
		TotalLatencyMs:          100,
		TtftSumMs:               40,
		TtftCount:               1,
		OutputTokens:            20,
		GenerationMs:            90,
		Err5xx:                  1,
		Err529:                  1,
		RetryAfterMaxMs:         250,
	}
	require.NoError(t, UpsertRoutingChannelMetric(metric))

	metric.RequestCount = 2
	metric.SuccessCount = 1
	metric.ReliabilityRequestCount = 3
	metric.ReliabilityFailureCount = 2
	metric.TotalLatencyMs = 300
	metric.TtftSumMs = 80
	metric.TtftCount = 2
	metric.OutputTokens = 30
	metric.GenerationMs = 270
	metric.Err5xx = 0
	metric.Err429 = 2
	metric.Err529 = 2
	metric.RetryAfterMaxMs = 150
	require.NoError(t, UpsertRoutingChannelMetric(metric))

	var saved RoutingChannelMetric
	require.NoError(t, DB.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ? AND bucket_ts = ?",
		1, RoutingMetricSingleKeyIndex, "gpt-test", "default", 60).First(&saved).Error)
	assert.Equal(t, int64(3), saved.RequestCount)
	assert.Equal(t, int64(2), saved.SuccessCount)
	assert.Equal(t, int64(5), saved.ReliabilityRequestCount)
	assert.Equal(t, int64(3), saved.ReliabilityFailureCount)
	assert.Equal(t, int64(400), saved.TotalLatencyMs)
	assert.Equal(t, int64(120), saved.TtftSumMs)
	assert.Equal(t, int64(3), saved.TtftCount)
	assert.Equal(t, int64(50), saved.OutputTokens)
	assert.Equal(t, int64(360), saved.GenerationMs)
	assert.Equal(t, int64(1), saved.Err5xx)
	assert.Equal(t, int64(2), saved.Err429)
	assert.Equal(t, int64(3), saved.Err529)
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
		APIKeyIndex:         RoutingMetricSingleKeyIndex,
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

	var currentBreaker RoutingBreakerState
	require.NoError(t, DB.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
		1, RoutingMetricSingleKeyIndex, "gpt-test", "default").First(&currentBreaker).Error)
	assert.Equal(t, RoutingBreakerSemanticVersion, currentBreaker.SemanticVersion)
	assert.Equal(t, RoutingBreakerStateOpen, currentBreaker.State)
	assert.Equal(t, "5xx", currentBreaker.Reason)
	assert.Equal(t, int64(3), currentBreaker.ConsecutiveFailures)
	assert.Equal(t, int64(3), currentBreaker.Consecutive5xx)
	assert.Equal(t, int64(1), currentBreaker.EjectionCount)
	assert.Equal(t, int64(100), currentBreaker.OpenedAt)
	assert.Equal(t, int64(500), currentBreaker.CooldownUntil)
	assert.Equal(t, int64(2), currentBreaker.HalfOpenInflight)
	assert.Equal(t, int64(50), currentBreaker.WindowRequests)
	assert.Equal(t, int64(25), currentBreaker.WindowFailures)
	assert.Zero(t, currentBreaker.LastProbeAt)
	assert.Equal(t, int64(1000), currentBreaker.UpdatedTime)
	assert.Equal(t, legacyBreaker.ID, currentBreaker.ID)

	var breakerCount int64
	require.NoError(t, DB.Model(&RoutingBreakerState{}).Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
		1, RoutingMetricSingleKeyIndex, "gpt-test", "default").Count(&breakerCount).Error)
	assert.Equal(t, int64(1), breakerCount)

	hydrationStates, err = GetRoutingBreakerStatesForHydration(5000)
	require.NoError(t, err)
	require.Len(t, hydrationStates, 1)
	assert.Equal(t, RoutingBreakerSemanticVersion, hydrationStates[0].SemanticVersion)
	assert.Equal(t, int64(1000), hydrationStates[0].UpdatedTime)
	nextHydrationPage, err := GetRoutingBreakerStatesForHydrationPage(5000, 0, hydrationStates[0].UpdatedTime, hydrationStates[0].ID)
	require.NoError(t, err)
	assert.Empty(t, nextHydrationPage)

	require.NoError(t, UpsertRoutingBreakerState(&RoutingBreakerState{
		ChannelID:           1,
		APIKeyIndex:         RoutingMetricSingleKeyIndex,
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
		APIKeyIndex:         RoutingMetricSingleKeyIndex,
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

	breakerCount = 0
	require.NoError(t, DB.Model(&RoutingBreakerState{}).Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
		1, RoutingMetricSingleKeyIndex, "gpt-test", "default").Count(&breakerCount).Error)
	assert.Equal(t, int64(1), breakerCount)

	var savedBreaker RoutingBreakerState
	require.NoError(t, DB.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
		1, RoutingMetricSingleKeyIndex, "gpt-test", "default").First(&savedBreaker).Error)
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
	assert.Equal(t, RoutingBreakerSemanticVersion, savedBreaker.SemanticVersion)
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

	if db.Migrator().HasTable(&Channel{}) {
		t.Skip("refusing to run against external database because channels already exists")
	}
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
