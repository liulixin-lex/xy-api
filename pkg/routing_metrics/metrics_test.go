package routingmetrics

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordAttemptNormalizesSingleKeyAndCapturesTiming(t *testing.T) {
	ResetForTest()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set(string(constant.ContextKeyUsingGroup), "default")

	start := time.Now().Add(-2 * time.Second)
	info := &relaycommon.RelayInfo{
		UsingGroup:        "default",
		OriginModelName:   "gpt-test",
		StartTime:         start,
		FirstResponseTime: start.Add(300 * time.Millisecond),
		IsStream:          true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         11,
			ChannelIsMultiKey: false,
		},
	}

	RecordAttempt(ctx, info, 11, nil)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	metric := snapshots[0]
	assert.Equal(t, 11, metric.ChannelID)
	assert.Equal(t, -1, metric.APIKeyIndex)
	assert.Equal(t, "gpt-test", metric.ModelName)
	assert.Equal(t, "default", metric.Group)
	assert.Equal(t, int64(1), metric.RequestCount)
	assert.Equal(t, int64(1), metric.SuccessCount)
	assert.GreaterOrEqual(t, metric.TotalLatencyMs, int64(1900))
	assert.Equal(t, int64(300), metric.TtftSumMs)
	assert.Equal(t, int64(1), metric.TtftCount)
}

func TestRecordAttemptClassifiesErrorStatus(t *testing.T) {
	ResetForTest()
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:            22,
			ChannelIsMultiKey:    true,
			ChannelMultiKeyIndex: 3,
		},
	}

	RecordAttempt(nil, info, 22, types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests))
	RecordAttempt(nil, info, 22, types.NewErrorWithStatusCode(errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway))
	RecordAttempt(nil, info, 22, types.NewErrorWithStatusCode(errors.New("bad request"), types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest))

	snapshots := Snapshots()
	require.Len(t, snapshots, 2)
	for _, metric := range snapshots {
		assert.Contains(t, []int{model.RoutingMetricSingleKeyIndex, 3}, metric.APIKeyIndex)
		assert.Equal(t, int64(3), metric.RequestCount)
		assert.Zero(t, metric.SuccessCount)
		assert.Equal(t, int64(1), metric.Err429)
		assert.Equal(t, int64(1), metric.Err5xx)
		assert.Equal(t, int64(1), metric.Err4xx)
	}
}

func TestRecordAttemptAddsAggregateSnapshotForMultiKeyChannels(t *testing.T) {
	ResetForTest()
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:            25,
			ChannelIsMultiKey:    true,
			ChannelMultiKeyIndex: 3,
		},
	}

	RecordAttempt(nil, info, 25, nil)

	snapshots := Snapshots()
	require.Len(t, snapshots, 2)
	assert.Equal(t, model.RoutingMetricSingleKeyIndex, snapshots[0].APIKeyIndex)
	assert.Equal(t, 3, snapshots[1].APIKeyIndex)
	for _, metric := range snapshots {
		assert.Equal(t, 25, metric.ChannelID)
		assert.Equal(t, "gpt-test", metric.ModelName)
		assert.Equal(t, "vip", metric.Group)
		assert.Equal(t, int64(1), metric.RequestCount)
		assert.Equal(t, int64(1), metric.SuccessCount)
	}
}

func TestInflightCountersAlsoTrackAggregateForMultiKeyChannels(t *testing.T) {
	ResetForTest()
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:            26,
			ChannelIsMultiKey:    true,
			ChannelMultiKeyIndex: 2,
		},
	}
	aggregate := InflightKey{ChannelID: 26, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
	perKey := InflightKey{ChannelID: 26, APIKeyIndex: 2, Model: "gpt-test", Group: "vip"}

	release := BeginInflight(nil, info, 26)

	assert.Equal(t, int64(1), InflightCount(aggregate))
	assert.Equal(t, int64(1), InflightCount(perKey))
	release()
	assert.Zero(t, InflightCount(aggregate))
	assert.Zero(t, InflightCount(perKey))
}

func TestRecordAttemptCapturesRetryAfterMax(t *testing.T) {
	ResetForTest()
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 23},
	}
	firstMetadata, err := common.Marshal(map[string]int64{"retry_after_ms": 1500})
	require.NoError(t, err)
	secondMetadata, err := common.Marshal(map[string]int64{"retry_after_ms": 2500})
	require.NoError(t, err)
	firstErr := types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	firstErr.Metadata = firstMetadata
	secondErr := types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	secondErr.Metadata = secondMetadata

	RecordAttempt(nil, info, 23, firstErr)
	RecordAttempt(nil, info, 23, secondErr)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(2500), snapshots[0].RetryAfterMaxMs)
}

func TestRecordAttemptComputesLatencyAndTTFTP95(t *testing.T) {
	ResetForTest()
	now := time.Now()
	info := &relaycommon.RelayInfo{
		UsingGroup:        "vip",
		OriginModelName:   "gpt-test",
		IsStream:          true,
		ChannelMeta:       &relaycommon.ChannelMeta{ChannelId: 27},
		FirstResponseTime: now,
	}

	for _, duration := range []time.Duration{100, 200, 300, 400, 500} {
		start := time.Now().Add(-duration * time.Millisecond)
		info.StartTime = start
		info.FirstResponseTime = start.Add(duration * time.Millisecond)
		RecordAttempt(nil, info, 27, nil)
	}

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(5), snapshots[0].RequestCount)
	assert.InDelta(t, 500, snapshots[0].LatencyP95Ms, 20)
	assert.InDelta(t, 500, snapshots[0].TtftP95Ms, 20)
}

func TestInflightCountersUseRoutingKeyAndReleaseOnce(t *testing.T) {
	ResetForTest()
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:            24,
			ChannelIsMultiKey:    true,
			ChannelMultiKeyIndex: 2,
		},
	}
	key := InflightKey{
		ChannelID:   24,
		APIKeyIndex: 2,
		Model:       "gpt-test",
		Group:       "vip",
	}

	release := BeginInflight(nil, info, 24)
	assert.Equal(t, int64(1), InflightCount(key))
	release()
	release()

	assert.Zero(t, InflightCount(key))
}

func TestDrainSnapshotsClearsInMemoryBuckets(t *testing.T) {
	ResetForTest()
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 33},
	}
	RecordAttempt(nil, info, 33, nil)

	first := DrainSnapshots()
	require.Len(t, first, 1)
	assert.Empty(t, DrainSnapshots())
	assert.Empty(t, Snapshots())
}

func TestRequeueSnapshotsRestoresDrainedBuckets(t *testing.T) {
	ResetForTest()
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 34},
	}
	RecordAttempt(nil, info, 34, nil)
	drained := DrainSnapshots()
	require.Len(t, drained, 1)
	require.Empty(t, Snapshots())

	RequeueSnapshots(drained)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, drained[0].ChannelID, snapshots[0].ChannelID)
	assert.Equal(t, drained[0].ModelName, snapshots[0].ModelName)
	assert.Equal(t, drained[0].RequestCount, snapshots[0].RequestCount)
	assert.Equal(t, drained[0].SuccessCount, snapshots[0].SuccessCount)
}
