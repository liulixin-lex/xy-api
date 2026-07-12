package controller

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type channelRoutingOverview struct {
	APIVersion            string                      `json:"api_version"`
	Enabled               bool                        `json:"enabled"`
	LegacyMode            string                      `json:"legacy_mode"`
	DeploymentStage       string                      `json:"deployment_stage"`
	ControlPlaneAvailable bool                        `json:"control_plane_available"`
	ControlPlaneRevision  int64                       `json:"control_plane_revision"`
	RevisionLag           int64                       `json:"revision_lag"`
	RevisionAhead         int64                       `json:"revision_ahead"`
	PropagationStatus     string                      `json:"propagation_status"`
	SnapshotAvailable     bool                        `json:"snapshot_available"`
	SnapshotRevision      uint64                      `json:"snapshot_revision"`
	RuntimeGeneration     uint64                      `json:"runtime_generation"`
	PolicyHash            string                      `json:"policy_hash"`
	NodeEpochID           string                      `json:"node_epoch_id"`
	SnapshotBuiltAt       int64                       `json:"snapshot_built_at"`
	SnapshotAgeSec        int64                       `json:"snapshot_age_sec"`
	SnapshotStale         bool                        `json:"snapshot_stale"`
	Telemetry             channelRoutingTelemetryView `json:"telemetry"`
	Topology              channelRoutingTopologyView  `json:"topology"`
	Runtime               channelrouting.RuntimeStats `json:"runtime"`
}

type channelRoutingNodeView struct {
	NodeID         string `json:"node_id"`
	PolicyRevision int64  `json:"policy_revision"`
	PolicyHash     string `json:"policy_hash,omitempty"`
	RevisionLag    int64  `json:"revision_lag"`
	RevisionAhead  int64  `json:"revision_ahead"`
	ObservedTime   int64  `json:"observed_time"`
	ExpiresTime    int64  `json:"expires_time"`
	Status         string `json:"status"`
	Stale          bool   `json:"stale"`
	Current        bool   `json:"current"`
}

type channelRoutingTelemetryView struct {
	Status                    string   `json:"status"`
	Reason                    string   `json:"reason,omitempty"`
	MetricRollupRows          int      `json:"metric_rollup_rows"`
	MetricRollupRowLimit      int      `json:"metric_rollup_row_limit"`
	MetricRollupScannedRows   int      `json:"metric_rollup_scanned_rows"`
	MetricRollupScanLimit     int      `json:"metric_rollup_scan_limit"`
	MetricSketchBytes         int64    `json:"metric_sketch_bytes"`
	MetricSketchByteLimit     int64    `json:"metric_sketch_byte_limit"`
	ObservedRequests          int64    `json:"observed_requests"`
	ObservedSuccesses         int64    `json:"observed_successes"`
	LogicalSuccessRate        *float64 `json:"logical_success_rate,omitempty"`
	P95TTFTMs                 *float64 `json:"p95_ttft_ms,omitempty"`
	P95TTFTStatus             string   `json:"p95_ttft_status"`
	MaxMemberP95TTFTMs        float64  `json:"max_member_p95_ttft_ms"`
	OutputTokensPerSecond     *float64 `json:"output_tokens_per_second,omitempty"`
	UnknownClassificationRate *float64 `json:"unknown_classification_rate,omitempty"`
	Coverage                  float64  `json:"coverage"`
}

type channelRoutingTopologyView struct {
	Pools                int     `json:"pools"`
	Members              int     `json:"members"`
	Channels             int     `json:"channels"`
	Credentials          int     `json:"credentials"`
	CredentialCoverage   float64 `json:"credential_coverage"`
	InvalidNumericValues int     `json:"invalid_numeric_values"`
}

type channelRoutingDecisionCandidates struct {
	Truncated  bool                               `json:"truncated"`
	Candidates []channelrouting.DecisionCandidate `json:"candidates"`
}

type channelRoutingDecisionGate struct {
	ActivationID       int64                     `json:"activation_id"`
	ActivationStage    string                    `json:"activation_stage"`
	PolicyRevision     int64                     `json:"policy_revision"`
	TrafficBasisPoints int                       `json:"traffic_basis_points"`
	Bucket             int                       `json:"bucket"`
	InCanary           bool                      `json:"in_canary"`
	RolloutKey         channelrouting.RolloutKey `json:"rollout_key"`
}

type channelRoutingDecisionIdentity struct {
	SnapshotRevision int64 `json:"snapshot_revision"`
	PoolID           int   `json:"pool_id"`
	MemberID         int   `json:"member_id"`
	CredentialID     int   `json:"credential_id"`
	ChannelID        int   `json:"channel_id"`
}

type channelRoutingDecisionView struct {
	ID                    int                                   `json:"id"`
	DecisionID            string                                `json:"decision_id"`
	RequestID             string                                `json:"request_id"`
	PoolID                int                                   `json:"pool_id"`
	GroupName             string                                `json:"group_name"`
	ModelName             string                                `json:"model_name"`
	SnapshotRevision      int64                                 `json:"snapshot_revision"`
	RuntimeGeneration     int64                                 `json:"runtime_generation"`
	PolicyHash            string                                `json:"policy_hash,omitempty"`
	SnapshotHash          string                                `json:"snapshot_hash,omitempty"`
	ProfileHash           string                                `json:"profile_hash,omitempty"`
	AlgorithmVersion      string                                `json:"algorithm_version"`
	Seed                  int64                                 `json:"seed"`
	RetryIndex            int                                   `json:"retry_index"`
	IsStream              bool                                  `json:"is_stream"`
	ActualChannelID       int                                   `json:"actual_channel_id"`
	ObservedChannelID     int                                   `json:"observed_channel_id"`
	CandidateCount        int                                   `json:"candidate_count"`
	EligibleCount         int                                   `json:"eligible_count"`
	FilteredOpen          int                                   `json:"filtered_open"`
	FilteredCapacity      int                                   `json:"filtered_capacity"`
	BreakerBypassed       bool                                  `json:"breaker_bypassed"`
	ObservedMatchesActual bool                                  `json:"observed_matches_actual"`
	DifferenceType        string                                `json:"difference_type,omitempty"`
	ActualCostKnown       bool                                  `json:"actual_cost_known"`
	ActualExpectedCost    float64                               `json:"actual_expected_cost"`
	ObservedCostKnown     bool                                  `json:"observed_cost_known"`
	ObservedExpectedCost  float64                               `json:"observed_expected_cost"`
	ExpectedCostDelta     float64                               `json:"expected_cost_delta"`
	Replayable            bool                                  `json:"replayable"`
	Gate                  *channelRoutingDecisionGate           `json:"gate,omitempty"`
	Cohort                string                                `json:"cohort,omitempty"`
	SelectedIdentity      *channelRoutingDecisionIdentity       `json:"selected_identity,omitempty"`
	CapacityAdmission     *channelrouting.CapacityAdmission     `json:"capacity_admission,omitempty"`
	ExclusionSummary      model.RoutingDecisionExclusionSummary `json:"exclusion_summary"`
	Candidates            channelRoutingDecisionCandidates      `json:"candidate_set"`
	CreatedTime           int64                                 `json:"created_time"`
}

func GetChannelRoutingOverview(c *gin.Context) {
	setting := smart_routing_setting.GetSetting()
	overview := channelRoutingOverview{
		APIVersion:      "v2",
		Enabled:         setting.Enabled,
		LegacyMode:      setting.Mode,
		DeploymentStage: channelRoutingDeploymentStage(setting.Mode),
		NodeEpochID:     channelrouting.NodeEpochID(),
		Runtime:         channelrouting.CurrentRuntimeStats(),
		Telemetry: channelRoutingTelemetryView{
			Status:        "unavailable",
			Reason:        "snapshot_initializing",
			P95TTFTStatus: "no_samples",
		},
		PropagationStatus: "initializing",
	}
	head, controlPlaneAvailable := channelRoutingPolicyHeadState(c.Request.Context())
	overview.ControlPlaneAvailable = controlPlaneAvailable
	if controlPlaneAvailable {
		overview.ControlPlaneRevision = head.CurrentRevision
		if head.CurrentStage != "" {
			overview.DeploymentStage = head.CurrentStage
		}
	} else {
		overview.PropagationStatus = "unknown"
	}
	metadata, aggregate, ok := channelrouting.CurrentSnapshotSummary()
	if !ok {
		common.ApiSuccess(c, overview)
		return
	}

	now := common.GetTimestamp()
	age := now - metadata.BuiltAtUnix
	if age < 0 {
		age = 0
	}
	overview.SnapshotAvailable = true
	overview.SnapshotRevision = metadata.Revision
	overview.RuntimeGeneration = metadata.RuntimeGeneration
	overview.PolicyHash = metadata.PolicyHash
	overview.NodeEpochID = metadata.NodeEpochID
	overview.SnapshotBuiltAt = metadata.BuiltAtUnix
	overview.SnapshotAgeSec = age
	overview.SnapshotStale = age > int64(setting.SnapshotStaleSec)
	if !overview.ControlPlaneAvailable {
		overview.PropagationStatus = "unknown"
	} else if overview.ControlPlaneRevision > int64(metadata.Revision) {
		overview.RevisionLag = overview.ControlPlaneRevision - int64(metadata.Revision)
		overview.PropagationStatus = "lagging"
	} else if overview.ControlPlaneRevision < int64(metadata.Revision) {
		overview.RevisionAhead = int64(metadata.Revision) - overview.ControlPlaneRevision
		overview.PropagationStatus = "ahead"
	} else if head.CurrentHash != metadata.PolicyHash {
		overview.PropagationStatus = "conflict"
	} else {
		overview.PropagationStatus = "converged"
	}
	overview.Topology = channelRoutingTopologyView{
		Pools:                metadata.Stats.PoolCount,
		Members:              metadata.Stats.MemberCount,
		Channels:             metadata.Stats.ChannelCount,
		Credentials:          metadata.Stats.CredentialCount,
		CredentialCoverage:   metadata.Stats.CredentialCoverage,
		InvalidNumericValues: metadata.Stats.InvalidNumericValues,
	}
	overview.Telemetry.Coverage = metadata.Stats.TelemetryCoverage
	overview.Telemetry.Status = metadata.Stats.MetricTelemetryStatus
	overview.Telemetry.Reason = metadata.Stats.MetricTelemetryReason
	overview.Telemetry.MetricRollupRows = metadata.Stats.MetricRollupRows
	overview.Telemetry.MetricRollupRowLimit = metadata.Stats.MetricRollupRowLimit
	overview.Telemetry.MetricRollupScannedRows = metadata.Stats.MetricRollupScannedRows
	overview.Telemetry.MetricRollupScanLimit = metadata.Stats.MetricRollupScanLimit
	overview.Telemetry.MetricSketchBytes = metadata.Stats.MetricSketchBytes
	overview.Telemetry.MetricSketchByteLimit = metadata.Stats.MetricSketchByteLimit
	overview.Telemetry.UnknownClassificationRate = metadata.Stats.UnknownClassificationRate
	overview.Telemetry.ObservedRequests = aggregate.ObservedRequests
	overview.Telemetry.ObservedSuccesses = aggregate.ObservedSuccesses
	overview.Telemetry.MaxMemberP95TTFTMs = aggregate.MaxMemberP95TTFTMs
	if overview.Telemetry.Status == "unavailable" {
		overview.Telemetry.P95TTFTStatus = "unavailable"
	} else if aggregate.P95TTFTKnown {
		p95TTFT := aggregate.P95TTFTMs
		overview.Telemetry.P95TTFTMs = &p95TTFT
		overview.Telemetry.P95TTFTStatus = "available"
	} else if aggregate.ObservedRequests > 0 {
		overview.Telemetry.P95TTFTStatus = "insufficient_distribution_coverage"
	}
	if overview.Telemetry.ObservedRequests > 0 {
		rate := float64(overview.Telemetry.ObservedSuccesses) / float64(overview.Telemetry.ObservedRequests)
		if rate < 0 {
			rate = 0
		} else if rate > 1 {
			rate = 1
		}
		overview.Telemetry.LogicalSuccessRate = &rate
	}
	if aggregate.GenerationMs > 0 && aggregate.OutputTokens > 0 {
		tokensPerSecond := float64(aggregate.OutputTokens) / (float64(aggregate.GenerationMs) / 1000)
		if !math.IsNaN(tokensPerSecond) && !math.IsInf(tokensPerSecond, 0) {
			overview.Telemetry.OutputTokensPerSecond = &tokensPerSecond
		}
	}
	common.ApiSuccess(c, overview)
}

func ListChannelRoutingNodes(c *gin.Context) {
	limit := parseChannelRoutingLimit(c, 50)
	beforeObservedTime, beforeID, err := parseChannelRoutingNodeCursor(c.Query("cursor"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing node cursor"})
		return
	}
	now := common.GetTimestamp()
	checkpoints, hasMore, err := model.ListActiveRoutingRuntimeCheckpointsContext(
		c.Request.Context(),
		channelrouting.RoutingConfigCheckpointKind,
		channelrouting.RoutingConfigCheckpointScope,
		now,
		beforeObservedTime,
		beforeID,
		limit,
	)
	if err != nil {
		common.ApiErrorMsg(c, "failed to load channel routing nodes")
		return
	}
	head, controlPlaneAvailable := channelRoutingPolicyHeadState(c.Request.Context())
	refreshSeconds := int64(smart_routing_setting.GetSetting().HotcacheRefreshSec)
	if refreshSeconds < 30 {
		refreshSeconds = 30
	}
	staleAfter := int64(math.MaxInt64)
	if refreshSeconds <= math.MaxInt64/3 {
		staleAfter = refreshSeconds * 3
	}
	items := make([]channelRoutingNodeView, 0, len(checkpoints))
	for index := range checkpoints {
		checkpoint := checkpoints[index]
		lag := int64(0)
		ahead := int64(0)
		var payload struct {
			PolicyHash string `json:"policy_hash"`
		}
		if err := checkpoint.DecodePayload(&payload); err != nil {
			payload.PolicyHash = ""
		}
		stale := checkpoint.ObservedTime <= 0 || now-checkpoint.ObservedTime > staleAfter
		status := "unknown"
		if controlPlaneAvailable {
			if head.CurrentRevision > checkpoint.PolicyRevision {
				lag = head.CurrentRevision - checkpoint.PolicyRevision
				status = "lagging"
			} else if head.CurrentRevision < checkpoint.PolicyRevision {
				ahead = checkpoint.PolicyRevision - head.CurrentRevision
				status = "ahead"
			} else if payload.PolicyHash == "" {
				status = "unknown"
			} else if payload.PolicyHash != head.CurrentHash {
				status = "conflict"
			} else if stale {
				status = "stale"
			} else {
				status = "converged"
			}
		}
		items = append(items, channelRoutingNodeView{
			NodeID:         checkpoint.NodeID,
			PolicyRevision: checkpoint.PolicyRevision,
			PolicyHash:     payload.PolicyHash,
			RevisionLag:    lag,
			RevisionAhead:  ahead,
			ObservedTime:   checkpoint.ObservedTime,
			ExpiresTime:    checkpoint.ExpiresTime,
			Status:         status,
			Stale:          stale,
			Current:        checkpoint.NodeID == channelrouting.NodeEpochID(),
		})
	}
	nextCursor := ""
	if hasMore && len(checkpoints) > 0 {
		last := checkpoints[len(checkpoints)-1]
		nextCursor = strconv.FormatInt(last.ObservedTime, 10) + ":" + strconv.FormatInt(last.ID, 10)
	}
	common.ApiSuccess(c, gin.H{
		"items":                   items,
		"next_cursor":             nextCursor,
		"limit":                   limit,
		"control_plane_available": controlPlaneAvailable,
		"control_plane_revision":  head.CurrentRevision,
	})
}

func ListChannelRoutingGroups(c *gin.Context) {
	page, pageSize := parseChannelRoutingPage(c)
	if len([]rune(c.Query("search"))) > 256 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel routing group search exceeds limit"})
		return
	}
	items, total, metadata, ok := channelrouting.ListPoolSnapshotSummaries(
		c.Query("search"),
		channelRoutingPageOffset(page, pageSize),
		pageSize,
	)
	if !ok {
		writeChannelRoutingSnapshotInitializing(c)
		return
	}
	common.ApiSuccess(c, gin.H{
		"items":             items,
		"total":             total,
		"page":              page,
		"page_size":         pageSize,
		"snapshot_revision": metadata.Revision,
		"snapshot_built_at": metadata.BuiltAtUnix,
	})
}

func GetChannelRoutingGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing group id"})
		return
	}
	page, pageSize := parseChannelRoutingPage(c)
	modelLimit, err := parseChannelRoutingBoundedLimit(c.Query("model_limit"), 50)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing model limit"})
		return
	}
	credentialLimit, err := parseChannelRoutingBoundedLimit(c.Query("credential_limit"), 50)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing credential limit"})
		return
	}
	pool, metadata, found := channelrouting.GetPoolSnapshotPage(
		id,
		channelRoutingPageOffset(page, pageSize),
		pageSize,
		modelLimit,
		credentialLimit,
	)
	if !found {
		if _, available := channelrouting.CurrentSnapshotMetadata(); !available {
			writeChannelRoutingSnapshotInitializing(c)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "channel routing group not found"})
		return
	}
	summary, _, summaryFound := channelrouting.GetPoolSnapshotSummary(id)
	if !summaryFound {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "channel routing group not found"})
		return
	}
	nextPage := 0
	if channelRoutingPageOffset(page, pageSize)+len(pool.Members) < pool.MemberCount {
		nextPage = page + 1
	}
	common.ApiSuccess(c, gin.H{
		"group":             pool,
		"summary":           summary,
		"page":              page,
		"page_size":         pageSize,
		"next_page":         nextPage,
		"model_limit":       modelLimit,
		"credential_limit":  credentialLimit,
		"snapshot_revision": metadata.Revision,
		"snapshot_built_at": metadata.BuiltAtUnix,
	})
}

func ListChannelRoutingChannels(c *gin.Context) {
	page, pageSize := parseChannelRoutingPage(c)
	if len([]rune(c.Query("search"))) > 256 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel routing channel search exceeds limit"})
		return
	}
	status, err := parseOptionalChannelRoutingInt(c.Query("status"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel status filter"})
		return
	}
	channelType, err := parseOptionalChannelRoutingInt(c.Query("type"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel type filter"})
		return
	}
	items, total, metadata, ok := channelrouting.ListChannelSnapshots(
		c.Query("search"),
		status,
		channelType,
		channelRoutingPageOffset(page, pageSize),
		pageSize,
	)
	if !ok {
		writeChannelRoutingSnapshotInitializing(c)
		return
	}
	common.ApiSuccess(c, gin.H{
		"items":             items,
		"total":             total,
		"page":              page,
		"page_size":         pageSize,
		"snapshot_revision": metadata.Revision,
		"snapshot_built_at": metadata.BuiltAtUnix,
	})
}

func ListChannelRoutingCosts(c *gin.Context) {
	page, pageSize := parseChannelRoutingPage(c)
	groupFilter := strings.TrimSpace(c.Query("group"))
	modelFilter := strings.TrimSpace(c.Query("model"))
	if len([]rune(groupFilter)) > 64 || len([]rune(modelFilter)) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel routing cost filter exceeds limit"})
		return
	}
	known, err := parseOptionalChannelRoutingBool(c.Query("known"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing cost filter"})
		return
	}
	items, total, metadata, ok := channelrouting.ListCostSnapshots(
		groupFilter,
		modelFilter,
		known,
		channelRoutingPageOffset(page, pageSize),
		pageSize,
	)
	if !ok {
		writeChannelRoutingSnapshotInitializing(c)
		return
	}
	common.ApiSuccess(c, gin.H{
		"items":             items,
		"total":             total,
		"page":              page,
		"page_size":         pageSize,
		"snapshot_revision": metadata.Revision,
		"snapshot_built_at": metadata.BuiltAtUnix,
	})
}

func ListChannelRoutingProbes(c *gin.Context) {
	limit := parseChannelRoutingLimit(c, 50)
	poolID, err := parseOptionalChannelRoutingInt(c.Query("pool_id"))
	if err != nil || (poolID != nil && *poolID <= 0) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing probe pool filter"})
		return
	}
	channelID, err := parseOptionalChannelRoutingInt(c.Query("channel_id"))
	if err != nil || (channelID != nil && *channelID <= 0) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing probe channel filter"})
		return
	}
	beforeID, err := parseOptionalChannelRoutingInt(c.Query("cursor"))
	if err != nil || (beforeID != nil && *beforeID <= 0) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing probe cursor"})
		return
	}
	outcome := strings.TrimSpace(c.Query("outcome"))
	switch outcome {
	case "", model.RoutingProbeOutcomeSuccess, model.RoutingProbeOutcomeFailure, model.RoutingProbeOutcomeTimeout,
		model.RoutingProbeOutcomeCanceled, model.RoutingProbeOutcomeLocalError:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing probe outcome"})
		return
	}
	filter := model.RoutingProbeResultFilter{
		Outcome: outcome,
		Limit:   limit,
	}
	if poolID != nil {
		filter.PoolID = *poolID
	}
	if channelID != nil {
		filter.ChannelID = *channelID
	}
	if beforeID != nil {
		filter.BeforeID = *beforeID
	}
	results, err := model.ListRoutingProbeResultsContext(c.Request.Context(), filter)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	nextCursor := ""
	if len(results) == limit {
		nextCursor = strconv.Itoa(results[len(results)-1].ID)
	}
	common.ApiSuccess(c, gin.H{
		"items":       results,
		"next_cursor": nextCursor,
	})
}

func ListChannelRoutingDecisions(c *gin.Context) {
	limit := parseChannelRoutingLimit(c, 50)
	cursor := 0
	if rawCursor := strings.TrimSpace(c.Query("cursor")); rawCursor != "" {
		parsedCursor, err := strconv.Atoi(rawCursor)
		if err != nil || parsedCursor <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing decision cursor"})
			return
		}
		cursor = parsedCursor
	}
	query := model.DB.WithContext(c.Request.Context()).Model(&model.RoutingDecisionAudit{})
	if cursor > 0 {
		query = query.Where("id < ?", cursor)
	}
	if group := strings.TrimSpace(c.Query("group")); group != "" {
		if !utf8.ValidString(group) || len([]rune(group)) > 64 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel routing decision group filter exceeds limit"})
			return
		}
		query = query.Where("group_key = ?", model.RoutingDecisionGroupKey(group))
	}
	if modelName := strings.TrimSpace(c.Query("model")); modelName != "" {
		if !utf8.ValidString(modelName) || len([]rune(modelName)) > 128 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel routing decision model filter exceeds limit"})
			return
		}
		query = query.Where("model_key = ?", model.RoutingDecisionModelKey(modelName))
	}
	if requestID := strings.TrimSpace(c.Query("request_id")); requestID != "" {
		if !utf8.ValidString(requestID) || len([]rune(requestID)) > 64 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel routing decision request filter exceeds limit"})
			return
		}
		query = query.Where("request_key = ?", model.RoutingDecisionRequestKey(requestID))
	}
	if matched := strings.TrimSpace(c.Query("matched")); matched != "" {
		value, err := strconv.ParseBool(matched)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing decision match filter"})
			return
		}
		query = query.Where("observed_matches_actual = ?", value)
	}
	if rawActivationID := strings.TrimSpace(c.Query("activation_id")); rawActivationID != "" {
		activationID, err := strconv.ParseInt(rawActivationID, 10, 64)
		if err != nil || activationID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing decision activation filter"})
			return
		}
		query = query.Where("activation_id = ?", activationID)
	}
	if rolloutKey := strings.TrimSpace(c.Query("rollout_key")); rolloutKey != "" {
		_, err := hex.DecodeString(rolloutKey)
		if err != nil || len(rolloutKey) != 64 || rolloutKey != strings.ToLower(rolloutKey) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing decision rollout filter"})
			return
		}
		query = query.Where("rollout_key = ?", rolloutKey)
	}
	if cohort := strings.TrimSpace(c.Query("cohort")); cohort != "" {
		if cohort != model.RoutingDecisionCohortControl && cohort != model.RoutingDecisionCohortCanary {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing decision cohort filter"})
			return
		}
		query = query.Where("cohort = ?", cohort)
	}
	var records []model.RoutingDecisionAudit
	if err := query.Order("id desc").Limit(limit + 1).Find(&records).Error; err != nil {
		common.ApiErrorMsg(c, "failed to load channel routing decisions")
		return
	}
	nextCursor := 0
	if len(records) > limit {
		records = records[:limit]
		nextCursor = records[len(records)-1].ID
	}
	items := make([]channelRoutingDecisionView, 0, len(records))
	for _, record := range records {
		items = append(items, buildChannelRoutingDecisionView(record))
	}
	common.ApiSuccess(c, gin.H{
		"items":       items,
		"next_cursor": nextCursor,
		"limit":       limit,
	})
}

func GetChannelRoutingDecision(c *gin.Context) {
	decisionID := strings.TrimSpace(c.Param("id"))
	if decisionID == "" || len(decisionID) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing decision id"})
		return
	}
	var record model.RoutingDecisionAudit
	if err := model.DB.WithContext(c.Request.Context()).Where("decision_id = ?", decisionID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "channel routing decision not found"})
			return
		}
		common.ApiErrorMsg(c, "failed to load channel routing decision")
		return
	}
	common.ApiSuccess(c, buildChannelRoutingDecisionView(record))
}

func writeChannelRoutingSnapshotInitializing(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"success": false,
		"message": "channel routing observe snapshot is initializing",
	})
}

func buildChannelRoutingDecisionView(record model.RoutingDecisionAudit) channelRoutingDecisionView {
	payload := channelRoutingDecisionCandidates{Candidates: []channelrouting.DecisionCandidate{}}
	if err := common.UnmarshalJsonStr(record.CandidatesJSON, &payload); err != nil {
		payload = channelRoutingDecisionCandidates{Truncated: true, Candidates: []channelrouting.DecisionCandidate{}}
	}
	exclusionSummary := model.RoutingDecisionExclusionSummary{Reasons: []model.RoutingDecisionExclusionCount{}}
	if record.ExclusionSummaryJSON != "" && common.UnmarshalJsonStr(record.ExclusionSummaryJSON, &exclusionSummary) != nil {
		exclusionSummary = model.RoutingDecisionExclusionSummary{Reasons: []model.RoutingDecisionExclusionCount{}}
	}
	var gate *channelRoutingDecisionGate
	if record.Cohort != "" {
		gate = &channelRoutingDecisionGate{
			ActivationID: record.ActivationID, ActivationStage: record.ActivationStage,
			PolicyRevision: record.SnapshotRevision, TrafficBasisPoints: record.TrafficBasisPoints,
			Bucket: record.CanaryBucket, InCanary: record.Cohort == model.RoutingDecisionCohortCanary,
			RolloutKey: channelrouting.RolloutKey(record.RolloutKey),
		}
	}
	var selectedIdentity *channelRoutingDecisionIdentity
	if record.SelectedMemberID > 0 {
		selectedIdentity = &channelRoutingDecisionIdentity{
			SnapshotRevision: record.SnapshotRevision, PoolID: record.PoolID, MemberID: record.SelectedMemberID,
			CredentialID: record.SelectedCredentialID, ChannelID: record.ObservedChannelID,
		}
	}
	var capacityAdmission *channelrouting.CapacityAdmission
	if record.ReservationMode != "" {
		capacityAdmission = &channelrouting.CapacityAdmission{
			Mode: channelrouting.CapacityMode(record.ReservationMode),
			Key: channelrouting.CapacityKey{
				PolicyRevision: uint64(record.SnapshotRevision), PoolID: record.PoolID,
				MemberID: record.SelectedMemberID, Model: record.ModelName,
			},
			Demand: channelrouting.Demand{
				RPM: record.ReservationRPM, InputTPM: record.ReservationInputTPM,
				OutputTPM: record.ReservationOutputTPM, Inflight: record.ReservationInflight,
			},
			Limit: channelrouting.Limit{
				RPM: record.ReservationLimitRPM, InputTPM: record.ReservationLimitInputTPM,
				OutputTPM: record.ReservationLimitOutputTPM, Inflight: record.ReservationLimitInflight,
			},
		}
	}
	return channelRoutingDecisionView{
		ID:                    record.ID,
		DecisionID:            record.DecisionID,
		RequestID:             record.RequestID,
		PoolID:                record.PoolID,
		GroupName:             record.GroupName,
		ModelName:             record.ModelName,
		SnapshotRevision:      record.SnapshotRevision,
		RuntimeGeneration:     record.RuntimeGeneration,
		PolicyHash:            record.PolicyHash,
		SnapshotHash:          record.SnapshotHash,
		ProfileHash:           record.ProfileHash,
		AlgorithmVersion:      record.AlgorithmVersion,
		Seed:                  record.Seed,
		RetryIndex:            record.RetryIndex,
		IsStream:              record.IsStream,
		ActualChannelID:       record.ActualChannelID,
		ObservedChannelID:     record.ObservedChannelID,
		CandidateCount:        record.CandidateCount,
		EligibleCount:         record.EligibleCount,
		FilteredOpen:          record.FilteredOpen,
		FilteredCapacity:      record.FilteredCapacity,
		BreakerBypassed:       record.BreakerBypassed,
		ObservedMatchesActual: record.ObservedMatchesActual,
		DifferenceType:        record.DifferenceType,
		ActualCostKnown:       record.ActualCostKnown,
		ActualExpectedCost:    record.ActualExpectedCost,
		ObservedCostKnown:     record.ObservedCostKnown,
		ObservedExpectedCost:  record.ObservedExpectedCost,
		ExpectedCostDelta:     record.ExpectedCostDelta,
		Replayable:            record.Replayable,
		Gate:                  gate,
		Cohort:                record.Cohort,
		SelectedIdentity:      selectedIdentity,
		CapacityAdmission:     capacityAdmission,
		ExclusionSummary:      exclusionSummary,
		Candidates:            payload,
		CreatedTime:           record.CreatedTime,
	}
}

func parseChannelRoutingPage(c *gin.Context) (int, int) {
	page := 1
	if parsed, err := strconv.Atoi(c.Query("page")); err == nil && parsed > 0 {
		page = parsed
	} else if parsed, err = strconv.Atoi(c.Query("p")); err == nil && parsed > 0 {
		page = parsed
	}
	pageSize := 20
	if parsed, err := strconv.Atoi(c.Query("page_size")); err == nil && parsed > 0 {
		pageSize = parsed
	}
	if pageSize > 100 {
		pageSize = 100
	}
	if page > 1_000_000 {
		page = 1_000_000
	}
	return page, pageSize
}

func parseChannelRoutingLimit(c *gin.Context, fallback int) int {
	limit := fallback
	if parsed, err := strconv.Atoi(c.Query("limit")); err == nil && parsed > 0 {
		limit = parsed
	}
	if limit > 100 {
		limit = 100
	}
	return limit
}

func parseChannelRoutingBoundedLimit(raw string, fallback int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > 100 {
		return 0, errors.New("invalid channel routing nested limit")
	}
	return value, nil
}

func parseChannelRoutingNodeCursor(raw string) (int64, int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, 0, nil
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("invalid channel routing node cursor")
	}
	observedTime, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || observedTime <= 0 {
		return 0, 0, errors.New("invalid channel routing node cursor")
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return 0, 0, errors.New("invalid channel routing node cursor")
	}
	return observedTime, id, nil
}

func channelRoutingPolicyHeadState(ctx context.Context) (model.RoutingPolicyHead, bool) {
	head, err := model.GetRoutingPolicyHeadContext(ctx)
	if err == nil {
		return head, true
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.RoutingPolicyHead{}, true
	}
	return model.RoutingPolicyHead{}, false
}

func parseOptionalChannelRoutingInt(raw string) (*int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func parseOptionalChannelRoutingBool(raw string) (*bool, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func channelRoutingDeploymentStage(mode string) string {
	switch mode {
	case smart_routing_setting.ModeObserve:
		return "observe"
	case smart_routing_setting.ModeShadow:
		return "shadow"
	case smart_routing_setting.ModeBalanced, smart_routing_setting.ModeEnterpriseSLO:
		return "active"
	default:
		return "observe"
	}
}

func channelRoutingPageOffset(page int, pageSize int) int {
	if page <= 1 || pageSize <= 0 {
		return 0
	}
	if page-1 > math.MaxInt/pageSize {
		return math.MaxInt
	}
	return (page - 1) * pageSize
}
