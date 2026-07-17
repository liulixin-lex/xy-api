package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"gorm.io/gorm"
)

var ErrRoutingPolicyReferenceInvalid = errors.New("routing policy live reference is invalid")

// validateRoutingPolicyLiveReferencesTx locks every referenced live row so a
// policy cannot pass validation and then commit against a different channel or
// credential state.
func validateRoutingPolicyLiveReferencesTx(tx *gorm.DB, document RoutingPolicyDocument) error {
	if tx == nil {
		return errRoutingPolicyDatabaseNil
	}
	if document.SchemaVersion != RoutingPolicySchemaVersion {
		return fmt.Errorf(
			"%w: legacy policy schema %d must be converted before activation",
			ErrRoutingPolicyReferenceInvalid, document.SchemaVersion,
		)
	}
	type memberReference struct {
		PoolID            int
		ChannelID         int
		RoutingGeneration string
	}
	type credentialReference struct {
		ChannelID         int
		RoutingGeneration string
	}
	channelSet := make(map[int]struct{})
	channelGenerations := make(map[int]string)
	memberReferences := make(map[int]memberReference)
	credentialReferences := make(map[int]credentialReference)
	for poolIndex := range document.Pools {
		pool := document.Pools[poolIndex]
		for memberIndex := range pool.Members {
			member := pool.Members[memberIndex]
			if !validRoutingIdentity(member.RoutingGeneration) {
				return fmt.Errorf(
					"%w: member %d has no exact routing generation",
					ErrRoutingPolicyReferenceInvalid, member.MemberID,
				)
			}
			memberReferences[member.MemberID] = memberReference{
				PoolID: pool.PoolID, ChannelID: member.ChannelID,
				RoutingGeneration: member.RoutingGeneration,
			}
			if generation, exists := channelGenerations[member.ChannelID]; exists &&
				generation != member.RoutingGeneration {
				return fmt.Errorf(
					"%w: channel %d is referenced through multiple generations",
					ErrRoutingPolicyReferenceInvalid, member.ChannelID,
				)
			}
			channelGenerations[member.ChannelID] = member.RoutingGeneration
			channelSet[member.ChannelID] = struct{}{}
			for _, credentialID := range member.CredentialIDs {
				expected := credentialReference{
					ChannelID: member.ChannelID, RoutingGeneration: member.RoutingGeneration,
				}
				if existing, exists := credentialReferences[credentialID]; exists && existing != expected {
					return fmt.Errorf(
						"%w: credential %d is referenced by multiple channel lifecycles",
						ErrRoutingPolicyReferenceInvalid, credentialID,
					)
				}
				credentialReferences[credentialID] = expected
			}
		}
	}

	memberIDs := make([]int, 0, len(memberReferences))
	for memberID := range memberReferences {
		memberIDs = append(memberIDs, memberID)
	}
	sort.Ints(memberIDs)
	activeMembers := make(map[int]RoutingPoolMember, len(memberIDs))
	for start := 0; start < len(memberIDs); start += routingPolicyMemberInsertBatch {
		end := min(start+routingPolicyMemberInsertBatch, len(memberIDs))
		var page []RoutingPoolMember
		if err := lockForUpdate(tx).
			Select("id", "pool_id", "channel_id", "channel_generation", "active").
			Where("id IN ? AND active = ?", memberIDs[start:end], true).
			Find(&page).Error; err != nil {
			return err
		}
		for index := range page {
			activeMembers[page[index].ID] = page[index]
		}
	}
	for _, memberID := range memberIDs {
		expected := memberReferences[memberID]
		member, exists := activeMembers[memberID]
		if !exists || member.PoolID != expected.PoolID || member.ChannelID != expected.ChannelID ||
			member.ChannelGeneration != expected.RoutingGeneration {
			return fmt.Errorf(
				"%w: member %d is missing, inactive, or belongs to another lifecycle",
				ErrRoutingPolicyReferenceInvalid, memberID,
			)
		}
	}

	channelIDs := make([]int, 0, len(channelSet))
	for channelID := range channelSet {
		channelIDs = append(channelIDs, channelID)
	}
	sort.Ints(channelIDs)
	channels := make(map[int]Channel, len(channelIDs))
	for start := 0; start < len(channelIDs); start += routingPolicyMemberInsertBatch {
		end := min(start+routingPolicyMemberInsertBatch, len(channelIDs))
		var page []Channel
		if err := lockForUpdate(tx).
			Select("id", "routing_identity", "routing_generation", "models", "model_mapping").
			Where("id IN ?", channelIDs[start:end]).
			Find(&page).Error; err != nil {
			return err
		}
		for index := range page {
			channel := page[index]
			if err := validateRoutingPolicyChannelModelMapping(channel); err != nil {
				return err
			}
			channels[channel.Id] = channel
		}
	}
	for _, channelID := range channelIDs {
		channel, exists := channels[channelID]
		if !exists {
			return fmt.Errorf("%w: channel %d does not exist", ErrRoutingPolicyReferenceInvalid, channelID)
		}
		if !validRoutingIdentity(channel.RoutingIdentity) || !validRoutingIdentity(channel.RoutingGeneration) {
			return fmt.Errorf("%w: channel %d lifecycle is invalid", ErrRoutingPolicyReferenceInvalid, channelID)
		}
		if channelGenerations[channelID] != channel.RoutingGeneration {
			return fmt.Errorf(
				"%w: channel %d generation changed",
				ErrRoutingPolicyReferenceInvalid, channelID,
			)
		}
	}

	credentialIDs := make([]int, 0, len(credentialReferences))
	for credentialID := range credentialReferences {
		credentialIDs = append(credentialIDs, credentialID)
	}
	sort.Ints(credentialIDs)
	activeCredentials := make(map[int]RoutingCredentialRef, len(credentialIDs))
	for start := 0; start < len(credentialIDs); start += routingPolicyMemberInsertBatch {
		end := min(start+routingPolicyMemberInsertBatch, len(credentialIDs))
		var page []RoutingCredentialRef
		if err := lockForUpdate(tx).
			Select("id", "channel_id", "channel_generation", "active").
			Where("id IN ? AND active = ?", credentialIDs[start:end], true).
			Find(&page).Error; err != nil {
			return err
		}
		for index := range page {
			activeCredentials[page[index].ID] = page[index]
		}
	}
	for _, credentialID := range credentialIDs {
		credential, exists := activeCredentials[credentialID]
		if !exists {
			return fmt.Errorf(
				"%w: credential %d is missing or inactive",
				ErrRoutingPolicyReferenceInvalid, credentialID,
			)
		}
		expected := credentialReferences[credentialID]
		if credential.ChannelID != expected.ChannelID || credential.ChannelGeneration != expected.RoutingGeneration {
			return fmt.Errorf(
				"%w: credential %d belongs to another channel lifecycle",
				ErrRoutingPolicyReferenceInvalid, credentialID,
			)
		}
	}
	return nil
}

func validateRoutingPolicyChannelModelMapping(channel Channel) error {
	mapping := strings.TrimSpace(channel.GetModelMapping())
	if mapping == "" || mapping == "{}" {
		return nil
	}
	declared := make(map[string]string)
	if err := common.UnmarshalJsonStr(mapping, &declared); err != nil {
		return fmt.Errorf(
			"%w: channel %d model mapping: %v",
			ErrRoutingPolicyReferenceInvalid, channel.Id, err,
		)
	}
	models := make(map[string]struct{}, len(declared)+len(channel.GetModels()))
	for modelName, upstreamModel := range declared {
		if strings.TrimSpace(modelName) == "" || strings.TrimSpace(upstreamModel) == "" {
			return fmt.Errorf(
				"%w: channel %d has an empty model mapping entry",
				ErrRoutingPolicyReferenceInvalid, channel.Id,
			)
		}
		models[modelName] = struct{}{}
	}
	for _, modelName := range channel.GetModels() {
		modelName = strings.TrimSpace(modelName)
		modelName = strings.TrimSuffix(modelName, ratio_setting.CompactModelSuffix)
		if modelName != "" {
			models[modelName] = struct{}{}
		}
	}
	for modelName := range models {
		if modelName == "" {
			return fmt.Errorf("%w: channel %d has an empty model mapping key", ErrRoutingPolicyReferenceInvalid, channel.Id)
		}
		resolved, _, err := ResolveChannelModelMapping(mapping, modelName)
		if err != nil || strings.TrimSpace(resolved) == "" {
			return fmt.Errorf(
				"%w: channel %d model %q mapping: %v",
				ErrRoutingPolicyReferenceInvalid, channel.Id, modelName, err,
			)
		}
	}
	return nil
}
