package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutingOperationNormalizesLegacyPostgresCharPadding(t *testing.T) {
	spec := routingOperationSpecForTest()
	normalized, idempotencyHash, err := normalizeRoutingOperationSpec(spec)
	require.NoError(t, err)
	paddedEmptyHash := strings.Repeat(" ", 64)
	paddedEmptyToken := strings.Repeat(" ", 32)
	paddedRequestKey := paddedEmptyHash
	operation := RoutingOperation{
		ID: 1, OperationType: normalized.Type, IdempotencyHash: idempotencyHash,
		RequestKeyHash: &paddedRequestKey, RequestPayloadHash: paddedEmptyHash,
		CreateToken: strings.Repeat("a", 32), EvaluationHash: normalized.EvaluationHash,
		PoolID: normalized.PoolID, ExpectedRevision: normalized.ExpectedRevision,
		ExpectedActivationID: normalized.ExpectedActivationID, ActorID: normalized.ActorID, Reason: normalized.Reason,
		Status: RoutingOperationStatusPending, ClaimToken: paddedEmptyToken,
		ResultPayloadHash: paddedEmptyHash, CreatedTimeMs: 1, UpdatedTimeMs: 1,
	}

	require.NoError(t, operation.AfterFind(nil))
	require.Nil(t, operation.RequestKeyHash)
	require.Empty(t, operation.RequestPayloadHash)
	require.Empty(t, operation.ClaimToken)
	require.Empty(t, operation.ResultPayloadHash)
	require.NoError(t, validateStoredRoutingOperation(operation))
}
