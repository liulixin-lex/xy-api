package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

type paymentLimitPolicyRequest struct {
	Provider         string `json:"provider"`
	PaymentMethod    string `json:"payment_method"`
	Currency         string `json:"currency"`
	SingleLimitMinor string `json:"single_limit_minor"`
	DailyLimitMinor  string `json:"daily_limit_minor"`
	Timezone         string `json:"timezone"`
	Enabled          bool   `json:"enabled"`
}

type paymentLimitUsageView struct {
	ID               int64  `json:"id"`
	Provider         string `json:"provider"`
	PaymentMethod    string `json:"payment_method"`
	Currency         string `json:"currency"`
	CurrencyExponent int32  `json:"currency_exponent"`
	SingleLimitMinor string `json:"single_limit_minor"`
	DailyLimitMinor  string `json:"daily_limit_minor"`
	Timezone         string `json:"timezone"`
	Enabled          bool   `json:"enabled"`
	Version          int64  `json:"version"`
	DayKey           string `json:"day_key"`
	ReservedMinor    string `json:"reserved_minor"`
	PaidMinor        string `json:"paid_minor"`
}

type paymentLimitMutationView struct {
	Saved            bool                   `json:"saved"`
	Refreshed        bool                   `json:"refreshed"`
	RefreshErrorCode string                 `json:"refresh_error_code,omitempty"`
	Usage            *paymentLimitUsageView `json:"usage,omitempty"`
}

type paymentCredentialRevocationPreviewView struct {
	ConfigurationVersion int64                                    `json:"configuration_version"`
	GeneratedAt          int64                                    `json:"generated_at"`
	Impact               *model.PaymentCredentialRevocationImpact `json:"impact"`
}

type paymentOperationsOverviewResponse struct {
	Operations *model.PaymentOperationsOverview `json:"operations"`
	Runtime    service.PaymentRuntimeInfo       `json:"runtime"`
	Cluster    paymentClusterOverviewView       `json:"cluster"`
}

type paymentClusterOverviewView struct {
	Ready bool   `json:"ready"`
	Code  string `json:"code"`
}

func publicPaymentLimitUsage(usage *model.PaymentLimitUsage) *paymentLimitUsageView {
	if usage == nil {
		return nil
	}
	return &paymentLimitUsageView{
		ID: usage.Policy.ID, Provider: usage.Policy.Provider,
		PaymentMethod: usage.Policy.PaymentMethod, Currency: usage.Policy.Currency,
		CurrencyExponent: usage.CurrencyExponent,
		SingleLimitMinor: strconv.FormatInt(usage.Policy.SingleLimitMinor, 10),
		DailyLimitMinor:  strconv.FormatInt(usage.Policy.DailyLimitMinor, 10),
		Timezone:         usage.Policy.Timezone, Enabled: usage.Policy.Enabled,
		Version: usage.Policy.Version, DayKey: usage.DayKey,
		ReservedMinor: strconv.FormatInt(usage.ReservedMinor, 10),
		PaidMinor:     strconv.FormatInt(usage.PaidMinor, 10),
	}
}

func ListPaymentLimitPolicies(c *gin.Context) {
	policies, err := model.ListPaymentLimitPolicies()
	if err != nil {
		paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "payment_limit_list_unavailable", "", nil)
		return
	}
	usage := make([]*paymentLimitUsageView, 0, len(policies))
	for i := range policies {
		current, err := model.CurrentPaymentLimitUsage(policies[i], common.GetTimestamp())
		if err != nil {
			paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "payment_limit_usage_unavailable", "", nil)
			return
		}
		usage = append(usage, publicPaymentLimitUsage(current))
	}
	common.ApiSuccess(c, usage)
}

func GetPaymentOperationsOverview(c *gin.Context) {
	overview, err := model.GetPaymentOperationsOverview(common.GetTimestamp())
	if err != nil {
		logger.LogWarn(c.Request.Context(), "failed to load payment operations overview: "+err.Error())
		if errors.Is(err, model.ErrPaymentOperationsSchemaNotReady) {
			paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "payment_operations_schema_not_ready", "", nil)
			return
		}
		paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "payment_overview_unavailable", "", nil)
		return
	}
	clusterErr := service.EnsurePaymentClusterReady()
	common.ApiSuccess(c, paymentOperationsOverviewResponse{
		Operations: overview,
		Runtime:    service.CurrentPaymentRuntimeInfo(),
		Cluster: paymentClusterOverviewView{
			Ready: clusterErr == nil,
			Code:  service.PaymentClusterReadinessCode(clusterErr),
		},
	})
}

func GetPaymentCredentialRevocationPreview(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	mode := strings.ToLower(strings.TrimSpace(c.Query("mode")))
	if mode != model.PaymentCredentialRevocationModePrevious && mode != model.PaymentCredentialRevocationModeAllActive {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_credential_revocation_preview_invalid", "", nil)
		return
	}
	if provider != model.PaymentProviderEpay && provider != model.PaymentProviderStripe && provider != model.PaymentProviderXorPay &&
		provider != model.PaymentProviderCreem && provider != model.PaymentProviderWaffo && provider != model.PaymentProviderWaffoPancake {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_credential_revocation_preview_invalid", "", nil)
		return
	}
	if (provider == model.PaymentProviderCreem || provider == model.PaymentProviderWaffo || provider == model.PaymentProviderWaffoPancake) &&
		mode != model.PaymentCredentialRevocationModeAllActive {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_credential_revocation_preview_invalid", "", nil)
		return
	}
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "payment_credential_revocation_preview_unavailable", "", nil)
		return
	}

	now := common.GetTimestamp()
	generations := make([]int64, 0, 2)
	allActiveOrders := false
	unlock := setting.LockPaymentConfigurationForRead()
	switch provider {
	case model.PaymentProviderEpay:
		if mode == model.PaymentCredentialRevocationModePrevious {
			if operation_setting.EpayPreviousCredentialActive() {
				generations = append(generations, operation_setting.EpayPreviousCredentialGeneration)
			}
		} else {
			if strings.TrimSpace(operation_setting.EpayId) != "" && strings.TrimSpace(operation_setting.EpayKey) != "" &&
				operation_setting.EpayCredentialGeneration > 0 {
				generations = append(generations, operation_setting.EpayCredentialGeneration)
			}
			if operation_setting.EpayPreviousCredentialActive() {
				generations = append(generations, operation_setting.EpayPreviousCredentialGeneration)
			}
		}
	case model.PaymentProviderXorPay:
		if mode == model.PaymentCredentialRevocationModePrevious {
			if setting.XorPayPreviousCredentialActive() {
				generations = append(generations, setting.XorPayPreviousCredentialGeneration)
			}
		} else {
			if strings.TrimSpace(setting.XorPayAid) != "" && strings.TrimSpace(setting.XorPayAppSecret) != "" &&
				setting.XorPayCredentialGeneration > 0 {
				generations = append(generations, setting.XorPayCredentialGeneration)
			}
			if setting.XorPayPreviousCredentialActive() {
				generations = append(generations, setting.XorPayPreviousCredentialGeneration)
			}
		}
	case model.PaymentProviderStripe:
		if mode != model.PaymentCredentialRevocationModeAllActive {
			unlock()
			paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_credential_revocation_preview_invalid", "", nil)
			return
		}
		if setting.StripeWebhookCredentialGeneration > 0 {
			generations = append(generations, setting.StripeWebhookCredentialGeneration)
		}
		if setting.StripePreviousWebhookSecretActive() && setting.StripeWebhookPreviousCredentialGeneration > 0 {
			generations = append(generations, setting.StripeWebhookPreviousCredentialGeneration)
		}
		allActiveOrders = true
	case model.PaymentProviderCreem:
		if mode != model.PaymentCredentialRevocationModeAllActive ||
			strings.TrimSpace(setting.CreemApiKey) == "" && strings.TrimSpace(setting.CreemWebhookSecret) == "" {
			unlock()
			paymentAPIErrorWithCode(c, http.StatusConflict, "payment_credential_revocation_unavailable", "", nil)
			return
		}
		generations = append(generations, 0)
		allActiveOrders = true
	case model.PaymentProviderWaffo:
		credentialAvailable := strings.TrimSpace(setting.WaffoApiKey) != "" || strings.TrimSpace(setting.WaffoPrivateKey) != "" ||
			strings.TrimSpace(setting.WaffoPublicCert) != ""
		if setting.WaffoSandbox {
			credentialAvailable = strings.TrimSpace(setting.WaffoSandboxApiKey) != "" || strings.TrimSpace(setting.WaffoSandboxPrivateKey) != "" ||
				strings.TrimSpace(setting.WaffoSandboxPublicCert) != ""
		}
		if mode != model.PaymentCredentialRevocationModeAllActive || !credentialAvailable {
			unlock()
			paymentAPIErrorWithCode(c, http.StatusConflict, "payment_credential_revocation_unavailable", "", nil)
			return
		}
		generations = append(generations, 0)
		allActiveOrders = true
	case model.PaymentProviderWaffoPancake:
		if mode != model.PaymentCredentialRevocationModeAllActive ||
			strings.TrimSpace(setting.WaffoPancakePrivateKey) == "" && strings.TrimSpace(setting.WaffoPancakeStoreID) == "" {
			unlock()
			paymentAPIErrorWithCode(c, http.StatusConflict, "payment_credential_revocation_unavailable", "", nil)
			return
		}
		generations = append(generations, 0)
		allActiveOrders = true
	}
	configurationVersion, versionErr := model.CurrentPaymentConfigurationVersion()
	unlock()
	if versionErr != nil {
		paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "payment_credential_revocation_preview_unavailable", "", nil)
		return
	}
	if len(generations) == 0 {
		paymentAPIErrorWithCode(c, http.StatusConflict, "payment_credential_revocation_unavailable", "", nil)
		return
	}
	impact, err := model.PreviewPaymentCredentialRevocation(provider, mode, generations, allActiveOrders, now)
	if err != nil {
		logger.LogWarn(c.Request.Context(), "payment credential revocation preview failed: "+err.Error())
		paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "payment_credential_revocation_preview_unavailable", "", nil)
		return
	}
	common.ApiSuccess(c, paymentCredentialRevocationPreviewView{
		ConfigurationVersion: configurationVersion,
		GeneratedAt:          now,
		Impact:               impact,
	})
}

func UpdatePaymentLimitPolicy(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var request paymentLimitPolicyRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_limit_invalid", "", nil)
		return
	}
	singleLimitMinor, err := strconv.ParseInt(request.SingleLimitMinor, 10, 64)
	if err != nil {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_limit_invalid", "", nil)
		return
	}
	dailyLimitMinor, err := strconv.ParseInt(request.DailyLimitMinor, 10, 64)
	if err != nil {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_limit_invalid", "", nil)
		return
	}
	policy := &model.PaymentLimitPolicy{
		Provider:         request.Provider,
		PaymentMethod:    request.PaymentMethod,
		Currency:         request.Currency,
		SingleLimitMinor: singleLimitMinor,
		DailyLimitMinor:  dailyLimitMinor,
		Timezone:         request.Timezone,
		Enabled:          request.Enabled,
	}
	if err := model.UpsertPaymentLimitPolicy(policy); err != nil {
		logger.LogWarn(c.Request.Context(), "payment limit update rejected: "+err.Error())
		if errors.Is(err, model.ErrPaymentLimitTimezoneLocked) {
			paymentAPIErrorWithCode(c, http.StatusConflict, "payment_limit_timezone_locked", "", nil)
			return
		}
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_limit_invalid", "", nil)
		return
	}
	current, err := model.GetPaymentLimitPolicy(policy.Provider, policy.PaymentMethod, policy.Currency)
	if err != nil || current == nil {
		common.ApiSuccess(c, paymentLimitMutationView{
			Saved: true, Refreshed: false, RefreshErrorCode: "payment_limit_reload_failed",
		})
		return
	}
	usage, err := model.CurrentPaymentLimitUsage(*current, common.GetTimestamp())
	if err != nil {
		common.ApiSuccess(c, paymentLimitMutationView{
			Saved: true, Refreshed: false, RefreshErrorCode: "payment_limit_usage_refresh_failed",
		})
		return
	}
	common.ApiSuccess(c, paymentLimitMutationView{
		Saved: true, Refreshed: true, Usage: publicPaymentLimitUsage(usage),
	})
}
