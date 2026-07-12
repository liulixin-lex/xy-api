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
	channelSet := make(map[int]struct{})
	credentialChannels := make(map[int]int)
	for poolIndex := range document.Pools {
		for memberIndex := range document.Pools[poolIndex].Members {
			member := document.Pools[poolIndex].Members[memberIndex]
			channelSet[member.ChannelID] = struct{}{}
			for _, credentialID := range member.CredentialIDs {
				if channelID, exists := credentialChannels[credentialID]; exists && channelID != member.ChannelID {
					return fmt.Errorf(
						"%w: credential %d is referenced by channels %d and %d",
						ErrRoutingPolicyReferenceInvalid, credentialID, channelID, member.ChannelID,
					)
				}
				credentialChannels[credentialID] = member.ChannelID
			}
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
			Select("id", "models", "model_mapping").
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
		if _, exists := channels[channelID]; !exists {
			return fmt.Errorf("%w: channel %d does not exist", ErrRoutingPolicyReferenceInvalid, channelID)
		}
	}

	credentialIDs := make([]int, 0, len(credentialChannels))
	for credentialID := range credentialChannels {
		credentialIDs = append(credentialIDs, credentialID)
	}
	sort.Ints(credentialIDs)
	activeCredentials := make(map[int]RoutingCredentialRef, len(credentialIDs))
	for start := 0; start < len(credentialIDs); start += routingPolicyMemberInsertBatch {
		end := min(start+routingPolicyMemberInsertBatch, len(credentialIDs))
		var page []RoutingCredentialRef
		if err := lockForUpdate(tx).
			Select("id", "channel_id", "active").
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
		expectedChannelID := credentialChannels[credentialID]
		if credential.ChannelID != expectedChannelID {
			return fmt.Errorf(
				"%w: credential %d belongs to channel %d, not %d",
				ErrRoutingPolicyReferenceInvalid, credentialID, credential.ChannelID, expectedChannelID,
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
