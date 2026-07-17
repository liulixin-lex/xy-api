package model

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingOperationV2RetryCreatesImmutableLinkedOperation(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}, &RoutingControlAudit{}))

	spec := routingActiveProbeOperationSpecForV2Test("a")
	original, created, err := CreateRoutingOperationContext(context.Background(), spec)
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, routingOperationRecordSchemaVersion, original.SchemaVersion)
	assert.Equal(t, RoutingOperationSourceAdmin, original.Source)
	assert.True(t, original.Retryable)
	assert.True(t, original.Cancellable)
	assert.Equal(t, RoutingOperationRetentionHighFrequency, original.RetentionCategory)
	require.Len(t, original.CorrelationID, 32)

	claimed, err := ClaimRoutingOperationContext(
		context.Background(), original.OperationType, original.CreatedTimeMs, 1_000,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, FailRoutingOperationContext(
		context.Background(), claimed.ID, claimed.ClaimToken,
		claimed.UpdatedTimeMs+1, errors.New("probe failed"),
	))
	failed, err := GetRoutingOperationContext(context.Background(), original.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingOperationStatusFailed, failed.Status)
	assert.True(t, failed.NeedsAttention)
	assert.Equal(t, RoutingOperationRetentionExtended, failed.RetentionCategory)
	failedCompletedTime := failed.CompletedTimeMs

	retried, created, err := RetryTerminalRoutingOperationContext(
		context.Background(), failed.ID, 9, "retry failed probe",
	)
	require.NoError(t, err)
	require.True(t, created)
	assert.NotEqual(t, failed.ID, retried.ID)
	assert.Equal(t, RoutingOperationStatusPending, retried.Status)
	assert.Equal(t, failed.CorrelationID, retried.CorrelationID)
	assert.Equal(t, failed.ID, retried.ParentOperationID)
	assert.Equal(t, failed.ID, retried.RetryOfOperationID)
	assert.Equal(t, 1, retried.RetrySequence)
	assert.Equal(t, 9, retried.ActorID)
	assert.Equal(t, RoutingOperationSourceAdmin, retried.Source)
	assert.Equal(t, RoutingOperationRetentionHighFrequency, retried.RetentionCategory)
	assert.False(t, retried.NeedsAttention)

	replay, created, err := RetryTerminalRoutingOperationContext(
		context.Background(), failed.ID, 9, "retry failed probe",
	)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, retried.ID, replay.ID)
	unchanged, err := GetRoutingOperationContext(context.Background(), failed.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingOperationStatusFailed, unchanged.Status)
	assert.Equal(t, failedCompletedTime, unchanged.CompletedTimeMs)

	retryClaim, err := ClaimRoutingOperationContext(
		context.Background(), retried.OperationType, retried.CreatedTimeMs, 1_000,
	)
	require.NoError(t, err)
	require.NotNil(t, retryClaim)
	require.Equal(t, retried.ID, retryClaim.ID)
	require.NoError(t, PartiallySucceedRoutingOperationWithPayloadContext(
		context.Background(), retryClaim.ID, retryClaim.ClaimToken,
		retryClaim.UpdatedTimeMs+1, map[string]any{"succeeded": 2, "failed": 1},
		errors.New("one endpoint still failed"),
	))
	partial, err := GetRoutingOperationContext(context.Background(), retried.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingOperationStatusPartial, partial.Status)
	assert.True(t, partial.NeedsAttention)
	assert.Equal(t, RoutingOperationRetentionExtended, partial.RetentionCategory)

	secondRetry, created, err := RetryTerminalRoutingOperationContext(
		context.Background(), partial.ID, 9, "retry remaining endpoint",
	)
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, 2, secondRetry.RetrySequence)
	assert.Equal(t, partial.ID, secondRetry.RetryOfOperationID)
	assert.Equal(t, failed.CorrelationID, secondRetry.CorrelationID)

	var audits []RoutingControlAudit
	require.NoError(t, db.Where("subject_type = ?", RoutingControlSubjectOperation).Order("id asc").Find(&audits).Error)
	require.Len(t, audits, 4)
	assert.Equal(t, "operation.failed", audits[0].EventType)
	assert.Equal(t, RoutingControlActionRetry, audits[1].Action)
	assert.Equal(t, retried.ID, audits[1].OperationID)
	assert.Equal(t, RoutingControlAuditResultPartial, audits[2].Result)
	assert.True(t, audits[2].NeedsAttention)
	assert.Equal(t, secondRetry.ID, audits[3].OperationID)
}

func TestRoutingOperationV2CancellationIsTerminalAndRetryable(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}, &RoutingControlAudit{}))
	operation, _, err := CreateRoutingOperationContext(
		context.Background(), routingActiveProbeOperationSpecForV2Test("b"),
	)
	require.NoError(t, err)

	cancelled, err := CancelRoutingOperationContext(
		context.Background(), operation.ID, 12, "operator cancelled duplicate probe",
	)
	require.NoError(t, err)
	assert.Equal(t, RoutingOperationStatusCancelled, cancelled.Status)
	assert.Equal(t, 12, cancelled.TerminalActorID)
	assert.False(t, cancelled.NeedsAttention)
	assert.Equal(t, RoutingOperationRetentionExtended, cancelled.RetentionCategory)
	assert.Zero(t, cancelled.Attempts)

	claimed, err := ClaimRoutingOperationContext(
		context.Background(), operation.OperationType, cancelled.CompletedTimeMs+1, 1_000,
	)
	require.NoError(t, err)
	assert.Nil(t, claimed)
	retried, created, err := RetryTerminalRoutingOperationContext(
		context.Background(), cancelled.ID, 12, "restart cancelled probe",
	)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, cancelled.ID, retried.RetryOfOperationID)
	var audits []RoutingControlAudit
	require.NoError(t, db.Where("subject_type = ?", RoutingControlSubjectOperation).Order("id asc").Find(&audits).Error)
	require.Len(t, audits, 2)
	assert.Equal(t, RoutingControlActionCancel, audits[0].Action)
	assert.Equal(t, RoutingOperationStatusCancelled, cancelled.Status)
	assert.Equal(t, RoutingControlActionRetry, audits[1].Action)
}

func TestRoutingOperationV2RetryWaitAndAttentionFilters(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	operation, _, err := CreateRoutingOperationContext(
		context.Background(), routingActiveProbeOperationSpecForV2Test("c"),
	)
	require.NoError(t, err)
	claimed, err := ClaimRoutingOperationContext(
		context.Background(), operation.OperationType, operation.CreatedTimeMs, 1_000,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, RetryRoutingOperationContext(
		context.Background(), claimed.ID, claimed.ClaimToken,
		claimed.UpdatedTimeMs+1, claimed.UpdatedTimeMs+100, errors.New("transient"),
	))
	waiting, err := GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingOperationStatusRetryWait, waiting.Status)
	assert.False(t, waiting.NeedsAttention)

	needsAttention := false
	items, more, err := ListRoutingOperationsContext(context.Background(), RoutingOperationFilter{
		Status: RoutingOperationStatusRetryWait, Source: RoutingOperationSourceAdmin,
		CorrelationID: waiting.CorrelationID, Retention: RoutingOperationRetentionHighFrequency,
		NeedsAttention: &needsAttention, Limit: 10,
	})
	require.NoError(t, err)
	assert.False(t, more)
	require.Len(t, items, 1)
	assert.Equal(t, waiting.ID, items[0].ID)
}

func TestRoutingOperationV2RetentionUsesOutcomeCategory(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	nowMs := time.Now().UnixMilli()

	standardSpec := routingActiveProbeOperationSpecForV2Test("d")
	standardSpec.RetentionCategory = RoutingOperationRetentionStandard
	standard, _, err := CreateSucceededRoutingOperationContext(
		context.Background(), standardSpec, RoutingOperationResult{}, map[string]bool{"ok": true},
	)
	require.NoError(t, err)
	highFrequency, _, err := CreateSucceededRoutingOperationContext(
		context.Background(), routingActiveProbeOperationSpecForV2Test("e"),
		RoutingOperationResult{}, map[string]bool{"ok": true},
	)
	require.NoError(t, err)
	extended, _, err := CreateFailedRoutingOperationContext(
		context.Background(), routingActiveProbeOperationSpecForV2Test("f"), errors.New("failed"),
	)
	require.NoError(t, err)
	recentExtended, _, err := CreateFailedRoutingOperationContext(
		context.Background(), routingActiveProbeOperationSpecForV2Test("1"), errors.New("recent failure"),
	)
	require.NoError(t, err)
	permanentSpec := routingActiveProbeOperationSpecForV2Test("2")
	permanentSpec.RetentionCategory = RoutingOperationRetentionPermanent
	permanent, _, err := CreateSucceededRoutingOperationContext(
		context.Background(), permanentSpec, RoutingOperationResult{}, map[string]bool{"ok": true},
	)
	require.NoError(t, err)

	setRoutingOperationTimesForV2Test(t, db, standard.ID, nowMs-int64(31*24*time.Hour/time.Millisecond))
	setRoutingOperationTimesForV2Test(t, db, highFrequency.ID, nowMs-int64(8*24*time.Hour/time.Millisecond))
	setRoutingOperationTimesForV2Test(t, db, extended.ID, nowMs-int64(91*24*time.Hour/time.Millisecond))
	setRoutingOperationTimesForV2Test(t, db, recentExtended.ID, nowMs-int64(89*24*time.Hour/time.Millisecond))
	setRoutingOperationTimesForV2Test(t, db, permanent.ID, nowMs-int64(365*24*time.Hour/time.Millisecond))

	deleted, err := DeleteCompletedRoutingOperationsBeforeContext(
		context.Background(), nowMs-int64(30*24*time.Hour/time.Millisecond), nowMs, 100,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted)
	var remaining []RoutingOperation
	require.NoError(t, db.Order("id asc").Find(&remaining).Error)
	require.Len(t, remaining, 2)
	assert.Equal(t, []int64{recentExtended.ID, permanent.ID}, []int64{remaining[0].ID, remaining[1].ID})
}

func TestRoutingOperationV2MigrationBackfillsMetadataAndRetryWait(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&legacyRoutingOperationForMigration{}))
	spec := routingOperationSpecForTest()
	_, idempotencyHash, err := normalizeRoutingOperationSpec(spec)
	require.NoError(t, err)
	legacy := legacyRoutingOperationForMigration{
		OperationType: spec.Type, IdempotencyHash: idempotencyHash,
		CreateToken: strings.Repeat("a", 32), EvaluationHash: spec.EvaluationHash,
		PoolID: spec.PoolID, ExpectedRevision: spec.ExpectedRevision,
		ExpectedActivationID: spec.ExpectedActivationID, ActorID: spec.ActorID, Reason: spec.Reason,
		Status: RoutingOperationStatusPending, Attempts: 1, NextRetryMs: 1_100, LastError: "retry",
		CreatedTimeMs: 1_000, UpdatedTimeMs: 1_050,
	}
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	require.NoError(t, migrateRoutingOperationStateInvariants(db))

	stored, err := GetRoutingOperationContext(context.Background(), legacy.ID)
	require.NoError(t, err)
	assert.Equal(t, routingOperationRecordSchemaVersion, stored.SchemaVersion)
	assert.Equal(t, RoutingOperationStatusRetryWait, stored.Status)
	assert.Equal(t, RoutingOperationSourceSystem, stored.Source)
	assert.Len(t, stored.CorrelationID, 32)
	assert.Equal(t, spec.Reason, stored.Summary)
	assert.False(t, stored.NeedsAttention)
}

func TestRoutingOperationTechnicalPayloadRecursivelyRedactsCredentials(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	operation, _, err := CreateSucceededRoutingOperationContext(
		context.Background(), routingActiveProbeOperationSpecForV2Test("3"),
		RoutingOperationResult{Revision: 15, ActivationID: 16, OutboxID: 17}, map[string]any{
			"safe": "visible",
			"nested": map[string]any{
				"credential_id": 987654,
				"api_key_index": 12,
				"secret_key":    "secret-signing-key",
			},
		},
	)
	require.NoError(t, err)

	technical, err := operation.TechnicalPayload()
	require.NoError(t, err)
	encoded, err := common.Marshal(technical)
	require.NoError(t, err)
	text := string(encoded)
	assert.Contains(t, text, "visible")
	assert.Contains(t, text, "[redacted]")
	assert.NotContains(t, text, "987654")
	assert.NotContains(t, text, `"api_key_index":12`)
	assert.NotContains(t, text, "secret-signing-key")
	assert.Equal(t, int64(17), technical.ResultOutboxID)
}

func routingActiveProbeOperationSpecForV2Test(hashCharacter string) RoutingOperationSpec {
	return RoutingOperationSpec{
		Type: RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat(hashCharacter, 64),
		SubjectType:      RoutingOperationSubjectRoutingProbes,
		ExpectedRevision: 1, ExpectedActivationID: 1, ActorID: 7, Reason: "manual active probe",
	}
}

func setRoutingOperationTimesForV2Test(t *testing.T, db *gorm.DB, id int64, timestamp int64) {
	t.Helper()
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", id).Updates(map[string]any{
		"created_time_ms": timestamp, "updated_time_ms": timestamp, "completed_time_ms": timestamp,
	}).Error)
}
