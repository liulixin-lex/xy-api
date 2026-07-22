package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

const (
	publicCheckoutModeQuote   = "quote"
	publicCheckoutModeProduct = "product"
	publicCheckoutModeOption  = "option"
	publicCheckoutModeDirect  = "direct"
)

type publicTopUpRouteView struct {
	RouteID      string `json:"route_id"`
	PublicMethod string `json:"public_method"`
	ChannelAlias string `json:"channel_alias,omitempty"`
	CheckoutMode string `json:"checkout_mode"`
	Currency     string `json:"currency,omitempty"`
	MinimumTopUp int64  `json:"min_topup,omitempty"`
}

type publicTopUpProductView struct {
	ProductID     string `json:"product_id"`
	RouteID       string `json:"route_id"`
	Name          string `json:"name"`
	PaymentAmount string `json:"payment_amount"`
	Currency      string `json:"currency"`
	TopUpAmount   int64  `json:"top_up_amount"`
}

type publicTopUpRouteOptionView struct {
	OptionID    string `json:"option_id"`
	RouteID     string `json:"route_id"`
	PublicLabel string `json:"public_label"`
}

// selectPublicTopUpRoutesForUser keeps provider fallback internal while an
// ordinary user sees one choice for each Alipay/WeChat brand. Selection happens
// after provider readiness checks, so an unavailable first provider cannot hide
// a later healthy route. WeChat browser checkout is preferred only where JSAPI
// can actually run; outside WeChat those routes are omitted.
func selectPublicTopUpRoutesForUser(routes []publicTopUpRouteView, userAgent string) []publicTopUpRouteView {
	preferredWeChatRouteID := ""
	isWeChatBrowser := strings.Contains(strings.ToLower(userAgent), "micromessenger")
	for _, route := range routes {
		if route.PublicMethod != "wechat_pay" {
			continue
		}
		isJSAPI := route.ChannelAlias == "wechat_browser" || route.ChannelAlias == "jsapi"
		if isWeChatBrowser {
			if isJSAPI {
				preferredWeChatRouteID = route.RouteID
				break
			}
			if preferredWeChatRouteID == "" {
				preferredWeChatRouteID = route.RouteID
			}
			continue
		}
		if !isJSAPI && preferredWeChatRouteID == "" {
			preferredWeChatRouteID = route.RouteID
		}
	}

	selected := make([]publicTopUpRouteView, 0, len(routes))
	alipaySelected := false
	for _, route := range routes {
		switch route.PublicMethod {
		case "alipay":
			if alipaySelected {
				continue
			}
			alipaySelected = true
		case "wechat_pay":
			if route.RouteID != preferredWeChatRouteID {
				continue
			}
		}
		selected = append(selected, route)
	}
	return selected
}

func GetTopUpInfo(c *gin.Context) {
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("用户支付方式同步失败 user_id=%d error=%q", c.GetInt("id"), err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	defer unlockPaymentConfiguration()
	publicRoutes, err := service.PublicPaymentRoutesLocked()
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("用户支付方式同步失败 user_id=%d error=%q", c.GetInt("id"), err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}
	complianceConfirmed := isPaymentComplianceConfirmedLocked()
	credentialStorageReady := model.PaymentSecretStorageReady()
	enableEpay := credentialStorageReady && isEpayTopUpEnabledLocked()
	enableStripe := credentialStorageReady && isStripeTopUpEnabledLocked()
	enableXorPay := credentialStorageReady && isXorPayTopUpEnabledLocked()
	enableCreem := credentialStorageReady && isCreemTopUpEnabledLocked()
	enableWaffo := credentialStorageReady && isWaffoTopUpEnabledLocked()
	enableWaffoPancake := credentialStorageReady && isWaffoPancakeTopUpEnabledLocked()
	var creemProducts []CreemProduct
	if enableCreem {
		var parseErr error
		creemProducts, parseErr = parseValidatedCreemProducts(setting.CreemProducts)
		if parseErr != nil {
			enableCreem = false
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("固定产品支付配置无效 user_id=%d error=%q", c.GetInt("id"), parseErr.Error()))
		}
	}

	paymentRoutes := make([]publicTopUpRouteView, 0, len(publicRoutes)+3)
	subscriptionPaymentRoutes := make([]publicTopUpRouteView, 0, len(publicRoutes))
	paymentProducts := make([]publicTopUpProductView, 0)
	paymentRouteOptions := make([]publicTopUpRouteOptionView, 0)
	catalogRoutesByID := make(map[string]service.PublicPaymentRoute, len(publicRoutes))
	for _, route := range publicRoutes {
		if service.ValidatePaymentRouteForOrderKind(
			route.Provider, route.PaymentMethod, model.PaymentOrderKindSubscription,
		) == nil {
			subscriptionCheckoutMode := publicCheckoutModeQuote
			subscriptionCurrency := route.Currency
			subscriptionEligible := true
			switch route.Provider {
			case model.PaymentProviderEpay:
				subscriptionCurrency = "CNY"
			case model.PaymentProviderStripe:
				subscriptionCurrency = strings.ToUpper(setting.StripeCurrency)
			case model.PaymentProviderXorPay:
				subscriptionCurrency = strings.ToUpper(setting.XorPayCurrency)
			case model.PaymentProviderCreem:
				subscriptionEligible = route.PaymentMethod == model.PaymentMethodCreem
				subscriptionCheckoutMode = publicCheckoutModeProduct
			case model.PaymentProviderWaffoPancake:
				subscriptionEligible = route.PaymentMethod == model.PaymentMethodWaffoPancake
				subscriptionCheckoutMode = publicCheckoutModeDirect
				subscriptionCurrency = "USD"
			default:
				// Waffo's option checkout has no subscription pricing contract.
				subscriptionEligible = false
			}
			if subscriptionEligible {
				subscriptionPaymentRoutes = append(subscriptionPaymentRoutes, publicTopUpRouteView{
					RouteID: route.RouteID, PublicMethod: route.PublicMethod, ChannelAlias: route.ChannelAlias,
					CheckoutMode: subscriptionCheckoutMode, Currency: subscriptionCurrency,
				})
			}
		}
		if service.ValidatePaymentRouteForOrderKind(
			route.Provider, route.PaymentMethod, model.PaymentOrderKindTopUp,
		) != nil {
			continue
		}

		checkoutMode := publicCheckoutModeQuote
		switch route.Provider {
		case model.PaymentProviderEpay:
			if !enableEpay {
				continue
			}
			route.Currency = "CNY"
		case model.PaymentProviderStripe:
			if !enableStripe {
				continue
			}
			route.Currency = strings.ToUpper(setting.StripeCurrency)
		case model.PaymentProviderXorPay:
			if !enableXorPay {
				continue
			}
			upstreamMethod := ""
			switch route.PaymentMethod {
			case model.PaymentMethodXorPayNative:
				upstreamMethod = setting.XorPayMethodNative
			case model.PaymentMethodXorPayAlipay:
				upstreamMethod = setting.XorPayMethodAlipay
			case model.PaymentMethodXorPayJSAPI:
				upstreamMethod = setting.XorPayMethodJSAPI
			}
			if upstreamMethod == "" || !setting.IsXorPayMethodEnabled(upstreamMethod) {
				continue
			}
			route.Currency = strings.ToUpper(setting.XorPayCurrency)
		case model.PaymentProviderCreem:
			if !enableCreem || route.PaymentMethod != model.PaymentMethodCreem {
				continue
			}
			checkoutMode = publicCheckoutModeProduct
		case model.PaymentProviderWaffo:
			if !enableWaffo || route.PaymentMethod != model.PaymentMethodWaffo {
				continue
			}
			checkoutMode = publicCheckoutModeOption
			route.Currency = strings.ToUpper(strings.TrimSpace(getWaffoCurrency()))
		case model.PaymentProviderWaffoPancake:
			if !enableWaffoPancake || route.PaymentMethod != model.PaymentMethodWaffoPancake {
				continue
			}
			checkoutMode = publicCheckoutModeDirect
			route.Currency = "USD"
		default:
			continue
		}
		if minimum, err := service.EffectivePaymentMethodMinimum(route.Provider, route.PaymentMethod); err == nil {
			route.MinimumTopUp = strconv.FormatInt(minimum, 10)
		}
		minimumTopUp, _ := strconv.ParseInt(route.MinimumTopUp, 10, 64)
		paymentRoutes = append(paymentRoutes, publicTopUpRouteView{
			RouteID:      route.RouteID,
			PublicMethod: route.PublicMethod,
			ChannelAlias: route.ChannelAlias,
			CheckoutMode: checkoutMode,
			Currency:     route.Currency,
			MinimumTopUp: minimumTopUp,
		})
		catalogRoutesByID[route.RouteID] = route
	}
	if !complianceConfirmed {
		paymentRoutes = []publicTopUpRouteView{}
		subscriptionPaymentRoutes = []publicTopUpRouteView{}
	} else {
		paymentRoutes = selectPublicTopUpRoutesForUser(paymentRoutes, c.Request.UserAgent())
		subscriptionPaymentRoutes = selectPublicTopUpRoutesForUser(subscriptionPaymentRoutes, c.Request.UserAgent())
	}
	activeCatalogRoutes := make(map[string]service.PublicPaymentRoute, len(paymentRoutes))
	for _, routeView := range paymentRoutes {
		route, exists := catalogRoutesByID[routeView.RouteID]
		if !exists {
			continue
		}
		activeCatalogRoutes[route.Provider+"\x00"+route.PaymentMethod] = route
	}

	// Retained product and option selectors can only attach to an active route
	// from the canonical PayMethods catalog. Real provider IDs and configuration
	// indexes remain behind separate opaque selector namespaces.
	if route, ok := activeCatalogRoutes[model.PaymentProviderCreem+"\x00"+model.PaymentMethodCreem]; ok {
		for _, product := range creemProducts {
			amountMinor, amountErr := model.ProviderPaymentAmountToMinor(
				product.Price,
				model.PaymentProviderCreem,
				product.Currency,
			)
			if amountErr != nil || amountMinor <= 0 {
				continue
			}
			paymentProducts = append(paymentProducts, publicTopUpProductView{
				ProductID: service.PublicRetainedProductID(
					model.PaymentProviderCreem,
					product.ProductId,
				),
				RouteID:       route.RouteID,
				Name:          service.PublicPaymentLabel(product.Name, "Online payment"),
				PaymentAmount: formatPublicPaymentAmount(amountMinor, product.Price, model.PaymentProviderCreem, product.Currency),
				Currency:      product.Currency,
				TopUpAmount:   product.Quota,
			})
		}
	}

	if route, ok := activeCatalogRoutes[model.PaymentProviderWaffo+"\x00"+model.PaymentMethodWaffo]; ok {
		for _, method := range setting.GetWaffoPayMethods() {
			optionID := service.PublicRetainedOptionID(
				model.PaymentProviderWaffo,
				waffoPayMethodIdentity(method.PayMethodType, method.PayMethodName),
			)
			if optionID == "" {
				continue
			}
			paymentRouteOptions = append(paymentRouteOptions, publicTopUpRouteOptionView{
				OptionID:    optionID,
				RouteID:     route.RouteID,
				PublicLabel: publicWaffoPayMethodLabel(method.PayMethodType, method.PayMethodName),
			})
		}
	}

	data := gin.H{
		"online_payment_available":         len(paymentRoutes) > 0,
		"enable_redemption":                complianceConfirmed,
		"payment_compliance_confirmed":     complianceConfirmed,
		"payment_compliance_terms_version": operation_setting.CurrentComplianceTermsVersion,
		"payment_routes":                   paymentRoutes,
		"subscription_payment_routes":      subscriptionPaymentRoutes,
		"payment_products":                 paymentProducts,
		"payment_route_options":            paymentRouteOptions,
		"min_topup":                        operation_setting.MinTopUp,
		"amount_options":                   operation_setting.GetPaymentSetting().AmountOptions,
		"discount":                         operation_setting.GetPaymentSetting().AmountDiscount,
		"affiliate_continuous_percent":     operation_setting.GetPaymentSetting().AffiliateContinuousPercent,
		"affiliate_first_topup_percent":    operation_setting.GetPaymentSetting().AffiliateFirstTopupPercent,
		"topup_link":                       common.TopUpLink,
	}
	common.ApiSuccess(c, data)
}

type EpayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
	RequestID     string `json:"request_id,omitempty"`
}

type AmountRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method,omitempty"`
}

type publicTopUpHistoryView struct {
	ID            int    `json:"id"`
	Amount        int64  `json:"amount"`
	PaymentAmount string `json:"payment_amount"`
	TradeNo       string `json:"trade_no"`
	RouteID       string `json:"route_id"`
	PublicMethod  string `json:"public_method"`
	ChannelAlias  string `json:"channel_alias"`
	Currency      string `json:"currency,omitempty"`
	StatusCode    string `json:"status_code"`
	CreatedAt     int64  `json:"created_at"`
	CompletedAt   int64  `json:"completed_at,omitempty"`
}

type adminTopUpHistoryView struct {
	ID                    int     `json:"id"`
	PaymentOrderID        *int64  `json:"payment_order_id,omitempty"`
	UserID                int     `json:"user_id"`
	Amount                int64   `json:"amount"`
	Money                 float64 `json:"money"`
	TradeNo               string  `json:"trade_no"`
	PaymentMethod         string  `json:"payment_method"`
	PaymentProvider       string  `json:"payment_provider"`
	Currency              string  `json:"currency,omitempty"`
	ExpectedAmountMinor   int64   `json:"expected_amount_minor,omitempty"`
	ProviderOrderID       string  `json:"provider_order_id,omitempty"`
	ReviewReason          string  `json:"review_reason,omitempty"`
	CreateTime            int64   `json:"create_time"`
	CompleteTime          int64   `json:"complete_time"`
	Status                string  `json:"status"`
	Provider              string  `json:"provider,omitempty"`
	OrderKind             string  `json:"order_kind,omitempty"`
	CreditQuota           int64   `json:"credit_quota,omitempty"`
	PaidAmountMinor       int64   `json:"paid_amount_minor,omitempty"`
	RefundedAmountMinor   int64   `json:"refunded_amount_minor,omitempty"`
	DisputedAmountMinor   int64   `json:"disputed_amount_minor,omitempty"`
	ReversedAmountMinor   int64   `json:"reversed_amount_minor,omitempty"`
	CanonicalOrderVersion int64   `json:"canonical_order_version,omitempty"`
	StatusReason          string  `json:"status_reason,omitempty"`
}

func adminTopUpHistory(topUp *model.TopUp) adminTopUpHistoryView {
	if topUp == nil {
		return adminTopUpHistoryView{}
	}
	return adminTopUpHistoryView{
		ID: topUp.Id, PaymentOrderID: topUp.PaymentOrderId, UserID: topUp.UserId,
		Amount: topUp.Amount, Money: topUp.Money, TradeNo: topUp.TradeNo,
		PaymentMethod: topUp.PaymentMethod, PaymentProvider: topUp.PaymentProvider,
		Currency: topUp.Currency, ExpectedAmountMinor: topUp.ExpectedAmountMinor,
		ProviderOrderID: topUp.ProviderOrderId, ReviewReason: topUp.ReviewReason,
		CreateTime: topUp.CreateTime, CompleteTime: topUp.CompleteTime, Status: topUp.Status,
		Provider: topUp.Provider, OrderKind: topUp.OrderKind, CreditQuota: topUp.CreditQuota,
		PaidAmountMinor: topUp.PaidAmountMinor, RefundedAmountMinor: topUp.RefundedAmountMinor,
		DisputedAmountMinor: topUp.DisputedAmountMinor, ReversedAmountMinor: topUp.ReversedAmountMinor,
		CanonicalOrderVersion: topUp.CanonicalOrderVersion, StatusReason: topUp.StatusReason,
	}
}

func RequestEpay(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req EpayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		legacyPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}
	start, err := startLegacyTopUpPayment(c, model.PaymentProviderEpay, req.PaymentMethod, req.Amount, req.RequestID)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 拉起支付失败 user_id=%d payment_method=%s amount=%d error=%q", c.GetInt("id"), req.PaymentMethod, req.Amount, err.Error()))
		legacyPaymentServiceAPIError(c, err)
		return
	}
	if start == nil || strings.TrimSpace(start.TradeNo) == "" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付返回无效本地订单 user_id=%d", c.GetInt("id")))
		legacyPaymentAPIError(c, "payment_not_ready", nil)
		return
	}
	// Compatibility clients still submit a form to this URL, but provider
	// fields and destinations remain behind the authenticated continuation
	// endpoint instead of crossing the user JSON boundary.
	c.JSON(http.StatusOK, gin.H{
		"message":  "success",
		"data":     gin.H{"trade_no": start.TradeNo},
		"url":      legacyPaymentFormBridgeURL(start.TradeNo),
		"trade_no": start.TradeNo,
	})
}

// tradeNo lock
var orderLocks sync.Map
var createLock sync.Mutex

// refCountedMutex 带引用计数的互斥锁，确保最后一个使用者才从 map 中删除
type refCountedMutex struct {
	mu       sync.Mutex
	refCount int
}

// LockOrder 尝试对给定订单号加锁
func LockOrder(tradeNo string) {
	createLock.Lock()
	var rcm *refCountedMutex
	if v, ok := orderLocks.Load(tradeNo); ok {
		rcm = v.(*refCountedMutex)
	} else {
		rcm = &refCountedMutex{}
		orderLocks.Store(tradeNo, rcm)
	}
	rcm.refCount++
	createLock.Unlock()
	rcm.mu.Lock()
}

// UnlockOrder 释放给定订单号的锁
func UnlockOrder(tradeNo string) {
	v, ok := orderLocks.Load(tradeNo)
	if !ok {
		return
	}
	rcm := v.(*refCountedMutex)
	rcm.mu.Unlock()

	createLock.Lock()
	rcm.refCount--
	if rcm.refCount == 0 {
		orderLocks.Delete(tradeNo)
	}
	createLock.Unlock()
}

func EpayNotify(c *gin.Context) {
	PaymentEpayNotify(c)
}

func RequestAmount(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req AmountRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		legacyPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}

	paymentMethod := strings.TrimSpace(req.PaymentMethod)
	if paymentMethod == "" {
		var selectedMinimum int64
		for _, configured := range operation_setting.PayMethods {
			provider := strings.TrimSpace(configured["provider"])
			if provider == "" {
				provider = model.PaymentProviderEpay
			}
			method := strings.TrimSpace(configured["type"])
			if provider != model.PaymentProviderEpay || method == "" {
				continue
			}
			minimum, err := service.EffectivePaymentMethodMinimum(model.PaymentProviderEpay, method)
			if err == nil && (paymentMethod == "" || minimum < selectedMinimum) {
				paymentMethod = method
				selectedMinimum = minimum
			}
		}
	}
	quote, err := service.PreviewPaymentQuote(c.Request.Context(), c.GetInt("id"), service.PaymentQuoteRequest{
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderEpay,
		PaymentMethod: paymentMethod, Amount: req.Amount,
	})
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付报价失败 user_id=%d payment_method=%s amount=%d error=%q", c.GetInt("id"), paymentMethod, req.Amount, err.Error()))
		legacyPaymentServiceAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": quote.PayableAmount})
}

func GetUserTopUps(c *gin.Context) {
	userId := c.GetInt("id")
	pageInfo := common.GetPageQuery(c)
	keyword := c.Query("keyword")

	var (
		topups []*model.TopUp
		total  int64
		err    error
	)
	if keyword != "" {
		topups, total, err = model.SearchUserTopUps(userId, keyword, pageInfo)
	} else {
		topups, total, err = model.GetUserTopUps(userId, pageInfo)
	}
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("用户充值记录查询失败 user_id=%d error=%q", userId, err.Error()))
		compatibilityPaymentAPIError(c, "payment_temporarily_unavailable", nil)
		return
	}

	items := make([]publicTopUpHistoryView, 0, len(topups))
	for _, topUp := range topups {
		if topUp == nil {
			continue
		}
		provider := topUp.Provider
		if provider == "" {
			provider = topUp.PaymentProvider
		}
		if provider == "" {
			provider = model.PaymentProviderEpay
		}
		route := service.PublicPaymentRouteForOrder(provider, topUp.PaymentMethod)
		items = append(items, publicTopUpHistoryView{
			ID: topUp.Id, Amount: topUp.Amount,
			PaymentAmount: formatPublicPaymentAmount(topUp.ExpectedAmountMinor, topUp.Money, provider, topUp.Currency),
			TradeNo:       topUp.TradeNo, RouteID: route.RouteID, PublicMethod: route.PublicMethod,
			ChannelAlias: route.ChannelAlias, Currency: topUp.Currency,
			StatusCode: topUpPublicStatusCode(topUp.Status), CreatedAt: topUp.CreateTime,
			CompletedAt: topUp.CompleteTime,
		})
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func topUpPublicStatusCode(status string) string {
	switch status {
	case common.TopUpStatusSuccess, model.PaymentOrderStatusPaid, model.PaymentOrderStatusFulfilled:
		return "succeeded"
	case common.TopUpStatusPending:
		return "awaiting_payment"
	case model.PaymentOrderStatusProcessing:
		return "preparing"
	case model.PaymentOrderStatusExpired:
		return "expired"
	default:
		return "temporarily_unavailable"
	}
}

// GetAllTopUps 管理员获取全平台充值记录
func GetAllTopUps(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	keyword := c.Query("keyword")

	var (
		topups []*model.TopUp
		total  int64
		err    error
	)
	if keyword != "" {
		topups, total, err = model.SearchAllTopUps(keyword, pageInfo)
	} else {
		topups, total, err = model.GetAllTopUps(pageInfo)
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}

	items := make([]adminTopUpHistoryView, 0, len(topups))
	for _, topUp := range topups {
		if topUp != nil {
			items = append(items, adminTopUpHistory(topUp))
		}
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

type AdminCompleteTopupRequest struct {
	TradeNo         string `json:"trade_no"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}

// AdminCompleteTopUp 管理员补单接口
func AdminCompleteTopUp(c *gin.Context) {
	if c.GetBool("use_access_token") {
		common.ApiErrorMsg(c, "Manual payment completion requires dashboard session authentication")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req AdminCompleteTopupRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil || strings.TrimSpace(req.TradeNo) == "" {
		common.ApiErrorMsg(c, "Invalid manual payment resolution")
		return
	}
	result, err := model.ResolveManualPaymentOrderByAdmin(req.TradeNo, req.ExpectedVersion, c.GetInt("id"), c.ClientIP(), req.Reason)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "payment.order.manual_fulfill", map[string]interface{}{
		"trade_no": strings.TrimSpace(req.TradeNo), "expected_version": req.ExpectedVersion, "compatibility_endpoint": true,
	})
	common.ApiSuccess(c, result.Order)
}
