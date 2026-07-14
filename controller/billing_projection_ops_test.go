package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupBillingProjectionOpsControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/billing-projection-ops.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.BillingStatsProjection{}, &model.BillingLogProjection{},
		&model.BillingLogSinkConflictAudit{}, &model.BillingLogSinkConflictResolution{},
		&model.BillingProjectionAdminOperation{}, &model.Log{},
	))
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
	})
	return db
}

func TestBillingProjectionOpsListsArePaginatedAndRedacted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupBillingProjectionOpsControllerTestDB(t)
	now := time.Unix(1_800_900_000, 0).UnixMilli()
	stats := []model.BillingStatsProjection{
		{
			OperationKey: "private-stats-operation-1", ProtocolVersion: model.BillingStatsProjectionProtocol,
			Kind: model.BillingStatsProjectionKindAccepted, ReferenceID: 1, UserID: 11, ChannelID: 21,
			QuotaDelta: 10, RequestDelta: 1, DataExportUsername: "private-user-name",
			State: model.BillingStatsProjectionStateFailed, FailureCode: "retry_exhausted",
			LastError: "Authorization: Bearer private-token", CreatedTimeMs: now - 10,
			UpdatedTimeMs: now, CompletedTimeMs: now,
		},
		{
			OperationKey: "private-stats-operation-2", ProtocolVersion: model.BillingStatsProjectionProtocol,
			Kind: model.BillingStatsProjectionKindAccepted, ReferenceID: 2, UserID: 12, ChannelID: 22,
			QuotaDelta: 10, RequestDelta: 1, State: model.BillingStatsProjectionStateFailed,
			FailureCode: "retry_exhausted", CreatedTimeMs: now - 5, UpdatedTimeMs: now + 1,
			CompletedTimeMs: now + 1,
		},
	}
	require.NoError(t, db.Create(&stats).Error)
	require.NoError(t, db.Create(&model.BillingLogProjection{
		OperationKey: "private-log-operation", ProtocolVersion: model.BillingLogProjectionProtocol,
		Kind: model.BillingLogProjectionKindTaskTerminal, ReferenceID: 3, Required: true,
		Disposition: model.BillingLogProjectionDispositionPending, PayloadProtocol: 1,
		PayloadHash: "private-payload-hash", Payload: "private-frozen-payload",
		State: model.BillingLogProjectionStateFailed, FailureCode: model.BillingLogProjectionFailureRetryExhausted,
		LastError: "api_key=private-log-key", CreatedTimeMs: now - 5, UpdatedTimeMs: now,
		CompletedTimeMs: now,
	}).Error)
	require.NoError(t, db.Create(&model.BillingLogSinkConflictAudit{
		OperationKey: "private-conflict-operation", ProjectionID: 1,
		ExpectedPayloadHash: "private-expected-hash", ExpectedPayloadProtocol: 1,
		Receipts: "private-raw-receipts", DistinctReceipts: 2, PhysicalRows: 3,
		State: model.BillingLogSinkConflictStateOpen, Version: 1,
		FirstDetectedMs: now - 10, LastDetectedMs: now,
	}).Error)

	tests := []struct {
		url     string
		handler gin.HandlerFunc
	}{
		{"/api/system-info/billing-projections/stats/failed?limit=1", ListFailedBillingStatsProjections},
		{"/api/system-info/billing-projections/logs/failed", ListFailedBillingLogProjections},
		{"/api/system-info/billing-projections/log-sink-conflicts/open", ListOpenBillingLogSinkConflicts},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, test.url, nil)
		test.handler(ctx)
		assert.Equal(t, http.StatusOK, recorder.Code)
		body := recorder.Body.String()
		assert.NotContains(t, body, "private-stats-operation")
		assert.NotContains(t, body, "private-log-operation")
		assert.NotContains(t, body, "private-conflict-operation")
		assert.NotContains(t, body, "private-user-name")
		assert.NotContains(t, body, "private-token")
		assert.NotContains(t, body, "private-log-key")
		assert.NotContains(t, body, "private-frozen-payload")
		assert.NotContains(t, body, "private-payload-hash")
		assert.NotContains(t, body, "private-expected-hash")
		assert.NotContains(t, body, "private-raw-receipts")
		assert.Contains(t, body, `"operation_key_hash"`)
	}
}

func TestRequeueFailedBillingStatsProjectionRequiresCASAndReplaysIdempotently(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupBillingProjectionOpsControllerTestDB(t)
	now := time.Unix(1_801_000_000, 0).UnixMilli()
	projection := model.BillingStatsProjection{
		OperationKey: "async:controller:accepted:v1", ProtocolVersion: model.BillingStatsProjectionProtocol,
		Kind: model.BillingStatsProjectionKindAccepted, ReferenceID: 10, UserID: 11, ChannelID: 21,
		QuotaDelta: 10, RequestDelta: 1, State: model.BillingStatsProjectionStateFailed,
		FailureCode: "retry_exhausted", CreatedTimeMs: now - 1, UpdatedTimeMs: now, CompletedTimeMs: now,
	}
	require.NoError(t, db.Create(&projection).Error)
	etag := billingFailedProjectionETag("stats", projection.ID, projection.UpdatedTimeMs, projection.FailureCode)

	perform := func(key, body, requestETag string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/system-info/billing-projections/stats/failed/"+
			strconv.FormatInt(projection.ID, 10)+"/requeue", bytes.NewBufferString(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Request.Header.Set("Idempotency-Key", key)
		ctx.Request.Header.Set("If-Match", requestETag)
		ctx.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(projection.ID, 10)}}
		ctx.Set("id", 99)
		RequeueFailedBillingStatsProjection(ctx)
		return recorder
	}

	body := `{"expected_failure_code":"retry_exhausted"}`
	first := perform("projection-requeue-0001", body, etag)
	assert.Equal(t, http.StatusOK, first.Code)
	assert.Contains(t, first.Body.String(), `"replayed":false`)
	require.NoError(t, db.First(&projection, projection.ID).Error)
	assert.Equal(t, model.BillingStatsProjectionStatePending, projection.State)

	replay := perform("projection-requeue-0001", body, etag)
	assert.Equal(t, http.StatusOK, replay.Code)
	assert.Contains(t, replay.Body.String(), `"replayed":true`)

	conflictingBody := `{"expected_failure_code":"different_failure"}`
	conflictingETag := billingFailedProjectionETag("stats", projection.ID, now, "different_failure")
	conflict := perform("projection-requeue-0001", conflictingBody, conflictingETag)
	assert.Equal(t, http.StatusConflict, conflict.Code)
	assert.Contains(t, conflict.Body.String(), `"code":"idempotency_key_conflict"`)

	stale := perform("projection-requeue-0002", body, etag)
	assert.Equal(t, http.StatusPreconditionFailed, stale.Code)
	assert.Contains(t, stale.Body.String(), `"code":"precondition_failed"`)
	assert.Equal(t, "projection-requeue-0002", stale.Header().Get("Idempotency-Key"))
	assert.False(t, strings.Contains(stale.Body.String(), projection.OperationKey))
}
