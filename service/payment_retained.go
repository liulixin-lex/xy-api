package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
)

const maxCreemProducts = 100

type CreemProduct struct {
	ProductID string  `json:"productId"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency"`
	Quota     int64   `json:"quota"`
}

type retainedPaymentSnapshot struct {
	Version          int    `json:"version"`
	CreemProductID   string `json:"creem_product_id,omitempty"`
	CreemProductName string `json:"creem_product_name,omitempty"`
	WaffoOptionType  string `json:"waffo_option_type,omitempty"`
	WaffoOptionName  string `json:"waffo_option_name,omitempty"`
	PancakeProductID string `json:"pancake_product_id,omitempty"`
}

func ParseValidatedCreemProducts(value string) ([]CreemProduct, error) {
	var products []CreemProduct
	if err := common.UnmarshalJsonStr(value, &products); err != nil || products == nil {
		return nil, errors.New("Creem products must be a JSON array")
	}
	if len(products) > maxCreemProducts {
		return nil, fmt.Errorf("Creem products exceed the maximum of %d entries", maxCreemProducts)
	}
	seen := make(map[string]struct{}, len(products))
	for index := range products {
		product := &products[index]
		product.ProductID = strings.TrimSpace(product.ProductID)
		product.Name = strings.TrimSpace(product.Name)
		product.Currency = strings.ToUpper(strings.TrimSpace(product.Currency))
		if product.Name == "" || utf8.RuneCountInString(product.Name) > 128 || strings.IndexFunc(product.Name, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("Creem product %d has an invalid public name", index+1)
		}
		if product.ProductID == "" || len(product.ProductID) > 255 {
			return nil, fmt.Errorf("Creem product %d has an invalid product ID", index+1)
		}
		if _, exists := seen[product.ProductID]; exists {
			return nil, fmt.Errorf("Creem product %d has a duplicate product ID", index+1)
		}
		seen[product.ProductID] = struct{}{}
		if product.Currency != "USD" && product.Currency != "EUR" {
			return nil, fmt.Errorf("Creem product %d has an unsupported currency", index+1)
		}
		minor, err := model.ProviderPaymentAmountToMinor(product.Price, model.PaymentProviderCreem, product.Currency)
		if err != nil || minor <= 0 {
			return nil, fmt.Errorf("Creem product %d has an invalid price", index+1)
		}
		if product.Quota < 1 || product.Quota > int64(common.MaxQuota) {
			return nil, fmt.Errorf("Creem product %d has an invalid quota", index+1)
		}
	}
	return products, nil
}

func fillRetainedTopUpQuote(quote *model.PaymentQuote, request PaymentQuoteRequest) (decimal.Decimal, error) {
	if quote == nil {
		return decimal.Zero, errors.New("payment quote is required")
	}
	switch quote.Provider {
	case model.PaymentProviderCreem:
		return fillCreemTopUpQuote(quote, request.ProductID)
	case model.PaymentProviderWaffo:
		payable, err := fillTopUpQuote(quote, request.Amount)
		if err != nil {
			return decimal.Zero, err
		}
		optionType, optionName, err := resolveWaffoOption(request.OptionID)
		if err != nil {
			return decimal.Zero, err
		}
		snapshot, err := common.Marshal(retainedPaymentSnapshot{
			Version: 1, WaffoOptionType: optionType, WaffoOptionName: optionName,
		})
		if err != nil {
			return decimal.Zero, err
		}
		quote.ProductSnapshot = string(snapshot)
		return payable, nil
	case model.PaymentProviderWaffoPancake:
		payable, err := fillTopUpQuote(quote, request.Amount)
		if err != nil {
			return decimal.Zero, err
		}
		productID := strings.TrimSpace(setting.WaffoPancakeProductID)
		if productID == "" {
			return decimal.Zero, errors.New("waffo pancake product is not configured")
		}
		snapshot, err := common.Marshal(retainedPaymentSnapshot{Version: 1, PancakeProductID: productID})
		if err != nil {
			return decimal.Zero, err
		}
		quote.ProductSnapshot = string(snapshot)
		return payable, nil
	default:
		return decimal.Zero, fmt.Errorf("unsupported retained payment provider: %s", quote.Provider)
	}
}

func fillCreemTopUpQuote(quote *model.PaymentQuote, publicProductID string) (decimal.Decimal, error) {
	publicProductID = strings.TrimSpace(publicProductID)
	if publicProductID == "" {
		return decimal.Zero, errors.New("payment product is required")
	}
	products, err := ParseValidatedCreemProducts(setting.CreemProducts)
	if err != nil {
		return decimal.Zero, err
	}
	var selected *CreemProduct
	for index := range products {
		if PublicRetainedProductID(model.PaymentProviderCreem, products[index].ProductID) == publicProductID {
			selected = &products[index]
			break
		}
	}
	if selected == nil {
		return decimal.Zero, errors.New("payment product is unavailable")
	}
	minor, err := model.ProviderPaymentAmountToMinor(selected.Price, model.PaymentProviderCreem, selected.Currency)
	if err != nil || minor <= 0 {
		return decimal.Zero, errors.New("payment product amount is invalid")
	}
	if math.IsNaN(selected.Price) || math.IsInf(selected.Price, 0) || selected.Price <= 0 {
		return decimal.Zero, errors.New("payment product amount is invalid")
	}
	pricing, err := common.Marshal(map[string]interface{}{
		"fixed_product": true,
		"price":         decimal.NewFromFloat(selected.Price).String(),
		"currency":      selected.Currency,
		"credit_quota":  selected.Quota,
	})
	if err != nil {
		return decimal.Zero, err
	}
	product, err := common.Marshal(retainedPaymentSnapshot{
		Version: 1, CreemProductID: selected.ProductID, CreemProductName: selected.Name,
	})
	if err != nil {
		return decimal.Zero, err
	}
	quote.RequestedAmount = selected.Quota
	quote.CreditQuota = selected.Quota
	quote.Currency = selected.Currency
	quote.ExpectedAmountMinor = minor
	quote.PricingSnapshot = string(pricing)
	quote.ProductSnapshot = string(product)
	exponent := common.PaymentProviderCurrencyExponent(model.PaymentProviderCreem, selected.Currency)
	return decimal.New(minor, -exponent), nil
}

func resolveWaffoOption(publicOptionID string) (string, string, error) {
	publicOptionID = strings.TrimSpace(publicOptionID)
	if publicOptionID == "" {
		return "", "", nil
	}
	for _, method := range setting.GetWaffoPayMethods() {
		identity := strings.TrimSpace(method.PayMethodType) + "\x00" + strings.TrimSpace(method.PayMethodName)
		if PublicRetainedOptionID(model.PaymentProviderWaffo, identity) == publicOptionID {
			return strings.TrimSpace(method.PayMethodType), strings.TrimSpace(method.PayMethodName), nil
		}
	}
	return "", "", errors.New("payment option is unavailable")
}

func validateRetainedSubscriptionSnapshot(provider string, snapshot *model.SubscriptionPlanSnapshot) error {
	if snapshot == nil {
		return model.ErrSubscriptionOrderSnapshotMissing
	}
	switch provider {
	case model.PaymentProviderCreem:
		if strings.TrimSpace(snapshot.CreemProductId) == "" {
			return errors.New("Creem product is not configured for this plan")
		}
	case model.PaymentProviderWaffoPancake:
		if strings.TrimSpace(snapshot.WaffoPancakeProductId) == "" {
			return errors.New("Waffo Pancake product is not configured for this plan")
		}
	}
	return nil
}

func retainedPaymentSnapshotForOrder(order *model.PaymentOrder) (*retainedPaymentSnapshot, error) {
	if order == nil || strings.TrimSpace(order.ProductSnapshot) == "" {
		return nil, errors.New("payment product snapshot is missing")
	}
	var snapshot retainedPaymentSnapshot
	if err := common.UnmarshalJsonStr(order.ProductSnapshot, &snapshot); err != nil || snapshot.Version != 1 {
		return nil, errors.New("payment product snapshot is invalid")
	}
	return &snapshot, nil
}

func subscriptionPlanSnapshotForPayment(order *model.PaymentOrder) (*model.SubscriptionPlanSnapshot, error) {
	if order == nil || strings.TrimSpace(order.ProductSnapshot) == "" {
		return nil, model.ErrSubscriptionOrderSnapshotMissing
	}
	var snapshot model.SubscriptionPlanSnapshot
	if err := common.UnmarshalJsonStr(order.ProductSnapshot, &snapshot); err != nil {
		return nil, model.ErrSubscriptionOrderSnapshotMissing
	}
	if _, err := snapshot.SubscriptionPlan(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func retainedProviderAuthorityKey(provider, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return strings.TrimSpace(provider) + ":" + raw
}

// RetainedProviderAuthorityKey namespaces an upstream order identifier before
// it is persisted in the canonical payment authority columns. Legacy
// projections keep their historical raw provider IDs, while canonical orders
// must not allow equal-looking IDs from different gateways to collide.
func RetainedProviderAuthorityKey(provider, raw string) string {
	return retainedProviderAuthorityKey(provider, raw)
}

func retainedProviderAuthorityValue(provider, key string) string {
	return strings.TrimPrefix(strings.TrimSpace(key), strings.TrimSpace(provider)+":")
}

func waffoPaymentRequestID(tradeNo string) string {
	digest := sha256.Sum256([]byte("waffo-payment-request\x00" + strings.TrimSpace(tradeNo)))
	return "wa_" + hex.EncodeToString(digest[:12])
}

func firstPartyPaymentReturnURL(tradeNo string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(GetCallbackAddress()), "/")
	if err := ValidatePaymentCallbackOrigin(base, true); err != nil {
		return "", err
	}
	return base + "/payment/" + url.PathEscape(strings.TrimSpace(tradeNo)), nil
}

func retainedTopUpPricingSnapshot(order *model.PaymentOrder) (map[string]interface{}, error) {
	var snapshot map[string]interface{}
	if order == nil || common.UnmarshalJsonStr(order.PricingSnapshot, &snapshot) != nil {
		return nil, errors.New("payment pricing snapshot is invalid")
	}
	return snapshot, nil
}

func retainedOrderDescription(order *model.PaymentOrder) string {
	if order != nil && order.OrderKind == model.PaymentOrderKindSubscription {
		return "Fixed-term access"
	}
	return fmt.Sprintf("Recharge %d credits", order.RequestedAmount)
}

func retainedPaymentCallbackURL(provider string) string {
	base := strings.TrimRight(GetCallbackAddress(), "/")
	switch provider {
	case model.PaymentProviderCreem:
		return base + "/api/creem/webhook"
	case model.PaymentProviderWaffo:
		if strings.TrimSpace(setting.WaffoNotifyUrl) != "" {
			return strings.TrimSpace(setting.WaffoNotifyUrl)
		}
		return base + "/api/waffo/webhook"
	case model.PaymentProviderWaffoPancake:
		return base + "/api/waffo-pancake/webhook/prod"
	default:
		return base
	}
}

func retainedPricingMultiplier(provider string) decimal.Decimal {
	switch provider {
	case model.PaymentProviderWaffo:
		return decimal.NewFromFloat(setting.WaffoUnitPrice)
	case model.PaymentProviderWaffoPancake:
		return decimal.NewFromFloat(setting.WaffoPancakeUnitPrice)
	default:
		return decimal.NewFromFloat(operation_setting.Price)
	}
}
