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
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelRoutingReadAPIsExposeSnapshotWithoutSecrets(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	previousModelPrices := ratio_setting.ModelPrice2JSONString()
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"gpt-test":0.002}`))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousModelPrices))
	})

	priority := int64(7)
	weight := uint(13)
	baseURL := "https://user:password@provider.example/v1?api_key=secret"
	channel := model.Channel{
		Id: 401, Name: "provider-a", Key: "serving-secret", Group: "vip", Models: "gpt-test",
		Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight, BaseURL: &baseURL,
	}
	require.NoError(t, db.Create(&channel).Error)
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	configuration, err := model.GetRoutingChannelConfigurationContext(context.Background(), 401)
	require.NoError(t, err)
	configurationMutation, err := model.UpdateRoutingChannelConfigurationContext(
		context.Background(),
		configuration,
		1.5,
		model.RoutingChannelTrafficClassAll,
		"",
		false,
		10,
	)
	require.NoError(t, err)

	routinghotcache.SetMetricForTest(routinghotcache.Key{
		ChannelID: 401, ChannelGeneration: channel.RoutingGeneration,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip",
	}, routinghotcache.MetricSnapshot{
		RequestCount: 20, SuccessCount: 19, ReliabilityRequestCount: 20, ReliabilityFailureCount: 1,
		P95TTFTMs: 250, OutputTokens: 1000, GenerationMs: 4000, TPS: 250, UpdatedUnix: common.GetTimestamp(),
	})
	snapshot, err := channelrouting.RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	require.Len(t, snapshot.Pools, 1)

	overviewRecorder := httptest.NewRecorder()
	overviewContext, _ := gin.CreateTestContext(overviewRecorder)
	overviewContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/overview", nil)
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
	groupsContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/groups?page=1&page_size=10", nil)
	ListChannelRoutingGroups(groupsContext)
	assert.Equal(t, http.StatusOK, groupsRecorder.Code)
	assert.Contains(t, groupsRecorder.Body.String(), `"group_name":"vip"`)
	assert.Contains(t, groupsRecorder.Body.String(), `"member_count":1`)

	groupRecorder := httptest.NewRecorder()
	groupContext, _ := gin.CreateTestContext(groupRecorder)
	groupContext.Params = gin.Params{{Key: "id", Value: strconv.Itoa(snapshot.Pools[0].ID)}}
	groupContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/groups/1", nil)
	GetChannelRoutingGroup(groupContext)
	assert.Equal(t, http.StatusOK, groupRecorder.Code)
	assert.Contains(t, groupRecorder.Body.String(), `"output_tokens_per_second":250`)

	channelsRecorder := httptest.NewRecorder()
	channelsContext, _ := gin.CreateTestContext(channelsRecorder)
	channelsContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/channels?search=provider", nil)
	ListChannelRoutingChannels(channelsContext)
	assert.Equal(t, http.StatusOK, channelsRecorder.Code)
	assert.Contains(t, channelsRecorder.Body.String(), `"endpoint":"https://provider.example"`)
	assert.Contains(t, channelsRecorder.Body.String(), `"region":"default"`)
	assert.Contains(t, channelsRecorder.Body.String(), `"models":["gpt-test"]`)
	assert.Contains(t, channelsRecorder.Body.String(), `"upstream_cost_multiplier":1.5`)
	assert.Contains(t, channelsRecorder.Body.String(), `"cost_source":"manual"`)
	assert.Contains(t, channelsRecorder.Body.String(), `"cost_confirmed":true`)
	assert.Contains(t, channelsRecorder.Body.String(), `"cost_basis_available":true`)
	assert.NotContains(t, channelsRecorder.Body.String(), "api_key")

	costsRecorder := httptest.NewRecorder()
	costsContext, _ := gin.CreateTestContext(costsRecorder)
	costsContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/costs?known=true", nil)
	ListChannelRoutingCosts(costsContext)
	require.Equal(t, http.StatusOK, costsRecorder.Code, costsRecorder.Body.String())
	var costsResponse struct {
		Success bool `json:"success"`
		Data    struct {
			Items []channelrouting.CostSnapshotSummary `json:"items"`
			Total int                                  `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(costsRecorder.Body.Bytes(), &costsResponse))
	assert.True(t, costsResponse.Success)
	assert.Equal(t, 1, costsResponse.Data.Total)
	require.Len(t, costsResponse.Data.Items, 1)
	cost := costsResponse.Data.Items[0]
	assert.True(t, cost.Known)
	assert.Equal(t, channelrouting.SystemRoutingPricingBasis, cost.BillingMode)
	assert.Equal(t, model.RoutingCostConfidenceExact, cost.Confidence)
	assert.Equal(t, "USD", cost.Currency)
	assert.Equal(t, "request", cost.Unit)
	require.NotNil(t, cost.DisplayRate)
	assert.InDelta(t, 0.002, *cost.DisplayRate, 1e-12)
	assert.Equal(t, "per_request", cost.DisplayRateBasis)
	assert.Equal(t, configurationMutation.Configuration.Revision, cost.ConfigurationRevision)
	assert.InDelta(t, 1.5, cost.UpstreamCostMultiplier, 1e-12)
	assert.InDelta(t, 0.003, *cost.DisplayRate*cost.UpstreamCostMultiplier, 1e-12)
	assert.Contains(t, cost.PricingIdentity, ":channel-config:"+strconv.FormatInt(cost.ConfigurationRevision, 10))
	assert.Empty(t, cost.UnknownReason)
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
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/overview", nil)
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
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/overview", nil)
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
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/overview", nil)
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

func TestChannelRoutingOverviewKeepsPendingAttemptMetricsAvailableWithPartialCoverage(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	reservation, err := channelrouting.ReserveUpstreamAttemptAudit(channelRoutingOverviewAttemptSpec("pending"))
	require.NoError(t, err)
	require.NotNil(t, reservation)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/overview", nil)
	GetChannelRoutingOverview(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, `"attempt_metrics_available":true`)
	assert.Contains(t, body, `"attempt_metrics_degraded":false`)
	assert.Contains(t, body, `"attempt_metrics_coverage":0`)
	assert.Contains(t, body, `"entries":1`)
}

func TestChannelRoutingOverviewMarksRejectedAttemptMetricsUnavailable(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	channelrouting.ResetHedgeAttemptAuditsForTest(1)
	_, err := channelrouting.ReserveUpstreamAttemptAudit(channelRoutingOverviewAttemptSpec("accepted"))
	require.NoError(t, err)
	_, err = channelrouting.ReserveUpstreamAttemptAudit(channelRoutingOverviewAttemptSpec("rejected"))
	require.ErrorIs(t, err, channelrouting.ErrHedgeAuditBufferFull)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/overview", nil)
	GetChannelRoutingOverview(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, `"attempt_metrics_available":false`)
	assert.Contains(t, body, `"attempt_metrics_degraded":false`)
	assert.Contains(t, body, `"rejected":1`)
}

func TestChannelRoutingAttemptMetricsPipelineHealthSeparatesLagFailureAndLoss(t *testing.T) {
	const metricsFromMs = int64(1_000)
	tests := []struct {
		name          string
		stats         channelrouting.HedgeAttemptAuditStats
		wantAvailable bool
		wantDegraded  bool
		wantCoverage  float64
	}{
		{
			name:          "pending only is healthy lag",
			stats:         channelrouting.HedgeAttemptAuditStats{Persisted: 9, Entries: 1},
			wantAvailable: true,
			wantCoverage:  0.9,
		},
		{
			name: "pending persistence failure is degraded",
			stats: channelrouting.HedgeAttemptAuditStats{
				Persisted: 8, Entries: 2, PersistFailures: 1, ConsecutivePersistFailures: 1,
			},
			wantAvailable: true,
			wantDegraded:  true, wantCoverage: 0.8,
		},
		{
			name: "historical failure with pending work has recovered",
			stats: channelrouting.HedgeAttemptAuditStats{
				Persisted: 8, Entries: 2, PersistFailures: 1,
			},
			wantAvailable: true,
			wantCoverage:  0.8,
		},
		{
			name: "rejection inside metrics window is unavailable",
			stats: channelrouting.HedgeAttemptAuditStats{
				Persisted: 9, Rejected: 1, LastRejectedMs: metricsFromMs,
			},
			wantCoverage: 0.9,
		},
		{
			name: "rejection before metrics window recovers availability",
			stats: channelrouting.HedgeAttemptAuditStats{
				Persisted: 9, Rejected: 1, LastRejectedMs: metricsFromMs - 1,
			},
			wantAvailable: true,
			wantCoverage:  0.9,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			available, degraded, coverage := channelRoutingAttemptMetricsPipelineHealth(test.stats, metricsFromMs)
			assert.Equal(t, test.wantAvailable, available)
			assert.Equal(t, test.wantDegraded, degraded)
			assert.InDelta(t, test.wantCoverage, coverage, 1e-12)
		})
	}
}

func channelRoutingOverviewAttemptSpec(requestID string) model.RoutingHedgeAttemptStartSpec {
	return model.RoutingHedgeAttemptStartSpec{
		RequestID: requestID, NodeEpochID: "0123456789abcdef0123456789abcdef",
		PolicyRevision: 1, AlgorithmVersion: channelrouting.DecisionAlgorithmBalanced,
		PoolID: 1, MemberID: 1, ChannelID: 1, CredentialID: 1, ModelName: "gpt-test",
		ExecutionMode: model.RoutingAttemptExecutionSerial, Role: model.RoutingAttemptRoleSerial,
		EndpointAuthority: "https://api.example.test", Region: "default", StartedTimeMs: 1_000,
	}
}

func TestChannelRoutingDeploymentStageRespectsEffectiveModeCeiling(t *testing.T) {
	tests := []struct {
		name        string
		mode        smart_routing_setting.EffectiveMode
		policyStage string
		want        string
	}{
		{name: "disabled cannot be promoted", mode: smart_routing_setting.EffectiveModeLegacy, policyStage: model.RoutingDeploymentStageActive, want: model.RoutingDeploymentStageObserve},
		{name: "observe cannot be promoted", mode: smart_routing_setting.EffectiveModeObserve, policyStage: model.RoutingDeploymentStageCanary, want: model.RoutingDeploymentStageObserve},
		{name: "shadow cannot be promoted", mode: smart_routing_setting.EffectiveModeShadow, policyStage: model.RoutingDeploymentStageActive, want: model.RoutingDeploymentStageShadow},
		{name: "balanced accepts active", mode: smart_routing_setting.EffectiveModeBalanced, policyStage: model.RoutingDeploymentStageActive, want: model.RoutingDeploymentStageActive},
		{name: "balanced blocks canary", mode: smart_routing_setting.EffectiveModeBalanced, policyStage: model.RoutingDeploymentStageCanary, want: model.RoutingDeploymentStageShadow},
		{name: "enterprise accepts canary", mode: smart_routing_setting.EffectiveModeEnterpriseSLO, policyStage: model.RoutingDeploymentStageCanary, want: model.RoutingDeploymentStageCanary},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, channelRoutingDeploymentStage(tt.mode, tt.policyStage))
		})
	}
}

func TestChannelRoutingNodesExposePerNodeRevisionLagAndStatus(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	now := common.GetTimestamp()
	headHash := strings.Repeat("a", 64)
	configurationState, err := model.GetRoutingConfigurationEpochContext(context.Background())
	require.NoError(t, err)
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
			map[string]any{
				"cursor": strconv.Itoa(index), "policy_hash": spec.policyHash,
				"configuration_epoch": configurationState.Epoch, "configuration_hash": configurationState.StateHash,
			},
			now-int64(index),
			now+600,
		)
		require.NoError(t, err)
		_, err = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), checkpoint)
		require.NoError(t, err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/nodes?limit=10", nil)
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

func TestChannelRoutingNodesExposeConfigurationEpochLagAheadAndConflict(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	now := common.GetTimestamp()
	headHash := strings.Repeat("a", 64)
	configurationHash := strings.Repeat("c", 64)
	require.NoError(t, db.Create(&model.RoutingPolicyHead{
		ID: 1, CurrentRevision: 3, CurrentHash: headHash,
		CurrentStage: model.RoutingDeploymentStageShadow, CreatedTime: now, UpdatedTime: now,
	}).Error)
	require.NoError(t, db.Model(&model.RoutingConfigurationEpoch{}).Where("id = ?", 1).Updates(map[string]any{
		"epoch": 3, "state_hash": configurationHash, "updated_time": now,
	}).Error)

	tests := []struct {
		nodeID             string
		configurationEpoch int64
		configurationHash  string
		wantStatus         string
	}{
		{nodeID: "node-config-current", configurationEpoch: 3, configurationHash: configurationHash, wantStatus: "converged"},
		{nodeID: "node-config-lagging", configurationEpoch: 2, configurationHash: strings.Repeat("b", 64), wantStatus: "config_lagging"},
		{nodeID: "node-config-ahead", configurationEpoch: 4, configurationHash: strings.Repeat("d", 64), wantStatus: "config_ahead"},
		{nodeID: "node-config-conflict", configurationEpoch: 3, configurationHash: strings.Repeat("e", 64), wantStatus: "config_conflict"},
	}
	for index, test := range tests {
		checkpoint, err := model.NewRoutingRuntimeCheckpoint(
			test.nodeID,
			channelrouting.RoutingConfigCheckpointKind,
			channelrouting.RoutingConfigCheckpointScope,
			3,
			1,
			map[string]any{
				"cursor": strconv.Itoa(index), "policy_hash": headHash,
				"configuration_epoch": test.configurationEpoch, "configuration_hash": test.configurationHash,
			},
			now-int64(index),
			now+600,
		)
		require.NoError(t, err)
		_, err = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), checkpoint)
		require.NoError(t, err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/nodes?limit=10", nil)
	ListChannelRoutingNodes(ctx)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := recorder.Body.String()
	for _, test := range tests {
		assert.Contains(t, body, `"node_id":"`+test.nodeID+`"`)
		assert.Contains(t, body, `"status":"`+test.wantStatus+`"`)
	}
	assert.Contains(t, body, `"configuration_epoch_lag":1`)
	assert.Contains(t, body, `"configuration_epoch_ahead":1`)
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
	listContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/decisions?limit=1", nil)
	ListChannelRoutingDecisions(listContext)
	assert.Equal(t, http.StatusOK, listRecorder.Code)
	assert.Contains(t, listRecorder.Body.String(), secondID)
	assert.NotContains(t, listRecorder.Body.String(), firstID)
	assert.Contains(t, listRecorder.Body.String(), `"next_cursor":`)
	assert.NotContains(t, listRecorder.Body.String(), "candidates_json")
	assert.NotContains(t, listRecorder.Body.String(), `"candidate_set"`)
	assert.NotContains(t, listRecorder.Body.String(), `"actual_cost_estimate"`)

	detailRecorder := httptest.NewRecorder()
	detailContext, _ := gin.CreateTestContext(detailRecorder)
	detailContext.Params = gin.Params{{Key: "id", Value: firstID}}
	detailContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/decisions/"+firstID, nil)
	GetChannelRoutingDecision(detailContext)
	assert.Equal(t, http.StatusOK, detailRecorder.Code)
	assert.Contains(t, detailRecorder.Body.String(), firstID)
	assert.Contains(t, detailRecorder.Body.String(), `"channel_id":2`)
}

func TestChannelRoutingDecisionDetailAggregatesAttemptTimelineAcrossDecisionIDs(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	const (
		requestID       = "logical-request-timeline"
		primaryDecision = "decision-primary-hedge"
		retryDecision   = "decision-serial-retry"
	)
	require.NoError(t, db.Create(&model.RoutingDecisionAudit{
		DecisionID: primaryDecision, RequestID: requestID,
		RequestKey: model.RoutingDecisionRequestKey(requestID),
		PoolID:     1, GroupName: "vip", GroupKey: model.RoutingDecisionGroupKey("vip"),
		ModelName: "gpt-test", ModelKey: model.RoutingDecisionModelKey("gpt-test"),
		CandidatesJSON: `{"candidates":[]}`, CreatedTime: 1_000,
	}).Error)

	attempts := []struct {
		decisionID   string
		execution    string
		role         string
		attemptIndex int
		memberID     int
		channelID    int
		startedMs    int64
		completion   model.RoutingHedgeAttemptCompleteSpec
	}{
		{
			decisionID: primaryDecision, execution: model.RoutingAttemptExecutionHedge,
			role: model.RoutingHedgeAttemptRolePrimary, memberID: 11, channelID: 101, startedMs: 1_000,
			completion: model.RoutingHedgeAttemptCompleteSpec{
				Result: model.RoutingHedgeAttemptResultUpstreamError, HTTPStatus: 502,
				UpstreamSent: true, WillRetry: true, CompletedTimeMs: 1_050,
			},
		},
		{
			decisionID: primaryDecision, execution: model.RoutingAttemptExecutionHedge,
			role: model.RoutingHedgeAttemptRoleSecondary, memberID: 12, channelID: 102, startedMs: 1_010,
			completion: model.RoutingHedgeAttemptCompleteSpec{
				Result:       model.RoutingHedgeAttemptResultHedgeLost,
				UpstreamSent: true, CompletedTimeMs: 1_060,
			},
		},
		{
			decisionID: retryDecision, execution: model.RoutingAttemptExecutionSerial,
			role: model.RoutingAttemptRoleSerial, attemptIndex: 1, memberID: 13, channelID: 103, startedMs: 1_100,
			completion: model.RoutingHedgeAttemptCompleteSpec{
				Result: model.RoutingHedgeAttemptResultSuccess, Winner: true, HTTPStatus: 200,
				UpstreamSent: true, ClientCommitted: true, FinalAttempt: true, CompletedTimeMs: 1_150,
			},
		},
	}
	for _, attempt := range attempts {
		audit, err := model.StartRoutingHedgeAttemptAuditContext(context.Background(), model.RoutingHedgeAttemptStartSpec{
			DecisionID: attempt.decisionID, RequestID: requestID,
			NodeEpochID:    "0123456789abcdef0123456789abcdef",
			PolicyRevision: 1, AlgorithmVersion: channelrouting.DecisionAlgorithmBalanced,
			PoolID: 1, MemberID: attempt.memberID, ChannelID: attempt.channelID,
			CredentialID: attempt.channelID, ModelName: "gpt-test",
			ExecutionMode: attempt.execution, AttemptIndex: attempt.attemptIndex, Role: attempt.role,
			EndpointAuthority: "https://api.example.test", Region: "default", StartedTimeMs: attempt.startedMs,
		})
		require.NoError(t, err)
		_, err = model.CompleteRoutingHedgeAttemptAuditContext(context.Background(), audit.ID, attempt.completion)
		require.NoError(t, err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: primaryDecision}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/decisions/"+primaryDecision, nil)
	GetChannelRoutingDecision(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool                       `json:"success"`
		Data    channelRoutingDecisionView `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.NotNil(t, response.Data.AttemptTimeline)
	require.NotNil(t, response.Data.Hedge)
	assert.Equal(t, 3, response.Data.AttemptTimeline.AttemptCount)
	require.Len(t, response.Data.AttemptTimeline.Attempts, 3)
	assert.Equal(t, model.RoutingAttemptExecutionHedge, response.Data.AttemptTimeline.Attempts[0].ExecutionMode)
	assert.Equal(t, model.RoutingHedgeAttemptRolePrimary, response.Data.AttemptTimeline.Attempts[0].Role)
	assert.Equal(t, model.RoutingHedgeAttemptRoleSecondary, response.Data.AttemptTimeline.Attempts[1].Role)
	assert.Equal(t, model.RoutingAttemptExecutionSerial, response.Data.AttemptTimeline.Attempts[2].ExecutionMode)
	assert.Equal(t, 103, response.Data.AttemptTimeline.FinalChannelID)
	assert.Equal(t, model.RoutingHedgeAttemptResultSuccess, response.Data.AttemptTimeline.FinalResult)
	assert.Equal(t, response.Data.AttemptTimeline, response.Data.Hedge)
}

func TestChannelRoutingDecisionViewDecodesVersionedCostEstimate(t *testing.T) {
	estimate := channelrouting.ShadowCostInput{
		Known: true, Cost: 0.003, WorstCaseKnown: true, WorstCaseCost: 0.012,
		EffectiveKnown: true, EffectiveCost: 0.004, Currency: "USD", Unit: "mixed",
		PricingBasis: "token", PricingHash: strings.Repeat("a", 64), PricingVersion: "upstream-v3",
		VersionConfidence: model.RoutingCostConfidenceExact, Freshness: model.RoutingCostFreshnessFresh,
		ConfidenceScore: 0.95, FreshnessScore: 0.9,
		ExpectedBreakdown:    model.RoutingCostBreakdown{Input: 0.001, Output: 0.002, Total: 0.003},
		WorstSingleBreakdown: model.RoutingCostBreakdown{Input: 0.002, Output: 0.004, Total: 0.006},
		UpdatedUnix:          1_700_000_000,
	}
	encoded, err := common.Marshal(estimate)
	require.NoError(t, err)
	view := buildChannelRoutingDecisionView(model.RoutingDecisionAudit{
		DecisionID: "cost-estimate", CandidatesJSON: `{"candidates":[]}`,
		ActualCostEstimateJSON: string(encoded), ObservedCostEstimateJSON: string(encoded),
	})

	require.NotNil(t, view.ActualCostEstimate)
	require.NotNil(t, view.ObservedCostEstimate)
	assert.Equal(t, estimate.PricingHash, view.ActualCostEstimate.PricingHash)
	assert.Equal(t, estimate.WorstCaseCost, view.ActualCostEstimate.WorstCaseCost)
	assert.Equal(t, estimate.ExpectedBreakdown, view.ActualCostEstimate.ExpectedBreakdown)
	assert.Equal(t, estimate, *view.ObservedCostEstimate)
}

func TestChannelRoutingDecisionViewRestoresRedisBlockLeaseMarker(t *testing.T) {
	view := buildChannelRoutingDecisionView(model.RoutingDecisionAudit{
		DecisionID: "redis-block-view", CandidatesJSON: `{"candidates":[]}`,
		SnapshotRevision: 7, PoolID: 3, SelectedMemberID: 31,
		ReservationMode:                 model.RoutingDecisionReservationRedisBlock,
		ReservationResourceCredentialID: 91, ReservationResourceModel: "upstream-gpt",
		ReservationPoolSharesJSON: `[{"pool_id":3,"guaranteed_basis_points":10000,"maximum_basis_points":10000}]`,
		ReservationInflight:       1, ReservationLimitInflight: 10, ReservationLeaseExpiresMs: 5_000,
	})

	require.NotNil(t, view.CapacityAdmission)
	require.NotNil(t, view.CapacityAdmission.Strict)
	assert.Equal(t, channelrouting.CapacityModeRedisBlock, view.CapacityAdmission.Strict.Mode)
	assert.True(t, view.CapacityAdmission.Strict.BlockLease)
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
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/decisions?group=VIP&model=Model-X&request_id=Request-X", nil)
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

	target := "/api/channel-routing/decisions?activation_id=401&cohort=canary&rollout_key=" + audit.RolloutKey
	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, target, nil)
	ListChannelRoutingDecisions(listContext)
	assert.Equal(t, http.StatusOK, listRecorder.Code)
	body := listRecorder.Body.String()
	assert.Contains(t, body, decisionID)
	assert.Contains(t, body, `"activation_id":401`)
	assert.Contains(t, body, `"cohort":"canary"`)
	assert.Contains(t, body, `"selected_member_id":11`)
	assert.Contains(t, body, `"selected_credential_id":1001`)
	assert.NotContains(t, body, `"selected_identity"`)
	assert.NotContains(t, body, `"capacity_admission"`)
	assert.NotContains(t, body, `"exclusion_summary"`)
	assert.NotContains(t, body, "exclusion_summary_json")

	detailRecorder := httptest.NewRecorder()
	detailContext, _ := gin.CreateTestContext(detailRecorder)
	detailContext.Params = gin.Params{{Key: "id", Value: decisionID}}
	detailContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/decisions/"+decisionID, nil)
	GetChannelRoutingDecision(detailContext)
	assert.Equal(t, http.StatusOK, detailRecorder.Code)
	assert.Contains(t, detailRecorder.Body.String(), `"rollout_key":"`+audit.RolloutKey+`"`)
	assert.Contains(t, detailRecorder.Body.String(), `"selected_identity":{"snapshot_revision":7,"pool_id":29,"member_id":11,"credential_id":1001,"channel_id":101}`)
	assert.Contains(t, detailRecorder.Body.String(), `"capacity_admission":{"mode":"local_soft"`)
	assert.Contains(t, detailRecorder.Body.String(), `"exclusion_summary":{"excluded_count":0,"reasons":[]}`)

	filteredRecorder := httptest.NewRecorder()
	filteredContext, _ := gin.CreateTestContext(filteredRecorder)
	filteredContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/decisions?cohort=control", nil)
	ListChannelRoutingDecisions(filteredContext)
	assert.Equal(t, http.StatusOK, filteredRecorder.Code)
	assert.NotContains(t, filteredRecorder.Body.String(), decisionID)
}

func TestChannelRoutingDecisionHedgeTimelineIsDetailOnlyAndNamesFinalRoute(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	decision := model.RoutingDecisionAudit{
		DecisionID: "decision-hedge-detail", RequestID: "request-hedge-detail",
		PoolID: 9, GroupName: "default", ModelName: "gpt-test", ActualChannelID: 7,
		AlgorithmVersion: "balanced-v1", CreatedTime: 100,
	}
	require.NoError(t, db.Create(&decision).Error)
	require.NoError(t, db.Create(&[]model.RoutingHedgeAttemptAudit{
		{
			AttemptKey: strings.Repeat("e", 64), DecisionID: decision.DecisionID,
			Role: model.RoutingHedgeAttemptRolePrimary, State: model.RoutingHedgeAttemptStateCompleted,
			Result: model.RoutingHedgeAttemptResultHedgeLost, MemberID: 71, ChannelID: 7, Region: "default",
			StartedTimeMs: 100_000, CompletedTimeMs: 100_020,
		},
		{
			AttemptKey: strings.Repeat("f", 64), DecisionID: decision.DecisionID,
			Role: model.RoutingHedgeAttemptRoleSecondary, State: model.RoutingHedgeAttemptStateCompleted,
			Result: model.RoutingHedgeAttemptResultSuccess, Winner: true,
			MemberID: 72, ChannelID: 8, Region: "default", StartedTimeMs: 100_010, CompletedTimeMs: 100_030,
		},
	}).Error)

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/decisions", nil)
	ListChannelRoutingDecisions(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code)
	assert.NotContains(t, listRecorder.Body.String(), `"hedge"`)

	detailRecorder := httptest.NewRecorder()
	detailContext, _ := gin.CreateTestContext(detailRecorder)
	detailContext.Params = gin.Params{{Key: "id", Value: decision.DecisionID}}
	detailContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/decisions/"+decision.DecisionID, nil)
	GetChannelRoutingDecision(detailContext)
	require.Equal(t, http.StatusOK, detailRecorder.Code)
	body := detailRecorder.Body.String()
	assert.Contains(t, body, `"actual_channel_id":7`)
	assert.Contains(t, body, `"winner_role":"secondary"`)
	assert.Contains(t, body, `"final_member_id":72`)
	assert.Contains(t, body, `"final_channel_id":8`)
	assert.NotContains(t, body, "request_key")
	assert.NotContains(t, body, "credential_reference")
}

func TestChannelRoutingSnapshotAPIsReturnServiceUnavailableWhileInitializing(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	channelrouting.ResetSnapshotForTest()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/groups", nil)
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
		"/api/channel-routing/groups/1?page=2&page_size=1&model_limit=1&credential_limit=1",
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
	assert.Contains(t, body, `"nested_item_budget":2000`)
}

func TestChannelRoutingGroupDetailRejectsExcessiveNestedResponseBudget(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "1"}}
	ctx.Request = httptest.NewRequest(
		http.MethodGet,
		"/api/channel-routing/groups/1?page_size=100&model_limit=100&credential_limit=100",
		nil,
	)

	GetChannelRoutingGroup(ctx)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "nested item budget")
}

func TestChannelRoutingDecisionSummarySupportsTimeWindowDeepLinks(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.Create(&[]model.RoutingDecisionAudit{
		{DecisionID: "decision-before-window", GroupName: "default", ModelName: "gpt-test", CreatedTime: 100},
		{DecisionID: "decision-in-window", GroupName: "default", ModelName: "gpt-test", CreatedTime: 200},
		{DecisionID: "decision-after-window", GroupName: "default", ModelName: "gpt-test", CreatedTime: 300},
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodGet, "/api/channel-routing/decisions?from_time=150&to_time=250", nil,
	)
	ListChannelRoutingDecisions(ctx)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "decision-in-window")
	assert.NotContains(t, recorder.Body.String(), "decision-before-window")
	assert.NotContains(t, recorder.Body.String(), "decision-after-window")
}

func TestChannelRoutingReplayProfilesReturnLatestDecisionPerModelAndStream(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.Create(&[]model.RoutingDecisionAudit{
		{DecisionID: "profile-old", PoolID: 7, ModelName: "gpt-a", ModelKey: model.RoutingDecisionModelKey("gpt-a"), Replayable: true, CreatedTime: 100},
		{DecisionID: "profile-new", PoolID: 7, ModelName: "gpt-a", ModelKey: model.RoutingDecisionModelKey("gpt-a"), Replayable: true, CreatedTime: 200},
		{DecisionID: "profile-stream", PoolID: 7, ModelName: "gpt-a", ModelKey: model.RoutingDecisionModelKey("gpt-a"), IsStream: true, Replayable: true, CreatedTime: 300},
		{DecisionID: "profile-ignored", PoolID: 7, ModelName: "gpt-b", ModelKey: model.RoutingDecisionModelKey("gpt-b"), CreatedTime: 400},
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: "7"}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/groups/7/replay-profiles", nil)
	ListChannelRoutingGroupReplayProfiles(ctx)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := recorder.Body.String()
	assert.Contains(t, body, "profile-new")
	assert.Contains(t, body, "profile-stream")
	assert.NotContains(t, body, "profile-old")
	assert.NotContains(t, body, "profile-ignored")
}

func TestChannelRoutingRejectsInvalidCursorsAndFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		target  string
		handler gin.HandlerFunc
		params  gin.Params
	}{
		{name: "decision cursor", target: "/api/channel-routing/decisions?cursor=not-a-number", handler: ListChannelRoutingDecisions},
		{name: "decision time", target: "/api/channel-routing/decisions?from_time=200&to_time=100", handler: ListChannelRoutingDecisions},
		{name: "decision replayable", target: "/api/channel-routing/decisions?replayable=unknown", handler: ListChannelRoutingDecisions},
		{name: "node cursor", target: "/api/channel-routing/nodes?cursor=not-a-cursor", handler: ListChannelRoutingNodes},
		{name: "group model limit", target: "/api/channel-routing/groups/1?model_limit=101", handler: GetChannelRoutingGroup, params: gin.Params{{Key: "id", Value: "1"}}},
		{name: "channel status", target: "/api/channel-routing/channels?status=unknown", handler: ListChannelRoutingChannels},
		{name: "channel type", target: "/api/channel-routing/channels?type=unknown", handler: ListChannelRoutingChannels},
		{name: "cost known", target: "/api/channel-routing/costs?known=unknown", handler: ListChannelRoutingCosts},
		{name: "probe cursor", target: "/api/channel-routing/probes?cursor=-1", handler: ListChannelRoutingProbes},
		{name: "probe outcome", target: "/api/channel-routing/probes?outcome=unknown", handler: ListChannelRoutingProbes},
		{name: "group search length", target: "/api/channel-routing/groups?search=" + strings.Repeat("x", 257), handler: ListChannelRoutingGroups},
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
			EndpointAuthority: "https://api.example.test:443", Region: "default",
			BreakerScope: "member", EvidenceCount: 1, NodeCount: 1,
			BreakerState: model.RoutingBreakerStateHealthy, Outcome: outcome,
			StartedTimeMs: int64(index + 1), FinishedTimeMs: int64(index + 1),
			LeaseFencingToken: int64(index + 1), NodeEpochID: "node-test", CreatedTime: int64(index + 1),
		}).Error)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodGet,
		"/api/channel-routing/probes?pool_id=10&outcome=failure&limit=1",
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
		&channelRoutingPermissionUserForTest{},
		&model.CasbinRule{},
		&model.Option{},
		&model.Channel{},
		&model.RoutingChannelConfiguration{},
		&model.RoutingConfigurationEpoch{},
		&model.RoutingChannelConfigurationOutbox{},
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
		&model.RoutingPolicySimulationEvidence{},
		&model.RoutingPolicyRiskAcceptance{},
		&model.RoutingPolicyApproval{},
		&model.RoutingPolicyRollbackApproval{},
		&model.RoutingConfigOutbox{},
		&model.RoutingRuntimeCheckpoint{},
		&model.RoutingOperation{},
		&model.RoutingBreakerResetCommand{},
		&model.RoutingBreakerResetFence{},
		&model.RoutingBreakerResetTombstone{},
		&model.RoutingBreakerResetOutbox{},
		&model.RoutingBreakerState{},
		&model.RoutingEndpointEvidence{},
		&model.RoutingEndpointSharedState{},
		&model.RoutingAuditExport{},
		&model.RoutingAuditExportChunk{},
		&model.RoutingChannelBinding{},
		&model.RoutingDecisionAudit{},
		&model.RoutingHedgeAttemptAudit{},
		&model.RoutingMetricRollup{},
		&model.RoutingTelemetryReceipt{},
		&model.RoutingUpstreamAccount{},
		&model.RoutingRuntimeSettingsState{},
		&model.RoutingControlAudit{},
		&model.RoutingCostSnapshotVersion{},
		&model.RoutingProbeResult{},
		&model.SystemTask{},
		&model.SystemTaskLock{},
	))
	require.NoError(t, model.EnsureRoutingConfigurationEpoch(db))
	require.NoError(t, db.Create(&[]channelRoutingPermissionUserForTest{
		{Id: 10, Role: common.RoleAdminUser, Status: common.UserStatusEnabled},
		{Id: 11, Role: common.RoleAdminUser, Status: common.UserStatusEnabled},
	}).Error)
	require.NoError(t, db.Create(&[]model.CasbinRule{
		{Ptype: "p", V0: "user:10", V1: "channel_routing", V2: "deploy", V3: "allow"},
		{Ptype: "p", V0: "user:11", V1: "channel_routing", V2: "deploy", V3: "allow"},
	}).Error)
	return db
}

type channelRoutingPermissionUserForTest struct {
	Id        int `gorm:"primaryKey"`
	Role      int
	Status    int
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (channelRoutingPermissionUserForTest) TableName() string {
	return "users"
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
	channelrouting.ResetHedgeAttemptAuditsForTest()
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
		channelrouting.ResetHedgeAttemptAuditsForTest()
		smart_routing_setting.ResetForTest()
	})
}
