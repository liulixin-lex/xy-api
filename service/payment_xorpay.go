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
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
)

const (
	xorPayBaseURL              = "https://xorpay.com"
	xorPayMaxResponseBytes     = 128 << 10
	xorPayMaxWebhookBytes      = 64 << 10
	xorPayMaxWebhookParameters = 32
	xorPayDefaultExpiry        = int64(7200)
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
	Status    string           `json:"status"`
	AOID      string           `json:"aoid"`
	ExpiresIn int64            `json:"expires_in"`
	ExpireIn  int64            `json:"expire_in"`
	Info      xorPayCreateInfo `json:"info"`
}

type xorPayCreateInfo struct {
	QR        string `json:"qr"`
	AppID     string `json:"appId"`
	Timestamp string `json:"timeStamp"`
	NonceStr  string `json:"nonceStr"`
	Package   string `json:"package"`
	SignType  string `json:"signType"`
	PaySign   string `json:"paySign"`
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
	requestStartedAt := time.Now().Unix()
	if order.ExpiresAt > 0 {
		remaining := order.ExpiresAt - requestStartedAt
		if remaining <= 0 {
			return nil, model.ErrPaymentQuoteExpired
		}
		form.Set("expire", strconv.FormatInt(remaining, 10))
	}
	if upstreamMethod == setting.XorPayMethodJSAPI {
		openID, authorizationErr := model.PaymentOrderBrowserAuthorization(order)
		if authorizationErr != nil {
			return nil, authorizationErr
		}
		form.Set("openid", openID)
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
	if err := decodeXorPayResponse(response.Body, &result); err != nil {
		return nil, fmt.Errorf("%w: invalid xorpay response", ErrPaymentStateUnknown)
	}
	if result.Status != "ok" {
		return nil, xorPayCreateError(result.Status)
	}
	result.AOID = strings.TrimSpace(result.AOID)
	if !xorPayAidPattern.MatchString(result.AOID) {
		return nil, fmt.Errorf("%w: xorpay response contains an invalid aoid", ErrPaymentStateUnknown)
	}
	expiresIn := result.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = result.ExpireIn
	}
	if expiresIn <= 0 || expiresIn > 24*60*60 {
		expiresIn = xorPayDefaultExpiry
	}
	responseReceivedAt := time.Now().Unix()
	expiresAt := responseReceivedAt + expiresIn
	if order.ExpiresAt > 0 && expiresAt > order.ExpiresAt {
		expiresAt = order.ExpiresAt
	}
	if expiresAt <= responseReceivedAt {
		return nil, fmt.Errorf("%w: xorpay order was created after the local payment session expired", ErrPaymentStateUnknown)
	}
	providerOrderKey := "xorpay:" + result.AOID
	start := &PaymentStart{ExpiresAt: expiresAt, ProviderOrderKey: providerOrderKey}
	if upstreamMethod == setting.XorPayMethodJSAPI {
		parameters, validationErr := validateXorPayJSAPIParameters(result.Info)
		if validationErr != nil {
			return nil, fmt.Errorf("%w: invalid xorpay JSAPI payment instructions: %v", ErrPaymentStateUnknown, validationErr)
		}
		start.Flow = PaymentFlowJSAPI
		start.JSAPI = parameters
		return start, nil
	}
	if err := validateXorPayQR(upstreamMethod, result.Info.QR); err != nil {
		// XORPay has already accepted the merchant order at this point. Treat an
		// invalid or missing QR as an ambiguous creation result so the worker
		// queries the existing order instead of blindly creating another one.
		return nil, fmt.Errorf("%w: invalid xorpay payment instructions: %v", ErrPaymentStateUnknown, err)
	}
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
	if len(request.URL.RawQuery) > xorPayMaxWebhookBytes ||
		request.ContentLength > xorPayMaxWebhookBytes {
		return nil, errors.New("xorpay callback is too large")
	}
	request.Body = http.MaxBytesReader(nil, request.Body, xorPayMaxWebhookBytes)
	if err := request.ParseForm(); err != nil {
		return nil, fmt.Errorf("invalid xorpay callback form: %w", err)
	}
	if len(request.Form) > xorPayMaxWebhookParameters {
		return nil, errors.New("xorpay callback contains too many parameters")
	}
	params := make(map[string]string, len(request.PostForm))
	for key, values := range request.PostForm {
		if key == "" || len(key) > 64 || len(values) != 1 {
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
	if !xorPayAidPattern.MatchString(aoid) || len(tradeNo) > 128 || len(price) > 64 || len(payTime) > 64 || len(sign) != md5.Size*2 {
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
		// query2 carries a merchant signature in the URL query. net/http errors
		// may embed the full request URL, so never return the raw error into the
		// durable task record or application logs.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return "", errors.New("xorpay query request timed out")
		}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return "", errors.New("xorpay query request timed out")
		}
		return "", errors.New("xorpay query request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("xorpay query returned HTTP %d", response.StatusCode)
	}
	var result xorPayQueryResponse
	if err := decodeXorPayResponse(response.Body, &result); err != nil {
		return "", errors.New("invalid xorpay query response")
	}
	switch result.Status {
	case "payed", "success", "expire", "fee_error", "new", "not_exist":
		return result.Status, nil
	default:
		return "", fmt.Errorf("unknown xorpay query status: %s", result.Status)
	}
}

func decodeXorPayResponse(body io.Reader, target any) error {
	payload, err := io.ReadAll(io.LimitReader(body, xorPayMaxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > xorPayMaxResponseBytes {
		return errors.New("xorpay response exceeds size limit")
	}
	return common.Unmarshal(payload, target)
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
	case model.PaymentMethodXorPayJSAPI:
		return setting.XorPayMethodJSAPI, nil
	default:
		return "", fmt.Errorf("unsupported xorpay payment method: %s", method)
	}
}

func xorPayMD5(value string) string {
	// XORPay's published request and callback authentication protocol mandates
	// this legacy MD5 construction, so it cannot be replaced locally without
	// breaking provider interoperability. Compensating controls include TLS-only
	// provider transport, constant-time callback comparison, credential-generation
	// scoping, exact order/currency/amount checks, and idempotent settlement.
	digest := md5.Sum([]byte(value)) // lgtm[go/weak-sensitive-data-hashing]
	return fmt.Sprintf("%x", digest)
}

func xorPayCreateError(status string) error {
	switch status {
	case "order_payed", "order_expire", "order_exist":
		return fmt.Errorf("%w: xorpay create returned %s", ErrPaymentStateUnknown, status)
	case "fee_error":
		return fmt.Errorf("%w: xorpay create returned %s", ErrPaymentStateUnknown, status)
	case "missing_argument", "aid_not_exist", "pay_type_error", "sign_error", "app_off", "no_contract", "no_alipay_contract", "wechat_api_error", "alipay_api_error", "invalid_openid":
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

func validateXorPayJSAPIParameters(info xorPayCreateInfo) (*PaymentJSAPIParameters, error) {
	appID := strings.TrimSpace(info.AppID)
	timestamp := strings.TrimSpace(info.Timestamp)
	nonceStr := strings.TrimSpace(info.NonceStr)
	packageValue := strings.TrimSpace(info.Package)
	signType := strings.ToUpper(strings.TrimSpace(info.SignType))
	paySign := strings.TrimSpace(info.PaySign)
	if len(appID) != 18 || !strings.HasPrefix(appID, "wx") {
		return nil, errors.New("invalid app id")
	}
	for _, character := range appID[2:] {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		return nil, errors.New("invalid app id")
	}
	if len(timestamp) < 1 || len(timestamp) > 16 {
		return nil, errors.New("invalid timestamp")
	}
	for _, character := range timestamp {
		if character < '0' || character > '9' {
			return nil, errors.New("invalid timestamp")
		}
	}
	if nonceStr == "" || len(nonceStr) > 128 || strings.ContainsAny(nonceStr, "\r\n\x00") {
		return nil, errors.New("invalid nonce")
	}
	if !strings.HasPrefix(packageValue, "prepay_id=") || len(packageValue) > 256 || strings.ContainsAny(packageValue, "\r\n\x00") {
		return nil, errors.New("invalid package")
	}
	if signType != "MD5" && signType != "HMAC-SHA256" {
		return nil, errors.New("invalid sign type")
	}
	if len(paySign) < 16 || len(paySign) > 256 || strings.ContainsAny(paySign, "\r\n\x00") {
		return nil, errors.New("invalid payment signature")
	}
	return &PaymentJSAPIParameters{
		AppID: appID, Timestamp: timestamp, NonceStr: nonceStr, Package: packageValue,
		SignType: signType, PaySign: paySign,
	}, nil
}
