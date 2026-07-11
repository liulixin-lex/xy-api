package palm

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// https://developers.generativeai.google/api/rest/generativelanguage/models/generateMessage#request-body
// https://developers.generativeai.google/api/rest/generativelanguage/models/generateMessage#response-body

func responsePaLM2OpenAI(response *PaLMChatResponse) *dto.OpenAITextResponse {
	fullTextResponse := dto.OpenAITextResponse{
		Choices: make([]dto.OpenAITextResponseChoice, 0, len(response.Candidates)),
	}
	for i, candidate := range response.Candidates {
		choice := dto.OpenAITextResponseChoice{
			Index: i,
			Message: dto.Message{
				Role:    "assistant",
				Content: candidate.Content,
			},
			FinishReason: "stop",
		}
		fullTextResponse.Choices = append(fullTextResponse.Choices, choice)
	}
	return &fullTextResponse
}

func streamResponsePaLM2OpenAI(palmResponse *PaLMChatResponse) *dto.ChatCompletionsStreamResponse {
	var choice dto.ChatCompletionsStreamResponseChoice
	if len(palmResponse.Candidates) > 0 {
		choice.Delta.SetContentString(palmResponse.Candidates[0].Content)
	}
	choice.FinishReason = &constant.FinishReasonStop
	var response dto.ChatCompletionsStreamResponse
	response.Object = "chat.completion.chunk"
	response.Model = "palm2"
	response.Choices = []dto.ChatCompletionsStreamResponseChoice{choice}
	return &response
}

func palmStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*types.NewAPIError, string) {
	defer service.CloseResponseBodyGracefully(resp)
	firstByteGuard := helper.NewFirstByteGuard(info, resp.Body)
	defer firstByteGuard.Stop()

	responseBody, err := io.ReadAll(resp.Body)
	if len(responseBody) > 0 {
		firstByteGuard.MarkReceived()
	}
	if firstByteGuard.TimedOutBeforeResponse() {
		return nil, ""
	}
	if err != nil {
		return palmStreamError(info, relaycommon.StreamEndReasonScannerErr, err), ""
	}

	var palmResponse PaLMChatResponse
	if err := common.Unmarshal(responseBody, &palmResponse); err != nil {
		return palmStreamError(info, relaycommon.StreamEndReasonHandlerStop, err), ""
	}
	responseText := ""
	if len(palmResponse.Candidates) > 0 {
		responseText = palmResponse.Candidates[0].Content
	}
	if palmResponse.Error.Code != 0 {
		err := fmt.Errorf("palm provider error %d (%s): %s", palmResponse.Error.Code, palmResponse.Error.Status, palmResponse.Error.Message)
		return palmStreamError(info, relaycommon.StreamEndReasonHandlerStop, err), responseText
	}
	if len(palmResponse.Candidates) == 0 {
		message := palmResponse.Error.Message
		if message == "" && len(palmResponse.Filters) > 0 {
			message = palmResponse.Filters[0].Message
			if message == "" {
				message = palmResponse.Filters[0].Reason
			}
		}
		if message == "" {
			message = "provider returned no candidate details"
		}
		err := fmt.Errorf("palm provider response has no candidates: %s", message)
		return palmStreamError(info, relaycommon.StreamEndReasonHandlerStop, err), ""
	}
	fullTextResponse := streamResponsePaLM2OpenAI(&palmResponse)
	fullTextResponse.Id = helper.GetResponseID(c)
	fullTextResponse.Created = common.GetTimestamp()
	jsonResponse, err := common.Marshal(fullTextResponse)
	if err != nil {
		return palmStreamError(info, relaycommon.StreamEndReasonHandlerStop, err), responseText
	}

	helper.SetEventStreamHeaders(c)
	if err := helper.StringData(c, string(jsonResponse)); err != nil {
		return palmStreamError(info, relaycommon.StreamEndReasonHandlerStop, err), responseText
	}
	if err := helper.Done(c); err != nil {
		return palmStreamError(info, relaycommon.StreamEndReasonHandlerStop, err), responseText
	}
	if info.StreamStatus != nil {
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	}
	return nil, responseText
}

func palmStreamError(info *relaycommon.RelayInfo, reason relaycommon.StreamEndReason, err error) *types.NewAPIError {
	if info != nil {
		if info.StreamStatus == nil {
			info.StreamStatus = relaycommon.NewStreamStatus()
		}
		info.StreamStatus.RecordError(err.Error())
		info.StreamStatus.SetEndReason(reason, err)
	}
	return types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
}

func palmHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	service.CloseResponseBodyGracefully(resp)
	var palmResponse PaLMChatResponse
	err = common.Unmarshal(responseBody, &palmResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if palmResponse.Error.Code != 0 || len(palmResponse.Candidates) == 0 {
		return nil, types.WithOpenAIError(types.OpenAIError{
			Message: palmResponse.Error.Message,
			Type:    palmResponse.Error.Status,
			Param:   "",
			Code:    palmResponse.Error.Code,
		}, resp.StatusCode)
	}
	fullTextResponse := responsePaLM2OpenAI(&palmResponse)
	usage := service.ResponseText2Usage(c, palmResponse.Candidates[0].Content, info.UpstreamModelName, info.GetEstimatePromptTokens())
	fullTextResponse.Usage = *usage
	jsonResponse, err := common.Marshal(fullTextResponse)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	service.IOCopyBytesGracefully(c, resp, jsonResponse)
	return usage, nil
}
