package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelRoutingV2ReadAPIsExposeSnapshotWithoutSecrets(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)

	priority := int64(7)
	weight := uint(13)
	baseURL := "https://user:password@provider.example/v1?api_key=secret"
	require.NoError(t, db.Create(&model.Channel{
		Id: 401, Name: "provider-a", Key: "serving-secret", Group: "vip", Models: "gpt-test",
		Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight, BaseURL: &baseURL,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelBinding{
		ChannelID: 401, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://cost.example", UpstreamGroup: "vip", Enabled: true,
	}).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)

	routinghotcache.SetMetricForTest(routinghotcache.Key{
		ChannelID: 401, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip",
	}, routinghotcache.MetricSnapshot{
		RequestCount: 20, SuccessCount: 19, ReliabilityRequestCount: 20, ReliabilityFailureCount: 1,
		P95TTFTMs: 250, OutputTokens: 1000, GenerationMs: 4000, TPS: 250, UpdatedUnix: common.GetTimestamp(),
	})
	routinghotcache.SetCostForTest(routinghotcache.CostKey{ChannelID: 401, Model: "gpt-test"}, routinghotcache.CostSnapshot{
		Known: true, Cost: 0.002, Confidence: model.RoutingCostConfidenceFull, UpdatedUnix: common.GetTimestamp(),
	})
	snapshot, err := channelrouting.RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	require.Len(t, snapshot.Pools, 1)

	overviewRecorder := httptest.NewRecorder()
	overviewContext, _ := gin.CreateTestContext(overviewRecorder)
	overviewContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/overview", nil)
	GetChannelRoutingOverview(overviewContext)
	assert.Equal(t, http.StatusOK, overviewRecorder.Code)
	assert.NotContains(t, overviewRecorder.Body.String(), "serving-secret")
	assert.NotContains(t, overviewRecorder.Body.String(), "password")
	assert.Contains(t, overviewRecorder.Body.String(), `"snapshot_available":true`)
	assert.Contains(t, overviewRecorder.Body.String(), `"observed_requests":20`)
	assert.Contains(t, overviewRecorder.Body.String(), `"identity_drops":0`)

	groupsRecorder := httptest.NewRecorder()
	groupsContext, _ := gin.CreateTestContext(groupsRecorder)
	groupsContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/groups?page=1&page_size=10", nil)
	ListChannelRoutingGroups(groupsContext)
	assert.Equal(t, http.StatusOK, groupsRecorder.Code)
	assert.Contains(t, groupsRecorder.Body.String(), `"group_name":"vip"`)
	assert.Contains(t, groupsRecorder.Body.String(), `"member_count":1`)

	groupRecorder := httptest.NewRecorder()
	groupContext, _ := gin.CreateTestContext(groupRecorder)
	groupContext.Params = gin.Params{{Key: "id", Value: strconv.Itoa(snapshot.Pools[0].ID)}}
	groupContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/groups/1", nil)
	GetChannelRoutingGroup(groupContext)
	assert.Equal(t, http.StatusOK, groupRecorder.Code)
	assert.Contains(t, groupRecorder.Body.String(), `"output_tokens_per_second":250`)

	channelsRecorder := httptest.NewRecorder()
	channelsContext, _ := gin.CreateTestContext(channelsRecorder)
	channelsContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/channels?search=provider", nil)
	ListChannelRoutingChannels(channelsContext)
	assert.Equal(t, http.StatusOK, channelsRecorder.Code)
	assert.Contains(t, channelsRecorder.Body.String(), `"endpoint":"https://provider.example"`)
	assert.NotContains(t, channelsRecorder.Body.String(), "api_key")

	costsRecorder := httptest.NewRecorder()
	costsContext, _ := gin.CreateTestContext(costsRecorder)
	costsContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/costs?known=true", nil)
	ListChannelRoutingCosts(costsContext)
	assert.Equal(t, http.StatusOK, costsRecorder.Code)
	assert.Contains(t, costsRecorder.Body.String(), `"confidence":"full"`)
	assert.Contains(t, costsRecorder.Body.String(), `"cost":0.002`)
}

func TestChannelRoutingDecisionAPIsUseBoundedCursorPagination(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)

	firstID, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID: "request-a", PoolID: 1, SnapshotRevision: 1, GroupName: "vip", ModelName: "gpt-test",
		ActualChannelID: 1, ObservedChannelID: 2,
		Candidates: []channelrouting.DecisionCandidate{{PoolMemberID: 2, ChannelID: 2, Eligible: true, Score: 0.9}},
	})
	require.NoError(t, err)
	secondID, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID: "request-b", PoolID: 1, SnapshotRevision: 1, GroupName: "vip", ModelName: "gpt-test",
		ActualChannelID: 2, ObservedChannelID: 2,
		Candidates: []channelrouting.DecisionCandidate{{PoolMemberID: 2, ChannelID: 2, Eligible: true, Score: 1}},
	})
	require.NoError(t, err)
	flushed, err := channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, flushed)

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/decisions?limit=1", nil)
	ListChannelRoutingDecisions(listContext)
	assert.Equal(t, http.StatusOK, listRecorder.Code)
	assert.Contains(t, listRecorder.Body.String(), secondID)
	assert.NotContains(t, listRecorder.Body.String(), firstID)
	assert.Contains(t, listRecorder.Body.String(), `"next_cursor":`)
	assert.NotContains(t, listRecorder.Body.String(), "candidates_json")

	detailRecorder := httptest.NewRecorder()
	detailContext, _ := gin.CreateTestContext(detailRecorder)
	detailContext.Params = gin.Params{{Key: "id", Value: firstID}}
	detailContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/decisions/"+firstID, nil)
	GetChannelRoutingDecision(detailContext)
	assert.Equal(t, http.StatusOK, detailRecorder.Code)
	assert.Contains(t, detailRecorder.Body.String(), firstID)
	assert.Contains(t, detailRecorder.Body.String(), `"channel_id":2`)
}

func TestChannelRoutingDecisionFiltersAreCaseExact(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	upperID, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID: "Request-X", PoolID: 1, SnapshotRevision: 1, GroupName: "VIP", ModelName: "Model-X",
	})
	require.NoError(t, err)
	lowerID, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID: "request-x", PoolID: 2, SnapshotRevision: 1, GroupName: "vip", ModelName: "model-x",
	})
	require.NoError(t, err)
	_, err = channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/decisions?group=VIP&model=Model-X&request_id=Request-X", nil)
	ListChannelRoutingDecisions(ctx)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), upperID)
	assert.NotContains(t, recorder.Body.String(), lowerID)
}

func TestChannelRoutingSnapshotAPIsReturnServiceUnavailableWhileInitializing(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	channelrouting.ResetSnapshotForTest()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/groups", nil)
	ListChannelRoutingGroups(ctx)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "initializing")
}

func TestChannelRoutingV2RejectsInvalidCursorsAndFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		target  string
		handler gin.HandlerFunc
	}{
		{name: "decision cursor", target: "/api/channel-routing/v2/decisions?cursor=not-a-number", handler: ListChannelRoutingDecisions},
		{name: "channel status", target: "/api/channel-routing/v2/channels?status=unknown", handler: ListChannelRoutingChannels},
		{name: "channel type", target: "/api/channel-routing/v2/channels?type=unknown", handler: ListChannelRoutingChannels},
		{name: "cost known", target: "/api/channel-routing/v2/costs?known=unknown", handler: ListChannelRoutingCosts},
		{name: "group search length", target: "/api/channel-routing/v2/groups?search=" + strings.Repeat("x", 257), handler: ListChannelRoutingGroups},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, test.target, nil)

			test.handler(ctx)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func openChannelRoutingControllerDB(t *testing.T) *gorm.DB {
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
		&model.RoutingDecisionAudit{},
		&model.RoutingMetricRollup{},
	))
	return db
}

func withChannelRoutingControllerState(t *testing.T, db *gorm.DB) {
	t.Helper()
	previousDB := model.DB
	previousMainType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousSecret := common.CryptoSecret
	previousMemoryCache := common.MemoryCacheEnabled
	model.DB = db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.CryptoSecret = "stable-channel-routing-controller-secret"
	common.MemoryCacheEnabled = true
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	routinghotcache.ResetForTest()
	routingmetrics.ResetForTest()
	channelrouting.ResetSnapshotForTest()
	channelrouting.ResetDecisionAuditsForTest()
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true, Mode: smart_routing_setting.ModeObserve, SnapshotStaleSec: 300,
	})
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		common.CryptoSecret = previousSecret
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		routingmetrics.ResetForTest()
		channelrouting.ResetSnapshotForTest()
		channelrouting.ResetDecisionAuditsForTest()
		smart_routing_setting.ResetForTest()
	})
}
