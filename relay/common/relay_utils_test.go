package common

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestValidateMultipartDirectNormalizesImageField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.NewReader(`{"model":"wan2.7-i2v","prompt":"animate","image":" https://example.com/first.png "}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/video/generations", body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	info := &RelayInfo{
		TaskRelayInfo: &TaskRelayInfo{},
	}

	taskErr := ValidateMultipartDirect(context, info)

	require.Nil(t, taskErr)
	storedReq, err := GetTaskRequest(context)
	require.NoError(t, err)
	require.Equal(t, []string{"https://example.com/first.png"}, storedReq.Images)
	require.Equal(t, constant.TaskActionGenerate, info.Action)
}

func TestValidateTaskRequestForRoutingNormalizesMultipartInputReferenceAndDuration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "video-test"))
	require.NoError(t, writer.WriteField("prompt", "animate"))
	require.NoError(t, writer.WriteField("input_reference", "https://example.test/input.png"))
	require.NoError(t, writer.WriteField("duration", "8"))
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, "/v1/videos", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	info := &RelayInfo{TaskRelayInfo: &TaskRelayInfo{}}

	taskErr := ValidateTaskRequestForRouting(context, info)

	require.Nil(t, taskErr)
	storedReq, err := GetTaskRequest(context)
	require.NoError(t, err)
	require.Equal(t, []string{"https://example.test/input.png"}, storedReq.Images)
	require.Equal(t, 8, storedReq.Duration)
	require.Equal(t, constant.TaskActionGenerate, info.Action)
}

// TestTaskDurationBounds guards the billing invariant that user-supplied
// video duration (a quota multiplier via OtherRatio "seconds") is bounded, so
// it can never overflow quota calculation into a negative charge.
func TestTaskDurationBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newContext := func(t *testing.T, body string) (*gin.Context, *RelayInfo) {
		request := httptest.NewRequest(http.MethodPost, "/v1/video/generations", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = request
		return context, &RelayInfo{TaskRelayInfo: &TaskRelayInfo{}}
	}

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name:    "huge duration is rejected",
			body:    `{"model":"sora-2","prompt":"a cat","duration":9999999999}`,
			wantErr: true,
		},
		{
			name:    "huge seconds string is rejected",
			body:    `{"model":"sora-2","prompt":"a cat","seconds":"9999999999"}`,
			wantErr: true,
		},
		{
			name:    "negative duration is rejected",
			body:    `{"model":"sora-2","prompt":"a cat","duration":-8}`,
			wantErr: true,
		},
		{
			name:    "non numeric seconds is rejected",
			body:    `{"model":"sora-2","prompt":"a cat","seconds":"many"}`,
			wantErr: true,
		},
		{
			name:    "explicit zero seconds is rejected",
			body:    `{"model":"sora-2","prompt":"a cat","seconds":"0"}`,
			wantErr: true,
		},
		{
			name:    "explicit zero duration is rejected",
			body:    `{"model":"sora-2","prompt":"a cat","duration":0}`,
			wantErr: true,
		},
		{
			name:    "invalid duration string is rejected",
			body:    `{"model":"sora-2","prompt":"a cat","duration":"many"}`,
			wantErr: true,
		},
		{
			name: "normal duration is accepted",
			body: `{"model":"sora-2","prompt":"a cat","seconds":"8"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" (multipart direct)", func(t *testing.T) {
			context, info := newContext(t, tt.body)
			taskErr := ValidateMultipartDirect(context, info)
			if tt.wantErr {
				require.NotNil(t, taskErr)
				require.Equal(t, "invalid_seconds", taskErr.Code)
			} else {
				require.Nil(t, taskErr)
			}
		})
		t.Run(tt.name+" (basic task request)", func(t *testing.T) {
			context, info := newContext(t, tt.body)
			taskErr := ValidateBasicTaskRequest(context, info, constant.TaskActionGenerate)
			if tt.wantErr {
				require.NotNil(t, taskErr)
				require.Equal(t, "invalid_seconds", taskErr.Code)
			} else {
				require.Nil(t, taskErr)
			}
		})
	}
}

func TestTaskDurationNormalizationUsesDurationPriorityAndCanonicalSeconds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/v1/video/generations",
		strings.NewReader(`{"model":"sora-2","prompt":"a cat","duration":10,"seconds":"0008"}`))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	info := &RelayInfo{TaskRelayInfo: &TaskRelayInfo{}}

	require.Nil(t, ValidateMultipartDirect(context, info))
	stored, err := GetTaskRequest(context)
	require.NoError(t, err)
	require.Equal(t, 10, stored.Duration)
	require.Equal(t, "10", stored.Seconds)
}

func TestMultipartTaskDurationRejectsInvalidValuesBeforeStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, field := range []struct {
		name  string
		value string
	}{{name: "duration", value: "many"}, {name: "seconds", value: "999999999999999999999"}} {
		t.Run(field.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			require.NoError(t, writer.WriteField("model", "sora-2"))
			require.NoError(t, writer.WriteField("prompt", "a cat"))
			require.NoError(t, writer.WriteField(field.name, field.value))
			require.NoError(t, writer.Close())
			request := httptest.NewRequest(http.MethodPost, "/v1/video/generations", &body)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = request
			taskErr := ValidateTaskRequestForRouting(context, &RelayInfo{TaskRelayInfo: &TaskRelayInfo{}})
			require.NotNil(t, taskErr)
		})
	}
}
