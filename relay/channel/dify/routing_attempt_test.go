package dify

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUploadDifyFileStopsBeforeNetworkWhenRoutingSendBoundaryFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	want := errors.New("routing send rejected")
	sendState := relaycommon.NewRoutingUpstreamSendState(func() error { return want })
	relaycommon.BindRoutingUpstreamSendState(c, sendState)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ApiKey: "test-key", ChannelBaseUrl: "http://127.0.0.1:1",
	}}
	media := dto.MediaContent{
		Type: dto.ContentTypeImageURL,
		ImageUrl: &dto.MessageImageUrl{
			Url: "data:image/png;base64,aW1hZ2U=", MimeType: "image/png",
		},
	}

	file, err := uploadDifyFile(c, info, "test-user", media)

	require.ErrorIs(t, err, want)
	assert.Nil(t, file)
	assert.False(t, sendState.Sent())
}
