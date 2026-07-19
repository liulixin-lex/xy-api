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

func GetTopUpInfo(c *gin.Context) {
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		common.ApiErrorMsg(c, "Failed to synchronize payment configuration")
		return
	}
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	defer unlockPaymentConfiguration()
	complianceConfirmed := isPaymentComplianceConfirmedLocked()
	credentialStorageReady := model.PaymentSecretStorageReady()
	enableEpay := credentialStorageReady && isEpayTopUpEnabledLocked()
	enableStripe := credentialStorageReady && isStripeTopUpEnabledLocked()
	enableXorPay := credentialStorageReady && isXorPayTopUpEnabledLocked()

	// 获取支付方式
	payMethods := make([]map[string]string, 0, len(operation_setting.PayMethods)+3)
	for _, configured := range operation_setting.PayMethods {
		provider := configured["provider"]
		if provider == "" {
			provider = model.PaymentProviderEpay
		}
		switch provider {
		case model.PaymentProviderEpay:
			if !enableEpay {
				continue
			}
		case model.PaymentProviderStripe:
			if !enableStripe {
				continue
			}
		case model.PaymentProviderXorPay:
			if !enableXorPay {
				continue
			}
			upstreamMethod := ""
			switch configured["type"] {
			case model.PaymentMethodXorPayNative:
				upstreamMethod = setting.XorPayMethodNative
			case model.PaymentMethodXorPayAlipay:
				upstreamMethod = setting.XorPayMethodAlipay
			}
			if upstreamMethod == "" || !setting.IsXorPayMethodEnabled(upstreamMethod) {
				continue
			}
		case model.PaymentProviderWaffoPancake:
			if !isWaffoPancakeTopUpEnabledLocked() {
				continue
			}
		default:
			continue
		}
		method := make(map[string]string, len(configured))
		for key, value := range configured {
			method[key] = value
		}
		if provider == model.PaymentProviderEpay || provider == model.PaymentProviderStripe || provider == model.PaymentProviderXorPay {
			if minimum, err := service.EffectivePaymentMethodMinimum(provider, method["type"]); err == nil {
				method["min_topup"] = strconv.FormatInt(minimum, 10)
			}
		}
		payMethods = append(payMethods, method)
	}
	if !complianceConfirmed {
		payMethods = []map[string]string{}
	}

	// 如果启用了 Stripe 支付，添加到支付方法列表
	if enableStripe {
		// 检查是否已经包含 Stripe
		hasStripe := false
		for _, method := range payMethods {
			if method["provider"] == model.PaymentProviderStripe && method["type"] == model.PaymentMethodStripe {
				hasStripe = true
				break
			}
		}

		if !hasStripe {
			stripeMethod := map[string]string{
				"name":      "Stripe",
				"type":      model.PaymentMethodStripe,
				"provider":  model.PaymentProviderStripe,
				"flow":      service.PaymentFlowHostedRedirect,
				"currency":  strings.ToUpper(setting.StripeCurrency),
				"color":     "rgba(var(--semi-purple-5), 1)",
				"min_topup": strconv.Itoa(setting.StripeMinTopUp),
			}
			payMethods = append(payMethods, stripeMethod)
		}
	}

	if enableXorPay {
		hasNative := false
		hasAlipay := false
		for _, method := range payMethods {
			if method["provider"] == model.PaymentProviderXorPay && method["type"] == model.PaymentMethodXorPayNative {
				hasNative = true
			}
			if method["provider"] == model.PaymentProviderXorPay && method["type"] == model.PaymentMethodXorPayAlipay {
				hasAlipay = true
			}
		}
		if setting.IsXorPayMethodEnabled(setting.XorPayMethodNative) && !hasNative {
			payMethods = append(payMethods, map[string]string{
				"name": "XORPay WeChat Pay", "type": model.PaymentMethodXorPayNative,
				"provider": model.PaymentProviderXorPay, "flow": service.PaymentFlowQR,
				"currency": strings.ToUpper(setting.XorPayCurrency), "min_topup": strconv.Itoa(setting.XorPayMinTopUp),
			})
		}
		if setting.IsXorPayMethodEnabled(setting.XorPayMethodAlipay) && !hasAlipay {
			payMethods = append(payMethods, map[string]string{
				"name": "XORPay Alipay", "type": model.PaymentMethodXorPayAlipay,
				"provider": model.PaymentProviderXorPay, "flow": service.PaymentFlowQR,
				"currency": strings.ToUpper(setting.XorPayCurrency), "min_topup": strconv.Itoa(setting.XorPayMinTopUp),
			})
		}
	}

	// Waffo Pancake displayed above the legacy Waffo gateway.
	enableWaffoPancake := isWaffoPancakeTopUpEnabledLocked()
	if enableWaffoPancake {
		hasWaffoPancake := false
		for _, method := range payMethods {
			if method["type"] == model.PaymentMethodWaffoPancake {
				hasWaffoPancake = true
				break
			}
		}

		if !hasWaffoPancake {
			payMethods = append(payMethods, map[string]string{
				"name":      "Waffo Pancake",
				"type":      model.PaymentMethodWaffoPancake,
				"color":     "rgba(var(--semi-orange-5), 1)",
				"min_topup": strconv.Itoa(setting.WaffoPancakeMinTopUp),
			})
		}
	}

	// 如果启用了 Waffo 支付，添加到支付方法列表
	enableWaffo := isWaffoTopUpEnabledLocked()
	if enableWaffo {
		hasWaffo := false
		for _, method := range payMethods {
			if method["type"] == model.PaymentMethodWaffo {
				hasWaffo = true
				break
			}
		}

		if !hasWaffo {
			waffoMethod := map[string]string{
				"name":      "Waffo (Global Payment)",
				"type":      model.PaymentMethodWaffo,
				"color":     "rgba(var(--semi-blue-5), 1)",
				"min_topup": strconv.Itoa(setting.WaffoMinTopUp),
			}
			payMethods = append(payMethods, waffoMethod)
		}
	}

	data := gin.H{
		"enable_online_topup":              enableEpay,
		"enable_stripe_topup":              enableStripe,
		"enable_xorpay_topup":              enableXorPay,
		"enable_creem_topup":               isCreemTopUpEnabledLocked(),
		"enable_waffo_topup":               enableWaffo,
		"enable_waffo_pancake_topup":       enableWaffoPancake,
		"enable_redemption":                complianceConfirmed,
		"payment_compliance_confirmed":     complianceConfirmed,
		"payment_compliance_terms_version": operation_setting.CurrentComplianceTermsVersion,
		"waffo_pay_methods": func() interface{} {
			if enableWaffo {
				return setting.GetWaffoPayMethods()
			}
			return nil
		}(),
		"creem_products":                setting.CreemProducts,
		"pay_methods":                   payMethods,
		"min_topup":                     operation_setting.MinTopUp,
		"stripe_min_topup":              setting.StripeMinTopUp,
		"xorpay_min_topup":              setting.XorPayMinTopUp,
		"waffo_min_topup":               setting.WaffoMinTopUp,
		"waffo_pancake_min_topup":       setting.WaffoPancakeMinTopUp,
		"amount_options":                operation_setting.GetPaymentSetting().AmountOptions,
		"discount":                      operation_setting.GetPaymentSetting().AmountDiscount,
		"affiliate_continuous_percent":  operation_setting.GetPaymentSetting().AffiliateContinuousPercent,
		"affiliate_first_topup_percent": operation_setting.GetPaymentSetting().AffiliateFirstTopupPercent,
		"topup_link":                    common.TopUpLink,
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

func RequestEpay(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req EpayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	start, err := startLegacyTopUpPayment(c, model.PaymentProviderEpay, req.PaymentMethod, req.Amount, req.RequestID)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 拉起支付失败 user_id=%d payment_method=%s amount=%d error=%q", c.GetInt("id"), req.PaymentMethod, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}
	if start.Flow != service.PaymentFlowFormPost || start.Action == "" || len(start.Fields) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付网关返回了无效的支付表单"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": start.Fields, "url": start.Action, "trade_no": start.TradeNo})
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
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
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
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
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
		common.ApiError(c, err)
		return
	}

	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(topups)
	common.ApiSuccess(c, pageInfo)
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

	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(topups)
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
