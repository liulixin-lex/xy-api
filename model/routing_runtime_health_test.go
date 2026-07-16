package model

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingRuntimeHealthUsesStableCredentialScopeAndMonotonicUpdates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/runtime-health.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&RoutingCredentialRef{},
		&RoutingCredentialHealthState{},
	))
	previousDB := DB
	previousType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, previousLogType)
	t.Cleanup(func() {
		DB = previousDB
		common.SetDatabaseTypes(previousType, previousLogType)
	})

	require.NoError(t, db.Create(&RoutingCredentialRef{
		ID: 101, ChannelID: 11, Fingerprint: "credential-fingerprint", FingerprintVersion: 1, Active: true,
	}).Error)

	require.NoError(t, UpsertRoutingCredentialHealthStatesContext(context.Background(), []RoutingCredentialHealthState{{
		CredentialID: 101, ChannelID: 11, AuthFailure: true, AuthFailureReason: "serving_401",
		AuthFailureUntilMs: 9_000, UpdatedTimeMs: 2_000,
	}}))
	require.NoError(t, UpsertRoutingCredentialHealthStatesContext(context.Background(), []RoutingCredentialHealthState{{
		CredentialID: 101, ChannelID: 11, AuthFailure: false, UpdatedTimeMs: 1_000,
	}}))
	var credentialState RoutingCredentialHealthState
	require.NoError(t, db.First(&credentialState, "credential_id = ?", 101).Error)
	assert.True(t, credentialState.AuthFailure)
	assert.Equal(t, int64(2_000), credentialState.UpdatedTimeMs)

	require.NoError(t, UpsertRoutingCredentialHealthStatesContext(context.Background(), []RoutingCredentialHealthState{{
		CredentialID: 101, ChannelID: 11, AuthFailure: false, UpdatedTimeMs: 3_000,
	}}))
	require.NoError(t, db.First(&credentialState, "credential_id = ?", 101).Error)
	assert.False(t, credentialState.AuthFailure)
	assert.Equal(t, int64(3_000), credentialState.UpdatedTimeMs)

	require.NoError(t, UpsertRoutingCredentialHealthStatesContext(context.Background(), []RoutingCredentialHealthState{{
		CredentialID: 999, ChannelID: 11, AuthFailure: true, AuthFailureReason: "unknown", UpdatedTimeMs: 4_000,
	}}))
	var missingCount int64
	require.NoError(t, db.Model(&RoutingCredentialHealthState{}).Where("credential_id = ?", 999).Count(&missingCount).Error)
	assert.Zero(t, missingCount)
}

func TestRoutingRuntimeHealthNormalizesReasonsAndRejectsInvalidBounds(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&RoutingCredentialRef{},
		&RoutingCredentialHealthState{},
	))
	require.NoError(t, db.Create(&RoutingCredentialRef{
		ID: 103, ChannelID: 13, Fingerprint: "credential-103", FingerprintVersion: 1, Active: true,
	}).Error)

	reason := strings.Repeat("凭", 100) + string([]byte{0xff})
	require.NoError(t, UpsertRoutingCredentialHealthStatesContext(context.Background(), []RoutingCredentialHealthState{{
		CredentialID: 103, ChannelID: 13,
		AuthFailure: true, AuthFailureReason: reason, AuthFailureUntilMs: 9_000,
		AuthVersion: 100, AuthUpdatedTimeMs: 1_000, UpdatedTimeMs: 1_000,
	}}))
	var state RoutingCredentialHealthState
	require.NoError(t, db.First(&state, "credential_id = ?", 103).Error)
	assert.LessOrEqual(t, len(state.AuthFailureReason), routingRuntimeHealthReasonMaxBytes)
	assert.True(t, utf8.ValidString(state.AuthFailureReason))

	err := UpsertRoutingCredentialHealthStatesContext(context.Background(), []RoutingCredentialHealthState{{
		CredentialID: 103, ChannelID: 13,
		CapacityLimited: true, CapacityStatusCode: http.StatusTooManyRequests,
		CapacityCooldownUntilMs: -1, CapacityVersion: 200, CapacityUpdatedTimeMs: 2_000,
		UpdatedTimeMs: 2_000,
	}})
	require.ErrorIs(t, err, ErrRoutingRuntimeHealthInvalid)
}

func TestRoutingRuntimeHealthCrossDatabaseDimensionMergeAndPrune(t *testing.T) {
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
			withRoutingTestDB(t, db, test.dbType)
			runRoutingRuntimeHealthDimensionContract(t)
		})
	}
}

func runRoutingRuntimeHealthDimensionContract(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(
		&RoutingCredentialRef{},
		&RoutingCredentialHealthState{},
	))
	for _, ref := range []RoutingCredentialRef{
		{ID: 301, ChannelID: 31, Fingerprint: "credential-301", FingerprintVersion: 1, Active: true},
		{ID: 302, ChannelID: 32, Fingerprint: "credential-302", FingerprintVersion: 1, Active: true},
		{ID: 304, ChannelID: 34, Fingerprint: "credential-304", FingerprintVersion: 1, Active: true},
	} {
		ref := ref
		require.NoError(t, DB.Create(&ref).Error)
	}

	require.NoError(t, UpsertRoutingCredentialHealthStatesContext(context.Background(), []RoutingCredentialHealthState{
		{
			CredentialID: 301, ChannelID: 31,
			AuthFailure: true, AuthFailureReason: "serving_401", AuthFailureUntilMs: 10_000,
			AuthVersion: 100, AuthUpdatedTimeMs: 1_000, UpdatedTimeMs: 1_000,
		},
		{
			CredentialID: 301, ChannelID: 31,
			CapacityLimited: true, CapacityStatusCode: http.StatusTooManyRequests,
			CapacityCooldownUntilMs: 20_000, CapacityVersion: 200, CapacityUpdatedTimeMs: 900,
			UpdatedTimeMs: 900,
		},
	}))
	var credential RoutingCredentialHealthState
	require.NoError(t, DB.First(&credential, "credential_id = ?", 301).Error)
	assert.True(t, credential.AuthFailure)
	assert.Equal(t, int64(100), credential.AuthVersion)
	assert.True(t, credential.CapacityLimited)
	assert.Equal(t, int64(200), credential.CapacityVersion)

	require.NoError(t, UpsertRoutingCredentialHealthStatesContext(context.Background(), []RoutingCredentialHealthState{
		{
			CredentialID: 301, ChannelID: 31, AuthFailure: false,
			AuthVersion: 300, AuthUpdatedTimeMs: 1_100, UpdatedTimeMs: 1_100,
		},
		{
			CredentialID: 301, ChannelID: 31, CapacityLimited: false,
			CapacityVersion: 200, CapacityUpdatedTimeMs: 5_000, UpdatedTimeMs: 5_000,
		},
	}))
	require.NoError(t, DB.First(&credential, "credential_id = ?", 301).Error)
	assert.False(t, credential.AuthFailure)
	assert.Equal(t, int64(300), credential.AuthVersion)
	assert.True(t, credential.CapacityLimited, "an equal capacity version must not clear the active cooldown")
	assert.Equal(t, int64(200), credential.CapacityVersion)
	assert.Equal(t, int64(1_100), credential.UpdatedTimeMs, "a stale capacity write must not advance row freshness")

	start := make(chan struct{})
	errorsByDimension := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		errorsByDimension <- UpsertRoutingCredentialHealthStatesContext(context.Background(), []RoutingCredentialHealthState{{
			CredentialID: 302, ChannelID: 32,
			AuthFailure: true, AuthFailureReason: "concurrent_401", AuthFailureUntilMs: 40_000,
			AuthVersion: 600, AuthUpdatedTimeMs: 6_000, UpdatedTimeMs: 6_000,
		}})
	}()
	go func() {
		defer wait.Done()
		<-start
		errorsByDimension <- UpsertRoutingCredentialHealthStatesContext(context.Background(), []RoutingCredentialHealthState{{
			CredentialID: 302, ChannelID: 32,
			CapacityLimited: true, CapacityStatusCode: http.StatusTooManyRequests,
			CapacityCooldownUntilMs: 50_000, CapacityVersion: 700, CapacityUpdatedTimeMs: 7_000,
			UpdatedTimeMs: 7_000,
		}})
	}()
	close(start)
	wait.Wait()
	close(errorsByDimension)
	for err := range errorsByDimension {
		require.NoError(t, err)
	}
	var concurrentCredential RoutingCredentialHealthState
	require.NoError(t, DB.First(&concurrentCredential, "credential_id = ?", 302).Error)
	assert.True(t, concurrentCredential.AuthFailure)
	assert.Equal(t, int64(600), concurrentCredential.AuthVersion)
	assert.True(t, concurrentCredential.CapacityLimited)
	assert.Equal(t, int64(700), concurrentCredential.CapacityVersion)

	require.NoError(t, DB.Create(&RoutingCredentialHealthState{
		CredentialID: 304, ChannelID: 34,
		AuthFailure: true, AuthFailureReason: "legacy_401", AuthFailureUntilMs: 70_000,
		CapacityLimited: true, CapacityStatusCode: http.StatusTooManyRequests,
		CapacityCooldownUntilMs: 70_000, UpdatedTimeMs: 10_000,
	}).Error)
	require.NoError(t, UpsertRoutingCredentialHealthStatesContext(context.Background(), []RoutingCredentialHealthState{{
		CredentialID: 304, ChannelID: 34,
		AuthFailure: false, AuthVersion: 900, AuthUpdatedTimeMs: 11_000,
		CapacityLimited: false, CapacityVersion: 1_000, CapacityUpdatedTimeMs: 9_000,
		UpdatedTimeMs: 11_000,
	}}))
	var upgradedLegacyCredential RoutingCredentialHealthState
	require.NoError(t, DB.First(&upgradedLegacyCredential, "credential_id = ?", 304).Error)
	assert.False(t, upgradedLegacyCredential.AuthFailure)
	assert.Equal(t, int64(900), upgradedLegacyCredential.AuthVersion)
	assert.True(t, upgradedLegacyCredential.CapacityLimited, "the pre-migration capacity state is newer than the incoming capacity revision")
	assert.Zero(t, upgradedLegacyCredential.CapacityVersion)

	require.NoError(t, DB.Model(&RoutingCredentialRef{}).
		Where("id IN ?", []int{301, 302, 304}).Update("active", false).Error)
	credentialStates, err := ListRoutingCredentialHealthStatesContext(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, credentialStates)
	require.NoError(t, PruneRoutingCredentialHealthStatesContext(context.Background()))
	var credentialCount int64
	require.NoError(t, DB.Model(&RoutingCredentialHealthState{}).Count(&credentialCount).Error)
	assert.Zero(t, credentialCount)
}
