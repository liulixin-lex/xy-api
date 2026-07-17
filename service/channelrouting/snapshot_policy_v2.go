package channelrouting

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

func composeSnapshotPolicyDocument(
	ctx context.Context,
	db *gorm.DB,
	document model.RoutingPolicyDocument,
) (model.RoutingPolicyDocument, error) {
	if document.SchemaVersion != model.RoutingPolicySchemaVersion {
		return document, nil
	}
	var pools []model.RoutingPool
	if err := db.WithContext(ctx).Where("active = ?", true).Order("id asc").Find(&pools).Error; err != nil {
		return model.RoutingPolicyDocument{}, err
	}
	var members []model.RoutingPoolMember
	if err := db.WithContext(ctx).Where("active = ?", true).Order("pool_id asc").Order("id asc").Find(&members).Error; err != nil {
		return model.RoutingPolicyDocument{}, err
	}
	var credentials []model.RoutingCredentialRef
	if err := db.WithContext(ctx).Where("active = ?", true).
		Order("channel_generation asc").Order("id asc").Find(&credentials).Error; err != nil {
		return model.RoutingPolicyDocument{}, err
	}

	policyPoolByID := make(map[int]model.RoutingPolicyPoolContent, len(document.Pools))
	policyMemberByID := make(map[int]model.RoutingPolicyMemberContent)
	for _, pool := range document.Pools {
		policyPoolByID[pool.PoolID] = pool
		for _, member := range pool.Members {
			policyMemberByID[member.MemberID] = member
		}
	}
	membersByPool := make(map[int][]model.RoutingPoolMember, len(pools))
	for _, member := range members {
		membersByPool[member.PoolID] = append(membersByPool[member.PoolID], member)
	}
	credentialIDsByGeneration := make(map[string][]int)
	for _, credential := range credentials {
		credentialIDsByGeneration[credential.ChannelGeneration] = append(
			credentialIDsByGeneration[credential.ChannelGeneration], credential.ID,
		)
	}

	effective := model.RoutingPolicyDocument{
		SchemaVersion:   model.RoutingPolicySchemaVersion,
		Pools:           make([]model.RoutingPolicyPoolContent, 0, len(pools)),
		ExtensionFields: document.ExtensionFields,
	}
	consumedPolicyMembers := make(map[int]struct{}, len(policyMemberByID))
	consumedPolicyPools := make(map[int]struct{}, len(pools))
	for _, topologyPool := range pools {
		policyPool, exists := policyPoolByID[topologyPool.ID]
		if exists && policyPool.GroupName != topologyPool.GroupName {
			return model.RoutingPolicyDocument{}, fmt.Errorf(
				"%w: pool %d group changed from %q to %q",
				ErrSnapshotPolicyReference, topologyPool.ID, policyPool.GroupName, topologyPool.GroupName,
			)
		}
		if !exists {
			policyPool = model.RoutingPolicyPoolContent{
				PoolID:          topologyPool.ID,
				DeploymentStage: model.RoutingDeploymentStageObserve,
				PolicyProfile:   model.RoutingPolicyProfileBalanced,
				Policy:          json.RawMessage(`{}`),
			}
		}
		consumedPolicyPools[topologyPool.ID] = struct{}{}
		policyPool.GroupName = topologyPool.GroupName
		policyPool.DisplayName = topologyPool.DisplayName
		if policyPool.DisplayName == "" {
			policyPool.DisplayName = topologyPool.GroupName
		}
		defaultEnabled := topologyPool.DefaultEnabled
		defaultPriority := topologyPool.DefaultPriority
		defaultWeight := topologyPool.DefaultWeight
		if policyPool.DefaultEnabled != nil {
			defaultEnabled = *policyPool.DefaultEnabled
		}
		if policyPool.DefaultPriority != nil {
			defaultPriority = *policyPool.DefaultPriority
		}
		if policyPool.DefaultWeight != nil {
			defaultWeight = *policyPool.DefaultWeight
		}
		if defaultWeight < 0 {
			return model.RoutingPolicyDocument{}, model.ErrRoutingPolicyInvalid
		}
		policyPool.DefaultEnabled = &defaultEnabled
		policyPool.DefaultPriority = &defaultPriority
		policyPool.DefaultWeight = &defaultWeight
		policyPool.Members = make([]model.RoutingPolicyMemberContent, 0, len(membersByPool[topologyPool.ID]))
		for _, topologyMember := range membersByPool[topologyPool.ID] {
			enabled := defaultEnabled
			priority := defaultPriority
			weight := defaultWeight
			if topologyMember.EnabledOverride != nil {
				enabled = *topologyMember.EnabledOverride
			}
			if topologyMember.PriorityOverride != nil {
				priority = *topologyMember.PriorityOverride
			}
			if topologyMember.WeightOverride != nil {
				weight = *topologyMember.WeightOverride
			}
			overrides := json.RawMessage(`{}`)
			credentialIDs := append([]int(nil), credentialIDsByGeneration[topologyMember.ChannelGeneration]...)
			if policyMember, overridden := policyMemberByID[topologyMember.ID]; overridden {
				if policyMember.ChannelID != topologyMember.ChannelID ||
					policyMember.RoutingGeneration != "" &&
						policyMember.RoutingGeneration != topologyMember.ChannelGeneration {
					return model.RoutingPolicyDocument{}, fmt.Errorf(
						"%w: member %d lifecycle changed",
						ErrSnapshotPolicyReference, topologyMember.ID,
					)
				}
				consumedPolicyMembers[topologyMember.ID] = struct{}{}
				if policyMember.EnabledOverride != nil {
					enabled = *policyMember.EnabledOverride
				}
				if policyMember.PriorityOverride != nil {
					priority = *policyMember.PriorityOverride
				}
				if policyMember.WeightOverride != nil {
					weight = *policyMember.WeightOverride
				}
				if len(policyMember.CredentialIDs) > 0 {
					credentialIDs = append([]int(nil), policyMember.CredentialIDs...)
				}
				if len(policyMember.Overrides) > 0 {
					overrides = append(json.RawMessage(nil), policyMember.Overrides...)
				}
			}
			if weight < 0 {
				return model.RoutingPolicyDocument{}, model.ErrRoutingPolicyInvalid
			}
			policyPool.Members = append(policyPool.Members, model.RoutingPolicyMemberContent{
				MemberID: topologyMember.ID, ChannelID: topologyMember.ChannelID,
				RoutingGeneration: topologyMember.ChannelGeneration,
				Enabled:           enabled, Priority: priority, Weight: weight,
				CredentialIDs: credentialIDs, Overrides: overrides,
			})
		}
		effective.Pools = append(effective.Pools, policyPool)
	}
	for _, policyPool := range document.Pools {
		if _, consumed := consumedPolicyPools[policyPool.PoolID]; consumed {
			continue
		}
		if len(policyPool.Members) > 0 {
			continue
		}
		policyPool.Members = []model.RoutingPolicyMemberContent{}
		effective.Pools = append(effective.Pools, policyPool)
	}
	for memberID := range policyMemberByID {
		if _, consumed := consumedPolicyMembers[memberID]; !consumed {
			return model.RoutingPolicyDocument{}, fmt.Errorf(
				"%w: overridden member %d is no longer active",
				ErrSnapshotPolicyReference, memberID,
			)
		}
	}
	sort.Slice(effective.Pools, func(left, right int) bool {
		return effective.Pools[left].PoolID < effective.Pools[right].PoolID
	})
	return effective, nil
}
