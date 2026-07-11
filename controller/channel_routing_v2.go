package controller

import (
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
	APIVersion        string                      `json:"api_version"`
	Enabled           bool                        `json:"enabled"`
	LegacyMode        string                      `json:"legacy_mode"`
	DeploymentStage   string                      `json:"deployment_stage"`
	SnapshotAvailable bool                        `json:"snapshot_available"`
	SnapshotRevision  uint64                      `json:"snapshot_revision"`
	SnapshotBuiltAt   int64                       `json:"snapshot_built_at"`
	SnapshotAgeSec    int64                       `json:"snapshot_age_sec"`
	SnapshotStale     bool                        `json:"snapshot_stale"`
	Telemetry         channelRoutingTelemetryView `json:"telemetry"`
	Topology          channelRoutingTopologyView  `json:"topology"`
	Runtime           channelrouting.RuntimeStats `json:"runtime"`
}

type channelRoutingTelemetryView struct {
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

type channelRoutingGroupListItem struct {
	ID                int     `json:"id"`
	GroupName         string  `json:"group_name"`
	DisplayName       string  `json:"display_name"`
	Source            string  `json:"source"`
	MemberCount       int     `json:"member_count"`
	EnabledChannels   int     `json:"enabled_channels"`
	TelemetryCoverage float64 `json:"telemetry_coverage"`
	OpenModels        int     `json:"open_models"`
	DegradedModels    int     `json:"degraded_models"`
	KnownCostModels   int     `json:"known_cost_models"`
}

type channelRoutingDecisionCandidates struct {
	Truncated  bool                               `json:"truncated"`
	Candidates []channelrouting.DecisionCandidate `json:"candidates"`
}

type channelRoutingDecisionView struct {
	ID                    int                              `json:"id"`
	DecisionID            string                           `json:"decision_id"`
	RequestID             string                           `json:"request_id"`
	PoolID                int                              `json:"pool_id"`
	GroupName             string                           `json:"group_name"`
	ModelName             string                           `json:"model_name"`
	SnapshotRevision      int64                            `json:"snapshot_revision"`
	AlgorithmVersion      string                           `json:"algorithm_version"`
	RetryIndex            int                              `json:"retry_index"`
	IsStream              bool                             `json:"is_stream"`
	ActualChannelID       int                              `json:"actual_channel_id"`
	ObservedChannelID     int                              `json:"observed_channel_id"`
	CandidateCount        int                              `json:"candidate_count"`
	EligibleCount         int                              `json:"eligible_count"`
	FilteredOpen          int                              `json:"filtered_open"`
	FilteredCapacity      int                              `json:"filtered_capacity"`
	BreakerBypassed       bool                             `json:"breaker_bypassed"`
	ObservedMatchesActual bool                             `json:"observed_matches_actual"`
	Candidates            channelRoutingDecisionCandidates `json:"candidate_set"`
	CreatedTime           int64                            `json:"created_time"`
}

func GetChannelRoutingOverview(c *gin.Context) {
	setting := smart_routing_setting.GetSetting()
	overview := channelRoutingOverview{
		APIVersion:      "v2",
		Enabled:         setting.Enabled,
		LegacyMode:      setting.Mode,
		DeploymentStage: channelRoutingDeploymentStage(setting.Mode),
		Runtime:         channelrouting.CurrentRuntimeStats(),
		Telemetry: channelRoutingTelemetryView{
			P95TTFTStatus: "pending_mergeable_distribution",
		},
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
	overview.SnapshotBuiltAt = metadata.BuiltAtUnix
	overview.SnapshotAgeSec = age
	overview.SnapshotStale = age > int64(setting.SnapshotStaleSec)
	overview.Topology = channelRoutingTopologyView{
		Pools:                metadata.Stats.PoolCount,
		Members:              metadata.Stats.MemberCount,
		Channels:             metadata.Stats.ChannelCount,
		Credentials:          metadata.Stats.CredentialCount,
		CredentialCoverage:   metadata.Stats.CredentialCoverage,
		InvalidNumericValues: metadata.Stats.InvalidNumericValues,
	}
	overview.Telemetry.Coverage = metadata.Stats.TelemetryCoverage
	overview.Telemetry.UnknownClassificationRate = metadata.Stats.UnknownClassificationRate
	overview.Telemetry.ObservedRequests = aggregate.ObservedRequests
	overview.Telemetry.ObservedSuccesses = aggregate.ObservedSuccesses
	overview.Telemetry.MaxMemberP95TTFTMs = aggregate.MaxMemberP95TTFTMs
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

func ListChannelRoutingGroups(c *gin.Context) {
	page, pageSize := parseChannelRoutingPage(c)
	if len([]rune(c.Query("search"))) > 256 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel routing group search exceeds limit"})
		return
	}
	pools, total, metadata, ok := channelrouting.ListPoolSnapshots(
		c.Query("search"),
		channelRoutingPageOffset(page, pageSize),
		pageSize,
	)
	if !ok {
		writeChannelRoutingSnapshotInitializing(c)
		return
	}
	items := make([]channelRoutingGroupListItem, 0, len(pools))
	for _, pool := range pools {
		items = append(items, summarizeChannelRoutingGroup(pool))
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
	pool, metadata, found := channelrouting.GetPoolSnapshot(id)
	if !found {
		if _, available := channelrouting.CurrentSnapshotMetadata(); !available {
			writeChannelRoutingSnapshotInitializing(c)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "channel routing group not found"})
		return
	}
	common.ApiSuccess(c, gin.H{
		"group":             pool,
		"summary":           summarizeChannelRoutingGroup(pool),
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

func summarizeChannelRoutingGroup(pool channelrouting.PoolSnapshot) channelRoutingGroupListItem {
	item := channelRoutingGroupListItem{
		ID:          pool.ID,
		GroupName:   pool.GroupName,
		DisplayName: pool.DisplayName,
		Source:      pool.Source,
		MemberCount: len(pool.Members),
	}
	telemetryMembers := 0
	for _, member := range pool.Members {
		if member.PhysicalStatus == common.ChannelStatusEnabled {
			item.EnabledChannels++
		}
		if member.TelemetryKnown {
			telemetryMembers++
		}
		for _, observation := range member.Models {
			if observation.BreakerState == model.RoutingBreakerStateOpen || observation.BreakerState == model.RoutingBreakerStateHalfOpen {
				item.OpenModels++
			} else if observation.BreakerState == model.RoutingBreakerStateDegraded {
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

func buildChannelRoutingDecisionView(record model.RoutingDecisionAudit) channelRoutingDecisionView {
	payload := channelRoutingDecisionCandidates{Candidates: []channelrouting.DecisionCandidate{}}
	if err := common.UnmarshalJsonStr(record.CandidatesJSON, &payload); err != nil {
		payload = channelRoutingDecisionCandidates{Truncated: true, Candidates: []channelrouting.DecisionCandidate{}}
	}
	return channelRoutingDecisionView{
		ID:                    record.ID,
		DecisionID:            record.DecisionID,
		RequestID:             record.RequestID,
		PoolID:                record.PoolID,
		GroupName:             record.GroupName,
		ModelName:             record.ModelName,
		SnapshotRevision:      record.SnapshotRevision,
		AlgorithmVersion:      record.AlgorithmVersion,
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
