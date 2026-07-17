package model

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type routingChannelV013ForControlPlaneMigration struct {
	ID                int     `gorm:"column:id;primaryKey"`
	RoutingGeneration string  `gorm:"column:routing_generation;type:varchar(32)"`
	Type              int     `gorm:"column:type"`
	Key               string  `gorm:"column:key;not null"`
	Name              string  `gorm:"column:name;index"`
	Status            int     `gorm:"column:status"`
	Weight            *uint   `gorm:"column:weight"`
	Priority          *int64  `gorm:"column:priority;bigint"`
	Models            string  `gorm:"column:models"`
	Group             string  `gorm:"column:group"`
	BaseURL           *string `gorm:"column:base_url"`
	CreatedTime       int64   `gorm:"column:created_time;bigint"`
}

func (routingChannelV013ForControlPlaneMigration) TableName() string {
	return "channels"
}

type routingChannelConfigurationV013ForControlPlaneMigration struct {
	ChannelID              int     `gorm:"column:channel_id;primaryKey;autoIncrement:false"`
	UpstreamCostMultiplier float64 `gorm:"column:upstream_cost_multiplier;not null"`
	CostSource             string  `gorm:"column:cost_source;type:varchar(32);index;not null"`
	CostConfirmed          bool    `gorm:"column:cost_confirmed;index;not null"`
	TrafficClass           string  `gorm:"column:traffic_class;type:varchar(32);index;not null"`
	FailureDomainLabel     string  `gorm:"column:failure_domain_label;type:varchar(512);not null"`
	FailureDomainHash      string  `gorm:"column:failure_domain_hash;type:char(64);index;not null"`
	FailureDomainStatus    string  `gorm:"column:failure_domain_status;type:varchar(32);index;not null"`
	Revision               int64   `gorm:"column:revision;bigint;not null"`
	UpdatedBy              int     `gorm:"column:updated_by;index;not null"`
	CreatedTime            int64   `gorm:"column:created_time;bigint;not null"`
	UpdatedTime            int64   `gorm:"column:updated_time;bigint;index;not null"`
}

func (routingChannelConfigurationV013ForControlPlaneMigration) TableName() string {
	return (RoutingChannelConfiguration{}).TableName()
}

type routingChannelConfigurationOutboxV013ForControlPlaneMigration struct {
	ID              int64  `gorm:"column:id;primaryKey"`
	EventID         string `gorm:"column:event_id;type:varchar(96);uniqueIndex;not null"`
	ChannelID       int    `gorm:"column:channel_id;index;not null"`
	Revision        int64  `gorm:"column:revision;bigint;index;not null"`
	ConfigEpoch     int64  `gorm:"column:config_epoch;bigint;uniqueIndex;not null"`
	EventType       string `gorm:"column:event_type;type:varchar(64);index;not null"`
	PayloadJSON     string `gorm:"column:payload_json;type:text;not null"`
	PayloadHash     string `gorm:"column:payload_hash;type:char(64);not null"`
	CreatedTime     int64  `gorm:"column:created_time;bigint;index;not null"`
	PublishedTime   int64  `gorm:"column:published_time;bigint;index;not null"`
	Attempts        int    `gorm:"column:attempts;not null"`
	NextAttemptTime int64  `gorm:"column:next_attempt_time;bigint;index;not null"`
	ClaimToken      string `gorm:"column:claim_token;type:char(32);index;not null"`
	ClaimedUntil    int64  `gorm:"column:claimed_until;bigint;index;not null"`
	LastError       string `gorm:"column:last_error;type:text;not null"`
}

func (routingChannelConfigurationOutboxV013ForControlPlaneMigration) TableName() string {
	return (RoutingChannelConfigurationOutbox{}).TableName()
}

type routingTopologyMetadataV013ForControlPlaneMigration struct {
	ID                   int    `gorm:"column:id;primaryKey"`
	CredentialSecretHash string `gorm:"column:credential_secret_hash;type:varchar(128);not null"`
	CreatedTime          int64  `gorm:"column:created_time;bigint"`
	UpdatedTime          int64  `gorm:"column:updated_time;bigint"`
}

func (routingTopologyMetadataV013ForControlPlaneMigration) TableName() string {
	return (RoutingTopologyMetadata{}).TableName()
}

type routingPoolV013ForControlPlaneMigration struct {
	ID          int    `gorm:"column:id;primaryKey"`
	GroupKey    string `gorm:"column:group_key;type:varchar(64);uniqueIndex;not null"`
	GroupName   string `gorm:"column:group_name;type:varchar(64);index;not null"`
	DisplayName string `gorm:"column:display_name;type:varchar(128);not null"`
	Source      string `gorm:"column:source;type:varchar(32);index;not null"`
	Active      bool   `gorm:"column:active;index"`
	CreatedTime int64  `gorm:"column:created_time;bigint"`
	UpdatedTime int64  `gorm:"column:updated_time;bigint;index"`
}

func (routingPoolV013ForControlPlaneMigration) TableName() string {
	return (RoutingPool{}).TableName()
}

type routingPoolMemberV013ForControlPlaneMigration struct {
	ID             int    `gorm:"column:id;primaryKey"`
	PoolID         int    `gorm:"column:pool_id;uniqueIndex:idx_routing_pool_member,priority:1;index;not null"`
	ChannelID      int    `gorm:"column:channel_id;uniqueIndex:idx_routing_pool_member,priority:2;index;not null"`
	Source         string `gorm:"column:source;type:varchar(32);index;not null"`
	Active         bool   `gorm:"column:active;index"`
	LegacyPriority int64  `gorm:"column:legacy_priority;bigint"`
	LegacyWeight   int64  `gorm:"column:legacy_weight;bigint"`
	CreatedTime    int64  `gorm:"column:created_time;bigint"`
	UpdatedTime    int64  `gorm:"column:updated_time;bigint;index"`
}

func (routingPoolMemberV013ForControlPlaneMigration) TableName() string {
	return (RoutingPoolMember{}).TableName()
}

type routingCredentialRefV013ForControlPlaneMigration struct {
	ID                 int    `gorm:"column:id;primaryKey"`
	ChannelID          int    `gorm:"column:channel_id;uniqueIndex:idx_routing_credential_ref,priority:1;index;not null"`
	ChannelGeneration  string `gorm:"column:channel_generation;type:varchar(32);index"`
	Fingerprint        string `gorm:"column:fingerprint;type:varchar(64);uniqueIndex:idx_routing_credential_ref,priority:2;not null"`
	FingerprintVersion int    `gorm:"column:fingerprint_version"`
	Active             bool   `gorm:"column:active;index"`
	LastSeenIndex      int    `gorm:"column:last_seen_index"`
	CurrentOccurrences int    `gorm:"column:current_occurrences"`
	CreatedTime        int64  `gorm:"column:created_time;bigint"`
	UpdatedTime        int64  `gorm:"column:updated_time;bigint;index"`
	RetiredTime        int64  `gorm:"column:retired_time;bigint;index"`
}

func (routingCredentialRefV013ForControlPlaneMigration) TableName() string {
	return (RoutingCredentialRef{}).TableName()
}

func TestRoutingControlPlaneV2UpgradeFromV013SQLite(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingControlPlaneV2UpgradeFromV013Contract(t, db, common.DatabaseTypeSQLite)
}

func TestRoutingControlPlaneV2UpgradeFromV013External(t *testing.T) {
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
				&routingChannelConfigurationV013ForControlPlaneMigration{},
				&routingChannelConfigurationOutboxV013ForControlPlaneMigration{},
			} {
				if db.Migrator().HasTable(table) {
					t.Skipf("refusing to run against external database because %s already exists", table.(interface{ TableName() string }).TableName())
				}
			}
			t.Cleanup(func() {
				_ = db.Migrator().DropTable(&RoutingChannelConfigurationOutbox{})
				_ = db.Migrator().DropTable(&RoutingChannelConfiguration{})
			})
			runRoutingControlPlaneV2UpgradeFromV013Contract(t, db, test.dbType)
		})
	}
}

func runRoutingControlPlaneV2UpgradeFromV013Contract(
	t *testing.T,
	db *gorm.DB,
	dbType common.DatabaseType,
) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "routing-control-plane-v2-migration-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	legacyModels := []any{
		&routingChannelV013ForControlPlaneMigration{},
		&routingChannelConfigurationV013ForControlPlaneMigration{},
		&routingChannelConfigurationOutboxV013ForControlPlaneMigration{},
		&routingTopologyMetadataV013ForControlPlaneMigration{},
		&routingPoolV013ForControlPlaneMigration{},
		&routingPoolMemberV013ForControlPlaneMigration{},
		&routingCredentialRefV013ForControlPlaneMigration{},
	}
	require.NoError(t, db.AutoMigrate(legacyModels...))

	const (
		channelID          = 1
		legacyMemberID     = 11
		legacyCredentialID = 21
	)
	channelGeneration := strings.Repeat("1", 32)
	priority := int64(7)
	weight := uint(23)
	require.NoError(t, db.Create(&routingChannelV013ForControlPlaneMigration{
		ID: channelID, RoutingGeneration: channelGeneration, Key: "legacy-serving-key", Name: "legacy channel",
		Status: common.ChannelStatusEnabled, Weight: &weight, Priority: &priority,
		Models: "gpt-test", Group: "default", CreatedTime: 100,
	}).Error)
	require.NoError(t, db.Create(&routingChannelConfigurationV013ForControlPlaneMigration{
		ChannelID: channelID, UpstreamCostMultiplier: 2, CostSource: RoutingChannelCostSourceManual,
		CostConfirmed: true, TrafficClass: RoutingChannelTrafficClassAll,
		FailureDomainStatus: RoutingFailureDomainStatusUnconfigured,
		Revision:            2, UpdatedBy: 7, CreatedTime: 100, UpdatedTime: 200,
	}).Error)

	legacyEventID := "routing-channel-config:1:00000000000000000002"
	legacyEvent := RoutingChannelConfigurationEvent{
		EventID: legacyEventID, EventType: RoutingChannelConfigurationEventType,
		Action: RoutingControlActionUpdate, ChannelID: channelID, Revision: 2, PreviousRevision: 1,
		ConfigEpoch: 1, ConfigurationHash: strings.Repeat("a", 64),
		TrafficClass: RoutingChannelTrafficClassAll, StateHash: strings.Repeat("b", 64), UpdatedTime: 200,
	}
	payload, err := common.Marshal(legacyEvent)
	require.NoError(t, err)
	require.NoError(t, db.Create(&routingChannelConfigurationOutboxV013ForControlPlaneMigration{
		EventID: legacyEventID, ChannelID: channelID, Revision: 2, ConfigEpoch: 1,
		EventType: RoutingChannelConfigurationEventType, PayloadJSON: string(payload),
		PayloadHash: routingChannelConfigurationHash(payload), CreatedTime: 200,
	}).Error)

	secretHash, err := common.Password2Hash(routingCredentialSecretVerifier())
	require.NoError(t, err)
	require.NoError(t, db.Create(&routingTopologyMetadataV013ForControlPlaneMigration{
		ID: routingTopologyMetadataID, CredentialSecretHash: secretHash, CreatedTime: 100, UpdatedTime: 100,
	}).Error)
	groupKey := routingGroupKey("default")
	require.NoError(t, db.Create(&routingPoolV013ForControlPlaneMigration{
		ID: 1, GroupKey: groupKey, GroupName: "default", DisplayName: "default",
		Source: RoutingPoolSourceLegacyGroup, Active: true, CreatedTime: 100, UpdatedTime: 100,
	}).Error)
	require.NoError(t, db.Create(&routingPoolMemberV013ForControlPlaneMigration{
		ID: legacyMemberID, PoolID: 1, ChannelID: channelID, Source: RoutingPoolSourceLegacyGroup,
		Active: true, LegacyPriority: priority, LegacyWeight: int64(weight), CreatedTime: 100, UpdatedTime: 100,
	}).Error)
	require.NoError(t, db.Create(&routingCredentialRefV013ForControlPlaneMigration{
		ID: legacyCredentialID, ChannelID: channelID, Fingerprint: strings.Repeat("c", 64),
		FingerprintVersion: RoutingCredentialFingerprintVersion, Active: true,
		CurrentOccurrences: 1, CreatedTime: 100, UpdatedTime: 100,
	}).Error)

	require.NoError(t, prepareRoutingControlPlaneV2Schema(db))
	// Simulate a MySQL non-transactional partial attempt from an older v2
	// prototype that already attached the legacy member to the current channel
	// generation. Absence of the completed cutover marker must still force a
	// conservative retirement instead of preserving that unproven binding.
	require.NoError(t, db.Migrator().AddColumn(&RoutingPoolMember{}, "ChannelGeneration"))
	require.NoError(t, db.Table((RoutingPoolMember{}).TableName()).Where("id = ?", legacyMemberID).
		Update("channel_generation", channelGeneration).Error)
	require.NoError(t, prepareRoutingTopologyGenerationSchema(db))
	require.NoError(t, db.AutoMigrate(
		&Channel{}, &RoutingSchemaVersion{}, &RoutingChannelConfiguration{},
		&RoutingChannelConfigurationOutbox{}, &RoutingTopologyMetadata{}, &RoutingPool{},
		&RoutingPoolMember{}, &RoutingCredentialRef{},
	))
	require.NoError(t, MigrateRoutingTopologyGenerationSchema(db))

	var channel Channel
	require.NoError(t, db.First(&channel, "id = ?", channelID).Error)
	assert.True(t, validRoutingIdentity(channel.RoutingIdentity))
	assert.Equal(t, channelGeneration, channel.RoutingGeneration)
	var configuration RoutingChannelConfiguration
	require.NoError(t, db.First(&configuration, "channel_id = ?", channelID).Error)
	assert.Equal(t, channel.RoutingIdentity, configuration.RoutingIdentity)
	assert.Equal(t, channelGeneration, configuration.RoutingGeneration)
	assert.True(t, validRoutingIdentity(configuration.RoutingIdentity))
	assert.True(t, validRoutingIdentity(configuration.RoutingGeneration))

	var outbox RoutingChannelConfigurationOutbox
	require.NoError(t, db.First(&outbox).Error)
	assert.Equal(t, legacyEventID, outbox.EventID)
	assert.True(t, validRoutingIdentity(outbox.AggregateID))
	assert.True(t, validRoutingIdentity(outbox.RoutingIdentity))
	assert.True(t, validRoutingIdentity(outbox.RoutingGeneration))
	assert.Equal(t, outbox.RoutingGeneration, outbox.AggregateID)
	assert.Equal(t, outbox.Revision, outbox.AggregateRevision)
	decoded, err := outbox.DecodePayload()
	require.NoError(t, err)
	assert.Equal(t, legacyEventID, decoded.EventID)
	assert.True(t, routingChannelConfigurationEventHasLegacyIdentity(decoded))

	var metadata RoutingTopologyMetadata
	require.NoError(t, db.First(&metadata, "id = ?", routingTopologyMetadataID).Error)
	assert.Zero(t, metadata.TopologyEpoch)
	assert.Equal(t, routingTopologyInitialHash(), metadata.TopologyHash)
	var pool RoutingPool
	require.NoError(t, db.First(&pool, "id = ?", 1).Error)
	assert.True(t, pool.DefaultEnabled)
	assert.Zero(t, pool.DefaultPriority)
	assert.Equal(t, int64(100), pool.DefaultWeight)

	var retiredMember RoutingPoolMember
	require.NoError(t, db.First(&retiredMember, "id = ?", legacyMemberID).Error)
	assert.False(t, retiredMember.Active)
	assert.True(t, validRoutingIdentity(retiredMember.ChannelGeneration))
	assert.NotEqual(t, channelGeneration, retiredMember.ChannelGeneration)
	var retiredCredential RoutingCredentialRef
	require.NoError(t, db.First(&retiredCredential, "id = ?", legacyCredentialID).Error)
	assert.False(t, retiredCredential.Active)
	assert.Zero(t, retiredCredential.CurrentOccurrences)
	assert.True(t, validRoutingIdentity(retiredCredential.ChannelGeneration))
	assert.NotEqual(t, channelGeneration, retiredCredential.ChannelGeneration)

	summary, err := ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, summary.CreatedMembers)
	assert.Equal(t, 1, summary.CreatedCredentials)
	var currentMember RoutingPoolMember
	require.NoError(t, db.Where(
		"pool_id = ? AND channel_generation = ? AND active = ?", 1, channelGeneration, true,
	).First(&currentMember).Error)
	assert.NotEqual(t, legacyMemberID, currentMember.ID)
	var currentCredential RoutingCredentialRef
	require.NoError(t, db.Where(
		"channel_generation = ? AND active = ?", channelGeneration, true,
	).First(&currentCredential).Error)
	assert.NotEqual(t, legacyCredentialID, currentCredential.ID)

	ready, err := routingControlPlaneV2PreparedDataReady(db)
	require.NoError(t, err)
	assert.True(t, ready)
	assertRoutingControlPlaneV2ColumnsNotNullable(t, db)

	// Every phase is safe to replay after a MySQL partial DDL migration or a
	// normal service restart. The legacy event identity and topology history
	// remain stable while current member IDs stay generation-scoped.
	require.NoError(t, prepareRoutingControlPlaneV2Schema(db))
	require.NoError(t, prepareRoutingTopologyGenerationSchema(db))
	require.NoError(t, db.AutoMigrate(
		&Channel{}, &RoutingChannelConfiguration{}, &RoutingChannelConfigurationOutbox{},
		&RoutingTopologyMetadata{}, &RoutingPool{}, &RoutingPoolMember{}, &RoutingCredentialRef{},
	))
	require.NoError(t, MigrateRoutingTopologyGenerationSchema(db))
	var replayedOutbox RoutingChannelConfigurationOutbox
	require.NoError(t, db.First(&replayedOutbox, outbox.ID).Error)
	assert.Equal(t, outbox.EventID, replayedOutbox.EventID)
	assert.Equal(t, outbox.AggregateID, replayedOutbox.AggregateID)
	assert.Equal(t, outbox.RoutingIdentity, replayedOutbox.RoutingIdentity)
}

func assertRoutingControlPlaneV2ColumnsNotNullable(t *testing.T, db *gorm.DB) {
	t.Helper()
	checks := []struct {
		model   any
		columns []string
	}{
		{model: &RoutingChannelConfiguration{}, columns: []string{"routing_identity", "routing_generation"}},
		{model: &RoutingChannelConfigurationOutbox{}, columns: []string{"aggregate_id", "aggregate_revision", "routing_identity", "routing_generation"}},
		{model: &RoutingTopologyMetadata{}, columns: []string{"topology_epoch", "topology_hash"}},
		{model: &RoutingPool{}, columns: []string{"default_enabled", "default_priority", "default_weight"}},
	}
	for _, check := range checks {
		columnTypes, err := db.Migrator().ColumnTypes(check.model)
		require.NoError(t, err)
		byName := make(map[string]gorm.ColumnType, len(columnTypes))
		for _, columnType := range columnTypes {
			byName[strings.ToLower(columnType.Name())] = columnType
		}
		for _, column := range check.columns {
			columnType, exists := byName[column]
			require.True(t, exists, "%T.%s", check.model, column)
			nullable, known := columnType.Nullable()
			require.True(t, known, "%T.%s nullable metadata", check.model, column)
			assert.False(t, nullable, "%T.%s must be NOT NULL after backfill", check.model, column)
		}
	}
}
