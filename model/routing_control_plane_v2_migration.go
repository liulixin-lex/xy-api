package model

import (
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

const routingControlPlaneV2MigrationBatch = 500

// These migration-only models deliberately keep new columns nullable. They
// are used before AutoMigrate so databases with historical rows never execute
// ADD ... NOT NULL before the values have been fenced and backfilled. The
// canonical models remain non-null and let AutoMigrate tighten the columns
// only after this preparation succeeds.
type routingChannelConfigurationIdentityColumns struct {
	RoutingIdentity   *string `gorm:"column:routing_identity;type:varchar(32)"`
	RoutingGeneration *string `gorm:"column:routing_generation;type:varchar(32)"`
}

func (routingChannelConfigurationIdentityColumns) TableName() string {
	return (RoutingChannelConfiguration{}).TableName()
}

type routingChannelConfigurationOutboxIdentityColumns struct {
	AggregateID       *string `gorm:"column:aggregate_id;type:varchar(32)"`
	AggregateRevision *int64  `gorm:"column:aggregate_revision;type:bigint"`
	RoutingIdentity   *string `gorm:"column:routing_identity;type:varchar(32)"`
	RoutingGeneration *string `gorm:"column:routing_generation;type:varchar(32)"`
}

func (routingChannelConfigurationOutboxIdentityColumns) TableName() string {
	return (RoutingChannelConfigurationOutbox{}).TableName()
}

type routingTopologyMetadataVersionColumns struct {
	TopologyEpoch *int64  `gorm:"column:topology_epoch;type:bigint"`
	TopologyHash  *string `gorm:"column:topology_hash;type:char(64)"`
}

func (routingTopologyMetadataVersionColumns) TableName() string {
	return (RoutingTopologyMetadata{}).TableName()
}

type routingPoolDefaultColumns struct {
	DefaultEnabled  *bool  `gorm:"column:default_enabled"`
	DefaultPriority *int64 `gorm:"column:default_priority;type:bigint"`
	DefaultWeight   *int64 `gorm:"column:default_weight;type:bigint"`
}

func (routingPoolDefaultColumns) TableName() string {
	return (RoutingPool{}).TableName()
}

type routingChannelConfigurationIdentityRow struct {
	ChannelID         int
	RoutingIdentity   *string
	RoutingGeneration *string
}

type routingChannelConfigurationOutboxIdentityRow struct {
	ID                int64
	EventID           string
	AggregateID       *string
	AggregateRevision *int64
	ChannelID         int
	RoutingIdentity   *string
	RoutingGeneration *string
	Revision          int64
	ConfigEpoch       int64
	EventType         string
	PayloadJSON       string
	PayloadHash       string
}

type routingTopologyMetadataVersionRow struct {
	ID            int
	TopologyEpoch *int64
	TopologyHash  *string
}

// prepareRoutingControlPlaneV2Schema is the re-entrant pre-AutoMigrate phase
// for columns added to tables that already existed in v0.1.13. It must stay
// free of lifecycle history, policy publication, and connector retirement;
// those later migration phases only run after the physical columns are safe.
func prepareRoutingControlPlaneV2Schema(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingSchemaNotReady
	}
	if err := prepareRoutingChannelIdentityColumns(db); err != nil {
		return err
	}
	if err := prepareRoutingChannelConfigurationIdentityColumns(db); err != nil {
		return err
	}
	if err := prepareRoutingChannelConfigurationOutboxIdentityColumns(db); err != nil {
		return err
	}
	if err := prepareRoutingTopologyMetadataVersionColumns(db); err != nil {
		return err
	}
	return prepareRoutingPoolDefaultColumns(db)
}

func prepareRoutingChannelIdentityColumns(db *gorm.DB) error {
	if !db.Migrator().HasTable(&Channel{}) {
		return nil
	}
	for _, field := range []string{"RoutingIdentity", "RoutingGeneration"} {
		if db.Migrator().HasColumn(&Channel{}, field) {
			continue
		}
		if err := db.Migrator().AddColumn(&Channel{}, field); err != nil {
			return err
		}
	}
	return EnsureChannelRoutingGenerations(db)
}

func prepareRoutingChannelConfigurationIdentityColumns(db *gorm.DB) error {
	if !db.Migrator().HasTable(&RoutingChannelConfiguration{}) {
		return nil
	}
	if !db.Migrator().HasTable(&Channel{}) {
		return ErrRoutingChannelConfigurationInvalid
	}
	columns := &routingChannelConfigurationIdentityColumns{}
	for _, field := range []string{"RoutingIdentity", "RoutingGeneration"} {
		if db.Migrator().HasColumn(columns, field) {
			continue
		}
		if err := db.Migrator().AddColumn(columns, field); err != nil {
			return err
		}
	}

	lastChannelID := 0
	for {
		var rows []routingChannelConfigurationIdentityRow
		if err := db.Table((RoutingChannelConfiguration{}).TableName()).
			Select("channel_id", "routing_identity", "routing_generation").
			Where("channel_id > ?", lastChannelID).
			Where("routing_identity IS NULL OR routing_identity = ? OR routing_generation IS NULL OR routing_generation = ?", "", "").
			Order("channel_id ASC").Limit(routingControlPlaneV2MigrationBatch).
			Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			channelIDs := make([]int, 0, len(rows))
			for index := range rows {
				channelIDs = append(channelIDs, rows[index].ChannelID)
			}
			var channels []Channel
			query := tx.Select("id", "routing_identity", "routing_generation").
				Where("id IN ?", channelIDs).Order("id ASC")
			if tx.Dialector.Name() != string(common.DatabaseTypeSQLite) {
				query = lockForUpdate(query)
			}
			if err := query.Find(&channels).Error; err != nil {
				return err
			}
			channelByID := make(map[int]Channel, len(channels))
			for index := range channels {
				channelByID[channels[index].Id] = channels[index]
			}
			for index := range rows {
				row := rows[index]
				channel, exists := channelByID[row.ChannelID]
				if !exists || !validRoutingChannel(channel) ||
					!routingOptionalIdentityMatches(row.RoutingIdentity, channel.RoutingIdentity) ||
					!routingOptionalIdentityMatches(row.RoutingGeneration, channel.RoutingGeneration) {
					return ErrRoutingChannelConfigurationChanged
				}
				if routingIdentityPointerValue(row.RoutingIdentity) == channel.RoutingIdentity &&
					routingIdentityPointerValue(row.RoutingGeneration) == channel.RoutingGeneration {
					continue
				}
				result := tx.Table((RoutingChannelConfiguration{}).TableName()).
					Where("channel_id = ?", row.ChannelID).
					Updates(map[string]any{
						"routing_identity": channel.RoutingIdentity, "routing_generation": channel.RoutingGeneration,
					})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrRoutingChannelConfigurationChanged
				}
			}
			return nil
		}); err != nil {
			return err
		}
		lastChannelID = rows[len(rows)-1].ChannelID
		if len(rows) < routingControlPlaneV2MigrationBatch {
			return nil
		}
	}
}

func prepareRoutingChannelConfigurationOutboxIdentityColumns(db *gorm.DB) error {
	if !db.Migrator().HasTable(&RoutingChannelConfigurationOutbox{}) {
		return nil
	}
	columns := &routingChannelConfigurationOutboxIdentityColumns{}
	for _, field := range []string{"AggregateID", "AggregateRevision", "RoutingIdentity", "RoutingGeneration"} {
		if db.Migrator().HasColumn(columns, field) {
			continue
		}
		if err := db.Migrator().AddColumn(columns, field); err != nil {
			return err
		}
	}

	lastID := int64(0)
	for {
		var rows []routingChannelConfigurationOutboxIdentityRow
		if err := db.Table((RoutingChannelConfigurationOutbox{}).TableName()).
			Select(
				"id", "event_id", "aggregate_id", "aggregate_revision", "channel_id", "routing_identity",
				"routing_generation", "revision", "config_epoch", "event_type", "payload_json", "payload_hash",
			).
			Where("id > ?", lastID).
			Where("aggregate_id IS NULL OR aggregate_id = ? OR aggregate_revision IS NULL OR aggregate_revision <= ? OR routing_identity IS NULL OR routing_identity = ? OR routing_generation IS NULL OR routing_generation = ?", "", 0, "", "").
			Order("id ASC").Limit(routingControlPlaneV2MigrationBatch).
			Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			for index := range rows {
				row := rows[index]
				event, err := decodeRoutingChannelConfigurationOutboxMigrationEvent(row)
				if err != nil {
					return err
				}
				expectedAggregateID := event.AggregateID
				expectedAggregateRevision := event.AggregateRevision
				expectedIdentity := event.RoutingIdentity
				expectedGeneration := event.RoutingGeneration
				if routingChannelConfigurationEventHasLegacyIdentity(event) {
					expectedIdentity, expectedGeneration = legacyRoutingChannelConfigurationOutboxIdentities(
						row.ID, row.EventID,
					)
					expectedAggregateID = expectedGeneration
					expectedAggregateRevision = row.Revision
				}
				if !validRoutingIdentity(expectedAggregateID) || !validRoutingIdentity(expectedIdentity) ||
					!validRoutingIdentity(expectedGeneration) || expectedAggregateID != expectedGeneration ||
					expectedAggregateRevision != row.Revision ||
					!routingOptionalIdentityMatches(row.AggregateID, expectedAggregateID) ||
					!routingOptionalRevisionMatches(row.AggregateRevision, expectedAggregateRevision) ||
					!routingOptionalIdentityMatches(row.RoutingIdentity, expectedIdentity) ||
					!routingOptionalIdentityMatches(row.RoutingGeneration, expectedGeneration) {
					return ErrRoutingChannelConfigurationInvalid
				}
				if routingIdentityPointerValue(row.AggregateID) == expectedAggregateID &&
					routingRevisionPointerValue(row.AggregateRevision) == expectedAggregateRevision &&
					routingIdentityPointerValue(row.RoutingIdentity) == expectedIdentity &&
					routingIdentityPointerValue(row.RoutingGeneration) == expectedGeneration {
					continue
				}
				result := tx.Table((RoutingChannelConfigurationOutbox{}).TableName()).
					Where("id = ? AND event_id = ?", row.ID, row.EventID).
					Updates(map[string]any{
						"aggregate_id": expectedAggregateID, "aggregate_revision": expectedAggregateRevision,
						"routing_identity": expectedIdentity, "routing_generation": expectedGeneration,
					})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrRoutingChannelConfigurationChanged
				}
			}
			return nil
		}); err != nil {
			return err
		}
		lastID = rows[len(rows)-1].ID
		if len(rows) < routingControlPlaneV2MigrationBatch {
			return nil
		}
	}
}

func decodeRoutingChannelConfigurationOutboxMigrationEvent(
	row routingChannelConfigurationOutboxIdentityRow,
) (RoutingChannelConfigurationEvent, error) {
	if row.ID <= 0 || row.EventID == "" || row.ChannelID <= 0 || row.Revision <= 0 || row.ConfigEpoch <= 0 ||
		row.EventType != RoutingChannelConfigurationEventType || !validRoutingChannelHash(row.PayloadHash) ||
		routingChannelConfigurationHash([]byte(row.PayloadJSON)) != row.PayloadHash {
		return RoutingChannelConfigurationEvent{}, ErrRoutingChannelConfigurationInvalid
	}
	var event RoutingChannelConfigurationEvent
	if err := common.UnmarshalJsonStr(row.PayloadJSON, &event); err != nil || !validRoutingChannelConfigurationEvent(event) ||
		event.EventID != row.EventID || event.EventType != row.EventType || event.ChannelID != row.ChannelID ||
		event.Revision != row.Revision || event.ConfigEpoch != row.ConfigEpoch {
		return RoutingChannelConfigurationEvent{}, ErrRoutingChannelConfigurationInvalid
	}
	return event, nil
}

func prepareRoutingTopologyMetadataVersionColumns(db *gorm.DB) error {
	if !db.Migrator().HasTable(&RoutingTopologyMetadata{}) {
		return nil
	}
	columns := &routingTopologyMetadataVersionColumns{}
	for _, field := range []string{"TopologyEpoch", "TopologyHash"} {
		if db.Migrator().HasColumn(columns, field) {
			continue
		}
		if err := db.Migrator().AddColumn(columns, field); err != nil {
			return err
		}
	}
	var rows []routingTopologyMetadataVersionRow
	if err := db.Table((RoutingTopologyMetadata{}).TableName()).
		Select("id", "topology_epoch", "topology_hash").Order("id ASC").Scan(&rows).Error; err != nil {
		return err
	}
	for index := range rows {
		row := rows[index]
		epoch := int64(0)
		if row.TopologyEpoch != nil {
			epoch = *row.TopologyEpoch
		}
		hash := strings.TrimSpace(routingIdentityPointerValue(row.TopologyHash))
		if epoch < 0 || epoch == math.MaxInt64 || hash != "" && !validRoutingChannelHash(hash) {
			return ErrRoutingSchemaNotReady
		}
		if hash == "" {
			hash = routingTopologyInitialHash()
		}
		if row.TopologyEpoch != nil && routingIdentityPointerValue(row.TopologyHash) == hash {
			continue
		}
		result := db.Table((RoutingTopologyMetadata{}).TableName()).Where("id = ?", row.ID).
			Updates(map[string]any{"topology_epoch": epoch, "topology_hash": hash})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRoutingSchemaNotReady
		}
	}
	return nil
}

func prepareRoutingPoolDefaultColumns(db *gorm.DB) error {
	if !db.Migrator().HasTable(&RoutingPool{}) {
		return nil
	}
	columns := &routingPoolDefaultColumns{}
	for _, field := range []string{"DefaultEnabled", "DefaultPriority", "DefaultWeight"} {
		if db.Migrator().HasColumn(columns, field) {
			continue
		}
		if err := db.Migrator().AddColumn(columns, field); err != nil {
			return err
		}
	}
	return db.Table((RoutingPool{}).TableName()).
		Where("default_enabled IS NULL OR default_priority IS NULL OR default_weight IS NULL").
		Updates(map[string]any{
			"default_enabled": true, "default_priority": int64(0), "default_weight": int64(100),
		}).Error
}

func routingControlPlaneV2PreparedDataReady(db *gorm.DB) (bool, error) {
	if db == nil || db.Dialector == nil {
		return false, ErrRoutingSchemaNotReady
	}
	checks := []struct {
		model     any
		predicate string
		args      []any
	}{
		{model: &Channel{}, predicate: "routing_identity IS NULL OR routing_identity = ? OR routing_generation IS NULL OR routing_generation = ?", args: []any{"", ""}},
		{model: &RoutingChannelConfiguration{}, predicate: "routing_identity IS NULL OR routing_identity = ? OR routing_generation IS NULL OR routing_generation = ?", args: []any{"", ""}},
		{model: &RoutingChannelConfigurationOutbox{}, predicate: "aggregate_id IS NULL OR aggregate_id = ? OR aggregate_revision IS NULL OR aggregate_revision <= ? OR routing_identity IS NULL OR routing_identity = ? OR routing_generation IS NULL OR routing_generation = ?", args: []any{"", 0, "", ""}},
		{model: &RoutingTopologyMetadata{}, predicate: "topology_epoch IS NULL OR topology_epoch < ? OR topology_hash IS NULL OR topology_hash = ?", args: []any{0, ""}},
		{model: &RoutingPool{}, predicate: "default_enabled IS NULL OR default_priority IS NULL OR default_weight IS NULL OR default_weight < ?", args: []any{0}},
	}
	for _, check := range checks {
		if !db.Migrator().HasTable(check.model) {
			return false, nil
		}
		var count int64
		if err := db.Model(check.model).Where(check.predicate, check.args...).Limit(1).Count(&count).Error; err != nil {
			return false, err
		}
		if count != 0 {
			return false, nil
		}
	}
	return true, nil
}

func routingChannelConfigurationEventHasLegacyIdentity(event RoutingChannelConfigurationEvent) bool {
	return event.AggregateID == "" && event.AggregateRevision == 0 &&
		event.RoutingIdentity == "" && event.RoutingGeneration == ""
}

func legacyRoutingChannelConfigurationOutboxIdentities(id int64, eventID string) (string, string) {
	identity := routingChannelConfigurationHash([]byte(fmt.Sprintf(
		"routing-channel-configuration-outbox-legacy-identity:v1\x00%d\x00%s", id, eventID,
	)))[:32]
	generation := routingChannelConfigurationHash([]byte(fmt.Sprintf(
		"routing-channel-configuration-outbox-legacy-generation:v1\x00%d\x00%s", id, eventID,
	)))[:32]
	return identity, generation
}

func routingOptionalIdentityMatches(value *string, expected string) bool {
	actual := routingIdentityPointerValue(value)
	return actual == "" || actual == expected
}

func routingOptionalRevisionMatches(value *int64, expected int64) bool {
	return value == nil || *value == 0 || *value == expected
}

func routingIdentityPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func routingRevisionPointerValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func validatePreparedRoutingChannelConfigurationOutbox(
	outbox RoutingChannelConfigurationOutbox,
	event RoutingChannelConfigurationEvent,
) error {
	if routingChannelConfigurationEventHasLegacyIdentity(event) {
		identity, generation := legacyRoutingChannelConfigurationOutboxIdentities(outbox.ID, outbox.EventID)
		if outbox.AggregateID == "" && outbox.AggregateRevision == 0 && outbox.RoutingIdentity == "" &&
			outbox.RoutingGeneration == "" {
			return nil
		}
		if outbox.AggregateID == generation && outbox.AggregateRevision == outbox.Revision &&
			outbox.RoutingIdentity == identity && outbox.RoutingGeneration == generation {
			return nil
		}
		return ErrRoutingChannelConfigurationInvalid
	}
	if event.AggregateID != outbox.AggregateID || event.AggregateRevision != outbox.AggregateRevision ||
		event.RoutingIdentity != outbox.RoutingIdentity || event.RoutingGeneration != outbox.RoutingGeneration {
		return ErrRoutingChannelConfigurationInvalid
	}
	return nil
}
