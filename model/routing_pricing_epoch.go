package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const routingPricingVersionID = 1

type RoutingPricingVersion struct {
	ID          int    `json:"-" gorm:"primaryKey;autoIncrement:false"`
	Epoch       int64  `json:"epoch" gorm:"bigint;not null"`
	StateHash   string `json:"state_hash" gorm:"type:char(64);index;not null"`
	UpdatedTime int64  `json:"updated_time" gorm:"bigint;index;not null"`
}

func (RoutingPricingVersion) TableName() string {
	return "routing_pricing_versions"
}

func EnsureRoutingPricingVersion(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || !db.Migrator().HasTable(&RoutingPricingVersion{}) ||
		!db.Migrator().HasTable(&Option{}) {
		return ErrRoutingSchemaNotReady
	}
	return db.Transaction(func(tx *gorm.DB) error {
		_, err := reconcileRoutingPricingVersionTx(tx, common.GetTimestamp())
		return err
	})
}

func GetRoutingPricingVersionDBContext(
	ctx context.Context,
	db *gorm.DB,
) (RoutingPricingVersion, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil || db.Dialector == nil {
		return RoutingPricingVersion{}, ErrRoutingSchemaNotReady
	}
	var version RoutingPricingVersion
	if err := db.WithContext(ctx).Where("id = ?", routingPricingVersionID).First(&version).Error; err != nil {
		return RoutingPricingVersion{}, err
	}
	if version.ID != routingPricingVersionID || version.Epoch < 0 ||
		!validRoutingChannelHash(version.StateHash) || version.UpdatedTime <= 0 {
		return RoutingPricingVersion{}, ErrRoutingSchemaNotReady
	}
	return version, nil
}

func advanceRoutingPricingVersionTx(tx *gorm.DB, now int64) (RoutingPricingVersion, error) {
	return reconcileRoutingPricingVersionTx(tx, now)
}

func reconcileRoutingPricingVersionTx(tx *gorm.DB, now int64) (RoutingPricingVersion, error) {
	if tx == nil || tx.Dialector == nil || now <= 0 {
		return RoutingPricingVersion{}, ErrRoutingSchemaNotReady
	}
	stateHash, err := routingPricingStateHashTx(tx)
	if err != nil {
		return RoutingPricingVersion{}, err
	}
	candidate := RoutingPricingVersion{
		ID: routingPricingVersionID, Epoch: 0, StateHash: stateHash, UpdatedTime: now,
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(&candidate).Error; err != nil {
		return RoutingPricingVersion{}, err
	}
	var current RoutingPricingVersion
	if err := lockForUpdate(tx).Where("id = ?", routingPricingVersionID).First(&current).Error; err != nil {
		return RoutingPricingVersion{}, err
	}
	if current.Epoch < 0 || current.Epoch == math.MaxInt64 {
		return RoutingPricingVersion{}, ErrRoutingSchemaNotReady
	}
	if current.StateHash == stateHash {
		return current, nil
	}
	if !validRoutingChannelHash(current.StateHash) {
		current.StateHash = routingPricingInitialHash()
	}
	if now <= current.UpdatedTime {
		now = current.UpdatedTime + 1
	}
	nextEpoch := current.Epoch + 1
	result := tx.Model(&RoutingPricingVersion{}).
		Where("id = ? AND epoch = ?", current.ID, current.Epoch).
		Updates(map[string]any{"epoch": nextEpoch, "state_hash": stateHash, "updated_time": now})
	if result.Error != nil {
		return RoutingPricingVersion{}, result.Error
	}
	if result.RowsAffected != 1 {
		return RoutingPricingVersion{}, ErrRoutingChannelConfigurationChanged
	}
	return RoutingPricingVersion{ID: current.ID, Epoch: nextEpoch, StateHash: stateHash, UpdatedTime: now}, nil
}

func routingPricingStateHashTx(tx *gorm.DB) (string, error) {
	if tx == nil || tx.Dialector == nil || !tx.Migrator().HasTable(&Option{}) {
		return "", ErrRoutingSchemaNotReady
	}
	var options []Option
	if err := tx.Order("key asc").Find(&options).Error; err != nil {
		return "", err
	}
	type pricingOption struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	state := make([]pricingOption, 0, len(options))
	for _, option := range options {
		if routingPricingOptionKey(option.Key) {
			state = append(state, pricingOption{Key: option.Key, Value: option.Value})
		}
	}
	sort.Slice(state, func(left, right int) bool { return state[left].Key < state[right].Key })
	encoded, err := common.Marshal(struct {
		SchemaVersion int             `json:"schema_version"`
		Options       []pricingOption `json:"options"`
	}{SchemaVersion: 2, Options: state})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func routingPricingOptionKey(key string) bool {
	key = strings.TrimSpace(key)
	if strings.HasPrefix(key, "billing_setting.") || strings.HasPrefix(key, "tool_price_setting.") {
		return true
	}
	switch key {
	case "ModelRatio", "CompletionRatio", "ModelPrice", "CacheRatio", "CreateCacheRatio",
		"ImageRatio", "AudioRatio", "AudioCompletionRatio":
		return true
	default:
		return false
	}
}

func routingPricingInitialHash() string {
	digest := sha256.Sum256([]byte("routing-pricing:empty:v2"))
	return hex.EncodeToString(digest[:])
}

func routingPricingOptionsChanged(values map[string]string) bool {
	for key := range values {
		if routingPricingOptionKey(key) {
			return true
		}
	}
	return false
}

func reconcileRoutingPricingVersionIfPresentTx(tx *gorm.DB, now int64) error {
	if tx == nil || !tx.Migrator().HasTable(&RoutingPricingVersion{}) {
		return nil
	}
	_, err := advanceRoutingPricingVersionTx(tx, now)
	return err
}
