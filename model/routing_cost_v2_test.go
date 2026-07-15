package model

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingCostV2MigrationAndCaseSensitiveModels(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(&routingCostSnapshotBeforeModelKey{}))
	require.NoError(t, DB.Create(&routingCostSnapshotBeforeModelKey{ChannelID: 900, ModelName: "Legacy-Model"}).Error)
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	var migratedLegacy RoutingCostSnapshot
	require.NoError(t, DB.Where("channel_id = ?", 900).First(&migratedLegacy).Error)
	require.NotNil(t, migratedLegacy.ModelKey)
	assert.Equal(t, RoutingCostModelKey("Legacy-Model"), *migratedLegacy.ModelKey)
	assert.False(t, DB.Migrator().HasIndex(&RoutingCostSnapshot{}, "idx_routing_cost_channel_model"))
	assert.True(t, DB.Migrator().HasIndex(&RoutingCostSnapshot{}, "idx_routing_cost_channel_model_key"))

	account, err := UpsertRoutingUpstreamAccountContext(context.Background(), RoutingUpstreamAccountSpec{
		SourceType:       RoutingUpstreamTypeNewAPI,
		StableIdentity:   "tenant-123",
		MaskedIdentity:   "tenant-***123",
		Status:           RoutingUpstreamAccountStatusActive,
		LastSyncStatus:   RoutingUpstreamSyncStatusSuccess,
		BalanceKnown:     true,
		Balance:          12.5,
		BalanceUpdatedAt: 100,
	})
	require.NoError(t, err)
	assert.Len(t, account.AccountKey, 64)
	assert.NotContains(t, account.AccountKey, "tenant-123")

	upper := routingCostVersionWriteForTest(account.ID, "Model-X", 0.4)
	lower := routingCostVersionWriteForTest(account.ID, "model-x", 0.8)
	upperResult, err := WriteRoutingCostSnapshotVersionContext(context.Background(), upper)
	require.NoError(t, err)
	lowerResult, err := WriteRoutingCostSnapshotVersionContext(context.Background(), lower)
	require.NoError(t, err)
	assert.NotEqual(t, upperResult.Version.LocalModelKey, lowerResult.Version.LocalModelKey)
	assert.NotEqual(t, upperResult.Version.PricingHash, lowerResult.Version.PricingHash)

	var versionCount int64
	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).Count(&versionCount).Error)
	assert.Equal(t, int64(2), versionCount)
	var latestCount int64
	require.NoError(t, DB.Model(&RoutingCostSnapshot{}).
		Where("channel_id = ? AND model_key IN ?", upper.ChannelID, []string{
			RoutingCostModelKey("Model-X"),
			RoutingCostModelKey("model-x"),
		}).
		Count(&latestCount).Error)
	assert.Equal(t, int64(2), latestCount)
	var latest RoutingCostSnapshot
	require.NoError(t, DB.Where("channel_id = ? AND model_key = ?", upper.ChannelID, RoutingCostModelKey("Model-X")).First(&latest).Error)
	assert.Equal(t, account.ID, latest.AccountID)
}

func TestNormalizeRoutingActualCostProfileAllowsClaudeCacheOutsideInputTokens(t *testing.T) {
	profile, err := normalizeRoutingActualCostProfile(
		RoutingNormalizedPricing{BillingExpression: "p * 1 + c * 2 + cr * 0.1 + cc * 1.25"},
		RoutingCostRequestProfile{ActualUsage: &RoutingCostActualUsage{
			PromptTokens: 100, CompletionTokens: 10, CacheReadTokens: 300, CacheWriteTokens: 50,
			ClaudeUsageSemantic: true,
		}},
	)

	require.NoError(t, err)
	assert.Equal(t, int64(100), profile.PromptTokens)
	assert.Equal(t, int64(10), profile.ExpectedCompletionTokens)
	require.NotNil(t, profile.actualTokenParams)
	assert.Equal(t, float64(450), profile.actualTokenParams.Len)
	assert.Equal(t, float64(100), profile.actualTokenParams.P)
	assert.Equal(t, float64(300), profile.actualTokenParams.CR)
	assert.Equal(t, float64(50), profile.actualTokenParams.CC)
}

func TestRoutingCostV2MigrationAcceptsRowsWithoutContentHash(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	account := createRoutingUpstreamAccountForTest(t)
	retryWrite := routingCostVersionWriteForTest(account.ID, "gpt-legacy-content-hash-retry", 0.5)
	retryCreated, err := WriteRoutingCostSnapshotVersionContext(context.Background(), retryWrite)
	require.NoError(t, err)
	loadWrite := routingCostVersionWriteForTest(account.ID, "gpt-legacy-content-hash-load", 0.6)
	loadCreated, err := WriteRoutingCostSnapshotVersionContext(context.Background(), loadWrite)
	require.NoError(t, err)

	require.NoError(t, DB.Migrator().DropColumn(&RoutingCostSnapshotVersion{}, "content_hash"))
	assert.False(t, DB.Migrator().HasColumn(&RoutingCostSnapshotVersion{}, "content_hash"))
	require.NoError(t, DB.AutoMigrate(&RoutingCostSnapshotVersion{}))
	assert.True(t, DB.Migrator().HasColumn(&RoutingCostSnapshotVersion{}, "content_hash"))

	var missingHashCount int64
	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).
		Where("id IN ? AND (content_hash IS NULL OR content_hash = '')", []int64{retryCreated.Version.ID, loadCreated.Version.ID}).
		Count(&missingHashCount).Error)
	assert.Equal(t, int64(2), missingHashCount)

	retry, err := WriteRoutingCostSnapshotVersionContext(context.Background(), retryWrite)
	require.NoError(t, err)
	assert.False(t, retry.Created)
	assert.Equal(t, retryCreated.Version.ID, retry.Version.ID)
	assert.Len(t, retry.Version.ContentHash, 64)

	loaded, _, err := LoadRoutingCostSnapshotVersionContext(context.Background(), loadCreated.Version.PricingHash)
	require.NoError(t, err)
	assert.Equal(t, loadCreated.Version.ID, loaded.ID)
	assert.Len(t, loaded.ContentHash, 64)

	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).
		Where("id IN ? AND (content_hash IS NULL OR content_hash = '')", []int64{retryCreated.Version.ID, loadCreated.Version.ID}).
		Count(&missingHashCount).Error)
	assert.Zero(t, missingHashCount)
}

func TestRoutingCostVersionDualWriteIsAtomic(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	account := createRoutingUpstreamAccountForTest(t)
	require.NoError(t, DB.Exec(`
		CREATE TRIGGER fail_routing_cost_latest_insert
		BEFORE INSERT ON routing_cost_snapshots
		BEGIN
			SELECT RAISE(FAIL, 'forced latest failure');
		END
	`).Error)

	_, err := WriteRoutingCostSnapshotVersionContext(
		context.Background(),
		routingCostVersionWriteForTest(account.ID, "gpt-atomic", 0.5),
	)
	require.Error(t, err)

	var versionCount int64
	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).Count(&versionCount).Error)
	assert.Zero(t, versionCount)
	var latestCount int64
	require.NoError(t, DB.Model(&RoutingCostSnapshot{}).Count(&latestCount).Error)
	assert.Zero(t, latestCount)
}

func TestCompleteRoutingCostVersionSyncReconcilesLatestModelsAtomicallyAndByChannel(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(&RoutingChannelBinding{}))
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))

	vipBinding := RoutingChannelBinding{
		ChannelID: 601, UpstreamType: RoutingUpstreamTypeNewAPI,
		BaseURL: "https://shared-upstream.example", UpstreamGroup: "vip", Enabled: true,
	}
	basicBinding := RoutingChannelBinding{
		ChannelID: 602, UpstreamType: RoutingUpstreamTypeNewAPI,
		BaseURL: "https://shared-upstream.example", UpstreamGroup: "basic", Enabled: true,
	}
	require.NoError(t, DB.Create(&vipBinding).Error)
	require.NoError(t, DB.Create(&basicBinding).Error)
	accountSpec := RoutingUpstreamAccountSpec{
		SourceType:     RoutingUpstreamTypeNewAPI,
		StableIdentity: "shared-upstream-account",
		MaskedIdentity: "shared-***-account",
		Status:         RoutingUpstreamAccountStatusActive,
		LastSyncStatus: RoutingUpstreamSyncStatusSuccess,
	}

	vipInitial, err := CompleteRoutingCostVersionSyncContext(
		context.Background(),
		vipBinding,
		accountSpec,
		[]RoutingCostSnapshotVersionWrite{
			routingCostVersionSyncWriteForTest(vipBinding, "model-a", 0.5),
			routingCostVersionSyncWriteForTest(vipBinding, "model-b", 0.6),
		},
	)
	require.NoError(t, err)
	basicInitial, err := CompleteRoutingCostVersionSyncContext(
		context.Background(),
		basicBinding,
		accountSpec,
		[]RoutingCostSnapshotVersionWrite{
			routingCostVersionSyncWriteForTest(basicBinding, "model-a", 0.7),
			routingCostVersionSyncWriteForTest(basicBinding, "model-b", 0.8),
		},
	)
	require.NoError(t, err)
	assert.Equal(t, vipInitial.Account.ID, basicInitial.Account.ID)

	var removedVersion RoutingCostSnapshotVersion
	for _, result := range vipInitial.Versions {
		if result.Version.LocalModel == "model-b" {
			removedVersion = result.Version
			break
		}
	}
	require.NotZero(t, removedVersion.ID)

	var currentVIPBinding RoutingChannelBinding
	require.NoError(t, DB.Where("id = ?", vipBinding.ID).First(&currentVIPBinding).Error)
	updatedA := routingCostVersionSyncWriteForTest(currentVIPBinding, "model-a", 0.9)
	updatedA.ObservedTime += 10
	updatedA.ExpiresTime += 10
	wrongGroupB := routingCostVersionSyncWriteForTest(currentVIPBinding, "model-b", 1.0)
	wrongGroupB.UpstreamGroup = basicBinding.UpstreamGroup

	var versionCountBeforeFailure int64
	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).Count(&versionCountBeforeFailure).Error)
	_, err = CompleteRoutingCostVersionSyncContext(
		context.Background(),
		currentVIPBinding,
		accountSpec,
		[]RoutingCostSnapshotVersionWrite{updatedA, wrongGroupB},
	)
	require.ErrorIs(t, err, ErrRoutingBindingChanged)

	var vipLatestAfterFailure []RoutingCostSnapshot
	require.NoError(t, DB.Where("channel_id = ?", vipBinding.ChannelID).
		Order("model_name asc").Find(&vipLatestAfterFailure).Error)
	require.Len(t, vipLatestAfterFailure, 2)
	assert.Equal(t, "model-a", vipLatestAfterFailure[0].ModelName)
	assert.Equal(t, "model-b", vipLatestAfterFailure[1].ModelName)
	var versionCountAfterFailure int64
	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).Count(&versionCountAfterFailure).Error)
	assert.Equal(t, versionCountBeforeFailure, versionCountAfterFailure)

	vipShrunk, err := CompleteRoutingCostVersionSyncContext(
		context.Background(), currentVIPBinding, accountSpec, []RoutingCostSnapshotVersionWrite{updatedA},
	)
	require.NoError(t, err)
	require.Len(t, vipShrunk.Latest, 1)
	assert.Equal(t, "model-a", vipShrunk.Latest[0].ModelName)

	var vipLatest []RoutingCostSnapshot
	require.NoError(t, DB.Where("channel_id = ?", vipBinding.ChannelID).
		Order("model_name asc").Find(&vipLatest).Error)
	require.Len(t, vipLatest, 1)
	assert.Equal(t, "model-a", vipLatest[0].ModelName)

	var basicLatest []RoutingCostSnapshot
	require.NoError(t, DB.Where("channel_id = ?", basicBinding.ChannelID).
		Order("model_name asc").Find(&basicLatest).Error)
	require.Len(t, basicLatest, 2)
	assert.Equal(t, "model-a", basicLatest[0].ModelName)
	assert.Equal(t, "model-b", basicLatest[1].ModelName)
	assert.Equal(t, basicBinding.UpstreamGroup, basicLatest[0].UpstreamGroup)
	assert.Equal(t, basicBinding.UpstreamGroup, basicLatest[1].UpstreamGroup)

	var retainedVersion RoutingCostSnapshotVersion
	require.NoError(t, DB.Where("id = ?", removedVersion.ID).First(&retainedVersion).Error)
	assert.Equal(t, removedVersion.PricingHash, retainedVersion.PricingHash)
	assert.Equal(t, "model-b", retainedVersion.LocalModel)
}

func TestCompleteRoutingCostVersionSyncClearsSubscriptionChannelBalanceAndKeepsAccountWallet(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(&RoutingChannelBinding{}, &RoutingChannelHealthState{}))
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))

	binding := RoutingChannelBinding{
		ChannelID: 603, UpstreamType: RoutingUpstreamTypeSub2API,
		BaseURL: "https://subscription.example", UpstreamGroup: "vip", Enabled: true,
	}
	require.NoError(t, DB.Create(&binding).Error)
	require.NoError(t, DB.Create(&RoutingChannelHealthState{
		ChannelID: binding.ChannelID, AuthFailure: true, AuthFailureReason: "serving-auth",
		AuthFailureUntil: 999, BalanceKnown: true, Balance: 0.25,
		BalanceUpdatedTime: 100, UpdatedTime: 100,
	}).Error)

	result, err := CompleteRoutingCostVersionSyncContext(
		context.Background(),
		binding,
		RoutingUpstreamAccountSpec{
			SourceType: RoutingUpstreamTypeSub2API, StableIdentity: "subscription-account",
			MaskedIdentity: "subscription-***", Status: RoutingUpstreamAccountStatusActive,
			BalanceKnown: true, Balance: 9.25, BalanceUpdatedAt: 200,
			ChannelBalanceNotApplicable: true,
			LastSyncStatus:              RoutingUpstreamSyncStatusSuccess,
		},
		[]RoutingCostSnapshotVersionWrite{
			routingCostVersionSyncWriteForTest(binding, "claude-subscription", 0.5),
		},
	)

	require.NoError(t, err)
	assert.True(t, result.Account.BalanceKnown)
	assert.Equal(t, 9.25, result.Account.Balance)
	assert.Equal(t, int64(200), result.Account.BalanceUpdatedAt)

	var health RoutingChannelHealthState
	require.NoError(t, DB.Where("channel_id = ?", binding.ChannelID).First(&health).Error)
	assert.True(t, health.AuthFailure)
	assert.Equal(t, "serving-auth", health.AuthFailureReason)
	assert.False(t, health.BalanceKnown)
	assert.Zero(t, health.Balance)
	assert.Zero(t, health.BalanceUpdatedTime)
}

func TestUpdateRoutingUpstreamAccountStatusForBindingIsFencedAndNeverCreates(t *testing.T) {
	newFixture := func(t *testing.T, createAccount bool) (RoutingChannelBinding, RoutingUpstreamAccountSpec) {
		t.Helper()
		db := openRoutingSQLiteTestDB(t)
		withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
		require.NoError(t, DB.AutoMigrate(&RoutingChannelBinding{}))
		require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
		userID := 42
		binding := RoutingChannelBinding{
			ChannelID: 604, UpstreamType: RoutingUpstreamTypeNewAPI,
			BaseURL: "https://fenced.example", UpstreamGroup: "vip",
			NewAPIUserID: &userID, Enabled: true,
		}
		require.NoError(t, DB.Create(&binding).Error)
		spec := RoutingUpstreamAccountSpec{
			SourceType: RoutingUpstreamTypeNewAPI, StableIdentity: "fenced-account",
			MaskedIdentity: "fenced-***", Status: RoutingUpstreamAccountStatusDegraded,
			PreserveBalance: true, LastSyncStatus: RoutingUpstreamSyncStatusFailed,
			LastSyncError: "sync failed",
		}
		if createAccount {
			_, err := UpsertRoutingUpstreamAccountContext(context.Background(), RoutingUpstreamAccountSpec{
				SourceType: RoutingUpstreamTypeNewAPI, StableIdentity: spec.StableIdentity,
				MaskedIdentity: spec.MaskedIdentity, Status: RoutingUpstreamAccountStatusActive,
				BalanceKnown: true, Balance: 8.5, BalanceUpdatedAt: 100,
				LastSyncStatus: RoutingUpstreamSyncStatusSuccess,
			})
			require.NoError(t, err)
		}
		return binding, spec
	}

	t.Run("current binding updates existing account and preserves balance", func(t *testing.T) {
		binding, spec := newFixture(t, true)
		applied, err := UpdateRoutingUpstreamAccountStatusForBindingContext(
			context.Background(), binding, spec,
		)
		require.NoError(t, err)
		assert.True(t, applied)
		var account RoutingUpstreamAccount
		require.NoError(t, DB.First(&account).Error)
		assert.Equal(t, RoutingUpstreamAccountStatusDegraded, account.Status)
		assert.Equal(t, RoutingUpstreamSyncStatusFailed, account.LastSyncStatus)
		assert.True(t, account.BalanceKnown)
		assert.Equal(t, 8.5, account.Balance)
	})

	t.Run("current binding does not create missing account", func(t *testing.T) {
		binding, spec := newFixture(t, false)
		applied, err := UpdateRoutingUpstreamAccountStatusForBindingContext(
			context.Background(), binding, spec,
		)
		require.NoError(t, err)
		assert.False(t, applied)
		var count int64
		require.NoError(t, DB.Model(&RoutingUpstreamAccount{}).Count(&count).Error)
		assert.Zero(t, count)
	})

	for _, mutation := range []struct {
		name   string
		mutate func(*testing.T, RoutingChannelBinding)
	}{
		{
			name: "rotated binding",
			mutate: func(t *testing.T, binding RoutingChannelBinding) {
				require.NoError(t, DB.Model(&RoutingChannelBinding{}).
					Where("id = ?", binding.ID).
					Updates(map[string]any{"base_url": "https://rotated.example", "updated_time": binding.UpdatedTime + 1}).Error)
			},
		},
		{
			name: "deleted binding",
			mutate: func(t *testing.T, binding RoutingChannelBinding) {
				require.NoError(t, DB.Delete(&RoutingChannelBinding{}, binding.ID).Error)
			},
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			binding, spec := newFixture(t, true)
			mutation.mutate(t, binding)
			applied, err := UpdateRoutingUpstreamAccountStatusForBindingContext(
				context.Background(), binding, spec,
			)
			require.ErrorIs(t, err, ErrRoutingBindingChanged)
			assert.False(t, applied)
			var account RoutingUpstreamAccount
			require.NoError(t, DB.First(&account).Error)
			assert.Equal(t, RoutingUpstreamAccountStatusActive, account.Status)
			assert.Equal(t, RoutingUpstreamSyncStatusSuccess, account.LastSyncStatus)
		})
	}
}

func TestUpsertRoutingUpstreamAccountStatusForBindingIsFenced(t *testing.T) {
	newFixture := func(t *testing.T, createAccount bool) (RoutingChannelBinding, RoutingUpstreamAccountSpec) {
		t.Helper()
		db := openRoutingSQLiteTestDB(t)
		withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
		require.NoError(t, DB.AutoMigrate(&RoutingChannelBinding{}))
		require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
		userID := 42
		binding := RoutingChannelBinding{
			ChannelID: 605, UpstreamType: RoutingUpstreamTypeNewAPI,
			BaseURL: "https://fenced-upsert.example", UpstreamGroup: "vip",
			NewAPIUserID: &userID, Enabled: true,
		}
		require.NoError(t, DB.Create(&binding).Error)
		spec := RoutingUpstreamAccountSpec{
			SourceType: RoutingUpstreamTypeNewAPI, StableIdentity: "fenced-upsert-account",
			MaskedIdentity: "fenced-upsert-***", Status: RoutingUpstreamAccountStatusDegraded,
			PreserveBalance: true, LastSyncStatus: RoutingUpstreamSyncStatusFailed,
			LastSyncError: "sync failed",
		}
		if createAccount {
			_, err := UpsertRoutingUpstreamAccountContext(context.Background(), RoutingUpstreamAccountSpec{
				SourceType: RoutingUpstreamTypeNewAPI, StableIdentity: spec.StableIdentity,
				MaskedIdentity: spec.MaskedIdentity, Status: RoutingUpstreamAccountStatusActive,
				BalanceKnown: true, Balance: 8.5, BalanceUpdatedAt: 100,
				LastSyncStatus: RoutingUpstreamSyncStatusSuccess,
			})
			require.NoError(t, err)
		}
		return binding, spec
	}

	t.Run("current binding creates an account from authoritative identity", func(t *testing.T) {
		binding, spec := newFixture(t, false)
		account, err := UpsertRoutingUpstreamAccountStatusForBindingContext(
			context.Background(), binding, spec,
		)
		require.NoError(t, err)
		assert.NotZero(t, account.ID)
		assert.Equal(t, RoutingUpstreamAccountStatusDegraded, account.Status)
		assert.Equal(t, RoutingUpstreamSyncStatusFailed, account.LastSyncStatus)
		assert.False(t, account.BalanceKnown)
	})

	t.Run("current binding degrades an existing account and preserves balance", func(t *testing.T) {
		binding, spec := newFixture(t, true)
		account, err := UpsertRoutingUpstreamAccountStatusForBindingContext(
			context.Background(), binding, spec,
		)
		require.NoError(t, err)
		assert.Equal(t, RoutingUpstreamAccountStatusDegraded, account.Status)
		assert.Equal(t, RoutingUpstreamSyncStatusFailed, account.LastSyncStatus)
		assert.True(t, account.BalanceKnown)
		assert.Equal(t, 8.5, account.Balance)
	})

	t.Run("status-only API rejects balance mutation", func(t *testing.T) {
		binding, spec := newFixture(t, false)
		spec.PreserveBalance = false
		_, err := UpsertRoutingUpstreamAccountStatusForBindingContext(
			context.Background(), binding, spec,
		)
		require.ErrorIs(t, err, ErrRoutingCostV2Invalid)
		var count int64
		require.NoError(t, DB.Model(&RoutingUpstreamAccount{}).Count(&count).Error)
		assert.Zero(t, count)
	})

	mutations := []struct {
		name   string
		mutate func(*testing.T, RoutingChannelBinding)
	}{
		{
			name: "rotated binding",
			mutate: func(t *testing.T, binding RoutingChannelBinding) {
				require.NoError(t, DB.Model(&RoutingChannelBinding{}).
					Where("id = ?", binding.ID).
					Updates(map[string]any{"base_url": "https://rotated.example", "updated_time": binding.UpdatedTime + 1}).Error)
			},
		},
		{
			name: "deleted binding",
			mutate: func(t *testing.T, binding RoutingChannelBinding) {
				require.NoError(t, DB.Delete(&RoutingChannelBinding{}, binding.ID).Error)
			},
		},
		{
			name: "disabled binding",
			mutate: func(t *testing.T, binding RoutingChannelBinding) {
				require.NoError(t, DB.Model(&RoutingChannelBinding{}).
					Where("id = ?", binding.ID).
					Updates(map[string]any{"enabled": false, "updated_time": binding.UpdatedTime + 1}).Error)
			},
		},
	}
	for _, mutation := range mutations {
		for _, accountExists := range []bool{false, true} {
			caseName := "does not create"
			if accountExists {
				caseName = "does not degrade"
			}
			t.Run(mutation.name+" "+caseName, func(t *testing.T) {
				binding, spec := newFixture(t, accountExists)
				mutation.mutate(t, binding)
				_, err := UpsertRoutingUpstreamAccountStatusForBindingContext(
					context.Background(), binding, spec,
				)
				require.ErrorIs(t, err, ErrRoutingBindingChanged)
				var accounts []RoutingUpstreamAccount
				require.NoError(t, DB.Find(&accounts).Error)
				if !accountExists {
					assert.Empty(t, accounts)
					return
				}
				require.Len(t, accounts, 1)
				assert.Equal(t, RoutingUpstreamAccountStatusActive, accounts[0].Status)
				assert.Equal(t, RoutingUpstreamSyncStatusSuccess, accounts[0].LastSyncStatus)
			})
		}
	}
}

func TestRoutingCostFailureAndSuccessfulSiblingAccountStatusAreAtomic(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, DB.AutoMigrate(&RoutingChannelBinding{}))
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))

	failed := RoutingChannelBinding{
		ChannelID: 606, UpstreamType: RoutingUpstreamTypeNewAPI,
		BaseURL: "https://atomic-account.example", UpstreamGroup: "bad", Enabled: true,
	}
	healthy := RoutingChannelBinding{
		ChannelID: 607, UpstreamType: RoutingUpstreamTypeNewAPI,
		BaseURL: "https://atomic-account.example", UpstreamGroup: "healthy", Enabled: true,
	}
	require.NoError(t, DB.Create(&failed).Error)
	require.NoError(t, DB.Create(&healthy).Error)

	degradedSpec := RoutingUpstreamAccountSpec{
		SourceType: RoutingUpstreamTypeNewAPI, StableIdentity: "atomic-account",
		MaskedIdentity: "atomic-***", Status: RoutingUpstreamAccountStatusDegraded,
		PreserveBalance: true, LastSyncStatus: RoutingUpstreamSyncStatusPartial,
		LastSyncError: "bad group unavailable",
	}
	invalidSpec := degradedSpec
	invalidSpec.MaskedIdentity = invalidSpec.StableIdentity
	_, err := ApplyRoutingCostSyncFailureWithAccountContext(
		context.Background(), failed, 1, 1_000, "bad group unavailable", invalidSpec,
	)
	require.ErrorIs(t, err, ErrRoutingCostV2Invalid)
	var unchanged RoutingChannelBinding
	require.NoError(t, DB.Where("id = ?", failed.ID).First(&unchanged).Error)
	assert.Zero(t, unchanged.SyncFailureCount)
	assert.Zero(t, unchanged.SyncBackoffUntil)

	failedFence, err := ApplyRoutingCostSyncFailureWithAccountContext(
		context.Background(), failed, 1, 1_000, "bad group unavailable", degradedSpec,
	)
	require.NoError(t, err)
	assert.Equal(t, 1, failedFence.SyncFailureCount)
	assert.Equal(t, int64(1_000), failedFence.SyncBackoffUntil)

	healthyResult, err := CompleteRoutingCostVersionSyncWithAccountFencesContext(
		context.Background(),
		healthy,
		[]RoutingChannelBinding{failedFence},
		degradedSpec,
		[]RoutingCostSnapshotVersionWrite{routingCostVersionSyncWriteForTest(healthy, "gpt-atomic", 1)},
	)
	require.NoError(t, err)
	assert.Equal(t, RoutingUpstreamAccountStatusDegraded, healthyResult.Account.Status)
	assert.Equal(t, RoutingUpstreamSyncStatusPartial, healthyResult.Account.LastSyncStatus)

	staleFence := failedFence
	require.NoError(t, DB.Model(&RoutingChannelBinding{}).
		Where("id = ?", failedFence.ID).
		Updates(map[string]any{"enabled": false, "updated_time": failedFence.UpdatedTime + 1}).Error)
	secondHealthy := RoutingChannelBinding{
		ChannelID: 608, UpstreamType: RoutingUpstreamTypeNewAPI,
		BaseURL: "https://atomic-account.example", UpstreamGroup: "other", Enabled: true,
	}
	require.NoError(t, DB.Create(&secondHealthy).Error)
	activeSpec := degradedSpec
	activeSpec.Status = RoutingUpstreamAccountStatusActive
	activeSpec.LastSyncStatus = RoutingUpstreamSyncStatusSuccess
	activeSpec.LastSyncError = ""
	_, err = CompleteRoutingCostVersionSyncWithAccountFencesContext(
		context.Background(),
		secondHealthy,
		[]RoutingChannelBinding{staleFence},
		activeSpec,
		[]RoutingCostSnapshotVersionWrite{routingCostVersionSyncWriteForTest(secondHealthy, "gpt-stale", 1)},
	)
	require.ErrorIs(t, err, ErrRoutingBindingChanged)
	var staleSnapshotCount int64
	require.NoError(t, DB.Model(&RoutingCostSnapshot{}).
		Where("channel_id = ?", secondHealthy.ChannelID).
		Count(&staleSnapshotCount).Error)
	assert.Zero(t, staleSnapshotCount)
	var account RoutingUpstreamAccount
	require.NoError(t, DB.First(&account).Error)
	assert.Equal(t, RoutingUpstreamAccountStatusDegraded, account.Status)
}

func TestRoutingCostVersionCompactsRepeatedContentAndPreservesPriceChanges(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	account := createRoutingUpstreamAccountForTest(t)

	write := routingCostVersionWriteForTest(account.ID, "gpt-idempotent", 0.5)
	first, err := WriteRoutingCostSnapshotVersionContext(context.Background(), write)
	require.NoError(t, err)
	assert.True(t, first.Created)
	assert.Len(t, first.Version.ApplyToken, 32)

	retry, err := WriteRoutingCostSnapshotVersionContext(context.Background(), write)
	require.NoError(t, err)
	assert.False(t, retry.Created)
	assert.Equal(t, first.Version.ID, retry.Version.ID)
	assert.Equal(t, first.Version.ApplyToken, retry.Version.ApplyToken)

	write.ObservedTime += 10
	write.ExpiresTime += 10
	second, err := WriteRoutingCostSnapshotVersionContext(context.Background(), write)
	require.NoError(t, err)
	assert.True(t, second.Created)
	assert.NotEqual(t, first.Version.ID, second.Version.ID)
	assert.NotEqual(t, first.Version.PricingHash, second.Version.PricingHash)
	assert.Equal(t, write.ObservedTime, second.Version.ObservedTime)

	var versionCount int64
	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).Count(&versionCount).Error)
	assert.Equal(t, int64(2), versionCount)
	var latest RoutingCostSnapshot
	require.NoError(t, DB.Where("channel_id = ? AND model_key = ?", write.ChannelID, RoutingCostModelKey(write.LocalModel)).First(&latest).Error)
	assert.Equal(t, write.ObservedTime, latest.SnapshotTS)
	assert.Equal(t, write.PricingVersion, latest.PricingVersion)
	older := write
	older.ObservedTime -= 20
	olderResult, err := WriteRoutingCostSnapshotVersionContext(context.Background(), older)
	require.NoError(t, err)
	assert.True(t, olderResult.Created)
	assert.Equal(t, write.ObservedTime, olderResult.Latest.SnapshotTS)
	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).Count(&versionCount).Error)
	assert.Equal(t, int64(2), versionCount)

	changed := write
	changed.ObservedTime += 20
	changed.ExpiresTime += 20
	changed.PricingVersion = "pricing-v2"
	changed.Pricing.InputCostPerMillion = routingCostFloatForTest(0.75)
	changedResult, err := WriteRoutingCostSnapshotVersionContext(context.Background(), changed)
	require.NoError(t, err)
	assert.True(t, changedResult.Created)
	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).Count(&versionCount).Error)
	assert.Equal(t, int64(3), versionCount)

	loaded, _, err := LoadRoutingCostSnapshotVersionContext(context.Background(), second.Version.PricingHash)
	require.NoError(t, err)
	assert.Equal(t, second.Version.ObservedTime, loaded.ObservedTime)
	assert.Equal(t, second.Version.ExpiresTime, loaded.ExpiresTime)

	err = DB.Model(&RoutingCostSnapshotVersion{}).
		Where("id = ?", first.Version.ID).
		Update("pricing_version", "tampered").Error
	require.ErrorIs(t, err, ErrRoutingCostHistoryImmutable)
}

func TestRoutingCostVersionRetentionDeletesExpiredHistoryButKeepsLatestAndScheduled(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	account := createRoutingUpstreamAccountForTest(t)

	now := common.GetTimestamp()
	activeWrite := routingCostVersionWriteForTest(account.ID, "gpt-retained-latest", 0.5)
	activeWrite.ObservedTime = now - 7200
	activeWrite.EffectiveTime = now - 7200
	activeWrite.ExpiresTime = now + 3600
	active, err := WriteRoutingCostSnapshotVersionContext(context.Background(), activeWrite)
	require.NoError(t, err)

	scheduledWrite := routingCostVersionWriteForTest(account.ID, "gpt-scheduled", 0.7)
	scheduledWrite.ObservedTime = now - 7200
	scheduledWrite.EffectiveTime = now + 3600
	scheduledWrite.ExpiresTime = now + 7200
	scheduled, err := WriteRoutingCostSnapshotVersionContext(context.Background(), scheduledWrite)
	require.NoError(t, err)
	assert.Zero(t, scheduled.Latest.ID)

	cutoff := now - 3600
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Model(&RoutingCostSnapshotVersion{}).
		Where("id IN ?", []int64{active.Version.ID, scheduled.Version.ID}).
		Update("created_time", cutoff-1).Error)

	deleted, err := DeleteRoutingCostSnapshotVersionsBeforeContext(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	var activeHistoryCount int64
	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).Where("id = ?", active.Version.ID).Count(&activeHistoryCount).Error)
	assert.Zero(t, activeHistoryCount)
	var scheduledHistoryCount int64
	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).Where("id = ?", scheduled.Version.ID).Count(&scheduledHistoryCount).Error)
	assert.Equal(t, int64(1), scheduledHistoryCount)
	var latestCount int64
	require.NoError(t, DB.Model(&RoutingCostSnapshot{}).
		Where("channel_id = ? AND model_key = ?", activeWrite.ChannelID, RoutingCostModelKey(activeWrite.LocalModel)).
		Count(&latestCount).Error)
	assert.Equal(t, int64(1), latestCount)
}

func TestRoutingCostVersionRejectsTamperedObservationMetadata(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	account := createRoutingUpstreamAccountForTest(t)
	result, err := WriteRoutingCostSnapshotVersionContext(
		context.Background(),
		routingCostVersionWriteForTest(account.ID, "gpt-tampered", 0.5),
	)
	require.NoError(t, err)

	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Model(&RoutingCostSnapshotVersion{}).
		Where("id = ?", result.Version.ID).
		Update("expires_time", result.Version.ExpiresTime+60).Error)
	_, _, err = LoadRoutingCostSnapshotVersionContext(context.Background(), result.Version.PricingHash)
	assert.ErrorIs(t, err, ErrRoutingCostVersionCorrupt)
}

func TestRoutingCostVersionRejectsExpiredAndInvalidPrices(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	account := createRoutingUpstreamAccountForTest(t)

	tests := []struct {
		name   string
		mutate func(*RoutingCostSnapshotVersionWrite)
		want   error
	}{
		{
			name: "expired",
			mutate: func(write *RoutingCostSnapshotVersionWrite) {
				write.ExpiresTime = common.GetTimestamp() - 1
			},
			want: ErrRoutingCostSnapshotExpired,
		},
		{
			name: "negative",
			mutate: func(write *RoutingCostSnapshotVersionWrite) {
				write.Pricing.InputCostPerMillion = routingCostFloatForTest(-1)
			},
			want: ErrRoutingCostV2Invalid,
		},
		{
			name: "nan",
			mutate: func(write *RoutingCostSnapshotVersionWrite) {
				write.Pricing.InputCostPerMillion = routingCostFloatForTest(math.NaN())
			},
			want: ErrRoutingCostV2Invalid,
		},
		{
			name: "infinity",
			mutate: func(write *RoutingCostSnapshotVersionWrite) {
				write.Pricing.InputCostPerMillion = routingCostFloatForTest(math.Inf(1))
			},
			want: ErrRoutingCostV2Invalid,
		},
		{
			name: "future observed time",
			mutate: func(write *RoutingCostSnapshotVersionWrite) {
				write.ObservedTime = common.GetTimestamp() + int64(routingCostMaxFutureClockSkew/time.Second) + 1
				write.ExpiresTime = write.ObservedTime + 3600
			},
			want: ErrRoutingCostV2Invalid,
		},
		{
			name: "invalid billing expression",
			mutate: func(write *RoutingCostSnapshotVersionWrite) {
				write.Pricing.BillingExpression = "tier("
			},
			want: ErrRoutingCostV2Invalid,
		},
		{
			name: "negative billing expression",
			mutate: func(write *RoutingCostSnapshotVersionWrite) {
				write.Pricing.BillingExpression = "-1"
			},
			want: ErrRoutingCostV2Invalid,
		},
		{
			name: "empty tiers are not known cost",
			mutate: func(write *RoutingCostSnapshotVersionWrite) {
				write.Pricing.BaseRatio = nil
				write.Pricing.InputCostPerMillion = nil
				write.Pricing.OutputCostPerMillion = nil
				write.Pricing.Tiers = []byte("[]")
			},
			want: ErrRoutingCostV2Invalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			write := routingCostVersionWriteForTest(account.ID, "gpt-invalid-"+test.name, 0.5)
			test.mutate(&write)
			_, err := WriteRoutingCostSnapshotVersionContext(context.Background(), write)
			require.ErrorIs(t, err, test.want)
		})
	}

	unknown := routingCostVersionWriteForTest(account.ID, "gpt-unknown", 0)
	unknown.Confidence = RoutingCostConfidenceUnknown
	unknown.ConfidenceScore = 0
	unknown.Freshness = RoutingCostFreshnessUnknown
	unknown.FreshnessScore = 0
	unknown.Pricing.InputCostPerMillion = nil
	unknown.Pricing.OutputCostPerMillion = nil
	result, err := WriteRoutingCostSnapshotVersionContext(context.Background(), unknown)
	require.NoError(t, err)
	assert.Equal(t, RoutingCostConfidenceUnknown, result.Latest.Confidence)
	assert.Zero(t, result.Latest.BaseRatio)
	assert.Zero(t, result.Latest.ModelPrice)
}

func TestRoutingCostFutureEffectiveVersionDoesNotReplaceCurrentLatest(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	account := createRoutingUpstreamAccountForTest(t)
	currentWrite := routingCostVersionWriteForTest(account.ID, "gpt-scheduled", 0.5)
	current, err := WriteRoutingCostSnapshotVersionContext(context.Background(), currentWrite)
	require.NoError(t, err)

	scheduledWrite := routingCostVersionWriteForTest(account.ID, "gpt-scheduled", 0.8)
	scheduledWrite.PricingVersion = "pricing-v2"
	scheduledWrite.EffectiveTime = common.GetTimestamp() + 3600
	scheduledWrite.ExpiresTime = scheduledWrite.EffectiveTime + 3600
	scheduled, err := WriteRoutingCostSnapshotVersionContext(context.Background(), scheduledWrite)
	require.NoError(t, err)
	assert.True(t, scheduled.Created)
	assert.Equal(t, current.Latest.ID, scheduled.Latest.ID)
	assert.Equal(t, current.Latest.PricingVersion, scheduled.Latest.PricingVersion)
	assert.Equal(t, current.Latest.BaseRatio, scheduled.Latest.BaseRatio)

	var versions int64
	require.NoError(t, DB.Model(&RoutingCostSnapshotVersion{}).Count(&versions).Error)
	assert.Equal(t, int64(2), versions)
}

func TestRoutingCostAcceptsValidatedExpressionPricing(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	account := createRoutingUpstreamAccountForTest(t)

	write := routingCostVersionWriteForTest(account.ID, "gpt-expression", 0)
	write.Pricing.BaseRatio = nil
	write.Pricing.InputCostPerMillion = nil
	write.Pricing.OutputCostPerMillion = nil
	write.Pricing.BillingExpression = `tier("base", p * 2.5 + c * 15)`
	result, err := WriteRoutingCostSnapshotVersionContext(context.Background(), write)
	require.NoError(t, err)
	assert.True(t, result.Created)
	assert.Equal(t, RoutingCostConfidenceFull, result.Latest.Confidence)

	tierWrite := routingCostVersionWriteForTest(account.ID, "gpt-tier-expression", 0)
	tierWrite.Pricing.BaseRatio = nil
	tierWrite.Pricing.InputCostPerMillion = nil
	tierWrite.Pricing.OutputCostPerMillion = nil
	tierWrite.Pricing.Tiers = []byte(`{"type":"expr","expr":"tier(\"base\", p * 1 + c * 2)"}`)
	tierResult, err := WriteRoutingCostSnapshotVersionContext(context.Background(), tierWrite)
	require.NoError(t, err)
	assert.True(t, tierResult.Created)
}

func TestRoutingCostEstimateDefinesExpectedWorstAndEffectivePlatformCost(t *testing.T) {
	observed := common.GetTimestamp()
	groupRatio := 2.0
	pricing := RoutingNormalizedPricing{
		QuotaType:         0,
		BillingMode:       "tiered_expr",
		Currency:          "USD",
		GroupRatio:        &groupRatio,
		BillingExpression: `tier("base", p * 2 + c * 10)`,
	}
	version := RoutingCostSnapshotVersion{
		Confidence:      RoutingCostConfidenceExact,
		ConfidenceScore: 1,
		Freshness:       RoutingCostFreshnessFresh,
		FreshnessScore:  1,
		ObservedTime:    observed,
		EffectiveTime:   observed,
		ExpiresTime:     observed + 3600,
	}
	profile := RoutingCostRequestProfile{
		PromptTokens:             1_000,
		ExpectedCompletionTokens: 500,
		MaximumCompletionTokens:  1_000,
		MaxAttempts:              3,
		RetryProbability:         0.5,
		HedgeProbability:         0.25,
		HedgeAllowed:             true,
	}

	estimate, err := EstimateRoutingCostSnapshot(version, pricing, profile, observed)

	require.NoError(t, err)
	assert.True(t, estimate.Known)
	assert.True(t, estimate.ExpectedKnown)
	assert.True(t, estimate.WorstCaseKnown)
	assert.True(t, estimate.ExpectedEffectiveKnown)
	assert.InDelta(t, 0.014, estimate.ExpectedCost, 1e-12)
	assert.InDelta(t, 0.096, estimate.WorstCaseCost, 1e-12)
	assert.InDelta(t, 0.028, estimate.ExpectedEffectiveCost, 1e-12)
	assert.Equal(t, "USD", estimate.Currency)
	assert.Equal(t, "expression", estimate.Unit)
	assert.InDelta(t, estimate.ExpectedCost, estimate.ExpectedBreakdown.Expression, 1e-12)

	expired, err := EstimateRoutingCostSnapshot(version, pricing, profile, version.ExpiresTime)
	require.NoError(t, err)
	assert.False(t, expired.Known)
	assert.Zero(t, expired.ExpectedCost)
	assert.Zero(t, expired.FreshnessScore)
}

func TestRoutingCostEstimatePreservesExplicitFreeGroupRatio(t *testing.T) {
	observed := common.GetTimestamp()
	groupRatio := 0.0
	inputRate := 3.0
	outputRate := 15.0
	perRequest := 0.08
	version := RoutingCostSnapshotVersion{
		Confidence: RoutingCostConfidenceExact, ConfidenceScore: 1,
		Freshness: RoutingCostFreshnessFresh, FreshnessScore: 1,
		ObservedTime: observed, EffectiveTime: observed, ExpiresTime: observed + 3_600,
	}
	profile := RoutingCostRequestProfile{
		PromptTokens: 1_000, ExpectedCompletionTokens: 500, MaximumCompletionTokens: 500,
		KnowledgeSpecified: true,
	}
	tests := []struct {
		name    string
		pricing RoutingNormalizedPricing
	}{
		{
			name: "token",
			pricing: RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "token", Currency: "USD",
				GroupRatio: &groupRatio, InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
			},
		},
		{
			name: "per request",
			pricing: RoutingNormalizedPricing{
				QuotaType: 1, BillingMode: "per_request", Currency: "USD",
				GroupRatio: &groupRatio, PerRequestCost: &perRequest,
			},
		},
		{
			name: "expression",
			pricing: RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "tiered_expr", Currency: "USD",
				GroupRatio:        &groupRatio,
				BillingExpression: `len > 0 ? tier("` + RoutingCostSub2APIIntervalUnmatchedTier + `", 0) : tier("empty", 0)`,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			estimate, err := EstimateRoutingCostSnapshot(version, test.pricing, profile, observed)

			require.NoError(t, err)
			assert.True(t, estimate.Known)
			assert.True(t, estimate.ExpectedKnown)
			assert.True(t, estimate.WorstCaseKnown)
			assert.Zero(t, estimate.ExpectedCost)
			assert.Zero(t, estimate.WorstCaseCost)
		})
	}
}

func TestRoutingCostVersionRoundTripsNilAndExplicitZeroGroupRatio(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	account := createRoutingUpstreamAccountForTest(t)

	write := routingCostVersionWriteForTest(account.ID, "gpt-group-ratio-semantics", 3)
	write.Pricing.GroupRatio = routingCostFloatForTest(0)
	zeroResult, err := WriteRoutingCostSnapshotVersionContext(context.Background(), write)
	require.NoError(t, err)

	write.Pricing.GroupRatio = nil
	nilResult, err := WriteRoutingCostSnapshotVersionContext(context.Background(), write)
	require.NoError(t, err)
	assert.NotEqual(t, zeroResult.Version.PricingHash, nilResult.Version.PricingHash)

	_, zeroPricing, err := LoadRoutingCostSnapshotVersionContext(
		context.Background(), zeroResult.Version.PricingHash,
	)
	require.NoError(t, err)
	require.NotNil(t, zeroPricing.GroupRatio)
	assert.Zero(t, *zeroPricing.GroupRatio)

	_, nilPricing, err := LoadRoutingCostSnapshotVersionContext(
		context.Background(), nilResult.Version.PricingHash,
	)
	require.NoError(t, err)
	assert.Nil(t, nilPricing.GroupRatio)

	var latest RoutingCostSnapshot
	require.NoError(t, DB.Where(
		"channel_id = ? AND model_key = ?", write.ChannelID, RoutingCostModelKey(write.LocalModel),
	).First(&latest).Error)
	latestPricing, known, err := DecodeRoutingCostSnapshotPricing(latest)
	require.NoError(t, err)
	assert.True(t, known)
	assert.Nil(t, latestPricing.GroupRatio)
}

func TestRoutingCostEstimateTreatsUnmatchedSub2APIIntervalsAsUnknown(t *testing.T) {
	observed := common.GetTimestamp()
	version := RoutingCostSnapshotVersion{
		Confidence: RoutingCostConfidenceDerived, ConfidenceScore: 0.9,
		Freshness: RoutingCostFreshnessFresh, FreshnessScore: 1,
		ObservedTime: observed, EffectiveTime: observed, ExpiresTime: observed + 3_600,
	}
	pricing := RoutingNormalizedPricing{
		QuotaType: 0, BillingMode: "tiered_expr", Currency: "USD",
		BillingExpression: `len > 0 ? tier("priced", p * 3 + c * 15 + cr * 0.3 + cc * 3.75 + cc1h * 3.75 + img_o * 30 + ao * 40) : tier("` +
			RoutingCostSub2APIIntervalUnmatchedTier + `", 0)`,
	}
	tests := []struct {
		name    string
		profile RoutingCostRequestProfile
		known   bool
	}{
		{
			name: "output without input context",
			profile: RoutingCostRequestProfile{
				ExpectedCompletionTokens: 10, MaximumCompletionTokens: 10,
			},
		},
		{
			name: "image output without input context",
			profile: RoutingCostRequestProfile{
				ImageOutputTokens: 1,
			},
		},
		{
			name: "audio output without input context",
			profile: RoutingCostRequestProfile{
				AudioOutputTokens: 1,
			},
		},
		{
			name: "actual output without input context",
			profile: RoutingCostRequestProfile{
				ActualUsage: &RoutingCostActualUsage{CompletionTokens: 10},
			},
		},
		{
			name:    "empty request has a known zero cost",
			profile: RoutingCostRequestProfile{},
			known:   true,
		},
		{
			name: "positive context matches a priced interval",
			profile: RoutingCostRequestProfile{
				PromptTokens: 1, ExpectedCompletionTokens: 1, MaximumCompletionTokens: 1,
			},
			known: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			estimate, err := EstimateRoutingCostSnapshot(version, pricing, test.profile, observed)

			require.NoError(t, err)
			assert.Equal(t, test.known, estimate.ExpectedKnown)
			assert.Equal(t, test.known, estimate.WorstCaseKnown)
			assert.Equal(t, test.known, estimate.Known)
			if !test.known {
				assert.Zero(t, estimate.ExpectedCost)
				assert.Zero(t, estimate.WorstCaseCost)
			}
		})
	}

	ordinaryZero := pricing
	ordinaryZero.BillingExpression = `len > 0 ? tier("priced", p) : tier("ordinary_zero", 0)`
	estimate, err := EstimateRoutingCostSnapshot(version, ordinaryZero, RoutingCostRequestProfile{}, observed)
	require.NoError(t, err)
	assert.True(t, estimate.Known)
	assert.Zero(t, estimate.ExpectedCost)
}

func TestRoutingCostEstimateEnforcesSub2APIDisplayContractPerRequest(t *testing.T) {
	observed := common.GetTimestamp()
	version := RoutingCostSnapshotVersion{
		SourceType: RoutingUpstreamTypeSub2API,
		Confidence: RoutingCostConfidenceDerived, ConfidenceScore: 0.8,
		Freshness: RoutingCostFreshnessFresh, FreshnessScore: 1,
		ObservedTime: observed, EffectiveTime: observed, ExpiresTime: observed + 3_600,
	}
	baseProfile := RoutingCostRequestProfile{
		PromptTokens: 1_000, MaximumPromptTokens: 1_000,
		ExpectedCompletionTokens: 100, MaximumCompletionTokens: 100,
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		MediaDimensionsKnown: true, RequestInputKnown: true,
		RequestPricingFeaturesKnown: true,
		Request:                     billingexpr.RequestInput{Body: []byte(`{}`)},
	}
	tests := []struct {
		name         string
		platform     string
		hasIntervals bool
		body         string
		headers      map[string]string
		mutate       func(*RoutingCostRequestProfile)
		known        bool
	}{
		{name: "flat standard", platform: "anthropic", body: `{"service_tier":"standard"}`, known: true},
		{
			name: "request pricing features unavailable", platform: "anthropic", body: `{}`,
			mutate: func(profile *RoutingCostRequestProfile) {
				profile.RequestPricingFeaturesKnown = false
			},
		},
		{
			name: "request knowledge unspecified", platform: "anthropic", body: `{}`,
			mutate: func(profile *RoutingCostRequestProfile) {
				profile.KnowledgeSpecified = false
			},
		},
		{
			name: "flat web search surcharge", platform: "openai", body: `{}`,
			mutate: func(profile *RoutingCostRequestProfile) { profile.UncataloguedSurchargePossible = true },
		},
		{
			name: "interval file search surcharge", platform: "openai", hasIntervals: true, body: `{}`,
			mutate: func(profile *RoutingCostRequestProfile) { profile.UncataloguedSurchargePossible = true },
		},
		{
			name: "interval image generation surcharge", platform: "openai", hasIntervals: true, body: `{}`,
			mutate: func(profile *RoutingCostRequestProfile) { profile.UncataloguedSurchargePossible = true },
		},
		{name: "flat priority", platform: "openai", body: `{"service_tier":"priority"}`},
		{name: "flat fast alias", platform: "openai", body: `{"service_tier":"fast"}`},
		{name: "flat flex", platform: "openai", body: `{"service_tier":"flex"}`},
		{name: "interval priority", platform: "openai", hasIntervals: true, body: `{"service_tier":"priority"}`, known: true},
		{name: "interval fast header", platform: "openai", hasIntervals: true, headers: map[string]string{"Anthropic-Beta": "context-1m-2025-08-07, fast-mode-2026-02-01"}, known: true},
		{name: "interval flex", platform: "openai", hasIntervals: true, body: `{"service_tier":"flex"}`},
		{
			name: "interval remote context", platform: "openai", hasIntervals: true,
			mutate: func(profile *RoutingCostRequestProfile) { profile.InputTokensKnown = false },
		},
		{
			name: "flat one hour cache write", platform: "anthropic",
			mutate: func(profile *RoutingCostRequestProfile) { profile.CacheWriteOneHourTokens = 1 },
		},
		{
			name: "interval one hour cache write", platform: "anthropic", hasIntervals: true, known: true,
			mutate: func(profile *RoutingCostRequestProfile) { profile.CacheWriteOneHourTokens = 1 },
		},
		{
			name: "flat openai at threshold", platform: "openai", known: true,
			mutate: func(profile *RoutingCostRequestProfile) {
				profile.PromptTokens = 272_000
				profile.MaximumPromptTokens = 272_000
			},
		},
		{
			name: "flat openai above threshold", platform: "openai",
			mutate: func(profile *RoutingCostRequestProfile) {
				profile.PromptTokens = 272_001
				profile.MaximumPromptTokens = 272_001
			},
		},
		{
			name: "flat openai remote context", platform: "openai",
			mutate: func(profile *RoutingCostRequestProfile) { profile.InputTokensKnown = false },
		},
		{
			name: "interval openai above threshold", platform: "openai", hasIntervals: true, known: true,
			mutate: func(profile *RoutingCostRequestProfile) {
				profile.PromptTokens = 272_001
				profile.MaximumPromptTokens = 272_001
			},
		},
		{
			name: "flat gemini at threshold", platform: "gemini", known: true,
			mutate: func(profile *RoutingCostRequestProfile) {
				profile.PromptTokens = 199_000
				profile.MaximumPromptTokens = 199_000
				profile.CacheReadTokens = 1_000
			},
		},
		{
			name: "flat gemini above threshold", platform: "gemini",
			mutate: func(profile *RoutingCostRequestProfile) {
				profile.PromptTokens = 199_000
				profile.MaximumPromptTokens = 199_000
				profile.CacheReadTokens = 1_001
			},
		},
		{
			name: "flat gemini remote context", platform: "gemini",
			mutate: func(profile *RoutingCostRequestProfile) { profile.InputTokensKnown = false },
		},
		{name: "unknown service tier", platform: "openai", body: `{"service_tier":"turbo"}`},
		{name: "missing platform fails closed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := baseProfile
			if test.body != "" {
				profile.Request.Body = []byte(test.body)
			}
			profile.Request.Headers = test.headers
			if test.mutate != nil {
				test.mutate(&profile)
			}
			extras, err := common.Marshal(map[string]any{
				"platform":            test.platform,
				"source_billing_mode": "token",
				"sub2api_contract":    RoutingCostSub2APIDisplayContractV1,
				"has_intervals":       test.hasIntervals,
			})
			require.NoError(t, err)
			pricing := RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "tiered_expr", Currency: "USD",
				BillingExpression: `tier("base", p * 2 + c * 10 + cr * 0.2 + cc * 2.5 + cc1h * 2.5)`,
				Extras:            extras,
			}

			estimate, err := EstimateRoutingCostSnapshot(version, pricing, profile, observed)

			require.NoError(t, err)
			assert.Equal(t, test.known, estimate.ExpectedKnown)
			assert.Equal(t, test.known, estimate.WorstCaseKnown)
			assert.Equal(t, test.known, estimate.Known)
		})
	}
}

func TestRoutingCostEstimateEnforcesNewAPIPricingCatalogScope(t *testing.T) {
	observed := common.GetTimestamp()
	version := RoutingCostSnapshotVersion{
		SourceType: RoutingUpstreamTypeNewAPI,
		Confidence: RoutingCostConfidenceExact, ConfidenceScore: 1,
		Freshness: RoutingCostFreshnessFresh, FreshnessScore: 1,
		ObservedTime: observed, EffectiveTime: observed, ExpiresTime: observed + 3_600,
	}
	inputRate := 2.0
	outputRate := 10.0
	groupRatio := 1.0
	baseProfile := RoutingCostRequestProfile{
		PromptTokens: 1_000, MaximumPromptTokens: 1_000,
		ExpectedCompletionTokens: 100, MaximumCompletionTokens: 100,
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		MediaDimensionsKnown: true, RequestInputKnown: true,
		RequestPricingFeaturesKnown: true,
	}
	tests := []struct {
		name            string
		featuresKnown   bool
		potentialCharge bool
		alwaysCharge    bool
		actualUsage     bool
		known           bool
	}{
		{name: "ordinary request", featuresKnown: true, known: true},
		{name: "request features unavailable"},
		{name: "tool or fixed image surcharge", featuresKnown: true, potentialCharge: true},
		{name: "search preview model surcharge", featuresKnown: true, alwaysCharge: true},
		{name: "actual usage retains request surcharge boundary", featuresKnown: true, potentialCharge: true, actualUsage: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extras, err := common.Marshal(map[string]any{
				"catalog_scope":                 RoutingCostCatalogScopeNewAPIPricing,
				"always_uncatalogued_surcharge": test.alwaysCharge,
			})
			require.NoError(t, err)
			pricing := RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "token", Currency: "USD",
				GroupRatio: &groupRatio, InputCostPerMillion: &inputRate,
				OutputCostPerMillion: &outputRate, Extras: extras,
			}
			profile := baseProfile
			profile.RequestPricingFeaturesKnown = test.featuresKnown
			profile.UncataloguedSurchargePossible = test.potentialCharge
			if test.actualUsage {
				profile.ActualUsage = &RoutingCostActualUsage{PromptTokens: 1_000, CompletionTokens: 100}
			}

			estimate, err := EstimateRoutingCostSnapshot(version, pricing, profile, observed)

			require.NoError(t, err)
			assert.Equal(t, test.known, estimate.ExpectedKnown)
			assert.Equal(t, test.known, estimate.WorstCaseKnown)
			assert.Equal(t, test.known, estimate.Known)
		})
	}

	freeGroup := 0.0
	extras, err := common.Marshal(map[string]any{
		"catalog_scope":                 RoutingCostCatalogScopeNewAPIPricing,
		"always_uncatalogued_surcharge": true,
	})
	require.NoError(t, err)
	freeEstimate, err := EstimateRoutingCostSnapshot(version, RoutingNormalizedPricing{
		QuotaType: 0, BillingMode: "token", Currency: "USD",
		GroupRatio: &freeGroup, InputCostPerMillion: &inputRate,
		OutputCostPerMillion: &outputRate, Extras: extras,
	}, baseProfile, observed)
	require.NoError(t, err)
	assert.True(t, freeEstimate.Known)
	assert.Zero(t, freeEstimate.ExpectedCost)
}

func TestRoutingCostEstimateFailsClosedForHistoricalProviderSnapshotsWithoutContractMetadata(t *testing.T) {
	observed := common.GetTimestamp()
	inputRate := 2.0
	outputRate := 10.0
	perRequestRate := 0.05
	groupRatio := 1.0
	newAPIExtras, err := common.Marshal(map[string]any{
		"catalog_scope":                 RoutingCostCatalogScopeNewAPIPricing,
		"always_uncatalogued_surcharge": false,
	})
	require.NoError(t, err)
	sub2APIExtras, err := common.Marshal(map[string]any{
		"platform":            "anthropic",
		"source_billing_mode": "token",
		"sub2api_contract":    RoutingCostSub2APIDisplayContractV1,
		"has_intervals":       false,
	})
	require.NoError(t, err)
	sub2APIPerRequestExtras, err := common.Marshal(map[string]any{
		"sub2api_contract": RoutingCostSub2APIDisplayContractV1,
	})
	require.NoError(t, err)

	profile := RoutingCostRequestProfile{
		PromptTokens: 1_000, MaximumPromptTokens: 1_000,
		ExpectedCompletionTokens: 100, MaximumCompletionTokens: 100,
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		MediaDimensionsKnown: true, RequestInputKnown: true,
		RequestPricingFeaturesKnown: true,
		Request:                     billingexpr.RequestInput{Body: []byte(`{}`)},
	}
	tests := []struct {
		name        string
		sourceType  string
		extras      []byte
		perRequest  bool
		expectKnown bool
	}{
		{name: "untyped legacy snapshot remains compatible", expectKnown: true},
		{name: "newapi legacy snapshot without extras", sourceType: RoutingUpstreamTypeNewAPI},
		{name: "newapi unrelated extras are not a contract", sourceType: RoutingUpstreamTypeNewAPI, extras: []byte(`{"platform":"openai"}`)},
		{name: "newapi declared catalog remains known", sourceType: RoutingUpstreamTypeNewAPI, extras: newAPIExtras, expectKnown: true},
		{name: "sub2api legacy flat snapshot without extras", sourceType: RoutingUpstreamTypeSub2API},
		{name: "sub2api unrelated extras are not a contract", sourceType: RoutingUpstreamTypeSub2API, extras: []byte(`{"input_price":2}`)},
		{name: "sub2api declared flat contract remains known", sourceType: RoutingUpstreamTypeSub2API, extras: sub2APIExtras, expectKnown: true},
		{name: "sub2api legacy per-request snapshot without extras", sourceType: RoutingUpstreamTypeSub2API, perRequest: true},
		{name: "sub2api declared per-request contract remains known", sourceType: RoutingUpstreamTypeSub2API, extras: sub2APIPerRequestExtras, perRequest: true, expectKnown: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pricing := RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "token", Currency: "USD",
				GroupRatio: &groupRatio, InputCostPerMillion: &inputRate,
				OutputCostPerMillion: &outputRate, Extras: test.extras,
			}
			if test.perRequest {
				pricing = RoutingNormalizedPricing{
					QuotaType: 1, BillingMode: "per_request", Currency: "USD",
					GroupRatio: &groupRatio, PerRequestCost: &perRequestRate, Extras: test.extras,
				}
			}
			version := RoutingCostSnapshotVersion{
				SourceType: test.sourceType,
				Confidence: RoutingCostConfidenceDerived, ConfidenceScore: 0.8,
				Freshness: RoutingCostFreshnessFresh, FreshnessScore: 1,
				ObservedTime: observed, EffectiveTime: observed, ExpiresTime: observed + 3_600,
			}

			estimate, err := EstimateRoutingCostSnapshot(version, pricing, profile, observed)

			require.NoError(t, err)
			assert.Equal(t, test.expectKnown, estimate.ExpectedKnown)
			assert.Equal(t, test.expectKnown, estimate.WorstCaseKnown)
			assert.Equal(t, test.expectKnown, estimate.Known)
			if test.expectKnown {
				assert.Positive(t, estimate.ExpectedCost)
			} else {
				assert.Zero(t, estimate.ExpectedCost)
				assert.Zero(t, estimate.WorstCaseCost)
			}
		})
	}
}

func TestRoutingCostEstimateUsesConservativeMaximumPromptOnlyForWorstCase(t *testing.T) {
	observed := common.GetTimestamp()
	inputRate := 2.0
	outputRate := 10.0
	estimate, err := EstimateRoutingCostSnapshot(
		RoutingCostSnapshotVersion{
			Confidence: RoutingCostConfidenceExact, ConfidenceScore: 1,
			Freshness: RoutingCostFreshnessFresh, FreshnessScore: 1,
			ObservedTime: observed, EffectiveTime: observed, ExpiresTime: observed + 3_600,
		},
		RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "token", Currency: "USD", Unit: "million_tokens",
			InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
		},
		RoutingCostRequestProfile{
			PromptTokens: 100, MaximumPromptTokens: 400,
			ExpectedCompletionTokens: 10, MaximumCompletionTokens: 20,
			MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
			MaximumCompletionKnown: true, CacheTokensKnown: true,
			MediaDimensionsKnown: true, RequestInputKnown: true,
		},
		observed,
	)

	require.NoError(t, err)
	assert.True(t, estimate.ExpectedKnown)
	assert.True(t, estimate.WorstCaseKnown)
	assert.InDelta(t, 0.0003, estimate.ExpectedCost, 1e-12)
	assert.InDelta(t, 0.001, estimate.WorstCaseCost, 1e-12)
	assert.InDelta(t, 0.0008, estimate.WorstCaseSingleBreakdown.Input, 1e-12)
}

func TestRoutingCostEstimateFailsClosedForUnknownRequestDimensions(t *testing.T) {
	observed := common.GetTimestamp()
	version := RoutingCostSnapshotVersion{
		Confidence: RoutingCostConfidenceExact, ConfidenceScore: 1,
		Freshness: RoutingCostFreshnessFresh, FreshnessScore: 1,
		ObservedTime: observed, EffectiveTime: observed, ExpiresTime: observed + 3600,
	}
	inputRate := 2.0
	outputRate := 10.0
	baseProfile := RoutingCostRequestProfile{
		PromptTokens: 1_000, ExpectedCompletionTokens: 500, MaximumCompletionTokens: 1_000,
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true, MediaDimensionsKnown: true,
		RequestInputKnown: true,
	}

	t.Run("remote input keeps expected proxy but closes worst", func(t *testing.T) {
		profile := baseProfile
		profile.InputTokensKnown = false
		estimate, err := EstimateRoutingCostSnapshot(version, RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "token", Currency: "USD",
			InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
		}, profile, observed)
		require.NoError(t, err)
		assert.True(t, estimate.ExpectedKnown)
		assert.False(t, estimate.WorstCaseKnown)
		assert.InDelta(t, 0.6, estimate.ConfidenceScore, 1e-12)
	})

	t.Run("inherited subtype prices do not require separate quantities", func(t *testing.T) {
		profile := baseProfile
		profile.CacheTokensKnown = false
		profile.MediaDimensionsKnown = false
		estimate, err := EstimateRoutingCostSnapshot(version, RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "token", Currency: "USD",
			InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
		}, profile, observed)
		require.NoError(t, err)
		assert.True(t, estimate.ExpectedKnown)
		assert.True(t, estimate.WorstCaseKnown)
	})

	t.Run("explicit free cache price requires cache quantity", func(t *testing.T) {
		profile := baseProfile
		profile.CacheTokensKnown = false
		freeCacheRate := 0.0
		estimate, err := EstimateRoutingCostSnapshot(version, RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "token", Currency: "USD",
			InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
			CacheReadCostPerMillion: &freeCacheRate,
		}, profile, observed)
		require.NoError(t, err)
		assert.False(t, estimate.ExpectedKnown)
		assert.False(t, estimate.WorstCaseKnown)
	})

	t.Run("known cache write zero keeps write-only pricing estimable", func(t *testing.T) {
		profile := baseProfile
		profile.CacheTokensKnown = false
		profile.CacheReadTokensKnown = false
		profile.CacheWriteTokensKnown = true
		cacheWriteRate := 2.5
		estimate, err := EstimateRoutingCostSnapshot(version, RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "token", Currency: "USD",
			InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
			CacheWriteCostPerMillion: &cacheWriteRate,
		}, profile, observed)
		require.NoError(t, err)
		assert.True(t, estimate.ExpectedKnown)
		assert.True(t, estimate.WorstCaseKnown)
	})

	t.Run("unknown automatic cache read still fails closed", func(t *testing.T) {
		profile := baseProfile
		profile.CacheTokensKnown = false
		profile.CacheReadTokensKnown = false
		profile.CacheWriteTokensKnown = true
		cacheReadRate := 0.2
		estimate, err := EstimateRoutingCostSnapshot(version, RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "token", Currency: "USD",
			InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
			CacheReadCostPerMillion: &cacheReadRate,
		}, profile, observed)
		require.NoError(t, err)
		assert.False(t, estimate.ExpectedKnown)
		assert.False(t, estimate.WorstCaseKnown)
	})

	t.Run("legacy combined cache knowledge remains compatible", func(t *testing.T) {
		profile := baseProfile
		profile.CacheReadTokensKnown = false
		profile.CacheWriteTokensKnown = false
		cacheReadRate := 0.2
		cacheWriteRate := 2.5
		estimate, err := EstimateRoutingCostSnapshot(version, RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "token", Currency: "USD",
			InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
			CacheReadCostPerMillion: &cacheReadRate, CacheWriteCostPerMillion: &cacheWriteRate,
		}, profile, observed)
		require.NoError(t, err)
		assert.True(t, estimate.ExpectedKnown)
		assert.True(t, estimate.WorstCaseKnown)
	})

	t.Run("cache expression dependencies are split by direction", func(t *testing.T) {
		profile := baseProfile
		profile.CacheTokensKnown = false
		profile.CacheReadTokensKnown = false
		profile.CacheWriteTokensKnown = true
		writeEstimate, err := EstimateRoutingCostSnapshot(version, RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "tiered_expr", Currency: "USD",
			BillingExpression: `tier("write", p * 2 + cc * 2.5 + cc1h * 4)`,
		}, profile, observed)
		require.NoError(t, err)
		assert.True(t, writeEstimate.ExpectedKnown)
		assert.True(t, writeEstimate.WorstCaseKnown)

		readEstimate, err := EstimateRoutingCostSnapshot(version, RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "tiered_expr", Currency: "USD",
			BillingExpression: `tier("read", p * 2 + cr * 0.2)`,
		}, profile, observed)
		require.NoError(t, err)
		assert.False(t, readEstimate.ExpectedKnown)
		assert.False(t, readEstimate.WorstCaseKnown)
	})

	tests := []struct {
		name    string
		pricing RoutingNormalizedPricing
		mutate  func(*RoutingCostRequestProfile)
	}{
		{
			name: "cache", pricing: RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "tiered_expr", Currency: "USD",
				BillingExpression: `tier("cache", p * 2 + c * 10 + cr * 0.2)`,
			}, mutate: func(profile *RoutingCostRequestProfile) { profile.CacheTokensKnown = false },
		},
		{
			name: "media", pricing: RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "token", Currency: "USD", ImageInputCostPerMillion: &inputRate,
			}, mutate: func(profile *RoutingCostRequestProfile) { profile.MediaDimensionsKnown = false },
		},
		{
			name: "request expression", pricing: RoutingNormalizedPricing{
				QuotaType: 1, BillingMode: "tiered_expr", Currency: "USD",
				BillingExpression: `header("x-priority") == "fast" ? tier("fast", 2) : tier("base", 1)`,
			}, mutate: func(profile *RoutingCostRequestProfile) { profile.RequestInputKnown = false },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := baseProfile
			test.mutate(&profile)
			estimate, err := EstimateRoutingCostSnapshot(version, test.pricing, profile, observed)
			require.NoError(t, err)
			assert.False(t, estimate.ExpectedKnown)
			assert.False(t, estimate.WorstCaseKnown)
			assert.Zero(t, estimate.ExpectedCost)
			assert.Zero(t, estimate.WorstCaseCost)
		})
	}
}

func TestRoutingCostLatestMaterializesVersionPricingAndMaskedAccount(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
	account := createRoutingUpstreamAccountForTest(t)
	write := routingCostVersionWriteForTest(account.ID, "gpt-materialized", 0.5)

	result, err := WriteRoutingCostSnapshotVersionContext(context.Background(), write)
	require.NoError(t, err)
	assert.Equal(t, result.Version.PricingHash, result.Latest.PricingHash)
	assert.Equal(t, write.UpstreamGroup, result.Latest.UpstreamGroup)
	assert.Equal(t, write.UpstreamModel, result.Latest.UpstreamModel)
	assert.Equal(t, account.AccountKey, result.Latest.AccountKeyHash)
	assert.Equal(t, account.MaskedIdentity, result.Latest.AccountMaskedID)
	assert.NotEmpty(t, result.Latest.PricingJSON)
	pricing, known, err := DecodeRoutingCostSnapshotPricing(result.Latest)
	require.NoError(t, err)
	assert.True(t, known)
	assert.Equal(t, "USD", pricing.Currency)
	assert.Equal(t, "mixed", pricing.Unit)
}

func TestRoutingCostV2ExternalDatabaseCompatibility(t *testing.T) {
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
			withRoutingTestDB(t, db, test.dbType)
			require.NoError(t, migrateRoutingCostV2ModelsForTest(db))
			account := createRoutingUpstreamAccountForTest(t)
			_, err := WriteRoutingCostSnapshotVersionContext(
				context.Background(),
				routingCostVersionWriteForTest(account.ID, "Model-X", 0.5),
			)
			require.NoError(t, err)
			_, err = WriteRoutingCostSnapshotVersionContext(
				context.Background(),
				routingCostVersionWriteForTest(account.ID, "model-x", 0.6),
			)
			require.NoError(t, err)
			var count int64
			require.NoError(t, DB.Model(&RoutingCostSnapshot{}).Count(&count).Error)
			assert.Equal(t, int64(2), count)
		})
	}
}

func migrateRoutingCostV2ModelsForTest(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&RoutingUpstreamAccount{},
		&RoutingCostSnapshotVersion{},
		&RoutingCostSnapshot{},
	); err != nil {
		return err
	}
	return migrateRoutingCostSnapshotModelKeys(db)
}

func createRoutingUpstreamAccountForTest(t *testing.T) RoutingUpstreamAccount {
	t.Helper()
	account, err := UpsertRoutingUpstreamAccountContext(context.Background(), RoutingUpstreamAccountSpec{
		SourceType:     RoutingUpstreamTypeNewAPI,
		StableIdentity: "account-for-" + t.Name(),
		MaskedIdentity: "account-***",
		Status:         RoutingUpstreamAccountStatusActive,
		LastSyncStatus: RoutingUpstreamSyncStatusSuccess,
	})
	require.NoError(t, err)
	return account
}

func routingCostVersionWriteForTest(accountID int, model string, inputCost float64) RoutingCostSnapshotVersionWrite {
	now := common.GetTimestamp()
	return RoutingCostSnapshotVersionWrite{
		AccountID:        accountID,
		ChannelID:        501,
		UpstreamGroup:    "VIP",
		UpstreamModel:    model + "-upstream",
		LocalModel:       model,
		ObservedTime:     now,
		EffectiveTime:    now - 60,
		ExpiresTime:      now + 3600,
		PricingVersion:   "pricing-v1",
		Confidence:       RoutingCostConfidenceExact,
		ConfidenceScore:  1,
		Freshness:        RoutingCostFreshnessFresh,
		FreshnessScore:   1,
		SourceSyncStatus: RoutingUpstreamSyncStatusSuccess,
		SourceSyncError:  "",
		Pricing: RoutingNormalizedPricing{
			QuotaType:            0,
			BillingMode:          "token",
			GroupRatio:           routingCostFloatForTest(1),
			BaseRatio:            routingCostFloatForTest(inputCost),
			CompletionRatio:      routingCostFloatForTest(2),
			InputCostPerMillion:  routingCostFloatForTest(inputCost),
			OutputCostPerMillion: routingCostFloatForTest(inputCost * 2),
		},
	}
}

func routingCostVersionSyncWriteForTest(
	binding RoutingChannelBinding,
	model string,
	inputCost float64,
) RoutingCostSnapshotVersionWrite {
	write := routingCostVersionWriteForTest(0, model, inputCost)
	write.ChannelID = binding.ChannelID
	write.UpstreamGroup = binding.UpstreamGroup
	return write
}

func routingCostFloatForTest(value float64) *float64 {
	return &value
}

type routingCostSnapshotBeforeModelKey struct {
	ID        int    `gorm:"primaryKey"`
	ChannelID int    `gorm:"uniqueIndex:idx_routing_cost_channel_model,priority:1"`
	ModelName string `gorm:"type:varchar(128);uniqueIndex:idx_routing_cost_channel_model,priority:2"`
}

func (routingCostSnapshotBeforeModelKey) TableName() string {
	return "routing_cost_snapshots"
}
