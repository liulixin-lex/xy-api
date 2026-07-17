package controller

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingOperationRetryCancelAndAttentionFilter(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	failed, _, err := model.CreateFailedRoutingOperationContext(
		context.Background(), model.RoutingOperationSpec{
			Type: model.RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat("a", 64),
			SubjectType:      model.RoutingOperationSubjectRoutingProbes,
			ExpectedRevision: 1, ExpectedActivationID: 1, ActorID: 7, Reason: "manual probe",
		}, errors.New("probe failed"),
	)
	require.NoError(t, err)

	retryRecorder := httptest.NewRecorder()
	retryContext, _ := gin.CreateTestContext(retryRecorder)
	retryContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(failed.ID, 10)}}
	common.SetContextKey(retryContext, constant.ContextKeyUserId, 10)
	retryContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/operations/1/retry",
		bytes.NewBufferString(`{"reason":"retry after provider recovery"}`),
	)
	RetryChannelRoutingOperation(retryContext)
	require.Equal(t, http.StatusCreated, retryRecorder.Code, retryRecorder.Body.String())
	assert.Contains(t, retryRecorder.Body.String(), `"retry_of_operation_id":1`)
	assert.Contains(t, retryRecorder.Body.String(), `"status":"pending"`)
	assert.NotContains(t, retryRecorder.Body.String(), `"evaluation_hash"`)
	assert.NotContains(t, retryRecorder.Body.String(), `"idempotency_hash"`)

	var retried model.RoutingOperation
	require.NoError(t, db.Where("retry_of_operation_id = ?", failed.ID).First(&retried).Error)
	cancelRecorder := httptest.NewRecorder()
	cancelContext, _ := gin.CreateTestContext(cancelRecorder)
	cancelContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(retried.ID, 10)}}
	common.SetContextKey(cancelContext, constant.ContextKeyUserId, 10)
	cancelContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/operations/2/cancel",
		bytes.NewBufferString(`{"reason":"duplicate operator request"}`),
	)
	CancelChannelRoutingOperation(cancelContext)
	require.Equal(t, http.StatusOK, cancelRecorder.Code, cancelRecorder.Body.String())
	assert.Contains(t, cancelRecorder.Body.String(), `"status":"cancelled"`)
	assert.Contains(t, cancelRecorder.Body.String(), `"terminal_actor_id":10`)
	assert.NotContains(t, cancelRecorder.Body.String(), `"evaluation_hash"`)
	assert.NotContains(t, cancelRecorder.Body.String(), `"idempotency_hash"`)

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(
		http.MethodGet, "/api/channel-routing/operations?needs_attention=true&source=admin&limit=10", nil,
	)
	ListChannelRoutingOperations(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code, listRecorder.Body.String())
	assert.Contains(t, listRecorder.Body.String(), `"needs_attention":true`)
	assert.NotContains(t, listRecorder.Body.String(), `"status":"cancelled"`)
}

func TestChannelRoutingOperationActionRejectsUnsupportedTransition(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	succeeded, _, err := model.CreateSucceededRoutingOperationContext(
		context.Background(), model.RoutingOperationSpec{
			Type: model.RoutingOperationTypePolicySimulation, EvaluationHash: strings.Repeat("b", 64),
			SubjectType: model.RoutingOperationSubjectPolicyDraft, SubjectID: 1, PoolID: 1,
			ExpectedRevision: 1, ExpectedActivationID: 1, ActorID: 7, Reason: "simulation",
		}, model.RoutingOperationResult{}, map[string]bool{"ok": true},
	)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(succeeded.ID, 10)}}
	common.SetContextKey(ctx, constant.ContextKeyUserId, 10)
	ctx.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/operations/1/retry",
		bytes.NewBufferString(`{"reason":"retry completed simulation"}`),
	)
	RetryChannelRoutingOperation(ctx)
	require.Equal(t, http.StatusConflict, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"code":"operation_not_retryable"`)
}

func TestChannelRoutingOperationPublicAndTechnicalViewsAreSeparated(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	operation, _, err := model.CreateSucceededRoutingOperationContext(
		context.Background(), model.RoutingOperationSpec{
			Type: model.RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat("c", 64),
			SubjectType:      model.RoutingOperationSubjectRoutingProbes,
			ExpectedRevision: 3, ExpectedActivationID: 4, ActorID: 7, Reason: "manual probe",
		}, model.RoutingOperationResult{Revision: 5, ActivationID: 6, OutboxID: 7}, map[string]any{
			"enabled":       true,
			"stats":         map[string]any{},
			"credential_id": 987654,
			"api_key_index": 12,
			"secret_key":    "secret-signing-key",
		},
	)
	require.NoError(t, err)

	publicRecorder := httptest.NewRecorder()
	publicContext, _ := gin.CreateTestContext(publicRecorder)
	publicContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(operation.ID, 10)}}
	publicContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/operations/1", nil)
	GetChannelRoutingOperation(publicContext)
	require.Equal(t, http.StatusOK, publicRecorder.Code, publicRecorder.Body.String())
	publicBody := publicRecorder.Body.String()
	assert.Contains(t, publicBody, `"enabled":true`)
	assert.NotContains(t, publicBody, "secret-signing-key")
	assert.NotContains(t, publicBody, "987654")
	assert.NotContains(t, publicBody, `"result_outbox_id"`)
	assert.NotContains(t, publicBody, `"evaluation_hash"`)
	assert.NotContains(t, publicBody, `"idempotency_hash"`)

	technicalRecorder := httptest.NewRecorder()
	technicalContext, _ := gin.CreateTestContext(technicalRecorder)
	technicalContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(operation.ID, 10)}}
	technicalContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/operations/1/technical", nil)
	GetChannelRoutingOperationTechnical(technicalContext)
	require.Equal(t, http.StatusOK, technicalRecorder.Code, technicalRecorder.Body.String())
	technicalBody := technicalRecorder.Body.String()
	assert.Contains(t, technicalBody, `"result_outbox_id":7`)
	assert.Contains(t, technicalBody, `"evaluation_hash"`)
	assert.Contains(t, technicalBody, "[redacted]")
	assert.NotContains(t, technicalBody, "secret-signing-key")
	assert.NotContains(t, technicalBody, "987654")
}
