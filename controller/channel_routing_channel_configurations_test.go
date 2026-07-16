package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingChannelConfigurationGetAndListDoNotExposeInternalHash(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.Create(&model.Channel{
		Id: 901, Name: "configuration-get", Models: "gpt-test,gpt-test", Group: "default",
	}).Error)
	configuration, err := model.GetRoutingChannelConfigurationContext(context.Background(), 901)
	require.NoError(t, err)
	configuration.FailureDomainHash = strings.Repeat("a", 64)
	configuration.FailureDomainStatus = model.RoutingFailureDomainStatusHistoricalMigrated
	require.NoError(t, db.Save(&configuration).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "901"}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/channel-configurations/901", nil)
	GetChannelRoutingChannelConfiguration(ctx)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		Success bool                                   `json:"success"`
		Data    channelRoutingChannelConfigurationView `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Equal(t, 901, response.Data.ChannelID)
	assert.Equal(t, "configuration-get", response.Data.ChannelName)
	assert.Equal(t, model.RoutingFailureDomainStatusHistoricalMigrated, response.Data.FailureDomainStatus)
	assert.Empty(t, response.Data.FailureDomainLabel)
	assert.Equal(t, response.Data.ETag, recorder.Header().Get("ETag"))
	assert.True(t, strings.HasPrefix(response.Data.ETag, `"rcc.901.1.`))
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	assert.NotContains(t, recorder.Body.String(), "failure_domain_hash")
	assert.NotContains(t, recorder.Body.String(), configuration.FailureDomainHash)

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(
		http.MethodGet,
		"/api/channel-routing/channel-configurations?cost_confirmed=false&cost_source=defaulted&traffic_class=all&search=configuration",
		nil,
	)
	ListChannelRoutingChannelConfigurations(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code, listRecorder.Body.String())
	assert.Contains(t, listRecorder.Body.String(), `"channel_id":901`)
	assert.Contains(t, listRecorder.Body.String(), `"total":1`)
	assert.NotContains(t, listRecorder.Body.String(), "failure_domain_hash")
}

func TestChannelRoutingChannelConfigurationPutRequiresCompleteStrictDocumentAndStrongETag(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.Create(&model.Channel{
		Id: 902, Name: "configuration-put", Models: "gpt-test", Group: "default",
	}).Error)
	configuration, err := model.GetRoutingChannelConfigurationContext(context.Background(), 902)
	require.NoError(t, err)
	etag, err := channelrouting.ChannelConfigurationETag(configuration)
	require.NoError(t, err)
	validBody := `{"upstream_cost_multiplier":1,"traffic_class":"all","failure_domain_label":"","clear_failure_domain":false}`

	missingIfMatch := putChannelRoutingChannelConfiguration(902, validBody, "")
	assert.Equal(t, http.StatusPreconditionRequired, missingIfMatch.Code)
	assert.Contains(t, missingIfMatch.Body.String(), `"code":"if_match_required"`)

	for _, invalidETag := range []string{"*", "W/" + etag, `"rcc.902.invalid.hash"`} {
		recorder := putChannelRoutingChannelConfiguration(902, validBody, invalidETag)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, invalidETag+": "+recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), `"code":"invalid_if_match"`)
	}

	invalidDocuments := []string{
		`{}`,
		`{"upstream_cost_multiplier":1,"traffic_class":"all","failure_domain_label":""}`,
		`{"upstream_cost_multiplier":1,"traffic_class":"all","failure_domain_label":"","clear_failure_domain":false,"unknown":true}`,
		`{"upstream_cost_multiplier":"1","traffic_class":"all","failure_domain_label":"","clear_failure_domain":false}`,
		`{"upstream_cost_multiplier":null,"traffic_class":"all","failure_domain_label":"","clear_failure_domain":false}`,
		`{"upstream_cost_multiplier":1e309,"traffic_class":"all","failure_domain_label":"","clear_failure_domain":false}`,
		`{"upstream_cost_multiplier":-1,"traffic_class":"all","failure_domain_label":"","clear_failure_domain":false}`,
		`{"upstream_cost_multiplier":1000.0001,"traffic_class":"all","failure_domain_label":"","clear_failure_domain":false}`,
		`{"upstream_cost_multiplier":1,"traffic_class":"unknown","failure_domain_label":"","clear_failure_domain":false}`,
		`{"upstream_cost_multiplier":1,"traffic_class":"all","failure_domain_label":"zone-a","clear_failure_domain":true}`,
		validBody + validBody,
	}
	for index, document := range invalidDocuments {
		recorder := putChannelRoutingChannelConfiguration(902, document, etag)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, strconv.Itoa(index)+": "+recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), `"code":"invalid_channel_configuration"`)
	}

	tooLarge := `{"upstream_cost_multiplier":1,"traffic_class":"all","failure_domain_label":"` +
		strings.Repeat("x", channelRoutingChannelConfigurationBodyMaxBytes) + `","clear_failure_domain":false}`
	tooLargeRecorder := putChannelRoutingChannelConfiguration(902, tooLarge, etag)
	assert.Equal(t, http.StatusRequestEntityTooLarge, tooLargeRecorder.Code)
	assert.Contains(t, tooLargeRecorder.Body.String(), `"code":"channel_configuration_too_large"`)

	unchanged, err := model.GetRoutingChannelConfigurationContext(context.Background(), 902)
	require.NoError(t, err)
	assert.Equal(t, configuration, unchanged)
}

func TestChannelRoutingChannelConfigurationPutPersistsAuditOutboxAndConflictState(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.Create(&model.Channel{
		Id: 903, Name: "configuration-update", Models: "gpt-test", Group: "default",
	}).Error)
	initial, err := model.GetRoutingChannelConfigurationContext(context.Background(), 903)
	require.NoError(t, err)
	initialETag, err := channelrouting.ChannelConfigurationETag(initial)
	require.NoError(t, err)

	firstBody := `{"upstream_cost_multiplier":0,"traffic_class":"claude_code_only","failure_domain_label":"  Primary   Zone  ","clear_failure_domain":false}`
	first := putChannelRoutingChannelConfiguration(903, firstBody, initialETag)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var firstResponse struct {
		Success bool                                   `json:"success"`
		Data    channelRoutingChannelConfigurationView `json:"data"`
	}
	require.NoError(t, common.Unmarshal(first.Body.Bytes(), &firstResponse))
	assert.True(t, firstResponse.Success)
	assert.Equal(t, float64(0), firstResponse.Data.UpstreamCostMultiplier)
	assert.Equal(t, model.RoutingChannelCostSourceManual, firstResponse.Data.CostSource)
	assert.True(t, firstResponse.Data.CostConfirmed)
	assert.Equal(t, model.RoutingChannelTrafficClassClaudeCodeOnly, firstResponse.Data.TrafficClass)
	assert.Equal(t, model.RoutingFailureDomainStatusConfigured, firstResponse.Data.FailureDomainStatus)
	assert.Equal(t, "Primary Zone", firstResponse.Data.FailureDomainLabel)
	assert.Equal(t, int64(2), firstResponse.Data.Revision)
	assert.Equal(t, firstResponse.Data.ETag, first.Header().Get("ETag"))
	assert.NotEqual(t, initialETag, firstResponse.Data.ETag)
	assert.NotContains(t, first.Body.String(), "failure_domain_hash")

	persisted, err := model.GetRoutingChannelConfigurationContext(context.Background(), 903)
	require.NoError(t, err)
	assert.Len(t, persisted.FailureDomainHash, 64)
	assert.NotContains(t, first.Body.String(), persisted.FailureDomainHash)

	stale := putChannelRoutingChannelConfiguration(903, firstBody, initialETag)
	require.Equal(t, http.StatusConflict, stale.Code, stale.Body.String())
	assert.Contains(t, stale.Body.String(), `"code":"channel_configuration_conflict"`)
	var conflictResponse struct {
		Conflict struct {
			Current     channelRoutingChannelConfigurationView `json:"current"`
			CurrentETag string                                 `json:"current_etag"`
		} `json:"conflict"`
	}
	require.NoError(t, common.Unmarshal(stale.Body.Bytes(), &conflictResponse))
	assert.Equal(t, firstResponse.Data.ETag, conflictResponse.Conflict.CurrentETag)
	assert.Equal(t, int64(2), conflictResponse.Conflict.Current.Revision)
	assert.Equal(t, firstResponse.Data.ETag, stale.Header().Get("ETag"))
	assert.NotContains(t, stale.Body.String(), "failure_domain_hash")

	preserveBody := `{"upstream_cost_multiplier":0.5,"traffic_class":"claude_code_only","failure_domain_label":"","clear_failure_domain":false}`
	preserved := putChannelRoutingChannelConfiguration(903, preserveBody, firstResponse.Data.ETag)
	require.Equal(t, http.StatusOK, preserved.Code, preserved.Body.String())
	var preservedResponse struct {
		Data channelRoutingChannelConfigurationView `json:"data"`
	}
	require.NoError(t, common.Unmarshal(preserved.Body.Bytes(), &preservedResponse))
	assert.Equal(t, "Primary Zone", preservedResponse.Data.FailureDomainLabel)
	assert.Equal(t, model.RoutingFailureDomainStatusConfigured, preservedResponse.Data.FailureDomainStatus)

	clearBody := `{"upstream_cost_multiplier":0.5,"traffic_class":"claude_code_only","failure_domain_label":"","clear_failure_domain":true}`
	cleared := putChannelRoutingChannelConfiguration(903, clearBody, preservedResponse.Data.ETag)
	require.Equal(t, http.StatusOK, cleared.Code, cleared.Body.String())
	var clearedResponse struct {
		Data channelRoutingChannelConfigurationView `json:"data"`
	}
	require.NoError(t, common.Unmarshal(cleared.Body.Bytes(), &clearedResponse))
	assert.Empty(t, clearedResponse.Data.FailureDomainLabel)
	assert.Equal(t, model.RoutingFailureDomainStatusUnconfigured, clearedResponse.Data.FailureDomainStatus)
	assert.Equal(t, int64(4), clearedResponse.Data.Revision)

	epoch, err := model.GetRoutingConfigurationEpochContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3), epoch.Epoch)
	assert.Len(t, epoch.StateHash, 64)
	var outboxes []model.RoutingChannelConfigurationOutbox
	require.NoError(t, db.Order("config_epoch asc").Find(&outboxes).Error)
	require.Len(t, outboxes, 3)
	for index := range outboxes {
		assert.Equal(t, int64(index+1), outboxes[index].ConfigEpoch)
		event, decodeErr := outboxes[index].DecodePayload()
		require.NoError(t, decodeErr)
		assert.Equal(t, outboxes[index].ConfigEpoch, event.ConfigEpoch)
		assert.Len(t, event.ConfigurationHash, 64)
	}
	var audits []model.RoutingControlAudit
	require.NoError(t, db.Where(
		"subject_type = ? AND subject_id = ? AND action = ?",
		model.RoutingControlSubjectChannelConfiguration, 903, model.RoutingControlActionUpdate,
	).Order("id asc").Find(&audits).Error)
	require.Len(t, audits, 3)
	for index := range audits {
		assert.Equal(t, 10, audits[index].ActorID)
		assert.Len(t, audits[index].BeforeHash, 64)
		assert.Len(t, audits[index].AfterHash, 64)
		assert.NotEqual(t, audits[index].BeforeHash, audits[index].AfterHash)
	}

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(
		http.MethodGet,
		"/api/channel-routing/channel-configurations?cost_confirmed=true&cost_source=manual&traffic_class=claude_code_only",
		nil,
	)
	ListChannelRoutingChannelConfigurations(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code, listRecorder.Body.String())
	assert.Contains(t, listRecorder.Body.String(), `"channel_id":903`)
	assert.Contains(t, listRecorder.Body.String(), `"total":1`)
}

func putChannelRoutingChannelConfiguration(channelID int, body string, etag string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: strconv.Itoa(channelID)}}
	ctx.Set("id", 10)
	ctx.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/channel-routing/channel-configurations/"+strconv.Itoa(channelID),
		strings.NewReader(body),
	)
	if etag != "" {
		ctx.Request.Header.Set("If-Match", etag)
	}
	UpdateChannelRoutingChannelConfiguration(ctx)
	return recorder
}
