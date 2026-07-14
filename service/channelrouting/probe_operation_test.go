package channelrouting

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestActiveProbeOperationIsClaimedOnceAndLeaseIsRenewed(t *testing.T) {
	db := activeProbeOperationTestDB(t)
	operation := createActiveProbeOperationForTest(t, db, "a")
	var calls atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	scheduler := activeProbeOperationSchedulerForTest(func(context.Context, ActiveProbeTarget) ActiveProbeExecution {
		calls.Add(1)
		close(started)
		<-release
		return ActiveProbeExecution{StatusCode: 200}
	})
	heartbeatTicks := make(chan time.Time, 1)
	heartbeatResults := make(chan error, 1)
	cycleResult := make(chan error, 1)
	go func() {
		cycleResult <- runActiveProbeOperationCycleWithHeartbeatContext(
			context.Background(), activeProbeSettingForTest(), scheduler, 30*time.Second,
			heartbeatTicks, heartbeatResults,
		)
	}()
	<-started
	before, err := model.GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	shortClaimUntilMs := before.UpdatedTimeMs + 1_000
	require.NoError(t, db.Model(&model.RoutingOperation{}).Where("id = ?", operation.ID).
		Update("claim_until_ms", shortClaimUntilMs).Error)
	heartbeatTicks <- time.Now()
	require.NoError(t, <-heartbeatResults)
	after, err := model.GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Greater(t, after.ClaimUntilMs, shortClaimUntilMs)
	close(release)
	require.NoError(t, <-cycleResult)

	stored, err := model.GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingOperationStatusSucceeded, stored.Status)
	assert.Equal(t, int64(1), calls.Load())
	var result ActiveProbeOperationResult
	require.NoError(t, common.UnmarshalJsonStr(stored.ResultPayloadJSON, &result))
	assert.True(t, result.Enabled)
	assert.Equal(t, int64(1), result.Stats.Executed)
	assert.Equal(t, int64(1), result.Stats.Succeeded)

	require.NoError(t, runActiveProbeOperationCycleContext(
		context.Background(), activeProbeSettingForTest(), activeProbeOperationSchedulerForTest(nil), time.Second,
	))
	assert.Equal(t, int64(1), calls.Load(), "a terminal operation must not execute twice")
}

func TestActiveProbeOperationHeartbeatFailureCancelsAndLeavesRecoverableClaim(t *testing.T) {
	db := activeProbeOperationTestDB(t)
	operation := createActiveProbeOperationForTest(t, db, "e")
	started := make(chan struct{})
	canceled := make(chan struct{})
	scheduler := activeProbeOperationSchedulerForTest(func(ctx context.Context, _ ActiveProbeTarget) ActiveProbeExecution {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ActiveProbeExecution{Err: ctx.Err()}
	})
	heartbeatTicks := make(chan time.Time, 1)
	heartbeatResults := make(chan error, 1)
	cycleResult := make(chan error, 1)
	go func() {
		cycleResult <- runActiveProbeOperationCycleWithHeartbeatContext(
			context.Background(), activeProbeSettingForTest(), scheduler, 30*time.Second,
			heartbeatTicks, heartbeatResults,
		)
	}()
	<-started
	claimed, err := model.GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.RoutingOperation{}).Where("id = ?", operation.ID).
		Update("claim_until_ms", int64(1)).Error)
	heartbeatTicks <- time.Now()
	assert.ErrorIs(t, <-heartbeatResults, model.ErrRoutingOperationClaimLost)
	<-canceled
	assert.ErrorIs(t, <-cycleResult, model.ErrRoutingOperationClaimLost)

	nowMs, err := model.RoutingEndpointDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	recovered, err := model.ClaimRoutingOperationContext(
		context.Background(), model.RoutingOperationTypeActiveProbe, nowMs, time.Second.Milliseconds(),
	)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	assert.Equal(t, operation.ID, recovered.ID)
	assert.NotEqual(t, claimed.ClaimToken, recovered.ClaimToken)
}

func TestActiveProbeOperationDisabledSettingBecomesSuperseded(t *testing.T) {
	db := activeProbeOperationTestDB(t)
	operation := createActiveProbeOperationForTest(t, db, "b")
	var calls atomic.Int64
	scheduler := activeProbeOperationSchedulerForTest(func(context.Context, ActiveProbeTarget) ActiveProbeExecution {
		calls.Add(1)
		return ActiveProbeExecution{StatusCode: 200}
	})
	setting := activeProbeSettingForTest()
	setting.Enabled = false

	require.NoError(t, runActiveProbeOperationCycleContext(context.Background(), setting, scheduler, time.Second))
	stored, err := model.GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingOperationStatusSuperseded, stored.Status)
	assert.Contains(t, stored.LastError, "disabled")
	assert.Zero(t, calls.Load())
}

func TestActiveProbeOperationCancellationLeavesRecoverableClaim(t *testing.T) {
	db := activeProbeOperationTestDB(t)
	operation := createActiveProbeOperationForTest(t, db, "c")
	started := make(chan struct{})
	scheduler := activeProbeOperationSchedulerForTest(func(ctx context.Context, _ ActiveProbeTarget) ActiveProbeExecution {
		close(started)
		<-ctx.Done()
		return ActiveProbeExecution{Err: ctx.Err()}
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runActiveProbeOperationCycleContext(ctx, activeProbeSettingForTest(), scheduler, 300*time.Millisecond)
	}()
	<-started
	cancel()
	assert.ErrorIs(t, <-result, context.Canceled)

	stored, err := model.GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingOperationStatusRunning, stored.Status)
	recovered, err := model.ClaimRoutingOperationContext(
		context.Background(), model.RoutingOperationTypeActiveProbe, stored.ClaimUntilMs+1, time.Second.Milliseconds(),
	)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	assert.Equal(t, operation.ID, recovered.ID)
	assert.NotEqual(t, stored.ClaimToken, recovered.ClaimToken)
}

func TestActiveProbeOperationFailureIsRetriedWithSanitizedError(t *testing.T) {
	db := activeProbeOperationTestDB(t)
	operation := createActiveProbeOperationForTest(t, db, "d")
	scheduler := activeProbeOperationSchedulerForTest(func(context.Context, ActiveProbeTarget) ActiveProbeExecution {
		return ActiveProbeExecution{StatusCode: 200}
	})
	scheduler.deps.persist = func(context.Context, model.RoutingControlLease, model.RoutingProbeResult) error {
		return errors.New("probe persist failed: api_key=SECRET-VALUE")
	}

	err := runActiveProbeOperationCycleContext(context.Background(), activeProbeSettingForTest(), scheduler, time.Second)
	require.Error(t, err)
	stored, getErr := model.GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, getErr)
	assert.Equal(t, model.RoutingOperationStatusPending, stored.Status)
	assert.Contains(t, stored.LastError, "api_key=***")
	assert.NotContains(t, stored.LastError, "SECRET-VALUE")
	assert.Greater(t, stored.NextRetryMs, stored.UpdatedTimeMs)
}

func activeProbeOperationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	model.DB = db
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedisEnabled
		_ = sqlDB.Close()
	})
	require.NoError(t, db.AutoMigrate(&model.RoutingOperation{}, &model.RoutingPolicyHead{}))
	require.NoError(t, db.Create(&model.RoutingPolicyHead{
		ID: 1, CurrentRevision: 1, CurrentActivationID: 1,
		CurrentHash: strings.Repeat("f", 64), CurrentStage: model.RoutingDeploymentStageActive,
		CreatedTime: 1, UpdatedTime: 1,
	}).Error)
	return db
}

func createActiveProbeOperationForTest(t *testing.T, db *gorm.DB, key string) model.RoutingOperation {
	t.Helper()
	var head model.RoutingPolicyHead
	require.NoError(t, db.First(&head).Error)
	operation, created, err := model.CreateRoutingOperationContext(context.Background(), model.RoutingOperationSpec{
		Type: model.RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat(key, 64),
		SubjectType:      model.RoutingOperationSubjectRoutingProbes,
		ExpectedRevision: head.CurrentRevision, ExpectedActivationID: head.CurrentActivationID,
		ActorID: 7, Reason: "manual routing active probe",
		RequestKeyHash: strings.Repeat(key, 64), RequestPayloadHash: strings.Repeat(string(key[0]+1), 64),
	})
	require.NoError(t, err)
	require.True(t, created)
	return operation
}

func activeProbeOperationSchedulerForTest(executor ActiveProbeExecutor) *ActiveProbeScheduler {
	if executor == nil {
		executor = func(context.Context, ActiveProbeTarget) ActiveProbeExecution {
			return ActiveProbeExecution{StatusCode: 200}
		}
	}
	target := activeProbeTargetForTest("e", "operation.example")
	target.ChannelID = 0
	target.MemberID = 0
	return newActiveProbeScheduler(activeProbeDeps{
		now:     time.Now,
		enabled: func() bool { return true },
		targets: func(smart_routing_setting.SmartRoutingSetting, time.Time) []ActiveProbeTarget {
			return []ActiveProbeTarget{target}
		},
		executor: func() ActiveProbeExecutor { return executor },
		acquire:  successfulProbeLeaseForTest,
		persist: func(context.Context, model.RoutingControlLease, model.RoutingProbeResult) error {
			return nil
		},
		complete: func(context.Context, model.RoutingControlLease, int64) error { return nil },
		release:  func(context.Context, model.RoutingControlLease, int64) error { return nil },
		waitUntil: func(ctx context.Context, _ time.Time) bool {
			return ctx.Err() == nil
		},
	})
}
