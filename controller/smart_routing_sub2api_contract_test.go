package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSub2APIOfficialNestedContractMapsNumericGroupsAndPerTokenPrices(t *testing.T) {
	groups, err := parseRoutingSub2APIGroups(json.RawMessage(`[
		{"id":10,"name":"vip","platform":"anthropic","subscription_type":"subscription","rate_multiplier":1.25,"peak_rate_enabled":false}
	]`))
	require.NoError(t, err)
	require.Contains(t, groups, "10")
	require.Contains(t, groups, "vip")
	assert.Equal(t, "10", string(groups["vip"].ID))
	assert.Equal(t, "subscription", groups["vip"].SubscriptionType)
	assert.True(t, routingSub2APIGroupUsesSubscription(groups["vip"]))

	rates, err := parseRoutingSub2APIRates(json.RawMessage(`{"10":1.1}`))
	require.NoError(t, err)
	rate, found := routingSub2APIResolvedGroupRate(groups, rates, "vip")
	require.True(t, found)
	assert.Equal(t, 1.1, rate)

	channels, err := parseRoutingSub2APIChannels(json.RawMessage(`[
		{
			"name":"primary",
			"description":"official contract fixture",
			"platforms":[{
				"platform":"anthropic",
				"groups":[{"id":10,"name":"vip","platform":"anthropic","rate_multiplier":1.25}],
				"supported_models":[{
					"name":"claude-sonnet-4-6",
					"platform":"anthropic",
					"pricing":{
						"billing_mode":"token",
						"input_price":0.000003,
						"output_price":0.000015,
						"cache_write_price":0.00000375,
						"cache_read_price":0.0000003,
						"image_output_price":0.00003,
						"per_request_price":null,
						"intervals":[]
					}
				}]
			}]
		}
	]`))
	require.NoError(t, err)
	require.Len(t, channels, 1)
	assert.Equal(t, []string{"claude-sonnet-4-6"}, routingSub2APIChannelModels(channels[0]))
	assert.True(t, routingSub2APIChannelServesBinding(channels[0], model.RoutingChannelBinding{UpstreamGroup: "vip"}))
	assert.NotContains(t, routingSub2APIChannelModels(channels[0]), "primary")

	pricing, confidence, score, err := routingSub2APINormalizedPricing(channels[0], rate, true)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingCostConfidenceDerived, confidence)
	assert.Equal(t, 0.8, score)
	require.NotNil(t, pricing.BaseRatio)
	assert.InDelta(t, 0.000003*common.QuotaPerUnit, *pricing.BaseRatio, 1e-12)
	require.NotNil(t, pricing.InputCostPerMillion)
	assert.InDelta(t, 3, *pricing.InputCostPerMillion, 1e-12)
	require.NotNil(t, pricing.OutputCostPerMillion)
	assert.InDelta(t, 15, *pricing.OutputCostPerMillion, 1e-12)
	require.NotNil(t, pricing.CacheWriteCostPerMillion)
	assert.InDelta(t, 3.75, *pricing.CacheWriteCostPerMillion, 1e-12)
	require.NotNil(t, pricing.CacheReadCostPerMillion)
	assert.InDelta(t, 0.3, *pricing.CacheReadCostPerMillion, 1e-12)
	require.NotNil(t, pricing.ImageOutputCostPerMillion)
	assert.InDelta(t, 30, *pricing.ImageOutputCostPerMillion, 1e-12)
	assert.Contains(t, pricing.BillingExpression, "cr * 0.3")
	assert.Contains(t, pricing.BillingExpression, "cc1h * 3.75")
	assert.Contains(t, pricing.BillingExpression, "img_o * 30")
	var extras map[string]any
	require.NoError(t, common.Unmarshal(pricing.Extras, &extras))
	assert.Equal(t, model.RoutingCostSub2APIDisplayContractV1, extras["sub2api_contract"])
	assert.Equal(t, false, extras["has_intervals"])
	assert.Equal(t, "anthropic", extras["platform"])
}

func TestSub2APIGroupContractRejectsInvalidSubscriptionAndMetadataDrift(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "zero id", raw: `[{"id":0,"name":"vip","platform":"anthropic","subscription_type":"standard"}]`},
		{name: "missing name", raw: `[{"id":10,"platform":"anthropic","subscription_type":"standard"}]`},
		{name: "missing subscription type", raw: `[{"id":10,"name":"vip","platform":"anthropic"}]`},
		{name: "unknown subscription type", raw: `[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"trial"}]`},
		{name: "duplicate id metadata conflict", raw: `[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard"},{"id":10,"name":"other","platform":"anthropic","subscription_type":"standard"}]`},
		{name: "duplicate id platform conflict before alias compression", raw: `[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard"},{"id":10,"name":"vip","platform":"openai","subscription_type":"standard"}]`},
		{name: "duplicate id subscription conflict before alias compression", raw: `[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard"},{"id":10,"name":"vip","platform":"anthropic","subscription_type":"subscription"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			groups, parseErr := parseRoutingSub2APIGroups(json.RawMessage(test.raw))
			if parseErr == nil {
				require.Error(t, validateRoutingSub2APIGroupContract(groups))
			}
		})
	}

	groups, err := parseRoutingSub2APIGroups(json.RawMessage(`[
		{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1}
	]`))
	require.NoError(t, err)
	require.NoError(t, validateRoutingSub2APIGroupContract(groups))
	for _, test := range []struct {
		name string
		raw  string
	}{
		{
			name: "group id drift",
			raw:  `[{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":20,"name":"vip","platform":"anthropic","subscription_type":"standard"}],"supported_models":[]}]}]`,
		},
		{
			name: "nested group missing name",
			raw:  `[{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":10,"platform":"anthropic","subscription_type":"standard"}],"supported_models":[]}]}]`,
		},
		{
			name: "name drift",
			raw:  `[{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":10,"name":"other","platform":"anthropic","subscription_type":"standard"}],"supported_models":[]}]}]`,
		},
		{
			name: "platform drift",
			raw:  `[{"name":"primary","platforms":[{"platform":"openai","groups":[{"id":10,"name":"vip","platform":"openai","subscription_type":"standard"}],"supported_models":[]}]}]`,
		},
		{
			name: "subscription drift",
			raw:  `[{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"subscription"}],"supported_models":[]}]}]`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, validateRoutingSub2APIChannelGroupContract(groups, json.RawMessage(test.raw)))
		})
	}
}

func TestSub2APIGroupAliasCollisionsOnlyRejectAmbiguousBindings(t *testing.T) {
	for _, raw := range []string{
		`[{"id":10,"name":"20","platform":"anthropic","subscription_type":"standard"},{"id":20,"name":"vip","platform":"anthropic","subscription_type":"standard"},{"id":30,"name":"healthy","platform":"anthropic","subscription_type":"standard"}]`,
		`[{"id":30,"name":"healthy","platform":"anthropic","subscription_type":"standard"},{"id":20,"name":"vip","platform":"anthropic","subscription_type":"standard"},{"id":10,"name":"20","platform":"anthropic","subscription_type":"standard"}]`,
	} {
		groups, err := parseRoutingSub2APIGroups(json.RawMessage(raw))
		require.NoError(t, err, raw)
		assert.Equal(t, routingSub2APIID("10"), groups["10"].ID, raw)
		assert.Equal(t, "vip", groups["20"].Name, raw)
		assert.Equal(t, routingSub2APIID("30"), groups["healthy"].ID, raw)

		selected, err := selectRoutingSub2APIGroups(groups, []string{"healthy"})
		require.NoError(t, err, raw)
		assert.Equal(t, routingSub2APIID("30"), selected["30"].ID, raw)
		assert.Equal(t, routingSub2APIID("30"), selected["healthy"].ID, raw)
		selected, err = selectRoutingSub2APIGroups(groups, []string{"10", "vip"})
		require.NoError(t, err, raw)
		assert.Equal(t, routingSub2APIID("10"), selected["10"].ID, raw)
		assert.Equal(t, routingSub2APIID("20"), selected["20"].ID, raw)
		assert.Equal(t, routingSub2APIID("20"), selected["vip"].ID, raw)

		_, err = selectRoutingSub2APIGroups(groups, []string{"20"})
		require.Error(t, err, raw)
		assert.Contains(t, err.Error(), "ambiguous")
	}
}

func TestSub2APIGroupDuplicateIDRejectsCostAndAdmissionConflicts(t *testing.T) {
	base := `{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1,"ratio":1,"peak_rate_enabled":true,"peak_start":"08:00","peak_end":"10:00","peak_rate_multiplier":1.5,"claude_code_only":false}`
	healthy := `{"id":20,"name":"healthy","platform":"anthropic","subscription_type":"standard","rate_multiplier":1}`
	for _, conflicting := range []string{
		`{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":2,"ratio":1,"peak_rate_enabled":true,"peak_start":"08:00","peak_end":"10:00","peak_rate_multiplier":1.5,"claude_code_only":false}`,
		`{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1,"ratio":2,"peak_rate_enabled":true,"peak_start":"08:00","peak_end":"10:00","peak_rate_multiplier":1.5,"claude_code_only":false}`,
		`{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1,"ratio":1,"peak_rate_enabled":false,"peak_start":"08:00","peak_end":"10:00","peak_rate_multiplier":1.5,"claude_code_only":false}`,
		`{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1,"ratio":1,"peak_rate_enabled":true,"peak_start":"09:00","peak_end":"10:00","peak_rate_multiplier":1.5,"claude_code_only":false}`,
		`{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1,"ratio":1,"peak_rate_enabled":true,"peak_start":"08:00","peak_end":"11:00","peak_rate_multiplier":1.5,"claude_code_only":false}`,
		`{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1,"ratio":1,"peak_rate_enabled":true,"peak_start":"08:00","peak_end":"10:00","peak_rate_multiplier":2,"claude_code_only":false}`,
		`{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1,"ratio":1,"peak_rate_enabled":true,"peak_start":"08:00","peak_end":"10:00","peak_rate_multiplier":1.5,"claude_code_only":true}`,
		`{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1,"ratio":1,"image_rate_multiplier":2,"peak_rate_enabled":true,"peak_start":"08:00","peak_end":"10:00","peak_rate_multiplier":1.5,"claude_code_only":false}`,
	} {
		groups, err := parseRoutingSub2APIGroups(json.RawMessage(`[` + base + `,` + conflicting + `,` + healthy + `]`))
		require.NoError(t, err, conflicting)
		err = validateRoutingSub2APIGroupContract(groups)
		require.Error(t, err, conflicting)
		assert.Contains(t, err.Error(), "inconsistent")
		_, err = selectRoutingSub2APIGroups(groups, []string{"vip"})
		require.Error(t, err, conflicting)
		selected, err := selectRoutingSub2APIGroups(groups, []string{"healthy"})
		require.NoError(t, err, conflicting)
		assert.Contains(t, selected, "20")
		assert.Contains(t, selected, "healthy")
	}
}

func TestSub2APIActionsRejectInvalidOfficialProfileID(t *testing.T) {
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "sub2api-profile-contract-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	invalidProfiles := []struct {
		name string
		data string
	}{
		{name: "missing", data: `{}`},
		{name: "string", data: `{"id":"42"}`},
		{name: "zero", data: `{"id":0}`},
		{name: "negative", data: `{"id":-1}`},
		{name: "decimal", data: `{"id":1.5}`},
		{name: "overflow", data: `{"id":9223372036854775808}`},
	}
	for _, profile := range invalidProfiles {
		for _, action := range []string{"test", "groups"} {
			t.Run(profile.name+" "+action, func(t *testing.T) {
				otherRequests := 0
				restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
					if request.URL.Path == "/api/v1/auth/me" {
						return routingSub2APITestJSONResponse(`{"code":0,"data":` + profile.data + `}`), nil
					}
					otherRequests++
					switch request.URL.Path {
					case "/api/v1/groups/available":
						return routingSub2APITestJSONResponse(`{"code":0,"data":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1}]}`), nil
					case "/api/v1/groups/rates":
						return routingSub2APITestJSONResponse(`{"code":0,"data":{"10":1}}`), nil
					case "/api/v1/channels/available":
						return routingSub2APITestJSONResponse(`{"code":0,"data":[{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard"}],"supported_models":[{"name":"claude-test","platform":"anthropic","pricing":{"billing_mode":"token","input_price":0.000002,"output_price":0.000004,"cache_write_price":0,"cache_read_price":0,"image_output_price":0,"intervals":[]}}]}]}]}`), nil
					default:
						return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
					}
				}))
				binding := model.RoutingChannelBinding{
					ChannelID: 883, UpstreamType: model.RoutingUpstreamTypeSub2API,
					BaseURL: "https://routing.example.com", UpstreamGroup: "vip", Enabled: true,
				}
				require.NoError(t, binding.SetCredentials(model.RoutingCredentials{Sub2APIToken: "jwt-secret"}))
				var err error
				if action == "test" {
					_, err = fetchRoutingPricingPayload(context.Background(), binding)
				} else {
					_, err = fetchRoutingSub2APIGroupDiscoveryPayload(context.Background(), binding)
				}
				require.Error(t, err)
				assert.Zero(t, otherRequests)
				assert.NotContains(t, err.Error(), "jwt-secret")
			})
		}
	}
}

func TestSub2APIJWTManagementEndpointForbiddenIsNotAuthenticationFailure(t *testing.T) {
	for _, path := range []string{
		"/api/v1/auth/me",
		"/api/v1/groups/available",
		"/api/v1/groups/rates",
		"/api/v1/channels/available",
	} {
		for _, httpForbidden := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/http_forbidden=%t", path, httpForbidden), func(t *testing.T) {
				restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(*http.Request) (*http.Response, error) {
					response := routingSub2APITestJSONResponse(`{"code":403,"message":"Backend mode is active. User self-service is disabled."}`)
					if httpForbidden {
						response.StatusCode = http.StatusForbidden
						response.Status = "403 Forbidden"
					}
					return response, nil
				}))

				_, err := routingSub2APIRequest(
					context.Background(),
					model.RoutingChannelBinding{BaseURL: "https://routing.example.com"},
					model.RoutingCredentials{Sub2APIToken: "jwt-token"},
					http.MethodGet,
					path,
					"jwt-token",
					nil,
				)

				require.Error(t, err)
				assert.False(t, routingUpstreamAuthError(err))
				assert.Contains(t, err.Error(), "Backend mode")
			})
		}
	}
}

func TestRoutingSub2APIRequestRequiresOfficialEnvelopeCode(t *testing.T) {
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(*http.Request) (*http.Response, error) {
		return routingSub2APITestJSONResponse(`{"success":true,"data":{"id":1}}`), nil
	}))

	_, err := routingSub2APIRequest(
		context.Background(),
		model.RoutingChannelBinding{BaseURL: "https://routing.example.com"},
		model.RoutingCredentials{Sub2APIToken: "jwt-token"},
		http.MethodGet,
		"/api/v1/auth/me",
		"jwt-token",
		nil,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid sub2api response")
}

func TestParseRoutingSub2APIRatesStrictContract(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    map[string]float64
		wantErr bool
	}{
		{name: "empty bytes", wantErr: true},
		{name: "null", raw: `null`, wantErr: true},
		{name: "empty map", raw: `{}`, want: map[string]float64{}},
		{name: "empty array", raw: `[]`, wantErr: true},
		{name: "empty wrapper", raw: `{"rates":null}`, wantErr: true},
		{name: "numeric map", raw: `{"10":1.1,"20":0.8}`, want: map[string]float64{"10": 1.1, "20": 0.8}},
		{name: "rates wrapper", raw: `{"rates":{"10":1.1}}`, wantErr: true},
		{name: "groups wrapper", raw: `{"groups":{"20":0.8}}`, wantErr: true},
		{name: "array", raw: `[{"id":10,"rate_multiplier":1.1},{"name":"vip","ratio":0.8}]`, wantErr: true},
		{name: "string payload", raw: `"invalid"`, wantErr: true},
		{name: "number payload", raw: `42`, wantErr: true},
		{name: "invalid object shape", raw: `{"unexpected":{"10":1.1}}`, wantErr: true},
		{name: "name key", raw: `{"vip":1.1}`, wantErr: true},
		{name: "zero ID key", raw: `{"0":1.1}`, wantErr: true},
		{name: "negative ID key", raw: `{"-1":1.1}`, wantErr: true},
		{name: "non canonical ID key", raw: `{"01":1.1}`, wantErr: true},
		{name: "string rate", raw: `{"10":"1.1"}`, wantErr: true},
		{name: "zero rate", raw: `{"10":0}`, wantErr: true},
		{name: "negative rate", raw: `{"10":-1}`, wantErr: true},
		{name: "overflowing rate", raw: `{"10":1e309}`, wantErr: true},
		{name: "malformed canonical wrapper does not fall through", raw: `{"rates":{"10":"bad"},"groups":{"10":1.1}}`, wantErr: true},
		{name: "string canonical wrapper does not fall through", raw: `{"rates":"bad","groups":{"10":1.1}}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rates, err := parseRoutingSub2APIRates(json.RawMessage(tt.raw))
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, rates)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, rates)
		})
	}
}

func TestParseRoutingSub2APIRatesForGroupsIsolatesUnselectedValues(t *testing.T) {
	groups, err := parseRoutingSub2APIGroups(json.RawMessage(`[
		{"id":10,"name":"other","platform":"anthropic","subscription_type":"standard"},
		{"id":20,"name":"vip","platform":"anthropic","subscription_type":"standard"}
	]`))
	require.NoError(t, err)
	selected, err := selectRoutingSub2APIGroups(groups, []string{"vip"})
	require.NoError(t, err)

	rates, err := parseRoutingSub2APIRatesForGroups(
		json.RawMessage(`{"10":"invalid","20":1.2}`),
		selected,
	)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"20": 1.2}, rates)

	_, err = parseRoutingSub2APIRates(json.RawMessage(`{"10":"invalid","20":1.2}`))
	require.Error(t, err)
	_, err = parseRoutingSub2APIRatesForGroups(json.RawMessage(`{"other":"invalid","20":1.2}`), selected)
	require.Error(t, err)
}

func TestFetchRoutingSub2APIAccountPricingIsolatesUnselectedGroupRateError(t *testing.T) {
	channelRequests := 0
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/auth/me":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":1}}`), nil
		case "/api/v1/groups/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"id":10,"name":"other","platform":"anthropic","subscription_type":"standard","rate_multiplier":1},{"id":20,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1}]}`), nil
		case "/api/v1/groups/rates":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"10":"invalid","20":1.2}}`), nil
		case "/api/v1/channels/available":
			channelRequests++
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":20,"name":"vip","platform":"anthropic","subscription_type":"standard"}],"supported_models":[{"name":"claude-test","platform":"anthropic","pricing":{"billing_mode":"token","input_price":0.000002,"output_price":0.000004,"cache_write_price":0,"cache_read_price":0,"image_output_price":0,"intervals":[]}}]}]}]}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
	}))
	binding := model.RoutingChannelBinding{
		BaseURL: "https://routing.example.com", UpstreamGroup: "vip",
	}
	credentials := model.RoutingCredentials{Sub2APIToken: "jwt-token"}

	pricing, err := fetchRoutingSub2APIAccountPricingForGroups(
		context.Background(), binding, credentials, []string{"vip"},
	)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"20": 1.2}, pricing.Rates)
	require.Len(t, pricing.Channels, 1)

	_, err = fetchRoutingSub2APIAccountPricingForGroups(
		context.Background(), binding, credentials, []string{"other"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid sub2api group rates response")
	assert.Equal(t, 1, channelRequests)
}

func TestSub2APIOfficialWireShapesRejectLegacyCompatibilityShapes(t *testing.T) {
	for _, raw := range []string{
		`{"groups":[{"id":10,"name":"vip"}]}`,
		`{"items":[{"id":10,"name":"vip"}]}`,
		`{"list":[{"id":10,"name":"vip"}]}`,
		`{"10":1.1}`,
		`[{"id":"10","name":"vip","platform":"anthropic","subscription_type":"standard"}]`,
	} {
		groups, err := parseRoutingSub2APIGroups(json.RawMessage(raw))
		require.Error(t, err, raw)
		assert.Nil(t, groups, raw)
	}

	channel := `[{"name":"primary","platforms":[]}]`
	for _, raw := range []string{
		`{"channels":` + channel + `}`,
		`{"models":` + channel + `}`,
		`{"items":` + channel + `}`,
		`{"list":` + channel + `}`,
	} {
		channels, err := parseRoutingSub2APIChannels(json.RawMessage(raw))
		require.Error(t, err, raw)
		assert.Nil(t, channels, raw)
	}
}

func TestSub2APIChannelsRejectUnversionedFlatPricingShape(t *testing.T) {
	_, err := parseRoutingSub2APIChannels(json.RawMessage(`[
		{"models":["gpt-test"],"input_price":0.000001,"output_price":0.000002}
	]`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "official nested contract")
}

func TestSub2APITokenIntervalsMapOfficialBoundariesToBillingExpression(t *testing.T) {
	channels, err := parseRoutingSub2APIChannels(json.RawMessage(`[
		{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":10,"name":"vip","rate_multiplier":1}],"supported_models":[{
			"name":"claude-sonnet-4-6","platform":"anthropic","pricing":{
				"billing_mode":"token","input_price":0.000003,"output_price":0.000015,
				"cache_write_price":0.00000375,"cache_read_price":0.0000003,"image_output_price":0.00003,
				"per_request_price":null,"intervals":[
					{"min_tokens":0,"max_tokens":200000,"input_price":0.000003,"output_price":0.000015,"cache_write_price":0.00000375,"cache_read_price":0.0000003,"per_request_price":null},
					{"min_tokens":200000,"max_tokens":null,"input_price":0.000006,"output_price":0.0000225,"cache_write_price":0.0000075,"cache_read_price":0.0000006,"per_request_price":null}
				]
			}
		}]}]}
	]`))
	require.NoError(t, err)
	require.Len(t, channels, 1)

	pricing, confidence, score, err := routingSub2APINormalizedPricing(channels[0], 1, true)
	require.NoError(t, err)
	assert.Equal(t, "tiered_expr", pricing.BillingMode)
	assert.Equal(t, model.RoutingCostConfidenceDerived, confidence)
	assert.Equal(t, 0.9, score)
	assert.Contains(t, pricing.BillingExpression, "len > 0 && len <= 200000")
	assert.Contains(t, pricing.BillingExpression, "len > 200000")
	assert.Contains(t, pricing.BillingExpression, model.RoutingCostSub2APIIntervalUnmatchedTier)
	var extras map[string]any
	require.NoError(t, common.Unmarshal(pricing.Extras, &extras))
	assert.Equal(t, model.RoutingCostSub2APIDisplayContractV1, extras["sub2api_contract"])
	assert.Equal(t, true, extras["has_intervals"])

	params := billingexpr.TokenParams{P: 1, C: 1, CR: 1, CC: 1, CC1h: 1, ImgO: 1, Len: 200000}
	firstCost, firstTrace, err := billingexpr.RunExpr(pricing.BillingExpression, params)
	require.NoError(t, err)
	assert.InDelta(t, 55.8, firstCost, 1e-9)
	assert.Equal(t, "ctx_0_200000", firstTrace.MatchedTier)

	params.Len = 200001
	secondCost, secondTrace, err := billingexpr.RunExpr(pricing.BillingExpression, params)
	require.NoError(t, err)
	assert.InDelta(t, 74.1, secondCost, 1e-9)
	assert.Equal(t, "ctx_gt_200000", secondTrace.MatchedTier)

	params.Len = 0
	_, unmatchedTrace, err := billingexpr.RunExpr(pricing.BillingExpression, params)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingCostSub2APIIntervalUnmatchedTier, unmatchedTrace.MatchedTier)

	boundedMax := 100_000
	gapMin := 200_000
	gapPricing := routingSub2APIModelPricing{
		BillingMode: "token",
		Intervals: []routingSub2APIPricingInterval{
			{MinTokens: 10, MaxTokens: &boundedMax, InputPrice: channels[0].OfficialPricing.InputPrice},
			{MinTokens: gapMin, MaxTokens: nil, InputPrice: channels[0].OfficialPricing.InputPrice},
		},
	}
	gapExpression, known, err := routingSub2APITokenPricingExpression(gapPricing)
	require.NoError(t, err)
	assert.True(t, known)
	_, gapTrace, err := billingexpr.RunExpr(gapExpression, billingexpr.TokenParams{P: 1, Len: 150_000})
	require.NoError(t, err)
	assert.Equal(t, model.RoutingCostSub2APIIntervalUnmatchedTier, gapTrace.MatchedTier)
}

func TestSub2APIExplicitZeroTokenPricingRemainsKnown(t *testing.T) {
	zero := 0.0
	flat := routingSub2APIModelPricing{
		BillingMode:      "token",
		InputPrice:       &zero,
		OutputPrice:      &zero,
		CacheWritePrice:  &zero,
		CacheReadPrice:   &zero,
		ImageOutputPrice: &zero,
	}
	flatExpression, flatKnown, err := routingSub2APITokenPricingExpression(flat)
	require.NoError(t, err)
	require.True(t, flatKnown)
	flatCost, flatTrace, err := billingexpr.RunExpr(flatExpression, billingexpr.TokenParams{
		P: 1, C: 1, CR: 1, CC: 1, CC1h: 1, ImgO: 1,
	})
	require.NoError(t, err)
	assert.Zero(t, flatCost)
	assert.Equal(t, "flat", flatTrace.MatchedTier)

	maxTokens := 100_000
	intervalExpression, intervalKnown, err := routingSub2APITokenPricingExpression(routingSub2APIModelPricing{
		BillingMode: "token",
		Intervals: []routingSub2APIPricingInterval{{
			MinTokens: 0, MaxTokens: &maxTokens, InputPrice: &zero,
		}},
	})
	require.NoError(t, err)
	require.True(t, intervalKnown)
	intervalCost, intervalTrace, err := billingexpr.RunExpr(intervalExpression, billingexpr.TokenParams{P: 1, Len: 1})
	require.NoError(t, err)
	assert.Zero(t, intervalCost)
	assert.Equal(t, "ctx_0_100000", intervalTrace.MatchedTier)

	_, unmatchedTrace, err := billingexpr.RunExpr(intervalExpression, billingexpr.TokenParams{P: 1, Len: 100_001})
	require.NoError(t, err)
	assert.Equal(t, model.RoutingCostSub2APIIntervalUnmatchedTier, unmatchedTrace.MatchedTier)
}

func TestSub2APIImageOutputPriceFollowsOfficialChannelOverrideSemantics(t *testing.T) {
	zero := 0.0
	output := 0.000015
	customImageOutput := 0.00003
	tests := []struct {
		name             string
		imageOutputPrice *float64
		wantCost         float64
	}{
		{name: "nil is explicit free", wantCost: 900},
		{name: "explicit zero is free", imageOutputPrice: &zero, wantCost: 900},
		{name: "nonzero is independently priced", imageOutputPrice: &customImageOutput, wantCost: 2100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expression, known, err := routingSub2APITokenPricingExpression(routingSub2APIModelPricing{
				BillingMode:      "token",
				InputPrice:       &zero,
				OutputPrice:      &output,
				CacheWritePrice:  &zero,
				CacheReadPrice:   &zero,
				ImageOutputPrice: tt.imageOutputPrice,
			})
			require.NoError(t, err)
			require.True(t, known)
			assert.Contains(t, expression, "img_o *")

			usage := &dto.Usage{
				CompletionTokens: 100,
				CompletionTokenDetails: dto.OutputTokenDetails{
					ImageTokens: 40,
				},
			}
			params := service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression))
			assert.Equal(t, float64(60), params.C)
			assert.Equal(t, float64(40), params.ImgO)
			cost, _, err := billingexpr.RunExpr(expression, params)
			require.NoError(t, err)
			assert.InDelta(t, tt.wantCost, cost, 1e-9)
		})
	}
}

func TestSub2APIUnsupportedOrIncompleteTierSemanticsFailClosed(t *testing.T) {
	perRequestPrice := 0.08
	imageTierPrice := 0.04
	_, _, _, err := routingSub2APINormalizedPricing(routingSub2APIChannel{
		PerTokenPrices: true,
		OfficialPricing: &routingSub2APIModelPricing{
			BillingMode:     "per_request",
			PerRequestPrice: &perRequestPrice,
			Intervals: []routingSub2APIPricingInterval{{
				TierLabel: "1K", PerRequestPrice: &imageTierPrice,
			}},
		},
	}, 1, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be mapped safely")

	_, _, _, err = routingSub2APINormalizedPricing(routingSub2APIChannel{
		PerTokenPrices: true,
		OfficialPricing: &routingSub2APIModelPricing{
			BillingMode:     "image",
			PerRequestPrice: &perRequestPrice,
		},
	}, 1, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "group-specific size and multiplier semantics")

	maxTokens := 100_000
	inputPrice := 0.000003
	overlapMin := 50_000
	_, _, _, err = routingSub2APINormalizedPricing(routingSub2APIChannel{
		PerTokenPrices: true,
		OfficialPricing: &routingSub2APIModelPricing{
			BillingMode: "token",
			Intervals: []routingSub2APIPricingInterval{
				{MinTokens: 0, MaxTokens: &maxTokens, InputPrice: &inputPrice},
				{MinTokens: overlapMin, MaxTokens: nil, InputPrice: &inputPrice},
			},
		},
	}, 1, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlap")

	outputPrice := 0.000015
	_, _, _, err = routingSub2APINormalizedPricing(routingSub2APIChannel{
		PerTokenPrices: true,
		OfficialPricing: &routingSub2APIModelPricing{
			BillingMode: "token",
			InputPrice:  &inputPrice,
			OutputPrice: &outputPrice,
		},
	}, 1, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "omits inherited price dimensions")
}

func TestSub2APINormalizedPriceScalingOverflowFailsClosed(t *testing.T) {
	hugePrice := 1e308
	zeroPrice := 0.0
	_, _, _, err := routingSub2APINormalizedPricing(routingSub2APIChannel{
		PerTokenPrices: true,
		OfficialPricing: &routingSub2APIModelPricing{
			BillingMode:     "token",
			InputPrice:      &hugePrice,
			OutputPrice:     &zeroPrice,
			CacheWritePrice: &zeroPrice,
			CacheReadPrice:  &zeroPrice,
		},
	}, 1, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overflows normalized units")
}

func TestSub2APIDuplicatePricingDedupesOnlyWhenIdentical(t *testing.T) {
	price := func(output float64) routingSub2APIChannel {
		input := 0.000003
		cacheWrite := 0.00000375
		cacheRead := 0.0000003
		return routingSub2APIChannel{
			Models:         []string{"claude-sonnet-4-6"},
			Groups:         []string{"10", "vip"},
			Platform:       "anthropic",
			PerTokenPrices: true,
			OfficialPricing: &routingSub2APIModelPricing{
				BillingMode:     "token",
				InputPrice:      &input,
				OutputPrice:     &output,
				CacheWritePrice: &cacheWrite,
				CacheReadPrice:  &cacheRead,
			},
		}
	}
	payload := routingCostAccountPayload{
		ObservedTime:   1,
		EffectiveTime:  1,
		ExpiresTime:    2,
		PricingVersion: "fixture",
		SyncStatus:     model.RoutingUpstreamSyncStatusSuccess,
		Sub2API: &routingSub2APIAccountPricing{
			Groups: map[string]routingSub2APIGroup{
				"vip": {ID: routingSub2APIID("10"), Name: "vip", RateMultiplier: 1},
			},
			Rates: map[string]float64{"10": 1},
		},
	}
	binding := model.RoutingChannelBinding{ChannelID: 1, UpstreamGroup: "vip"}

	payload.Sub2API.Channels = []routingSub2APIChannel{price(0.000015), price(0.000015)}
	writes, err := routingSub2APICostVersionWrites(binding, nil, payload)
	require.NoError(t, err)
	require.Len(t, writes, 1)

	differentPlatform := price(0.000015)
	differentPlatform.Platform = "openai"
	payload.Sub2API.Channels = []routingSub2APIChannel{price(0.000015), differentPlatform}
	writes, err = routingSub2APICostVersionWrites(binding, nil, payload)
	require.NoError(t, err)
	require.Len(t, writes, 1)

	payload.Sub2API.Channels = []routingSub2APIChannel{price(0.000015), price(0.000020)}
	_, err = routingSub2APICostVersionWrites(binding, nil, payload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicting pricing")

	payload.Sub2API.Channels = []routingSub2APIChannel{price(0.000015)}
	payload.Sub2API.Groups["vip"] = routingSub2APIGroup{ID: routingSub2APIID("10"), Name: "vip"}
	payload.Sub2API.Rates = nil
	_, err = routingSub2APICostVersionWrites(binding, nil, payload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid group ratio")
}

func TestSub2APIClaudeCodeOnlyGroupRequiresExplicitBinding(t *testing.T) {
	input := 0.000003
	output := 0.000015
	zero := 0.0
	payload := routingCostAccountPayload{
		ObservedTime: 1, EffectiveTime: 1, ExpiresTime: 2,
		PricingVersion: "fixture", SyncStatus: model.RoutingUpstreamSyncStatusSuccess,
		Sub2API: &routingSub2APIAccountPricing{
			Groups: map[string]routingSub2APIGroup{
				"10":  {ID: routingSub2APIID("10"), Name: "claude-code", ClaudeCodeOnly: true, RateMultiplier: 1},
				"vip": {ID: routingSub2APIID("10"), Name: "claude-code", ClaudeCodeOnly: true, RateMultiplier: 1},
			},
			Rates: map[string]float64{"10": 1},
			Channels: []routingSub2APIChannel{{
				Models: []string{"claude-test"}, Groups: []string{"10", "vip"}, Platform: "anthropic",
				PerTokenPrices: true,
				OfficialPricing: &routingSub2APIModelPricing{
					BillingMode: "token", InputPrice: &input, OutputPrice: &output,
					CacheWritePrice: &zero, CacheReadPrice: &zero, ImageOutputPrice: &zero,
				},
			}},
		},
	}
	binding := model.RoutingChannelBinding{
		ChannelID: 1, UpstreamType: model.RoutingUpstreamTypeSub2API,
		UpstreamGroup: "vip",
	}

	_, err := routingSub2APICostVersionWrites(binding, nil, payload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restricted to Claude Code")

	binding.ServesClaudeCode = true
	writes, err := routingSub2APICostVersionWrites(binding, nil, payload)
	require.NoError(t, err)
	require.Len(t, writes, 1)
}

func TestSub2APILegacySnapshotPreservesPerRequestPricing(t *testing.T) {
	perRequestPrice := 0.08
	normalized, confidence, _, err := routingSub2APINormalizedPricing(
		routingSub2APIChannel{
			PerTokenPrices: true,
			OfficialPricing: &routingSub2APIModelPricing{
				BillingMode: "per_request", PerRequestPrice: &perRequestPrice,
			},
		},
		1.25,
		true,
	)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingCostConfidenceExact, confidence)
	var extras map[string]any
	require.NoError(t, common.Unmarshal(normalized.Extras, &extras))
	assert.Equal(t, "usd_per_request", extras["price_unit"])

	snapshot, err := routingSub2APIChannelSnapshot(
		1,
		"image-model",
		1.25,
		true,
		routingSub2APIChannel{
			PerTokenPrices: true,
			OfficialPricing: &routingSub2APIModelPricing{
				BillingMode: "per_request", PerRequestPrice: &perRequestPrice,
			},
		},
		1,
	)
	require.NoError(t, err)
	assert.Equal(t, 1, snapshot.QuotaType)
	assert.Equal(t, "per_request", snapshot.BillingMode)
	assert.Equal(t, perRequestPrice, snapshot.ModelPrice)
	assert.Zero(t, snapshot.BaseRatio)
}

func TestRoutingManagementCredentialBoundaryRejectsGatewayOnlyBindings(t *testing.T) {
	userID := 42
	tests := []struct {
		name        string
		binding     model.RoutingChannelBinding
		credentials model.RoutingCredentials
		wantError   string
	}{
		{
			name:        "newapi requires user ID",
			binding:     model.RoutingChannelBinding{UpstreamType: model.RoutingUpstreamTypeNewAPI},
			credentials: model.RoutingCredentials{NewAPIAccessToken: "access-token"},
			wantError:   "user ID is required",
		},
		{
			name:        "newapi gateway key is not an access token",
			binding:     model.RoutingChannelBinding{UpstreamType: model.RoutingUpstreamTypeNewAPI, NewAPIUserID: &userID},
			credentials: model.RoutingCredentials{GatewayAPIKey: "gateway-key"},
			wantError:   "access token is required",
		},
		{
			name:        "sub2api gateway key is not management JWT",
			binding:     model.RoutingChannelBinding{UpstreamType: model.RoutingUpstreamTypeSub2API},
			credentials: model.RoutingCredentials{GatewayAPIKey: "gateway-key"},
			wantError:   "JWT or email and password",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fetchRoutingCostAccountPayload(
				context.Background(), tt.binding, tt.credentials, smart_routing_setting.SmartRoutingSetting{}, nil,
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
		})
	}
}

func TestRoutingUpstreamAccountIdentityRejectsSub2APICredentialDerivedIdentity(t *testing.T) {
	binding := model.RoutingChannelBinding{
		UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL:      "https://routing.example.com",
	}
	_, err := routingUpstreamAccountIdentity(binding, model.RoutingCredentials{
		Sub2APIToken: "jwt-token", GatewayAPIKey: "gateway-one",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authenticated user profile")

	_, err = routingUpstreamAccountIdentity(binding, model.RoutingCredentials{
		Sub2APIEmail: "same@example.com", Sub2APIPassword: "password",
	})
	require.Error(t, err)
}

func TestSub2APIProfileIdentitySurvivesJWTRotation(t *testing.T) {
	binding := model.RoutingChannelBinding{
		UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL:      "https://ROUTING.example.com/api/",
	}
	profile := routingSub2APIUserProfile{
		ID: 42, Email: "owner@example.com", Username: "owner",
	}
	first, err := routingSub2APIProfileAccountIdentity(
		binding,
		profile,
	)
	require.NoError(t, err)
	second, err := routingSub2APIProfileAccountIdentity(
		binding,
		profile,
	)
	require.NoError(t, err)

	assert.Equal(t, first.AccountKey, second.AccountKey)
	assert.Equal(t, first.StableIdentity, second.StableIdentity)
	assert.Contains(t, first.MaskedIdentity, "user 42")
	assert.NotContains(t, first.MaskedIdentity, "owner@example.com")
	assert.NotContains(t, first.MaskedIdentity, "/ owner")
	assert.NotContains(t, first.MaskedIdentity, "jwt-first")

	differentUser, err := routingSub2APIProfileAccountIdentity(
		binding,
		routingSub2APIUserProfile{ID: 43, Email: "owner@example.com"},
	)
	require.NoError(t, err)
	assert.NotEqual(t, first.StableIdentity, differentUser.StableIdentity)

	_, err = routingSub2APIProfileAccountIdentity(binding, routingSub2APIUserProfile{Email: "owner@example.com"})
	require.Error(t, err)
}

func TestSub2APIProfileIdentityCanonicalizesUpstreamInstance(t *testing.T) {
	profile := routingSub2APIUserProfile{ID: 42}
	identity := func(baseURL string) (routingCostAccountIdentity, error) {
		return routingSub2APIProfileAccountIdentity(model.RoutingChannelBinding{
			UpstreamType: model.RoutingUpstreamTypeSub2API,
			BaseURL:      baseURL,
		}, profile)
	}

	canonical, err := identity("https://EXAMPLE.com.:443/api/")
	require.NoError(t, err)
	equivalent, err := identity("https://example.com/api")
	require.NoError(t, err)
	assert.Equal(t, canonical.AccountKey, equivalent.AccountKey)

	differentPort, err := identity("https://example.com:8443/api")
	require.NoError(t, err)
	assert.NotEqual(t, canonical.AccountKey, differentPort.AccountKey)
	differentPath, err := identity("https://example.com/other")
	require.NoError(t, err)
	assert.NotEqual(t, canonical.AccountKey, differentPath.AccountKey)

	for _, invalid := range []string{
		"http://example.com/api",
		"https://user:password@example.com/api",
		"https://example.com/api?tenant=one",
		"https://example.com/api#fragment",
	} {
		_, err := identity(invalid)
		require.Error(t, err, invalid)
	}
}

func TestParseSub2APIUserProfilePreservesAuthoritativeIdentityAndBalance(t *testing.T) {
	profile, err := parseRoutingSub2APIUserProfile(json.RawMessage(
		`{"id":42,"email":" owner@example.com ","username":" owner ","balance":14.25}`,
	))

	require.NoError(t, err)
	assert.Equal(t, int64(42), profile.ID)
	assert.Equal(t, "owner@example.com", profile.Email)
	assert.Equal(t, "owner", profile.Username)
	require.NotNil(t, profile.Balance)
	assert.Equal(t, 14.25, *profile.Balance)

	for _, raw := range []string{
		`{"balance":1}`,
		`{"id":"42","balance":0}`,
		`{"id":0,"balance":1}`,
		`{"id":-1,"balance":1}`,
		`{"id":1.5,"balance":1}`,
		`{"id":"9223372036854775808","balance":1}`,
		`{"id":true,"balance":1}`,
	} {
		_, err := parseRoutingSub2APIUserProfile(json.RawMessage(raw))
		require.Error(t, err, raw)
	}
}

func TestSub2APIBalanceAlwaysUsesJWTAccountProfile(t *testing.T) {
	tests := []struct {
		name              string
		credentials       model.RoutingCredentials
		jwt               string
		wantPath          string
		wantAuthorization string
		response          string
		wantBalance       float64
	}{
		{
			name:        "management JWT uses auth me envelope",
			credentials: model.RoutingCredentials{Sub2APIToken: "jwt-token"},
			jwt:         "jwt-token", wantPath: "/api/v1/auth/me", wantAuthorization: "Bearer jwt-token",
			response: `{"code":0,"data":{"id":1,"balance":12.5,"run_mode":"standard"}}`, wantBalance: 12.5,
		},
		{
			name:        "gateway key remains supplemental and is ignored for account balance",
			credentials: model.RoutingCredentials{Sub2APIToken: "jwt-token", GatewayAPIKey: "sk-gateway"},
			jwt:         "jwt-token", wantPath: "/api/v1/auth/me", wantAuthorization: "Bearer jwt-token",
			response: `{"code":0,"data":{"id":1,"balance":14.25,"run_mode":"standard"}}`, wantBalance: 14.25,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
				assert.Equal(t, tt.wantPath, request.URL.Path)
				assert.Equal(t, tt.wantAuthorization, request.Header.Get("Authorization"))
				return routingSub2APITestJSONResponse(tt.response), nil
			}))
			balance, known, err := fetchRoutingSub2APIBalanceValue(
				context.Background(),
				model.RoutingChannelBinding{BaseURL: "https://routing.example.com"},
				tt.credentials,
				tt.jwt,
			)
			require.NoError(t, err)
			require.True(t, known)
			assert.Equal(t, tt.wantBalance, balance)
		})
	}
}

func TestSub2APIGroupDiscoveryDoesNotRequireAPreselectedGroup(t *testing.T) {
	requests := make(map[string]int)
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		requests[request.URL.Path]++
		switch request.URL.Path {
		case "/api/v1/auth/me":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":1,"balance":14.25}}`), nil
		case "/api/v1/groups/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"id":10,"name":"standard","platform":"anthropic","subscription_type":"standard","rate_multiplier":1},{"id":20,"name":"vip","platform":"anthropic","subscription_type":"subscription","rate_multiplier":1.2}]}`), nil
		case "/api/v1/groups/rates":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"10":1,"20":1.1}}`), nil
		case "/api/v1/channels/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":10,"name":"standard","platform":"anthropic","subscription_type":"standard","rate_multiplier":1},{"id":20,"name":"vip","platform":"anthropic","subscription_type":"subscription","rate_multiplier":1.2}],"supported_models":[{"name":"claude-test","platform":"anthropic","pricing":{"billing_mode":"token","input_price":0.000003,"output_price":0.000015,"cache_write_price":0,"cache_read_price":0,"image_output_price":0,"intervals":[]}}]}]}]}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
	}))
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
	binding := model.RoutingChannelBinding{
		ChannelID: 1, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://routing.example.com",
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{Sub2APIToken: "jwt-token"}))

	payload, err := fetchRoutingSub2APIGroupDiscoveryPayload(context.Background(), binding)

	require.NoError(t, err)
	groups, total := routingPricingGroups(payload)
	assert.Equal(t, []string{"standard", "vip"}, groups)
	assert.Equal(t, 2, total)
	assert.Empty(t, payload.Data)
	assert.Equal(t, "standard", payload.Sub2APIGroupMeta["standard"].SubscriptionType)
	assert.Equal(t, "subscription", payload.Sub2APIGroupMeta["vip"].SubscriptionType)
	assert.Zero(t, requests["/api/v1/groups/rates"])
	assert.Zero(t, requests["/api/v1/channels/available"])
}

func TestSub2APIIndependentGroupDiscoveryReusesManagedJWTRetry(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	loginCount := 0
	profileAuthorizations := make([]string, 0, 2)
	groupAuthorizations := make([]string, 0, 1)
	ratesRequests := 0
	channelRequests := 0
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		authorization := request.Header.Get("Authorization")
		switch request.URL.Path {
		case "/api/v1/auth/login":
			loginCount++
			return routingSub2APITestJSONResponse(fmt.Sprintf(
				`{"code":0,"data":{"token":"jwt-%d","expires_in":86400}}`, loginCount,
			)), nil
		case "/api/v1/auth/me":
			profileAuthorizations = append(profileAuthorizations, authorization)
			if authorization == "Bearer jwt-1" {
				response := routingSub2APITestJSONResponse(`{"code":401,"message":"expired token"}`)
				response.StatusCode = http.StatusUnauthorized
				response.Status = "401 Unauthorized"
				return response, nil
			}
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":1,"balance":99}}`), nil
		case "/api/v1/groups/available":
			groupAuthorizations = append(groupAuthorizations, authorization)
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"subscription","rate_multiplier":1.25,"claude_code_only":true}]}`), nil
		case "/api/v1/groups/rates":
			ratesRequests++
			return routingSub2APITestJSONResponse(`{"code":500,"message":"must not be called"}`), nil
		case "/api/v1/channels/available":
			channelRequests++
			return routingSub2APITestJSONResponse(`{"code":500,"message":"must not be called"}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
	}))

	credentials := model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "password",
	}
	binding := model.RoutingChannelBinding{
		ChannelID: 882, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://routing.example.com",
	}
	require.NoError(t, binding.SetCredentials(credentials))

	payload, err := fetchRoutingSub2APIGroupDiscoveryPayload(context.Background(), binding)
	require.NoError(t, err)
	groups, total := routingPricingGroups(payload)
	assert.Equal(t, []string{"vip"}, groups)
	assert.Equal(t, 1, total)
	assert.Equal(t, routingSub2APIGroupMetadata{
		ID: "10", Name: "vip", Platform: "anthropic", SubscriptionType: "subscription", ClaudeCodeOnly: true,
	}, payload.Sub2APIGroupMeta["vip"])
	assert.Equal(t, 2, loginCount)
	assert.Equal(t, []string{"Bearer jwt-1", "Bearer jwt-2"}, profileAuthorizations)
	assert.Equal(t, []string{"Bearer jwt-2"}, groupAuthorizations)
	assert.Zero(t, ratesRequests)
	assert.Zero(t, channelRequests)
}

func TestNewAPIAccountContractUsesAuthenticatedGroupsAndModelsAsAuthority(t *testing.T) {
	userID := 42
	groupName := "vip plus/slash"
	requests := make(map[string]int)
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "Bearer account-token", request.Header.Get("Authorization"))
		assert.Equal(t, "42", request.Header.Get("New-Api-User"))
		requests[request.URL.Path]++
		switch request.URL.Path {
		case "/api/status":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"quota_per_unit":1000000}}`), nil
		case "/api/user/self/groups":
			return routingSub2APITestJSONResponse(`{
				"success":true,
				"data":{
					"vip plus/slash":{"ratio":1.25,"desc":"Account group"},
					"free":{"ratio":0,"desc":"Free group"},
					"auto":{"ratio":"auto","desc":"Automatic group"}
				}
			}`), nil
		case "/api/user/models":
			assert.Equal(t, groupName, request.URL.Query().Get("group"))
			return routingSub2APITestJSONResponse(`{"success":true,"data":["gpt-account"]}`), nil
		case "/api/pricing":
			return routingSub2APITestJSONResponse(`{
				"success":true,
				"group_ratio":{"public":99},
				"usable_group":{"public":"Anonymous"},
				"data":[
					{"model_name":"gpt-account","quota_type":0,"model_ratio":2,"completion_ratio":3,"enable_groups":["public"]},
					{"model_name":"gpt-public","quota_type":0,"model_ratio":1,"completion_ratio":1,"enable_groups":["public"]}
				]
			}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"success":false,"message":"unexpected endpoint"}`), nil
		}
	}))
	binding := model.RoutingChannelBinding{
		ChannelID: 1, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://routing.example.com", UpstreamGroup: groupName, NewAPIUserID: &userID,
	}
	credentials := model.RoutingCredentials{NewAPIAccessToken: "account-token"}

	payload, err := fetchRoutingNewAPIAccountPricingPayload(
		context.Background(), binding, credentials, []string{groupName},
	)

	require.NoError(t, err)
	assert.Equal(t, 1, requests["/api/user/self/groups"])
	assert.Equal(t, 1, requests["/api/user/models"])
	assert.Equal(t, 1, requests["/api/pricing"])
	assert.Equal(t, 1.25, payload.GroupRatio[groupName])
	freeRatio, exists := payload.GroupRatio["free"]
	require.True(t, exists)
	assert.Zero(t, freeRatio)
	assert.NotContains(t, payload.GroupRatio, "public")
	assert.NotContains(t, payload.GroupRatio, "auto")
	require.Len(t, payload.Data, 1)
	assert.Equal(t, "gpt-account", payload.Data[0].ModelName)
	assert.Equal(t, []string{groupName}, payload.Data[0].EnableGroups)
}

func TestNewAPIGatewayServingContractIntersectsAccountModels(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}))
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "newapi-serving-contract-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	userID := 42
	requests := make(map[string]int)
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		requests[request.URL.Path]++
		if request.URL.Path == "/v1/models" {
			assert.Equal(t, "Bearer gateway-key", request.Header.Get("Authorization"))
			assert.Empty(t, request.Header.Get("New-Api-User"))
			return routingSub2APITestJSONResponse(`{"success":true,"object":"list","data":[{"id":"gpt-serving"}]}`), nil
		}
		assert.Equal(t, "Bearer account-token", request.Header.Get("Authorization"))
		assert.Equal(t, "42", request.Header.Get("New-Api-User"))
		switch request.URL.Path {
		case "/api/status":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"quota_per_unit":1000000}}`), nil
		case "/api/user/self":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"quota":5000000}}`), nil
		case "/api/user/self/groups":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"vip":{"ratio":1}}}`), nil
		case "/api/user/models":
			return routingSub2APITestJSONResponse(`{"success":true,"data":["gpt-serving","gpt-management-only"]}`), nil
		case "/api/pricing":
			return routingSub2APITestJSONResponse(`{"success":true,"data":[{"model_name":"gpt-serving","quota_type":0,"model_ratio":1,"completion_ratio":1},{"model_name":"gpt-management-only","quota_type":0,"model_ratio":2,"completion_ratio":1}]}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"success":false,"message":"unexpected endpoint"}`), nil
		}
	}))

	binding := model.RoutingChannelBinding{
		ChannelID: 884, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://routing.example.com", UpstreamGroup: "vip", NewAPIUserID: &userID,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{
		NewAPIAccessToken: "account-token",
		GatewayAPIKey:     "gateway-key",
	}))

	payload, err := fetchRoutingPricingPayload(context.Background(), binding)

	require.NoError(t, err)
	require.Len(t, payload.Data, 1)
	assert.Equal(t, "gpt-serving", payload.Data[0].ModelName)
	assert.Equal(t, 1, requests["/v1/models"])
	assert.Equal(t, 1, requests["/api/user/models"])
}

func TestNewAPIGatewayServingContractFailsClosedWithoutGatewayKey(t *testing.T) {
	called := false
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return routingSub2APITestJSONResponse(`{"success":true,"data":[]}`), nil
	}))

	_, err := fetchRoutingNewAPIGatewayModels(
		context.Background(),
		model.RoutingChannelBinding{BaseURL: "https://routing.example.com"},
		model.RoutingCredentials{NewAPIAccessToken: "account-token"},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "gateway API key is required")
	assert.False(t, called)
}

func TestNewAPIAccountContractIsolatesInvalidGroupPricing(t *testing.T) {
	userID := 42
	catalogAuthenticated := false
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/status":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"quota_per_unit":1000000}}`), nil
		case "/api/user/self/groups":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"basic":{"ratio":1},"private":{"ratio":1.5},"auto":{"ratio":"auto"}}}`), nil
		case "/api/user/models":
			if request.URL.Query().Get("group") == "private" {
				return routingSub2APITestJSONResponse(`{"success":true,"data":["private-model"]}`), nil
			}
			return routingSub2APITestJSONResponse(`{"success":true,"data":["basic-model"]}`), nil
		case "/api/pricing":
			response := routingSub2APITestJSONResponse(`{"success":true,"data":[{"model_name":"basic-model","quota_type":0,"model_ratio":1,"completion_ratio":1}]}`)
			if catalogAuthenticated {
				response.Header.Set("Auth-Version", "2")
			}
			return response, nil
		default:
			return routingSub2APITestJSONResponse(`{"success":false,"message":"unexpected endpoint"}`), nil
		}
	}))
	binding := model.RoutingChannelBinding{
		ChannelID: 1, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://routing.example.com", NewAPIUserID: &userID,
	}
	payload, err := fetchRoutingNewAPIAccountPricingPayload(
		context.Background(), binding, model.RoutingCredentials{NewAPIAccessToken: "account-token"},
		[]string{"private", "basic", "auto"},
	)
	require.NoError(t, err)
	assert.Contains(t, payload.AccountGroupErrors["private"], "no reliable price")
	assert.Contains(t, payload.AccountGroupErrors["private"], "pricing.requireAuth=true")
	assert.Contains(t, payload.AccountGroupErrors["auto"], "no stable numeric ratio")

	catalogAuthenticated = true
	authenticatedPayload, err := fetchRoutingNewAPIAccountPricingPayload(
		context.Background(), binding, model.RoutingCredentials{NewAPIAccessToken: "account-token"},
		[]string{"private"},
	)
	require.NoError(t, err)
	assert.Contains(t, authenticatedPayload.AccountGroupErrors["private"], "authenticated pricing directory")
	assert.NotContains(t, authenticatedPayload.AccountGroupErrors["private"], "pricing.requireAuth=true")

	accountPayload := routingCostAccountPayload{
		ObservedTime: 1, EffectiveTime: 1, ExpiresTime: 2,
		PricingVersion: payload.PricingVersion, SyncStatus: model.RoutingUpstreamSyncStatusSuccess,
		NewAPI: &payload,
	}
	binding.UpstreamGroup = "basic"
	writes, err := routingNewAPICostVersionWrites(binding, nil, accountPayload)
	require.NoError(t, err)
	require.Len(t, writes, 1)
	assert.Equal(t, "basic-model", writes[0].UpstreamModel)
	assert.Equal(t, 1.0, *writes[0].Pricing.GroupRatio)

	binding.UpstreamGroup = "private"
	_, err = routingNewAPICostVersionWrites(binding, nil, accountPayload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no reliable price")
}

func TestNewAPIMissingQuotaIsNotTreatedAsKnownZeroBalance(t *testing.T) {
	userID := 42
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/status":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"quota_per_unit":1000000}}`), nil
		case "/api/user/self":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"used_quota":100}}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"success":false,"message":"unexpected endpoint"}`), nil
		}
	}))

	balance, known, err := fetchRoutingUpstreamBalanceValue(
		context.Background(),
		model.RoutingChannelBinding{
			UpstreamType: model.RoutingUpstreamTypeNewAPI,
			BaseURL:      "https://routing.example.com", NewAPIUserID: &userID,
		},
		model.RoutingCredentials{NewAPIAccessToken: "account-token"},
	)

	require.NoError(t, err)
	assert.False(t, known)
	assert.Zero(t, balance)
}

func TestNewAPIFreeGroupRatioIsKnownZeroCost(t *testing.T) {
	pricing, confidence, score, err := routingNewAPINormalizedPricing(
		routingPricingItem{
			ModelName: "free-model", QuotaType: 0,
			ModelRatio: routingCostFloatPointer(0), CompletionRatio: routingCostFloatPointer(1),
		},
		0,
		true,
		common.QuotaPerUnit,
	)

	require.NoError(t, err)
	assert.Equal(t, model.RoutingCostConfidenceExact, confidence)
	assert.Equal(t, 1.0, score)
	require.NotNil(t, pricing.GroupRatio)
	assert.Zero(t, *pricing.GroupRatio)

	for _, invalidRatio := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, _, _, err := routingNewAPINormalizedPricing(
			routingPricingItem{
				ModelName: "invalid-group-model", QuotaType: 0,
				ModelRatio: routingCostFloatPointer(1), CompletionRatio: routingCostFloatPointer(1),
			},
			invalidRatio,
			true,
			common.QuotaPerUnit,
		)
		require.Error(t, err)
	}
}

func TestNewAPIQuotaPerUnitConvertsBalanceAndTokenPricingAcrossInstances(t *testing.T) {
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500_000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })

	pricing, confidence, score, err := routingNewAPINormalizedPricing(
		routingPricingItem{
			ModelName:       "gpt-qpu",
			QuotaType:       0,
			ModelRatio:      routingCostFloatPointer(2),
			CompletionRatio: routingCostFloatPointer(3),
		},
		1.25,
		true,
		2_000_000,
	)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingCostConfidenceExact, confidence)
	assert.Equal(t, 1.0, score)
	require.NotNil(t, pricing.BaseRatio)
	require.NotNil(t, pricing.InputCostPerMillion)
	require.NotNil(t, pricing.OutputCostPerMillion)
	require.NotNil(t, pricing.CacheWriteCostPerMillion)
	require.NotNil(t, pricing.CacheWrite1hCostPerMillion)
	require.NotNil(t, pricing.AudioInputCostPerMillion)
	require.NotNil(t, pricing.AudioOutputCostPerMillion)
	assert.Equal(t, 0.5, *pricing.BaseRatio)
	assert.Equal(t, 1.0, *pricing.InputCostPerMillion)
	assert.Equal(t, 3.0, *pricing.OutputCostPerMillion)
	assert.Equal(t, 1.25, *pricing.CacheWriteCostPerMillion)
	assert.Equal(t, 2.0, *pricing.CacheWrite1hCostPerMillion)
	assert.Equal(t, 1.0, *pricing.AudioInputCostPerMillion)
	assert.Equal(t, 1.0, *pricing.AudioOutputCostPerMillion)
	var extras map[string]any
	require.NoError(t, common.Unmarshal(pricing.Extras, &extras))
	assert.Equal(t, model.RoutingCostCatalogScopeNewAPIPricing, extras["catalog_scope"])
	assert.Equal(t, false, extras["always_uncatalogued_surcharge"])

	searchPricing, _, _, err := routingNewAPINormalizedPricing(
		routingPricingItem{
			ModelName: "gpt-4o-search-preview", QuotaType: 0,
			ModelRatio: routingCostFloatPointer(1), CompletionRatio: routingCostFloatPointer(1),
		},
		1,
		true,
		2_000_000,
	)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(searchPricing.Extras, &extras))
	assert.Equal(t, true, extras["always_uncatalogued_surcharge"])

	userID := 42
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/status":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"quota_per_unit":2000000}}`), nil
		case "/api/user/self":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"quota":5000000}}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"success":false,"message":"unexpected endpoint"}`), nil
		}
	}))
	balance, known, err := fetchRoutingUpstreamBalanceValue(
		context.Background(),
		model.RoutingChannelBinding{
			UpstreamType: model.RoutingUpstreamTypeNewAPI,
			BaseURL:      "https://routing.example.com",
			NewAPIUserID: &userID,
		},
		model.RoutingCredentials{NewAPIAccessToken: "account-token"},
	)
	require.NoError(t, err)
	assert.True(t, known)
	assert.Equal(t, 2.5, balance)
}

func TestNewAPIQuotaPerUnitFailsClosedWhenStatusIsMissingOrInvalid(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing", body: `{"success":true,"data":{}}`},
		{name: "null", body: `{"success":true,"data":{"quota_per_unit":null}}`},
		{name: "zero", body: `{"success":true,"data":{"quota_per_unit":0}}`},
		{name: "negative", body: `{"success":true,"data":{"quota_per_unit":-1}}`},
		{name: "overflow", body: `{"success":true,"data":{"quota_per_unit":1e309}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
				assert.Equal(t, "/api/status", request.URL.Path)
				return routingSub2APITestJSONResponse(tt.body), nil
			}))

			_, err := fetchRoutingNewAPIQuotaPerUnit(
				context.Background(),
				model.RoutingChannelBinding{BaseURL: "https://routing.example.com"},
				model.RoutingCredentials{},
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "status")
		})
	}
}

func TestNewAPIGroupDiscoveryDoesNotDependOnStatus(t *testing.T) {
	statusCalled := false
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/status":
			statusCalled = true
			return routingSub2APITestJSONResponse(`{"success":false,"message":"status unavailable"}`), nil
		case "/api/user/self/groups":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"vip":{"ratio":1.25}}}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"success":false,"message":"unexpected endpoint"}`), nil
		}
	}))

	payload, err := fetchRoutingNewAPIAccountPricingPayload(
		context.Background(),
		model.RoutingChannelBinding{BaseURL: "https://routing.example.com"},
		model.RoutingCredentials{},
		nil,
	)
	require.NoError(t, err)
	assert.False(t, statusCalled)
	assert.Equal(t, 1.25, payload.GroupRatio["vip"])
	assert.Zero(t, payload.QuotaPerUnit)
}

func TestNewAPITokenPricingRequiresCompletionRatioButPreservesExplicitZero(t *testing.T) {
	_, _, _, err := routingNewAPINormalizedPricing(
		routingPricingItem{
			ModelName:  "missing-completion",
			QuotaType:  0,
			ModelRatio: routingCostFloatPointer(1),
		},
		1,
		true,
		common.QuotaPerUnit,
	)
	require.Error(t, err)

	pricing, confidence, _, err := routingNewAPINormalizedPricing(
		routingPricingItem{
			ModelName:       "free-output",
			QuotaType:       0,
			ModelRatio:      routingCostFloatPointer(2),
			CompletionRatio: routingCostFloatPointer(0),
		},
		1,
		true,
		common.QuotaPerUnit,
	)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingCostConfidenceExact, confidence)
	require.NotNil(t, pricing.CompletionRatio)
	require.NotNil(t, pricing.OutputCostPerMillion)
	assert.Zero(t, *pricing.CompletionRatio)
	assert.Zero(t, *pricing.OutputCostPerMillion)
}

func TestNewAPIRatioPricingAppliesOfficialCacheAndAudioDefaults(t *testing.T) {
	tests := []struct {
		name             string
		createCacheRatio *float64
		audioRatio       *float64
		audioCompletion  *float64
		wantCacheWrite   float64
		wantCacheWrite1h float64
		wantAudioInput   *float64
		wantAudioOutput  *float64
	}{
		{
			name:             "default cache creation and audio ratios",
			wantCacheWrite:   2.5,
			wantCacheWrite1h: 4,
			wantAudioInput:   routingCostFloatPointer(2),
			wantAudioOutput:  routingCostFloatPointer(2),
		},
		{
			name:             "explicit free cache creation",
			createCacheRatio: routingCostFloatPointer(0),
			wantCacheWrite:   0,
			wantCacheWrite1h: 0,
			wantAudioInput:   routingCostFloatPointer(2),
			wantAudioOutput:  routingCostFloatPointer(2),
		},
		{
			name:             "audio input ratio defaults output multiplier",
			audioRatio:       routingCostFloatPointer(3),
			wantCacheWrite:   2.5,
			wantCacheWrite1h: 4,
			wantAudioInput:   routingCostFloatPointer(6),
			wantAudioOutput:  routingCostFloatPointer(6),
		},
		{
			name:             "audio output ratio defaults input multiplier",
			audioCompletion:  routingCostFloatPointer(4),
			wantCacheWrite:   2.5,
			wantCacheWrite1h: 4,
			wantAudioInput:   routingCostFloatPointer(2),
			wantAudioOutput:  routingCostFloatPointer(8),
		},
		{
			name:             "explicit zero audio input remains free",
			audioRatio:       routingCostFloatPointer(0),
			audioCompletion:  routingCostFloatPointer(5),
			wantCacheWrite:   2.5,
			wantCacheWrite1h: 4,
			wantAudioInput:   routingCostFloatPointer(0),
			wantAudioOutput:  routingCostFloatPointer(0),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing, _, _, err := routingNewAPINormalizedPricing(
				routingPricingItem{
					ModelName:            "ratio-model",
					QuotaType:            0,
					ModelRatio:           routingCostFloatPointer(2),
					CompletionRatio:      routingCostFloatPointer(1),
					CreateCacheRatio:     tt.createCacheRatio,
					AudioRatio:           tt.audioRatio,
					AudioCompletionRatio: tt.audioCompletion,
				},
				1,
				true,
				1_000_000,
			)
			require.NoError(t, err)
			require.NotNil(t, pricing.CacheWriteCostPerMillion)
			require.NotNil(t, pricing.CacheWrite1hCostPerMillion)
			assert.Equal(t, tt.wantCacheWrite, *pricing.CacheWriteCostPerMillion)
			assert.Equal(t, tt.wantCacheWrite1h, *pricing.CacheWrite1hCostPerMillion)
			require.NotNil(t, pricing.AudioInputCostPerMillion)
			require.NotNil(t, pricing.AudioOutputCostPerMillion)
			assert.Equal(t, *tt.wantAudioInput, *pricing.AudioInputCostPerMillion)
			assert.Equal(t, *tt.wantAudioOutput, *pricing.AudioOutputCostPerMillion)
		})
	}
}

func TestNewAPIRatioPricingRejectsDerivedOverflow(t *testing.T) {
	_, _, _, err := routingNewAPINormalizedPricing(
		routingPricingItem{
			ModelName:        "overflow-cache",
			QuotaType:        0,
			ModelRatio:       routingCostFloatPointer(1),
			CompletionRatio:  routingCostFloatPointer(1),
			CreateCacheRatio: routingCostFloatPointer(math.MaxFloat64),
		},
		1,
		true,
		1_000_000,
	)
	require.Error(t, err)
}

func TestNewAPISuccessFalseUsesAuthVersionAsAuthenticationBoundary(t *testing.T) {
	authenticatedResponse := routingSub2APITestJSONResponse(`{"success":false}`)
	authenticatedResponse.Header.Set("Auth-Version", "official-contract")
	err := routingNewAPISuccessFalseError(
		authenticatedResponse,
		"invalid access token",
		"fallback",
		model.RoutingCredentials{},
	)
	require.Error(t, err)
	assert.False(t, routingUpstreamAuthError(err))

	unauthenticatedResponse := routingSub2APITestJSONResponse(`{"success":false}`)
	err = routingNewAPISuccessFalseError(
		unauthenticatedResponse,
		"Unauthorized, invalid access token",
		"fallback",
		model.RoutingCredentials{},
	)
	require.Error(t, err)
	assert.True(t, routingUpstreamAuthError(err))

	err = routingNewAPISuccessFalseError(
		unauthenticatedResponse,
		"database temporarily unavailable",
		"fallback",
		model.RoutingCredentials{},
	)
	require.Error(t, err)
	assert.False(t, routingUpstreamAuthError(err))
}

func TestNewAPIPricingDisabledIsNotClassifiedAsAuthenticationFailure(t *testing.T) {
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		response := routingSub2APITestJSONResponse(`{"success":false,"message":"pricing is disabled"}`)
		response.StatusCode = http.StatusForbidden
		response.Status = "403 Forbidden"
		return response, nil
	}))

	_, err := fetchRoutingNewAPIPricingPayload(
		context.Background(),
		model.RoutingChannelBinding{BaseURL: "https://routing.example.com"},
		model.RoutingCredentials{NewAPIAccessToken: "account-token"},
	)

	require.Error(t, err)
	assert.False(t, routingUpstreamAuthError(err))
	assert.Contains(t, err.Error(), "pricing is disabled")
}

func TestNewAPIUserModelsBusinessErrorOnlyIsolatesAffectedGroup(t *testing.T) {
	userID := 42
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/status":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"quota_per_unit":1000000}}`), nil
		case "/api/user/self/groups":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"good":{"ratio":1},"broken":{"ratio":1}}}`), nil
		case "/api/user/models":
			if request.URL.Query().Get("group") == "broken" {
				response := routingSub2APITestJSONResponse(`{"success":false,"message":"database temporarily unavailable"}`)
				response.Header.Set("Auth-Version", "official-contract")
				return response, nil
			}
			return routingSub2APITestJSONResponse(`{"success":true,"data":["gpt-good"]}`), nil
		case "/api/pricing":
			return routingSub2APITestJSONResponse(`{"success":true,"data":[{"model_name":"gpt-good","quota_type":0,"model_ratio":1,"completion_ratio":1}]}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"success":false,"message":"unexpected endpoint"}`), nil
		}
	}))

	payload, err := fetchRoutingNewAPIAccountPricingPayload(
		context.Background(),
		model.RoutingChannelBinding{
			BaseURL:      "https://routing.example.com",
			NewAPIUserID: &userID,
		},
		model.RoutingCredentials{NewAPIAccessToken: "account-token"},
		[]string{"broken", "good"},
	)
	require.NoError(t, err)
	assert.Contains(t, payload.AccountGroupErrors["broken"], "database temporarily unavailable")
	require.Len(t, payload.Data, 1)
	assert.Equal(t, "gpt-good", payload.Data[0].ModelName)
}

func TestNewAPIUserSelfBusinessErrorKeepsPricingSyncPartial(t *testing.T) {
	userID := 42
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/status":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"quota_per_unit":2000000}}`), nil
		case "/api/user/self":
			response := routingSub2APITestJSONResponse(`{"success":false,"message":"database temporarily unavailable"}`)
			response.Header.Set("Auth-Version", "official-contract")
			return response, nil
		case "/api/user/self/groups":
			return routingSub2APITestJSONResponse(`{"success":true,"data":{"vip":{"ratio":1.5}}}`), nil
		case "/api/user/models":
			return routingSub2APITestJSONResponse(`{"success":true,"data":["gpt-partial"]}`), nil
		case "/api/pricing":
			return routingSub2APITestJSONResponse(`{"success":true,"data":[{"model_name":"gpt-partial","quota_type":0,"model_ratio":2,"completion_ratio":1}]}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"success":false,"message":"unexpected endpoint"}`), nil
		}
	}))

	payload, err := fetchRoutingCostAccountPayload(
		context.Background(),
		model.RoutingChannelBinding{
			UpstreamType:  model.RoutingUpstreamTypeNewAPI,
			BaseURL:       "https://routing.example.com",
			UpstreamGroup: "vip",
			NewAPIUserID:  &userID,
		},
		model.RoutingCredentials{NewAPIAccessToken: "account-token"},
		smart_routing_setting.SmartRoutingSetting{SyncIntervalMin: 5},
		nil,
		"vip",
	)
	require.NoError(t, err)
	assert.Equal(t, model.RoutingUpstreamSyncStatusPartial, payload.SyncStatus)
	assert.False(t, payload.BalanceKnown)
	assert.Contains(t, payload.SyncError, "database temporarily unavailable")
	require.NotNil(t, payload.NewAPI)
	require.Len(t, payload.NewAPI.Data, 1)
	assert.Equal(t, 2_000_000.0, payload.NewAPI.QuotaPerUnit)
}

func TestSub2APIEmptyAvailableChannelsFailsClosed(t *testing.T) {
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/auth/me":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":1,"balance":5}}`), nil
		case "/api/v1/groups/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1}]}`), nil
		case "/api/v1/groups/rates":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"10":1}}`), nil
		case "/api/v1/channels/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[]}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
	}))

	_, _, err := fetchRoutingSub2APIAccountPricingWithJWT(
		context.Background(),
		model.RoutingChannelBinding{BaseURL: "https://routing.example.com", UpstreamGroup: "vip"},
		model.RoutingCredentials{Sub2APIToken: "jwt-token"},
		"jwt-token",
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usable model pricing")
}

func TestSub2APIBadRatesDoNotFallBackToAvailableGroupRate(t *testing.T) {
	channelRequests := 0
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/auth/me":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":1,"balance":5}}`), nil
		case "/api/v1/groups/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1.25}]}`), nil
		case "/api/v1/groups/rates":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"10":"invalid"}}`), nil
		case "/api/v1/channels/available":
			channelRequests++
			return routingSub2APITestJSONResponse(`{"code":0,"data":[]}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
	}))

	_, managedJWTRejected, err := fetchRoutingSub2APIAccountPricingWithJWT(
		context.Background(),
		model.RoutingChannelBinding{BaseURL: "https://routing.example.com", UpstreamGroup: "vip"},
		model.RoutingCredentials{Sub2APIToken: "jwt-token"},
		"jwt-token",
		nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid sub2api group rates response")
	assert.False(t, managedJWTRejected)
	assert.Zero(t, channelRequests)
}

func TestSub2APIPreloadedProfilePreservesItsObservationTimestamp(t *testing.T) {
	profileRequests := 0
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/auth/me":
			profileRequests++
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":1,"balance":99}}`), nil
		case "/api/v1/groups/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1}]}`), nil
		case "/api/v1/groups/rates":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"10":1}}`), nil
		case "/api/v1/channels/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard"}],"supported_models":[{"name":"claude-test","platform":"anthropic","pricing":{"billing_mode":"token","input_price":0.000003,"output_price":0.000015,"cache_write_price":0,"cache_read_price":0,"image_output_price":0,"intervals":[]}}]}]}]}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
	}))

	balance := 5.0
	pricing, managedJWTRejected, err := fetchRoutingSub2APIAccountPricingWithJWTAndProfile(
		context.Background(),
		model.RoutingChannelBinding{BaseURL: "https://routing.example.com", UpstreamGroup: "vip"},
		model.RoutingCredentials{Sub2APIToken: "jwt-token"},
		"jwt-token",
		&routingSub2APIProfileObservation{
			Profile:    routingSub2APIUserProfile{ID: 1, Balance: &balance},
			ObservedAt: 1234,
		},
		[]string{"vip"},
	)
	require.NoError(t, err)
	assert.False(t, managedJWTRejected)
	assert.Zero(t, profileRequests)
	assert.True(t, pricing.BalanceKnown)
	assert.Equal(t, 5.0, pricing.Balance)
	assert.Equal(t, int64(1234), pricing.BalanceUpdatedAt)
}

func TestSub2APIPreloadedProfileIsRefetchedAfterManagedJWTRetry(t *testing.T) {
	setupRoutingSub2APIFencingTest(t)
	loginCount := 0
	profileAuthorizations := make([]string, 0, 1)
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		authorization := request.Header.Get("Authorization")
		switch request.URL.Path {
		case "/api/v1/auth/login":
			loginCount++
			return routingSub2APITestJSONResponse(fmt.Sprintf(
				`{"code":0,"data":{"token":"jwt-%d","expires_in":86400}}`, loginCount,
			)), nil
		case "/api/v1/auth/me":
			profileAuthorizations = append(profileAuthorizations, authorization)
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":2,"balance":9}}`), nil
		case "/api/v1/groups/available":
			if authorization == "Bearer jwt-1" {
				response := routingSub2APITestJSONResponse(`{"code":401,"message":"expired token"}`)
				response.StatusCode = http.StatusUnauthorized
				response.Status = "401 Unauthorized"
				return response, nil
			}
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1}]}`), nil
		case "/api/v1/groups/rates":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"10":1}}`), nil
		case "/api/v1/channels/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard"}],"supported_models":[{"name":"claude-test","platform":"anthropic","pricing":{"billing_mode":"token","input_price":0.000003,"output_price":0.000015,"cache_write_price":0,"cache_read_price":0,"image_output_price":0,"intervals":[]}}]}]}]}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
	}))

	credentials := model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "password",
	}
	binding := model.RoutingChannelBinding{
		ChannelID: 883, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://routing.example.com", UpstreamGroup: "vip",
	}
	oldBalance := 5.0
	pricing, err := fetchRoutingSub2APIAccountPricingForGroupsWithProfile(
		context.Background(),
		binding,
		credentials,
		&routingSub2APIProfileObservation{
			Profile:    routingSub2APIUserProfile{ID: 1, Balance: &oldBalance},
			ObservedAt: 1234,
		},
		[]string{"vip"},
	)
	require.NoError(t, err)
	assert.Equal(t, 2, loginCount)
	assert.Equal(t, []string{"Bearer jwt-2"}, profileAuthorizations)
	assert.Equal(t, int64(2), pricing.Profile.ID)
	assert.Equal(t, 9.0, pricing.Balance)
	assert.Greater(t, pricing.BalanceUpdatedAt, int64(1234))
}

func TestSub2APIActionSnapshotPathFailsClosedWithoutUsableBoundPricing(t *testing.T) {
	tests := []struct {
		name      string
		channels  string
		wantError string
	}{
		{
			name:      "matching model has no pricing",
			channels:  `[{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1}],"supported_models":[{"name":"claude-test","platform":"anthropic","pricing":null}]}]}]`,
			wantError: "no usable pricing",
		},
		{
			name:      "no model serves bound group",
			channels:  `[{"name":"primary","platforms":[{"platform":"anthropic","groups":[{"id":20,"name":"other","platform":"anthropic","subscription_type":"standard","rate_multiplier":1}],"supported_models":[{"name":"claude-test","platform":"anthropic","pricing":{"billing_mode":"token","input_price":0.000003,"output_price":0.000015,"cache_write_price":0,"cache_read_price":0,"image_output_price":0,"intervals":[]}}]}]}]`,
			wantError: "no pricing for the bound group",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/api/v1/auth/me":
					return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":1}}`), nil
				case "/api/v1/groups/available":
					return routingSub2APITestJSONResponse(`{"code":0,"data":[{"id":10,"name":"vip","platform":"anthropic","subscription_type":"standard","rate_multiplier":1},{"id":20,"name":"other","platform":"anthropic","subscription_type":"standard","rate_multiplier":1}]}`), nil
				case "/api/v1/groups/rates":
					return routingSub2APITestJSONResponse(`{"code":0,"data":{"10":1,"20":1}}`), nil
				case "/api/v1/channels/available":
					return routingSub2APITestJSONResponse(`{"code":0,"data":` + tt.channels + `}`), nil
				default:
					return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
				}
			}))

			_, err := fetchRoutingSub2APICostSnapshots(
				context.Background(),
				model.RoutingChannelBinding{BaseURL: "https://routing.example.com", UpstreamGroup: "vip"},
				model.RoutingCredentials{Sub2APIToken: "jwt-token"},
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
		})
	}
}

func TestNewAPIV2BindingRequiresPositiveUserID(t *testing.T) {
	err := validateChannelRoutingCostBindingRequest(routingBindingRequest{
		ChannelID:     1,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       "https://example.com",
		UpstreamGroup: "default",
	}, true, true)
	require.Error(t, err)
	var validationErr routingBindingValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, "new_api_user_id", validationErr.Field)
	assert.Equal(t, "required", validationErr.Reason)
}
