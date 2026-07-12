package channelrouting

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	DecisionAlgorithmObserveV1 = "smart-routing-beta-observe-v1"
	// Policy pools may be larger; immutable audit/replay payloads are
	// independently truncated to stay within the cross-database TEXT budget.
	MaxDecisionCandidates = 64
	// MySQL TEXT is limited to 64 KiB. Keep enough headroom for connection and
	// charset differences so a valid in-memory audit cannot become a poison
	// record that permanently blocks the ordered flush queue.
	MaxDecisionCandidatesJSON  = 60 << 10
	MaxDecisionReplayJSON      = 60 << 10
	MaxDecisionReasonRunes     = 128
	defaultDecisionBufferSize  = 4_096
	defaultDecisionBufferBytes = 32 << 20
	decisionBufferTTL          = 24 * time.Hour
)

var (
	ErrDecisionAuditTooLarge   = errors.New("channel routing decision audit exceeds limit")
	ErrDecisionIdentityUnknown = errors.New("channel routing decision identity is unknown")
)

type DecisionCandidate struct {
	PoolMemberID    int     `json:"pool_member_id"`
	ChannelID       int     `json:"channel_id"`
	Eligible        bool    `json:"eligible"`
	ExclusionReason string  `json:"exclusion_reason,omitempty"`
	Score           float64 `json:"score"`
	Availability    float64 `json:"availability"`
	Latency         float64 `json:"latency"`
	Throughput      float64 `json:"throughput"`
	CostScore       float64 `json:"cost_score"`
	CostKnown       bool    `json:"cost_known"`
	Degraded        bool    `json:"degraded"`
	Open            bool    `json:"open"`
	Inflight        int64   `json:"inflight"`
}

type DecisionInput struct {
	RequestID            string
	PoolID               int
	GroupName            string
	ModelName            string
	SnapshotRevision     uint64
	AlgorithmVersion     string
	RetryIndex           int
	IsStream             bool
	ActualChannelID      int
	ObservedChannelID    int
	FilteredOpen         int
	FilteredCapacity     int
	BreakerBypassed      bool
	Candidates           []DecisionCandidate
	CandidatesTruncated  bool
	ReplayInput          *ShadowReplayInput
	DifferenceType       string
	ActualCostKnown      bool
	ActualExpectedCost   float64
	ObservedCostKnown    bool
	ObservedExpectedCost float64
	Gate                 *CanaryGate
	SelectedIdentity     Identity
	CapacityAdmission    *CapacityAdmission
}

type decisionCanaryAuditFields struct {
	activationID         int64
	activationStage      string
	trafficBasisPoints   int
	canaryBucket         int
	rolloutKey           string
	cohort               string
	selectedMemberID     int
	selectedCredentialID int
	reservationMode      string
	reservationDemand    Demand
	reservationLimit     Limit
}

type DecisionBufferStats struct {
	Entries              int   `json:"entries"`
	Capacity             int   `json:"capacity"`
	Bytes                int64 `json:"buffered_bytes"`
	ByteCapacity         int64 `json:"byte_capacity"`
	Enqueued             int64 `json:"enqueued"`
	Dropped              int64 `json:"dropped"`
	ByteDrops            int64 `json:"byte_drops"`
	Expired              int64 `json:"expired"`
	UnknownIdentityDrops int64 `json:"unknown_identity_drops"`
	Flushed              int64 `json:"flushed"`
	OldestCreatedTime    int64 `json:"oldest_created_time"`
	OldestAgeSec         int64 `json:"oldest_age_sec"`
}

var decisionBuffer = newAuditBuffer(defaultDecisionBufferSize)

func EnqueueDecision(input DecisionInput) (string, error) {
	if input.GroupName == "" || input.ModelName == "" {
		return "", errors.New("channel routing decision identity is incomplete")
	}
	if !utf8.ValidString(input.GroupName) || !utf8.ValidString(input.ModelName) {
		return "", errors.New("channel routing decision identity is not valid UTF-8")
	}
	if !utf8.ValidString(input.RequestID) || !utf8.ValidString(input.AlgorithmVersion) {
		return "", errors.New("channel routing decision metadata is not valid UTF-8")
	}
	if len([]rune(input.GroupName)) > 64 || len([]rune(input.ModelName)) > 128 {
		return "", errors.New("channel routing decision identity exceeds limit")
	}
	if input.PoolID <= 0 || input.SnapshotRevision == 0 {
		decisionBuffer.markUnknownIdentityDrop()
		return "", ErrDecisionIdentityUnknown
	}
	algorithmVersion := input.AlgorithmVersion
	if algorithmVersion == "" {
		algorithmVersion = DecisionAlgorithmObserveV1
	}
	algorithmVersion = truncateDecisionText(algorithmVersion, 64)
	input.AlgorithmVersion = algorithmVersion
	input.Candidates = append([]DecisionCandidate(nil), input.Candidates...)
	originalCandidateCount := len(input.Candidates)
	originalEligibleCount := 0
	for index := range input.Candidates {
		if input.Candidates[index].PoolMemberID <= 0 {
			decisionBuffer.markUnknownIdentityDrop()
			return "", ErrDecisionIdentityUnknown
		}
		sanitizeDecisionCandidate(&input.Candidates[index])
		if input.Candidates[index].Eligible {
			originalEligibleCount++
		}
	}
	exclusionSummaryJSON, err := marshalDecisionExclusionSummary(input.Candidates)
	if err != nil {
		return "", err
	}
	if len(input.Candidates) > MaxDecisionCandidates {
		input.Candidates = input.Candidates[:MaxDecisionCandidates]
		input.CandidatesTruncated = true
	}
	candidatesJSON, err := common.Marshal(struct {
		Truncated  bool                `json:"truncated"`
		Candidates []DecisionCandidate `json:"candidates"`
	}{
		Truncated:  input.CandidatesTruncated,
		Candidates: input.Candidates,
	})
	if err != nil {
		return "", err
	}
	if len(candidatesJSON) > MaxDecisionCandidatesJSON {
		return "", ErrDecisionAuditTooLarge
	}

	decisionID := common.GetUUID()
	createdTime := common.GetTimestamp()
	requestProfileJSON := ""
	replayInputJSON := ""
	replayInputHash := ""
	replayInputBytes := 0
	replayChunks := []model.RoutingDecisionReplayChunk(nil)
	profileHash := ""
	policyHash := ""
	snapshotHash := ""
	runtimeGeneration := int64(0)
	seed := int64(0)
	replayable := input.ReplayInput != nil
	if replayable {
		if err := input.ReplayInput.Validate(); err != nil {
			return "", err
		}
		profile := input.ReplayInput.Profile
		expectedSeed, err := DeriveDecisionSeed(input.RequestID, input.SnapshotRevision, input.RetryIndex)
		if err != nil || input.PoolID != input.ReplayInput.PoolID || input.SnapshotRevision != input.ReplayInput.PolicyRevision ||
			input.AlgorithmVersion != input.ReplayInput.AlgorithmVersion || input.ReplayInput.Settings.RandomSeed != expectedSeed ||
			input.GroupName != profile.GroupName ||
			input.ModelName != profile.ModelName || input.RetryIndex != profile.RetryIndex || input.IsStream != profile.IsStream {
			return "", ErrShadowReplayInvalid
		}
		profileJSON, err := common.Marshal(profile)
		if err != nil {
			return "", err
		}
		replayJSON, err := common.Marshal(input.ReplayInput)
		if err != nil {
			return "", err
		}
		if len(profileJSON) > MaxDecisionReplayJSON {
			return "", ErrDecisionAuditTooLarge
		}
		replayInputHash, replayChunks, err = model.NewRoutingDecisionReplayChunks(decisionID, replayJSON, createdTime)
		if err != nil {
			return "", err
		}
		replayInputBytes = len(replayJSON)
		if len(replayJSON) <= MaxDecisionReplayJSON {
			replayInputJSON = string(replayJSON)
			replayChunks = nil
		} else {
			replayInputJSON = ""
		}
		requestProfileJSON = string(profileJSON)
		profileHash, err = profile.Hash()
		if err != nil {
			return "", err
		}
		policyHash = input.ReplayInput.PolicyHash
		snapshotHash = input.ReplayInput.SnapshotHash
		runtimeGeneration = snapshotRevisionInt64(input.ReplayInput.RuntimeGeneration)
		seed = input.ReplayInput.Settings.RandomSeed
	}
	canaryFields, err := decisionCanaryFieldsFromInput(input, replayable)
	if err != nil {
		return "", err
	}
	input.DifferenceType = truncateDecisionText(input.DifferenceType, 64)
	if !input.ActualCostKnown || !finiteDecisionNumber(input.ActualExpectedCost) || input.ActualExpectedCost < 0 {
		input.ActualCostKnown = false
		input.ActualExpectedCost = 0
	}
	if !input.ObservedCostKnown || !finiteDecisionNumber(input.ObservedExpectedCost) || input.ObservedExpectedCost < 0 {
		input.ObservedCostKnown = false
		input.ObservedExpectedCost = 0
	}
	expectedCostDelta := 0.0
	if input.ActualCostKnown && input.ObservedCostKnown {
		expectedCostDelta = input.ObservedExpectedCost - input.ActualExpectedCost
	}
	decisionBuffer.enqueue(model.RoutingDecisionAudit{
		DecisionID:                decisionID,
		RequestID:                 truncateDecisionText(input.RequestID, 64),
		PoolID:                    input.PoolID,
		GroupName:                 input.GroupName,
		ModelName:                 input.ModelName,
		SnapshotRevision:          snapshotRevisionInt64(input.SnapshotRevision),
		RuntimeGeneration:         runtimeGeneration,
		ActivationID:              canaryFields.activationID,
		ActivationStage:           canaryFields.activationStage,
		TrafficBasisPoints:        canaryFields.trafficBasisPoints,
		CanaryBucket:              canaryFields.canaryBucket,
		RolloutKey:                canaryFields.rolloutKey,
		Cohort:                    canaryFields.cohort,
		PolicyHash:                policyHash,
		SnapshotHash:              snapshotHash,
		ProfileHash:               profileHash,
		AlgorithmVersion:          algorithmVersion,
		Seed:                      seed,
		RetryIndex:                input.RetryIndex,
		IsStream:                  input.IsStream,
		ActualChannelID:           input.ActualChannelID,
		ObservedChannelID:         input.ObservedChannelID,
		SelectedMemberID:          canaryFields.selectedMemberID,
		SelectedCredentialID:      canaryFields.selectedCredentialID,
		ReservationMode:           canaryFields.reservationMode,
		ReservationRPM:            canaryFields.reservationDemand.RPM,
		ReservationInputTPM:       canaryFields.reservationDemand.InputTPM,
		ReservationOutputTPM:      canaryFields.reservationDemand.OutputTPM,
		ReservationInflight:       canaryFields.reservationDemand.Inflight,
		ReservationLimitRPM:       canaryFields.reservationLimit.RPM,
		ReservationLimitInputTPM:  canaryFields.reservationLimit.InputTPM,
		ReservationLimitOutputTPM: canaryFields.reservationLimit.OutputTPM,
		ReservationLimitInflight:  canaryFields.reservationLimit.Inflight,
		CandidateCount:            originalCandidateCount,
		EligibleCount:             originalEligibleCount,
		FilteredOpen:              input.FilteredOpen,
		FilteredCapacity:          input.FilteredCapacity,
		BreakerBypassed:           input.BreakerBypassed,
		ObservedMatchesActual:     input.ObservedChannelID > 0 && input.ObservedChannelID == input.ActualChannelID,
		DifferenceType:            input.DifferenceType,
		ActualCostKnown:           input.ActualCostKnown,
		ActualExpectedCost:        input.ActualExpectedCost,
		ObservedCostKnown:         input.ObservedCostKnown,
		ObservedExpectedCost:      input.ObservedExpectedCost,
		ExpectedCostDelta:         expectedCostDelta,
		Replayable:                replayable,
		RequestProfileJSON:        requestProfileJSON,
		ReplayInputJSON:           replayInputJSON,
		ReplayInputHash:           replayInputHash,
		ReplayInputBytes:          replayInputBytes,
		ReplayChunkCount:          len(replayChunks),
		ReplayChunks:              replayChunks,
		CandidatesJSON:            string(candidatesJSON),
		ExclusionSummaryJSON:      string(exclusionSummaryJSON),
		CreatedTime:               createdTime,
	})
	return decisionID, nil
}

func decisionCanaryFieldsFromInput(input DecisionInput, replayable bool) (decisionCanaryAuditFields, error) {
	if input.Gate == nil {
		if input.AlgorithmVersion == DecisionAlgorithmCanaryV1 || input.SelectedIdentity != (Identity{}) || input.CapacityAdmission != nil {
			return decisionCanaryAuditFields{}, ErrShadowReplayInvalid
		}
		return decisionCanaryAuditFields{}, nil
	}
	gate := *input.Gate
	expectedGate, err := EvaluateCanaryGate(
		input.PoolID,
		gate.ActivationID,
		input.SnapshotRevision,
		input.RequestID,
		gate.TrafficBasisPoints,
	)
	if err != nil || gate != expectedGate || input.AlgorithmVersion != DecisionAlgorithmCanaryV1 ||
		gate.PoolID != input.PoolID || gate.PolicyRevision != input.SnapshotRevision {
		return decisionCanaryAuditFields{}, ErrShadowReplayInvalid
	}
	fields := decisionCanaryAuditFields{
		activationID: gate.ActivationID, activationStage: model.RoutingDeploymentStageCanary,
		trafficBasisPoints: gate.TrafficBasisPoints, canaryBucket: gate.Bucket, rolloutKey: string(gate.RolloutKey),
		cohort: model.RoutingDecisionCohortControl,
	}
	if gate.InCanary {
		fields.cohort = model.RoutingDecisionCohortCanary
		if !replayable {
			return decisionCanaryAuditFields{}, ErrShadowReplayInvalid
		}
	} else if replayable || input.CapacityAdmission != nil {
		return decisionCanaryAuditFields{}, ErrShadowReplayInvalid
	}
	if input.ObservedChannelID <= 0 {
		if input.ActualChannelID != 0 || input.SelectedIdentity != (Identity{}) || input.CapacityAdmission != nil {
			return decisionCanaryAuditFields{}, ErrShadowReplayInvalid
		}
		return fields, nil
	}
	identity := input.SelectedIdentity
	if input.ActualChannelID != input.ObservedChannelID || identity.SnapshotRevision != input.SnapshotRevision ||
		identity.PoolID != input.PoolID || identity.MemberID <= 0 || identity.CredentialID < 0 {
		return decisionCanaryAuditFields{}, ErrShadowReplayInvalid
	}
	if gate.InCanary {
		matched := false
		for index := range input.ReplayInput.Candidates {
			candidate := input.ReplayInput.Candidates[index]
			if candidate.ChannelID == input.ObservedChannelID && candidate.PoolMemberID == identity.MemberID &&
				candidate.CredentialID == identity.CredentialID {
				matched = true
				break
			}
		}
		if !matched || input.CapacityAdmission == nil {
			return decisionCanaryAuditFields{}, ErrShadowReplayInvalid
		}
	}
	fields.selectedMemberID = identity.MemberID
	fields.selectedCredentialID = identity.CredentialID
	if input.CapacityAdmission == nil {
		return fields, nil
	}
	admission := *input.CapacityAdmission
	if admission.Mode != CapacityModeLocalSoft || admission.Key.PoolID != input.PoolID ||
		admission.Key.MemberID != identity.MemberID || admission.Key.Model != input.ModelName ||
		!validDemand(admission.Demand) || !validLimit(admission.Limit) ||
		!limitCoversDemand(admission.Limit, admission.Demand) || exceedsLimit(admission.Demand, admission.Limit) {
		return decisionCanaryAuditFields{}, ErrShadowReplayInvalid
	}
	fields.reservationMode = string(admission.Mode)
	fields.reservationDemand = admission.Demand
	fields.reservationLimit = admission.Limit
	return fields, nil
}

func marshalDecisionExclusionSummary(candidates []DecisionCandidate) ([]byte, error) {
	counts := make(map[string]int)
	excludedCount := 0
	for index := range candidates {
		candidate := candidates[index]
		if candidate.Eligible {
			continue
		}
		reason := candidate.ExclusionReason
		if reason == "" {
			reason = "unspecified"
		}
		counts[reason]++
		excludedCount++
	}
	if len(counts) > model.RoutingDecisionExclusionMaxReasons {
		return nil, ErrDecisionAuditTooLarge
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	summary := model.RoutingDecisionExclusionSummary{
		ExcludedCount: excludedCount,
		Reasons:       make([]model.RoutingDecisionExclusionCount, 0, len(reasons)),
	}
	for _, reason := range reasons {
		summary.Reasons = append(summary.Reasons, model.RoutingDecisionExclusionCount{Reason: reason, Count: counts[reason]})
	}
	encoded, err := common.Marshal(summary)
	if err != nil {
		return nil, err
	}
	if len(encoded) > model.RoutingDecisionExclusionMaxBytes {
		return nil, ErrDecisionAuditTooLarge
	}
	return encoded, nil
}

func FlushDecisionAuditsContext(ctx context.Context) (int, error) {
	batch := decisionBuffer.drain(model.RoutingDecisionAuditMaxBatch)
	if len(batch) == 0 {
		return 0, nil
	}
	if err := model.CreateRoutingDecisionAuditsContext(ctx, batch); err != nil {
		decisionBuffer.requeueFront(batch)
		return 0, err
	}
	decisionBuffer.markFlushed(len(batch))
	return len(batch), nil
}

func DeleteExpiredDecisionAuditsContext(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays < 1 {
		return 0, nil
	}
	maxRetentionDays := int((time.Duration(1<<63 - 1)) / (24 * time.Hour))
	if retentionDays > maxRetentionDays {
		retentionDays = maxRetentionDays
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()
	return model.DeleteRoutingDecisionAuditsBeforeContext(ctx, cutoff)
}

func DecisionAuditsStats() DecisionBufferStats {
	return decisionBuffer.stats()
}

func ResetDecisionAuditsForTest(capacity ...int) {
	size := defaultDecisionBufferSize
	if len(capacity) > 0 && capacity[0] > 0 {
		size = capacity[0]
	}
	decisionBuffer = newAuditBuffer(size)
}

func ResolveMemberIdentity(group string, channelID int) (Identity, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || channelID <= 0 || group == "" {
		return Identity{}, false
	}
	poolID, ok := snapshot.poolByGroup[group]
	if !ok {
		return Identity{}, false
	}
	memberID, ok := snapshot.memberByPoolChannel[poolChannelKey{PoolID: poolID, ChannelID: channelID}]
	if !ok {
		return Identity{}, false
	}
	return Identity{
		SnapshotRevision: snapshot.view.Revision,
		PoolID:           poolID,
		MemberID:         memberID,
	}, true
}

type DecisionIdentitySnapshot struct {
	SnapshotRevision uint64
	PoolID           int
	MemberIDs        map[int]int
}

func ResolveDecisionIdentities(group string, channelIDs []int) (DecisionIdentitySnapshot, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || group == "" {
		return DecisionIdentitySnapshot{}, false
	}
	poolID, ok := snapshot.poolByGroup[group]
	if !ok {
		return DecisionIdentitySnapshot{}, false
	}
	result := DecisionIdentitySnapshot{
		SnapshotRevision: snapshot.view.Revision,
		PoolID:           poolID,
		MemberIDs:        make(map[int]int, len(channelIDs)),
	}
	for _, channelID := range channelIDs {
		if channelID <= 0 {
			continue
		}
		if memberID, found := snapshot.memberByPoolChannel[poolChannelKey{PoolID: poolID, ChannelID: channelID}]; found {
			result.MemberIDs[channelID] = memberID
		}
	}
	return result, true
}

func sanitizeDecisionCandidate(candidate *DecisionCandidate) {
	if candidate == nil {
		return
	}
	if !finiteDecisionNumber(candidate.Score) {
		candidate.Score = 0
	}
	if !finiteDecisionNumber(candidate.Availability) {
		candidate.Availability = 0
	}
	if !finiteDecisionNumber(candidate.Latency) {
		candidate.Latency = 0
	}
	if !finiteDecisionNumber(candidate.Throughput) {
		candidate.Throughput = 0
	}
	if !finiteDecisionNumber(candidate.CostScore) {
		candidate.CostScore = 0
	}
	candidate.ExclusionReason = truncateDecisionText(candidate.ExclusionReason, MaxDecisionReasonRunes)
}

func finiteDecisionNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func snapshotRevisionInt64(revision uint64) int64 {
	if revision > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(revision)
}

func truncateDecisionText(value string, limit int) string {
	if limit < 1 {
		return ""
	}
	value = strings.ToValidUTF8(value, "\uFFFD")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

type auditBuffer struct {
	mu                   sync.Mutex
	items                []model.RoutingDecisionAudit
	capacity             int
	byteCapacity         int64
	bytes                int64
	head                 int
	size                 int
	enqueued             int64
	dropped              int64
	byteDrops            int64
	expired              int64
	unknownIdentityDrops int64
	flushed              int64
}

func newAuditBuffer(capacity int) *auditBuffer {
	return newAuditBufferWithLimits(capacity, defaultDecisionBufferBytes)
}

func newAuditBufferWithLimits(capacity int, byteCapacity int64) *auditBuffer {
	if capacity < 1 {
		capacity = 1
	}
	if byteCapacity < 1 {
		byteCapacity = 1
	}
	return &auditBuffer{capacity: capacity, byteCapacity: byteCapacity}
}

func (buffer *auditBuffer) enqueue(audit model.RoutingDecisionAudit) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.ensureAllocated()
	buffer.expireLocked(common.GetTimestamp() - int64(decisionBufferTTL/time.Second))
	buffer.enqueued++
	auditBytes := decisionAuditSize(audit)
	if auditBytes > buffer.byteCapacity {
		buffer.dropped++
		buffer.byteDrops++
		return
	}
	for buffer.size == buffer.capacity || buffer.bytes+auditBytes > buffer.byteCapacity {
		bytePressure := buffer.bytes+auditBytes > buffer.byteCapacity
		buffer.removeOldestLocked()
		buffer.dropped++
		if bytePressure {
			buffer.byteDrops++
		}
	}
	index := (buffer.head + buffer.size) % buffer.capacity
	buffer.items[index] = audit
	buffer.size++
	buffer.bytes += auditBytes
}

func (buffer *auditBuffer) drain(limit int) []model.RoutingDecisionAudit {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.expireLocked(common.GetTimestamp() - int64(decisionBufferTTL/time.Second))
	if limit < 1 || buffer.size == 0 {
		return nil
	}
	if limit > buffer.size {
		limit = buffer.size
	}
	batch := make([]model.RoutingDecisionAudit, limit)
	for index := 0; index < limit; index++ {
		itemIndex := (buffer.head + index) % buffer.capacity
		batch[index] = buffer.items[itemIndex]
		buffer.bytes -= decisionAuditSize(buffer.items[itemIndex])
		buffer.items[itemIndex] = model.RoutingDecisionAudit{}
	}
	buffer.head = (buffer.head + limit) % buffer.capacity
	buffer.size -= limit
	return batch
}

func (buffer *auditBuffer) requeueFront(batch []model.RoutingDecisionAudit) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	buffer.ensureAllocated()
	cutoff := common.GetTimestamp() - int64(decisionBufferTTL/time.Second)
	buffer.expireLocked(cutoff)
	for index := len(batch) - 1; index >= 0; index-- {
		item := batch[index]
		if item.CreatedTime < cutoff {
			buffer.expired++
			continue
		}
		itemBytes := decisionAuditSize(item)
		if itemBytes > buffer.byteCapacity {
			buffer.dropped++
			buffer.byteDrops++
			continue
		}
		for buffer.size == buffer.capacity || buffer.bytes+itemBytes > buffer.byteCapacity {
			tail := (buffer.head + buffer.size - 1) % buffer.capacity
			bytePressure := buffer.bytes+itemBytes > buffer.byteCapacity
			buffer.bytes -= decisionAuditSize(buffer.items[tail])
			buffer.items[tail] = model.RoutingDecisionAudit{}
			buffer.size--
			buffer.dropped++
			if bytePressure {
				buffer.byteDrops++
			}
		}
		buffer.head = (buffer.head - 1 + buffer.capacity) % buffer.capacity
		buffer.items[buffer.head] = item
		buffer.size++
		buffer.bytes += itemBytes
	}
}

func (buffer *auditBuffer) markFlushed(count int) {
	buffer.mu.Lock()
	buffer.flushed += int64(count)
	buffer.mu.Unlock()
}

func (buffer *auditBuffer) markUnknownIdentityDrop() {
	buffer.mu.Lock()
	buffer.dropped++
	buffer.unknownIdentityDrops++
	buffer.mu.Unlock()
}

func (buffer *auditBuffer) stats() DecisionBufferStats {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	now := common.GetTimestamp()
	buffer.expireLocked(now - int64(decisionBufferTTL/time.Second))
	stats := DecisionBufferStats{
		Entries:              buffer.size,
		Capacity:             buffer.capacity,
		Bytes:                buffer.bytes,
		ByteCapacity:         buffer.byteCapacity,
		Enqueued:             buffer.enqueued,
		Dropped:              buffer.dropped,
		ByteDrops:            buffer.byteDrops,
		Expired:              buffer.expired,
		UnknownIdentityDrops: buffer.unknownIdentityDrops,
		Flushed:              buffer.flushed,
	}
	if buffer.size > 0 {
		stats.OldestCreatedTime = buffer.items[buffer.head].CreatedTime
		stats.OldestAgeSec = max(int64(0), now-stats.OldestCreatedTime)
	}
	return stats
}

func (buffer *auditBuffer) ensureAllocated() {
	if buffer.items == nil {
		buffer.items = make([]model.RoutingDecisionAudit, buffer.capacity)
	}
}

func (buffer *auditBuffer) removeOldestLocked() {
	if buffer.size == 0 {
		return
	}
	buffer.bytes -= decisionAuditSize(buffer.items[buffer.head])
	buffer.items[buffer.head] = model.RoutingDecisionAudit{}
	buffer.head = (buffer.head + 1) % buffer.capacity
	buffer.size--
}

func (buffer *auditBuffer) expireLocked(cutoff int64) {
	for buffer.size > 0 {
		if buffer.items[buffer.head].CreatedTime >= cutoff {
			return
		}
		buffer.removeOldestLocked()
		buffer.expired++
	}
}

func decisionAuditSize(audit model.RoutingDecisionAudit) int64 {
	size := int64(len(audit.DecisionID) + len(audit.RequestID) + len(audit.GroupName) + len(audit.ModelName) +
		len(audit.AlgorithmVersion) + len(audit.PolicyHash) + len(audit.SnapshotHash) + len(audit.ProfileHash) +
		len(audit.DifferenceType) + len(audit.ActivationStage) + len(audit.RolloutKey) + len(audit.Cohort) +
		len(audit.ReservationMode) + len(audit.RequestProfileJSON) + len(audit.ReplayInputJSON) +
		len(audit.CandidatesJSON) + len(audit.ExclusionSummaryJSON) + 512)
	for index := range audit.ReplayChunks {
		size += int64(len(audit.ReplayChunks[index].Payload) + len(audit.ReplayChunks[index].PayloadHash) + 256)
	}
	return size
}
