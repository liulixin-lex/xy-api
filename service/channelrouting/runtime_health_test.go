package channelrouting

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupRuntimeHealthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/runtime-health.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingCredentialRef{},
		&model.RoutingCredentialHealthState{},
		&model.RoutingControlLease{},
	))
	previousDB := model.DB
	model.DB = db
	resetRuntimeHealthForTest()
	t.Cleanup(func() {
		resetRuntimeHealthForTest()
		model.DB = previousDB
	})
	return db
}

func TestRuntimeHealthFlushFailureRetainsDirtyCredential(t *testing.T) {
	db := setupRuntimeHealthTestDB(t)
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 1_001, ChannelID: 101, Fingerprint: "credential-1001", FingerprintVersion: 1, Active: true,
	}).Error)

	now := time.Unix(60_000, 0)
	RecordCredentialAuthFailure(1_001, 101, "serving_401", time.Time{}, now)
	require.NoError(t, db.Migrator().DropTable(&model.RoutingCredentialHealthState{}))

	err := FlushRuntimeHealthContext(context.Background())
	require.Error(t, err)
	stats := RuntimeHealthRuntimeStats()
	assert.Equal(t, 1, stats.CredentialDirty)
}

func TestRuntimeHealthRefreshDropsRetiredCredentials(t *testing.T) {
	db := setupRuntimeHealthTestDB(t)
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 1_002, ChannelID: 102, Fingerprint: "credential-1002", FingerprintVersion: 1, Active: true,
	}).Error)
	now := time.Unix(70_000, 0)
	RecordCredentialAuthFailure(1_002, 102, "serving_401", time.Time{}, now)
	require.NoError(t, FlushRuntimeHealthContext(context.Background()))

	require.NoError(t, db.Model(&model.RoutingCredentialRef{}).Where("id = ?", 1_002).Update("active", false).Error)
	runtimeHealth.Lock()
	runtimeHealth.maintenanceNextMs = time.Now().Add(time.Hour).UnixMilli()
	runtimeHealth.Unlock()

	require.NoError(t, RefreshRuntimeHealthContext(context.Background()))
	_, credentialExists := CredentialRuntimeHealth(1_002)
	assert.False(t, credentialExists)

	var credentialRows int64
	require.NoError(t, db.Model(&model.RoutingCredentialHealthState{}).Count(&credentialRows).Error)
	assert.Equal(t, int64(1), credentialRows, "physical pruning is independently throttled")
}

func TestRuntimeHealthRestartIgnoresRetiredAccountHealthTable(t *testing.T) {
	db := setupRuntimeHealthTestDB(t)
	require.NoError(t, db.Exec(`
		CREATE TABLE routing_upstream_account_health_states (
			account_id INTEGER PRIMARY KEY
		)
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO routing_upstream_account_health_states (account_id) VALUES (7001)
	`).Error)

	resetRuntimeHealthForTest()
	require.NoError(t, RefreshRuntimeHealthContext(context.Background()))

	stats := RuntimeHealthRuntimeStats()
	assert.Zero(t, stats.CredentialEntries)
	assert.Zero(t, stats.CredentialDirty)
	var legacyRows int64
	require.NoError(t, db.Table("routing_upstream_account_health_states").Count(&legacyRows).Error)
	assert.Equal(t, int64(1), legacyRows, "runtime refresh must neither restore nor mutate retired account health")
}

func TestRuntimeHealthRebuildMergesIndependentCredentialDimensions(t *testing.T) {
	resetRuntimeHealthForTest()
	t.Cleanup(resetRuntimeHealthForTest)

	local := model.RoutingCredentialHealthState{
		CredentialID: 1_003, ChannelID: 103,
		AuthFailure: false, AuthVersion: 300, AuthUpdatedTimeMs: 3_000,
		CapacityLimited: false, CapacityVersion: 100, CapacityUpdatedTimeMs: 1_000,
		UpdatedTimeMs: 3_000,
	}
	database := model.RoutingCredentialHealthState{
		CredentialID: 1_003, ChannelID: 103,
		AuthFailure: true, AuthFailureReason: "stale_401", AuthFailureUntilMs: 9_000,
		AuthVersion: 200, AuthUpdatedTimeMs: 2_000,
		CapacityLimited: true, CapacityStatusCode: http.StatusTooManyRequests,
		CapacityCooldownUntilMs: 8_000, CapacityVersion: 400, CapacityUpdatedTimeMs: 4_000,
		UpdatedTimeMs: 4_000,
	}

	merged := mergeCredentialRuntimeHealth(local, database)
	assert.False(t, merged.AuthFailure, "a stale auth revision must not replace the local clear")
	assert.Equal(t, int64(300), merged.AuthVersion)
	assert.True(t, merged.CapacityLimited, "a newer capacity revision must merge independently")
	assert.Equal(t, int64(400), merged.CapacityVersion)
	assert.Equal(t, int64(4_000), merged.UpdatedTimeMs)
}

func TestRuntimeHealthLimitNeverEvictsDirtySafetyState(t *testing.T) {
	resetRuntimeHealthForTest()
	t.Cleanup(resetRuntimeHealthForTest)
	setRuntimeHealthLimitForTest(2)
	now := time.Unix(80_000, 0)

	RecordCredentialAuthFailure(1, 11, "serving_401", time.Time{}, now)
	RecordCredentialAuthFailure(2, 22, "serving_401", time.Time{}, now)
	RecordCredentialAuthFailure(3, 33, "serving_401", time.Time{}, now)

	stats := RuntimeHealthRuntimeStats()
	assert.Equal(t, 2, stats.CredentialEntries)
	assert.Equal(t, 2, stats.CredentialDirty)
	assert.True(t, stats.CredentialOverflow)
	assert.Equal(t, int64(1), stats.CredentialOverflowDrops)
	_, credentialOneExists := CredentialRuntimeHealth(1)
	_, credentialTwoExists := CredentialRuntimeHealth(2)
	_, credentialThreeExists := CredentialRuntimeHealth(3)
	assert.True(t, credentialOneExists)
	assert.True(t, credentialTwoExists)
	assert.False(t, credentialThreeExists)
	reason, blocked := CredentialRuntimeBlocked(3, now)
	assert.True(t, blocked)
	assert.Equal(t, "credential_runtime_health_overflow", reason)
}

func TestRuntimeHealthReasonsRemainBoundedValidUTF8(t *testing.T) {
	resetRuntimeHealthForTest()
	t.Cleanup(resetRuntimeHealthForTest)
	now := time.Unix(90_000, 0)
	reason := strings.Repeat("凭", 100) + string([]byte{0xff})

	RecordCredentialAuthFailure(1, 11, reason, time.Time{}, now)
	credential, ok := CredentialRuntimeHealth(1)
	require.True(t, ok)
	assert.LessOrEqual(t, len(credential.AuthFailureReason), 256)
	assert.True(t, utf8.ValidString(credential.AuthFailureReason))
}

func TestRuntimeHealthSaturatesFarFutureDeadlines(t *testing.T) {
	resetRuntimeHealthForTest()
	t.Cleanup(resetRuntimeHealthForTest)
	now := time.Unix(100_000, 0)
	farFuture := time.Unix(math.MaxInt64, 0)

	RecordCredentialAuthFailure(1, 11, "serving_401", farFuture, now)
	RecordCredentialCapacityCooldown(1, 11, http.StatusTooManyRequests, farFuture, now)
	credential, ok := CredentialRuntimeHealth(1)
	require.True(t, ok)
	assert.Equal(t, int64(math.MaxInt64), credential.AuthFailureUntilMs)
	assert.Equal(t, int64(math.MaxInt64), credential.CapacityCooldownUntilMs)
}
