package channelrouting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"gorm.io/gorm"
)

var ErrSnapshotLimitExceeded = errors.New("channel routing snapshot limit exceeded")

type SnapshotLimits struct {
	MaxPools                int
	MaxMembers              int
	MaxCredentials          int
	MaxChannels             int
	MaxModelsPerChannel     int
	MaxTotalModelSnapshots  int
	MaxModelBytesPerChannel int
	MaxTotalChannelBytes    int
	MaxTotalBindingBytes    int
	MaxMetricAggregates     int
}

var DefaultSnapshotLimits = SnapshotLimits{
	MaxPools:                4_096,
	MaxMembers:              100_000,
	MaxCredentials:          100_000,
	MaxChannels:             100_000,
	MaxModelsPerChannel:     512,
	MaxTotalModelSnapshots:  200_000,
	MaxModelBytesPerChannel: 1 << 20,
	MaxTotalChannelBytes:    64 << 20,
	MaxTotalBindingBytes:    16 << 20,
	MaxMetricAggregates:     model.RoutingMetricRollupMaxQueryLimit,
}

type SnapshotStats struct {
	PoolCount                 int                        `json:"pool_count"`
	MemberCount               int                        `json:"member_count"`
	CredentialCount           int                        `json:"credential_count"`
	ChannelCount              int                        `json:"channel_count"`
	ModelSnapshotCount        int                        `json:"model_snapshot_count"`
	MembersWithTelemetry      int                        `json:"members_with_telemetry"`
	ChannelsWithCredentials   int                        `json:"channels_with_credentials"`
	TelemetryCoverage         float64                    `json:"telemetry_coverage"`
	CredentialCoverage        float64                    `json:"credential_coverage"`
	UnknownClassificationRate *float64                   `json:"unknown_classification_rate,omitempty"`
	InvalidNumericValues      int                        `json:"invalid_numeric_values"`
	Hotcache                  routinghotcache.Stats      `json:"hotcache"`
	StableTelemetry           routingmetrics.StableStats `json:"stable_telemetry"`
}

type SnapshotView struct {
	Revision        uint64            `json:"revision"`
	BuiltAtUnix     int64             `json:"built_at"`
	BuildDurationMs int64             `json:"build_duration_ms"`
	Pools           []PoolSnapshot    `json:"pools"`
	Channels        []ChannelSnapshot `json:"channels"`
	Stats           SnapshotStats     `json:"stats"`
}

type PoolSnapshot struct {
	ID          int                  `json:"id"`
	GroupName   string               `json:"group_name"`
	DisplayName string               `json:"display_name"`
	Source      string               `json:"source"`
	Members     []PoolMemberSnapshot `json:"members"`
}

type PoolMemberSnapshot struct {
	ID             int             `json:"id"`
	PoolID         int             `json:"pool_id"`
	ChannelID      int             `json:"channel_id"`
	ChannelName    string          `json:"channel_name"`
	ChannelType    int             `json:"channel_type"`
	PhysicalStatus int             `json:"physical_status"`
	LegacyPriority int64           `json:"legacy_priority"`
	LegacyWeight   int64           `json:"legacy_weight"`
	MultiKey       bool            `json:"multi_key"`
	CredentialIDs  []int           `json:"credential_ids"`
	Models         []ModelSnapshot `json:"models"`
	TelemetryKnown bool            `json:"telemetry_known"`
}

type ModelSnapshot struct {
	ModelName                  string  `json:"model_name"`
	MetricKnown                bool    `json:"metric_known"`
	MetricSource               string  `json:"metric_source,omitempty"`
	RequestCount               int64   `json:"request_count"`
	SuccessCount               int64   `json:"success_count"`
	FailureCount               int64   `json:"failure_count"`
	UnknownClassificationCount int64   `json:"unknown_classification_count"`
	ReliabilityRequestCount    int64   `json:"reliability_request_count"`
	ReliabilityFailureCount    int64   `json:"reliability_failure_count"`
	AverageLatencyMs           float64 `json:"average_latency_ms"`
	AverageTTFTMs              float64 `json:"average_ttft_ms"`
	P95LatencyMs               float64 `json:"p95_latency_ms"`
	P95TTFTMs                  float64 `json:"p95_ttft_ms"`
	OutputTokens               int64   `json:"output_tokens"`
	GenerationMs               int64   `json:"generation_ms"`
	OutputTokensPerSecond      float64 `json:"output_tokens_per_second"`
	Err4xx                     int64   `json:"err_4xx"`
	Err5xx                     int64   `json:"err_5xx"`
	Err429                     int64   `json:"err_429"`
	Err529                     int64   `json:"err_529"`
	AverageRetryAfterMs        float64 `json:"average_retry_after_ms"`
	MetricUpdatedUnix          int64   `json:"metric_updated_at"`
	BreakerKnown               bool    `json:"breaker_known"`
	BreakerState               string  `json:"breaker_state"`
	BreakerReason              string  `json:"breaker_reason"`
	BreakerCooldownUntil       int64   `json:"breaker_cooldown_until"`
	CapacityLimited            bool    `json:"capacity_limited"`
	CapacityStatusCode         int     `json:"capacity_status_code"`
	CapacityCooldownUntilMs    int64   `json:"capacity_cooldown_until_ms"`
	CostKnown                  bool    `json:"cost_known"`
	Cost                       float64 `json:"cost"`
	CostConfidence             string  `json:"cost_confidence"`
	CostUpdatedUnix            int64   `json:"cost_updated_at"`
}

type ChannelSnapshot struct {
	ID                   int     `json:"id"`
	Name                 string  `json:"name"`
	Type                 int     `json:"type"`
	Status               int     `json:"status"`
	Endpoint             string  `json:"endpoint,omitempty"`
	MultiKey             bool    `json:"multi_key"`
	CredentialIDs        []int   `json:"credential_ids"`
	AuthFailure          bool    `json:"auth_failure"`
	AuthFailureUpdatedAt int64   `json:"auth_failure_updated_at"`
	BalanceKnown         bool    `json:"balance_known"`
	Balance              float64 `json:"balance"`
	BalanceUpdatedAt     int64   `json:"balance_updated_at"`
	CostConnectorEnabled bool    `json:"cost_connector_enabled"`
	CostSyncFailures     int     `json:"cost_sync_failures"`
	CostSyncBackoffUntil int64   `json:"cost_sync_backoff_until"`
	CostSyncError        string  `json:"cost_sync_error,omitempty"`
}

type Identity struct {
	SnapshotRevision uint64 `json:"snapshot_revision"`
	PoolID           int    `json:"pool_id"`
	MemberID         int    `json:"member_id"`
	CredentialID     int    `json:"credential_id"`
}

type runtimeSnapshot struct {
	view                    SnapshotView
	poolByGroup             map[string]int
	memberByPoolChannel     map[poolChannelKey]int
	credentialByFingerprint map[credentialFingerprintKey]int
	modelByMemberModel      map[memberModelKey]ModelSnapshot
}

type poolChannelKey struct {
	PoolID    int
	ChannelID int
}

type credentialFingerprintKey struct {
	ChannelID   int
	Fingerprint string
}

type memberModelKey struct {
	memberID int
	model    string
}

type stableMetricKey struct {
	memberID int
	model    string
}

type activeMetricMember struct {
	poolID    int
	channelID int
}

type stableMetricAggregate struct {
	requestCount            int64
	successCount            int64
	failureCount            int64
	unknownCount            int64
	reliabilityRequestCount int64
	reliabilityFailureCount int64
	totalLatencyMs          int64
	ttftSumMs               int64
	ttftCount               int64
	outputTokens            int64
	generationMs            int64
	err4xx                  int64
	err5xx                  int64
	err429                  int64
	err529                  int64
	retryAfterCount         int64
	retryAfterTotalMs       int64
	latestBucketTs          int64
	invalidNumericValues    int
}

type persistedMetricAggregate struct {
	MemberID                int
	ModelName               string
	LatestBucketTs          int64
	RequestCount            int64
	SuccessCount            int64
	FailureCount            int64
	UnknownCount            int64
	ReliabilityRequestCount int64
	ReliabilityFailureCount int64
	TotalLatencyMs          int64
	TtftSumMs               int64
	TtftCount               int64
	OutputTokens            int64
	GenerationMs            int64
	Err4xx                  int64 `gorm:"column:err_4xx"`
	Err5xx                  int64 `gorm:"column:err_5xx"`
	Err429                  int64 `gorm:"column:err_429"`
	Err529                  int64 `gorm:"column:err_529"`
	RetryAfterCount         int64
	RetryAfterTotalMs       int64
}

var (
	currentSnapshot   atomic.Pointer[runtimeSnapshot]
	snapshotRevision  atomic.Uint64
	snapshotPublishMu sync.Mutex
)

func RefreshSnapshotContext(ctx context.Context) (SnapshotView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := lockRoutingTelemetry(ctx); err != nil {
		return SnapshotView{}, err
	}
	defer unlockRoutingTelemetry()

	snapshot, err := buildSnapshotContext(ctx, model.DB, DefaultSnapshotLimits)
	if err != nil {
		return SnapshotView{}, err
	}

	snapshotPublishMu.Lock()
	snapshot.view.Revision = snapshotRevision.Add(1)
	currentSnapshot.Store(snapshot)
	snapshotPublishMu.Unlock()

	return cloneSnapshotView(snapshot.view), nil
}

func CurrentSnapshot() (SnapshotView, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil {
		return SnapshotView{}, false
	}
	return cloneSnapshotView(snapshot.view), true
}

func ResolveIdentity(group string, channelID int, credential string) (Identity, bool) {
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
	identity := Identity{
		SnapshotRevision: snapshot.view.Revision,
		PoolID:           poolID,
		MemberID:         memberID,
	}
	if credential == "" {
		return identity, true
	}
	fingerprint, err := model.RoutingCredentialFingerprint(channelID, credential)
	if err != nil {
		return identity, true
	}
	credentialID, ok := snapshot.credentialByFingerprint[credentialFingerprintKey{
		ChannelID:   channelID,
		Fingerprint: fingerprint,
	}]
	if !ok {
		return identity, true
	}
	identity.CredentialID = credentialID
	return identity, true
}

func ResolveObserveModelSnapshot(group string, channelID int, modelName string) (ModelSnapshot, Identity, bool) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || group == "" || channelID <= 0 || modelName == "" {
		return ModelSnapshot{}, Identity{}, false
	}
	poolID, ok := snapshot.poolByGroup[group]
	if !ok {
		return ModelSnapshot{}, Identity{}, false
	}
	memberID, ok := snapshot.memberByPoolChannel[poolChannelKey{PoolID: poolID, ChannelID: channelID}]
	if !ok {
		return ModelSnapshot{}, Identity{}, false
	}
	observation, ok := snapshot.modelByMemberModel[memberModelKey{memberID: memberID, model: modelName}]
	if !ok {
		return ModelSnapshot{}, Identity{}, false
	}
	return observation, Identity{
		SnapshotRevision: snapshot.view.Revision,
		PoolID:           poolID,
		MemberID:         memberID,
	}, true
}

func buildSnapshotContext(ctx context.Context, db *gorm.DB, limits SnapshotLimits) (*runtimeSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil {
		return nil, errors.New("channel routing snapshot database is nil")
	}
	if err := validateSnapshotLimits(limits); err != nil {
		return nil, err
	}
	var snapshot *runtimeSnapshot
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var buildErr error
		snapshot, buildErr = buildSnapshotWithinTransaction(ctx, tx, limits)
		return buildErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return snapshot, err
}

func buildSnapshotWithinTransaction(ctx context.Context, db *gorm.DB, limits SnapshotLimits) (*runtimeSnapshot, error) {
	started := time.Now()

	var pools []model.RoutingPool
	if err := db.WithContext(ctx).Where("active = ?", true).Order("id asc").Limit(limits.MaxPools + 1).Find(&pools).Error; err != nil {
		return nil, err
	}
	if len(pools) > limits.MaxPools {
		return nil, fmt.Errorf("%w: pools", ErrSnapshotLimitExceeded)
	}

	var members []model.RoutingPoolMember
	if err := db.WithContext(ctx).Where("active = ?", true).Order("id asc").Limit(limits.MaxMembers + 1).Find(&members).Error; err != nil {
		return nil, err
	}
	if len(members) > limits.MaxMembers {
		return nil, fmt.Errorf("%w: members", ErrSnapshotLimitExceeded)
	}

	var credentials []model.RoutingCredentialRef
	if err := db.WithContext(ctx).Where("active = ?", true).Order("id asc").Limit(limits.MaxCredentials + 1).Find(&credentials).Error; err != nil {
		return nil, err
	}
	if len(credentials) > limits.MaxCredentials {
		return nil, fmt.Errorf("%w: credentials", ErrSnapshotLimitExceeded)
	}
	activeMembers := make(map[int]activeMetricMember, len(members))
	for _, member := range members {
		activeMembers[member.ID] = activeMetricMember{poolID: member.PoolID, channelID: member.ChannelID}
	}
	activeCredentialChannels := make(map[int]int, len(credentials))
	channelsWithActiveCredentials := make(map[int]struct{}, len(credentials))
	for _, credential := range credentials {
		activeCredentialChannels[credential.ID] = credential.ChannelID
		channelsWithActiveCredentials[credential.ChannelID] = struct{}{}
	}

	setting := smart_routing_setting.GetSetting()
	metricCutoff := int64(0)
	nowUnix := common.GetTimestamp()
	staleSeconds := int64(setting.SnapshotStaleSec)
	metricWindow := staleSeconds
	bucketSeconds := int64(setting.MetricBucketSec)
	if bucketSeconds > math.MaxInt64/5 {
		metricWindow = math.MaxInt64
	} else if bucketWindow := bucketSeconds * 5; bucketWindow > metricWindow {
		metricWindow = bucketWindow
	}
	if metricWindow > 0 && metricWindow < nowUnix {
		metricCutoff = nowUnix - metricWindow
	}
	var persistedRollups []persistedMetricAggregate
	if err := db.WithContext(ctx).Table("routing_metric_rollups AS metric_rollups").
		Select(
			"metric_rollups.member_id, MIN(metric_rollups.model_name) AS model_name, "+
				"MAX(metric_rollups.bucket_ts) AS latest_bucket_ts, "+
				"SUM(metric_rollups.request_count) AS request_count, SUM(metric_rollups.success_count) AS success_count, "+
				"SUM(metric_rollups.failure_count) AS failure_count, SUM(metric_rollups.unknown_count) AS unknown_count, "+
				"SUM(metric_rollups.reliability_request_count) AS reliability_request_count, "+
				"SUM(metric_rollups.reliability_failure_count) AS reliability_failure_count, "+
				"SUM(metric_rollups.total_latency_ms) AS total_latency_ms, SUM(metric_rollups.ttft_sum_ms) AS ttft_sum_ms, "+
				"SUM(metric_rollups.ttft_count) AS ttft_count, SUM(metric_rollups.output_tokens) AS output_tokens, "+
				"SUM(metric_rollups.generation_ms) AS generation_ms, SUM(metric_rollups.err_4xx) AS err_4xx, "+
				"SUM(metric_rollups.err_5xx) AS err_5xx, SUM(metric_rollups.err_429) AS err_429, "+
				"SUM(metric_rollups.err_529) AS err_529, SUM(metric_rollups.retry_after_count) AS retry_after_count, "+
				"SUM(metric_rollups.retry_after_total_ms) AS retry_after_total_ms",
		).
		Joins("JOIN routing_pool_members AS metric_members ON metric_members.id = metric_rollups.member_id AND metric_members.active = ?", true).
		Joins("LEFT JOIN routing_credential_refs AS metric_credentials ON metric_credentials.id = metric_rollups.credential_id AND metric_credentials.channel_id = metric_members.channel_id").
		Where(
			"metric_rollups.bucket_ts >= ? AND ((metric_rollups.credential_id = 0 AND NOT EXISTS ("+
				"SELECT 1 FROM routing_credential_refs AS current_credentials "+
				"WHERE current_credentials.channel_id = metric_members.channel_id AND current_credentials.active = ?"+
				")) OR metric_credentials.active = ?)",
			metricCutoff,
			true,
			true,
		).
		Order("metric_rollups.member_id asc").
		Order("metric_rollups.model_key asc").
		Group("metric_rollups.member_id, metric_rollups.model_key").
		Limit(limits.MaxMetricAggregates + 1).
		Scan(&persistedRollups).Error; err != nil {
		return nil, err
	}
	if len(persistedRollups) > limits.MaxMetricAggregates {
		return nil, fmt.Errorf("%w: metric aggregates", ErrSnapshotLimitExceeded)
	}
	liveRollups := routingmetrics.StableSnapshots()
	stableMetrics := aggregateStableMetrics(
		persistedRollups,
		liveRollups,
		metricCutoff,
		activeMembers,
		activeCredentialChannels,
		channelsWithActiveCredentials,
	)
	if len(stableMetrics) > limits.MaxMetricAggregates {
		return nil, fmt.Errorf("%w: persisted and live metric aggregates", ErrSnapshotLimitExceeded)
	}

	channels := make([]model.Channel, 0)
	credentialRequiredByChannel := make(map[int]bool)
	lastChannelID := 0
	totalChannelBytes := 0
	for len(channels) <= limits.MaxChannels {
		var page []model.Channel
		query := db.WithContext(ctx).
			Select("id", "type", "status", "name", "key", "base_url", "balance", "balance_updated_time", "models", "channel_info").
			Order("id asc").Limit(500)
		if lastChannelID > 0 {
			query = query.Where("id > ?", lastChannelID)
		}
		if err := query.Find(&page).Error; err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for index := range page {
			channelInfo, err := common.Marshal(page[index].ChannelInfo)
			if err != nil {
				return nil, err
			}
			channelBytes := len(page[index].Name) + len(page[index].Key) + len(page[index].Models) + len(channelInfo)
			if page[index].BaseURL != nil {
				channelBytes += len(*page[index].BaseURL)
			}
			totalChannelBytes += channelBytes
			if totalChannelBytes > limits.MaxTotalChannelBytes {
				return nil, fmt.Errorf("%w: total channel bytes", ErrSnapshotLimitExceeded)
			}
			credentialRequiredByChannel[page[index].Id] = strings.TrimSpace(page[index].Key) != ""
			page[index].Key = ""
			page[index].ChannelInfo = model.ChannelInfo{IsMultiKey: page[index].ChannelInfo.IsMultiKey}
		}
		channels = append(channels, page...)
		lastChannelID = page[len(page)-1].Id
		if len(page) < 500 {
			break
		}
	}
	if len(channels) > limits.MaxChannels {
		return nil, fmt.Errorf("%w: channels", ErrSnapshotLimitExceeded)
	}

	bindings := make([]model.RoutingChannelBinding, 0)
	lastBindingChannelID := 0
	totalBindingBytes := 0
	for len(bindings) <= limits.MaxChannels {
		var page []model.RoutingChannelBinding
		query := db.WithContext(ctx).
			Select("channel_id", "enabled", "sync_failure_count", "sync_backoff_until", "last_sync_error").
			Order("channel_id asc").Limit(500)
		if lastBindingChannelID > 0 {
			query = query.Where("channel_id > ?", lastBindingChannelID)
		}
		if err := query.Find(&page).Error; err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for index := range page {
			if page[index].LastSyncError != nil {
				totalBindingBytes += len(*page[index].LastSyncError)
			}
			if totalBindingBytes > limits.MaxTotalBindingBytes {
				return nil, fmt.Errorf("%w: total binding bytes", ErrSnapshotLimitExceeded)
			}
		}
		bindings = append(bindings, page...)
		lastBindingChannelID = page[len(page)-1].ChannelID
		if len(page) < 500 {
			break
		}
	}
	if len(bindings) > limits.MaxChannels {
		return nil, fmt.Errorf("%w: cost bindings", ErrSnapshotLimitExceeded)
	}

	channelByID := make(map[int]model.Channel, len(channels))
	modelNamesByChannel := make(map[int][]string, len(channels))
	for _, channel := range channels {
		if len(channel.Models) > limits.MaxModelBytesPerChannel {
			return nil, fmt.Errorf("%w: channel %d model bytes", ErrSnapshotLimitExceeded, channel.Id)
		}
		modelNames := normalizedModels(channel.GetModels())
		if len(modelNames) > limits.MaxModelsPerChannel {
			return nil, fmt.Errorf("%w: channel %d models", ErrSnapshotLimitExceeded, channel.Id)
		}
		channelByID[channel.Id] = channel
		modelNamesByChannel[channel.Id] = modelNames
	}

	credentialsByChannel := make(map[int][]int)
	credentialByFingerprint := make(map[credentialFingerprintKey]int, len(credentials))
	for _, credential := range credentials {
		credentialsByChannel[credential.ChannelID] = append(credentialsByChannel[credential.ChannelID], credential.ID)
		credentialByFingerprint[credentialFingerprintKey{
			ChannelID:   credential.ChannelID,
			Fingerprint: credential.Fingerprint,
		}] = credential.ID
	}
	for channelID := range credentialsByChannel {
		sort.Ints(credentialsByChannel[channelID])
	}

	bindingByChannel := make(map[int]model.RoutingChannelBinding, len(bindings))
	for _, binding := range bindings {
		bindingByChannel[binding.ChannelID] = binding
	}

	channelViews := make([]ChannelSnapshot, 0, len(channels))
	invalidNumericValues := 0
	for _, channel := range channels {
		view := ChannelSnapshot{
			ID:            channel.Id,
			Name:          channel.Name,
			Type:          channel.Type,
			Status:        channel.Status,
			Endpoint:      safeEndpoint(channel.BaseURL),
			MultiKey:      channel.ChannelInfo.IsMultiKey,
			CredentialIDs: append([]int(nil), credentialsByChannel[channel.Id]...),
		}
		if authFailure, ok := routinghotcache.GetAuthFailure(channel.Id); ok {
			view.AuthFailure = authFailure.Marked
			view.AuthFailureUpdatedAt = authFailure.UpdatedUnix
		}
		if balance, ok := routinghotcache.GetBalance(channel.Id); ok {
			if balance.Known && finiteNumber(balance.Balance) {
				view.BalanceKnown = true
				view.Balance = balance.Balance
				view.BalanceUpdatedAt = balance.UpdatedUnix
			} else if balance.Known {
				invalidNumericValues++
			}
		} else if channel.BalanceUpdatedTime > 0 {
			if finiteNumber(channel.Balance) {
				view.BalanceKnown = true
				view.Balance = channel.Balance
				view.BalanceUpdatedAt = channel.BalanceUpdatedTime
			} else {
				invalidNumericValues++
			}
		}
		if binding, ok := bindingByChannel[channel.Id]; ok {
			view.CostConnectorEnabled = binding.Enabled
			view.CostSyncFailures = binding.SyncFailureCount
			view.CostSyncBackoffUntil = binding.SyncBackoffUntil
			if binding.LastSyncError != nil {
				view.CostSyncError = truncateRoutingSnapshotText(common.SanitizeErrorMessage(*binding.LastSyncError), 1_024)
			}
		}
		channelViews = append(channelViews, view)
	}

	poolByID := make(map[int]model.RoutingPool, len(pools))
	poolByGroup := make(map[string]int, len(pools))
	for _, pool := range pools {
		poolByID[pool.ID] = pool
		poolByGroup[pool.GroupName] = pool.ID
	}

	membersByPool := make(map[int][]model.RoutingPoolMember)
	memberByPoolChannel := make(map[poolChannelKey]int, len(members))
	for _, member := range members {
		if _, ok := poolByID[member.PoolID]; !ok {
			continue
		}
		if _, ok := channelByID[member.ChannelID]; !ok {
			continue
		}
		membersByPool[member.PoolID] = append(membersByPool[member.PoolID], member)
		memberByPoolChannel[poolChannelKey{PoolID: member.PoolID, ChannelID: member.ChannelID}] = member.ID
	}

	poolViews := make([]PoolSnapshot, 0, len(pools))
	modelByMemberModel := make(map[memberModelKey]ModelSnapshot, min(limits.MaxTotalModelSnapshots, len(members)*4))
	membersWithTelemetry := 0
	modelSnapshotCount := 0
	unknownFailedAttempts := int64(0)
	allFailedAttempts := int64(0)
	memberAssociatedChannels := make(map[int]struct{})
	credentialRequiredChannels := make(map[int]struct{})
	channelsWithCredentials := make(map[int]struct{})
	for _, pool := range pools {
		poolView := PoolSnapshot{
			ID:          pool.ID,
			GroupName:   pool.GroupName,
			DisplayName: pool.DisplayName,
			Source:      pool.Source,
			Members:     make([]PoolMemberSnapshot, 0, len(membersByPool[pool.ID])),
		}
		for _, member := range membersByPool[pool.ID] {
			channel := channelByID[member.ChannelID]
			memberAssociatedChannels[channel.Id] = struct{}{}
			if credentialRequiredByChannel[channel.Id] {
				credentialRequiredChannels[channel.Id] = struct{}{}
			}
			credentialIDs := append([]int(nil), credentialsByChannel[channel.Id]...)
			if len(credentialIDs) > 0 {
				channelsWithCredentials[channel.Id] = struct{}{}
			}
			memberView := PoolMemberSnapshot{
				ID:             member.ID,
				PoolID:         member.PoolID,
				ChannelID:      member.ChannelID,
				ChannelName:    channel.Name,
				ChannelType:    channel.Type,
				PhysicalStatus: channel.Status,
				LegacyPriority: member.LegacyPriority,
				LegacyWeight:   member.LegacyWeight,
				MultiKey:       channel.ChannelInfo.IsMultiKey,
				CredentialIDs:  credentialIDs,
				Models:         make([]ModelSnapshot, 0, len(modelNamesByChannel[channel.Id])),
			}
			for _, modelName := range modelNamesByChannel[channel.Id] {
				if modelSnapshotCount >= limits.MaxTotalModelSnapshots {
					return nil, fmt.Errorf("%w: model snapshots", ErrSnapshotLimitExceeded)
				}
				modelView, invalidValues := snapshotModel(channel, member.ID, pool.GroupName, modelName, stableMetrics)
				invalidNumericValues += invalidValues
				if modelView.MetricSource == "stable_rollup" {
					allFailedAttempts = saturatingMetricTotal(allFailedAttempts, modelView.FailureCount)
					unknownFailedAttempts = saturatingMetricTotal(unknownFailedAttempts, modelView.UnknownClassificationCount)
				}
				if modelView.MetricKnown {
					memberView.TelemetryKnown = true
				}
				memberView.Models = append(memberView.Models, modelView)
				modelByMemberModel[memberModelKey{memberID: member.ID, model: modelName}] = modelView
				modelSnapshotCount++
			}
			if memberView.TelemetryKnown {
				membersWithTelemetry++
			}
			poolView.Members = append(poolView.Members, memberView)
		}
		poolViews = append(poolViews, poolView)
	}

	stats := SnapshotStats{
		PoolCount:               len(poolViews),
		MemberCount:             len(memberByPoolChannel),
		CredentialCount:         len(credentials),
		ChannelCount:            len(channelViews),
		ModelSnapshotCount:      modelSnapshotCount,
		MembersWithTelemetry:    membersWithTelemetry,
		ChannelsWithCredentials: len(channelsWithCredentials),
		Hotcache:                routinghotcache.RuntimeStats(),
		StableTelemetry:         routingmetrics.StableRuntimeStats(),
		InvalidNumericValues:    invalidNumericValues,
	}
	if len(memberByPoolChannel) > 0 {
		stats.TelemetryCoverage = float64(membersWithTelemetry) / float64(len(memberByPoolChannel))
	}
	if len(credentialRequiredChannels) > 0 {
		stats.CredentialCoverage = float64(len(channelsWithCredentials)) / float64(len(credentialRequiredChannels))
	} else if len(memberAssociatedChannels) > 0 {
		stats.CredentialCoverage = 1
	}
	if allFailedAttempts > 0 {
		rate := float64(min(unknownFailedAttempts, allFailedAttempts)) / float64(allFailedAttempts)
		stats.UnknownClassificationRate = &rate
	}

	return &runtimeSnapshot{
		view: SnapshotView{
			BuiltAtUnix:     common.GetTimestamp(),
			BuildDurationMs: time.Since(started).Milliseconds(),
			Pools:           poolViews,
			Channels:        channelViews,
			Stats:           stats,
		},
		poolByGroup:             poolByGroup,
		memberByPoolChannel:     memberByPoolChannel,
		credentialByFingerprint: credentialByFingerprint,
		modelByMemberModel:      modelByMemberModel,
	}, ctx.Err()
}

func snapshotModel(
	channel model.Channel,
	memberID int,
	group string,
	modelName string,
	stableMetrics map[stableMetricKey]stableMetricAggregate,
) (ModelSnapshot, int) {
	view := ModelSnapshot{ModelName: modelName}
	invalidValues := 0
	if metric, ok := stableMetrics[stableMetricKey{memberID: memberID, model: modelName}]; ok && metric.requestCount > 0 {
		view.MetricKnown = true
		view.MetricSource = "stable_rollup"
		view.RequestCount = metric.requestCount
		view.SuccessCount = metric.successCount
		view.FailureCount = metric.failureCount
		view.UnknownClassificationCount = min(metric.unknownCount, metric.failureCount)
		view.ReliabilityRequestCount = metric.reliabilityRequestCount
		view.ReliabilityFailureCount = metric.reliabilityFailureCount
		view.OutputTokens = metric.outputTokens
		view.GenerationMs = metric.generationMs
		view.Err4xx = metric.err4xx
		view.Err5xx = metric.err5xx
		view.Err429 = metric.err429
		view.Err529 = metric.err529
		view.MetricUpdatedUnix = metric.latestBucketTs
		invalidValues += metric.invalidNumericValues
		view.AverageLatencyMs = finiteMetricRatio(metric.totalLatencyMs, metric.requestCount, 1)
		view.AverageTTFTMs = finiteMetricRatio(metric.ttftSumMs, metric.ttftCount, 1)
		view.OutputTokensPerSecond = finiteMetricRatio(metric.outputTokens, metric.generationMs, 1000)
		view.AverageRetryAfterMs = finiteMetricRatio(metric.retryAfterTotalMs, metric.retryAfterCount, 1)
		if metric.unknownCount > metric.failureCount {
			invalidValues++
		}
	}
	key := routinghotcache.Key{
		ChannelID:   channel.Id,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       modelName,
		Group:       group,
	}
	if !channel.ChannelInfo.IsMultiKey {
		if !view.MetricKnown {
			if metric, ok := routinghotcache.GetMetric(key); ok {
				view.MetricKnown = true
				view.MetricSource = "legacy_compat"
				view.RequestCount = max(metric.RequestCount, 0)
				view.SuccessCount = max(metric.SuccessCount, 0)
				view.FailureCount = max(view.RequestCount-view.SuccessCount, 0)
				view.ReliabilityRequestCount = max(metric.ReliabilityRequestCount, 0)
				view.ReliabilityFailureCount = max(metric.ReliabilityFailureCount, 0)
				view.OutputTokens = max(metric.OutputTokens, 0)
				view.GenerationMs = max(metric.GenerationMs, 0)
				if finiteNonNegative(metric.P95LatencyMs) {
					view.P95LatencyMs = metric.P95LatencyMs
				} else {
					invalidValues++
				}
				if finiteNonNegative(metric.P95TTFTMs) {
					view.P95TTFTMs = metric.P95TTFTMs
				} else {
					invalidValues++
				}
				if finiteNonNegative(metric.TPS) {
					view.OutputTokensPerSecond = metric.TPS
				} else {
					invalidValues++
				}
				view.MetricUpdatedUnix = metric.UpdatedUnix
			}
		}
		if breaker, ok := routinghotcache.GetBreaker(key); ok {
			view.BreakerKnown = true
			view.BreakerState = breaker.State
			view.BreakerReason = breaker.Reason
			view.BreakerCooldownUntil = breaker.CooldownUntilUnix
		}
		if capacity, ok := routinghotcache.GetCapacityCooldown(key); ok {
			view.CapacityLimited = capacity.CooldownUntilUnixMilli > time.Now().UnixMilli()
			view.CapacityStatusCode = capacity.SourceStatusCode
			view.CapacityCooldownUntilMs = capacity.CooldownUntilUnixMilli
		}
	}
	if cost, ok := routinghotcache.GetCost(key.CostKey()); ok {
		view.CostKnown = cost.Known && finiteNonNegative(cost.Cost)
		if view.CostKnown {
			view.Cost = cost.Cost
		} else if cost.Known {
			invalidValues++
		}
		view.CostConfidence = cost.Confidence
		view.CostUpdatedUnix = cost.UpdatedUnix
	}
	return view, invalidValues
}

func aggregateStableMetrics(
	persisted []persistedMetricAggregate,
	live []routingmetrics.StableSnapshot,
	cutoff int64,
	activeMembers map[int]activeMetricMember,
	activeCredentialChannels map[int]int,
	channelsWithActiveCredentials map[int]struct{},
) map[stableMetricKey]stableMetricAggregate {
	result := make(map[stableMetricKey]stableMetricAggregate, len(persisted)+len(live))
	for index := range persisted {
		rollup := persisted[index]
		if rollup.MemberID <= 0 || rollup.ModelName == "" || rollup.LatestBucketTs < cutoff {
			continue
		}
		key := stableMetricKey{memberID: rollup.MemberID, model: rollup.ModelName}
		aggregate := result[key]
		aggregate.addCounters(
			rollup.RequestCount,
			rollup.SuccessCount,
			rollup.FailureCount,
			rollup.UnknownCount,
			rollup.ReliabilityRequestCount,
			rollup.ReliabilityFailureCount,
			rollup.TotalLatencyMs,
			rollup.TtftSumMs,
			rollup.TtftCount,
			rollup.OutputTokens,
			rollup.GenerationMs,
			rollup.Err4xx,
			rollup.Err5xx,
			rollup.Err429,
			rollup.Err529,
			rollup.RetryAfterCount,
			rollup.RetryAfterTotalMs,
		)
		aggregate.latestBucketTs = max(aggregate.latestBucketTs, rollup.LatestBucketTs)
		result[key] = aggregate
	}
	for index := range live {
		snapshot := live[index]
		if snapshot.PoolMemberID <= 0 || snapshot.Model == "" || snapshot.BucketTs < cutoff {
			continue
		}
		member, active := activeMembers[snapshot.PoolMemberID]
		if !active || member.poolID != snapshot.PoolID || member.channelID != snapshot.ChannelID {
			continue
		}
		if snapshot.CredentialID > 0 && activeCredentialChannels[snapshot.CredentialID] != snapshot.ChannelID {
			continue
		}
		if snapshot.CredentialID == 0 {
			if _, keyed := channelsWithActiveCredentials[snapshot.ChannelID]; keyed {
				continue
			}
		}
		key := stableMetricKey{memberID: snapshot.PoolMemberID, model: snapshot.Model}
		aggregate := result[key]
		aggregate.addCounters(
			snapshot.RequestCount,
			snapshot.SuccessCount,
			snapshot.FailureCount,
			snapshot.UnknownClassificationCount,
			snapshot.ReliabilityRequestCount,
			snapshot.ReliabilityFailureCount,
			snapshot.TotalLatencyMs,
			snapshot.TtftSumMs,
			snapshot.TtftCount,
			snapshot.OutputTokens,
			snapshot.GenerationMs,
			snapshot.Err4xx,
			snapshot.Err5xx,
			snapshot.Err429,
			snapshot.Err529,
			snapshot.RetryAfterCount,
			snapshot.RetryAfterTotalMs,
		)
		aggregate.latestBucketTs = max(aggregate.latestBucketTs, snapshot.BucketTs)
		result[key] = aggregate
	}
	return result
}

func (aggregate *stableMetricAggregate) addCounters(values ...int64) {
	targets := []*int64{
		&aggregate.requestCount,
		&aggregate.successCount,
		&aggregate.failureCount,
		&aggregate.unknownCount,
		&aggregate.reliabilityRequestCount,
		&aggregate.reliabilityFailureCount,
		&aggregate.totalLatencyMs,
		&aggregate.ttftSumMs,
		&aggregate.ttftCount,
		&aggregate.outputTokens,
		&aggregate.generationMs,
		&aggregate.err4xx,
		&aggregate.err5xx,
		&aggregate.err429,
		&aggregate.err529,
		&aggregate.retryAfterCount,
		&aggregate.retryAfterTotalMs,
	}
	for index, value := range values {
		if value < 0 {
			aggregate.invalidNumericValues++
			continue
		}
		if *targets[index] > math.MaxInt64-value {
			*targets[index] = math.MaxInt64
			aggregate.invalidNumericValues++
			continue
		}
		*targets[index] += value
	}
}

func finiteMetricRatio(numerator int64, denominator int64, scale float64) float64 {
	if numerator <= 0 || denominator <= 0 || scale <= 0 {
		return 0
	}
	value := float64(numerator) * scale / float64(denominator)
	if !finiteNonNegative(value) {
		return 0
	}
	return value
}

func saturatingMetricTotal(current int64, delta int64) int64 {
	if delta <= 0 {
		return current
	}
	if current > math.MaxInt64-delta {
		return math.MaxInt64
	}
	return current + delta
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && finiteNumber(value)
}

func finiteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func truncateRoutingSnapshotText(value string, limit int) string {
	if limit < 1 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func validateSnapshotLimits(limits SnapshotLimits) error {
	if limits.MaxPools < 1 || limits.MaxMembers < 1 || limits.MaxCredentials < 1 || limits.MaxChannels < 1 ||
		limits.MaxModelsPerChannel < 1 || limits.MaxTotalModelSnapshots < 1 || limits.MaxModelBytesPerChannel < 1 ||
		limits.MaxTotalChannelBytes < 1 || limits.MaxTotalBindingBytes < 1 || limits.MaxMetricAggregates < 1 ||
		limits.MaxMetricAggregates > model.RoutingMetricRollupMaxQueryLimit {
		return errors.New("channel routing snapshot limits must be positive")
	}
	return nil
}

func normalizedModels(models []string) []string {
	unique := make(map[string]struct{}, len(models))
	for _, modelName := range models {
		modelName = strings.TrimSpace(modelName)
		if modelName == "" {
			continue
		}
		unique[modelName] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for modelName := range unique {
		result = append(result, modelName)
	}
	sort.Strings(result)
	return result
}

func safeEndpoint(raw *string) string {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(*raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func cloneSnapshotView(source SnapshotView) SnapshotView {
	result := source
	if source.Stats.UnknownClassificationRate != nil {
		value := *source.Stats.UnknownClassificationRate
		result.Stats.UnknownClassificationRate = &value
	}
	result.Channels = append([]ChannelSnapshot(nil), source.Channels...)
	for index := range result.Channels {
		result.Channels[index].CredentialIDs = append([]int(nil), source.Channels[index].CredentialIDs...)
	}
	result.Pools = append([]PoolSnapshot(nil), source.Pools...)
	for poolIndex := range result.Pools {
		result.Pools[poolIndex].Members = append([]PoolMemberSnapshot(nil), source.Pools[poolIndex].Members...)
		for memberIndex := range result.Pools[poolIndex].Members {
			member := &result.Pools[poolIndex].Members[memberIndex]
			sourceMember := source.Pools[poolIndex].Members[memberIndex]
			member.CredentialIDs = append([]int(nil), sourceMember.CredentialIDs...)
			member.Models = append([]ModelSnapshot(nil), sourceMember.Models...)
		}
	}
	return result
}

func ResetSnapshotForTest() {
	snapshotPublishMu.Lock()
	currentSnapshot.Store(nil)
	snapshotRevision.Store(0)
	snapshotPublishMu.Unlock()
}
