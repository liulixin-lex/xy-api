package channelrouting

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCostCatalogIsLayeredAndComparisonUsesOneRequestProfile(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	inputRate := 1.0
	freeCacheRead := 0.0
	pricingHash := strings.Repeat("a", 64)
	newPricing := func(multiplier float64) *model.RoutingNormalizedPricing {
		contract, err := model.NormalizeRoutingPricingContractV2(model.RoutingPricingContractV2{
			SchemaVersion:           model.RoutingPricingContractSchemaVersion,
			Mode:                    model.RoutingPricingContractModeDimensions,
			Currency:                "USD",
			InputCostPerMillion:     &inputRate,
			CacheReadCostPerMillion: &freeCacheRead,
		})
		require.NoError(t, err)
		pricing := contract.ToRoutingNormalizedPricing(SystemRoutingPricingBasis, multiplier)
		return &pricing
	}
	modelSnapshot := func(multiplier float64) ModelSnapshot {
		return ModelSnapshot{
			ModelName: "gpt-cost", UpstreamModelName: "gpt-cost-upstream",
			ChannelConfigurationRevision: 3, CostPricing: newPricing(multiplier),
			CostPricingHash: pricingHash, CostPricingIdentity: "billing:" + pricingHash + ":channel-config:3",
			CostUpstreamMultiplier: multiplier, CostObservedTime: 1_700_000_000,
			CostEffectiveTime: 1_700_000_000, CostExpiresTime: 1_800_000_000,
			CostVersionConfidence: model.RoutingCostConfidenceExact, CostConfidenceScore: 1,
			CostFreshness: model.RoutingCostFreshnessFresh, CostFreshnessScore: 1,
		}
	}
	SetSnapshotForTest(SnapshotView{
		Revision: 9, PolicyRevision: 9, TopologyEpoch: 4, PricingEpoch: 7,
		PricingHash: pricingHash, BuiltAtUnix: 1_700_000_000,
		Channels: []ChannelSnapshot{
			{ID: 101, RoutingIdentity: strings.Repeat("1", 32), RoutingGeneration: strings.Repeat("2", 32), Name: "provider-a"},
			{ID: 102, RoutingIdentity: strings.Repeat("3", 32), RoutingGeneration: strings.Repeat("4", 32), Name: "provider-b"},
		},
		Pools: []PoolSnapshot{{
			ID: 5, GroupName: "vip", DisplayName: "VIP",
			Members: []PoolMemberSnapshot{
				{ID: 11, PoolID: 5, ChannelID: 101, ChannelGeneration: strings.Repeat("2", 32), ChannelName: "provider-a", Models: []ModelSnapshot{modelSnapshot(1)}},
				{ID: 12, PoolID: 5, ChannelID: 102, ChannelGeneration: strings.Repeat("4", 32), ChannelName: "provider-b", Models: []ModelSnapshot{modelSnapshot(2)}},
			},
		}},
	})

	pools, total, metadata, ok := ListCostCatalogPoolSummaries("vip", 0, 20)
	require.True(t, ok)
	require.Len(t, pools, 1)
	assert.Equal(t, 1, total)
	assert.Equal(t, 2, pools[0].MemberCount)
	assert.Equal(t, 1, pools[0].ModelCount)
	assert.Equal(t, 2, pools[0].KnownContractCount)
	assert.Equal(t, uint64(7), metadata.PricingEpoch)
	assert.Equal(t, pricingHash, metadata.PricingHash)

	members, total, _, ok := ListCostCatalogMemberSummaries(5, "provider", 0, 20)
	require.True(t, ok)
	assert.Equal(t, 2, total)
	require.Len(t, members, 2)
	assert.Equal(t, strings.Repeat("1", 32), members[0].RoutingIdentity)

	models, total, _, ok := ListCostCatalogModelSummaries(5, 11, "gpt", 0, 20)
	require.True(t, ok)
	assert.Equal(t, 1, total)
	require.Len(t, models, 1)
	assert.Equal(t, []string{"input_tokens", "cache_read_tokens"}, models[0].ConfiguredDimensions)
	assert.Equal(t, []string{"cache_read_tokens"}, models[0].ExplicitFreeDimensions)
	assert.Equal(t, model.RoutingPricingContractModeDimensions, models[0].ContractMode)

	comparison, err := CompareRoutingCosts(RoutingCostComparisonRequest{
		PoolID: 5, ModelName: "gpt-cost", ProfileSource: "manual",
		QuantitySources: map[string]string{"input_tokens": "manual"},
		Profile: model.RoutingCostRequestProfile{
			PromptTokens: 1_000, MaximumPromptTokens: 1_000, MaxAttempts: 1,
			KnowledgeSpecified: true, InputTokensKnown: true,
			MaximumCompletionKnown: false, CacheReadTokensKnown: false,
			CacheWriteTokensKnown:       false,
			RequestPricingFeaturesKnown: true,
		},
		AtUnix: 1_700_000_001,
	})
	require.NoError(t, err)
	require.Len(t, comparison.Candidates, 2)
	assert.Equal(t, 11, comparison.Candidates[0].MemberID)
	assert.True(t, comparison.Candidates[0].Comparable)
	assert.InDelta(t, 0.001, comparison.Candidates[0].SingleAttempt.ExpectedCost, 1e-12)
	assert.InDelta(t, 0.001, comparison.Candidates[0].BeforeMultiplier.ExpectedCost, 1e-12)
	assert.InDelta(t, 0.002, comparison.Candidates[1].SingleAttempt.ExpectedCost, 1e-12)
	assert.InDelta(t, 0.001, comparison.Candidates[1].BeforeMultiplier.ExpectedCost, 1e-12)
	assert.Equal(t, "manual", comparison.QuantitySources["input_tokens"])
	assert.Equal(t, uint64(7), comparison.PricingEpoch)
}

func TestCostCatalogDoesNotBindReusedChannelIDAcrossGenerations(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	retiredGeneration := strings.Repeat("2", 32)
	SetSnapshotForTest(SnapshotView{
		Channels: []ChannelSnapshot{{
			ID: 101, RoutingIdentity: strings.Repeat("3", 32), RoutingGeneration: strings.Repeat("4", 32),
		}},
		Pools: []PoolSnapshot{{
			ID: 5, GroupName: "vip", Members: []PoolMemberSnapshot{{
				ID: 11, PoolID: 5, ChannelID: 101, ChannelGeneration: retiredGeneration,
				ChannelName: "retired-provider",
			}},
		}},
	})

	members, total, _, ok := ListCostCatalogMemberSummaries(5, "", 0, 20)
	require.True(t, ok)
	require.Len(t, members, 1)
	assert.Equal(t, 1, total)
	assert.Empty(t, members[0].RoutingIdentity)
	assert.Equal(t, retiredGeneration, members[0].RoutingGeneration)
}
