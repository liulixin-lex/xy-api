package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingAttemptWindowMetricsSQLiteContract(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingHedgeAttemptAudit{}))

	rows := []RoutingHedgeAttemptAudit{
		routingAttemptMetricRow("retry-success", 0, RoutingAttemptExecutionSerial, true, false, RoutingHedgeAttemptResultUpstreamError, true, 0.20, "USD", "request"),
		routingAttemptMetricRow("retry-success", 1, RoutingAttemptExecutionSerial, false, true, RoutingHedgeAttemptResultSuccess, true, 0.30, "USD", "request"),
		routingAttemptMetricRow("retry-failure", 0, RoutingAttemptExecutionSerial, true, false, RoutingHedgeAttemptResultUpstreamError, true, 0.40, "USD", "request"),
		routingAttemptMetricRow("retry-failure", 1, RoutingAttemptExecutionSerial, false, true, RoutingHedgeAttemptResultUpstreamError, true, 0.50, "USD", "request"),
		routingAttemptMetricRow("hedge-cost", 0, RoutingAttemptExecutionHedge, false, true, RoutingHedgeAttemptResultSuccess, true, 0.60, "USD", "request"),
		routingAttemptMetricRow("hedge-cost", 1, RoutingAttemptExecutionHedge, false, false, RoutingHedgeAttemptResultHedgeLost, true, 0.70, "USD", "request"),
	}
	require.NoError(t, db.Create(&rows).Error)

	metrics, err := GetRoutingAttemptWindowMetricsContext(context.Background(), 1_000, 121_000)
	require.NoError(t, err)
	assert.True(t, metrics.PreCommitFailover.Known)
	assert.Equal(t, int64(1), metrics.PreCommitFailover.Numerator)
	assert.Equal(t, int64(2), metrics.PreCommitFailover.Denominator)
	assert.Equal(t, int64(2), metrics.PreCommitFailover.Covered)
	assert.InDelta(t, 0.5, metrics.PreCommitFailover.Rate, 1e-12)
	assert.InDelta(t, 1.0, metrics.PreCommitFailover.Coverage, 1e-12)

	assert.True(t, metrics.UnitPlatformCost.Known)
	assert.Equal(t, int64(3), metrics.UnitPlatformCost.RequestCount)
	assert.Equal(t, int64(6), metrics.UnitPlatformCost.SentAttempts)
	assert.Equal(t, int64(6), metrics.UnitPlatformCost.KnownAttempts)
	assert.Zero(t, metrics.UnitPlatformCost.UnknownAttempts)
	assert.InDelta(t, 2.70, metrics.UnitPlatformCost.TotalPlatformCost, 1e-12)
	assert.InDelta(t, 0.90, metrics.UnitPlatformCost.Value, 1e-12)
	assert.Equal(t, "USD", metrics.UnitPlatformCost.Currency)
	assert.Equal(t, "request", metrics.UnitPlatformCost.Unit)
	assert.True(t, metrics.UnitPlatformCost.DimensionConsistent)
}

func TestRoutingAttemptWindowMetricsKeepsIncompleteFailoverCoverageUnknown(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingHedgeAttemptAudit{}))

	row := routingAttemptMetricRow(
		"unfinished-retry", 0, RoutingAttemptExecutionSerial, true, false,
		RoutingHedgeAttemptResultUpstreamError, true, 0.25, "USD", "request",
	)
	require.NoError(t, db.Create(&row).Error)

	metrics, err := GetRoutingAttemptWindowMetricsContext(context.Background(), 1_000, 121_000)
	require.NoError(t, err)
	assert.False(t, metrics.PreCommitFailover.Known)
	assert.Equal(t, int64(1), metrics.PreCommitFailover.Denominator)
	assert.Zero(t, metrics.PreCommitFailover.Numerator)
	assert.Zero(t, metrics.PreCommitFailover.Covered)
	assert.Zero(t, metrics.PreCommitFailover.Coverage)
}

func TestRoutingAttemptWindowMetricsDoesNotTreatUnknownOrInvalidCostAsZero(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*RoutingHedgeAttemptAudit)
		coverage float64
	}{
		{
			name: "unknown",
			mutate: func(row *RoutingHedgeAttemptAudit) {
				row.ActualCostKnown = false
				row.ActualCost = 0
			},
		},
		{
			name: "negative",
			mutate: func(row *RoutingHedgeAttemptAudit) {
				row.ActualCost = -1
			},
		},
		{
			name: "unreasonably large",
			mutate: func(row *RoutingHedgeAttemptAudit) {
				row.ActualCost = routingAttemptMaximumPlatformCost + 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openRoutingSQLiteTestDB(t)
			withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
			require.NoError(t, db.AutoMigrate(&RoutingHedgeAttemptAudit{}))
			row := routingAttemptMetricRow(
				"invalid-cost", 0, RoutingAttemptExecutionSerial, false, true,
				RoutingHedgeAttemptResultSuccess, true, 0.25, "USD", "request",
			)
			test.mutate(&row)
			require.NoError(t, db.Create(&row).Error)

			metrics, err := GetRoutingAttemptWindowMetricsContext(context.Background(), 1_000, 121_000)
			require.NoError(t, err)
			assert.False(t, metrics.UnitPlatformCost.Known)
			assert.Equal(t, int64(1), metrics.UnitPlatformCost.RequestCount)
			assert.Equal(t, int64(1), metrics.UnitPlatformCost.SentAttempts)
			assert.Zero(t, metrics.UnitPlatformCost.KnownAttempts)
			assert.Equal(t, int64(1), metrics.UnitPlatformCost.UnknownAttempts)
			assert.Zero(t, metrics.UnitPlatformCost.Value)
			assert.Zero(t, metrics.UnitPlatformCost.TotalPlatformCost)
		})
	}
}

func TestRoutingAttemptWindowMetricsRejectsMixedCostDimensions(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingHedgeAttemptAudit{}))

	rows := []RoutingHedgeAttemptAudit{
		routingAttemptMetricRow("usd-request", 0, RoutingAttemptExecutionSerial, false, true, RoutingHedgeAttemptResultSuccess, true, 0.25, "USD", "request"),
		routingAttemptMetricRow("eur-token", 0, RoutingAttemptExecutionSerial, false, true, RoutingHedgeAttemptResultSuccess, true, 0.50, "EUR", "token"),
	}
	require.NoError(t, db.Create(&rows).Error)

	metrics, err := GetRoutingAttemptWindowMetricsContext(context.Background(), 1_000, 121_000)
	require.NoError(t, err)
	assert.False(t, metrics.UnitPlatformCost.Known)
	assert.False(t, metrics.UnitPlatformCost.DimensionConsistent)
	assert.Equal(t, int64(2), metrics.UnitPlatformCost.KnownAttempts)
	assert.Zero(t, metrics.UnitPlatformCost.UnknownAttempts)
	assert.InDelta(t, 1.0, metrics.UnitPlatformCost.Coverage, 1e-12)
	assert.Empty(t, metrics.UnitPlatformCost.Currency)
	assert.Empty(t, metrics.UnitPlatformCost.Unit)
	assert.Zero(t, metrics.UnitPlatformCost.Value)
	assert.Zero(t, metrics.UnitPlatformCost.TotalPlatformCost)
}

func routingAttemptMetricRow(
	requestID string,
	attemptIndex int,
	executionMode string,
	willRetry bool,
	finalAttempt bool,
	result string,
	actualCostKnown bool,
	actualCost float64,
	currency string,
	unit string,
) RoutingHedgeAttemptAudit {
	row := RoutingHedgeAttemptAudit{
		AttemptKey:              routingHedgeAuditHash("metrics-attempt", requestID+string(rune('a'+attemptIndex))+executionMode),
		RequestKey:              routingHedgeAuditHash("request", requestID),
		NodeEpochID:             "0123456789abcdef0123456789abcdef",
		SchemaVersion:           RoutingHedgeAttemptAuditSchemaVersion,
		PolicyRevision:          1,
		AlgorithmVersion:        "balanced-v1",
		PoolID:                  1,
		MemberID:                1,
		ChannelID:               1,
		CredentialReferenceHash: routingHedgeAuditHash("credential", requestID),
		ModelKey:                routingHedgeAuditHash("model", "model-a"),
		ExecutionMode:           executionMode,
		AttemptIndex:            attemptIndex,
		Role:                    RoutingAttemptRoleSerial,
		State:                   RoutingHedgeAttemptStateCompleted,
		Result:                  result,
		CostCurrency:            currency,
		CostUnit:                unit,
		ActualCostKnown:         actualCostKnown,
		ActualCost:              actualCost,
		UpstreamSent:            true,
		WillRetry:               willRetry,
		FinalAttempt:            finalAttempt,
		StartedTimeMs:           2_000 + int64(attemptIndex),
		CompletedTimeMs:         3_000 + int64(attemptIndex),
		CreatedTimeMs:           2_000 + int64(attemptIndex),
		UpdatedTimeMs:           3_000 + int64(attemptIndex),
	}
	if executionMode == RoutingAttemptExecutionHedge {
		row.Role = RoutingHedgeAttemptRolePrimary
		if attemptIndex > 0 {
			row.Role = RoutingHedgeAttemptRoleSecondary
		}
	}
	return row
}
