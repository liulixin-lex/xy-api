package model

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// RoutingPolicyV2ConversionSummary records the safety decisions made while a
// legacy policy is converted. Historical v1 rows remain untouched; only the
// newly published v2 revision uses this result.
type RoutingPolicyV2ConversionSummary struct {
	SourceSchemaVersion      int `json:"source_schema_version"`
	PreservedPools           int `json:"preserved_pools"`
	AddedTopologyPools       int `json:"added_topology_pools"`
	PreservedMemberOverrides int `json:"preserved_member_overrides"`
	ResetMemberOverrides     int `json:"reset_member_overrides"`
	DroppedCredentialRefs    int `json:"dropped_credential_refs"`
}

// ConvertRoutingPolicyDocumentToV2DBContext creates a generation-fenced v2
// policy document from a legacy v1 revision. Pool policy is preserved, while
// member-level state is retained only when the exact live member and channel
// lifecycle can be proven. Dynamic members without explicit overrides stay in
// topology and are intentionally omitted from the formal policy document.
func ConvertRoutingPolicyDocumentToV2DBContext(
	ctx context.Context,
	db *gorm.DB,
	document RoutingPolicyDocument,
	preserveMemberOverrides bool,
) (RoutingPolicyDocument, RoutingPolicyV2ConversionSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	summary := RoutingPolicyV2ConversionSummary{SourceSchemaVersion: document.SchemaVersion}
	if db == nil {
		return RoutingPolicyDocument{}, summary, errRoutingPolicyDatabaseNil
	}
	if document.SchemaVersion == 0 {
		document.SchemaVersion = RoutingPolicyLegacySchemaVersion
	}
	normalized, _, err := normalizeRoutingPolicyDocument(document)
	if err != nil {
		return RoutingPolicyDocument{}, summary, err
	}
	if normalized.SchemaVersion == RoutingPolicySchemaVersion {
		return normalized, summary, nil
	}
	if normalized.SchemaVersion != RoutingPolicyLegacySchemaVersion {
		return RoutingPolicyDocument{}, summary, ErrRoutingPolicyInvalid
	}

	var topologyPools []RoutingPool
	if err := db.WithContext(ctx).Where("active = ?", true).Order("id asc").Find(&topologyPools).Error; err != nil {
		return RoutingPolicyDocument{}, summary, err
	}
	var topologyMembers []RoutingPoolMember
	if err := db.WithContext(ctx).Where("active = ?", true).Order("id asc").Find(&topologyMembers).Error; err != nil {
		return RoutingPolicyDocument{}, summary, err
	}
	var credentials []RoutingCredentialRef
	if err := db.WithContext(ctx).Where("active = ?", true).
		Order("channel_generation asc").Order("id asc").Find(&credentials).Error; err != nil {
		return RoutingPolicyDocument{}, summary, err
	}
	var channels []Channel
	if err := db.WithContext(ctx).
		Select("id", "routing_identity", "routing_generation").Find(&channels).Error; err != nil {
		return RoutingPolicyDocument{}, summary, err
	}

	topologyPoolByID := make(map[int]RoutingPool, len(topologyPools))
	for index := range topologyPools {
		topologyPoolByID[topologyPools[index].ID] = topologyPools[index]
	}
	topologyMemberByID := make(map[int]RoutingPoolMember, len(topologyMembers))
	for index := range topologyMembers {
		topologyMemberByID[topologyMembers[index].ID] = topologyMembers[index]
	}
	channelByID := make(map[int]Channel, len(channels))
	for index := range channels {
		channelByID[channels[index].Id] = channels[index]
	}
	credentialByID := make(map[int]RoutingCredentialRef, len(credentials))
	credentialIDsByGeneration := make(map[string][]int)
	for index := range credentials {
		credential := credentials[index]
		credentialByID[credential.ID] = credential
		credentialIDsByGeneration[credential.ChannelGeneration] = append(
			credentialIDsByGeneration[credential.ChannelGeneration], credential.ID,
		)
	}

	converted := RoutingPolicyDocument{
		SchemaVersion:   RoutingPolicySchemaVersion,
		Pools:           make([]RoutingPolicyPoolContent, 0, len(normalized.Pools)+len(topologyPools)),
		ExtensionFields: normalized.ExtensionFields,
	}
	convertedPoolIDs := make(map[int]struct{}, len(normalized.Pools))
	for poolIndex := range normalized.Pools {
		legacyPool := normalized.Pools[poolIndex]
		defaultEnabled := true
		defaultPriority := int64(0)
		defaultWeight := int64(100)
		if topologyPool, exists := topologyPoolByID[legacyPool.PoolID]; exists {
			if topologyPool.GroupName != legacyPool.GroupName {
				return RoutingPolicyDocument{}, summary, ErrRoutingPolicyPoolIdentity
			}
			defaultEnabled = topologyPool.DefaultEnabled
			defaultPriority = topologyPool.DefaultPriority
			defaultWeight = topologyPool.DefaultWeight
		}
		pool := RoutingPolicyPoolContent{
			PoolID: legacyPool.PoolID, GroupName: legacyPool.GroupName,
			DisplayName: legacyPool.DisplayName, DeploymentStage: legacyPool.DeploymentStage,
			PolicyProfile: legacyPool.PolicyProfile, Policy: append(json.RawMessage(nil), legacyPool.Policy...),
			DefaultEnabled: &defaultEnabled, DefaultPriority: &defaultPriority, DefaultWeight: &defaultWeight,
			Members: []RoutingPolicyMemberContent{}, ExtensionFields: legacyPool.ExtensionFields,
		}
		convertedPoolIDs[pool.PoolID] = struct{}{}
		summary.PreservedPools++
		for memberIndex := range legacyPool.Members {
			legacyMember := legacyPool.Members[memberIndex]
			liveMember, memberExists := topologyMemberByID[legacyMember.MemberID]
			channel, channelExists := channelByID[legacyMember.ChannelID]
			proven := preserveMemberOverrides && memberExists && channelExists &&
				liveMember.PoolID == legacyPool.PoolID && liveMember.ChannelID == legacyMember.ChannelID &&
				validRoutingIdentity(liveMember.ChannelGeneration) &&
				validRoutingIdentity(channel.RoutingIdentity) &&
				channel.RoutingGeneration == liveMember.ChannelGeneration
			if !proven {
				summary.ResetMemberOverrides++
				summary.DroppedCredentialRefs += len(legacyMember.CredentialIDs)
				continue
			}

			effectiveEnabled := defaultEnabled
			effectivePriority := defaultPriority
			effectiveWeight := defaultWeight
			if liveMember.EnabledOverride != nil {
				effectiveEnabled = *liveMember.EnabledOverride
			}
			if liveMember.PriorityOverride != nil {
				effectivePriority = *liveMember.PriorityOverride
			}
			if liveMember.WeightOverride != nil {
				effectiveWeight = *liveMember.WeightOverride
			}
			member := RoutingPolicyMemberContent{
				MemberID: legacyMember.MemberID, ChannelID: legacyMember.ChannelID,
				RoutingGeneration: liveMember.ChannelGeneration,
				Overrides:         append(json.RawMessage(nil), legacyMember.Overrides...),
				ExtensionFields:   legacyMember.ExtensionFields,
			}
			if legacyMember.Enabled != effectiveEnabled {
				enabled := legacyMember.Enabled
				member.EnabledOverride = &enabled
			}
			if legacyMember.Priority != effectivePriority {
				priority := legacyMember.Priority
				member.PriorityOverride = &priority
			}
			if legacyMember.Weight != effectiveWeight {
				weight := legacyMember.Weight
				member.WeightOverride = &weight
			}

			requestedCredentialIDs := append([]int(nil), legacyMember.CredentialIDs...)
			sort.Ints(requestedCredentialIDs)
			allCredentialIDs := append([]int(nil), credentialIDsByGeneration[liveMember.ChannelGeneration]...)
			sort.Ints(allCredentialIDs)
			credentialsProven := true
			for _, credentialID := range requestedCredentialIDs {
				credential, exists := credentialByID[credentialID]
				if !exists || credential.ChannelID != legacyMember.ChannelID ||
					credential.ChannelGeneration != liveMember.ChannelGeneration {
					credentialsProven = false
					break
				}
			}
			if !credentialsProven {
				summary.DroppedCredentialRefs += len(requestedCredentialIDs)
			} else if len(requestedCredentialIDs) > 0 && !routingPolicyIntSlicesEqual(requestedCredentialIDs, allCredentialIDs) {
				member.CredentialIDs = requestedCredentialIDs
			}

			hasStructuredOverride := strings.TrimSpace(string(member.Overrides)) != "" &&
				strings.TrimSpace(string(member.Overrides)) != "{}" && strings.TrimSpace(string(member.Overrides)) != "null"
			hasExtension := len(member.ExtensionFields) > 0
			hasTrafficOverride := member.EnabledOverride != nil || member.PriorityOverride != nil || member.WeightOverride != nil
			if !hasStructuredOverride && !hasExtension && !hasTrafficOverride && len(member.CredentialIDs) == 0 {
				continue
			}
			if len(member.Overrides) == 0 {
				member.Overrides = json.RawMessage(`{}`)
			}
			pool.Members = append(pool.Members, member)
			summary.PreservedMemberOverrides++
		}
		converted.Pools = append(converted.Pools, pool)
	}

	for index := range topologyPools {
		topologyPool := topologyPools[index]
		if _, exists := convertedPoolIDs[topologyPool.ID]; exists {
			continue
		}
		defaultEnabled := topologyPool.DefaultEnabled
		defaultPriority := topologyPool.DefaultPriority
		defaultWeight := topologyPool.DefaultWeight
		converted.Pools = append(converted.Pools, RoutingPolicyPoolContent{
			PoolID: topologyPool.ID, GroupName: topologyPool.GroupName, DisplayName: topologyPool.DisplayName,
			DeploymentStage: RoutingDeploymentStageObserve, PolicyProfile: RoutingPolicyProfileBalanced,
			Policy: json.RawMessage(`{}`), DefaultEnabled: &defaultEnabled,
			DefaultPriority: &defaultPriority, DefaultWeight: &defaultWeight,
			Members: []RoutingPolicyMemberContent{},
		})
		summary.AddedTopologyPools++
	}

	converted, _, err = normalizeRoutingPolicyDocument(converted)
	if err != nil {
		if errors.Is(err, ErrRoutingPolicyInvalid) {
			return RoutingPolicyDocument{}, summary, ErrRoutingPolicyInvalid
		}
		return RoutingPolicyDocument{}, summary, err
	}
	return converted, summary, nil
}

func routingPolicyIntSlicesEqual(left []int, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
