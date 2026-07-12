package channelrouting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

const (
	legacyRoutingPolicySyncReason         = "legacy_topology_sync"
	legacyRoutingPolicyPreserveSyncReason = "legacy_topology_sync_preserving_policy"
	legacyRoutingPolicyMaxPublishAttempts = 3
)

func SyncLegacyRoutingPolicyContext(ctx context.Context) (model.RoutingPolicyHead, error) {
	return syncLegacyRoutingPolicyDBContext(ctx, model.DB)
}

func syncLegacyRoutingPolicyDBContext(ctx context.Context, db *gorm.DB) (model.RoutingPolicyHead, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil {
		return model.RoutingPolicyHead{}, errors.New("legacy routing policy database is nil")
	}
	if err := model.EnsureRoutingPolicyHeadDBContext(ctx, db); err != nil {
		return model.RoutingPolicyHead{}, err
	}
	for attempt := 0; attempt < legacyRoutingPolicyMaxPublishAttempts; attempt++ {
		var synchronized model.RoutingPolicyHead
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			head, err := model.GetRoutingPolicyHeadDBContext(ctx, tx)
			if err != nil {
				return err
			}

			var (
				current         model.RoutingPolicyDocument
				currentRevision model.RoutingPolicyRevision
			)
			if head.CurrentRevision > 0 {
				current, currentRevision, err = model.LoadRoutingPolicyRevisionDBContext(ctx, tx, head.CurrentRevision)
				if err != nil {
					return err
				}
			}
			preservePolicy := currentRevision.Revision > 0 &&
				(currentRevision.ActorID != 0 || currentRevision.Reason != legacyRoutingPolicySyncReason)
			document, err := buildLegacyRoutingPolicyDocumentContext(ctx, tx, current, preservePolicy)
			if err != nil {
				return err
			}
			_, contentHash, err := model.NormalizeRoutingPolicyDocument(document)
			if err != nil {
				return err
			}
			if head.CurrentRevision > 0 && head.CurrentHash == contentHash {
				synchronized = head
				return nil
			}

			stage := head.CurrentStage
			if stage == "" {
				stage = model.RoutingDeploymentStageObserve
			}
			trafficBasisPoints := 0
			if head.CurrentActivationID > 0 {
				var activation model.RoutingPolicyActivation
				if err := tx.WithContext(ctx).Where("id = ?", head.CurrentActivationID).First(&activation).Error; err != nil {
					return err
				}
				trafficBasisPoints = activation.TrafficBasisPoints
			}
			reason := legacyRoutingPolicySyncReason
			if preservePolicy {
				reason = legacyRoutingPolicyPreserveSyncReason
			}
			published, err := model.PublishRoutingPolicyRevisionDBContext(ctx, tx, head.CurrentRevision, document, model.RoutingPolicyActivationSpec{
				Stage:              stage,
				TrafficBasisPoints: trafficBasisPoints,
				ActorID:            0,
				Reason:             reason,
			})
			if err != nil {
				return err
			}
			synchronized = model.RoutingPolicyHead{
				ID:                  head.ID,
				CurrentRevision:     published.Revision.Revision,
				CurrentActivationID: published.Activation.ID,
				CurrentHash:         published.Revision.ContentHash,
				CurrentStage:        published.Activation.Stage,
				CreatedTime:         head.CreatedTime,
				UpdatedTime:         published.Revision.CreatedTime,
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
		if err == nil {
			return synchronized, nil
		}
		if !legacyRoutingPolicyRetryableConflict(err) {
			return model.RoutingPolicyHead{}, err
		}
	}
	return model.RoutingPolicyHead{}, model.ErrRoutingPolicyRevisionConflict
}

func legacyRoutingPolicyRetryableConflict(err error) bool {
	if errors.Is(err, model.ErrRoutingPolicyRevisionConflict) {
		return true
	}
	var sqlState interface{ SQLState() string }
	if !errors.As(err, &sqlState) {
		return false
	}
	switch sqlState.SQLState() {
	case "40001", "40P01":
		return true
	default:
		return false
	}
}

func buildLegacyRoutingPolicyDocumentContext(
	ctx context.Context,
	db *gorm.DB,
	current model.RoutingPolicyDocument,
	preservePolicy bool,
) (model.RoutingPolicyDocument, error) {
	var pools []model.RoutingPool
	if err := db.WithContext(ctx).Where("active = ?", true).Order("id asc").Find(&pools).Error; err != nil {
		return model.RoutingPolicyDocument{}, err
	}
	var members []model.RoutingPoolMember
	if err := db.WithContext(ctx).Where("active = ?", true).Order("pool_id asc").Order("id asc").Find(&members).Error; err != nil {
		return model.RoutingPolicyDocument{}, err
	}
	var credentials []model.RoutingCredentialRef
	if err := db.WithContext(ctx).Where("active = ?", true).Order("channel_id asc").Order("id asc").Find(&credentials).Error; err != nil {
		return model.RoutingPolicyDocument{}, err
	}

	currentPools := make(map[int]model.RoutingPolicyPoolContent, len(current.Pools))
	currentMembers := make(map[int]model.RoutingPolicyMemberContent)
	for poolIndex := range current.Pools {
		pool := current.Pools[poolIndex]
		currentPools[pool.PoolID] = pool
		for memberIndex := range pool.Members {
			currentMembers[pool.Members[memberIndex].MemberID] = pool.Members[memberIndex]
		}
	}
	membersByPool := make(map[int][]model.RoutingPoolMember)
	for index := range members {
		membersByPool[members[index].PoolID] = append(membersByPool[members[index].PoolID], members[index])
	}
	credentialsByChannel := make(map[int][]int)
	for index := range credentials {
		credential := credentials[index]
		credentialsByChannel[credential.ChannelID] = append(credentialsByChannel[credential.ChannelID], credential.ID)
	}

	document := model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicySchemaVersion,
		Pools:         make([]model.RoutingPolicyPoolContent, 0, len(pools)),
	}
	for index := range pools {
		pool := pools[index]
		content, exists := currentPools[pool.ID]
		if !exists {
			content = model.RoutingPolicyPoolContent{
				PoolID:          pool.ID,
				DeploymentStage: model.RoutingDeploymentStageObserve,
				PolicyProfile:   model.RoutingPolicyProfileBalanced,
				Policy:          json.RawMessage(`{}`),
			}
		}
		content.PoolID = pool.ID
		content.GroupName = pool.GroupName
		content.DisplayName = pool.DisplayName
		content.Members = make([]model.RoutingPolicyMemberContent, 0, len(membersByPool[pool.ID]))
		for memberIndex := range membersByPool[pool.ID] {
			member := membersByPool[pool.ID][memberIndex]
			memberContent, memberExists := currentMembers[member.ID]
			if !memberExists || memberContent.ChannelID != member.ChannelID {
				memberContent = model.RoutingPolicyMemberContent{
					MemberID:  member.ID,
					ChannelID: member.ChannelID,
					Enabled:   true,
					Priority:  member.LegacyPriority,
					Weight:    member.LegacyWeight,
					Overrides: json.RawMessage(`{}`),
				}
			} else if !preservePolicy {
				memberContent.Enabled = true
				memberContent.Priority = member.LegacyPriority
				memberContent.Weight = member.LegacyWeight
			}
			memberContent.MemberID = member.ID
			memberContent.ChannelID = member.ChannelID
			memberContent.CredentialIDs = append([]int(nil), credentialsByChannel[member.ChannelID]...)
			content.Members = append(content.Members, memberContent)
		}
		document.Pools = append(document.Pools, content)
	}
	return document, nil
}
