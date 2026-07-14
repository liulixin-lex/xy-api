package taskcommon_test

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/ali"
	"github.com/QuantumNous/new-api/relay/channel/task/doubao"
	"github.com/QuantumNous/new-api/relay/channel/task/gemini"
	"github.com/QuantumNous/new-api/relay/channel/task/hailuo"
	"github.com/QuantumNous/new-api/relay/channel/task/jimeng"
	"github.com/QuantumNous/new-api/relay/channel/task/kling"
	"github.com/QuantumNous/new-api/relay/channel/task/sora"
	"github.com/QuantumNous/new-api/relay/channel/task/suno"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	"github.com/QuantumNous/new-api/relay/channel/task/vertex"
	"github.com/QuantumNous/new-api/relay/channel/task/vidu"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type openAIVideoConverter interface {
	ConvertToOpenAIVideo(*model.Task) ([]byte, error)
}

const signedProviderURL = "https://provider.invalid/video.mp4?X-Amz-Signature=secret&token=hidden"

func TestOpenAIVideoConvertersExposeOnlyLocalContentURL(t *testing.T) {
	tests := []struct {
		name      string
		converter openAIVideoConverter
		data      string
	}{
		{
			name:      "ali",
			converter: &ali.TaskAdaptor{},
			data:      `{"output":{"task_status":"SUCCEEDED","video_url":"` + signedProviderURL + `"}}`,
		},
		{
			name:      "doubao",
			converter: &doubao.TaskAdaptor{},
			data:      `{"status":"succeeded","content":{"video_url":"` + signedProviderURL + `"}}`,
		},
		{
			name:      "gemini",
			converter: &gemini.TaskAdaptor{},
			data:      `{"done":true,"response":{"generateVideoResponse":{"generatedVideos":[{"video":{"uri":"` + signedProviderURL + `"}}]}}}`,
		},
		{
			name:      "hailuo",
			converter: &hailuo.TaskAdaptor{},
			data:      `{"status":"Success","base_resp":{"status_code":0},"download_url":"` + signedProviderURL + `"}`,
		},
		{
			name:      "jimeng",
			converter: &jimeng.TaskAdaptor{},
			data:      `{"code":10000,"data":{"status":"done","video_url":"` + signedProviderURL + `"}}`,
		},
		{
			name:      "kling",
			converter: &kling.TaskAdaptor{},
			data:      `{"code":0,"data":{"task_status":"succeed","task_result":{"videos":[{"url":"` + signedProviderURL + `","duration":"5"}]}}}`,
		},
		{
			name:      "sora",
			converter: &sora.TaskAdaptor{},
			data:      `{"id":"upstream-task-id","task_id":"upstream-task-id","status":"completed","download_url":"` + signedProviderURL + `","metadata":{"url":"` + signedProviderURL + `"}}`,
		},
		{
			name:      "vertex",
			converter: &vertex.TaskAdaptor{},
			data:      `{"done":true,"response":{"download_url":"` + signedProviderURL + `"}}`,
		},
		{
			name:      "vidu",
			converter: &vidu.TaskAdaptor{},
			data:      `{"state":"success","creations":[{"url":"` + signedProviderURL + `","cover_url":"` + signedProviderURL + `"}]}`,
		},
	}

	const taskID = "task/signed?variant=1 #fragment"
	const localContentURL = "/v1/videos/task%2Fsigned%3Fvariant=1%20%23fragment/content"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &model.Task{
				TaskID:    taskID,
				Platform:  constant.TaskPlatform(tt.name),
				Status:    model.TaskStatusSuccess,
				Progress:  "100%",
				CreatedAt: 100,
				UpdatedAt: 200,
				Properties: model.Properties{
					OriginModelName: "public-model",
				},
				PrivateData: model.TaskPrivateData{
					UpstreamTaskID:    "upstream-task-id",
					UpstreamResultURL: signedProviderURL,
					ResultURL:         signedProviderURL,
				},
				FailReason: signedProviderURL,
				Data:       []byte(tt.data),
			}

			body, err := tt.converter.ConvertToOpenAIVideo(task)
			require.NoError(t, err)

			var video dto.OpenAIVideo
			require.NoError(t, common.Unmarshal(body, &video))
			assert.Equal(t, taskID, video.ID)
			assert.Equal(t, dto.VideoStatusCompleted, video.Status)
			require.NotNil(t, video.Metadata)
			assert.Equal(t, localContentURL, video.Metadata["url"])
			assert.NotContains(t, string(body), "provider.invalid")
			assert.NotContains(t, string(body), "X-Amz-Signature")
			assert.NotContains(t, string(body), "upstream-task-id")
		})
	}
}

func TestOpenAIVideoConvertersOmitContentURLBeforeSuccess(t *testing.T) {
	converters := map[string]openAIVideoConverter{
		"ali":    &ali.TaskAdaptor{},
		"doubao": &doubao.TaskAdaptor{},
		"gemini": &gemini.TaskAdaptor{},
		"hailuo": &hailuo.TaskAdaptor{},
		"jimeng": &jimeng.TaskAdaptor{},
		"kling":  &kling.TaskAdaptor{},
		"sora":   &sora.TaskAdaptor{},
		"vertex": &vertex.TaskAdaptor{},
		"vidu":   &vidu.TaskAdaptor{},
	}

	for name, converter := range converters {
		t.Run(name, func(t *testing.T) {
			task := &model.Task{
				TaskID:   "task_public",
				Platform: constant.TaskPlatform(name),
				Status:   model.TaskStatusInProgress,
				Progress: "50%",
				Data:     []byte(`{}`),
			}

			body, err := converter.ConvertToOpenAIVideo(task)
			require.NoError(t, err)

			var video dto.OpenAIVideo
			require.NoError(t, common.Unmarshal(body, &video))
			_, hasURL := video.Metadata["url"]
			assert.False(t, hasURL)
		})
	}
}

func TestOpenAIVideoConvertersSanitizeProviderURLsInErrors(t *testing.T) {
	tests := []struct {
		name      string
		converter openAIVideoConverter
		data      string
	}{
		{
			name:      "ali",
			converter: &ali.TaskAdaptor{},
			data:      `{"code":"provider_error","message":"download failed: ` + signedProviderURL + `","output":{"task_status":"FAILED"}}`,
		},
		{
			name:      "doubao",
			converter: &doubao.TaskAdaptor{},
			data:      `{"status":"failed","error":{"code":"provider_error","message":"download failed: ` + signedProviderURL + `"}}`,
		},
		{
			name:      "hailuo",
			converter: &hailuo.TaskAdaptor{},
			data:      `{"status":"Fail","base_resp":{"status_code":1004,"status_msg":"download failed: ` + signedProviderURL + `"}}`,
		},
		{
			name:      "jimeng",
			converter: &jimeng.TaskAdaptor{},
			data:      `{"code":10001,"message":"download failed: ` + signedProviderURL + `"}`,
		},
		{
			name:      "kling",
			converter: &kling.TaskAdaptor{},
			data:      `{"code":1,"message":"download failed: ` + signedProviderURL + `","data":{"task_status":"failed","task_status_msg":"download failed: ` + signedProviderURL + `"}}`,
		},
		{
			name:      "sora",
			converter: &sora.TaskAdaptor{},
			data:      `{"status":"failed","error":{"code":"provider_error","message":"download failed: ` + signedProviderURL + `"}}`,
		},
		{
			name:      "vidu",
			converter: &vidu.TaskAdaptor{},
			data:      `{"state":"failed","err_code":"` + signedProviderURL + `"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &model.Task{
				TaskID:   "task_public",
				Platform: constant.TaskPlatform(tt.name),
				Status:   model.TaskStatusFailure,
				Progress: "100%",
				Data:     []byte(tt.data),
			}

			body, err := tt.converter.ConvertToOpenAIVideo(task)
			require.NoError(t, err)

			var video dto.OpenAIVideo
			require.NoError(t, common.Unmarshal(body, &video))
			require.NotNil(t, video.Error)
			assert.NotContains(t, video.Error.Message, "provider.invalid")
			assert.NotContains(t, video.Error.Message, "secret")
			assert.NotContains(t, video.Error.Message, "hidden")
			assert.NotContains(t, video.Error.Code, "provider.invalid")
			assert.NotContains(t, video.Error.Code, "secret")
			assert.NotContains(t, video.Error.Code, "hidden")
		})
	}
}

func TestSunoDoesNotExposeVideoContentURL(t *testing.T) {
	task := &model.Task{
		TaskID:   "task_public",
		Platform: constant.TaskPlatformSuno,
		Status:   model.TaskStatusSuccess,
	}

	assert.Empty(t, task.GetResultURL())
	_, implementsVideoConversion := any(&suno.TaskAdaptor{}).(openAIVideoConverter)
	assert.False(t, implementsVideoConversion)
}

func TestBuildProxyURLEscapesTaskIDAsOnePathSegment(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })
	system_setting.ServerAddress = "https://gateway.example/base/"

	got := taskcommon.BuildProxyURL("task/signed?variant=1 #fragment")
	assert.Equal(t, "https://gateway.example/base/v1/videos/task%2Fsigned%3Fvariant=1%20%23fragment/content", got)
	assert.NotContains(t, got, "?variant=1")
}
