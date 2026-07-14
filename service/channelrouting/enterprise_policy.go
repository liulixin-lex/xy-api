package channelrouting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

const (
	EnterpriseCapacityScopeAuto              = "auto"
	EnterpriseCapacityScopeAccount           = "account"
	EnterpriseCapacityScopeCredential        = "credential"
	EnterpriseCapacityScopeAccountCredential = "account_credential"

	enterpriseCapacityDefaultLeaseSeconds = 60
)

var (
	ErrEnterprisePolicyInvalid       = errors.New("invalid enterprise channel routing policy")
	ErrEnterpriseCapacityIdentity    = errors.New("enterprise capacity identity is unavailable")
	ErrEnterpriseCapacityCostUnknown = errors.New("enterprise capacity requires a known request cost")
)

type EnterprisePoolPolicy struct {
	Capacity EnterpriseCapacityPolicy `json:"capacity"`
	Hedge    EnterpriseHedgePolicy    `json:"hedge"`
}

type EnterpriseCapacityPolicy struct {
	Mode                  CapacityMode        `json:"mode"`
	Scope                 string              `json:"scope"`
	Limit                 StrictCapacityLimit `json:"limit"`
	LeaseTTL              time.Duration       `json:"-"`
	GuaranteedBasisPoints int                 `json:"guaranteed_basis_points"`
	MaximumBasisPoints    int                 `json:"maximum_basis_points"`
	guaranteeConfigured   bool
}

type enterpriseMemberPolicy struct {
	AccountID int
	Capacity  EnterpriseCapacityPolicy
}

type enterpriseCapacityOverrides struct {
	Mode                  *CapacityMode `json:"mode"`
	Scope                 *string       `json:"scope"`
	AccountID             *int          `json:"account_id"`
	RPM                   *int64        `json:"rpm"`
	InputTPM              *int64        `json:"input_tpm"`
	OutputTPM             *int64        `json:"output_tpm"`
	TotalTPM              *int64        `json:"total_tpm"`
	Inflight              *int64        `json:"inflight"`
	CostNanoUSD           *int64        `json:"cost_nano_usd"`
	LeaseTTLSeconds       *int          `json:"lease_ttl_seconds"`
	GuaranteedBasisPoints *int          `json:"guaranteed_basis_points"`
	MaximumBasisPoints    *int          `json:"maximum_basis_points"`
}

type strictCapacityPlan struct {
	Mode       CapacityMode
	Key        StrictCapacityKey
	Limit      StrictCapacityLimit
	PoolShares []StrictCapacityPoolShare
	LeaseTTL   time.Duration
}

type strictCapacityPlanKey struct {
	memberID     int
	credentialID int
	model        string
}

func resolveEnterprisePoolPolicy(profile string, policyJSON json.RawMessage) (EnterprisePoolPolicy, error) {
	policy := defaultEnterprisePoolPolicy(profile)
	capacity, exists, err := enterpriseCapacityOverridesFromDocument(policyJSON)
	if err != nil {
		return EnterprisePoolPolicy{}, err
	}
	if exists {
		applyEnterpriseCapacityOverrides(&policy.Capacity, capacity, false)
	}
	policy.Hedge, err = resolveEnterpriseHedgePolicy(profile, policyJSON)
	if err != nil {
		return EnterprisePoolPolicy{}, err
	}
	if err := validateEnterprisePoolPolicy(profile, policy); err != nil {
		return EnterprisePoolPolicy{}, err
	}
	return policy, nil
}

func resolveEnterpriseMemberPolicy(
	profile string,
	policyJSON json.RawMessage,
	poolPolicy EnterprisePoolPolicy,
) (enterpriseMemberPolicy, error) {
	result := enterpriseMemberPolicy{Capacity: poolPolicy.Capacity}
	capacity, exists, err := enterpriseCapacityOverridesFromDocument(policyJSON)
	if err != nil {
		return enterpriseMemberPolicy{}, err
	}
	if exists {
		applyEnterpriseCapacityOverrides(&result.Capacity, capacity, true)
		if capacity.AccountID != nil {
			result.AccountID = *capacity.AccountID
		}
	}
	if result.AccountID < 0 || validateEnterpriseCapacityPolicy(result.Capacity) != nil {
		return enterpriseMemberPolicy{}, ErrEnterprisePolicyInvalid
	}
	if profile != model.RoutingPolicyProfileEnterpriseSLO &&
		(result.Capacity.Mode == CapacityModeRedisStrict || result.Capacity.Mode == CapacityModeRedisBlock) {
		return enterpriseMemberPolicy{}, ErrEnterprisePolicyInvalid
	}
	return result, nil
}

func defaultEnterprisePoolPolicy(profile string) EnterprisePoolPolicy {
	mode := CapacityModeLocalSoft
	if profile == model.RoutingPolicyProfileEnterpriseSLO {
		mode = CapacityModeRedisBlock
	}
	return EnterprisePoolPolicy{Capacity: EnterpriseCapacityPolicy{
		Mode:  mode,
		Scope: EnterpriseCapacityScopeAuto,
		Limit: StrictCapacityLimit{
			RPM: 600, InputTPM: 1_000_000, OutputTPM: 250_000, TotalTPM: 1_250_000, Inflight: 32,
		},
		LeaseTTL:           enterpriseCapacityDefaultLeaseSeconds * time.Second,
		MaximumBasisPoints: 10_000,
	}, Hedge: defaultEnterpriseHedgePolicy()}
}

func enterpriseCapacityOverridesFromDocument(
	policyJSON json.RawMessage,
) (enterpriseCapacityOverrides, bool, error) {
	if len(bytes.TrimSpace(policyJSON)) == 0 {
		policyJSON = json.RawMessage(`{}`)
	}
	var root map[string]json.RawMessage
	if err := common.Unmarshal(policyJSON, &root); err != nil || root == nil {
		return enterpriseCapacityOverrides{}, false, ErrEnterprisePolicyInvalid
	}
	raw, exists := root["enterprise"]
	if !exists {
		return enterpriseCapacityOverrides{}, false, nil
	}
	var enterprise map[string]json.RawMessage
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || common.Unmarshal(raw, &enterprise) != nil || enterprise == nil {
		return enterpriseCapacityOverrides{}, false, ErrEnterprisePolicyInvalid
	}
	raw, exists = enterprise["capacity"]
	if !exists {
		return enterpriseCapacityOverrides{}, true, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return enterpriseCapacityOverrides{}, false, ErrEnterprisePolicyInvalid
	}
	var overrides enterpriseCapacityOverrides
	if err := common.Unmarshal(raw, &overrides); err != nil {
		return enterpriseCapacityOverrides{}, false, ErrEnterprisePolicyInvalid
	}
	return overrides, true, nil
}

func applyEnterpriseCapacityOverrides(
	policy *EnterpriseCapacityPolicy,
	overrides enterpriseCapacityOverrides,
	member bool,
) {
	if overrides.Mode != nil {
		policy.Mode = *overrides.Mode
	}
	if overrides.Scope != nil {
		policy.Scope = strings.TrimSpace(*overrides.Scope)
	}
	if overrides.RPM != nil {
		policy.Limit.RPM = *overrides.RPM
	}
	if overrides.InputTPM != nil {
		policy.Limit.InputTPM = *overrides.InputTPM
	}
	if overrides.OutputTPM != nil {
		policy.Limit.OutputTPM = *overrides.OutputTPM
	}
	if overrides.TotalTPM != nil {
		policy.Limit.TotalTPM = *overrides.TotalTPM
	}
	if overrides.Inflight != nil {
		policy.Limit.Inflight = *overrides.Inflight
	}
	if overrides.CostNanoUSD != nil {
		policy.Limit.CostNanoUSD = *overrides.CostNanoUSD
	}
	if overrides.LeaseTTLSeconds != nil {
		policy.LeaseTTL = time.Duration(*overrides.LeaseTTLSeconds) * time.Second
	}
	if !member {
		if overrides.GuaranteedBasisPoints != nil {
			policy.GuaranteedBasisPoints = *overrides.GuaranteedBasisPoints
			policy.guaranteeConfigured = true
		}
		if overrides.MaximumBasisPoints != nil {
			policy.MaximumBasisPoints = *overrides.MaximumBasisPoints
		}
	}
}

func validateEnterprisePoolPolicy(profile string, policy EnterprisePoolPolicy) error {
	if err := validateEnterpriseCapacityPolicy(policy.Capacity); err != nil {
		return err
	}
	if err := validateEnterpriseHedgePolicy(profile, policy.Hedge); err != nil {
		return err
	}
	if profile == model.RoutingPolicyProfileEnterpriseSLO {
		if policy.Capacity.Mode != CapacityModeRedisStrict && policy.Capacity.Mode != CapacityModeRedisBlock {
			return ErrEnterprisePolicyInvalid
		}
	} else if policy.Capacity.Mode == CapacityModeRedisStrict || policy.Capacity.Mode == CapacityModeRedisBlock {
		return ErrEnterprisePolicyInvalid
	}
	return nil
}

func validateEnterpriseCapacityPolicy(policy EnterpriseCapacityPolicy) error {
	if policy.Mode != CapacityModeLocalSoft && policy.Mode != CapacityModeRedisStrict && policy.Mode != CapacityModeRedisBlock {
		return ErrEnterprisePolicyInvalid
	}
	switch policy.Scope {
	case EnterpriseCapacityScopeAuto, EnterpriseCapacityScopeAccount,
		EnterpriseCapacityScopeCredential, EnterpriseCapacityScopeAccountCredential:
	default:
		return ErrEnterprisePolicyInvalid
	}
	values := []int64{
		policy.Limit.RPM, policy.Limit.InputTPM, policy.Limit.OutputTPM,
		policy.Limit.TotalTPM, policy.Limit.Inflight, policy.Limit.CostNanoUSD,
	}
	for index, value := range values {
		if value < 0 || value > strictCapacityMaximumValue ||
			((policy.Mode == CapacityModeRedisStrict || policy.Mode == CapacityModeRedisBlock) && index < 5 && value == 0) {
			return ErrEnterprisePolicyInvalid
		}
	}
	if policy.Limit.TotalTPM < max(policy.Limit.InputTPM, policy.Limit.OutputTPM) ||
		policy.LeaseTTL < strictCapacityMinimumLease || policy.LeaseTTL > strictCapacityMaximumLease ||
		policy.GuaranteedBasisPoints < 0 || policy.GuaranteedBasisPoints > 10_000 ||
		policy.MaximumBasisPoints < 1 || policy.MaximumBasisPoints > 10_000 ||
		policy.GuaranteedBasisPoints > policy.MaximumBasisPoints {
		return ErrEnterprisePolicyInvalid
	}
	return nil
}

func (snapshot *runtimeSnapshot) compileStrictCapacityPlans() error {
	if snapshot == nil {
		return ErrRoutingSessionInvalid
	}
	snapshot.strictCapacityPlans = make(map[strictCapacityPlanKey]strictCapacityPlan)
	type seed struct {
		key      strictCapacityPlanKey
		resource StrictCapacityKey
		policy   EnterpriseCapacityPolicy
		poolID   int
	}
	type resourceConfig struct {
		mode                CapacityMode
		limit               StrictCapacityLimit
		leaseTTL            time.Duration
		shares              map[int]StrictCapacityPoolShare
		guaranteeConfigured map[int]bool
	}
	seeds := make([]seed, 0)
	resources := make(map[StrictCapacityKey]*resourceConfig)
	for poolIndex := range snapshot.view.Pools {
		pool := &snapshot.view.Pools[poolIndex]
		if pool.PolicyProfile != model.RoutingPolicyProfileEnterpriseSLO {
			continue
		}
		poolPolicy := pool.enterprisePolicy
		if poolPolicy.Capacity.Mode == "" {
			poolPolicy = defaultEnterprisePoolPolicy(pool.PolicyProfile)
		}
		if err := validateEnterprisePoolPolicy(pool.PolicyProfile, poolPolicy); err != nil {
			return err
		}
		for memberIndex := range pool.Members {
			member := &pool.Members[memberIndex]
			channel, channelExists := snapshot.channelByID[member.ChannelID]
			if !channelExists {
				return ErrEnterpriseCapacityIdentity
			}
			memberPolicy := member.enterprisePolicy
			if memberPolicy.Capacity.Mode == "" {
				memberPolicy.Capacity = poolPolicy.Capacity
			}
			if err := validateEnterpriseCapacityPolicy(memberPolicy.Capacity); err != nil {
				return err
			}
			if memberPolicy.Capacity.Mode != CapacityModeRedisStrict && memberPolicy.Capacity.Mode != CapacityModeRedisBlock {
				continue
			}
			for modelIndex := range member.Models {
				observation := &member.Models[modelIndex]
				mappingModelName := observation.ModelName
				if strings.HasSuffix(mappingModelName, ratio_setting.CompactModelSuffix) {
					mappingModelName = strings.TrimSuffix(mappingModelName, ratio_setting.CompactModelSuffix)
				}
				upstreamModel, _, err := model.ResolveChannelModelMapping(channel.ModelMapping, mappingModelName)
				if err != nil || strings.TrimSpace(upstreamModel) == "" {
					return fmt.Errorf("resolve strict capacity upstream model: %w", errors.Join(ErrEnterprisePolicyInvalid, err))
				}
				accountID := memberPolicy.AccountID
				if accountID == 0 {
					accountID = observation.upstreamAccountID
				}
				credentialIDs := append([]int(nil), member.CredentialIDs...)
				if len(credentialIDs) == 0 {
					credentialIDs = []int{0}
				}
				if member.CredentialsTruncated &&
					(memberPolicy.Capacity.Scope == EnterpriseCapacityScopeCredential ||
						memberPolicy.Capacity.Scope == EnterpriseCapacityScopeAccountCredential ||
						(memberPolicy.Capacity.Scope == EnterpriseCapacityScopeAuto && accountID <= 0)) {
					return ErrEnterpriseCapacityIdentity
				}
				for _, credentialID := range credentialIDs {
					resource := StrictCapacityKey{AccountID: accountID, CredentialID: credentialID, Model: upstreamModel}
					switch memberPolicy.Capacity.Scope {
					case EnterpriseCapacityScopeAuto:
						if resource.AccountID > 0 {
							resource.CredentialID = 0
						} else {
							resource.AccountID = 0
						}
					case EnterpriseCapacityScopeAccount:
						resource.CredentialID = 0
					case EnterpriseCapacityScopeCredential:
						resource.AccountID = 0
					}
					if (resource.AccountID <= 0 && resource.CredentialID <= 0) ||
						(memberPolicy.Capacity.Scope == EnterpriseCapacityScopeAccount && resource.AccountID <= 0) ||
						(memberPolicy.Capacity.Scope == EnterpriseCapacityScopeCredential && resource.CredentialID <= 0) ||
						(memberPolicy.Capacity.Scope == EnterpriseCapacityScopeAccountCredential &&
							(resource.AccountID <= 0 || resource.CredentialID <= 0)) {
						return ErrEnterpriseCapacityIdentity
					}
					config := resources[resource]
					if config == nil {
						config = &resourceConfig{
							mode: memberPolicy.Capacity.Mode, limit: memberPolicy.Capacity.Limit,
							leaseTTL: memberPolicy.Capacity.LeaseTTL,
							shares:   make(map[int]StrictCapacityPoolShare), guaranteeConfigured: make(map[int]bool),
						}
						resources[resource] = config
					} else if config.mode != memberPolicy.Capacity.Mode || config.limit != memberPolicy.Capacity.Limit ||
						config.leaseTTL != memberPolicy.Capacity.LeaseTTL {
						return ErrStrictCapacityConflict
					}
					share := StrictCapacityPoolShare{
						PoolID: pool.ID, GuaranteedBasisPoints: poolPolicy.Capacity.GuaranteedBasisPoints,
						MaximumBasisPoints: poolPolicy.Capacity.MaximumBasisPoints,
					}
					if existing, exists := config.shares[pool.ID]; exists {
						if existing != share || config.guaranteeConfigured[pool.ID] != poolPolicy.Capacity.guaranteeConfigured {
							return ErrStrictCapacityConflict
						}
					}
					config.shares[pool.ID] = share
					config.guaranteeConfigured[pool.ID] = poolPolicy.Capacity.guaranteeConfigured
					seeds = append(seeds, seed{
						key:      strictCapacityPlanKey{memberID: member.ID, credentialID: credentialID, model: observation.ModelName},
						resource: resource, policy: memberPolicy.Capacity, poolID: pool.ID,
					})
				}
			}
		}
	}
	compiledShares := make(map[StrictCapacityKey][]StrictCapacityPoolShare, len(resources))
	for resource, config := range resources {
		poolIDs := make([]int, 0, len(config.shares))
		configuredTotal := 0
		unconfigured := make([]int, 0, len(config.shares))
		for poolID, share := range config.shares {
			poolIDs = append(poolIDs, poolID)
			if config.guaranteeConfigured[poolID] {
				configuredTotal += share.GuaranteedBasisPoints
				if configuredTotal > 10_000 {
					return ErrEnterprisePolicyInvalid
				}
			} else {
				unconfigured = append(unconfigured, poolID)
			}
		}
		sort.Ints(poolIDs)
		sort.Ints(unconfigured)
		compiledGuarantees := make(map[int]int, len(config.shares))
		for _, poolID := range poolIDs {
			compiledGuarantees[poolID] = config.shares[poolID].GuaranteedBasisPoints
		}
		if len(unconfigured) > 0 {
			remaining := 10_000 - configuredTotal
			base := remaining / len(unconfigured)
			extra := remaining % len(unconfigured)
			for index, poolID := range unconfigured {
				compiledGuarantees[poolID] = base
				if index < extra {
					compiledGuarantees[poolID]++
				}
			}
		}
		shares := make([]StrictCapacityPoolShare, 0, len(poolIDs))
		compiledTotal := 0
		for _, poolID := range poolIDs {
			share := config.shares[poolID]
			share.GuaranteedBasisPoints = compiledGuarantees[poolID]
			if share.GuaranteedBasisPoints > share.MaximumBasisPoints {
				return ErrEnterprisePolicyInvalid
			}
			compiledTotal += share.GuaranteedBasisPoints
			if compiledTotal > 10_000 {
				return ErrEnterprisePolicyInvalid
			}
			shares = append(shares, share)
		}
		compiledShares[resource] = shares
	}
	for _, item := range seeds {
		shares := compiledShares[item.resource]
		if len(shares) == 0 {
			return ErrEnterprisePolicyInvalid
		}
		snapshot.strictCapacityPlans[item.key] = strictCapacityPlan{
			Mode: item.policy.Mode, Key: item.resource, Limit: item.policy.Limit,
			PoolShares: append([]StrictCapacityPoolShare(nil), shares...), LeaseTTL: item.policy.LeaseTTL,
		}
	}
	return nil
}

func (session *RequestRoutingSession) StrictCapacityRequest(
	identity Identity,
	modelName string,
	upstreamModelName string,
	inputEstimate CapacityDimensionEstimate,
	outputEstimate CapacityDimensionEstimate,
	expectedCost float64,
	costKnown bool,
) (StrictCapacityRequest, bool, error) {
	if session == nil || session.snapshot == nil || identity.SnapshotRevision != session.snapshot.view.Revision ||
		identity.PoolID != session.PoolID() || identity.MemberID <= 0 {
		return StrictCapacityRequest{}, false, ErrRoutingSessionInvalid
	}
	modelName = strings.TrimSpace(modelName)
	upstreamModelName = strings.TrimSpace(upstreamModelName)
	plan, exists := session.snapshot.strictCapacityPlans[strictCapacityPlanKey{
		memberID: identity.MemberID, credentialID: identity.CredentialID, model: modelName,
	}]
	if !exists {
		poolIndex := session.poolIndex
		if poolIndex >= 0 && poolIndex < len(session.snapshot.view.Pools) &&
			session.snapshot.view.Pools[poolIndex].PolicyProfile == model.RoutingPolicyProfileEnterpriseSLO {
			return StrictCapacityRequest{}, true, ErrEnterpriseCapacityIdentity
		}
		return StrictCapacityRequest{}, false, nil
	}
	if upstreamModelName == "" || plan.Key.Model != upstreamModelName {
		return StrictCapacityRequest{}, true, ErrStrictCapacityConflict
	}
	input, err := inputEstimate.Demand(plan.Limit.InputTPM)
	if err != nil {
		return StrictCapacityRequest{}, true, ErrStrictCapacityInvalid
	}
	output, err := outputEstimate.Demand(plan.Limit.OutputTPM)
	if err != nil {
		return StrictCapacityRequest{}, true, ErrStrictCapacityInvalid
	}
	if input > math.MaxInt64-output {
		return StrictCapacityRequest{}, true, ErrStrictCapacityInvalid
	}
	demand := StrictCapacityDemand{RPM: 1, InputTPM: input, OutputTPM: output, TotalTPM: input + output, Inflight: 1}
	if plan.Limit.CostNanoUSD > 0 {
		if !costKnown || math.IsNaN(expectedCost) || math.IsInf(expectedCost, 0) || expectedCost < 0 {
			return StrictCapacityRequest{}, true, ErrEnterpriseCapacityCostUnknown
		} else if expectedCost > float64(strictCapacityMaximumValue)/1_000_000_000 {
			return StrictCapacityRequest{}, true, ErrStrictCapacityExhausted
		} else {
			demand.CostNanoUSD = int64(math.Ceil(expectedCost * 1_000_000_000))
		}
	}
	if !validStrictCapacityLimit(plan.Limit, demand) {
		return StrictCapacityRequest{}, true, ErrStrictCapacityExhausted
	}
	return StrictCapacityRequest{
		Mode: plan.Mode, Key: plan.Key, PoolID: identity.PoolID, PolicyRevision: identity.SnapshotRevision,
		Demand: demand, Limit: plan.Limit, PoolShares: append([]StrictCapacityPoolShare(nil), plan.PoolShares...),
		LeaseTTL: plan.LeaseTTL,
	}, true, nil
}

func CapacityAdmissionFromStrict(
	identity Identity,
	modelName string,
	admission StrictCapacityAdmission,
) (CapacityAdmission, error) {
	modelName = strings.TrimSpace(modelName)
	if identity.SnapshotRevision == 0 || identity.PoolID <= 0 || identity.MemberID <= 0 || modelName == "" ||
		(admission.Mode != CapacityModeRedisStrict && admission.Mode != CapacityModeRedisBlock) ||
		admission.PoolID != identity.PoolID ||
		admission.PolicyRevision != identity.SnapshotRevision || admission.Key.Model == "" ||
		admission.LeaseTTLMillis < strictCapacityMinimumLease.Milliseconds() ||
		admission.LeaseTTLMillis > strictCapacityMaximumLease.Milliseconds() || admission.LeaseExpiresMs <= 0 {
		return CapacityAdmission{}, ErrStrictCapacityInvalid
	}
	normalized, err := normalizeStrictCapacityRequest(StrictCapacityRequest{
		Mode: admission.Mode, Key: admission.Key, PoolID: admission.PoolID, PolicyRevision: admission.PolicyRevision,
		Demand: admission.Demand, Limit: admission.Limit,
		PoolShares: append([]StrictCapacityPoolShare(nil), admission.PoolShares...),
		LeaseTTL:   time.Duration(admission.LeaseTTLMillis) * time.Millisecond,
	})
	if err != nil {
		return CapacityAdmission{}, err
	}
	strict := admission
	strict.PoolShares = append([]StrictCapacityPoolShare(nil), normalized.PoolShares...)
	return CapacityAdmission{
		Mode: admission.Mode,
		Key: CapacityKey{
			PolicyRevision: identity.SnapshotRevision, PoolID: identity.PoolID,
			MemberID: identity.MemberID, Model: modelName,
		},
		Demand: Demand{
			RPM: admission.Demand.RPM, InputTPM: admission.Demand.InputTPM,
			OutputTPM: admission.Demand.OutputTPM, Inflight: admission.Demand.Inflight,
		},
		Limit: Limit{
			RPM: admission.Limit.RPM, InputTPM: admission.Limit.InputTPM,
			OutputTPM: admission.Limit.OutputTPM, Inflight: admission.Limit.Inflight,
		},
		Strict: &strict,
	}, nil
}
