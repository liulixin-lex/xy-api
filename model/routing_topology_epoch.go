package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

type RoutingTopologyVersion struct {
	Epoch       int64  `json:"epoch"`
	StateHash   string `json:"state_hash"`
	UpdatedTime int64  `json:"updated_time"`
}

func GetRoutingTopologyVersionDBContext(
	ctx context.Context,
	db *gorm.DB,
) (RoutingTopologyVersion, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil || db.Dialector == nil || !db.Migrator().HasTable(&RoutingTopologyMetadata{}) {
		return RoutingTopologyVersion{}, ErrRoutingSchemaNotReady
	}
	var metadata RoutingTopologyMetadata
	if err := db.WithContext(ctx).Where("id = ?", routingTopologyMetadataID).First(&metadata).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RoutingTopologyVersion{Epoch: 0, StateHash: routingTopologyInitialHash()}, nil
		}
		return RoutingTopologyVersion{}, err
	}
	if metadata.TopologyEpoch < 0 || !validRoutingChannelHash(metadata.TopologyHash) || metadata.UpdatedTime <= 0 {
		return RoutingTopologyVersion{}, ErrRoutingSchemaNotReady
	}
	return RoutingTopologyVersion{
		Epoch: metadata.TopologyEpoch, StateHash: metadata.TopologyHash, UpdatedTime: metadata.UpdatedTime,
	}, nil
}

func advanceRoutingTopologyVersionTx(tx *gorm.DB, now int64) (RoutingTopologyVersion, error) {
	if tx == nil || tx.Dialector == nil || now <= 0 {
		return RoutingTopologyVersion{}, ErrRoutingSchemaNotReady
	}
	stateHash, err := routingTopologyStateHashTx(tx)
	if err != nil {
		return RoutingTopologyVersion{}, err
	}
	var metadata RoutingTopologyMetadata
	if err := lockForUpdate(tx).Where("id = ?", routingTopologyMetadataID).First(&metadata).Error; err != nil {
		return RoutingTopologyVersion{}, err
	}
	currentHash := metadata.TopologyHash
	if !validRoutingChannelHash(currentHash) {
		currentHash = routingTopologyInitialHash()
	}
	if metadata.TopologyEpoch < 0 || metadata.TopologyEpoch == math.MaxInt64 {
		return RoutingTopologyVersion{}, ErrRoutingSchemaNotReady
	}
	if currentHash == stateHash {
		if metadata.TopologyHash != currentHash {
			if err := tx.Model(&RoutingTopologyMetadata{}).Where("id = ?", metadata.ID).
				Updates(map[string]any{"topology_hash": currentHash, "updated_time": now}).Error; err != nil {
				return RoutingTopologyVersion{}, err
			}
		}
		return RoutingTopologyVersion{
			Epoch: metadata.TopologyEpoch, StateHash: currentHash, UpdatedTime: max(metadata.UpdatedTime, now),
		}, nil
	}
	if now <= metadata.UpdatedTime {
		now = metadata.UpdatedTime + 1
	}
	nextEpoch := metadata.TopologyEpoch + 1
	result := tx.Model(&RoutingTopologyMetadata{}).
		Where("id = ? AND topology_epoch = ?", metadata.ID, metadata.TopologyEpoch).
		Updates(map[string]any{
			"topology_epoch": nextEpoch,
			"topology_hash":  stateHash,
			"updated_time":   now,
		})
	if result.Error != nil {
		return RoutingTopologyVersion{}, result.Error
	}
	if result.RowsAffected != 1 {
		return RoutingTopologyVersion{}, ErrRoutingChannelLifecycleConflicted
	}
	return RoutingTopologyVersion{Epoch: nextEpoch, StateHash: stateHash, UpdatedTime: now}, nil
}

func routingTopologyStateHashTx(tx *gorm.DB) (string, error) {
	if tx == nil || tx.Dialector == nil {
		return "", ErrRoutingSchemaNotReady
	}
	var pools []RoutingPool
	if err := tx.Where("active = ?", true).Order("id asc").Find(&pools).Error; err != nil {
		return "", err
	}
	var members []RoutingPoolMember
	if err := tx.Where("active = ?", true).Order("pool_id asc").Order("id asc").Find(&members).Error; err != nil {
		return "", err
	}
	var credentials []RoutingCredentialRef
	if err := tx.Where("active = ?", true).Order("channel_generation asc").Order("id asc").Find(&credentials).Error; err != nil {
		return "", err
	}
	type poolState struct {
		ID              int    `json:"id"`
		GroupKey        string `json:"group_key"`
		GroupName       string `json:"group_name"`
		DisplayName     string `json:"display_name"`
		Source          string `json:"source"`
		DefaultEnabled  bool   `json:"default_enabled"`
		DefaultPriority int64  `json:"default_priority"`
		DefaultWeight   int64  `json:"default_weight"`
	}
	type memberState struct {
		ID                int    `json:"id"`
		PoolID            int    `json:"pool_id"`
		ChannelID         int    `json:"channel_id"`
		ChannelGeneration string `json:"channel_generation"`
		Source            string `json:"source"`
		EnabledOverride   *bool  `json:"enabled_override,omitempty"`
		PriorityOverride  *int64 `json:"priority_override,omitempty"`
		WeightOverride    *int64 `json:"weight_override,omitempty"`
	}
	type credentialState struct {
		ID                 int    `json:"id"`
		ChannelID          int    `json:"channel_id"`
		ChannelGeneration  string `json:"channel_generation"`
		Fingerprint        string `json:"fingerprint"`
		FingerprintVersion int    `json:"fingerprint_version"`
		LastSeenIndex      int    `json:"last_seen_index"`
		CurrentOccurrences int    `json:"current_occurrences"`
	}
	payload := struct {
		SchemaVersion int               `json:"schema_version"`
		Pools         []poolState       `json:"pools"`
		Members       []memberState     `json:"members"`
		Credentials   []credentialState `json:"credentials"`
	}{SchemaVersion: 2}
	payload.Pools = make([]poolState, 0, len(pools))
	for _, pool := range pools {
		payload.Pools = append(payload.Pools, poolState{
			ID: pool.ID, GroupKey: pool.GroupKey, GroupName: pool.GroupName, DisplayName: pool.DisplayName,
			Source: pool.Source, DefaultEnabled: pool.DefaultEnabled,
			DefaultPriority: pool.DefaultPriority, DefaultWeight: pool.DefaultWeight,
		})
	}
	payload.Members = make([]memberState, 0, len(members))
	for _, member := range members {
		payload.Members = append(payload.Members, memberState{
			ID: member.ID, PoolID: member.PoolID, ChannelID: member.ChannelID,
			ChannelGeneration: member.ChannelGeneration, Source: member.Source,
			EnabledOverride: member.EnabledOverride, PriorityOverride: member.PriorityOverride,
			WeightOverride: member.WeightOverride,
		})
	}
	payload.Credentials = make([]credentialState, 0, len(credentials))
	for _, credential := range credentials {
		payload.Credentials = append(payload.Credentials, credentialState{
			ID: credential.ID, ChannelID: credential.ChannelID, ChannelGeneration: credential.ChannelGeneration,
			Fingerprint: credential.Fingerprint, FingerprintVersion: credential.FingerprintVersion,
			LastSeenIndex: credential.LastSeenIndex, CurrentOccurrences: credential.CurrentOccurrences,
		})
	}
	encoded, err := common.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func routingTopologyInitialHash() string {
	digest := sha256.Sum256([]byte("routing-topology:empty:v2"))
	return hex.EncodeToString(digest[:])
}

func ensureRoutingTopologyVersion(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || !db.Migrator().HasTable(&RoutingTopologyMetadata{}) {
		return ErrRoutingSchemaNotReady
	}
	var metadata RoutingTopologyMetadata
	err := db.Where("id = ?", routingTopologyMetadataID).First(&metadata).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if metadata.TopologyEpoch < 0 {
		return ErrRoutingSchemaNotReady
	}
	if validRoutingChannelHash(metadata.TopologyHash) {
		return nil
	}
	return db.Model(&RoutingTopologyMetadata{}).Where("id = ?", metadata.ID).
		Update("topology_hash", routingTopologyInitialHash()).Error
}
