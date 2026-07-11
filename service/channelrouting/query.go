package channelrouting

import (
	"math"
	"strings"
)

type SnapshotMetadata struct {
	Revision        uint64        `json:"revision"`
	BuiltAtUnix     int64         `json:"built_at"`
	BuildDurationMs int64         `json:"build_duration_ms"`
	Stats           SnapshotStats `json:"stats"`
}

type TelemetryAggregate struct {
	ObservedRequests   int64
	ObservedSuccesses  int64
	OutputTokens       int64
	GenerationMs       int64
	MaxMemberP95TTFTMs float64
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
		Revision:        view.Revision,
		BuiltAtUnix:     view.BuiltAtUnix,
		BuildDurationMs: view.BuildDurationMs,
		Stats:           stats,
	}
}

func CurrentTelemetryAggregate() (TelemetryAggregate, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return TelemetryAggregate{}, false
	}
	return telemetryAggregate(snapshot.view), true
}

func CurrentSnapshotSummary() (SnapshotMetadata, TelemetryAggregate, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return SnapshotMetadata{}, TelemetryAggregate{}, false
	}
	return snapshotMetadata(snapshot.view), telemetryAggregate(snapshot.view), true
}

func telemetryAggregate(view SnapshotView) TelemetryAggregate {
	var aggregate TelemetryAggregate
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
				if observation.P95TTFTMs > aggregate.MaxMemberP95TTFTMs {
					aggregate.MaxMemberP95TTFTMs = observation.P95TTFTMs
				}
			}
		}
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

func addPositiveInt64(current int64, value int64) int64 {
	if value <= 0 {
		return current
	}
	if current > math.MaxInt64-value {
		return math.MaxInt64
	}
	return current + value
}
