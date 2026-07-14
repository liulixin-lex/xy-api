package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUseLocalMidjourneyMediaURLsRedactsProviderURLs(t *testing.T) {
	previousServerAddress := system_setting.ServerAddress
	previousForwarding := setting.MjForwardUrlEnabled
	system_setting.ServerAddress = "https://gateway.example/"
	setting.MjForwardUrlEnabled = true
	t.Cleanup(func() {
		system_setting.ServerAddress = previousServerAddress
		setting.MjForwardUrlEnabled = previousForwarding
	})

	videoURLs, err := common.Marshal([]dto.ImgUrls{{Url: "https://provider.example/variant.mp4?sig=secret"}})
	require.NoError(t, err)
	task := &model.Midjourney{
		MjId:      "task/media id",
		ImageUrl:  "https://provider.example/image.png?sig=secret",
		VideoUrl:  "https://provider.example/main.mp4?sig=secret",
		VideoUrls: string(videoURLs),
	}

	useLocalMidjourneyMediaURLs(task)

	assert.Equal(t, "https://gateway.example/mj/image/task%2Fmedia%20id", task.ImageUrl)
	assert.Equal(t, "https://gateway.example/mj/video/task%2Fmedia%20id", task.VideoUrl)
	assert.JSONEq(t, `[{"url":"https://gateway.example/mj/video/task%2Fmedia%20id/0"}]`, task.VideoUrls)
	assert.NotContains(t, task.VideoUrls, "provider.example")
	assert.NotContains(t, task.VideoUrls, "sig=secret")
}

func TestUseLocalMidjourneyMediaURLsClearsDisabledOrMalformedMedia(t *testing.T) {
	previousForwarding := setting.MjForwardUrlEnabled
	setting.MjForwardUrlEnabled = false
	t.Cleanup(func() { setting.MjForwardUrlEnabled = previousForwarding })

	task := &model.Midjourney{
		MjId:      "task",
		ImageUrl:  "https://provider.example/image.png",
		VideoUrls: "not-json-with-provider-url",
	}
	useLocalMidjourneyMediaURLs(task)

	assert.Empty(t, task.ImageUrl)
	assert.Empty(t, task.VideoUrls)
}
