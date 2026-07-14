package relay

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func WssHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)
	return wssHelperWithAdaptor(c, info, GetAdaptor(info.ApiType))
}

func wssHelperWithAdaptor(c *gin.Context, info *relaycommon.RelayInfo, adaptor channel.Adaptor) (newAPIError *types.NewAPIError) {
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	//var requestBody io.Reader
	//firstWssRequest, _ := c.Get("first_wss_request")
	//requestBody = bytes.NewBuffer(firstWssRequest.([]byte))

	statusCodeMappingStr := c.GetString("status_code_mapping")
	resp, err := adaptor.DoRequest(c, info, nil)
	if err != nil {
		var upstreamErr *types.NewAPIError
		if errors.As(err, &upstreamErr) {
			return upstreamErr
		}
		return types.NewError(err, types.ErrorCodeDoRequestFailed)
	}

	if resp != nil {
		info.TargetWs = resp.(*websocket.Conn)
		defer info.TargetWs.Close()
	}

	usage, newAPIError := adaptor.DoResponse(c, nil, info)
	if newAPIError != nil {
		if info.ReceivedResponseCount > 0 || info.HasSendResponse() {
			realtimeUsage, ok := usage.(*dto.RealtimeUsage)
			if !ok || realtimeUsage == nil {
				logger.LogError(c, fmt.Sprintf("committed realtime request returned invalid usage type %T", usage))
				realtimeUsage = nil
			}
			service.PostWssConsumeQuota(c, info, info.UpstreamModelName, realtimeUsage, "")
		}
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}
	service.PostWssConsumeQuota(c, info, info.UpstreamModelName, usage.(*dto.RealtimeUsage), "")
	return nil
}
