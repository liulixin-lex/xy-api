package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
)

type epayPaymentProvider struct{}

type epayCredential struct {
	merchantID string
	key        string
	generation int64
}

const epayDefaultExpirySeconds int64 = 2 * 60 * 60

const (
	epayMaxWebhookBytes      = 64 << 10
	epayMaxWebhookParameters = 32
)

var (
	epayPaymentMethodPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	externalPaymentHostPattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*\.?$`)
)

func init() {
	RegisterPaymentProvider(&epayPaymentProvider{})
}

func (*epayPaymentProvider) Name() string { return model.PaymentProviderEpay }

func (*epayPaymentProvider) CredentialGeneration() int64 {
	return operation_setting.EpayCredentialGeneration
}

func (*epayPaymentProvider) ValidateMethod(method string) error {
	if !operation_setting.ContainsPayMethod(method) {
		return fmt.Errorf("unsupported epay payment method: %s", method)
	}
	return nil
}

func (p *epayPaymentProvider) Create(_ context.Context, order *model.PaymentOrder) (*PaymentStart, error) {
	if order == nil {
		return nil, errors.New("payment order is required")
	}
	if err := p.ValidateMethod(order.PaymentMethod); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(order.Currency), "CNY") || !strings.EqualFold(strings.TrimSpace(operation_setting.EpayCurrency), "CNY") {
		return nil, errors.New("epay currency must be CNY")
	}
	action, err := validateEpayEndpoint(operation_setting.PayAddress)
	if err != nil {
		return nil, err
	}
	callbackAddress := strings.TrimRight(GetPaymentCallbackAddress(), "/")
	if callbackAddress == "" {
		return nil, errors.New("callback address is not configured")
	}
	if err := ValidatePaymentCallbackOrigin(callbackAddress, true); err != nil {
		return nil, err
	}
	notifyURL, err := url.Parse(callbackAddress + "/api/payment/epay/notify")
	if err != nil {
		return nil, err
	}
	if err := ValidateExternalPaymentURL(notifyURL.String(), true); err != nil {
		return nil, err
	}
	returnBase, err := firstPartyPaymentReturnURL(order.TradeNo)
	if err != nil {
		return nil, err
	}
	returnAddress := returnBase + "?payment_result=pending"
	if err := ValidateExternalPaymentURL(returnAddress, true); err != nil {
		return nil, err
	}
	returnURL, err := url.Parse(returnAddress)
	if err != nil {
		return nil, err
	}
	credential, err := epayCredentialForOrder(order)
	if err != nil {
		return nil, err
	}
	client, err := epay.NewClient(&epay.Config{PartnerID: credential.merchantID, Key: credential.key}, action)
	if err != nil {
		return nil, err
	}
	name := "AI API top-up"
	if order.OrderKind == model.PaymentOrderKindSubscription {
		name = "AI API subscription"
	}
	uri, fields, err := client.Purchase(&epay.PurchaseArgs{
		Type:           order.PaymentMethod,
		ServiceTradeNo: order.TradeNo,
		Name:           name,
		Money:          formatPaymentMinor(order.ExpectedAmountMinor, common.PaymentCurrencyExponent(order.Currency)),
		Device:         epay.PC,
		NotifyUrl:      notifyURL,
		ReturnUrl:      returnURL,
	})
	if err != nil {
		return nil, err
	}
	if err := ValidateExternalPaymentURL(uri, false); err != nil {
		return nil, err
	}
	expiresAt := time.Now().Unix() + epayDefaultExpirySeconds
	return &PaymentStart{Flow: PaymentFlowFormPost, Action: uri, Fields: fields, ExpiresAt: expiresAt}, nil
}

func epayCredentialForOrder(order *model.PaymentOrder) (epayCredential, error) {
	if order == nil {
		return epayCredential{}, errors.New("payment order is required")
	}
	generation := order.ProviderCredentialGeneration
	if generation == 0 || generation == operation_setting.EpayCredentialGeneration {
		if strings.TrimSpace(operation_setting.EpayId) != "" && strings.TrimSpace(operation_setting.EpayKey) != "" {
			return epayCredential{
				merchantID: operation_setting.EpayId, key: operation_setting.EpayKey,
				generation: operation_setting.EpayCredentialGeneration,
			}, nil
		}
	}
	if generation == operation_setting.EpayPreviousCredentialGeneration && operation_setting.EpayPreviousCredentialActive() &&
		order.CreatedAt > 0 && order.CreatedAt <= operation_setting.EpayPreviousValidBefore {
		return epayCredential{
			merchantID: operation_setting.EpayIdPrevious, key: operation_setting.EpayKeyPrevious,
			generation: operation_setting.EpayPreviousCredentialGeneration,
		}, nil
	}
	return epayCredential{}, errors.New("epay credential generation for this order is no longer available")
}

func (*epayPaymentProvider) VerifyWebhook(request *http.Request) (*NormalizedPaymentEvent, error) {
	if request == nil {
		return nil, errors.New("request is required")
	}
	if (strings.TrimSpace(operation_setting.EpayId) == "" || strings.TrimSpace(operation_setting.EpayKey) == "") &&
		!operation_setting.EpayPreviousCredentialActive() {
		return nil, errors.New("epay webhook is not configured")
	}
	if len(request.URL.RawQuery) > epayMaxWebhookBytes ||
		request.ContentLength > epayMaxWebhookBytes {
		return nil, errors.New("epay callback is too large")
	}
	if request.Body != nil {
		request.Body = http.MaxBytesReader(nil, request.Body, epayMaxWebhookBytes)
	}
	if err := request.ParseForm(); err != nil {
		return nil, fmt.Errorf("invalid epay callback form: %w", err)
	}
	if len(request.Form) > epayMaxWebhookParameters {
		return nil, errors.New("epay callback contains too many parameters")
	}
	params := make(map[string]string, len(request.Form))
	for key, values := range request.Form {
		if key == "" || len(key) > 64 || len(values) != 1 || len(values[0]) > 1024 {
			return nil, fmt.Errorf("invalid epay parameter: %s", key)
		}
		params[key] = values[0]
	}
	tradeNo := strings.TrimSpace(params["out_trade_no"])
	providerOrderID := strings.TrimSpace(params["trade_no"])
	paymentMethod := strings.TrimSpace(params["type"])
	status := strings.TrimSpace(params["trade_status"])
	if tradeNo == "" || providerOrderID == "" || strings.TrimSpace(params["money"]) == "" || status == "" || paymentMethod == "" {
		return nil, errors.New("epay callback is missing required fields")
	}
	if len(tradeNo) > 128 || len(providerOrderID) > 200 || len(params["money"]) > 64 || len(status) > 128 ||
		!epayPaymentMethodPattern.MatchString(paymentMethod) {
		return nil, errors.New("epay callback contains invalid identifiers")
	}
	sign := strings.ToLower(strings.TrimSpace(params["sign"]))
	if sign == "" || params["sign_type"] != "" && !strings.EqualFold(params["sign_type"], "MD5") {
		return nil, errors.New("invalid epay signature metadata")
	}
	scope, err := resolvePaymentCredentialScope(model.PaymentProviderEpay, tradeNo)
	if err != nil {
		return nil, err
	}
	credentials := make([]epayCredential, 0, 2)
	if scope.allowsCurrent(operation_setting.EpayCredentialGeneration) && operation_setting.EpayId != "" && operation_setting.EpayKey != "" {
		credentials = append(credentials, epayCredential{
			merchantID: operation_setting.EpayId, key: operation_setting.EpayKey,
			generation: operation_setting.EpayCredentialGeneration,
		})
	}
	if operation_setting.EpayPreviousCredentialActive() && scope.allowsPrevious(
		operation_setting.EpayPreviousCredentialGeneration,
		operation_setting.EpayPreviousValidBefore,
	) {
		credentials = append(credentials, epayCredential{
			merchantID: operation_setting.EpayIdPrevious, key: operation_setting.EpayKeyPrevious,
			generation: operation_setting.EpayPreviousCredentialGeneration,
		})
	}
	merchantMatched := false
	signatureMatched := false
	matchedGeneration := int64(0)
	for _, credential := range credentials {
		if params["pid"] != credential.merchantID {
			continue
		}
		merchantMatched = true
		copyParams := make(map[string]string, len(params))
		for key, value := range params {
			copyParams[key] = value
		}
		expected := strings.ToLower(epay.GenerateParams(copyParams, credential.key)["sign"])
		if len(sign) == len(expected) && subtle.ConstantTimeCompare([]byte(sign), []byte(expected)) == 1 {
			signatureMatched = true
			matchedGeneration = credential.generation
			break
		}
	}
	if !merchantMatched {
		return nil, errors.New("epay merchant mismatch")
	}
	if !signatureMatched {
		return nil, errors.New("epay signature mismatch")
	}
	providerOrderKey := fmt.Sprintf("%s:g%d:%s", model.PaymentProviderEpay, matchedGeneration, providerOrderID)
	legacyProviderOrderKey := model.PaymentProviderEpay + ":" + providerOrderID
	if scope.providerOrderKey == legacyProviderOrderKey || scope.providerOrderKey == providerOrderKey {
		providerOrderKey = scope.providerOrderKey
	}
	// The standard Epay protocol signs only the numeric money field and carries
	// no currency. Treat it as CNY unconditionally; a historical order snapshot
	// with any other currency will be retained as a settlement mismatch for
	// administrator review instead of being credited under an unverifiable unit.
	currency := "CNY"
	exponent, ok := common.PaymentCurrencyExponentOK(currency)
	if !ok {
		return nil, errors.New("unsupported epay currency")
	}
	minor, err := parsePaymentMinor(params["money"], exponent)
	if err != nil {
		return nil, err
	}
	paid := status == epay.StatusTradeSuccess
	normalized := common.GetJsonString(map[string]string{
		"trade_no":     providerOrderKey,
		"out_trade_no": tradeNo,
		"money":        params["money"],
		"trade_status": status,
		"type":         paymentMethod,
	})
	return &NormalizedPaymentEvent{
		Provider:                     model.PaymentProviderEpay,
		EventKey:                     model.PaymentEventKey(model.PaymentProviderEpay, status, providerOrderKey, tradeNo, normalized),
		EventType:                    status,
		TradeNo:                      tradeNo,
		ProviderOrderKey:             providerOrderKey,
		ProviderCredentialGeneration: matchedGeneration,
		PaidAmountMinor:              minor,
		Currency:                     currency,
		PaymentMethod:                paymentMethod,
		Paid:                         paid,
		NormalizedPayload:            normalized,
	}, nil
}

func (*epayPaymentProvider) Query(context.Context, *model.PaymentOrder) (*NormalizedPaymentEvent, error) {
	return nil, errors.New("epay query is not available for arbitrary payment endpoints")
}

func validateEpayEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid epay endpoint")
	}
	if u.Scheme != "https" && !isLocalDevelopmentHost(u.Hostname()) {
		return "", errors.New("epay endpoint must use HTTPS")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func ValidateExternalPaymentURL(raw string, allowLocalHTTP bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 4096 || strings.ContainsAny(raw, "\r\n\t\\") {
		return errors.New("invalid external payment URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("invalid external payment URL")
	}
	if u.Scheme != "https" && !(allowLocalHTTP && u.Scheme == "http" && isLocalDevelopmentHost(u.Hostname())) {
		return errors.New("external payment URL must use HTTPS")
	}
	hostname := strings.ToLower(u.Hostname())
	if net.ParseIP(strings.TrimSuffix(hostname, ".")) == nil && !externalPaymentHostPattern.MatchString(hostname) {
		return errors.New("invalid external payment URL host")
	}
	if port := u.Port(); port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return errors.New("invalid external payment URL port")
		}
	}
	return nil
}

func isLocalDevelopmentHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "127.0.0.1" || host == "::1"
}

func formatPaymentMinor(minor int64, exponent int32) string {
	if exponent == 0 {
		return strconv.FormatInt(minor, 10)
	}
	value := decimal.New(minor, -exponent)
	return value.StringFixed(exponent)
}

func parsePaymentMinor(value string, exponent int32) (int64, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil || amount.IsNegative() {
		return 0, errors.New("invalid payment amount")
	}
	minor := amount.Mul(decimal.New(1, exponent))
	if !minor.Equal(minor.Truncate(0)) || minor.IsZero() || minor.GreaterThan(decimal.NewFromInt(math.MaxInt32)) {
		return 0, errors.New("invalid payment precision")
	}
	return minor.IntPart(), nil
}
