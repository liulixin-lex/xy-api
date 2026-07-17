package channelrouting

import (
	"math"
	"sort"
	"strings"
)

const (
	ChannelSummaryCredentialLimit = 20
	ChannelSummaryModelLimit      = 50
)

type ChannelSnapshotSummary struct {
	ID                     int                       `json:"id"`
	RoutingIdentity        string                    `json:"routing_identity"`
	RoutingGeneration      string                    `json:"routing_generation"`
	Name                   string                    `json:"name"`
	Type                   int                       `json:"type"`
	Status                 int                       `json:"status"`
	Endpoint               string                    `json:"endpoint,omitempty"`
	EndpointAuthority      string                    `json:"endpoint_authority"`
	Region                 string                    `json:"region"`
	EndpointState          EndpointBreakerSourceView `json:"endpoint_state"`
	MultiKey               bool                      `json:"multi_key"`
	CredentialCount        int                       `json:"credential_count"`
	CredentialsTruncated   bool                      `json:"credentials_truncated"`
	CredentialIDs          []int                     `json:"credential_ids"`
	ModelCount             int                       `json:"model_count"`
	ModelsTruncated        bool                      `json:"models_truncated"`
	Models                 []string                  `json:"models"`
	AuthFailure            bool                      `json:"auth_failure"`
	AuthFailureUpdatedAt   int64                     `json:"auth_failure_updated_at"`
	BalanceKnown           bool                      `json:"balance_known"`
	Balance                float64                   `json:"balance"`
	BalanceUpdatedAt       int64                     `json:"balance_updated_at"`
	ConfigurationRevision  int64                     `json:"configuration_revision"`
	UpstreamCostMultiplier float64                   `json:"upstream_cost_multiplier"`
	CostSource             string                    `json:"cost_source"`
	CostConfirmed          bool                      `json:"cost_confirmed"`
	CostBasisAvailable     bool                      `json:"cost_basis_available"`
	EffectiveModelCount    int                       `json:"effective_model_count"`
	TrafficClass           string                    `json:"traffic_class"`
	FailureDomainLabel     string                    `json:"failure_domain_label"`
	FailureDomainStatus    string                    `json:"failure_domain_status"`
}

type CostSnapshotSummary struct {
	PoolID                 int      `json:"pool_id"`
	GroupName              string   `json:"group_name"`
	MemberID               int      `json:"member_id"`
	ChannelID              int      `json:"channel_id"`
	RoutingIdentity        string   `json:"routing_identity"`
	RoutingGeneration      string   `json:"routing_generation"`
	ChannelName            string   `json:"channel_name"`
	ModelName              string   `json:"model_name"`
	Known                  bool     `json:"known"`
	Cost                   float64  `json:"cost,omitempty"`
	BillingMode            string   `json:"billing_mode,omitempty"`
	Currency               string   `json:"currency,omitempty"`
	Unit                   string   `json:"unit,omitempty"`
	DisplayRate            *float64 `json:"display_rate,omitempty"`
	DisplayRateBasis       string   `json:"display_rate_basis,omitempty"`
	ExpressionPricing      bool     `json:"expression_pricing"`
	Version                string   `json:"version,omitempty"`
	PricingVersion         string   `json:"pricing_version,omitempty"`
	PricingIdentity        string   `json:"pricing_identity,omitempty"`
	UnknownReason          string   `json:"unknown_reason,omitempty"`
	ConfigurationRevision  int64    `json:"configuration_revision"`
	UpstreamCostMultiplier float64  `json:"upstream_cost_multiplier"`
	UpstreamGroup          string   `json:"upstream_group,omitempty"`
	UpstreamModel          string   `json:"upstream_model,omitempty"`
	ObservedTime           int64    `json:"observed_time,omitempty"`
	EffectiveTime          int64    `json:"effective_time,omitempty"`
	ExpiresTime            int64    `json:"expires_time,omitempty"`
	Confidence             string   `json:"confidence"`
	ConfidenceScore        float64  `json:"confidence_score"`
	Freshness              string   `json:"freshness"`
	FreshnessScore         float64  `json:"freshness_score"`
	SnapshotTime           int64    `json:"snapshot_time"`
}

func ListRiskPoolSnapshotSummaries(limit int) ([]PoolSnapshotSummary, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return nil, SnapshotMetadata{}, false
	}
	_, limit = normalizePageWindow(0, limit)
	items := make([]PoolSnapshotSummary, 0, len(snapshot.poolSummaries))
	for index := range snapshot.poolSummaries {
		item := snapshot.poolSummaries[index]
		if item.MemberCount == 0 || item.EnabledChannels == 0 || item.OpenModels > 0 || item.DegradedModels > 0 ||
			item.TelemetryCoverage < 1 || item.UnknownCostModels > 0 {
			items = append(items, item)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
		if left.OpenModels != right.OpenModels {
			return left.OpenModels > right.OpenModels
		}
		if left.DegradedModels != right.DegradedModels {
			return left.DegradedModels > right.DegradedModels
		}
		if (left.EnabledChannels == 0) != (right.EnabledChannels == 0) {
			return left.EnabledChannels == 0
		}
		if left.TelemetryCoverage != right.TelemetryCoverage {
			return left.TelemetryCoverage < right.TelemetryCoverage
		}
		if left.UnknownCostModels != right.UnknownCostModels {
			return left.UnknownCostModels > right.UnknownCostModels
		}
		if left.GroupName != right.GroupName {
			return left.GroupName < right.GroupName
		}
		return left.ID < right.ID
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, snapshotMetadata(snapshot.view), true
}

func ListChannelSnapshotSummaries(
	search string,
	status *int,
	channelType *int,
	offset int,
	limit int,
) ([]ChannelSnapshotSummary, int, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return nil, 0, SnapshotMetadata{}, false
	}
	search = strings.ToLower(strings.TrimSpace(search))
	offset, limit = normalizePageWindow(offset, limit)
	selected := make([]ChannelSnapshot, 0, limit)
	total := 0
	for index := range snapshot.view.Channels {
		channel := snapshot.view.Channels[index]
		if search != "" && !strings.Contains(strings.ToLower(channel.Name+" "+channel.Endpoint), search) {
			continue
		}
		if status != nil && channel.Status != *status {
			continue
		}
		if channelType != nil && channel.Type != *channelType {
			continue
		}
		if total >= offset && len(selected) < limit {
			selected = append(selected, channel)
		}
		total++
	}

	region := RoutingRegion()
	endpointStates := make(map[string]EndpointBreakerSourceView)
	for _, view := range ListEndpointBreakerViews() {
		endpointStates[view.EndpointAuthority+"\x00"+view.Region] = view.Effective
	}
	items := make([]ChannelSnapshotSummary, 0, len(selected))
	for index := range selected {
		channel := selected[index]
		authority := EndpointAuthority(channel.Endpoint, channel.ID)
		credentialEnd := min(ChannelSummaryCredentialLimit, len(channel.CredentialIDs))
		modelEnd := min(ChannelSummaryModelLimit, len(channel.ModelNames))
		items = append(items, ChannelSnapshotSummary{
			ID: channel.ID, RoutingIdentity: channel.RoutingIdentity,
			RoutingGeneration: channel.RoutingGeneration,
			Name:              channel.Name, Type: channel.Type, Status: channel.Status,
			Endpoint: channel.Endpoint, EndpointAuthority: authority, Region: region,
			EndpointState: endpointStates[authority+"\x00"+region], MultiKey: channel.MultiKey,
			CredentialCount: len(channel.CredentialIDs), CredentialsTruncated: credentialEnd < len(channel.CredentialIDs),
			CredentialIDs: append([]int(nil), channel.CredentialIDs[:credentialEnd]...),
			ModelCount:    len(channel.ModelNames), ModelsTruncated: modelEnd < len(channel.ModelNames),
			Models:      append([]string(nil), channel.ModelNames[:modelEnd]...),
			AuthFailure: channel.AuthFailure, AuthFailureUpdatedAt: channel.AuthFailureUpdatedAt,
			BalanceKnown: channel.BalanceKnown, Balance: channel.Balance, BalanceUpdatedAt: channel.BalanceUpdatedAt,
			ConfigurationRevision:  channel.ConfigurationRevision,
			UpstreamCostMultiplier: channel.UpstreamCostMultiplier,
			CostSource:             channel.CostSource, CostConfirmed: channel.CostConfirmed,
			CostBasisAvailable:  channel.CostBasisAvailable,
			EffectiveModelCount: channel.EffectiveModelCount,
			TrafficClass:        channel.TrafficClass, FailureDomainLabel: channel.FailureDomainLabel,
			FailureDomainStatus: channel.FailureDomainStatus,
		})
	}
	return items, total, snapshotMetadata(snapshot.view), true
}

func ListCostSnapshotSummaries(
	group string,
	modelFilter string,
	known *bool,
	offset int,
	limit int,
) ([]CostSnapshotSummary, int, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return nil, 0, SnapshotMetadata{}, false
	}
	group = strings.TrimSpace(group)
	modelFilter = strings.ToLower(strings.TrimSpace(modelFilter))
	offset, limit = normalizePageWindow(offset, limit)
	items := make([]CostSnapshotSummary, 0, limit)
	total := 0
	for poolIndex := range snapshot.view.Pools {
		pool := snapshot.view.Pools[poolIndex]
		if group != "" && pool.GroupName != group {
			continue
		}
		for memberIndex := range pool.Members {
			member := pool.Members[memberIndex]
			for modelIndex := range member.Models {
				observation := member.Models[modelIndex]
				if modelFilter != "" && !strings.Contains(strings.ToLower(observation.ModelName), modelFilter) {
					continue
				}
				costKnown := observation.CostKnown || observation.CostPricing != nil
				if known != nil && costKnown != *known {
					continue
				}
				if total >= offset && len(items) < limit {
					items = append(items, costSnapshotSummary(
						pool, member, routingIdentityForSnapshotMember(snapshot, member), observation,
					))
				}
				total++
			}
		}
	}
	return items, total, snapshotMetadata(snapshot.view), true
}

func GetCostSnapshotDetail(
	poolID int,
	memberID int,
	modelName string,
) (CostSnapshotItem, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || poolID <= 0 || memberID <= 0 || modelName == "" {
		return CostSnapshotItem{}, SnapshotMetadata{}, false
	}
	poolIndex, exists := snapshot.poolIndexByID[poolID]
	if !exists || poolIndex < 0 || poolIndex >= len(snapshot.view.Pools) {
		return CostSnapshotItem{}, snapshotMetadata(snapshot.view), false
	}
	pool := snapshot.view.Pools[poolIndex]
	for memberIndex := range pool.Members {
		member := pool.Members[memberIndex]
		if member.ID != memberID {
			continue
		}
		for modelIndex := range member.Models {
			observation := member.Models[modelIndex]
			if observation.ModelName == modelName {
				return costSnapshotDetail(
					pool, member, routingIdentityForSnapshotMember(snapshot, member), observation,
				), snapshotMetadata(snapshot.view), true
			}
		}
		return CostSnapshotItem{}, snapshotMetadata(snapshot.view), false
	}
	return CostSnapshotItem{}, snapshotMetadata(snapshot.view), false
}

func costSnapshotSummary(
	pool PoolSnapshot,
	member PoolMemberSnapshot,
	routingIdentity string,
	observation ModelSnapshot,
) CostSnapshotSummary {
	item := CostSnapshotSummary{
		PoolID: pool.ID, GroupName: pool.GroupName, MemberID: member.ID, ChannelID: member.ChannelID,
		RoutingIdentity: routingIdentity, RoutingGeneration: member.ChannelGeneration,
		ChannelName: member.ChannelName, ModelName: observation.ModelName,
		Known: observation.CostKnown || observation.CostPricing != nil, Cost: observation.Cost,
		BillingMode: observation.CostBillingMode,
		Version:     observation.CostPricingHash, PricingVersion: observation.CostPricingVersion,
		PricingIdentity: observation.CostPricingIdentity, UnknownReason: observation.CostUnknownReason,
		ConfigurationRevision:  observation.ChannelConfigurationRevision,
		UpstreamCostMultiplier: observation.CostUpstreamMultiplier,
		UpstreamGroup:          observation.CostUpstreamGroup, UpstreamModel: observation.CostUpstreamModel,
		ObservedTime: observation.CostObservedTime, EffectiveTime: observation.CostEffectiveTime,
		ExpiresTime: observation.CostExpiresTime, Confidence: observation.CostConfidence,
		ConfidenceScore: observation.CostConfidenceScore, Freshness: observation.CostFreshness,
		FreshnessScore: observation.CostFreshnessScore, SnapshotTime: observation.CostUpdatedUnix,
	}
	if observation.CostPricing != nil {
		item.BillingMode = observation.CostPricing.BillingMode
		item.Currency = observation.CostPricing.Currency
		item.Unit = observation.CostPricing.Unit
		item.Confidence = observation.CostVersionConfidence
		item.SnapshotTime = observation.CostObservedTime
		item.ExpressionPricing = strings.TrimSpace(observation.CostPricing.BillingExpression) != ""
		rates := []struct {
			value *float64
			basis string
		}{
			{value: observation.CostPricing.PerRequestCost, basis: "per_request"},
			{value: observation.CostPricing.ModelPrice, basis: "model_price"},
			{value: observation.CostPricing.InputCostPerMillion, basis: "input_per_million"},
		}
		for _, rate := range rates {
			if rate.value != nil && !math.IsNaN(*rate.value) && !math.IsInf(*rate.value, 0) && *rate.value >= 0 {
				value := *rate.value
				item.DisplayRate = &value
				item.DisplayRateBasis = rate.basis
				break
			}
		}
	}
	return item
}

func costSnapshotDetail(
	pool PoolSnapshot,
	member PoolMemberSnapshot,
	routingIdentity string,
	observation ModelSnapshot,
) CostSnapshotItem {
	summary := costSnapshotSummary(pool, member, routingIdentity, observation)
	item := CostSnapshotItem{
		PoolID: summary.PoolID, GroupName: summary.GroupName, MemberID: summary.MemberID,
		ChannelID: summary.ChannelID, RoutingIdentity: summary.RoutingIdentity,
		RoutingGeneration: summary.RoutingGeneration, ChannelName: summary.ChannelName, ModelName: summary.ModelName,
		Known: summary.Known, Cost: summary.Cost, Currency: summary.Currency, Unit: summary.Unit,
		Version: summary.Version, PricingVersion: summary.PricingVersion,
		PricingIdentity: summary.PricingIdentity, UnknownReason: summary.UnknownReason,
		ConfigurationRevision:  summary.ConfigurationRevision,
		UpstreamCostMultiplier: summary.UpstreamCostMultiplier, UpstreamGroup: summary.UpstreamGroup,
		UpstreamModel: summary.UpstreamModel, ObservedTime: summary.ObservedTime,
		EffectiveTime: summary.EffectiveTime, ExpiresTime: summary.ExpiresTime,
		Confidence: summary.Confidence, ConfidenceScore: summary.ConfidenceScore,
		Freshness: summary.Freshness, FreshnessScore: summary.FreshnessScore, SnapshotTime: summary.SnapshotTime,
	}
	if observation.CostPricing != nil {
		pricing := *observation.CostPricing
		pricing.Tiers = append([]byte(nil), observation.CostPricing.Tiers...)
		pricing.Extras = append([]byte(nil), observation.CostPricing.Extras...)
		item.Pricing = &pricing
	}
	return item
}
