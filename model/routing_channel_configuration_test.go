package model

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelCreationInitializesDefaultRoutingConfiguration(t *testing.T) {
	db := openRoutingChannelConfigurationTestDB(t)

	direct := Channel{Id: 101, Name: "direct", Models: "gpt-direct", Group: "default"}
	require.NoError(t, DB.Create(&direct).Error)

	inserted := Channel{Id: 102, Name: "inserted", Models: "gpt-inserted", Group: "default"}
	require.NoError(t, inserted.Insert())

	require.NoError(t, BatchInsertChannels([]Channel{
		{Id: 103, Name: "batch-a", Models: "gpt-batch-a", Group: "default"},
		{Id: 104, Name: "batch-b", Models: "gpt-batch-b", Group: "default"},
	}))

	var configurations []RoutingChannelConfiguration
	require.NoError(t, db.Order("channel_id asc").Find(&configurations).Error)
	require.Len(t, configurations, 4)
	for index := range configurations {
		configuration := configurations[index]
		assert.Equal(t, 101+index, configuration.ChannelID)
		assert.Equal(t, float64(1), configuration.UpstreamCostMultiplier)
		assert.Equal(t, RoutingChannelCostSourceDefaulted, configuration.CostSource)
		assert.False(t, configuration.CostConfirmed)
		assert.Equal(t, RoutingChannelTrafficClassAll, configuration.TrafficClass)
		assert.Equal(t, RoutingFailureDomainStatusUnconfigured, configuration.FailureDomainStatus)
		assert.Equal(t, int64(1), configuration.Revision)
		assert.True(t, ValidRoutingChannelConfiguration(configuration))
	}

	var bootstrapAudits int64
	require.NoError(t, db.Model(&RoutingControlAudit{}).
		Where("subject_type = ? AND action = ?", RoutingControlSubjectChannelConfiguration, RoutingControlActionBootstrap).
		Count(&bootstrapAudits).Error)
	assert.Equal(t, int64(4), bootstrapAudits)
}

func TestChannelCreationRollsBackWhenDefaultConfigurationFails(t *testing.T) {
	db := openRoutingChannelConfigurationTestDB(t)
	require.NoError(t, db.Exec(`
		CREATE TRIGGER fail_routing_channel_configuration_insert
		BEFORE INSERT ON routing_channel_configurations
		BEGIN
			SELECT RAISE(FAIL, 'forced routing configuration failure');
		END
	`).Error)

	err := DB.Create(&Channel{Id: 201, Name: "atomic", Models: "gpt-atomic", Group: "default"}).Error
	require.Error(t, err)

	var channelCount int64
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", 201).Count(&channelCount).Error)
	assert.Zero(t, channelCount)
	var configurationCount int64
	require.NoError(t, db.Model(&RoutingChannelConfiguration{}).Where("channel_id = ?", 201).Count(&configurationCount).Error)
	assert.Zero(t, configurationCount)
}

func TestRoutingChannelConfigurationMultiplierEpochAndOutbox(t *testing.T) {
	db := openRoutingChannelConfigurationTestDB(t)
	require.NoError(t, DB.Create(&[]Channel{
		{Id: 301, Name: "multiplier-a", Models: "gpt-a", Group: "default"},
		{Id: 302, Name: "multiplier-b", Models: "gpt-b", Group: "default"},
	}).Error)

	initialEpoch, err := GetRoutingConfigurationEpochContext(context.Background())
	require.NoError(t, err)
	assert.Zero(t, initialEpoch.Epoch)
	assert.Len(t, initialEpoch.StateHash, 64)

	configuration, err := GetRoutingChannelConfigurationContext(context.Background(), 301)
	require.NoError(t, err)
	for _, invalid := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1), 1000.0001} {
		_, updateErr := UpdateRoutingChannelConfigurationContext(
			context.Background(), configuration, invalid, RoutingChannelTrafficClassAll, "", false, 10,
		)
		assert.ErrorIs(t, updateErr, ErrRoutingChannelConfigurationInvalid)
	}
	values := []float64{0, 0.5, 1, 2, 1000}
	for index, value := range values {
		mutation, updateErr := UpdateRoutingChannelConfigurationContext(
			context.Background(), configuration, value, RoutingChannelTrafficClassAll, "", false, 10,
		)
		require.NoError(t, updateErr)
		configuration = mutation.Configuration
		assert.Equal(t, value, configuration.UpstreamCostMultiplier)
		assert.Equal(t, RoutingChannelCostSourceManual, configuration.CostSource)
		assert.True(t, configuration.CostConfirmed)
		assert.Equal(t, int64(index+2), configuration.Revision)
		assert.Equal(t, int64(index+1), mutation.Outbox.ConfigEpoch)

		event, decodeErr := mutation.Outbox.DecodePayload()
		require.NoError(t, decodeErr)
		assert.Equal(t, mutation.Outbox.ConfigEpoch, event.ConfigEpoch)
		assert.Equal(t, configuration.ChannelID, event.ChannelID)
		assert.Equal(t, configuration.Revision, event.Revision)
		assert.Equal(t, configuration.Revision-1, event.PreviousRevision)
		assert.Equal(t, mutation.Outbox.Revision, event.Revision)
		assert.Equal(t, mutation.Outbox.EventType, event.EventType)
		assert.Len(t, event.ConfigurationHash, 64)
		assert.Len(t, event.StateHash, 64)

		epoch, epochErr := GetRoutingConfigurationEpochContext(context.Background())
		require.NoError(t, epochErr)
		assert.Equal(t, mutation.Outbox.ConfigEpoch, epoch.Epoch)
		assert.Equal(t, event.ConfigurationHash, epoch.StateHash)
	}

	other, err := GetRoutingChannelConfigurationContext(context.Background(), 302)
	require.NoError(t, err)
	otherMutation, err := UpdateRoutingChannelConfigurationContext(
		context.Background(), other, 0.5, RoutingChannelTrafficClassClaudeCodeOnly, "", false, 11,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(6), otherMutation.Outbox.ConfigEpoch)
	assert.Equal(t, int64(2), otherMutation.Configuration.Revision)

	var outboxes []RoutingChannelConfigurationOutbox
	require.NoError(t, db.Order("config_epoch asc").Find(&outboxes).Error)
	require.Len(t, outboxes, 6)
	for index := range outboxes {
		assert.Equal(t, int64(index+1), outboxes[index].ConfigEpoch)
	}
	var updateAudits int64
	require.NoError(t, db.Model(&RoutingControlAudit{}).
		Where("subject_type = ? AND action = ?", RoutingControlSubjectChannelConfiguration, RoutingControlActionUpdate).
		Count(&updateAudits).Error)
	assert.Equal(t, int64(6), updateAudits)
}

func TestRoutingChannelConfigurationUpdateIsAtomic(t *testing.T) {
	db := openRoutingChannelConfigurationTestDB(t)
	require.NoError(t, DB.Create(&Channel{Id: 401, Name: "atomic-update", Models: "gpt-test", Group: "default"}).Error)
	before, err := GetRoutingChannelConfigurationContext(context.Background(), 401)
	require.NoError(t, err)
	beforeEpoch, err := GetRoutingConfigurationEpochContext(context.Background())
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TRIGGER fail_routing_channel_configuration_outbox
		BEFORE INSERT ON routing_channel_configuration_outbox
		BEGIN
			SELECT RAISE(FAIL, 'forced routing outbox failure');
		END
	`).Error)

	_, err = UpdateRoutingChannelConfigurationContext(
		context.Background(), before, 2, RoutingChannelTrafficClassClaudeCodeOnly, "zone-a", false, 10,
	)
	require.Error(t, err)

	after, err := GetRoutingChannelConfigurationContext(context.Background(), 401)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	afterEpoch, err := GetRoutingConfigurationEpochContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, beforeEpoch, afterEpoch)
	var updateAudits int64
	require.NoError(t, db.Model(&RoutingControlAudit{}).
		Where("subject_type = ? AND action = ?", RoutingControlSubjectChannelConfiguration, RoutingControlActionUpdate).
		Count(&updateAudits).Error)
	assert.Zero(t, updateAudits)
}

func TestRoutingChannelConfigurationPreservesHistoricalFailureDomainUntilExplicitClear(t *testing.T) {
	db := openRoutingChannelConfigurationTestDB(t)
	require.NoError(t, DB.Create(&Channel{Id: 501, Name: "failure-domain", Models: "gpt-test", Group: "default"}).Error)
	historicalHash := strings.Repeat("a", 64)
	require.NoError(t, db.Model(&RoutingChannelConfiguration{}).Where("channel_id = ?", 501).Updates(map[string]any{
		"failure_domain_hash": historicalHash, "failure_domain_status": RoutingFailureDomainStatusHistoricalMigrated,
	}).Error)

	historical, err := GetRoutingChannelConfigurationContext(context.Background(), 501)
	require.NoError(t, err)
	require.True(t, ValidRoutingChannelConfiguration(historical))
	preserved, err := UpdateRoutingChannelConfigurationContext(
		context.Background(), historical, 2, RoutingChannelTrafficClassAll, "", false, 10,
	)
	require.NoError(t, err)
	assert.Equal(t, historicalHash, preserved.Configuration.FailureDomainHash)
	assert.Equal(t, RoutingFailureDomainStatusHistoricalMigrated, preserved.Configuration.FailureDomainStatus)

	cleared, err := UpdateRoutingChannelConfigurationContext(
		context.Background(), preserved.Configuration, 2, RoutingChannelTrafficClassAll, "", true, 10,
	)
	require.NoError(t, err)
	assert.Empty(t, cleared.Configuration.FailureDomainHash)
	assert.Empty(t, cleared.Configuration.FailureDomainLabel)
	assert.Equal(t, RoutingFailureDomainStatusUnconfigured, cleared.Configuration.FailureDomainStatus)
}

func TestMigrateRoutingChannelConfigurationsUsesOnlyReliableHistoryAndIsIdempotent(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&Channel{}, &RoutingChannelBinding{}, &RoutingUpstreamAccount{},
		&RoutingCostSnapshotVersion{}, &RoutingCostSnapshot{},
	))
	require.NoError(t, db.Create(&[]Channel{
		{Id: 601, Name: "reliable-fallback", Models: "model-a,model-b", Group: "default", CreatedTime: 100},
		{Id: 602, Name: "conflict-only", Models: "model-a,model-b", Group: "default", CreatedTime: 100},
		{Id: 603, Name: "explicit-zero", Models: "model-a", Group: "default", CreatedTime: 100},
		{Id: 604, Name: "manual", Models: "model-a", Group: "default", CreatedTime: 100},
		{Id: 605, Name: "missing", Models: "model-a", Group: "default", CreatedTime: 100},
	}).Error)
	now := common.GetTimestamp()
	account := RoutingUpstreamAccount{
		AccountKey: routingCostHash([]byte("legacy-account")), SourceType: RoutingUpstreamTypeNewAPI,
		MaskedIdentity: "legacy-***", Status: "active", LastSyncStatus: RoutingUpstreamSyncStatusSuccess,
		CreatedTime: now, UpdatedTime: now,
	}
	require.NoError(t, db.Create(&account).Error)
	require.NoError(t, db.Create(&[]RoutingChannelBinding{
		{ChannelID: 601, UpstreamType: RoutingUpstreamTypeNewAPI, BaseURL: "https://legacy.example", UpstreamGroup: "vip", Enabled: true, ServesClaudeCode: true, AccountKeyHash: account.AccountKey},
		{ChannelID: 602, UpstreamType: RoutingUpstreamTypeNewAPI, BaseURL: "https://legacy.example", UpstreamGroup: "vip", Enabled: true, AccountKeyHash: account.AccountKey},
		{ChannelID: 603, UpstreamType: RoutingUpstreamTypeNewAPI, BaseURL: "https://legacy.example", UpstreamGroup: "vip", Enabled: true, AccountKeyHash: account.AccountKey},
	}).Error)

	writeLegacyRoutingCostVersionForConfigurationTest(t, account.ID, 601, "model-a", now-100, 0.5)
	writeLegacyRoutingCostVersionForConfigurationTest(t, account.ID, 601, "model-b", now-100, 0.5)
	writeLegacyRoutingCostVersionForConfigurationTest(t, account.ID, 601, "model-a", now, 0.75)
	writeLegacyRoutingCostVersionForConfigurationTest(t, account.ID, 601, "model-b", now, 1)
	writeLegacyRoutingCostVersionForConfigurationTest(t, account.ID, 602, "model-a", now, 0.5)
	writeLegacyRoutingCostVersionForConfigurationTest(t, account.ID, 602, "model-b", now, 2)
	writeLegacyRoutingCostVersionForConfigurationTest(t, account.ID, 603, "model-a", now, 0)

	require.NoError(t, db.AutoMigrate(
		&RoutingConfigurationEpoch{}, &RoutingChannelConfiguration{},
		&RoutingChannelConfigurationOutbox{}, &RoutingControlAudit{},
	))
	require.NoError(t, EnsureRoutingConfigurationEpoch(db))
	var manualChannel Channel
	require.NoError(t, db.Where("id = ?", 604).First(&manualChannel).Error)
	manual, err := NewDefaultRoutingChannelConfigurationForChannel(manualChannel)
	require.NoError(t, err)
	manual.UpstreamCostMultiplier = 3
	manual.CostSource = RoutingChannelCostSourceManual
	manual.CostConfirmed = true
	manual.Revision = 7
	manual.UpdatedBy = 99
	manual.UpdatedTime = 200
	require.True(t, ValidRoutingChannelConfiguration(manual))
	require.NoError(t, db.Create(&manual).Error)

	require.NoError(t, MigrateRoutingChannelConfigurations(db))
	configurations := make(map[int]RoutingChannelConfiguration)
	var rows []RoutingChannelConfiguration
	require.NoError(t, db.Order("channel_id asc").Find(&rows).Error)
	require.Len(t, rows, 5)
	for index := range rows {
		configurations[rows[index].ChannelID] = rows[index]
	}

	reliable := configurations[601]
	assert.Equal(t, 0.5, reliable.UpstreamCostMultiplier)
	assert.Equal(t, RoutingChannelCostSourceLegacyMigrated, reliable.CostSource)
	assert.False(t, reliable.CostConfirmed)
	assert.Equal(t, RoutingChannelTrafficClassClaudeCodeOnly, reliable.TrafficClass)
	assert.Equal(t, RoutingFailureDomainStatusHistoricalMigrated, reliable.FailureDomainStatus)
	assert.Len(t, reliable.FailureDomainHash, 64)
	assert.Empty(t, reliable.FailureDomainLabel)

	conflict := configurations[602]
	assert.Equal(t, float64(1), conflict.UpstreamCostMultiplier)
	assert.Equal(t, RoutingChannelCostSourceDefaulted, conflict.CostSource)
	assert.False(t, conflict.CostConfirmed)

	explicitZero := configurations[603]
	assert.Equal(t, float64(0), explicitZero.UpstreamCostMultiplier)
	assert.Equal(t, RoutingChannelCostSourceLegacyMigrated, explicitZero.CostSource)
	assert.False(t, explicitZero.CostConfirmed)

	assert.Equal(t, manual, configurations[604])
	missing := configurations[605]
	assert.Equal(t, float64(1), missing.UpstreamCostMultiplier)
	assert.Equal(t, RoutingChannelCostSourceDefaulted, missing.CostSource)

	beforeRestart := reliable
	require.NoError(t, db.Model(&RoutingChannelBinding{}).Where("channel_id = ?", 601).Updates(map[string]any{
		"serves_claude_code": false, "account_key_hash": "",
	}).Error)
	require.NoError(t, MigrateRoutingChannelConfigurations(db))
	afterRestart, err := GetRoutingChannelConfigurationContext(context.Background(), 601)
	require.NoError(t, err)
	assert.Equal(t, beforeRestart, afterRestart)
}

func TestRetireRoutingUpstreamConnectorsDoesNotRequireRedisClient(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&Channel{}, &RoutingChannelConfiguration{}, &RoutingChannelBinding{},
		&legacyRoutingUpstreamAccountHealthStateForTest{},
	))
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&Channel{
		Id: 701, Name: "retirement", Key: "serving-key", CreatedTime: 100,
	}).Error)
	configuration, err := NewDefaultRoutingChannelConfiguration(701, 100)
	require.NoError(t, err)
	require.NoError(t, db.Create(&configuration).Error)
	secret := "retired-encrypted-credential"
	require.NoError(t, db.Create(&RoutingChannelBinding{
		ChannelID: 701, UpstreamType: RoutingUpstreamTypeSub2API,
		BaseURL: "https://retired.example", UpstreamGroup: "legacy",
		Enabled: true, EncCredentials: &secret, KeyVersion: 2,
	}).Error)
	require.NoError(t, db.Create(&legacyRoutingUpstreamAccountHealthStateForTest{AccountID: 901}).Error)

	previousRedisEnabled := common.RedisEnabled
	previousRedisClient := common.RDB
	common.RedisEnabled = true
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRedisClient
	})

	result, err := retireRoutingUpstreamAccountConnectorsDB(context.Background(), db)
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.BindingsScrubbed)
	assert.Equal(t, int64(1), result.AccountHealthRowsCleared)

	var retired RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", 701).First(&retired).Error)
	assert.False(t, retired.Enabled)
	assert.Nil(t, retired.EncCredentials)
	assert.Zero(t, retired.KeyVersion)

	restarted, err := retireRoutingUpstreamAccountConnectorsDB(context.Background(), db)
	require.NoError(t, err)
	assert.Zero(t, restarted.AccountHealthRowsCleared)
	var accountHealthRows int64
	require.NoError(t, db.Table(retiredRoutingUpstreamAccountHealthTable).Count(&accountHealthRows).Error)
	assert.Zero(t, accountHealthRows)

	require.NoError(t, db.Migrator().DropTable(retiredRoutingUpstreamAccountHealthTable))
	_, err = retireRoutingUpstreamAccountConnectorsDB(context.Background(), db)
	require.NoError(t, err)
	assert.False(t, db.Migrator().HasTable(retiredRoutingUpstreamAccountHealthTable),
		"connector retirement must not recreate the retired account-health table")
}

func TestRetireRoutingUpstreamConnectorsClearsSub2APIJWTCachesInRedis(t *testing.T) {
	address := os.Getenv("ROUTING_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("ROUTING_TEST_REDIS_ADDR is not set")
	}

	previousRedisClient := common.RDB
	previousRedisEnabled := common.RedisEnabled
	client := redis.NewClient(&redis.Options{Addr: address})
	redisReady := false
	t.Cleanup(func() {
		var flushErr error
		if redisReady {
			flushErr = client.FlushDB(context.Background()).Err()
		}
		closeErr := client.Close()
		common.RDB = previousRedisClient
		common.RedisEnabled = previousRedisEnabled
		assert.NoError(t, flushErr)
		assert.NoError(t, closeErr)
	})
	require.NoError(t, client.Ping(context.Background()).Err())
	require.NoError(t, client.FlushDB(context.Background()).Err())
	redisReady = true
	common.RDB = client
	common.RedisEnabled = true

	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&Channel{}, &RoutingChannelConfiguration{}, &RoutingChannelBinding{},
	))
	const channelID = 702
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&Channel{
		Id: channelID, Name: "redis-retirement", Key: "serving-key", CreatedTime: 100,
	}).Error)
	configuration, err := NewDefaultRoutingChannelConfiguration(channelID, 100)
	require.NoError(t, err)
	require.NoError(t, db.Create(&configuration).Error)
	require.NoError(t, db.Create(&RoutingChannelBinding{
		ChannelID: channelID, UpstreamType: RoutingUpstreamTypeSub2API,
		BaseURL: "https://retired.example", UpstreamGroup: "legacy", Enabled: true,
	}).Error)

	targetKeys := []string{
		"routing:sub2api:jwt:702",
		"routing:sub2api:jwt:702:session",
		"routing:sub2api:retired:702",
		"routing:sub2api:retired:702:marker",
		"routing:sub2api:lock:702",
		"routing:sub2api:lock:702:lease",
	}
	const unrelatedKey = "routing:sub2api:jwt:703"
	for _, key := range append(targetKeys, unrelatedKey) {
		require.NoError(t, client.Set(context.Background(), key, "fixture", 0).Err())
	}

	result, err := retireRoutingUpstreamAccountConnectorsDB(context.Background(), db)
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.BindingsScrubbed)
	for _, key := range targetKeys {
		exists, existsErr := client.Exists(context.Background(), key).Result()
		require.NoError(t, existsErr)
		assert.Zero(t, exists, key)
	}
	unrelatedExists, err := client.Exists(context.Background(), unrelatedKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), unrelatedExists)

	restarted, err := retireRoutingUpstreamAccountConnectorsDB(context.Background(), db)
	require.NoError(t, err)
	assert.Zero(t, restarted.BindingsScrubbed)
	for _, key := range targetKeys {
		exists, existsErr := client.Exists(context.Background(), key).Result()
		require.NoError(t, existsErr)
		assert.Zero(t, exists, key)
	}
	unrelatedExists, err = client.Exists(context.Background(), unrelatedKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), unrelatedExists)
}

func TestRoutingChannelConfigurationLegacyConnectorSchemaCompatibility(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingChannelConfigurationLegacyConnectorSchemaContract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingChannelConfigurationLegacyConnectorSchemaExternalCompatibility(t *testing.T) {
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
			for _, table := range []any{
				&RoutingConfigurationEpoch{}, &RoutingChannelConfiguration{}, &RoutingControlAudit{},
			} {
				if db.Migrator().HasTable(table) {
					t.Skipf("refusing to run against external database because %s already exists", table.(interface{ TableName() string }).TableName())
				}
			}
			runRoutingChannelConfigurationLegacyConnectorSchemaContract(t, db, test.dbType)
		})
	}
}

func runRoutingChannelConfigurationLegacyConnectorSchemaContract(
	t *testing.T,
	db *gorm.DB,
	dbType common.DatabaseType,
) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	previousRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedisEnabled })
	newTables := []any{&RoutingConfigurationEpoch{}, &RoutingChannelConfiguration{}, &RoutingControlAudit{}}
	t.Cleanup(func() {
		for index := len(newTables) - 1; index >= 0; index-- {
			_ = db.Migrator().DropTable(newTables[index])
		}
	})

	require.NoError(t, db.AutoMigrate(
		&Channel{},
		&routingChannelBindingBeforeSyncFailureCount{},
		&routingUpstreamAccountPartialForRetirementTest{},
		&routingChannelHealthStateWithRetiredBalanceForTest{},
		&routingCostSnapshotPartialForRetirementTest{},
	))
	channel := Channel{
		Id: 801, Name: "legacy-upgrade", Key: "serving-key", Balance: 42, BalanceUpdatedTime: 123,
		Models: "gpt-test", Group: "default", CreatedTime: 100,
	}
	require.NoError(t, db.Create(&channel).Error)
	secret := "encrypted-management-credential"
	lastSyncError := "legacy connector failed"
	require.NoError(t, db.Create(&routingChannelBindingBeforeSyncFailureCount{
		ChannelID: 801, UpstreamType: RoutingUpstreamTypeSub2API,
		BaseURL: "https://legacy.example", UpstreamGroup: "legacy", ServesClaudeCode: true,
		EncCredentials: &secret, KeyVersion: 2, Enabled: true, SyncBackoffUntil: 999,
		LastSyncError: &lastSyncError, CreatedTime: 100, UpdatedTime: 100,
	}).Error)
	require.NoError(t, db.Create(&routingUpstreamAccountPartialForRetirementTest{
		ID: 901, AccountKey: strings.Repeat("a", 64), SourceType: RoutingUpstreamTypeSub2API,
		MaskedIdentity: "legacy-user", Status: RoutingUpstreamAccountStatusActive,
		BalanceKnown: true, Balance: 17, BalanceUpdatedAt: 120, CreatedTime: 100,
	}).Error)
	require.NoError(t, db.Create(&routingChannelHealthStateWithRetiredBalanceForTest{
		ChannelID: 801, AuthFailure: true, AuthFailureReason: "serving credential rejected",
		AuthFailureUntil: 1_000, BalanceKnown: true, Balance: 17, BalanceUpdatedTime: 120, UpdatedTime: 120,
	}).Error)
	require.NoError(t, db.Create(&routingCostSnapshotPartialForRetirementTest{
		ChannelID: 801, ModelName: "gpt-test", AccountKeyHash: strings.Repeat("b", 64),
	}).Error)

	for _, missingColumn := range []string{"egress_policy_json", "account_key_hash", "sync_failure_count"} {
		assert.False(t, db.Migrator().HasColumn(&RoutingChannelBinding{}, missingColumn))
	}
	require.NoError(t, db.AutoMigrate(newTables...))
	require.NoError(t, EnsureRoutingConfigurationEpoch(db))
	require.NoError(t, MigrateRoutingChannelConfigurations(db))

	configuration, err := GetRoutingChannelConfigurationContext(context.Background(), 801)
	require.NoError(t, err)
	require.True(t, ValidRoutingChannelConfiguration(configuration), "%+v", configuration)
	assert.Equal(t, RoutingChannelTrafficClassClaudeCodeOnly, configuration.TrafficClass)
	assert.Equal(t, RoutingFailureDomainStatusUnconfigured, configuration.FailureDomainStatus)
	assert.Equal(t, float64(1), configuration.UpstreamCostMultiplier)
	beforeRestart := configuration

	result, err := retireRoutingUpstreamAccountConnectorsDB(context.Background(), db)
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.BindingsScrubbed)
	assert.Equal(t, int64(1), result.AccountsScrubbed)
	assert.Equal(t, int64(1), result.ChannelBalanceRowsCleared)

	var retiredBinding routingChannelBindingBeforeSyncFailureCount
	require.NoError(t, db.Where("channel_id = ?", 801).First(&retiredBinding).Error)
	assert.False(t, retiredBinding.Enabled)
	assert.Nil(t, retiredBinding.EncCredentials)
	assert.Zero(t, retiredBinding.KeyVersion)
	assert.Zero(t, retiredBinding.SyncBackoffUntil)
	assert.Nil(t, retiredBinding.LastSyncError)

	var retiredAccount routingUpstreamAccountPartialForRetirementTest
	require.NoError(t, db.Where("id = ?", 901).First(&retiredAccount).Error)
	assert.Equal(t, "retired", retiredAccount.MaskedIdentity)
	assert.Equal(t, RoutingUpstreamAccountStatusDisabled, retiredAccount.Status)
	assert.False(t, retiredAccount.BalanceKnown)
	assert.Zero(t, retiredAccount.Balance)
	assert.Zero(t, retiredAccount.BalanceUpdatedAt)

	var retiredHealth routingChannelHealthStateWithRetiredBalanceForTest
	require.NoError(t, db.Where("channel_id = ?", 801).First(&retiredHealth).Error)
	assert.True(t, retiredHealth.AuthFailure, "serving credential health remains current routing state")
	assert.Equal(t, "serving credential rejected", retiredHealth.AuthFailureReason)
	assert.False(t, retiredHealth.BalanceKnown)
	assert.Zero(t, retiredHealth.Balance)
	assert.Zero(t, retiredHealth.BalanceUpdatedTime)

	var retiredCost routingCostSnapshotPartialForRetirementTest
	require.NoError(t, db.Where("channel_id = ?", 801).First(&retiredCost).Error)
	assert.Empty(t, strings.TrimSpace(retiredCost.AccountKeyHash))
	var preservedChannel Channel
	require.NoError(t, db.Where("id = ?", 801).First(&preservedChannel).Error)
	assert.Equal(t, "serving-key", preservedChannel.Key)
	assert.Equal(t, float64(42), preservedChannel.Balance)
	assert.Equal(t, int64(123), preservedChannel.BalanceUpdatedTime)

	require.NoError(t, MigrateRoutingChannelConfigurations(db))
	afterRestart, err := GetRoutingChannelConfigurationContext(context.Background(), 801)
	require.NoError(t, err)
	assert.Equal(t, beforeRestart, afterRestart)
	restarted, err := retireRoutingUpstreamAccountConnectorsDB(context.Background(), db)
	require.NoError(t, err)
	assert.Zero(t, restarted.BindingsScrubbed)
	assert.Zero(t, restarted.AccountsScrubbed)
	assert.Zero(t, restarted.ChannelBalanceRowsCleared)
	for _, missingColumn := range []string{"egress_policy_json", "account_key_hash", "sync_failure_count"} {
		assert.False(t, db.Migrator().HasColumn(&RoutingChannelBinding{}, missingColumn),
			"retirement must not rebuild retired connector columns")
	}
}

type legacyRoutingUpstreamAccountHealthStateForTest struct {
	AccountID int `gorm:"primaryKey"`
}

func (legacyRoutingUpstreamAccountHealthStateForTest) TableName() string {
	return retiredRoutingUpstreamAccountHealthTable
}

type routingUpstreamAccountPartialForRetirementTest struct {
	ID               int    `gorm:"primaryKey"`
	AccountKey       string `gorm:"type:char(64);uniqueIndex;not null"`
	SourceType       string `gorm:"type:varchar(32);not null"`
	MaskedIdentity   string `gorm:"type:varchar(256)"`
	Status           string `gorm:"type:varchar(32)"`
	BalanceKnown     bool
	Balance          float64
	BalanceUpdatedAt int64 `gorm:"bigint"`
	CreatedTime      int64 `gorm:"bigint"`
}

func (routingUpstreamAccountPartialForRetirementTest) TableName() string {
	return "routing_upstream_accounts"
}

type routingChannelHealthStateWithRetiredBalanceForTest struct {
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

func (routingChannelHealthStateWithRetiredBalanceForTest) TableName() string {
	return "routing_channel_health_states"
}

type routingCostSnapshotPartialForRetirementTest struct {
	ID             int    `gorm:"primaryKey"`
	ChannelID      int    `gorm:"index"`
	ModelName      string `gorm:"type:varchar(128)"`
	AccountKeyHash string `gorm:"type:char(64)"`
}

func (routingCostSnapshotPartialForRetirementTest) TableName() string {
	return "routing_cost_snapshots"
}

func openRoutingChannelConfigurationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&Channel{}, &Ability{}, &RoutingChannelLifecycle{}, &RoutingConfigurationEpoch{}, &RoutingChannelConfiguration{},
		&RoutingChannelConfigurationOutbox{}, &RoutingControlAudit{},
	))
	require.NoError(t, EnsureRoutingConfigurationEpoch(db))
	return db
}

func writeLegacyRoutingCostVersionForConfigurationTest(
	t *testing.T,
	accountID int,
	channelID int,
	modelName string,
	observedTime int64,
	multiplier float64,
) {
	t.Helper()
	var account RoutingUpstreamAccount
	require.NoError(t, DB.Where("id = ?", accountID).First(&account).Error)
	inputRate := 1.0
	manifest := routingCostSnapshotManifest{
		AccountID: accountID, ChannelID: channelID, UpstreamGroup: "vip",
		UpstreamModel: modelName, LocalModel: modelName,
		ObservedTime: observedTime, EffectiveTime: observedTime, ExpiresTime: observedTime + 3_600,
		PricingVersion: "legacy-migration-v1", Confidence: RoutingCostConfidenceExact, ConfidenceScore: 1,
		Freshness: RoutingCostFreshnessFresh, FreshnessScore: 1,
		SourceSyncStatus: RoutingUpstreamSyncStatusSuccess,
		Pricing: RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "token", Currency: "USD", Unit: "million_tokens",
			GroupRatio: &multiplier, InputCostPerMillion: &inputRate,
		},
	}
	normalizedPricing, pricingJSON, err := normalizeRoutingNormalizedPricing(manifest.Pricing)
	require.NoError(t, err)
	manifest.Pricing = normalizedPricing
	pricingHash, err := routingCostPricingHash(account, manifest, pricingJSON)
	require.NoError(t, err)
	contentHash, err := routingCostContentHash(account, manifest, pricingJSON)
	require.NoError(t, err)
	require.NoError(t, DB.Create(&RoutingCostSnapshotVersion{
		SchemaVersion: routingCostSnapshotVersionSchema, PricingHash: pricingHash, ContentHash: contentHash,
		ApplyToken: "legacy-migration-fixture", AccountID: account.ID, AccountKey: account.AccountKey,
		SourceType: account.SourceType, ChannelID: channelID,
		UpstreamGroup: manifest.UpstreamGroup, UpstreamGroupKey: routingCostHash([]byte(manifest.UpstreamGroup)),
		UpstreamModel: manifest.UpstreamModel, UpstreamModelKey: RoutingCostModelKey(manifest.UpstreamModel),
		LocalModel: manifest.LocalModel, LocalModelKey: RoutingCostModelKey(manifest.LocalModel),
		ObservedTime: manifest.ObservedTime, EffectiveTime: manifest.EffectiveTime, ExpiresTime: manifest.ExpiresTime,
		PricingVersion: manifest.PricingVersion, PricingJSON: string(pricingJSON),
		Confidence: manifest.Confidence, ConfidenceScore: manifest.ConfidenceScore,
		Freshness: manifest.Freshness, FreshnessScore: manifest.FreshnessScore,
		SourceSyncStatus: manifest.SourceSyncStatus, SourceSyncError: manifest.SourceSyncError,
		CreatedTime: observedTime,
	}).Error)
}
