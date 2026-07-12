package model

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

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
