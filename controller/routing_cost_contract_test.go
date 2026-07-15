package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingBearerTokenIsProviderScoped(t *testing.T) {
	credentials := model.RoutingCredentials{
		NewAPIAccessToken: "newapi-token",
		GatewayAPIKey:     "gateway-token",
		Sub2APIToken:      "sub2api-token",
	}

	assert.Equal(t, "newapi-token", routingBearerToken(model.RoutingUpstreamTypeNewAPI, credentials))
	assert.Equal(t, "sub2api-token", routingBearerToken(model.RoutingUpstreamTypeSub2API, credentials))
	assert.Empty(t, routingBearerToken(model.RoutingUpstreamTypeNewAPI, model.RoutingCredentials{
		Sub2APIToken: "must-never-be-sent-to-newapi",
	}))
	assert.Empty(t, routingBearerToken(model.RoutingUpstreamTypeNewAPI, model.RoutingCredentials{
		GatewayAPIKey: "gateway-token",
	}))
}

func TestNewAPIReadinessSeparatesAccountDiscoveryFromServingSync(t *testing.T) {
	userID := 42
	binding := model.RoutingChannelBinding{
		UpstreamType: model.RoutingUpstreamTypeNewAPI,
		NewAPIUserID: &userID,
		Enabled:      true,
	}
	accessOnly := model.RoutingCredentials{NewAPIAccessToken: "access-token"}

	require.NoError(t, validateRoutingBindingAccountManagementReadiness(binding, accessOnly))
	fullReadinessErr := validateRoutingBindingManagementReadiness(binding, accessOnly)
	require.Error(t, fullReadinessErr)
	var validationErr routingBindingValidationError
	require.ErrorAs(t, fullReadinessErr, &validationErr)
	assert.Equal(t, "gateway_api_key", validationErr.Field)
	assert.Equal(t, "serving_auth_required", validationErr.Reason)

	require.NoError(t, validateRoutingBindingManagementReadiness(binding, model.RoutingCredentials{
		NewAPIAccessToken: "access-token",
		GatewayAPIKey:     "gateway-key",
	}))
}

func TestRoutingPricingGroupsReturnsDeterministicBoundedSummary(t *testing.T) {
	payload := routingPricingResponse{
		GroupRatio:  make(map[string]float64, routingPricingGroupOutputLimit+25),
		UsableGroup: map[string]string{"group-0000": "duplicate"},
	}
	for index := 0; index < routingPricingGroupOutputLimit+25; index++ {
		group := fmt.Sprintf("group-%04d", index)
		payload.GroupRatio[group] = 1
	}

	groups, total := routingPricingGroups(payload)

	require.Len(t, groups, routingPricingGroupOutputLimit)
	assert.Equal(t, routingPricingGroupOutputLimit+25, total)
	assert.Equal(t, "group-0000", groups[0])
	assert.Equal(t, fmt.Sprintf("group-%04d", routingPricingGroupOutputLimit-1), groups[len(groups)-1])
}

func TestChannelRoutingCostBindingValidationReturnsStableFieldCode(t *testing.T) {
	err := validateChannelRoutingCostBindingRequest(routingBindingRequest{
		ChannelID:     1,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       "http://provider.example",
		UpstreamGroup: "default",
	}, true, true)
	require.Error(t, err)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	writeChannelRoutingCostBindingError(
		context,
		http.StatusBadRequest,
		"invalid_cost_binding",
		"invalid cost binding",
		err,
	)

	var response map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "base_url", response["field"])
	assert.Equal(t, "insecure_scheme", response["reason"])
	assert.NotContains(t, recorder.Body.String(), "provider.example")
}

func TestChannelRoutingCostBindingGroupDiscoveryAllowsAnEmptyGroup(t *testing.T) {
	request := routingBindingRequest{
		ChannelID: 1, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://routing.example.com",
	}

	require.NoError(t, validateChannelRoutingCostBindingRequest(request, true, false))
	err := validateChannelRoutingCostBindingRequest(request, true, true)
	require.Error(t, err)
	var validationError routingBindingValidationError
	require.ErrorAs(t, err, &validationError)
	assert.Equal(t, "upstream_group", validationError.Field)
	assert.Equal(t, "required", validationError.Reason)
}

func TestLoadChannelRoutingCostBindingGroupsReturnsBoundedContract(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelHealthState{}))
	require.NoError(t, db.Create(&model.Channel{Id: 880, Name: "cost-source", Key: "serving-key"}).Error)
	userID := 42
	binding := model.RoutingChannelBinding{
		ChannelID: 880, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://cost-source.example", UpstreamGroup: "default",
		NewAPIUserID: &userID, Enabled: true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{NewAPIAccessToken: "access-token"}))
	require.NoError(t, db.Create(&binding).Error)

	groupsData := make(map[string]routingNewAPIUserGroup, routingPricingGroupOutputLimit+1)
	for index := 0; index <= routingPricingGroupOutputLimit; index++ {
		groupsData[fmt.Sprintf("group-%04d", index)] = routingNewAPIUserGroup{Ratio: json.RawMessage(`1`)}
	}
	groupsBody, err := common.Marshal(routingNewAPIUserGroupsResponse{Success: true, Data: groupsData})
	require.NoError(t, err)
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		body := groupsBody
		if strings.HasSuffix(request.URL.Path, "/api/user/self") {
			body = []byte(`{"success":true,"data":{"quota":100,"used_quota":0}}`)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    request,
		}, nil
	}))

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "channelId", Value: "880"}}
	context.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/v2/cost-bindings/880/groups", nil)
	LoadChannelRoutingCostBindingGroups(context)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		Data struct {
			Groups          []string `json:"groups"`
			GroupsTotal     int      `json:"groups_total"`
			GroupsTruncated bool     `json:"groups_truncated"`
			CredentialTest  bool     `json:"credential_test"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Len(t, response.Data.Groups, routingPricingGroupOutputLimit)
	assert.Equal(t, routingPricingGroupOutputLimit+1, response.Data.GroupsTotal)
	assert.True(t, response.Data.GroupsTruncated)
	assert.True(t, response.Data.CredentialTest)
}

func TestLoadChannelRoutingCostBindingGroupsUsesIndependentSub2APIDiscovery(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.Create(&model.Channel{
		Id: 881, Name: "sub2api-cost-source", Key: "serving-key",
		Balance: 77, BalanceUpdatedTime: 123,
	}).Error)
	binding := model.RoutingChannelBinding{
		ChannelID: 881, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://sub2api-cost-source.example", UpstreamGroup: "old-selection", Enabled: true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{Sub2APIToken: "jwt-token"}))
	require.NoError(t, db.Create(&binding).Error)

	requests := make(map[string]int)
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		requests[request.URL.Path]++
		assert.Equal(t, "Bearer jwt-token", request.Header.Get("Authorization"))
		switch request.URL.Path {
		case "/api/v1/auth/me":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"id":42,"balance":99}}`), nil
		case "/api/v1/groups/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[{"id":10,"name":"standard","platform":"anthropic","subscription_type":"standard","rate_multiplier":1},{"id":20,"name":"vip","platform":"anthropic","subscription_type":"subscription","rate_multiplier":1.2,"claude_code_only":true}]}`), nil
		case "/api/v1/groups/rates":
			return routingSub2APITestJSONResponse(`{"code":0,"data":{"10":1,"20":1.2}}`), nil
		case "/api/v1/channels/available":
			return routingSub2APITestJSONResponse(`{"code":0,"data":[]}`), nil
		default:
			return routingSub2APITestJSONResponse(`{"code":404,"message":"not found"}`), nil
		}
	}))

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "channelId", Value: "881"}}
	context.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/v2/cost-bindings/881/groups", nil)
	LoadChannelRoutingCostBindingGroups(context)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		Data struct {
			Groups         []string                               `json:"groups"`
			GroupsTotal    int                                    `json:"groups_total"`
			GroupMeta      map[string]routingSub2APIGroupMetadata `json:"group_meta"`
			ModelCount     int                                    `json:"model_count"`
			PricingVersion string                                 `json:"pricing_version"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, []string{"standard", "vip"}, response.Data.Groups)
	assert.Equal(t, 2, response.Data.GroupsTotal)
	assert.True(t, response.Data.GroupMeta["vip"].ClaudeCodeOnly)
	assert.Equal(t, "20", response.Data.GroupMeta["vip"].ID)
	assert.Zero(t, response.Data.ModelCount)
	assert.Empty(t, response.Data.PricingVersion)
	assert.Equal(t, 1, requests["/api/v1/auth/me"])
	assert.Equal(t, 1, requests["/api/v1/groups/available"])
	assert.Zero(t, requests["/api/v1/groups/rates"])
	assert.Zero(t, requests["/api/v1/channels/available"])

	testRecorder := httptest.NewRecorder()
	testContext, _ := gin.CreateTestContext(testRecorder)
	testContext.Params = gin.Params{{Key: "channelId", Value: "881"}}
	testContext.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/v2/cost-bindings/881/test", nil)
	TestChannelRoutingCostBinding(testContext)
	require.Equal(t, http.StatusBadGateway, testRecorder.Code, testRecorder.Body.String())
	assert.Contains(t, testRecorder.Body.String(), "cost_binding_test_failed")
	assert.Equal(t, 2, requests["/api/v1/groups/available"])
	assert.Zero(t, requests["/api/v1/groups/rates"])
	assert.Zero(t, requests["/api/v1/channels/available"])

	var channel model.Channel
	require.NoError(t, db.First(&channel, 881).Error)
	assert.Equal(t, 77.0, channel.Balance)
	assert.Equal(t, int64(123), channel.BalanceUpdatedTime)
}
