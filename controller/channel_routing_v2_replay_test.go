package controller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"
	routingselector "github.com/QuantumNous/new-api/service/routing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplayChannelRoutingDecisionVerifiesAuditAndDistinguishesFailureModes(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)

	validID := enqueueControllerReplayAudit(t, 5, "controller-replay-valid")
	nonReplayableID, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID: "observe-request", PoolID: 5, GroupName: "group-5", ModelName: "gpt-test", SnapshotRevision: 7,
	})
	require.NoError(t, err)
	unsupportedID := enqueueControllerReplayAudit(t, 5, "controller-replay-unsupported")
	tamperedID := enqueueControllerReplayAudit(t, 5, "controller-replay-tampered")
	flushed, err := channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 4, flushed)

	require.NoError(t, db.Model(&model.RoutingDecisionAudit{}).
		Where("decision_id = ?", unsupportedID).Update("algorithm_version", "channel-routing-shadow-v99").Error)
	var tampered model.RoutingDecisionAudit
	require.NoError(t, db.Where("decision_id = ?", tamperedID).First(&tampered).Error)
	var replayInput channelrouting.ShadowReplayInput
	require.NoError(t, common.UnmarshalJsonStr(tampered.ReplayInputJSON, &replayInput))
	replayInput.Candidates[0].Cost.Cost = 0.125
	encoded, err := common.Marshal(replayInput)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.RoutingDecisionAudit{}).
		Where("decision_id = ?", tamperedID).Update("replay_input_json", string(encoded)).Error)

	valid := performControllerReplayRequest(validID)
	assert.Equal(t, http.StatusOK, valid.Code)
	assert.Contains(t, valid.Body.String(), `"audit_verified":true`)
	assert.Contains(t, valid.Body.String(), `"stored_channel_id":101`)
	assert.Contains(t, valid.Body.String(), `"replayed_channel_id":101`)
	assert.NotContains(t, valid.Body.String(), "replay_input_json")
	assert.NotContains(t, valid.Body.String(), "request_profile")
	assert.NotContains(t, valid.Body.String(), "/v1/chat/completions")

	tests := []struct {
		name   string
		id     string
		status int
		code   string
	}{
		{name: "not found", id: "missing-decision", status: http.StatusNotFound, code: "decision_not_found"},
		{name: "not replayable", id: nonReplayableID, status: http.StatusUnprocessableEntity, code: "decision_not_replayable"},
		{name: "unsupported algorithm", id: unsupportedID, status: http.StatusUnprocessableEntity, code: "replay_algorithm_unsupported"},
		{name: "tampered audit", id: tamperedID, status: http.StatusConflict, code: "replay_integrity_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := performControllerReplayRequest(test.id)
			assert.Equal(t, test.status, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"`+test.code+`"`)
		})
	}
}

func TestReplayChannelRoutingCanaryDecisionVerifiesGateAndMetadata(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	decisionID := enqueueControllerCanaryReplayAudit(t)
	flushed, err := channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)

	valid := performControllerReplayRequest(decisionID)
	assert.Equal(t, http.StatusOK, valid.Code)
	assert.Contains(t, valid.Body.String(), `"algorithm_version":"`+channelrouting.DecisionAlgorithmCanaryV1+`"`)
	assert.Contains(t, valid.Body.String(), `"gate_verified":true`)
	assert.Contains(t, valid.Body.String(), `"stored_channel_id":101`)

	require.NoError(t, db.Model(&model.RoutingDecisionAudit{}).
		Where("decision_id = ?", decisionID).Update("canary_bucket", 999).Error)
	tampered := performControllerReplayRequest(decisionID)
	assert.Equal(t, http.StatusConflict, tampered.Code)
	assert.Contains(t, tampered.Body.String(), `"code":"replay_integrity_failed"`)
}

func TestSimulateChannelRoutingGroupUsesStrictBoundedRequestSchema(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	for index := 0; index < 3; index++ {
		enqueueControllerReplayAudit(t, 5, "controller-simulation-"+strconv.Itoa(index))
	}
	_, err := channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)

	body := `{"limit":2,"selector":{"weight_availability":0,"weight_latency":0,"weight_throughput":0,"weight_cost":1}}`
	valid := performControllerSimulationRequest("5", body)
	assert.Equal(t, http.StatusOK, valid.Code)
	assert.Contains(t, valid.Body.String(), `"scanned_samples":2`)
	assert.Contains(t, valid.Body.String(), `"selection_changed_count":2`)
	assert.Contains(t, valid.Body.String(), `"cost_known_samples":2`)
	assert.Contains(t, valid.Body.String(), `"next_cursor":`)
	assert.NotContains(t, valid.Body.String(), "replay_input_json")
	assert.NotContains(t, valid.Body.String(), "request_profile")
	assert.NotContains(t, valid.Body.String(), "/v1/chat/completions")

	tests := []struct {
		name string
		id   string
		body string
	}{
		{name: "invalid pool", id: "nope", body: `{}`},
		{name: "limit exceeds bound", id: "5", body: `{"limit":101}`},
		{name: "unknown root field", id: "5", body: `{"window":10}`},
		{name: "unknown selector field", id: "5", body: `{"selector":{"random_seed":1}}`},
		{name: "null selector value", id: "5", body: `{"selector":{"weight_cost":null}}`},
		{name: "out of range weight", id: "5", body: `{"selector":{"weight_cost":1.1}}`},
		{name: "trailing json", id: "5", body: `{} {}`},
		{name: "empty body", id: "5", body: ``},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := performControllerSimulationRequest(test.id, test.body)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func enqueueControllerReplayAudit(t *testing.T, poolID int, requestID string) string {
	t.Helper()
	profile, err := channelrouting.NewRequestProfile(
		"/v1/chat/completions", "group-"+strconv.Itoa(poolID), "gpt-test", false, 0, 1_000, 200,
	)
	require.NoError(t, err)
	seed, err := channelrouting.DeriveShadowSeed(requestID, 7, profile.RetryIndex)
	require.NoError(t, err)
	input, err := channelrouting.BuildShadowReplayInput(poolID, 7, 3, strings.Repeat("a", 64), profile, routingselector.Settings{
		WeightAvailability: 1,
		WeightLatency:      0,
		WeightThroughput:   0,
		WeightCost:         0,
		AvailabilityFloor:  0,
		MinVolume:          1,
		TopK:               1,
		MaxEjectedPct:      50,
		HalfOpenProbes:     1,
		SnapshotStaleSec:   1_800,
		NowUnix:            1_000,
		NowUnixMilli:       1_000_000,
		RandomSeed:         seed,
	}, []channelrouting.ShadowCandidateInput{
		{
			PoolMemberID: 11, ChannelID: 101, Priority: 10, Weight: 10,
			Metric: &channelrouting.ShadowMetricInput{RequestCount: 100, SuccessCount: 100, ReliabilityRequestCount: 100, P95LatencyMs: 300, OutputTokensPerSecond: 50},
			Cost:   &channelrouting.ShadowCostInput{Known: true, Cost: 10, UpdatedUnix: 990},
		},
		{
			PoolMemberID: 12, ChannelID: 102, Priority: 10, Weight: 10,
			Metric: &channelrouting.ShadowMetricInput{RequestCount: 100, SuccessCount: 80, ReliabilityRequestCount: 100, ReliabilityFailureCount: 20, P95LatencyMs: 250, OutputTokensPerSecond: 60},
			Cost:   &channelrouting.ShadowCostInput{Known: true, Cost: 1, UpdatedUnix: 990},
		},
	})
	require.NoError(t, err)
	replay, err := channelrouting.RunShadowReplay(input)
	require.NoError(t, err)
	actualCost, actualCostKnown := channelrouting.ShadowExpectedCostForChannel(input, 101)
	decisionID, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID:            requestID,
		PoolID:               poolID,
		GroupName:            profile.GroupName,
		ModelName:            profile.ModelName,
		SnapshotRevision:     input.PolicyRevision,
		AlgorithmVersion:     input.AlgorithmVersion,
		RetryIndex:           profile.RetryIndex,
		IsStream:             profile.IsStream,
		ActualChannelID:      101,
		ObservedChannelID:    replay.SelectedChannelID,
		FilteredOpen:         replay.FilteredOpen,
		FilteredCapacity:     replay.FilteredCapacity,
		BreakerBypassed:      replay.BreakerBypassed,
		Candidates:           replay.Candidates,
		ReplayInput:          &input,
		DifferenceType:       channelrouting.ClassifyShadowDifference(101, replay),
		ActualCostKnown:      actualCostKnown,
		ActualExpectedCost:   actualCost,
		ObservedCostKnown:    replay.SelectedCostKnown,
		ObservedExpectedCost: replay.SelectedCost,
	})
	require.NoError(t, err)
	return decisionID
}

func enqueueControllerCanaryReplayAudit(t *testing.T) string {
	t.Helper()
	const (
		poolID         = 29
		policyRevision = 7
		requestID      = "cohort-0005"
	)
	profile, err := channelrouting.NewRequestProfile(
		"/v1/chat/completions", "group-29", "gpt-test", false, 0, 100, 20,
	)
	require.NoError(t, err)
	seed, err := channelrouting.DeriveDecisionSeed(requestID, policyRevision, 0)
	require.NoError(t, err)
	input, err := channelrouting.BuildCanaryReplayInput(poolID, policyRevision, 3, strings.Repeat("c", 64), profile, routingselector.Settings{
		WeightAvailability: 1, TopK: 1, MaxEjectedPct: 50, HalfOpenProbes: 1,
		SnapshotStaleSec: 1_800, NowUnix: 1_000, NowUnixMilli: 1_000_000, RandomSeed: seed,
	}, []channelrouting.ShadowCandidateInput{{
		PoolMemberID: 11, ChannelID: 101, CredentialID: 1_001,
		Priority: 10, Weight: 10, SlowStartFactor: 1,
	}})
	require.NoError(t, err)
	replay, err := channelrouting.RunShadowReplay(input)
	require.NoError(t, err)
	gate, err := channelrouting.EvaluateCanaryGate(poolID, 401, policyRevision, requestID, 100)
	require.NoError(t, err)
	require.True(t, gate.InCanary)
	admission := channelrouting.CapacityAdmission{
		Mode: channelrouting.CapacityModeLocalSoft,
		Key: channelrouting.CapacityKey{
			PolicyRevision: policyRevision, PoolID: poolID, MemberID: 11, Model: profile.ModelName,
		},
		Demand: channelrouting.Demand{RPM: 1, InputTPM: 100, OutputTPM: 20, Inflight: 1},
		Limit:  channelrouting.Limit{RPM: 10, InputTPM: 1_000, OutputTPM: 200, Inflight: 4},
	}
	decisionID, err := channelrouting.EnqueueDecision(channelrouting.DecisionInput{
		RequestID: requestID, PoolID: poolID, GroupName: profile.GroupName, ModelName: profile.ModelName,
		SnapshotRevision: policyRevision, AlgorithmVersion: channelrouting.DecisionAlgorithmCanaryV1,
		ActualChannelID: replay.SelectedChannelID, ObservedChannelID: replay.SelectedChannelID,
		FilteredOpen: replay.FilteredOpen, FilteredCapacity: replay.FilteredCapacity,
		BreakerBypassed: replay.BreakerBypassed, Candidates: replay.Candidates, ReplayInput: &input,
		DifferenceType: channelrouting.ClassifyShadowDifference(replay.SelectedChannelID, replay),
		Gate:           &gate,
		SelectedIdentity: channelrouting.Identity{
			SnapshotRevision: policyRevision, PoolID: poolID, MemberID: 11, CredentialID: 1_001,
		},
		CapacityAdmission: &admission,
	})
	require.NoError(t, err)
	return decisionID
}

func performControllerReplayRequest(decisionID string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: decisionID}}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/v2/decisions/"+decisionID+"/replay", nil)
	ReplayChannelRoutingDecision(ctx)
	return recorder
}

func performControllerSimulationRequest(poolID string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: poolID}}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/v2/groups/"+poolID+"/simulations", bytes.NewBufferString(body))
	SimulateChannelRoutingGroup(ctx)
	return recorder
}
