package channelrouting

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestApplyRoutingBreakerResetEventIsIdempotentAcrossNodes(t *testing.T) {
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 1, FailureRateThreshold: 1, FailureRateMinSamples: 1,
		WindowSize: 4, EntryTTL: time.Hour, MaxEntries: 16,
	})
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
	})
	target := model.RoutingBreakerResetTarget{
		Scope: model.RoutingBreakerResetScopeMember, PoolID: 1, MemberID: 2, ChannelID: 3,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-reset", GroupName: "default",
	}
	key := routingbreaker.Key{
		ChannelID: target.ChannelID, APIKeyIndex: target.APIKeyIndex,
		Model: target.ModelName, Group: target.GroupName,
	}
	require.Equal(t, routingbreaker.StateOpen, routingbreaker.RecordReliabilityFailure(key, routingbreaker.FailureProvider5xx).State)
	event := model.RoutingBreakerResetEvent{
		SchemaVersion: 1, OperationID: 10, OutboxID: 20, Generation: 1,
		ResetAtMs: time.Now().UnixMilli(), Target: target,
	}
	payload, err := common.Marshal(event)
	require.NoError(t, err)
	require.NoError(t, ApplyRoutingBreakerResetEventPayload(payload))
	assert.Equal(t, routingbreaker.StateHealthy, routingbreaker.RecordReliabilitySuccess(key).State)
	require.Equal(t, routingbreaker.StateOpen, routingbreaker.RecordReliabilityFailure(key, routingbreaker.FailureProvider5xx).State)

	require.NoError(t, ApplyRoutingBreakerResetEventPayload(payload))
	assert.Equal(t, routingbreaker.StateOpen, routingbreaker.RecordReliabilityFailure(key, routingbreaker.FailureProvider5xx).State)
}

func TestSyncRoutingBreakerResetStateRestoresDurableTombstoneWithoutRedis(t *testing.T) {
	db := breakerResetServiceTestDB(t)
	resetRoutingBreakerResetRuntimeForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 1, FailureRateThreshold: 1, FailureRateMinSamples: 1,
		WindowSize: 4, EntryTTL: time.Hour, MaxEntries: 16,
	})
	t.Cleanup(func() {
		resetRoutingBreakerResetRuntimeForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})
	target := model.RoutingBreakerResetTarget{
		Scope: model.RoutingBreakerResetScopeMember, PoolID: 1, MemberID: 2, ChannelID: 3,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-reset", GroupName: "default",
	}
	seedBreakerResetServicePolicy(t, db, target)
	operation, _, err := model.CreateRoutingBreakerResetOperationContext(context.Background(), model.RoutingOperationSpec{
		Type: model.RoutingOperationTypeBreakerReset, EvaluationHash: strings.Repeat("a", 64),
		SubjectType: model.RoutingOperationSubjectMemberBreaker, SubjectID: int64(target.MemberID), PoolID: target.PoolID,
		ExpectedRevision: 1, ExpectedActivationID: 1, Reason: "reset",
	}, target)
	require.NoError(t, err)
	nowMs, err := model.RoutingEndpointDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	claimed, err := model.ClaimRoutingOperationContext(
		context.Background(), model.RoutingOperationTypeBreakerReset, nowMs, 30_000,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	execution, err := model.ExecuteRoutingBreakerResetOperationContext(context.Background(), *claimed)
	require.NoError(t, err)
	assert.Equal(t, operation.ID, execution.Operation.ID)

	key := routingbreaker.Key{
		ChannelID: target.ChannelID, APIKeyIndex: target.APIKeyIndex,
		Model: target.ModelName, Group: target.GroupName,
	}
	require.Equal(t, routingbreaker.StateOpen, routingbreaker.RecordReliabilityFailure(key, routingbreaker.FailureProvider5xx).State)
	require.NoError(t, SyncRoutingBreakerResetStateContext(context.Background()))
	assert.Equal(t, int64(1), routingbreaker.DefaultResetGeneration(key))
	assert.Equal(t, routingbreaker.StateHealthy, routingbreaker.RecordReliabilitySuccess(key).State)

	var outboxCount int64
	require.NoError(t, db.Model(&model.RoutingBreakerResetOutbox{}).Count(&outboxCount).Error)
	assert.Equal(t, int64(1), outboxCount)
}

func TestEndpointBreakerResetClearsLocalAndSharedViews(t *testing.T) {
	routingbreaker.ResetDefaultForTest(routingbreaker.Config{
		Consecutive5xxThreshold: 1, FailureRateThreshold: 1, FailureRateMinSamples: 1,
		WindowSize: 4, EntryTTL: time.Hour, MaxEntries: 16,
	})
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
	})
	target := model.RoutingBreakerResetTarget{
		Scope: model.RoutingBreakerResetScopeEndpoint, EndpointHost: "api.reset.test",
		EndpointAuthority: "https://api.reset.test:443", Region: "test-region",
	}
	key := routingbreaker.NewEndpointKey(target.EndpointAuthority, target.Region)
	require.Equal(t, routingbreaker.StateOpen, routingbreaker.RecordReliabilityFailure(key, routingbreaker.FailureNetwork).State)
	routinghotcache.ReplaceSharedEndpointBreakers([]routinghotcache.SharedEndpointBreakerEntry{{
		Key: key.HotcacheKey(), Snapshot: routinghotcache.SharedEndpointBreakerSnapshot{
			State: model.RoutingBreakerStateOpen, UpdatedUnix: time.Now().Unix(), ExpiresUnix: time.Now().Add(time.Minute).Unix(),
		},
	}})

	applied := applyRoutingBreakerReset(model.RoutingBreakerResetEvent{Generation: 1, Target: target})
	require.True(t, applied)
	_, shared := routinghotcache.GetSharedEndpointBreaker(key.HotcacheKey())
	assert.False(t, shared)
	assert.Equal(t, routingbreaker.StateHealthy, routingbreaker.RecordReliabilitySuccess(key).State)
	routinghotcache.ReplaceSharedEndpointBreakers([]routinghotcache.SharedEndpointBreakerEntry{{
		Key: key.HotcacheKey(), Snapshot: routinghotcache.SharedEndpointBreakerSnapshot{
			State: model.RoutingBreakerStateHealthy, UpdatedUnix: time.Now().Unix(), ExpiresUnix: time.Now().Add(time.Minute).Unix(),
		},
	}})
	applied = applyRoutingBreakerReset(model.RoutingBreakerResetEvent{Generation: 1, Target: target})
	assert.False(t, applied)
	_, shared = routinghotcache.GetSharedEndpointBreaker(key.HotcacheKey())
	assert.True(t, shared, "a duplicate reset event must not clear state produced after that generation")
}

func TestResolveEndpointBreakerResetTargetRequiresSnapshotAuthorityAndLocalRegion(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	t.Setenv("ROUTING_REGION", "test-region")
	SetSnapshotForTest(SnapshotView{
		Revision: 7, ActivationID: 9,
		Channels: []ChannelSnapshot{{ID: 33, Endpoint: "https://API.Reset.Test/v1"}},
	})

	resolved, err := ResolveEndpointBreakerResetTarget("https://api.reset.test", "test-region")
	require.NoError(t, err)
	assert.Equal(t, "api.reset.test", resolved.Target.EndpointHost)
	assert.Equal(t, "https://api.reset.test:443", resolved.Target.EndpointAuthority)
	assert.Equal(t, "test-region", resolved.Target.Region)
	assert.Equal(t, int64(7), resolved.ExpectedRevision)
	assert.Equal(t, int64(9), resolved.ExpectedActivationID)

	_, err = ResolveEndpointBreakerResetTarget("https://unknown.reset.test", "test-region")
	assert.ErrorIs(t, err, ErrBreakerResetTargetNotFound)
	_, err = ResolveEndpointBreakerResetTarget("https://api.reset.test", "other-region")
	assert.ErrorIs(t, err, ErrBreakerResetTargetNotFound)
}

func TestBreakerResetControlCycleDoesNotPublishSupersededTarget(t *testing.T) {
	db := breakerResetServiceTestDB(t)
	resetRoutingBreakerResetRuntimeForTest()
	ResetRoutingEventsForTest()
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() {
		resetRoutingBreakerResetRuntimeForTest()
		ResetRoutingEventsForTest()
		common.RedisEnabled = previousRedisEnabled
	})
	target := model.RoutingBreakerResetTarget{
		Scope: model.RoutingBreakerResetScopeMember, PoolID: 1, MemberID: 2, ChannelID: 3,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-reset", GroupName: "default",
	}
	seedBreakerResetServicePolicy(t, db, target)
	_, _, err := model.CreateRoutingBreakerResetOperationContext(context.Background(), model.RoutingOperationSpec{
		Type: model.RoutingOperationTypeBreakerReset, EvaluationHash: strings.Repeat("a", 64),
		SubjectType: model.RoutingOperationSubjectMemberBreaker, SubjectID: int64(target.MemberID), PoolID: target.PoolID,
		ExpectedRevision: 1, ExpectedActivationID: 1, Reason: "reset",
	}, target)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.RoutingPolicyHead{}).Where("id = ?", 1).Updates(map[string]any{
		"current_revision": 2, "current_activation_id": 2,
	}).Error)

	require.NoError(t, RunBreakerResetControlCycleContext(context.Background()))
	var operation model.RoutingOperation
	require.NoError(t, db.First(&operation).Error)
	assert.Equal(t, model.RoutingOperationStatusSuperseded, operation.Status)
	assert.Zero(t, CurrentRoutingEventStats().LatestID)
	var outboxCount int64
	require.NoError(t, db.Model(&model.RoutingBreakerResetOutbox{}).Count(&outboxCount).Error)
	assert.Zero(t, outboxCount)
}

func breakerResetServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	withSnapshotTestDB(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingOperation{}, &model.RoutingBreakerResetCommand{}, &model.RoutingBreakerResetFence{},
		&model.RoutingBreakerResetTombstone{}, &model.RoutingBreakerResetOutbox{},
		&model.RoutingBreakerState{}, &model.RoutingEndpointEvidence{}, &model.RoutingEndpointSharedState{},
		&model.RoutingPolicyHead{}, &model.RoutingPolicyPoolRevision{}, &model.RoutingPolicyMemberRevision{},
	))
	return db
}

func seedBreakerResetServicePolicy(t *testing.T, db *gorm.DB, target model.RoutingBreakerResetTarget) {
	t.Helper()
	require.NoError(t, db.Create(&model.RoutingPolicyHead{
		ID: 1, CurrentRevision: 1, CurrentActivationID: 1, CurrentHash: strings.Repeat("f", 64),
		CurrentStage: model.RoutingDeploymentStageActive, CreatedTime: 1, UpdatedTime: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingPolicyPoolRevision{
		Revision: 1, PoolID: target.PoolID, GroupKey: strings.Repeat("e", 64), GroupName: target.GroupName,
		DisplayName: target.GroupName, DeploymentStage: model.RoutingDeploymentStageActive,
		PolicyProfile: "balanced", PolicyJSON: `{}`,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingPolicyMemberRevision{
		Revision: 1, PoolID: target.PoolID, MemberID: target.MemberID, ChannelID: target.ChannelID,
		Enabled: true, Weight: 1, CredentialIDsJSON: `[]`, OverridesJSON: `{}`,
	}).Error)
}
