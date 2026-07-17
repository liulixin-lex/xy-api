package controller

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
)

const maxChannelRoutingCostEstimateBody = 32 << 10

type channelRoutingCostEstimateRequest struct {
	PoolID     int                              `json:"pool_id"`
	ModelName  string                           `json:"model_name"`
	MemberIDs  []int                            `json:"member_ids"`
	DecisionID string                           `json:"decision_id"`
	Profile    *channelRoutingManualCostProfile `json:"profile"`
}

type channelRoutingManualCostProfile struct {
	InputTokens             *int64   `json:"input_tokens"`
	MaximumInputTokens      *int64   `json:"maximum_input_tokens"`
	OutputTokens            *int64   `json:"output_tokens"`
	MaximumOutputTokens     *int64   `json:"maximum_output_tokens"`
	CacheReadTokens         *int64   `json:"cache_read_tokens"`
	CacheWriteTokens        *int64   `json:"cache_write_tokens"`
	CacheWriteOneHourTokens *int64   `json:"cache_write_1h_tokens"`
	ImageInputTokens        *int64   `json:"image_input_tokens"`
	ImageOutputTokens       *int64   `json:"image_output_tokens"`
	AudioInputTokens        *int64   `json:"audio_input_tokens"`
	AudioOutputTokens       *int64   `json:"audio_output_tokens"`
	ImageUnits              *float64 `json:"image_units"`
	AudioSeconds            *float64 `json:"audio_seconds"`
	VideoSeconds            *float64 `json:"video_seconds"`
	TaskUnits               *float64 `json:"task_units"`
	MaxAttempts             int      `json:"max_attempts"`
	RetryProbability        float64  `json:"retry_probability"`
	HedgeProbability        float64  `json:"hedge_probability"`
	HedgeAllowed            bool     `json:"hedge_allowed"`
}

func ListChannelRoutingCostCatalogPools(c *gin.Context) {
	page, pageSize := parseChannelRoutingPage(c)
	search := strings.TrimSpace(c.Query("search"))
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 128 {
		writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_catalog_filter", "invalid cost catalog filter")
		return
	}
	items, total, metadata, ok := channelrouting.ListCostCatalogPoolSummaries(
		search, channelRoutingPageOffset(page, pageSize), pageSize,
	)
	if !ok {
		writeChannelRoutingSnapshotInitializing(c)
		return
	}
	common.ApiSuccess(c, gin.H{
		"items": items, "total": total, "page": page, "page_size": pageSize,
		"policy_revision": metadata.PolicyRevision, "topology_epoch": metadata.TopologyEpoch,
		"pricing_epoch": metadata.PricingEpoch, "pricing_hash": metadata.PricingHash,
		"snapshot_built_at": metadata.BuiltAtUnix,
	})
}

func ListChannelRoutingCostCatalogMembers(c *gin.Context) {
	poolID, err := strconv.Atoi(strings.TrimSpace(c.Param("pool_id")))
	if err != nil || poolID <= 0 {
		writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_catalog_pool", "invalid cost catalog pool")
		return
	}
	page, pageSize := parseChannelRoutingPage(c)
	search := strings.TrimSpace(c.Query("search"))
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 128 {
		writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_catalog_filter", "invalid cost catalog filter")
		return
	}
	items, total, metadata, ok := channelrouting.ListCostCatalogMemberSummaries(
		poolID, search, channelRoutingPageOffset(page, pageSize), pageSize,
	)
	if !ok {
		writeChannelRoutingSnapshotInitializing(c)
		return
	}
	common.ApiSuccess(c, gin.H{
		"items": items, "total": total, "page": page, "page_size": pageSize,
		"pool_id": poolID, "pricing_epoch": metadata.PricingEpoch, "pricing_hash": metadata.PricingHash,
		"snapshot_built_at": metadata.BuiltAtUnix,
	})
}

func ListChannelRoutingCostCatalogModels(c *gin.Context) {
	poolID, err := strconv.Atoi(strings.TrimSpace(c.Param("pool_id")))
	if err != nil || poolID <= 0 {
		writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_catalog_pool", "invalid cost catalog pool")
		return
	}
	memberID, err := strconv.Atoi(strings.TrimSpace(c.Param("member_id")))
	if err != nil || memberID <= 0 {
		writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_catalog_member", "invalid cost catalog member")
		return
	}
	page, pageSize := parseChannelRoutingPage(c)
	search := strings.TrimSpace(c.Query("search"))
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 256 {
		writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_catalog_filter", "invalid cost catalog filter")
		return
	}
	items, total, metadata, ok := channelrouting.ListCostCatalogModelSummaries(
		poolID, memberID, search, channelRoutingPageOffset(page, pageSize), pageSize,
	)
	if !ok {
		writeChannelRoutingSnapshotInitializing(c)
		return
	}
	common.ApiSuccess(c, gin.H{
		"items": items, "total": total, "page": page, "page_size": pageSize,
		"pool_id": poolID, "member_id": memberID,
		"pricing_epoch": metadata.PricingEpoch, "pricing_hash": metadata.PricingHash,
		"snapshot_built_at": metadata.BuiltAtUnix,
	})
}

func EstimateChannelRoutingCosts(c *gin.Context) {
	var request channelRoutingCostEstimateRequest
	if c.Request.Body == nil {
		writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_profile", "invalid request cost profile")
		return
	}
	body, readErr := io.ReadAll(io.LimitReader(c.Request.Body, maxChannelRoutingCostEstimateBody+1))
	if readErr != nil || len(body) == 0 || len(body) > maxChannelRoutingCostEstimateBody ||
		common.Unmarshal(body, &request) != nil {
		writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_profile", "invalid request cost profile")
		return
	}
	request.ModelName = strings.TrimSpace(request.ModelName)
	request.DecisionID = strings.TrimSpace(request.DecisionID)
	if request.PoolID <= 0 || len(request.MemberIDs) > channelrouting.RoutingCostComparisonMaxCandidates ||
		!utf8.ValidString(request.ModelName) || utf8.RuneCountInString(request.ModelName) > 256 ||
		!utf8.ValidString(request.DecisionID) || utf8.RuneCountInString(request.DecisionID) > 64 {
		writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_profile", "invalid request cost profile")
		return
	}

	profileSource := "manual"
	quantitySources := map[string]string{}
	var profile model.RoutingCostRequestProfile
	if request.DecisionID != "" {
		if request.Profile != nil {
			writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "ambiguous_cost_profile", "choose either a recent decision or a manual request profile")
			return
		}
		decisionProfile, decisionModel, err := channelrouting.RoutingCostProfileFromDecisionContext(
			c.Request.Context(), request.DecisionID,
		)
		if err != nil {
			if errors.Is(err, channelrouting.ErrRoutingCostProfileNotFound) {
				writeChannelRoutingCostCatalogError(c, http.StatusNotFound, "cost_profile_not_found", "recent request profile not found")
				return
			}
			writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_profile", "invalid recent request profile")
			return
		}
		if request.ModelName == "" {
			request.ModelName = decisionModel
		} else if decisionModel != "" && request.ModelName != decisionModel {
			writeChannelRoutingCostCatalogError(c, http.StatusConflict, "cost_profile_model_conflict", "the recent request profile belongs to another model")
			return
		}
		profile = decisionProfile
		profileSource = "recent_decision"
		quantitySources = channelRoutingCostQuantitySources(profile, profileSource)
	} else {
		if request.Profile == nil || request.ModelName == "" {
			writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_profile", "manual request cost profile is required")
			return
		}
		profile, quantitySources = request.Profile.routingCostProfile()
	}

	comparison, err := channelrouting.CompareRoutingCosts(channelrouting.RoutingCostComparisonRequest{
		PoolID: request.PoolID, ModelName: request.ModelName, MemberIDs: request.MemberIDs,
		Profile: profile, ProfileSource: profileSource, QuantitySources: quantitySources,
	})
	if err != nil {
		switch {
		case errors.Is(err, channelrouting.ErrRoutingCostComparisonUnavailable):
			writeChannelRoutingSnapshotInitializing(c)
		case errors.Is(err, channelrouting.ErrRoutingCostCatalogInvalid), errors.Is(err, model.ErrRoutingCostInvalid):
			writeChannelRoutingCostCatalogError(c, http.StatusBadRequest, "invalid_cost_profile", "invalid request cost profile")
		default:
			writeChannelRoutingCostCatalogError(c, http.StatusInternalServerError, "cost_estimate_failed", "request cost comparison failed")
		}
		return
	}
	common.ApiSuccess(c, comparison)
}

func (input channelRoutingManualCostProfile) routingCostProfile() (model.RoutingCostRequestProfile, map[string]string) {
	maxAttempts := input.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 1
	}
	profile := model.RoutingCostRequestProfile{
		MaxAttempts: maxAttempts, RetryProbability: input.RetryProbability,
		HedgeProbability: input.HedgeProbability, HedgeAllowed: input.HedgeAllowed,
		KnowledgeSpecified: true, InputTokensKnown: input.InputTokens != nil,
		MaximumCompletionKnown:       input.OutputTokens != nil,
		CacheReadTokensKnown:         input.CacheReadTokens != nil,
		CacheWriteTokensKnown:        input.CacheWriteTokens != nil,
		CacheWriteOneHourTokensKnown: input.CacheWriteOneHourTokens != nil,
		ImageInputTokensKnown:        input.ImageInputTokens != nil,
		ImageOutputTokensKnown:       input.ImageOutputTokens != nil,
		ImageUnitsKnown:              input.ImageUnits != nil,
		AudioInputTokensKnown:        input.AudioInputTokens != nil,
		AudioOutputTokensKnown:       input.AudioOutputTokens != nil,
		AudioDurationKnown:           input.AudioSeconds != nil,
		VideoDurationKnown:           input.VideoSeconds != nil,
		TaskUnitsKnown:               input.TaskUnits != nil,
		RequestInputKnown:            false, RequestPricingFeaturesKnown: true,
	}
	if input.InputTokens != nil {
		profile.PromptTokens = *input.InputTokens
		profile.MaximumPromptTokens = *input.InputTokens
	}
	if input.MaximumInputTokens != nil {
		profile.MaximumPromptTokens = *input.MaximumInputTokens
	}
	if input.OutputTokens != nil {
		profile.ExpectedCompletionTokens = *input.OutputTokens
		profile.MaximumCompletionTokens = *input.OutputTokens
	}
	if input.MaximumOutputTokens != nil {
		profile.MaximumCompletionTokens = *input.MaximumOutputTokens
	}
	if input.CacheReadTokens != nil {
		profile.CacheReadTokens = *input.CacheReadTokens
	}
	if input.CacheWriteTokens != nil {
		profile.CacheWriteTokens = *input.CacheWriteTokens
	}
	if input.CacheWriteOneHourTokens != nil {
		profile.CacheWriteOneHourTokens = *input.CacheWriteOneHourTokens
	}
	if input.ImageInputTokens != nil {
		profile.ImageInputTokens = *input.ImageInputTokens
	}
	if input.ImageOutputTokens != nil {
		profile.ImageOutputTokens = *input.ImageOutputTokens
	}
	if input.AudioInputTokens != nil {
		profile.AudioInputTokens = *input.AudioInputTokens
	}
	if input.AudioOutputTokens != nil {
		profile.AudioOutputTokens = *input.AudioOutputTokens
	}
	if input.ImageUnits != nil {
		profile.ImageUnits = *input.ImageUnits
	}
	if input.AudioSeconds != nil {
		profile.AudioSeconds = *input.AudioSeconds
	}
	if input.VideoSeconds != nil {
		profile.VideoSeconds = *input.VideoSeconds
	}
	if input.TaskUnits != nil {
		profile.TaskUnits = *input.TaskUnits
	}
	profile.UncataloguedSurchargePossible = profile.ImageUnits > 0 || profile.AudioSeconds > 0 ||
		profile.VideoSeconds > 0 || profile.TaskUnits > 0
	return profile, channelRoutingCostQuantitySources(profile, "manual")
}

func channelRoutingCostQuantitySources(profile model.RoutingCostRequestProfile, source string) map[string]string {
	sources := map[string]string{}
	if profile.InputTokensKnown {
		sources["input_tokens"] = source
	}
	if profile.MaximumCompletionKnown {
		sources["completion_tokens"] = source
	}
	if profile.CacheTokensKnown || profile.CacheReadTokensKnown {
		sources["cache_read_tokens"] = source
	}
	if profile.CacheTokensKnown || profile.CacheWriteTokensKnown {
		sources["cache_write_tokens"] = source
	}
	if profile.CacheTokensKnown || profile.CacheWriteOneHourTokensKnown {
		sources["cache_write_1h_tokens"] = source
	}
	if profile.ImageInputTokensKnown {
		sources["image_input_tokens"] = source
	}
	if profile.ImageOutputTokensKnown {
		sources["image_output_tokens"] = source
	}
	if profile.ImageUnitsKnown {
		sources["image_units"] = source
	}
	if profile.AudioInputTokensKnown {
		sources["audio_input_tokens"] = source
	}
	if profile.AudioOutputTokensKnown {
		sources["audio_output_tokens"] = source
	}
	if profile.AudioDurationKnown {
		sources["audio_seconds"] = source
	}
	if profile.VideoDurationKnown {
		sources["video_seconds"] = source
	}
	if profile.TaskUnitsKnown {
		sources["task_units"] = source
	}
	return sources
}

func writeChannelRoutingCostCatalogError(c *gin.Context, status int, code string, message string) {
	c.JSON(status, gin.H{
		"success": false, "code": code, "message": message,
		"retryable": status >= http.StatusInternalServerError,
		"impact":    "request_not_applied",
	})
}
