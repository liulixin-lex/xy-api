package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingPolicyDraftAPIUsesETagCASAndBoundedSummaries(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	base, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), 0, channelRoutingPolicyDraftDocumentForTest(100),
		model.RoutingPolicyActivationSpec{
			Stage: model.RoutingDeploymentStageShadow, ActorID: 1, Reason: "base",
		},
	)
	require.NoError(t, err)

	createBody := channelRoutingPolicyDraftBody(t, map[string]any{
		"base_revision": base.Revision.Revision,
		"document":      channelRoutingPolicyDraftDocumentForTest(200),
	})
	decodedBase, decodedDocument, err := decodeChannelRoutingPolicyDraftCreate(bytes.NewReader(createBody))
	require.NoError(t, err)
	assert.Equal(t, base.Revision.Revision, decodedBase)
	assert.Equal(t, int64(200), decodedDocument.Pools[0].Members[0].Weight)
	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	common.SetContextKey(createContext, constant.ContextKeyUserId, 9)
	createContext.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/v2/policy-drafts", bytes.NewReader(createBody))
	CreateChannelRoutingPolicyDraft(createContext)
	require.Equal(t, http.StatusCreated, createRecorder.Code, createRecorder.Body.String())
	createETag := createRecorder.Header().Get("ETag")
	require.NotEmpty(t, createETag)
	var createdEnvelope struct {
		Success bool                            `json:"success"`
		Data    model.RoutingPolicyDraftSummary `json:"data"`
	}
	require.NoError(t, common.Unmarshal(createRecorder.Body.Bytes(), &createdEnvelope))
	require.True(t, createdEnvelope.Success)
	require.Positive(t, createdEnvelope.Data.ID)
	assert.Equal(t, model.RoutingPolicyDraftStatusEditing, createdEnvelope.Data.Status)

	detailRecorder := httptest.NewRecorder()
	detailContext, _ := gin.CreateTestContext(detailRecorder)
	detailContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(createdEnvelope.Data.ID, 10)}}
	detailContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/policy-drafts/1", nil)
	GetChannelRoutingPolicyDraft(detailContext)
	require.Equal(t, http.StatusOK, detailRecorder.Code)
	assert.Equal(t, createETag, detailRecorder.Header().Get("ETag"))
	var detailEnvelope struct {
		Success bool                            `json:"success"`
		Data    channelRoutingPolicyDraftDetail `json:"data"`
	}
	require.NoError(t, common.Unmarshal(detailRecorder.Body.Bytes(), &detailEnvelope))
	require.True(t, detailEnvelope.Success)
	assert.Equal(t, int64(200), detailEnvelope.Data.Document.Pools[0].Members[0].Weight)

	missingPreconditionRecorder := httptest.NewRecorder()
	missingPreconditionContext, _ := gin.CreateTestContext(missingPreconditionRecorder)
	missingPreconditionContext.Params = detailContext.Params
	missingPreconditionContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/policy-drafts/1",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"document": channelRoutingPolicyDraftDocumentForTest(250)})),
	)
	UpdateChannelRoutingPolicyDraft(missingPreconditionContext)
	assert.Equal(t, http.StatusPreconditionRequired, missingPreconditionRecorder.Code)

	staleETag := createETag[:len(createETag)-2] + "f\""
	if staleETag == createETag {
		staleETag = createETag[:len(createETag)-2] + "e\""
	}
	staleRecorder := httptest.NewRecorder()
	staleContext, _ := gin.CreateTestContext(staleRecorder)
	staleContext.Params = detailContext.Params
	staleContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/policy-drafts/1",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"document": channelRoutingPolicyDraftDocumentForTest(250)})),
	)
	staleContext.Request.Header.Set("If-Match", staleETag)
	UpdateChannelRoutingPolicyDraft(staleContext)
	assert.Equal(t, http.StatusConflict, staleRecorder.Code)
	assert.Equal(t, createETag, staleRecorder.Header().Get("ETag"))

	updateRecorder := httptest.NewRecorder()
	updateContext, _ := gin.CreateTestContext(updateRecorder)
	updateContext.Params = detailContext.Params
	common.SetContextKey(updateContext, constant.ContextKeyUserId, 10)
	updateContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/policy-drafts/1",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"document": channelRoutingPolicyDraftDocumentForTest(300)})),
	)
	updateContext.Request.Header.Set("If-Match", createETag)
	UpdateChannelRoutingPolicyDraft(updateContext)
	require.Equal(t, http.StatusOK, updateRecorder.Code)
	updateETag := updateRecorder.Header().Get("ETag")
	require.NotEmpty(t, updateETag)
	assert.NotEqual(t, createETag, updateETag)

	validateRecorder := httptest.NewRecorder()
	validateContext, _ := gin.CreateTestContext(validateRecorder)
	validateContext.Params = detailContext.Params
	common.SetContextKey(validateContext, constant.ContextKeyUserId, 11)
	validateContext.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/v2/policy-drafts/1/validate", nil)
	validateContext.Request.Header.Set("If-Match", updateETag)
	ValidateChannelRoutingPolicyDraft(validateContext)
	require.Equal(t, http.StatusOK, validateRecorder.Code)
	validateETag := validateRecorder.Header().Get("ETag")
	assert.NotEqual(t, updateETag, validateETag)
	var validatedEnvelope struct {
		Success bool                            `json:"success"`
		Data    model.RoutingPolicyDraftSummary `json:"data"`
	}
	require.NoError(t, common.Unmarshal(validateRecorder.Body.Bytes(), &validatedEnvelope))
	assert.Equal(t, model.RoutingPolicyDraftStatusValidated, validatedEnvelope.Data.Status)

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/policy-drafts?limit=10", nil)
	ListChannelRoutingPolicyDrafts(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code)
	var listEnvelope struct {
		Success bool `json:"success"`
		Data    struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(listRecorder.Body.Bytes(), &listEnvelope))
	require.True(t, listEnvelope.Success)
	require.Len(t, listEnvelope.Data.Items, 1)
	_, includesDocument := listEnvelope.Data.Items[0]["document"]
	assert.False(t, includesDocument)
}

func TestChannelRoutingPolicyDraftAPIRejectsUnknownFieldsAndMismatchedTags(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	base, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), 0, channelRoutingPolicyDraftDocumentForTest(100),
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageShadow, ActorID: 1, Reason: "base"},
	)
	require.NoError(t, err)

	invalidRecorder := httptest.NewRecorder()
	invalidContext, _ := gin.CreateTestContext(invalidRecorder)
	invalidContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/policy-drafts",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{
			"base_revision": base.Revision.Revision,
			"document":      channelRoutingPolicyDraftDocumentForTest(200),
			"unexpected":    true,
		})),
	)
	CreateChannelRoutingPolicyDraft(invalidContext)
	assert.Equal(t, http.StatusBadRequest, invalidRecorder.Code)

	draft, err := model.CreateRoutingPolicyDraftContext(
		context.Background(), base.Revision.Revision, channelRoutingPolicyDraftDocumentForTest(200), 2,
	)
	require.NoError(t, err)
	mismatchedRecorder := httptest.NewRecorder()
	mismatchedContext, _ := gin.CreateTestContext(mismatchedRecorder)
	mismatchedContext.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(draft.ID, 10)}}
	mismatchedContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/policy-drafts/1",
		bytes.NewReader(channelRoutingPolicyDraftBody(t, map[string]any{"document": channelRoutingPolicyDraftDocumentForTest(250)})),
	)
	mismatchedContext.Request.Header.Set(
		"If-Match", fmt.Sprintf("\"crd.999.%d.%s\"", draft.Version, draft.ETag),
	)
	UpdateChannelRoutingPolicyDraft(mismatchedContext)
	assert.Equal(t, http.StatusBadRequest, mismatchedRecorder.Code)
}

func channelRoutingPolicyDraftDocumentForTest(weight int64) model.RoutingPolicyDocument {
	return model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicySchemaVersion,
		Pools: []model.RoutingPolicyPoolContent{{
			PoolID: 11, GroupName: "VIP", DisplayName: "VIP",
			DeploymentStage: model.RoutingDeploymentStageShadow,
			PolicyProfile:   model.RoutingPolicyProfileBalanced,
			Policy:          json.RawMessage(`{}`),
			Members: []model.RoutingPolicyMemberContent{{
				MemberID: 101, ChannelID: 1001, Enabled: true, Priority: 1, Weight: weight,
				Overrides: json.RawMessage(`{}`),
			}},
		}},
	}
}

func channelRoutingPolicyDraftBody(t *testing.T, value any) []byte {
	t.Helper()
	data, err := common.Marshal(value)
	require.NoError(t, err)
	return data
}
