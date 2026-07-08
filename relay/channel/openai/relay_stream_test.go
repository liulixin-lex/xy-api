package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOaiStreamHandlerFirstByteTimeoutDoesNotFinalize(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldStreamingTimeout })

	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:                  true,
		Mode:                     smart_routing_setting.ModeBalanced,
		FirstByteFailoverEnabled: true,
		FirstByteMinMs:           20,
		FirstByteCapMs:           20,
		FirstByteP95Multiplier:   1,
	})
	t.Cleanup(smart_routing_setting.ResetForTest)

	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		IsStream:        true,
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "gpt-test",
		UsingGroup:      "default",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         501,
			UpstreamModelName: "gpt-test",
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       reader,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, apiErr := OaiStreamHandler(c, info, resp)

	require.Nil(t, usage)
	require.Nil(t, apiErr)
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonFirstByteTimeout, info.StreamStatus.EndReason)
	assert.Zero(t, info.SendResponseCount)
	assert.Empty(t, recorder.Body.String())
}
