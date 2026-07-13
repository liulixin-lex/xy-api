package xunfei

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// https://console.xfyun.cn/services/cbm
// https://www.xfyun.cn/doc/spark/Web.html

func requestOpenAI2Xunfei(request dto.GeneralOpenAIRequest, xunfeiAppId string, domain string) *XunfeiChatRequest {
	messages := make([]XunfeiMessage, 0, len(request.Messages))
	shouldCovertSystemMessage := !strings.HasSuffix(request.Model, "3.5")
	for _, message := range request.Messages {
		if message.Role == "system" && shouldCovertSystemMessage {
			messages = append(messages, XunfeiMessage{
				Role:    "user",
				Content: message.StringContent(),
			})
			messages = append(messages, XunfeiMessage{
				Role:    "assistant",
				Content: "Okay",
			})
		} else {
			messages = append(messages, XunfeiMessage{
				Role:    message.Role,
				Content: message.StringContent(),
			})
		}
	}
	xunfeiRequest := XunfeiChatRequest{}
	xunfeiRequest.Header.AppId = xunfeiAppId
	xunfeiRequest.Parameter.Chat.Domain = domain
	xunfeiRequest.Parameter.Chat.Temperature = request.Temperature
	xunfeiRequest.Parameter.Chat.TopK = lo.FromPtrOr(request.N, 0)
	xunfeiRequest.Parameter.Chat.MaxTokens = request.GetMaxTokens()
	xunfeiRequest.Payload.Message.Text = messages
	return &xunfeiRequest
}

func responseXunfei2OpenAI(response *XunfeiChatResponse) *dto.OpenAITextResponse {
	if len(response.Payload.Choices.Text) == 0 {
		response.Payload.Choices.Text = []XunfeiChatResponseTextItem{
			{
				Content: "",
			},
		}
	}
	choice := dto.OpenAITextResponseChoice{
		Index: 0,
		Message: dto.Message{
			Role:    "assistant",
			Content: response.Payload.Choices.Text[0].Content,
		},
		FinishReason: constant.FinishReasonStop,
	}
	fullTextResponse := dto.OpenAITextResponse{
		Object:  "chat.completion",
		Created: common.GetTimestamp(),
		Choices: []dto.OpenAITextResponseChoice{choice},
		Usage:   response.Payload.Usage.Text,
	}
	return &fullTextResponse
}

func streamResponseXunfei2OpenAI(xunfeiResponse *XunfeiChatResponse) *dto.ChatCompletionsStreamResponse {
	if len(xunfeiResponse.Payload.Choices.Text) == 0 {
		xunfeiResponse.Payload.Choices.Text = []XunfeiChatResponseTextItem{
			{
				Content: "",
			},
		}
	}
	var choice dto.ChatCompletionsStreamResponseChoice
	choice.Delta.SetContentString(xunfeiResponse.Payload.Choices.Text[0].Content)
	if xunfeiResponse.Payload.Choices.Status == 2 {
		choice.FinishReason = &constant.FinishReasonStop
	}
	response := dto.ChatCompletionsStreamResponse{
		Object:  "chat.completion.chunk",
		Created: common.GetTimestamp(),
		Model:   "SparkDesk",
		Choices: []dto.ChatCompletionsStreamResponseChoice{choice},
	}
	return &response
}

func buildXunfeiAuthUrl(hostUrl string, apiKey, apiSecret string) string {
	HmacWithShaToBase64 := func(algorithm, data, key string) string {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(data))
		encodeData := mac.Sum(nil)
		return base64.StdEncoding.EncodeToString(encodeData)
	}
	ul, err := url.Parse(hostUrl)
	if err != nil {
		fmt.Println(err)
	}
	date := time.Now().UTC().Format(time.RFC1123)
	signString := []string{"host: " + ul.Host, "date: " + date, "GET " + ul.Path + " HTTP/1.1"}
	sign := strings.Join(signString, "\n")
	sha := HmacWithShaToBase64("hmac-sha256", sign, apiSecret)
	authUrl := fmt.Sprintf("hmac username=\"%s\", algorithm=\"%s\", headers=\"%s\", signature=\"%s\"", apiKey,
		"hmac-sha256", "host date request-line", sha)
	authorization := base64.StdEncoding.EncodeToString([]byte(authUrl))
	v := url.Values{}
	v.Add("host", ul.Host)
	v.Add("date", date)
	v.Add("authorization", authorization)
	callUrl := hostUrl + "?" + v.Encode()
	return callUrl
}

func xunfeiStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, textRequest dto.GeneralOpenAIRequest, appId string, apiSecret string, apiKey string) (*dto.Usage, *types.NewAPIError) {
	domain, authUrl := getXunfeiAuthUrl(c, apiKey, apiSecret, textRequest.Model)
	dataChan, endChan, closeUpstream, err := xunfeiMakeRequest(c.Request.Context(), info, textRequest, domain, authUrl, appId)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeDoRequestFailed)
	}
	return relayXunfeiStream(c, info, dataChan, endChan, closeUpstream)
}

func relayXunfeiStream(c *gin.Context, info *relaycommon.RelayInfo, dataChan chan XunfeiChatResponse, endChan chan xunfeiStreamEnd, closeUpstream xunfeiCloseFunc) (*dto.Usage, *types.NewAPIError) {
	defer closeUpstream.Close()
	firstByteGuard := helper.NewFirstByteGuard(info, closeUpstream)
	defer firstByteGuard.Stop()
	helper.SetEventStreamHeaders(c)
	usage := &dto.Usage{}
	var responseText strings.Builder
	for {
		select {
		case <-c.Request.Context().Done():
			cause := context.Cause(c.Request.Context())
			return xunfeiStreamUsage(c, info, usage, responseText.String()), xunfeiStreamError(info, relaycommon.StreamEndReasonHandlerStop, cause)
		case xunfeiResponse := <-dataChan:
			firstByteGuard.MarkReceived()
			usage.PromptTokens += xunfeiResponse.Payload.Usage.Text.PromptTokens
			usage.CompletionTokens += xunfeiResponse.Payload.Usage.Text.CompletionTokens
			usage.TotalTokens += xunfeiResponse.Payload.Usage.Text.TotalTokens
			if len(xunfeiResponse.Payload.Choices.Text) > 0 {
				responseText.WriteString(xunfeiResponse.Payload.Choices.Text[0].Content)
			}
			response := streamResponseXunfei2OpenAI(&xunfeiResponse)
			jsonResponse, err := common.Marshal(response)
			if err != nil {
				return xunfeiStreamUsage(c, info, usage, responseText.String()), xunfeiStreamError(info, relaycommon.StreamEndReasonHandlerStop, err)
			}
			if err := helper.StringData(c, string(jsonResponse)); err != nil {
				return xunfeiStreamUsage(c, info, usage, responseText.String()), xunfeiStreamError(info, relaycommon.StreamEndReasonHandlerStop, err)
			}
		case end := <-endChan:
			if c.Request.Context().Err() != nil {
				cause := context.Cause(c.Request.Context())
				return xunfeiStreamUsage(c, info, usage, responseText.String()), xunfeiStreamError(info, relaycommon.StreamEndReasonHandlerStop, cause)
			}
			if end.Received {
				firstByteGuard.MarkReceived()
			}
			if firstByteGuard.TimedOutBeforeResponse() {
				return &dto.Usage{}, nil
			}
			if end.Err != nil {
				return xunfeiStreamUsage(c, info, usage, responseText.String()), xunfeiStreamError(info, end.Reason, end.Err)
			}
			usage = xunfeiStreamUsage(c, info, usage, responseText.String())
			if err := helper.Done(c); err != nil {
				return usage, xunfeiStreamError(info, relaycommon.StreamEndReasonHandlerStop, err)
			}
			if info.StreamStatus != nil {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
			}
			return usage, nil
		}
	}
}

func xunfeiStreamUsage(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, responseText string) *dto.Usage {
	if service.ValidUsage(usage) {
		return usage
	}
	return service.ResponseText2Usage(c, responseText, info.UpstreamModelName, info.GetEstimatePromptTokens())
}

func xunfeiStreamError(info *relaycommon.RelayInfo, reason relaycommon.StreamEndReason, err error) *types.NewAPIError {
	if info != nil {
		if info.StreamStatus == nil {
			info.StreamStatus = relaycommon.NewStreamStatus()
		}
		info.StreamStatus.RecordError(err.Error())
		info.StreamStatus.SetEndReason(reason, err)
	}
	return types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
}

func xunfeiHandler(c *gin.Context, textRequest dto.GeneralOpenAIRequest, appId string, apiSecret string, apiKey string) (*dto.Usage, *types.NewAPIError) {
	domain, authUrl := getXunfeiAuthUrl(c, apiKey, apiSecret, textRequest.Model)
	dataChan, endChan, closeUpstream, err := xunfeiMakeRequest(c.Request.Context(), nil, textRequest, domain, authUrl, appId)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeDoRequestFailed)
	}
	defer closeUpstream.Close()
	var usage dto.Usage
	var content string
	var xunfeiResponse XunfeiChatResponse
	stop := false
	for !stop {
		select {
		case <-c.Request.Context().Done():
			return &usage, types.NewError(context.Cause(c.Request.Context()), types.ErrorCodeDoRequestFailed)
		case xunfeiResponse = <-dataChan:
			if len(xunfeiResponse.Payload.Choices.Text) == 0 {
				continue
			}
			content += xunfeiResponse.Payload.Choices.Text[0].Content
			usage.PromptTokens += xunfeiResponse.Payload.Usage.Text.PromptTokens
			usage.CompletionTokens += xunfeiResponse.Payload.Usage.Text.CompletionTokens
			usage.TotalTokens += xunfeiResponse.Payload.Usage.Text.TotalTokens
		case end := <-endChan:
			if c.Request.Context().Err() != nil {
				return &usage, types.NewError(context.Cause(c.Request.Context()), types.ErrorCodeDoRequestFailed)
			}
			if end.Err != nil {
				return &usage, types.NewError(end.Err, types.ErrorCodeBadResponseBody)
			}
			stop = true
		}
	}
	if len(xunfeiResponse.Payload.Choices.Text) == 0 {
		xunfeiResponse.Payload.Choices.Text = []XunfeiChatResponseTextItem{
			{
				Content: "",
			},
		}
	}
	xunfeiResponse.Payload.Choices.Text[0].Content = content

	response := responseXunfei2OpenAI(&xunfeiResponse)
	jsonResponse, err := common.Marshal(response)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	_, _ = c.Writer.Write(jsonResponse)
	return &usage, nil
}

func xunfeiMakeRequest(ctx context.Context, info *relaycommon.RelayInfo, textRequest dto.GeneralOpenAIRequest, domain, authUrl, appId string) (chan XunfeiChatResponse, chan xunfeiStreamEnd, xunfeiCloseFunc, error) {
	d := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}
	if ctx == nil {
		return nil, nil, nil, errors.New("xunfei request context is unavailable")
	}
	dialCtx := ctx
	cancelDial := func() {}
	if firstByteTimeout := helper.FirstByteFailoverTimeout(info); firstByteTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(dialCtx, firstByteTimeout)
		cancelDial = cancel
	}
	defer cancelDial()
	conn, resp, err := d.DialContext(dialCtx, authUrl, nil)
	if err != nil {
		if info != nil && errors.Is(dialCtx.Err(), context.DeadlineExceeded) {
			if info.StreamStatus == nil {
				info.StreamStatus = relaycommon.NewStreamStatus()
			}
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, err)
		}
		return nil, nil, nil, err
	}
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
		}
		return nil, nil, nil, fmt.Errorf("unexpected websocket status: %d", statusCode)
	}

	data := requestOpenAI2Xunfei(textRequest, appId, domain)
	err = conn.WriteJSON(data)
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, err
	}

	dataChan := make(chan XunfeiChatResponse)
	endChan := make(chan xunfeiStreamEnd, 1)
	streamDone := make(chan struct{})
	var closeOnce sync.Once
	var closeErr error
	closeUpstream := xunfeiCloseFunc(func() error {
		closeOnce.Do(func() {
			close(streamDone)
			closeErr = conn.Close()
		})
		return closeErr
	})
	go func() {
		select {
		case <-ctx.Done():
			_ = closeUpstream.Close()
		case <-streamDone:
		}
	}()
	go func() {
		defer conn.Close()
		end := xunfeiStreamEnd{}
	readLoop:
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				select {
				case <-streamDone:
					break readLoop
				default:
				}
				end = xunfeiStreamEnd{Reason: relaycommon.StreamEndReasonScannerErr, Err: err}
				break readLoop
			}
			var response XunfeiChatResponse
			err = common.Unmarshal(msg, &response)
			if err != nil {
				end = xunfeiStreamEnd{Reason: relaycommon.StreamEndReasonHandlerStop, Err: err, Received: true}
				break
			}
			select {
			case dataChan <- response:
			case <-streamDone:
				break readLoop
			}
			if response.Payload.Choices.Status == 2 {
				break readLoop
			}
		}
		endChan <- end
	}()

	return dataChan, endChan, closeUpstream, nil
}

type xunfeiStreamEnd struct {
	Reason   relaycommon.StreamEndReason
	Err      error
	Received bool
}

type xunfeiCloseFunc func() error

func (f xunfeiCloseFunc) Close() error {
	if f == nil {
		return nil
	}
	return f()
}

func apiVersion2domain(apiVersion string) string {
	switch apiVersion {
	case "v1.1":
		return "lite"
	case "v2.1":
		return "generalv2"
	case "v3.1":
		return "generalv3"
	case "v3.5":
		return "generalv3.5"
	case "v4.0":
		return "4.0Ultra"
	}
	return "general" + apiVersion
}

func getXunfeiAuthUrl(c *gin.Context, apiKey string, apiSecret string, modelName string) (string, string) {
	apiVersion := getAPIVersion(c, modelName)
	domain := apiVersion2domain(apiVersion)
	authUrl := buildXunfeiAuthUrl(fmt.Sprintf("wss://spark-api.xf-yun.com/%s/chat", apiVersion), apiKey, apiSecret)
	return domain, authUrl
}

func getAPIVersion(c *gin.Context, modelName string) string {
	query := c.Request.URL.Query()
	apiVersion := query.Get("api-version")
	if apiVersion != "" {
		return apiVersion
	}
	parts := strings.Split(modelName, "-")
	if len(parts) == 2 {
		apiVersion = parts[1]
		return apiVersion

	}
	apiVersion = c.GetString("api_version")
	if apiVersion != "" {
		return apiVersion
	}
	apiVersion = "v1.1"
	common.SysLog("api_version not found, using default: " + apiVersion)
	return apiVersion
}
