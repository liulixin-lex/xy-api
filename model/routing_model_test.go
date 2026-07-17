package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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

			require.NoError(t, DB.AutoMigrate(&Channel{}, &RoutingBreakerResetFence{}, &RoutingChannelMetric{}, &RoutingBreakerState{}))
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
			models: []interface{}{&Channel{}, &RoutingBreakerResetFence{}, &RoutingBreakerState{}},
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

func TestRoutingModelContextCancellationStopsChannelEligibilityQuery(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	require.NoError(t, DB.AutoMigrate(&Channel{}))
	require.NoError(t, DB.Create(&Channel{Id: 1, Name: "single", Key: "single-key"}).Error)

	queryStarted := make(chan struct{}, 1)
	const callbackName = "test:block_routing_channel_eligibility_query"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "channels" {
			return
		}
		queryStarted <- struct{}{}
		<-tx.Statement.Context.Done()
		tx.AddError(tx.Statement.Context.Err())
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := ResolveLegacyRoutingStateEligibilityContext(ctx, 1, RoutingMetricSingleKeyIndex)
		result <- err
	}()
	<-queryStarted
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
}

func TestRoutingModelContextCanceledOperationsDoNotMutateState(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	require.NoError(t, DB.AutoMigrate(&Channel{}, &RoutingBreakerResetFence{}, &RoutingChannelMetric{}, &RoutingBreakerState{}))
	require.NoError(t, DB.Create(&Channel{Id: 1, Name: "single", Key: "single-key"}).Error)
	require.NoError(t, DB.Create(&RoutingChannelMetric{
		ChannelID: 1, APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "retained",
		Group: "default", BucketTs: 100, RequestCount: 1,
	}).Error)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ResolveLegacyRoutingStateEligibilityContext(ctx, 1, RoutingMetricSingleKeyIndex)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, UpsertRoutingChannelMetricContext(ctx, &RoutingChannelMetric{
		ChannelID: 1, APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "canceled",
		Group: "default", BucketTs: 100, RequestCount: 1,
	}), context.Canceled)
	require.ErrorIs(t, UpsertRoutingBreakerStateContext(ctx, &RoutingBreakerState{
		ChannelID: 1, APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "canceled",
		Group: "default", State: RoutingBreakerStateOpen, UpdatedTime: 100,
	}), context.Canceled)
	_, err = DeleteRoutingMetricsBeforeContext(ctx, 101)
	require.ErrorIs(t, err, context.Canceled)
	_, err = GetRoutingBreakerStatesForHydrationPageContext(ctx, 5000, 0, 0, 0)
	require.ErrorIs(t, err, context.Canceled)

	var metrics []RoutingChannelMetric
	require.NoError(t, DB.Order("model_name asc").Find(&metrics).Error)
	require.Len(t, metrics, 1)
	assert.Equal(t, "retained", metrics[0].ModelName)
	var breakerCount int64
	require.NoError(t, DB.Model(&RoutingBreakerState{}).Count(&breakerCount).Error)
	assert.Zero(t, breakerCount)
}

func TestFencedRoutingBreakerOlderStateUsesConflictSafeUpsert(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	require.NoError(t, DB.AutoMigrate(&Channel{}, &RoutingBreakerResetFence{}, &RoutingBreakerState{}))

	const channelID = 1203
	require.NoError(t, DB.Create(&Channel{Id: channelID, Name: "single", Key: "single-key", CreatedTime: 100}).Error)
	eligibility, err := ResolveLegacyRoutingStateEligibility(channelID, RoutingMetricSingleKeyIndex)
	require.NoError(t, err)

	newer := &RoutingBreakerState{
		ChannelID: channelID, APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "gpt-test",
		Group: "default", State: RoutingBreakerStateHealthy, Reason: "newer", UpdatedTime: 300,
	}
	fence, stateAccepted, err := eligibility.UpsertRoutingBreakerStateForChannelContext(
		context.Background(), newer, RoutingChannelStateFence{},
	)
	require.NoError(t, err)
	require.True(t, stateAccepted)
	require.True(t, fence.Valid())

	var unsafePlainCreates atomic.Int32
	const callbackName = "test:reject_plain_fenced_breaker_create"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (RoutingBreakerState{}).TableName() {
			return
		}
		if _, conflictSafe := tx.Statement.Clauses["ON CONFLICT"]; !conflictSafe {
			unsafePlainCreates.Add(1)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	older := &RoutingBreakerState{
		ChannelID: channelID, APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "gpt-test",
		Group: "default", State: RoutingBreakerStateOpen, Reason: "older", UpdatedTime: 200,
	}
	_, stateAccepted, err = eligibility.UpsertRoutingBreakerStateForChannelContext(context.Background(), older, fence)
	require.NoError(t, err)
	require.True(t, stateAccepted)
	assert.Zero(t, unsafePlainCreates.Load(), "a unique-key error would abort the surrounding PostgreSQL transaction")

	var persisted RoutingBreakerState
	require.NoError(t, DB.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
		channelID, RoutingMetricSingleKeyIndex, "gpt-test", "default").First(&persisted).Error)
	assert.Equal(t, RoutingBreakerStateHealthy, persisted.State)
	assert.Equal(t, "newer", persisted.Reason)
	assert.Equal(t, int64(300), persisted.UpdatedTime)
}

func TestFencedRoutingStateRejectsTimestampsAtOrBeforeChannelCreation(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	require.NoError(t, DB.AutoMigrate(&Channel{}, &RoutingBreakerResetFence{}, &RoutingChannelMetric{}, &RoutingBreakerState{}))

	const (
		channelID   = 1204
		createdTime = int64(630)
	)
	require.NoError(t, DB.Create(&Channel{Id: channelID, Name: "single", Key: "single-key", CreatedTime: createdTime}).Error)
	eligibility, err := ResolveLegacyRoutingStateEligibility(channelID, RoutingMetricSingleKeyIndex)
	require.NoError(t, err)

	fence, stateAccepted, err := eligibility.UpsertRoutingChannelMetricForChannelContext(context.Background(), &RoutingChannelMetric{
		ChannelID: channelID, APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "crossing-bucket",
		Group: "default", BucketTs: 600, RequestCount: 1,
	}, RoutingChannelStateFence{})
	require.NoError(t, err)
	require.True(t, fence.Valid())
	assert.False(t, stateAccepted)

	_, stateAccepted, err = eligibility.UpsertRoutingBreakerStateForChannelContext(context.Background(), &RoutingBreakerState{
		ChannelID: channelID, APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "same-second-breaker",
		Group: "default", State: RoutingBreakerStateOpen, UpdatedTime: createdTime,
	}, fence)
	require.NoError(t, err)
	assert.False(t, stateAccepted)

	for _, table := range []any{&RoutingChannelMetric{}, &RoutingBreakerState{}} {
		var count int64
		require.NoError(t, DB.Model(table).Where("channel_id = ?", channelID).Count(&count).Error)
		assert.Zero(t, count)
	}
}

func TestLegacyRoutingStateEligibilityRejectsMismatchedRecords(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	require.NoError(t, DB.AutoMigrate(&Channel{}, &RoutingBreakerResetFence{}, &RoutingChannelMetric{}, &RoutingBreakerState{}))
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
	&RoutingSchemaVersion{},
	&RoutingTopologyMetadata{},
	&RoutingPool{},
	&RoutingPoolMember{},
	&RoutingCredentialRef{},
	&RoutingDecisionAudit{},
	&RoutingPolicyHead{},
	&RoutingPolicyRevision{},
	&RoutingPolicyPoolRevision{},
	&RoutingPolicyMemberRevision{},
	&RoutingPolicyActivation{},
	&RoutingPolicyDraft{},
	&RoutingPolicyApproval{},
	&RoutingPolicyRollbackApproval{},
	&RoutingConfigOutbox{},
	&RoutingRuntimeCheckpoint{},
	&RoutingControlLease{},
	&RoutingProbeResult{},
	&RoutingEndpointEvidence{},
	&RoutingEndpointSharedState{},
	&RoutingCanaryEvaluation{},
	&RoutingOperation{},
	&RoutingBreakerResetCommand{},
	&RoutingBreakerResetFence{},
	&RoutingBreakerResetTombstone{},
	&RoutingBreakerResetOutbox{},
	&RoutingAuditExport{},
	&RoutingAuditExportChunk{},
	&RoutingHedgeAttemptAudit{},
	&SystemTask{},
	&SystemTaskLock{},
	&RoutingUpstreamAccount{},
	&RoutingCostSnapshotVersion{},
	&RoutingChannelBinding{},
	&RoutingCostSnapshot{},
	&RoutingChannelMetric{},
	&RoutingMetricRollup{},
	&RoutingTelemetryReceipt{},
	&RoutingBreakerState{},
	&RoutingChannelHealthState{},
	&RoutingCredentialHealthState{},
}

var routingExternalCleanupModels = append([]interface{}{
	&RoutingErrorBudgetCursor{},
	&RoutingErrorBudgetHistory{},
	&RoutingErrorBudgetState{},
	&routingPolicyApprovalUserForTest{},
	&CasbinRule{},
}, routingMigrationModels...)

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

type routingChannelBindingBeforeSyncFailureCount struct {
	ID               int    `gorm:"primaryKey"`
	ChannelID        int    `gorm:"uniqueIndex;not null"`
	UpstreamType     string `gorm:"type:varchar(32);not null"`
	BaseURL          string `gorm:"type:varchar(512);not null"`
	UpstreamGroup    string `gorm:"type:varchar(128);not null"`
	ServesClaudeCode bool
	EncCredentials   *string `gorm:"type:text"`
	KeyVersion       int
	NewAPIUserID     *int
	Enabled          bool
	SyncBackoffUntil int64   `gorm:"bigint"`
	LastSyncError    *string `gorm:"type:text"`
	CreatedTime      int64   `gorm:"bigint"`
	UpdatedTime      int64   `gorm:"bigint"`
}

func (routingChannelBindingBeforeSyncFailureCount) TableName() string {
	return "routing_channel_bindings"
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

type routingCredentialRefBeforeChannelGeneration struct {
	ID                 int    `gorm:"primaryKey"`
	ChannelID          int    `gorm:"uniqueIndex:idx_routing_credential_ref,priority:1;index;not null"`
	Fingerprint        string `gorm:"type:varchar(64);uniqueIndex:idx_routing_credential_ref,priority:2;not null"`
	FingerprintVersion int
	Active             bool `gorm:"index"`
	LastSeenIndex      int
	CurrentOccurrences int
	CreatedTime        int64 `gorm:"bigint"`
	UpdatedTime        int64 `gorm:"bigint;index"`
	RetiredTime        int64 `gorm:"bigint;index"`
}

func (routingCredentialRefBeforeChannelGeneration) TableName() string {
	return "routing_credential_refs"
}

func (routingBreakerStateBeforeSemanticVersion) TableName() string {
	return "routing_breaker_states"
}

type channelBeforeRoutingGeneration struct {
	Id          int    `gorm:"primaryKey"`
	Key         string `gorm:"not null"`
	Name        string
	CreatedTime int64 `gorm:"bigint"`
}

func (channelBeforeRoutingGeneration) TableName() string {
	return "channels"
}

func runRoutingMigrationAndUpsertContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()

	withRoutingTestDB(t, db, dbType)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	require.NoError(t, DB.AutoMigrate(
		&routingChannelBindingBeforeSyncFailureCount{},
		&routingChannelMetricBeforeReliability{},
		&routingBreakerStateBeforeSemanticVersion{},
		&routingCredentialRefBeforeChannelGeneration{},
	))
	legacyCredential := routingCredentialRefBeforeChannelGeneration{
		ID: 9902, ChannelID: 9902, Fingerprint: strings.Repeat("a", 64),
		FingerprintVersion: 1, Active: true, CreatedTime: 100, UpdatedTime: 100,
	}
	require.NoError(t, DB.Create(&legacyCredential).Error)
	legacyBinding := routingChannelBindingBeforeSyncFailureCount{
		ChannelID:        77,
		UpstreamType:     RoutingUpstreamTypeNewAPI,
		BaseURL:          "https://legacy.example",
		UpstreamGroup:    "legacy",
		Enabled:          true,
		SyncBackoffUntil: 1234,
		CreatedTime:      100,
		UpdatedTime:      200,
	}
	require.NoError(t, DB.Create(&legacyBinding).Error)
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
	require.NoError(t, prepareRoutingRuntimeGenerationSchema(DB))
	require.NoError(t, DB.AutoMigrate(routingMigrationModels...))
	require.NoError(t, DB.AutoMigrate(routingMigrationModels...))
	require.True(t, DB.Migrator().HasColumn(&RoutingCredentialRef{}, "channel_generation"))
	var migratedLegacyCredential RoutingCredentialRef
	require.NoError(t, DB.First(&migratedLegacyCredential, "id = ?", legacyCredential.ID).Error)
	assert.Empty(t, migratedLegacyCredential.ChannelGeneration)
	require.NoError(t, DB.Delete(&RoutingCredentialRef{}, legacyCredential.ID).Error)
	t.Cleanup(func() { _ = db.Migrator().DropTable(&Channel{}) })
	require.NoError(t, DB.AutoMigrate(&channelBeforeRoutingGeneration{}))
	require.NoError(t, DB.Create(&channelBeforeRoutingGeneration{
		Id: 9901, Key: "legacy-key", Name: "legacy-channel", CreatedTime: 100,
	}).Error)
	require.NoError(t, DB.AutoMigrate(&Channel{}))
	require.NoError(t, EnsureChannelRoutingGenerations(DB))
	var migratedChannel Channel
	require.NoError(t, DB.First(&migratedChannel, "id = ?", 9901).Error)
	require.NotEmpty(t, migratedChannel.RoutingGeneration)
	migratedGeneration := migratedChannel.RoutingGeneration
	require.NoError(t, EnsureChannelRoutingGenerations(DB))
	require.NoError(t, DB.First(&migratedChannel, "id = ?", 9901).Error)
	assert.Equal(t, migratedGeneration, migratedChannel.RoutingGeneration)
	require.NoError(t, DB.Delete(&Channel{}, 9901).Error)
	require.NoError(t, DB.Create(&[]Channel{
		{Id: 1, Name: "single-one", Key: "single-key-one", Group: "default"},
		{Id: 91, Name: "single-ninety-one", Key: "single-key-ninety-one", Group: "legacy"},
		{
			Id: 92, Name: "multi-ninety-two", Key: "key-a\nkey-b", Group: "default,vip",
			ChannelInfo: ChannelInfo{IsMultiKey: true},
		},
	}).Error)
	var createdChannels []Channel
	require.NoError(t, DB.Where("id IN ?", []int{1, 91, 92}).Order("id asc").Find(&createdChannels).Error)
	require.Len(t, createdChannels, 3)
	seenGenerations := make(map[string]struct{}, len(createdChannels))
	for i := range createdChannels {
		require.NotEmpty(t, createdChannels[i].RoutingGeneration)
		_, duplicate := seenGenerations[createdChannels[i].RoutingGeneration]
		assert.False(t, duplicate)
		seenGenerations[createdChannels[i].RoutingGeneration] = struct{}{}
	}
	runRoutingTopologyReconcileContract(t)

	for _, model := range routingMigrationModels {
		require.True(t, DB.Migrator().HasTable(model))
	}
	require.True(t, DB.Migrator().HasColumn(&RoutingChannelMetric{}, "ReliabilityRequestCount"))
	require.True(t, DB.Migrator().HasColumn(&RoutingChannelMetric{}, "ReliabilityFailureCount"))
	require.True(t, DB.Migrator().HasColumn(&RoutingChannelMetric{}, "Err529"))
	require.True(t, DB.Migrator().HasColumn(&RoutingBreakerState{}, "SemanticVersion"))
	require.True(t, DB.Migrator().HasColumn(&RoutingChannelBinding{}, "SyncFailureCount"))
	require.True(t, DB.Migrator().HasColumn(&RoutingDecisionAudit{}, "GroupKey"))
	require.True(t, DB.Migrator().HasColumn(&RoutingDecisionAudit{}, "ModelKey"))
	require.True(t, DB.Migrator().HasColumn(&RoutingDecisionAudit{}, "RequestKey"))
	require.True(t, DB.Migrator().HasColumn(&RoutingDecisionAudit{}, "ActivationID"))
	require.True(t, DB.Migrator().HasColumn(&RoutingDecisionAudit{}, "RolloutKey"))
	require.True(t, DB.Migrator().HasColumn(&RoutingDecisionAudit{}, "Cohort"))
	require.True(t, DB.Migrator().HasColumn(&RoutingDecisionAudit{}, "SelectedMemberID"))
	require.True(t, DB.Migrator().HasColumn(&RoutingDecisionAudit{}, "ReservationMode"))
	require.True(t, DB.Migrator().HasColumn(&RoutingDecisionAudit{}, "ReservationResourceModel"))
	require.True(t, DB.Migrator().HasColumn(&RoutingDecisionAudit{}, "ExclusionSummaryJSON"))
	require.True(t, DB.Migrator().HasColumn(&RoutingCanaryEvaluation{}, "EvaluationHash"))
	require.True(t, DB.Migrator().HasColumn(&RoutingCanaryEvaluation{}, "CreateToken"))
	require.True(t, DB.Migrator().HasColumn(&RoutingCanaryEvaluation{}, "ControlExpectedCostTotal"))
	require.True(t, DB.Migrator().HasColumn(&RoutingCanaryEvaluation{}, "CanaryP95TTFTMilliseconds"))
	require.True(t, DB.Migrator().HasColumn(&RoutingCanaryEvaluation{}, "RetryAmplificationRatioBasisPoints"))
	require.True(t, DB.Migrator().HasIndex(&RoutingCanaryEvaluation{}, routingCanaryEvaluationWindowUniqueIndex))
	require.True(t, DB.Migrator().HasColumn(&RoutingPolicyDraft{}, "DocumentHash"))
	require.True(t, DB.Migrator().HasColumn(&RoutingPolicyDraft{}, "ValidatedHeadRevision"))
	require.True(t, DB.Migrator().HasColumn(&RoutingPolicyDraft{}, "PublishedRevision"))
	require.True(t, DB.Migrator().HasColumn(&RoutingOperation{}, "IdempotencyHash"))
	require.True(t, DB.Migrator().HasColumn(&RoutingOperation{}, "CreateToken"))
	require.True(t, DB.Migrator().HasColumn(&RoutingOperation{}, "ClaimToken"))
	require.True(t, DB.Migrator().HasColumn(&RoutingOperation{}, "ResultOutboxID"))
	require.True(t, DB.Migrator().HasColumn(&RoutingCostSnapshot{}, "ModelKey"))
	require.True(t, DB.Migrator().HasIndex(&RoutingCostSnapshot{}, "idx_routing_cost_channel_model_key"))
	require.NoError(t, CreateRoutingDecisionAuditsContext(context.Background(), []RoutingDecisionAudit{
		{DecisionID: "case-upper", RequestID: "Request-X", PoolID: 1, GroupName: "VIP", ModelName: "Model-X", SnapshotRevision: 1, CreatedTime: 1},
		{DecisionID: "case-lower", RequestID: "request-x", PoolID: 2, GroupName: "vip", ModelName: "model-x", SnapshotRevision: 1, CreatedTime: 1},
	}))
	var exactDecision RoutingDecisionAudit
	require.NoError(t, DB.Where("group_key = ? AND model_key = ? AND request_key = ?",
		RoutingDecisionGroupKey("VIP"), RoutingDecisionModelKey("Model-X"), RoutingDecisionRequestKey("Request-X"),
	).First(&exactDecision).Error)
	assert.Equal(t, "case-upper", exactDecision.DecisionID)

	var migratedLegacyBinding RoutingChannelBinding
	require.NoError(t, DB.Where("channel_id = ?", 77).First(&migratedLegacyBinding).Error)
	assert.Equal(t, "https://legacy.example", migratedLegacyBinding.BaseURL)
	assert.Equal(t, int64(1234), migratedLegacyBinding.SyncBackoffUntil)
	assert.Zero(t, migratedLegacyBinding.SyncFailureCount)

	var legacyMetricCount int64
	require.NoError(t, DB.Model(&RoutingChannelMetric{}).Where(
		"channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ? AND bucket_ts = ?",
		91, RoutingMetricSingleKeyIndex, "legacy-gpt-test", "legacy", 6000,
	).Count(&legacyMetricCount).Error)
	assert.Zero(t, legacyMetricCount, "unscoped legacy runtime metrics must cold-start")

	var legacyBreakerCount int64
	require.NoError(t, DB.Model(&RoutingBreakerState{}).Where(
		"channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
		1, RoutingMetricSingleKeyIndex, "gpt-test", "default",
	).Count(&legacyBreakerCount).Error)
	assert.Zero(t, legacyBreakerCount, "unscoped legacy breaker state must cold-start")

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
	var migratedLegacyMetric RoutingChannelMetric
	require.NoError(t, DB.Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ? AND bucket_ts = ?",
		91, RoutingMetricSingleKeyIndex, "legacy-gpt-test", "legacy", 6000).First(&migratedLegacyMetric).Error)
	assert.Equal(t, int64(1), migratedLegacyMetric.RequestCount)
	assert.Equal(t, int64(1), migratedLegacyMetric.SuccessCount)
	assert.Equal(t, int64(2), migratedLegacyMetric.ReliabilityRequestCount)
	assert.Equal(t, int64(1), migratedLegacyMetric.ReliabilityFailureCount)
	assert.Equal(t, int64(1), migratedLegacyMetric.Err529)

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
	var health RoutingChannelHealthState
	require.NoError(t, DB.Where("channel_id = ?", 1).First(&health).Error)
	assert.True(t, health.AuthFailure)
	assert.Equal(t, "unauthorized", health.AuthFailureReason)
	assert.Equal(t, int64(3000), health.AuthFailureUntil)

	require.NoError(t, ClearRoutingChannelAuthFailure(1, 3200))
	require.NoError(t, DB.Where("channel_id = ?", 1).First(&health).Error)
	assert.False(t, health.AuthFailure)

}

func runRoutingTopologyReconcileContract(t *testing.T) {
	t.Helper()

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-external-contract-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	first, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, first.ActivePools)
	assert.Equal(t, 4, first.ActiveMembers)
	assert.Equal(t, 4, first.ActiveCredentials)

	pools := loadRoutingPoolsForTest(t)
	defaultPool := pools["default"]
	vipPool := pools["vip"]
	require.NotZero(t, defaultPool.ID)
	require.NotZero(t, vipPool.ID)

	members := loadRoutingMembersForTest(t)
	defaultMember := members[routingMemberTestKey{poolID: defaultPool.ID, channelID: 92}]
	vipMember := members[routingMemberTestKey{poolID: vipPool.ID, channelID: 92}]
	require.True(t, defaultMember.Active)
	require.True(t, vipMember.Active)

	credentials := loadRoutingCredentialsForTest(t, 92)
	keyAID := credentials["key-a"].ID
	keyBID := credentials["key-b"].ID
	require.NotZero(t, keyAID)
	require.NotZero(t, keyBID)

	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 92).Updates(map[string]any{
		"group": "vip",
		"key":   "key-b\nkey-a",
	}).Error)
	second, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, second.ActivePools)
	assert.Equal(t, 3, second.ActiveMembers)
	assert.Equal(t, 4, second.ActiveCredentials)

	members = loadRoutingMembersForTest(t)
	assert.False(t, members[routingMemberTestKey{poolID: defaultPool.ID, channelID: 92}].Active)
	assert.Equal(t, vipMember.ID, members[routingMemberTestKey{poolID: vipPool.ID, channelID: 92}].ID)

	credentials = loadRoutingCredentialsForTest(t, 92)
	assert.Equal(t, keyAID, credentials["key-a"].ID)
	assert.Equal(t, keyBID, credentials["key-b"].ID)
	assert.Equal(t, 1, credentials["key-a"].LastSeenIndex)
	assert.Equal(t, 0, credentials["key-b"].LastSeenIndex)

	before := routingTopologyIdentitySnapshotForTest(t)
	_, err = ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, before, routingTopologyIdentitySnapshotForTest(t))

	require.NoError(t, DB.Create(&Channel{
		Id: 93, Name: "case-sensitive-groups", Key: "case-key", Group: "VIP,vip",
	}).Error)
	_, err = ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	pools = loadRoutingPoolsForTest(t)
	upperPool := pools["VIP"]
	lowerPool := pools["vip"]
	require.NotZero(t, upperPool.ID)
	require.NotZero(t, lowerPool.ID)
	assert.NotEqual(t, upperPool.ID, lowerPool.ID)
	members = loadRoutingMembersForTest(t)
	assert.True(t, members[routingMemberTestKey{poolID: upperPool.ID, channelID: 93}].Active)
	assert.True(t, members[routingMemberTestKey{poolID: lowerPool.ID, channelID: 93}].Active)
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
	for _, model := range routingExternalCleanupModels {
		if db.Migrator().HasTable(model) {
			t.Skipf("refusing to run against external database because %s already exists", model.(interface{ TableName() string }).TableName())
		}
	}

	t.Cleanup(func() {
		for index := len(routingExternalCleanupModels) - 1; index >= 0; index-- {
			_ = db.Migrator().DropTable(routingExternalCleanupModels[index])
		}
		_ = db.Migrator().DropTable(&Channel{})
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
