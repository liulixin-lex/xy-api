package model

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

const (
	RoutingPolicySimulationRiskPass    = "pass"
	RoutingPolicySimulationRiskFail    = "fail"
	RoutingPolicySimulationRiskUnknown = "unknown"
)

var (
	ErrRoutingPolicySimulationEvidenceInvalid        = errors.New("invalid routing policy simulation evidence")
	ErrRoutingPolicySimulationRiskAcceptanceRequired = errors.New("routing policy simulation risk acceptance is required")
	ErrRoutingPolicySimulationRiskAcceptanceInvalid  = errors.New("invalid routing policy simulation risk acceptance")
)

type RoutingPolicySimulationEvidence struct {
	ID                       int64  `json:"id" gorm:"primaryKey"`
	OperationID              int64  `json:"operation_id" gorm:"uniqueIndex;not null"`
	DraftID                  int64  `json:"draft_id" gorm:"index;not null"`
	DraftVersion             int64  `json:"draft_version" gorm:"bigint;index;not null"`
	DraftETag                string `json:"draft_etag" gorm:"type:char(64);not null"`
	DocumentHash             string `json:"document_hash" gorm:"type:char(64);index;not null"`
	HeadRevision             int64  `json:"head_revision" gorm:"bigint;index;not null"`
	HeadHash                 string `json:"head_hash" gorm:"type:char(64);not null"`
	HeadActivationID         int64  `json:"head_activation_id" gorm:"bigint;index;not null"`
	TargetBound              bool   `json:"target_bound" gorm:"index;not null"`
	TargetStage              string `json:"target_stage,omitempty" gorm:"type:varchar(24);not null"`
	TargetTrafficBasisPoints int    `json:"target_traffic_basis_points" gorm:"not null"`
	RiskState                string `json:"risk_state" gorm:"type:varchar(16);index;not null"`
	RiskHash                 string `json:"risk_hash" gorm:"type:char(64);index;not null"`
	CreatedTimeMs            int64  `json:"created_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingPolicySimulationEvidence) TableName() string {
	return "routing_policy_simulation_evidence"
}

func (*RoutingPolicySimulationEvidence) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

func (*RoutingPolicySimulationEvidence) BeforeDelete(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

type RoutingPolicyRiskAcceptance struct {
	ID                       int64  `json:"id" gorm:"primaryKey"`
	EvidenceID               int64  `json:"evidence_id" gorm:"index;not null"`
	OperationID              int64  `json:"operation_id" gorm:"index;not null"`
	DraftID                  int64  `json:"draft_id" gorm:"index;not null"`
	DraftVersion             int64  `json:"draft_version" gorm:"bigint;not null"`
	DocumentHash             string `json:"document_hash" gorm:"type:char(64);not null"`
	TargetStage              string `json:"target_stage" gorm:"type:varchar(24);not null"`
	TargetTrafficBasisPoints int    `json:"target_traffic_basis_points" gorm:"not null"`
	PublishedRevision        int64  `json:"published_revision" gorm:"bigint;index;not null"`
	ActorID                  int    `json:"actor_id" gorm:"index;not null"`
	Reason                   string `json:"reason" gorm:"type:varchar(512);not null"`
	RiskHash                 string `json:"risk_hash" gorm:"type:char(64);not null"`
	CreatedTimeMs            int64  `json:"created_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingPolicyRiskAcceptance) TableName() string {
	return "routing_policy_risk_acceptances"
}

func (*RoutingPolicyRiskAcceptance) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

func (*RoutingPolicyRiskAcceptance) BeforeDelete(*gorm.DB) error {
	return ErrRoutingPolicyHistoryImmutable
}

type RoutingPolicySimulationEvidenceSpec struct {
	OperationID              int64
	Draft                    RoutingPolicyDraftSummary
	Head                     RoutingPolicyHead
	TargetBound              bool
	TargetStage              string
	TargetTrafficBasisPoints int
	RiskState                string
	RiskPayload              any
}

type RoutingPolicyRiskAcceptanceSpec struct {
	Accepted bool
	Reason   string
}

func CreateRoutingPolicySimulationEvidenceContext(
	ctx context.Context,
	spec RoutingPolicySimulationEvidenceSpec,
) (RoutingPolicySimulationEvidence, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	riskPayload, err := common.Marshal(spec.RiskPayload)
	if err != nil {
		return RoutingPolicySimulationEvidence{}, ErrRoutingPolicySimulationEvidenceInvalid
	}
	riskHash := routingPolicyHash(riskPayload)
	evidence := RoutingPolicySimulationEvidence{
		OperationID: spec.OperationID, DraftID: spec.Draft.ID, DraftVersion: spec.Draft.Version,
		DraftETag: spec.Draft.ETag, DocumentHash: spec.Draft.DocumentHash,
		HeadRevision: spec.Head.CurrentRevision, HeadHash: spec.Head.CurrentHash,
		HeadActivationID: spec.Head.CurrentActivationID, TargetBound: spec.TargetBound,
		TargetStage:              strings.TrimSpace(spec.TargetStage),
		TargetTrafficBasisPoints: spec.TargetTrafficBasisPoints,
		RiskState:                spec.RiskState, RiskHash: riskHash, CreatedTimeMs: time.Now().UnixMilli(),
	}
	if !validRoutingPolicySimulationEvidence(evidence) {
		return RoutingPolicySimulationEvidence{}, ErrRoutingPolicySimulationEvidenceInvalid
	}
	if err := DB.WithContext(ctx).Create(&evidence).Error; err != nil {
		return RoutingPolicySimulationEvidence{}, err
	}
	return evidence, nil
}

func GetRoutingPolicySimulationEvidenceByOperationContext(
	ctx context.Context,
	operationID int64,
) (RoutingPolicySimulationEvidence, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if operationID <= 0 {
		return RoutingPolicySimulationEvidence{}, ErrRoutingPolicySimulationEvidenceInvalid
	}
	var evidence RoutingPolicySimulationEvidence
	if err := DB.WithContext(ctx).Where("operation_id = ?", operationID).First(&evidence).Error; err != nil {
		return RoutingPolicySimulationEvidence{}, err
	}
	if !validRoutingPolicySimulationEvidence(evidence) {
		return RoutingPolicySimulationEvidence{}, ErrRoutingPolicySimulationEvidenceInvalid
	}
	return evidence, nil
}

func latestMatchingRoutingPolicySimulationEvidenceTx(
	tx *gorm.DB,
	draft RoutingPolicyDraft,
	head RoutingPolicyHead,
	activation RoutingPolicyActivationSpec,
) (RoutingPolicySimulationEvidence, bool, error) {
	if tx == nil || !tx.Migrator().HasTable(&RoutingPolicySimulationEvidence{}) {
		return RoutingPolicySimulationEvidence{}, false, nil
	}
	var evidence RoutingPolicySimulationEvidence
	err := tx.Where(
		"draft_id = ? AND draft_version = ? AND draft_e_tag = ? AND document_hash = ? AND head_revision = ? AND head_hash = ? AND head_activation_id = ?",
		draft.ID, draft.Version, draft.ETag, draft.DocumentHash,
		head.CurrentRevision, head.CurrentHash, head.CurrentActivationID,
	).Where(
		"target_bound = ? AND target_stage = ? AND target_traffic_basis_points = ?",
		true, activation.Stage, activation.TrafficBasisPoints,
	).Order("id desc").First(&evidence).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RoutingPolicySimulationEvidence{}, false, nil
	}
	if err != nil {
		return RoutingPolicySimulationEvidence{}, false, err
	}
	if !validRoutingPolicySimulationEvidence(evidence) {
		return RoutingPolicySimulationEvidence{}, false, ErrRoutingPolicySimulationEvidenceInvalid
	}
	return evidence, true, nil
}

func validateRoutingPolicyRiskAcceptance(spec RoutingPolicyRiskAcceptanceSpec) error {
	spec.Reason = strings.TrimSpace(spec.Reason)
	if !spec.Accepted {
		if spec.Reason != "" {
			return ErrRoutingPolicySimulationRiskAcceptanceInvalid
		}
		return nil
	}
	if spec.Reason == "" || !utf8.ValidString(spec.Reason) || utf8.RuneCountInString(spec.Reason) > routingPolicyReasonMaxRunes {
		return ErrRoutingPolicySimulationRiskAcceptanceInvalid
	}
	return nil
}

func validRoutingPolicySimulationEvidence(evidence RoutingPolicySimulationEvidence) bool {
	if evidence.ID < 0 || evidence.OperationID <= 0 || evidence.DraftID <= 0 || evidence.DraftVersion <= 0 ||
		!validRoutingHash(evidence.DraftETag) || !validRoutingHash(evidence.DocumentHash) ||
		evidence.HeadRevision < 0 || evidence.HeadActivationID < 0 || evidence.CreatedTimeMs <= 0 ||
		!validRoutingHash(evidence.RiskHash) {
		return false
	}
	if evidence.HeadRevision == 0 {
		if evidence.HeadHash != "" {
			return false
		}
	} else if !validRoutingHash(evidence.HeadHash) {
		return false
	}
	switch evidence.RiskState {
	case RoutingPolicySimulationRiskPass, RoutingPolicySimulationRiskFail, RoutingPolicySimulationRiskUnknown:
	default:
		return false
	}
	if evidence.TargetBound {
		if !validRoutingDeploymentStage(evidence.TargetStage) ||
			!validRoutingPolicySimulationTarget(evidence.TargetStage, evidence.TargetTrafficBasisPoints) {
			return false
		}
	} else if evidence.TargetStage != "" || evidence.TargetTrafficBasisPoints != 0 {
		return false
	}
	return true
}

func validRoutingPolicySimulationTarget(stage string, trafficBasisPoints int) bool {
	if stage == RoutingDeploymentStageCanary {
		return trafficBasisPoints >= RoutingPolicyCanaryMinBasisPoints &&
			trafficBasisPoints <= RoutingPolicyCanaryMaxBasisPoints
	}
	return trafficBasisPoints == 0
}
