package controller

import (
	"errors"
	"fmt"
	"io"
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
	"github.com/shopspring/decimal"
	"github.com/waffo-com/waffo-go/config"
	"github.com/waffo-com/waffo-go/core"
	waffoutils "github.com/waffo-com/waffo-go/utils"
)

func getWaffoWebhookHandler() (*core.WebhookHandler, error) {
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	privateKey := setting.WaffoPrivateKey
	publicKey := setting.WaffoPublicCert
	if setting.WaffoSandbox {
		privateKey = setting.WaffoSandboxPrivateKey
		publicKey = setting.WaffoSandboxPublicCert
	}
	unlockPaymentConfiguration()
	if err := waffoutils.ValidatePrivateKey(privateKey); err != nil {
		return nil, err
	}
	if err := waffoutils.ValidatePublicKey(publicKey); err != nil {
		return nil, err
	}
	return core.NewWebhookHandler(&config.WaffoConfig{
		PrivateKey:     privateKey,
		WaffoPublicKey: publicKey,
	}), nil
}

func getWaffoCurrency() string {
	if setting.WaffoCurrency != "" {
		return setting.WaffoCurrency
	}
	return "USD"
}

// getWaffoPayMoney converts the user-facing amount to USD for Waffo payment.
// Waffo only accepts USD, so this function handles the conversion from different
// display types (USD/CNY/TOKENS) to the actual USD amount to charge.
func getWaffoPayMoney(requestedAmount, normalizedAmount int64, group string) float64 {
	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}
	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(requestedAmount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	return decimal.NewFromInt(normalizedAmount).
		Mul(decimal.NewFromFloat(setting.WaffoUnitPrice)).
		Mul(decimal.NewFromFloat(topupGroupRatio)).
		Mul(decimal.NewFromFloat(discount)).
		InexactFloat64()
}

type WaffoPayRequest struct {
	Amount         int64  `json:"amount"`
	OptionID       string `json:"option_id,omitempty"`
	RequestID      string `json:"request_id"`
	PayMethodIndex *int   `json:"pay_method_index"` // 服务端支付方式列表的索引，nil 表示由 Waffo 自动选择
	PayMethodType  string `json:"pay_method_type"`  // Deprecated: 兼容旧前端，优先使用 pay_method_index
	PayMethodName  string `json:"pay_method_name"`  // Deprecated: 兼容旧前端，优先使用 pay_method_index
}

func waffoPayMethodIdentity(payMethodType, payMethodName string) string {
	payMethodType = strings.TrimSpace(payMethodType)
	payMethodName = strings.TrimSpace(payMethodName)
	if payMethodType == "" && payMethodName == "" {
		return ""
	}
	return payMethodType + "\x00" + payMethodName
}

func publicWaffoPayMethodLabel(payMethodType, payMethodName string) string {
	value := strings.ToUpper(strings.TrimSpace(payMethodType + "," + payMethodName))
	switch {
	case strings.Contains(value, "APPLEPAY"):
		return "Apple Pay"
	case strings.Contains(value, "GOOGLEPAY"):
		return "Google Pay"
	case strings.Contains(value, "CREDITCARD"), strings.Contains(value, "DEBITCARD"):
		return "Card"
	default:
		return "Online payment"
	}
}

func RequestWaffoAmount(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req WaffoPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		legacyPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}
	if req.Amount <= 0 || req.Amount > service.MaxPaymentTopUpAmount {
		legacyPaymentAPIError(c, "payment_amount_invalid", gin.H{"min": 1, "max": service.MaxPaymentTopUpAmount})
		return
	}

	waffoMinTopup := int64(setting.WaffoMinTopUp)
	if req.Amount < waffoMinTopup {
		legacyPaymentAPIError(c, "payment_amount_below_minimum", gin.H{"min": waffoMinTopup})
		return
	}
	normalizedAmount, _, valid := normalizeRetainedTopUpCredit(req.Amount)
	if !valid {
		legacyPaymentAPIError(c, "payment_amount_invalid", gin.H{"min": 1, "max": service.MaxPaymentTopUpAmount})
		return
	}
	if !isWaffoTopUpEnabled() {
		legacyPaymentAPIError(c, "payment_method_unavailable", nil)
		return
	}

	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 报价用户分组查询失败 user_id=%d error=%q", id, err.Error()))
		legacyPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}

	payMoney := getWaffoPayMoney(req.Amount, normalizedAmount, group)
	if payMoney <= 0.01 {
		legacyPaymentAPIError(c, "payment_amount_below_minimum", nil)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "success", "data": strconv.FormatFloat(payMoney, 'f', 2, 64)})
}

// RequestWaffoPay 创建 Waffo 支付订单
func RequestWaffoPay(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req WaffoPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		legacyPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}
	if req.Amount <= 0 || req.Amount > service.MaxPaymentTopUpAmount {
		legacyPaymentAPIError(c, "payment_amount_invalid", gin.H{"min": 1, "max": service.MaxPaymentTopUpAmount})
		return
	}
	if !isWaffoTopUpEnabled() {
		legacyPaymentAPIError(c, "payment_method_unavailable", nil)
		return
	}

	// 从服务端配置查找支付方式。新客户端只提交当前配置快照生成的
	// opaque option_id；索引和网关字段仅保留在请求边界兼容旧客户端。
	var resolvedPayMethodType, resolvedPayMethodName string
	methods := setting.GetWaffoPayMethods()
	req.OptionID = strings.TrimSpace(req.OptionID)
	if req.OptionID != "" {
		valid := false
		for _, method := range methods {
			if service.PublicRetainedOptionID(
				model.PaymentProviderWaffo,
				waffoPayMethodIdentity(method.PayMethodType, method.PayMethodName),
			) != req.OptionID {
				continue
			}
			valid = true
			resolvedPayMethodType = method.PayMethodType
			resolvedPayMethodName = method.PayMethodName
			break
		}
		if !valid {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 公开支付选项无效 user_id=%d", c.GetInt("id")))
			legacyPaymentAPIError(c, "payment_method_unavailable", nil)
			return
		}
	} else if req.PayMethodIndex != nil {
		// 新协议：按索引查找
		idx := *req.PayMethodIndex
		if idx < 0 || idx >= len(methods) {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 支付方式索引无效 user_id=%d pay_method_index=%d method_count=%d", c.GetInt("id"), idx, len(methods)))
			legacyPaymentAPIError(c, "payment_method_unavailable", nil)
			return
		}
		resolvedPayMethodType = methods[idx].PayMethodType
		resolvedPayMethodName = methods[idx].PayMethodName
	} else if req.PayMethodType != "" {
		// 兼容旧前端：验证客户端传的值在服务端列表中
		valid := false
		for _, m := range methods {
			if m.PayMethodType == req.PayMethodType && m.PayMethodName == req.PayMethodName {
				valid = true
				resolvedPayMethodType = m.PayMethodType
				resolvedPayMethodName = m.PayMethodName
				break
			}
		}
		if !valid {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 支付方式无效 user_id=%d pay_method_type=%s pay_method_name=%q", c.GetInt("id"), req.PayMethodType, req.PayMethodName))
			legacyPaymentAPIError(c, "payment_method_unavailable", nil)
			return
		}
	}
	optionID := req.OptionID
	if optionID == "" && (resolvedPayMethodType != "" || resolvedPayMethodName != "") {
		optionID = service.PublicRetainedOptionID(
			model.PaymentProviderWaffo,
			waffoPayMethodIdentity(resolvedPayMethodType, resolvedPayMethodName),
		)
	}
	startRetainedCompatibilityPayment(c, req.RequestID, service.PaymentQuoteRequest{
		OrderKind:     model.PaymentOrderKindTopUp,
		Provider:      model.PaymentProviderWaffo,
		PaymentMethod: model.PaymentMethodWaffo,
		Amount:        req.Amount,
		OptionID:      optionID,
	})
}

// webhookPayloadWithSubInfo 扩展 PAYMENT_NOTIFICATION，包含 SDK 未定义的 subscriptionInfo 字段
type webhookPayloadWithSubInfo struct {
	EventType string `json:"eventType"`
	Result    struct {
		core.PaymentNotificationResult
		SubscriptionInfo *webhookSubscriptionInfo `json:"subscriptionInfo,omitempty"`
	} `json:"result"`
}

type webhookSubscriptionInfo struct {
	Period              string `json:"period,omitempty"`
	MerchantRequest     string `json:"merchantRequest,omitempty"`
	SubscriptionID      string `json:"subscriptionId,omitempty"`
	SubscriptionRequest string `json:"subscriptionRequest,omitempty"`
}

type waffoOrderStatusDisposition uint8

const (
	waffoOrderStatusPending waffoOrderStatusDisposition = iota
	waffoOrderStatusSucceeded
	waffoOrderStatusFailed
	waffoOrderStatusManualReview
)

func classifyWaffoOrderStatus(status string) waffoOrderStatusDisposition {
	switch strings.TrimSpace(status) {
	case core.OrderStatusPaySuccess:
		return waffoOrderStatusSucceeded
	case core.OrderStatusOrderClose:
		return waffoOrderStatusFailed
	case core.OrderStatusPayInProgress,
		core.OrderStatusAuthorizationRequired,
		core.OrderStatusAuthedWaitingCapture:
		return waffoOrderStatusPending
	default:
		return waffoOrderStatusManualReview
	}
}

func normalizedWaffoWebhookEvent(eventType string, result *core.PaymentNotificationResult) (*service.NormalizedPaymentEvent, error) {
	if result == nil {
		return nil, errors.New("missing Waffo payment notification result")
	}
	tradeNo := strings.TrimSpace(result.MerchantOrderID)
	providerOrderKey := service.RetainedProviderAuthorityKey(model.PaymentProviderWaffo, result.AcquiringOrderID)
	providerState := strings.TrimSpace(result.OrderStatus)
	statusDisposition := classifyWaffoOrderStatus(providerState)
	currency := strings.ToUpper(strings.TrimSpace(result.OrderCurrency))
	paidAmountMinor := int64(0)
	if amount, err := model.ParseProviderPaymentAmountMinor(result.OrderAmount, model.PaymentProviderWaffo, currency); err == nil && amount > 0 {
		paidAmountMinor = amount
	}
	payload, err := retainedPaymentNormalizedPayload(retainedPaymentWebhookFacts{
		EventType:        strings.TrimSpace(eventType),
		TradeNo:          tradeNo,
		ProviderOrderKey: providerOrderKey,
		ProviderState:    providerState,
		PaidAmountMinor:  paidAmountMinor,
		Currency:         currency,
		PaymentMethod:    model.PaymentMethodWaffo,
	})
	if err != nil {
		return nil, err
	}
	return &service.NormalizedPaymentEvent{
		Provider:          model.PaymentProviderWaffo,
		EventKey:          retainedPaymentEventKey(model.PaymentProviderWaffo, "", strings.TrimSpace(eventType)+":"+providerState, providerOrderKey, tradeNo),
		EventType:         strings.TrimSpace(eventType),
		TradeNo:           tradeNo,
		ProviderOrderKey:  providerOrderKey,
		ProviderState:     providerState,
		PaidAmountMinor:   paidAmountMinor,
		Currency:          currency,
		PaymentMethod:     model.PaymentMethodWaffo,
		Paid:              statusDisposition == waffoOrderStatusSucceeded,
		Failed:            statusDisposition == waffoOrderStatusFailed,
		PermanentFailure:  statusDisposition == waffoOrderStatusFailed,
		ManualReview:      statusDisposition == waffoOrderStatusManualReview,
		NormalizedPayload: payload,
	}, nil
}

// WaffoWebhook 处理 Waffo 回调通知（支付/退款/订阅）
func WaffoWebhook(c *gin.Context) {
	if !ensurePaymentWebhookClusterReady(c, model.PaymentProviderWaffo, "") {
		return
	}
	if !isWaffoWebhookEnabled() {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", c.Request.URL.Path, c.ClientIP()))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, retainedPaymentWebhookBodyLimit)
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo webhook 读取请求体失败 path=%q client_ip=%s error=%q", c.Request.URL.Path, c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	wh, err := getWaffoWebhookHandler()
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo webhook 验签器初始化失败 path=%q client_ip=%s error=%q", c.Request.URL.Path, c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	bodyStr := string(bodyBytes)
	signature := c.GetHeader("X-SIGNATURE")
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo webhook 收到请求 path=%q client_ip=%s body_size=%d", c.Request.URL.Path, c.ClientIP(), len(bodyBytes)))

	// 验证请求签名
	if !wh.VerifySignature(bodyStr, signature) {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo webhook 验签失败 path=%q client_ip=%s body_size=%d", c.Request.URL.Path, c.ClientIP(), len(bodyBytes)))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	var event core.WebhookEvent
	if err := common.Unmarshal(bodyBytes, &event); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo webhook 解析失败 path=%q client_ip=%s body_size=%d error=%q", c.Request.URL.Path, c.ClientIP(), len(bodyBytes), err.Error()))
		sendWaffoWebhookResponse(c, wh, false, "invalid payload")
		return
	}

	switch event.EventType {
	case core.EventPayment:
		// 解析为扩展类型，区分普通支付和订阅支付
		var payload webhookPayloadWithSubInfo
		if err := common.Unmarshal(bodyBytes, &payload); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 支付回调载荷解析失败 event_type=%s client_ip=%s body_size=%d error=%q", event.EventType, c.ClientIP(), len(bodyBytes), err.Error()))
			sendWaffoWebhookResponse(c, wh, false, "invalid payment payload")
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo webhook 验签并解析成功 event_type=%s merchant_order_id=%s order_status=%s client_ip=%s", event.EventType, payload.Result.MerchantOrderID, payload.Result.OrderStatus, c.ClientIP()))
		normalizedEvent, err := normalizedWaffoWebhookEvent(event.EventType, &payload.Result.PaymentNotificationResult)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo webhook 标准化失败 event_type=%s merchant_order_id=%s error=%q", event.EventType, payload.Result.MerchantOrderID, err.Error()))
			sendWaffoWebhookResponse(c, wh, false, "processing failed")
			return
		}
		if handled, canonicalErr := processCanonicalRetainedPaymentEvent(normalizedEvent); handled {
			if canonicalErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo canonical webhook 处理失败 event_type=%s trade_no=%s error=%q", event.EventType, normalizedEvent.TradeNo, canonicalErr.Error()))
				sendWaffoWebhookResponse(c, wh, false, "processing failed")
				return
			}
			sendWaffoWebhookResponse(c, wh, true, "")
			return
		}
		if err := service.RecordVerifiedRetainedPaymentWebhookReceived(normalizedEvent); err != nil {
			if retainedPaymentInboxStopsSettlement(err) {
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo webhook 重复或冲突 event_type=%s trade_no=%s error=%q", event.EventType, normalizedEvent.TradeNo, err.Error()))
				sendWaffoWebhookResponse(c, wh, true, "")
				return
			}
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo webhook 事件持久化失败 event_type=%s trade_no=%s error=%q", event.EventType, normalizedEvent.TradeNo, err.Error()))
			sendWaffoWebhookResponse(c, wh, false, "processing failed")
			return
		}
		handleWaffoPayment(c, wh, &payload.Result.PaymentNotificationResult, normalizedEvent)
	default:
		normalizedPayload, normalizeErr := retainedPaymentNormalizedPayload(retainedPaymentWebhookFacts{
			EventType:     strings.TrimSpace(event.EventType),
			PayloadDigest: model.PaymentPayloadDigest(bodyStr),
		})
		if normalizeErr != nil {
			sendWaffoWebhookResponse(c, wh, false, "processing failed")
			return
		}
		normalizedEvent := &service.NormalizedPaymentEvent{
			Provider:          model.PaymentProviderWaffo,
			EventKey:          model.PaymentEventKey(model.PaymentProviderWaffo, event.EventType, "", "", bodyStr),
			EventType:         strings.TrimSpace(event.EventType),
			NormalizedPayload: normalizedPayload,
		}
		if err := service.RecordVerifiedRetainedPaymentWebhookReceived(normalizedEvent); err != nil {
			if retainedPaymentInboxStopsSettlement(err) {
				sendWaffoWebhookResponse(c, wh, true, "")
				return
			}
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo webhook 忽略事件持久化失败 event_type=%s error=%q", event.EventType, err.Error()))
			sendWaffoWebhookResponse(c, wh, false, "processing failed")
			return
		}
		if err := service.MarkVerifiedRetainedPaymentWebhookProcessed(normalizedEvent); err != nil &&
			!errors.Is(err, model.ErrPaymentEventConflict) && !errors.Is(err, model.ErrPaymentManualReview) {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo webhook 忽略事件状态持久化失败 event_type=%s error=%q", event.EventType, err.Error()))
			sendWaffoWebhookResponse(c, wh, false, "processing failed")
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo webhook 忽略事件 event_type=%s client_ip=%s", event.EventType, c.ClientIP()))
		sendWaffoWebhookResponse(c, wh, true, "")
	}
}

// handleWaffoPayment 处理支付完成通知
func handleWaffoPayment(c *gin.Context, wh *core.WebhookHandler, result *core.PaymentNotificationResult, normalizedEvent *service.NormalizedPaymentEvent) {
	statusDisposition := classifyWaffoOrderStatus(result.OrderStatus)
	if statusDisposition != waffoOrderStatusSucceeded {
		if statusDisposition == waffoOrderStatusManualReview {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 订单状态未知，保持本地订单状态并转人工核对 trade_no=%s order_status=%s client_ip=%s", result.MerchantOrderID, result.OrderStatus, c.ClientIP()))
			if err := service.RecordUnmatchedPaymentEvent(normalizedEvent, "waffo_order_status_unknown"); err != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 未知订单状态事件留存失败 trade_no=%s order_status=%s error=%q", result.MerchantOrderID, result.OrderStatus, err.Error()))
				sendWaffoWebhookResponse(c, wh, false, "processing failed")
				return
			}
			sendWaffoWebhookResponse(c, wh, true, "")
			return
		}

		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo 订单尚未支付成功，保持当前处理语义 trade_no=%s order_status=%s client_ip=%s", result.MerchantOrderID, result.OrderStatus, c.ClientIP()))
		if statusDisposition == waffoOrderStatusFailed && result.MerchantOrderID != "" {
			if err := model.UpdatePendingTopUpStatus(result.MerchantOrderID, model.PaymentProviderWaffo, common.TopUpStatusFailed); err != nil &&
				!errors.Is(err, model.ErrTopUpNotFound) &&
				!errors.Is(err, model.ErrTopUpStatusInvalid) {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 标记失败订单状态失败 trade_no=%s error=%q", result.MerchantOrderID, err.Error()))
				_ = service.MarkVerifiedPaymentWebhookValidationFailed(normalizedEvent, "waffo_order_status_update_failed")
				sendWaffoWebhookResponse(c, wh, false, "processing failed")
				return
			}
		}
		if err := service.MarkVerifiedRetainedPaymentWebhookProcessed(normalizedEvent); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 非成功事件状态持久化失败 trade_no=%s error=%q", result.MerchantOrderID, err.Error()))
			sendWaffoWebhookResponse(c, wh, false, "processing failed")
			return
		}
		sendWaffoWebhookResponse(c, wh, true, "")
		return
	}

	merchantOrderId := result.MerchantOrderID
	var paidAmountMinor *int64
	if amount, err := model.ParseProviderPaymentAmountMinor(
		result.OrderAmount,
		model.PaymentProviderWaffo,
		result.OrderCurrency,
	); err == nil && amount > 0 {
		paidAmountMinor = &amount
	}

	LockOrder(merchantOrderId)
	defer UnlockOrder(merchantOrderId)

	if err := model.RechargeWaffo(merchantOrderId, model.TopUpPaymentConfirmation{
		PaidAmountMinor: paidAmountMinor,
		Currency:        result.OrderCurrency,
		ProviderOrderId: result.AcquiringOrderID,
	}, c.ClientIP()); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 充值处理失败 trade_no=%s client_ip=%s error=%q", merchantOrderId, c.ClientIP(), err.Error()))
		if model.IsTopUpPaymentReviewError(err) {
			if recordErr := service.RecordUnmatchedPaymentEvent(normalizedEvent, "waffo_topup_settlement_manual_review"); recordErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 充值人工核对事件留存失败 trade_no=%s error=%q", merchantOrderId, recordErr.Error()))
				sendWaffoWebhookResponse(c, wh, false, "processing failed")
				return
			}
			sendWaffoWebhookResponse(c, wh, true, "")
			return
		}
		_ = service.MarkVerifiedPaymentWebhookValidationFailed(normalizedEvent, "waffo_topup_settlement_failed")
		sendWaffoWebhookResponse(c, wh, false, "processing failed")
		return
	}
	if err := service.MarkVerifiedRetainedPaymentWebhookProcessed(normalizedEvent); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 充值事件完成状态持久化失败 trade_no=%s error=%q", merchantOrderId, err.Error()))
		sendWaffoWebhookResponse(c, wh, false, "processing failed")
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo 充值成功 trade_no=%s client_ip=%s", merchantOrderId, c.ClientIP()))
	sendWaffoWebhookResponse(c, wh, true, "")
}

// sendWaffoWebhookResponse 发送签名响应
func sendWaffoWebhookResponse(c *gin.Context, wh *core.WebhookHandler, success bool, msg string) {
	var body, sig string
	if success {
		body, sig = wh.BuildSuccessResponse()
	} else {
		body, sig = wh.BuildFailedResponse(msg)
	}
	c.Header("X-SIGNATURE", sig)
	c.Data(http.StatusOK, "application/json", []byte(body))
}
