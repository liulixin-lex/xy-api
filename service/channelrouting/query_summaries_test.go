package channelrouting

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelAndCostSummaryQueriesKeepLargeDetailsOutOfLists(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	t.Setenv("ROUTING_REGION", "qa-east")

	credentialIDs := make([]int, ChannelSummaryCredentialLimit+5)
	for index := range credentialIDs {
		credentialIDs[index] = index + 1
	}
	modelNames := make([]string, ChannelSummaryModelLimit+5)
	models := make([]ModelSnapshot, len(modelNames))
	for index := range modelNames {
		modelNames[index] = fmt.Sprintf("model-%03d", index)
		models[index] = ModelSnapshot{ModelName: modelNames[index]}
	}
	pricingSentinel := "pricing-detail-must-not-appear-in-list"
	displayRate := 4.5
	models[0] = ModelSnapshot{
		ModelName: modelNames[0], ChannelConfigurationRevision: 7, Cost: 0.25,
		CostPricing: &model.RoutingNormalizedPricing{
			BillingMode: "expression", Currency: "USD", Unit: "request", BillingExpression: pricingSentinel,
			PerRequestCost: &displayRate,
		},
		CostPricingHash:        strings.Repeat("a", 64),
		CostPricingIdentity:    "billing:" + strings.Repeat("a", 64) + ":channel-config:7",
		CostUpstreamMultiplier: 1.5, CostConfidence: model.RoutingCostConfidenceExact,
		CostFreshness: model.RoutingCostFreshnessFresh,
	}
	SetSnapshotForTest(SnapshotView{
		Revision: 31, BuiltAtUnix: 1_700_000_000,
		Channels: []ChannelSnapshot{{
			ID: 101, Name: "provider-a", Status: common.ChannelStatusEnabled,
			Endpoint: "https://api.example.test", CredentialIDs: credentialIDs, ModelNames: modelNames,
			ConfigurationRevision: 7, UpstreamCostMultiplier: 1.5,
			CostSource: model.RoutingChannelCostSourceManual, CostConfirmed: true,
			CostBasisAvailable: true, EffectiveModelCount: len(modelNames),
			FailureDomainLabel:  "provider-a/account-main",
			FailureDomainStatus: model.RoutingFailureDomainStatusConfigured,
		}},
		Pools: []PoolSnapshot{{
			ID: 7, GroupName: "vip", Members: []PoolMemberSnapshot{{
				ID: 11, PoolID: 7, ChannelID: 101, ChannelName: "provider-a", Models: models,
			}},
		}},
	})

	channels, total, metadata, ok := ListChannelSnapshotSummaries("provider", nil, nil, 0, 10)
	require.True(t, ok)
	require.Len(t, channels, 1)
	assert.Equal(t, 1, total)
	assert.Equal(t, uint64(31), metadata.Revision)
	assert.Equal(t, "qa-east", channels[0].Region)
	assert.Equal(t, "https://api.example.test:443", channels[0].EndpointAuthority)
	assert.Equal(t, len(credentialIDs), channels[0].CredentialCount)
	assert.True(t, channels[0].CredentialsTruncated)
	assert.Len(t, channels[0].CredentialIDs, ChannelSummaryCredentialLimit)
	assert.Equal(t, len(modelNames), channels[0].ModelCount)
	assert.True(t, channels[0].ModelsTruncated)
	assert.Len(t, channels[0].Models, ChannelSummaryModelLimit)
	assert.Equal(t, int64(7), channels[0].ConfigurationRevision)
	assert.True(t, channels[0].CostBasisAvailable)
	assert.Equal(t, len(modelNames), channels[0].EffectiveModelCount)
	assert.Equal(t, "provider-a/account-main", channels[0].FailureDomainLabel)

	costs, costTotal, _, ok := ListCostSnapshotSummaries("vip", modelNames[0], nil, 0, 10)
	require.True(t, ok)
	require.Len(t, costs, 1)
	assert.Equal(t, 1, costTotal)
	assert.True(t, costs[0].Known)
	assert.Equal(t, "expression", costs[0].BillingMode)
	require.NotNil(t, costs[0].DisplayRate)
	assert.Equal(t, displayRate, *costs[0].DisplayRate)
	assert.Equal(t, "per_request", costs[0].DisplayRateBasis)
	assert.True(t, costs[0].ExpressionPricing)
	assert.Equal(t, "billing:"+strings.Repeat("a", 64)+":channel-config:7", costs[0].PricingIdentity)
	assert.Equal(t, int64(7), costs[0].ConfigurationRevision)
	assert.Equal(t, 1.5, costs[0].UpstreamCostMultiplier)
	encoded, err := common.Marshal(costs[0])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), pricingSentinel)
	assert.NotContains(t, string(encoded), "source_sync_status")
	assert.NotContains(t, string(encoded), "account_source_type")

	detail, _, found := GetCostSnapshotDetail(7, 11, modelNames[0])
	require.True(t, found)
	require.NotNil(t, detail.Pricing)
	assert.Equal(t, pricingSentinel, detail.Pricing.BillingExpression)
	detail.Pricing.BillingExpression = "mutated"
	again, _, found := GetCostSnapshotDetail(7, 11, modelNames[0])
	require.True(t, found)
	require.NotNil(t, again.Pricing)
	assert.Equal(t, pricingSentinel, again.Pricing.BillingExpression)
}

func TestRiskPoolSummariesRankAcrossTheWholeSnapshot(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	SetSnapshotForTest(SnapshotView{
		Revision: 44,
		Pools: []PoolSnapshot{
			{ID: 1, GroupName: "degraded", Members: []PoolMemberSnapshot{{
				ID: 11, PhysicalStatus: common.ChannelStatusEnabled, TelemetryKnown: true,
				Models: []ModelSnapshot{{ModelName: "a", BreakerState: model.RoutingBreakerStateDegraded, CostKnown: true}},
			}}},
			{ID: 2, GroupName: "open", Members: []PoolMemberSnapshot{{
				ID: 22, PhysicalStatus: common.ChannelStatusEnabled, TelemetryKnown: true,
				Models: []ModelSnapshot{{ModelName: "b", BreakerState: model.RoutingBreakerStateOpen, CostKnown: true}},
			}}},
			{ID: 3, GroupName: "unknown", Members: []PoolMemberSnapshot{{
				ID: 33, PhysicalStatus: common.ChannelStatusEnabled,
				Models: []ModelSnapshot{{ModelName: "c"}},
			}}},
		},
	})

	items, metadata, ok := ListRiskPoolSnapshotSummaries(2)
	require.True(t, ok)
	require.Len(t, items, 2)
	assert.Equal(t, uint64(44), metadata.Revision)
	assert.Equal(t, "open", items[0].GroupName)
	assert.Equal(t, "degraded", items[1].GroupName)
}
