package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingPolicyRequiredApprovals              = 2
	routingPolicyApprovalRetentionBatch         = 500
	routingPolicyApprovalActorLegacyUniqueIndex = "idx_routing_policy_approval_actor"
	routingPolicyApprovalIntentActorUniqueIndex = "idx_routing_policy_approval_intent_actor"
)

var (
	ErrRoutingPolicyApprovalInvalid  = errors.New("invalid routing policy approval")
	ErrRoutingPolicyApprovalRequired = errors.New("routing policy approval quorum is not satisfied")
)

type RoutingPolicyApproval struct {
	ID                           int64  `json:"id" gorm:"primaryKey"`
	DraftID                      int64  `json:"draft_id" gorm:"not null;uniqueIndex:idx_routing_policy_approval_intent_actor,priority:1;index"`
	DraftVersion                 int64  `json:"draft_version" gorm:"bigint;not null;uniqueIndex:idx_routing_policy_approval_intent_actor,priority:2;index"`
	DraftETag                    string `json:"draft_etag" gorm:"type:char(64);not null"`
	DocumentHash                 string `json:"document_hash" gorm:"type:char(64);not null;index"`
	HeadRevision                 int64  `json:"head_revision" gorm:"bigint;not null;index"`
	HeadHash                     string `json:"head_hash" gorm:"type:varchar(64);not null"`
	ActivationStage              string `json:"activation_stage" gorm:"type:varchar(16);index"`
	ActivationTrafficBasisPoints int    `json:"activation_traffic_basis_points"`
	ActivationReasonHash         string `json:"activation_reason_hash" gorm:"type:char(64);index"`
	ActivationHash               string `json:"activation_hash" gorm:"type:char(64);index;uniqueIndex:idx_routing_policy_approval_intent_actor,priority:3"`
	ActorID                      int    `json:"actor_id" gorm:"not null;uniqueIndex:idx_routing_policy_approval_intent_actor,priority:4;index"`
	CreatedTimeMs                int64  `json:"created_time_ms" gorm:"bigint;not null;index"`
}

func (RoutingPolicyApproval) TableName() string {
	return "routing_policy_approvals"
}

func (*RoutingPolicyApproval) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

func (*RoutingPolicyApproval) BeforeDelete(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

func CreateRoutingPolicyApprovalContext(
	ctx context.Context,
	draftID int64,
	expectedVersion int64,
	expectedETag string,
	activation RoutingPolicyActivationSpec,
	actorID int,
) (RoutingPolicyApproval, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	activation.ActorID = 0
	activationHash, reasonHash, activationErr := routingPolicyApprovalActivationHash(activation)
	if draftID <= 0 || expectedVersion <= 0 || !validRoutingHash(expectedETag) || actorID <= 0 || activationErr != nil {
		return RoutingPolicyApproval{}, false, ErrRoutingPolicyApprovalInvalid
	}

	var stored RoutingPolicyApproval
	created := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		draft, err := loadRoutingPolicyDraftForUpdate(ctx, tx, draftID)
		if err != nil {
			return err
		}
		if err := requireRoutingPolicyDraftVersion(draft, expectedVersion, expectedETag); err != nil {
			return err
		}
		if draft.Status != RoutingPolicyDraftStatusValidated || draft.ValidatedTimeMs <= 0 {
			return ErrRoutingPolicyApprovalInvalid
		}
		if actorID == draft.CreatedBy {
			return ErrRoutingPolicyApprovalInvalid
		}
		head, err := ensureRoutingPolicyHeadTx(tx.WithContext(ctx))
		if err != nil {
			return err
		}
		if head.CurrentRevision != draft.ValidatedHeadRevision || head.CurrentHash != draft.ValidatedHeadHash {
			return newRoutingPolicyRevisionConflict(draft.ValidatedHeadRevision, head)
		}

		candidate := RoutingPolicyApproval{
			DraftID:                      draft.ID,
			DraftVersion:                 draft.Version,
			DraftETag:                    draft.ETag,
			DocumentHash:                 draft.DocumentHash,
			HeadRevision:                 head.CurrentRevision,
			HeadHash:                     head.CurrentHash,
			ActivationStage:              activation.Stage,
			ActivationTrafficBasisPoints: activation.TrafficBasisPoints,
			ActivationReasonHash:         reasonHash,
			ActivationHash:               activationHash,
			ActorID:                      actorID,
			CreatedTimeMs:                time.Now().UnixMilli(),
		}
		result := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "draft_id"}, {Name: "draft_version"}, {Name: "activation_hash"}, {Name: "actor_id"},
			},
			DoNothing: true,
		}).Create(&candidate)
		if result.Error != nil {
			return result.Error
		}
		created = result.RowsAffected == 1
		if err := tx.WithContext(ctx).
			Where(
				"draft_id = ? AND draft_version = ? AND activation_hash = ? AND actor_id = ?",
				draft.ID, draft.Version, activationHash, actorID,
			).
			First(&stored).Error; err != nil {
			return err
		}
		if stored.DraftETag != draft.ETag || stored.DocumentHash != draft.DocumentHash ||
			stored.HeadRevision != head.CurrentRevision || stored.HeadHash != head.CurrentHash ||
			stored.ActivationHash != activationHash {
			return ErrRoutingPolicyApprovalInvalid
		}
		return validateRoutingPolicyApproval(stored)
	})
	return stored, created, err
}

func ListRoutingPolicyApprovalsContext(
	ctx context.Context,
	draftID int64,
	draftVersion int64,
) ([]RoutingPolicyApproval, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if draftID <= 0 || draftVersion <= 0 {
		return nil, ErrRoutingPolicyApprovalInvalid
	}
	var approvals []RoutingPolicyApproval
	if err := DB.WithContext(ctx).
		Where("draft_id = ? AND draft_version = ?", draftID, draftVersion).
		Order("created_time_ms asc").Order("id asc").
		Limit(100).
		Find(&approvals).Error; err != nil {
		return nil, err
	}
	for index := range approvals {
		if err := validateRoutingPolicyApproval(approvals[index]); err != nil {
			return nil, err
		}
	}
	return approvals, nil
}

func RequireRoutingPolicyApprovalQuorumContext(
	ctx context.Context,
	draft RoutingPolicyDraft,
	activation RoutingPolicyActivationSpec,
	required int,
) ([]RoutingPolicyApproval, error) {
	return requireRoutingPolicyApprovalQuorumDBContext(ctx, DB, draft, activation, required)
}

// DeleteStaleRoutingPolicyApprovalsContext is retained for compatibility with
// older maintenance callers. Approval rows are immutable historical facts and
// are no longer eligible for retention cleanup.
func DeleteStaleRoutingPolicyApprovalsContext(
	ctx context.Context,
	cutoffTimeMs int64,
	limit int,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cutoffTimeMs <= 0 || limit < 1 || limit > routingPolicyApprovalRetentionBatch {
		return 0, ErrRoutingPolicyApprovalInvalid
	}
	return 0, nil
}

func requireRoutingPolicyApprovalQuorumDBContext(
	ctx context.Context,
	db *gorm.DB,
	draft RoutingPolicyDraft,
	activation RoutingPolicyActivationSpec,
	required int,
) ([]RoutingPolicyApproval, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	approvalActivation := activation
	approvalActivation.ActorID = 0
	activationHash, _, activationErr := routingPolicyApprovalActivationHash(approvalActivation)
	if required < 1 || draft.ID <= 0 || draft.Version <= 0 || !validRoutingHash(draft.ETag) || activationErr != nil ||
		draft.Status != RoutingPolicyDraftStatusValidated || db == nil {
		return nil, ErrRoutingPolicyApprovalInvalid
	}
	var approvals []RoutingPolicyApproval
	if err := db.WithContext(ctx).
		Where("draft_id = ? AND draft_version = ?", draft.ID, draft.Version).
		Order("created_time_ms asc").Order("id asc").
		Limit(100).
		Find(&approvals).Error; err != nil {
		return nil, err
	}
	actorIDs := make([]int, 0, len(approvals))
	for index := range approvals {
		if validateRoutingPolicyApproval(approvals[index]) != nil {
			return nil, ErrRoutingPolicyApprovalInvalid
		}
		actorIDs = append(actorIDs, approvals[index].ActorID)
	}
	authorizedActors, err := routingPolicyDeployAuthorizedActorsDBContext(ctx, db, actorIDs)
	if err != nil {
		return nil, err
	}
	valid := make([]RoutingPolicyApproval, 0, len(approvals))
	actors := make(map[int]struct{}, len(approvals))
	for index := range approvals {
		approval := approvals[index]
		if approval.DraftETag != draft.ETag || approval.DocumentHash != draft.DocumentHash ||
			approval.HeadRevision != draft.ValidatedHeadRevision || approval.HeadHash != draft.ValidatedHeadHash ||
			approval.ActivationHash != activationHash || approval.ActorID == draft.CreatedBy ||
			approval.ActorID == activation.ActorID {
			continue
		}
		if _, authorized := authorizedActors[approval.ActorID]; !authorized {
			continue
		}
		if _, exists := actors[approval.ActorID]; exists {
			continue
		}
		actors[approval.ActorID] = struct{}{}
		valid = append(valid, approval)
	}
	if len(valid) < required {
		return valid, ErrRoutingPolicyApprovalRequired
	}
	return valid, nil
}

func validateRoutingPolicyApproval(approval RoutingPolicyApproval) error {
	if approval.ID <= 0 || approval.DraftID <= 0 || approval.DraftVersion <= 0 || approval.ActorID <= 0 ||
		approval.CreatedTimeMs <= 0 || !validRoutingHash(approval.DraftETag) || !validRoutingHash(approval.DocumentHash) ||
		!validRoutingPolicyApprovalActivation(approval.ActivationStage, approval.ActivationTrafficBasisPoints) ||
		!validRoutingHash(approval.ActivationReasonHash) || !validRoutingHash(approval.ActivationHash) ||
		approval.HeadRevision < 0 ||
		(approval.HeadRevision == 0 && approval.HeadHash != "") ||
		(approval.HeadRevision > 0 && !validRoutingHash(approval.HeadHash)) {
		return ErrRoutingPolicyApprovalInvalid
	}
	return nil
}

func validRoutingPolicyApprovalActivation(stage string, trafficBasisPoints int) bool {
	if stage == RoutingDeploymentStageCanary {
		return trafficBasisPoints >= RoutingPolicyCanaryMinBasisPoints &&
			trafficBasisPoints <= RoutingPolicyCanaryMaxBasisPoints
	}
	return validRoutingDeploymentStage(stage) && trafficBasisPoints == 0
}

func routingPolicyApprovalActivationHash(activation RoutingPolicyActivationSpec) (string, string, error) {
	if activation.ActorID != 0 || activation.Validate() != nil {
		return "", "", ErrRoutingPolicyApprovalInvalid
	}
	reasonPayload, err := common.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: activation.Reason})
	if err != nil {
		return "", "", err
	}
	reasonHash := routingPolicyHash(reasonPayload)
	payload, err := common.Marshal(struct {
		SchemaVersion      int    `json:"schema_version"`
		Stage              string `json:"stage"`
		TrafficBasisPoints int    `json:"traffic_basis_points"`
		ReasonHash         string `json:"reason_hash"`
	}{
		SchemaVersion: 1, Stage: activation.Stage,
		TrafficBasisPoints: activation.TrafficBasisPoints, ReasonHash: reasonHash,
	})
	if err != nil {
		return "", "", err
	}
	return routingPolicyHash(payload), reasonHash, nil
}

func RoutingPolicyApprovalActivationIdentity(
	activation RoutingPolicyActivationSpec,
) (string, string, error) {
	activation.ActorID = 0
	return routingPolicyApprovalActivationHash(activation)
}
