package model

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingOperationExternalDatabaseCompatibility(t *testing.T) {
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
			require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
			_, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
			require.NoError(t, err)
			claimed, err := ClaimRoutingOperationContext(
				context.Background(), RoutingOperationTypeCanaryAutoRollback, 1_000, 100,
			)
			require.NoError(t, err)
			require.NotNil(t, claimed)
			require.NoError(t, RetryRoutingOperationContext(
				context.Background(), claimed.ID, claimed.ClaimToken, 1_050, 1_100, errors.New("retry"),
			))
			recovered, err := ClaimRoutingOperationContext(
				context.Background(), RoutingOperationTypeCanaryAutoRollback, 1_100, 100,
			)
			require.NoError(t, err)
			require.NotNil(t, recovered)
			require.NoError(t, SupersedeRoutingOperationContext(
				context.Background(), recovered.ID, recovered.ClaimToken, 1_150, "head changed",
			))
		})
	}
}

func TestRoutingOperationIsIdempotentAndClaimIsCAS(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))

	spec := routingOperationSpecForTest()
	first, created, err := CreateRoutingOperationContext(context.Background(), spec)
	require.NoError(t, err)
	assert.True(t, created)
	retry, created, err := CreateRoutingOperationContext(context.Background(), spec)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, retry.ID)

	const claimers = 2
	start := make(chan struct{})
	claimed := make([]*RoutingOperation, claimers)
	errs := make([]error, claimers)
	var wait sync.WaitGroup
	wait.Add(claimers)
	for index := 0; index < claimers; index++ {
		go func(index int) {
			defer wait.Done()
			<-start
			claimed[index], errs[index] = ClaimRoutingOperationContext(
				context.Background(), RoutingOperationTypeCanaryAutoRollback, 1_000, 100,
			)
		}(index)
	}
	close(start)
	wait.Wait()

	winners := 0
	for index := range claimed {
		require.NoError(t, errs[index])
		if claimed[index] != nil {
			winners++
			assert.Equal(t, RoutingOperationStatusRunning, claimed[index].Status)
			assert.Len(t, claimed[index].ClaimToken, 32)
			assert.Equal(t, 1, claimed[index].Attempts)
		}
	}
	assert.Equal(t, 1, winners)
}

func TestRoutingOperationExpiredClaimIsRecoverableAndFenced(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	_, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
	require.NoError(t, err)

	first, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, 1_000, 100,
	)
	require.NoError(t, err)
	require.NotNil(t, first)

	recovered, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, 1_101, 100,
	)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	assert.NotEqual(t, first.ClaimToken, recovered.ClaimToken)
	assert.Equal(t, 2, recovered.Attempts)

	result := RoutingOperationResult{Revision: 12, ActivationID: 22, OutboxID: 32}
	err = SucceedRoutingOperationContext(context.Background(), first.ID, first.ClaimToken, 1_102, result)
	assert.ErrorIs(t, err, ErrRoutingOperationClaimLost)
	require.NoError(t, SucceedRoutingOperationContext(
		context.Background(), recovered.ID, recovered.ClaimToken, 1_102, result,
	))

	claimed, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, 2_000, 100,
	)
	require.NoError(t, err)
	assert.Nil(t, claimed)
}

func TestRoutingOperationRetryAndTerminalTransitionsAreCAS(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))

	operation, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
	require.NoError(t, err)
	claimed, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, 1_000, 200,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, RetryRoutingOperationContext(
		context.Background(), operation.ID, claimed.ClaimToken, 1_050, 1_100, errors.New("transient"),
	))

	notDue, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, 1_099, 100,
	)
	require.NoError(t, err)
	assert.Nil(t, notDue)
	due, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, 1_100, 100,
	)
	require.NoError(t, err)
	require.NotNil(t, due)
	assert.Equal(t, 2, due.Attempts)
	require.NoError(t, FailRoutingOperationContext(
		context.Background(), due.ID, due.ClaimToken, 1_150, errors.New("permanent"),
	))

	var failed RoutingOperation
	require.NoError(t, db.First(&failed, due.ID).Error)
	assert.Equal(t, RoutingOperationStatusFailed, failed.Status)
	assert.Equal(t, "permanent", failed.LastError)
	assert.Empty(t, failed.ClaimToken)
	assert.Equal(t, int64(1_150), failed.CompletedTimeMs)

	err = FailRoutingOperationContext(context.Background(), due.ID, due.ClaimToken, 1_151, errors.New("again"))
	assert.ErrorIs(t, err, ErrRoutingOperationClaimLost)
	terminal, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, 2_000, 100,
	)
	require.NoError(t, err)
	assert.Nil(t, terminal)

	supersedeSpec := routingOperationSpecForTest()
	supersedeSpec.EvaluationHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, _, err = CreateRoutingOperationContext(context.Background(), supersedeSpec)
	require.NoError(t, err)
	superseded, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, 2_100, 100,
	)
	require.NoError(t, err)
	require.NotNil(t, superseded)
	require.NoError(t, SupersedeRoutingOperationContext(
		context.Background(), superseded.ID, superseded.ClaimToken, 2_150, "head changed",
	))
	var storedSuperseded RoutingOperation
	require.NoError(t, db.First(&storedSuperseded, superseded.ID).Error)
	assert.Equal(t, RoutingOperationStatusSuperseded, storedSuperseded.Status)
}

func routingOperationSpecForTest() RoutingOperationSpec {
	return RoutingOperationSpec{
		Type:                 RoutingOperationTypeCanaryAutoRollback,
		EvaluationHash:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PoolID:               31,
		ExpectedRevision:     11,
		ExpectedActivationID: 21,
		ActorID:              0,
		Reason:               "automatic canary rollback",
	}
}
