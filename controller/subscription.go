package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---- Shared types ----

type SubscriptionPlanDTO struct {
	Plan                 model.SubscriptionPlan `json:"plan"`
	StripePriceIDPurpose string                 `json:"stripe_price_id_purpose,omitempty"`
}

type PublicSubscriptionPlanDTO struct {
	Plan PublicSubscriptionPlan `json:"plan"`
}

// PublicSubscriptionPlan deliberately excludes every provider product/price
// identifier. Those identifiers are routing inventory for administrators and
// must never become part of a user's plan contract or DOM.
type PublicSubscriptionPlan struct {
	ID                      int      `json:"id"`
	Title                   string   `json:"title"`
	Subtitle                string   `json:"subtitle"`
	PriceAmount             float64  `json:"price_amount"`
	Currency                string   `json:"currency"`
	DurationUnit            string   `json:"duration_unit"`
	DurationValue           int      `json:"duration_value"`
	CustomSeconds           int64    `json:"custom_seconds"`
	AllowBalancePay         *bool    `json:"allow_balance_pay"`
	MaxPurchasePerUser      int      `json:"max_purchase_per_user"`
	IncludesExpandedAccess  bool     `json:"includes_expanded_access"`
	TotalAmount             int64    `json:"total_amount"`
	QuotaResetPeriod        string   `json:"quota_reset_period"`
	QuotaResetCustomSeconds int64    `json:"quota_reset_custom_seconds"`
	ExternalPaymentRouteIDs []string `json:"external_payment_route_ids,omitempty"`
}

// PublicUserSubscription is the user-visible entitlement projection. It must
// not grow provider, accounting, ownership, or internal group fields merely
// because UserSubscription gains a new database column.
type PublicUserSubscription struct {
	ID            int    `json:"id"`
	PlanID        int    `json:"plan_id"`
	PlanTitle     string `json:"plan_title"`
	AmountTotal   int64  `json:"amount_total"`
	AmountUsed    int64  `json:"amount_used"`
	StartTime     int64  `json:"start_time"`
	EndTime       int64  `json:"end_time"`
	Status        string `json:"status"`
	NextResetTime int64  `json:"next_reset_time"`
}

type PublicSubscriptionSummary struct {
	Subscription PublicUserSubscription `json:"subscription"`
}

type PublicSubscriptionSelf struct {
	BillingPreference string                      `json:"billing_preference"`
	Subscriptions     []PublicSubscriptionSummary `json:"subscriptions"`
	AllSubscriptions  []PublicSubscriptionSummary `json:"all_subscriptions"`
}

type BillingPreferenceRequest struct {
	BillingPreference string `json:"billing_preference"`
}

type SubscriptionBalancePayRequest struct {
	PlanId    int    `json:"plan_id"`
	RequestId string `json:"request_id,omitempty"`
}

// ---- User APIs ----

func GetSubscriptionPlans(c *gin.Context) {
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("subscription payment configuration sync failed user_id=%d error=%q", c.GetInt("id"), err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	if !operation_setting.IsPaymentComplianceConfirmed() {
		unlockPaymentConfiguration()
		common.ApiSuccess(c, []PublicSubscriptionPlanDTO{})
		return
	}
	unlockPaymentConfiguration()

	var plans []model.SubscriptionPlan
	if err := model.DB.Where("enabled = ?", true).Order("sort_order desc, id desc").Find(&plans).Error; err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("subscription plan list failed user_id=%d error=%q", c.GetInt("id"), err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("subscription payment configuration resync failed user_id=%d error=%q", c.GetInt("id"), err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}
	unlockPaymentConfiguration = setting.LockPaymentConfigurationForRead()
	if !operation_setting.IsPaymentComplianceConfirmed() {
		unlockPaymentConfiguration()
		common.ApiSuccess(c, []PublicSubscriptionPlanDTO{})
		return
	}
	publicRoutes, err := service.PublicPaymentRoutesLocked()
	if err != nil {
		unlockPaymentConfiguration()
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("subscription payment route projection failed user_id=%d error=%q", c.GetInt("id"), err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}
	result := make([]PublicSubscriptionPlanDTO, 0, len(plans))
	for _, p := range plans {
		p.NormalizeDefaults()
		eligibleRoutes := make([]service.PublicPaymentRoute, 0, len(publicRoutes))
		for _, route := range publicRoutes {
			if service.ValidateSubscriptionPlanForPaymentRoute(route.Provider, route.PaymentMethod, &p) == nil {
				eligibleRoutes = append(eligibleRoutes, route)
			}
		}
		result = append(result, PublicSubscriptionPlanDTO{
			Plan: publicSubscriptionPlan(p, eligibleRoutes),
		})
	}
	unlockPaymentConfiguration()
	common.ApiSuccess(c, result)
}

func GetSubscriptionSelf(c *gin.Context) {
	userId := c.GetInt("id")
	settingMap, err := model.GetUserSetting(userId, false)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("subscription preference lookup failed user_id=%d error=%q", userId, err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}
	pref := common.NormalizeBillingPreference(settingMap.BillingPreference)

	// Get all subscriptions (including expired)
	allSubscriptions, err := model.GetAllUserSubscriptions(userId)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("subscription history lookup failed user_id=%d error=%q", userId, err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}

	// Get active subscriptions for backward compatibility
	activeSubscriptions, err := model.GetAllActiveUserSubscriptions(userId)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("active subscription lookup failed user_id=%d error=%q", userId, err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}

	planIDs := make(map[int]struct{})
	for _, summary := range allSubscriptions {
		if summary.Subscription != nil && summary.Subscription.PlanId > 0 {
			planIDs[summary.Subscription.PlanId] = struct{}{}
		}
	}
	for _, summary := range activeSubscriptions {
		if summary.Subscription != nil && summary.Subscription.PlanId > 0 {
			planIDs[summary.Subscription.PlanId] = struct{}{}
		}
	}
	planTitles := make(map[int]string, len(planIDs))
	if len(planIDs) > 0 {
		ids := make([]int, 0, len(planIDs))
		for planID := range planIDs {
			ids = append(ids, planID)
		}
		var plans []model.SubscriptionPlan
		if err := model.DB.Select("id", "title").Where("id IN ?", ids).Find(&plans).Error; err != nil {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("subscription plan title lookup failed user_id=%d error=%q", userId, err.Error()))
		} else {
			for _, plan := range plans {
				planTitles[plan.Id] = plan.Title
			}
		}
	}

	common.ApiSuccess(c, PublicSubscriptionSelf{
		BillingPreference: pref,
		Subscriptions:     publicSubscriptionSummaries(activeSubscriptions, planTitles),
		AllSubscriptions:  publicSubscriptionSummaries(allSubscriptions, planTitles),
	})
}

func UpdateSubscriptionPreference(c *gin.Context) {
	userId := c.GetInt("id")
	var req BillingPreferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		compatibilityPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}
	pref := common.NormalizeBillingPreference(req.BillingPreference)

	user, err := model.GetUserById(userId, true)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("subscription preference user lookup failed user_id=%d error=%q", userId, err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}
	current := user.GetSetting()
	current.BillingPreference = pref
	if err := model.UpdateUserSetting(user.Id, current); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("subscription preference update failed user_id=%d error=%q", userId, err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}
	common.ApiSuccess(c, gin.H{"billing_preference": pref})
}

func SubscriptionRequestBalancePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	userId := c.GetInt("id")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req SubscriptionBalancePayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		compatibilityPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}
	requestId, err := subscriptionBalanceRequestID(req.RequestId)
	if err != nil {
		compatibilityPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}

	if err := model.PurchaseSubscriptionWithBalance(userId, req.PlanId, requestId); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("subscription balance purchase failed user_id=%d plan_id=%d error=%q", userId, req.PlanId, err.Error()))
		compatibilityPaymentServiceAPIError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func subscriptionBalanceRequestID(requestId string) (string, error) {
	return legacyPaymentRequestID(requestId, "legacy_balance_")
}

// ---- Admin APIs ----

func AdminListSubscriptionPlans(c *gin.Context) {
	var plans []model.SubscriptionPlan
	if err := model.DB.Order("sort_order desc, id desc").Find(&plans).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	result := make([]SubscriptionPlanDTO, 0, len(plans))
	for _, p := range plans {
		p.NormalizeDefaults()
		result = append(result, SubscriptionPlanDTO{
			Plan:                 p,
			StripePriceIDPurpose: model.SubscriptionPlanStripePriceIDPurposeLegacyRecurring,
		})
	}
	common.ApiSuccess(c, result)
}

func publicSubscriptionPlan(plan model.SubscriptionPlan, eligibleRoutes []service.PublicPaymentRoute) PublicSubscriptionPlan {
	externalPaymentRouteIDs := make([]string, 0, len(eligibleRoutes))
	for _, route := range eligibleRoutes {
		externalPaymentRouteIDs = append(externalPaymentRouteIDs, route.RouteID)
	}
	return PublicSubscriptionPlan{
		ID:                      plan.Id,
		Title:                   plan.Title,
		Subtitle:                plan.Subtitle,
		PriceAmount:             plan.PriceAmount,
		Currency:                plan.Currency,
		DurationUnit:            plan.DurationUnit,
		DurationValue:           plan.DurationValue,
		CustomSeconds:           plan.CustomSeconds,
		AllowBalancePay:         plan.AllowBalancePay,
		MaxPurchasePerUser:      plan.MaxPurchasePerUser,
		IncludesExpandedAccess:  strings.TrimSpace(plan.UpgradeGroup) != "",
		TotalAmount:             plan.TotalAmount,
		QuotaResetPeriod:        plan.QuotaResetPeriod,
		QuotaResetCustomSeconds: plan.QuotaResetCustomSeconds,
		ExternalPaymentRouteIDs: externalPaymentRouteIDs,
	}
}

func publicSubscriptionSummaries(summaries []model.SubscriptionSummary, planTitles map[int]string) []PublicSubscriptionSummary {
	result := make([]PublicSubscriptionSummary, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Subscription == nil {
			continue
		}
		subscription := summary.Subscription
		result = append(result, PublicSubscriptionSummary{
			Subscription: PublicUserSubscription{
				ID:            subscription.Id,
				PlanID:        subscription.PlanId,
				PlanTitle:     planTitles[subscription.PlanId],
				AmountTotal:   subscription.AmountTotal,
				AmountUsed:    subscription.AmountUsed,
				StartTime:     subscription.StartTime,
				EndTime:       subscription.EndTime,
				Status:        subscription.Status,
				NextResetTime: subscription.NextResetTime,
			},
		})
	}
	return result
}

type AdminUpsertSubscriptionPlanRequest struct {
	Plan model.SubscriptionPlan `json:"plan"`
}

func subscriptionPlanAdminAPIError(c *gin.Context, status int, code string, diagnostic error) {
	if diagnostic != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"subscription plan mutation rejected admin_id=%d code=%s error=%q",
			c.GetInt("id"), code, diagnostic.Error(),
		))
	}
	paymentAPIErrorWithCode(c, status, code, "", nil)
}

func AdminCreateSubscriptionPlan(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req AdminUpsertSubscriptionPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		subscriptionPlanAdminAPIError(c, http.StatusBadRequest, "subscription_plan_invalid", err)
		return
	}
	req.Plan.Id = 0
	req.Plan.Title = strings.TrimSpace(req.Plan.Title)
	if req.Plan.Title == "" {
		subscriptionPlanAdminAPIError(c, http.StatusBadRequest, "subscription_plan_invalid", fmt.Errorf("subscription plan title is empty"))
		return
	}
	if req.Plan.Currency == "" {
		req.Plan.Currency = "USD"
	}
	req.Plan.Currency = "USD"
	if req.Plan.AllowBalancePay == nil {
		req.Plan.AllowBalancePay = common.GetPointer(true)
	}
	if req.Plan.AllowWalletOverflow == nil {
		req.Plan.AllowWalletOverflow = common.GetPointer(true)
	}
	if req.Plan.DurationUnit == "" {
		req.Plan.DurationUnit = model.SubscriptionDurationMonth
	}
	if req.Plan.DurationValue == 0 && req.Plan.DurationUnit != model.SubscriptionDurationCustom {
		req.Plan.DurationValue = 1
	}
	if strings.TrimSpace(req.Plan.QuotaResetPeriod) == "" {
		req.Plan.QuotaResetPeriod = model.SubscriptionResetNever
	}
	req.Plan.NormalizeDefaults()
	if err := model.ValidateSubscriptionPlan(&req.Plan); err != nil {
		subscriptionPlanAdminAPIError(c, http.StatusBadRequest, "subscription_plan_invalid", err)
		return
	}
	req.Plan.UpgradeGroup = strings.TrimSpace(req.Plan.UpgradeGroup)
	if req.Plan.UpgradeGroup != "" {
		if _, ok := ratio_setting.GetGroupRatioCopy()[req.Plan.UpgradeGroup]; !ok {
			subscriptionPlanAdminAPIError(c, http.StatusBadRequest, "subscription_plan_invalid", fmt.Errorf("upgrade group does not exist"))
			return
		}
	}
	req.Plan.DowngradeGroup = strings.TrimSpace(req.Plan.DowngradeGroup)
	if req.Plan.DowngradeGroup != "" {
		if _, ok := ratio_setting.GetGroupRatioCopy()[req.Plan.DowngradeGroup]; !ok {
			subscriptionPlanAdminAPIError(c, http.StatusBadRequest, "subscription_plan_invalid", fmt.Errorf("downgrade group does not exist"))
			return
		}
	}
	err := model.DB.Create(&req.Plan).Error
	if err != nil {
		subscriptionPlanAdminAPIError(c, http.StatusInternalServerError, "subscription_plan_save_failed", err)
		return
	}
	model.InvalidateSubscriptionPlanCache(req.Plan.Id)
	common.ApiSuccess(c, req.Plan)
}

func AdminUpdateSubscriptionPlan(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		subscriptionPlanAdminAPIError(c, http.StatusBadRequest, "subscription_plan_invalid", fmt.Errorf("invalid subscription plan id"))
		return
	}
	var req AdminUpsertSubscriptionPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		subscriptionPlanAdminAPIError(c, http.StatusBadRequest, "subscription_plan_invalid", err)
		return
	}
	req.Plan.Title = strings.TrimSpace(req.Plan.Title)
	if req.Plan.Title == "" {
		subscriptionPlanAdminAPIError(c, http.StatusBadRequest, "subscription_plan_invalid", fmt.Errorf("subscription plan title is empty"))
		return
	}
	req.Plan.Id = id
	if req.Plan.Currency == "" {
		req.Plan.Currency = "USD"
	}
	req.Plan.Currency = "USD"
	if req.Plan.DurationUnit == "" {
		req.Plan.DurationUnit = model.SubscriptionDurationMonth
	}
	if req.Plan.DurationValue == 0 && req.Plan.DurationUnit != model.SubscriptionDurationCustom {
		req.Plan.DurationValue = 1
	}
	if strings.TrimSpace(req.Plan.QuotaResetPeriod) == "" {
		req.Plan.QuotaResetPeriod = model.SubscriptionResetNever
	}
	req.Plan.NormalizeDefaults()
	if err := model.ValidateSubscriptionPlan(&req.Plan); err != nil {
		subscriptionPlanAdminAPIError(c, http.StatusBadRequest, "subscription_plan_invalid", err)
		return
	}
	req.Plan.UpgradeGroup = strings.TrimSpace(req.Plan.UpgradeGroup)
	if req.Plan.UpgradeGroup != "" {
		if _, ok := ratio_setting.GetGroupRatioCopy()[req.Plan.UpgradeGroup]; !ok {
			subscriptionPlanAdminAPIError(c, http.StatusBadRequest, "subscription_plan_invalid", fmt.Errorf("upgrade group does not exist"))
			return
		}
	}
	req.Plan.DowngradeGroup = strings.TrimSpace(req.Plan.DowngradeGroup)
	if req.Plan.DowngradeGroup != "" {
		if _, ok := ratio_setting.GetGroupRatioCopy()[req.Plan.DowngradeGroup]; !ok {
			subscriptionPlanAdminAPIError(c, http.StatusBadRequest, "subscription_plan_invalid", fmt.Errorf("downgrade group does not exist"))
			return
		}
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		// update plan (allow zero values updates with map)
		updateMap := map[string]interface{}{
			"title":                      req.Plan.Title,
			"subtitle":                   req.Plan.Subtitle,
			"price_amount":               req.Plan.PriceAmount,
			"currency":                   req.Plan.Currency,
			"duration_unit":              req.Plan.DurationUnit,
			"duration_value":             req.Plan.DurationValue,
			"custom_seconds":             req.Plan.CustomSeconds,
			"enabled":                    req.Plan.Enabled,
			"sort_order":                 req.Plan.SortOrder,
			"stripe_price_id":            req.Plan.StripePriceId,
			"creem_product_id":           req.Plan.CreemProductId,
			"waffo_pancake_product_id":   req.Plan.WaffoPancakeProductId,
			"max_purchase_per_user":      req.Plan.MaxPurchasePerUser,
			"total_amount":               req.Plan.TotalAmount,
			"upgrade_group":              req.Plan.UpgradeGroup,
			"downgrade_group":            req.Plan.DowngradeGroup,
			"quota_reset_period":         req.Plan.QuotaResetPeriod,
			"quota_reset_custom_seconds": req.Plan.QuotaResetCustomSeconds,
			"updated_at":                 common.GetTimestamp(),
		}
		if req.Plan.AllowBalancePay != nil {
			updateMap["allow_balance_pay"] = *req.Plan.AllowBalancePay
		}
		if req.Plan.AllowWalletOverflow != nil {
			updateMap["allow_wallet_overflow"] = *req.Plan.AllowWalletOverflow
		}
		if err := tx.Model(&model.SubscriptionPlan{}).Where("id = ?", id).Updates(updateMap).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		subscriptionPlanAdminAPIError(c, http.StatusInternalServerError, "subscription_plan_save_failed", err)
		return
	}
	model.InvalidateSubscriptionPlanCache(id)
	common.ApiSuccess(c, nil)
}

type AdminUpdateSubscriptionPlanStatusRequest struct {
	Enabled *bool `json:"enabled"`
}

func AdminUpdateSubscriptionPlanStatus(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		common.ApiErrorMsg(c, "无效的ID")
		return
	}
	var req AdminUpdateSubscriptionPlanStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if err := model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", id).Update("enabled", *req.Enabled).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	model.InvalidateSubscriptionPlanCache(id)
	common.ApiSuccess(c, nil)
}

type AdminBindSubscriptionRequest struct {
	UserId int `json:"user_id"`
	PlanId int `json:"plan_id"`
}

func AdminBindSubscription(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req AdminBindSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.UserId <= 0 || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	msg, err := model.AdminBindSubscription(req.UserId, req.PlanId, "")
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}

// ---- Admin: user subscription management ----

func AdminListUserSubscriptions(c *gin.Context) {
	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		common.ApiErrorMsg(c, "无效的用户ID")
		return
	}
	subs, err := model.GetAllUserSubscriptions(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, subs)
}

type AdminCreateUserSubscriptionRequest struct {
	PlanId int `json:"plan_id"`
}

type AdminResetSubscriptionRequest struct {
	PlanId           int   `json:"plan_id"`
	AdvanceResetTime *bool `json:"advance_reset_time"`
}

func resolveAdvanceResetTime(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

func recordSubscriptionResetUserLogs(result *model.SubscriptionResetResult, adminInfo map[string]interface{}) {
	if result == nil || result.ResetCount == 0 {
		return
	}
	content := fmt.Sprintf("管理员重置订阅套餐 %s（ID: %d）额度", result.PlanTitle, result.PlanId)
	for _, userId := range result.AffectedUserIds {
		model.RecordLogWithAdminInfo(userId, model.LogTypeManage, content, adminInfo)
	}
}

// AdminCreateUserSubscription creates a new user subscription from a plan (no payment).
func AdminCreateUserSubscription(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		common.ApiErrorMsg(c, "无效的用户ID")
		return
	}
	var req AdminCreateUserSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	msg, err := model.AdminBindSubscription(userId, req.PlanId, "")
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}

func AdminResetUserSubscriptionsByPlan(c *gin.Context) {
	userId, _ := strconv.Atoi(c.Param("id"))
	if userId <= 0 {
		common.ApiErrorMsg(c, "无效的用户ID")
		return
	}
	var req AdminResetSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	advanceResetTime := resolveAdvanceResetTime(req.AdvanceResetTime)
	result, err := model.AdminResetUserSubscriptionsByPlan(userId, req.PlanId, advanceResetTime)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordSubscriptionResetUserLogs(result, auditOperatorInfo(c))
	recordManageAuditFor(c, userId, "subscription.user_plan_reset", map[string]interface{}{
		"target_user_id":     userId,
		"plan_id":            result.PlanId,
		"plan_title":         result.PlanTitle,
		"reset_count":        result.ResetCount,
		"user_count":         result.UserCount,
		"advance_reset_time": result.AdvanceResetTime,
	})
	common.ApiSuccess(c, result)
}

func AdminResetPlanSubscriptions(c *gin.Context) {
	planId, _ := strconv.Atoi(c.Param("id"))
	if planId <= 0 {
		common.ApiErrorMsg(c, "无效的ID")
		return
	}
	var req AdminResetSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	advanceResetTime := resolveAdvanceResetTime(req.AdvanceResetTime)
	result, err := model.AdminResetPlanSubscriptions(planId, advanceResetTime)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordSubscriptionResetUserLogs(result, auditOperatorInfo(c))
	common.SysLog(fmt.Sprintf("admin reset subscription plan %d quota: reset_count=%d user_count=%d advance_reset_time=%t",
		result.PlanId, result.ResetCount, result.UserCount, result.AdvanceResetTime))
	recordManageAudit(c, "subscription.plan_reset", map[string]interface{}{
		"plan_id":            result.PlanId,
		"plan_title":         result.PlanTitle,
		"reset_count":        result.ResetCount,
		"user_count":         result.UserCount,
		"advance_reset_time": result.AdvanceResetTime,
	})
	common.ApiSuccess(c, result)
}

// AdminInvalidateUserSubscription cancels a user subscription immediately.
func AdminInvalidateUserSubscription(c *gin.Context) {
	subId, _ := strconv.Atoi(c.Param("id"))
	if subId <= 0 {
		common.ApiErrorMsg(c, "无效的订阅ID")
		return
	}
	msg, err := model.AdminInvalidateUserSubscription(subId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}

// AdminDeleteUserSubscription hard-deletes a user subscription.
func AdminDeleteUserSubscription(c *gin.Context) {
	subId, _ := strconv.Atoi(c.Param("id"))
	if subId <= 0 {
		common.ApiErrorMsg(c, "无效的订阅ID")
		return
	}
	msg, err := model.AdminDeleteUserSubscription(subId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if msg != "" {
		common.ApiSuccess(c, gin.H{"message": msg})
		return
	}
	common.ApiSuccess(c, nil)
}
