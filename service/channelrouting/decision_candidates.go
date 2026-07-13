package channelrouting

import (
	"context"
	"errors"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const MaxDecisionCandidatePageSize = 100

var ErrDecisionCandidatePageInvalid = errors.New("invalid channel routing decision candidate page")

type DecisionCandidateDetail struct {
	DecisionCandidate
	Rank                    int      `json:"rank"`
	CredentialID            int      `json:"credential_id,omitempty"`
	BusinessTier            *int64   `json:"business_tier,omitempty"`
	TargetWeight            *float64 `json:"target_weight,omitempty"`
	Confidence              *float64 `json:"confidence,omitempty"`
	Freshness               *float64 `json:"freshness,omitempty"`
	SlowStartFactor         *float64 `json:"slow_start_factor,omitempty"`
	CapacityUtilization     *float64 `json:"capacity_utilization,omitempty"`
	QueueDelayMs            *float64 `json:"queue_delay_ms,omitempty"`
	MetricUpdatedUnix       *int64   `json:"metric_updated_unix,omitempty"`
	RequestCount            *int64   `json:"request_count,omitempty"`
	SuccessCount            *int64   `json:"success_count,omitempty"`
	ReliabilityRequestCount *int64   `json:"reliability_request_count,omitempty"`
	ReliabilityFailureCount *int64   `json:"reliability_failure_count,omitempty"`
	P95LatencyMs            *float64 `json:"p95_latency_ms,omitempty"`
	P95TTFTMs               *float64 `json:"p95_ttft_ms,omitempty"`
	OutputTokensPerSecond   *float64 `json:"output_tokens_per_second,omitempty"`
	ExplorationEligible     *bool    `json:"exploration_eligible,omitempty"`
}

type DecisionCandidatePage struct {
	DecisionID           string                    `json:"decision_id"`
	SnapshotRevision     int64                     `json:"snapshot_revision"`
	Items                []DecisionCandidateDetail `json:"items"`
	Total                int                       `json:"total"`
	Available            int                       `json:"available"`
	Cursor               int                       `json:"cursor"`
	NextCursor           int                       `json:"next_cursor"`
	Complete             bool                      `json:"complete"`
	Source               string                    `json:"source"`
	RequestCountKnown    bool                      `json:"request_count_known"`
	RequestCountCoverage float64                   `json:"request_count_coverage"`
	TotalRequestCount    int64                     `json:"total_request_count"`
	TruncationReason     string                    `json:"truncation_reason,omitempty"`
}

func ListDecisionCandidateDetailsContext(
	ctx context.Context,
	audit model.RoutingDecisionAudit,
	cursor int,
	limit int,
) (DecisionCandidatePage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if audit.DecisionID == "" || cursor < 0 || limit < 1 || limit > MaxDecisionCandidatePageSize {
		return DecisionCandidatePage{}, ErrDecisionCandidatePageInvalid
	}

	page := DecisionCandidatePage{
		DecisionID: audit.DecisionID, SnapshotRevision: audit.SnapshotRevision, Cursor: cursor,
		Items: []DecisionCandidateDetail{}, Total: audit.CandidateCount, Complete: true,
	}
	var details []DecisionCandidateDetail
	switch audit.AlgorithmVersion {
	case DecisionAlgorithmBalancedV1:
		result, err := ReplayBalancedDecisionAuditContext(ctx, audit)
		if err != nil {
			return DecisionCandidatePage{}, err
		}
		input, err := loadBalancedDecisionCandidateInputContext(ctx, audit)
		if err != nil {
			return DecisionCandidatePage{}, err
		}
		rawByChannel := make(map[int]BalancedReplayCandidate, len(input.Candidates))
		for index := range input.Candidates {
			rawByChannel[input.Candidates[index].ChannelID] = input.Candidates[index]
		}
		details = make([]DecisionCandidateDetail, 0, len(result.Candidates))
		for index := range result.Candidates {
			candidate := result.Candidates[index]
			detail := DecisionCandidateDetail{DecisionCandidate: candidate, Rank: index + 1}
			if raw, ok := rawByChannel[candidate.ChannelID]; ok {
				detail.CredentialID = raw.CredentialID
				detail.BusinessTier = int64Pointer(raw.BusinessTier)
				detail.TargetWeight = float64Pointer(raw.TargetWeight)
				detail.Confidence = float64Pointer(raw.Confidence)
				detail.Freshness = float64Pointer(raw.Freshness)
				detail.SlowStartFactor = float64Pointer(raw.SlowStartFactor)
				detail.CapacityUtilization = float64Pointer(raw.CapacityUtilization)
				detail.QueueDelayMs = float64Pointer(raw.QueueDelayMs)
				detail.MetricUpdatedUnix = int64Pointer(raw.MetricUpdatedUnix)
				detail.ExplorationEligible = boolPointer(raw.ExplorationEligible)
				attachDecisionCandidateMetric(&detail, raw.Metric)
			}
			details = append(details, detail)
		}
		page.Source = "balanced_replay"
	case DecisionAlgorithmShadowV1, DecisionAlgorithmCanaryV1:
		result, err := ReplayDecisionAuditContext(ctx, audit)
		if err != nil {
			return DecisionCandidatePage{}, err
		}
		input, err := loadShadowDecisionCandidateInputContext(ctx, audit)
		if err != nil {
			return DecisionCandidatePage{}, err
		}
		rawByChannel := make(map[int]ShadowCandidateInput, len(input.Candidates))
		for index := range input.Candidates {
			rawByChannel[input.Candidates[index].ChannelID] = input.Candidates[index]
		}
		details = make([]DecisionCandidateDetail, 0, len(result.Candidates))
		for index := range result.Candidates {
			candidate := result.Candidates[index]
			detail := DecisionCandidateDetail{DecisionCandidate: candidate, Rank: index + 1}
			if raw, ok := rawByChannel[candidate.ChannelID]; ok {
				detail.CredentialID = raw.CredentialID
				detail.BusinessTier = int64Pointer(raw.Priority)
				detail.TargetWeight = float64Pointer(float64(raw.Weight))
				detail.SlowStartFactor = float64Pointer(raw.SlowStartFactor)
				attachDecisionCandidateMetric(&detail, raw.Metric)
			}
			details = append(details, detail)
		}
		page.Source = "shadow_replay"
	default:
		var payload struct {
			Truncated  bool                `json:"truncated"`
			Candidates []DecisionCandidate `json:"candidates"`
		}
		if common.UnmarshalJsonStr(audit.CandidatesJSON, &payload) != nil {
			return DecisionCandidatePage{}, ErrDecisionCandidatePageInvalid
		}
		details = make([]DecisionCandidateDetail, len(payload.Candidates))
		for index := range payload.Candidates {
			details[index] = DecisionCandidateDetail{DecisionCandidate: payload.Candidates[index], Rank: index + 1}
		}
		page.Complete = !payload.Truncated
		page.Source = "stored_summary"
		if payload.Truncated {
			page.TruncationReason = "non_replayable_candidate_payload_limit"
		}
	}

	page.Available = len(details)
	if page.Total < page.Available {
		return DecisionCandidatePage{}, ErrDecisionCandidatePageInvalid
	}
	requestCountKnown := 0
	requestCountTotalValid := true
	for index := range details {
		if details[index].RequestCount == nil {
			continue
		}
		requestCountKnown++
		value := *details[index].RequestCount
		if value > 0 && page.TotalRequestCount > math.MaxInt64-value {
			requestCountTotalValid = false
			continue
		}
		page.TotalRequestCount += value
	}
	if len(details) > 0 {
		page.RequestCountCoverage = float64(requestCountKnown) / float64(len(details))
		page.RequestCountKnown = requestCountKnown == len(details) && requestCountTotalValid
		if !requestCountTotalValid {
			page.TotalRequestCount = 0
		}
	}
	start := min(cursor, len(details))
	end := min(start+limit, len(details))
	page.Items = append(page.Items, details[start:end]...)
	if end < len(details) {
		page.NextCursor = end
	}
	return page, nil
}

func loadBalancedDecisionCandidateInputContext(
	ctx context.Context,
	audit model.RoutingDecisionAudit,
) (BalancedReplayInput, error) {
	replayJSON, err := model.LoadRoutingDecisionReplayInputContext(ctx, audit)
	if err != nil {
		return BalancedReplayInput{}, err
	}
	var input BalancedReplayInput
	if common.UnmarshalJsonStr(replayJSON, &input) != nil || input.Validate() != nil ||
		input.PoolID != audit.PoolID || input.PolicyRevision != uint64(audit.SnapshotRevision) ||
		input.RuntimeGeneration != uint64(audit.RuntimeGeneration) || input.PolicyHash != audit.PolicyHash ||
		input.SnapshotHash != audit.SnapshotHash || input.AlgorithmVersion != audit.AlgorithmVersion {
		return BalancedReplayInput{}, ErrBalancedReplayInvalid
	}
	return input, nil
}

func loadShadowDecisionCandidateInputContext(
	ctx context.Context,
	audit model.RoutingDecisionAudit,
) (ShadowReplayInput, error) {
	replayJSON, err := model.LoadRoutingDecisionReplayInputContext(ctx, audit)
	if err != nil {
		return ShadowReplayInput{}, err
	}
	var input ShadowReplayInput
	if common.UnmarshalJsonStr(replayJSON, &input) != nil || input.Validate() != nil ||
		input.PoolID != audit.PoolID || input.PolicyRevision != uint64(audit.SnapshotRevision) ||
		input.RuntimeGeneration != uint64(audit.RuntimeGeneration) || input.PolicyHash != audit.PolicyHash ||
		input.SnapshotHash != audit.SnapshotHash || input.AlgorithmVersion != audit.AlgorithmVersion {
		return ShadowReplayInput{}, ErrShadowReplayAudit
	}
	return input, nil
}

func attachDecisionCandidateMetric(detail *DecisionCandidateDetail, metric *ShadowMetricInput) {
	if detail == nil || metric == nil {
		return
	}
	detail.RequestCount = int64Pointer(metric.RequestCount)
	detail.SuccessCount = int64Pointer(metric.SuccessCount)
	detail.ReliabilityRequestCount = int64Pointer(metric.ReliabilityRequestCount)
	detail.ReliabilityFailureCount = int64Pointer(metric.ReliabilityFailureCount)
	detail.P95LatencyMs = float64Pointer(metric.P95LatencyMs)
	detail.P95TTFTMs = float64Pointer(metric.P95TTFTMs)
	detail.OutputTokensPerSecond = float64Pointer(metric.OutputTokensPerSecond)
}

func int64Pointer(value int64) *int64 {
	return &value
}

func float64Pointer(value float64) *float64 {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}
