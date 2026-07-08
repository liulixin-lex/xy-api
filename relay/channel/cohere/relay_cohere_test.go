package cohere

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCohereStreamHandlerFirstByteTimeoutDoesNotWriteDone(t *testing.T) {
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

	pr, pw := io.Pipe()
	defer pw.Close()
	recorder := &closeNotifyRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		closeCh:          make(chan bool, 1),
	}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	start := time.Now()
	info := &relaycommon.RelayInfo{
		IsStream:          true,
		StartTime:         start,
		FirstResponseTime: start.Add(-time.Second),
		OriginModelName:   "command-test",
		UsingGroup:        "default",
		ChannelMeta:       &relaycommon.ChannelMeta{ChannelId: 88},
	}
	resp := &http.Response{Body: pr}

	done := make(chan struct{})
	go func() {
		_, _ = cohereStreamHandler(c, info, resp)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		_ = pr.Close()
		require.Fail(t, "cohere stream handler did not return after first-byte timeout")
	}

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonFirstByteTimeout, info.StreamStatus.EndReason)
	assert.Empty(t, recorder.Body.String())
}

type closeNotifyRecorder struct {
	*httptest.ResponseRecorder
	closeCh chan bool
}

func (r *closeNotifyRecorder) CloseNotify() <-chan bool {
	return r.closeCh
}
