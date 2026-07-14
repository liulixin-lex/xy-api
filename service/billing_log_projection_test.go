package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func createServiceBillingLogProjection(
	t *testing.T,
	operationKey string,
	referenceID int64,
	requestID string,
	now time.Time,
) *model.BillingLogProjection {
	t.Helper()
	var projection *model.BillingLogProjection
	require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		projection, _, err = model.CreateBillingLogProjectionTx(tx, model.BillingLogProjectionSpec{
			OperationKey: operationKey, Kind: model.BillingLogProjectionKindAccepted,
			ReferenceID: referenceID, Required: true,
			Entry: &model.Log{
				UserId: 1, CreatedAt: now.Unix(), Type: model.LogTypeConsume,
				Content: "service projection", ModelName: "service-model", Quota: 1,
				ChannelId: 1, RequestId: requestID, Other: `{"source":"service-test"}`,
			},
		}, now)
		return err
	}))
	return projection
}

func TestProcessNextBillingLogProjectionQuarantinesConflictAndContinues(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.BillingLogProjection{}, &model.Log{}))
	require.NoError(t, model.DB.Exec("DELETE FROM billing_log_projections").Error)
	t.Cleanup(func() { require.NoError(t, model.DB.Exec("DELETE FROM billing_log_projections").Error) })
	now := time.Now()
	conflicting := createServiceBillingLogProjection(
		t, "async:9961:accepted:v1", 9961, "request-9961", now,
	)
	key := conflicting.OperationKey
	require.NoError(t, model.LOG_DB.Create(&model.Log{
		CreatedAt: now.Unix(), BillingOperationKey: &key,
		BillingPayloadHash: fmt.Sprintf("%064x", 1), BillingPayloadProtocol: conflicting.PayloadProtocol,
	}).Error)
	next := createServiceBillingLogProjection(
		t, "async:9962:accepted:v1", 9962, "request-9962", now,
	)

	failed, processed, err := processNextBillingLogProjectionAt(
		context.Background(), "log-worker", now, time.Minute,
	)
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, conflicting.ID, failed.ID)
	assert.Equal(t, model.BillingLogProjectionStateFailed, failed.State)
	assert.Equal(t, "sink_receipt_conflict", failed.FailureCode)

	completed, processed, err := processNextBillingLogProjectionAt(
		context.Background(), "log-worker", now.Add(time.Second), time.Minute,
	)
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, next.ID, completed.ID)
	assert.Equal(t, model.BillingLogProjectionStateCompleted, completed.State)
	var count int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("billing_operation_key = ?", next.OperationKey).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestProcessNextBillingLogProjectionRetriesTransientSinkFailure(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.BillingLogProjection{}, &model.Log{}))
	require.NoError(t, model.DB.Exec("DELETE FROM billing_log_projections").Error)
	t.Cleanup(func() { require.NoError(t, model.DB.Exec("DELETE FROM billing_log_projections").Error) })
	now := time.Now()
	projection := createServiceBillingLogProjection(
		t, "async:9963:accepted:v1", 9963, "request-9963", now,
	)
	previousLogDB := model.LOG_DB
	model.LOG_DB = nil
	_, processed, err := processNextBillingLogProjectionAt(
		context.Background(), "log-worker", now, time.Minute,
	)
	model.LOG_DB = previousLogDB
	require.Error(t, err)
	require.True(t, processed)
	retried, readErr := model.GetBillingLogProjection(context.Background(), projection.ID)
	require.NoError(t, readErr)
	assert.Equal(t, model.BillingLogProjectionStatePending, retried.State)
	assert.Greater(t, retried.NextRetryMs, now.UnixMilli())
	assert.NotEmpty(t, retried.LastError)
}
