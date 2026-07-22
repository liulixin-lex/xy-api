package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

type PaymentComplianceRequest struct {
	Confirmed bool `json:"confirmed"`
}

func requirePaymentCompliance(c *gin.Context) bool {
	if !isPaymentComplianceConfirmed() {
		// Compliance is administrator configuration, so public callers receive
		// only the generic availability contract. The exact state remains in the
		// administrator settings and audit surfaces.
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return false
	}
	return true
}

func ConfirmPaymentCompliance(c *gin.Context) {
	if c.GetBool("use_access_token") {
		paymentSettingsAPIError(c, http.StatusForbidden, "payment_settings_auth_required", nil)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req PaymentComplianceRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", err)
		return
	}
	if !req.Confirmed {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", errors.New("payment compliance confirmation is required"))
		return
	}

	now := time.Now().Unix()
	userId := c.GetInt("id")
	clientIP := c.ClientIP()

	updates := map[string]string{
		"payment_setting.compliance_confirmed":     "true",
		"payment_setting.compliance_terms_version": operation_setting.CurrentComplianceTermsVersion,
		"payment_setting.compliance_confirmed_at":  strconv.FormatInt(now, 10),
		"payment_setting.compliance_confirmed_by":  strconv.Itoa(userId),
		"payment_setting.compliance_confirmed_ip":  clientIP,
	}

	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		paymentSettingsAPIError(c, http.StatusServiceUnavailable, "payment_settings_sync_failed", err)
		return
	}
	expectedVersion, err := model.CurrentPaymentConfigurationVersion()
	if err != nil {
		paymentSettingsAPIError(c, http.StatusServiceUnavailable, "payment_settings_sync_failed", err)
		return
	}
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForUpdate()
	defer unlockPaymentConfiguration()
	if _, err := model.UpdatePaymentOptionsBulkWithVersionLockHeld(updates, expectedVersion); errors.Is(err, model.ErrPaymentConfigurationVersionConflict) {
		paymentSettingsAPIError(c, http.StatusConflict, "payment_settings_version_conflict", err)
		return
	} else if err != nil {
		paymentSettingsAPIError(c, http.StatusInternalServerError, "payment_settings_save_failed", err)
		return
	}
	recordManageAudit(c, "payment.compliance.confirm", map[string]interface{}{
		"terms_version": operation_setting.CurrentComplianceTermsVersion,
		"confirmed_at":  now,
	})

	logger.LogInfo(c.Request.Context(), fmt.Sprintf(
		"payment compliance confirmed user_id=%d ip=%s terms_version=%s confirmed_at=%d",
		userId,
		clientIP,
		operation_setting.CurrentComplianceTermsVersion,
		now,
	))

	common.ApiSuccess(c, gin.H{
		"confirmed":     true,
		"terms_version": operation_setting.CurrentComplianceTermsVersion,
		"confirmed_at":  now,
		"confirmed_by":  userId,
	})
}
