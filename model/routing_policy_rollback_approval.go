package model

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	routingPolicyRollbackApprovalRetentionBatch         = 500
	routingPolicyRollbackApprovalActorLegacyUniqueIndex = "idx_routing_policy_rollback_approval_actor"
	routingPolicyRollbackApprovalIntentActorUniqueIndex = "idx_routing_policy_rollback_approval_intent_actor"
)

type RoutingPolicyRollbackApproval struct {
	ID                           int64  `json:"id" gorm:"primaryKey"`
	ExpectedRevision             int64  `json:"expected_revision" gorm:"bigint;index;not null;uniqueIndex:idx_routing_policy_rollback_approval_intent_actor,priority:1"`
	ExpectedActivationID         int64  `json:"expected_activation_id" gorm:"bigint;index;not null;uniqueIndex:idx_routing_policy_rollback_approval_intent_actor,priority:2"`
	ExpectedHeadHash             string `json:"expected_head_hash" gorm:"type:char(64);not null"`
	TargetRevision               int64  `json:"target_revision" gorm:"bigint;index;not null;uniqueIndex:idx_routing_policy_rollback_approval_intent_actor,priority:3"`
	TargetContentHash            string `json:"target_content_hash" gorm:"type:char(64);not null"`
	ActivationStage              string `json:"activation_stage" gorm:"type:varchar(16);index"`
	ActivationTrafficBasisPoints int    `json:"activation_traffic_basis_points"`
	ActivationReasonHash         string `json:"activation_reason_hash" gorm:"type:char(64);index"`
	ActivationHash               string `json:"activation_hash" gorm:"type:char(64);index;uniqueIndex:idx_routing_policy_rollback_approval_intent_actor,priority:4"`
	ActorID                      int    `json:"actor_id" gorm:"index;not null;uniqueIndex:idx_routing_policy_rollback_approval_intent_actor,priority:5"`
	CreatedTimeMs                int64  `json:"created_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingPolicyRollbackApproval) TableName() string {
	return "routing_policy_rollback_approvals"
}

func (*RoutingPolicyRollbackApproval) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

func (*RoutingPolicyRollbackApproval) BeforeDelete(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

func CreateRoutingPolicyRollbackApprovalContext(
	ctx context.Context,
	expectedHead RoutingPolicyHead,
	targetRevision int64,
	activation RoutingPolicyActivationSpec,
	actorID int,
) (RoutingPolicyRollbackApproval, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	activation.ActorID = 0
	activationHash, reasonHash, activationErr := routingPolicyApprovalActivationHash(activation)
	if !validRoutingPolicyRollbackApprovalHead(expectedHead) || targetRevision <= 0 ||
		targetRevision >= expectedHead.CurrentRevision || actorID <= 0 || activationErr != nil ||
		strings.TrimSpace(activation.Reason) == "" {
		return RoutingPolicyRollbackApproval{}, false, ErrRoutingPolicyApprovalInvalid
	}

	var stored RoutingPolicyRollbackApproval
	created := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		head, err := ensureRoutingPolicyHeadTx(tx.WithContext(ctx))
		if err != nil {
			return err
		}
		if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
			return err
		}
		if !routingPolicyRollbackApprovalHeadMatches(head, expectedHead) {
			return newRoutingPolicyRevisionConflict(expectedHead.CurrentRevision, head)
		}
		document, target, err := LoadRoutingPolicyRevisionDBContext(ctx, tx, targetRevision)
		if err != nil {
			return err
		}
		if !routingPolicyPublishRequiresApproval(document, activation) {
			return ErrRoutingPolicyApprovalInvalid
		}
		if err := validateRoutingPolicyLiveReferencesTx(tx.WithContext(ctx), document); err != nil {
			return err
		}

		candidate := RoutingPolicyRollbackApproval{
			ExpectedRevision: expectedHead.CurrentRevision, ExpectedActivationID: expectedHead.CurrentActivationID,
			ExpectedHeadHash: expectedHead.CurrentHash, TargetRevision: target.Revision,
			TargetContentHash: target.ContentHash, ActivationStage: activation.Stage,
			ActivationTrafficBasisPoints: activation.TrafficBasisPoints,
			ActivationReasonHash:         reasonHash, ActivationHash: activationHash,
			ActorID: actorID, CreatedTimeMs: time.Now().UnixMilli(),
		}
		result := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "expected_revision"}, {Name: "expected_activation_id"},
				{Name: "target_revision"}, {Name: "activation_hash"}, {Name: "actor_id"},
			},
			DoNothing: true,
		}).Create(&candidate)
		if result.Error != nil {
			return result.Error
		}
		created = result.RowsAffected == 1
		if err := tx.WithContext(ctx).Where(
			"expected_revision = ? AND expected_activation_id = ? AND target_revision = ? AND activation_hash = ? AND actor_id = ?",
			expectedHead.CurrentRevision, expectedHead.CurrentActivationID, targetRevision, activationHash, actorID,
		).First(&stored).Error; err != nil {
			return err
		}
		if stored.ExpectedHeadHash != expectedHead.CurrentHash || stored.TargetContentHash != target.ContentHash ||
			stored.ActivationHash != activationHash {
			return ErrRoutingPolicyApprovalInvalid
		}
		return validateRoutingPolicyRollbackApproval(stored)
	})
	return stored, created, err
}

func ListRoutingPolicyRollbackApprovalsContext(
	ctx context.Context,
	expectedHead RoutingPolicyHead,
	targetRevision int64,
) ([]RoutingPolicyRollbackApproval, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingPolicyRollbackApprovalHead(expectedHead) || targetRevision <= 0 ||
		targetRevision >= expectedHead.CurrentRevision {
		return nil, ErrRoutingPolicyApprovalInvalid
	}
	var approvals []RoutingPolicyRollbackApproval
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		head, err := ensureRoutingPolicyHeadTx(tx.WithContext(ctx))
		if err != nil {
			return err
		}
		if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
			return err
		}
		if !routingPolicyRollbackApprovalHeadMatches(head, expectedHead) {
			return newRoutingPolicyRevisionConflict(expectedHead.CurrentRevision, head)
		}
		_, target, err := LoadRoutingPolicyRevisionDBContext(ctx, tx, targetRevision)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).
			Where(
				"expected_revision = ? AND expected_activation_id = ? AND target_revision = ?",
				head.CurrentRevision, head.CurrentActivationID, targetRevision,
			).
			Order("created_time_ms asc").Order("id asc").Limit(100).
			Find(&approvals).Error; err != nil {
			return err
		}
		for index := range approvals {
			approval := approvals[index]
			if validateRoutingPolicyRollbackApproval(approval) != nil ||
				approval.ExpectedHeadHash != head.CurrentHash || approval.TargetContentHash != target.ContentHash {
				return ErrRoutingPolicyApprovalInvalid
			}
		}
		return nil
	})
	return approvals, err
}

func requireRoutingPolicyRollbackApprovalQuorumDBContext(
	ctx context.Context,
	db *gorm.DB,
	head RoutingPolicyHead,
	target RoutingPolicyRevision,
	activation RoutingPolicyActivationSpec,
	required int,
) ([]RoutingPolicyRollbackApproval, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	approvalActivation := activation
	approvalActivation.ActorID = 0
	activationHash, _, activationErr := routingPolicyApprovalActivationHash(approvalActivation)
	if db == nil || required < 1 || !validRoutingPolicyRollbackApprovalHead(head) ||
		target.Revision <= 0 || target.Revision >= head.CurrentRevision || !validRoutingHash(target.ContentHash) ||
		activationErr != nil || strings.TrimSpace(activation.Reason) == "" {
		return nil, ErrRoutingPolicyApprovalInvalid
	}
	var approvals []RoutingPolicyRollbackApproval
	if err := db.WithContext(ctx).
		Where(
			"expected_revision = ? AND expected_activation_id = ? AND target_revision = ?",
			head.CurrentRevision, head.CurrentActivationID, target.Revision,
		).
		Order("created_time_ms asc").Order("id asc").Limit(100).
		Find(&approvals).Error; err != nil {
		return nil, err
	}
	actorIDs := make([]int, 0, len(approvals))
	for index := range approvals {
		if validateRoutingPolicyRollbackApproval(approvals[index]) != nil {
			return nil, ErrRoutingPolicyApprovalInvalid
		}
		actorIDs = append(actorIDs, approvals[index].ActorID)
	}
	authorizedActors, err := routingPolicyDeployAuthorizedActorsDBContext(ctx, db, actorIDs)
	if err != nil {
		return nil, err
	}
	valid := make([]RoutingPolicyRollbackApproval, 0, len(approvals))
	actors := make(map[int]struct{}, len(approvals))
	for index := range approvals {
		approval := approvals[index]
		if approval.ExpectedHeadHash != head.CurrentHash || approval.TargetContentHash != target.ContentHash ||
			approval.ActivationHash != activationHash || approval.ActorID == activation.ActorID {
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

// DeleteStaleRoutingPolicyRollbackApprovalsContext removes only approvals that
// can no longer authorize the current monotonic policy head. Current-head
// approvals remain available regardless of age until that head changes.
func DeleteStaleRoutingPolicyRollbackApprovalsContext(
	ctx context.Context,
	cutoffTimeMs int64,
	limit int,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cutoffTimeMs <= 0 || limit < 1 || limit > routingPolicyRollbackApprovalRetentionBatch {
		return 0, ErrRoutingPolicyApprovalInvalid
	}
	head, err := GetRoutingPolicyHeadContext(ctx)
	if err != nil {
		return 0, err
	}
	ids := make([]int64, 0, limit)
	if err := DB.WithContext(ctx).Model(&RoutingPolicyRollbackApproval{}).
		Select("id").
		Where("created_time_ms < ?", cutoffTimeMs).
		Where(
			"expected_revision <> ? OR expected_activation_id <> ? OR expected_head_hash <> ?",
			head.CurrentRevision, head.CurrentActivationID, head.CurrentHash,
		).
		Order("id asc").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	deleted := DB.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).
		Where("id IN ?", ids).Delete(&RoutingPolicyRollbackApproval{})
	return deleted.RowsAffected, deleted.Error
}

func validateRoutingPolicyRollbackApproval(approval RoutingPolicyRollbackApproval) error {
	if approval.ID <= 0 || approval.ExpectedRevision <= 0 || approval.ExpectedActivationID <= 0 ||
		approval.TargetRevision <= 0 || approval.TargetRevision >= approval.ExpectedRevision || approval.ActorID <= 0 ||
		approval.CreatedTimeMs <= 0 || !validRoutingHash(approval.ExpectedHeadHash) ||
		!validRoutingHash(approval.TargetContentHash) ||
		!validRoutingPolicyApprovalActivation(approval.ActivationStage, approval.ActivationTrafficBasisPoints) ||
		!validRoutingHash(approval.ActivationReasonHash) || !validRoutingHash(approval.ActivationHash) {
		return ErrRoutingPolicyApprovalInvalid
	}
	return nil
}

func validRoutingPolicyRollbackApprovalHead(head RoutingPolicyHead) bool {
	return head.CurrentRevision > 0 && head.CurrentActivationID > 0 && validRoutingHash(head.CurrentHash)
}

func routingPolicyRollbackApprovalHeadMatches(actual RoutingPolicyHead, expected RoutingPolicyHead) bool {
	return actual.CurrentRevision == expected.CurrentRevision &&
		actual.CurrentActivationID == expected.CurrentActivationID && actual.CurrentHash == expected.CurrentHash
}
