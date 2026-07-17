package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingPoolSourceLegacyGroup         = "legacy_group"
	RoutingCredentialFingerprintVersion  = 2
	routingTopologyMetadataID            = 1
	routingTopologyMaxChannels           = 100_000
	routingTopologyMaxPools              = 4_096
	routingTopologyMaxMembers            = 100_000
	routingTopologyMaxCredentials        = 100_000
	routingTopologyMaxKeysPerChannel     = 4_096
	routingTopologyMaxKeyBytesPerChannel = 1 << 20
	routingTopologyMaxTotalKeyBytes      = 64 << 20
)

var ErrRoutingTopologyLimitExceeded = errors.New("routing topology limit exceeded")

type RoutingTopologyMetadata struct {
	ID                   int    `json:"id" gorm:"primaryKey"`
	CredentialSecretHash string `json:"-" gorm:"type:varchar(128);not null"`
	TopologyEpoch        int64  `json:"topology_epoch" gorm:"bigint;not null"`
	TopologyHash         string `json:"topology_hash" gorm:"type:char(64);index;not null"`
	CreatedTime          int64  `json:"created_time" gorm:"bigint"`
	UpdatedTime          int64  `json:"updated_time" gorm:"bigint"`
}

func (RoutingTopologyMetadata) TableName() string {
	return "routing_topology_metadata"
}

type RoutingPool struct {
	ID              int    `json:"id" gorm:"primaryKey"`
	GroupKey        string `json:"-" gorm:"type:varchar(64);uniqueIndex;not null"`
	GroupName       string `json:"group_name" gorm:"type:varchar(64);index;not null"`
	DisplayName     string `json:"display_name" gorm:"type:varchar(128);not null"`
	Source          string `json:"source" gorm:"type:varchar(32);index;not null"`
	Active          bool   `json:"active" gorm:"index"`
	DefaultEnabled  bool   `json:"default_enabled" gorm:"not null"`
	DefaultPriority int64  `json:"default_priority" gorm:"bigint;not null"`
	DefaultWeight   int64  `json:"default_weight" gorm:"bigint;not null"`
	CreatedTime     int64  `json:"created_time" gorm:"bigint"`
	UpdatedTime     int64  `json:"updated_time" gorm:"bigint;index"`
}

func (RoutingPool) TableName() string {
	return "routing_pools"
}

type RoutingPoolMember struct {
	ID                int    `json:"id" gorm:"primaryKey"`
	PoolID            int    `json:"pool_id" gorm:"uniqueIndex:idx_routing_pool_member_generation,priority:1;index;not null"`
	ChannelID         int    `json:"channel_id" gorm:"index;not null"`
	ChannelGeneration string `json:"channel_generation" gorm:"type:varchar(32);uniqueIndex:idx_routing_pool_member_generation,priority:2;index"`
	Source            string `json:"source" gorm:"type:varchar(32);index;not null"`
	Active            bool   `json:"active" gorm:"index"`
	EnabledOverride   *bool  `json:"enabled_override,omitempty"`
	PriorityOverride  *int64 `json:"priority_override,omitempty" gorm:"bigint"`
	WeightOverride    *int64 `json:"weight_override,omitempty" gorm:"bigint"`
	LegacyPriority    int64  `json:"legacy_priority" gorm:"bigint"`
	LegacyWeight      int64  `json:"legacy_weight" gorm:"bigint"`
	CreatedTime       int64  `json:"created_time" gorm:"bigint"`
	UpdatedTime       int64  `json:"updated_time" gorm:"bigint;index"`
}

func (RoutingPoolMember) TableName() string {
	return "routing_pool_members"
}

type RoutingCredentialRef struct {
	ID                 int    `json:"id" gorm:"primaryKey"`
	ChannelID          int    `json:"channel_id" gorm:"index;not null"`
	ChannelGeneration  string `json:"-" gorm:"type:varchar(32);uniqueIndex:idx_routing_credential_ref_generation,priority:1;index"`
	Fingerprint        string `json:"-" gorm:"type:varchar(64);uniqueIndex:idx_routing_credential_ref_generation,priority:2;not null"`
	FingerprintVersion int    `json:"fingerprint_version"`
	Active             bool   `json:"active" gorm:"index"`
	LastSeenIndex      int    `json:"last_seen_index"`
	CurrentOccurrences int    `json:"current_occurrences"`
	CreatedTime        int64  `json:"created_time" gorm:"bigint"`
	UpdatedTime        int64  `json:"updated_time" gorm:"bigint;index"`
	RetiredTime        int64  `json:"retired_time" gorm:"bigint;index"`
}

func (RoutingCredentialRef) TableName() string {
	return "routing_credential_refs"
}

type RoutingTopologyReconcileSummary struct {
	ActivePools        int    `json:"active_pools"`
	ActiveMembers      int    `json:"active_members"`
	ActiveCredentials  int    `json:"active_credentials"`
	CreatedPools       int    `json:"created_pools"`
	CreatedMembers     int    `json:"created_members"`
	CreatedCredentials int    `json:"created_credentials"`
	RetiredPools       int    `json:"retired_pools"`
	RetiredMembers     int    `json:"retired_members"`
	RetiredCredentials int    `json:"retired_credentials"`
	TopologyEpoch      int64  `json:"topology_epoch"`
	TopologyHash       string `json:"topology_hash"`
}

func RoutingCredentialFingerprint(channelID int, channelGeneration string, key string) (string, error) {
	if channelID <= 0 || channelGeneration == "" || key == "" {
		return "", errors.New("routing credential identity is invalid")
	}
	if !common.CryptoSecretIsPersistent() {
		return "", ErrCredentialSecretUnstable
	}
	payload := "routing-credential:v" + strconv.Itoa(RoutingCredentialFingerprintVersion) + "\x00" +
		strconv.Itoa(channelID) + "\x00" + channelGeneration + "\x00" + key
	return common.GenerateHMAC(payload), nil
}

func ReconcileLegacyRoutingTopologyContext(ctx context.Context) (RoutingTopologyReconcileSummary, error) {
	var summary RoutingTopologyReconcileSummary
	if ctx == nil {
		ctx = context.Background()
	}
	if !common.CryptoSecretIsPersistent() {
		return summary, ErrCredentialSecretUnstable
	}

	now := common.GetTimestamp()
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		secretVerifier := routingCredentialSecretVerifier()
		metadata := RoutingTopologyMetadata{}
		err := lockForUpdate(tx).Where("id = ?", routingTopologyMetadataID).First(&metadata).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			secretHash, hashErr := common.Password2Hash(secretVerifier)
			if hashErr != nil {
				return hashErr
			}
			candidate := RoutingTopologyMetadata{
				ID:                   routingTopologyMetadataID,
				CredentialSecretHash: secretHash,
				TopologyHash:         routingTopologyInitialHash(),
				CreatedTime:          now,
				UpdatedTime:          now,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				DoNothing: true,
			}).Create(&candidate).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", routingTopologyMetadataID).First(&metadata).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !common.ValidatePasswordAndHash(secretVerifier, metadata.CredentialSecretHash) {
			return ErrCredentialSecretMismatch
		}

		channels := make([]Channel, 0)
		lastChannelID := 0
		totalKeyBytes := 0
		for len(channels) <= routingTopologyMaxChannels {
			var page []Channel
			query := tx.Select("id", "routing_generation", "key", "group", "priority", "weight", "channel_info").Order("id asc").Limit(500)
			if lastChannelID > 0 {
				query = query.Where("id > ?", lastChannelID)
			}
			if err := query.Find(&page).Error; err != nil {
				return err
			}
			if len(page) == 0 {
				break
			}
			for index := range page {
				keyBytes := len(page[index].Key)
				if keyBytes > routingTopologyMaxKeyBytesPerChannel {
					return fmt.Errorf("%w: channel %d credential bytes", ErrRoutingTopologyLimitExceeded, page[index].Id)
				}
				totalKeyBytes += keyBytes
				if totalKeyBytes > routingTopologyMaxTotalKeyBytes {
					return fmt.Errorf("%w: total credential bytes", ErrRoutingTopologyLimitExceeded)
				}
			}
			channels = append(channels, page...)
			lastChannelID = page[len(page)-1].Id
			if len(page) < 500 {
				break
			}
		}
		if len(channels) > routingTopologyMaxChannels {
			return fmt.Errorf("%w: channels", ErrRoutingTopologyLimitExceeded)
		}

		desiredGroups := make(map[string]struct{})
		channelGroups := make(map[int][]string, len(channels))
		for i := range channels {
			groups := normalizedRoutingGroups(channels[i].GetGroups())
			channelGroups[channels[i].Id] = groups
			for _, group := range groups {
				desiredGroups[group] = struct{}{}
			}
		}
		if len(desiredGroups) > routingTopologyMaxPools {
			return fmt.Errorf("%w: pools", ErrRoutingTopologyLimitExceeded)
		}

		groupNames := make([]string, 0, len(desiredGroups))
		for group := range desiredGroups {
			groupNames = append(groupNames, group)
		}
		sort.Strings(groupNames)

		var pools []RoutingPool
		if err := tx.Order("id asc").Limit(routingTopologyMaxPools + 1).Find(&pools).Error; err != nil {
			return err
		}
		if len(pools) > routingTopologyMaxPools {
			return fmt.Errorf("%w: pools", ErrRoutingTopologyLimitExceeded)
		}
		poolsByGroup := make(map[string]RoutingPool, len(pools))
		for index := range pools {
			pool := pools[index]
			expectedGroupKey := routingGroupKey(pool.GroupName)
			if pool.GroupKey != expectedGroupKey {
				if err := tx.Model(&RoutingPool{}).Where("id = ?", pool.ID).Update("group_key", expectedGroupKey).Error; err != nil {
					return err
				}
				pool.GroupKey = expectedGroupKey
				pools[index] = pool
			}
			poolsByGroup[pool.GroupName] = pool
		}

		for _, group := range groupNames {
			pool, exists := poolsByGroup[group]
			if !exists {
				candidate := RoutingPool{
					GroupKey:        routingGroupKey(group),
					GroupName:       group,
					DisplayName:     group,
					Source:          RoutingPoolSourceLegacyGroup,
					Active:          true,
					DefaultEnabled:  true,
					DefaultPriority: 0,
					DefaultWeight:   100,
					CreatedTime:     now,
					UpdatedTime:     now,
				}
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "group_key"}},
					DoNothing: true,
				}).Create(&candidate).Error; err != nil {
					return err
				}
				if err := tx.Where("group_key = ?", candidate.GroupKey).First(&pool).Error; err != nil {
					return err
				}
				if candidate.ID != 0 && candidate.ID == pool.ID {
					summary.CreatedPools++
				}
			} else if !pool.Active || (pool.Source == RoutingPoolSourceLegacyGroup && pool.DisplayName != group) {
				updates := map[string]any{"active": true, "updated_time": now}
				if pool.Source == RoutingPoolSourceLegacyGroup {
					updates["display_name"] = group
				}
				if err := tx.Model(&RoutingPool{}).Where("id = ?", pool.ID).Updates(updates).Error; err != nil {
					return err
				}
				pool.Active = true
				pool.UpdatedTime = now
			}
			poolsByGroup[group] = pool
			summary.ActivePools++
		}

		for _, pool := range pools {
			if pool.Source != RoutingPoolSourceLegacyGroup || !pool.Active {
				continue
			}
			if _, desired := desiredGroups[pool.GroupName]; desired {
				continue
			}
			if err := tx.Model(&RoutingPool{}).Where("id = ?", pool.ID).Updates(map[string]any{
				"active":       false,
				"updated_time": now,
			}).Error; err != nil {
				return err
			}
			summary.RetiredPools++
		}

		var existingMembers []RoutingPoolMember
		if err := tx.Order("id asc").Limit(routingTopologyMaxMembers + 1).Find(&existingMembers).Error; err != nil {
			return err
		}
		if len(existingMembers) > routingTopologyMaxMembers {
			return fmt.Errorf("%w: members", ErrRoutingTopologyLimitExceeded)
		}
		membersByKey := make(map[routingPoolMemberIdentity]RoutingPoolMember, len(existingMembers))
		for _, member := range existingMembers {
			membersByKey[routingPoolMemberIdentity{
				PoolID: member.PoolID, ChannelGeneration: member.ChannelGeneration,
			}] = member
		}
		desiredMembers := make(map[routingPoolMemberIdentity]struct{})
		for i := range channels {
			priority := int64(0)
			var priorityOverride *int64
			if channels[i].Priority != nil {
				priority = *channels[i].Priority
				value := priority
				priorityOverride = &value
			}
			weight := int64(100)
			var weightOverride *int64
			if channels[i].Weight != nil {
				weight = int64(*channels[i].Weight)
				value := weight
				weightOverride = &value
			}
			for _, group := range channelGroups[channels[i].Id] {
				pool := poolsByGroup[group]
				identity := routingPoolMemberIdentity{
					PoolID: pool.ID, ChannelGeneration: channels[i].RoutingGeneration,
				}
				if _, exists := desiredMembers[identity]; !exists && len(desiredMembers) >= routingTopologyMaxMembers {
					return fmt.Errorf("%w: members", ErrRoutingTopologyLimitExceeded)
				}
				desiredMembers[identity] = struct{}{}
				member, exists := membersByKey[identity]
				if !exists {
					candidate := RoutingPoolMember{
						PoolID:            pool.ID,
						ChannelID:         channels[i].Id,
						ChannelGeneration: channels[i].RoutingGeneration,
						Source:            RoutingPoolSourceLegacyGroup,
						Active:            true,
						PriorityOverride:  priorityOverride,
						WeightOverride:    weightOverride,
						LegacyPriority:    priority,
						LegacyWeight:      weight,
						CreatedTime:       now,
						UpdatedTime:       now,
					}
					if err := tx.Clauses(clause.OnConflict{
						Columns:   []clause.Column{{Name: "pool_id"}, {Name: "channel_generation"}},
						DoNothing: true,
					}).Create(&candidate).Error; err != nil {
						return err
					}
					if err := tx.Where(
						"pool_id = ? AND channel_generation = ?", pool.ID, channels[i].RoutingGeneration,
					).First(&member).Error; err != nil {
						return err
					}
					if candidate.ID != 0 && candidate.ID == member.ID {
						summary.CreatedMembers++
					}
				} else if member.ChannelID != channels[i].Id {
					return ErrRoutingChannelLifecycleConflicted
				} else if !member.Active || member.LegacyPriority != priority || member.LegacyWeight != weight ||
					!routingOptionalInt64Equal(member.PriorityOverride, priorityOverride) ||
					!routingOptionalInt64Equal(member.WeightOverride, weightOverride) {
					if err := tx.Model(&RoutingPoolMember{}).Where("id = ?", member.ID).Updates(map[string]any{
						"active":            true,
						"priority_override": priorityOverride,
						"weight_override":   weightOverride,
						"legacy_priority":   priority,
						"legacy_weight":     weight,
						"updated_time":      now,
					}).Error; err != nil {
						return err
					}
				}
				summary.ActiveMembers++
			}
		}
		for _, member := range existingMembers {
			if member.Source != RoutingPoolSourceLegacyGroup || !member.Active {
				continue
			}
			identity := routingPoolMemberIdentity{
				PoolID: member.PoolID, ChannelGeneration: member.ChannelGeneration,
			}
			if _, desired := desiredMembers[identity]; desired {
				continue
			}
			if err := tx.Model(&RoutingPoolMember{}).Where("id = ?", member.ID).Updates(map[string]any{
				"active":       false,
				"updated_time": now,
			}).Error; err != nil {
				return err
			}
			summary.RetiredMembers++
		}

		var existingCredentials []RoutingCredentialRef
		if err := tx.Order("id asc").Limit(routingTopologyMaxCredentials + 1).Find(&existingCredentials).Error; err != nil {
			return err
		}
		if len(existingCredentials) > routingTopologyMaxCredentials {
			return fmt.Errorf("%w: credentials", ErrRoutingTopologyLimitExceeded)
		}
		credentialsByKey := make(map[routingCredentialIdentity]RoutingCredentialRef, len(existingCredentials))
		for _, ref := range existingCredentials {
			credentialsByKey[routingCredentialIdentity{
				ChannelGeneration: ref.ChannelGeneration, Fingerprint: ref.Fingerprint,
			}] = ref
		}
		desiredCredentials := make(map[routingCredentialIdentity]routingCredentialObservation)
		for i := range channels {
			observations, err := routingCredentialObservations(channels[i])
			if err != nil {
				return err
			}
			for identity, observation := range observations {
				if _, exists := desiredCredentials[identity]; !exists && len(desiredCredentials) >= routingTopologyMaxCredentials {
					return fmt.Errorf("%w: credentials", ErrRoutingTopologyLimitExceeded)
				}
				desiredCredentials[identity] = observation
			}
		}
		credentialKeys := make([]routingCredentialIdentity, 0, len(desiredCredentials))
		for identity := range desiredCredentials {
			credentialKeys = append(credentialKeys, identity)
		}
		sort.Slice(credentialKeys, func(i, j int) bool {
			if credentialKeys[i].ChannelGeneration == credentialKeys[j].ChannelGeneration {
				return credentialKeys[i].Fingerprint < credentialKeys[j].Fingerprint
			}
			return credentialKeys[i].ChannelGeneration < credentialKeys[j].ChannelGeneration
		})
		for _, identity := range credentialKeys {
			observation := desiredCredentials[identity]
			ref, exists := credentialsByKey[identity]
			if !exists {
				candidate := RoutingCredentialRef{
					ChannelID:          observation.ChannelID,
					ChannelGeneration:  observation.ChannelGeneration,
					Fingerprint:        identity.Fingerprint,
					FingerprintVersion: RoutingCredentialFingerprintVersion,
					Active:             true,
					LastSeenIndex:      observation.FirstIndex,
					CurrentOccurrences: observation.Occurrences,
					CreatedTime:        now,
					UpdatedTime:        now,
				}
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "channel_generation"}, {Name: "fingerprint"}},
					DoNothing: true,
				}).Create(&candidate).Error; err != nil {
					return err
				}
				if err := tx.Where(
					"channel_generation = ? AND fingerprint = ?", identity.ChannelGeneration, identity.Fingerprint,
				).First(&ref).Error; err != nil {
					return err
				}
				if candidate.ID != 0 && candidate.ID == ref.ID {
					summary.CreatedCredentials++
				}
			} else if ref.ChannelID != observation.ChannelID {
				return ErrRoutingChannelLifecycleConflicted
			} else if !ref.Active || ref.ChannelGeneration != observation.ChannelGeneration ||
				ref.FingerprintVersion != RoutingCredentialFingerprintVersion || ref.LastSeenIndex != observation.FirstIndex ||
				ref.CurrentOccurrences != observation.Occurrences || ref.RetiredTime != 0 {
				if err := tx.Model(&RoutingCredentialRef{}).Where("id = ?", ref.ID).Updates(map[string]any{
					"active":              true,
					"channel_generation":  observation.ChannelGeneration,
					"fingerprint_version": RoutingCredentialFingerprintVersion,
					"last_seen_index":     observation.FirstIndex,
					"current_occurrences": observation.Occurrences,
					"retired_time":        0,
					"updated_time":        now,
				}).Error; err != nil {
					return err
				}
			}
			summary.ActiveCredentials++
		}
		for _, ref := range existingCredentials {
			if !ref.Active {
				continue
			}
			identity := routingCredentialIdentity{
				ChannelGeneration: ref.ChannelGeneration, Fingerprint: ref.Fingerprint,
			}
			if _, desired := desiredCredentials[identity]; desired {
				continue
			}
			if err := tx.Model(&RoutingCredentialRef{}).Where("id = ?", ref.ID).Updates(map[string]any{
				"active":              false,
				"current_occurrences": 0,
				"retired_time":        now,
				"updated_time":        now,
			}).Error; err != nil {
				return err
			}
			summary.RetiredCredentials++
		}

		version, err := advanceRoutingTopologyVersionTx(tx, now)
		if err != nil {
			return err
		}
		summary.TopologyEpoch = version.Epoch
		summary.TopologyHash = version.StateHash
		return ctx.Err()
	})
	if err != nil {
		return RoutingTopologyReconcileSummary{}, err
	}
	return summary, nil
}

type routingPoolMemberIdentity struct {
	PoolID            int
	ChannelGeneration string
}

type routingCredentialIdentity struct {
	ChannelGeneration string
	Fingerprint       string
}

type routingCredentialObservation struct {
	ChannelID         int
	ChannelGeneration string
	FirstIndex        int
	Occurrences       int
}

func normalizedRoutingGroups(groups []string) []string {
	unique := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		unique[group] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for group := range unique {
		result = append(result, group)
	}
	sort.Strings(result)
	return result
}

func routingGroupKey(group string) string {
	sum := sha256.Sum256([]byte(group))
	return hex.EncodeToString(sum[:])
}

func routingCredentialSecretVerifier() string {
	sum := sha256.Sum256([]byte("routing-credential-secret-verifier:v1\x00" + common.CryptoSecret))
	return hex.EncodeToString(sum[:])
}

func routingCredentialObservations(channel Channel) (map[routingCredentialIdentity]routingCredentialObservation, error) {
	if channel.RoutingGeneration == "" {
		return nil, errors.New("routing channel generation is invalid")
	}
	keys := []string{channel.Key}
	indexes := []int{RoutingMetricSingleKeyIndex}
	if channel.ChannelInfo.IsMultiKey {
		keys = channel.GetKeys()
		if len(keys) > routingTopologyMaxKeysPerChannel {
			return nil, fmt.Errorf("%w: channel %d credential count", ErrRoutingTopologyLimitExceeded, channel.Id)
		}
		indexes = make([]int, len(keys))
		for index := range keys {
			indexes[index] = index
		}
	}

	result := make(map[routingCredentialIdentity]routingCredentialObservation, len(keys))
	for index, key := range keys {
		if key == "" {
			continue
		}
		fingerprint, err := RoutingCredentialFingerprint(channel.Id, channel.RoutingGeneration, key)
		if err != nil {
			return nil, fmt.Errorf("routing credential fingerprint for channel %d: %w", channel.Id, err)
		}
		identity := routingCredentialIdentity{
			ChannelGeneration: channel.RoutingGeneration, Fingerprint: fingerprint,
		}
		observation, exists := result[identity]
		if !exists {
			observation.ChannelID = channel.Id
			observation.ChannelGeneration = channel.RoutingGeneration
			observation.FirstIndex = indexes[index]
		}
		observation.Occurrences++
		result[identity] = observation
	}
	return result, nil
}

func routingOptionalInt64Equal(left *int64, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
