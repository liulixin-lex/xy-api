package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const channelRoutingGroupNestedItemBudget = 2_000

func channelRoutingGroupNestedBudgetValid(pageSize int, modelLimit int, credentialLimit int) bool {
	if pageSize < 1 || modelLimit < 1 || credentialLimit < 1 {
		return false
	}
	if modelLimit > channelRoutingGroupNestedItemBudget/pageSize {
		return false
	}
	remaining := channelRoutingGroupNestedItemBudget - pageSize*modelLimit
	return credentialLimit <= remaining/pageSize
}

func parseChannelRoutingDecisionTime(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid channel routing decision time")
	}
	return value, nil
}

func ListChannelRoutingDecisionCandidates(c *gin.Context) {
	decisionID := strings.TrimSpace(c.Param("id"))
	if decisionID == "" || !utf8.ValidString(decisionID) || utf8.RuneCountInString(decisionID) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing decision id"})
		return
	}
	limit, err := parseChannelRoutingBoundedLimit(c.Query("limit"), 50)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing candidate limit"})
		return
	}
	cursor := 0
	if raw := strings.TrimSpace(c.Query("cursor")); raw != "" {
		cursor, err = strconv.Atoi(raw)
		if err != nil || cursor < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing candidate cursor"})
			return
		}
	}

	var audit model.RoutingDecisionAudit
	if err := model.DB.WithContext(c.Request.Context()).Where("decision_id = ?", decisionID).First(&audit).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "channel routing decision not found"})
			return
		}
		common.ApiErrorMsg(c, "failed to load channel routing decision")
		return
	}
	page, err := channelrouting.ListDecisionCandidateDetailsContext(c.Request.Context(), audit, cursor, limit)
	if err != nil {
		switch {
		case errors.Is(err, channelrouting.ErrDecisionCandidatePageInvalid):
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"success": false, "code": "candidate_payload_invalid",
				"message": "channel routing decision candidate payload is invalid",
			})
		case errors.Is(err, channelrouting.ErrShadowReplayHash),
			errors.Is(err, channelrouting.ErrShadowReplayAudit),
			errors.Is(err, channelrouting.ErrShadowReplayInvalid),
			errors.Is(err, channelrouting.ErrBalancedReplayHash),
			errors.Is(err, channelrouting.ErrBalancedReplayInvalid),
			errors.Is(err, model.ErrRoutingDecisionReplayIntegrity):
			c.JSON(http.StatusConflict, gin.H{
				"success": false, "code": "candidate_replay_integrity_failed",
				"message": "channel routing decision candidate replay failed integrity verification",
			})
		default:
			common.ApiErrorMsg(c, "failed to load channel routing decision candidates")
		}
		return
	}
	common.ApiSuccess(c, page)
}

func ListChannelRoutingGroupReplayProfiles(c *gin.Context) {
	poolID, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil || poolID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing group id"})
		return
	}
	limit := 20
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > model.RoutingDecisionReplayProfileMaxLimit {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing replay profile limit"})
			return
		}
	}
	items, err := model.ListLatestRoutingDecisionReplayProfilesContext(c.Request.Context(), poolID, limit)
	if err != nil {
		common.ApiErrorMsg(c, "failed to load channel routing replay profiles")
		return
	}
	common.ApiSuccess(c, gin.H{"items": items, "limit": limit})
}

func GetChannelRoutingCost(c *gin.Context) {
	poolID, err := strconv.Atoi(strings.TrimSpace(c.Param("pool_id")))
	if err != nil || poolID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing cost pool id"})
		return
	}
	memberID, err := strconv.Atoi(strings.TrimSpace(c.Param("member_id")))
	if err != nil || memberID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing cost member id"})
		return
	}
	modelName := strings.TrimSpace(c.Query("model"))
	if modelName == "" || !utf8.ValidString(modelName) || utf8.RuneCountInString(modelName) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel routing cost model"})
		return
	}
	item, metadata, found := channelrouting.GetCostSnapshotDetail(poolID, memberID, modelName)
	if !found {
		if _, available := channelrouting.CurrentSnapshotMetadata(); !available {
			writeChannelRoutingSnapshotInitializing(c)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "channel routing cost snapshot not found"})
		return
	}
	common.ApiSuccess(c, gin.H{
		"item": item, "snapshot_revision": metadata.Revision, "snapshot_built_at": metadata.BuiltAtUnix,
	})
}
