package controller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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
)

func TestChannelRoutingCostSyncOperationIsPersistentAndIdempotent(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))

	first := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/costs/sync", `{}`, "cost-sync-key-0001", 7,
		SyncChannelRoutingCosts,
	)
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
	assert.Contains(t, first.Body.String(), `"type":"cost_sync"`)
	assert.Contains(t, first.Body.String(), `"status":"pending"`)
	assert.NotContains(t, first.Body.String(), `"status":"succeeded"`)
	assert.Contains(t, first.Body.String(), `"execution_state":"accepted"`)
	assert.Contains(t, first.Body.String(), `"system_task_type":"routing_cost_sync"`)
	joined := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/costs/sync", `{}`, "cost-sync-key-0002", 7,
		SyncChannelRoutingCosts,
	)
	require.Equal(t, http.StatusAccepted, joined.Code, joined.Body.String())
	assert.Contains(t, joined.Body.String(), `"created":false`)

	replay := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/costs/sync", `{}`, "cost-sync-key-0001", 7,
		SyncChannelRoutingCosts,
	)
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	var operationCount int64
	require.NoError(t, db.Model(&model.RoutingOperation{}).Where("operation_type = ?", model.RoutingOperationTypeCostSync).Count(&operationCount).Error)
	assert.Equal(t, int64(2), operationCount)
	var taskCount int64
	require.NoError(t, db.Model(&model.SystemTask{}).Where("type = ?", model.SystemTaskTypeRoutingCostSync).Count(&taskCount).Error)
	assert.Equal(t, int64(1), taskCount)

	task, err := model.GetActiveSystemTask(model.SystemTaskTypeRoutingCostSync)
	require.NoError(t, err)
	require.NotNil(t, task)
	const runnerID = "controller-cost-sync-runner"
	claimedTask, claimed, err := model.ClaimSystemTask(task.ID, task.Type, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	nowMs := time.Now().UnixMilli()
	require.NoError(t, model.ClaimRoutingCostSyncOperationsContext(
		context.Background(), task.TaskID, runnerID, nowMs, 60_000,
	))
	finishedCount, err := model.FinishRoutingCostSyncTaskContext(
		context.Background(), claimedTask.TaskID, runnerID, model.SystemTaskStatusSucceeded,
		map[string]int{"snapshots": 4}, "", nowMs+1_000,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), finishedCount)
	completed := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/costs/sync", `{}`, "cost-sync-key-0001", 7,
		SyncChannelRoutingCosts,
	)
	require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
	assert.Contains(t, completed.Body.String(), `"status":"succeeded"`)
	assert.Contains(t, completed.Body.String(), `"execution_state":"completed"`)

	missingKey := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/costs/sync", `{}`, "", 7,
		SyncChannelRoutingCosts,
	)
	assert.Equal(t, http.StatusBadRequest, missingKey.Code)
}

func TestChannelRoutingActiveProbeOperationIsAcceptedBeforeExecutionAndReplaysTerminalState(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))

	first := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/probes/run", `{}`, "active-probe-key-0001", 8,
		RunChannelRoutingActiveProbe,
	)
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
	assert.Contains(t, first.Body.String(), `"type":"active_probe"`)
	assert.Contains(t, first.Body.String(), `"status":"pending"`)
	replay := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/probes/run", `{}`, "active-probe-key-0001", 8,
		RunChannelRoutingActiveProbe,
	)
	require.Equal(t, http.StatusAccepted, replay.Code, replay.Body.String())
	var operationCount int64
	require.NoError(t, db.Model(&model.RoutingOperation{}).
		Where("operation_type = ?", model.RoutingOperationTypeActiveProbe).Count(&operationCount).Error)
	assert.Equal(t, int64(1), operationCount)
	nowMs, err := model.RoutingEndpointDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	claimed, err := model.ClaimRoutingOperationContext(
		context.Background(), model.RoutingOperationTypeActiveProbe, nowMs, 30_000,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, model.SucceedRoutingOperationWithPayloadContext(
		context.Background(), claimed.ID, claimed.ClaimToken, nowMs+1,
		channelrouting.ActiveProbeOperationResult{
			Enabled: true,
			Stats:   channelrouting.ActiveProbeStats{Executed: 3, Succeeded: 2, Failed: 1},
		},
	))
	completed := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/probes/run", `{}`, "active-probe-key-0001", 8,
		RunChannelRoutingActiveProbe,
	)
	require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
	assert.Contains(t, completed.Body.String(), `"status":"succeeded"`)
	assert.Contains(t, completed.Body.String(), `"executed":3`)

	failed := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/probes/run", `{}`, "active-probe-key-failed", 8,
		RunChannelRoutingActiveProbe,
	)
	require.Equal(t, http.StatusAccepted, failed.Code, failed.Body.String())
	nowMs, err = model.RoutingEndpointDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	claimed, err = model.ClaimRoutingOperationContext(
		context.Background(), model.RoutingOperationTypeActiveProbe, nowMs, 30_000,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, model.FailRoutingOperationContext(
		context.Background(), claimed.ID, claimed.ClaimToken, nowMs+1, context.DeadlineExceeded,
	))
	failedReplay := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/probes/run", `{}`, "active-probe-key-failed", 8,
		RunChannelRoutingActiveProbe,
	)
	require.Equal(t, http.StatusOK, failedReplay.Code, failedReplay.Body.String())
	assert.Contains(t, failedReplay.Body.String(), `"status":"failed"`)
}

func TestChannelRoutingOperationResultPreservesLargeIntegerWhenCreatedIsAppended(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	operation, _, err := model.CreateSucceededRoutingOperationContext(
		context.Background(),
		model.RoutingOperationSpec{
			Type: model.RoutingOperationTypeCostSync, EvaluationHash: strings.Repeat("a", 64),
			SubjectType:      model.RoutingOperationSubjectRoutingCosts,
			ExpectedRevision: 1, ExpectedActivationID: 1, ActorID: 7, Reason: "large integer result",
		},
		model.RoutingOperationResult{},
		map[string]any{"outbox_id": int64(9_007_199_254_740_993), "status": "completed"},
	)
	require.NoError(t, err)
	view, err := channelRoutingOperationViewFromModel(operation)
	require.NoError(t, err)
	require.NoError(t, appendChannelRoutingOperationCreatedResult(&view, true))

	encoded, err := common.Marshal(view)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"outbox_id":9007199254740993`)
	assert.Contains(t, string(encoded), `"created":true`)
	assert.NotContains(t, string(encoded), "9007199254740992")
}

func TestChannelRoutingAuditExportPersistsAllowlistedDownload(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	secret := "SECRET-REQUEST-CREDENTIAL"
	require.NoError(t, db.Create(&model.RoutingDecisionAudit{
		DecisionID: "audit-export-decision", RequestID: secret, PoolID: 17, GroupName: "default", ModelName: "gpt-test",
		SnapshotRevision: 3, RuntimeGeneration: 4, ActivationID: 5, ActivationStage: model.RoutingDeploymentStageActive,
		AlgorithmVersion: "balanced-v1", ActualChannelID: 7, ObservedChannelID: 7, SelectedCredentialID: 99,
		RequestProfileJSON: `{"prompt":"` + secret + `"}`, ReplayInputJSON: `{"body":"` + secret + `"}`,
		CandidatesJSON: `{"credential":"` + secret + `"}`, CreatedTime: 100,
	}).Error)

	first := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/audit-exports",
		`{"from_time":90,"to_time":110,"limit":100}`, "audit-export-key-0001", 9,
		CreateChannelRoutingAuditExport,
	)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	var envelope struct {
		Data channelRoutingAuditExportResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(first.Body.Bytes(), &envelope))
	require.NotEmpty(t, envelope.Data.Export.ExportID)
	assert.Equal(t, 1, envelope.Data.Export.RecordCount)
	assert.Equal(t, model.RoutingOperationTypeAuditExport, envelope.Data.Operation.OperationType)

	replay := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/audit-exports",
		`{"from_time":90,"to_time":110,"limit":100}`, "audit-export-key-0001", 9,
		CreateChannelRoutingAuditExport,
	)
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	assert.Contains(t, replay.Body.String(), envelope.Data.Export.ExportID)

	downloadRecorder := httptest.NewRecorder()
	downloadContext, _ := gin.CreateTestContext(downloadRecorder)
	downloadContext.Params = gin.Params{{Key: "id", Value: envelope.Data.Export.ExportID}}
	downloadContext.Request = httptest.NewRequest(
		http.MethodGet, "/api/channel-routing/v2/audit-exports/"+envelope.Data.Export.ExportID+"/download", nil,
	)
	DownloadChannelRoutingAuditExport(downloadContext)
	require.Equal(t, http.StatusOK, downloadRecorder.Code, downloadRecorder.Body.String())
	assert.Contains(t, downloadRecorder.Header().Get("Content-Disposition"), envelope.Data.Export.ExportID)
	assert.Equal(t, `"`+envelope.Data.Export.ContentHash+`"`, downloadRecorder.Header().Get("ETag"))
	assert.Contains(t, downloadRecorder.Body.String(), `"decision_id":"audit-export-decision"`)
	assert.NotContains(t, downloadRecorder.Body.String(), secret)
	assert.NotContains(t, downloadRecorder.Body.String(), "request_id")
	assert.NotContains(t, downloadRecorder.Body.String(), "credential")

	invalid := performChannelRoutingControlRequest(
		t, http.MethodPost, "/api/channel-routing/v2/audit-exports",
		`{"from_time":90,"to_time":110,"unknown":true}`, "audit-export-key-0002", 9,
		CreateChannelRoutingAuditExport,
	)
	assert.Equal(t, http.StatusBadRequest, invalid.Code)
}

func performChannelRoutingControlRequest(
	t *testing.T,
	method string,
	target string,
	body string,
	idempotencyKey string,
	actorID int,
	handler gin.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	common.SetContextKey(c, constant.ContextKeyUserId, actorID)
	c.Request = httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if idempotencyKey != "" {
		c.Request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	handler(c)
	return recorder
}

func TestParseChannelRoutingAuditExportIDIsStrict(t *testing.T) {
	valid := "rae_" + strings.Repeat("a", 32)
	parsed, err := parseChannelRoutingAuditExportID(valid)
	require.NoError(t, err)
	assert.Equal(t, valid, parsed)
	_, err = parseChannelRoutingAuditExportID("rae_../secret")
	assert.ErrorIs(t, err, model.ErrRoutingAuditExportInvalid)
}
