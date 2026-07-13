package zhipu

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	"github.com/golang-jwt/jwt/v5"
)

// https://open.bigmodel.cn/doc/api#chatglm_std
// chatglm_std, chatglm_lite
// https://open.bigmodel.cn/api/paas/v3/model-api/chatglm_std/invoke
// https://open.bigmodel.cn/api/paas/v3/model-api/chatglm_std/sse-invoke

var zhipuTokens sync.Map
var expSeconds int64 = 24 * 3600

func getZhipuToken(apikey string) string {
	data, ok := zhipuTokens.Load(apikey)
	if ok {
		tokenData := data.(zhipuTokenData)
		if time.Now().Before(tokenData.ExpiryTime) {
			return tokenData.Token
		}
	}

	split := strings.Split(apikey, ".")
	if len(split) != 2 {
		common.SysLog("invalid zhipu key: " + apikey)
		return ""
	}

	id := split[0]
	secret := split[1]

	expMillis := time.Now().Add(time.Duration(expSeconds)*time.Second).UnixNano() / 1e6
	expiryTime := time.Now().Add(time.Duration(expSeconds) * time.Second)

	timestamp := time.Now().UnixNano() / 1e6

	payload := jwt.MapClaims{
		"api_key":   id,
		"exp":       expMillis,
		"timestamp": timestamp,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, payload)

	token.Header["alg"] = "HS256"
	token.Header["sign_type"] = "SIGN"

	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		return ""
	}

	zhipuTokens.Store(apikey, zhipuTokenData{
		Token:      tokenString,
		ExpiryTime: expiryTime,
	})

	return tokenString
}

func requestOpenAI2Zhipu(request dto.GeneralOpenAIRequest) *ZhipuRequest {
	messages := make([]ZhipuMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		if message.Role == "system" {
			messages = append(messages, ZhipuMessage{
				Role:    "system",
				Content: message.StringContent(),
			})
			messages = append(messages, ZhipuMessage{
				Role:    "user",
				Content: "Okay",
			})
		} else {
			messages = append(messages, ZhipuMessage{
				Role:    message.Role,
				Content: message.StringContent(),
			})
		}
	}
	return &ZhipuRequest{
		Prompt:      messages,
		Temperature: request.Temperature,
		TopP:        lo.FromPtrOr(request.TopP, 0),
		Incremental: false,
	}
}

func responseZhipu2OpenAI(response *ZhipuResponse) *dto.OpenAITextResponse {
	fullTextResponse := dto.OpenAITextResponse{
		Id:      response.Data.TaskId,
		Object:  "chat.completion",
		Created: common.GetTimestamp(),
		Choices: make([]dto.OpenAITextResponseChoice, 0, len(response.Data.Choices)),
		Usage:   response.Data.Usage,
	}
	for i, choice := range response.Data.Choices {
		openaiChoice := dto.OpenAITextResponseChoice{
			Index: i,
			Message: dto.Message{
				Role:    choice.Role,
				Content: strings.Trim(choice.Content, "\""),
			},
			FinishReason: "",
		}
		if i == len(response.Data.Choices)-1 {
			openaiChoice.FinishReason = "stop"
		}
		fullTextResponse.Choices = append(fullTextResponse.Choices, openaiChoice)
	}
	return &fullTextResponse
}

func streamResponseZhipu2OpenAI(zhipuResponse string) *dto.ChatCompletionsStreamResponse {
	var choice dto.ChatCompletionsStreamResponseChoice
	choice.Delta.SetContentString(zhipuResponse)
	response := dto.ChatCompletionsStreamResponse{
		Object:  "chat.completion.chunk",
		Created: common.GetTimestamp(),
		Model:   "chatglm",
		Choices: []dto.ChatCompletionsStreamResponseChoice{choice},
	}
	return &response
}

func streamMetaResponseZhipu2OpenAI(zhipuResponse *ZhipuStreamMetaResponse) (*dto.ChatCompletionsStreamResponse, *dto.Usage) {
	var choice dto.ChatCompletionsStreamResponseChoice
	choice.Delta.SetContentString("")
	choice.FinishReason = &constant.FinishReasonStop
	response := dto.ChatCompletionsStreamResponse{
		Id:      zhipuResponse.RequestId,
		Object:  "chat.completion.chunk",
		Created: common.GetTimestamp(),
		Model:   "chatglm",
		Choices: []dto.ChatCompletionsStreamResponseChoice{choice},
	}
	return &response, &zhipuResponse.Usage
}

func zhipuStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	var usage *dto.Usage
	var responseText strings.Builder
	var streamErr *types.NewAPIError
	normalEndReason := relaycommon.StreamEndReasonEOF
	scanner := helper.NewStreamScanner(resp.Body)
	scanner.Split(bufio.ScanLines)
	firstByteGuard := helper.NewFirstByteGuard(info, resp.Body)
	defer firstByteGuard.Stop()
	helper.SetEventStreamHeaders(c)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 5 {
			continue
		}
		switch line[:5] {
		case "data:":
			firstByteGuard.MarkReceived()
			data := line[5:]
			if strings.TrimSpace(data) == "[DONE]" {
				normalEndReason = relaycommon.StreamEndReasonDone
				break
			}
			responseText.WriteString(data)
			response := streamResponseZhipu2OpenAI(data)
			if err := helper.ObjectData(c, response); err != nil {
				common.SysLog("error sending stream response: " + err.Error())
				streamErr = types.NewError(err, types.ErrorCodeBadResponse)
				if info.StreamStatus != nil {
					info.StreamStatus.RecordError(err.Error())
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonHandlerStop, err)
				}
			}
		case "meta:":
			firstByteGuard.MarkReceived()
			data := line[5:]
			var zhipuResponse ZhipuStreamMetaResponse
			if err := common.Unmarshal([]byte(data), &zhipuResponse); err != nil {
				common.SysLog("error unmarshalling stream response: " + err.Error())
				streamErr = types.NewError(err, types.ErrorCodeBadResponseBody)
				if info.StreamStatus != nil {
					info.StreamStatus.RecordError(err.Error())
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonHandlerStop, err)
				}
				break
			}
			response, zhipuUsage := streamMetaResponseZhipu2OpenAI(&zhipuResponse)
			usage = zhipuUsage
			if err := helper.ObjectData(c, response); err != nil {
				common.SysLog("error sending stream response: " + err.Error())
				streamErr = types.NewError(err, types.ErrorCodeBadResponse)
				if info.StreamStatus != nil {
					info.StreamStatus.RecordError(err.Error())
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonHandlerStop, err)
				}
			}
		}
		if streamErr != nil || normalEndReason == relaycommon.StreamEndReasonDone {
			break
		}
	}

	if streamErr == nil {
		if err := scanner.Err(); err != nil && !firstByteGuard.TimedOutBeforeResponse() {
			common.SysLog("error reading stream: " + err.Error())
			streamErr = types.NewError(err, types.ErrorCodeBadResponse)
			if info.StreamStatus != nil {
				info.StreamStatus.RecordError(err.Error())
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
			}
		}
	}

	service.CloseResponseBodyGracefully(resp)
	if firstByteGuard.TimedOutBeforeResponse() {
		return nil, nil
	}
	if !service.ValidUsage(usage) && responseText.Len() > 0 {
		usage = service.ResponseText2Usage(c, responseText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
	}
	if info.HTTPStreamFailedBeforeCommit(c) {
		return nil, nil
	}
	if streamErr != nil {
		return usage, streamErr
	}
	if info.HTTPStreamHasFailure() {
		err := info.StreamStatus.EndError
		if err == nil {
			err = fmt.Errorf("zhipu stream ended abnormally: %s", info.StreamStatus.Summary())
		}
		return usage, types.NewError(err, types.ErrorCodeBadResponse)
	}
	if err := helper.Done(c); err != nil {
		if info.StreamStatus != nil {
			info.StreamStatus.RecordError(err.Error())
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonHandlerStop, err)
		}
		return usage, types.NewError(err, types.ErrorCodeBadResponse)
	}
	if info.StreamStatus != nil {
		info.StreamStatus.SetEndReason(normalEndReason, nil)
	}
	return usage, nil
}

func zhipuHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	var zhipuResponse ZhipuResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	service.CloseResponseBodyGracefully(resp)
	err = json.Unmarshal(responseBody, &zhipuResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if !zhipuResponse.Success {
		return nil, types.WithOpenAIError(types.OpenAIError{
			Message: zhipuResponse.Msg,
			Code:    zhipuResponse.Code,
		}, resp.StatusCode)
	}
	fullTextResponse := responseZhipu2OpenAI(&zhipuResponse)
	jsonResponse, err := json.Marshal(fullTextResponse)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	_, err = c.Writer.Write(jsonResponse)
	return &fullTextResponse.Usage, nil
}
