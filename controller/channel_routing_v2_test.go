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
	assert.Contains(t, overviewRecorder.Body.String(), `"control_plane_available":true`)
	assert.Contains(t, overviewRecorder.Body.String(), `"status":"complete"`)
	assert.Contains(t, overviewRecorder.Body.String(), `"observed_requests":20`)
	assert.Contains(t, overviewRecorder.Body.String(), `"identity_drops":0`)
	assert.Contains(t, overviewRecorder.Body.String(), `"p95_ttft_status":"available"`)
	assert.Contains(t, overviewRecorder.Body.String(), `"p95_ttft_ms":250`)
	assert.Contains(t, overviewRecorder.Body.String(), `"runtime_generation":1`)
	assert.Contains(t, overviewRecorder.Body.String(), `"policy_hash":"`)
	assert.Contains(t, overviewRecorder.Body.String(), `"node_epoch_id":"`)

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

func TestChannelRoutingOverviewExposesMetricTelemetryDegradation(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 1, PolicyHash: strings.Repeat("a", 64), BuiltAtUnix: common.GetTimestamp(),
		Stats: channelrouting.SnapshotStats{
			MetricTelemetryStatus:   "unavailable",
			MetricTelemetryReason:   "metric_rollup_scan_rows_limit",
			MetricRollupRows:        80,
			MetricRollupRowLimit:    100,
			MetricRollupScannedRows: 101,
			MetricRollupScanLimit:   100,
			MetricSketchBytes:       1024,
			MetricSketchByteLimit:   2048,
		},
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/overview", nil)
	GetChannelRoutingOverview(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, `"status":"unavailable"`)
	assert.Contains(t, body, `"reason":"metric_rollup_scan_rows_limit"`)
	assert.Contains(t, body, `"metric_rollup_scanned_rows":101`)
	assert.Contains(t, body, `"metric_rollup_scan_limit":100`)
	assert.Contains(t, body, `"p95_ttft_status":"unavailable"`)
	assert.NotContains(t, body, `"p95_ttft_status":"no_samples"`)
}

func TestChannelRoutingOverviewDoesNotReportConvergedWhenControlPlaneIsUnavailable(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 1, PolicyHash: strings.Repeat("a", 64), BuiltAtUnix: common.GetTimestamp(),
	})
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/overview", nil)
	GetChannelRoutingOverview(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"control_plane_available":false`)
	assert.Contains(t, recorder.Body.String(), `"propagation_status":"unknown"`)
	assert.NotContains(t, recorder.Body.String(), `"propagation_status":"converged"`)
}

func TestChannelRoutingOverviewReportsAheadAndHashConflict(t *testing.T) {
	tests := []struct {
		name             string
		headRevision     int64
		headHash         string
		snapshotRevision uint64
		snapshotHash     string
		expectedStatus   string
	}{
		{
			name: "snapshot ahead of restored database", headRevision: 2, headHash: strings.Repeat("a", 64),
			snapshotRevision: 3, snapshotHash: strings.Repeat("b", 64), expectedStatus: "ahead",
		},
		{
			name: "same revision hash conflict", headRevision: 3, headHash: strings.Repeat("a", 64),
			snapshotRevision: 3, snapshotHash: strings.Repeat("b", 64), expectedStatus: "conflict",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openChannelRoutingControllerDB(t)
			withChannelRoutingControllerState(t, db)
			now := common.GetTimestamp()
			require.NoError(t, db.Create(&model.RoutingPolicyHead{
				ID: 1, CurrentRevision: test.headRevision, CurrentHash: test.headHash,
				CurrentStage: model.RoutingDeploymentStageShadow, CreatedTime: now, UpdatedTime: now,
			}).Error)
			channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
				Revision: test.snapshotRevision, PolicyHash: test.snapshotHash, BuiltAtUnix: now,
			})

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/overview", nil)
			GetChannelRoutingOverview(ctx)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"propagation_status":"`+test.expectedStatus+`"`)
			assert.NotContains(t, recorder.Body.String(), `"propagation_status":"converged"`)
			if test.expectedStatus == "ahead" {
				assert.Contains(t, recorder.Body.String(), `"revision_ahead":1`)
			}
		})
	}
}

func TestChannelRoutingNodesExposePerNodeRevisionLagAndStatus(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	now := common.GetTimestamp()
	headHash := strings.Repeat("a", 64)
	require.NoError(t, db.Create(&model.RoutingPolicyHead{
		ID: 1, CurrentRevision: 3, CurrentHash: headHash,
		CurrentStage: model.RoutingDeploymentStageShadow, CreatedTime: now, UpdatedTime: now,
	}).Error)
	for index, spec := range []struct {
		nodeID     string
		revision   int64
		policyHash string
	}{
		{nodeID: "node-current-a", revision: 3, policyHash: headHash},
		{nodeID: "node-lagging", revision: 2, policyHash: headHash},
		{nodeID: "node-current-b", revision: 3, policyHash: headHash},
		{nodeID: "node-ahead", revision: 4, policyHash: headHash},
		{nodeID: "node-conflict", revision: 3, policyHash: strings.Repeat("b", 64)},
	} {
		checkpoint, err := model.NewRoutingRuntimeCheckpoint(
			spec.nodeID,
			channelrouting.RoutingConfigCheckpointKind,
			channelrouting.RoutingConfigCheckpointScope,
			spec.revision,
			1,
			map[string]any{"cursor": strconv.Itoa(index), "policy_hash": spec.policyHash},
			now-int64(index),
			now+600,
		)
		require.NoError(t, err)
		_, err = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), checkpoint)
		require.NoError(t, err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/nodes?limit=10", nil)
	ListChannelRoutingNodes(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, `"control_plane_available":true`)
	assert.Contains(t, body, `"control_plane_revision":3`)
	assert.Equal(t, 2, strings.Count(body, `"status":"converged"`))
	assert.Equal(t, 1, strings.Count(body, `"status":"lagging"`))
	assert.Equal(t, 1, strings.Count(body, `"status":"ahead"`))
	assert.Equal(t, 1, strings.Count(body, `"status":"conflict"`))
	assert.Contains(t, body, `"node_id":"node-lagging"`)
	assert.Contains(t, body, `"revision_lag":1`)
	assert.Contains(t, body, `"revision_ahead":1`)
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

func TestChannelRoutingDecisionAPIExposesAndFiltersCanaryMetadata(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	decisionID := enqueueControllerCanaryReplayAudit(t)
	flushed, err := channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	var audit model.RoutingDecisionAudit
	require.NoError(t, db.Where("decision_id = ?", decisionID).First(&audit).Error)

	target := "/api/channel-routing/v2/decisions?activation_id=401&cohort=canary&rollout_key=" + audit.RolloutKey
	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, target, nil)
	ListChannelRoutingDecisions(listContext)
	assert.Equal(t, http.StatusOK, listRecorder.Code)
	body := listRecorder.Body.String()
	assert.Contains(t, body, decisionID)
	assert.Contains(t, body, `"activation_id":401`)
	assert.Contains(t, body, `"cohort":"canary"`)
	assert.Contains(t, body, `"selected_identity":{"snapshot_revision":7,"pool_id":29,"member_id":11,"credential_id":1001,"channel_id":101}`)
	assert.Contains(t, body, `"capacity_admission":{"mode":"local_soft"`)
	assert.Contains(t, body, `"exclusion_summary":{"excluded_count":0,"reasons":[]}`)
	assert.NotContains(t, body, "exclusion_summary_json")

	detailRecorder := httptest.NewRecorder()
	detailContext, _ := gin.CreateTestContext(detailRecorder)
	detailContext.Params = gin.Params{{Key: "id", Value: decisionID}}
	detailContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/decisions/"+decisionID, nil)
	GetChannelRoutingDecision(detailContext)
	assert.Equal(t, http.StatusOK, detailRecorder.Code)
	assert.Contains(t, detailRecorder.Body.String(), `"rollout_key":"`+audit.RolloutKey+`"`)

	filteredRecorder := httptest.NewRecorder()
	filteredContext, _ := gin.CreateTestContext(filteredRecorder)
	filteredContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/decisions?cohort=control", nil)
	ListChannelRoutingDecisions(filteredContext)
	assert.Equal(t, http.StatusOK, filteredRecorder.Code)
	assert.NotContains(t, filteredRecorder.Body.String(), decisionID)
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

func TestChannelRoutingGroupDetailUsesBoundedNestedPagination(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	members := make([]channelrouting.PoolMemberSnapshot, 3)
	for index := range members {
		members[index] = channelrouting.PoolMemberSnapshot{
			ID: index + 1, PoolID: 1, ChannelID: 100 + index,
			CredentialIDs: []int{index + 1, index + 11},
			Models: []channelrouting.ModelSnapshot{
				{ModelName: "model-a"},
				{ModelName: "model-b"},
			},
		}
	}
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 1, PolicyHash: strings.Repeat("a", 64), BuiltAtUnix: common.GetTimestamp(),
		Pools: []channelrouting.PoolSnapshot{{
			ID: 1, GroupName: "default", DisplayName: "Default", MemberCount: len(members), Members: members,
		}},
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "1"}}
	ctx.Request = httptest.NewRequest(
		http.MethodGet,
		"/api/channel-routing/v2/groups/1?page=2&page_size=1&model_limit=1&credential_limit=1",
		nil,
	)
	GetChannelRoutingGroup(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, `"member_count":3`)
	assert.Contains(t, body, `"members_truncated":true`)
	assert.Contains(t, body, `"channel_id":101`)
	assert.NotContains(t, body, `"channel_id":100`)
	assert.Contains(t, body, `"model_count":2`)
	assert.Contains(t, body, `"models_truncated":true`)
	assert.Contains(t, body, `"credential_count":2`)
	assert.Contains(t, body, `"credentials_truncated":true`)
	assert.Contains(t, body, `"next_page":3`)
}

func TestChannelRoutingV2RejectsInvalidCursorsAndFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		target  string
		handler gin.HandlerFunc
		params  gin.Params
	}{
		{name: "decision cursor", target: "/api/channel-routing/v2/decisions?cursor=not-a-number", handler: ListChannelRoutingDecisions},
		{name: "node cursor", target: "/api/channel-routing/v2/nodes?cursor=not-a-cursor", handler: ListChannelRoutingNodes},
		{name: "group model limit", target: "/api/channel-routing/v2/groups/1?model_limit=101", handler: GetChannelRoutingGroup, params: gin.Params{{Key: "id", Value: "1"}}},
		{name: "channel status", target: "/api/channel-routing/v2/channels?status=unknown", handler: ListChannelRoutingChannels},
		{name: "channel type", target: "/api/channel-routing/v2/channels?type=unknown", handler: ListChannelRoutingChannels},
		{name: "cost known", target: "/api/channel-routing/v2/costs?known=unknown", handler: ListChannelRoutingCosts},
		{name: "probe cursor", target: "/api/channel-routing/v2/probes?cursor=-1", handler: ListChannelRoutingProbes},
		{name: "probe outcome", target: "/api/channel-routing/v2/probes?outcome=unknown", handler: ListChannelRoutingProbes},
		{name: "group search length", target: "/api/channel-routing/v2/groups?search=" + strings.Repeat("x", 257), handler: ListChannelRoutingGroups},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = test.params
			ctx.Request = httptest.NewRequest(http.MethodGet, test.target, nil)

			test.handler(ctx)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func TestListChannelRoutingProbesFiltersAndPaginates(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	for index, outcome := range []string{model.RoutingProbeOutcomeSuccess, model.RoutingProbeOutcomeFailure} {
		repeated := strings.Repeat(strconv.Itoa(index+1), 64)
		require.NoError(t, db.Create(&model.RoutingProbeResult{
			ProbeID: repeated, TargetKey: repeated, ProbeType: model.RoutingProbeTypeServing,
			SnapshotRevision: 1, PoolID: 10, MemberID: index + 1, ChannelID: 20 + index,
			GroupName: "default", ModelName: "gpt-test", EndpointHost: "api.example.test",
			BreakerState: model.RoutingBreakerStateHealthy, Outcome: outcome,
			StartedTimeMs: int64(index + 1), FinishedTimeMs: int64(index + 1),
			LeaseFencingToken: int64(index + 1), NodeEpochID: "node-test", CreatedTime: int64(index + 1),
		}).Error)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodGet,
		"/api/channel-routing/v2/probes?pool_id=10&outcome=failure&limit=1",
		nil,
	)
	ListChannelRoutingProbes(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, `"outcome":"failure"`)
	assert.NotContains(t, body, `"outcome":"success"`)
	assert.Contains(t, body, `"next_cursor":"2"`)
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
		&model.RoutingPolicyHead{},
		&model.RoutingPolicyRevision{},
		&model.RoutingPolicyPoolRevision{},
		&model.RoutingPolicyMemberRevision{},
		&model.RoutingPolicyActivation{},
		&model.RoutingPolicyDraft{},
		&model.RoutingConfigOutbox{},
		&model.RoutingRuntimeCheckpoint{},
		&model.RoutingChannelBinding{},
		&model.RoutingDecisionAudit{},
		&model.RoutingMetricRollup{},
		&model.RoutingTelemetryReceipt{},
		&model.RoutingUpstreamAccount{},
		&model.RoutingCostSnapshotVersion{},
		&model.RoutingProbeResult{},
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
