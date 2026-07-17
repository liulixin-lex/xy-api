package model

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingChannelLifecycleFencesReusedSQLiteChannelID(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&Channel{}, &Ability{}, &RoutingChannelLifecycle{},
		&RoutingConfigurationEpoch{}, &RoutingChannelConfiguration{},
		&RoutingChannelConfigurationOutbox{}, &RoutingControlAudit{},
		&RoutingTopologyMetadata{}, &RoutingPool{}, &RoutingPoolMember{}, &RoutingCredentialRef{},
		&RoutingChannelMetric{}, &RoutingBreakerState{}, &RoutingChannelHealthState{},
		&RoutingMetricRollup{}, &RoutingTelemetryReceipt{}, &RoutingControlLease{}, &RoutingProbeResult{},
		&RoutingBreakerResetFence{},
	))
	require.NoError(t, EnsureRoutingConfigurationEpoch(db))

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "routing-lifecycle-regression-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	baseURL := "https://user:password@provider-a.example/v1?token=secret"
	original := Channel{
		Id: 1, Name: "original", Key: "serving-secret-a", Group: "default",
		Models: "gpt-test", BaseURL: &baseURL,
	}
	require.NoError(t, db.Create(&original).Error)
	require.NotEmpty(t, original.RoutingIdentity)
	require.NotEmpty(t, original.RoutingGeneration)

	created, err := GetRoutingChannelLifecycleByGenerationContext(context.Background(), original.RoutingGeneration)
	require.NoError(t, err)
	assert.Equal(t, RoutingChannelLifecycleStatusActive, created.Status)
	assert.Equal(t, RoutingChannelLifecycleReasonCreated, created.CreatedReason)
	assert.Equal(t, "https://provider-a.example", created.EndpointSnapshot)
	encodedLifecycle, err := common.Marshal(created)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedLifecycle), original.Key)
	assert.NotContains(t, string(encodedLifecycle), "password")
	assert.NotContains(t, string(encodedLifecycle), "token=secret")

	configuration, err := GetRoutingChannelConfigurationContext(context.Background(), original.Id)
	require.NoError(t, err)
	firstMutation, err := UpdateRoutingChannelConfigurationContext(
		context.Background(), configuration, 2, RoutingChannelTrafficClassAll, "", false, 7,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), firstMutation.Configuration.Revision)
	legacyEventID := firstMutation.Outbox.EventID

	_, err = ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	var originalMember RoutingPoolMember
	require.NoError(t, db.Where(
		"channel_id = ? AND channel_generation = ? AND active = ?",
		original.Id, original.RoutingGeneration, true,
	).First(&originalMember).Error)

	original.Key = "serving-secret-b"
	require.NoError(t, original.Update())
	assert.Equal(t, created.RoutingGeneration, original.RoutingGeneration)
	var lifecycleCount int64
	require.NoError(t, db.Model(&RoutingChannelLifecycle{}).
		Where("routing_identity = ?", original.RoutingIdentity).Count(&lifecycleCount).Error)
	assert.Equal(t, int64(1), lifecycleCount)

	previousGeneration := original.RoutingGeneration
	replacementURL := "https://provider-b.example/api"
	original.BaseURL = &replacementURL
	require.NoError(t, original.Update())
	assert.NotEqual(t, previousGeneration, original.RoutingGeneration)
	assert.Equal(t, created.RoutingIdentity, original.RoutingIdentity)

	var retired RoutingChannelLifecycle
	require.NoError(t, db.Where("routing_generation = ?", previousGeneration).First(&retired).Error)
	assert.Equal(t, RoutingChannelLifecycleStatusRetired, retired.Status)
	assert.Equal(t, RoutingChannelLifecycleReasonUpstreamChanged, retired.RetiredReason)
	var active RoutingChannelLifecycle
	require.NoError(t, db.Where("routing_generation = ?", original.RoutingGeneration).First(&active).Error)
	assert.Equal(t, RoutingChannelLifecycleStatusActive, active.Status)
	assert.Equal(t, "https://provider-b.example", active.EndpointSnapshot)

	rotatedConfiguration, err := GetRoutingChannelConfigurationContext(context.Background(), original.Id)
	require.NoError(t, err)
	assert.Equal(t, original.RoutingIdentity, rotatedConfiguration.RoutingIdentity)
	assert.Equal(t, original.RoutingGeneration, rotatedConfiguration.RoutingGeneration)
	assert.Equal(t, int64(3), rotatedConfiguration.Revision)

	_, err = ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	require.NoError(t, db.First(&originalMember, originalMember.ID).Error)
	assert.False(t, originalMember.Active)
	var replacementMember RoutingPoolMember
	require.NoError(t, db.Where(
		"channel_id = ? AND channel_generation = ? AND active = ?",
		original.Id, original.RoutingGeneration, true,
	).First(&replacementMember).Error)
	assert.NotEqual(t, originalMember.ID, replacementMember.ID)
	var replacementCredential RoutingCredentialRef
	require.NoError(t, db.Where(
		"channel_id = ? AND channel_generation = ? AND active = ?",
		original.Id, original.RoutingGeneration, true,
	).First(&replacementCredential).Error)
	rollup := RoutingMetricRollup{
		MemberID: replacementMember.ID, CredentialID: replacementCredential.ID,
		ChannelID: original.Id, ChannelGeneration: original.RoutingGeneration, PoolID: replacementMember.PoolID,
		ModelName: "gpt-test", BucketTs: time.Now().Unix(), LastSnapshotRevision: 1,
		RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
	}
	require.NoError(t, UpsertRoutingMetricRollupsContext(context.Background(), []RoutingMetricRollup{rollup}))
	eligibility, err := ResolveLegacyRoutingStateEligibility(original.Id, RoutingMetricSingleKeyIndex)
	require.NoError(t, err)
	lateBreaker := RoutingBreakerState{
		ChannelID: original.Id, ChannelGeneration: original.RoutingGeneration,
		APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "gpt-test", Group: "default",
		State: RoutingBreakerStateOpen, UpdatedTime: common.GetTimestamp() + 1,
	}
	_, accepted, err := eligibility.UpsertRoutingBreakerStateForChannelContext(
		context.Background(), &lateBreaker, RoutingChannelStateFence{},
	)
	require.NoError(t, err)
	require.True(t, accepted)

	retiredIdentity := original.RoutingIdentity
	retiredGeneration := original.RoutingGeneration
	require.NoError(t, original.Delete())
	require.NoError(t, db.Where("routing_generation = ?", retiredGeneration).First(&active).Error)
	assert.Equal(t, RoutingChannelLifecycleStatusRetired, active.Status)
	assert.Equal(t, RoutingChannelLifecycleReasonDeleted, active.RetiredReason)

	recreated := Channel{
		Id: 1, Name: "recreated", Key: "serving-secret-c", Group: "default", Models: "gpt-test",
	}
	require.NoError(t, db.Create(&recreated).Error)
	assert.NotEqual(t, retiredIdentity, recreated.RoutingIdentity)
	assert.NotEqual(t, retiredGeneration, recreated.RoutingGeneration)
	_, err = ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)

	lateRollup := rollup
	lateRollup.RequestCount = 10
	lateRollup.SuccessCount = 10
	lateRollup.ReliabilityRequestCount = 10
	telemetryBatch := RoutingTelemetryBatch{
		NodeID: "late-old-lifecycle", Sequence: 1, ProducedAtMs: time.Now().UnixMilli(),
		Items: []RoutingMetricRollup{lateRollup},
	}
	telemetryBatch.PayloadHash, err = ComputeRoutingTelemetryPayloadHash(telemetryBatch)
	require.NoError(t, err)
	applyResult, err := ApplyRoutingTelemetryBatchContext(context.Background(), telemetryBatch)
	require.NoError(t, err)
	assert.False(t, applyResult.Duplicate)
	assert.Zero(t, applyResult.AppliedItems)
	duplicateResult, err := ApplyRoutingTelemetryBatchContext(context.Background(), telemetryBatch)
	require.NoError(t, err)
	assert.True(t, duplicateResult.Duplicate)
	assert.Zero(t, duplicateResult.AppliedItems)
	var preservedRollup RoutingMetricRollup
	require.NoError(t, db.Where(
		"member_id = ? AND credential_id = ? AND model_key = ?",
		rollup.MemberID, rollup.CredentialID, RoutingMetricRollupModelKey(rollup.ModelName),
	).First(&preservedRollup).Error)
	assert.Equal(t, int64(1), preservedRollup.RequestCount)

	lateBreaker.ID = 0
	lateBreaker.UpdatedTime = common.GetTimestamp() + 10
	_, accepted, err = eligibility.UpsertRoutingBreakerStateForChannelContext(
		context.Background(), &lateBreaker, RoutingChannelStateFence{},
	)
	require.NoError(t, err)
	assert.False(t, accepted)
	var lateBreakerCount int64
	require.NoError(t, db.Model(&RoutingBreakerState{}).
		Where("channel_id = ? AND channel_generation = ?", original.Id, retiredGeneration).
		Count(&lateBreakerCount).Error)
	assert.Zero(t, lateBreakerCount)

	authApplied, err := ApplyRoutingChannelProbeAuthStateContext(
		context.Background(), original.Id, retiredGeneration, replacementCredential.ID,
		true, "late old lifecycle", common.GetTimestamp()+60,
	)
	require.NoError(t, err)
	assert.False(t, authApplied)

	nowMs, err := RoutingDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	lease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "routing-probe-reused-id", "node-reused-id", 5_000, 0, false,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	_, probeCreated, err := CreateRoutingProbeResultContext(context.Background(), lease, RoutingProbeResult{
		ProbeID: strings.Repeat("a", 64), TargetKey: strings.Repeat("b", 64), ProbeType: RoutingProbeTypeServing,
		SnapshotRevision: 1, PoolID: replacementMember.PoolID, MemberID: replacementMember.ID,
		ChannelID: original.Id, ChannelGeneration: retiredGeneration, CredentialID: replacementCredential.ID,
		GroupName: "default", ModelName: "gpt-test", EndpointHost: "provider-b.example",
		EndpointAuthority: "https://provider-b.example:443", Region: "global", BreakerScope: "member",
		EvidenceCount: 1, NodeCount: 1, BreakerState: RoutingBreakerStateHealthy,
		Outcome: RoutingProbeOutcomeSuccess, StartedTimeMs: nowMs, FinishedTimeMs: nowMs,
		LeaseFencingToken: lease.FencingToken, NodeEpochID: lease.HolderID, CreatedTime: nowMs,
	})
	require.NoError(t, err)
	assert.False(t, probeCreated)
	var probeCount int64
	require.NoError(t, db.Model(&RoutingProbeResult{}).Count(&probeCount).Error)
	assert.Zero(t, probeCount)

	recreatedConfiguration, err := GetRoutingChannelConfigurationContext(context.Background(), recreated.Id)
	require.NoError(t, err)
	recreatedMutation, err := UpdateRoutingChannelConfigurationContext(
		context.Background(), recreatedConfiguration, 0.5, RoutingChannelTrafficClassAll, "", false, 8,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), recreatedMutation.Configuration.Revision)
	assert.NotEqual(t, legacyEventID, recreatedMutation.Outbox.EventID)
	assert.Equal(t, recreated.RoutingGeneration, recreatedMutation.Outbox.AggregateID)

	var oldOutbox RoutingChannelConfigurationOutbox
	require.NoError(t, db.Where("event_id = ?", legacyEventID).First(&oldOutbox).Error)
	oldEvent, err := oldOutbox.DecodePayload()
	require.NoError(t, err)
	assert.Equal(t, retiredIdentity, oldEvent.RoutingIdentity)
	assert.NotEqual(t, recreated.RoutingIdentity, oldEvent.RoutingIdentity)
}
