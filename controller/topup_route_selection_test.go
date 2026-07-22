package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSelectPublicTopUpRoutesForUserDeduplicatesPaymentBrands(t *testing.T) {
	routes := []publicTopUpRouteView{
		{RouteID: "pay_alpha", PublicMethod: "alipay", ChannelAlias: "redirect"},
		{RouteID: "pay_beta", PublicMethod: "alipay", ChannelAlias: "qr"},
		{RouteID: "pay_gamma", PublicMethod: "wechat_pay", ChannelAlias: "qr"},
		{RouteID: "pay_delta", PublicMethod: "wechat_pay", ChannelAlias: "wechat_browser"},
		{RouteID: "pay_epsilon", PublicMethod: "online_payment", ChannelAlias: "product_checkout"},
		{RouteID: "pay_zeta", PublicMethod: "online_payment", ChannelAlias: "payment_options"},
	}

	desktop := selectPublicTopUpRoutesForUser(routes, "Mozilla/5.0")
	assert.Equal(t, []string{"pay_alpha", "pay_gamma", "pay_epsilon", "pay_zeta"}, publicTopUpRouteIDs(desktop))

	wechat := selectPublicTopUpRoutesForUser(routes, "Mozilla/5.0 MicroMessenger/8.0")
	assert.Equal(t, []string{"pay_alpha", "pay_delta", "pay_epsilon", "pay_zeta"}, publicTopUpRouteIDs(wechat))
}

func TestSelectPublicTopUpRoutesForUserUsesSafeWeChatFallback(t *testing.T) {
	nativeOnly := []publicTopUpRouteView{{RouteID: "pay_native", PublicMethod: "wechat_pay", ChannelAlias: "qr"}}
	assert.Equal(t, []string{"pay_native"}, publicTopUpRouteIDs(
		selectPublicTopUpRoutesForUser(nativeOnly, "MicroMessenger"),
	))

	jsapiOnly := []publicTopUpRouteView{{RouteID: "pay_jsapi", PublicMethod: "wechat_pay", ChannelAlias: "jsapi"}}
	assert.Empty(t, selectPublicTopUpRoutesForUser(jsapiOnly, "Mozilla/5.0"))
	assert.Equal(t, []string{"pay_jsapi"}, publicTopUpRouteIDs(
		selectPublicTopUpRoutesForUser(jsapiOnly, "MicroMessenger"),
	))
}

func publicTopUpRouteIDs(routes []publicTopUpRouteView) []string {
	ids := make([]string, 0, len(routes))
	for _, route := range routes {
		ids = append(ids, route.RouteID)
	}
	return ids
}
