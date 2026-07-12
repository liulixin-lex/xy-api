package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingCanaryEvaluationSchemaVersion     = 1
	routingCanaryEvaluationWindowUniqueIndex = "uidx_routing_canary_window_v2"

	RoutingCanaryEvaluationStatusPassed       RoutingCanaryEvaluationStatus = "passed"
	RoutingCanaryEvaluationStatusBreached     RoutingCanaryEvaluationStatus = "breached"
	RoutingCanaryEvaluationStatusInconclusive RoutingCanaryEvaluationStatus = "inconclusive"
	RoutingCanaryEvaluationStatusRolloutGrace RoutingCanaryEvaluationStatus = "rollout_grace"
	routingCanaryEvaluationReasonMaxRunes                                   = 512
)

var (
	ErrRoutingCanaryEvaluationInvalid        = errors.New("invalid routing canary evaluation")
	ErrRoutingCanaryEvaluationImmutable      = errors.New("routing canary evaluation is immutable")
	ErrRoutingCanaryEvaluationWindowConflict = errors.New("routing canary evaluation window has duplicate rows")
)

type RoutingCanaryEvaluationStatus string

type RoutingCanaryEvaluationWindowConflictError struct {
	RolloutKey    string
	PoolID        int
	WindowStartMs int64
	WindowEndMs   int64
	Count         int64
}

func (err *RoutingCanaryEvaluationWindowConflictError) Error() string {
	if err == nil {
		return ErrRoutingCanaryEvaluationWindowConflict.Error()
	}
	return fmt.Sprintf(
		"%s: rollout_key=%s pool_id=%d window_start_ms=%d window_end_ms=%d count=%d",
		ErrRoutingCanaryEvaluationWindowConflict,
		err.RolloutKey,
		err.PoolID,
		err.WindowStartMs,
		err.WindowEndMs,
		err.Count,
	)
}

func (err *RoutingCanaryEvaluationWindowConflictError) Unwrap() error {
	return ErrRoutingCanaryEvaluationWindowConflict
}

type RoutingCanaryCohortMetrics struct {
	RequestCount        int64   `json:"request_count"`
	SuccessCount        int64   `json:"success_count"`
	TTFTSampleCount     int64   `json:"ttft_sample_count"`
	P95TTFTMilliseconds float64 `json:"p95_ttft_milliseconds"`
	CostSampleCount     int64   `json:"cost_sample_count"`
	ExpectedCostTotal   float64 `json:"expected_cost_total"`
	AttemptCount        int64   `json:"attempt_count"`
	RetryCount          int64   `json:"retry_count"`
}

type RoutingCanaryEvaluationSpec struct {
	PolicyRevision                     int64                         `json:"policy_revision"`
	ActivationID                       int64                         `json:"activation_id"`
	PoolID                             int                           `json:"pool_id"`
	RolloutKey                         string                        `json:"rollout_key"`
	WindowStartMs                      int64                         `json:"window_start_ms"`
	WindowEndMs                        int64                         `json:"window_end_ms"`
	Control                            RoutingCanaryCohortMetrics    `json:"control"`
	Canary                             RoutingCanaryCohortMetrics    `json:"canary"`
	NodeCoverageBasisPoints            int                           `json:"node_coverage_basis_points"`
	CostCoverageBasisPoints            int                           `json:"cost_coverage_basis_points"`
	ControlSuccessRateBasisPoints      int                           `json:"control_success_rate_basis_points"`
	CanarySuccessRateBasisPoints       int                           `json:"canary_success_rate_basis_points"`
	SuccessRateDropBasisPoints         int                           `json:"success_rate_drop_basis_points"`
	P95TTFTRatioBasisPoints            int64                         `json:"p95_ttft_ratio_basis_points"`
	P95TTFTDeltaMilliseconds           float64                       `json:"p95_ttft_delta_milliseconds"`
	CostRatioBasisPoints               int64                         `json:"cost_ratio_basis_points"`
	RetryAmplificationRatioBasisPoints int64                         `json:"retry_amplification_ratio_basis_points"`
	TrafficBasisPoints                 int                           `json:"traffic_basis_points"`
	Status                             RoutingCanaryEvaluationStatus `json:"status"`
	Reason                             string                        `json:"reason"`
}

type RoutingCanaryEvaluation struct {
	ID                                 int64                         `json:"id" gorm:"primaryKey"`
	EvaluationHash                     string                        `json:"evaluation_hash" gorm:"type:char(64);uniqueIndex;not null"`
	CreateToken                        string                        `json:"-" gorm:"type:char(32);not null"`
	PolicyRevision                     int64                         `json:"policy_revision" gorm:"bigint;index;not null"`
	ActivationID                       int64                         `json:"activation_id" gorm:"bigint;index;not null"`
	PoolID                             int                           `json:"pool_id" gorm:"index;uniqueIndex:uidx_routing_canary_window_v2,priority:2;not null"`
	RolloutKey                         string                        `json:"rollout_key" gorm:"type:char(64);index;uniqueIndex:uidx_routing_canary_window_v2,priority:1;not null"`
	WindowStartMs                      int64                         `json:"window_start_ms" gorm:"bigint;index;uniqueIndex:uidx_routing_canary_window_v2,priority:3;not null"`
	WindowEndMs                        int64                         `json:"window_end_ms" gorm:"bigint;index;uniqueIndex:uidx_routing_canary_window_v2,priority:4;not null"`
	ControlRequestCount                int64                         `json:"control_request_count" gorm:"bigint;not null"`
	ControlSuccessCount                int64                         `json:"control_success_count" gorm:"bigint;not null"`
	ControlTTFTSampleCount             int64                         `json:"control_ttft_sample_count" gorm:"bigint;not null"`
	ControlP95TTFTMilliseconds         float64                       `json:"control_p95_ttft_milliseconds" gorm:"not null"`
	ControlCostSampleCount             int64                         `json:"control_cost_sample_count" gorm:"bigint;not null"`
	ControlExpectedCostTotal           float64                       `json:"control_expected_cost_total" gorm:"not null"`
	ControlAttemptCount                int64                         `json:"control_attempt_count" gorm:"bigint;not null"`
	ControlRetryCount                  int64                         `json:"control_retry_count" gorm:"bigint;not null"`
	CanaryRequestCount                 int64                         `json:"canary_request_count" gorm:"bigint;not null"`
	CanarySuccessCount                 int64                         `json:"canary_success_count" gorm:"bigint;not null"`
	CanaryTTFTSampleCount              int64                         `json:"canary_ttft_sample_count" gorm:"bigint;not null"`
	CanaryP95TTFTMilliseconds          float64                       `json:"canary_p95_ttft_milliseconds" gorm:"not null"`
	CanaryCostSampleCount              int64                         `json:"canary_cost_sample_count" gorm:"bigint;not null"`
	CanaryExpectedCostTotal            float64                       `json:"canary_expected_cost_total" gorm:"not null"`
	CanaryAttemptCount                 int64                         `json:"canary_attempt_count" gorm:"bigint;not null"`
	CanaryRetryCount                   int64                         `json:"canary_retry_count" gorm:"bigint;not null"`
	NodeCoverageBasisPoints            int                           `json:"node_coverage_basis_points" gorm:"not null"`
	CostCoverageBasisPoints            int                           `json:"cost_coverage_basis_points" gorm:"not null"`
	ControlSuccessRateBasisPoints      int                           `json:"control_success_rate_basis_points" gorm:"not null"`
	CanarySuccessRateBasisPoints       int                           `json:"canary_success_rate_basis_points" gorm:"not null"`
	SuccessRateDropBasisPoints         int                           `json:"success_rate_drop_basis_points" gorm:"not null"`
	P95TTFTRatioBasisPoints            int64                         `json:"p95_ttft_ratio_basis_points" gorm:"bigint;not null"`
	P95TTFTDeltaMilliseconds           float64                       `json:"p95_ttft_delta_milliseconds" gorm:"not null"`
	CostRatioBasisPoints               int64                         `json:"cost_ratio_basis_points" gorm:"bigint;not null"`
	RetryAmplificationRatioBasisPoints int64                         `json:"retry_amplification_ratio_basis_points" gorm:"bigint;not null"`
	TrafficBasisPoints                 int                           `json:"traffic_basis_points" gorm:"not null"`
	Status                             RoutingCanaryEvaluationStatus `json:"status" gorm:"type:varchar(24);index;not null"`
	Reason                             string                        `json:"reason" gorm:"type:varchar(512);not null"`
	CreatedTimeMs                      int64                         `json:"created_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingCanaryEvaluation) TableName() string {
	return "routing_canary_evaluations"
}

func (*RoutingCanaryEvaluation) BeforeUpdate(*gorm.DB) error {
	return ErrRoutingCanaryEvaluationImmutable
}

func (*RoutingCanaryEvaluation) BeforeDelete(*gorm.DB) error {
	return ErrRoutingCanaryEvaluationImmutable
}

func prepareRoutingCanaryEvaluationWindowUniqueIndex(db *gorm.DB) error {
	if db == nil {
		return ErrRoutingCanaryEvaluationInvalid
	}
	if !db.Migrator().HasTable(&RoutingCanaryEvaluation{}) ||
		db.Migrator().HasIndex(&RoutingCanaryEvaluation{}, routingCanaryEvaluationWindowUniqueIndex) {
		return nil
	}

	var conflict RoutingCanaryEvaluationWindowConflictError
	err := db.Model(&RoutingCanaryEvaluation{}).
		Select(
			"rollout_key, pool_id, window_start_ms, window_end_ms, COUNT(*) AS count",
		).
		Group("rollout_key, pool_id, window_start_ms, window_end_ms").
		Having("COUNT(*) > ?", 1).
		Order("rollout_key asc, pool_id asc, window_start_ms asc, window_end_ms asc").
		Limit(1).
		Scan(&conflict).Error
	if err != nil {
		return err
	}
	if conflict.Count > 1 {
		return &conflict
	}
	return nil
}

func CreateRoutingCanaryEvaluationContext(
	ctx context.Context,
	spec RoutingCanaryEvaluationSpec,
) (RoutingCanaryEvaluation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, evaluationHash, err := normalizeRoutingCanaryEvaluationSpec(spec)
	if err != nil {
		return RoutingCanaryEvaluation{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return RoutingCanaryEvaluation{}, false, err
	}

	createToken, err := newRoutingPersistenceToken()
	if err != nil {
		return RoutingCanaryEvaluation{}, false, err
	}
	evaluation := routingCanaryEvaluationFromSpec(normalized)
	evaluation.EvaluationHash = evaluationHash
	evaluation.CreateToken = createToken
	evaluation.CreatedTimeMs = time.Now().UnixMilli()
	created := DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "rollout_key"},
			{Name: "pool_id"},
			{Name: "window_start_ms"},
			{Name: "window_end_ms"},
		},
		DoNothing: true,
	}).Create(&evaluation)
	if created.Error != nil {
		return RoutingCanaryEvaluation{}, false, created.Error
	}

	var stored RoutingCanaryEvaluation
	if err := DB.WithContext(ctx).
		Where("rollout_key = ? AND pool_id = ? AND window_start_ms = ? AND window_end_ms = ?",
			normalized.RolloutKey, normalized.PoolID, normalized.WindowStartMs, normalized.WindowEndMs,
		).
		First(&stored).Error; err != nil {
		return RoutingCanaryEvaluation{}, false, err
	}
	if err := validateStoredRoutingCanaryEvaluation(stored); err != nil {
		return RoutingCanaryEvaluation{}, false, ErrRoutingCanaryEvaluationInvalid
	}
	return stored, stored.CreateToken == createToken, nil
}

func GetRoutingCanaryEvaluationWindowContext(
	ctx context.Context,
	rolloutKey string,
	poolID int,
	windowStartMs int64,
	windowEndMs int64,
) (RoutingCanaryEvaluation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rolloutKey = strings.ToLower(strings.TrimSpace(rolloutKey))
	if !validRoutingHash(rolloutKey) || poolID <= 0 || windowStartMs <= 0 || windowEndMs <= windowStartMs {
		return RoutingCanaryEvaluation{}, ErrRoutingCanaryEvaluationInvalid
	}
	var evaluation RoutingCanaryEvaluation
	err := DB.WithContext(ctx).
		Where("rollout_key = ? AND pool_id = ? AND window_start_ms = ? AND window_end_ms = ?",
			rolloutKey, poolID, windowStartMs, windowEndMs,
		).
		First(&evaluation).Error
	if err == nil {
		err = validateStoredRoutingCanaryEvaluation(evaluation)
	}
	return evaluation, err
}

func ListRoutingCanaryEvaluationsBeforeContext(
	ctx context.Context,
	rolloutKey string,
	poolID int,
	beforeWindowEndMs int64,
	limit int,
) ([]RoutingCanaryEvaluation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rolloutKey = strings.ToLower(strings.TrimSpace(rolloutKey))
	if !validRoutingHash(rolloutKey) || poolID <= 0 || beforeWindowEndMs <= 0 || limit <= 0 || limit > 10 {
		return nil, ErrRoutingCanaryEvaluationInvalid
	}
	evaluations := make([]RoutingCanaryEvaluation, 0, limit)
	err := DB.WithContext(ctx).
		Where("rollout_key = ? AND pool_id = ? AND window_end_ms < ?", rolloutKey, poolID, beforeWindowEndMs).
		Order("window_end_ms desc").
		Order("id desc").
		Limit(limit).
		Find(&evaluations).Error
	if err == nil {
		for index := range evaluations {
			if validateErr := validateStoredRoutingCanaryEvaluation(evaluations[index]); validateErr != nil {
				return nil, validateErr
			}
		}
	}
	return evaluations, err
}

func validateStoredRoutingCanaryEvaluation(evaluation RoutingCanaryEvaluation) error {
	if evaluation.ID <= 0 || evaluation.CreatedTimeMs <= 0 || !validRoutingPersistenceToken(evaluation.CreateToken) {
		return ErrRoutingCanaryEvaluationInvalid
	}
	_, storedHash, err := normalizeRoutingCanaryEvaluationSpec(evaluation.Spec())
	if err != nil || storedHash != evaluation.EvaluationHash {
		return ErrRoutingCanaryEvaluationInvalid
	}
	return nil
}

func (evaluation RoutingCanaryEvaluation) Spec() RoutingCanaryEvaluationSpec {
	return RoutingCanaryEvaluationSpec{
		PolicyRevision: evaluation.PolicyRevision,
		ActivationID:   evaluation.ActivationID,
		PoolID:         evaluation.PoolID,
		RolloutKey:     evaluation.RolloutKey,
		WindowStartMs:  evaluation.WindowStartMs,
		WindowEndMs:    evaluation.WindowEndMs,
		Control: RoutingCanaryCohortMetrics{
			RequestCount:        evaluation.ControlRequestCount,
			SuccessCount:        evaluation.ControlSuccessCount,
			TTFTSampleCount:     evaluation.ControlTTFTSampleCount,
			P95TTFTMilliseconds: evaluation.ControlP95TTFTMilliseconds,
			CostSampleCount:     evaluation.ControlCostSampleCount,
			ExpectedCostTotal:   evaluation.ControlExpectedCostTotal,
			AttemptCount:        evaluation.ControlAttemptCount,
			RetryCount:          evaluation.ControlRetryCount,
		},
		Canary: RoutingCanaryCohortMetrics{
			RequestCount:        evaluation.CanaryRequestCount,
			SuccessCount:        evaluation.CanarySuccessCount,
			TTFTSampleCount:     evaluation.CanaryTTFTSampleCount,
			P95TTFTMilliseconds: evaluation.CanaryP95TTFTMilliseconds,
			CostSampleCount:     evaluation.CanaryCostSampleCount,
			ExpectedCostTotal:   evaluation.CanaryExpectedCostTotal,
			AttemptCount:        evaluation.CanaryAttemptCount,
			RetryCount:          evaluation.CanaryRetryCount,
		},
		NodeCoverageBasisPoints:            evaluation.NodeCoverageBasisPoints,
		CostCoverageBasisPoints:            evaluation.CostCoverageBasisPoints,
		ControlSuccessRateBasisPoints:      evaluation.ControlSuccessRateBasisPoints,
		CanarySuccessRateBasisPoints:       evaluation.CanarySuccessRateBasisPoints,
		SuccessRateDropBasisPoints:         evaluation.SuccessRateDropBasisPoints,
		P95TTFTRatioBasisPoints:            evaluation.P95TTFTRatioBasisPoints,
		P95TTFTDeltaMilliseconds:           evaluation.P95TTFTDeltaMilliseconds,
		CostRatioBasisPoints:               evaluation.CostRatioBasisPoints,
		RetryAmplificationRatioBasisPoints: evaluation.RetryAmplificationRatioBasisPoints,
		TrafficBasisPoints:                 evaluation.TrafficBasisPoints,
		Status:                             evaluation.Status,
		Reason:                             evaluation.Reason,
	}
}

func normalizeRoutingCanaryEvaluationSpec(
	spec RoutingCanaryEvaluationSpec,
) (RoutingCanaryEvaluationSpec, string, error) {
	spec.RolloutKey = strings.ToLower(strings.TrimSpace(spec.RolloutKey))
	spec.Reason = strings.TrimSpace(spec.Reason)
	if spec.Control.P95TTFTMilliseconds == 0 {
		spec.Control.P95TTFTMilliseconds = 0
	}
	if spec.Control.ExpectedCostTotal == 0 {
		spec.Control.ExpectedCostTotal = 0
	}
	if spec.Canary.P95TTFTMilliseconds == 0 {
		spec.Canary.P95TTFTMilliseconds = 0
	}
	if spec.Canary.ExpectedCostTotal == 0 {
		spec.Canary.ExpectedCostTotal = 0
	}
	if spec.P95TTFTDeltaMilliseconds == 0 {
		spec.P95TTFTDeltaMilliseconds = 0
	}
	if spec.PolicyRevision <= 0 || spec.ActivationID <= 0 || spec.PoolID <= 0 ||
		spec.WindowStartMs <= 0 || spec.WindowEndMs <= spec.WindowStartMs ||
		!validRoutingHash(spec.RolloutKey) ||
		!validRoutingCanaryEvaluationReason(spec.Reason) ||
		!validRoutingCanaryCohortMetrics(spec.Control) || !validRoutingCanaryCohortMetrics(spec.Canary) ||
		!validRoutingBasisPoints(spec.NodeCoverageBasisPoints) ||
		!validRoutingBasisPoints(spec.CostCoverageBasisPoints) ||
		!validRoutingBasisPoints(spec.ControlSuccessRateBasisPoints) ||
		!validRoutingBasisPoints(spec.CanarySuccessRateBasisPoints) ||
		spec.ControlSuccessRateBasisPoints != routingCanarySuccessRateBasisPoints(spec.Control) ||
		spec.CanarySuccessRateBasisPoints != routingCanarySuccessRateBasisPoints(spec.Canary) ||
		spec.SuccessRateDropBasisPoints != spec.ControlSuccessRateBasisPoints-spec.CanarySuccessRateBasisPoints ||
		spec.SuccessRateDropBasisPoints < -10_000 || spec.SuccessRateDropBasisPoints > 10_000 ||
		spec.P95TTFTRatioBasisPoints < 0 || spec.CostRatioBasisPoints < 0 ||
		math.IsNaN(spec.P95TTFTDeltaMilliseconds) || math.IsInf(spec.P95TTFTDeltaMilliseconds, 0) ||
		spec.RetryAmplificationRatioBasisPoints < 0 ||
		spec.TrafficBasisPoints < RoutingPolicyCanaryMinBasisPoints ||
		spec.TrafficBasisPoints > RoutingPolicyCanaryMaxBasisPoints ||
		!validRoutingCanaryEvaluationStatus(spec.Status) {
		return RoutingCanaryEvaluationSpec{}, "", ErrRoutingCanaryEvaluationInvalid
	}
	canonical, err := common.Marshal(struct {
		SchemaVersion int `json:"schema_version"`
		RoutingCanaryEvaluationSpec
	}{
		SchemaVersion:               RoutingCanaryEvaluationSchemaVersion,
		RoutingCanaryEvaluationSpec: spec,
	})
	if err != nil {
		return RoutingCanaryEvaluationSpec{}, "", err
	}
	return spec, routingPolicyHash(canonical), nil
}

func routingCanaryEvaluationFromSpec(spec RoutingCanaryEvaluationSpec) RoutingCanaryEvaluation {
	return RoutingCanaryEvaluation{
		PolicyRevision:                     spec.PolicyRevision,
		ActivationID:                       spec.ActivationID,
		PoolID:                             spec.PoolID,
		RolloutKey:                         spec.RolloutKey,
		WindowStartMs:                      spec.WindowStartMs,
		WindowEndMs:                        spec.WindowEndMs,
		ControlRequestCount:                spec.Control.RequestCount,
		ControlSuccessCount:                spec.Control.SuccessCount,
		ControlTTFTSampleCount:             spec.Control.TTFTSampleCount,
		ControlP95TTFTMilliseconds:         spec.Control.P95TTFTMilliseconds,
		ControlCostSampleCount:             spec.Control.CostSampleCount,
		ControlExpectedCostTotal:           spec.Control.ExpectedCostTotal,
		ControlAttemptCount:                spec.Control.AttemptCount,
		ControlRetryCount:                  spec.Control.RetryCount,
		CanaryRequestCount:                 spec.Canary.RequestCount,
		CanarySuccessCount:                 spec.Canary.SuccessCount,
		CanaryTTFTSampleCount:              spec.Canary.TTFTSampleCount,
		CanaryP95TTFTMilliseconds:          spec.Canary.P95TTFTMilliseconds,
		CanaryCostSampleCount:              spec.Canary.CostSampleCount,
		CanaryExpectedCostTotal:            spec.Canary.ExpectedCostTotal,
		CanaryAttemptCount:                 spec.Canary.AttemptCount,
		CanaryRetryCount:                   spec.Canary.RetryCount,
		NodeCoverageBasisPoints:            spec.NodeCoverageBasisPoints,
		CostCoverageBasisPoints:            spec.CostCoverageBasisPoints,
		ControlSuccessRateBasisPoints:      spec.ControlSuccessRateBasisPoints,
		CanarySuccessRateBasisPoints:       spec.CanarySuccessRateBasisPoints,
		SuccessRateDropBasisPoints:         spec.SuccessRateDropBasisPoints,
		P95TTFTRatioBasisPoints:            spec.P95TTFTRatioBasisPoints,
		P95TTFTDeltaMilliseconds:           spec.P95TTFTDeltaMilliseconds,
		CostRatioBasisPoints:               spec.CostRatioBasisPoints,
		RetryAmplificationRatioBasisPoints: spec.RetryAmplificationRatioBasisPoints,
		TrafficBasisPoints:                 spec.TrafficBasisPoints,
		Status:                             spec.Status,
		Reason:                             spec.Reason,
	}
}

func validRoutingCanaryCohortMetrics(metrics RoutingCanaryCohortMetrics) bool {
	if metrics.RequestCount < 0 || metrics.SuccessCount < 0 || metrics.SuccessCount > metrics.RequestCount ||
		metrics.TTFTSampleCount < 0 || metrics.TTFTSampleCount > metrics.RequestCount ||
		math.IsNaN(metrics.P95TTFTMilliseconds) || math.IsInf(metrics.P95TTFTMilliseconds, 0) ||
		metrics.P95TTFTMilliseconds < 0 || metrics.CostSampleCount < 0 || metrics.CostSampleCount > metrics.RequestCount ||
		math.IsNaN(metrics.ExpectedCostTotal) || math.IsInf(metrics.ExpectedCostTotal, 0) ||
		metrics.ExpectedCostTotal < 0 || metrics.AttemptCount < 0 || metrics.RetryCount < 0 ||
		metrics.RetryCount > metrics.AttemptCount {
		return false
	}
	if metrics.TTFTSampleCount == 0 && metrics.P95TTFTMilliseconds != 0 {
		return false
	}
	if metrics.CostSampleCount == 0 && metrics.ExpectedCostTotal != 0 {
		return false
	}
	return true
}

func routingCanarySuccessRateBasisPoints(metrics RoutingCanaryCohortMetrics) int {
	if metrics.RequestCount == 0 {
		return 0
	}
	high, low := bits.Mul64(uint64(metrics.SuccessCount), 10_000)
	quotient, _ := bits.Div64(high, low, uint64(metrics.RequestCount))
	return int(quotient)
}

func validRoutingCanaryEvaluationStatus(status RoutingCanaryEvaluationStatus) bool {
	switch status {
	case RoutingCanaryEvaluationStatusPassed, RoutingCanaryEvaluationStatusBreached,
		RoutingCanaryEvaluationStatusInconclusive, RoutingCanaryEvaluationStatusRolloutGrace:
		return true
	default:
		return false
	}
}

func validRoutingBasisPoints(value int) bool {
	return value >= 0 && value <= 10_000
}

func validRoutingCanaryEvaluationReason(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= routingCanaryEvaluationReasonMaxRunes
}
