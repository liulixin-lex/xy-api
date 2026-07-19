package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const billingReservationAdminBodyLimit = 64 << 10

type billingReservationAdminRequest struct {
	RequestId       string `json:"request_id"`
	ExpectedVersion int    `json:"expected_version"`
	Resolution      string `json:"resolution"`
	ActualQuota     *int64 `json:"actual_quota"`
	Reason          string `json:"reason"`
}

func ListBillingReservationsForAdmin(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	userId := 0
	if rawUserId := strings.TrimSpace(c.Query("user_id")); rawUserId != "" {
		parsed, err := strconv.Atoi(rawUserId)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid billing reservation user ID"})
			return
		}
		userId = parsed
	}
	staleAge := billingReservationStaleAge()
	staleAfterSeconds := int64(staleAge.Seconds())
	page, err := model.ListBillingReservationsForAdmin(model.BillingReservationAdminFilters{
		RequestId:    c.Query("request_id"),
		UserId:       userId,
		ResourceType: c.Query("resource_type"),
		StaleBefore:  common.GetTimestamp() - staleAfterSeconds,
		Offset:       pageInfo.GetStartIdx(),
		Limit:        pageInfo.GetPageSize(),
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	page.StaleAfterSeconds = staleAfterSeconds
	common.ApiSuccess(c, page)
}

func GetBillingReservationForAdmin(c *gin.Context) {
	detail, err := model.GetBillingReservationAdminDetail(c.Param("request_id"))
	if err != nil {
		if errors.Is(err, model.ErrBillingReservationNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Billing reservation not found"})
			return
		}
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, detail)
}

func ResolveBillingReservationForAdmin(c *gin.Context) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "Billing reservation resolution requires dashboard session authentication",
		})
		return
	}
	requestId := strings.TrimSpace(c.Param("request_id"))
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, billingReservationAdminBodyLimit)
	var request billingReservationAdminRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || strings.TrimSpace(request.RequestId) != requestId {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid billing reservation resolution"})
		return
	}
	result, err := model.ResolveBillingReservationByAdmin(model.BillingReservationAdminResolutionInput{
		RequestId:       requestId,
		ExpectedVersion: request.ExpectedVersion,
		AdminId:         c.GetInt("id"),
		ActorIp:         c.ClientIP(),
		Resolution:      request.Resolution,
		ActualQuota:     request.ActualQuota,
		Reason:          request.Reason,
	})
	if err != nil {
		switch {
		case errors.Is(err, model.ErrBillingReservationNotFound):
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": err.Error()})
		case errors.Is(err, model.ErrBillingReservationVersionConflict),
			errors.Is(err, model.ErrBillingAdminResolutionConflict),
			errors.Is(err, model.ErrBillingReservationFinalized),
			errors.Is(err, model.ErrBillingReservationReviewRequired),
			errors.Is(err, model.ErrBillingReservationConflict),
			errors.Is(err, model.ErrInsufficientUserQuota),
			errors.Is(err, model.ErrInsufficientTokenQuota),
			errors.Is(err, model.ErrInsufficientSubscriptionQuota),
			errors.Is(err, model.ErrNoActiveSubscription),
			errors.Is(err, model.ErrBillingAccountNotFound),
			errors.Is(err, model.ErrQuotaOverflow):
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": err.Error()})
		case errors.Is(err, model.ErrBillingAdminResolutionInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to resolve billing reservation"})
		}
		return
	}
	recordManageAudit(c, "billing.reservation.resolve", map[string]interface{}{
		"request_id":       requestId,
		"expected_version": request.ExpectedVersion,
		"resolution":       request.Resolution,
		"actual_quota":     request.ActualQuota,
		"applied":          result.Applied,
	})
	common.ApiSuccess(c, result)
}
