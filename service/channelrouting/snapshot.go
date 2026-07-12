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
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"gorm.io/gorm"
)

var (
	ErrSnapshotLimitExceeded    = errors.New("channel routing snapshot limit exceeded")
	ErrSnapshotPolicyReference  = errors.New("channel routing snapshot policy reference is invalid")
	ErrSnapshotActivation       = errors.New("channel routing snapshot activation is invalid")
	ErrSnapshotRevisionRollback = errors.New("channel routing snapshot revision cannot move backwards")
	ErrSnapshotRevisionConflict = errors.New("channel routing snapshot revision hash conflict")
)

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
	MaxMetricRollupRows     int
	MaxMetricRollupScanRows int
	MaxMetricSketchBytes    int
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
	MaxMetricRollupRows:     snapshotMetricRollupDefaultMaxRows,
	MaxMetricRollupScanRows: snapshotMetricRollupDefaultMaxScanRows,
	MaxMetricSketchBytes:    64 << 20,
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
	MetricTelemetryStatus     string                     `json:"metric_telemetry_status"`
	MetricTelemetryReason     string                     `json:"metric_telemetry_reason,omitempty"`
	MetricRollupRows          int                        `json:"metric_rollup_rows"`
	MetricRollupRowLimit      int                        `json:"metric_rollup_row_limit"`
	MetricRollupScannedRows   int                        `json:"metric_rollup_scanned_rows"`
	MetricRollupScanLimit     int                        `json:"metric_rollup_scan_limit"`
	MetricSketchBytes         int64                      `json:"metric_sketch_bytes"`
	MetricSketchByteLimit     int64                      `json:"metric_sketch_byte_limit"`
	Hotcache                  routinghotcache.Stats      `json:"hotcache"`
	StableTelemetry           routingmetrics.StableStats `json:"stable_telemetry"`
}

type SnapshotView struct {
	Revision              uint64            `json:"revision"`
	RuntimeGeneration     uint64            `json:"runtime_generation"`
	PolicyHash            string            `json:"policy_hash"`
	ActivationID          int64             `json:"activation_id"`
	ActivationStage       string            `json:"activation_stage"`
	TrafficBasisPoints    int               `json:"traffic_basis_points"`
	BuiltAtUnix           int64             `json:"built_at"`
	BuildDurationMs       int64             `json:"build_duration_ms"`
	AggregateP95TTFTMs    float64           `json:"aggregate_p95_ttft_ms"`
	AggregateP95TTFTKnown bool              `json:"aggregate_p95_ttft_known"`
	Pools                 []PoolSnapshot    `json:"pools"`
	Channels              []ChannelSnapshot `json:"channels"`
	Stats                 SnapshotStats     `json:"stats"`
}

type PoolSnapshot struct {
	ID               int                       `json:"id"`
	GroupName        string                    `json:"group_name"`
	DisplayName      string                    `json:"display_name"`
	Source           string                    `json:"source"`
	DeploymentStage  string                    `json:"deployment_stage"`
	PolicyProfile    string                    `json:"policy_profile"`
	SelectorPolicy   PoolSelectorPolicy        `json:"selector_policy"`
	CanaryPolicy     model.RoutingCanaryPolicy `json:"canary_policy"`
	MemberCount      int                       `json:"member_count"`
	MembersTruncated bool                      `json:"members_truncated"`
	Members          []PoolMemberSnapshot      `json:"members"`
}

type PoolMemberSnapshot struct {
	ID                   int             `json:"id"`
	PoolID               int             `json:"pool_id"`
	ChannelID            int             `json:"channel_id"`
	ChannelName          string          `json:"channel_name"`
	ChannelType          int             `json:"channel_type"`
	PhysicalStatus       int             `json:"physical_status"`
	LegacyPriority       int64           `json:"legacy_priority"`
	LegacyWeight         int64           `json:"legacy_weight"`
	MultiKey             bool            `json:"multi_key"`
	CredentialCount      int             `json:"credential_count"`
	CredentialsTruncated bool            `json:"credentials_truncated"`
	CredentialIDs        []int           `json:"credential_ids"`
	ModelCount           int             `json:"model_count"`
	ModelsTruncated      bool            `json:"models_truncated"`
	Models               []ModelSnapshot `json:"models"`
	TelemetryKnown       bool            `json:"telemetry_known"`
}

type ModelSnapshot struct {
	ModelName                   string  `json:"model_name"`
	MetricKnown                 bool    `json:"metric_known"`
	MetricSource                string  `json:"metric_source,omitempty"`
	RequestCount                int64   `json:"request_count"`
	SuccessCount                int64   `json:"success_count"`
	FailureCount                int64   `json:"failure_count"`
	UnknownClassificationCount  int64   `json:"unknown_classification_count"`
	ReliabilityRequestCount     int64   `json:"reliability_request_count"`
	ReliabilityFailureCount     int64   `json:"reliability_failure_count"`
	AverageLatencyMs            float64 `json:"average_latency_ms"`
	AverageTTFTMs               float64 `json:"average_ttft_ms"`
	P50LatencyMs                float64 `json:"p50_latency_ms"`
	P95LatencyMs                float64 `json:"p95_latency_ms"`
	P99LatencyMs                float64 `json:"p99_latency_ms"`
	P50TTFTMs                   float64 `json:"p50_ttft_ms"`
	P95TTFTMs                   float64 `json:"p95_ttft_ms"`
	P99TTFTMs                   float64 `json:"p99_ttft_ms"`
	P95LatencyKnown             bool    `json:"p95_latency_known"`
	P95TTFTKnown                bool    `json:"p95_ttft_known"`
	LatencyDistributionKnown    bool    `json:"latency_distribution_known"`
	TTFTDistributionKnown       bool    `json:"ttft_distribution_known"`
	LatencyDistributionCoverage float64 `json:"latency_distribution_coverage"`
	TTFTDistributionCoverage    float64 `json:"ttft_distribution_coverage"`
	OutputTokens                int64   `json:"output_tokens"`
	GenerationMs                int64   `json:"generation_ms"`
	OutputTokensPerSecond       float64 `json:"output_tokens_per_second"`
	Err4xx                      int64   `json:"err_4xx"`
	Err5xx                      int64   `json:"err_5xx"`
	Err429                      int64   `json:"err_429"`
	Err529                      int64   `json:"err_529"`
	AverageRetryAfterMs         float64 `json:"average_retry_after_ms"`
	MetricUpdatedUnix           int64   `json:"metric_updated_at"`
	Inflight                    int64   `json:"inflight"`
	BreakerKnown                bool    `json:"breaker_known"`
	BreakerState                string  `json:"breaker_state"`
	BreakerReason               string  `json:"breaker_reason"`
	BreakerCooldownUntil        int64   `json:"breaker_cooldown_until"`
	BreakerHalfOpenInflight     int64   `json:"breaker_half_open_inflight"`
	BreakerUpdatedUnix          int64   `json:"breaker_updated_at"`
	CapacityLimited             bool    `json:"capacity_limited"`
	CapacityStatusCode          int     `json:"capacity_status_code"`
	CapacityCooldownUntilMs     int64   `json:"capacity_cooldown_until_ms"`
	CapacityUpdatedUnixMilli    int64   `json:"capacity_updated_at_ms"`
	CostKnown                   bool    `json:"cost_known"`
	Cost                        float64 `json:"cost"`
	CostConfidence              string  `json:"cost_confidence"`
	CostUpdatedUnix             int64   `json:"cost_updated_at"`
	CostQuotaType               int     `json:"cost_quota_type"`
	CostGroupRatio              float64 `json:"cost_group_ratio"`
	CostBaseRatio               float64 `json:"cost_base_ratio"`
	CostCompletionRatio         float64 `json:"cost_completion_ratio"`
	CostModelPrice              float64 `json:"cost_model_price"`
	CostBillingMode             string  `json:"cost_billing_mode"`
	ttftCount                   int64
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
	view                     SnapshotView
	poolByGroup              map[string]int
	memberByPoolChannel      map[poolChannelKey]int
	credentialByFingerprint  map[credentialFingerprintKey]int
	modelByMemberModel       map[memberModelKey]ModelSnapshot
	channelByID              map[int]ChannelSnapshot
	poolIndexByID            map[int]int
	memberIndexesByPoolModel map[poolModelKey][]int
	poolSummaries            []PoolSnapshotSummary
	telemetrySummary         TelemetryAggregate
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

type poolModelKey struct {
	poolID int
	model  string
}

type stableMetricKey struct {
	memberID int
	model    string
}

type activeMetricMember struct {
	poolID    int
	channelID int
}

const (
	// The default scan budget is five times the selected-row budget. These
	// limits bound telemetry rebuild work only; valid policy topology remains
	// publishable when either budget is exhausted.
	snapshotMetricRollupPageSize           = 500
	snapshotMetricRollupScanBudgetPages    = 400
	snapshotMetricRollupDefaultMaxRows     = snapshotMetricRollupPageSize * snapshotMetricRollupScanBudgetPages
	snapshotMetricRollupDefaultMaxScanRows = snapshotMetricRollupDefaultMaxRows * 5

	snapshotTelemetryStatusComplete    = "complete"
	snapshotTelemetryStatusPartial     = "partial"
	snapshotTelemetryStatusUnavailable = "unavailable"

	snapshotTelemetryReasonDistributionCoverage = "distribution_coverage"
	snapshotTelemetryReasonRollupRows           = "metric_rollup_rows_limit"
	snapshotTelemetryReasonScanRows             = "metric_rollup_scan_rows_limit"
	snapshotTelemetryReasonSketchBlob           = "metric_sketch_blob_limit"
	snapshotTelemetryReasonSketchBytes          = "metric_sketch_bytes_limit"
	snapshotTelemetryReasonAggregates           = "metric_aggregate_limit"
)

type routingMetricRollupCursor struct {
	memberID     int
	modelKey     string
	credentialID int
	bucketTs     int64
	id           int
}

type routingMetricRollupPageMetadata struct {
	ID                 int
	MemberID           int
	ModelKey           string
	ModelName          string
	CredentialID       int
	BucketTs           int64
	LatencySketchBytes int64
	TtftSketchBytes    int64
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
	latencySketch           *routingdistribution.DurationSketch
	ttftSketch              *routingdistribution.DurationSketch
}

var (
	currentSnapshot           atomic.Pointer[runtimeSnapshot]
	snapshotRuntimeGeneration atomic.Uint64
	snapshotPublishMu         sync.Mutex
)

func RefreshSnapshotContext(ctx context.Context) (SnapshotView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db := model.DB
	if _, err := syncLegacyRoutingPolicyDBContext(ctx, db); err != nil {
		return SnapshotView{}, err
	}

	snapshot, err := buildSnapshotContext(ctx, db, DefaultSnapshotLimits)
	if err != nil {
		return SnapshotView{}, err
	}

	return publishRuntimeSnapshot(snapshot)
}

func publishRuntimeSnapshot(snapshot *runtimeSnapshot) (SnapshotView, error) {
	if snapshot == nil {
		return SnapshotView{}, errors.New("channel routing snapshot is nil")
	}
	snapshotPublishMu.Lock()
	defer snapshotPublishMu.Unlock()
	if current := currentSnapshot.Load(); current != nil {
		if snapshot.view.Revision < current.view.Revision {
			return SnapshotView{}, ErrSnapshotRevisionRollback
		}
		if snapshot.view.Revision == current.view.Revision {
			if snapshot.view.PolicyHash != current.view.PolicyHash ||
				snapshot.view.ActivationID != current.view.ActivationID ||
				snapshot.view.ActivationStage != current.view.ActivationStage ||
				snapshot.view.TrafficBasisPoints != current.view.TrafficBasisPoints {
				return SnapshotView{}, ErrSnapshotRevisionConflict
			}
		}
	}
	snapshot.view.RuntimeGeneration = snapshotRuntimeGeneration.Add(1)
	currentSnapshot.Store(snapshot)
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := lockRoutingTelemetry(ctx); err != nil {
		return nil, err
	}
	telemetryLocked := true
	defer func() {
		if telemetryLocked {
			unlockRoutingTelemetry()
		}
	}()
	var snapshot *runtimeSnapshot
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		head, err := model.GetRoutingPolicyHeadDBContext(ctx, tx)
		if err != nil {
			return err
		}
		liveRollups := routingmetrics.StableSnapshots()
		unlockRoutingTelemetry()
		telemetryLocked = false
		if head.CurrentRevision <= 0 || head.CurrentHash == "" {
			return model.ErrRoutingPolicyRevisionNotFound
		}
		document, revision, err := model.LoadRoutingPolicyRevisionDBContext(ctx, tx, head.CurrentRevision)
		if err != nil {
			return err
		}
		if revision.ContentHash != head.CurrentHash {
			return model.ErrRoutingPolicyContentCorrupt
		}
		if head.CurrentActivationID <= 0 {
			return fmt.Errorf("%w: policy head has no current activation", ErrSnapshotActivation)
		}
		var activation model.RoutingPolicyActivation
		if err := tx.WithContext(ctx).Where("id = ?", head.CurrentActivationID).First(&activation).Error; err != nil {
			return fmt.Errorf("%w: load current activation %d: %w", ErrSnapshotActivation, head.CurrentActivationID, err)
		}
		if err := validateSnapshotActivation(head, revision, activation, document); err != nil {
			return err
		}
		var buildErr error
		snapshot, buildErr = buildSnapshotWithinTransaction(ctx, tx, limits, document, revision, activation, liveRollups)
		return buildErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return snapshot, err
}

func validateSnapshotActivation(
	head model.RoutingPolicyHead,
	revision model.RoutingPolicyRevision,
	activation model.RoutingPolicyActivation,
	document model.RoutingPolicyDocument,
) error {
	if revision.Revision != head.CurrentRevision || activation.ID != head.CurrentActivationID ||
		activation.Revision != revision.Revision || activation.PreviousRevision != revision.ParentRevision ||
		activation.RollbackOfRevision != revision.RollbackOfRevision || activation.Stage != head.CurrentStage {
		return fmt.Errorf("%w: head, revision, and activation identities do not match", ErrSnapshotActivation)
	}
	activationSpec := model.RoutingPolicyActivationSpec{
		Stage:              activation.Stage,
		TrafficBasisPoints: activation.TrafficBasisPoints,
		ActorID:            activation.ActorID,
		Reason:             activation.Reason,
	}
	if err := activationSpec.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrSnapshotActivation, err)
	}
	if err := model.ValidateRoutingPolicyActivationDocument(document, activationSpec); err != nil {
		return fmt.Errorf("%w: pool stage conflicts with activation stage %q", ErrSnapshotActivation, activation.Stage)
	}
	return nil
}

func validateSnapshotPolicyReferences(
	ctx context.Context,
	db *gorm.DB,
	document model.RoutingPolicyDocument,
	activeCredentials map[int]model.RoutingCredentialRef,
	limits SnapshotLimits,
) (map[int][]string, error) {
	channelIDs := make(map[int]struct{})
	for poolIndex := range document.Pools {
		for memberIndex := range document.Pools[poolIndex].Members {
			member := document.Pools[poolIndex].Members[memberIndex]
			channelIDs[member.ChannelID] = struct{}{}
			for _, credentialID := range member.CredentialIDs {
				credential, exists := activeCredentials[credentialID]
				if !exists {
					return nil, fmt.Errorf("%w: credential %d is missing or inactive", ErrSnapshotPolicyReference, credentialID)
				}
				if credential.ChannelID != member.ChannelID {
					return nil, fmt.Errorf("%w: credential %d belongs to channel %d, not %d", ErrSnapshotPolicyReference, credentialID, credential.ChannelID, member.ChannelID)
				}
			}
		}
	}
	if len(channelIDs) == 0 {
		return map[int][]string{}, nil
	}

	orderedIDs := make([]int, 0, len(channelIDs))
	for channelID := range channelIDs {
		orderedIDs = append(orderedIDs, channelID)
	}
	sort.Ints(orderedIDs)
	modelsByChannel := make(map[int][]string, len(orderedIDs))
	for start := 0; start < len(orderedIDs); start += snapshotMetricRollupPageSize {
		end := min(start+snapshotMetricRollupPageSize, len(orderedIDs))
		var batch []model.Channel
		if err := db.WithContext(ctx).
			Select("id", "models").
			Where("id IN ?", orderedIDs[start:end]).
			Find(&batch).Error; err != nil {
			return nil, err
		}
		for index := range batch {
			channel := batch[index]
			if len(channel.Models) > limits.MaxModelBytesPerChannel {
				return nil, fmt.Errorf("%w: channel %d model bytes", ErrSnapshotLimitExceeded, channel.Id)
			}
			modelNames := normalizedModels(channel.GetModels())
			if len(modelNames) > limits.MaxModelsPerChannel {
				return nil, fmt.Errorf("%w: channel %d models", ErrSnapshotLimitExceeded, channel.Id)
			}
			modelsByChannel[channel.Id] = modelNames
		}
	}
	for _, channelID := range orderedIDs {
		if _, exists := modelsByChannel[channelID]; !exists {
			return nil, fmt.Errorf("%w: channel %d does not exist", ErrSnapshotPolicyReference, channelID)
		}
	}
	return modelsByChannel, nil
}

func buildSnapshotWithinTransaction(
	ctx context.Context,
	db *gorm.DB,
	limits SnapshotLimits,
	document model.RoutingPolicyDocument,
	revision model.RoutingPolicyRevision,
	activation model.RoutingPolicyActivation,
	liveRollups []routingmetrics.StableSnapshot,
) (*runtimeSnapshot, error) {
	started := time.Now()

	pools := make([]model.RoutingPool, 0, len(document.Pools))
	members := make([]model.RoutingPoolMember, 0, revision.MemberCount)
	memberCredentialIDs := make(map[int][]int, revision.MemberCount)
	policyPoolByID := make(map[int]model.RoutingPolicyPoolContent, len(document.Pools))
	selectorPolicyByPoolID := make(map[int]PoolSelectorPolicy, len(document.Pools))
	canaryPolicyByPoolID := make(map[int]model.RoutingCanaryPolicy, len(document.Pools))
	for poolIndex := range document.Pools {
		pool := document.Pools[poolIndex]
		selectorPolicy, err := resolvePoolSelectorPolicy(pool.PolicyProfile, pool.Policy)
		if err != nil {
			return nil, fmt.Errorf("invalid routing selector policy for pool %d: %w", pool.PoolID, err)
		}
		canaryPolicy, err := model.ResolveRoutingCanaryPolicy(pool.Policy)
		if err != nil {
			return nil, fmt.Errorf("invalid routing canary policy for pool %d: %w", pool.PoolID, err)
		}
		policyPoolByID[pool.PoolID] = pool
		selectorPolicyByPoolID[pool.PoolID] = selectorPolicy
		canaryPolicyByPoolID[pool.PoolID] = canaryPolicy
		pools = append(pools, model.RoutingPool{
			ID:          pool.PoolID,
			GroupName:   pool.GroupName,
			DisplayName: pool.DisplayName,
			Source:      "policy_revision",
			Active:      true,
		})
		for memberIndex := range pool.Members {
			member := pool.Members[memberIndex]
			if !member.Enabled {
				continue
			}
			members = append(members, model.RoutingPoolMember{
				ID:             member.MemberID,
				PoolID:         pool.PoolID,
				ChannelID:      member.ChannelID,
				Source:         "policy_revision",
				Active:         true,
				LegacyPriority: member.Priority,
				LegacyWeight:   member.Weight,
			})
			memberCredentialIDs[member.MemberID] = append([]int(nil), member.CredentialIDs...)
		}
	}
	if len(pools) > limits.MaxPools {
		return nil, fmt.Errorf("%w: pools", ErrSnapshotLimitExceeded)
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
	activeCredentials := make(map[int]model.RoutingCredentialRef, len(credentials))
	channelsWithActiveCredentials := make(map[int]struct{}, len(credentials))
	for _, credential := range credentials {
		activeCredentialChannels[credential.ID] = credential.ChannelID
		activeCredentials[credential.ID] = credential
		channelsWithActiveCredentials[credential.ChannelID] = struct{}{}
	}
	modelNamesByPolicyChannel, err := validateSnapshotPolicyReferences(ctx, db, document, activeCredentials, limits)
	if err != nil {
		return nil, err
	}
	visibleModelsByMember := make(map[int]map[string]struct{}, len(activeMembers))
	for memberID, member := range activeMembers {
		modelNames := modelNamesByPolicyChannel[member.channelID]
		visible := make(map[string]struct{}, len(modelNames))
		for _, modelName := range modelNames {
			visible[modelName] = struct{}{}
		}
		visibleModelsByMember[memberID] = visible
	}
	allowedCredentialsByMember := make(map[int]map[int]struct{}, len(memberCredentialIDs))
	activeMemberCredentialIDs := make(map[int][]int, len(memberCredentialIDs))
	for memberID, credentialIDs := range memberCredentialIDs {
		_, active := activeMembers[memberID]
		if !active || len(credentialIDs) == 0 {
			continue
		}
		allowed := make(map[int]struct{}, len(credentialIDs))
		for _, credentialID := range credentialIDs {
			allowed[credentialID] = struct{}{}
			activeMemberCredentialIDs[memberID] = append(activeMemberCredentialIDs[memberID], credentialID)
		}
		if len(allowed) > 0 {
			allowedCredentialsByMember[memberID] = allowed
		}
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
	metricRollupRowLimit := limits.MaxMetricRollupRows
	if metricRollupRowLimit <= 0 {
		metricRollupRowLimit = snapshotMetricRollupDefaultMaxRows
	}
	metricRollupScanLimit := limits.MaxMetricRollupScanRows
	if metricRollupScanLimit <= 0 {
		metricRollupScanLimit = snapshotMetricRollupDefaultMaxScanRows
	}
	metricSketchBytesLimit := int64(limits.MaxMetricSketchBytes)
	if metricSketchBytesLimit <= 0 {
		metricSketchBytesLimit = int64(DefaultSnapshotLimits.MaxMetricSketchBytes)
	}
	stableMetrics := make(map[stableMetricKey]stableMetricAggregate)
	metricRollupRows := 0
	metricRollupScannedRows := 0
	metricSketchBytes := int64(0)
	telemetryStatus := snapshotTelemetryStatusComplete
	telemetryReason := ""

	// Rollup rows are an independent telemetry work budget, not a topology
	// validity limit. An overflow discards the entire overlay so a new policy
	// revision can still publish without scoring from truncated observations.
	var metricCursor *routingMetricRollupCursor
metricPages:
	for {
		metricQuery := db.WithContext(ctx).Table("routing_metric_rollups AS metric_rollups").
			Select(
				"metric_rollups.id, metric_rollups.member_id, metric_rollups.model_key, metric_rollups.model_name, "+
					"metric_rollups.credential_id, metric_rollups.bucket_ts, "+
					"COALESCE(LENGTH(metric_rollups.latency_sketch), 0) AS latency_sketch_bytes, "+
					"COALESCE(LENGTH(metric_rollups.ttft_sketch), 0) AS ttft_sketch_bytes",
			).
			Joins(
				"JOIN routing_policy_member_revisions AS metric_members ON metric_members.revision = ? "+
					"AND metric_members.member_id = metric_rollups.member_id "+
					"AND metric_members.pool_id = metric_rollups.pool_id "+
					"AND metric_members.channel_id = metric_rollups.channel_id "+
					"AND metric_members.enabled = ?",
				revision.Revision,
				true,
			).
			Joins("LEFT JOIN routing_credential_refs AS metric_credentials ON metric_credentials.id = metric_rollups.credential_id AND metric_credentials.channel_id = metric_rollups.channel_id").
			Where(
				"metric_rollups.bucket_ts >= ? AND ((metric_rollups.credential_id = 0 AND NOT EXISTS ("+
					"SELECT 1 FROM routing_credential_refs AS current_credentials "+
					"WHERE current_credentials.channel_id = metric_rollups.channel_id AND current_credentials.active = ?"+
					")) OR metric_credentials.active = ?)",
				metricCutoff,
				true,
				true,
			)
		if metricCursor != nil {
			metricQuery = metricQuery.Where(
				"(metric_rollups.member_id > ? OR "+
					"(metric_rollups.member_id = ? AND metric_rollups.credential_id > ?) OR "+
					"(metric_rollups.member_id = ? AND metric_rollups.credential_id = ? AND metric_rollups.model_key > ?) OR "+
					"(metric_rollups.member_id = ? AND metric_rollups.credential_id = ? AND metric_rollups.model_key = ? AND metric_rollups.bucket_ts > ?) OR "+
					"(metric_rollups.member_id = ? AND metric_rollups.credential_id = ? AND metric_rollups.model_key = ? AND metric_rollups.bucket_ts = ? AND metric_rollups.id > ?))",
				metricCursor.memberID,
				metricCursor.memberID, metricCursor.credentialID,
				metricCursor.memberID, metricCursor.credentialID, metricCursor.modelKey,
				metricCursor.memberID, metricCursor.credentialID, metricCursor.modelKey, metricCursor.bucketTs,
				metricCursor.memberID, metricCursor.credentialID, metricCursor.modelKey, metricCursor.bucketTs, metricCursor.id,
			)
		}
		var metadata []routingMetricRollupPageMetadata
		if err := orderRoutingMetricRollups(metricQuery).Limit(snapshotMetricRollupPageSize).Scan(&metadata).Error; err != nil {
			return nil, err
		}
		if len(metadata) == 0 {
			break
		}
		pageIDs := make([]int, 0, len(metadata))
		selectedMetadata := make([]routingMetricRollupPageMetadata, 0, len(metadata))
		for index := range metadata {
			metricRollupScannedRows++
			if metricRollupScannedRows > metricRollupScanLimit {
				telemetryStatus = snapshotTelemetryStatusUnavailable
				telemetryReason = snapshotTelemetryReasonScanRows
				break metricPages
			}
			if _, visible := visibleModelsByMember[metadata[index].MemberID][metadata[index].ModelName]; !visible {
				continue
			}
			member := activeMembers[metadata[index].MemberID]
			credentialID := metadata[index].CredentialID
			if credentialID > 0 {
				if activeCredentialChannels[credentialID] != member.channelID {
					continue
				}
				if _, allowed := allowedCredentialsByMember[metadata[index].MemberID][credentialID]; !allowed {
					continue
				}
			} else {
				if len(allowedCredentialsByMember[metadata[index].MemberID]) > 0 {
					continue
				}
				if _, keyed := channelsWithActiveCredentials[member.channelID]; keyed {
					continue
				}
			}
			metricRollupRows++
			if metricRollupRows > metricRollupRowLimit {
				telemetryStatus = snapshotTelemetryStatusUnavailable
				telemetryReason = snapshotTelemetryReasonRollupRows
				break metricPages
			}
			latencyBytes := metadata[index].LatencySketchBytes
			ttftBytes := metadata[index].TtftSketchBytes
			if latencyBytes < 0 || ttftBytes < 0 ||
				latencyBytes > int64(routingdistribution.MaxEncodedBytes) ||
				ttftBytes > int64(routingdistribution.MaxEncodedBytes) {
				telemetryStatus = snapshotTelemetryStatusUnavailable
				telemetryReason = snapshotTelemetryReasonSketchBlob
				break metricPages
			}
			rowBytes := latencyBytes + ttftBytes
			if rowBytes > metricSketchBytesLimit-metricSketchBytes {
				metricSketchBytes = saturatingMetricTotal(metricSketchBytes, rowBytes)
				telemetryStatus = snapshotTelemetryStatusUnavailable
				telemetryReason = snapshotTelemetryReasonSketchBytes
				break metricPages
			}
			metricSketchBytes += rowBytes
			pageIDs = append(pageIDs, metadata[index].ID)
			selectedMetadata = append(selectedMetadata, metadata[index])
		}

		if len(pageIDs) > 0 {
			var page []model.RoutingMetricRollup
			if err := orderRoutingMetricRollups(
				db.WithContext(ctx).Table("routing_metric_rollups AS metric_rollups").
					Select("metric_rollups.*").
					Where("metric_rollups.id IN ?", pageIDs),
			).Scan(&page).Error; err != nil {
				return nil, err
			}
			if len(page) != len(selectedMetadata) {
				return nil, errors.New("channel routing snapshot metric rollup page changed during build")
			}
			for index := range page {
				if page[index].ID != selectedMetadata[index].ID ||
					int64(len(page[index].LatencySketch)) != selectedMetadata[index].LatencySketchBytes ||
					int64(len(page[index].TtftSketch)) != selectedMetadata[index].TtftSketchBytes {
					return nil, errors.New("channel routing snapshot metric rollup page changed during build")
				}
			}
			stableMetrics = aggregateStableMetrics(
				stableMetrics,
				page,
				nil,
				metricCutoff,
				activeMembers,
				activeCredentialChannels,
				channelsWithActiveCredentials,
				allowedCredentialsByMember,
				visibleModelsByMember,
			)
			if len(stableMetrics) > limits.MaxMetricAggregates {
				telemetryStatus = snapshotTelemetryStatusUnavailable
				telemetryReason = snapshotTelemetryReasonAggregates
				break metricPages
			}
		}
		last := metadata[len(metadata)-1]
		metricCursor = &routingMetricRollupCursor{
			memberID:     last.MemberID,
			modelKey:     last.ModelKey,
			credentialID: last.CredentialID,
			bucketTs:     last.BucketTs,
			id:           last.ID,
		}
		if len(metadata) < snapshotMetricRollupPageSize {
			break
		}
	}
	if telemetryStatus == snapshotTelemetryStatusUnavailable {
		stableMetrics = make(map[stableMetricKey]stableMetricAggregate)
	} else {
		stableMetrics = aggregateStableMetrics(
			stableMetrics,
			nil,
			liveRollups,
			metricCutoff,
			activeMembers,
			activeCredentialChannels,
			channelsWithActiveCredentials,
			allowedCredentialsByMember,
			visibleModelsByMember,
		)
		if len(stableMetrics) > limits.MaxMetricAggregates {
			stableMetrics = make(map[stableMetricKey]stableMetricAggregate)
			telemetryStatus = snapshotTelemetryStatusUnavailable
			telemetryReason = snapshotTelemetryReasonAggregates
		}
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
	channelViewByID := make(map[int]ChannelSnapshot, len(channels))
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
		channelViewByID[channel.Id] = view
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
	memberIndexesByPoolModel := make(map[poolModelKey][]int, min(limits.MaxTotalModelSnapshots, len(members)*4))
	membersWithTelemetry := 0
	modelSnapshotCount := 0
	unknownFailedAttempts := int64(0)
	allFailedAttempts := int64(0)
	memberAssociatedChannels := make(map[int]struct{})
	credentialRequiredChannels := make(map[int]struct{})
	channelsWithCredentials := make(map[int]struct{})
	telemetryDistributionPartial := false
	telemetryAvailable := telemetryStatus != snapshotTelemetryStatusUnavailable
	for _, pool := range pools {
		policyPool := policyPoolByID[pool.ID]
		poolView := PoolSnapshot{
			ID:              pool.ID,
			GroupName:       pool.GroupName,
			DisplayName:     pool.DisplayName,
			Source:          pool.Source,
			DeploymentStage: policyPool.DeploymentStage,
			PolicyProfile:   policyPool.PolicyProfile,
			SelectorPolicy:  selectorPolicyByPoolID[pool.ID],
			CanaryPolicy:    canaryPolicyByPoolID[pool.ID],
			MemberCount:     len(membersByPool[pool.ID]),
			Members:         make([]PoolMemberSnapshot, 0, len(membersByPool[pool.ID])),
		}
		for _, member := range membersByPool[pool.ID] {
			memberIndex := len(poolView.Members)
			channel := channelByID[member.ChannelID]
			memberAssociatedChannels[channel.Id] = struct{}{}
			if credentialRequiredByChannel[channel.Id] {
				credentialRequiredChannels[channel.Id] = struct{}{}
			}
			credentialIDs := append([]int(nil), activeMemberCredentialIDs[member.ID]...)
			if len(credentialIDs) > 0 {
				channelsWithCredentials[channel.Id] = struct{}{}
			}
			memberView := PoolMemberSnapshot{
				ID:              member.ID,
				PoolID:          member.PoolID,
				ChannelID:       member.ChannelID,
				ChannelName:     channel.Name,
				ChannelType:     channel.Type,
				PhysicalStatus:  channel.Status,
				LegacyPriority:  member.LegacyPriority,
				LegacyWeight:    member.LegacyWeight,
				MultiKey:        channel.ChannelInfo.IsMultiKey,
				CredentialCount: len(credentialIDs),
				CredentialIDs:   credentialIDs,
				ModelCount:      len(modelNamesByChannel[channel.Id]),
				Models:          make([]ModelSnapshot, 0, len(modelNamesByChannel[channel.Id])),
			}
			for _, modelName := range modelNamesByChannel[channel.Id] {
				if modelSnapshotCount >= limits.MaxTotalModelSnapshots {
					return nil, fmt.Errorf("%w: model snapshots", ErrSnapshotLimitExceeded)
				}
				modelView, invalidValues := snapshotModel(channel, member.ID, credentialIDs, pool.GroupName, modelName, stableMetrics, telemetryAvailable)
				invalidNumericValues += invalidValues
				if modelView.MetricSource == "stable_rollup" {
					allFailedAttempts = saturatingMetricTotal(allFailedAttempts, modelView.FailureCount)
					unknownFailedAttempts = saturatingMetricTotal(unknownFailedAttempts, modelView.UnknownClassificationCount)
					if (modelView.RequestCount > 0 && modelView.LatencyDistributionCoverage < 1) ||
						(modelView.ttftCount > 0 && modelView.TTFTDistributionCoverage < 1) {
						telemetryDistributionPartial = true
					}
				}
				if modelView.MetricKnown {
					memberView.TelemetryKnown = true
				}
				memberView.Models = append(memberView.Models, modelView)
				modelByMemberModel[memberModelKey{memberID: member.ID, model: modelName}] = modelView
				key := poolModelKey{poolID: pool.ID, model: modelName}
				memberIndexesByPoolModel[key] = append(memberIndexesByPoolModel[key], memberIndex)
				modelSnapshotCount++
			}
			if memberView.TelemetryKnown {
				membersWithTelemetry++
			}
			poolView.Members = append(poolView.Members, memberView)
		}
		poolViews = append(poolViews, poolView)
	}
	if telemetryStatus == snapshotTelemetryStatusComplete && telemetryDistributionPartial {
		telemetryStatus = snapshotTelemetryStatusPartial
		telemetryReason = snapshotTelemetryReasonDistributionCoverage
	}

	stats := SnapshotStats{
		PoolCount:               len(poolViews),
		MemberCount:             len(memberByPoolChannel),
		CredentialCount:         len(credentials),
		ChannelCount:            len(channelViews),
		ModelSnapshotCount:      modelSnapshotCount,
		MembersWithTelemetry:    membersWithTelemetry,
		ChannelsWithCredentials: len(channelsWithCredentials),
		MetricTelemetryStatus:   telemetryStatus,
		MetricTelemetryReason:   telemetryReason,
		MetricRollupRows:        metricRollupRows,
		MetricRollupRowLimit:    metricRollupRowLimit,
		MetricRollupScannedRows: metricRollupScannedRows,
		MetricRollupScanLimit:   metricRollupScanLimit,
		MetricSketchBytes:       metricSketchBytes,
		MetricSketchByteLimit:   metricSketchBytesLimit,
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
	aggregateP95TTFTMs, aggregateP95TTFTKnown, err := aggregateRoutingMetricTTFTP95(stableMetrics, modelByMemberModel)
	if err != nil {
		return nil, err
	}

	view := SnapshotView{
		Revision:              uint64(revision.Revision),
		PolicyHash:            revision.ContentHash,
		ActivationID:          activation.ID,
		ActivationStage:       activation.Stage,
		TrafficBasisPoints:    activation.TrafficBasisPoints,
		BuiltAtUnix:           common.GetTimestamp(),
		BuildDurationMs:       time.Since(started).Milliseconds(),
		AggregateP95TTFTMs:    aggregateP95TTFTMs,
		AggregateP95TTFTKnown: aggregateP95TTFTKnown,
		Pools:                 poolViews,
		Channels:              channelViews,
		Stats:                 stats,
	}
	poolIndexByID := make(map[int]int, len(poolViews))
	poolSummaries := make([]PoolSnapshotSummary, len(poolViews))
	for index := range poolViews {
		poolIndexByID[poolViews[index].ID] = index
		poolSummaries[index] = summarizePoolSnapshot(poolViews[index])
	}

	return &runtimeSnapshot{
		view:                     view,
		poolByGroup:              poolByGroup,
		memberByPoolChannel:      memberByPoolChannel,
		credentialByFingerprint:  credentialByFingerprint,
		modelByMemberModel:       modelByMemberModel,
		channelByID:              channelViewByID,
		poolIndexByID:            poolIndexByID,
		memberIndexesByPoolModel: memberIndexesByPoolModel,
		poolSummaries:            poolSummaries,
		telemetrySummary:         telemetryAggregate(view),
	}, ctx.Err()
}

func aggregateRoutingMetricTTFTP95(
	metrics map[stableMetricKey]stableMetricAggregate,
	included map[memberModelKey]ModelSnapshot,
) (float64, bool, error) {
	merged := routingdistribution.NewDurationSketch()
	expectedCount := int64(0)
	for key, metric := range metrics {
		observation, exists := included[memberModelKey{memberID: key.memberID, model: key.model}]
		if !exists || observation.MetricSource != "stable_rollup" {
			continue
		}
		if metric.ttftCount <= 0 {
			continue
		}
		if metric.ttftSketch == nil || metric.ttftSketch.Count() != metric.ttftCount {
			return 0, false, nil
		}
		if expectedCount > math.MaxInt64-metric.ttftCount {
			return 0, false, fmt.Errorf("%w: aggregate ttft sample count", ErrSnapshotLimitExceeded)
		}
		expectedCount += metric.ttftCount
		if err := merged.Merge(metric.ttftSketch); err != nil {
			return 0, false, fmt.Errorf("merge aggregate ttft distribution: %w", err)
		}
	}
	if expectedCount == 0 || merged.Count() != expectedCount {
		return 0, false, nil
	}
	quantile, err := merged.Quantile(0.95)
	if err != nil {
		return 0, false, fmt.Errorf("read aggregate ttft distribution: %w", err)
	}
	return quantile.ValueMilliseconds, quantile.Known, nil
}

func snapshotModel(
	channel model.Channel,
	memberID int,
	credentialIDs []int,
	group string,
	modelName string,
	stableMetrics map[stableMetricKey]stableMetricAggregate,
	telemetryAvailable bool,
) (ModelSnapshot, int) {
	view := ModelSnapshot{ModelName: modelName}
	if len(credentialIDs) == 0 {
		view.Inflight = routingmetrics.StableInflightCount(routingmetrics.StableInflightKey{
			PoolMemberID: memberID,
			CredentialID: 0,
			Model:        modelName,
		})
	} else {
		for _, credentialID := range credentialIDs {
			view.Inflight = saturatingMetricTotal(view.Inflight, routingmetrics.StableInflightCount(routingmetrics.StableInflightKey{
				PoolMemberID: memberID,
				CredentialID: credentialID,
				Model:        modelName,
			}))
		}
	}
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
		view.ttftCount = metric.ttftCount
		invalidValues += metric.invalidNumericValues
		view.AverageLatencyMs = finiteMetricRatio(metric.totalLatencyMs, metric.requestCount, 1)
		view.AverageTTFTMs = finiteMetricRatio(metric.ttftSumMs, metric.ttftCount, 1)
		view.OutputTokensPerSecond = finiteMetricRatio(metric.outputTokens, metric.generationMs, 1000)
		view.AverageRetryAfterMs = finiteMetricRatio(metric.retryAfterTotalMs, metric.retryAfterCount, 1)
		latencyQuantiles := routingMetricQuantiles(metric.latencySketch, metric.requestCount)
		view.LatencyDistributionCoverage = latencyQuantiles.coverage
		view.LatencyDistributionKnown = latencyQuantiles.known
		view.P95LatencyKnown = latencyQuantiles.known
		view.P50LatencyMs = latencyQuantiles.p50
		view.P95LatencyMs = latencyQuantiles.p95
		view.P99LatencyMs = latencyQuantiles.p99
		invalidValues += latencyQuantiles.invalidValues
		ttftQuantiles := routingMetricQuantiles(metric.ttftSketch, metric.ttftCount)
		view.TTFTDistributionCoverage = ttftQuantiles.coverage
		view.TTFTDistributionKnown = ttftQuantiles.known
		view.P95TTFTKnown = ttftQuantiles.known
		view.P50TTFTMs = ttftQuantiles.p50
		view.P95TTFTMs = ttftQuantiles.p95
		view.P99TTFTMs = ttftQuantiles.p99
		invalidValues += ttftQuantiles.invalidValues
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
		if telemetryAvailable && !view.MetricKnown {
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
					view.P95LatencyKnown = true
				} else {
					invalidValues++
				}
				if finiteNonNegative(metric.P95TTFTMs) {
					view.P95TTFTMs = metric.P95TTFTMs
					view.P95TTFTKnown = true
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
			view.BreakerHalfOpenInflight = breaker.HalfOpenInflight
			view.BreakerUpdatedUnix = breaker.UpdatedUnix
		}
		if capacity, ok := routinghotcache.GetCapacityCooldown(key); ok {
			view.CapacityLimited = capacity.CooldownUntilUnixMilli > time.Now().UnixMilli()
			view.CapacityStatusCode = capacity.SourceStatusCode
			view.CapacityCooldownUntilMs = capacity.CooldownUntilUnixMilli
			view.CapacityUpdatedUnixMilli = capacity.UpdatedUnixMilli
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
		view.CostQuotaType = cost.QuotaType
		view.CostGroupRatio = cost.GroupRatio
		view.CostBaseRatio = cost.BaseRatio
		view.CostCompletionRatio = cost.CompletionRatio
		view.CostModelPrice = cost.ModelPrice
		view.CostBillingMode = cost.BillingMode
	}
	return view, invalidValues
}

type routingMetricDistributionQuantiles struct {
	coverage      float64
	known         bool
	p50           float64
	p95           float64
	p99           float64
	invalidValues int
}

func routingMetricQuantiles(sketch *routingdistribution.DurationSketch, expectedCount int64) routingMetricDistributionQuantiles {
	if expectedCount <= 0 {
		return routingMetricDistributionQuantiles{}
	}
	if sketch == nil {
		return routingMetricDistributionQuantiles{}
	}
	count := sketch.Count()
	result := routingMetricDistributionQuantiles{
		coverage: float64(min(max(count, 0), expectedCount)) / float64(expectedCount),
	}
	if count != expectedCount {
		if count > expectedCount {
			result.invalidValues++
		}
		return result
	}
	quantiles := []*float64{&result.p50, &result.p95, &result.p99}
	for index, quantile := range []float64{0.50, 0.95, 0.99} {
		value, err := sketch.Quantile(quantile)
		if err != nil || !value.Known || !finiteNonNegative(value.ValueMilliseconds) {
			result.invalidValues++
			return result
		}
		*quantiles[index] = value.ValueMilliseconds
	}
	result.known = true
	return result
}

func orderRoutingMetricRollups(query *gorm.DB) *gorm.DB {
	return query.
		Order("metric_rollups.member_id asc").
		Order("metric_rollups.credential_id asc").
		Order("metric_rollups.model_key asc").
		Order("metric_rollups.bucket_ts asc").
		Order("metric_rollups.id asc")
}

func aggregateStableMetrics(
	result map[stableMetricKey]stableMetricAggregate,
	persisted []model.RoutingMetricRollup,
	live []routingmetrics.StableSnapshot,
	cutoff int64,
	activeMembers map[int]activeMetricMember,
	activeCredentialChannels map[int]int,
	channelsWithActiveCredentials map[int]struct{},
	allowedCredentialsByMember map[int]map[int]struct{},
	visibleModelsByMember map[int]map[string]struct{},
) map[stableMetricKey]stableMetricAggregate {
	if result == nil {
		result = make(map[stableMetricKey]stableMetricAggregate, len(persisted)+len(live))
	}
	for index := range persisted {
		rollup := persisted[index]
		if rollup.MemberID <= 0 || rollup.ModelName == "" || rollup.BucketTs < cutoff {
			continue
		}
		member, active := activeMembers[rollup.MemberID]
		if !active || member.poolID != rollup.PoolID || member.channelID != rollup.ChannelID {
			continue
		}
		if _, visible := visibleModelsByMember[rollup.MemberID][rollup.ModelName]; !visible {
			continue
		}
		if rollup.CredentialID > 0 && activeCredentialChannels[rollup.CredentialID] != rollup.ChannelID {
			continue
		}
		if rollup.CredentialID > 0 {
			if _, allowed := allowedCredentialsByMember[rollup.MemberID][rollup.CredentialID]; !allowed {
				continue
			}
		}
		if rollup.CredentialID == 0 {
			if len(allowedCredentialsByMember[rollup.MemberID]) > 0 {
				continue
			}
			if _, keyed := channelsWithActiveCredentials[rollup.ChannelID]; keyed {
				continue
			}
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
		if rollup.LatencySampleCount > rollup.RequestCount || rollup.TtftSampleCount > rollup.TtftCount {
			aggregate.invalidNumericValues++
		} else {
			aggregate.addDistributions(
				rollup.SketchCodecVersion,
				rollup.LatencySampleCount,
				rollup.LatencySketch,
				rollup.TtftSampleCount,
				rollup.TtftSketch,
			)
		}
		aggregate.latestBucketTs = max(aggregate.latestBucketTs, rollup.BucketTs)
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
		if _, visible := visibleModelsByMember[snapshot.PoolMemberID][snapshot.Model]; !visible {
			continue
		}
		if snapshot.CredentialID > 0 && activeCredentialChannels[snapshot.CredentialID] != snapshot.ChannelID {
			continue
		}
		if snapshot.CredentialID > 0 {
			if _, allowed := allowedCredentialsByMember[snapshot.PoolMemberID][snapshot.CredentialID]; !allowed {
				continue
			}
		}
		if snapshot.CredentialID == 0 {
			if len(allowedCredentialsByMember[snapshot.PoolMemberID]) > 0 {
				continue
			}
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
		if snapshot.LatencySampleCount > snapshot.RequestCount || snapshot.TtftSampleCount > snapshot.TtftCount {
			aggregate.invalidNumericValues++
		} else {
			aggregate.addDistributions(
				snapshot.SketchCodecVersion,
				snapshot.LatencySampleCount,
				snapshot.LatencySketch,
				snapshot.TtftSampleCount,
				snapshot.TtftSketch,
			)
		}
		aggregate.latestBucketTs = max(aggregate.latestBucketTs, snapshot.BucketTs)
		result[key] = aggregate
	}
	return result
}

func (aggregate *stableMetricAggregate) addDistributions(
	codecVersion int,
	latencySampleCount int64,
	latencyData []byte,
	ttftSampleCount int64,
	ttftData []byte,
) {
	aggregate.mergeDistribution(&aggregate.latencySketch, codecVersion, latencySampleCount, latencyData)
	aggregate.mergeDistribution(&aggregate.ttftSketch, codecVersion, ttftSampleCount, ttftData)
}

func (aggregate *stableMetricAggregate) mergeDistribution(
	target **routingdistribution.DurationSketch,
	codecVersion int,
	sampleCount int64,
	data []byte,
) {
	if sampleCount == 0 {
		if len(data) != 0 {
			aggregate.invalidNumericValues++
		}
		return
	}
	if sampleCount < 0 || codecVersion <= 0 || len(data) == 0 {
		aggregate.invalidNumericValues++
		return
	}
	sketch, err := routingdistribution.DecodeDurationSketch(data, codecVersion)
	if err != nil || sketch.Count() != sampleCount {
		aggregate.invalidNumericValues++
		return
	}
	if *target == nil {
		*target = sketch
		return
	}
	if err := (*target).Merge(sketch); err != nil {
		aggregate.invalidNumericValues++
	}
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
		limits.MaxMetricAggregates > model.RoutingMetricRollupMaxQueryLimit || limits.MaxMetricRollupRows < 0 ||
		limits.MaxMetricRollupRows > model.RoutingMetricRollupMaxQueryLimit || limits.MaxMetricRollupScanRows < 0 ||
		limits.MaxMetricSketchBytes < 0 {
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
	snapshotRuntimeGeneration.Store(0)
	snapshotPublishMu.Unlock()
}

func SetSnapshotForTest(view SnapshotView) {
	cloned := cloneSnapshotView(view)
	snapshot := &runtimeSnapshot{
		view:                     cloned,
		poolByGroup:              make(map[string]int, len(cloned.Pools)),
		memberByPoolChannel:      make(map[poolChannelKey]int),
		credentialByFingerprint:  make(map[credentialFingerprintKey]int),
		modelByMemberModel:       make(map[memberModelKey]ModelSnapshot),
		channelByID:              make(map[int]ChannelSnapshot, len(cloned.Channels)),
		poolIndexByID:            make(map[int]int, len(cloned.Pools)),
		memberIndexesByPoolModel: make(map[poolModelKey][]int),
		poolSummaries:            make([]PoolSnapshotSummary, len(cloned.Pools)),
	}
	for index := range cloned.Channels {
		channel := cloned.Channels[index]
		snapshot.channelByID[channel.ID] = channel
	}
	for poolIndex := range cloned.Pools {
		pool := &snapshot.view.Pools[poolIndex]
		pool.MemberCount = len(pool.Members)
		snapshot.poolIndexByID[pool.ID] = poolIndex
		for memberIndex := range pool.Members {
			pool.Members[memberIndex].CredentialCount = len(pool.Members[memberIndex].CredentialIDs)
			pool.Members[memberIndex].ModelCount = len(pool.Members[memberIndex].Models)
		}
		snapshot.poolSummaries[poolIndex] = summarizePoolSnapshot(*pool)
		snapshot.poolByGroup[pool.GroupName] = pool.ID
		for memberIndex := range pool.Members {
			member := pool.Members[memberIndex]
			snapshot.memberByPoolChannel[poolChannelKey{PoolID: pool.ID, ChannelID: member.ChannelID}] = member.ID
			for modelIndex := range member.Models {
				observation := member.Models[modelIndex]
				snapshot.modelByMemberModel[memberModelKey{memberID: member.ID, model: observation.ModelName}] = observation
				key := poolModelKey{poolID: pool.ID, model: observation.ModelName}
				snapshot.memberIndexesByPoolModel[key] = append(snapshot.memberIndexesByPoolModel[key], memberIndex)
			}
		}
	}
	snapshot.telemetrySummary = telemetryAggregate(snapshot.view)
	snapshotPublishMu.Lock()
	currentSnapshot.Store(snapshot)
	snapshotPublishMu.Unlock()
}
