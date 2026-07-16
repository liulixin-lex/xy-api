package model

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingHedgeAttemptAuditSQLiteContract(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingHedgeAttemptAuditContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingHedgeAttemptAuditExternalDatabaseCompatibility(t *testing.T) {
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
			runRoutingHedgeAttemptAuditContract(t, db, test.dbType)
		})
	}
}

func runRoutingHedgeAttemptAuditContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	require.NoError(t, db.AutoMigrate(&RoutingHedgeAttemptAudit{}))
	require.NoError(t, db.Where("id > ?", 0).Delete(&RoutingHedgeAttemptAudit{}).Error)

	spec := routingHedgeAttemptStartSpecForTest("request-secret-value", RoutingHedgeAttemptRolePrimary, 101, 1_000)
	audit, err := StartRoutingHedgeAttemptAuditContext(context.Background(), spec)
	require.NoError(t, err)
	assert.NotEqual(t, spec.RequestID, audit.RequestKey)
	assert.NotEqual(t, spec.ModelName, audit.ModelKey)
	assert.NotContains(t, audit.CredentialReferenceHash, fmt.Sprint(spec.CredentialID))
	assert.Equal(t, "https://api.example.test:443", audit.EndpointAuthority)
	assert.False(t, audit.CrossRegion)
	assert.True(t, audit.CostKnown)
	assert.Equal(t, "system_pricing_x_channel_multiplier", audit.PricingBasis)
	assert.Equal(t, spec.Cost.PricingIdentity, audit.PricingIdentity)
	assert.Equal(t, spec.Cost.ConfigurationRevision, audit.ConfigurationRevision)
	assert.Equal(t, spec.Cost.UpstreamCostMultiplier, audit.UpstreamCostMultiplier)
	assert.Equal(t, spec.Cost.BaselineExpectedCost, audit.BaselineExpectedCost)
	assert.Equal(t, spec.Cost.BaselineWorstCaseCost, audit.BaselineWorstCaseCost)
	assert.Empty(t, audit.CostSourceSyncStatus)
	assert.Empty(t, audit.AccountSourceType)
	assert.Empty(t, audit.AccountReferenceHash)
	assert.Equal(t, RoutingHedgeAttemptStateStarted, audit.State)
	assert.Equal(t, RoutingHedgeAttemptResultPending, audit.Result)
	assert.NotContains(t, audit.CostBreakdownJSON, spec.RequestID)

	completed, err := CompleteRoutingHedgeAttemptAuditContext(context.Background(), audit.ID, RoutingHedgeAttemptCompleteSpec{
		Result: RoutingHedgeAttemptResultSuccess, Winner: true, HTTPStatus: 200, UpstreamSent: true,
		CompletedTimeMs: 1_050,
	})
	require.NoError(t, err)
	assert.Equal(t, RoutingHedgeAttemptStateCompleted, completed.State)
	assert.Equal(t, int64(50), completed.DurationMs)
	assert.True(t, completed.Winner)

	idempotent, err := CompleteRoutingHedgeAttemptAuditContext(context.Background(), audit.ID, RoutingHedgeAttemptCompleteSpec{
		Result: RoutingHedgeAttemptResultSuccess, Winner: true, HTTPStatus: 200, UpstreamSent: true,
		CompletedTimeMs: 1_060,
	})
	require.NoError(t, err)
	assert.Equal(t, completed.ID, idempotent.ID)

	_, err = CompleteRoutingHedgeAttemptAuditContext(context.Background(), audit.ID, RoutingHedgeAttemptCompleteSpec{
		Result: RoutingHedgeAttemptResultHedgeLost, HTTPStatus: 0, CompletedTimeMs: 1_060,
	})
	assert.ErrorIs(t, err, ErrRoutingHedgeAttemptTransition)
}

func TestRoutingHedgeAttemptAuditAcceptsCurrentSystemPricingForSerialAndHedge(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingHedgeAttemptAudit{}))

	tests := []struct {
		name          string
		executionMode string
		role          string
	}{
		{name: "serial", executionMode: RoutingAttemptExecutionSerial, role: RoutingAttemptRoleSerial},
		{name: "hedge", executionMode: RoutingAttemptExecutionHedge, role: RoutingHedgeAttemptRolePrimary},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := routingHedgeAttemptStartSpecForTest(
				"current-system-pricing-"+test.name,
				test.role,
				201+index,
				1_000+int64(index),
			)
			spec.ExecutionMode = test.executionMode
			audit, err := StartRoutingHedgeAttemptAuditContext(context.Background(), spec)
			require.NoError(t, err)
			assert.Equal(t, RoutingHedgeAttemptAuditSchemaVersion, audit.SchemaVersion)
			assert.Equal(t, RoutingHedgeCostAuditSchemaVersion, audit.CostSchemaVersion)
			assert.Equal(t, test.executionMode, audit.ExecutionMode)
			assert.Equal(t, test.role, audit.Role)
			assert.True(t, audit.CostKnown)
			assert.Equal(t, spec.Cost.PricingIdentity, audit.PricingIdentity)
			assert.Equal(t, spec.Cost.ConfigurationRevision, audit.ConfigurationRevision)
		})
	}
}

func TestRoutingHedgeAttemptAuditRejectsSensitiveEndpointAndUnknownCost(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingHedgeAttemptAudit{}))

	spec := routingHedgeAttemptStartSpecForTest("request-sensitive", RoutingHedgeAttemptRolePrimary, 101, 1_000)
	spec.EndpointAuthority = "https://secret@example.test/path"
	_, err := StartRoutingHedgeAttemptAuditContext(context.Background(), spec)
	assert.ErrorIs(t, err, ErrRoutingHedgeAttemptInvalid)

	spec = routingHedgeAttemptStartSpecForTest("request-cost", RoutingHedgeAttemptRolePrimary, 101, 1_000)
	spec.Cost.PricingHash = ""
	_, err = StartRoutingHedgeAttemptAuditContext(context.Background(), spec)
	assert.ErrorIs(t, err, ErrRoutingHedgeAttemptInvalid)
}

func TestRoutingHedgeAttemptAuditRetentionUsesBatchesAndKeepsStarted(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingHedgeAttemptAudit{}))
	require.NoError(t, db.Where("id > ?", 0).Delete(&RoutingHedgeAttemptAudit{}).Error)

	base, err := StartRoutingHedgeAttemptAuditContext(
		context.Background(),
		routingHedgeAttemptStartSpecForTest("retention-base", RoutingHedgeAttemptRolePrimary, 101, 100),
	)
	require.NoError(t, err)
	oldRows := make([]RoutingHedgeAttemptAudit, 0, routingHedgeAttemptRetentionBatch+1)
	for index := 0; index < routingHedgeAttemptRetentionBatch+1; index++ {
		row := base
		row.ID = 0
		row.AttemptKey = routingHedgeAuditHash("retention-attempt", fmt.Sprint(index))
		row.RequestKey = routingHedgeAuditHash("retention-request", fmt.Sprint(index))
		row.State = RoutingHedgeAttemptStateCompleted
		row.Result = RoutingHedgeAttemptResultUpstreamError
		row.CompletedTimeMs = 200
		row.UpdatedTimeMs = 200
		oldRows = append(oldRows, row)
	}
	require.NoError(t, db.CreateInBatches(oldRows, 100).Error)

	deleted, err := DeleteRoutingHedgeAttemptAuditsBeforeContext(context.Background(), 300)
	require.NoError(t, err)
	assert.Equal(t, int64(routingHedgeAttemptRetentionBatch+1), deleted)
	var remaining []RoutingHedgeAttemptAudit
	require.NoError(t, db.Order("id ASC").Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, base.ID, remaining[0].ID)
	assert.Equal(t, RoutingHedgeAttemptStateStarted, remaining[0].State)
}

func TestBuildRoutingHedgeDecisionAuditSummaryUsesSecondaryActualCostSemantics(t *testing.T) {
	primaryKnown := RoutingHedgeAttemptSummary{
		Role: RoutingHedgeAttemptRolePrimary, State: RoutingHedgeAttemptStateCompleted,
		Result: RoutingHedgeAttemptResultSuccess, Winner: true, MemberID: 11, ChannelID: 101, Region: "default",
		CostKnown: true, ExpectedCost: 0.1, WorstCaseCost: 0.2, CostCurrency: "USD", CostUnit: "request",
		ActualCostKnown: true, ActualCost: 0.15,
	}
	secondaryKnown := RoutingHedgeAttemptSummary{
		Role: RoutingHedgeAttemptRoleSecondary, State: RoutingHedgeAttemptStateCompleted,
		Result: RoutingHedgeAttemptResultHedgeLost, MemberID: 12, ChannelID: 102, Region: "default",
		CostKnown: true, ExpectedCost: 0.2, WorstCaseCost: 0.3, CostCurrency: "USD", CostUnit: "request",
		ActualCostKnown: true, ActualCost: 0.25,
	}
	secondaryUnknown := secondaryKnown
	secondaryUnknown.ActualCostKnown = false
	secondaryUnknown.ActualCost = 0

	tests := []struct {
		name                 string
		rows                 []RoutingHedgeAttemptSummary
		wantDuplicateKnown   bool
		wantDuplicateActual  float64
		wantTotalActualKnown bool
	}{
		{
			name: "single primary has no duplicate actual cost", rows: []RoutingHedgeAttemptSummary{primaryKnown},
			wantTotalActualKnown: true,
		},
		{
			name: "primary unknown does not hide known secondary duplicate cost",
			rows: func() []RoutingHedgeAttemptSummary {
				primary := primaryKnown
				primary.ActualCostKnown = false
				primary.ActualCost = 0
				return []RoutingHedgeAttemptSummary{primary, secondaryKnown}
			}(),
			wantDuplicateKnown: true, wantDuplicateActual: 0.25,
		},
		{
			name: "unknown secondary keeps duplicate actual cost unknown",
			rows: []RoutingHedgeAttemptSummary{primaryKnown, secondaryUnknown},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := buildRoutingHedgeDecisionAuditSummary(test.rows)
			assert.Equal(t, test.wantDuplicateKnown, summary.DuplicateActualCostKnown)
			assert.Equal(t, test.wantDuplicateActual, summary.DuplicateActualCost)
			assert.Equal(t, test.wantTotalActualKnown, summary.ActualTotalCostKnown)
		})
	}

	summary := buildRoutingHedgeDecisionAuditSummary([]RoutingHedgeAttemptSummary{primaryKnown, secondaryKnown})
	assert.Equal(t, RoutingHedgeAttemptRolePrimary, summary.WinnerRole)
	assert.Equal(t, 11, summary.FinalMemberID)
	assert.Equal(t, 101, summary.FinalChannelID)
	assert.Equal(t, "default", summary.FinalRegion)
}

func TestBuildRoutingHedgeDecisionAuditSummaryIsConservativeWhenAttemptsAreTruncated(t *testing.T) {
	rows := make([]RoutingHedgeAttemptSummary, routingHedgeAttemptsPerDecision+1)
	for index := range rows {
		rows[index] = RoutingHedgeAttemptSummary{
			Role: RoutingAttemptRoleSerial, State: RoutingHedgeAttemptStateCompleted,
			CostKnown: true, ActualCostKnown: true, CostCurrency: "USD", CostUnit: "request",
		}
	}

	summary := buildRoutingHedgeDecisionAuditSummary(rows)

	assert.True(t, summary.AttemptsTruncated)
	assert.False(t, summary.AllAttemptsCompleted)
	assert.False(t, summary.EstimatedTotalCostKnown)
	assert.False(t, summary.WorstCaseTotalCostKnown)
	assert.False(t, summary.ActualTotalCostKnown)
}

func TestGetRoutingHedgeDecisionAuditFallbackIsIsolatedByRequestHash(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingHedgeAttemptAudit{}))
	require.NoError(t, db.Create(&[]RoutingHedgeAttemptAudit{
		{
			AttemptKey: routingHedgeAuditHash("attempt", "other"),
			RequestKey: routingHedgeAuditHash("request", "other-request"),
			Role:       RoutingHedgeAttemptRolePrimary, State: RoutingHedgeAttemptStateCompleted,
			Result: RoutingHedgeAttemptResultSuccess, Winner: true, MemberID: 11, ChannelID: 101,
			StartedTimeMs: 100, CompletedTimeMs: 110,
		},
		{
			AttemptKey: routingHedgeAuditHash("attempt", "target"),
			RequestKey: routingHedgeAuditHash("request", "target-request"),
			Role:       RoutingHedgeAttemptRolePrimary, State: RoutingHedgeAttemptStateCompleted,
			Result: RoutingHedgeAttemptResultSuccess, Winner: true, MemberID: 12, ChannelID: 102,
			StartedTimeMs: 120, CompletedTimeMs: 130,
		},
	}).Error)

	summary, err := GetRoutingHedgeDecisionAuditContext(context.Background(), "", "target-request")

	require.NoError(t, err)
	require.Len(t, summary.Attempts, 1)
	assert.Equal(t, 102, summary.FinalChannelID)
	assert.Equal(t, 102, summary.Attempts[0].ChannelID)
}

func routingHedgeAttemptStartSpecForTest(
	requestID string,
	role string,
	channelID int,
	startedAt int64,
) RoutingHedgeAttemptStartSpec {
	return RoutingHedgeAttemptStartSpec{
		RequestID: requestID, NodeEpochID: strings.Repeat("a", 32),
		PolicyRevision: 7, AlgorithmVersion: "balanced-v1", PoolID: 3, MemberID: channelID + 10,
		ChannelID: channelID, CredentialID: channelID + 20, ModelName: "gpt-test", Role: role,
		ExecutionMode:     RoutingAttemptExecutionHedge,
		EndpointAuthority: "HTTPS://API.EXAMPLE.TEST:443", Region: "default", StartedTimeMs: startedAt,
		Cost: RoutingHedgeAttemptCostSpec{
			Known:        true,
			ExpectedCost: 0.001, WorstCaseCost: 0.002, EffectiveCost: 0.0012,
			Currency: "USD", Unit: "request", PricingBasis: "system_pricing_x_channel_multiplier",
			PricingHash: routingHedgeAuditHash("pricing", "v1"), PricingVersion: "pricing-v1",
			PricingIdentity:       "billing:" + routingHedgeAuditHash("pricing", "v1") + ":channel-config:7",
			ConfigurationRevision: 7, UpstreamCostMultiplier: 1.5,
			BaselineExpectedKnown: true, BaselineExpectedCost: 0.0005,
			BaselineWorstCaseKnown: true, BaselineWorstCaseCost: 0.001,
			ConfidenceScore: 1, FreshnessScore: 1,
			ExpectedBreakdown:    RoutingCostBreakdown{Input: 0.0004, Output: 0.0006, Total: 0.001},
			WorstSingleBreakdown: RoutingCostBreakdown{Input: 0.0008, Output: 0.0012, Total: 0.002},
			ObservedTime:         900, EffectiveTime: 900, ExpiresTime: 2_000,
		},
	}
}
