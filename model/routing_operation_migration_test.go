package model

import (
	"context"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateRoutingOperationStateInvariantsRepairsKnownLegacyRows(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}, &RoutingBreakerResetCommand{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))

	activeSpec := RoutingOperationSpec{
		Type: RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat("a", 64),
		SubjectType: RoutingOperationSubjectRoutingProbes, ExpectedRevision: 1,
		ExpectedActivationID: 1, ActorID: 7, Reason: "manual probe",
	}
	running, _, err := CreateRoutingOperationContext(context.Background(), activeSpec)
	require.NoError(t, err)
	claimed, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeActiveProbe, running.CreatedTimeMs, 60_000,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	runningCreatedTimeMs := claimed.UpdatedTimeMs + 10

	terminalSpec := activeSpec
	terminalSpec.EvaluationHash = strings.Repeat("b", 64)
	terminal, _, err := CreateRoutingOperationContext(context.Background(), terminalSpec)
	require.NoError(t, err)
	terminalClaim, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeActiveProbe, terminal.CreatedTimeMs, 1_000,
	)
	require.NoError(t, err)
	require.NotNil(t, terminalClaim)
	require.NoError(t, SucceedRoutingOperationWithPayloadContext(
		context.Background(), terminalClaim.ID, terminalClaim.ClaimToken,
		terminalClaim.UpdatedTimeMs, map[string]bool{"enabled": true},
	))
	terminalCreatedTimeMs := terminalClaim.UpdatedTimeMs + 10
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", terminal.ID).Updates(map[string]any{
		"created_time_ms":   terminalCreatedTimeMs,
		"updated_time_ms":   terminalClaim.UpdatedTimeMs,
		"completed_time_ms": terminalClaim.UpdatedTimeMs,
	}).Error)
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", running.ID).Updates(map[string]any{
		"created_time_ms": runningCreatedTimeMs,
		"updated_time_ms": claimed.UpdatedTimeMs,
		"claim_until_ms":  claimed.UpdatedTimeMs + 1,
	}).Error)

	canarySpec := routingOperationSpecForTest()
	canarySpec.EvaluationHash = strings.Repeat("c", 64)
	canary, _, err := CreateRoutingOperationContext(context.Background(), canarySpec)
	require.NoError(t, err)
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", canary.ID).Updates(map[string]any{
		"status":               RoutingOperationStatusSucceeded,
		"attempts":             0,
		"result_revision":      12,
		"result_activation_id": 22,
		"result_outbox_id":     32,
		"completed_time_ms":    canary.CreatedTimeMs,
		"updated_time_ms":      canary.CreatedTimeMs,
	}).Error)

	target := routingBreakerResetMemberTargetForTest()
	breaker, _, err := CreateRoutingBreakerResetOperationContext(
		context.Background(), routingBreakerResetOperationSpecForTest(target, "d"), target,
	)
	require.NoError(t, err)
	var command RoutingBreakerResetCommand
	require.NoError(t, db.Where("operation_id = ?", breaker.ID).First(&command).Error)
	require.NoError(t, db.Model(&RoutingBreakerResetCommand{}).Where("id = ?", command.ID).Update(
		"completed_time_ms", command.CreatedTimeMs-1,
	).Error)

	for iteration := 0; iteration < 2; iteration++ {
		require.NoError(t, migrateRoutingOperationStateInvariants(db))
	}

	storedRunning, err := GetRoutingOperationContext(context.Background(), running.ID)
	require.NoError(t, err)
	assert.Equal(t, runningCreatedTimeMs, storedRunning.UpdatedTimeMs)
	assert.Equal(t, runningCreatedTimeMs+1, storedRunning.ClaimUntilMs)

	storedTerminal, err := GetRoutingOperationContext(context.Background(), terminal.ID)
	require.NoError(t, err)
	assert.Equal(t, terminalCreatedTimeMs, storedTerminal.UpdatedTimeMs)
	assert.Equal(t, terminalCreatedTimeMs, storedTerminal.CompletedTimeMs)

	storedCanary, err := GetRoutingOperationContext(context.Background(), canary.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, storedCanary.Attempts)

	storedCommand, err := GetRoutingBreakerResetCommandByOperationContext(context.Background(), breaker.ID)
	require.NoError(t, err)
	assert.Equal(t, storedCommand.CreatedTimeMs, storedCommand.CompletedTimeMs)
}

func TestMigrateRoutingOperationStateInvariantsFailsClosed(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))

	spec := RoutingOperationSpec{
		Type: RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat("e", 64),
		SubjectType: RoutingOperationSubjectRoutingProbes, ExpectedRevision: 1,
		ExpectedActivationID: 1, ActorID: 7, Reason: "manual probe",
	}
	validCandidate, _, err := CreateRoutingOperationContext(context.Background(), spec)
	require.NoError(t, err)
	validFutureTimeMs := validCandidate.CreatedTimeMs + 10
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", validCandidate.ID).Updates(map[string]any{
		"created_time_ms": validFutureTimeMs,
		"updated_time_ms": validCandidate.UpdatedTimeMs,
	}).Error)

	spec.EvaluationHash = strings.Repeat("f", 64)
	corruptCandidate, _, err := CreateRoutingOperationContext(context.Background(), spec)
	require.NoError(t, err)
	corruptFutureTimeMs := corruptCandidate.CreatedTimeMs + 10
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", corruptCandidate.ID).Updates(map[string]any{
		"create_token":    "invalid",
		"created_time_ms": corruptFutureTimeMs,
		"updated_time_ms": corruptCandidate.UpdatedTimeMs,
	}).Error)

	assert.ErrorIs(t, migrateRoutingOperationStateInvariants(db), ErrRoutingOperationCorrupt)
	var unchanged RoutingOperation
	require.NoError(t, db.Where("id = ?", validCandidate.ID).First(&unchanged).Error)
	assert.Equal(t, validCandidate.UpdatedTimeMs, unchanged.UpdatedTimeMs)
}
