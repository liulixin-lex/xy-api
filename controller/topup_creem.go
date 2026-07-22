package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

const CreemSignatureHeader = "creem-signature"

const maxCreemProducts = 100

var creemAdaptor = &CreemAdaptor{}

// 生成HMAC-SHA256签名
func generateCreemSignature(payload string, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}

// 验证Creem webhook签名
func verifyCreemSignature(payload string, signature string, secret string) bool {
	if secret == "" || signature == "" {
		return false
	}

	expectedSignature := generateCreemSignature(payload, secret)
	return hmac.Equal([]byte(signature), []byte(expectedSignature))
}

type CreemPayRequest struct {
	ProductId     string `json:"product_id"`
	PaymentMethod string `json:"payment_method,omitempty"`
	RequestID     string `json:"request_id"`
}

type CreemProduct struct {
	ProductId string  `json:"productId"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency"`
	Quota     int64   `json:"quota"`
}

func parseValidatedCreemProducts(value string) ([]CreemProduct, error) {
	var products []CreemProduct
	if err := common.UnmarshalJsonStr(value, &products); err != nil || products == nil {
		return nil, errors.New("Creem products must be a JSON array")
	}
	if len(products) > maxCreemProducts {
		return nil, fmt.Errorf("Creem products exceed the maximum of %d entries", maxCreemProducts)
	}

	seenProductIDs := make(map[string]struct{}, len(products))
	for index := range products {
		name := strings.TrimSpace(products[index].Name)
		if name == "" || utf8.RuneCountInString(name) > 128 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("Creem product %d has an invalid public name", index+1)
		}
		productID := strings.TrimSpace(products[index].ProductId)
		if productID == "" || len(productID) > 255 {
			return nil, fmt.Errorf("Creem product %d has an invalid product ID", index+1)
		}
		if _, exists := seenProductIDs[productID]; exists {
			return nil, fmt.Errorf("Creem product %d has a duplicate product ID", index+1)
		}
		seenProductIDs[productID] = struct{}{}

		currency := strings.ToUpper(strings.TrimSpace(products[index].Currency))
		if currency != "USD" && currency != "EUR" {
			return nil, fmt.Errorf("Creem product %d has an unsupported currency", index+1)
		}
		amountMinor, err := model.ProviderPaymentAmountToMinor(
			products[index].Price,
			model.PaymentProviderCreem,
			currency,
		)
		if err != nil || amountMinor <= 0 {
			return nil, fmt.Errorf("Creem product %d has an invalid price", index+1)
		}
		if products[index].Quota < 1 || products[index].Quota > int64(common.MaxQuota) {
			return nil, fmt.Errorf("Creem product %d has an invalid quota", index+1)
		}

		products[index].Name = name
		products[index].ProductId = productID
		products[index].Currency = currency
	}
	return products, nil
}

type CreemAdaptor struct {
}

func (*CreemAdaptor) RequestPay(c *gin.Context, req *CreemPayRequest) {
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	productsConfig := setting.CreemProducts
	unlockPaymentConfiguration()
	if req.PaymentMethod != "" && req.PaymentMethod != model.PaymentMethodCreem {
		legacyPaymentAPIError(c, "payment_method_unavailable", nil)
		return
	}

	req.ProductId = strings.TrimSpace(req.ProductId)
	if req.ProductId == "" {
		legacyPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}

	// 配置保存和下单时使用同一套产品校验，防止旧数据或绕过管理接口的异常配置入账。
	products, err := parseValidatedCreemProducts(productsConfig)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 产品配置解析失败 user_id=%d error=%q", c.GetInt("id"), err.Error()))
		legacyPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}

	// 新客户端只提交 opaque product_id。真实产品 ID 仅在当前配置快照
	// 内解析；直接提交真实 ID 的路径仅保留在请求边界兼容旧客户端。
	var selectedProduct *CreemProduct
	for _, product := range products {
		if product.ProductId == req.ProductId ||
			service.PublicRetainedProductID(model.PaymentProviderCreem, product.ProductId) == req.ProductId {
			selectedProduct = &product
			break
		}
	}

	if selectedProduct == nil {
		legacyPaymentAPIError(c, "payment_product_unavailable", nil)
		return
	}
	if !isCreemTopUpEnabled() {
		legacyPaymentAPIError(c, "payment_method_unavailable", nil)
		return
	}

	startRetainedCompatibilityPayment(c, req.RequestID, service.PaymentQuoteRequest{
		OrderKind:     model.PaymentOrderKindTopUp,
		Provider:      model.PaymentProviderCreem,
		PaymentMethod: model.PaymentMethodCreem,
		ProductID:     service.PublicRetainedProductID(model.PaymentProviderCreem, selectedProduct.ProductId),
	})
}

func RequestCreemPay(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req CreemPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		legacyPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}
	creemAdaptor.RequestPay(c, &req)
}

// 新的Creem Webhook结构体，匹配实际的webhook数据格式
type CreemWebhookEvent struct {
	Id        string `json:"id"`
	EventType string `json:"eventType"`
	CreatedAt int64  `json:"created_at"`
	Object    struct {
		Id        string `json:"id"`
		Object    string `json:"object"`
		RequestId string `json:"request_id"`
		Order     struct {
			Object      string `json:"object"`
			Id          string `json:"id"`
			Customer    string `json:"customer"`
			Product     string `json:"product"`
			Amount      int    `json:"amount"`
			Currency    string `json:"currency"`
			SubTotal    int    `json:"sub_total"`
			TaxAmount   int    `json:"tax_amount"`
			AmountDue   int    `json:"amount_due"`
			AmountPaid  int    `json:"amount_paid"`
			Status      string `json:"status"`
			Type        string `json:"type"`
			Transaction string `json:"transaction"`
			CreatedAt   string `json:"created_at"`
			UpdatedAt   string `json:"updated_at"`
			Mode        string `json:"mode"`
		} `json:"order"`
		Product struct {
			Id                string  `json:"id"`
			Object            string  `json:"object"`
			Name              string  `json:"name"`
			Description       string  `json:"description"`
			Price             int     `json:"price"`
			Currency          string  `json:"currency"`
			BillingType       string  `json:"billing_type"`
			BillingPeriod     string  `json:"billing_period"`
			Status            string  `json:"status"`
			TaxMode           string  `json:"tax_mode"`
			TaxCategory       string  `json:"tax_category"`
			DefaultSuccessUrl *string `json:"default_success_url"`
			CreatedAt         string  `json:"created_at"`
			UpdatedAt         string  `json:"updated_at"`
			Mode              string  `json:"mode"`
		} `json:"product"`
		Units    int `json:"units"`
		Customer struct {
			Id        string `json:"id"`
			Object    string `json:"object"`
			Email     string `json:"email"`
			Name      string `json:"name"`
			Country   string `json:"country"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
			Mode      string `json:"mode"`
		} `json:"customer"`
		Status   string            `json:"status"`
		Metadata map[string]string `json:"metadata"`
		Mode     string            `json:"mode"`
	} `json:"object"`
}

func creemWebhookEnvironment(event *CreemWebhookEvent) (string, bool, error) {
	if event == nil {
		return "", false, errors.New("missing Creem webhook event")
	}
	checkoutEnvironment, checkoutLivemode, err := service.ParseCreemWebhookEnvironment(event.Object.Mode)
	if err != nil {
		return "", false, fmt.Errorf("invalid checkout environment: %w", err)
	}
	_, orderLivemode, err := service.ParseCreemWebhookEnvironment(event.Object.Order.Mode)
	if err != nil {
		return "", false, fmt.Errorf("invalid order environment: %w", err)
	}
	if checkoutLivemode != orderLivemode {
		return "", false, errors.New("inconsistent checkout and order environments")
	}
	return checkoutEnvironment, checkoutLivemode, nil
}

func normalizedCreemWebhookEvent(event *CreemWebhookEvent) (*service.NormalizedPaymentEvent, error) {
	if event == nil {
		return nil, errors.New("missing Creem webhook event")
	}
	eventType := strings.TrimSpace(event.EventType)
	tradeNo := strings.TrimSpace(event.Object.RequestId)
	providerOrderKey := service.RetainedProviderAuthorityKey(model.PaymentProviderCreem, event.Object.Id)
	providerState := strings.TrimSpace(event.Object.Order.Status)
	paidAmountMinor := int64(event.Object.Order.AmountPaid)
	payload, err := retainedPaymentNormalizedPayload(retainedPaymentWebhookFacts{
		EventID:          strings.TrimSpace(event.Id),
		EventType:        eventType,
		TradeNo:          tradeNo,
		ProviderOrderKey: providerOrderKey,
		ProviderState:    providerState,
		PaidAmountMinor:  paidAmountMinor,
		Currency:         strings.ToUpper(strings.TrimSpace(event.Object.Order.Currency)),
		PaymentMethod:    model.PaymentMethodCreem,
		Environment:      strings.TrimSpace(event.Object.Order.Mode),
	})
	if err != nil {
		return nil, err
	}
	normalizedEvent := &service.NormalizedPaymentEvent{
		Provider:            model.PaymentProviderCreem,
		EventKey:            retainedPaymentEventKey(model.PaymentProviderCreem, event.Id, eventType+":"+providerState, providerOrderKey, tradeNo),
		EventType:           eventType,
		TradeNo:             tradeNo,
		ProviderOrderKey:    providerOrderKey,
		ProviderResourceKey: strings.TrimSpace(event.Object.Order.Id),
		ProviderState:       providerState,
		PaidAmountMinor:     paidAmountMinor,
		Currency:            strings.ToUpper(strings.TrimSpace(event.Object.Order.Currency)),
		PaymentMethod:       model.PaymentMethodCreem,
		Paid:                eventType == "checkout.completed" && providerState == "paid",
		NormalizedPayload:   payload,
	}
	_, providerLivemode, environmentErr := creemWebhookEnvironment(event)
	if environmentErr != nil {
		normalizedEvent.Paid = false
		normalizedEvent.ManualReview = true
		return normalizedEvent, environmentErr
	}
	normalizedEvent.ProviderLivemode = &providerLivemode
	return normalizedEvent, nil
}

func CreemWebhook(c *gin.Context) {
	if !ensurePaymentWebhookClusterReady(c, model.PaymentProviderCreem, "") {
		return
	}
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	webhookSecret := setting.CreemWebhookSecret
	expectedProviderLivemode := !setting.CreemTestMode
	unlockPaymentConfiguration()
	if strings.TrimSpace(webhookSecret) == "" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", c.Request.URL.Path, c.ClientIP()))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, retainedPaymentWebhookBodyLimit)
	// 读取body内容用于验签，同时保留原始数据供后续使用
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 读取请求体失败 path=%q client_ip=%s error=%q", c.Request.URL.Path, c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	signature := c.GetHeader(CreemSignatureHeader)
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem webhook 收到请求 path=%q client_ip=%s body_size=%d", c.Request.URL.Path, c.ClientIP(), len(bodyBytes)))
	if signature == "" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 缺少签名 path=%q client_ip=%s body_size=%d", c.Request.URL.Path, c.ClientIP(), len(bodyBytes)))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	// 验证签名
	if !verifyCreemSignature(string(bodyBytes), signature, webhookSecret) {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 验签失败 path=%q client_ip=%s body_size=%d", c.Request.URL.Path, c.ClientIP(), len(bodyBytes)))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem webhook 验签成功 path=%q client_ip=%s", c.Request.URL.Path, c.ClientIP()))

	// 解析新格式的webhook数据
	var webhookEvent CreemWebhookEvent
	if err := common.Unmarshal(bodyBytes, &webhookEvent); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 解析失败 path=%q client_ip=%s body_size=%d error=%q", c.Request.URL.Path, c.ClientIP(), len(bodyBytes), err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem webhook 解析成功 event_type=%s event_id=%s request_id=%s order_id=%s order_status=%s", webhookEvent.EventType, webhookEvent.Id, webhookEvent.Object.RequestId, webhookEvent.Object.Order.Id, webhookEvent.Object.Order.Status))
	normalizedEvent, err := normalizedCreemWebhookEvent(&webhookEvent)
	if err != nil {
		if normalizedEvent == nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 标准化失败 event_type=%s event_id=%s error=%q", webhookEvent.EventType, webhookEvent.Id, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		if handled, canonicalErr := processCanonicalRetainedPaymentEvent(normalizedEvent); handled {
			if canonicalErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 环境异常留存失败 event_type=%s event_id=%s trade_no=%s error=%q", webhookEvent.EventType, webhookEvent.Id, normalizedEvent.TradeNo, canonicalErr.Error()))
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 环境异常已转人工核对 event_type=%s event_id=%s trade_no=%s reason=%q", webhookEvent.EventType, webhookEvent.Id, normalizedEvent.TradeNo, err.Error()))
			c.Status(http.StatusOK)
			return
		}
		if recordErr := service.RecordUnmatchedPaymentEvent(normalizedEvent, "creem_webhook_environment_invalid"); recordErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 环境异常事件留存失败 event_type=%s event_id=%s error=%q", webhookEvent.EventType, webhookEvent.Id, recordErr.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 环境异常已留存 event_type=%s event_id=%s reason=%q", webhookEvent.EventType, webhookEvent.Id, err.Error()))
		c.Status(http.StatusOK)
		return
	}
	if handled, canonicalErr := processCanonicalRetainedPaymentEvent(normalizedEvent); handled {
		if canonicalErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem canonical webhook 处理失败 event_type=%s event_id=%s trade_no=%s error=%q", webhookEvent.EventType, webhookEvent.Id, normalizedEvent.TradeNo, canonicalErr.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
		return
	}
	if normalizedEvent.ProviderLivemode == nil || *normalizedEvent.ProviderLivemode != expectedProviderLivemode {
		normalizedEvent.Paid = false
		normalizedEvent.Failed = false
		normalizedEvent.Expired = false
		normalizedEvent.ManualReview = true
		if err := service.RecordUnmatchedPaymentEvent(normalizedEvent, "creem_legacy_environment_mismatch"); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem legacy webhook 环境不一致事件留存失败 event_type=%s event_id=%s error=%q", webhookEvent.EventType, webhookEvent.Id, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem legacy webhook 环境不一致已转人工核对 event_type=%s event_id=%s", webhookEvent.EventType, webhookEvent.Id))
		c.Status(http.StatusOK)
		return
	}
	if err := service.RecordVerifiedRetainedPaymentWebhookReceived(normalizedEvent); err != nil {
		if retainedPaymentInboxStopsSettlement(err) {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 重复或冲突 event_type=%s event_id=%s trade_no=%s error=%q", webhookEvent.EventType, webhookEvent.Id, normalizedEvent.TradeNo, err.Error()))
			c.Status(http.StatusOK)
			return
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 事件持久化失败 event_type=%s event_id=%s error=%q", webhookEvent.EventType, webhookEvent.Id, err.Error()))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// 根据事件类型处理不同的webhook
	switch webhookEvent.EventType {
	case "checkout.completed":
		handleCheckoutCompleted(c, &webhookEvent, normalizedEvent)
	default:
		if err := service.MarkVerifiedRetainedPaymentWebhookProcessed(normalizedEvent); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 忽略事件状态持久化失败 event_type=%s event_id=%s error=%q", webhookEvent.EventType, webhookEvent.Id, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem webhook 忽略事件 event_type=%s event_id=%s", webhookEvent.EventType, webhookEvent.Id))
		c.Status(http.StatusOK)
	}
}

// 处理支付完成事件
func handleCheckoutCompleted(c *gin.Context, event *CreemWebhookEvent, normalizedEvent *service.NormalizedPaymentEvent) {
	// 验证订单状态
	if event.Object.Order.Status != "paid" {
		if err := service.MarkVerifiedRetainedPaymentWebhookProcessed(normalizedEvent); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 非支付完成事件状态持久化失败 event_id=%s error=%q", event.Id, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem 订单状态未支付，忽略处理 request_id=%s order_id=%s order_status=%s", event.Object.RequestId, event.Object.Order.Id, event.Object.Order.Status))
		c.Status(http.StatusOK)
		return
	}

	// 获取引用ID（这是我们创建订单时传递的request_id）
	referenceId := event.Object.RequestId
	if referenceId == "" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 缺少 request_id event_id=%s order_id=%s", event.Id, event.Object.Order.Id))
		if err := service.RecordUnmatchedPaymentEvent(normalizedEvent, "creem_request_id_missing"); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 缺少订单号事件留存失败 event_id=%s error=%q", event.Id, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
		return
	}

	// Try complete subscription order first
	LockOrder(referenceId)
	defer UnlockOrder(referenceId)
	var paidAmountMinor *int64
	if event.Object.Order.AmountPaid > 0 {
		amount := int64(event.Object.Order.AmountPaid)
		paidAmountMinor = &amount
	}
	if err := model.CompleteSubscriptionOrderVerified(referenceId, model.SubscriptionPaymentConfirmation{
		ProviderPayload:         common.GetJsonString(event),
		ExpectedPaymentProvider: model.PaymentProviderCreem,
		ActualPaymentMethod:     model.PaymentMethodCreem,
		PaidAmountMinor:         paidAmountMinor,
		Currency:                event.Object.Order.Currency,
		ProviderOrderId:         event.Object.Order.Id,
	}); err == nil {
		if err := service.MarkVerifiedRetainedPaymentWebhookProcessed(normalizedEvent); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 订阅事件完成状态持久化失败 trade_no=%s event_id=%s error=%q", referenceId, event.Id, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem 订阅订单处理成功 trade_no=%s creem_order_id=%s", referenceId, event.Object.Order.Id))
		c.Status(http.StatusOK)
		return
	} else if err != nil && !errors.Is(err, model.ErrSubscriptionOrderNotFound) {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 订阅订单处理失败 trade_no=%s creem_order_id=%s error=%q", referenceId, event.Object.Order.Id, err.Error()))
		if errors.Is(err, model.ErrSubscriptionOrderSnapshotMissing) ||
			errors.Is(err, model.ErrSubscriptionOrderManualReview) ||
			errors.Is(err, model.ErrSubscriptionPaymentAmountRequired) ||
			errors.Is(err, model.ErrSubscriptionPaymentAmountMismatch) ||
			errors.Is(err, model.ErrSubscriptionPaymentCurrencyMismatch) ||
			errors.Is(err, model.ErrSubscriptionProviderOrderRequired) ||
			errors.Is(err, model.ErrPaymentMethodMismatch) {
			if recordErr := service.RecordUnmatchedPaymentEvent(normalizedEvent, "creem_subscription_settlement_manual_review"); recordErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 订阅人工核对事件留存失败 trade_no=%s event_id=%s error=%q", referenceId, event.Id, recordErr.Error()))
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			c.Status(http.StatusOK)
			return
		}
		_ = service.MarkVerifiedPaymentWebhookValidationFailed(normalizedEvent, "creem_subscription_settlement_failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// 验证订单类型，目前只处理一次性付款（充值）
	if event.Object.Order.Type != "onetime" {
		if err := service.MarkVerifiedRetainedPaymentWebhookProcessed(normalizedEvent); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 未支持订单类型事件状态持久化失败 trade_no=%s event_id=%s error=%q", referenceId, event.Id, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem 暂不支持该订单类型，忽略处理 request_id=%s creem_order_id=%s order_type=%s", referenceId, event.Object.Order.Id, event.Object.Order.Type))
		c.Status(http.StatusOK)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem 支付完成回调 trade_no=%s creem_order_id=%s amount_paid=%d currency=%s", referenceId, event.Object.Order.Id, event.Object.Order.AmountPaid, event.Object.Order.Currency))

	// 查询本地订单确认存在
	topUp := model.GetTopUpByTradeNo(referenceId)
	if topUp == nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem 充值订单不存在 trade_no=%s creem_order_id=%s", referenceId, event.Object.Order.Id))
		if err := service.RecordUnmatchedPaymentEvent(normalizedEvent, "creem_topup_order_not_found"); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 未匹配订单事件留存失败 trade_no=%s event_id=%s error=%q", referenceId, event.Id, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
		return
	}

	err := model.RechargeCreem(referenceId, model.TopUpPaymentConfirmation{
		PaidAmountMinor: paidAmountMinor,
		Currency:        event.Object.Order.Currency,
		ProviderOrderId: event.Object.Order.Id,
	}, c.ClientIP())
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 充值处理失败 trade_no=%s creem_order_id=%s client_ip=%s error=%q", referenceId, event.Object.Order.Id, c.ClientIP(), err.Error()))
		if model.IsTopUpPaymentReviewError(err) {
			if recordErr := service.RecordUnmatchedPaymentEvent(normalizedEvent, "creem_topup_settlement_manual_review"); recordErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 充值人工核对事件留存失败 trade_no=%s event_id=%s error=%q", referenceId, event.Id, recordErr.Error()))
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			c.Status(http.StatusOK)
			return
		}
		_ = service.MarkVerifiedPaymentWebhookValidationFailed(normalizedEvent, "creem_topup_settlement_failed")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err := service.MarkVerifiedRetainedPaymentWebhookProcessed(normalizedEvent); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 充值事件完成状态持久化失败 trade_no=%s event_id=%s error=%q", referenceId, event.Id, err.Error()))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem 充值成功 trade_no=%s creem_order_id=%s quota=%d money=%.2f client_ip=%s", referenceId, event.Object.Order.Id, topUp.Amount, topUp.Money, c.ClientIP()))
	c.Status(http.StatusOK)
}
