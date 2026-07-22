package controller

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type WaffoPancakePayRequest struct {
	Amount    int64  `json:"amount"`
	RequestID string `json:"request_id"`
}

func RequestWaffoPancakeAmount(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req WaffoPancakePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		legacyPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}
	if req.Amount <= 0 || req.Amount > service.MaxPaymentTopUpAmount {
		legacyPaymentAPIError(c, "payment_amount_invalid", gin.H{"min": 1, "max": service.MaxPaymentTopUpAmount})
		return
	}

	if req.Amount < int64(setting.WaffoPancakeMinTopUp) {
		legacyPaymentAPIError(c, "payment_amount_below_minimum", gin.H{"min": setting.WaffoPancakeMinTopUp})
		return
	}
	normalizedAmount, _, valid := normalizeRetainedTopUpCredit(req.Amount)
	if !valid {
		legacyPaymentAPIError(c, "payment_amount_invalid", gin.H{"min": 1, "max": service.MaxPaymentTopUpAmount})
		return
	}
	if !isWaffoPancakeTopUpEnabled() {
		legacyPaymentAPIError(c, "payment_method_unavailable", nil)
		return
	}

	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake 报价用户分组查询失败 user_id=%d error=%q", id, err.Error()))
		legacyPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}

	payMoney := getWaffoPancakePayMoney(req.Amount, normalizedAmount, group)
	if payMoney <= 0.01 {
		legacyPaymentAPIError(c, "payment_amount_below_minimum", nil)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "success", "data": fmt.Sprintf("%.2f", payMoney)})
}

func getWaffoPancakePayMoney(requestedAmount, normalizedAmount int64, group string) float64 {
	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}

	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(requestedAmount)]; ok && ds > 0 {
		discount = ds
	}

	payMoney := decimal.NewFromInt(normalizedAmount).
		Mul(decimal.NewFromFloat(setting.WaffoPancakeUnitPrice)).
		Mul(decimal.NewFromFloat(topupGroupRatio)).
		Mul(decimal.NewFromFloat(discount))

	return payMoney.InexactFloat64()
}

func formatWaffoPancakeAmount(payMoney float64) string {
	return decimal.NewFromFloat(payMoney).StringFixed(2)
}

// The admin config endpoints below accept typed-but-not-yet-saved creds in
// the body and fall back to persisted creds when the body is blank (see
// resolveWaffoPancakeAdminCreds). Catalog/pair operations never persist them.

type saveWaffoPancakeRequest struct {
	MerchantID      string   `json:"merchant_id"`
	PrivateKey      string   `json:"private_key"`
	ReturnURL       string   `json:"return_url"`
	StoreID         string   `json:"store_id"`
	ProductID       string   `json:"product_id"`
	TestMode        *bool    `json:"test_mode,omitempty"`
	UnitPrice       *float64 `json:"unit_price,omitempty"`
	MinTopUp        *int     `json:"min_top_up,omitempty"`
	ExpectedVersion int64    `json:"expected_version"`
}

type createWaffoPancakePairRequest struct {
	MerchantID string `json:"merchant_id"`
	PrivateKey string `json:"private_key"`
	ReturnURL  string `json:"return_url"`
}

// SaveWaffoPancake atomically persists the gateway binding and optional pricing
// fields. Pricing pointers preserve compatibility with clients that predate the
// fields: omitted values keep the authoritative persisted values unchanged.
// Catalog / pair endpoints are transient — only this one writes the OptionMap.
func SaveWaffoPancake(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req saveWaffoPancakeRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", err)
		return
	}
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		paymentSettingsAPIError(c, http.StatusServiceUnavailable, "payment_settings_sync_failed", err)
		return
	}
	values := map[string]string{
		"WaffoPancakeMerchantID": strings.TrimSpace(req.MerchantID),
		"WaffoPancakeReturnURL":  strings.TrimSpace(req.ReturnURL),
		"WaffoPancakeStoreID":    strings.TrimSpace(req.StoreID),
		"WaffoPancakeProductID":  strings.TrimSpace(req.ProductID),
	}
	if privateKey := strings.TrimSpace(req.PrivateKey); privateKey != "" {
		values["WaffoPancakePrivateKey"] = privateKey
		if !model.PaymentSecretEncryptionReady() {
			paymentSettingsAPIError(c, http.StatusServiceUnavailable, "payment_settings_secret_storage_unavailable", nil)
			return
		}
	}
	if req.UnitPrice != nil {
		unitPrice := strconv.FormatFloat(*req.UnitPrice, 'f', -1, 64)
		if err := validatePaymentSettingValue("WaffoPancakeUnitPrice", unitPrice); err != nil {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", err)
			return
		}
		values["WaffoPancakeUnitPrice"] = unitPrice
	}
	if req.TestMode != nil {
		values["WaffoPancakeTestMode"] = strconv.FormatBool(*req.TestMode)
	}
	if req.MinTopUp != nil {
		minTopUp := strconv.Itoa(*req.MinTopUp)
		if err := validatePaymentSettingValue("WaffoPancakeMinTopUp", minTopUp); err != nil {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", err)
			return
		}
		values["WaffoPancakeMinTopUp"] = minTopUp
	}
	if err := validatePaymentSettingValue("WaffoPancakeReturnURL", values["WaffoPancakeReturnURL"]); err != nil {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", err)
		return
	}

	unlockPaymentConfiguration := service.LockPaymentConfigurationForUpdate()
	defer unlockPaymentConfiguration()
	if err := validateInFlightPaymentConfigurationChanges(values, nil); err != nil {
		paymentSettingsAPIError(c, http.StatusConflict, "payment_settings_change_blocked", err)
		return
	}
	effectivePrivateKey := setting.WaffoPancakePrivateKey
	if next, ok := values["WaffoPancakePrivateKey"]; ok {
		effectivePrivateKey = next
	}
	if err := service.ValidateWaffoPancakeConfig(
		values["WaffoPancakeMerchantID"], effectivePrivateKey, values["WaffoPancakeReturnURL"],
		values["WaffoPancakeStoreID"], values["WaffoPancakeProductID"],
	); err != nil {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", err)
		return
	}
	expectedVersion := req.ExpectedVersion
	if expectedVersion <= 0 {
		var err error
		expectedVersion, err = model.CurrentPaymentConfigurationVersion()
		if err != nil {
			paymentSettingsAPIError(c, http.StatusServiceUnavailable, "payment_settings_sync_failed", err)
			return
		}
	}
	nextVersion, err := model.UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
		values, expectedVersion, nil, paymentConfigurationPreconditions(values, nil),
		&model.PaymentConfigurationAuditInput{AdminID: c.GetInt("id"), ActorIP: c.ClientIP()},
	)
	if errors.Is(err, model.ErrPaymentConfigurationVersionConflict) {
		paymentSettingsAPIError(c, http.StatusConflict, "payment_settings_version_conflict", err)
		return
	}
	if errors.Is(err, model.ErrPaymentConfigurationPrecondition) {
		paymentSettingsAPIError(c, http.StatusConflict, "payment_settings_change_blocked", err)
		return
	}
	if err != nil {
		paymentSettingsAPIError(c, http.StatusInternalServerError, "payment_settings_save_failed", err)
		return
	}
	changedKeys := make([]string, 0, len(values))
	for key := range values {
		changedKeys = append(changedKeys, key)
	}
	sort.Strings(changedKeys)
	recordManageAudit(c, "payment.settings.update", map[string]interface{}{
		"keys": changedKeys,
	})
	common.ApiSuccess(c, gin.H{
		"product_id": setting.WaffoPancakeProductID,
		"store_id":   setting.WaffoPancakeStoreID,
		"test_mode":  setting.WaffoPancakeTestMode,
		"unit_price": setting.WaffoPancakeUnitPrice,
		"min_top_up": setting.WaffoPancakeMinTopUp,
		"version":    nextVersion,
		"readiness":  paymentGatewayReadinessLocked(),
	})
}

// resolveWaffoPancakeAdminCreds prefers body creds (typed-but-not-yet-saved
// values, for verification) and falls back to persisted creds when the body
// is blank (so returning admins don't have to re-paste the private key,
// which is stripped from GET /api/option/).
func resolveWaffoPancakeAdminCreds(bodyMerchantID, bodyPrivateKey string) (string, string) {
	m := strings.TrimSpace(bodyMerchantID)
	k := strings.TrimSpace(bodyPrivateKey)
	if m == "" && k == "" {
		return setting.WaffoPancakeMerchantID, setting.WaffoPancakePrivateKey
	}
	return m, k
}

// CreateWaffoPancakePair mints a Store + OnetimeProduct pair in one round-
// trip. Surfaces an orphan-store flag when the product half fails so the
// frontend can preselect / retry without losing context.
func CreateWaffoPancakePair(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req createWaffoPancakePairRequest
	if c.Request.ContentLength > 0 {
		if err := common.DecodeJson(c.Request.Body, &req); err != nil {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", err)
			return
		}
	}
	if err := validatePaymentSettingValue("WaffoPancakeReturnURL", strings.TrimSpace(req.ReturnURL)); err != nil {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", err)
		return
	}
	merchantID, privateKey := resolveWaffoPancakeAdminCreds(req.MerchantID, req.PrivateKey)
	if merchantID == "" || privateKey == "" {
		paymentSettingsAPIError(c, http.StatusConflict, "waffo_pancake_configuration_incomplete", nil)
		return
	}
	if err := service.ValidateWaffoPancakeCredentials(merchantID, privateKey); err != nil {
		paymentSettingsAPIError(c, http.StatusUnprocessableEntity, "waffo_pancake_credentials_invalid", err)
		return
	}
	result, err := service.CreateWaffoPancakePrimaryPair(
		c.Request.Context(), merchantID, privateKey, req.ReturnURL,
	)
	if err != nil {
		orphan := result != nil && result.OrphanStore
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 创建店铺与产品失败 orphan_store=%t store_id=%q error=%q",
			orphan, func() string {
				if result == nil {
					return ""
				}
				return result.StoreID
			}(), err.Error(),
		))
		params := gin.H{}
		if orphan {
			params["store_id"] = result.StoreID
			params["store_name"] = result.StoreName
			params["orphan_store"] = true
		}
		code := "waffo_pancake_pair_create_failed"
		if orphan {
			code = "waffo_pancake_pair_partial_failure"
		}
		paymentAPIErrorWithCode(c, http.StatusBadGateway, code, "", params)
		return
	}
	common.ApiSuccess(c, gin.H{
		"store_id":     result.StoreID,
		"store_name":   result.StoreName,
		"product_id":   result.ProductID,
		"product_name": result.ProductName,
	})
}

// ListWaffoPancakeCatalog returns the merchant's Stores + OnetimeProducts.
// Doubles as a credential probe (a successful 200 proves the resolved creds
// authenticate). See resolveWaffoPancakeAdminCreds for credential resolution.
func ListWaffoPancakeCatalog(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req createWaffoPancakePairRequest
	if c.Request.ContentLength != 0 {
		if err := common.DecodeJson(c.Request.Body, &req); err != nil {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", err)
			return
		}
	}
	merchantID, privateKey := resolveWaffoPancakeAdminCreds(req.MerchantID, req.PrivateKey)
	if merchantID == "" || privateKey == "" {
		paymentSettingsAPIError(c, http.StatusConflict, "waffo_pancake_configuration_incomplete", nil)
		return
	}
	if err := service.ValidateWaffoPancakeCredentials(merchantID, privateKey); err != nil {
		paymentSettingsAPIError(c, http.StatusUnprocessableEntity, "waffo_pancake_credentials_invalid", err)
		return
	}
	catalog, err := service.ListWaffoPancakeCatalog(c.Request.Context(), merchantID, privateKey)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 拉取店铺与产品目录失败 error=%q", err.Error(),
		))
		paymentSettingsAPIError(c, http.StatusBadGateway, "waffo_pancake_catalog_unavailable", err)
		return
	}
	common.ApiSuccess(c, catalog)
}

type createWaffoPancakeSubscriptionProductRequest struct {
	Name   string `json:"name"`
	Amount string `json:"amount"`
}

// CreateWaffoPancakeSubscriptionProduct mints an OnetimeProduct (not
// SubscriptionProduct — see service.CreateWaffoPancakeProductForPlan)
// sized to a plan's `name` + `amount`, using persisted Pancake credentials
// + StoreID. Reads from the form, not the plan row, so newly-typed unsaved
// plans can mint a product too.
func CreateWaffoPancakeSubscriptionProduct(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req createWaffoPancakeSubscriptionProductRequest
	if c.Request.ContentLength > 0 {
		if err := common.DecodeJson(c.Request.Body, &req); err != nil {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", err)
			return
		}
	}
	if strings.TrimSpace(req.Name) == "" {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", nil)
		return
	}
	if strings.TrimSpace(req.Amount) == "" {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", nil)
		return
	}
	merchantID, privateKey := resolveWaffoPancakeAdminCreds("", "")
	storeID := strings.TrimSpace(setting.WaffoPancakeStoreID)
	if merchantID == "" || privateKey == "" || storeID == "" {
		paymentSettingsAPIError(c, http.StatusConflict, "waffo_pancake_configuration_incomplete", nil)
		return
	}
	productID, err := service.CreateWaffoPancakeProductForPlan(
		c.Request.Context(),
		merchantID,
		privateKey,
		storeID,
		req.Name,
		req.Amount,
		setting.WaffoPancakeReturnURL,
	)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 创建套餐产品失败 store_id=%q name=%q amount=%q error=%q",
			storeID, req.Name, req.Amount, err.Error(),
		))
		paymentSettingsAPIError(c, http.StatusBadGateway, "waffo_pancake_product_create_failed", err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"product_id":   productID,
		"product_name": req.Name,
		"store_id":     storeID,
	})
}

// ListWaffoPancakeSubscriptionProductOptions returns the OnetimeProducts
// in the saved Pancake store, for the subscription-plan dropdown. The name
// reflects new-api's plan concept; under the hood it's still OnetimeProducts.
func ListWaffoPancakeSubscriptionProductOptions(c *gin.Context) {
	merchantID, privateKey := resolveWaffoPancakeAdminCreds("", "")
	storeID := strings.TrimSpace(setting.WaffoPancakeStoreID)
	if merchantID == "" || privateKey == "" || storeID == "" {
		paymentSettingsAPIError(c, http.StatusConflict, "waffo_pancake_configuration_incomplete", nil)
		return
	}
	catalog, err := service.ListWaffoPancakeCatalog(c.Request.Context(), merchantID, privateKey)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 拉取订阅产品列表失败 store_id=%q error=%q", storeID, err.Error(),
		))
		paymentSettingsAPIError(c, http.StatusBadGateway, "waffo_pancake_catalog_unavailable", err)
		return
	}
	products := []service.WaffoPancakeCatalogProduct{}
	for _, store := range catalog.Stores {
		if store.ID == storeID {
			products = store.OnetimeProducts
			break
		}
	}
	common.ApiSuccess(c, gin.H{
		"store_id": storeID,
		"products": products,
	})
}

func RequestWaffoPancakePay(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req WaffoPancakePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		legacyPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}
	if req.Amount <= 0 || req.Amount > service.MaxPaymentTopUpAmount {
		legacyPaymentAPIError(c, "payment_amount_invalid", gin.H{"min": 1, "max": service.MaxPaymentTopUpAmount})
		return
	}
	if !isWaffoPancakeTopUpEnabled() {
		legacyPaymentAPIError(c, "payment_method_unavailable", nil)
		return
	}
	startRetainedCompatibilityPayment(c, req.RequestID, service.PaymentQuoteRequest{
		OrderKind:     model.PaymentOrderKindTopUp,
		Provider:      model.PaymentProviderWaffoPancake,
		PaymentMethod: model.PaymentMethodWaffoPancake,
		Amount:        req.Amount,
	})
}

func normalizedWaffoPancakeWebhookEvent(event *service.WaffoPancakeWebhookEvent) (*service.NormalizedPaymentEvent, error) {
	if event == nil {
		return nil, errors.New("missing Waffo Pancake webhook event")
	}
	eventType := strings.TrimSpace(event.NormalizedEventType())
	tradeNo := strings.TrimSpace(event.Data.OrderMerchantExternalID)
	providerOrderKey := service.RetainedProviderAuthorityKey(model.PaymentProviderWaffoPancake, event.Data.OrderID)
	currency := strings.ToUpper(strings.TrimSpace(event.Data.Currency))
	paidAmountMinor := int64(0)
	if amount, err := model.ParseProviderPaymentAmountMinor(event.Data.Amount, model.PaymentProviderWaffoPancake, currency); err == nil && amount > 0 {
		paidAmountMinor = amount
	}
	payload, err := retainedPaymentNormalizedPayload(retainedPaymentWebhookFacts{
		EventID:          strings.TrimSpace(event.ID),
		BusinessEventID:  strings.TrimSpace(event.EventID),
		EventType:        eventType,
		TradeNo:          tradeNo,
		ProviderOrderKey: providerOrderKey,
		ProviderState:    eventType,
		PaidAmountMinor:  paidAmountMinor,
		Currency:         currency,
		PaymentMethod:    model.PaymentMethodWaffoPancake,
		Environment:      strings.TrimSpace(event.Mode),
	})
	if err != nil {
		return nil, err
	}
	normalizedEvent := &service.NormalizedPaymentEvent{
		Provider:            model.PaymentProviderWaffoPancake,
		EventKey:            retainedPaymentEventKey(model.PaymentProviderWaffoPancake, event.ID, eventType, providerOrderKey, tradeNo),
		EventType:           eventType,
		TradeNo:             tradeNo,
		ProviderOrderKey:    providerOrderKey,
		ProviderResourceKey: strings.TrimSpace(event.EventID),
		ProviderState:       eventType,
		PaidAmountMinor:     paidAmountMinor,
		Currency:            currency,
		PaymentMethod:       model.PaymentMethodWaffoPancake,
		Paid:                eventType == "order.completed",
		NormalizedPayload:   payload,
	}
	_, providerLivemode, environmentErr := service.ParseWaffoPancakeEnvironment(event.Mode)
	if environmentErr != nil {
		normalizedEvent.Paid = false
		normalizedEvent.ManualReview = true
		return normalizedEvent, environmentErr
	}
	normalizedEvent.ProviderLivemode = &providerLivemode
	return normalizedEvent, nil
}

func quarantineWaffoPancakeEnvironmentEvent(normalizedEvent *service.NormalizedPaymentEvent, reason string) error {
	if normalizedEvent == nil {
		return errors.New("missing normalized Waffo Pancake event")
	}
	normalizedEvent.Paid = false
	normalizedEvent.Failed = false
	normalizedEvent.Expired = false
	normalizedEvent.Refunded = false
	normalizedEvent.Disputed = false
	normalizedEvent.DisputeResolved = false
	normalizedEvent.ManualReview = true
	if handled, err := processCanonicalRetainedPaymentEvent(normalizedEvent); handled {
		return err
	}
	return service.RecordUnmatchedPaymentEvent(normalizedEvent, reason)
}

func waffoPancakeWebhookEnvironmentMismatchReason(pathLivemode, configuredLivemode bool, eventLivemode *bool) string {
	if eventLivemode == nil {
		return "waffo_pancake_webhook_environment_invalid"
	}
	if *eventLivemode != pathLivemode {
		return "waffo_pancake_webhook_path_environment_mismatch"
	}
	if *eventLivemode != configuredLivemode {
		return "waffo_pancake_webhook_configuration_environment_mismatch"
	}
	return ""
}

func trustedWaffoPancakeWebhookStore(eventStoreID string) bool {
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	trustedStoreID := strings.TrimSpace(setting.WaffoPancakeStoreID)
	unlockPaymentConfiguration()
	return trustedStoreID != "" && strings.TrimSpace(eventStoreID) == trustedStoreID
}

func ensureTrustedWaffoPancakeWebhookStore(c *gin.Context, event *service.WaffoPancakeWebhookEvent, normalizedEvent *service.NormalizedPaymentEvent) bool {
	if trustedWaffoPancakeWebhookStore(event.StoreID) {
		return true
	}
	logger.LogError(c.Request.Context(), fmt.Sprintf(
		"Waffo Pancake webhook StoreID 不受信任 event_store_id=%q event_id=%s order_id=%s client_ip=%s",
		event.StoreID, event.ID, event.Data.OrderID, c.ClientIP(),
	))
	if recordErr := service.RecordUnmatchedPaymentEvent(normalizedEvent, "waffo_pancake_store_mismatch"); recordErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake StoreID 不匹配事件留存失败 event_id=%s error=%q", event.ID, recordErr.Error()))
		c.String(http.StatusInternalServerError, "retry")
		return false
	}
	c.String(http.StatusOK, "OK")
	return false
}

func WaffoPancakeWebhook(c *gin.Context) {
	if !ensurePaymentWebhookClusterReady(c, model.PaymentProviderWaffoPancake, "retry") {
		return
	}
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	webhookConfigured := isWaffoPancakeWebhookConfiguredLocked()
	expectedConfiguredLivemode := !setting.WaffoPancakeTestMode
	unlockPaymentConfiguration()
	if !webhookConfigured {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", c.Request.URL.Path, c.ClientIP()))
		c.String(http.StatusForbidden, "webhook disabled")
		return
	}

	// :env splits test vs prod traffic at the routing layer — operator
	// registers each URL in the matching webhook slot in Pancake's dashboard.
	// We then enforce event.mode == expectedEnv to catch mis-registrations.
	expectedEnv, expectedPathLivemode, pathEnvironmentErr := service.ParseWaffoPancakeEnvironment(c.Param("env"))
	if pathEnvironmentErr != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake webhook 路径环境段无效 env=%q path=%q client_ip=%s",
			c.Param("env"), c.Request.URL.Path, c.ClientIP(),
		))
		c.String(http.StatusNotFound, "unknown env")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, retainedPaymentWebhookBodyLimit)
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 读取请求体失败 path=%q client_ip=%s error=%q", c.Request.URL.Path, c.ClientIP(), err.Error()))
		c.String(http.StatusBadRequest, "bad request")
		return
	}

	signature := c.GetHeader("X-Waffo-Signature")
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 收到请求 path=%q client_ip=%s body_size=%d", c.Request.URL.Path, c.ClientIP(), len(bodyBytes)))

	event, err := service.VerifyConfiguredWaffoPancakeWebhook(string(bodyBytes), signature)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 验签失败 path=%q client_ip=%s body_size=%d error=%q", c.Request.URL.Path, c.ClientIP(), len(bodyBytes), err.Error()))
		c.String(http.StatusUnauthorized, "invalid signature")
		return
	}
	normalizedEvent, err := normalizedWaffoPancakeWebhookEvent(event)
	if normalizedEvent == nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 标准化失败 event_id=%s event_type=%s error=%q", event.ID, event.NormalizedEventType(), err))
		c.String(http.StatusInternalServerError, "retry")
		return
	}
	// Store and buyer identity are the merchant authority boundary. Validate
	// them before an environment anomaly is allowed to move any canonical order
	// into manual review, otherwise a signed event for another store could cause
	// a cross-merchant denial of service through a colliding trade number.
	if !ensureTrustedWaffoPancakeWebhookStore(c, event, normalizedEvent) {
		return
	}
	if canonicalOrder, lookupErr := model.GetPaymentOrderByTradeNo(normalizedEvent.TradeNo); lookupErr == nil &&
		canonicalOrder.Provider == model.PaymentProviderWaffoPancake {
		expectedIdentity := service.WaffoPancakeBuyerIdentityFromUserID(canonicalOrder.UserID)
		if strings.TrimSpace(event.Data.MerchantProvidedBuyerIdentity) != expectedIdentity {
			logger.LogError(c.Request.Context(), fmt.Sprintf(
				"Waffo Pancake canonical webhook 买家身份不匹配 event_id=%s trade_no=%s",
				event.ID, normalizedEvent.TradeNo,
			))
			if recordErr := service.RecordUnmatchedPaymentEvent(normalizedEvent, "waffo_pancake_buyer_identity_mismatch"); recordErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake canonical 买家身份异常事件留存失败 event_id=%s error=%q", event.ID, recordErr.Error()))
				c.String(http.StatusInternalServerError, "retry")
				return
			}
			c.String(http.StatusOK, "OK")
			return
		}
	}
	if err != nil {
		if quarantineErr := quarantineWaffoPancakeEnvironmentEvent(normalizedEvent, "waffo_pancake_webhook_environment_invalid"); quarantineErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 环境异常留存失败 event_id=%s error=%q", event.ID, quarantineErr.Error()))
			c.String(http.StatusInternalServerError, "retry")
			return
		}
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 环境异常已转人工核对 event_id=%s actual_mode=%q error=%q", event.ID, event.Mode, err.Error()))
		c.String(http.StatusOK, "OK")
		return
	}
	environmentMismatchReason := waffoPancakeWebhookEnvironmentMismatchReason(
		expectedPathLivemode, expectedConfiguredLivemode, normalizedEvent.ProviderLivemode,
	)
	if environmentMismatchReason != "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake webhook 环境不匹配 path_environment=%q configured_livemode=%t actual_mode=%q event_id=%s order_id=%s client_ip=%s",
			expectedEnv, expectedConfiguredLivemode, event.Mode, event.ID, event.Data.OrderID, c.ClientIP(),
		))
		if recordErr := quarantineWaffoPancakeEnvironmentEvent(normalizedEvent, environmentMismatchReason); recordErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 环境不匹配事件留存失败 event_id=%s error=%q", event.ID, recordErr.Error()))
			c.String(http.StatusInternalServerError, "retry")
			return
		}
		c.String(http.StatusOK, "OK")
		return
	}
	if handled, canonicalErr := processCanonicalRetainedPaymentEvent(normalizedEvent); handled {
		if canonicalErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake canonical webhook 处理失败 event_id=%s trade_no=%s error=%q", event.ID, normalizedEvent.TradeNo, canonicalErr.Error()))
			c.String(http.StatusInternalServerError, "retry")
			return
		}
		c.String(http.StatusOK, "OK")
		return
	}
	if err := service.RecordVerifiedRetainedPaymentWebhookReceived(normalizedEvent); err != nil {
		if retainedPaymentInboxStopsSettlement(err) {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 重复或冲突 event_id=%s event_type=%s trade_no=%s error=%q", event.ID, event.NormalizedEventType(), normalizedEvent.TradeNo, err.Error()))
			c.String(http.StatusOK, "OK")
			return
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 事件持久化失败 event_id=%s event_type=%s error=%q", event.ID, event.NormalizedEventType(), err.Error()))
		c.String(http.StatusInternalServerError, "retry")
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 验签成功 event_type=%s event_id=%s order_id=%s client_ip=%s", event.NormalizedEventType(), event.ID, event.Data.OrderID, c.ClientIP()))
	if event.NormalizedEventType() != "order.completed" {
		if err := service.MarkVerifiedRetainedPaymentWebhookProcessed(normalizedEvent); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 忽略事件状态持久化失败 event_id=%s event_type=%s error=%q", event.ID, event.NormalizedEventType(), err.Error()))
			c.String(http.StatusInternalServerError, "retry")
			return
		}
		c.String(http.StatusOK, "OK")
		return
	}

	// Dispatch by trade_no prefix. OrderMerchantExternalID = our trade_no;
	// OrderID is Pancake's internal ORD_* (logs only).
	rawTradeNo := strings.TrimSpace(event.Data.OrderMerchantExternalID)
	isSubscription := strings.HasPrefix(rawTradeNo, "WAFFO_PANCAKE_SUB-")
	var paidAmountMinor *int64
	if amount, err := model.ParseProviderPaymentAmountMinor(
		event.Data.Amount,
		model.PaymentProviderWaffoPancake,
		event.Data.Currency,
	); err == nil && amount > 0 {
		paidAmountMinor = &amount
	}

	if isSubscription {
		tradeNo, err := service.ResolveWaffoPancakeSubscriptionTradeNo(event)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf(
				"Waffo Pancake webhook 订阅订单解析失败 event_id=%s order_id=%s client_ip=%s error=%q",
				event.ID, event.Data.OrderID, c.ClientIP(), err.Error(),
			))
			if recordErr := service.RecordUnmatchedPaymentEvent(normalizedEvent, "waffo_pancake_subscription_order_unmatched"); recordErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 未匹配订阅事件留存失败 event_id=%s error=%q", event.ID, recordErr.Error()))
				c.String(http.StatusInternalServerError, "retry")
				return
			}
			c.String(http.StatusOK, "OK")
			return
		}
		LockOrder(tradeNo)
		defer UnlockOrder(tradeNo)
		if err := model.CompleteSubscriptionOrderVerified(tradeNo, model.SubscriptionPaymentConfirmation{
			ProviderPayload:         string(bodyBytes),
			ExpectedPaymentProvider: model.PaymentProviderWaffoPancake,
			ActualPaymentMethod:     model.PaymentMethodWaffoPancake,
			PaidAmountMinor:         paidAmountMinor,
			Currency:                event.Data.Currency,
			ProviderOrderId:         event.Data.OrderID,
		}); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅完成失败 trade_no=%s event_id=%s order_id=%s client_ip=%s error=%q", tradeNo, event.ID, event.Data.OrderID, c.ClientIP(), err.Error()))
			if errors.Is(err, model.ErrSubscriptionOrderSnapshotMissing) ||
				errors.Is(err, model.ErrSubscriptionOrderManualReview) ||
				errors.Is(err, model.ErrSubscriptionPaymentAmountRequired) ||
				errors.Is(err, model.ErrSubscriptionPaymentAmountMismatch) ||
				errors.Is(err, model.ErrSubscriptionPaymentCurrencyMismatch) ||
				errors.Is(err, model.ErrSubscriptionProviderOrderRequired) ||
				errors.Is(err, model.ErrPaymentMethodMismatch) {
				if recordErr := service.RecordUnmatchedPaymentEvent(normalizedEvent, "waffo_pancake_subscription_settlement_manual_review"); recordErr != nil {
					logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅人工核对事件留存失败 trade_no=%s event_id=%s error=%q", tradeNo, event.ID, recordErr.Error()))
					c.String(http.StatusInternalServerError, "retry")
					return
				}
				c.String(http.StatusOK, "OK")
				return
			}
			_ = service.MarkVerifiedPaymentWebhookValidationFailed(normalizedEvent, "waffo_pancake_subscription_settlement_failed")
			c.String(http.StatusInternalServerError, "retry")
			return
		}
		if err := service.MarkVerifiedRetainedPaymentWebhookProcessed(normalizedEvent); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅事件完成状态持久化失败 trade_no=%s event_id=%s error=%q", tradeNo, event.ID, err.Error()))
			c.String(http.StatusInternalServerError, "retry")
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅完成 trade_no=%s event_id=%s order_id=%s client_ip=%s", tradeNo, event.ID, event.Data.OrderID, c.ClientIP()))
		c.String(http.StatusOK, "OK")
		return
	}

	tradeNo, err := service.ResolveWaffoPancakeTradeNo(event)
	if err != nil {
		// LogError (not LogWarn): covers order-not-found and buyer-identity
		// mismatch — both warrant human attention. 200 OK so Waffo doesn't
		// retry a permanently-unresolvable webhook.
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake webhook 订单解析失败 event_id=%s order_id=%s client_ip=%s error=%q",
			event.ID, event.Data.OrderID, c.ClientIP(), err.Error(),
		))
		if recordErr := service.RecordUnmatchedPaymentEvent(normalizedEvent, "waffo_pancake_topup_order_unmatched"); recordErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 未匹配充值事件留存失败 event_id=%s error=%q", event.ID, recordErr.Error()))
			c.String(http.StatusInternalServerError, "retry")
			return
		}
		c.String(http.StatusOK, "OK")
		return
	}

	LockOrder(tradeNo)
	defer UnlockOrder(tradeNo)

	if err := model.RechargeWaffoPancake(tradeNo, model.TopUpPaymentConfirmation{
		PaidAmountMinor: paidAmountMinor,
		Currency:        event.Data.Currency,
		ProviderOrderId: event.Data.OrderID,
	}); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值处理失败 trade_no=%s event_id=%s order_id=%s client_ip=%s error=%q", tradeNo, event.ID, event.Data.OrderID, c.ClientIP(), err.Error()))
		if model.IsTopUpPaymentReviewError(err) {
			if recordErr := service.RecordUnmatchedPaymentEvent(normalizedEvent, "waffo_pancake_topup_settlement_manual_review"); recordErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值人工核对事件留存失败 trade_no=%s event_id=%s error=%q", tradeNo, event.ID, recordErr.Error()))
				c.String(http.StatusInternalServerError, "retry")
				return
			}
			c.String(http.StatusOK, "OK")
			return
		}
		_ = service.MarkVerifiedPaymentWebhookValidationFailed(normalizedEvent, "waffo_pancake_topup_settlement_failed")
		c.String(http.StatusInternalServerError, "retry")
		return
	}
	if err := service.MarkVerifiedRetainedPaymentWebhookProcessed(normalizedEvent); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值事件完成状态持久化失败 trade_no=%s event_id=%s error=%q", tradeNo, event.ID, err.Error()))
		c.String(http.StatusInternalServerError, "retry")
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值成功 trade_no=%s event_id=%s order_id=%s client_ip=%s", tradeNo, event.ID, event.Data.OrderID, c.ClientIP()))
	c.String(http.StatusOK, "OK")
}
