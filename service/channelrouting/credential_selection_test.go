package channelrouting

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCredentialSelectionUsesStableHealthAndRequestExclusions(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	SetSnapshotForTest(SnapshotView{
		Revision: 1,
		Channels: []ChannelSnapshot{{
			ID: 101, Status: common.ChannelStatusEnabled, MultiKey: true,
			CredentialRequired: true, CredentialIDs: []int{1_001, 1_002},
		}},
		Pools: []PoolSnapshot{{
			ID: 11, GroupName: "default",
			Members: []PoolMemberSnapshot{{
				ID: 21, PoolID: 11, ChannelID: 101, MultiKey: true,
				CredentialIDs: []int{1_001, 1_002}, Models: []ModelSnapshot{{ModelName: "gpt-test"}},
			}},
		}},
	})
	snapshot := currentSnapshot.Load()
	require.NotNil(t, snapshot)
	member := snapshot.view.Pools[0].Members[0]
	now := time.Unix(10_000, 0)
	selected, reason := snapshot.selectCredential(member, "gpt-test", 7, nil, 1_002, now)
	assert.Equal(t, 1_002, selected)
	assert.Empty(t, reason)
	selected, reason = snapshot.selectCredential(member, "gpt-test", 7, nil, 9_999, now)
	assert.Zero(t, selected)
	assert.Equal(t, ExclusionReasonCredentialUnavailable, reason)
	selected, reason = snapshot.selectCredential(member, "gpt-test", 7, map[int]struct{}{1_002: {}}, 1_002, now)
	assert.Zero(t, selected)
	assert.Equal(t, ExclusionReasonCredentialRequest, reason)

	RecordCredentialAuthFailure(1_001, 101, "serving_401", time.Time{}, now)
	selected, reason = snapshot.selectCredential(member, "gpt-test", 7, nil, 0, now)
	assert.Equal(t, 1_002, selected)
	assert.Empty(t, reason)

	selected, reason = snapshot.selectCredential(member, "gpt-test", 7, map[int]struct{}{1_002: {}}, 0, now)
	assert.Zero(t, selected)
	assert.Equal(t, ExclusionReasonCredentialRequest, reason)

	ClearCredentialAuthFailure(1_001, 101, now.Add(time.Second))
	RecordCredentialCapacityCooldown(1_002, 101, 429, now.Add(time.Minute), now.Add(2*time.Second))
	selected, reason = snapshot.selectCredential(member, "gpt-test", 7, nil, 0, now.Add(3*time.Second))
	assert.Equal(t, 1_001, selected)
	assert.Empty(t, reason)
}

func TestResolveCredentialKeySurvivesReorderingAndRejectsDuplicates(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-credential-selection-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	const channelGeneration = "credential-generation-101"
	fingerprintA, err := model.RoutingCredentialFingerprint(101, channelGeneration, "key-a")
	require.NoError(t, err)
	fingerprintB, err := model.RoutingCredentialFingerprint(101, channelGeneration, "key-b")
	require.NoError(t, err)
	SetSnapshotForTest(SnapshotView{
		Revision: 1,
		Channels: []ChannelSnapshot{{ID: 101, RoutingGeneration: channelGeneration}},
	})
	snapshot := currentSnapshot.Load()
	require.NotNil(t, snapshot)
	snapshot.credentialByID[1_001] = credentialRuntime{
		ID: 1_001, ChannelID: 101, Fingerprint: fingerprintA,
		FingerprintVersion: model.RoutingCredentialFingerprintVersion,
		LastSeenIndex:      0, CurrentOccurrences: 1, Operational: true,
	}
	snapshot.credentialByID[1_002] = credentialRuntime{
		ID: 1_002, ChannelID: 101, Fingerprint: fingerprintB,
		FingerprintVersion: model.RoutingCredentialFingerprintVersion,
		LastSeenIndex:      1, CurrentOccurrences: 1, Operational: true,
	}

	reordered := &model.Channel{
		Id: 101, RoutingGeneration: channelGeneration, Key: "key-b\nkey-a",
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyStatusList: map[int]int{
			0: common.ChannelStatusEnabled, 1: common.ChannelStatusEnabled,
		}},
	}
	key, index, resolved := ResolveCredentialKey(reordered, 1_001)
	require.True(t, resolved)
	assert.Equal(t, "key-a", key)
	assert.Equal(t, 1, index)

	duplicate := *reordered
	duplicate.Key = "key-a\nkey-a"
	_, _, resolved = ResolveCredentialKey(&duplicate, 1_001)
	assert.False(t, resolved)
}

func TestResolvePersistedCredentialKeySurvivesReorderingAndRejectsChannelReplacement(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/persisted-credential.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingCredentialRef{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "persisted-credential-selection-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	const generation = "persisted-generation-101"
	fingerprint, err := model.RoutingCredentialFingerprint(101, generation, "key-a")
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 1_001, ChannelID: 101, ChannelGeneration: generation, Fingerprint: fingerprint,
		FingerprintVersion: model.RoutingCredentialFingerprintVersion,
		Active:             true, CurrentOccurrences: 1,
	}).Error)
	channel := &model.Channel{
		Id: 101, RoutingGeneration: generation, Key: "key-b\nkey-a",
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyStatusList: map[int]int{
			0: common.ChannelStatusEnabled, 1: common.ChannelStatusEnabled,
		}},
	}

	key, index, err := ResolvePersistedCredentialKey(context.Background(), channel, 1_001)
	require.NoError(t, err)
	assert.Equal(t, "key-a", key)
	assert.Equal(t, 1, index)
	credentialID, err := ResolvePersistedCredentialID(context.Background(), channel, "key-a")
	require.NoError(t, err)
	assert.Equal(t, 1_001, credentialID)

	replaced := *channel
	replaced.RoutingGeneration = "replacement-generation-101"
	_, _, err = ResolvePersistedCredentialKey(context.Background(), &replaced, 1_001)
	assert.ErrorIs(t, err, ErrPersistedCredentialUnavailable)
}

func TestRuntimeHealthFlushAndHydratePreservesCredentialServingState(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/runtime-health.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingCredentialRef{},
		&model.RoutingCredentialHealthState{},
		&model.RoutingControlLease{},
	))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 501, ChannelID: 51, Fingerprint: "fingerprint", FingerprintVersion: 1, Active: true,
	}).Error)
	resetRuntimeHealthForTest()
	t.Cleanup(resetRuntimeHealthForTest)
	now := time.Unix(20_000, 0)
	RecordCredentialAuthFailure(501, 51, "serving_401", time.Time{}, now)
	RecordCredentialCapacityCooldown(501, 51, 429, now.Add(time.Minute), now)
	require.NoError(t, FlushRuntimeHealthContext(context.Background()))

	resetRuntimeHealthForTest()
	require.NoError(t, RefreshRuntimeHealthContext(context.Background()))
	_, credentialBlocked := CredentialRuntimeBlocked(501, now.Add(time.Second))
	assert.True(t, credentialBlocked)

	ClearCredentialAuthFailure(501, 51, now.Add(2*time.Second))
	ClearCredentialCapacityCooldown(501, 51, now.Add(2*time.Second))
	require.NoError(t, FlushRuntimeHealthContext(context.Background()))
	resetRuntimeHealthForTest()
	require.NoError(t, RefreshRuntimeHealthContext(context.Background()))
	_, credentialBlocked = CredentialRuntimeBlocked(501, now.Add(3*time.Second))
	assert.False(t, credentialBlocked)
}

func TestRuntimeHealthSameTimestampKeepsIndependentCredentialDimensions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/runtime-health-same-time.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingCredentialRef{},
		&model.RoutingCredentialHealthState{},
		&model.RoutingControlLease{},
	))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 701, ChannelID: 71, Fingerprint: "fingerprint-701", FingerprintVersion: 1, Active: true,
	}).Error)
	resetRuntimeHealthForTest()
	t.Cleanup(resetRuntimeHealthForTest)

	now := time.Unix(30_000, 123_000_000)
	RecordCredentialAuthFailure(701, 71, "serving_401", time.Time{}, now)
	ClearCredentialAuthFailure(701, 71, now)
	RecordCredentialCapacityCooldown(701, 71, http.StatusTooManyRequests, now.Add(time.Minute), now)

	state, ok := CredentialRuntimeHealth(701)
	require.True(t, ok)
	assert.False(t, state.AuthFailure)
	assert.True(t, state.CapacityLimited)
	assert.Greater(t, state.AuthVersion, int64(0))
	assert.Greater(t, state.CapacityVersion, int64(0))

	require.NoError(t, FlushRuntimeHealthContext(context.Background()))
	resetRuntimeHealthForTest()
	require.NoError(t, RefreshRuntimeHealthContext(context.Background()))
	state, ok = CredentialRuntimeHealth(701)
	require.True(t, ok)
	assert.False(t, state.AuthFailure)
	assert.True(t, state.CapacityLimited)
	_, blocked := CredentialRuntimeBlocked(701, now.Add(time.Second))
	assert.True(t, blocked)
}

func TestRuntimeHealthDirtyAcknowledgementKeepsConcurrentNewerState(t *testing.T) {
	resetRuntimeHealthForTest()
	t.Cleanup(resetRuntimeHealthForTest)

	flushedCredential := model.RoutingCredentialHealthState{
		CredentialID: 801, ChannelID: 81, AuthVersion: 10, CapacityVersion: 20,
	}
	newerCredential := flushedCredential
	newerCredential.AuthVersion = 11
	runtimeHealth.Lock()
	runtimeHealth.dirtyCredentials[801] = newerCredential
	runtimeHealth.Unlock()

	acknowledgeFlushedCredentialHealth([]model.RoutingCredentialHealthState{flushedCredential})
	stats := RuntimeHealthRuntimeStats()
	assert.Equal(t, 1, stats.CredentialDirty)

	acknowledgeFlushedCredentialHealth([]model.RoutingCredentialHealthState{newerCredential})
	stats = RuntimeHealthRuntimeStats()
	assert.Zero(t, stats.CredentialDirty)

}

func TestRuntimeHealthSuccessfulRebuildClearsRecoveredOverflow(t *testing.T) {
	resetRuntimeHealthForTest()
	t.Cleanup(resetRuntimeHealthForTest)
	setRuntimeHealthLimitForTest(1)

	now := time.Unix(40_000, 0)
	runtimeHealth.Lock()
	runtimeHealth.credentials[1] = model.RoutingCredentialHealthState{
		CredentialID: 1, ChannelID: 11, AuthFailure: true,
		AuthFailureUntilMs: now.Add(time.Hour).UnixMilli(), AuthVersion: 1,
		AuthUpdatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}
	runtimeHealth.Unlock()

	RecordCredentialAuthFailure(2, 22, "serving_401", now.Add(time.Hour), now)
	stats := RuntimeHealthRuntimeStats()
	assert.True(t, stats.CredentialOverflow)

	setRuntimeHealthLimitForTest(2)
	runtimeHealth.Lock()
	runtimeHealth.dirtyCredentials = make(map[int]model.RoutingCredentialHealthState)
	rebuildCredentialRuntimeHealthLocked([]model.RoutingCredentialHealthState{{
		CredentialID: 2, ChannelID: 22, AuthFailure: true,
		AuthFailureUntilMs: now.Add(time.Hour).UnixMilli(), AuthVersion: 2,
		AuthUpdatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}})
	runtimeHealth.Unlock()

	stats = RuntimeHealthRuntimeStats()
	assert.False(t, stats.CredentialOverflow)
	assert.Equal(t, int64(1), stats.Evictions)
	_, blocked := CredentialRuntimeBlocked(99, now)
	assert.False(t, blocked)
}

func TestRuntimeHealthMaintenanceIsLocallyThrottled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/runtime-health-maintenance.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingCredentialRef{},
		&model.RoutingCredentialHealthState{},
		&model.RoutingControlLease{},
	))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	resetRuntimeHealthForTest()
	t.Cleanup(resetRuntimeHealthForTest)

	now := time.Unix(50_000, 0)
	require.NoError(t, maintainRuntimeHealthContext(context.Background(), now))
	require.NoError(t, maintainRuntimeHealthContext(context.Background(), now.Add(time.Second)))

	stats := RuntimeHealthRuntimeStats()
	assert.Equal(t, int64(1), stats.MaintenanceRuns)
	assert.Zero(t, stats.MaintenanceFailures)
	assert.Equal(t, now.Unix(), stats.MaintenanceLastUnix)
	assert.Empty(t, stats.MaintenanceLastError)
}
