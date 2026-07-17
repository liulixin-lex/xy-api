package model

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMigrateRoutingDedicatedSchemasRequiresExplicitAlphaDrain(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&routingMetricRollupLegacyIndex{},
		&RoutingPolicyApproval{},
		&RoutingPolicyRollbackApproval{},
	))
	t.Setenv("ROUTING_V2_ALPHA_DRAINED", "true")
	t.Setenv(routingAlphaDrainedEnv, "false")

	err := migrateRoutingDedicatedSchemas(db)
	assert.ErrorIs(t, err, ErrRoutingRetirementAlphaDrainRequired)
	assert.Contains(t, err.Error(), "legacy Redis telemetry stream routing:v2:telemetry")
	assert.Contains(t, err.Error(), routingAlphaDrainedEnv)
	assert.False(t, db.Migrator().HasTable(&RoutingErrorBudgetState{}))

	t.Setenv(routingAlphaDrainedEnv, "true")
	require.NoError(t, migrateRoutingDedicatedSchemas(db))
	rollupReady, err := RoutingMetricRollupRevisionKeySchemaReady(db)
	require.NoError(t, err)
	assert.True(t, rollupReady)
	errorBudgetReady, err := RoutingErrorBudgetSchemaReady(db)
	require.NoError(t, err)
	assert.True(t, errorBudgetReady)
}

func TestMigrateRoutingDedicatedSchemasFreshInstallDoesNotRequireAlphaDrain(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	t.Setenv(routingAlphaDrainedEnv, "false")
	require.NoError(t, db.AutoMigrate(
		&Channel{},
		&RoutingConfigurationEpoch{},
		&RoutingChannelConfiguration{},
		&RoutingPolicyApproval{},
		&RoutingPolicyRollbackApproval{},
	))

	require.NoError(t, migrateRoutingDedicatedSchemas(db))
	rollupReady, err := RoutingMetricRollupRevisionKeySchemaReady(db)
	require.NoError(t, err)
	assert.True(t, rollupReady)
	errorBudgetReady, err := RoutingErrorBudgetSchemaReady(db)
	require.NoError(t, err)
	assert.True(t, errorBudgetReady)
	assert.False(t, db.Migrator().HasTable(&RoutingChannelBinding{}))
	assert.False(t, db.Migrator().HasTable(&RoutingUpstreamAccount{}))
	assert.False(t, db.Migrator().HasTable(retiredRoutingUpstreamAccountHealthTable))
	assert.False(t, db.Migrator().HasTable(&RoutingCostSnapshot{}))
}

func TestMigrateRoutingDedicatedSchemasPreflightDetectsPendingRetirementSurfaces(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *gorm.DB)
	}{
		{
			name: "active connector binding",
			setup: func(t *testing.T, db *gorm.DB) {
				t.Helper()
				require.NoError(t, db.AutoMigrate(&RoutingChannelBinding{}))
				secret := "encrypted-management-credential"
				require.NoError(t, db.Create(&RoutingChannelBinding{
					ChannelID: 101, UpstreamType: RoutingUpstreamTypeSub2API,
					BaseURL: "https://legacy.example", UpstreamGroup: "legacy",
					Enabled: true, EncCredentials: &secret, KeyVersion: 2,
				}).Error)
			},
		},
		{
			name: "active upstream account",
			setup: func(t *testing.T, db *gorm.DB) {
				t.Helper()
				require.NoError(t, db.AutoMigrate(&RoutingUpstreamAccount{}))
				require.NoError(t, db.Create(&RoutingUpstreamAccount{
					AccountKey: strings.Repeat("a", 64), SourceType: RoutingUpstreamTypeSub2API,
					MaskedIdentity: "legacy-user", Status: RoutingUpstreamAccountStatusActive,
					BalanceKnown: true, Balance: 17, BalanceUpdatedAt: 100,
					LastSyncStatus: RoutingUpstreamSyncStatusSuccess,
					CreatedTime:    100, UpdatedTime: 100,
				}).Error)
			},
		},
		{
			name: "upstream account health",
			setup: func(t *testing.T, db *gorm.DB) {
				t.Helper()
				require.NoError(t, db.AutoMigrate(&routingRetirementPreflightAccountHealth{}))
				require.NoError(t, db.Create(&routingRetirementPreflightAccountHealth{AccountID: 103}).Error)
			},
		},
		{
			name: "channel balance health",
			setup: func(t *testing.T, db *gorm.DB) {
				t.Helper()
				require.NoError(t, db.AutoMigrate(&routingRetirementPreflightChannelHealth{}))
				require.NoError(t, db.Create(&routingRetirementPreflightChannelHealth{
					ChannelID: 104, BalanceKnown: true, Balance: 9, BalanceUpdatedTime: 100,
				}).Error)
			},
		},
		{
			name: "cost snapshot account state",
			setup: func(t *testing.T, db *gorm.DB) {
				t.Helper()
				require.NoError(t, db.AutoMigrate(&RoutingCostSnapshot{}))
				modelKey := strings.Repeat("m", 64)
				require.NoError(t, db.Create(&RoutingCostSnapshot{
					ChannelID: 105, ModelName: "gpt-test", ModelKey: &modelKey,
					AccountKeyHash: strings.Repeat("b", 64), AccountMaskedID: "legacy-user",
					AccountStatus:       RoutingUpstreamAccountStatusActive,
					AccountBalanceKnown: true, AccountBalance: 9, AccountBalanceAt: 100,
					AccountSyncStatus: RoutingUpstreamSyncStatusSuccess,
				}).Error)
			},
		},
		{
			name: "pending cost sync operation",
			setup: func(t *testing.T, db *gorm.DB) {
				t.Helper()
				require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
				_, _, err := createRoutingOperationDB(context.Background(), db, RoutingOperationSpec{
					Type: RoutingOperationTypeCostSync, EvaluationHash: strings.Repeat("c", 64),
					SubjectType:      RoutingOperationSubjectRoutingCosts,
					ExpectedRevision: 1, ExpectedActivationID: 1, ActorID: 7,
					Reason: "legacy cost sync", RequestKeyHash: strings.Repeat("d", 64),
					RequestPayloadHash: strings.Repeat("e", 64),
				})
				require.NoError(t, err)
			},
		},
		{
			name: "running cost sync task",
			setup: func(t *testing.T, db *gorm.DB) {
				t.Helper()
				require.NoError(t, db.AutoMigrate(&SystemTask{}))
				require.NoError(t, db.Create(&SystemTask{
					TaskID: "systask_" + strings.Repeat("r", 32), Type: SystemTaskTypeRoutingCostSync,
					Status: SystemTaskStatusRunning, CreatedAt: 100, UpdatedAt: 100,
				}).Error)
			},
		},
		{
			name: "cost sync task lock",
			setup: func(t *testing.T, db *gorm.DB) {
				t.Helper()
				require.NoError(t, db.AutoMigrate(&SystemTaskLock{}))
				require.NoError(t, db.Create(&SystemTaskLock{
					Type: SystemTaskTypeRoutingCostSync, TaskID: "systask_" + strings.Repeat("l", 32),
					LockedBy: "legacy-worker", LockedUntil: 200, UpdatedAt: 100,
				}).Error)
			},
		},
		{
			name: "legacy error budget writer index",
			setup: func(t *testing.T, db *gorm.DB) {
				t.Helper()
				require.NoError(t, db.AutoMigrate(&routingErrorBudgetLegacyState{}))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openRoutingSQLiteTestDB(t)
			withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
			t.Setenv(routingAlphaDrainedEnv, "false")
			test.setup(t, db)

			err := migrateRoutingDedicatedSchemas(db)
			assert.ErrorIs(t, err, ErrRoutingRetirementAlphaDrainRequired)
			assert.Contains(t, err.Error(), routingAlphaDrainedEnv)
			assert.False(t, db.Migrator().HasTable(&RoutingConfigurationEpoch{}),
				"the preflight rejection must happen before the first migration write")
		})
	}
}

func TestMigrateRoutingDedicatedSchemasPreflightRejectsMalformedLegacySchemaBeforeAuthorizedRetirement(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	t.Setenv(routingAlphaDrainedEnv, "true")
	require.NoError(t, db.AutoMigrate(
		&Channel{},
		&RoutingConfigurationEpoch{},
		&RoutingChannelConfiguration{},
		&RoutingControlAudit{},
		&RoutingChannelBinding{},
		&routingRetirementUnexpectedRollupIndex{},
		&RoutingPolicyApproval{},
		&RoutingPolicyRollbackApproval{},
	))

	const channelID = 701
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&Channel{
		Id: channelID, Name: "malformed-cutover", Key: "serving-key",
		Models: "gpt-test", Group: "default", CreatedTime: 100,
	}).Error)
	secret := "encrypted-management-credential"
	require.NoError(t, db.Create(&RoutingChannelBinding{
		ChannelID: channelID, UpstreamType: RoutingUpstreamTypeSub2API,
		BaseURL: "https://legacy.example", UpstreamGroup: "legacy",
		Enabled: true, EncCredentials: &secret, KeyVersion: 2,
		CreatedTime: 100, UpdatedTime: 100,
	}).Error)
	var bindingBefore RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&bindingBefore).Error)

	err := migrateRoutingDedicatedSchemas(db)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRoutingRetirementAlphaDrainRequired)
	assert.Contains(t, err.Error(), "unexpected definition")
	var bindingAfter RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&bindingAfter).Error)
	assert.Equal(t, bindingBefore, bindingAfter)
	var configurationCount int64
	require.NoError(t, db.Model(&RoutingChannelConfiguration{}).
		Where("channel_id = ?", channelID).Count(&configurationCount).Error)
	assert.Zero(t, configurationCount)
	var epochCount int64
	require.NoError(t, db.Model(&RoutingConfigurationEpoch{}).Count(&epochCount).Error)
	assert.Zero(t, epochCount)
}

func TestMigrateRoutingDedicatedSchemasPreflightIsReadOnlyBeforeRetirement(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	t.Setenv(routingAlphaDrainedEnv, "false")
	require.NoError(t, db.AutoMigrate(
		&Channel{},
		&RoutingConfigurationEpoch{},
		&RoutingChannelConfiguration{},
		&RoutingControlAudit{},
		&RoutingChannelBinding{},
		&RoutingUpstreamAccount{},
		&routingRetirementPreflightAccountHealth{},
		&routingRetirementPreflightChannelHealth{},
		&RoutingCostSnapshot{},
		&RoutingOperation{},
		&SystemTask{},
		&SystemTaskLock{},
		&routingMetricRollupLegacyIndex{},
		&RoutingPolicyApproval{},
		&RoutingPolicyRollbackApproval{},
	))

	const channelID = 801
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&Channel{
		Id: channelID, Name: "legacy-retirement", Key: "serving-key",
		RoutingIdentity: common.GetUUID(), RoutingGeneration: common.GetUUID(),
		Models: "gpt-test", Group: "default", Balance: 42,
		BalanceUpdatedTime: 123, CreatedTime: 100,
	}).Error)
	secret := "encrypted-management-credential"
	egressPolicy := `{"allow_cidrs":["10.0.0.0/8"]}`
	newAPIUserID := 91
	lastSyncError := "legacy connector failed"
	accountKey := strings.Repeat("a", 64)
	require.NoError(t, db.Create(&RoutingChannelBinding{
		ChannelID: channelID, UpstreamType: RoutingUpstreamTypeSub2API,
		BaseURL: "https://legacy.example", UpstreamGroup: "legacy", ServesClaudeCode: true,
		EgressPolicyJSON: &egressPolicy, EncCredentials: &secret, KeyVersion: 2,
		NewAPIUserID: &newAPIUserID, Enabled: true, AccountKeyHash: accountKey,
		SyncFailureCount: 3, SyncBackoffUntil: 999, LastSyncError: &lastSyncError,
		CreatedTime: 100, UpdatedTime: 100,
	}).Error)
	require.NoError(t, db.Create(&RoutingUpstreamAccount{
		ID: 901, AccountKey: accountKey, SourceType: RoutingUpstreamTypeSub2API,
		MaskedIdentity: "legacy-user", Status: RoutingUpstreamAccountStatusActive,
		BalanceKnown: true, Balance: 17, BalanceUpdatedAt: 120,
		LastSyncStatus: RoutingUpstreamSyncStatusSuccess, LastSyncError: "",
		CreatedTime: 100, UpdatedTime: 120,
	}).Error)
	require.NoError(t, db.Create(&routingRetirementPreflightAccountHealth{AccountID: 901}).Error)
	require.NoError(t, db.Create(&routingRetirementPreflightChannelHealth{
		ChannelID: channelID, AuthFailure: true, AuthFailureReason: "serving credential rejected",
		AuthFailureUntil: 1_000, BalanceKnown: true, Balance: 17,
		BalanceUpdatedTime: 120, UpdatedTime: 120,
	}).Error)
	modelKey := strings.Repeat("m", 64)
	require.NoError(t, db.Create(&RoutingCostSnapshot{
		ChannelID: channelID, ModelName: "gpt-test", ModelKey: &modelKey,
		AccountKeyHash: accountKey, AccountMaskedID: "legacy-user",
		AccountStatus:       RoutingUpstreamAccountStatusActive,
		AccountBalanceKnown: true, AccountBalance: 17, AccountBalanceAt: 120,
		AccountSyncStatus: RoutingUpstreamSyncStatusSuccess,
	}).Error)

	runningOperation, _, err := CreateRoutingOperationContext(
		context.Background(), routingCostSyncOperationSpecForTest("f"),
	)
	require.NoError(t, err)
	claimedOperation, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCostSync, runningOperation.CreatedTimeMs, 60_000,
	)
	require.NoError(t, err)
	require.NotNil(t, claimedOperation)
	pendingOperation, _, err := CreateRoutingOperationContext(
		context.Background(), routingCostSyncOperationSpecForTest("d"),
	)
	require.NoError(t, err)
	pendingActiveKey := SystemTaskTypeRoutingCostSync
	pendingTask := SystemTask{
		TaskID: "systask_" + strings.Repeat("p", 32), Type: SystemTaskTypeRoutingCostSync,
		Status: SystemTaskStatusPending, ActiveKey: &pendingActiveKey,
		Payload: `{"request":"pending"}`, State: `{"cursor":1}`,
		Error: "legacy pending error", LockedBy: "pending-runner", CreatedAt: 100, UpdatedAt: 110,
	}
	runningTask := SystemTask{
		TaskID: "systask_" + strings.Repeat("r", 32), Type: SystemTaskTypeRoutingCostSync,
		Status: SystemTaskStatusRunning, Payload: `{"request":"running"}`, State: `{"cursor":2}`,
		Error: "legacy running error", LockedBy: "running-runner", CreatedAt: 120, UpdatedAt: 130,
	}
	require.NoError(t, db.Create(&pendingTask).Error)
	require.NoError(t, db.Create(&runningTask).Error)
	costSyncLock := SystemTaskLock{
		Type: SystemTaskTypeRoutingCostSync, TaskID: runningTask.TaskID,
		LockedBy: "running-runner", LockedUntil: 500, UpdatedAt: 130,
	}
	require.NoError(t, db.Create(&costSyncLock).Error)

	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	previousRedisClient := common.RDB
	previousRedisEnabled := common.RedisEnabled
	common.RDB = redisClient
	common.RedisEnabled = true
	t.Cleanup(func() {
		common.RDB = previousRedisClient
		common.RedisEnabled = previousRedisEnabled
		assert.NoError(t, redisClient.Close())
	})
	const retiredJWTKey = "routing:sub2api:jwt:801:session"
	require.NoError(t, redisClient.Set(context.Background(), retiredJWTKey, "encrypted-jwt", 0).Err())

	var bindingBefore RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&bindingBefore).Error)
	var accountBefore RoutingUpstreamAccount
	require.NoError(t, db.Where("id = ?", 901).First(&accountBefore).Error)
	var channelHealthBefore routingRetirementPreflightChannelHealth
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&channelHealthBefore).Error)
	var costBefore RoutingCostSnapshot
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&costBefore).Error)
	var runningOperationBefore RoutingOperation
	require.NoError(t, db.Where("id = ?", claimedOperation.ID).First(&runningOperationBefore).Error)
	var pendingOperationBefore RoutingOperation
	require.NoError(t, db.Where("id = ?", pendingOperation.ID).First(&pendingOperationBefore).Error)
	var pendingTaskBefore SystemTask
	require.NoError(t, db.Where("id = ?", pendingTask.ID).First(&pendingTaskBefore).Error)
	var runningTaskBefore SystemTask
	require.NoError(t, db.Where("id = ?", runningTask.ID).First(&runningTaskBefore).Error)
	var costSyncLockBefore SystemTaskLock
	require.NoError(t, db.Where("type = ?", SystemTaskTypeRoutingCostSync).First(&costSyncLockBefore).Error)

	err = migrateRoutingDedicatedSchemas(db)
	assert.ErrorIs(t, err, ErrRoutingRetirementAlphaDrainRequired)
	assert.Contains(t, err.Error(), routingAlphaDrainedEnv)
	var configurationEpochCount int64
	require.NoError(t, db.Model(&RoutingConfigurationEpoch{}).Count(&configurationEpochCount).Error)
	assert.Zero(t, configurationEpochCount)
	assert.False(t, db.Migrator().HasTable(&RoutingErrorBudgetState{}))
	assert.False(t, db.Migrator().HasIndex(&RoutingMetricRollup{}, routingMetricRollupUniqueIndex))
	assert.True(t, db.Migrator().HasIndex(&routingMetricRollupLegacyIndex{}, routingMetricRollupLegacyUniqueIndex))
	var configurationCount int64
	require.NoError(t, db.Model(&RoutingChannelConfiguration{}).
		Where("channel_id = ?", channelID).Count(&configurationCount).Error)
	assert.Zero(t, configurationCount)

	var bindingAfter RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&bindingAfter).Error)
	assert.Equal(t, bindingBefore, bindingAfter)
	var accountAfter RoutingUpstreamAccount
	require.NoError(t, db.Where("id = ?", 901).First(&accountAfter).Error)
	assert.Equal(t, accountBefore, accountAfter)
	var accountHealthCount int64
	require.NoError(t, db.Table(retiredRoutingUpstreamAccountHealthTable).Count(&accountHealthCount).Error)
	assert.Equal(t, int64(1), accountHealthCount)
	var channelHealthAfter routingRetirementPreflightChannelHealth
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&channelHealthAfter).Error)
	assert.Equal(t, channelHealthBefore, channelHealthAfter)
	var costAfter RoutingCostSnapshot
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&costAfter).Error)
	assert.Equal(t, costBefore, costAfter)
	var runningOperationAfter RoutingOperation
	require.NoError(t, db.Where("id = ?", claimedOperation.ID).First(&runningOperationAfter).Error)
	assert.Equal(t, runningOperationBefore, runningOperationAfter)
	var pendingOperationAfter RoutingOperation
	require.NoError(t, db.Where("id = ?", pendingOperation.ID).First(&pendingOperationAfter).Error)
	assert.Equal(t, pendingOperationBefore, pendingOperationAfter)
	var pendingTaskAfter SystemTask
	require.NoError(t, db.Where("id = ?", pendingTask.ID).First(&pendingTaskAfter).Error)
	assert.Equal(t, pendingTaskBefore, pendingTaskAfter)
	var runningTaskAfter SystemTask
	require.NoError(t, db.Where("id = ?", runningTask.ID).First(&runningTaskAfter).Error)
	assert.Equal(t, runningTaskBefore, runningTaskAfter)
	var costSyncLockAfter SystemTaskLock
	require.NoError(t, db.Where("type = ?", SystemTaskTypeRoutingCostSync).First(&costSyncLockAfter).Error)
	assert.Equal(t, costSyncLockBefore, costSyncLockAfter)
	redisExists, err := redisClient.Exists(context.Background(), retiredJWTKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), redisExists)

	t.Setenv(routingAlphaDrainedEnv, "true")
	require.NoError(t, migrateRoutingDedicatedSchemas(db))
	var configuration RoutingChannelConfiguration
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&configuration).Error)
	assert.True(t, ValidRoutingChannelConfiguration(configuration))
	var retiredBinding RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&retiredBinding).Error)
	assert.False(t, retiredBinding.Enabled)
	assert.Nil(t, retiredBinding.EgressPolicyJSON)
	assert.Nil(t, retiredBinding.EncCredentials)
	assert.Zero(t, retiredBinding.KeyVersion)
	assert.Nil(t, retiredBinding.NewAPIUserID)
	assert.Empty(t, retiredBinding.AccountKeyHash)
	assert.Zero(t, retiredBinding.SyncFailureCount)
	assert.Zero(t, retiredBinding.SyncBackoffUntil)
	assert.Nil(t, retiredBinding.LastSyncError)
	var retiredAccount RoutingUpstreamAccount
	require.NoError(t, db.Where("id = ?", 901).First(&retiredAccount).Error)
	assert.Equal(t, "retired", retiredAccount.MaskedIdentity)
	assert.Equal(t, RoutingUpstreamAccountStatusDisabled, retiredAccount.Status)
	assert.False(t, retiredAccount.BalanceKnown)
	assert.Zero(t, retiredAccount.Balance)
	assert.Zero(t, retiredAccount.BalanceUpdatedAt)
	assert.Equal(t, RoutingUpstreamSyncStatusUnknown, retiredAccount.LastSyncStatus)
	assert.Empty(t, retiredAccount.LastSyncError)
	require.NoError(t, db.Table(retiredRoutingUpstreamAccountHealthTable).Count(&accountHealthCount).Error)
	assert.Zero(t, accountHealthCount)
	var retiredChannelHealth routingRetirementPreflightChannelHealth
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&retiredChannelHealth).Error)
	assert.True(t, retiredChannelHealth.AuthFailure)
	assert.Equal(t, "serving credential rejected", retiredChannelHealth.AuthFailureReason)
	assert.False(t, retiredChannelHealth.BalanceKnown)
	assert.Zero(t, retiredChannelHealth.Balance)
	assert.Zero(t, retiredChannelHealth.BalanceUpdatedTime)
	var retiredCost RoutingCostSnapshot
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&retiredCost).Error)
	assert.Empty(t, retiredCost.AccountKeyHash)
	assert.Empty(t, retiredCost.AccountMaskedID)
	assert.Equal(t, RoutingUpstreamAccountStatusDisabled, retiredCost.AccountStatus)
	assert.False(t, retiredCost.AccountBalanceKnown)
	assert.Zero(t, retiredCost.AccountBalance)
	assert.Zero(t, retiredCost.AccountBalanceAt)
	assert.Equal(t, RoutingUpstreamSyncStatusUnknown, retiredCost.AccountSyncStatus)
	assert.Empty(t, retiredCost.AccountSyncError)
	for _, operationID := range []int64{claimedOperation.ID, pendingOperation.ID} {
		var operation RoutingOperation
		require.NoError(t, db.Where("id = ?", operationID).First(&operation).Error)
		assert.Equal(t, RoutingOperationStatusSuperseded, operation.Status)
		assert.Equal(t, routingCostSyncRetiredReason, operation.LastError)
	}
	for _, taskID := range []int64{pendingTask.ID, runningTask.ID} {
		var task SystemTask
		require.NoError(t, db.Where("id = ?", taskID).First(&task).Error)
		assert.Equal(t, SystemTaskStatusFailed, task.Status)
		assert.Nil(t, task.ActiveKey)
		assert.Equal(t, routingCostSyncRetiredReason, task.Error)
		assert.Empty(t, task.LockedBy)
	}
	var lockCount int64
	require.NoError(t, db.Model(&SystemTaskLock{}).
		Where("type = ?", SystemTaskTypeRoutingCostSync).Count(&lockCount).Error)
	assert.Zero(t, lockCount)
	redisExists, err = redisClient.Exists(context.Background(), retiredJWTKey).Result()
	require.NoError(t, err)
	assert.Zero(t, redisExists)

	configurationBeforeRestart := configuration
	retiredBindingBeforeRestart := retiredBinding
	retiredAccountBeforeRestart := retiredAccount
	retiredChannelHealthBeforeRestart := retiredChannelHealth
	retiredCostBeforeRestart := retiredCost
	t.Setenv(routingAlphaDrainedEnv, "false")
	require.NoError(t, migrateRoutingDedicatedSchemas(db),
		"a fully retired database must not require a permanent cutover flag")
	var configurationAfterRestart RoutingChannelConfiguration
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&configurationAfterRestart).Error)
	assert.Equal(t, configurationBeforeRestart, configurationAfterRestart)
	var bindingAfterRestart RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&bindingAfterRestart).Error)
	assert.Equal(t, retiredBindingBeforeRestart, bindingAfterRestart)
	var accountAfterRestart RoutingUpstreamAccount
	require.NoError(t, db.Where("id = ?", 901).First(&accountAfterRestart).Error)
	assert.Equal(t, retiredAccountBeforeRestart, accountAfterRestart)
	var channelHealthAfterRestart routingRetirementPreflightChannelHealth
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&channelHealthAfterRestart).Error)
	assert.Equal(t, retiredChannelHealthBeforeRestart, channelHealthAfterRestart)
	var costAfterRestart RoutingCostSnapshot
	require.NoError(t, db.Where("channel_id = ?", channelID).First(&costAfterRestart).Error)
	assert.Equal(t, retiredCostBeforeRestart, costAfterRestart)
}

func TestMigrateDBPathsPreflightProtectsRetirementData(t *testing.T) {
	tests := []struct {
		name    string
		migrate func() error
	}{
		{name: "serial", migrate: migrateDB},
		{name: "fast", migrate: migrateDBFast},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openRoutingSQLiteTestDB(t)
			withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
			t.Setenv(routingAlphaDrainedEnv, "false")
			require.NoError(t, db.AutoMigrate(
				&Channel{},
				&RoutingChannelBinding{},
				&routingRetirementPreflightAccountHealth{},
				&SystemTask{},
				&SystemTaskLock{},
			))

			channelID := 900 + index
			require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&Channel{
				Id: channelID, Name: "startup-preflight", Key: "serving-key",
				Models: "gpt-test", Group: "default", CreatedTime: 100,
			}).Error)
			secret := "encrypted-management-credential"
			lastSyncError := "legacy connector failed"
			require.NoError(t, db.Create(&RoutingChannelBinding{
				ChannelID: channelID, UpstreamType: RoutingUpstreamTypeSub2API,
				BaseURL: "https://legacy.example", UpstreamGroup: "legacy",
				Enabled: true, EncCredentials: &secret, KeyVersion: 2,
				SyncFailureCount: 2, SyncBackoffUntil: 999, LastSyncError: &lastSyncError,
				CreatedTime: 100, UpdatedTime: 100,
			}).Error)
			require.NoError(t, db.Create(&routingRetirementPreflightAccountHealth{AccountID: channelID}).Error)
			activeKey := SystemTaskTypeRoutingCostSync
			task := SystemTask{
				TaskID: "systask_" + strings.Repeat(string(rune('a'+index)), 32),
				Type:   SystemTaskTypeRoutingCostSync, Status: SystemTaskStatusPending,
				ActiveKey: &activeKey, Error: "legacy pending error", LockedBy: "legacy-worker",
				CreatedAt: 100, UpdatedAt: 100,
			}
			require.NoError(t, db.Create(&task).Error)
			lock := SystemTaskLock{
				Type: SystemTaskTypeRoutingCostSync, TaskID: task.TaskID,
				LockedBy: "legacy-worker", LockedUntil: 500, UpdatedAt: 100,
			}
			require.NoError(t, db.Create(&lock).Error)

			redisServer := miniredis.RunT(t)
			redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
			previousRedisClient := common.RDB
			previousRedisEnabled := common.RedisEnabled
			common.RDB = redisClient
			common.RedisEnabled = true
			t.Cleanup(func() {
				common.RDB = previousRedisClient
				common.RedisEnabled = previousRedisEnabled
				assert.NoError(t, redisClient.Close())
			})
			redisKey := fmt.Sprintf("routing:sub2api:jwt:%d:session", channelID)
			require.NoError(t, redisClient.Set(context.Background(), redisKey, "encrypted-jwt", 0).Err())

			var bindingBefore RoutingChannelBinding
			require.NoError(t, db.Where("channel_id = ?", channelID).First(&bindingBefore).Error)
			var taskBefore SystemTask
			require.NoError(t, db.Where("id = ?", task.ID).First(&taskBefore).Error)
			var lockBefore SystemTaskLock
			require.NoError(t, db.Where("type = ?", SystemTaskTypeRoutingCostSync).First(&lockBefore).Error)

			err := test.migrate()
			assert.ErrorIs(t, err, ErrRoutingRetirementAlphaDrainRequired)
			assert.Contains(t, err.Error(), routingAlphaDrainedEnv)

			var bindingAfter RoutingChannelBinding
			require.NoError(t, db.Where("channel_id = ?", channelID).First(&bindingAfter).Error)
			assert.Equal(t, bindingBefore, bindingAfter)
			var accountHealthCount int64
			require.NoError(t, db.Table(retiredRoutingUpstreamAccountHealthTable).Count(&accountHealthCount).Error)
			assert.Equal(t, int64(1), accountHealthCount)
			var taskAfter SystemTask
			require.NoError(t, db.Where("id = ?", task.ID).First(&taskAfter).Error)
			assert.Equal(t, taskBefore, taskAfter)
			var lockAfter SystemTaskLock
			require.NoError(t, db.Where("type = ?", SystemTaskTypeRoutingCostSync).First(&lockAfter).Error)
			assert.Equal(t, lockBefore, lockAfter)
			var configurationCount int64
			require.NoError(t, db.Model(&RoutingChannelConfiguration{}).
				Where("channel_id = ?", channelID).Count(&configurationCount).Error)
			assert.Zero(t, configurationCount)
			var epochCount int64
			require.NoError(t, db.Model(&RoutingConfigurationEpoch{}).Count(&epochCount).Error)
			assert.Zero(t, epochCount)
			redisExists, existsErr := redisClient.Exists(context.Background(), redisKey).Result()
			require.NoError(t, existsErr)
			assert.Equal(t, int64(1), redisExists)
		})
	}
}

func TestWaitRoutingSchemaReadyFailsClosedBeforeMasterMigration(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	t.Setenv(routingSchemaReadyWaitSecondsEnv, "0")

	err := waitRoutingSchemaReady(db)
	assert.ErrorIs(t, err, ErrRoutingSchemaNotReady)
}

func TestRoutingSchemaVersionGateRequiresExactMarkerAndPhysicalSchema(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, prepareRoutingCanaryEvaluationWindowUniqueIndex(db))
	require.NoError(t, db.AutoMigrate(routingRequiredSchemaModels()...))
	require.NoError(t, migrateRoutingDedicatedSchemas(db))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))

	physicalReady, err := routingPhysicalSchemaReady(db)
	require.NoError(t, err)
	require.True(t, physicalReady)
	ready, err := RoutingSchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)

	require.NoError(t, db.Create(&RoutingSchemaVersion{
		Component: routingLegacySchemaComponent, Version: "channel-routing-v2-phase5-20260714.9", UpdatedTimeMs: 1,
	}).Error)
	ready, err = RoutingSchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready, "the retired schema marker must never open the new runtime gate")
	require.NoError(t, db.Create(&RoutingSchemaVersion{
		Component: routingSchemaComponent, Version: "channel-routing-stale", UpdatedTimeMs: 1,
	}).Error)
	ready, err = RoutingSchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)

	require.NoError(t, publishRoutingSchemaVersion(db))
	ready, err = RoutingSchemaReady(db)
	require.NoError(t, err)
	assert.True(t, ready)
	t.Setenv(routingSchemaReadyWaitSecondsEnv, "0")
	require.NoError(t, waitRoutingSchemaReady(db))
	require.NoError(t, invalidateRoutingSchemaVersion(db))
	ready, err = RoutingSchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
	var retiredMarkers int64
	require.NoError(t, db.Model(&RoutingSchemaVersion{}).
		Where("component IN ?", []string{routingSchemaComponent, routingLegacySchemaComponent}).
		Count(&retiredMarkers).Error)
	assert.Zero(t, retiredMarkers)
	require.NoError(t, publishRoutingSchemaVersion(db))

	require.NoError(t, db.Migrator().DropIndex(&RoutingOperation{}, routingOperationRequestKeyUniqueIndex))
	ready, err = RoutingSchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
	assert.ErrorIs(t, waitRoutingSchemaReady(db), ErrRoutingSchemaNotReady)
}

func TestMigrateDBPathsRegisterDurableTaskBillingOperations(t *testing.T) {
	tests := []struct {
		name    string
		migrate func() error
	}{
		{name: "serial", migrate: migrateDB},
		{name: "fast", migrate: migrateDBFast},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openRoutingSQLiteTestDB(t)
			withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
			t.Setenv(routingAlphaDrainedEnv, "true")
			require.NoError(t, db.AutoMigrate(&Option{}))
			require.NoError(t, db.Create(&Option{
				Key: legacyRequestProfileEnabledOptionKey, Value: "false",
			}).Error)
			require.NoError(t, db.AutoMigrate(&legacyLogV0110{}))
			require.NoError(t, db.Create(&legacyLogV0110{
				UserId: 19, CreatedAt: 1_720_000_000, Type: LogTypeManage,
				Content: "v0.1.10 main migration log", RequestId: "legacy-main-log",
			}).Error)
			require.NoError(t, db.AutoMigrate(&AsyncBillingManualResolution{}))
			require.NoError(t, db.Migrator().DropIndex(
				&AsyncBillingManualResolution{}, asyncBillingManualResolutionUniqueIndex,
			))
			require.NoError(t, db.Migrator().CreateIndex(
				&legacyAsyncBillingManualResolutionIndex{}, asyncBillingManualResolutionUniqueIndex,
			))

			require.NoError(t, test.migrate())
			require.NoError(t, test.migrate(), "migration must remain idempotent across restarts")
			for _, retiredTable := range []string{
				"routing_channel_bindings",
				"routing_upstream_accounts",
				retiredRoutingUpstreamAccountHealthTable,
				"routing_cost_snapshots",
			} {
				assert.False(t, db.Migrator().HasTable(retiredTable),
					"fresh installs and restarts must not recreate retired connector tables")
			}
			var requestProfileOption Option
			require.NoError(t, db.Where("key = ?", requestProfileEnabledOptionKey).First(&requestProfileOption).Error)
			assert.Equal(t, "false", requestProfileOption.Value)
			var legacyRequestProfileOptions int64
			require.NoError(t, db.Model(&Option{}).
				Where("key = ?", legacyRequestProfileEnabledOptionKey).
				Count(&legacyRequestProfileOptions).Error)
			assert.Zero(t, legacyRequestProfileOptions)

			assert.True(t, db.Migrator().HasTable(&TaskBillingOperation{}))
			assert.True(t, db.Migrator().HasIndex(&TaskBillingOperation{}, "uidx_task_billing_operation_task"))
			assert.True(t, db.Migrator().HasIndex(&TaskBillingOperation{}, "uidx_task_billing_operation_key"))
			assert.True(t, db.Migrator().HasIndex(&TaskBillingOperation{}, "idx_task_billing_pending"))
			assert.True(t, db.Migrator().HasIndex(&TaskBillingOperation{}, "idx_task_billing_lease"))
			assert.True(t, db.Migrator().HasIndex(&TaskBillingOperation{}, "idx_task_billing_log_pending"))

			assert.True(t, db.Migrator().HasTable(&MidjourneyBillingOperation{}))
			assert.True(t, db.Migrator().HasIndex(&MidjourneyBillingOperation{}, "uidx_midjourney_billing_operation_task"))
			assert.True(t, db.Migrator().HasIndex(&MidjourneyBillingOperation{}, "uidx_midjourney_billing_operation_key"))
			assert.True(t, db.Migrator().HasIndex(&MidjourneyBillingOperation{}, "idx_midjourney_billing_pending"))
			assert.True(t, db.Migrator().HasIndex(&MidjourneyBillingOperation{}, "idx_midjourney_billing_lease"))
			assert.True(t, db.Migrator().HasIndex(&MidjourneyBillingOperation{}, "idx_midjourney_billing_log_pending"))
			assert.True(t, db.Migrator().HasTable(&IdentityCacheSync{}))
			assert.True(t, db.Migrator().HasIndex(&IdentityCacheSync{}, "idx_identity_cache_sync_pending"))
			assert.True(t, db.Migrator().HasIndex(&IdentityCacheSync{}, "idx_identity_cache_sync_live_pending"))
			var retainedLog Log
			require.NoError(t, db.Where("request_id = ?", "legacy-main-log").First(&retainedLog).Error)
			assert.Equal(t, "v0.1.10 main migration log", retainedLog.Content)
			assert.Nil(t, retainedLog.BillingOperationKey)
			assert.True(t, db.Migrator().HasIndex(&Log{}, billingLogOperationKeyIndex))

			ready, err := RoutingSchemaReady(db)
			require.NoError(t, err)
			assert.True(t, ready)
			asyncBillingReady, err := AsyncBillingV2SchemaReady(db)
			require.NoError(t, err)
			assert.True(t, asyncBillingReady)
			manualIndexReady, err := asyncBillingUniqueIndexReady(
				db, "async_billing_manual_resolutions", asyncBillingManualResolutionUniqueIndex,
				[]string{"reservation_id", "expected_version"},
			)
			require.NoError(t, err)
			assert.True(t, manualIndexReady)
		})
	}
}

type routingRetirementPreflightAccountHealth struct {
	AccountID int `gorm:"primaryKey"`
}

func (routingRetirementPreflightAccountHealth) TableName() string {
	return retiredRoutingUpstreamAccountHealthTable
}

type routingRetirementPreflightChannelHealth struct {
	ID                 int `gorm:"primaryKey"`
	ChannelID          int `gorm:"uniqueIndex;not null"`
	AuthFailure        bool
	AuthFailureReason  string `gorm:"type:varchar(128)"`
	AuthFailureUntil   int64  `gorm:"bigint"`
	BalanceKnown       bool
	Balance            float64
	BalanceUpdatedTime int64 `gorm:"bigint"`
	UpdatedTime        int64 `gorm:"bigint"`
}

func (routingRetirementPreflightChannelHealth) TableName() string {
	return (RoutingChannelHealthState{}).TableName()
}

type routingRetirementUnexpectedRollupIndex struct {
	MemberID int `gorm:"uniqueIndex:idx_routing_metric_rollup_key"`
}

func (routingRetirementUnexpectedRollupIndex) TableName() string {
	return (RoutingMetricRollup{}).TableName()
}
