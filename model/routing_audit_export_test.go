package model

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingAuditExportExternalDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "mysql", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.envKey)
			if dsn == "" {
				t.Skipf("%s is not set", test.envKey)
			}
			db := openRoutingExternalTestDB(t, test.dbType, dsn)
			withRoutingTestDB(t, db, test.dbType)
			require.NoError(t, db.AutoMigrate(
				&RoutingPolicyHead{}, &RoutingPolicyRevision{}, &RoutingOperation{}, &RoutingDecisionAudit{},
				&RoutingAuditExport{}, &RoutingAuditExportChunk{}, &RoutingHedgeAttemptAudit{},
			))
			require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

			audits := make([]RoutingDecisionAudit, 256)
			for index := range audits {
				audits[index] = RoutingDecisionAudit{
					DecisionID: fmt.Sprintf("external-audit-%04d", index), PoolID: 9,
					GroupName: "external", ModelName: "gpt-external-compatibility",
					SnapshotRevision: 3, RuntimeGeneration: 4, ActivationID: 5,
					ActivationStage: RoutingDeploymentStageActive, AlgorithmVersion: "balanced-v1",
					ActualChannelID: 7, ObservedChannelID: 7, ObservedMatchesActual: true,
					CreatedTime: 1_000 + int64(index),
				}
			}
			require.NoError(t, db.Create(&audits).Error)
			export, operation, created, err := CreateRoutingAuditExportContext(
				context.Background(), RoutingAuditExportRequest{FromTime: 1_000, ToTime: 2_000, Limit: len(audits)}, 7,
				RoutingOperationRequestIdentity{KeyHash: strings.Repeat("a", 64), PayloadHash: strings.Repeat("b", 64)},
			)
			require.NoError(t, err)
			assert.True(t, created)
			assert.Equal(t, RoutingOperationStatusSucceeded, operation.Status)
			assert.Equal(t, len(audits), export.RecordCount)
			assert.Greater(t, export.ChunkCount, 1)
			stored, payload, err := GetRoutingAuditExportContext(context.Background(), export.ExportID)
			require.NoError(t, err)
			assert.Equal(t, export.ContentHash, stored.ContentHash)
			var items []RoutingAuditExportItem
			require.NoError(t, common.Unmarshal(payload, &items))
			assert.Len(t, items, len(audits))
			assert.Equal(t, "external-audit-0000", items[0].DecisionID)
			assert.Equal(t, "external-audit-0255", items[len(items)-1].DecisionID)
		})
	}
}

func TestRoutingAuditExportIsIdempotentStableAndAllowlisted(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&RoutingPolicyHead{}, &RoutingPolicyRevision{}, &RoutingOperation{}, &RoutingDecisionAudit{},
		&RoutingAuditExport{}, &RoutingAuditExportChunk{}, &RoutingHedgeAttemptAudit{},
	))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
	secret := "SECRET-PROMPT-CREDENTIAL"
	require.NoError(t, db.Create(&[]RoutingDecisionAudit{
		{
			DecisionID: "decision-1", RequestID: secret, PoolID: 9, GroupName: "default", ModelName: "gpt-test",
			SnapshotRevision: 3, RuntimeGeneration: 4, ActivationID: 5, ActivationStage: RoutingDeploymentStageActive,
			AlgorithmVersion: "balanced-v1", ActualChannelID: 7, ObservedChannelID: 7,
			SelectedCredentialID: 81, ReservationAccountID: 91,
			RequestProfileJSON: `{"prompt":"` + secret + `"}`, ReplayInputJSON: `{"body":"` + secret + `"}`,
			CandidatesJSON: `{"credential":"` + secret + `"}`, CreatedTime: 100,
		},
		{
			DecisionID: "decision-2", PoolID: 10, GroupName: "backup", ModelName: "claude-test",
			SnapshotRevision: 3, RuntimeGeneration: 4, ActivationID: 5, ActivationStage: RoutingDeploymentStageActive,
			AlgorithmVersion: "balanced-v1", ActualChannelID: 8, ObservedChannelID: 9,
			ObservedMatchesActual: false, DifferenceType: "selection_changed", CreatedTime: 101,
		},
	}).Error)
	require.NoError(t, db.Create(&[]RoutingHedgeAttemptAudit{
		{
			AttemptKey: strings.Repeat("c", 64), DecisionID: "decision-1",
			Role: RoutingHedgeAttemptRolePrimary, State: RoutingHedgeAttemptStateCompleted,
			Result: RoutingHedgeAttemptResultHedgeLost, MemberID: 71, ChannelID: 7, Region: "default",
			CostKnown: true, ExpectedCost: 0.1, WorstCaseCost: 0.2, CostCurrency: "USD", CostUnit: "request",
			StartedTimeMs: 100_000, CompletedTimeMs: 100_020,
		},
		{
			AttemptKey: strings.Repeat("d", 64), DecisionID: "decision-1",
			Role: RoutingHedgeAttemptRoleSecondary, State: RoutingHedgeAttemptStateCompleted,
			Result: RoutingHedgeAttemptResultSuccess, Winner: true, MemberID: 72, ChannelID: 8, Region: "default",
			CostKnown: true, ExpectedCost: 0.11, WorstCaseCost: 0.21, CostCurrency: "USD", CostUnit: "request",
			ActualCostKnown: true, ActualCost: 0.12,
			StartedTimeMs: 100_010, CompletedTimeMs: 100_030,
		},
	}).Error)

	request := RoutingAuditExportRequest{FromTime: 90, ToTime: 110, Limit: 100}
	identity := RoutingOperationRequestIdentity{KeyHash: strings.Repeat("a", 64), PayloadHash: strings.Repeat("b", 64)}
	export, operation, created, err := CreateRoutingAuditExportContext(context.Background(), request, 7, identity)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, RoutingOperationStatusSucceeded, operation.Status)
	assert.Equal(t, 2, export.RecordCount)
	assert.Positive(t, export.ChunkCount)

	stored, payload, err := GetRoutingAuditExportContext(context.Background(), export.ExportID)
	require.NoError(t, err)
	assert.Equal(t, export.ContentHash, stored.ContentHash)
	assert.NotContains(t, string(payload), secret)
	assert.NotContains(t, string(payload), "request_id")
	assert.NotContains(t, string(payload), "credential")
	assert.NotContains(t, string(payload), "replay_input")
	assert.NotContains(t, string(payload), "candidates")
	var items []RoutingAuditExportItem
	require.NoError(t, common.Unmarshal(payload, &items))
	require.Len(t, items, 2)
	assert.Equal(t, "decision-1", items[0].DecisionID)
	assert.Equal(t, 7, items[0].ActualChannelID)
	require.NotNil(t, items[0].Hedge)
	require.NotNil(t, items[0].AttemptTimeline)
	assert.Equal(t, items[0].AttemptTimeline, items[0].Hedge)
	assert.Equal(t, RoutingHedgeAttemptRoleSecondary, items[0].Hedge.WinnerRole)
	assert.Equal(t, 72, items[0].Hedge.FinalMemberID)
	assert.Equal(t, 8, items[0].Hedge.FinalChannelID)

	retryExport, retryOperation, created, err := CreateRoutingAuditExportContext(context.Background(), request, 7, identity)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, export.ExportID, retryExport.ExportID)
	assert.Equal(t, operation.ID, retryOperation.ID)
	_, retryPayload, err := GetRoutingAuditExportContext(context.Background(), retryExport.ExportID)
	require.NoError(t, err)
	assert.Equal(t, payload, retryPayload)
}

func TestRoutingAuditExportLimitKeepsCompleteLogicalRequestTimeline(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&RoutingPolicyHead{}, &RoutingPolicyRevision{}, &RoutingOperation{}, &RoutingDecisionAudit{},
		&RoutingAuditExport{}, &RoutingAuditExportChunk{}, &RoutingHedgeAttemptAudit{},
	))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	requestID := "request-plaintext-must-not-leak"
	requestKey := routingHedgeAuditHash("request", requestID)
	require.NoError(t, db.Create(&[]RoutingDecisionAudit{
		{DecisionID: "decision-primary", RequestID: requestID, CreatedTime: 100},
		{DecisionID: "decision-retry", RequestID: requestID, RetryIndex: 1, CreatedTime: 101},
	}).Error)
	require.NoError(t, db.Create(&[]RoutingHedgeAttemptAudit{
		{
			AttemptKey: strings.Repeat("1", 64), DecisionID: "decision-primary", RequestKey: requestKey,
			ExecutionMode: RoutingAttemptExecutionHedge, AttemptIndex: 0,
			Role: RoutingHedgeAttemptRolePrimary, State: RoutingHedgeAttemptStateCompleted,
			Result: RoutingHedgeAttemptResultHedgeLost, StartedTimeMs: 1_000, CompletedTimeMs: 1_010,
		},
		{
			AttemptKey: strings.Repeat("2", 64), DecisionID: "decision-primary", RequestKey: requestKey,
			ExecutionMode: RoutingAttemptExecutionHedge, AttemptIndex: 1,
			Role: RoutingHedgeAttemptRoleSecondary, State: RoutingHedgeAttemptStateCompleted,
			Result: RoutingHedgeAttemptResultUpstreamError, WillRetry: true,
			StartedTimeMs: 1_005, CompletedTimeMs: 1_015,
		},
		{
			AttemptKey: strings.Repeat("3", 64), DecisionID: "decision-retry", RequestKey: requestKey,
			ExecutionMode: RoutingAttemptExecutionSerial, AttemptIndex: 2,
			Role: RoutingAttemptRoleSerial, State: RoutingHedgeAttemptStateCompleted,
			Result: RoutingHedgeAttemptResultSuccess, Winner: true, FinalAttempt: true,
			StartedTimeMs: 1_020, CompletedTimeMs: 1_030,
		},
	}).Error)

	export, _, _, err := CreateRoutingAuditExportContext(
		context.Background(), RoutingAuditExportRequest{FromTime: 90, ToTime: 110, Limit: 1}, 7,
		RoutingOperationRequestIdentity{KeyHash: strings.Repeat("e", 64), PayloadHash: strings.Repeat("f", 64)},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, export.RecordCount)
	_, payload, err := GetRoutingAuditExportContext(context.Background(), export.ExportID)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), requestID)
	assert.NotContains(t, string(payload), requestKey)

	var items []RoutingAuditExportItem
	require.NoError(t, common.Unmarshal(payload, &items))
	require.Len(t, items, 1)
	assert.Equal(t, "decision-primary", items[0].DecisionID)
	require.NotNil(t, items[0].AttemptTimeline)
	require.NotNil(t, items[0].Hedge)
	assert.Equal(t, items[0].AttemptTimeline, items[0].Hedge)
	require.Len(t, items[0].AttemptTimeline.Attempts, 3)
	assert.Equal(t, RoutingHedgeAttemptRolePrimary, items[0].AttemptTimeline.Attempts[0].Role)
	assert.Equal(t, RoutingHedgeAttemptRoleSecondary, items[0].AttemptTimeline.Attempts[1].Role)
	assert.Equal(t, RoutingAttemptRoleSerial, items[0].AttemptTimeline.Attempts[2].Role)
	assert.Equal(t, 2, items[0].AttemptTimeline.Attempts[2].AttemptIndex)
}

func TestRoutingAuditExportDetectsChunkCorruption(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&RoutingPolicyHead{}, &RoutingPolicyRevision{}, &RoutingOperation{}, &RoutingDecisionAudit{},
		&RoutingAuditExport{}, &RoutingAuditExportChunk{}, &RoutingHedgeAttemptAudit{},
	))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))
	require.NoError(t, db.Create(&RoutingDecisionAudit{DecisionID: "decision-corrupt", CreatedTime: 100}).Error)
	export, _, _, err := CreateRoutingAuditExportContext(
		context.Background(), RoutingAuditExportRequest{FromTime: 90, ToTime: 110, Limit: 10}, 1,
		RoutingOperationRequestIdentity{KeyHash: strings.Repeat("c", 64), PayloadHash: strings.Repeat("d", 64)},
	)
	require.NoError(t, err)
	require.NoError(t, db.Model(&RoutingAuditExportChunk{}).
		Where("export_id = ? AND chunk_index = ?", export.ExportID, 0).
		Update("payload_hash", strings.Repeat("0", 64)).Error)
	_, _, err = GetRoutingAuditExportContext(context.Background(), export.ExportID)
	assert.ErrorIs(t, err, ErrRoutingAuditExportInvalid)
}

func TestRoutingAuditExportChunksPreserveUTF8Boundaries(t *testing.T) {
	payload := []byte(`[{"model_name":"` + strings.Repeat("模型", routingAuditExportChunkMaxBytes) + `"}]`)
	require.LessOrEqual(t, len(payload), RoutingAuditExportMaxBytes)
	chunks, err := newRoutingAuditExportChunks("rae_"+strings.Repeat("e", 32), payload)
	require.NoError(t, err)
	require.Greater(t, len(chunks), 1)
	var rebuilt strings.Builder
	for _, chunk := range chunks {
		assert.LessOrEqual(t, chunk.PayloadBytes, routingAuditExportChunkMaxBytes)
		assert.True(t, utf8.ValidString(chunk.Payload))
		rebuilt.WriteString(chunk.Payload)
	}
	assert.Equal(t, string(payload), rebuilt.String())
}

func TestRoutingAuditExportRejectsUnboundedRequest(t *testing.T) {
	request := RoutingAuditExportRequest{
		FromTime: 1, ToTime: 1 + RoutingAuditExportMaxRangeSeconds + 1, Limit: RoutingAuditExportMaxRecords,
	}
	_, _, _, err := CreateRoutingAuditExportContext(
		context.Background(), request, 1,
		RoutingOperationRequestIdentity{KeyHash: strings.Repeat("f", 64), PayloadHash: strings.Repeat("a", 64)},
	)
	assert.ErrorIs(t, err, ErrRoutingAuditExportInvalid)
}
