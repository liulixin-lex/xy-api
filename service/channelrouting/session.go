package channelrouting

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

var (
	ErrRoutingSessionInvalid       = errors.New("invalid channel routing request session")
	ErrRoutingSessionUnavailable   = errors.New("channel routing request snapshot is unavailable")
	ErrRoutingSessionGroupRequired = errors.New("channel routing request session requires a concrete group")
	ErrRoutingSessionPoolNotFound  = errors.New("channel routing request pool was not found")
)

const (
	ExclusionReasonRequestFailed         = "request_failed"
	ExclusionReasonChannelNotAllowed     = "channel_not_allowed"
	ExclusionReasonLocalCapacity         = "local_capacity_exhausted"
	ExclusionReasonHalfOpenProbe         = "half_open_probe_unavailable"
	ExclusionReasonMultiKeyUnsupported   = "multi_key_unsupported"
	ExclusionReasonCredentialUnavailable = "credential_unavailable"
	ExclusionReasonCredentialRequest     = "credential_request_excluded"
	ExclusionReasonEndpointRequest       = "endpoint_request_excluded"
	ExclusionReasonFailureDomainRequest  = "failure_domain_request_excluded"
	ExclusionReasonChannelBalance        = "channel_balance_unavailable"
	ExclusionReasonSlowStartUnavailable  = "slow_start_unavailable"
)

type RequestRoutingSession struct {
	requestID        string
	groupName        string
	poolIndex        int
	snapshot         *runtimeSnapshot
	planningTime     time.Time
	slowStartMu      sync.Mutex
	slowStartFactors map[SlowStartKey]float64
}

type RequestRoutingSessionSet struct {
	requestID    string
	snapshot     *runtimeSnapshot
	planningTime time.Time
	mu           sync.Mutex
	sessions     map[string]*RequestRoutingSession
}

type RequestEndpointIdentity struct {
	EndpointAuthority string
	Region            string
}

type RequestRoutingPlanInput struct {
	RequestPath             string
	ModelName               string
	IsStream                bool
	RetryIndex              int
	PromptTokenEstimate     int
	CompletionTokenEstimate int
	CostProfile             *model.RoutingCostRequestProfile `json:"-"`
	Profile                 *RequestProfile                  `json:"-"`
	// A nil allowed list means unrestricted. A non-nil empty list fails closed.
	AllowedChannelIDs           []int
	ExcludedChannelIDs          []int
	ExcludedCredentialIDs       []int
	ExcludedEndpointIdentities  []RequestEndpointIdentity
	ExcludedFailureDomainHashes []string
	RequiredCredentialID        int
	CapacityExcludedChannelIDs  []int
	ProbeExcludedChannelIDs     []int
	SlowStartFactor             func(SlowStartKey) (float64, error)
}

type RequestRoutingCostInput struct {
	RequestPath             string
	ModelName               string
	IsStream                bool
	RetryIndex              int
	PromptTokenEstimate     int
	CompletionTokenEstimate int
	CostProfile             *model.RoutingCostRequestProfile `json:"-"`
	Profile                 *RequestProfile                  `json:"-"`
}

type RequestRoutingPlan struct {
	Gate                      CanaryGate         `json:"gate"`
	Replay                    ShadowReplayInput  `json:"replay"`
	Result                    ShadowReplayResult `json:"result"`
	SelectedIdentity          Identity           `json:"selected_identity"`
	SelectedBreakerScope      string             `json:"selected_breaker_scope,omitempty"`
	SelectedEndpointAuthority string             `json:"selected_endpoint_authority,omitempty"`
	SelectedRegion            string             `json:"selected_region,omitempty"`
}

func NewRequestRoutingSession(requestID string, groupName string) (*RequestRoutingSession, error) {
	sessions, err := NewRequestRoutingSessionSet(requestID)
	if err != nil {
		return nil, err
	}
	return sessions.Session(groupName)
}

func NewRequestRoutingSessionSet(requestID string) (*RequestRoutingSessionSet, error) {
	if !validShadowText(requestID, 64) || strings.TrimSpace(requestID) == "" {
		return nil, ErrRoutingSessionInvalid
	}
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return nil, ErrRoutingSessionUnavailable
	}
	return &RequestRoutingSessionSet{
		requestID:    requestID,
		snapshot:     snapshot,
		planningTime: time.Now(),
		sessions:     make(map[string]*RequestRoutingSession),
	}, nil
}

func (sessions *RequestRoutingSessionSet) Session(groupName string) (*RequestRoutingSession, error) {
	if sessions == nil || sessions.snapshot == nil || !validShadowText(sessions.requestID, 64) {
		return nil, ErrRoutingSessionInvalid
	}
	groupName = strings.TrimSpace(groupName)
	if groupName == "" || strings.EqualFold(groupName, "auto") || !validShadowText(groupName, 64) {
		return nil, ErrRoutingSessionGroupRequired
	}
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if existing := sessions.sessions[groupName]; existing != nil {
		return existing, nil
	}
	snapshot := sessions.snapshot
	poolID, ok := snapshot.poolByGroup[groupName]
	if !ok {
		return nil, ErrRoutingSessionPoolNotFound
	}
	poolIndex, ok := snapshot.poolIndexByID[poolID]
	if !ok || poolIndex < 0 || poolIndex >= len(snapshot.view.Pools) || snapshot.view.Pools[poolIndex].ID != poolID {
		return nil, ErrRoutingSessionInvalid
	}
	session := &RequestRoutingSession{
		requestID:        sessions.requestID,
		groupName:        groupName,
		poolIndex:        poolIndex,
		snapshot:         snapshot,
		planningTime:     sessions.planningTime,
		slowStartFactors: make(map[SlowStartKey]float64),
	}
	sessions.sessions[groupName] = session
	return session, nil
}

func (session *RequestRoutingSession) SnapshotRevision() uint64 {
	if session == nil || session.snapshot == nil {
		return 0
	}
	return session.snapshot.view.Revision
}

func (session *RequestRoutingSession) RuntimeGeneration() uint64 {
	if session == nil || session.snapshot == nil {
		return 0
	}
	return session.snapshot.view.RuntimeGeneration
}

func (session *RequestRoutingSession) PoolID() int {
	if session == nil || session.snapshot == nil || session.poolIndex < 0 || session.poolIndex >= len(session.snapshot.view.Pools) {
		return 0
	}
	return session.snapshot.view.Pools[session.poolIndex].ID
}

func (session *RequestRoutingSession) CanaryPolicy() (model.RoutingCanaryPolicy, error) {
	if session == nil || session.snapshot == nil || session.poolIndex < 0 || session.poolIndex >= len(session.snapshot.view.Pools) {
		return model.RoutingCanaryPolicy{}, ErrRoutingSessionInvalid
	}
	pool := session.snapshot.view.Pools[session.poolIndex]
	if pool.ID <= 0 || pool.GroupName != session.groupName {
		return model.RoutingCanaryPolicy{}, ErrRoutingSessionInvalid
	}
	policy, err := model.NormalizeRoutingCanaryPolicy(pool.CanaryPolicy)
	if err != nil {
		return model.RoutingCanaryPolicy{}, ErrRoutingSessionInvalid
	}
	return policy, nil
}

func (session *RequestRoutingSession) IdentityForChannel(channelID int) (Identity, bool) {
	if session == nil || session.snapshot == nil || channelID <= 0 ||
		session.poolIndex < 0 || session.poolIndex >= len(session.snapshot.view.Pools) {
		return Identity{}, false
	}
	pool := &session.snapshot.view.Pools[session.poolIndex]
	memberID, exists := session.snapshot.memberByPoolChannel[poolChannelKey{PoolID: pool.ID, ChannelID: channelID}]
	if !exists || memberID <= 0 {
		return Identity{}, false
	}
	for index := range pool.Members {
		member := &pool.Members[index]
		if member.ID != memberID || member.ChannelID != channelID || member.PoolID != pool.ID {
			continue
		}
		identity := Identity{SnapshotRevision: session.snapshot.view.Revision, PoolID: pool.ID, MemberID: member.ID}
		if channel, ok := session.snapshot.channelByID[channelID]; ok {
			identity.FailureDomainHash = channel.FailureDomainHash
		}
		if len(member.CredentialIDs) == 1 {
			identity.CredentialID = member.CredentialIDs[0]
		}
		return identity, true
	}
	return Identity{}, false
}

func (session *RequestRoutingSession) ExpectedCostForChannel(
	channelID int,
	input RequestRoutingCostInput,
) (float64, bool, error) {
	estimate, available, err := session.CostEstimateForChannel(channelID, input)
	if err != nil || !available || !estimate.Known {
		return 0, false, err
	}
	return estimate.Cost, true, nil
}

func (session *RequestRoutingSession) WorstCaseCostForChannel(
	channelID int,
	input RequestRoutingCostInput,
) (float64, bool, error) {
	estimate, available, err := session.CostEstimateForChannel(channelID, input)
	if err != nil || !available || !estimate.WorstCaseKnown {
		return 0, false, err
	}
	return estimate.WorstCaseCost, true, nil
}

func (session *RequestRoutingSession) CostEstimateForChannel(
	channelID int,
	input RequestRoutingCostInput,
) (ShadowCostInput, bool, error) {
	if session == nil || session.snapshot == nil || channelID <= 0 ||
		session.poolIndex < 0 || session.poolIndex >= len(session.snapshot.view.Pools) {
		return ShadowCostInput{}, false, ErrRoutingSessionInvalid
	}
	pool := &session.snapshot.view.Pools[session.poolIndex]
	if pool.ID <= 0 || pool.GroupName != session.groupName || session.planningTime.IsZero() {
		return ShadowCostInput{}, false, ErrRoutingSessionInvalid
	}
	memberID, exists := session.snapshot.memberByPoolChannel[poolChannelKey{PoolID: pool.ID, ChannelID: channelID}]
	if !exists || memberID <= 0 {
		return ShadowCostInput{}, false, ErrRoutingSessionInvalid
	}
	profile, err := resolveRequestProfile(
		input.Profile,
		input.RequestPath,
		session.groupName,
		input.ModelName,
		input.IsStream,
		input.RetryIndex,
		input.PromptTokenEstimate,
		input.CompletionTokenEstimate,
	)
	if err != nil {
		return ShadowCostInput{}, false, ErrRoutingSessionInvalid
	}
	profile = attachRoutingCostProfile(profile, input.CostProfile, session.planningTime.Unix())
	observation, exists := session.snapshot.modelByMemberModel[memberModelKey{memberID: memberID, model: profile.ModelName}]
	if !exists {
		return ShadowCostInput{}, false, nil
	}
	cost, err := shadowExpectedCost(observation, profile)
	if err != nil {
		return ShadowCostInput{}, false, ErrRoutingSessionInvalid
	}
	settings := ShadowSelectorSettings{
		NowUnix:          session.planningTime.Unix(),
		SnapshotStaleSec: pool.SelectorPolicy.SnapshotStaleSec,
	}
	if cost == nil || shadowCostStale(cost, settings) {
		return ShadowCostInput{}, false, nil
	}
	return *cost, true, nil
}

func (session *RequestRoutingSession) Gate() (CanaryGate, bool, error) {
	if session == nil || session.snapshot == nil || session.poolIndex < 0 || session.poolIndex >= len(session.snapshot.view.Pools) {
		return CanaryGate{}, false, ErrRoutingSessionInvalid
	}
	pool := &session.snapshot.view.Pools[session.poolIndex]
	if pool.ID <= 0 || pool.GroupName != session.groupName {
		return CanaryGate{}, false, ErrRoutingSessionInvalid
	}
	if pool.DeploymentStage != model.RoutingDeploymentStageCanary {
		return CanaryGate{}, false, nil
	}
	gate, err := EvaluateCanaryGate(
		pool.ID,
		session.snapshot.view.ActivationID,
		session.snapshot.view.Revision,
		session.requestID,
		session.snapshot.view.TrafficBasisPoints,
	)
	if err != nil || session.snapshot.view.ActivationStage != model.RoutingDeploymentStageCanary {
		return CanaryGate{}, true, ErrRoutingSessionInvalid
	}
	return gate, true, nil
}

func (session *RequestRoutingSession) Plan(input RequestRoutingPlanInput) (RequestRoutingPlan, bool, error) {
	if input.RequiredCredentialID < 0 {
		return RequestRoutingPlan{}, false, ErrRoutingSessionInvalid
	}
	if session == nil || session.snapshot == nil || session.poolIndex < 0 || session.poolIndex >= len(session.snapshot.view.Pools) {
		return RequestRoutingPlan{}, false, ErrRoutingSessionInvalid
	}
	snapshot := session.snapshot
	pool := &snapshot.view.Pools[session.poolIndex]
	if pool.ID <= 0 || pool.GroupName != session.groupName {
		return RequestRoutingPlan{}, false, ErrRoutingSessionInvalid
	}
	gate, active, err := session.Gate()
	if err != nil || !active {
		return RequestRoutingPlan{}, active, err
	}
	plan := RequestRoutingPlan{Gate: gate}
	if !gate.InCanary {
		return plan, true, nil
	}

	profile, err := resolveRequestProfile(
		input.Profile,
		input.RequestPath,
		session.groupName,
		input.ModelName,
		input.IsStream,
		input.RetryIndex,
		input.PromptTokenEstimate,
		input.CompletionTokenEstimate,
	)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	profile = attachRoutingCostProfile(profile, input.CostProfile, session.planningTime.Unix())
	allowedChannels, allowedRestricted, err := routingSessionChannelSet(input.AllowedChannelIDs)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	excludedChannels, _, err := routingSessionChannelSet(input.ExcludedChannelIDs)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	excludedCredentials, _, err := routingSessionChannelSet(input.ExcludedCredentialIDs)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	excludedEndpoints, err := routingSessionEndpointSet(input.ExcludedEndpointIdentities)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	excludedFailureDomains, err := routingSessionFailureDomainSet(input.ExcludedFailureDomainHashes)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	capacityExcludedChannels, _, err := routingSessionChannelSet(input.CapacityExcludedChannelIDs)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	probeExcludedChannels, _, err := routingSessionChannelSet(input.ProbeExcludedChannelIDs)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	seed, err := DeriveDecisionSeed(session.requestID, snapshot.view.Revision, input.RetryIndex)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	now := session.planningTime
	if now.IsZero() {
		return RequestRoutingPlan{}, true, ErrRoutingSessionInvalid
	}
	settings := pool.SelectorPolicy.selectorSettings(now.Unix(), now.UnixMilli(), seed, input.IsStream)
	memberIndexes := snapshot.memberIndexesByPoolModel[poolModelKey{poolID: pool.ID, model: profile.ModelName}]
	if len(memberIndexes) > MaxShadowCandidates {
		return RequestRoutingPlan{}, true, ErrRoutingSessionInvalid
	}
	candidates := make([]ShadowCandidateInput, 0, len(memberIndexes))
	costEstimates := make(map[int]ShadowCostInput, len(memberIndexes))
	identities := make(map[int]Identity, len(memberIndexes))
	endpointBreakerByMemberID := make(map[int]struct {
		authority string
		region    string
	}, len(memberIndexes))
	for _, memberIndex := range memberIndexes {
		if memberIndex < 0 || memberIndex >= len(pool.Members) {
			return RequestRoutingPlan{}, true, ErrRoutingSessionInvalid
		}
		member := pool.Members[memberIndex]
		if member.PoolID != pool.ID || member.PhysicalStatus != common.ChannelStatusEnabled {
			continue
		}
		observation, exists := snapshot.modelByMemberModel[memberModelKey{memberID: member.ID, model: profile.ModelName}]
		if !exists {
			return RequestRoutingPlan{}, true, ErrRoutingSessionInvalid
		}
		channel, exists := snapshot.channelByID[member.ChannelID]
		if !exists || channel.Status != common.ChannelStatusEnabled {
			continue
		}
		candidate, costEstimate, candidateErr := shadowCandidateFromSnapshot(*pool, member, observation, channel, profile, settings)
		if candidateErr != nil {
			return RequestRoutingPlan{}, true, candidateErr
		}
		if costEstimate != nil {
			costEstimates[member.ChannelID] = *costEstimate
		}
		endpointBreaker, authority, region := endpointBreakerForChannel(channel, now, settings.SnapshotStaleSec)
		var endpointSelected bool
		candidate.Breaker, endpointSelected = mergeShadowBreaker(candidate.Breaker, endpointBreaker)
		if endpointSelected {
			endpointBreakerByMemberID[member.ID] = struct {
				authority string
				region    string
			}{authority: authority, region: region}
		}
		credentialID, credentialReason := snapshot.selectCredential(
			member, profile.ModelName, seed, excludedCredentials, input.RequiredCredentialID, now,
		)
		candidate.CredentialID = credentialID
		if candidate.RequestExclusionReason == "" {
			switch credentialReason {
			case credentialExclusionRequest:
				candidate.RequestExclusionReason = ExclusionReasonCredentialRequest
			case credentialExclusionUnavailable:
				candidate.RequestExclusionReason = ExclusionReasonCredentialUnavailable
			}
		}
		if candidate.RequestExclusionReason == "" {
			if _, blocked := ChannelBalanceRuntimeBlocked(member.ChannelID, now); blocked {
				candidate.RequestExclusionReason = ExclusionReasonChannelBalance
			}
		}
		if candidate.RequestExclusionReason == "" {
			switch {
			case routingSessionChannelContains(excludedChannels, member.ChannelID):
				candidate.RequestExclusionReason = ExclusionReasonRequestFailed
			case routingSessionEndpointExcluded(excludedEndpoints, channel):
				candidate.RequestExclusionReason = ExclusionReasonEndpointRequest
			case routingSessionFailureDomainExcluded(excludedFailureDomains, channel.FailureDomainHash):
				candidate.RequestExclusionReason = ExclusionReasonFailureDomainRequest
			case routingSessionChannelContains(probeExcludedChannels, member.ChannelID):
				candidate.RequestExclusionReason = ExclusionReasonHalfOpenProbe
			case routingSessionChannelContains(capacityExcludedChannels, member.ChannelID):
				candidate.RequestExclusionReason = ExclusionReasonLocalCapacity
			case allowedRestricted && !routingSessionChannelContains(allowedChannels, member.ChannelID):
				candidate.RequestExclusionReason = ExclusionReasonChannelNotAllowed
			case member.CredentialsTruncated:
				candidate.RequestExclusionReason = ExclusionReasonCredentialUnavailable
			case input.SlowStartFactor != nil:
				factor, factorErr := session.slowStartFactor(SlowStartKey{
					PoolID: pool.ID, MemberID: member.ID, Model: profile.ModelName,
				}, input.SlowStartFactor)
				if factorErr != nil || math.IsNaN(factor) || math.IsInf(factor, 0) || factor < 0 || factor > 1 {
					return RequestRoutingPlan{}, true, fmt.Errorf("%w: slow start factor", ErrRoutingSessionInvalid)
				}
				if factor == 0 {
					candidate.RequestExclusionReason = ExclusionReasonSlowStartUnavailable
				} else {
					candidate.SlowStartFactor = factor
				}
			}
		}
		identity := Identity{
			SnapshotRevision:  snapshot.view.Revision,
			PoolID:            pool.ID,
			MemberID:          member.ID,
			CredentialID:      credentialID,
			FailureDomainHash: channel.FailureDomainHash,
		}
		identities[member.ID] = identity
		candidates = append(candidates, candidate)
	}

	replay, err := BuildCanaryReplayInput(
		pool.ID,
		snapshot.view.Revision,
		snapshot.view.RuntimeGeneration,
		snapshot.view.PolicyHash,
		profile,
		settings,
		candidates,
	)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	replay.costEstimates = costEstimates
	result, err := RunShadowReplay(replay)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	plan.Replay = replay
	plan.Result = result
	if result.SelectedMemberID > 0 {
		identity, exists := identities[result.SelectedMemberID]
		if !exists {
			return RequestRoutingPlan{}, true, ErrRoutingSessionInvalid
		}
		plan.SelectedIdentity = identity
		if endpoint, endpointSelected := endpointBreakerByMemberID[result.SelectedMemberID]; endpointSelected {
			plan.SelectedBreakerScope = BreakerScopeEndpoint
			plan.SelectedEndpointAuthority = endpoint.authority
			plan.SelectedRegion = endpoint.region
		}
	}
	return plan, true, nil
}

func (session *RequestRoutingSession) slowStartFactor(
	key SlowStartKey,
	provider func(SlowStartKey) (float64, error),
) (float64, error) {
	session.slowStartMu.Lock()
	factor, exists := session.slowStartFactors[key]
	session.slowStartMu.Unlock()
	if exists {
		return factor, nil
	}
	factor, err := provider(key)
	if err != nil {
		return 0, err
	}
	session.slowStartMu.Lock()
	if stored, storedExists := session.slowStartFactors[key]; storedExists {
		factor = stored
	} else {
		session.slowStartFactors[key] = factor
	}
	session.slowStartMu.Unlock()
	return factor, nil
}

func attachRoutingCostProfile(
	profile RequestProfile,
	costProfile *model.RoutingCostRequestProfile,
	atUnix int64,
) RequestProfile {
	profile.costAtUnix = atUnix
	if costProfile == nil {
		return profile
	}
	cloned := *costProfile
	if len(costProfile.Request.Body) > 0 {
		cloned.Request.Body = append([]byte(nil), costProfile.Request.Body...)
	}
	if len(costProfile.Request.Headers) > 0 {
		cloned.Request.Headers = make(map[string]string, len(costProfile.Request.Headers))
		for key, value := range costProfile.Request.Headers {
			cloned.Request.Headers[key] = value
		}
	}
	profile.costProfile = &cloned
	return profile
}

func routingSessionChannelSet(channelIDs []int) (map[int]struct{}, bool, error) {
	restricted := channelIDs != nil
	if len(channelIDs) > MaxShadowCandidates {
		return nil, restricted, ErrRoutingSessionInvalid
	}
	result := make(map[int]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID <= 0 {
			return nil, restricted, ErrRoutingSessionInvalid
		}
		result[channelID] = struct{}{}
	}
	return result, restricted, nil
}

func routingSessionChannelContains(channelIDs map[int]struct{}, channelID int) bool {
	_, exists := channelIDs[channelID]
	return exists
}

func routingSessionEndpointSet(identities []RequestEndpointIdentity) (map[string]struct{}, error) {
	if len(identities) > MaxShadowCandidates {
		return nil, ErrRoutingSessionInvalid
	}
	result := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		key, ok := requestEndpointIdentityKey(identity.EndpointAuthority, identity.Region)
		if !ok {
			return nil, ErrRoutingSessionInvalid
		}
		result[key] = struct{}{}
	}
	return result, nil
}

func routingSessionFailureDomainSet(hashes []string) (map[string]struct{}, error) {
	if len(hashes) > MaxShadowCandidates {
		return nil, ErrRoutingSessionInvalid
	}
	result := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		hash = strings.ToLower(strings.TrimSpace(hash))
		if !validShadowHash(hash) {
			return nil, ErrRoutingSessionInvalid
		}
		result[hash] = struct{}{}
	}
	return result, nil
}

func requestEndpointIdentityKey(endpointAuthority string, region string) (string, bool) {
	endpointAuthority = strings.ToLower(strings.TrimSpace(endpointAuthority))
	region = normalizeRoutingRegion(region)
	if endpointAuthority == "" || region == "" {
		return "", false
	}
	return endpointAuthority + "\x00" + region, true
}

func routingSessionEndpointExcluded(excluded map[string]struct{}, channel ChannelSnapshot) bool {
	if len(excluded) == 0 {
		return false
	}
	key, ok := requestEndpointIdentityKey(EndpointAuthority(channel.Endpoint, channel.ID), RoutingRegion())
	if !ok {
		return false
	}
	_, exists := excluded[key]
	return exists
}

func routingSessionFailureDomainExcluded(excluded map[string]struct{}, failureDomainHash string) bool {
	failureDomainHash = strings.ToLower(strings.TrimSpace(failureDomainHash))
	if failureDomainHash == "" || len(excluded) == 0 {
		return false
	}
	_, exists := excluded[failureDomainHash]
	return exists
}
