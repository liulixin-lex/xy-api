package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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
		"document":      channelRoutingPolicyDraftDocumentWithExtensionsForTest(200),
	})
	decodedBase, decodedDocument, err := decodeChannelRoutingPolicyDraftCreate(bytes.NewReader(createBody))
	require.NoError(t, err)
	assert.Equal(t, base.Revision.Revision, decodedBase)
	require.NotNil(t, decodedDocument.Pools[0].Members[0].WeightOverride)
	assert.Equal(t, int64(200), *decodedDocument.Pools[0].Members[0].WeightOverride)
	assert.Contains(t, decodedDocument.ExtensionFields, "root_extension")
	assert.Contains(t, decodedDocument.Pools[0].ExtensionFields, "pool_extension")
	assert.Contains(t, decodedDocument.Pools[0].Members[0].ExtensionFields, "member_extension")
	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	common.SetContextKey(createContext, constant.ContextKeyUserId, 9)
	createContext.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/policy-drafts", bytes.NewReader(createBody))
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
	detailContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/policy-drafts/1", nil)
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
	assert.Contains(t, detailRecorder.Body.String(), `"root_extension"`)
	assert.Contains(t, detailRecorder.Body.String(), `"pool_extension"`)
	assert.Contains(t, detailRecorder.Body.String(), `"member_extension"`)
	assert.Contains(t, detailRecorder.Body.String(), `"future_policy"`)
	assert.Contains(t, detailRecorder.Body.String(), `"future_override"`)

	missingPreconditionRecorder := httptest.NewRecorder()
	missingPreconditionContext, _ := gin.CreateTestContext(missingPreconditionRecorder)
	missingPreconditionContext.Params = detailContext.Params
	missingPreconditionContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/policy-drafts/1",
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
		http.MethodPut, "/api/channel-routing/policy-drafts/1",
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
		http.MethodPut, "/api/channel-routing/policy-drafts/1",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"document": channelRoutingPolicyDraftDocumentWithExtensionsForTest(300),
		})),
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
	validateContext.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/policy-drafts/1/validate", nil)
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

	enqueueControllerReplayAuditWithCandidates(t, 11, "draft-simulation-history", 101, 1001, 102, 1002)
	flushed, err := channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	simulateRecorder := httptest.NewRecorder()
	simulateContext, _ := gin.CreateTestContext(simulateRecorder)
	simulateContext.Params = detailContext.Params
	simulateContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/policy-drafts/1/simulate",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"pool_id": 11, "limit": 10})),
	)
	simulateContext.Request.Header.Set("If-Match", validateETag)
	simulateContext.Request.Header.Set("Idempotency-Key", "policy-simulation-0001")
	SimulateChannelRoutingPolicyDraft(simulateContext)
	require.Equal(t, http.StatusOK, simulateRecorder.Code, simulateRecorder.Body.String())
	assert.Equal(t, validateETag, simulateRecorder.Header().Get("ETag"))
	assert.Contains(t, simulateRecorder.Body.String(), `"simulated_algorithm":"channel-routing-balanced"`)
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
	var evidence model.RoutingPolicySimulationEvidence
	require.NoError(t, db.Where("operation_id = ?", operation.ID).First(&evidence).Error)

	simulationReplayRecorder := httptest.NewRecorder()
	simulationReplayContext, _ := gin.CreateTestContext(simulationReplayRecorder)
	simulationReplayContext.Params = detailContext.Params
	simulationReplayContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/policy-drafts/1/simulate",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"pool_id": 11, "limit": 10})),
	)
	simulationReplayContext.Request.Header.Set("If-Match", validateETag)
	simulationReplayContext.Request.Header.Set("Idempotency-Key", "policy-simulation-0001")
	SimulateChannelRoutingPolicyDraft(simulationReplayContext)
	require.Equal(t, http.StatusOK, simulationReplayRecorder.Code, simulationReplayRecorder.Body.String())
	assert.Contains(t, simulationReplayRecorder.Body.String(), fmt.Sprintf(`"evidence":{"id":%d`, evidence.ID))
	assert.Contains(t, simulationReplayRecorder.Body.String(), fmt.Sprintf(`"operation":{"id":%d`, operation.ID))

	enqueueControllerReplayAuditWithCandidates(t, 11, "draft-simulation-history-newer", 101, 1001, 102, 1002)
	flushed, err = channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	resimulateRecorder := httptest.NewRecorder()
	resimulateContext, _ := gin.CreateTestContext(resimulateRecorder)
	resimulateContext.Params = detailContext.Params
	resimulateContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/policy-drafts/1/simulate",
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
		http.MethodPost, "/api/channel-routing/policy-drafts/1/publish",
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
		http.MethodPost, "/api/channel-routing/policy-drafts/1/publish",
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
		http.MethodPost, "/api/channel-routing/policy-drafts/1/publish",
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
	currentContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/policies/current", nil)
	GetChannelRoutingCurrentPolicy(currentContext)
	require.Equal(t, http.StatusOK, currentRecorder.Code, currentRecorder.Body.String())
	currentETag := currentRecorder.Header().Get("ETag")
	require.NotEmpty(t, currentETag)
	assert.Contains(t, currentRecorder.Body.String(), `"current_revision":2`)
	assert.Contains(t, currentRecorder.Body.String(), `"root_extension"`)
	assert.Contains(t, currentRecorder.Body.String(), `"pool_extension"`)
	assert.Contains(t, currentRecorder.Body.String(), `"member_extension"`)

	rollbackRecorder := httptest.NewRecorder()
	rollbackContext, _ := gin.CreateTestContext(rollbackRecorder)
	rollbackContext.Params = gin.Params{{Key: "version", Value: strconv.FormatInt(base.Revision.Revision, 10)}}
	common.SetContextKey(rollbackContext, constant.ContextKeyUserId, 13)
	rollbackContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/policies/1/rollback",
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
		http.MethodPost, "/api/channel-routing/policies/1/rollback",
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
	operationsContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/operations?limit=10", nil)
	ListChannelRoutingOperations(operationsContext)
	require.Equal(t, http.StatusOK, operationsRecorder.Code, operationsRecorder.Body.String())
	assert.Contains(t, operationsRecorder.Body.String(), `"type":"policy_simulation"`)
	assert.Contains(t, operationsRecorder.Body.String(), `"type":"policy_publish"`)
	assert.Contains(t, operationsRecorder.Body.String(), `"type":"policy_manual_rollback"`)
	assert.NotContains(t, operationsRecorder.Body.String(), `"evaluated_samples"`)

	operationRecorder := httptest.NewRecorder()
	operationContext, _ := gin.CreateTestContext(operationRecorder)
	operationContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(operation.ID, 10)}}
	operationContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/operations/1", nil)
	GetChannelRoutingOperation(operationContext)
	require.Equal(t, http.StatusOK, operationRecorder.Code, operationRecorder.Body.String())
	assert.Contains(t, operationRecorder.Body.String(), `"evaluated_samples":1`)

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/policy-drafts?limit=10", nil)
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
	assert.Empty(t, listEnvelope.Data.Items, "published drafts leave the workspace")

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

func TestChannelRoutingPolicyActivePublishNeedsNoApprovalQuorum(t *testing.T) {
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

	publishRecorder := httptest.NewRecorder()
	publishContext, _ := gin.CreateTestContext(publishRecorder)
	publishContext.Params = params
	common.SetContextKey(publishContext, constant.ContextKeyUserId, 20)
	publishContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/policy-drafts/1/publish",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageActive, "traffic_basis_points": 0, "reason": "deploy",
		})),
	)
	publishContext.Request.Header.Set("If-Match", etag)
	publishContext.Request.Header.Set("Idempotency-Key", "publish-active-0001")
	PublishChannelRoutingPolicyDraft(publishContext)
	require.Equal(t, http.StatusOK, publishRecorder.Code, publishRecorder.Body.String())
	assert.Contains(t, publishRecorder.Body.String(), `"status":"published"`)
	assert.NotContains(t, publishRecorder.Body.String(), "approval")
}

func TestChannelRoutingPolicyFailedSimulationRequiresExplicitRiskAcceptance(t *testing.T) {
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
	head, err := model.GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	_, err = model.CreateRoutingPolicySimulationEvidenceContext(
		context.Background(), model.RoutingPolicySimulationEvidenceSpec{
			OperationID: 9_901, Draft: draft.Summary(), Head: head, TargetBound: true,
			TargetStage: model.RoutingDeploymentStageActive,
			RiskState:   model.RoutingPolicySimulationRiskFail,
			RiskPayload: map[string]any{"state": "fail", "reason": "capacity_insufficient"},
		},
	)
	require.NoError(t, err)
	etag := channelRoutingPolicyDraftETag(draft.Summary())
	params := gin.Params{{Key: "id", Value: strconv.FormatInt(draft.ID, 10)}}

	blocked := httptest.NewRecorder()
	blockedContext, _ := gin.CreateTestContext(blocked)
	blockedContext.Params = params
	common.SetContextKey(blockedContext, constant.ContextKeyUserId, 20)
	blockedContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/policy-drafts/1/publish",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageActive, "traffic_basis_points": 0, "reason": "deploy",
		})),
	)
	blockedContext.Request.Header.Set("If-Match", etag)
	blockedContext.Request.Header.Set("Idempotency-Key", "publish-risk-blocked-0001")
	PublishChannelRoutingPolicyDraft(blockedContext)
	require.Equal(t, http.StatusPreconditionFailed, blocked.Code, blocked.Body.String())
	assert.Contains(t, blocked.Body.String(), `"code":"policy_simulation_risk_acceptance_required"`)

	accepted := httptest.NewRecorder()
	acceptedContext, _ := gin.CreateTestContext(accepted)
	acceptedContext.Params = params
	common.SetContextKey(acceptedContext, constant.ContextKeyUserId, 20)
	acceptedContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/policy-drafts/1/publish",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageActive, "traffic_basis_points": 0, "reason": "deploy",
			"accept_simulation_risk": true,
			"risk_acceptance_reason": "Capacity is constrained; proceed with monitored rollout.",
		})),
	)
	acceptedContext.Request.Header.Set("If-Match", etag)
	acceptedContext.Request.Header.Set("Idempotency-Key", "publish-risk-accepted-0001")
	PublishChannelRoutingPolicyDraft(acceptedContext)
	require.Equal(t, http.StatusOK, accepted.Code, accepted.Body.String())
	assert.Contains(t, accepted.Body.String(), `"status":"published"`)

	var acceptance model.RoutingPolicyRiskAcceptance
	require.NoError(t, db.First(&acceptance).Error)
	assert.Equal(t, 20, acceptance.ActorID)
	assert.Equal(t, "Capacity is constrained; proceed with monitored rollout.", acceptance.Reason)
}

func TestChannelRoutingPolicyActiveRollbackNeedsNoApprovalQuorum(t *testing.T) {
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
	success := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(success)
	ctx.Params = gin.Params{{Key: "version", Value: strconv.FormatInt(first.Revision.Revision, 10)}}
	common.SetContextKey(ctx, constant.ContextKeyUserId, 20)
	ctx.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/policies/1/rollback",
		bytes.NewReader(activationBody("restore active policy")),
	)
	ctx.Request.Header.Set("If-Match", headETag)
	ctx.Request.Header.Set("Idempotency-Key", "rollback-without-approval-0001")
	RollbackChannelRoutingPolicy(ctx)
	require.Equal(t, http.StatusOK, success.Code, success.Body.String())
	assert.Contains(t, success.Body.String(), `"rollback_of_revision":1`)
	assert.Contains(t, success.Body.String(), `"type":"policy_manual_rollback"`)
	assert.NotContains(t, success.Body.String(), "approval")
}

func TestChannelRoutingLegacyRollbackRequiresSafeV2Draft(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	seedChannelRoutingPolicyChannelForTest(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	seedChannelRoutingLegacyPolicyForTest(t, db)
	current, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), 1, channelRoutingPolicyDraftDocumentForTest(200),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 2, Reason: "current v2"},
	)
	require.NoError(t, err)
	head, err := model.GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, current.Revision.Revision, head.CurrentRevision)
	headETag := channelRoutingPolicyHeadETag(head)

	direct := httptest.NewRecorder()
	directContext, _ := gin.CreateTestContext(direct)
	directContext.Params = gin.Params{{Key: "version", Value: "1"}}
	common.SetContextKey(directContext, constant.ContextKeyUserId, 20)
	directContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/policies/1/rollback",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"stage": model.RoutingDeploymentStageActive, "traffic_basis_points": 0, "reason": "legacy rollback",
		})),
	)
	directContext.Request.Header.Set("If-Match", headETag)
	directContext.Request.Header.Set("Idempotency-Key", "legacy-rollback-direct-0001")
	RollbackChannelRoutingPolicy(directContext)
	require.Equal(t, http.StatusConflict, direct.Code, direct.Body.String())
	assert.Contains(t, direct.Body.String(), `"code":"legacy_policy_requires_conversion_draft"`)

	preview := httptest.NewRecorder()
	previewContext, _ := gin.CreateTestContext(preview)
	previewContext.Params = gin.Params{{Key: "version", Value: "1"}}
	common.SetContextKey(previewContext, constant.ContextKeyUserId, 20)
	previewContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/policies/1/rollback-draft", nil,
	)
	previewContext.Request.Header.Set("If-Match", headETag)
	CreateChannelRoutingPolicyRollbackDraft(previewContext)
	require.Equal(t, http.StatusCreated, preview.Code, preview.Body.String())
	var envelope struct {
		Success bool                                      `json:"success"`
		Data    channelRoutingPolicyRollbackDraftResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(preview.Body.Bytes(), &envelope))
	require.True(t, envelope.Success)
	assert.Equal(t, model.RoutingPolicySchemaVersion, envelope.Data.Document.SchemaVersion)
	assert.Equal(t, model.RoutingPolicyLegacySchemaVersion, envelope.Data.Conversion.SourceSchemaVersion)
	validated, err := model.ValidateRoutingPolicyDraftContext(
		context.Background(), envelope.Data.Draft.ID, envelope.Data.Draft.Version,
		envelope.Data.Draft.ETag, 20,
	)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingPolicyDraftStatusValidated, validated.Status)

	history, revision, err := model.LoadRoutingPolicyRevisionContext(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingPolicyLegacySchemaVersion, revision.SchemaVersion)
	assert.Equal(t, model.RoutingPolicyLegacySchemaVersion, history.SchemaVersion)
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
		http.MethodPost, "/api/channel-routing/policy-drafts",
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
		http.MethodPost, "/api/channel-routing/policy-drafts/1/simulate",
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
		http.MethodPut, "/api/channel-routing/policy-drafts/1",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"document": channelRoutingPolicyDraftDocumentForTest(250)})),
	)
	mismatchedContext.Request.Header.Set(
		"If-Match", fmt.Sprintf("\"crd.999.%d.%s\"", draft.Version, draft.ETag),
	)
	UpdateChannelRoutingPolicyDraft(mismatchedContext)
	assert.Equal(t, http.StatusBadRequest, mismatchedRecorder.Code)
}

func channelRoutingPolicyDraftDocumentForTest(weight int64) model.RoutingPolicyDocument {
	enabled := true
	priority := int64(1)
	generation := fmt.Sprintf("%032x", 1001)
	return model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicySchemaVersion,
		Pools: []model.RoutingPolicyPoolContent{{
			PoolID: 11, GroupName: "VIP", DisplayName: "VIP",
			DeploymentStage: model.RoutingDeploymentStageShadow,
			PolicyProfile:   model.RoutingPolicyProfileBalanced,
			Policy:          json.RawMessage(`{}`),
			Members: []model.RoutingPolicyMemberContent{{
				MemberID: 101, ChannelID: 1001, RoutingGeneration: generation,
				EnabledOverride: &enabled, PriorityOverride: &priority, WeightOverride: &weight,
				Overrides: json.RawMessage(`{}`),
			}},
		}},
	}
}

func channelRoutingPolicyDraftDocumentWithExtensionsForTest(weight int64) model.RoutingPolicyDocument {
	document := channelRoutingPolicyDraftDocumentForTest(weight)
	document.ExtensionFields = map[string]json.RawMessage{
		"root_extension": json.RawMessage(`{"revision":3}`),
	}
	document.Pools[0].Policy = json.RawMessage(`{"future_policy":{"mode":"adaptive"}}`)
	document.Pools[0].ExtensionFields = map[string]json.RawMessage{
		"pool_extension": json.RawMessage(`{"owner":"operations"}`),
	}
	document.Pools[0].Members[0].Overrides = json.RawMessage(`{"future_override":{"enabled":true}}`)
	document.Pools[0].Members[0].ExtensionFields = map[string]json.RawMessage{
		"member_extension": json.RawMessage(`{"zone":"a"}`),
	}
	return document
}

func seedChannelRoutingPolicyChannelForTest(t *testing.T, db *gorm.DB) {
	t.Helper()
	mapping := `{}`
	generation := fmt.Sprintf("%032x", 1001)
	key := "routing-policy-controller-key"
	fingerprint, err := model.RoutingCredentialFingerprint(1001, generation, key)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Channel{
		Id: 1001, Name: "routing-policy-controller", Key: key, Models: "gpt-test", ModelMapping: &mapping,
		RoutingIdentity: fmt.Sprintf("%032x", 1_001_001), RoutingGeneration: generation,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingPool{
		ID: 11, GroupKey: "vip", GroupName: "VIP", DisplayName: "VIP",
		Source: model.RoutingPoolSourceLegacyGroup, Active: true,
		DefaultEnabled: true, DefaultPriority: 0, DefaultWeight: 100,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingPoolMember{
		ID: 101, PoolID: 11, ChannelID: 1001, ChannelGeneration: generation,
		Source: model.RoutingPoolSourceLegacyGroup, Active: true,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingCredentialRef{
		ID: 201, ChannelID: 1001, ChannelGeneration: generation, Fingerprint: fingerprint,
		FingerprintVersion: model.RoutingCredentialFingerprintVersion,
		Active:             true, LastSeenIndex: model.RoutingMetricSingleKeyIndex, CurrentOccurrences: 1,
	}).Error)
}

func seedChannelRoutingLegacyPolicyForTest(t *testing.T, db *gorm.DB) {
	t.Helper()
	legacy := model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicyLegacySchemaVersion,
		Pools: []model.RoutingPolicyPoolContent{{
			PoolID: 11, GroupName: "VIP", DisplayName: "Legacy VIP",
			DeploymentStage: model.RoutingDeploymentStageShadow,
			PolicyProfile:   model.RoutingPolicyProfileBalanced,
			Policy:          json.RawMessage(`{}`),
			Members:         []model.RoutingPolicyMemberContent{},
		}},
	}
	legacy, contentHash, err := model.NormalizeRoutingPolicyDocument(legacy)
	require.NoError(t, err)
	groupHash := sha256.Sum256([]byte("VIP"))
	now := time.Now().Unix()
	revision := model.RoutingPolicyRevision{
		Revision: 1, SchemaVersion: model.RoutingPolicyLegacySchemaVersion,
		ContentHash: contentHash, PoolCount: 1, ActorID: 1, Reason: "legacy policy", CreatedTime: now,
	}
	pool := model.RoutingPolicyPoolRevision{
		Revision: 1, PoolID: 11, GroupKey: fmt.Sprintf("%x", groupHash),
		GroupName: "VIP", DisplayName: legacy.Pools[0].DisplayName,
		DeploymentStage: legacy.Pools[0].DeploymentStage,
		PolicyProfile:   legacy.Pools[0].PolicyProfile,
		PolicyJSON:      string(legacy.Pools[0].Policy),
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&revision).Error; err != nil {
			return err
		}
		if err := tx.Create(&pool).Error; err != nil {
			return err
		}
		activation := model.RoutingPolicyActivation{
			Revision: 1, Stage: model.RoutingDeploymentStageShadow,
			ActorID: 1, Reason: "legacy policy", CreatedTime: now,
		}
		if err := tx.Create(&activation).Error; err != nil {
			return err
		}
		return tx.Model(&model.RoutingPolicyHead{}).Where("id = ?", 1).Updates(map[string]any{
			"current_revision": int64(1), "current_activation_id": activation.ID,
			"current_hash": contentHash, "current_stage": activation.Stage, "updated_time": now,
		}).Error
	}))
}

func channelRoutingPolicyDraftBody(t *testing.T, value any) []byte {
	t.Helper()
	data, err := common.Marshal(value)
	require.NoError(t, err)
	return data
}
