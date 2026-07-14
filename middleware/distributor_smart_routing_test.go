package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	routingselector "github.com/QuantumNous/new-api/service/routing"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSetupContextForSelectedChannelUsesOperationalMultiKeyStateOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:          true,
		Mode:             smart_routing_setting.ModeBalanced,
		SnapshotStaleSec: 300,
	})

	now := time.Now()
	for _, apiKeyIndex := range []int{model.RoutingMetricSingleKeyIndex, 1} {
		cacheKey := routinghotcache.Key{ChannelID: 9201, APIKeyIndex: apiKeyIndex, Model: "gpt-test", Group: "vip"}
		routinghotcache.SetBreakerForTest(cacheKey, routinghotcache.BreakerSnapshot{
			State:             routingselector.BreakerStateOpen,
			CooldownUntilUnix: now.Add(time.Minute).Unix(),
			UpdatedUnix:       now.Unix(),
		})
		routinghotcache.SetCapacityCooldownForTest(cacheKey, routinghotcache.CapacityCooldownSnapshot{
			SourceStatusCode:       http.StatusTooManyRequests,
			CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(),
			UpdatedUnixMilli:       now.UnixMilli(),
		})
	}
	breakerKey := routingbreaker.Key{ChannelID: 9201, APIKeyIndex: 1, Model: "gpt-test", Group: "vip"}
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key:       breakerKey,
		State:     routingbreaker.StateHalfOpen,
		UpdatedAt: now,
	}})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	channel := &model.Channel{
		Id:  9201,
		Key: "disabled-key\nenabled-key",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:         true,
			MultiKeyMode:       constant.MultiKeyModeRandom,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled, 1: common.ChannelStatusEnabled},
		},
	}

	err := SetupContextForSelectedChannel(ctx, channel, "gpt-test")

	require.Nil(t, err)
	assert.Equal(t, "enabled-key", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.True(t, common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey))
	assert.Equal(t, 1, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
	_, hasProbe := common.GetContextKey(ctx, constant.ContextKeyRoutingHalfOpenProbes)
	_, hasLease := common.GetContextKey(ctx, constant.ContextKeyRoutingHalfOpenLeases)
	assert.False(t, hasProbe)
	assert.False(t, hasLease)

	service.ReleaseRoutingHalfOpenProbe(ctx, channel.Id, "gpt-test", "vip")
	snapshot, acquired := routingbreaker.AcquireDefaultHalfOpenProbe(breakerKey, 1)
	require.True(t, acquired)
	assert.Equal(t, 1, snapshot.HalfOpenInflight)
	snapshot = routingbreaker.ReleaseDefaultHalfOpenProbe(breakerKey)
	assert.Zero(t, snapshot.HalfOpenInflight)
}

func TestSetupContextForSelectedChannelResetsSingleKeyMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "stale-key")
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 7)
	channel := &model.Channel{Id: 9202, Key: "single-key"}

	err := SetupContextForSelectedChannel(ctx, channel, "gpt-test")

	require.Nil(t, err)
	assert.Equal(t, "single-key", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey))
	assert.Equal(t, model.RoutingMetricSingleKeyIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
}

func TestSetupContextForSelectedChannelClearsRoutingIdentityWhenKeySelectionFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "stale-key")
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 7)
	common.SetContextKey(ctx, constant.ContextKeyRoutingSnapshotRevision, uint64(17))
	common.SetContextKey(ctx, constant.ContextKeyRoutingPoolID, 3)
	common.SetContextKey(ctx, constant.ContextKeyRoutingMemberID, 5)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCredentialID, 7)
	channel := &model.Channel{
		Id: 9204, Key: "disabled-key",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:         true,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled},
		},
	}

	err := SetupContextForSelectedChannel(ctx, channel, "gpt-test")

	require.NotNil(t, err)
	assert.Empty(t, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey))
	assert.Equal(t, model.RoutingMetricSingleKeyIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
	assert.Zero(t, routingSnapshotRevisionFromContext(t, ctx))
	assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingPoolID))
	assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingMemberID))
	assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCredentialID))
}

func TestSetupContextForSelectedChannelPublishesStableRoutingIdentity(t *testing.T) {
	db := openDistributorRoutingIdentityDB(t)
	withDistributorRoutingIdentityState(t, db)
	require.NoError(t, db.Create(&model.Channel{
		Id: 9203, Name: "identity-channel", Key: "stable-key", Group: "vip", Models: "gpt-test",
	}).Error)
	require.NoError(t, db.Create(&model.Channel{
		Id: 9205, Name: "keyless-channel", Group: "local", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	view, err := channelrouting.RefreshSnapshotContext(context.Background())
	require.NoError(t, err)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	errResult := SetupContextForSelectedChannel(ctx, &model.Channel{
		Id: 9203, Name: "identity-channel", Key: "stable-key",
	}, "gpt-test")

	require.Nil(t, errResult)
	assert.Equal(t, view.Revision, routingSnapshotRevisionFromContext(t, ctx))
	assert.Positive(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingPoolID))
	assert.Positive(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingMemberID))
	assert.Positive(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCredentialID))

	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "local")
	errResult = SetupContextForSelectedChannel(ctx, &model.Channel{
		Id: 9205, Name: "keyless-channel",
	}, "gpt-test")
	require.Nil(t, errResult)
	assert.Equal(t, view.Revision, routingSnapshotRevisionFromContext(t, ctx))
	assert.Positive(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingPoolID))
	assert.Positive(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingMemberID))
	assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCredentialID))

	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "auto")
	common.SetContextKey(ctx, constant.ContextKeyAutoGroup, "vip")
	errResult = SetupContextForSelectedChannel(ctx, &model.Channel{
		Id: 9203, Name: "identity-channel", Key: "stable-key",
	}, "gpt-test")
	require.Nil(t, errResult)
	assert.Equal(t, view.Revision, routingSnapshotRevisionFromContext(t, ctx))
	assert.Positive(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingPoolID))
	assert.Positive(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingMemberID))
	assert.Positive(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCredentialID))

	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{Enabled: false})
	errResult = SetupContextForSelectedChannel(ctx, &model.Channel{
		Id: 9203, Name: "identity-channel", Key: "stable-key",
	}, "gpt-test")
	require.Nil(t, errResult)
	assert.Zero(t, routingSnapshotRevisionFromContext(t, ctx))
	assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingPoolID))
	assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingMemberID))
	assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCredentialID))

	errResult = SetupContextForSelectedChannel(ctx, &model.Channel{
		Id: 9999, Name: "retry-channel", Key: "retry-key",
	}, "gpt-test")
	require.Nil(t, errResult)
	assert.Zero(t, routingSnapshotRevisionFromContext(t, ctx))
	assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingPoolID))
	assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingMemberID))
	assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCredentialID))
}

func TestSetupContextForSelectedChannelPreservesPinnedRoutingIdentity(t *testing.T) {
	db := openDistributorRoutingIdentityDB(t)
	withDistributorRoutingIdentityState(t, db)
	require.NoError(t, db.Create(&model.Channel{
		Id: 9210, Name: "pinned-identity-channel", Key: "stable-key", Group: "vip", Models: "gpt-test",
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	_, err = channelrouting.RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	currentIdentity, ok := channelrouting.ResolveIdentity("vip", 9210, "stable-key")
	require.True(t, ok)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	service.SetSelectedRoutingIdentity(ctx, service.SelectedRoutingIdentity{
		ChannelID:        9210,
		SnapshotRevision: currentIdentity.SnapshotRevision + 10,
		PoolID:           currentIdentity.PoolID,
		MemberID:         currentIdentity.MemberID,
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{Enabled: false})
	errResult := SetupContextForSelectedChannel(ctx, &model.Channel{
		Id: 9210, Name: "pinned-identity-channel", Key: "stable-key",
	}, "gpt-test")

	require.Nil(t, errResult)
	assert.Equal(t, currentIdentity.SnapshotRevision+10, routingSnapshotRevisionFromContext(t, ctx))
	assert.Equal(t, currentIdentity.PoolID, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingPoolID))
	assert.Equal(t, currentIdentity.MemberID, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingMemberID))
	assert.Equal(t, currentIdentity.CredentialID, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCredentialID))
}

func TestSetupContextForSelectedChannelLocksPlannedCredentialAcrossKeyReordering(t *testing.T) {
	db := openDistributorRoutingIdentityDB(t)
	withDistributorRoutingIdentityState(t, db)
	require.NoError(t, db.Create(&model.Channel{
		Id: 9211, Name: "multi-key-pinned-channel", Key: "key-a\nkey-b", Group: "vip", Models: "gpt-test",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled, 1: common.ChannelStatusEnabled},
		},
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	_, err = channelrouting.RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	identity, ok := channelrouting.ResolveIdentity("vip", 9211, "key-a")
	require.True(t, ok)
	require.Positive(t, identity.CredentialID)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
	service.SetSelectedRoutingIdentity(ctx, service.SelectedRoutingIdentity{
		ChannelID: 9211, SnapshotRevision: identity.SnapshotRevision,
		PoolID: identity.PoolID, MemberID: identity.MemberID, CredentialID: identity.CredentialID,
	})
	reordered := &model.Channel{
		Id: 9211, Name: "multi-key-pinned-channel", Key: "key-b\nkey-a", Status: common.ChannelStatusEnabled,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true, MultiKeySize: 2, MultiKeyPollingIndex: 1, MultiKeyMode: constant.MultiKeyModePolling,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled, 1: common.ChannelStatusEnabled},
		},
	}

	errResult := SetupContextForSelectedChannel(ctx, reordered, "gpt-test")
	require.Nil(t, errResult)
	assert.Equal(t, "key-a", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
	assert.Equal(t, 1, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
	assert.Equal(t, identity.CredentialID, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCredentialID))
	assert.Equal(t, 1, reordered.ChannelInfo.MultiKeyPollingIndex, "exact locking must not advance the production cursor")
}

func openDistributorRoutingIdentityDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Channel{},
		&model.RoutingTopologyMetadata{},
		&model.RoutingPool{},
		&model.RoutingPoolMember{},
		&model.RoutingCredentialRef{},
		&model.RoutingChannelBinding{},
		&model.RoutingMetricRollup{},
		&model.RoutingPolicyHead{},
		&model.RoutingPolicyRevision{},
		&model.RoutingPolicyPoolRevision{},
		&model.RoutingPolicyMemberRevision{},
		&model.RoutingPolicyActivation{},
		&model.RoutingConfigOutbox{},
	))
	return db
}

func withDistributorRoutingIdentityState(t *testing.T, db *gorm.DB) {
	t.Helper()
	previousDB := model.DB
	previousMainType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousSecret := common.CryptoSecret
	model.DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.CryptoSecret = "stable-distributor-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true, Mode: smart_routing_setting.ModeObserve,
	})
	channelrouting.ResetSnapshotForTest()
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		common.CryptoSecret = previousSecret
		smart_routing_setting.ResetForTest()
		channelrouting.ResetSnapshotForTest()
	})
}

func routingSnapshotRevisionFromContext(t *testing.T, ctx *gin.Context) uint64 {
	t.Helper()
	revision, ok := common.GetContextKeyType[uint64](ctx, constant.ContextKeyRoutingSnapshotRevision)
	require.True(t, ok)
	return revision
}

func TestSetRoutingPromptCostProxyCapturesStreamWithoutConsumingJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		body       string
		wantExists bool
		wantStream bool
	}{
		{name: "true", body: `{"model":"gpt-test","stream":true}`, wantExists: true, wantStream: true},
		{name: "false", body: `{"model":"gpt-test","stream":false}`, wantExists: true, wantStream: false},
		{name: "absent", body: `{"model":"gpt-test"}`, wantExists: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(test.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() {
				common.CleanupBodyStorage(ctx)
			})

			setRoutingPromptCostProxy(ctx)

			stream, exists := common.GetContextKey(ctx, constant.ContextKeyIsStream)
			assert.Equal(t, test.wantExists, exists)
			if test.wantExists {
				assert.Equal(t, test.wantStream, stream)
			}

			var replayed struct {
				Model  string `json:"model"`
				Stream *bool  `json:"stream"`
			}
			require.NoError(t, common.UnmarshalBodyReusable(ctx, &replayed))
			assert.Equal(t, "gpt-test", replayed.Model)
			if test.wantExists {
				require.NotNil(t, replayed.Stream)
				assert.Equal(t, test.wantStream, *replayed.Stream)
			} else {
				assert.Nil(t, replayed.Stream)
			}
		})
	}
}

func TestSetRoutingPromptCostProxySeparatesCostAndCapacityEstimates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"max_tokens":128}`
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	t.Cleanup(func() { common.CleanupBodyStorage(ctx) })

	setRoutingPromptCostProxy(ctx)

	promptProxy := max(len(body)/4, 1)
	assert.Equal(t, promptProxy, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingPromptProxy))
	assert.Equal(t, min(promptProxy+promptProxy/2, 512), common.GetContextKeyInt(ctx, constant.ContextKeyRoutingEstimatedOutput))
	assert.Equal(t, len(body), common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCapacityInput))
	assert.True(t, common.GetContextKeyBool(ctx, constant.ContextKeyRoutingCapacityInputKnown))
	assert.Equal(t, 128, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCapacityOutput))
	assert.True(t, common.GetContextKeyBool(ctx, constant.ContextKeyRoutingCapacityOutputKnown))
	profile, ok := common.GetContextKeyType[*model.RoutingCostRequestProfile](ctx, constant.ContextKeyRoutingCostProfile)
	require.True(t, ok)
	require.NotNil(t, profile)
	assert.Equal(t, int64(promptProxy), profile.PromptTokens)
	assert.Equal(t, int64(len(body)), profile.MaximumPromptTokens)
	inputRate := 1.0
	now := time.Now().Unix()
	cost, err := model.EstimateRoutingCostSnapshot(
		model.RoutingCostSnapshotVersion{
			Confidence: model.RoutingCostConfidenceExact, ConfidenceScore: 1,
			Freshness: model.RoutingCostFreshnessFresh, FreshnessScore: 1,
			ObservedTime: now, EffectiveTime: now, ExpiresTime: now + 3_600,
		},
		model.RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "token", Currency: "USD", Unit: "million_tokens",
			InputCostPerMillion: &inputRate,
		},
		*profile,
		now,
	)
	require.NoError(t, err)
	assert.True(t, cost.WorstCaseKnown)
	assert.InDelta(t, float64(len(body))/1_000_000, cost.WorstCaseSingleBreakdown.Input, 1e-12)
}

func TestMiddlewareCostProfileKeepsHedgeDependencyChecksFailClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	channelrouting.ResetSnapshotForTest()
	t.Cleanup(channelrouting.ResetSnapshotForTest)
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`
	now := time.Now().Unix()
	inputRate := 2.0
	outputRate := 10.0
	cacheRate := 0.2
	imageRate := 3.0
	tests := []struct {
		name    string
		pricing model.RoutingNormalizedPricing
		mutate  func(*model.RoutingCostRequestProfile)
		known   bool
	}{
		{
			name: "ordinary token pricing",
			pricing: model.RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "token", Currency: "USD", Unit: "million_tokens",
				InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
			},
			known: true,
		},
		{
			name: "unknown cache dependency",
			pricing: model.RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "token", Currency: "USD", Unit: "million_tokens",
				InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
				CacheReadCostPerMillion: &cacheRate,
			},
		},
		{
			name: "unknown media dependency",
			pricing: model.RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "token", Currency: "USD", Unit: "million_tokens",
				InputCostPerMillion: &inputRate, OutputCostPerMillion: &outputRate,
				ImageInputCostPerMillion: &imageRate,
			},
			mutate: func(profile *model.RoutingCostRequestProfile) {
				profile.MediaDimensionsKnown = false
			},
		},
		{
			name: "unknown request dependency",
			pricing: model.RoutingNormalizedPricing{
				QuotaType: 0, BillingMode: "tiered_expr", Currency: "USD", Unit: "expression",
				BillingExpression: `header("x-priority") == "fast" ? tier("fast", p * 4 + c * 20) : tier("base", p * 2 + c * 10)`,
			},
			mutate: func(profile *model.RoutingCostRequestProfile) {
				profile.RequestInputKnown = false
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			t.Cleanup(func() { common.CleanupBodyStorage(ctx) })
			setRoutingPromptCostProxy(ctx)
			common.SetContextKey(ctx, common.RequestIdKey, "middleware-hedge-cost-profile")
			common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
			profile, ok := common.GetContextKeyType[*model.RoutingCostRequestProfile](ctx, constant.ContextKeyRoutingCostProfile)
			require.True(t, ok)
			require.NotNil(t, profile)
			if test.mutate != nil {
				test.mutate(profile)
			}
			channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
				Revision: 1, PolicyHash: strings.Repeat("a", 64), ActivationID: 1,
				ActivationStage: model.RoutingDeploymentStageActive,
				Pools: []channelrouting.PoolSnapshot{{
					ID: 29, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageActive,
					Members: []channelrouting.PoolMemberSnapshot{{
						ID: 11, PoolID: 29, ChannelID: 101, PhysicalStatus: common.ChannelStatusEnabled,
						LegacyPriority: 10, LegacyWeight: 10, CredentialIDs: []int{1_001},
						Models: []channelrouting.ModelSnapshot{{
							ModelName: "gpt-test", CostPricing: &test.pricing,
							CostPricingHash: strings.Repeat("b", 64), CostPricingVersion: "middleware-v1",
							CostObservedTime: now, CostEffectiveTime: now, CostExpiresTime: now + 3_600,
							CostVersionConfidence: model.RoutingCostConfidenceExact, CostConfidenceScore: 1,
							CostFreshness: model.RoutingCostFreshnessFresh, CostFreshnessScore: 1,
							CostSourceSyncStatus:  model.RoutingUpstreamSyncStatusSuccess,
							CostAccountSourceType: model.RoutingUpstreamTypeNewAPI,
							CostAccountKeyHash:    strings.Repeat("c", 64),
						}},
					}},
				}},
				Channels: []channelrouting.ChannelSnapshot{{ID: 101, Status: common.ChannelStatusEnabled}},
			})

			estimate, known, err := service.ChannelRoutingHedgeCostEstimate(
				ctx, 101, "gpt-test", "/v1/chat/completions", 0,
			)

			require.NoError(t, err)
			assert.Equal(t, test.known, known)
			if test.known {
				assert.True(t, estimate.WorstCaseKnown)
				assert.InDelta(t, float64(len(body))*inputRate/1_000_000, estimate.WorstSingleBreakdown.Input, 1e-12)
				assert.Less(t, estimate.Cost, estimate.WorstCaseCost)
			}
		})
	}
}

func TestSetRoutingPromptCostProxyAltSSEAndRealtimeCapacitySemantics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("multipart media token dimensions are not applicable", func(t *testing.T) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", strings.NewReader("form-data"))
		ctx.Request.Header.Set("Content-Type", "multipart/form-data; boundary=test")

		setRoutingPromptCostProxy(ctx)

		inputState, inputExists := common.GetContextKey(ctx, constant.ContextKeyRoutingCapacityInputState)
		outputState, outputExists := common.GetContextKey(ctx, constant.ContextKeyRoutingCapacityOutputState)
		require.True(t, inputExists)
		require.True(t, outputExists)
		assert.Equal(t, channelrouting.CapacityDimensionNotApplicable, inputState)
		assert.Equal(t, channelrouting.CapacityDimensionNotApplicable, outputState)
		assert.True(t, common.GetContextKeyBool(ctx, constant.ContextKeyRoutingCapacityInputKnown))
		assert.True(t, common.GetContextKeyBool(ctx, constant.ContextKeyRoutingCapacityOutputKnown))
		assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCapacityInput))
		assert.Zero(t, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingCapacityOutput))
	})

	t.Run("gemini alt sse wins", func(t *testing.T) {
		body := `{"contents":[{"parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":32}}`
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-test:generateContent?alt=sse", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		t.Cleanup(func() { common.CleanupBodyStorage(ctx) })

		setRoutingPromptCostProxy(ctx)

		stream, exists := common.GetContextKey(ctx, constant.ContextKeyIsStream)
		require.True(t, exists)
		assert.Equal(t, true, stream)
	})

	t.Run("realtime is capacity unknown", func(t *testing.T) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime?model=gpt-realtime", nil)

		setRoutingPromptCostProxy(ctx)

		_, promptProxyExists := common.GetContextKey(ctx, constant.ContextKeyRoutingPromptProxy)
		assert.False(t, promptProxyExists)
		_, capacityInputExists := common.GetContextKey(ctx, constant.ContextKeyRoutingCapacityInput)
		assert.True(t, capacityInputExists)
		assert.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyRoutingCapacityInputKnown))
		assert.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyRoutingCapacityOutputKnown))
		inputState, _ := common.GetContextKey(ctx, constant.ContextKeyRoutingCapacityInputState)
		outputState, _ := common.GetContextKey(ctx, constant.ContextKeyRoutingCapacityOutputState)
		assert.Equal(t, channelrouting.CapacityDimensionApplicableUnknown, inputState)
		assert.Equal(t, channelrouting.CapacityDimensionApplicableUnknown, outputState)
		assert.True(t, common.GetContextKeyBool(ctx, constant.ContextKeyIsStream))
	})
}
