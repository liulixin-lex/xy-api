package channelrouting

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingselector "github.com/QuantumNous/new-api/service/routing"
)

const (
	DecisionAlgorithmShadowV1   = "channel-routing-shadow-v1"
	DecisionAlgorithmShadowV2   = "channel-routing-shadow-v2"
	DecisionAlgorithmCanaryV1   = model.RoutingDecisionAlgorithmCanaryV1
	DecisionAlgorithmCanaryV2   = model.RoutingDecisionAlgorithmCanaryV2
	MaxShadowCandidates         = model.RoutingPolicyMaxMembersPerPool
	shadowReplaySchemaVersionV1 = 1
	shadowReplaySchemaVersionV2 = 2
	shadowReplaySchemaVersion   = shadowReplaySchemaVersionV1
)

var (
	ErrShadowReplayInvalid   = errors.New("invalid channel routing shadow replay")
	ErrShadowReplayHash      = errors.New("channel routing shadow replay hash mismatch")
	ErrShadowReplayAlgorithm = errors.New("unsupported channel routing shadow algorithm")
	ErrShadowReplayAudit     = errors.New("channel routing shadow audit does not match replay")
)

type PoolSelectorPolicy struct {
	WeightAvailability float64 `json:"weight_availability"`
	WeightLatency      float64 `json:"weight_latency"`
	WeightThroughput   float64 `json:"weight_throughput"`
	WeightCost         float64 `json:"weight_cost"`
	AvailabilityFloor  float64 `json:"availability_floor"`
	MinVolume          int     `json:"min_volume"`
	TopK               int     `json:"top_k"`
	MaxEjectedPct      int     `json:"max_ejected_pct"`
	HalfOpenProbes     int     `json:"half_open_probes"`
	SnapshotStaleSec   int     `json:"snapshot_stale_sec"`
	BalanceMarginUSD   float64 `json:"balance_margin_usd"`
}

type poolSelectorPolicyOverrides struct {
	WeightAvailability *float64 `json:"weight_availability"`
	WeightLatency      *float64 `json:"weight_latency"`
	WeightThroughput   *float64 `json:"weight_throughput"`
	WeightCost         *float64 `json:"weight_cost"`
	AvailabilityFloor  *float64 `json:"availability_floor"`
	MinVolume          *int     `json:"min_volume"`
	TopK               *int     `json:"top_k"`
	MaxEjectedPct      *int     `json:"max_ejected_pct"`
	HalfOpenProbes     *int     `json:"half_open_probes"`
	SnapshotStaleSec   *int     `json:"snapshot_stale_sec"`
	BalanceMarginUSD   *float64 `json:"balance_margin_usd"`
}

type ShadowRequest struct {
	RequestID               string
	RequestPath             string
	GroupName               string
	ModelName               string
	IsStream                bool
	RetryIndex              int
	PromptTokenEstimate     int
	CompletionTokenEstimate int
	CostProfile             *model.RoutingCostRequestProfile `json:"-"`
	Profile                 *RequestProfile                  `json:"-"`
}

type RequestProfile struct {
	SchemaVersion            int                   `json:"schema_version"`
	RequestPath              string                `json:"request_path"`
	GroupName                string                `json:"group_name"`
	ModelName                string                `json:"model_name"`
	IsStream                 bool                  `json:"is_stream"`
	RetryIndex               int                   `json:"retry_index"`
	PromptTokenEstimate      int                   `json:"prompt_token_estimate"`
	CompletionTokenEstimate  int                   `json:"completion_token_estimate"`
	RequestKind              RequestKind           `json:"request_kind,omitempty"`
	SourceFormat             RequestSourceFormat   `json:"source_format,omitempty"`
	InputModalities          RequestModalityMask   `json:"input_modalities,omitempty"`
	OutputModalities         RequestModalityMask   `json:"output_modalities,omitempty"`
	RequiredCapabilities     RequestCapabilityMask `json:"required_capabilities,omitempty"`
	InputTokens              *RequestQuantity      `json:"input_tokens,omitempty"`
	OutputTokens             *RequestQuantity      `json:"output_tokens,omitempty"`
	CachedTokens             *RequestQuantity      `json:"cached_tokens,omitempty"`
	ImageUnits               *RequestQuantity      `json:"image_units,omitempty"`
	AudioMillis              *RequestQuantity      `json:"audio_millis,omitempty"`
	VideoMillis              *RequestQuantity      `json:"video_millis,omitempty"`
	DeadlineUnixMs           int64                 `json:"deadline_unix_ms,omitempty"`
	NodeID                   string                `json:"node_id,omitempty"`
	Region                   string                `json:"region,omitempty"`
	RetrySafety              RequestRetrySafety    `json:"retry_safety,omitempty"`
	RetryAllowed             bool                  `json:"retry_allowed,omitempty"`
	CrossChannelRetryAllowed bool                  `json:"cross_channel_retry_allowed,omitempty"`
	HedgeAllowed             bool                  `json:"hedge_allowed,omitempty"`
	IdempotencyKeyPresent    bool                  `json:"idempotency_key_present,omitempty"`
	TenantTier               RequestTenantTier     `json:"tenant_tier,omitempty"`
	CostBudgetNanoUSD        int64                 `json:"cost_budget_nano_usd,omitempty"`
	TrafficClass             RequestTrafficClass   `json:"traffic_class,omitempty"`
	costProfile              *model.RoutingCostRequestProfile
	costAtUnix               int64
}

type ShadowSelectorSettings struct {
	WeightAvailability float64 `json:"weight_availability"`
	WeightLatency      float64 `json:"weight_latency"`
	WeightThroughput   float64 `json:"weight_throughput"`
	WeightCost         float64 `json:"weight_cost"`
	AvailabilityFloor  float64 `json:"availability_floor"`
	MinVolume          int     `json:"min_volume"`
	TopK               int     `json:"top_k"`
	MaxEjectedPct      int     `json:"max_ejected_pct"`
	HalfOpenProbes     int     `json:"half_open_probes"`
	SnapshotStaleSec   int     `json:"snapshot_stale_sec"`
	NowUnix            int64   `json:"now_unix"`
	NowUnixMilli       int64   `json:"now_unix_milli"`
	RandomSeed         int64   `json:"random_seed"`
	PreferTTFT         bool    `json:"prefer_ttft"`
}

type ShadowMetricInput struct {
	RequestCount            int64   `json:"request_count"`
	SuccessCount            int64   `json:"success_count"`
	ReliabilityRequestCount int64   `json:"reliability_request_count"`
	ReliabilityFailureCount int64   `json:"reliability_failure_count"`
	P95LatencyMs            float64 `json:"p95_latency_ms"`
	P95TTFTMs               float64 `json:"p95_ttft_ms"`
	OutputTokensPerSecond   float64 `json:"output_tokens_per_second"`
	Inflight                int64   `json:"inflight"`
}

type ShadowCostInput struct {
	Known                bool                       `json:"known"`
	Cost                 float64                    `json:"cost"`
	WorstCaseKnown       bool                       `json:"worst_case_known,omitempty"`
	WorstCaseCost        float64                    `json:"worst_case_cost,omitempty"`
	EffectiveKnown       bool                       `json:"effective_known,omitempty"`
	EffectiveCost        float64                    `json:"effective_cost,omitempty"`
	Currency             string                     `json:"currency,omitempty"`
	Unit                 string                     `json:"unit,omitempty"`
	PricingBasis         string                     `json:"pricing_basis,omitempty"`
	PricingHash          string                     `json:"pricing_hash,omitempty"`
	PricingVersion       string                     `json:"pricing_version,omitempty"`
	ObservedTime         int64                      `json:"observed_time,omitempty"`
	EffectiveTime        int64                      `json:"effective_time,omitempty"`
	ExpiresTime          int64                      `json:"expires_time,omitempty"`
	VersionConfidence    string                     `json:"version_confidence,omitempty"`
	Freshness            string                     `json:"freshness,omitempty"`
	SourceSyncStatus     string                     `json:"source_sync_status,omitempty"`
	AccountSourceType    string                     `json:"account_source_type,omitempty"`
	AccountKeyHash       string                     `json:"account_key_hash,omitempty"`
	ConfidenceScore      float64                    `json:"confidence_score,omitempty"`
	FreshnessScore       float64                    `json:"freshness_score,omitempty"`
	ExpectedBreakdown    model.RoutingCostBreakdown `json:"expected_breakdown,omitempty"`
	WorstSingleBreakdown model.RoutingCostBreakdown `json:"worst_single_breakdown,omitempty"`
	UpdatedUnix          int64                      `json:"updated_unix"`
}

// ShadowReplayCostInput is the stable selector input persisted for replay.
// Version metadata and cost breakdowns are audited separately and are not
// needed to reproduce a routing decision.
type ShadowReplayCostInput struct {
	Known       bool    `json:"known"`
	Cost        float64 `json:"cost"`
	UpdatedUnix int64   `json:"updated_unix"`
}

type ShadowBreakerInput struct {
	State             string `json:"state"`
	Reason            string `json:"reason"`
	CooldownUntilUnix int64  `json:"cooldown_until_unix"`
	HalfOpenInflight  int64  `json:"half_open_inflight"`
	UpdatedUnix       int64  `json:"updated_unix"`
}

type ShadowCapacityInput struct {
	SourceStatusCode       int   `json:"source_status_code"`
	CooldownUntilUnixMilli int64 `json:"cooldown_until_unix_milli"`
	UpdatedUnixMilli       int64 `json:"updated_unix_milli"`
}

type ShadowCandidateInput struct {
	PoolMemberID           int                    `json:"pool_member_id"`
	ChannelID              int                    `json:"channel_id"`
	CredentialID           int                    `json:"credential_id,omitempty"`
	Priority               int64                  `json:"priority"`
	Weight                 uint                   `json:"weight"`
	Metric                 *ShadowMetricInput     `json:"metric,omitempty"`
	Cost                   *ShadowReplayCostInput `json:"cost,omitempty"`
	Breaker                *ShadowBreakerInput    `json:"breaker,omitempty"`
	Capacity               *ShadowCapacityInput   `json:"capacity,omitempty"`
	SlowStartFactor        float64                `json:"slow_start_factor,omitempty"`
	RequestExclusionReason string                 `json:"request_exclusion_reason,omitempty"`
}

type ShadowReplayInput struct {
	SchemaVersion     int                    `json:"schema_version"`
	AlgorithmVersion  string                 `json:"algorithm_version"`
	PoolID            int                    `json:"pool_id"`
	PolicyRevision    uint64                 `json:"policy_revision"`
	RuntimeGeneration uint64                 `json:"runtime_generation"`
	PolicyHash        string                 `json:"policy_hash"`
	SnapshotHash      string                 `json:"snapshot_hash"`
	Profile           RequestProfile         `json:"profile"`
	Settings          ShadowSelectorSettings `json:"settings"`
	Candidates        []ShadowCandidateInput `json:"candidates"`
	costEstimates     map[int]ShadowCostInput
}

type ShadowReplayResult struct {
	SelectedChannelID    int                 `json:"selected_channel_id"`
	SelectedMemberID     int                 `json:"selected_member_id"`
	SelectedCredentialID int                 `json:"selected_credential_id"`
	SelectedCost         float64             `json:"selected_cost"`
	SelectedCostKnown    bool                `json:"selected_cost_known"`
	FilteredOpen         int                 `json:"filtered_open"`
	FilteredCapacity     int                 `json:"filtered_capacity"`
	BreakerBypassed      bool                `json:"breaker_bypassed"`
	Ranked               []DecisionCandidate `json:"ranked"`
	Candidates           []DecisionCandidate `json:"candidates"`
}

func NewRequestProfile(
	requestPath string,
	groupName string,
	modelName string,
	isStream bool,
	retryIndex int,
	promptTokenEstimate int,
	completionTokenEstimate int,
) (RequestProfile, error) {
	profile := RequestProfile{
		SchemaVersion:           RequestProfileSchemaV1,
		RequestPath:             strings.TrimSpace(requestPath),
		GroupName:               strings.TrimSpace(groupName),
		ModelName:               strings.TrimSpace(modelName),
		IsStream:                isStream,
		RetryIndex:              retryIndex,
		PromptTokenEstimate:     promptTokenEstimate,
		CompletionTokenEstimate: completionTokenEstimate,
	}
	if err := profile.Validate(); err != nil {
		return RequestProfile{}, err
	}
	return profile, nil
}

func (profile RequestProfile) Validate() error {
	return validateRequestProfile(profile)
}

func (profile RequestProfile) Hash() (string, error) {
	if err := profile.Validate(); err != nil {
		return "", err
	}
	encoded, err := common.Marshal(profile)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func DeriveDecisionSeed(requestID string, policyRevision uint64, retryIndex int) (int64, error) {
	if !validShadowText(requestID, 64) || strings.TrimSpace(requestID) == "" || policyRevision == 0 || retryIndex < 0 {
		return 0, ErrShadowReplayInvalid
	}
	payload := fmt.Sprintf("channel-routing-shadow-seed:v1\x00%s\x00%d\x00%d", requestID, policyRevision, retryIndex)
	sum := sha256.Sum256([]byte(payload))
	return int64(binary.BigEndian.Uint64(sum[:8])), nil
}

func DeriveShadowSeed(requestID string, policyRevision uint64, retryIndex int) (int64, error) {
	return DeriveDecisionSeed(requestID, policyRevision, retryIndex)
}

// CaptureShadowReplayRequest reads one immutable runtime snapshot and derives
// every replay field from that single generation. The boolean reports whether
// the pool is in Shadow, including cases where building the replay fails.
func CaptureShadowReplayRequest(request ShadowRequest) (ShadowReplayInput, bool, error) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || request.GroupName == "" {
		return ShadowReplayInput{}, false, nil
	}
	poolID, ok := snapshot.poolByGroup[request.GroupName]
	if !ok {
		return ShadowReplayInput{}, false, nil
	}
	var pool *PoolSnapshot
	for index := range snapshot.view.Pools {
		if snapshot.view.Pools[index].ID == poolID {
			pool = &snapshot.view.Pools[index]
			break
		}
	}
	if pool == nil || pool.DeploymentStage != model.RoutingDeploymentStageShadow {
		return ShadowReplayInput{}, false, nil
	}

	profile, err := resolveRequestProfile(
		request.Profile,
		request.RequestPath,
		request.GroupName,
		request.ModelName,
		request.IsStream,
		request.RetryIndex,
		request.PromptTokenEstimate,
		request.CompletionTokenEstimate,
	)
	if err != nil {
		return ShadowReplayInput{}, true, err
	}
	profile = attachRoutingCostProfile(profile, request.CostProfile, time.Now().Unix())
	seed, err := DeriveDecisionSeed(request.RequestID, snapshot.view.Revision, request.RetryIndex)
	if err != nil {
		return ShadowReplayInput{}, true, err
	}
	now := time.Now()
	settings := pool.SelectorPolicy.selectorSettings(now.Unix(), now.UnixMilli(), seed, request.IsStream)
	candidates := make([]ShadowCandidateInput, 0, len(pool.Members))
	costEstimates := make(map[int]ShadowCostInput, len(pool.Members))
	for memberIndex := range pool.Members {
		member := pool.Members[memberIndex]
		if member.PoolID != pool.ID || member.PhysicalStatus != common.ChannelStatusEnabled {
			continue
		}
		observation, exists := snapshot.modelByMemberModel[memberModelKey{memberID: member.ID, model: request.ModelName}]
		if !exists {
			continue
		}
		channel, channelKnown := snapshot.channelByID[member.ChannelID]
		if !channelKnown || channel.Status != common.ChannelStatusEnabled {
			continue
		}
		candidate, costEstimate, candidateErr := shadowCandidateFromSnapshot(*pool, member, observation, channel, profile, settings)
		if candidateErr != nil {
			return ShadowReplayInput{}, true, candidateErr
		}
		credentialID, credentialReason := snapshot.selectCredential(member, profile.ModelName, seed, nil, 0, now)
		candidate.CredentialID = credentialID
		if candidate.RequestExclusionReason == "" && credentialReason != "" {
			candidate.RequestExclusionReason = credentialReason
		}
		if candidate.RequestExclusionReason == "" && observation.upstreamAccountID > 0 {
			if _, blocked := UpstreamAccountRuntimeBlocked(observation.upstreamAccountID, now); blocked {
				candidate.RequestExclusionReason = ExclusionReasonUpstreamAccount
			}
		}
		if costEstimate != nil {
			costEstimates[member.ChannelID] = *costEstimate
		}
		candidates = append(candidates, candidate)
	}
	input, err := BuildShadowReplayInput(
		pool.ID,
		snapshot.view.Revision,
		snapshot.view.RuntimeGeneration,
		snapshot.view.PolicyHash,
		profile,
		settings,
		candidates,
	)
	if err == nil {
		input.costEstimates = costEstimates
	}
	return input, true, err
}

func resolvePoolSelectorPolicy(profile string, policyJSON []byte) (PoolSelectorPolicy, error) {
	policy := defaultPoolSelectorPolicy(profile)
	if policy == (PoolSelectorPolicy{}) {
		return PoolSelectorPolicy{}, ErrShadowReplayInvalid
	}
	if len(policyJSON) > 0 {
		var overrides poolSelectorPolicyOverrides
		if err := common.Unmarshal(policyJSON, &overrides); err != nil {
			return PoolSelectorPolicy{}, err
		}
		if overrides.WeightAvailability != nil {
			policy.WeightAvailability = *overrides.WeightAvailability
		}
		if overrides.WeightLatency != nil {
			policy.WeightLatency = *overrides.WeightLatency
		}
		if overrides.WeightThroughput != nil {
			policy.WeightThroughput = *overrides.WeightThroughput
		}
		if overrides.WeightCost != nil {
			policy.WeightCost = *overrides.WeightCost
		}
		if overrides.AvailabilityFloor != nil {
			policy.AvailabilityFloor = *overrides.AvailabilityFloor
		}
		if overrides.MinVolume != nil {
			policy.MinVolume = *overrides.MinVolume
		}
		if overrides.TopK != nil {
			policy.TopK = *overrides.TopK
		}
		if overrides.MaxEjectedPct != nil {
			policy.MaxEjectedPct = *overrides.MaxEjectedPct
		}
		if overrides.HalfOpenProbes != nil {
			policy.HalfOpenProbes = *overrides.HalfOpenProbes
		}
		if overrides.SnapshotStaleSec != nil {
			policy.SnapshotStaleSec = *overrides.SnapshotStaleSec
		}
		if overrides.BalanceMarginUSD != nil {
			policy.BalanceMarginUSD = *overrides.BalanceMarginUSD
		}
	}
	return normalizePoolSelectorPolicy(policy)
}

func defaultPoolSelectorPolicy(profile string) PoolSelectorPolicy {
	policy := PoolSelectorPolicy{
		WeightAvailability: 0.45,
		WeightLatency:      0.25,
		WeightThroughput:   0.10,
		WeightCost:         0.20,
		AvailabilityFloor:  0.95,
		MinVolume:          50,
		TopK:               3,
		MaxEjectedPct:      50,
		HalfOpenProbes:     1,
		SnapshotStaleSec:   1_800,
		BalanceMarginUSD:   1,
	}
	switch profile {
	case model.RoutingPolicyProfileBalanced, model.RoutingPolicyProfileCustom:
	case model.RoutingPolicyProfileReliabilityFirst:
		policy.WeightAvailability = 0.65
		policy.WeightLatency = 0.20
		policy.WeightThroughput = 0.10
		policy.WeightCost = 0.05
		policy.AvailabilityFloor = 0.98
	case model.RoutingPolicyProfileCostAware:
		policy.WeightAvailability = 0.30
		policy.WeightLatency = 0.15
		policy.WeightThroughput = 0.10
		policy.WeightCost = 0.45
		policy.AvailabilityFloor = 0.90
	case model.RoutingPolicyProfileEnterpriseSLO:
		policy.WeightAvailability = 0.55
		policy.WeightLatency = 0.30
		policy.WeightThroughput = 0.10
		policy.WeightCost = 0.05
		policy.AvailabilityFloor = 0.98
	default:
		return PoolSelectorPolicy{}
	}
	return policy
}

func normalizePoolSelectorPolicy(policy PoolSelectorPolicy) (PoolSelectorPolicy, error) {
	weights := []*float64{
		&policy.WeightAvailability,
		&policy.WeightLatency,
		&policy.WeightThroughput,
		&policy.WeightCost,
	}
	total := 0.0
	for _, weight := range weights {
		if !finiteShadowNumber(*weight) || *weight < 0 {
			return PoolSelectorPolicy{}, ErrShadowReplayInvalid
		}
		total += *weight
	}
	if !finiteShadowNumber(total) || total <= 0 || !finiteShadowNumber(policy.AvailabilityFloor) ||
		policy.AvailabilityFloor < 0 || policy.AvailabilityFloor > 1 || policy.MinVolume < 0 || policy.TopK < 1 ||
		policy.MaxEjectedPct < 0 || policy.MaxEjectedPct > 100 || policy.HalfOpenProbes < 1 ||
		policy.SnapshotStaleSec < 1 || !finiteShadowNumber(policy.BalanceMarginUSD) || policy.BalanceMarginUSD < 0 {
		return PoolSelectorPolicy{}, ErrShadowReplayInvalid
	}
	for _, weight := range weights {
		*weight /= total
	}
	return policy, nil
}

func (policy PoolSelectorPolicy) selectorSettings(nowUnix int64, nowUnixMilli int64, seed int64, preferTTFT bool) routingselector.Settings {
	return routingselector.Settings{
		WeightAvailability: policy.WeightAvailability,
		WeightLatency:      policy.WeightLatency,
		WeightThroughput:   policy.WeightThroughput,
		WeightCost:         policy.WeightCost,
		AvailabilityFloor:  policy.AvailabilityFloor,
		MinVolume:          policy.MinVolume,
		TopK:               policy.TopK,
		MaxEjectedPct:      policy.MaxEjectedPct,
		HalfOpenProbes:     policy.HalfOpenProbes,
		SnapshotStaleSec:   policy.SnapshotStaleSec,
		NowUnix:            nowUnix,
		NowUnixMilli:       nowUnixMilli,
		RandomSeed:         seed,
		PreferTTFT:         preferTTFT,
	}
}

func BuildShadowReplayInput(
	poolID int,
	policyRevision uint64,
	runtimeGeneration uint64,
	policyHash string,
	profile RequestProfile,
	settings routingselector.Settings,
	candidates []ShadowCandidateInput,
) (ShadowReplayInput, error) {
	algorithmVersion := DecisionAlgorithmShadowV1
	if profile.SchemaVersion == RequestProfileSchemaV2 {
		algorithmVersion = DecisionAlgorithmShadowV2
	}
	return buildDecisionReplayInput(
		algorithmVersion,
		poolID,
		policyRevision,
		runtimeGeneration,
		policyHash,
		profile,
		settings,
		candidates,
	)
}

func BuildCanaryReplayInput(
	poolID int,
	policyRevision uint64,
	runtimeGeneration uint64,
	policyHash string,
	profile RequestProfile,
	settings routingselector.Settings,
	candidates []ShadowCandidateInput,
) (ShadowReplayInput, error) {
	algorithmVersion := DecisionAlgorithmCanaryV1
	if profile.SchemaVersion == RequestProfileSchemaV2 {
		algorithmVersion = DecisionAlgorithmCanaryV2
	}
	return buildDecisionReplayInput(
		algorithmVersion,
		poolID,
		policyRevision,
		runtimeGeneration,
		policyHash,
		profile,
		settings,
		candidates,
	)
}

func buildDecisionReplayInput(
	algorithmVersion string,
	poolID int,
	policyRevision uint64,
	runtimeGeneration uint64,
	policyHash string,
	profile RequestProfile,
	settings routingselector.Settings,
	candidates []ShadowCandidateInput,
) (ShadowReplayInput, error) {
	profile = cloneRequestProfile(profile)
	schemaVersion := shadowReplaySchemaVersionV1
	if profile.SchemaVersion == RequestProfileSchemaV2 {
		schemaVersion = shadowReplaySchemaVersionV2
	}
	input := ShadowReplayInput{
		SchemaVersion:     schemaVersion,
		AlgorithmVersion:  algorithmVersion,
		PoolID:            poolID,
		PolicyRevision:    policyRevision,
		RuntimeGeneration: runtimeGeneration,
		PolicyHash:        policyHash,
		Profile:           profile,
		Settings:          shadowSettingsFromSelector(settings),
		Candidates:        append([]ShadowCandidateInput(nil), candidates...),
	}
	if err := input.validateWithoutHash(); err != nil {
		return ShadowReplayInput{}, err
	}
	hash, err := input.computeHash()
	if err != nil {
		return ShadowReplayInput{}, err
	}
	input.SnapshotHash = hash
	return input, nil
}

func shadowCandidateFromSnapshot(
	pool PoolSnapshot,
	member PoolMemberSnapshot,
	observation ModelSnapshot,
	channel ChannelSnapshot,
	profile RequestProfile,
	settings routingselector.Settings,
) (ShadowCandidateInput, *ShadowCostInput, error) {
	if member.PoolID != pool.ID || member.LegacyWeight < 0 {
		return ShadowCandidateInput{}, nil, ErrShadowReplayInvalid
	}
	candidate := ShadowCandidateInput{
		PoolMemberID: member.ID,
		ChannelID:    member.ChannelID,
		Priority:     member.LegacyPriority,
		Weight:       uint(member.LegacyWeight),
	}
	candidate.RequestExclusionReason = requestTrafficExclusionReason(profile, channel)
	if candidate.RequestExclusionReason == "" && pool.CapabilityRoutingEnabled {
		candidate.RequestExclusionReason = requestCapabilityExclusionReason(profile, observation)
	}
	if observation.MetricKnown || observation.Inflight > 0 {
		candidate.Metric = &ShadowMetricInput{
			RequestCount:            observation.RequestCount,
			SuccessCount:            observation.SuccessCount,
			ReliabilityRequestCount: observation.ReliabilityRequestCount,
			ReliabilityFailureCount: observation.ReliabilityFailureCount,
			OutputTokensPerSecond:   observation.OutputTokensPerSecond,
			Inflight:                observation.Inflight,
		}
		if observation.P95LatencyKnown {
			candidate.Metric.P95LatencyMs = observation.P95LatencyMs
		}
		if observation.P95TTFTKnown {
			candidate.Metric.P95TTFTMs = observation.P95TTFTMs
		}
	}
	cost, err := shadowExpectedCost(observation, profile)
	if err != nil {
		return ShadowCandidateInput{}, nil, err
	}
	if cost != nil {
		candidate.Cost = &ShadowReplayCostInput{Known: cost.Known, Cost: cost.Cost, UpdatedUnix: cost.UpdatedUnix}
	}
	if observation.BreakerKnown {
		candidate.Breaker = &ShadowBreakerInput{
			State:             observation.BreakerState,
			Reason:            observation.BreakerReason,
			CooldownUntilUnix: observation.BreakerCooldownUntil,
			HalfOpenInflight:  observation.BreakerHalfOpenInflight,
			UpdatedUnix:       observation.BreakerUpdatedUnix,
		}
	}
	if channel.AuthFailure && shadowMarkerFresh(channel.AuthFailureUpdatedAt, settings.NowUnix, settings.SnapshotStaleSec) {
		candidate.Breaker = &ShadowBreakerInput{
			State:       routingselector.BreakerStateOpen,
			Reason:      routingselector.BreakerReasonAuthFail,
			UpdatedUnix: channel.AuthFailureUpdatedAt,
		}
	} else if channel.BalanceKnown && channel.Balance < pool.SelectorPolicy.BalanceMarginUSD &&
		shadowMarkerFresh(channel.BalanceUpdatedAt, settings.NowUnix, settings.SnapshotStaleSec) {
		candidate.Breaker = &ShadowBreakerInput{
			State:       routingselector.BreakerStateOpen,
			Reason:      routingselector.BreakerReasonBalance,
			UpdatedUnix: channel.BalanceUpdatedAt,
		}
	}
	if observation.CapacityCooldownUntilMs > 0 || observation.CapacityStatusCode > 0 || observation.CapacityUpdatedUnixMilli > 0 {
		candidate.Capacity = &ShadowCapacityInput{
			SourceStatusCode:       observation.CapacityStatusCode,
			CooldownUntilUnixMilli: observation.CapacityCooldownUntilMs,
			UpdatedUnixMilli:       observation.CapacityUpdatedUnixMilli,
		}
	}
	return candidate, cost, nil
}

func shadowExpectedCost(observation ModelSnapshot, profile RequestProfile) (*ShadowCostInput, error) {
	if observation.CostPricing != nil {
		requestProfile := model.RoutingCostRequestProfile{
			PromptTokens:             int64(profile.PromptTokenEstimate),
			ExpectedCompletionTokens: int64(profile.CompletionTokenEstimate),
			MaximumCompletionTokens:  int64(profile.CompletionTokenEstimate),
			MaxAttempts:              1,
		}
		if profile.costProfile != nil {
			requestProfile = *profile.costProfile
		}
		atUnix := profile.costAtUnix
		if atUnix <= 0 {
			atUnix = time.Now().Unix()
		}
		version := model.RoutingCostSnapshotVersion{
			PricingHash:      observation.CostPricingHash,
			PricingVersion:   observation.CostPricingVersion,
			SourceType:       observation.CostAccountSourceType,
			ObservedTime:     observation.CostObservedTime,
			EffectiveTime:    observation.CostEffectiveTime,
			ExpiresTime:      observation.CostExpiresTime,
			Confidence:       observation.CostVersionConfidence,
			ConfidenceScore:  observation.CostConfidenceScore,
			Freshness:        observation.CostFreshness,
			FreshnessScore:   observation.CostFreshnessScore,
			SourceSyncStatus: observation.CostSourceSyncStatus,
			SourceSyncError:  observation.CostSourceSyncError,
		}
		estimate, err := model.EstimateRoutingCostSnapshot(version, *observation.CostPricing, requestProfile, atUnix)
		if err != nil {
			return nil, ErrShadowReplayInvalid
		}
		return &ShadowCostInput{
			Known:                estimate.ExpectedKnown,
			Cost:                 estimate.ExpectedCost,
			WorstCaseKnown:       estimate.WorstCaseKnown,
			WorstCaseCost:        estimate.WorstCaseCost,
			EffectiveKnown:       estimate.ExpectedEffectiveKnown,
			EffectiveCost:        estimate.ExpectedEffectiveCost,
			Currency:             estimate.Currency,
			Unit:                 estimate.Unit,
			PricingBasis:         observation.CostPricing.BillingMode,
			PricingHash:          observation.CostPricingHash,
			PricingVersion:       observation.CostPricingVersion,
			ObservedTime:         observation.CostObservedTime,
			EffectiveTime:        observation.CostEffectiveTime,
			ExpiresTime:          observation.CostExpiresTime,
			VersionConfidence:    observation.CostVersionConfidence,
			Freshness:            observation.CostFreshness,
			SourceSyncStatus:     observation.CostSourceSyncStatus,
			AccountSourceType:    observation.CostAccountSourceType,
			AccountKeyHash:       observation.CostAccountKeyHash,
			ConfidenceScore:      estimate.ConfidenceScore,
			FreshnessScore:       estimate.FreshnessScore,
			ExpectedBreakdown:    estimate.ExpectedBreakdown,
			WorstSingleBreakdown: estimate.WorstCaseSingleBreakdown,
			UpdatedUnix:          observation.CostObservedTime,
		}, nil
	}
	if !observation.CostKnown && observation.CostUpdatedUnix <= 0 {
		return nil, nil
	}
	cost := observation.Cost
	known := observation.CostKnown
	if known {
		groupRatio := positiveShadowCostOrDefault(observation.CostGroupRatio, 1)
		switch strings.ToLower(strings.TrimSpace(observation.CostBillingMode)) {
		case "per_request":
			if observation.CostModelPrice > 0 {
				cost = groupRatio * observation.CostModelPrice
			} else {
				known = false
			}
		case "token", "":
			if observation.CostBaseRatio <= 0 || profile.PromptTokenEstimate <= 0 {
				cost = groupRatio
			} else {
				completionRatio := positiveShadowCostOrDefault(observation.CostCompletionRatio, 1)
				cost = groupRatio *
					(float64(profile.PromptTokenEstimate)*observation.CostBaseRatio +
						float64(profile.CompletionTokenEstimate)*observation.CostBaseRatio*completionRatio) /
					common.QuotaPerUnit
			}
		default:
			if observation.CostGroupRatio > 0 {
				cost = observation.CostGroupRatio
			}
		}
	}
	if !finiteShadowNumber(cost) || cost < 0 {
		return nil, ErrShadowReplayInvalid
	}
	if !known {
		cost = 0
	}
	return &ShadowCostInput{
		Known: known, Cost: cost, PricingBasis: observation.CostBillingMode, UpdatedUnix: observation.CostUpdatedUnix,
	}, nil
}

func ShadowExpectedCostForChannel(input ShadowReplayInput, channelID int) (float64, bool) {
	cost, known := ShadowCostEstimateForChannel(input, channelID)
	if !known {
		return 0, false
	}
	return cost.Cost, true
}

func ShadowCostEstimateForChannel(input ShadowReplayInput, channelID int) (*ShadowCostInput, bool) {
	if channelID <= 0 {
		return nil, false
	}
	if estimate, exists := input.costEstimates[channelID]; exists {
		cost := estimate
		return &cost, shadowCostKnown(&cost, input.Settings)
	}
	for index := range input.Candidates {
		candidate := input.Candidates[index]
		if candidate.ChannelID == channelID && shadowReplayCostKnown(candidate.Cost, input.Settings) {
			return &ShadowCostInput{
				Known: candidate.Cost.Known, Cost: candidate.Cost.Cost, UpdatedUnix: candidate.Cost.UpdatedUnix,
			}, true
		}
	}
	return nil, false
}

func positiveShadowCostOrDefault(value float64, fallback float64) float64 {
	if value <= 0 || !finiteShadowNumber(value) {
		return fallback
	}
	return value
}

func shadowMarkerFresh(updatedUnix int64, nowUnix int64, staleSeconds int) bool {
	return updatedUnix <= 0 || nowUnix <= 0 || staleSeconds <= 0 || nowUnix-updatedUnix <= int64(staleSeconds)
}

func (input ShadowReplayInput) Validate() error {
	if err := input.validateWithoutHash(); err != nil {
		return err
	}
	if !validShadowHash(input.SnapshotHash) {
		return ErrShadowReplayInvalid
	}
	hash, err := input.computeHash()
	if err != nil {
		return err
	}
	if hash != input.SnapshotHash {
		return ErrShadowReplayHash
	}
	return nil
}

func RunShadowReplay(input ShadowReplayInput) (ShadowReplayResult, error) {
	if err := input.Validate(); err != nil {
		return ShadowReplayResult{}, err
	}
	if !supportedDecisionAlgorithm(input.AlgorithmVersion) {
		return ShadowReplayResult{}, ErrShadowReplayAlgorithm
	}

	candidates := make([]routingselector.Candidate, 0, len(input.Candidates))
	memberByChannel := make(map[int]int, len(input.Candidates))
	credentialByChannel := make(map[int]int, len(input.Candidates))
	costByChannel := make(map[int]ShadowCostInput, len(input.Candidates))
	for index := range input.Candidates {
		candidate := input.Candidates[index]
		memberByChannel[candidate.ChannelID] = candidate.PoolMemberID
		credentialByChannel[candidate.ChannelID] = candidate.CredentialID
		if candidate.Cost != nil {
			costByChannel[candidate.ChannelID] = ShadowCostInput{
				Known: candidate.Cost.Known, Cost: candidate.Cost.Cost, UpdatedUnix: candidate.Cost.UpdatedUnix,
			}
		}
		if candidate.RequestExclusionReason != "" {
			continue
		}
		priority := candidate.Priority
		weight := candidate.Weight
		selectorCandidate := routingselector.Candidate{
			Channel: &model.Channel{Id: candidate.ChannelID, Priority: &priority, Weight: &weight},
		}
		if input.AlgorithmVersion == DecisionAlgorithmCanaryV1 || input.AlgorithmVersion == DecisionAlgorithmCanaryV2 {
			selectorCandidate.ScoreMultiplier = candidate.SlowStartFactor
		}
		if candidate.Metric != nil {
			selectorCandidate.Metric = &routingselector.MetricSnapshot{
				RequestCount:            candidate.Metric.RequestCount,
				SuccessCount:            candidate.Metric.SuccessCount,
				ReliabilityRequestCount: candidate.Metric.ReliabilityRequestCount,
				ReliabilityFailureCount: candidate.Metric.ReliabilityFailureCount,
				P95LatencyMs:            candidate.Metric.P95LatencyMs,
				P95TTFTMs:               candidate.Metric.P95TTFTMs,
				TPS:                     candidate.Metric.OutputTokensPerSecond,
				Inflight:                candidate.Metric.Inflight,
			}
		}
		if candidate.Cost != nil {
			selectorCandidate.Cost = &routingselector.CostSnapshot{
				Known: candidate.Cost.Known, Cost: candidate.Cost.Cost, UpdatedUnix: candidate.Cost.UpdatedUnix,
			}
		}
		if candidate.Breaker != nil {
			selectorCandidate.Breaker = &routingselector.BreakerSnapshot{
				State: candidate.Breaker.State, Reason: candidate.Breaker.Reason,
				CooldownUntilUnix: candidate.Breaker.CooldownUntilUnix,
				HalfOpenInflight:  candidate.Breaker.HalfOpenInflight,
				UpdatedUnix:       candidate.Breaker.UpdatedUnix,
			}
		}
		if candidate.Capacity != nil {
			selectorCandidate.Capacity = &routingselector.CapacityCooldownSnapshot{
				SourceStatusCode:       candidate.Capacity.SourceStatusCode,
				CooldownUntilUnixMilli: candidate.Capacity.CooldownUntilUnixMilli,
				UpdatedUnixMilli:       candidate.Capacity.UpdatedUnixMilli,
			}
		}
		candidates = append(candidates, selectorCandidate)
	}

	decision := routingselector.SelectRankedFromCandidates(candidates, input.Settings.selectorSettings())
	result := ShadowReplayResult{
		FilteredOpen:     decision.FilteredOpen,
		FilteredCapacity: decision.FilteredCapacity,
		BreakerBypassed:  decision.BreakerBypassed,
		Ranked:           make([]DecisionCandidate, 0, len(decision.Ranked)),
		Candidates:       make([]DecisionCandidate, 0, len(input.Candidates)),
	}
	rankedChannels := make(map[int]struct{}, len(decision.Ranked))
	for _, ranked := range decision.Ranked {
		channelID := ranked.Channel.Id
		candidate := DecisionCandidate{
			PoolMemberID: memberByChannel[channelID],
			ChannelID:    channelID,
			Eligible:     true,
			Score:        ranked.Score,
			Availability: ranked.Availability,
			Latency:      ranked.Latency,
			Throughput:   ranked.Throughput,
			CostScore:    ranked.CostScore,
			CostKnown:    ranked.CostKnown,
			Degraded:     ranked.Degraded,
			Open:         ranked.Open,
			Inflight:     ranked.Inflight,
		}
		result.Ranked = append(result.Ranked, candidate)
		result.Candidates = append(result.Candidates, candidate)
		rankedChannels[channelID] = struct{}{}
	}
	for index := range input.Candidates {
		candidate := input.Candidates[index]
		if _, ranked := rankedChannels[candidate.ChannelID]; ranked {
			continue
		}
		result.Candidates = append(result.Candidates, DecisionCandidate{
			PoolMemberID:    candidate.PoolMemberID,
			ChannelID:       candidate.ChannelID,
			ExclusionReason: shadowExclusionReason(candidate, input.Settings),
		})
	}
	if decision.Selected != nil && decision.Selected.Channel != nil {
		result.SelectedChannelID = decision.Selected.Channel.Id
		result.SelectedMemberID = memberByChannel[result.SelectedChannelID]
		result.SelectedCredentialID = credentialByChannel[result.SelectedChannelID]
		if cost, ok := costByChannel[result.SelectedChannelID]; ok && shadowCostKnown(&cost, input.Settings) {
			result.SelectedCostKnown = true
			result.SelectedCost = cost.Cost
		}
	}
	return result, nil
}

func ClassifyShadowDifference(actualChannelID int, replay ShadowReplayResult) string {
	if actualChannelID <= 0 && replay.SelectedChannelID <= 0 {
		return "both_unavailable"
	}
	if actualChannelID <= 0 {
		return "legacy_unavailable"
	}
	if replay.SelectedChannelID <= 0 {
		return "shadow_unavailable"
	}
	if actualChannelID == replay.SelectedChannelID {
		return "match"
	}
	for _, candidate := range replay.Ranked {
		if candidate.ChannelID == actualChannelID {
			return "ranking_difference"
		}
	}
	for _, candidate := range replay.Candidates {
		if candidate.ChannelID == actualChannelID {
			return "eligibility_difference"
		}
	}
	return "legacy_outside_shadow_candidates"
}

func ReplayDecisionAudit(audit model.RoutingDecisionAudit) (ShadowReplayResult, error) {
	return ReplayDecisionAuditContext(context.Background(), audit)
}

func ReplayDecisionAuditContext(ctx context.Context, audit model.RoutingDecisionAudit) (ShadowReplayResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !audit.Replayable || audit.PoolID <= 0 || audit.SnapshotRevision <= 0 ||
		audit.RuntimeGeneration <= 0 || !supportedDecisionAlgorithm(audit.AlgorithmVersion) {
		return ShadowReplayResult{}, ErrShadowReplayAudit
	}
	replayInputJSON, err := model.LoadRoutingDecisionReplayInputContext(ctx, audit)
	if err != nil {
		if errors.Is(err, model.ErrRoutingDecisionReplayIntegrity) {
			return ShadowReplayResult{}, ErrShadowReplayHash
		}
		return ShadowReplayResult{}, ErrShadowReplayAudit
	}
	var input ShadowReplayInput
	if err := common.UnmarshalJsonStr(replayInputJSON, &input); err != nil {
		return ShadowReplayResult{}, ErrShadowReplayAudit
	}
	if err := input.Validate(); err != nil {
		return ShadowReplayResult{}, err
	}
	expectedSeed, err := DeriveDecisionSeed(audit.RequestID, uint64(audit.SnapshotRevision), audit.RetryIndex)
	if err != nil || input.PoolID != audit.PoolID || input.PolicyRevision != uint64(audit.SnapshotRevision) ||
		input.RuntimeGeneration != uint64(audit.RuntimeGeneration) || input.PolicyHash != audit.PolicyHash ||
		input.SnapshotHash != audit.SnapshotHash || input.AlgorithmVersion != audit.AlgorithmVersion ||
		input.Settings.RandomSeed != expectedSeed || input.Profile.GroupName != audit.GroupName ||
		input.Profile.ModelName != audit.ModelName || input.Profile.RetryIndex != audit.RetryIndex ||
		input.Profile.IsStream != audit.IsStream {
		return ShadowReplayResult{}, ErrShadowReplayAudit
	}
	profileHash, err := input.Profile.Hash()
	if err != nil || profileHash != audit.ProfileHash {
		return ShadowReplayResult{}, ErrShadowReplayAudit
	}
	result, err := RunShadowReplay(input)
	if err != nil {
		return ShadowReplayResult{}, err
	}
	actualCost, actualCostKnown := ShadowExpectedCostForChannel(input, audit.ActualChannelID)
	differenceType := ClassifyShadowDifference(audit.ActualChannelID, result)
	eligibleCount := 0
	for index := range result.Candidates {
		if result.Candidates[index].Eligible {
			eligibleCount++
		}
	}
	expectedCostDelta := 0.0
	if actualCostKnown && result.SelectedCostKnown {
		expectedCostDelta = result.SelectedCost - actualCost
	}
	if result.SelectedChannelID != audit.ObservedChannelID || result.FilteredOpen != audit.FilteredOpen ||
		result.FilteredCapacity != audit.FilteredCapacity || result.BreakerBypassed != audit.BreakerBypassed ||
		result.SelectedCostKnown != audit.ObservedCostKnown || result.SelectedCost != audit.ObservedExpectedCost ||
		actualCostKnown != audit.ActualCostKnown || actualCost != audit.ActualExpectedCost ||
		expectedCostDelta != audit.ExpectedCostDelta || differenceType != audit.DifferenceType ||
		audit.CandidateCount != len(result.Candidates) || audit.EligibleCount != eligibleCount ||
		audit.ObservedMatchesActual != (result.SelectedChannelID > 0 && result.SelectedChannelID == audit.ActualChannelID) {
		return ShadowReplayResult{}, ErrShadowReplayAudit
	}
	if audit.ExclusionSummaryJSON != "" {
		expectedSummary, summaryErr := marshalDecisionExclusionSummary(result.Candidates)
		if summaryErr != nil || string(expectedSummary) != audit.ExclusionSummaryJSON {
			return ShadowReplayResult{}, ErrShadowReplayAudit
		}
	}
	if (audit.AlgorithmVersion == DecisionAlgorithmCanaryV1 || audit.AlgorithmVersion == DecisionAlgorithmCanaryV2) &&
		!validCanaryDecisionAudit(audit, result) {
		return ShadowReplayResult{}, ErrShadowReplayAudit
	}
	var stored struct {
		Truncated  bool                `json:"truncated"`
		Candidates []DecisionCandidate `json:"candidates"`
	}
	expectedCandidates := result.Candidates
	expectedTruncated := len(expectedCandidates) > MaxDecisionCandidates
	if expectedTruncated {
		expectedCandidates = expectedCandidates[:MaxDecisionCandidates]
	}
	if err := common.UnmarshalJsonStr(audit.CandidatesJSON, &stored); err != nil ||
		stored.Truncated != expectedTruncated || !reflect.DeepEqual(stored.Candidates, expectedCandidates) {
		return ShadowReplayResult{}, ErrShadowReplayAudit
	}
	return result, nil
}

func validCanaryDecisionAudit(audit model.RoutingDecisionAudit, result ShadowReplayResult) bool {
	gate, err := EvaluateCanaryGate(
		audit.PoolID,
		audit.ActivationID,
		uint64(audit.SnapshotRevision),
		audit.RequestID,
		audit.TrafficBasisPoints,
	)
	if err != nil || audit.ActivationStage != model.RoutingDeploymentStageCanary ||
		audit.CanaryBucket != gate.Bucket || audit.RolloutKey != string(gate.RolloutKey) ||
		audit.Cohort != model.RoutingDecisionCohortCanary || !gate.InCanary || audit.ExclusionSummaryJSON == "" ||
		audit.ActualChannelID != result.SelectedChannelID || audit.ObservedChannelID != result.SelectedChannelID ||
		audit.SelectedMemberID != result.SelectedMemberID || audit.SelectedCredentialID != result.SelectedCredentialID {
		return false
	}
	if result.SelectedChannelID <= 0 {
		return audit.SelectedMemberID == 0 && audit.SelectedCredentialID == 0 &&
			audit.ReservationMode == "" && audit.ReservationRPM == 0 && audit.ReservationInputTPM == 0 &&
			audit.ReservationOutputTPM == 0 && audit.ReservationInflight == 0 && audit.ReservationLimitRPM == 0 &&
			audit.ReservationLimitInputTPM == 0 && audit.ReservationLimitOutputTPM == 0 && audit.ReservationLimitInflight == 0
	}
	admission := CapacityAdmission{
		Mode: CapacityMode(audit.ReservationMode),
		Key: CapacityKey{
			PolicyRevision: uint64(audit.SnapshotRevision), PoolID: audit.PoolID,
			MemberID: audit.SelectedMemberID, Model: audit.ModelName,
		},
		Demand: Demand{
			RPM: audit.ReservationRPM, InputTPM: audit.ReservationInputTPM,
			OutputTPM: audit.ReservationOutputTPM, Inflight: audit.ReservationInflight,
		},
		Limit: Limit{
			RPM: audit.ReservationLimitRPM, InputTPM: audit.ReservationLimitInputTPM,
			OutputTPM: audit.ReservationLimitOutputTPM, Inflight: audit.ReservationLimitInflight,
		},
	}
	return admission.Mode == CapacityModeLocalSoft && validCapacityKey(admission.Key) && validDemand(admission.Demand) &&
		validLimit(admission.Limit) && limitCoversDemand(admission.Limit, admission.Demand) &&
		!exceedsLimit(admission.Demand, admission.Limit)
}

func shadowExclusionReason(candidate ShadowCandidateInput, settings ShadowSelectorSettings) string {
	if candidate.RequestExclusionReason != "" {
		return candidate.RequestExclusionReason
	}
	if candidate.Capacity != nil && candidate.Capacity.CooldownUntilUnixMilli > settings.NowUnixMilli {
		return "capacity_cooldown"
	}
	if candidate.Breaker != nil && !shadowBreakerStale(candidate.Breaker, settings) {
		state := strings.ToLower(strings.TrimSpace(candidate.Breaker.State))
		reason := strings.ToLower(strings.TrimSpace(candidate.Breaker.Reason))
		if state == routingselector.BreakerReasonAuthFail || reason == routingselector.BreakerReasonAuthFail {
			return "credential_unavailable"
		}
		if state == routingselector.BreakerReasonBalance || reason == routingselector.BreakerReasonBalance {
			return "balance_unavailable"
		}
		if state == routingselector.BreakerStateOpen &&
			(candidate.Breaker.CooldownUntilUnix == 0 || candidate.Breaker.CooldownUntilUnix > settings.NowUnix) {
			return "reliability_breaker_open"
		}
		if state == routingselector.BreakerStateHalfOpen && candidate.Breaker.HalfOpenInflight >= int64(settings.HalfOpenProbes) {
			return "half_open_capacity"
		}
	}
	if candidate.Metric != nil && settings.AvailabilityFloor > 0 &&
		candidate.Metric.ReliabilityRequestCount >= int64(settings.MinVolume) &&
		candidate.Metric.ReliabilityRequestCount > 0 {
		availability := 1 - float64(min(candidate.Metric.ReliabilityFailureCount, candidate.Metric.ReliabilityRequestCount))/
			float64(candidate.Metric.ReliabilityRequestCount)
		if availability < settings.AvailabilityFloor {
			return "availability_floor"
		}
	}
	return "selector_filtered"
}

func shadowBreakerStale(breaker *ShadowBreakerInput, settings ShadowSelectorSettings) bool {
	return breaker != nil && breaker.UpdatedUnix > 0 && settings.NowUnix > 0 && settings.SnapshotStaleSec > 0 &&
		settings.NowUnix-breaker.UpdatedUnix > int64(settings.SnapshotStaleSec)
}

func shadowCostKnown(cost *ShadowCostInput, settings ShadowSelectorSettings) bool {
	if cost == nil || !cost.Known || !finiteShadowNumber(cost.Cost) || cost.Cost < 0 {
		return false
	}
	return !shadowCostStale(cost, settings)
}

func shadowCostStale(cost *ShadowCostInput, settings ShadowSelectorSettings) bool {
	return cost != nil && cost.UpdatedUnix > 0 && settings.NowUnix > 0 && settings.SnapshotStaleSec > 0 &&
		settings.NowUnix-cost.UpdatedUnix > int64(settings.SnapshotStaleSec)
}

func (input ShadowReplayInput) validateWithoutHash() error {
	if !validShadowReplayVersion(input.SchemaVersion, input.AlgorithmVersion, input.Profile.SchemaVersion) ||
		input.PoolID <= 0 || input.PolicyRevision == 0 || input.RuntimeGeneration == 0 || !validShadowHash(input.PolicyHash) ||
		len(input.Candidates) > MaxShadowCandidates || input.Profile.Validate() != nil ||
		input.Settings.NowUnix <= 0 || input.Settings.NowUnixMilli <= 0 || !validShadowSettings(input.Settings) {
		return ErrShadowReplayInvalid
	}
	seenMembers := make(map[int]struct{}, len(input.Candidates))
	seenChannels := make(map[int]struct{}, len(input.Candidates))
	for _, candidate := range input.Candidates {
		if candidate.PoolMemberID <= 0 || candidate.ChannelID <= 0 {
			return ErrShadowReplayInvalid
		}
		if _, exists := seenMembers[candidate.PoolMemberID]; exists {
			return ErrShadowReplayInvalid
		}
		if _, exists := seenChannels[candidate.ChannelID]; exists {
			return ErrShadowReplayInvalid
		}
		seenMembers[candidate.PoolMemberID] = struct{}{}
		seenChannels[candidate.ChannelID] = struct{}{}
		if !validShadowCandidate(candidate) {
			return ErrShadowReplayInvalid
		}
	}
	return nil
}

func validShadowSettings(settings ShadowSelectorSettings) bool {
	weights := []float64{
		settings.WeightAvailability,
		settings.WeightLatency,
		settings.WeightThroughput,
		settings.WeightCost,
	}
	total := 0.0
	for _, weight := range weights {
		if !finiteShadowNumber(weight) || weight < 0 {
			return false
		}
		total += weight
	}
	return finiteShadowNumber(total) && total > 0 && finiteShadowNumber(settings.AvailabilityFloor) &&
		settings.AvailabilityFloor >= 0 && settings.AvailabilityFloor <= 1 && settings.MinVolume >= 0 &&
		settings.TopK >= 1 && settings.MaxEjectedPct >= 0 && settings.MaxEjectedPct <= 100 &&
		settings.HalfOpenProbes >= 1 && settings.SnapshotStaleSec >= 1
}

func (input ShadowReplayInput) computeHash() (string, error) {
	input.SnapshotHash = ""
	encoded, err := common.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func validShadowCandidate(candidate ShadowCandidateInput) bool {
	if candidate.CredentialID < 0 || !finiteShadowNumber(candidate.SlowStartFactor) ||
		candidate.SlowStartFactor < 0 || candidate.SlowStartFactor > 1 ||
		!validShadowText(candidate.RequestExclusionReason, MaxDecisionReasonRunes) ||
		(candidate.RequestExclusionReason != "" && strings.TrimSpace(candidate.RequestExclusionReason) != candidate.RequestExclusionReason) {
		return false
	}
	if candidate.Metric != nil {
		values := []int64{
			candidate.Metric.RequestCount, candidate.Metric.SuccessCount,
			candidate.Metric.ReliabilityRequestCount, candidate.Metric.ReliabilityFailureCount,
			candidate.Metric.Inflight,
		}
		for _, value := range values {
			if value < 0 {
				return false
			}
		}
		if candidate.Metric.SuccessCount > candidate.Metric.RequestCount ||
			candidate.Metric.ReliabilityFailureCount > candidate.Metric.ReliabilityRequestCount ||
			!finiteShadowNumber(candidate.Metric.P95LatencyMs) || !finiteShadowNumber(candidate.Metric.P95TTFTMs) ||
			!finiteShadowNumber(candidate.Metric.OutputTokensPerSecond) {
			return false
		}
	}
	if !validShadowReplayCost(candidate.Cost) {
		return false
	}
	if candidate.Breaker != nil && (!validShadowText(candidate.Breaker.State, 32) ||
		!validShadowText(candidate.Breaker.Reason, 64) || candidate.Breaker.CooldownUntilUnix < 0 ||
		candidate.Breaker.HalfOpenInflight < 0 || candidate.Breaker.UpdatedUnix < 0) {
		return false
	}
	return candidate.Capacity == nil || (candidate.Capacity.SourceStatusCode >= 0 &&
		candidate.Capacity.CooldownUntilUnixMilli >= 0 && candidate.Capacity.UpdatedUnixMilli >= 0)
}

func validShadowReplayCost(cost *ShadowReplayCostInput) bool {
	return cost == nil || (finiteShadowNumber(cost.Cost) && cost.Cost >= 0 && cost.UpdatedUnix >= 0)
}

func shadowReplayCostKnown(cost *ShadowReplayCostInput, settings ShadowSelectorSettings) bool {
	return cost != nil && cost.Known && finiteShadowNumber(cost.Cost) && cost.Cost >= 0 &&
		(cost.UpdatedUnix <= 0 || settings.NowUnix <= 0 || settings.SnapshotStaleSec <= 0 ||
			settings.NowUnix-cost.UpdatedUnix <= int64(settings.SnapshotStaleSec))
}

func supportedDecisionAlgorithm(algorithmVersion string) bool {
	return algorithmVersion == DecisionAlgorithmShadowV1 || algorithmVersion == DecisionAlgorithmCanaryV1 ||
		algorithmVersion == DecisionAlgorithmShadowV2 || algorithmVersion == DecisionAlgorithmCanaryV2
}

func validShadowReplayVersion(schemaVersion int, algorithmVersion string, profileSchemaVersion int) bool {
	switch {
	case schemaVersion == shadowReplaySchemaVersionV1 && profileSchemaVersion == RequestProfileSchemaV1:
		return algorithmVersion == DecisionAlgorithmShadowV1 || algorithmVersion == DecisionAlgorithmCanaryV1
	case schemaVersion == shadowReplaySchemaVersionV2 && profileSchemaVersion == RequestProfileSchemaV2:
		return algorithmVersion == DecisionAlgorithmShadowV2 || algorithmVersion == DecisionAlgorithmCanaryV2
	default:
		return false
	}
}

func shadowSettingsFromSelector(settings routingselector.Settings) ShadowSelectorSettings {
	return ShadowSelectorSettings{
		WeightAvailability: settings.WeightAvailability,
		WeightLatency:      settings.WeightLatency,
		WeightThroughput:   settings.WeightThroughput,
		WeightCost:         settings.WeightCost,
		AvailabilityFloor:  settings.AvailabilityFloor,
		MinVolume:          settings.MinVolume,
		TopK:               settings.TopK,
		MaxEjectedPct:      settings.MaxEjectedPct,
		HalfOpenProbes:     settings.HalfOpenProbes,
		SnapshotStaleSec:   settings.SnapshotStaleSec,
		NowUnix:            settings.NowUnix,
		NowUnixMilli:       settings.NowUnixMilli,
		RandomSeed:         settings.RandomSeed,
		PreferTTFT:         settings.PreferTTFT,
	}
}

func (settings ShadowSelectorSettings) selectorSettings() routingselector.Settings {
	return routingselector.Settings{
		WeightAvailability: settings.WeightAvailability,
		WeightLatency:      settings.WeightLatency,
		WeightThroughput:   settings.WeightThroughput,
		WeightCost:         settings.WeightCost,
		AvailabilityFloor:  settings.AvailabilityFloor,
		MinVolume:          settings.MinVolume,
		TopK:               settings.TopK,
		MaxEjectedPct:      settings.MaxEjectedPct,
		HalfOpenProbes:     settings.HalfOpenProbes,
		SnapshotStaleSec:   settings.SnapshotStaleSec,
		NowUnix:            settings.NowUnix,
		NowUnixMilli:       settings.NowUnixMilli,
		RandomSeed:         settings.RandomSeed,
		PreferTTFT:         settings.PreferTTFT,
	}
}

func validShadowHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func validShadowText(value string, maxRunes int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes && len(value) <= maxRunes*4
}

func finiteShadowNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validRoutingCostBreakdown(breakdown model.RoutingCostBreakdown) bool {
	for _, value := range []float64{
		breakdown.Input, breakdown.Output, breakdown.CacheRead, breakdown.CacheWrite,
		breakdown.CacheWrite1h, breakdown.ImageInput, breakdown.ImageOutput, breakdown.ImageUnits,
		breakdown.AudioInput, breakdown.AudioOutput, breakdown.PerRequest, breakdown.Expression, breakdown.Total,
	} {
		if !finiteShadowNumber(value) || value < 0 {
			return false
		}
	}
	return true
}
