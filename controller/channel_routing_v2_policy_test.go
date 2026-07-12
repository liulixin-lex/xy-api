package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPolicySimulationOperationPayloadFitsCrossDatabaseTextLimit(t *testing.T) {
	samples := make([]channelrouting.HistoricalSimulationSample, channelrouting.MaxSimulationLimit)
	for index := range samples {
		samples[index] = channelrouting.HistoricalSimulationSample{
			DecisionID: strings.Repeat("d", 64), CreatedTime: math.MaxInt64,
			AlgorithmVersion: strings.Repeat("a", 64), ActualChannelID: math.MaxInt32,
			BaselineChannelID: math.MaxInt32, SimulatedChannelID: math.MaxInt32,
			MatchesActual: true, SelectionChanged: true,
			BaselineCostKnown: true, BaselineExpectedCost: math.MaxFloat64,
			SimulatedCostKnown: true, SimulatedExpectedCost: math.MaxFloat64,
			ExpectedCostDelta: math.MaxFloat64, CounterfactualHash: strings.Repeat("f", 64),
			SimulatedAlgorithm: strings.Repeat("a", 64),
		}
	}
	impactIDs := make([]int, 100)
	impactModels := make([]string, 100)
	for index := range impactIDs {
		impactIDs[index] = math.MaxInt32 - index
		impactModels[index] = fmt.Sprintf("%03d%s", index, strings.Repeat("m", 125))
	}
	successDelta := -1.0
	latencyDelta := math.MaxFloat64
	capacityUtilization := 1.0
	capacityLimit := 1.0
	selectionChangeRate := 1.0
	payload, err := common.Marshal(channelRoutingPolicySimulationOperationResult{
		Draft: model.RoutingPolicyDraftSummary{
			ID: 1, BaseRevision: math.MaxInt64, BaseHash: strings.Repeat("a", 64),
			Version: math.MaxInt64, ETag: strings.Repeat("b", 64), DocumentHash: strings.Repeat("c", 64),
			Status: model.RoutingPolicyDraftStatusValidated, ValidatedHeadRevision: math.MaxInt64,
			ValidatedHeadHash: strings.Repeat("d", 64),
		},
		Result: channelrouting.HistoricalSimulationResult{
			PoolID: math.MaxInt32, Limit: channelrouting.MaxSimulationLimit,
			ScannedSamples: len(samples), EvaluatedSamples: len(samples), Samples: samples,
			SkipReasons: map[string]int{}, Skipped: []channelrouting.HistoricalSimulationSkip{},
			SimulatedAlgorithm: strings.Repeat("a", 64),
			Risk: &channelrouting.PolicySimulationRiskAssessment{
				State:   channelrouting.PolicySimulationRiskBlocked,
				Reasons: []string{"slo_degradation_detected", "capacity_insufficient", "traffic_change_rate_limit_unconfigured"},
				Scope: channelrouting.PolicySimulationImpactScope{
					AffectedPoolCount: 100, AffectedPoolIDs: impactIDs,
					UnsimulatedPoolCount: 100, UnsimulatedPoolIDs: impactIDs,
					AffectedChannelCount: 100, AffectedChannelIDs: impactIDs,
					AffectedModelCount: 100, AffectedModels: impactModels,
					ModelEvidenceState: channelrouting.PolicySimulationEvidenceKnown, Truncated: true,
				},
				Changes: channelrouting.PolicySimulationStructuralChanges{
					AddedPools: 100, RemovedPools: 100, PolicyChanges: 100, DisplayNameChanges: 100,
					GroupChanges: 100, DeploymentStageChanges: 100, PolicyProfileChanges: 100, PolicyConfigChanges: 100,
					AddedMembers: 100, RemovedMembers: 100, ChangedMembers: 100,
					MemberChannelChanges: 100, MemberEnablementChanges: 100, MemberPriorityChanges: 100,
					MemberWeightChanges: 100, MemberCredentialChanges: 100, MemberOverrideChanges: 100,
					TrafficAffecting: true,
				},
				SLO: channelrouting.PolicySimulationSLOImpact{
					State: channelrouting.PolicySimulationStatusFail, KnownSamples: channelrouting.MaxSimulationLimit,
					TotalSamples: channelrouting.MaxSimulationLimit, AverageSuccessRateDelta: &successDelta,
					AverageLatencyDeltaMs: &latencyDelta, LatencyMetric: "p95_latency_ms", Assessment: "degraded",
				},
				Capacity: channelrouting.PolicySimulationCapacityAssessment{
					State: channelrouting.PolicySimulationCapacityInsufficient, KnownSamples: channelrouting.MaxSimulationLimit,
					TotalSamples: channelrouting.MaxSimulationLimit, ExceededSamples: channelrouting.MaxSimulationLimit,
					MaxObservedUtilization: &capacityUtilization, UtilizationLimit: &capacityLimit,
				},
				Traffic: channelrouting.PolicySimulationTrafficRateAssessment{
					State:                        channelrouting.PolicySimulationTrafficUnknown,
					EstimatedSelectionChangeRate: &selectionChangeRate,
					Reason:                       "traffic_change_rate_limit_unconfigured",
				},
			},
		},
	})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(payload), 60<<10)
}

func TestChannelRoutingPolicyDraftAPIUsesETagCASAndBoundedSummaries(t *testing.T) {
	channelrouting.ResetRoutingEventsForTest()
	channelrouting.ResetRoutingEventTransportForTest()
	t.Cleanup(channelrouting.ResetRoutingEventsForTest)
	t.Cleanup(channelrouting.ResetRoutingEventTransportForTest)
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	seedChannelRoutingPolicyChannelForTest(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	base, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), 0, channelRoutingPolicyDraftDocumentForTest(100),
		model.RoutingPolicyActivationSpec{
			Stage: model.RoutingDeploymentStageShadow, ActorID: 1, Reason: "base",
		},
	)
	require.NoError(t, err)

	createBody := channelRoutingPolicyDraftBody(t, map[string]any{
		"base_revision": base.Revision.Revision,
		"document":      channelRoutingPolicyDraftDocumentForTest(200),
	})
	decodedBase, decodedDocument, err := decodeChannelRoutingPolicyDraftCreate(bytes.NewReader(createBody))
	require.NoError(t, err)
	assert.Equal(t, base.Revision.Revision, decodedBase)
	assert.Equal(t, int64(200), decodedDocument.Pools[0].Members[0].Weight)
	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	common.SetContextKey(createContext, constant.ContextKeyUserId, 9)
	createContext.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/v2/policy-drafts", bytes.NewReader(createBody))
	CreateChannelRoutingPolicyDraft(createContext)
	require.Equal(t, http.StatusCreated, createRecorder.Code, createRecorder.Body.String())
	createETag := createRecorder.Header().Get("ETag")
	require.NotEmpty(t, createETag)
	var createdEnvelope struct {
		Success bool                            `json:"success"`
		Data    model.RoutingPolicyDraftSummary `json:"data"`
	}
	require.NoError(t, common.Unmarshal(createRecorder.Body.Bytes(), &createdEnvelope))
	require.True(t, createdEnvelope.Success)
	require.Positive(t, createdEnvelope.Data.ID)
	assert.Equal(t, model.RoutingPolicyDraftStatusEditing, createdEnvelope.Data.Status)

	detailRecorder := httptest.NewRecorder()
	detailContext, _ := gin.CreateTestContext(detailRecorder)
	detailContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(createdEnvelope.Data.ID, 10)}}
	detailContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/policy-drafts/1", nil)
	GetChannelRoutingPolicyDraft(detailContext)
	require.Equal(t, http.StatusOK, detailRecorder.Code)
	assert.Equal(t, createETag, detailRecorder.Header().Get("ETag"))
	var detailEnvelope struct {
		Success bool                            `json:"success"`
		Data    channelRoutingPolicyDraftDetail `json:"data"`
	}
	require.NoError(t, common.Unmarshal(detailRecorder.Body.Bytes(), &detailEnvelope))
	require.True(t, detailEnvelope.Success)
	assert.Equal(t, int64(200), detailEnvelope.Data.Document.Pools[0].Members[0].Weight)

	missingPreconditionRecorder := httptest.NewRecorder()
	missingPreconditionContext, _ := gin.CreateTestContext(missingPreconditionRecorder)
	missingPreconditionContext.Params = detailContext.Params
	missingPreconditionContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/policy-drafts/1",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"document": channelRoutingPolicyDraftDocumentForTest(250)})),
	)
	UpdateChannelRoutingPolicyDraft(missingPreconditionContext)
	assert.Equal(t, http.StatusPreconditionRequired, missingPreconditionRecorder.Code)

	staleETag := createETag[:len(createETag)-2] + "f\""
	if staleETag == createETag {
		staleETag = createETag[:len(createETag)-2] + "e\""
	}
	staleRecorder := httptest.NewRecorder()
	staleContext, _ := gin.CreateTestContext(staleRecorder)
	staleContext.Params = detailContext.Params
	staleContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/policy-drafts/1",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"document": channelRoutingPolicyDraftDocumentForTest(250)})),
	)
	staleContext.Request.Header.Set("If-Match", staleETag)
	UpdateChannelRoutingPolicyDraft(staleContext)
	assert.Equal(t, http.StatusConflict, staleRecorder.Code)
	assert.Equal(t, createETag, staleRecorder.Header().Get("ETag"))

	updateRecorder := httptest.NewRecorder()
	updateContext, _ := gin.CreateTestContext(updateRecorder)
	updateContext.Params = detailContext.Params
	common.SetContextKey(updateContext, constant.ContextKeyUserId, 10)
	updateContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/policy-drafts/1",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"document": channelRoutingPolicyDraftDocumentForTest(300)})),
	)
	updateContext.Request.Header.Set("If-Match", createETag)
	UpdateChannelRoutingPolicyDraft(updateContext)
	require.Equal(t, http.StatusOK, updateRecorder.Code)
	updateETag := updateRecorder.Header().Get("ETag")
	require.NotEmpty(t, updateETag)
	assert.NotEqual(t, createETag, updateETag)

	validateRecorder := httptest.NewRecorder()
	validateContext, _ := gin.CreateTestContext(validateRecorder)
	validateContext.Params = detailContext.Params
	common.SetContextKey(validateContext, constant.ContextKeyUserId, 11)
	validateContext.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/v2/policy-drafts/1/validate", nil)
	validateContext.Request.Header.Set("If-Match", updateETag)
	ValidateChannelRoutingPolicyDraft(validateContext)
	require.Equal(t, http.StatusOK, validateRecorder.Code)
	validateETag := validateRecorder.Header().Get("ETag")
	assert.NotEqual(t, updateETag, validateETag)
	var validatedEnvelope struct {
		Success bool                            `json:"success"`
		Data    model.RoutingPolicyDraftSummary `json:"data"`
	}
	require.NoError(t, common.Unmarshal(validateRecorder.Body.Bytes(), &validatedEnvelope))
	assert.Equal(t, model.RoutingPolicyDraftStatusValidated, validatedEnvelope.Data.Status)

	enqueueControllerReplayAudit(t, 11, "draft-simulation-history")
	flushed, err := channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	simulateRecorder := httptest.NewRecorder()
	simulateContext, _ := gin.CreateTestContext(simulateRecorder)
	simulateContext.Params = detailContext.Params
	simulateContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policy-drafts/1/simulate",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"pool_id": 11, "limit": 10})),
	)
	simulateContext.Request.Header.Set("If-Match", validateETag)
	simulateContext.Request.Header.Set("Idempotency-Key", "policy-simulation-0001")
	SimulateChannelRoutingPolicyDraft(simulateContext)
	require.Equal(t, http.StatusOK, simulateRecorder.Code, simulateRecorder.Body.String())
	assert.Equal(t, validateETag, simulateRecorder.Header().Get("ETag"))
	assert.Contains(t, simulateRecorder.Body.String(), `"simulated_algorithm":"channel-routing-balanced-v1"`)
	assert.Contains(t, simulateRecorder.Body.String(), `"scanned_samples":1`)
	assert.Contains(t, simulateRecorder.Body.String(), `"simulated_channel_id":1001`)
	assert.Contains(t, simulateRecorder.Body.String(), `"type":"policy_simulation"`)
	assert.Contains(t, simulateRecorder.Body.String(), `"status":"succeeded"`)
	assert.Contains(t, simulateRecorder.Body.String(), `"risk":{"state":"unknown"`)
	assert.Contains(t, simulateRecorder.Body.String(), `"affected_pool_ids":[11]`)
	assert.Contains(t, simulateRecorder.Body.String(), `"traffic_change_rate_limit_unconfigured"`)
	var operation model.RoutingOperation
	require.NoError(t, db.Where("operation_type = ?", model.RoutingOperationTypePolicySimulation).First(&operation).Error)
	assert.Equal(t, createdEnvelope.Data.ID, operation.SubjectID)
	assert.Equal(t, 11, operation.PoolID)
	assert.Len(t, operation.ResultPayloadHash, 64)

	enqueueControllerReplayAudit(t, 11, "draft-simulation-history-newer")
	flushed, err = channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	resimulateRecorder := httptest.NewRecorder()
	resimulateContext, _ := gin.CreateTestContext(resimulateRecorder)
	resimulateContext.Params = detailContext.Params
	resimulateContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policy-drafts/1/simulate",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"pool_id": 11, "limit": 10})),
	)
	resimulateContext.Request.Header.Set("If-Match", validateETag)
	resimulateContext.Request.Header.Set("Idempotency-Key", "policy-simulation-0002")
	SimulateChannelRoutingPolicyDraft(resimulateContext)
	require.Equal(t, http.StatusOK, resimulateRecorder.Code, resimulateRecorder.Body.String())
	assert.Contains(t, resimulateRecorder.Body.String(), `"scanned_samples":2`)
	var simulationOperationCount int64
	require.NoError(t, db.Model(&model.RoutingOperation{}).
		Where("operation_type = ?", model.RoutingOperationTypePolicySimulation).
		Count(&simulationOperationCount).Error)
	assert.Equal(t, int64(2), simulationOperationCount)

	publishRecorder := httptest.NewRecorder()
	publishContext, _ := gin.CreateTestContext(publishRecorder)
	publishContext.Params = detailContext.Params
	common.SetContextKey(publishContext, constant.ContextKeyUserId, 12)
	publishContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policy-drafts/1/publish",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageShadow, "traffic_basis_points": 0, "reason": "publish draft",
		})),
	)
	publishContext.Request.Header.Set("If-Match", validateETag)
	publishContext.Request.Header.Set("Idempotency-Key", "publish-draft-0001")
	PublishChannelRoutingPolicyDraft(publishContext)
	require.Equal(t, http.StatusOK, publishRecorder.Code, publishRecorder.Body.String())
	publishedETag := publishRecorder.Header().Get("ETag")
	require.NotEmpty(t, publishedETag)
	assert.NotEqual(t, validateETag, publishedETag)
	assert.Contains(t, publishRecorder.Body.String(), `"status":"published"`)
	assert.Contains(t, publishRecorder.Body.String(), `"type":"policy_publish"`)
	var publishOperation model.RoutingOperation
	require.NoError(t, db.Where("operation_type = ?", model.RoutingOperationTypePolicyPublish).First(&publishOperation).Error)
	assert.Equal(t, createdEnvelope.Data.ID, publishOperation.SubjectID)
	assert.Positive(t, publishOperation.ResultRevision)
	assert.Positive(t, publishOperation.ResultActivationID)
	assert.Positive(t, publishOperation.ResultOutboxID)

	replayRecorder := httptest.NewRecorder()
	replayContext, _ := gin.CreateTestContext(replayRecorder)
	replayContext.Params = detailContext.Params
	common.SetContextKey(replayContext, constant.ContextKeyUserId, 12)
	replayContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policy-drafts/1/publish",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageShadow, "traffic_basis_points": 0, "reason": "publish draft",
		})),
	)
	replayContext.Request.Header.Set("If-Match", validateETag)
	replayContext.Request.Header.Set("Idempotency-Key", "publish-draft-0001")
	PublishChannelRoutingPolicyDraft(replayContext)
	require.Equal(t, http.StatusOK, replayRecorder.Code, replayRecorder.Body.String())
	assert.Contains(t, replayRecorder.Body.String(), fmt.Sprintf(`"id":%d`, publishOperation.ID))
	var publishOperationCount int64
	require.NoError(t, db.Model(&model.RoutingOperation{}).
		Where("operation_type = ?", model.RoutingOperationTypePolicyPublish).Count(&publishOperationCount).Error)
	assert.Equal(t, int64(1), publishOperationCount)

	conflictRecorder := httptest.NewRecorder()
	conflictContext, _ := gin.CreateTestContext(conflictRecorder)
	conflictContext.Params = detailContext.Params
	common.SetContextKey(conflictContext, constant.ContextKeyUserId, 12)
	conflictContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policy-drafts/1/publish",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageShadow, "traffic_basis_points": 0, "reason": "different request",
		})),
	)
	conflictContext.Request.Header.Set("If-Match", validateETag)
	conflictContext.Request.Header.Set("Idempotency-Key", "publish-draft-0001")
	PublishChannelRoutingPolicyDraft(conflictContext)
	assert.Equal(t, http.StatusConflict, conflictRecorder.Code, conflictRecorder.Body.String())
	assert.Contains(t, conflictRecorder.Body.String(), `"code":"idempotency_key_conflict"`)

	currentRecorder := httptest.NewRecorder()
	currentContext, _ := gin.CreateTestContext(currentRecorder)
	currentContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/policies/current", nil)
	GetChannelRoutingCurrentPolicy(currentContext)
	require.Equal(t, http.StatusOK, currentRecorder.Code, currentRecorder.Body.String())
	currentETag := currentRecorder.Header().Get("ETag")
	require.NotEmpty(t, currentETag)
	assert.Contains(t, currentRecorder.Body.String(), `"current_revision":2`)

	rollbackRecorder := httptest.NewRecorder()
	rollbackContext, _ := gin.CreateTestContext(rollbackRecorder)
	rollbackContext.Params = gin.Params{{Key: "version", Value: strconv.FormatInt(base.Revision.Revision, 10)}}
	common.SetContextKey(rollbackContext, constant.ContextKeyUserId, 13)
	rollbackContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policies/1/rollback",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageShadow, "traffic_basis_points": 0, "reason": "manual rollback",
		})),
	)
	rollbackContext.Request.Header.Set("If-Match", currentETag)
	rollbackContext.Request.Header.Set("Idempotency-Key", "rollback-policy-0001")
	RollbackChannelRoutingPolicy(rollbackContext)
	require.Equal(t, http.StatusOK, rollbackRecorder.Code, rollbackRecorder.Body.String())
	rolledBackETag := rollbackRecorder.Header().Get("ETag")
	assert.NotEqual(t, currentETag, rolledBackETag)
	assert.Contains(t, rollbackRecorder.Body.String(), `"type":"policy_manual_rollback"`)
	assert.Contains(t, rollbackRecorder.Body.String(), `"rollback_of_revision":1`)

	staleRollbackRecorder := httptest.NewRecorder()
	staleRollbackContext, _ := gin.CreateTestContext(staleRollbackRecorder)
	staleRollbackContext.Params = rollbackContext.Params
	staleRollbackContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policies/1/rollback",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageShadow, "traffic_basis_points": 0, "reason": "stale rollback",
		})),
	)
	staleRollbackContext.Request.Header.Set("If-Match", currentETag)
	staleRollbackContext.Request.Header.Set("Idempotency-Key", "rollback-policy-stale-0001")
	RollbackChannelRoutingPolicy(staleRollbackContext)
	assert.Equal(t, http.StatusConflict, staleRollbackRecorder.Code)
	assert.Equal(t, rolledBackETag, staleRollbackRecorder.Header().Get("ETag"))
	assert.Contains(t, staleRollbackRecorder.Body.String(), `"code":"policy_head_conflict"`)

	operationsRecorder := httptest.NewRecorder()
	operationsContext, _ := gin.CreateTestContext(operationsRecorder)
	operationsContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/operations?limit=10", nil)
	ListChannelRoutingOperations(operationsContext)
	require.Equal(t, http.StatusOK, operationsRecorder.Code, operationsRecorder.Body.String())
	assert.Contains(t, operationsRecorder.Body.String(), `"type":"policy_simulation"`)
	assert.Contains(t, operationsRecorder.Body.String(), `"type":"policy_publish"`)
	assert.Contains(t, operationsRecorder.Body.String(), `"type":"policy_manual_rollback"`)
	assert.NotContains(t, operationsRecorder.Body.String(), `"evaluated_samples"`)

	operationRecorder := httptest.NewRecorder()
	operationContext, _ := gin.CreateTestContext(operationRecorder)
	operationContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(operation.ID, 10)}}
	operationContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/operations/1", nil)
	GetChannelRoutingOperation(operationContext)
	require.Equal(t, http.StatusOK, operationRecorder.Code, operationRecorder.Body.String())
	assert.Contains(t, operationRecorder.Body.String(), `"evaluated_samples":1`)

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/policy-drafts?limit=10", nil)
	ListChannelRoutingPolicyDrafts(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code)
	var listEnvelope struct {
		Success bool `json:"success"`
		Data    struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(listRecorder.Body.Bytes(), &listEnvelope))
	require.True(t, listEnvelope.Success)
	require.Len(t, listEnvelope.Data.Items, 1)
	_, includesDocument := listEnvelope.Data.Items[0]["document"]
	assert.False(t, includesDocument)

	eventReplay, _, cancelEvents, err := channelrouting.SubscribeRoutingEvents(0)
	require.NoError(t, err)
	cancelEvents()
	eventTypes := make(map[string]bool, len(eventReplay.Events))
	for _, event := range eventReplay.Events {
		eventTypes[event.Type] = true
	}
	assert.True(t, eventTypes[channelrouting.RoutingEventTypePolicyDraftChanged])
	assert.True(t, eventTypes[channelrouting.RoutingEventTypePolicySimulation])
	assert.True(t, eventTypes[channelrouting.RoutingEventTypePolicyPublished])
	assert.True(t, eventTypes[channelrouting.RoutingEventTypePolicyRolledBack])
}

func TestChannelRoutingPolicyApprovalAPIEnforcesActiveDeployQuorum(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	seedChannelRoutingPolicyChannelForTest(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	base, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), 0, channelRoutingPolicyDraftDocumentForTest(100),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	draft, err := model.CreateRoutingPolicyDraftContext(
		context.Background(), base.Revision.Revision, channelRoutingPolicyDraftDocumentForTest(200), 2,
	)
	require.NoError(t, err)
	draft, err = model.ValidateRoutingPolicyDraftContext(
		context.Background(), draft.ID, draft.Version, draft.ETag, 2,
	)
	require.NoError(t, err)
	etag := channelRoutingPolicyDraftETag(draft.Summary())
	params := gin.Params{{Key: "id", Value: strconv.FormatInt(draft.ID, 10)}}

	approve := func(actorID int) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = params
		common.SetContextKey(ctx, constant.ContextKeyUserId, actorID)
		ctx.Request = httptest.NewRequest(
			http.MethodPost, "/api/channel-routing/v2/policy-drafts/1/approvals",
			bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
				"stage": model.RoutingDeploymentStageActive, "traffic_basis_points": 0, "reason": "deploy",
			})),
		)
		ctx.Request.Header.Set("If-Match", etag)
		ApproveChannelRoutingPolicyDraft(ctx)
		return recorder
	}

	first := approve(10)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	retry := approve(10)
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	assert.Contains(t, retry.Body.String(), `"created":false`)

	publishRecorder := httptest.NewRecorder()
	publishContext, _ := gin.CreateTestContext(publishRecorder)
	publishContext.Params = params
	common.SetContextKey(publishContext, constant.ContextKeyUserId, 20)
	publishContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policy-drafts/1/publish",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageActive, "traffic_basis_points": 0, "reason": "deploy",
		})),
	)
	publishContext.Request.Header.Set("If-Match", etag)
	publishContext.Request.Header.Set("Idempotency-Key", "publish-active-0001")
	PublishChannelRoutingPolicyDraft(publishContext)
	assert.Equal(t, http.StatusPreconditionFailed, publishRecorder.Code, publishRecorder.Body.String())
	assert.Contains(t, publishRecorder.Body.String(), `"code":"policy_approval_required"`)

	second := approve(11)
	require.Equal(t, http.StatusCreated, second.Code, second.Body.String())
	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Params = params
	listContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/policy-drafts/1/approvals", nil)
	ListChannelRoutingPolicyApprovals(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code, listRecorder.Body.String())
	assert.Contains(t, listRecorder.Body.String(), `"count":2`)
	assert.Contains(t, listRecorder.Body.String(), `"quorum":true`)

	publishRecorder = httptest.NewRecorder()
	publishContext, _ = gin.CreateTestContext(publishRecorder)
	publishContext.Params = params
	common.SetContextKey(publishContext, constant.ContextKeyUserId, 20)
	publishContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policy-drafts/1/publish",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageActive, "traffic_basis_points": 0, "reason": "deploy",
		})),
	)
	publishContext.Request.Header.Set("If-Match", etag)
	publishContext.Request.Header.Set("Idempotency-Key", "publish-active-0001")
	PublishChannelRoutingPolicyDraft(publishContext)
	require.Equal(t, http.StatusOK, publishRecorder.Code, publishRecorder.Body.String())
	assert.Contains(t, publishRecorder.Body.String(), `"status":"published"`)
}

func TestChannelRoutingPolicyRollbackApprovalAPIEnforcesBoundQuorum(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	seedChannelRoutingPolicyChannelForTest(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	first, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), 0, channelRoutingPolicyDraftDocumentForTest(100),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	second, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), first.Revision.Revision, channelRoutingPolicyDraftDocumentForTest(200),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 2, Reason: "change"},
	)
	require.NoError(t, err)
	_, err = model.PublishRoutingPolicyRevisionContext(
		context.Background(), second.Revision.Revision, channelRoutingPolicyDraftDocumentForTest(300),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 3, Reason: "second change"},
	)
	require.NoError(t, err)
	head, err := model.GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	headETag := channelRoutingPolicyHeadETag(head)
	activationBody := func(reason string) []byte {
		return channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageActive, "traffic_basis_points": 0, "reason": reason,
		})
	}
	rollback := func(targetRevision int64, actorID int, reason string, idempotencyKey string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "version", Value: strconv.FormatInt(targetRevision, 10)}}
		common.SetContextKey(ctx, constant.ContextKeyUserId, actorID)
		ctx.Request = httptest.NewRequest(
			http.MethodPost, "/api/channel-routing/v2/policies/1/rollback", bytes.NewReader(activationBody(reason)),
		)
		ctx.Request.Header.Set("If-Match", headETag)
		ctx.Request.Header.Set("Idempotency-Key", idempotencyKey)
		RollbackChannelRoutingPolicy(ctx)
		return recorder
	}
	approve := func(actorID int) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "version", Value: strconv.FormatInt(first.Revision.Revision, 10)}}
		common.SetContextKey(ctx, constant.ContextKeyUserId, actorID)
		ctx.Request = httptest.NewRequest(
			http.MethodPost, "/api/channel-routing/v2/policies/1/rollback-approvals",
			bytes.NewReader(activationBody("restore active policy")),
		)
		ctx.Request.Header.Set("If-Match", headETag)
		ApproveChannelRoutingPolicyRollback(ctx)
		return recorder
	}

	withoutApproval := rollback(first.Revision.Revision, 20, "restore active policy", "rollback-without-approval-0001")
	require.Equal(t, http.StatusPreconditionFailed, withoutApproval.Code, withoutApproval.Body.String())
	assert.Contains(t, withoutApproval.Body.String(), `"code":"policy_approval_required"`)

	firstApproval := approve(10)
	require.Equal(t, http.StatusCreated, firstApproval.Code, firstApproval.Body.String())
	retry := approve(10)
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	assert.Contains(t, retry.Body.String(), `"created":false`)
	secondApproval := approve(11)
	require.Equal(t, http.StatusCreated, secondApproval.Code, secondApproval.Body.String())

	wrongTarget := rollback(second.Revision.Revision, 20, "restore active policy", "rollback-wrong-target-0001")
	require.Equal(t, http.StatusPreconditionFailed, wrongTarget.Code, wrongTarget.Body.String())
	wrongReason := rollback(first.Revision.Revision, 20, "different rollback intent", "rollback-wrong-reason-0001")
	require.Equal(t, http.StatusPreconditionFailed, wrongReason.Code, wrongReason.Body.String())
	executorApproved := rollback(first.Revision.Revision, 10, "restore active policy", "rollback-self-approval-0001")
	require.Equal(t, http.StatusPreconditionFailed, executorApproved.Code, executorApproved.Body.String())

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Params = gin.Params{{Key: "version", Value: strconv.FormatInt(first.Revision.Revision, 10)}}
	common.SetContextKey(listContext, constant.ContextKeyUserId, 20)
	listContext.Request = httptest.NewRequest(
		http.MethodGet, "/api/channel-routing/v2/policies/1/rollback-approvals", nil,
	)
	ListChannelRoutingPolicyRollbackApprovals(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code, listRecorder.Body.String())
	assert.Contains(t, listRecorder.Body.String(), `"count":2`)
	assert.Contains(t, listRecorder.Body.String(), `"quorum":true`)

	success := rollback(first.Revision.Revision, 20, "restore active policy", "rollback-approved-0001")
	require.Equal(t, http.StatusOK, success.Code, success.Body.String())
	assert.Contains(t, success.Body.String(), `"rollback_of_revision":1`)
	assert.Contains(t, success.Body.String(), `"type":"policy_manual_rollback"`)
}

func TestChannelRoutingPolicyDraftApprovalStatusReportsRequirement(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	seedChannelRoutingPolicyChannelForTest(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	base, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), 0, channelRoutingPolicyDraftDocumentForTest(100),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	balanced, err := model.CreateRoutingPolicyDraftContext(
		context.Background(), base.Revision.Revision, channelRoutingPolicyDraftDocumentForTest(200), 2,
	)
	require.NoError(t, err)
	balanced, err = model.ValidateRoutingPolicyDraftContext(
		context.Background(), balanced.ID, balanced.Version, balanced.ETag, 2,
	)
	require.NoError(t, err)
	enterpriseDocument := channelRoutingPolicyDraftDocumentForTest(300)
	enterpriseDocument.Pools[0].PolicyProfile = model.RoutingPolicyProfileEnterpriseSLO
	enterprise, err := model.CreateRoutingPolicyDraftContext(
		context.Background(), base.Revision.Revision, enterpriseDocument, 3,
	)
	require.NoError(t, err)
	enterprise, err = model.ValidateRoutingPolicyDraftContext(
		context.Background(), enterprise.ID, enterprise.Version, enterprise.ETag, 3,
	)
	require.NoError(t, err)
	status := func(draftID int64, query string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(draftID, 10)}}
		common.SetContextKey(ctx, constant.ContextKeyUserId, 20)
		ctx.Request = httptest.NewRequest(
			http.MethodGet, "/api/channel-routing/v2/policy-drafts/1/approvals"+query, nil,
		)
		ListChannelRoutingPolicyApprovals(ctx)
		return recorder
	}

	untargeted := status(balanced.ID, "")
	require.Equal(t, http.StatusOK, untargeted.Code, untargeted.Body.String())
	assert.Contains(t, untargeted.Body.String(), `"requires_approval":false`)
	ordinary := status(balanced.ID, "?stage=shadow&traffic_basis_points=0&reason=status-check")
	require.Equal(t, http.StatusOK, ordinary.Code, ordinary.Body.String())
	assert.Contains(t, ordinary.Body.String(), `"requires_approval":false`)
	active := status(balanced.ID, "?stage=active&traffic_basis_points=0&reason=status-check")
	require.Equal(t, http.StatusOK, active.Code, active.Body.String())
	assert.Contains(t, active.Body.String(), `"requires_approval":true`)
	enterpriseStatus := status(enterprise.ID, "?stage=shadow&traffic_basis_points=0&reason=status-check")
	require.Equal(t, http.StatusOK, enterpriseStatus.Code, enterpriseStatus.Body.String())
	assert.Contains(t, enterpriseStatus.Body.String(), `"requires_approval":true`)
	invalid := status(balanced.ID, "?stage=canary&traffic_basis_points=0&reason=status-check")
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
	assert.Contains(t, invalid.Body.String(), `"code":"invalid_activation"`)
}

func TestChannelRoutingPolicyRollbackApprovalStatusReportsRequirement(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	seedChannelRoutingPolicyChannelForTest(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	balanced, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), 0, channelRoutingPolicyDraftDocumentForTest(100),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)
	enterpriseDocument := channelRoutingPolicyDraftDocumentForTest(200)
	enterpriseDocument.Pools[0].PolicyProfile = model.RoutingPolicyProfileEnterpriseSLO
	enterprise, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), balanced.Revision.Revision, enterpriseDocument,
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 2, Reason: "enterprise"},
	)
	require.NoError(t, err)
	_, err = model.PublishRoutingPolicyRevisionContext(
		context.Background(), enterprise.Revision.Revision, channelRoutingPolicyDraftDocumentForTest(300),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 3, Reason: "current"},
	)
	require.NoError(t, err)
	status := func(targetRevision int64, query string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Params = gin.Params{{Key: "version", Value: strconv.FormatInt(targetRevision, 10)}}
		common.SetContextKey(ctx, constant.ContextKeyUserId, 20)
		ctx.Request = httptest.NewRequest(
			http.MethodGet, "/api/channel-routing/v2/policies/1/rollback-approvals"+query, nil,
		)
		ListChannelRoutingPolicyRollbackApprovals(ctx)
		return recorder
	}

	ordinary := status(balanced.Revision.Revision, "?stage=shadow&traffic_basis_points=0&reason=status-check")
	require.Equal(t, http.StatusOK, ordinary.Code, ordinary.Body.String())
	assert.Contains(t, ordinary.Body.String(), `"requires_approval":false`)
	active := status(balanced.Revision.Revision, "?stage=active&traffic_basis_points=0&reason=status-check")
	require.Equal(t, http.StatusOK, active.Code, active.Body.String())
	assert.Contains(t, active.Body.String(), `"requires_approval":true`)
	enterpriseStatus := status(enterprise.Revision.Revision, "?stage=shadow&traffic_basis_points=0&reason=status-check")
	require.Equal(t, http.StatusOK, enterpriseStatus.Code, enterpriseStatus.Body.String())
	assert.Contains(t, enterpriseStatus.Body.String(), `"requires_approval":true`)
	invalid := status(balanced.Revision.Revision, "?reason=status-check")
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
	assert.Contains(t, invalid.Body.String(), `"code":"invalid_activation"`)
}

func TestChannelRoutingPolicyDraftAPIRejectsUnknownFieldsAndMismatchedTags(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	seedChannelRoutingPolicyChannelForTest(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	base, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), 0, channelRoutingPolicyDraftDocumentForTest(100),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)

	invalidRecorder := httptest.NewRecorder()
	invalidContext, _ := gin.CreateTestContext(invalidRecorder)
	invalidContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policy-drafts",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"base_revision": base.Revision.Revision,
			"document":      channelRoutingPolicyDraftDocumentForTest(200),
			"unexpected":    true,
		})),
	)
	CreateChannelRoutingPolicyDraft(invalidContext)
	assert.Equal(t, http.StatusBadRequest, invalidRecorder.Code)

	draft, err := model.CreateRoutingPolicyDraftContext(
		context.Background(), base.Revision.Revision, channelRoutingPolicyDraftDocumentForTest(200), 2,
	)
	require.NoError(t, err)
	notValidatedRecorder := httptest.NewRecorder()
	notValidatedContext, _ := gin.CreateTestContext(notValidatedRecorder)
	notValidatedContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(draft.ID, 10)}}
	notValidatedContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policy-drafts/1/simulate",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"pool_id": 11})),
	)
	notValidatedContext.Request.Header.Set("If-Match", channelRoutingPolicyDraftETag(draft.Summary()))
	notValidatedContext.Request.Header.Set("Idempotency-Key", "policy-simulation-not-validated")
	SimulateChannelRoutingPolicyDraft(notValidatedContext)
	assert.Equal(t, http.StatusConflict, notValidatedRecorder.Code)
	assert.Contains(t, notValidatedRecorder.Body.String(), `"code":"policy_draft_not_validated"`)

	mismatchedRecorder := httptest.NewRecorder()
	mismatchedContext, _ := gin.CreateTestContext(mismatchedRecorder)
	mismatchedContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(draft.ID, 10)}}
	mismatchedContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/policy-drafts/1",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"document": channelRoutingPolicyDraftDocumentForTest(250)})),
	)
	mismatchedContext.Request.Header.Set(
		"If-Match", fmt.Sprintf("\"crd.999.%d.%s\"", draft.Version, draft.ETag),
	)
	UpdateChannelRoutingPolicyDraft(mismatchedContext)
	assert.Equal(t, http.StatusBadRequest, mismatchedRecorder.Code)
}

func channelRoutingPolicyDraftDocumentForTest(weight int64) model.RoutingPolicyDocument {
	return model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicySchemaVersion,
		Pools: []model.RoutingPolicyPoolContent{{
			PoolID: 11, GroupName: "VIP", DisplayName: "VIP",
			DeploymentStage: model.RoutingDeploymentStageShadow,
			PolicyProfile:   model.RoutingPolicyProfileBalanced,
			Policy:          json.RawMessage(`{}`),
			Members: []model.RoutingPolicyMemberContent{{
				MemberID: 101, ChannelID: 1001, Enabled: true, Priority: 1, Weight: weight,
				Overrides: json.RawMessage(`{}`),
			}},
		}},
	}
}

func seedChannelRoutingPolicyChannelForTest(t *testing.T, db *gorm.DB) {
	t.Helper()
	mapping := `{}`
	require.NoError(t, db.Create(&model.Channel{
		Id: 1001, Name: "routing-policy-controller", Models: "gpt-test", ModelMapping: &mapping,
	}).Error)
}

func channelRoutingPolicyDraftBody(t *testing.T, value any) []byte {
	t.Helper()
	data, err := common.Marshal(value)
	require.NoError(t, err)
	return data
}
