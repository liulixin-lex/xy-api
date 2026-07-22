package service

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

var ErrPublicPaymentRouteNotFound = errors.New("payment method is unavailable")
var ErrPublicPaymentRouteConflict = errors.New("public payment route configuration conflicts")

// PublicPaymentRoute is the user-facing payment identifier. Provider and
// PaymentMethod remain server-only routing data and can safely coexist with
// the public aliases in the same value because they are excluded from JSON.
type PublicPaymentRoute struct {
	RouteID       string `json:"route_id"`
	PublicMethod  string `json:"public_method"`
	ChannelAlias  string `json:"channel_alias"`
	Icon          string `json:"icon,omitempty"`
	Color         string `json:"color,omitempty"`
	Currency      string `json:"currency,omitempty"`
	MinimumTopUp  string `json:"min_topup,omitempty"`
	Provider      string `json:"-"`
	PaymentMethod string `json:"-"`
}

// PublicPaymentRoutes returns a configuration snapshot. The route identifiers
// are deterministic for existing PayMethods JSON that predates route_id, while
// explicitly configured route identifiers remain stable across all nodes.
func PublicPaymentRoutes() ([]PublicPaymentRoute, error) {
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		return nil, err
	}
	unlock := setting.LockPaymentConfigurationForRead()
	defer unlock()
	routes := publicPaymentRoutesLocked()
	if err := validatePublicPaymentRoutes(routes); err != nil {
		return nil, err
	}
	return routes, nil
}

// validatePublicPaymentRoutes keeps a configured public alias from shadowing
// another canonical route. ParsePayMethodsByJsonString rejects duplicates that
// are present in the saved list, but Stripe and XORPay may also contribute
// implicit compatibility routes. The final merged snapshot therefore needs an
// independent uniqueness check before a user-supplied route_id is resolved.
func validatePublicPaymentRoutes(routes []PublicPaymentRoute) error {
	seen := make(map[string]string, len(routes))
	for _, route := range routes {
		if !safePublicPaymentAlias(route.RouteID) ||
			!safePublicPaymentAlias(route.PublicMethod) ||
			!safePublicPaymentAlias(route.ChannelAlias) {
			return fmt.Errorf("%w: invalid public alias", ErrPublicPaymentRouteConflict)
		}
		identity := strings.ToLower(strings.TrimSpace(route.Provider)) + "\x00" +
			NormalizePaymentMethod(route.Provider, route.PaymentMethod)
		if existing, ok := seen[route.RouteID]; ok && existing != identity {
			return fmt.Errorf("%w: duplicate route_id %q", ErrPublicPaymentRouteConflict, route.RouteID)
		}
		seen[route.RouteID] = identity
	}
	return nil
}

// ResolvePublicPaymentRoute resolves a user-supplied opaque route into the
// provider/method pair used by the canonical quote and order models.
func ResolvePublicPaymentRoute(routeID string) (*PublicPaymentRoute, error) {
	routeID = strings.ToLower(strings.TrimSpace(routeID))
	if routeID == "" {
		return nil, ErrPublicPaymentRouteNotFound
	}
	routes, err := PublicPaymentRoutes()
	if err != nil {
		return nil, err
	}
	for index := range routes {
		if routes[index].RouteID == routeID {
			return &routes[index], nil
		}
	}
	return nil, ErrPublicPaymentRouteNotFound
}

// ResolveLegacyPublicPaymentRoute keeps older clients working while the public
// API migrates from provider/payment_method to route_id. It only resolves
// currently configured routes and never returns the internal identifiers.
func ResolveLegacyPublicPaymentRoute(provider, paymentMethod string) (*PublicPaymentRoute, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	paymentMethod = NormalizePaymentMethod(provider, paymentMethod)
	if provider == "" || paymentMethod == "" {
		return nil, ErrPublicPaymentRouteNotFound
	}
	routes, err := PublicPaymentRoutes()
	if err != nil {
		return nil, err
	}
	for index := range routes {
		if routes[index].Provider == provider && routes[index].PaymentMethod == paymentMethod {
			return &routes[index], nil
		}
	}
	return nil, ErrPublicPaymentRouteNotFound
}

// PublicPaymentRouteForOrder maps historical orders without exposing provider
// details. If the current configuration no longer contains the route, the
// deterministic default still gives the user a stable, non-sensitive label.
func PublicPaymentRouteForOrder(provider, paymentMethod string) PublicPaymentRoute {
	provider = strings.ToLower(strings.TrimSpace(provider))
	paymentMethod = NormalizePaymentMethod(provider, paymentMethod)

	unlock := setting.LockPaymentConfigurationForRead()
	defer unlock()
	for _, route := range publicPaymentRoutesLocked() {
		if route.Provider == provider && route.PaymentMethod == paymentMethod {
			return route
		}
	}
	return publicPaymentRouteFromValues(provider, paymentMethod, nil)
}

func publicPaymentRoutesLocked() []PublicPaymentRoute {
	routes := make([]PublicPaymentRoute, 0, len(operation_setting.PayMethods)+5)
	seen := make(map[string]struct{}, len(operation_setting.PayMethods)+5)
	appendRoute := func(provider, paymentMethod string, configured map[string]string) {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			provider = model.PaymentProviderEpay
		}
		paymentMethod = NormalizePaymentMethod(provider, paymentMethod)
		if paymentMethod == "" {
			return
		}
		identity := provider + "\x00" + paymentMethod
		if _, exists := seen[identity]; exists {
			return
		}
		seen[identity] = struct{}{}
		routes = append(routes, publicPaymentRouteFromValues(provider, paymentMethod, configured))
	}

	for _, configured := range operation_setting.PayMethods {
		provider := configured["provider"]
		if provider == "" {
			provider = model.PaymentProviderEpay
		}
		switch strings.ToLower(strings.TrimSpace(provider)) {
		case model.PaymentProviderEpay, model.PaymentProviderStripe, model.PaymentProviderXorPay,
			model.PaymentProviderWaffoPancake:
			appendRoute(provider, configured["type"], configured)
		}
	}
	appendRoute(model.PaymentProviderStripe, model.PaymentMethodStripe, nil)
	appendRoute(model.PaymentProviderCreem, model.PaymentMethodCreem, map[string]string{
		"public_method": "online_payment", "channel_alias": "product_checkout",
	})
	appendRoute(model.PaymentProviderWaffo, model.PaymentMethodWaffo, map[string]string{
		"public_method": "online_payment", "channel_alias": "payment_options",
	})
	appendRoute(model.PaymentProviderWaffoPancake, model.PaymentMethodWaffoPancake, map[string]string{
		"public_method": "online_payment", "channel_alias": "hosted_checkout",
	})

	// XORPay historically did not require entries in PayMethods. Keep those
	// deployments compatible while still exposing only public route aliases.
	if setting.IsXorPayMethodEnabled(setting.XorPayMethodNative) {
		appendRoute(model.PaymentProviderXorPay, model.PaymentMethodXorPayNative, nil)
	}
	if setting.IsXorPayMethodEnabled(setting.XorPayMethodAlipay) {
		appendRoute(model.PaymentProviderXorPay, model.PaymentMethodXorPayAlipay, nil)
	}
	if setting.IsXorPayMethodEnabled(setting.XorPayMethodJSAPI) {
		appendRoute(model.PaymentProviderXorPay, model.PaymentMethodXorPayJSAPI, nil)
	}
	return routes
}

func publicPaymentRouteFromValues(provider, paymentMethod string, configured map[string]string) PublicPaymentRoute {
	route := PublicPaymentRoute{
		RouteID:       operation_setting.PublicPaymentRouteID(provider, paymentMethod),
		PublicMethod:  operation_setting.DefaultPublicPaymentMethod(provider, paymentMethod),
		ChannelAlias:  operation_setting.DefaultPaymentChannelAlias(provider, paymentMethod),
		Provider:      provider,
		PaymentMethod: paymentMethod,
	}
	if configured == nil {
		return route
	}
	if value := strings.ToLower(strings.TrimSpace(configured["route_id"])); safePublicPaymentAlias(value) {
		route.RouteID = value
	}
	if value := strings.ToLower(strings.TrimSpace(configured["public_method"])); safePublicPaymentAlias(value) {
		route.PublicMethod = value
	}
	if value := strings.ToLower(strings.TrimSpace(configured["channel_alias"])); safePublicPaymentAlias(value) {
		route.ChannelAlias = value
	}
	route.Icon = strings.TrimSpace(configured["icon"])
	route.Color = strings.TrimSpace(configured["color"])
	if containsInternalPaymentIdentifier(route.Icon) {
		route.Icon = ""
	}
	if containsInternalPaymentIdentifier(route.Color) {
		route.Color = ""
	}
	route.Currency = strings.ToUpper(strings.TrimSpace(configured["currency"]))
	route.MinimumTopUp = strings.TrimSpace(configured["min_topup"])
	return route
}

func safePublicPaymentAlias(value string) bool {
	if value == "" || len(value) > 64 || containsInternalPaymentIdentifier(value) {
		return false
	}
	for index, character := range value {
		if index == 0 {
			if character < 'a' || character > 'z' {
				return false
			}
			continue
		}
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func containsInternalPaymentIdentifier(value string) bool {
	return operation_setting.ContainsInternalPaymentProviderName(value)
}

// PublicPaymentLabel prevents operator-entered catalog names from exposing
// internal gateway or provider terminology in user-facing payment surfaces.
// The original value remains available to administrators and in immutable
// payment snapshots; only the public projection is sanitized.
func PublicPaymentLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	fallback = strings.TrimSpace(fallback)
	if fallback == "" || utf8.RuneCountInString(fallback) > 128 || strings.IndexFunc(fallback, unicode.IsControl) >= 0 ||
		containsInternalPaymentIdentifier(fallback) {
		fallback = "Online payment"
	}
	if value == "" || utf8.RuneCountInString(value) > 128 || strings.IndexFunc(value, unicode.IsControl) >= 0 ||
		containsInternalPaymentIdentifier(value) {
		return fallback
	}
	return value
}
