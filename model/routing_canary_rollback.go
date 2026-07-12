package model

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

var ErrRoutingCanaryAutoRollbackInvalid = errors.New("invalid routing canary auto rollback")

type RoutingCanaryAutoRollbackRequest struct {
	Operation            RoutingOperation    `json:"operation"`
	Lease                RoutingControlLease `json:"lease"`
	ExpectedRevision     int64               `json:"expected_revision"`
	ExpectedActivationID int64               `json:"expected_activation_id"`
	PoolID               int                 `json:"pool_id"`
	NowMs                int64               `json:"now_ms"`
}

type RoutingCanaryAutoRollbackResult struct {
	Operation  RoutingOperation           `json:"operation"`
	Publish    RoutingPolicyPublishResult `json:"publish"`
	Superseded bool                       `json:"superseded"`
}

func AutoRollbackRoutingCanaryPoolContext(
	ctx context.Context,
	request RoutingCanaryAutoRollbackRequest,
) (RoutingCanaryAutoRollbackResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.Operation.ID <= 0 || request.Operation.OperationType != RoutingOperationTypeCanaryAutoRollback ||
		request.Operation.Status != RoutingOperationStatusRunning || len(request.Operation.ClaimToken) != 32 ||
		!validRoutingHash(request.Operation.IdempotencyHash) || !validRoutingHash(request.Operation.EvaluationHash) ||
		request.ExpectedRevision <= 0 || request.ExpectedActivationID <= 0 || request.PoolID <= 0 || request.NowMs < 1_000 ||
		request.Operation.ExpectedRevision != request.ExpectedRevision ||
		request.Operation.ExpectedActivationID != request.ExpectedActivationID ||
		request.Operation.PoolID != request.PoolID ||
		!validRoutingControlLeaseText(request.Lease.LeaseName, 64) ||
		!validRoutingControlLeaseText(request.Lease.HolderID, 128) ||
		len(request.Lease.LeaseToken) != 32 || request.Lease.FencingToken <= 0 {
		return RoutingCanaryAutoRollbackResult{}, ErrRoutingCanaryAutoRollbackInvalid
	}
	if request.Lease.LeaseUntilMs <= request.NowMs {
		return RoutingCanaryAutoRollbackResult{}, ErrRoutingControlLeaseLost
	}
	if err := ctx.Err(); err != nil {
		return RoutingCanaryAutoRollbackResult{}, err
	}
	if DB == nil {
		return RoutingCanaryAutoRollbackResult{}, errRoutingPolicyDatabaseNil
	}

	var result RoutingCanaryAutoRollbackResult
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var currentLease RoutingControlLease
		if err := lockForUpdate(tx.WithContext(ctx)).
			Where("lease_name = ?", request.Lease.LeaseName).
			First(&currentLease).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoutingControlLeaseLost
			}
			return err
		}
		if currentLease.HolderID != request.Lease.HolderID ||
			currentLease.LeaseToken != request.Lease.LeaseToken ||
			currentLease.FencingToken != request.Lease.FencingToken ||
			currentLease.LeaseUntilMs != request.Lease.LeaseUntilMs ||
			currentLease.LeaseUntilMs <= request.NowMs {
			return ErrRoutingControlLeaseLost
		}

		var operation RoutingOperation
		if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", request.Operation.ID).First(&operation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoutingOperationClaimLost
			}
			return err
		}
		if operation.Status != RoutingOperationStatusRunning ||
			operation.ClaimToken != request.Operation.ClaimToken || operation.ClaimUntilMs <= request.NowMs {
			return ErrRoutingOperationClaimLost
		}
		if operation.OperationType != RoutingOperationTypeCanaryAutoRollback ||
			operation.IdempotencyHash != request.Operation.IdempotencyHash ||
			operation.EvaluationHash != request.Operation.EvaluationHash ||
			operation.PoolID != request.PoolID || operation.ExpectedRevision != request.ExpectedRevision ||
			operation.ExpectedActivationID != request.ExpectedActivationID ||
			operation.ActorID != request.Operation.ActorID || operation.Reason != request.Operation.Reason {
			return ErrRoutingCanaryAutoRollbackInvalid
		}
		var evaluation RoutingCanaryEvaluation
		if err := tx.WithContext(ctx).
			Where("evaluation_hash = ?", operation.EvaluationHash).
			First(&evaluation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoutingCanaryAutoRollbackInvalid
			}
			return err
		}
		if evaluation.PolicyRevision != request.ExpectedRevision ||
			evaluation.ActivationID != request.ExpectedActivationID || evaluation.PoolID != request.PoolID ||
			evaluation.Status != RoutingCanaryEvaluationStatusBreached {
			return ErrRoutingCanaryAutoRollbackInvalid
		}

		var head RoutingPolicyHead
		if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
			return err
		}
		if head.CurrentRevision != request.ExpectedRevision || head.CurrentActivationID != request.ExpectedActivationID {
			completed, err := finishRoutingOperationTx(
				ctx,
				tx,
				operation,
				request.NowMs,
				RoutingOperationStatusSuperseded,
				"routing policy head or activation changed",
				RoutingOperationResult{},
			)
			if err != nil {
				return err
			}
			result.Operation = completed
			result.Superseded = true
			return nil
		}

		document, revision, err := LoadRoutingPolicyRevisionDBContext(ctx, tx, request.ExpectedRevision)
		if err != nil {
			return err
		}
		if head.CurrentHash != revision.ContentHash || head.CurrentHash == "" {
			completed, err := finishRoutingOperationTx(
				ctx,
				tx,
				operation,
				request.NowMs,
				RoutingOperationStatusSuperseded,
				"routing policy head hash changed",
				RoutingOperationResult{},
			)
			if err != nil {
				return err
			}
			result.Operation = completed
			result.Superseded = true
			return nil
		}
		var activation RoutingPolicyActivation
		if err := lockForUpdate(tx.WithContext(ctx)).
			Where("id = ?", request.ExpectedActivationID).
			First(&activation).Error; err != nil {
			return err
		}
		if activation.Revision != request.ExpectedRevision || activation.Stage != head.CurrentStage ||
			activation.Stage != RoutingDeploymentStageCanary ||
			activation.TrafficBasisPoints < RoutingPolicyCanaryMinBasisPoints ||
			activation.TrafficBasisPoints > RoutingPolicyCanaryMaxBasisPoints {
			return ErrRoutingPolicyContentCorrupt
		}

		rollbackDocument := RoutingPolicyDocument{
			SchemaVersion: document.SchemaVersion,
			Pools:         make([]RoutingPolicyPoolContent, len(document.Pools)),
		}
		targetFound := false
		remainingCanary := false
		for poolIndex := range document.Pools {
			pool := document.Pools[poolIndex]
			copiedPool := pool
			copiedPool.Policy = append(pool.Policy[:0:0], pool.Policy...)
			copiedPool.Members = make([]RoutingPolicyMemberContent, len(pool.Members))
			for memberIndex := range pool.Members {
				member := pool.Members[memberIndex]
				copiedMember := member
				copiedMember.CredentialIDs = append([]int(nil), member.CredentialIDs...)
				copiedMember.Overrides = append(member.Overrides[:0:0], member.Overrides...)
				copiedPool.Members[memberIndex] = copiedMember
			}
			if copiedPool.PoolID == request.PoolID {
				if copiedPool.DeploymentStage != RoutingDeploymentStageCanary {
					return ErrRoutingCanaryAutoRollbackInvalid
				}
				copiedPool.DeploymentStage = RoutingDeploymentStageShadow
				targetFound = true
			} else if copiedPool.DeploymentStage == RoutingDeploymentStageCanary {
				remainingCanary = true
			}
			rollbackDocument.Pools[poolIndex] = copiedPool
		}
		if !targetFound {
			return ErrRoutingCanaryAutoRollbackInvalid
		}

		activationSpec := RoutingPolicyActivationSpec{
			Stage:              RoutingDeploymentStageShadow,
			TrafficBasisPoints: 0,
			ActorID:            operation.ActorID,
			Reason:             strings.TrimSpace(operation.Reason),
		}
		if remainingCanary {
			activationSpec.Stage = RoutingDeploymentStageCanary
			activationSpec.TrafficBasisPoints = activation.TrafficBasisPoints
		}
		if activationSpec.Reason == "" {
			activationSpec.Reason = "automatic canary rollback"
		}
		normalized, contentHash, err := normalizeRoutingPolicyDocument(rollbackDocument)
		if err != nil {
			return err
		}
		published, err := publishNormalizedRoutingPolicyRevisionTx(
			ctx,
			tx,
			request.ExpectedRevision,
			request.ExpectedRevision,
			normalized,
			contentHash,
			activationSpec,
			[]int{request.PoolID},
			request.NowMs/1_000,
		)
		if err != nil {
			return err
		}
		completed, err := finishRoutingOperationTx(
			ctx,
			tx,
			operation,
			request.NowMs,
			RoutingOperationStatusSucceeded,
			"",
			RoutingOperationResult{
				Revision: published.Revision.Revision, ActivationID: published.Activation.ID, OutboxID: published.Outbox.ID,
			},
		)
		if err != nil {
			return err
		}
		result.Operation = completed
		result.Publish = published
		return nil
	})
	if err != nil {
		return RoutingCanaryAutoRollbackResult{}, err
	}
	return result, nil
}
