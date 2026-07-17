package channelrouting

import (
	"context"
	"database/sql"
	"errors"

	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

const (
	legacyRoutingPolicySyncReason                  = "legacy_topology_sync"
	legacyRoutingPolicyPreserveSyncReason          = "legacy_topology_sync_preserving_policy"
	legacyRoutingPolicyNormalizeActivationReason   = "legacy_topology_sync_normalizing_activation"
	legacyRoutingPolicyCanarySafetyDowngradeReason = "legacy_topology_sync_canary_safety_downgrade"
	legacyRoutingPolicyMaxPublishAttempts          = 3
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
			document := current
			if currentRevision.SchemaVersion != model.RoutingPolicySchemaVersion {
				document, err = buildLegacyRoutingPolicyDocumentContext(ctx, tx, current, preservePolicy)
				if err != nil {
					return err
				}
			}

			stage := head.CurrentStage
			if stage == "" {
				stage = model.RoutingDeploymentStageObserve
			}
			trafficBasisPoints := 0
			var currentActivation model.RoutingPolicyActivation
			if head.CurrentActivationID > 0 {
				if err := tx.WithContext(ctx).Where("id = ?", head.CurrentActivationID).First(&currentActivation).Error; err != nil {
					return err
				}
			}
			reason := legacyRoutingPolicySyncReason
			if preservePolicy {
				reason = legacyRoutingPolicyPreserveSyncReason
			}
			if stage == model.RoutingDeploymentStageCanary {
				if currentActivation.TrafficBasisPoints >= model.RoutingPolicyCanaryMinBasisPoints &&
					currentActivation.TrafficBasisPoints <= model.RoutingPolicyCanaryMaxBasisPoints {
					trafficBasisPoints = currentActivation.TrafficBasisPoints
				} else {
					stage = model.RoutingDeploymentStageShadow
					for index := range document.Pools {
						if document.Pools[index].DeploymentStage == model.RoutingDeploymentStageCanary {
							document.Pools[index].DeploymentStage = model.RoutingDeploymentStageShadow
						}
					}
					reason = legacyRoutingPolicyCanarySafetyDowngradeReason
				}
			} else if currentActivation.TrafficBasisPoints != 0 {
				reason = legacyRoutingPolicyNormalizeActivationReason
			}
			_, contentHash, err := model.NormalizeRoutingPolicyDocument(document)
			if err != nil {
				return err
			}
			activationCurrent := head.CurrentRevision > 0 && currentActivation.ID == head.CurrentActivationID &&
				currentActivation.Revision == head.CurrentRevision && currentActivation.Stage == stage &&
				currentActivation.TrafficBasisPoints == trafficBasisPoints
			if head.CurrentRevision > 0 && head.CurrentHash == contentHash && activationCurrent {
				synchronized = head
				return nil
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
	document, _, err := model.ConvertRoutingPolicyDocumentToV2DBContext(
		ctx, db, current, preservePolicy,
	)
	return document, err
}
