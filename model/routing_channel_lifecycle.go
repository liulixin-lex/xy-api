package model

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingChannelLifecycleStatusActive  = "active"
	RoutingChannelLifecycleStatusRetired = "retired"

	RoutingChannelLifecycleReasonCreated         = "created"
	RoutingChannelLifecycleReasonMigrated        = "migrated"
	RoutingChannelLifecycleReasonUpstreamChanged = "upstream_changed"
	RoutingChannelLifecycleReasonDeleted         = "deleted"

	routingChannelLifecycleMigrationBatch = 200
)

var (
	ErrRoutingChannelIdentityInvalid     = errors.New("invalid routing channel identity")
	ErrRoutingChannelLifecycleImmutable  = errors.New("routing channel lifecycle history is immutable")
	ErrRoutingChannelLifecycleConflicted = errors.New("routing channel lifecycle conflicts with current channel")
)

// RoutingChannelLifecycle is the durable identity record for one concrete
// upstream lifecycle. It deliberately stores only non-secret, point-in-time
// channel metadata. Serving credentials and management credentials never
// belong in this table.
type RoutingChannelLifecycle struct {
	ID                int64  `json:"id" gorm:"primaryKey"`
	ChannelID         int    `json:"channel_id" gorm:"index;not null"`
	RoutingIdentity   string `json:"routing_identity" gorm:"type:varchar(32);index;not null"`
	RoutingGeneration string `json:"routing_generation" gorm:"type:varchar(32);uniqueIndex;not null"`
	Status            string `json:"status" gorm:"type:varchar(16);index;not null"`
	CreatedReason     string `json:"created_reason" gorm:"type:varchar(32);not null"`
	RetiredReason     string `json:"retired_reason,omitempty" gorm:"type:varchar(32);not null"`
	NameSnapshot      string `json:"name_snapshot" gorm:"type:varchar(128);not null"`
	GroupSnapshot     string `json:"group_snapshot" gorm:"type:varchar(64);not null"`
	ChannelType       int    `json:"channel_type" gorm:"not null"`
	EndpointSnapshot  string `json:"endpoint_snapshot" gorm:"type:varchar(512);not null"`
	CreatedTime       int64  `json:"created_time" gorm:"bigint;index;not null"`
	RetiredTime       int64  `json:"retired_time" gorm:"bigint;index;not null"`
	UpdatedTime       int64  `json:"updated_time" gorm:"bigint;index;not null"`
}

func (RoutingChannelLifecycle) TableName() string {
	return "routing_channel_lifecycles"
}

func (*RoutingChannelLifecycle) BeforeDelete(*gorm.DB) error {
	return ErrRoutingChannelLifecycleImmutable
}

func EnsureChannelRoutingIdentitiesAndLifecycles(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || !db.Migrator().HasTable(&Channel{}) ||
		!db.Migrator().HasTable(&RoutingChannelLifecycle{}) {
		return ErrRoutingChannelIdentityInvalid
	}
	lastChannelID := 0
	for {
		var channels []Channel
		query := db.Select(
			"id", "routing_identity", "routing_generation", "name", "group", "type", "base_url", "created_time",
		).Order("id asc").Limit(routingChannelLifecycleMigrationBatch)
		if lastChannelID > 0 {
			query = query.Where("id > ?", lastChannelID)
		}
		if err := query.Find(&channels).Error; err != nil {
			return err
		}
		if len(channels) == 0 {
			return nil
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			for index := range channels {
				channel := channels[index]
				updates := map[string]any{}
				if !validRoutingIdentity(channel.RoutingIdentity) {
					channel.RoutingIdentity = common.GetUUID()
					updates["routing_identity"] = channel.RoutingIdentity
				}
				if !validRoutingIdentity(channel.RoutingGeneration) {
					channel.RoutingGeneration = common.GetUUID()
					updates["routing_generation"] = channel.RoutingGeneration
				}
				if len(updates) > 0 {
					result := tx.Model(&Channel{}).Where("id = ?", channel.Id).Updates(updates)
					if result.Error != nil {
						return result.Error
					}
					if result.RowsAffected != 1 {
						return ErrRoutingChannelLifecycleConflicted
					}
				}
				if err := ensureRoutingChannelLifecycleTx(
					tx, channel, RoutingChannelLifecycleReasonMigrated, common.GetTimestamp(),
				); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		lastChannelID = channels[len(channels)-1].Id
		if len(channels) < routingChannelLifecycleMigrationBatch {
			return nil
		}
	}
}

func ensureRoutingChannelLifecycleTx(
	tx *gorm.DB,
	channel Channel,
	reason string,
	now int64,
) error {
	if tx == nil || !validRoutingChannel(channel) || !validRoutingChannelLifecycleReason(reason) || now <= 0 {
		return ErrRoutingChannelIdentityInvalid
	}
	var current RoutingChannelLifecycle
	err := tx.Where("routing_generation = ?", channel.RoutingGeneration).First(&current).Error
	if err == nil {
		if current.ChannelID != channel.Id || current.RoutingIdentity != channel.RoutingIdentity ||
			current.Status != RoutingChannelLifecycleStatusActive || current.RetiredTime != 0 {
			return ErrRoutingChannelLifecycleConflicted
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	retired := tx.Model(&RoutingChannelLifecycle{}).
		Where("routing_identity = ? AND status = ? AND routing_generation <> ?",
			channel.RoutingIdentity, RoutingChannelLifecycleStatusActive, channel.RoutingGeneration,
		).
		Updates(map[string]any{
			"status": RoutingChannelLifecycleStatusRetired, "retired_reason": reason,
			"retired_time": now, "updated_time": now,
		})
	if retired.Error != nil {
		return retired.Error
	}
	candidate := RoutingChannelLifecycle{
		ChannelID: channel.Id, RoutingIdentity: channel.RoutingIdentity,
		RoutingGeneration: channel.RoutingGeneration, Status: RoutingChannelLifecycleStatusActive,
		CreatedReason: reason, NameSnapshot: strings.TrimSpace(channel.Name),
		GroupSnapshot: strings.TrimSpace(channel.Group), ChannelType: channel.Type,
		EndpointSnapshot: routingLifecycleEndpointSnapshot(channel.BaseURL),
		CreatedTime:      now, UpdatedTime: now,
	}
	created := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "routing_generation"}},
		DoNothing: true,
	}).Create(&candidate)
	if created.Error != nil {
		return created.Error
	}
	var stored RoutingChannelLifecycle
	if err := tx.Where("routing_generation = ?", channel.RoutingGeneration).First(&stored).Error; err != nil {
		return err
	}
	if stored.ChannelID != channel.Id || stored.RoutingIdentity != channel.RoutingIdentity ||
		stored.Status != RoutingChannelLifecycleStatusActive || stored.RetiredTime != 0 {
		return ErrRoutingChannelLifecycleConflicted
	}
	if created.RowsAffected == 1 {
		action := RoutingControlActionCreate
		if reason == RoutingChannelLifecycleReasonUpstreamChanged {
			action = RoutingControlActionRotate
		}
		if err := insertRoutingChannelLifecycleAuditTx(tx, nil, stored, action, reason); err != nil {
			return err
		}
	}
	return nil
}

func rotateRoutingChannelLifecycleTx(
	tx *gorm.DB,
	previous Channel,
	current Channel,
	reason string,
	now int64,
) error {
	if tx == nil || previous.RoutingIdentity != current.RoutingIdentity ||
		previous.RoutingGeneration == current.RoutingGeneration ||
		!validRoutingChannel(previous) || !validRoutingChannel(current) ||
		!validRoutingChannelLifecycleReason(reason) || now <= 0 {
		return ErrRoutingChannelIdentityInvalid
	}
	var previousLifecycle RoutingChannelLifecycle
	if err := tx.Where("routing_generation = ? AND routing_identity = ? AND status = ?",
		previous.RoutingGeneration, previous.RoutingIdentity, RoutingChannelLifecycleStatusActive,
	).First(&previousLifecycle).Error; err != nil {
		return err
	}
	result := tx.Model(&RoutingChannelLifecycle{}).
		Where("routing_generation = ? AND routing_identity = ? AND status = ?",
			previous.RoutingGeneration, previous.RoutingIdentity, RoutingChannelLifecycleStatusActive,
		).
		Updates(map[string]any{
			"status": RoutingChannelLifecycleStatusRetired, "retired_reason": reason,
			"retired_time": now, "updated_time": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRoutingChannelLifecycleConflicted
	}
	retiredLifecycle := previousLifecycle
	retiredLifecycle.Status = RoutingChannelLifecycleStatusRetired
	retiredLifecycle.RetiredReason = reason
	retiredLifecycle.RetiredTime = now
	retiredLifecycle.UpdatedTime = now
	if err := insertRoutingChannelLifecycleAuditTx(
		tx, &previousLifecycle, retiredLifecycle, RoutingControlActionRetire, reason,
	); err != nil {
		return err
	}
	return ensureRoutingChannelLifecycleTx(tx, current, reason, now)
}

func retireRoutingChannelLifecycleTx(
	tx *gorm.DB,
	channel Channel,
	reason string,
	now int64,
) error {
	if tx == nil || !validRoutingChannel(channel) || !validRoutingChannelLifecycleReason(reason) || now <= 0 {
		return ErrRoutingChannelIdentityInvalid
	}
	var currentLifecycle RoutingChannelLifecycle
	if err := tx.Where("routing_generation = ? AND routing_identity = ? AND status = ?",
		channel.RoutingGeneration, channel.RoutingIdentity, RoutingChannelLifecycleStatusActive,
	).First(&currentLifecycle).Error; err != nil {
		return err
	}
	result := tx.Model(&RoutingChannelLifecycle{}).
		Where("routing_generation = ? AND routing_identity = ? AND status = ?",
			channel.RoutingGeneration, channel.RoutingIdentity, RoutingChannelLifecycleStatusActive,
		).
		Updates(map[string]any{
			"status": RoutingChannelLifecycleStatusRetired, "retired_reason": reason,
			"retired_time": now, "updated_time": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRoutingChannelLifecycleConflicted
	}
	retiredLifecycle := currentLifecycle
	retiredLifecycle.Status = RoutingChannelLifecycleStatusRetired
	retiredLifecycle.RetiredReason = reason
	retiredLifecycle.RetiredTime = now
	retiredLifecycle.UpdatedTime = now
	return insertRoutingChannelLifecycleAuditTx(
		tx, &currentLifecycle, retiredLifecycle, RoutingControlActionRetire, reason,
	)
}

func retireRoutingChannelGenerationStateTx(tx *gorm.DB, channel Channel, now int64) error {
	if tx == nil || !validRoutingChannel(channel) || now <= 0 {
		return ErrRoutingChannelIdentityInvalid
	}
	if tx.Migrator().HasTable(&RoutingPoolMember{}) &&
		tx.Migrator().HasColumn(&RoutingPoolMember{}, "channel_generation") {
		if err := tx.Model(&RoutingPoolMember{}).
			Where("channel_id = ? AND channel_generation = ? AND active = ?",
				channel.Id, channel.RoutingGeneration, true,
			).
			Updates(map[string]any{"active": false, "updated_time": now}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&RoutingCredentialRef{}) &&
		tx.Migrator().HasColumn(&RoutingCredentialRef{}, "channel_generation") {
		if err := tx.Model(&RoutingCredentialRef{}).
			Where("channel_id = ? AND channel_generation = ? AND active = ?",
				channel.Id, channel.RoutingGeneration, true,
			).
			Updates(map[string]any{
				"active": false, "current_occurrences": 0,
				"retired_time": now, "updated_time": now,
			}).Error; err != nil {
			return err
		}
	}
	// Legacy channel-scoped runtime rows cannot prove lifecycle ownership. They
	// are intentionally cold-started whenever a lifecycle retires.
	for _, state := range []any{
		&RoutingChannelHealthState{}, &RoutingChannelMetric{}, &RoutingBreakerState{},
	} {
		if tx.Migrator().HasTable(state) {
			query := tx.Where("channel_id = ?", channel.Id)
			if tx.Migrator().HasColumn(state, "channel_generation") {
				query = query.Where("channel_generation = ?", channel.RoutingGeneration)
			}
			if err := query.Delete(state).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func GetRoutingChannelLifecycleByGenerationContext(
	ctx context.Context,
	generation string,
) (RoutingChannelLifecycle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingIdentity(generation) {
		return RoutingChannelLifecycle{}, ErrRoutingChannelIdentityInvalid
	}
	var lifecycle RoutingChannelLifecycle
	err := DB.WithContext(ctx).Where("routing_generation = ?", generation).First(&lifecycle).Error
	return lifecycle, err
}

func routingChannelGenerationBindingActiveTx(
	ctx context.Context,
	tx *gorm.DB,
	poolID int,
	memberID int,
	channelID int,
	channelGeneration string,
	credentialID int,
) (bool, error) {
	if tx == nil || poolID <= 0 || memberID <= 0 || channelID <= 0 ||
		!validRoutingIdentity(channelGeneration) || credentialID < 0 {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var channel Channel
	query := tx.WithContext(ctx).Select("id", "routing_generation").Where("id = ?", channelID)
	if tx.Dialector.Name() != string(common.DatabaseTypeSQLite) {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&channel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if channel.RoutingGeneration != channelGeneration {
		return false, nil
	}

	var member RoutingPoolMember
	if err := tx.WithContext(ctx).
		Where("id = ? AND pool_id = ? AND channel_id = ? AND channel_generation = ? AND active = ?",
			memberID, poolID, channelID, channelGeneration, true,
		).First(&member).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if credentialID == 0 {
		return true, nil
	}

	var credential RoutingCredentialRef
	if err := tx.WithContext(ctx).
		Where("id = ? AND channel_id = ? AND channel_generation = ? AND active = ?",
			credentialID, channelID, channelGeneration, true,
		).First(&credential).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func routingGenerationFencingAvailable(db *gorm.DB) bool {
	return db != nil && db.Migrator().HasTable(&Channel{}) &&
		db.Migrator().HasTable(&RoutingPoolMember{}) && db.Migrator().HasTable(&RoutingCredentialRef{})
}

func validRoutingChannel(channel Channel) bool {
	return channel.Id > 0 && validRoutingIdentity(channel.RoutingIdentity) &&
		validRoutingIdentity(channel.RoutingGeneration)
}

func validRoutingIdentity(value string) bool {
	if len(value) != 32 {
		return false
	}
	for index := range value {
		char := value[index]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validRoutingChannelLifecycleReason(reason string) bool {
	switch reason {
	case RoutingChannelLifecycleReasonCreated,
		RoutingChannelLifecycleReasonMigrated,
		RoutingChannelLifecycleReasonUpstreamChanged,
		RoutingChannelLifecycleReasonDeleted:
		return true
	default:
		return false
	}
}

func routingLifecycleEndpointSnapshot(raw *string) string {
	if raw == nil {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(*raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}
