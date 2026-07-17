package channelrouting

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

const RoutingCostComparisonMaxCandidates = 200

var (
	ErrRoutingCostCatalogInvalid        = errors.New("invalid routing cost catalog request")
	ErrRoutingCostProfileNotFound       = errors.New("routing cost request profile not found")
	ErrRoutingCostComparisonUnavailable = errors.New("routing cost comparison snapshot is unavailable")
)

type CostCatalogPoolSummary struct {
	PoolID               int    `json:"pool_id"`
	GroupName            string `json:"group_name"`
	DisplayName          string `json:"display_name"`
	MemberCount          int    `json:"member_count"`
	ModelCount           int    `json:"model_count"`
	KnownContractCount   int    `json:"known_contract_count"`
	UnknownContractCount int    `json:"unknown_contract_count"`
}

type CostCatalogMemberSummary struct {
	PoolID                 int     `json:"pool_id"`
	MemberID               int     `json:"member_id"`
	ChannelID              int     `json:"channel_id"`
	RoutingIdentity        string  `json:"routing_identity"`
	RoutingGeneration      string  `json:"routing_generation"`
	ChannelName            string  `json:"channel_name"`
	ChannelType            int     `json:"channel_type"`
	PhysicalStatus         int     `json:"physical_status"`
	ModelCount             int     `json:"model_count"`
	KnownContractCount     int     `json:"known_contract_count"`
	UnknownContractCount   int     `json:"unknown_contract_count"`
	ConfigurationRevision  int64   `json:"configuration_revision"`
	UpstreamCostMultiplier float64 `json:"upstream_cost_multiplier"`
}

type CostCatalogModelSummary struct {
	PoolID                 int      `json:"pool_id"`
	MemberID               int      `json:"member_id"`
	ChannelID              int      `json:"channel_id"`
	RoutingGeneration      string   `json:"routing_generation"`
	ModelName              string   `json:"model_name"`
	UpstreamModelName      string   `json:"upstream_model_name,omitempty"`
	Known                  bool     `json:"known"`
	UnknownReason          string   `json:"unknown_reason,omitempty"`
	BillingMode            string   `json:"billing_mode,omitempty"`
	Currency               string   `json:"currency,omitempty"`
	ContractMode           string   `json:"contract_mode,omitempty"`
	ConfiguredDimensions   []string `json:"configured_dimensions"`
	ExplicitFreeDimensions []string `json:"explicit_free_dimensions"`
	ConfigurationRevision  int64    `json:"configuration_revision"`
	UpstreamCostMultiplier float64  `json:"upstream_cost_multiplier"`
	PricingIdentity        string   `json:"pricing_identity,omitempty"`
}

type RoutingCostComparisonRequest struct {
	PoolID          int
	ModelName       string
	MemberIDs       []int
	Profile         model.RoutingCostRequestProfile
	ProfileSource   string
	QuantitySources map[string]string
	AtUnix          int64
}

type RoutingCostComparisonCandidate struct {
	PoolID                 int                       `json:"pool_id"`
	MemberID               int                       `json:"member_id"`
	ChannelID              int                       `json:"channel_id"`
	RoutingIdentity        string                    `json:"routing_identity"`
	RoutingGeneration      string                    `json:"routing_generation"`
	ChannelName            string                    `json:"channel_name"`
	ModelName              string                    `json:"model_name"`
	UpstreamModelName      string                    `json:"upstream_model_name,omitempty"`
	Comparable             bool                      `json:"comparable"`
	MissingContext         []string                  `json:"missing_context,omitempty"`
	UnknownReason          string                    `json:"unknown_reason,omitempty"`
	SingleAttempt          model.RoutingCostEstimate `json:"single_attempt"`
	BeforeMultiplier       model.RoutingCostEstimate `json:"before_multiplier"`
	UpstreamCostMultiplier float64                   `json:"upstream_cost_multiplier"`
	PricingIdentity        string                    `json:"pricing_identity,omitempty"`
	PricingHash            string                    `json:"pricing_hash,omitempty"`
}

type RoutingCostComparison struct {
	ProfileSource   string                           `json:"profile_source"`
	ModelName       string                           `json:"model_name"`
	PoolID          int                              `json:"pool_id"`
	PricingEpoch    uint64                           `json:"pricing_epoch"`
	PricingHash     string                           `json:"pricing_hash"`
	GeneratedAt     int64                            `json:"generated_at"`
	QuantitySources map[string]string                `json:"quantity_sources"`
	Candidates      []RoutingCostComparisonCandidate `json:"candidates"`
}

func ListCostCatalogPoolSummaries(
	search string,
	offset int,
	limit int,
) ([]CostCatalogPoolSummary, int, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return nil, 0, SnapshotMetadata{}, false
	}
	search = strings.ToLower(strings.TrimSpace(search))
	offset, limit = normalizePageWindow(offset, limit)
	items := make([]CostCatalogPoolSummary, 0, limit)
	total := 0
	for poolIndex := range snapshot.view.Pools {
		pool := snapshot.view.Pools[poolIndex]
		if search != "" && !strings.Contains(strings.ToLower(pool.GroupName+" "+pool.DisplayName), search) {
			continue
		}
		item := CostCatalogPoolSummary{
			PoolID: pool.ID, GroupName: pool.GroupName, DisplayName: pool.DisplayName,
			MemberCount: len(pool.Members),
		}
		models := make(map[string]struct{})
		for memberIndex := range pool.Members {
			for modelIndex := range pool.Members[memberIndex].Models {
				observation := pool.Members[memberIndex].Models[modelIndex]
				models[observation.ModelName] = struct{}{}
				if observation.CostPricing != nil {
					item.KnownContractCount++
				} else {
					item.UnknownContractCount++
				}
			}
		}
		item.ModelCount = len(models)
		if total >= offset && len(items) < limit {
			items = append(items, item)
		}
		total++
	}
	return items, total, snapshotMetadata(snapshot.view), true
}

func ListCostCatalogMemberSummaries(
	poolID int,
	search string,
	offset int,
	limit int,
) ([]CostCatalogMemberSummary, int, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || poolID <= 0 {
		return nil, 0, SnapshotMetadata{}, false
	}
	poolIndex, exists := snapshot.poolIndexByID[poolID]
	if !exists {
		return []CostCatalogMemberSummary{}, 0, snapshotMetadata(snapshot.view), true
	}
	search = strings.ToLower(strings.TrimSpace(search))
	offset, limit = normalizePageWindow(offset, limit)
	pool := snapshot.view.Pools[poolIndex]
	items := make([]CostCatalogMemberSummary, 0, limit)
	total := 0
	for memberIndex := range pool.Members {
		member := pool.Members[memberIndex]
		if search != "" && !strings.Contains(strings.ToLower(member.ChannelName), search) {
			continue
		}
		item := CostCatalogMemberSummary{
			PoolID: pool.ID, MemberID: member.ID, ChannelID: member.ChannelID,
			RoutingIdentity: routingIdentityForSnapshotMember(snapshot, member), RoutingGeneration: member.ChannelGeneration,
			ChannelName: member.ChannelName, ChannelType: member.ChannelType,
			PhysicalStatus: member.PhysicalStatus, ModelCount: len(member.Models),
		}
		for modelIndex := range member.Models {
			observation := member.Models[modelIndex]
			if observation.CostPricing != nil {
				item.KnownContractCount++
			} else {
				item.UnknownContractCount++
			}
			item.ConfigurationRevision = max(item.ConfigurationRevision, observation.ChannelConfigurationRevision)
			item.UpstreamCostMultiplier = observation.CostUpstreamMultiplier
		}
		if total >= offset && len(items) < limit {
			items = append(items, item)
		}
		total++
	}
	return items, total, snapshotMetadata(snapshot.view), true
}

func ListCostCatalogModelSummaries(
	poolID int,
	memberID int,
	search string,
	offset int,
	limit int,
) ([]CostCatalogModelSummary, int, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || poolID <= 0 || memberID <= 0 {
		return nil, 0, SnapshotMetadata{}, false
	}
	poolIndex, exists := snapshot.poolIndexByID[poolID]
	if !exists {
		return []CostCatalogModelSummary{}, 0, snapshotMetadata(snapshot.view), true
	}
	search = strings.ToLower(strings.TrimSpace(search))
	offset, limit = normalizePageWindow(offset, limit)
	pool := snapshot.view.Pools[poolIndex]
	for memberIndex := range pool.Members {
		member := pool.Members[memberIndex]
		if member.ID != memberID {
			continue
		}
		items := make([]CostCatalogModelSummary, 0, limit)
		total := 0
		for modelIndex := range member.Models {
			observation := member.Models[modelIndex]
			if search != "" && !strings.Contains(strings.ToLower(observation.ModelName+" "+observation.UpstreamModelName), search) {
				continue
			}
			if total >= offset && len(items) < limit {
				configured, free, mode, currency := routingPricingDimensionSummary(observation.CostPricing)
				items = append(items, CostCatalogModelSummary{
					PoolID: pool.ID, MemberID: member.ID, ChannelID: member.ChannelID,
					RoutingGeneration: member.ChannelGeneration, ModelName: observation.ModelName,
					UpstreamModelName: observation.UpstreamModelName, Known: observation.CostPricing != nil,
					UnknownReason: observation.CostUnknownReason, BillingMode: observation.CostBillingMode,
					Currency: currency, ContractMode: mode, ConfiguredDimensions: configured,
					ExplicitFreeDimensions: free, ConfigurationRevision: observation.ChannelConfigurationRevision,
					UpstreamCostMultiplier: observation.CostUpstreamMultiplier,
					PricingIdentity:        observation.CostPricingIdentity,
				})
			}
			total++
		}
		return items, total, snapshotMetadata(snapshot.view), true
	}
	return []CostCatalogModelSummary{}, 0, snapshotMetadata(snapshot.view), true
}

func CompareRoutingCosts(request RoutingCostComparisonRequest) (RoutingCostComparison, error) {
	snapshot := currentSnapshot.Load()
	request.ModelName = strings.TrimSpace(request.ModelName)
	request.ProfileSource = strings.TrimSpace(request.ProfileSource)
	if snapshot == nil {
		return RoutingCostComparison{}, ErrRoutingCostComparisonUnavailable
	}
	if request.PoolID <= 0 || request.ModelName == "" || len(request.ModelName) > 256 ||
		len(request.MemberIDs) > RoutingCostComparisonMaxCandidates {
		return RoutingCostComparison{}, ErrRoutingCostCatalogInvalid
	}
	if err := model.ValidateRoutingCostRequestProfile(request.Profile); err != nil {
		return RoutingCostComparison{}, ErrRoutingCostCatalogInvalid
	}
	if request.AtUnix <= 0 {
		request.AtUnix = time.Now().Unix()
	}
	poolIndex, exists := snapshot.poolIndexByID[request.PoolID]
	if !exists {
		return RoutingCostComparison{}, ErrRoutingCostCatalogInvalid
	}
	allowedMembers := make(map[int]struct{}, len(request.MemberIDs))
	for _, memberID := range request.MemberIDs {
		if memberID <= 0 {
			return RoutingCostComparison{}, ErrRoutingCostCatalogInvalid
		}
		allowedMembers[memberID] = struct{}{}
	}
	result := RoutingCostComparison{
		ProfileSource: request.ProfileSource, ModelName: request.ModelName, PoolID: request.PoolID,
		PricingEpoch: snapshot.view.PricingEpoch, PricingHash: snapshot.view.PricingHash,
		GeneratedAt: request.AtUnix, QuantitySources: cloneRoutingCostQuantitySources(request.QuantitySources),
		Candidates: []RoutingCostComparisonCandidate{},
	}
	pool := snapshot.view.Pools[poolIndex]
	for memberIndex := range pool.Members {
		member := pool.Members[memberIndex]
		if len(allowedMembers) > 0 {
			if _, allowed := allowedMembers[member.ID]; !allowed {
				continue
			}
		}
		if len(result.Candidates) >= RoutingCostComparisonMaxCandidates {
			break
		}
		for modelIndex := range member.Models {
			observation := member.Models[modelIndex]
			if observation.ModelName != request.ModelName {
				continue
			}
			estimate, resolved, _, estimateErr := estimateModelSnapshotRoutingCost(observation, request.Profile, request.AtUnix)
			if estimateErr != nil {
				return RoutingCostComparison{}, estimateErr
			}
			beforeMultiplier := model.RoutingCostEstimate{}
			if resolved.CostPricing != nil {
				baselinePricing := *resolved.CostPricing
				one := 1.0
				baselinePricing.GroupRatio = &one
				version := model.RoutingCostSnapshotVersion{
					SourceType: SystemRoutingPricingSourceType, ObservedTime: resolved.CostObservedTime,
					EffectiveTime: resolved.CostEffectiveTime, ExpiresTime: resolved.CostExpiresTime,
					Confidence: resolved.CostVersionConfidence, ConfidenceScore: resolved.CostConfidenceScore,
					Freshness: resolved.CostFreshness, FreshnessScore: resolved.CostFreshnessScore,
				}
				beforeMultiplier, estimateErr = model.EstimateRoutingCostSnapshot(
					version, baselinePricing, request.Profile, request.AtUnix,
				)
				if estimateErr != nil {
					return RoutingCostComparison{}, estimateErr
				}
			}
			candidate := RoutingCostComparisonCandidate{
				PoolID: pool.ID, MemberID: member.ID, ChannelID: member.ChannelID,
				RoutingIdentity: routingIdentityForSnapshotMember(snapshot, member), RoutingGeneration: member.ChannelGeneration,
				ChannelName: member.ChannelName, ModelName: observation.ModelName,
				UpstreamModelName: observation.UpstreamModelName,
				Comparable:        estimate.ExpectedEffectiveKnown, MissingContext: append([]string(nil), estimate.MissingContext...),
				UnknownReason: estimate.UnknownReason, SingleAttempt: estimate, BeforeMultiplier: beforeMultiplier,
				UpstreamCostMultiplier: resolved.CostUpstreamMultiplier,
				PricingIdentity:        resolved.CostPricingIdentity, PricingHash: resolved.CostPricingHash,
			}
			if candidate.UnknownReason == "" && !candidate.Comparable {
				candidate.UnknownReason = resolved.CostUnknownReason
			}
			result.Candidates = append(result.Candidates, candidate)
			break
		}
	}
	sort.SliceStable(result.Candidates, func(left int, right int) bool {
		leftCandidate := result.Candidates[left]
		rightCandidate := result.Candidates[right]
		if leftCandidate.Comparable != rightCandidate.Comparable {
			return leftCandidate.Comparable
		}
		if leftCandidate.Comparable && leftCandidate.SingleAttempt.ExpectedEffectiveCost != rightCandidate.SingleAttempt.ExpectedEffectiveCost {
			return leftCandidate.SingleAttempt.ExpectedEffectiveCost < rightCandidate.SingleAttempt.ExpectedEffectiveCost
		}
		return leftCandidate.MemberID < rightCandidate.MemberID
	})
	return result, nil
}

func RoutingCostProfileFromDecisionContext(
	ctx context.Context,
	decisionID string,
) (model.RoutingCostRequestProfile, string, error) {
	decisionID = strings.TrimSpace(decisionID)
	if ctx == nil {
		ctx = context.Background()
	}
	if decisionID == "" || len(decisionID) > 64 {
		return model.RoutingCostRequestProfile{}, "", ErrRoutingCostCatalogInvalid
	}
	var audit model.RoutingDecisionAudit
	err := model.DB.WithContext(ctx).Where("decision_id = ?", decisionID).First(&audit).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.RoutingCostRequestProfile{}, "", ErrRoutingCostProfileNotFound
	}
	if err != nil {
		return model.RoutingCostRequestProfile{}, "", err
	}
	var profile RequestProfile
	if audit.RequestProfileJSON == "" || common.UnmarshalJsonStr(audit.RequestProfileJSON, &profile) != nil ||
		validateRequestProfileSchema(profile) != nil {
		return model.RoutingCostRequestProfile{}, "", ErrRoutingCostProfileNotFound
	}
	costProfile := model.RoutingCostRequestProfile{
		MaxAttempts: 1, KnowledgeSpecified: true,
		InputTokensKnown:             requestQuantityKnown(profile.InputTokens),
		MaximumCompletionKnown:       requestQuantityKnown(profile.OutputTokens),
		CacheReadTokensKnown:         requestQuantityKnown(profile.CachedTokens),
		CacheWriteTokensKnown:        false,
		CacheWriteOneHourTokensKnown: false,
		ImageInputTokensKnown:        profile.InputModalities&RequestModalityImage == 0,
		ImageOutputTokensKnown:       profile.OutputModalities&RequestModalityImage == 0,
		ImageUnitsKnown:              requestQuantityResolved(profile.ImageUnits),
		AudioInputTokensKnown:        profile.InputModalities&RequestModalityAudio == 0,
		AudioOutputTokensKnown:       profile.OutputModalities&RequestModalityAudio == 0,
		AudioDurationKnown:           requestQuantityResolved(profile.AudioMillis),
		VideoDurationKnown:           requestQuantityResolved(profile.VideoMillis),
		TaskUnitsKnown:               true,
		RequestInputKnown:            false, RequestPricingFeaturesKnown: true,
	}
	if costProfile.InputTokensKnown {
		costProfile.PromptTokens = profile.InputTokens.Value
		costProfile.MaximumPromptTokens = profile.InputTokens.Value
	}
	if costProfile.MaximumCompletionKnown {
		costProfile.ExpectedCompletionTokens = profile.OutputTokens.Value
		costProfile.MaximumCompletionTokens = profile.OutputTokens.Value
	}
	if costProfile.CacheReadTokensKnown {
		costProfile.CacheReadTokens = profile.CachedTokens.Value
	}
	if requestQuantityKnown(profile.ImageUnits) {
		costProfile.ImageUnits = float64(profile.ImageUnits.Value)
	}
	if requestQuantityKnown(profile.AudioMillis) {
		costProfile.AudioSeconds = float64(profile.AudioMillis.Value) / 1_000
	}
	if requestQuantityKnown(profile.VideoMillis) {
		costProfile.VideoSeconds = float64(profile.VideoMillis.Value) / 1_000
	}
	if profile.RequestKind == RequestKindTask {
		costProfile.TaskUnits = 1
	}
	switch profile.RequestKind {
	case RequestKindImage, RequestKindAudio, RequestKindTask, RequestKindMidjourney, RequestKindSuno:
		costProfile.UncataloguedSurchargePossible = true
	}
	return costProfile, profile.ModelName, nil
}

func routingPricingDimensionSummary(pricing *model.RoutingNormalizedPricing) ([]string, []string, string, string) {
	if pricing == nil {
		return []string{}, []string{}, "", ""
	}
	mode := "legacy"
	if pricing.ContractV2 != nil {
		mode = pricing.ContractV2.Mode
	}
	type dimension struct {
		name  string
		value *float64
	}
	dimensions := []dimension{
		{name: "input_tokens", value: pricing.InputCostPerMillion},
		{name: "output_tokens", value: pricing.OutputCostPerMillion},
		{name: "cache_read_tokens", value: pricing.CacheReadCostPerMillion},
		{name: "cache_write_tokens", value: pricing.CacheWriteCostPerMillion},
		{name: "cache_write_1h_tokens", value: pricing.CacheWrite1hCostPerMillion},
		{name: "image_input_tokens", value: pricing.ImageInputCostPerMillion},
		{name: "image_output_tokens", value: pricing.ImageOutputCostPerMillion},
		{name: "image_units", value: pricing.PerImageCost},
		{name: "audio_input_tokens", value: pricing.AudioInputCostPerMillion},
		{name: "audio_output_tokens", value: pricing.AudioOutputCostPerMillion},
		{name: "audio_seconds", value: pricing.AudioCostPerSecond},
		{name: "video_seconds", value: pricing.VideoCostPerSecond},
		{name: "task_units", value: pricing.PerTaskCost},
		{name: "request", value: pricing.PerRequestCost},
	}
	configured := make([]string, 0, len(dimensions)+1)
	free := make([]string, 0, len(dimensions))
	for _, dimension := range dimensions {
		if dimension.value == nil {
			continue
		}
		configured = append(configured, dimension.name)
		if *dimension.value == 0 {
			free = append(free, dimension.name)
		}
	}
	if strings.TrimSpace(pricing.BillingExpression) != "" {
		configured = append(configured, "expression")
	}
	return configured, free, mode, pricing.Currency
}

func requestQuantityKnown(quantity *RequestQuantity) bool {
	return quantity != nil && quantity.State == RequestQuantityKnown
}

func requestQuantityResolved(quantity *RequestQuantity) bool {
	return quantity != nil && quantity.State != RequestQuantityUnknown
}

func cloneRoutingCostQuantitySources(source map[string]string) map[string]string {
	if len(source) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			cloned[key] = value
		}
	}
	return cloned
}
