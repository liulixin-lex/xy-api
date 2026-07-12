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
	ExclusionReasonRequestFailed        = "request_failed"
	ExclusionReasonChannelNotAllowed    = "channel_not_allowed"
	ExclusionReasonLocalCapacity        = "local_capacity_exhausted"
	ExclusionReasonMultiKeyUnsupported  = "multi_key_unsupported"
	ExclusionReasonSlowStartUnavailable = "slow_start_unavailable"
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

type RequestRoutingPlanInput struct {
	RequestPath             string
	ModelName               string
	IsStream                bool
	RetryIndex              int
	PromptTokenEstimate     int
	CompletionTokenEstimate int
	// A nil allowed list means unrestricted. A non-nil empty list fails closed.
	AllowedChannelIDs          []int
	ExcludedChannelIDs         []int
	CapacityExcludedChannelIDs []int
	SlowStartFactor            func(SlowStartKey) (float64, error)
}

type RequestRoutingPlan struct {
	Gate             CanaryGate         `json:"gate"`
	Replay           ShadowReplayInput  `json:"replay"`
	Result           ShadowReplayResult `json:"result"`
	SelectedIdentity Identity           `json:"selected_identity"`
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
		if len(member.CredentialIDs) == 1 {
			identity.CredentialID = member.CredentialIDs[0]
		}
		return identity, true
	}
	return Identity{}, false
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

	profile, err := NewRequestProfile(
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
	allowedChannels, allowedRestricted, err := routingSessionChannelSet(input.AllowedChannelIDs)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	excludedChannels, _, err := routingSessionChannelSet(input.ExcludedChannelIDs)
	if err != nil {
		return RequestRoutingPlan{}, true, err
	}
	capacityExcludedChannels, _, err := routingSessionChannelSet(input.CapacityExcludedChannelIDs)
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
	identities := make(map[int]Identity, len(memberIndexes))
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
		candidate, candidateErr := shadowCandidateFromSnapshot(*pool, member, observation, channel, profile, settings)
		if candidateErr != nil {
			return RequestRoutingPlan{}, true, candidateErr
		}
		switch {
		case routingSessionChannelContains(excludedChannels, member.ChannelID):
			candidate.RequestExclusionReason = ExclusionReasonRequestFailed
		case routingSessionChannelContains(capacityExcludedChannels, member.ChannelID):
			candidate.RequestExclusionReason = ExclusionReasonLocalCapacity
		case allowedRestricted && !routingSessionChannelContains(allowedChannels, member.ChannelID):
			candidate.RequestExclusionReason = ExclusionReasonChannelNotAllowed
		case member.MultiKey || channel.MultiKey || member.CredentialsTruncated || len(member.CredentialIDs) > 1:
			candidate.RequestExclusionReason = ExclusionReasonMultiKeyUnsupported
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
		identity := Identity{SnapshotRevision: snapshot.view.Revision, PoolID: pool.ID, MemberID: member.ID}
		if len(member.CredentialIDs) == 1 {
			identity.CredentialID = member.CredentialIDs[0]
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
