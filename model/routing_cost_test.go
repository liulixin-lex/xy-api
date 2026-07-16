package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingCostMigrationBackfillsCaseSensitiveModelKeys(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&routingCostSnapshotBeforeModelKey{}))
	require.NoError(t, db.Create(&[]routingCostSnapshotBeforeModelKey{
		{ChannelID: 900, ModelName: "Model-X"},
		{ChannelID: 901, ModelName: "model-x"},
	}).Error)
	require.NoError(t, db.AutoMigrate(&RoutingCostSnapshot{}))

	require.NoError(t, migrateRoutingCostSnapshotModelKeys(db))
	require.NoError(t, migrateRoutingCostSnapshotModelKeys(db))

	var rows []RoutingCostSnapshot
	require.NoError(t, db.Order("channel_id asc").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.NotNil(t, rows[0].ModelKey)
	require.NotNil(t, rows[1].ModelKey)
	assert.Equal(t, RoutingCostModelKey("Model-X"), *rows[0].ModelKey)
	assert.Equal(t, RoutingCostModelKey("model-x"), *rows[1].ModelKey)
	assert.NotEqual(t, *rows[0].ModelKey, *rows[1].ModelKey)
	assert.True(t, db.Migrator().HasIndex(&RoutingCostSnapshot{}, "idx_routing_cost_channel_model_key"))
}

func TestRoutingCostHistoryLoadsVerifiedPricingAndRemainsImmutable(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingUpstreamAccount{}, &RoutingCostSnapshotVersion{}))

	version, expectedPricing := createHistoricalRoutingCostVersionForTest(t, db, "gpt-history", 0.75)
	loaded, pricing, err := LoadRoutingCostSnapshotVersionContext(context.Background(), version.PricingHash)

	require.NoError(t, err)
	assert.Equal(t, version.ID, loaded.ID)
	require.NotNil(t, pricing.GroupRatio)
	assert.Equal(t, *expectedPricing.GroupRatio, *pricing.GroupRatio)
	assert.Equal(t, expectedPricing.InputCostPerMillion, pricing.InputCostPerMillion)

	assert.ErrorIs(t,
		db.Model(&version).Update("pricing_version", "tampered").Error,
		ErrRoutingCostHistoryImmutable,
	)
	assert.ErrorIs(t, db.Delete(&version).Error, ErrRoutingCostHistoryImmutable)

	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).
		Model(&RoutingCostSnapshotVersion{}).Where("id = ?", version.ID).
		Update("pricing_json", `{"billing_mode":"tampered"}`).Error)
	_, _, err = LoadRoutingCostSnapshotVersionContext(context.Background(), version.PricingHash)
	assert.ErrorIs(t, err, ErrRoutingCostVersionCorrupt)
}

func TestRoutingCostHistoryBackfillsLegacyContentHashOnRead(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingUpstreamAccount{}, &RoutingCostSnapshotVersion{}))

	version, _ := createHistoricalRoutingCostVersionForTest(t, db, "gpt-legacy-hash", 1)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).
		Model(&RoutingCostSnapshotVersion{}).Where("id = ?", version.ID).
		Update("content_hash", "").Error)

	loaded, _, err := LoadRoutingCostSnapshotVersionContext(context.Background(), version.PricingHash)
	require.NoError(t, err)
	assert.Len(t, loaded.ContentHash, 64)

	var stored RoutingCostSnapshotVersion
	require.NoError(t, db.Where("id = ?", version.ID).First(&stored).Error)
	assert.Equal(t, loaded.ContentHash, stored.ContentHash)
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
	assert.Equal(t, float64(300), profile.actualTokenParams.CR)
	assert.Equal(t, float64(50), profile.actualTokenParams.CC)
}

func TestRoutingCostEstimateDefinesExpectedWorstAndEffectiveCost(t *testing.T) {
	observed := common.GetTimestamp()
	groupRatio := 2.0
	pricing := RoutingNormalizedPricing{
		QuotaType: 0, BillingMode: "tiered_expr", Currency: "USD", GroupRatio: &groupRatio,
		BillingExpression: `tier("base", p * 2 + c * 10)`,
	}
	version := freshRoutingCostVersionForTest(observed, "")
	profile := RoutingCostRequestProfile{
		PromptTokens: 1_000, MaximumPromptTokens: 1_000,
		ExpectedCompletionTokens: 500, MaximumCompletionTokens: 1_000,
		MaxAttempts: 3, RetryProbability: 0.5, HedgeProbability: 0.25, HedgeAllowed: true,
	}

	estimate, err := EstimateRoutingCostSnapshot(version, pricing, profile, observed)
	require.NoError(t, err)
	assert.True(t, estimate.Known)
	assert.True(t, estimate.WorstCaseKnown)
	assert.InDelta(t, 0.014, estimate.ExpectedCost, 1e-12)
	assert.InDelta(t, 0.096, estimate.WorstCaseCost, 1e-12)
	assert.InDelta(t, 0.028, estimate.ExpectedEffectiveCost, 1e-12)

	expired, err := EstimateRoutingCostSnapshot(version, pricing, profile, version.ExpiresTime)
	require.NoError(t, err)
	assert.False(t, expired.Known)
	assert.Zero(t, expired.ExpectedCost)
}

func TestRoutingCostEstimateFailsClosedWithoutProviderContractMetadata(t *testing.T) {
	observed := common.GetTimestamp()
	inputRate := 2.0
	outputRate := 10.0
	groupRatio := 1.0
	baseProfile := RoutingCostRequestProfile{
		PromptTokens: 1_000, MaximumPromptTokens: 1_000,
		ExpectedCompletionTokens: 100, MaximumCompletionTokens: 100,
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		MediaDimensionsKnown: true, RequestInputKnown: true, RequestPricingFeaturesKnown: true,
		Request: billingexpr.RequestInput{Body: []byte(`{}`)},
	}
	newAPIContract, err := common.Marshal(map[string]any{
		"catalog_scope": RoutingCostCatalogScopeNewAPIPricing,
	})
	require.NoError(t, err)
	sub2APIContract, err := common.Marshal(map[string]any{
		"sub2api_contract": RoutingCostSub2APIDisplayContractV1,
		"platform":         "anthropic", "source_billing_mode": "token", "has_intervals": false,
	})
	require.NoError(t, err)
	tests := []struct {
		name       string
		sourceType string
		extras     []byte
		known      bool
	}{
		{name: "untyped legacy pricing remains readable", known: true},
		{name: "newapi history without current catalog contract", sourceType: RoutingUpstreamTypeNewAPI},
		{name: "newapi declared catalog", sourceType: RoutingUpstreamTypeNewAPI, extras: newAPIContract, known: true},
		{name: "sub2api history without display contract", sourceType: RoutingUpstreamTypeSub2API},
		{name: "sub2api declared display contract", sourceType: RoutingUpstreamTypeSub2API, extras: sub2APIContract, known: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pricing := RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "token", Currency: "USD", GroupRatio: &groupRatio,
				InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate, Extras: test.extras,
			}
			estimate, err := EstimateRoutingCostSnapshot(
				freshRoutingCostVersionForTest(observed, test.sourceType), pricing, baseProfile, observed,
			)
			require.NoError(t, err)
			assert.Equal(t, test.known, estimate.Known)
			assert.Equal(t, test.known, estimate.WorstCaseKnown)
			if !test.known {
				assert.Zero(t, estimate.ExpectedCost)
			}
		})
	}
}

func TestRoutingCostEstimatePreservesFreeMultiplierAndUnknownDimensions(t *testing.T) {
	observed := common.GetTimestamp()
	inputRate := 2.0
	outputRate := 10.0
	freeMultiplier := 0.0
	version := freshRoutingCostVersionForTest(observed, RoutingUpstreamTypeNewAPI)
	profile := RoutingCostRequestProfile{
		PromptTokens: 1_000, MaximumPromptTokens: 1_000,
		ExpectedCompletionTokens: 100, MaximumCompletionTokens: 100,
		MaxAttempts: 1, KnowledgeSpecified: true, InputTokensKnown: true,
		MaximumCompletionKnown: true, CacheTokensKnown: true,
		MediaDimensionsKnown: true, RequestInputKnown: true,
	}

	free, err := EstimateRoutingCostSnapshot(version, RoutingNormalizedPricing{
		QuotaType: 0, BillingMode: "token", Currency: "USD", GroupRatio: &freeMultiplier,
		InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
	}, profile, observed)
	require.NoError(t, err)
	assert.True(t, free.Known)
	assert.True(t, free.WorstCaseKnown)
	assert.Zero(t, free.ExpectedCost)

	profile.InputTokensKnown = false
	unknown, err := EstimateRoutingCostSnapshot(freshRoutingCostVersionForTest(observed, ""), RoutingNormalizedPricing{
		QuotaType: 0, BillingMode: "token", Currency: "USD",
		InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
	}, profile, observed)
	require.NoError(t, err)
	assert.True(t, unknown.ExpectedKnown)
	assert.False(t, unknown.WorstCaseKnown)
	assert.InDelta(t, 0.6, unknown.ConfidenceScore, 1e-12)
}

func TestRoutingCostEstimateTreatsUnmatchedSub2APIIntervalsAsUnknown(t *testing.T) {
	observed := common.GetTimestamp()
	pricing := RoutingNormalizedPricing{
		QuotaType: 0, BillingMode: "tiered_expr", Currency: "USD",
		BillingExpression: `len > 0 ? tier("priced", p * 3 + c * 15) : tier("` +
			RoutingCostSub2APIIntervalUnmatchedTier + `", 0)`,
	}
	version := freshRoutingCostVersionForTest(observed, "")

	unknown, err := EstimateRoutingCostSnapshot(version, pricing, RoutingCostRequestProfile{
		ExpectedCompletionTokens: 10, MaximumCompletionTokens: 10,
	}, observed)
	require.NoError(t, err)
	assert.False(t, unknown.Known)
	assert.Zero(t, unknown.ExpectedCost)

	known, err := EstimateRoutingCostSnapshot(version, pricing, RoutingCostRequestProfile{
		PromptTokens: 1, MaximumPromptTokens: 1,
		ExpectedCompletionTokens: 1, MaximumCompletionTokens: 1,
	}, observed)
	require.NoError(t, err)
	assert.True(t, known.Known)
	assert.Positive(t, known.ExpectedCost)
}

func createHistoricalRoutingCostVersionForTest(
	t *testing.T,
	db *gorm.DB,
	modelName string,
	groupRatio float64,
) (RoutingCostSnapshotVersion, RoutingNormalizedPricing) {
	t.Helper()
	now := common.GetTimestamp()
	account := RoutingUpstreamAccount{
		AccountKey: routingCostHash([]byte("history-account-" + modelName)),
		SourceType: RoutingUpstreamTypeNewAPI, MaskedIdentity: "retired",
		Status: RoutingUpstreamAccountStatusDisabled, LastSyncStatus: RoutingUpstreamSyncStatusUnknown,
		CreatedTime: now, UpdatedTime: now,
	}
	require.NoError(t, db.Create(&account).Error)
	inputRate := 2.0
	outputRate := 10.0
	manifest := routingCostSnapshotManifest{
		AccountID: account.ID, ChannelID: 700, UpstreamGroup: "legacy",
		UpstreamModel: modelName, LocalModel: modelName,
		ObservedTime: now, EffectiveTime: now, ExpiresTime: now + 3_600,
		PricingVersion: "history-v1", Confidence: RoutingCostConfidenceExact, ConfidenceScore: 1,
		Freshness: RoutingCostFreshnessFresh, FreshnessScore: 1,
		SourceSyncStatus: RoutingUpstreamSyncStatusSuccess,
		Pricing: RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "token", Currency: "USD", Unit: "million_tokens",
			GroupRatio: &groupRatio, InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
		},
	}
	normalizedPricing, pricingJSON, err := normalizeRoutingNormalizedPricing(manifest.Pricing)
	require.NoError(t, err)
	manifest.Pricing = normalizedPricing
	pricingHash, err := routingCostPricingHash(account, manifest, pricingJSON)
	require.NoError(t, err)
	contentHash, err := routingCostContentHash(account, manifest, pricingJSON)
	require.NoError(t, err)
	version := RoutingCostSnapshotVersion{
		SchemaVersion: routingCostSnapshotVersionSchema, PricingHash: pricingHash, ContentHash: contentHash,
		ApplyToken: "historical-read-fixture", AccountID: account.ID, AccountKey: account.AccountKey,
		SourceType: account.SourceType, ChannelID: manifest.ChannelID,
		UpstreamGroup: manifest.UpstreamGroup, UpstreamGroupKey: routingCostHash([]byte(manifest.UpstreamGroup)),
		UpstreamModel: manifest.UpstreamModel, UpstreamModelKey: RoutingCostModelKey(manifest.UpstreamModel),
		LocalModel: manifest.LocalModel, LocalModelKey: RoutingCostModelKey(manifest.LocalModel),
		ObservedTime: manifest.ObservedTime, EffectiveTime: manifest.EffectiveTime, ExpiresTime: manifest.ExpiresTime,
		PricingVersion: manifest.PricingVersion, PricingJSON: string(pricingJSON),
		Confidence: manifest.Confidence, ConfidenceScore: manifest.ConfidenceScore,
		Freshness: manifest.Freshness, FreshnessScore: manifest.FreshnessScore,
		SourceSyncStatus: manifest.SourceSyncStatus, SourceSyncError: manifest.SourceSyncError,
		CreatedTime: now,
	}
	require.NoError(t, db.Create(&version).Error)
	return version, normalizedPricing
}

func freshRoutingCostVersionForTest(observed int64, sourceType string) RoutingCostSnapshotVersion {
	return RoutingCostSnapshotVersion{
		SourceType: sourceType, Confidence: RoutingCostConfidenceExact, ConfidenceScore: 1,
		Freshness: RoutingCostFreshnessFresh, FreshnessScore: 1,
		ObservedTime: observed, EffectiveTime: observed, ExpiresTime: observed + 3_600,
	}
}

type routingCostSnapshotBeforeModelKey struct {
	ID        int    `gorm:"primaryKey"`
	ChannelID int    `gorm:"uniqueIndex:idx_routing_cost_channel_model,priority:1"`
	ModelName string `gorm:"type:varchar(128);uniqueIndex:idx_routing_cost_channel_model,priority:2"`
}

func (routingCostSnapshotBeforeModelKey) TableName() string {
	return "routing_cost_snapshots"
}
