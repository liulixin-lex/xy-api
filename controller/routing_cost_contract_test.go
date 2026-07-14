package controller

import (
	"bytes"
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
	assert.Equal(t, "gateway-token", routingBearerToken(model.RoutingUpstreamTypeNewAPI, model.RoutingCredentials{
		GatewayAPIKey: "gateway-token",
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
	}, true)
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

func TestLoadChannelRoutingCostBindingGroupsReturnsBoundedContract(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelHealthState{}))
	require.NoError(t, db.Create(&model.Channel{Id: 880, Name: "cost-source", Key: "serving-key"}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelBinding{
		ChannelID: 880, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://cost-source.example", UpstreamGroup: "default", Enabled: true,
	}).Error)

	groupRatio := make(map[string]float64, routingPricingGroupOutputLimit+1)
	for index := 0; index <= routingPricingGroupOutputLimit; index++ {
		groupRatio[fmt.Sprintf("group-%04d", index)] = 1
	}
	pricingBody, err := common.Marshal(routingPricingResponse{Success: true, GroupRatio: groupRatio})
	require.NoError(t, err)
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(request *http.Request) (*http.Response, error) {
		body := pricingBody
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
	assert.False(t, response.Data.CredentialTest)
}
