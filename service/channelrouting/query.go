package channelrouting

import (
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type SnapshotMetadata struct {
	Revision              uint64        `json:"revision"`
	RuntimeGeneration     uint64        `json:"runtime_generation"`
	PolicyHash            string        `json:"policy_hash"`
	ActivationID          int64         `json:"activation_id"`
	ActivationStage       string        `json:"activation_stage"`
	TrafficBasisPoints    int           `json:"traffic_basis_points"`
	ActivationCreatedTime int64         `json:"activation_created_time"`
	NodeEpochID           string        `json:"node_epoch_id"`
	BuiltAtUnix           int64         `json:"built_at"`
	BuildDurationMs       int64         `json:"build_duration_ms"`
	Stats                 SnapshotStats `json:"stats"`
}

type TelemetryAggregate struct {
	ObservedRequests      int64
	ObservedSuccesses     int64
	OutputTokens          int64
	GenerationMs          int64
	P95TTFTMs             float64
	P95TTFTKnown          bool
	MaxMemberP95TTFTMs    float64
	MaxMemberP95TTFTKnown bool
}

type CostSnapshotItem struct {
	PoolID       int     `json:"pool_id"`
	GroupName    string  `json:"group_name"`
	MemberID     int     `json:"member_id"`
	ChannelID    int     `json:"channel_id"`
	ChannelName  string  `json:"channel_name"`
	ModelName    string  `json:"model_name"`
	Known        bool    `json:"known"`
	Cost         float64 `json:"cost"`
	Confidence   string  `json:"confidence"`
	SnapshotTime int64   `json:"snapshot_time"`
}

type PoolSnapshotSummary struct {
	ID                int     `json:"id"`
	GroupName         string  `json:"group_name"`
	DisplayName       string  `json:"display_name"`
	Source            string  `json:"source"`
	DeploymentStage   string  `json:"deployment_stage"`
	PolicyProfile     string  `json:"policy_profile"`
	MemberCount       int     `json:"member_count"`
	EnabledChannels   int     `json:"enabled_channels"`
	TelemetryCoverage float64 `json:"telemetry_coverage"`
	OpenModels        int     `json:"open_models"`
	DegradedModels    int     `json:"degraded_models"`
	KnownCostModels   int     `json:"known_cost_models"`
}

func CurrentSnapshotMetadata() (SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return SnapshotMetadata{}, false
	}
	return snapshotMetadata(snapshot.view), true
}

func snapshotMetadata(view SnapshotView) SnapshotMetadata {
	stats := view.Stats
	if stats.UnknownClassificationRate != nil {
		value := *stats.UnknownClassificationRate
		stats.UnknownClassificationRate = &value
	}
	return SnapshotMetadata{
		Revision:              view.Revision,
		RuntimeGeneration:     view.RuntimeGeneration,
		PolicyHash:            view.PolicyHash,
		ActivationID:          view.ActivationID,
		ActivationStage:       view.ActivationStage,
		TrafficBasisPoints:    view.TrafficBasisPoints,
		ActivationCreatedTime: view.ActivationCreatedTime,
		NodeEpochID:           NodeEpochID(),
		BuiltAtUnix:           view.BuiltAtUnix,
		BuildDurationMs:       view.BuildDurationMs,
		Stats:                 stats,
	}
}

func CurrentTelemetryAggregate() (TelemetryAggregate, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return TelemetryAggregate{}, false
	}
	return snapshot.telemetrySummary, true
}

func CurrentSnapshotSummary() (SnapshotMetadata, TelemetryAggregate, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return SnapshotMetadata{}, TelemetryAggregate{}, false
	}
	return snapshotMetadata(snapshot.view), snapshot.telemetrySummary, true
}

func CurrentPoolDeploymentStage(groupName string) (string, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || groupName == "" {
		return "", false
	}
	poolID, exists := snapshot.poolByGroup[groupName]
	if !exists {
		return "", false
	}
	poolIndex, exists := snapshot.poolIndexByID[poolID]
	if !exists || poolIndex < 0 || poolIndex >= len(snapshot.view.Pools) {
		return "", false
	}
	pool := snapshot.view.Pools[poolIndex]
	if pool.ID != poolID || pool.GroupName != groupName {
		return "", false
	}
	return pool.DeploymentStage, true
}

func ListPoolSnapshotSummaries(search string, offset int, limit int) ([]PoolSnapshotSummary, int, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return nil, 0, SnapshotMetadata{}, false
	}
	search = strings.ToLower(strings.TrimSpace(search))
	offset, limit = normalizePageWindow(offset, limit)
	items := make([]PoolSnapshotSummary, 0, limit)
	total := 0
	for index := range snapshot.poolSummaries {
		pool := snapshot.poolSummaries[index]
		if search != "" && !strings.Contains(strings.ToLower(pool.GroupName+" "+pool.DisplayName), search) {
			continue
		}
		if total >= offset && len(items) < limit {
			items = append(items, pool)
		}
		total++
	}
	return items, total, snapshotMetadata(snapshot.view), true
}

func GetPoolSnapshotSummary(id int) (PoolSnapshotSummary, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || id <= 0 {
		return PoolSnapshotSummary{}, SnapshotMetadata{}, false
	}
	poolIndex, exists := snapshot.poolIndexByID[id]
	if !exists || poolIndex < 0 || poolIndex >= len(snapshot.poolSummaries) {
		return PoolSnapshotSummary{}, snapshotMetadata(snapshot.view), false
	}
	return snapshot.poolSummaries[poolIndex], snapshotMetadata(snapshot.view), true
}

func GetPoolSnapshotPage(
	id int,
	memberOffset int,
	memberLimit int,
	modelLimit int,
	credentialLimit int,
) (PoolSnapshot, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || id <= 0 {
		return PoolSnapshot{}, SnapshotMetadata{}, false
	}
	poolIndex, exists := snapshot.poolIndexByID[id]
	if !exists || poolIndex < 0 || poolIndex >= len(snapshot.view.Pools) {
		return PoolSnapshot{}, snapshotMetadata(snapshot.view), false
	}
	source := snapshot.view.Pools[poolIndex]
	memberOffset, memberLimit = normalizePageWindow(memberOffset, memberLimit)
	modelLimit = normalizeNestedLimit(modelLimit)
	credentialLimit = normalizeNestedLimit(credentialLimit)
	start := min(memberOffset, len(source.Members))
	end := min(start+memberLimit, len(source.Members))
	result := source
	result.MemberCount = len(source.Members)
	result.MembersTruncated = start > 0 || end < len(source.Members)
	result.Members = make([]PoolMemberSnapshot, 0, end-start)
	for index := start; index < end; index++ {
		member := source.Members[index]
		member.CredentialCount = len(source.Members[index].CredentialIDs)
		credentialEnd := min(credentialLimit, len(source.Members[index].CredentialIDs))
		member.CredentialsTruncated = credentialEnd < len(source.Members[index].CredentialIDs)
		member.CredentialIDs = append([]int(nil), source.Members[index].CredentialIDs[:credentialEnd]...)
		member.ModelCount = len(source.Members[index].Models)
		modelEnd := min(modelLimit, len(source.Members[index].Models))
		member.ModelsTruncated = modelEnd < len(source.Members[index].Models)
		member.Models = append([]ModelSnapshot(nil), source.Members[index].Models[:modelEnd]...)
		result.Members = append(result.Members, member)
	}
	return result, snapshotMetadata(snapshot.view), true
}

func telemetryAggregate(view SnapshotView) TelemetryAggregate {
	aggregate := TelemetryAggregate{
		P95TTFTMs:    view.AggregateP95TTFTMs,
		P95TTFTKnown: view.AggregateP95TTFTKnown,
	}
	scalarP95Count := 0
	scalarP95 := 0.0
	incompleteTTFTDistribution := false
	for _, pool := range view.Pools {
		for _, member := range pool.Members {
			for _, observation := range member.Models {
				if !observation.MetricKnown {
					continue
				}
				aggregate.ObservedRequests = addPositiveInt64(aggregate.ObservedRequests, observation.RequestCount)
				aggregate.ObservedSuccesses = addPositiveInt64(aggregate.ObservedSuccesses, observation.SuccessCount)
				aggregate.OutputTokens = addPositiveInt64(aggregate.OutputTokens, observation.OutputTokens)
				aggregate.GenerationMs = addPositiveInt64(aggregate.GenerationMs, observation.GenerationMs)
				if observation.P95TTFTKnown {
					scalarP95Count++
					scalarP95 = observation.P95TTFTMs
				}
				if observation.MetricSource == "stable_rollup" && observation.ttftCount > 0 && !observation.P95TTFTKnown {
					incompleteTTFTDistribution = true
				}
				if observation.P95TTFTKnown && (!aggregate.MaxMemberP95TTFTKnown || observation.P95TTFTMs > aggregate.MaxMemberP95TTFTMs) {
					aggregate.MaxMemberP95TTFTMs = observation.P95TTFTMs
					aggregate.MaxMemberP95TTFTKnown = true
				}
			}
		}
	}
	if !aggregate.P95TTFTKnown && scalarP95Count == 1 && !incompleteTTFTDistribution {
		aggregate.P95TTFTMs = scalarP95
		aggregate.P95TTFTKnown = true
	}
	return aggregate
}

func ListPoolSnapshots(search string, offset int, limit int) ([]PoolSnapshot, int, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return nil, 0, SnapshotMetadata{}, false
	}
	search = strings.ToLower(strings.TrimSpace(search))
	offset, limit = normalizePageWindow(offset, limit)
	items := make([]PoolSnapshot, 0, limit)
	total := 0
	for _, pool := range snapshot.view.Pools {
		if search != "" && !strings.Contains(strings.ToLower(pool.GroupName+" "+pool.DisplayName), search) {
			continue
		}
		if total >= offset && len(items) < limit {
			items = append(items, clonePoolSnapshot(pool))
		}
		total++
	}
	return items, total, snapshotMetadata(snapshot.view), true
}

func GetPoolSnapshot(id int) (PoolSnapshot, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || id <= 0 {
		return PoolSnapshot{}, SnapshotMetadata{}, false
	}
	for _, pool := range snapshot.view.Pools {
		if pool.ID == id {
			return clonePoolSnapshot(pool), snapshotMetadata(snapshot.view), true
		}
	}
	return PoolSnapshot{}, snapshotMetadata(snapshot.view), false
}

func ListChannelSnapshots(search string, status *int, channelType *int, offset int, limit int) ([]ChannelSnapshot, int, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return nil, 0, SnapshotMetadata{}, false
	}
	search = strings.ToLower(strings.TrimSpace(search))
	offset, limit = normalizePageWindow(offset, limit)
	items := make([]ChannelSnapshot, 0, limit)
	total := 0
	for _, channel := range snapshot.view.Channels {
		if search != "" && !strings.Contains(strings.ToLower(channel.Name+" "+channel.Endpoint), search) {
			continue
		}
		if status != nil && channel.Status != *status {
			continue
		}
		if channelType != nil && channel.Type != *channelType {
			continue
		}
		if total >= offset && len(items) < limit {
			item := channel
			item.CredentialIDs = append([]int(nil), channel.CredentialIDs...)
			items = append(items, item)
		}
		total++
	}
	return items, total, snapshotMetadata(snapshot.view), true
}

func ListCostSnapshots(group string, modelFilter string, known *bool, offset int, limit int) ([]CostSnapshotItem, int, SnapshotMetadata, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return nil, 0, SnapshotMetadata{}, false
	}
	group = strings.TrimSpace(group)
	modelFilter = strings.ToLower(strings.TrimSpace(modelFilter))
	offset, limit = normalizePageWindow(offset, limit)
	items := make([]CostSnapshotItem, 0, limit)
	total := 0
	for _, pool := range snapshot.view.Pools {
		if group != "" && pool.GroupName != group {
			continue
		}
		for _, member := range pool.Members {
			for _, observation := range member.Models {
				if modelFilter != "" && !strings.Contains(strings.ToLower(observation.ModelName), modelFilter) {
					continue
				}
				if known != nil && observation.CostKnown != *known {
					continue
				}
				if total >= offset && len(items) < limit {
					items = append(items, CostSnapshotItem{
						PoolID:       pool.ID,
						GroupName:    pool.GroupName,
						MemberID:     member.ID,
						ChannelID:    member.ChannelID,
						ChannelName:  member.ChannelName,
						ModelName:    observation.ModelName,
						Known:        observation.CostKnown,
						Cost:         observation.Cost,
						Confidence:   observation.CostConfidence,
						SnapshotTime: observation.CostUpdatedUnix,
					})
				}
				total++
			}
		}
	}
	return items, total, snapshotMetadata(snapshot.view), true
}

func clonePoolSnapshot(source PoolSnapshot) PoolSnapshot {
	result := source
	result.Members = append([]PoolMemberSnapshot(nil), source.Members...)
	for index := range result.Members {
		result.Members[index].CredentialIDs = append([]int(nil), source.Members[index].CredentialIDs...)
		result.Members[index].Models = append([]ModelSnapshot(nil), source.Members[index].Models...)
	}
	return result
}

func summarizePoolSnapshot(pool PoolSnapshot) PoolSnapshotSummary {
	item := PoolSnapshotSummary{
		ID:              pool.ID,
		GroupName:       pool.GroupName,
		DisplayName:     pool.DisplayName,
		Source:          pool.Source,
		DeploymentStage: pool.DeploymentStage,
		PolicyProfile:   pool.PolicyProfile,
		MemberCount:     len(pool.Members),
	}
	telemetryMembers := 0
	for memberIndex := range pool.Members {
		member := pool.Members[memberIndex]
		if member.PhysicalStatus == common.ChannelStatusEnabled {
			item.EnabledChannels++
		}
		if member.TelemetryKnown {
			telemetryMembers++
		}
		for modelIndex := range member.Models {
			observation := member.Models[modelIndex]
			switch observation.BreakerState {
			case model.RoutingBreakerStateOpen, model.RoutingBreakerStateHalfOpen:
				item.OpenModels++
			case model.RoutingBreakerStateDegraded:
				item.DegradedModels++
			}
			if observation.CostKnown {
				item.KnownCostModels++
			}
		}
	}
	if item.MemberCount > 0 {
		item.TelemetryCoverage = float64(telemetryMembers) / float64(item.MemberCount)
	}
	return item
}

func normalizePageWindow(offset int, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	return offset, limit
}

func normalizeNestedLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func addPositiveInt64(current int64, value int64) int64 {
	if value <= 0 {
		return current
	}
	if current > math.MaxInt64-value {
		return math.MaxInt64
	}
	return current + value
}
