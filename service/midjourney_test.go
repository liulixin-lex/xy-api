package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareMidjourneyRequestNormalizesStatefulOperations(t *testing.T) {
	tests := []struct {
		name          string
		mode          int
		request       dto.MidjourneyRequest
		action        string
		taskReference string
		modelName     string
		consumesQuota bool
	}{
		{
			name: "simple change", mode: relayconstant.RelayModeMidjourneySimpleChange,
			request: dto.MidjourneyRequest{Content: "task-simple U2"},
			action:  constant.MjActionUpscale, taskReference: "task-simple",
			modelName: "mj_upscale", consumesQuota: true,
		},
		{
			name: "plus action", mode: relayconstant.RelayModeMidjourneyAction,
			request: dto.MidjourneyRequest{CustomId: "MJ::JOB::variation::4::task-plus-1234"},
			action:  constant.MjActionVariation, taskReference: "task-plus-1234",
			modelName: "mj_variation", consumesQuota: true,
		},
		{
			name: "modal", mode: relayconstant.RelayModeMidjourneyModal,
			request: dto.MidjourneyRequest{TaskId: "task-modal"},
			action:  constant.MjActionModal, taskReference: "task-modal",
			modelName: "mj_modal", consumesQuota: true,
		},
		{
			name: "video", mode: relayconstant.RelayModeMidjourneyVideo,
			request: dto.MidjourneyRequest{TaskId: "task-video"},
			action:  constant.MjActionVideo, taskReference: "task-video",
			modelName: "mj_video", consumesQuota: true,
		},
		{
			name: "custom zoom", mode: relayconstant.RelayModeMidjourneyAction,
			request: dto.MidjourneyRequest{CustomId: "MJ::JOB::CustomZoom::task-zoom-1234"},
			action:  constant.MjActionCustomZoom, taskReference: "task-zoom-1234",
			modelName: "mj_custom_zoom", consumesQuota: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, mjErr := PrepareMidjourneyRequest(test.mode, test.request)
			require.Nil(t, mjErr)
			require.NotNil(t, prepared)
			assert.True(t, prepared.Stateful)
			assert.Equal(t, test.action, prepared.Request.Action)
			assert.Equal(t, test.taskReference, prepared.TaskReference)
			assert.Equal(t, test.modelName, prepared.ModelName)
			assert.Equal(t, test.consumesQuota, prepared.ConsumesQuota)
		})
	}
}

func TestPrepareMidjourneyRequestRejectsMalformedStatefulInputsWithoutPanicking(t *testing.T) {
	tests := []struct {
		name    string
		mode    int
		request dto.MidjourneyRequest
	}{
		{name: "empty custom ID", mode: relayconstant.RelayModeMidjourneyAction},
		{name: "short custom ID", mode: relayconstant.RelayModeMidjourneyAction, request: dto.MidjourneyRequest{CustomId: "MJ"}},
		{name: "missing plus index", mode: relayconstant.RelayModeMidjourneyAction, request: dto.MidjourneyRequest{CustomId: "MJ::JOB::upsample"}},
		{name: "plus index out of range", mode: relayconstant.RelayModeMidjourneyAction, request: dto.MidjourneyRequest{CustomId: "MJ::JOB::upsample::9::task-12345678"}},
		{name: "simple missing action", mode: relayconstant.RelayModeMidjourneySimpleChange, request: dto.MidjourneyRequest{Content: "task"}},
		{name: "simple short upscale", mode: relayconstant.RelayModeMidjourneySimpleChange, request: dto.MidjourneyRequest{Content: "task U"}},
		{name: "simple invalid index", mode: relayconstant.RelayModeMidjourneySimpleChange, request: dto.MidjourneyRequest{Content: "task V0"}},
		{name: "video missing task", mode: relayconstant.RelayModeMidjourneyVideo},
		{name: "modal missing task", mode: relayconstant.RelayModeMidjourneyModal},
		{name: "change missing action", mode: relayconstant.RelayModeMidjourneyChange, request: dto.MidjourneyRequest{TaskId: "task", Index: 1}},
		{name: "change missing index", mode: relayconstant.RelayModeMidjourneyChange, request: dto.MidjourneyRequest{TaskId: "task", Action: constant.MjActionUpscale}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				prepared, mjErr := PrepareMidjourneyRequest(test.mode, test.request)
				assert.Nil(t, prepared)
				require.NotNil(t, mjErr)
			})
		})
	}
}

func TestCoverPlusActionParsesSupportedShapesAndDoesNotEchoUnknownCustomID(t *testing.T) {
	tests := []struct {
		customID string
		action   string
		index    int
		taskID   string
	}{
		{customID: "MJ::JOB::upsample::2::task-upscale-1234", action: constant.MjActionUpscale, index: 2, taskID: "task-upscale-1234"},
		{customID: "MJ::upsample::3::task-short-shape", action: constant.MjActionUpscale, index: 3, taskID: "task-short-shape"},
		{customID: "MJ::JOB::variation::4::task-variation-1234", action: constant.MjActionVariation, index: 4, taskID: "task-variation-1234"},
		{customID: "MJ::JOB::low_variation::task-low-variation", action: constant.MjActionLowVariation, index: 1, taskID: "task-low-variation"},
		{customID: "MJ::JOB::pan_left::task-pan-123456", action: constant.MjActionPan, index: 1, taskID: "task-pan-123456"},
		{customID: "MJ::JOB::reroll::0::task-reroll-1234", action: constant.MjActionReRoll, index: 1, taskID: "task-reroll-1234"},
	}

	for _, test := range tests {
		request := dto.MidjourneyRequest{CustomId: test.customID}
		mjErr := CoverPlusActionToNormalAction(&request)
		require.Nil(t, mjErr, test.customID)
		assert.Equal(t, test.action, request.Action)
		assert.Equal(t, test.index, request.Index)
		assert.Equal(t, test.taskID, request.TaskId)
	}

	secretReference := "task-reference-that-must-not-be-echoed"
	request := dto.MidjourneyRequest{CustomId: "MJ::JOB::unknown_action::" + secretReference}
	mjErr := CoverPlusActionToNormalAction(&request)
	require.NotNil(t, mjErr)
	assert.NotContains(t, mjErr.Description, secretReference)
}

func TestMidjourneyParserBoundsUntrustedInput(t *testing.T) {
	request := dto.MidjourneyRequest{CustomId: "MJ::JOB::upsample::1::" + strings.Repeat("x", 8_192)}
	mjErr := CoverPlusActionToNormalAction(&request)
	require.NotNil(t, mjErr)

	assert.Nil(t, ConvertSimpleChangeParams(strings.Repeat("x", 8_192)+" U1"))
}

func TestBuildMidjourneyRequestBodyRestoresEveryStatefulIdentityShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousAccountFilter := setting.MjAccountFilterEnabled
	previousNotify := setting.MjNotifyEnabled
	setting.MjAccountFilterEnabled = true
	setting.MjNotifyEnabled = true
	t.Cleanup(func() {
		setting.MjAccountFilterEnabled = previousAccountFilter
		setting.MjNotifyEnabled = previousNotify
	})

	tests := []struct {
		name       string
		mode       int
		body       string
		request    dto.MidjourneyRequest
		wantTaskID string
		wantField  string
		wantValue  string
	}{
		{
			name: "change taskId", mode: relayconstant.RelayModeMidjourneyChange,
			body:       `{"taskId":"task_public","action":"UPSCALE","index":2,"providerExtension":"kept"}`,
			request:    dto.MidjourneyRequest{TaskId: "task_public", Action: constant.MjActionUpscale, Index: 2},
			wantTaskID: "upstream-123", wantField: "providerExtension", wantValue: "kept",
		},
		{
			name: "simple change content", mode: relayconstant.RelayModeMidjourneySimpleChange,
			body:       `{"content":"task_public v4","providerExtension":"kept"}`,
			request:    dto.MidjourneyRequest{TaskId: "task_public", Content: "task_public v4", Action: constant.MjActionVariation, Index: 4},
			wantTaskID: "upstream-123", wantField: "content", wantValue: "upstream-123 v4",
		},
		{
			name: "plus customId", mode: relayconstant.RelayModeMidjourneyChange,
			body:       `{"customId":"MJ::JOB::reroll::0::task_public","providerExtension":"kept"}`,
			request:    dto.MidjourneyRequest{TaskId: "task_public", CustomId: "MJ::JOB::reroll::0::task_public", Action: constant.MjActionReRoll, Index: 1},
			wantTaskID: "upstream-123", wantField: "customId", wantValue: "MJ::JOB::reroll::0::upstream-123",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/mj/submit/change", strings.NewReader(test.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			prepared := &PreparedMidjourneyRequest{
				RelayMode: test.mode, Request: test.request, ModelName: "mj_test",
				TaskReference: "task_public", Stateful: true, ConsumesQuota: true,
			}

			body, err := BuildMidjourneyRequestBody(ctx, prepared, "upstream-123")
			require.NoError(t, err)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(body, &payload))
			assert.Equal(t, test.wantTaskID, payload["taskId"])
			assert.Equal(t, test.wantValue, payload[test.wantField])
			if test.wantField != "providerExtension" {
				assert.Equal(t, "kept", payload["providerExtension"])
			}
		})
	}
}

func TestDoMidjourneyHTTPRequestMarksSendBoundaryAndBoundsResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name          string
		responseBytes int64
		wantErr       bool
	}{
		{name: "valid response"},
		{name: "oversized response", responseBytes: midjourneyResponseMaxBytes + 1, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.responseBytes > 0 {
					w.WriteHeader(http.StatusOK)
					_, _ = io.CopyN(w, zeroReader{}, test.responseBytes)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"code":1,"description":"ok","result":"task-123"}`)
			}))
			t.Cleanup(server.Close)
			previousClient := httpClient
			httpClient = server.Client()
			t.Cleanup(func() { httpClient = previousClient })

			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/mj/submit/imagine", strings.NewReader(`{"prompt":"test"}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			var sendMarks atomic.Int32
			sendState := relaycommon.NewRoutingUpstreamSendState(func() error {
				sendMarks.Add(1)
				return nil
			})
			relaycommon.BindRoutingUpstreamSendState(ctx, sendState)

			response, body, err := DoMidjourneyHttpRequest(
				ctx, time.Second, server.URL, "stable-key", "", []byte(`{"prompt":"test"}`),
			)
			assert.True(t, sendState.Sent())
			assert.EqualValues(t, 1, sendMarks.Load())
			if test.wantErr {
				require.Error(t, err)
				assert.Nil(t, body)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, response)
			assert.Equal(t, 1, response.Response.Code)
			assert.NotEmpty(t, body)
		})
	}
}

func TestDoMidjourneyHTTPRequestHonorsParentCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	previousClient := httpClient
	httpClient = server.Client()
	t.Cleanup(func() { httpClient = previousClient })

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/mj/submit/imagine", strings.NewReader(`{"prompt":"test"}`)).WithContext(parent)
	ctx.Request.Header.Set("Content-Type", "application/json")
	var sendMarks atomic.Int32
	sendState := relaycommon.NewRoutingUpstreamSendState(func() error {
		sendMarks.Add(1)
		return nil
	})
	relaycommon.BindRoutingUpstreamSendState(ctx, sendState)

	_, _, err := DoMidjourneyHttpRequest(
		ctx, time.Minute, server.URL, "stable-key", "", []byte(`{"prompt":"test"}`),
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, sendState.Sent())
	assert.Zero(t, sendMarks.Load())
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}
