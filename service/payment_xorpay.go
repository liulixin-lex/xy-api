package service

import (
	"context"
	"crypto/md5"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
)

const (
	xorPayBaseURL          = "https://xorpay.com"
	xorPayMaxResponseBytes = 128 << 10
	xorPayDefaultExpiry    = int64(7200)
)

var xorPayAidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type xorPayProvider struct {
	client *http.Client
}

type xorPayCredential struct {
	aid        string
	secret     string
	generation int64
}

type xorPayCreateResponse struct {
	Status    string `json:"status"`
	AOID      string `json:"aoid"`
	ExpiresIn int64  `json:"expires_in"`
	Info      struct {
		QR string `json:"qr"`
	} `json:"info"`
}

type xorPayQueryResponse struct {
	Status string `json:"status"`
}

func init() {
	RegisterPaymentProvider(&xorPayProvider{client: newXorPayHTTPClient()})
}

func (*xorPayProvider) Name() string { return model.PaymentProviderXorPay }

func (*xorPayProvider) CredentialGeneration() int64 {
	return setting.XorPayCredentialGeneration
}

func (*xorPayProvider) ValidateMethod(method string) error {
	upstreamMethod, err := xorPayUpstreamMethod(method)
	if err != nil {
		return err
	}
	if !setting.IsXorPayMethodEnabled(upstreamMethod) {
		return fmt.Errorf("xorpay method is disabled: %s", method)
	}
	return nil
}

func (p *xorPayProvider) Create(ctx context.Context, order *model.PaymentOrder) (*PaymentStart, error) {
	if order == nil {
		return nil, errors.New("payment order is required")
	}
	credential, err := xorPayCredentialForOrder(order)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(order.Currency), "CNY") || !strings.EqualFold(strings.TrimSpace(setting.XorPayCurrency), "CNY") {
		return nil, errors.New("xorpay currency must be CNY")
	}
	upstreamMethod, err := xorPayUpstreamMethod(order.PaymentMethod)
	if err != nil {
		return nil, err
	}
	if !setting.IsXorPayMethodEnabled(upstreamMethod) {
		return nil, errors.New("xorpay payment method is disabled")
	}
	name := "AI API top-up"
	if order.OrderKind == model.PaymentOrderKindSubscription {
		name = "AI API subscription"
	}
	price := formatPaymentMinor(order.ExpectedAmountMinor, common.PaymentCurrencyExponent(order.Currency))
	callbackAddress := strings.TrimRight(GetPaymentCallbackAddress(), "/")
	if err := ValidatePaymentCallbackOrigin(callbackAddress, true); err != nil {
		return nil, err
	}
	notifyURL := callbackAddress + "/api/xorpay/notify"
	if err := ValidateExternalPaymentURL(notifyURL, true); err != nil {
		return nil, err
	}
	form := url.Values{
		"name":       {name},
		"pay_type":   {upstreamMethod},
		"price":      {price},
		"order_id":   {order.TradeNo},
		"notify_url": {notifyURL},
	}
	form.Set("sign", xorPayMD5(name+upstreamMethod+price+order.TradeNo+notifyURL+credential.secret))
	endpoint := xorPayBaseURL + "/api/pay/" + url.PathEscape(credential.aid)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPaymentStateUnknown, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: xorpay returned HTTP %d", ErrPaymentStateUnknown, response.StatusCode)
	}
	var result xorPayCreateResponse
	if err := common.DecodeJson(io.LimitReader(response.Body, xorPayMaxResponseBytes), &result); err != nil {
		return nil, fmt.Errorf("%w: invalid xorpay response", ErrPaymentStateUnknown)
	}
	if result.Status != "ok" {
		return nil, xorPayCreateError(result.Status)
	}
	result.AOID = strings.TrimSpace(result.AOID)
	if !xorPayAidPattern.MatchString(result.AOID) {
		return nil, fmt.Errorf("%w: xorpay response contains an invalid aoid", ErrPaymentStateUnknown)
	}
	if err := validateXorPayQR(upstreamMethod, result.Info.QR); err != nil {
		return nil, err
	}
	if result.ExpiresIn <= 0 || result.ExpiresIn > 24*60*60 {
		result.ExpiresIn = xorPayDefaultExpiry
	}
	expiresAt := time.Now().Unix() + result.ExpiresIn
	providerOrderKey := "xorpay:" + result.AOID
	return &PaymentStart{
		Flow: PaymentFlowQR, QRContent: result.Info.QR, ExpiresAt: expiresAt,
		ProviderOrderKey: providerOrderKey,
	}, nil
}

func xorPayCredentialForOrder(order *model.PaymentOrder) (xorPayCredential, error) {
	if order == nil {
		return xorPayCredential{}, errors.New("payment order is required")
	}
	generation := order.ProviderCredentialGeneration
	if generation == 0 || generation == setting.XorPayCredentialGeneration {
		if xorPayAidPattern.MatchString(setting.XorPayAid) && strings.TrimSpace(setting.XorPayAppSecret) != "" {
			return xorPayCredential{
				aid: setting.XorPayAid, secret: setting.XorPayAppSecret, generation: setting.XorPayCredentialGeneration,
			}, nil
		}
	}
	if generation == setting.XorPayPreviousCredentialGeneration && setting.XorPayPreviousCredentialActive() &&
		order.CreatedAt > 0 && order.CreatedAt <= setting.XorPayPreviousValidBefore &&
		xorPayAidPattern.MatchString(setting.XorPayAidPrevious) && strings.TrimSpace(setting.XorPayAppSecretPrevious) != "" {
		return xorPayCredential{
			aid: setting.XorPayAidPrevious, secret: setting.XorPayAppSecretPrevious,
			generation: setting.XorPayPreviousCredentialGeneration,
		}, nil
	}
	return xorPayCredential{}, errors.New("xorpay credential generation for this order is no longer available")
}

func (*xorPayProvider) VerifyWebhook(request *http.Request) (*NormalizedPaymentEvent, error) {
	if request == nil || request.Method != http.MethodPost {
		return nil, errors.New("xorpay callback must use POST")
	}
	currentConfigured := xorPayAidPattern.MatchString(setting.XorPayAid) && strings.TrimSpace(setting.XorPayAppSecret) != ""
	if !currentConfigured && !setting.XorPayPreviousCredentialActive() {
		return nil, errors.New("xorpay webhook is not configured")
	}
	if err := request.ParseForm(); err != nil {
		return nil, err
	}
	params := make(map[string]string, len(request.PostForm))
	for key, values := range request.PostForm {
		if len(values) != 1 || len(values[0]) > 2048 {
			return nil, fmt.Errorf("invalid xorpay parameter: %s", key)
		}
		params[key] = values[0]
	}
	aoid := strings.TrimSpace(params["aoid"])
	tradeNo := strings.TrimSpace(params["order_id"])
	price := strings.TrimSpace(params["pay_price"])
	payTime := strings.TrimSpace(params["pay_time"])
	sign := strings.ToLower(strings.TrimSpace(params["sign"]))
	if aoid == "" || tradeNo == "" || price == "" || payTime == "" || sign == "" {
		return nil, errors.New("xorpay callback is missing required fields")
	}
	if !xorPayAidPattern.MatchString(aoid) || len(tradeNo) > 128 || len(price) > 64 || len(payTime) > 64 {
		return nil, errors.New("xorpay callback contains invalid identifiers")
	}
	scope, err := resolvePaymentCredentialScope(model.PaymentProviderXorPay, tradeNo)
	if err != nil {
		return nil, err
	}
	credentials := make([]xorPayCredential, 0, 2)
	if currentConfigured && scope.allowsCurrent(setting.XorPayCredentialGeneration) {
		credentials = append(credentials, xorPayCredential{
			aid: setting.XorPayAid, secret: setting.XorPayAppSecret, generation: setting.XorPayCredentialGeneration,
		})
	}
	if setting.XorPayPreviousCredentialActive() && scope.allowsPrevious(
		setting.XorPayPreviousCredentialGeneration,
		setting.XorPayPreviousValidBefore,
	) {
		credentials = append(credentials, xorPayCredential{
			aid: setting.XorPayAidPrevious, secret: setting.XorPayAppSecretPrevious,
			generation: setting.XorPayPreviousCredentialGeneration,
		})
	}
	signatureMatched := false
	matchedGeneration := int64(0)
	for _, credential := range credentials {
		expected := xorPayMD5(aoid + tradeNo + price + payTime + credential.secret)
		if len(sign) == len(expected) && subtle.ConstantTimeCompare([]byte(sign), []byte(expected)) == 1 {
			signatureMatched = true
			matchedGeneration = credential.generation
			break
		}
	}
	if !signatureMatched {
		return nil, errors.New("xorpay signature mismatch")
	}
	currency := "CNY"
	if order, lookupErr := model.GetPaymentOrderByTradeNo(tradeNo); lookupErr == nil {
		if !strings.EqualFold(strings.TrimSpace(order.Currency), currency) {
			return nil, errors.New("xorpay order currency mismatch")
		}
	} else if !errors.Is(lookupErr, model.ErrPaymentOrderNotFound) {
		return nil, lookupErr
	}
	exponent, ok := common.PaymentCurrencyExponentOK(currency)
	if !ok {
		return nil, errors.New("unsupported xorpay currency")
	}
	minor, err := parsePaymentMinor(price, exponent)
	if err != nil {
		return nil, err
	}
	providerOrderKey := "xorpay:" + aoid
	normalized := common.GetJsonString(map[string]string{
		"aoid":      aoid,
		"order_id":  tradeNo,
		"pay_price": price,
		"pay_time":  payTime,
	})
	return &NormalizedPaymentEvent{
		Provider:                     model.PaymentProviderXorPay,
		EventKey:                     model.PaymentEventKey(model.PaymentProviderXorPay, "paid", providerOrderKey, tradeNo, normalized),
		EventType:                    "paid",
		TradeNo:                      tradeNo,
		ProviderOrderKey:             providerOrderKey,
		ProviderCredentialGeneration: matchedGeneration,
		PaidAmountMinor:              minor,
		Currency:                     currency,
		Paid:                         true,
		NormalizedPayload:            normalized,
	}, nil
}

func (p *xorPayProvider) Query(ctx context.Context, order *model.PaymentOrder) (*NormalizedPaymentEvent, error) {
	if order == nil || order.TradeNo == "" {
		return nil, errors.New("payment order is required")
	}
	if !strings.EqualFold(strings.TrimSpace(order.Currency), "CNY") || !strings.EqualFold(strings.TrimSpace(setting.XorPayCurrency), "CNY") {
		return nil, errors.New("xorpay currency must be CNY")
	}
	hasProviderOrderIdentity := order.ProviderOrderKey != nil && strings.HasPrefix(*order.ProviderOrderKey, "xorpay:")
	currentConfigured := xorPayAidPattern.MatchString(setting.XorPayAid) && strings.TrimSpace(setting.XorPayAppSecret) != ""
	if !hasProviderOrderIdentity && !currentConfigured && !setting.XorPayPreviousCredentialActive() {
		return nil, errors.New("xorpay is not configured")
	}
	if order.ProviderCredentialGeneration > 0 {
		available, err := model.PaymentCredentialGenerationAvailable(
			model.PaymentProviderXorPay, order.ProviderCredentialGeneration, order.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		if !available {
			if err := model.MarkPaymentOrderCredentialGenerationManualReview(order.TradeNo); err != nil && !errors.Is(err, model.ErrPaymentOrderNotFound) {
				return nil, err
			}
			return nil, model.ErrPaymentManualReview
		}
	} else if hasProviderOrderIdentity {
		// Query-by-AOID is intentionally credentialless. For an upgrade-era order
		// that never persisted its credential generation, it cannot prove which
		// merchant generation owns the order, so do not guess a settlement fence.
		return nil, model.ErrPaymentManualReview
	}
	status := ""
	queryType := "query2"
	matchedCredentialGeneration := order.ProviderCredentialGeneration
	if hasProviderOrderIdentity {
		aoid := strings.TrimPrefix(*order.ProviderOrderKey, "xorpay:")
		if !xorPayAidPattern.MatchString(aoid) {
			return nil, errors.New("xorpay provider order identity is invalid")
		}
		var err error
		status, err = p.queryStatus(ctx, xorPayBaseURL+"/api/query/"+url.PathEscape(aoid))
		if err != nil {
			return nil, err
		}
		queryType = "query"
	} else {
		credentials := make([]xorPayCredential, 0, 2)
		if (order.ProviderCredentialGeneration == 0 || order.ProviderCredentialGeneration == setting.XorPayCredentialGeneration) && currentConfigured {
			credentials = append(credentials, xorPayCredential{
				aid: setting.XorPayAid, secret: setting.XorPayAppSecret, generation: setting.XorPayCredentialGeneration,
			})
		}
		legacyPreviousEligible := order.ProviderCredentialGeneration == 0 && order.CreatedAt > 0 && order.CreatedAt <= setting.XorPayPreviousValidBefore
		if setting.XorPayPreviousCredentialActive() &&
			(order.ProviderCredentialGeneration == setting.XorPayPreviousCredentialGeneration || legacyPreviousEligible) {
			credentials = append(credentials, xorPayCredential{
				aid: setting.XorPayAidPrevious, secret: setting.XorPayAppSecretPrevious,
				generation: setting.XorPayPreviousCredentialGeneration,
			})
		}
		if len(credentials) == 0 {
			return nil, errors.New("xorpay credential generation for this order is no longer available")
		}
		for index, credential := range credentials {
			sign := xorPayMD5(order.TradeNo + credential.secret)
			endpoint := xorPayBaseURL + "/api/query2/" + url.PathEscape(credential.aid) + "?order_id=" + url.QueryEscape(order.TradeNo) + "&sign=" + url.QueryEscape(sign)
			candidateStatus, err := p.queryStatus(ctx, endpoint)
			if err != nil {
				return nil, err
			}
			status = candidateStatus
			if status != "not_exist" || index+1 == len(credentials) {
				matchedCredentialGeneration = credential.generation
				break
			}
		}
	}
	event := &NormalizedPaymentEvent{
		Provider:                     model.PaymentProviderXorPay,
		ProviderCredentialGeneration: matchedCredentialGeneration,
		EventType:                    queryType + ":" + status,
		TradeNo:                      order.TradeNo,
		PaidAmountMinor:              order.ExpectedAmountMinor,
		Currency:                     order.Currency,
		PaymentMethod:                order.PaymentMethod,
		NormalizedPayload:            common.GetJsonString(map[string]string{"status": status, "order_id": order.TradeNo}),
	}
	if order.ProviderOrderKey != nil {
		event.ProviderOrderKey = *order.ProviderOrderKey
	}
	switch status {
	case "payed", "success":
		event.Paid = true
	case "expire":
		event.Expired = true
		event.PermanentFailure = true
	case "fee_error":
		event.ManualReview = true
	case "new", "not_exist":
	default:
		return nil, fmt.Errorf("unknown xorpay query status: %s", status)
	}
	event.EventKey = model.PaymentEventKey(model.PaymentProviderXorPay, event.EventType, event.ProviderOrderKey, order.TradeNo, event.NormalizedPayload)
	return event, nil
}

func (p *xorPayProvider) queryStatus(ctx context.Context, endpoint string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("xorpay query returned HTTP %d", response.StatusCode)
	}
	var result xorPayQueryResponse
	if err := common.DecodeJson(io.LimitReader(response.Body, xorPayMaxResponseBytes), &result); err != nil {
		return "", errors.New("invalid xorpay query response")
	}
	switch result.Status {
	case "payed", "success", "expire", "fee_error", "new", "not_exist":
		return result.Status, nil
	default:
		return "", fmt.Errorf("unknown xorpay query status: %s", result.Status)
	}
}

func newXorPayHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   10,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("xorpay redirects are not allowed")
		},
	}
}

func xorPayUpstreamMethod(method string) (string, error) {
	switch method {
	case model.PaymentMethodXorPayNative:
		return setting.XorPayMethodNative, nil
	case model.PaymentMethodXorPayAlipay:
		return setting.XorPayMethodAlipay, nil
	default:
		return "", fmt.Errorf("unsupported xorpay payment method: %s", method)
	}
}

func xorPayMD5(value string) string {
	digest := md5.Sum([]byte(value))
	return fmt.Sprintf("%x", digest)
}

func xorPayCreateError(status string) error {
	switch status {
	case "order_payed", "order_expire", "order_exist":
		return fmt.Errorf("%w: xorpay create returned %s", ErrPaymentStateUnknown, status)
	case "fee_error":
		return fmt.Errorf("%w: xorpay create returned %s", ErrPaymentStateUnknown, status)
	case "missing_argument", "aid_not_exist", "pay_type_error", "sign_error", "app_off", "no_contract", "no_alipay_contract", "wechat_api_error", "alipay_api_error":
		return fmt.Errorf("xorpay create failed: %s", status)
	case "":
		return fmt.Errorf("%w: xorpay response is missing status", ErrPaymentStateUnknown)
	default:
		return fmt.Errorf("%w: unknown xorpay status", ErrPaymentStateUnknown)
	}
}

func validateXorPayQR(method, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 {
		return errors.New("invalid xorpay QR content")
	}
	if method == setting.XorPayMethodNative {
		if !strings.HasPrefix(value, "weixin://wxpay/") {
			return errors.New("unexpected xorpay native QR scheme")
		}
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || strings.ToLower(u.Hostname()) != "qr.alipay.com" {
		return errors.New("unexpected xorpay alipay QR URL")
	}
	return nil
}
