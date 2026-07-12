package model

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingCanaryEvaluationIsContentAddressedAndImmutable(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingCanaryEvaluationContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingCanaryEvaluationConcurrentCreateHasOneRow(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingCanaryEvaluation{}))

	const writers = 2
	start := make(chan struct{})
	results := make([]RoutingCanaryEvaluation, writers)
	created := make([]bool, writers)
	errs := make([]error, writers)
	var wait sync.WaitGroup
	wait.Add(writers)
	for index := 0; index < writers; index++ {
		go func(index int) {
			defer wait.Done()
			<-start
			spec := routingCanaryEvaluationSpecForTest()
			if index > 0 {
				spec.Canary.AttemptCount++
				spec.Canary.RetryCount++
			}
			results[index], created[index], errs[index] = CreateRoutingCanaryEvaluationContext(
				context.Background(), spec,
			)
		}(index)
	}
	close(start)
	wait.Wait()

	createdCount := 0
	for index := range results {
		require.NoError(t, errs[index])
		if created[index] {
			createdCount++
		}
		assert.Equal(t, results[0].ID, results[index].ID)
		assert.Equal(t, results[0].EvaluationHash, results[index].EvaluationHash)
	}
	assert.Equal(t, 1, createdCount)
	var count int64
	require.NoError(t, db.Model(&RoutingCanaryEvaluation{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestRoutingCanaryEvaluationExternalDatabaseCompatibility(t *testing.T) {
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
			runRoutingCanaryEvaluationContract(t, db, test.dbType)
		})
	}
}

func TestRoutingCanaryEvaluationWindowUniqueIndexUpgrade(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingCanaryEvaluationWindowUniqueIndexUpgradeContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingCanaryEvaluationWindowUniqueIndexUpgradeExternalDatabaseCompatibility(t *testing.T) {
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
			runRoutingCanaryEvaluationWindowUniqueIndexUpgradeContract(t, db, test.dbType)
		})
	}
}

func TestRoutingCanaryEvaluationWindowUniqueIndexPreflightRejectsHistoricalDuplicates(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	prepareLegacyRoutingCanaryEvaluationWindowIndex(t, db)

	firstSpec := routingCanaryEvaluationSpecForTest()
	secondSpec := firstSpec
	secondSpec.Canary.AttemptCount++
	secondSpec.Canary.RetryCount++
	for index, spec := range []RoutingCanaryEvaluationSpec{firstSpec, secondSpec} {
		normalized, evaluationHash, err := normalizeRoutingCanaryEvaluationSpec(spec)
		require.NoError(t, err)
		row := routingCanaryEvaluationFromSpec(normalized)
		row.EvaluationHash = evaluationHash
		row.CreateToken = strings.Repeat(string(rune('a'+index)), 32)
		row.CreatedTimeMs = int64(index + 1)
		require.NoError(t, db.Create(&row).Error)
	}

	err := prepareRoutingCanaryEvaluationWindowUniqueIndex(db)
	assert.ErrorIs(t, err, ErrRoutingCanaryEvaluationWindowConflict)
	var conflict *RoutingCanaryEvaluationWindowConflictError
	require.True(t, errors.As(err, &conflict))
	assert.Equal(t, firstSpec.RolloutKey, conflict.RolloutKey)
	assert.Equal(t, firstSpec.PoolID, conflict.PoolID)
	assert.Equal(t, firstSpec.WindowStartMs, conflict.WindowStartMs)
	assert.Equal(t, firstSpec.WindowEndMs, conflict.WindowEndMs)
	assert.Equal(t, int64(2), conflict.Count)
	assert.False(t, db.Migrator().HasIndex(&RoutingCanaryEvaluation{}, routingCanaryEvaluationWindowUniqueIndex))
}

func runRoutingCanaryEvaluationWindowUniqueIndexUpgradeContract(
	t *testing.T,
	db *gorm.DB,
	dbType common.DatabaseType,
) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	prepareLegacyRoutingCanaryEvaluationWindowIndex(t, db)
	require.True(t, db.Migrator().HasIndex(&RoutingCanaryEvaluation{}, "idx_routing_canary_evaluation_window"))
	require.False(t, db.Migrator().HasIndex(&RoutingCanaryEvaluation{}, routingCanaryEvaluationWindowUniqueIndex))

	require.NoError(t, prepareRoutingCanaryEvaluationWindowUniqueIndex(db))
	require.NoError(t, db.AutoMigrate(&RoutingCanaryEvaluation{}))
	require.True(t, db.Migrator().HasIndex(&RoutingCanaryEvaluation{}, routingCanaryEvaluationWindowUniqueIndex))

	firstSpec := routingCanaryEvaluationSpecForTest()
	first, created, err := CreateRoutingCanaryEvaluationContext(context.Background(), firstSpec)
	require.NoError(t, err)
	require.True(t, created)
	secondSpec := firstSpec
	secondSpec.Canary.AttemptCount++
	secondSpec.Canary.RetryCount++
	second, created, err := CreateRoutingCanaryEvaluationContext(context.Background(), secondSpec)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, first.EvaluationHash, second.EvaluationHash)
	var count int64
	require.NoError(t, db.Model(&RoutingCanaryEvaluation{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func prepareLegacyRoutingCanaryEvaluationWindowIndex(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&RoutingCanaryEvaluation{}))
	require.NoError(t, db.Migrator().DropIndex(&RoutingCanaryEvaluation{}, routingCanaryEvaluationWindowUniqueIndex))
	require.NoError(t, db.AutoMigrate(&routingCanaryEvaluationLegacyWindowIndex{}))
}

type routingCanaryEvaluationLegacyWindowIndex struct {
	RolloutKey    string `gorm:"type:char(64);index:idx_routing_canary_evaluation_window,priority:1"`
	PoolID        int    `gorm:"index:idx_routing_canary_evaluation_window,priority:2"`
	WindowStartMs int64  `gorm:"bigint;index:idx_routing_canary_evaluation_window,priority:3"`
	WindowEndMs   int64  `gorm:"bigint;index:idx_routing_canary_evaluation_window,priority:4"`
}

func (routingCanaryEvaluationLegacyWindowIndex) TableName() string {
	return "routing_canary_evaluations"
}

func runRoutingCanaryEvaluationContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	require.NoError(t, db.AutoMigrate(&RoutingCanaryEvaluation{}))

	spec := routingCanaryEvaluationSpecForTest()
	first, created, err := CreateRoutingCanaryEvaluationContext(context.Background(), spec)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotZero(t, first.ID)
	assert.Len(t, first.EvaluationHash, 64)

	retry, created, err := CreateRoutingCanaryEvaluationContext(context.Background(), spec)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, retry.ID)
	assert.Equal(t, first.EvaluationHash, retry.EvaluationHash)
	formatted := spec
	formatted.RolloutKey = strings.ToUpper(formatted.RolloutKey)
	formatted.Reason = "  " + formatted.Reason + "  "
	normalizedRetry, created, err := CreateRoutingCanaryEvaluationContext(context.Background(), formatted)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, normalizedRetry.ID)

	changed := spec
	changed.Canary.AttemptCount++
	changed.Canary.RetryCount++
	firstWriterWins, created, err := CreateRoutingCanaryEvaluationContext(context.Background(), changed)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, firstWriterWins.ID)
	assert.Equal(t, first.EvaluationHash, firstWriterWins.EvaluationHash)

	changed.WindowStartMs = spec.WindowEndMs
	changed.WindowEndMs = changed.WindowStartMs + (spec.WindowEndMs - spec.WindowStartMs)
	second, created, err := CreateRoutingCanaryEvaluationContext(context.Background(), changed)
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, first.EvaluationHash, second.EvaluationHash)

	var count int64
	require.NoError(t, db.Model(&RoutingCanaryEvaluation{}).Count(&count).Error)
	assert.Equal(t, int64(2), count)

	loaded, err := GetRoutingCanaryEvaluationWindowContext(
		context.Background(), spec.RolloutKey, spec.PoolID, spec.WindowStartMs, spec.WindowEndMs,
	)
	require.NoError(t, err)
	assert.Equal(t, first.ID, loaded.ID)
	previous, err := ListRoutingCanaryEvaluationsBeforeContext(
		context.Background(), spec.RolloutKey, spec.PoolID, second.WindowEndMs+1, 2,
	)
	require.NoError(t, err)
	require.Len(t, previous, 2)
	assert.Equal(t, second.ID, previous[0].ID)
	assert.Equal(t, first.ID, previous[1].ID)

	invalidSpecs := []RoutingCanaryEvaluationSpec{spec, spec, spec, spec}
	invalidSpecs[0].CanarySuccessRateBasisPoints++
	invalidSpecs[1].RolloutKey = "not-a-rollout-hash"
	invalidSpecs[2].Canary.ExpectedCostTotal = math.NaN()
	invalidSpecs[3].Canary.P95TTFTMilliseconds = math.Inf(1)
	for index := range invalidSpecs {
		_, _, err = CreateRoutingCanaryEvaluationContext(context.Background(), invalidSpecs[index])
		assert.ErrorIs(t, err, ErrRoutingCanaryEvaluationInvalid)
	}

	err = db.Model(&RoutingCanaryEvaluation{}).
		Where("id = ?", first.ID).
		Update("reason", "tampered").Error
	assert.ErrorIs(t, err, ErrRoutingCanaryEvaluationImmutable)
	err = db.Where("id = ?", first.ID).Delete(&RoutingCanaryEvaluation{}).Error
	assert.ErrorIs(t, err, ErrRoutingCanaryEvaluationImmutable)

	require.NoError(t, db.Exec(
		"UPDATE routing_canary_evaluations SET reason = ? WHERE id = ?", "raw tamper", first.ID,
	).Error)
	_, err = GetRoutingCanaryEvaluationWindowContext(
		context.Background(), spec.RolloutKey, spec.PoolID, spec.WindowStartMs, spec.WindowEndMs,
	)
	assert.ErrorIs(t, err, ErrRoutingCanaryEvaluationInvalid)
}

func routingCanaryEvaluationSpecForTest() RoutingCanaryEvaluationSpec {
	return RoutingCanaryEvaluationSpec{
		PolicyRevision: 11,
		ActivationID:   21,
		PoolID:         31,
		RolloutKey:     "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		WindowStartMs:  1_000,
		WindowEndMs:    61_000,
		Control: RoutingCanaryCohortMetrics{
			RequestCount:        1_000,
			SuccessCount:        990,
			TTFTSampleCount:     950,
			P95TTFTMilliseconds: 800,
			CostSampleCount:     900,
			ExpectedCostTotal:   1.8,
			AttemptCount:        1_050,
			RetryCount:          50,
		},
		Canary: RoutingCanaryCohortMetrics{
			RequestCount:        100,
			SuccessCount:        94,
			TTFTSampleCount:     90,
			P95TTFTMilliseconds: 1_200,
			CostSampleCount:     88,
			ExpectedCostTotal:   0.22,
			AttemptCount:        110,
			RetryCount:          10,
		},
		NodeCoverageBasisPoints:            9_500,
		CostCoverageBasisPoints:            8_800,
		ControlSuccessRateBasisPoints:      9_900,
		CanarySuccessRateBasisPoints:       9_400,
		SuccessRateDropBasisPoints:         500,
		P95TTFTRatioBasisPoints:            15_000,
		P95TTFTDeltaMilliseconds:           400,
		CostRatioBasisPoints:               12_500,
		RetryAmplificationRatioBasisPoints: 20_000,
		TrafficBasisPoints:                 250,
		Status:                             RoutingCanaryEvaluationStatusBreached,
		Reason:                             "retry amplification exceeded policy",
	}
}
