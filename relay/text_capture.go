package relay

import (
	"errors"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const textResponseCaptureContextKey = "routing_hedge_text_response_capture"

var ErrTextResponseCaptureMissing = errors.New("captured text response did not provide usage")

type TextResponseCapture struct {
	Usage         *dto.Usage
	ContainsAudio bool
}

func TextHelperCapture(
	c *gin.Context,
	info *relaycommon.RelayInfo,
) (*TextResponseCapture, *types.NewAPIError) {
	if c == nil || info == nil {
		return nil, types.NewError(ErrTextResponseCaptureMissing, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
	}
	capture := &TextResponseCapture{}
	c.Set(textResponseCaptureContextKey, capture)
	apiErr := TextHelper(c, info)
	c.Set(textResponseCaptureContextKey, nil)
	if apiErr != nil {
		return nil, apiErr
	}
	if capture.Usage == nil {
		return nil, types.NewError(ErrTextResponseCaptureMissing, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
	}
	return capture, nil
}

func FinalizeTextResponseCapture(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	capture *TextResponseCapture,
) error {
	if c == nil || info == nil || capture == nil || capture.Usage == nil {
		return ErrTextResponseCaptureMissing
	}
	if capture.ContainsAudio {
		service.PostAudioConsumeQuota(c, info, capture.Usage, "")
	} else {
		service.PostTextConsumeQuota(c, info, capture.Usage, nil)
	}
	return nil
}

func settleOrCaptureTextUsage(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	usage *dto.Usage,
	containsAudio bool,
) {
	if c != nil {
		if value, exists := c.Get(textResponseCaptureContextKey); exists {
			if capture, ok := value.(*TextResponseCapture); ok && capture != nil {
				capture.Usage = usage
				capture.ContainsAudio = containsAudio
				return
			}
		}
	}
	if containsAudio {
		service.PostAudioConsumeQuota(c, info, usage, "")
	} else {
		service.PostTextConsumeQuota(c, info, usage, nil)
	}
}
