package relay

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

func midjourneyResponseAccepted(code int) bool {
	return code == 1 || code == 21 || code == 22
}

func midjourneyResponseStateUnknown(statusCode int, code int) bool {
	return code == 0 && statusCode >= http.StatusInternalServerError
}

func preConsumeMidjourneyBilling(c *gin.Context, info *relaycommon.RelayInfo, consumeQuota bool) *dto.MidjourneyResponse {
	if info == nil || !consumeQuota || info.PriceData.FreeModel {
		return nil
	}
	info.ForcePreConsume = true
	if apiErr := service.PreConsumeBilling(c, info.PriceData.Quota, info); apiErr != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, apiErr.Error())
	}
	claimed, err := model.ClaimMidjourneyBillingReservation(info.RequestId)
	if err != nil {
		logger.LogError(c, "claim Midjourney billing reservation failed: "+err.Error())
		// Claim happens before any upstream request. Refund only when a fresh
		// read proves that the reservation is still unbound. If the claim commit
		// is ambiguous or another request already owns it, retaining the debit for
		// reconciliation is safer than refunding a job that may be dispatched.
		if _, refundErr := model.RefundUnclaimedMidjourneyBillingReservation(info.RequestId, info.TokenKey); refundErr != nil {
			logger.LogError(c, "refund unclaimed Midjourney billing reservation failed: "+refundErr.Error())
		}
		return service.MidjourneyErrorWrapper(constant.MjErrorUnknown, "billing_claim_failed")
	}
	if !claimed {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "duplicate_request_id")
	}
	return nil
}

func midjourneyBillingPrivateData(info *relaycommon.RelayInfo) model.TaskPrivateData {
	return model.TaskPrivateData{
		BillingSource:    info.BillingSource,
		SubscriptionId:   info.SubscriptionId,
		TokenId:          info.TokenId,
		BillingRequestId: info.RequestId,
		NodeName:         common.NodeName,
		BillingContext: &model.TaskBillingContext{
			ModelPrice:      info.PriceData.ModelPrice,
			GroupRatio:      info.PriceData.GroupRatioInfo.GroupRatio,
			ModelRatio:      info.PriceData.ModelRatio,
			OtherRatios:     info.PriceData.OtherRatios(),
			OriginModelName: info.OriginModelName,
			PerCallBilling:  true,
		},
	}
}

func recordMidjourneyConsumption(c *gin.Context, info *relaycommon.RelayInfo, action string, upstreamTaskId string) {
	if info == nil {
		return
	}
	other := service.GenerateMjOtherInfo(info, info.PriceData)
	other["is_task"] = true
	other["task_id"] = upstreamTaskId
	other["billing_request_id"] = info.RequestId
	logContent := fmt.Sprintf("模型固定价格 %.2f，分组倍率 %.2f，操作 %s，ID %s", info.PriceData.ModelPrice, info.PriceData.GroupRatioInfo.GroupRatio, action, upstreamTaskId)
	model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{
		ChannelId: info.ChannelId,
		ModelName: service.CovertMjpActionToModelName(action),
		TokenName: c.GetString("token_name"),
		Quota:     info.PriceData.Quota,
		Content:   logContent,
		TokenId:   info.TokenId,
		Group:     info.UsingGroup,
		Other:     other,
	})
	model.UpdateUserUsedQuotaAndRequestCount(info.UserId, info.PriceData.Quota)
	model.UpdateChannelUsedQuota(info.ChannelId, info.PriceData.Quota)
}

func RelayMidjourneyImage(c *gin.Context) {
	taskId := c.Param("id")
	midjourneyTask := model.GetByOnlyMJId(taskId)
	if midjourneyTask == nil {
		c.JSON(400, gin.H{
			"error": "midjourney_task_not_found",
		})
		return
	}
	var httpClient *http.Client
	var proxy string
	if channel, err := model.CacheGetChannel(midjourneyTask.ChannelId); err == nil {
		proxy = channel.GetSetting().Proxy
		if proxy != "" {
			if httpClient, err = service.NewProxyHttpClient(proxy); err != nil {
				c.JSON(400, gin.H{
					"error": "proxy_url_invalid",
				})
				return
			}
		}
	}
	if httpClient == nil {
		httpClient = service.GetSSRFProtectedHTTPClient()
	}
	var validateErr error
	if proxy == "" {
		validateErr = service.ValidateSSRFProtectedFetchURL(midjourneyTask.ImageUrl)
	} else {
		// 渠道代理路径的连接由代理侧建立，无法做拨号时逐 IP 校验，
		// 因此保留请求前的一次性 SSRF 校验。
		fetchSetting := system_setting.GetFetchSetting()
		validateErr = common.ValidateURLWithFetchSetting(midjourneyTask.ImageUrl, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp, fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList, fetchSetting.IpList, fetchSetting.AllowedPorts, fetchSetting.ApplyIPFilterForDomain)
	}
	if validateErr != nil {
		c.JSON(http.StatusForbidden, gin.H{
			"error": fmt.Sprintf("request blocked: %v", validateErr),
		})
		return
	}
	resp, err := httpClient.Get(midjourneyTask.ImageUrl)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "http_get_image_failed",
		})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		c.JSON(resp.StatusCode, gin.H{
			"error": string(responseBody),
		})
		return
	}
	// 从Content-Type头获取MIME类型
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		// 如果无法确定内容类型，则默认为jpeg
		contentType = "image/jpeg"
	}
	// 设置响应的内容类型
	c.Writer.Header().Set("Content-Type", contentType)
	// 将图片流式传输到响应体
	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		log.Println("Failed to stream image:", err)
	}
	return
}

func RelayMidjourneyNotify(c *gin.Context) *dto.MidjourneyResponse {
	var midjRequest dto.MidjourneyDto
	err := common.UnmarshalBodyReusable(c, &midjRequest)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "bind_request_body_failed",
			Properties:  nil,
			Result:      "",
		}
	}
	midjourneyTasks, err := model.FindMidjourneysByUpstreamId(midjRequest.MjId)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjErrorUnknown, "find_midjourney_task_failed")
	}
	if len(midjourneyTasks) == 0 {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "midjourney_task_not_found",
			Properties:  nil,
			Result:      "",
		}
	}
	for _, midjourneyTask := range midjourneyTasks {
		if updateErr := updateMidjourneyTaskFromCallback(c, midjourneyTask, midjRequest); updateErr != nil {
			return updateErr
		}
	}
	return nil
}

func updateMidjourneyTaskFromCallback(c *gin.Context, midjourneyTask *model.Midjourney, midjRequest dto.MidjourneyDto) *dto.MidjourneyResponse {
	if midjourneyTask == nil {
		return service.MidjourneyErrorWrapper(constant.MjErrorUnknown, "midjourney_task_not_found")
	}
	if midjourneyTask.IsTerminal() {
		if midjRequest.Status != "" && midjRequest.Status != midjourneyTask.Status {
			logger.LogWarn(c, fmt.Sprintf("Midjourney callback ignored terminal status regression %s -> %s for %s", midjourneyTask.Status, midjRequest.Status, midjourneyTask.MjId))
		}
		switch midjourneyTask.Status {
		case model.MidjourneyStatusSuccess:
			service.SettleMidjourneyTaskQuota(c, midjourneyTask, midjourneyTask.Quota, "Midjourney 回调成功结算")
		case model.MidjourneyStatusFailure:
			service.RefundMidjourneyTaskQuota(c, midjourneyTask, midjourneyTask.FailReason)
		}
		return nil
	}
	if midjourneyTask.Quota > 0 && midjourneyTask.PrivateData.BillingRequestId == "" {
		reservation, adoptErr := model.AdoptLegacyMidjourneyBillingReservation(midjourneyTask.Id)
		if adoptErr != nil {
			logger.LogError(c, fmt.Sprintf("Midjourney callback billing adoption failed for %s: %v", midjourneyTask.MjId, adoptErr))
			return service.MidjourneyErrorWrapper(constant.MjErrorUnknown, "billing_adoption_failed")
		}
		midjourneyTask.PrivateData.BillingRequestId = reservation.RequestId
	}
	preStatus := midjourneyTask.Status
	midjourneyTask.Progress = midjRequest.Progress
	midjourneyTask.PromptEn = midjRequest.PromptEn
	midjourneyTask.State = midjRequest.State
	if midjRequest.SubmitTime != 0 {
		midjourneyTask.SubmitTime = midjRequest.SubmitTime
	}
	if midjRequest.StartTime != 0 {
		midjourneyTask.StartTime = midjRequest.StartTime
	}
	if midjRequest.FinishTime != 0 {
		midjourneyTask.FinishTime = midjRequest.FinishTime
	}
	midjourneyTask.ImageUrl = midjRequest.ImageUrl
	midjourneyTask.VideoUrl = midjRequest.VideoUrl
	videoUrlsStr, _ := common.Marshal(midjRequest.VideoUrls)
	midjourneyTask.VideoUrls = string(videoUrlsStr)
	midjourneyTask.Status = midjRequest.Status
	midjourneyTask.FailReason = midjRequest.FailReason
	shouldRefund := midjRequest.FailReason != "" || midjourneyTask.Status == model.MidjourneyStatusFailure
	shouldSettle := !shouldRefund && midjourneyTask.Status == model.MidjourneyStatusSuccess
	if shouldRefund {
		midjourneyTask.Status = model.MidjourneyStatusFailure
		midjourneyTask.Progress = "100%"
	} else if shouldSettle {
		midjourneyTask.Progress = "100%"
		if err := midjourneyTask.PrivateData.RecordBillingTargetQuota(midjourneyTask.Quota); err != nil {
			logger.LogError(c, fmt.Sprintf("Midjourney callback billing intent conflict for %s: %v", midjourneyTask.MjId, err))
			return service.MidjourneyErrorWrapper(constant.MjErrorUnknown, "billing_intent_conflict")
		}
	}
	won, err := midjourneyTask.UpdateWithStatus(preStatus)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "update_midjourney_task_failed",
		}
	}
	if !won {
		return nil
	}
	if shouldSettle {
		service.SettleMidjourneyTaskQuota(c, midjourneyTask, midjourneyTask.Quota, "Midjourney 回调成功结算")
	}
	if shouldRefund {
		service.RefundMidjourneyTaskQuota(c, midjourneyTask, midjourneyTask.FailReason)
	}
	return nil
}

func coverMidjourneyTaskDto(c *gin.Context, originTask *model.Midjourney) (midjourneyTask dto.MidjourneyDto) {
	midjourneyTask.MjId = originTask.MjId
	midjourneyTask.Progress = originTask.Progress
	midjourneyTask.PromptEn = originTask.PromptEn
	midjourneyTask.State = originTask.State
	midjourneyTask.SubmitTime = originTask.SubmitTime
	midjourneyTask.StartTime = originTask.StartTime
	midjourneyTask.FinishTime = originTask.FinishTime
	midjourneyTask.ImageUrl = ""
	if originTask.ImageUrl != "" && setting.MjForwardUrlEnabled {
		midjourneyTask.ImageUrl = system_setting.ServerAddress + "/mj/image/" + originTask.MjId
		if originTask.Status != "SUCCESS" {
			midjourneyTask.ImageUrl += "?rand=" + strconv.FormatInt(time.Now().UnixNano(), 10)
		}
	} else {
		midjourneyTask.ImageUrl = originTask.ImageUrl
	}
	if originTask.VideoUrl != "" {
		midjourneyTask.VideoUrl = originTask.VideoUrl
	}
	midjourneyTask.Status = originTask.Status
	midjourneyTask.FailReason = originTask.FailReason
	midjourneyTask.Action = originTask.Action
	midjourneyTask.Description = originTask.Description
	midjourneyTask.Prompt = originTask.Prompt
	if originTask.Buttons != "" {
		var buttons []dto.ActionButton
		err := common.Unmarshal([]byte(originTask.Buttons), &buttons)
		if err == nil {
			midjourneyTask.Buttons = buttons
		}
	}
	if originTask.VideoUrls != "" {
		var videoUrls []dto.ImgUrls
		err := common.Unmarshal([]byte(originTask.VideoUrls), &videoUrls)
		if err == nil {
			midjourneyTask.VideoUrls = videoUrls
		}
	}
	if originTask.Properties != "" {
		var properties dto.Properties
		err := common.Unmarshal([]byte(originTask.Properties), &properties)
		if err == nil {
			midjourneyTask.Properties = &properties
		}
	}
	return
}

func RelaySwapFace(c *gin.Context, info *relaycommon.RelayInfo) *dto.MidjourneyResponse {
	var swapFaceRequest dto.SwapFaceRequest
	err := common.UnmarshalBodyReusable(c, &swapFaceRequest)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "bind_request_body_failed")
	}

	info.InitChannelMeta(c)

	if swapFaceRequest.SourceBase64 == "" || swapFaceRequest.TargetBase64 == "" {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "sour_base64_and_target_base64_is_required")
	}
	priceData, err := helper.ModelPriceHelperPerCall(c, info)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: err.Error(),
		}
	}
	info.PriceData = priceData
	info.Action = constant.MjActionSwapFace
	if billingErr := preConsumeMidjourneyBilling(c, info, true); billingErr != nil {
		return billingErr
	}
	reservationOwned := info.Billing != nil
	defer func() {
		if reservationOwned && info.Billing != nil {
			info.Billing.Refund(c)
		}
	}()

	requestURL := getMjRequestPath(c.Request.URL.String())
	baseURL := c.GetString("base_url")
	fullRequestURL := fmt.Sprintf("%s%s", baseURL, requestURL)
	mjResp, _, err := service.DoMidjourneyHttpRequest(c, time.Second*60, fullRequestURL)
	if err != nil {
		if service.IsMidjourneyUpstreamStateUnknown(err) {
			reservationOwned = false
		}
		return &mjResp.Response
	}
	midjResponse := &mjResp.Response
	accepted := midjourneyResponseAccepted(midjResponse.Code)
	stateUnknown := midjourneyResponseStateUnknown(mjResp.StatusCode, midjResponse.Code)
	if stateUnknown {
		reservationOwned = false
	}
	if !stateUnknown {
		midjourneyTask := &model.Midjourney{
			UserId:      info.UserId,
			Code:        midjResponse.Code,
			Action:      constant.MjActionSwapFace,
			MjId:        midjResponse.Result,
			Prompt:      "InsightFace",
			Description: midjResponse.Description,
			SubmitTime:  info.StartTime.UnixNano() / int64(time.Millisecond),
			StartTime:   time.Now().UnixNano() / int64(time.Millisecond),
			ChannelId:   info.ChannelId,
			Group:       info.UsingGroup,
			Progress:    "0%",
		}
		if accepted {
			midjourneyTask.Quota = priceData.Quota
			if info.Billing != nil {
				midjourneyTask.PrivateData = midjourneyBillingPrivateData(info)
				reservationOwned = false
				if bindErr := model.CreateMidjourneyWithBillingReservation(midjourneyTask, info.RequestId, priceData.Quota, info.TokenKey); bindErr != nil {
					common.SysError("bind Midjourney swap-face billing error: " + bindErr.Error())
					if settleErr := service.SettleBilling(c, info, priceData.Quota); settleErr != nil {
						common.SysError("settle unbound Midjourney swap-face billing error: " + settleErr.Error())
					}
					recordMidjourneyConsumption(c, info, constant.MjActionSwapFace, midjResponse.Result)
					return service.MidjourneyErrorWrapper(constant.MjRequestError, "insert_midjourney_task_failed")
				}
			} else if err := midjourneyTask.Insert(); err != nil {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "insert_midjourney_task_failed")
			}
			recordMidjourneyConsumption(c, info, constant.MjActionSwapFace, midjResponse.Result)
		} else {
			midjourneyTask.Status = model.MidjourneyStatusFailure
			midjourneyTask.Progress = "100%"
			midjourneyTask.FailReason = midjResponse.Description
			if err := midjourneyTask.Insert(); err != nil {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "insert_midjourney_task_failed")
			}
		}
	}
	respBody, err := common.Marshal(midjResponse)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "unmarshal_response_body_failed")
	}
	c.Writer.WriteHeader(mjResp.StatusCode)
	_, err = io.Copy(c.Writer, bytes.NewBuffer(respBody))
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "copy_response_body_failed")
	}
	return nil
}

func RelayMidjourneyTaskImageSeed(c *gin.Context) *dto.MidjourneyResponse {
	taskId := c.Param("id")
	userId := c.GetInt("id")
	originTask := model.GetByMJId(userId, taskId)
	if originTask == nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_no_found")
	}
	channel, err := model.GetChannelById(originTask.ChannelId, true)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "get_channel_info_failed")
	}
	if channel.Status != common.ChannelStatusEnabled {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "该任务所属渠道已被禁用")
	}
	c.Set("channel_id", originTask.ChannelId)
	c.Request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", channel.Key))

	requestURL := getMjRequestPath(c.Request.URL.String())
	fullRequestURL := fmt.Sprintf("%s%s", channel.GetBaseURL(), requestURL)
	midjResponseWithStatus, _, err := service.DoMidjourneyHttpRequest(c, time.Second*30, fullRequestURL)
	if err != nil {
		return &midjResponseWithStatus.Response
	}
	midjResponse := &midjResponseWithStatus.Response
	c.Writer.WriteHeader(midjResponseWithStatus.StatusCode)
	respBody, err := common.Marshal(midjResponse)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "unmarshal_response_body_failed")
	}
	service.IOCopyBytesGracefully(c, nil, respBody)
	return nil
}

func RelayMidjourneyTask(c *gin.Context, relayMode int) *dto.MidjourneyResponse {
	userId := c.GetInt("id")
	var err error
	var respBody []byte
	switch relayMode {
	case relayconstant.RelayModeMidjourneyTaskFetch:
		taskId := c.Param("id")
		originTask := model.GetByMJId(userId, taskId)
		if originTask == nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "task_no_found",
			}
		}
		midjourneyTask := coverMidjourneyTaskDto(c, originTask)
		respBody, err = common.Marshal(midjourneyTask)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "unmarshal_response_body_failed",
			}
		}
	case relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		var condition = struct {
			IDs []string `json:"ids"`
		}{}
		err = c.BindJSON(&condition)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "do_request_failed",
			}
		}
		var tasks []dto.MidjourneyDto
		if len(condition.IDs) != 0 {
			originTasks := model.GetByMJIds(userId, condition.IDs)
			for _, originTask := range originTasks {
				midjourneyTask := coverMidjourneyTaskDto(c, originTask)
				tasks = append(tasks, midjourneyTask)
			}
		}
		if tasks == nil {
			tasks = make([]dto.MidjourneyDto, 0)
		}
		respBody, err = common.Marshal(tasks)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "unmarshal_response_body_failed",
			}
		}
	}

	c.Writer.Header().Set("Content-Type", "application/json")

	_, err = io.Copy(c.Writer, bytes.NewBuffer(respBody))
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "copy_response_body_failed",
		}
	}
	return nil
}

func RelayMidjourneySubmit(c *gin.Context, relayInfo *relaycommon.RelayInfo) *dto.MidjourneyResponse {
	consumeQuota := true
	var midjRequest dto.MidjourneyRequest
	err := common.UnmarshalBodyReusable(c, &midjRequest)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "bind_request_body_failed")
	}

	relayInfo.InitChannelMeta(c)

	if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyAction { // midjourney plus，需要从customId中获取任务信息
		mjErr := service.CoverPlusActionToNormalAction(&midjRequest)
		if mjErr != nil {
			return mjErr
		}
		relayInfo.RelayMode = relayconstant.RelayModeMidjourneyChange
	}
	if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyVideo {
		midjRequest.Action = constant.MjActionVideo
	}

	if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyImagine { //绘画任务，此类任务可重复
		if midjRequest.Prompt == "" {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "prompt_is_required")
		}
		midjRequest.Action = constant.MjActionImagine
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyDescribe { //按图生文任务，此类任务可重复
		midjRequest.Action = constant.MjActionDescribe
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyEdits { //编辑任务，此类任务可重复
		midjRequest.Action = constant.MjActionEdits
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyShorten { //缩短任务，此类任务可重复，plus only
		midjRequest.Action = constant.MjActionShorten
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyBlend { //绘画任务，此类任务可重复
		midjRequest.Action = constant.MjActionBlend
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyUpload { //绘画任务，此类任务可重复
		midjRequest.Action = constant.MjActionUpload
	} else if midjRequest.TaskId != "" { //放大、变换任务，此类任务，如果重复且已有结果，远端api会直接返回最终结果
		mjId := ""
		if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyChange {
			if midjRequest.TaskId == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_id_is_required")
			} else if midjRequest.Action == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "action_is_required")
			} else if midjRequest.Index == 0 {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "index_is_required")
			}
			//action = midjRequest.Action
			mjId = midjRequest.TaskId
		} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneySimpleChange {
			if midjRequest.Content == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "content_is_required")
			}
			params := service.ConvertSimpleChangeParams(midjRequest.Content)
			if params == nil {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "content_parse_failed")
			}
			mjId = params.TaskId
			midjRequest.Action = params.Action
		} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyModal {
			//if midjRequest.MaskBase64 == "" {
			//	return service.MidjourneyErrorWrapper(constant.MjRequestError, "mask_base64_is_required")
			//}
			mjId = midjRequest.TaskId
			midjRequest.Action = constant.MjActionModal
		} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyVideo {
			midjRequest.Action = constant.MjActionVideo
			if midjRequest.TaskId == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_id_is_required")
			} else if midjRequest.Action == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "action_is_required")
			}
			mjId = midjRequest.TaskId
		}

		originTask := model.GetByMJId(relayInfo.UserId, mjId)
		if originTask == nil {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_not_found")
		} else { //原任务的Status=SUCCESS，则可以做放大UPSCALE、变换VARIATION等动作，此时必须使用原来的请求地址才能正确处理
			if setting.MjActionCheckSuccessEnabled {
				if originTask.Status != "SUCCESS" && relayInfo.RelayMode != relayconstant.RelayModeMidjourneyModal {
					return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_status_not_success")
				}
			}
			channel, err := model.GetChannelById(originTask.ChannelId, true)
			if err != nil {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "get_channel_info_failed")
			}
			if channel.Status != common.ChannelStatusEnabled {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "该任务所属渠道已被禁用")
			}
			c.Set("base_url", channel.GetBaseURL())
			c.Set("channel_id", originTask.ChannelId)
			c.Request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", channel.Key))
			logger.LogDebug(c, "Midjourney action uses origin channel: id=%s, base_url=%s", strconv.Itoa(originTask.ChannelId), channel.GetBaseURL())
		}
		midjRequest.Prompt = originTask.Prompt

		//if channelType == common.ChannelTypeMidjourneyPlus {
		//	// plus
		//} else {
		//	// 普通版渠道
		//
		//}
	}

	if midjRequest.Action == constant.MjActionInPaint || midjRequest.Action == constant.MjActionCustomZoom {
		consumeQuota = false
	}

	//baseURL := common.ChannelBaseURLs[channelType]
	requestURL := getMjRequestPath(c.Request.URL.String())

	baseURL := c.GetString("base_url")

	//midjRequest.NotifyHook = "http://127.0.0.1:3000/mj/notify"

	fullRequestURL := fmt.Sprintf("%s%s", baseURL, requestURL)

	priceData, err := helper.ModelPriceHelperPerCall(c, relayInfo)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: err.Error(),
		}
	}
	if actualChannelId := c.GetInt("channel_id"); actualChannelId > 0 {
		relayInfo.ChannelId = actualChannelId
	}
	relayInfo.Action = midjRequest.Action
	relayInfo.PriceData = priceData
	if billingErr := preConsumeMidjourneyBilling(c, relayInfo, consumeQuota); billingErr != nil {
		return billingErr
	}
	reservationOwned := relayInfo.Billing != nil
	defer func() {
		if reservationOwned && relayInfo.Billing != nil {
			relayInfo.Billing.Refund(c)
		}
	}()

	midjResponseWithStatus, responseBody, err := service.DoMidjourneyHttpRequest(c, time.Second*60, fullRequestURL)
	if err != nil {
		if service.IsMidjourneyUpstreamStateUnknown(err) {
			reservationOwned = false
		}
		return &midjResponseWithStatus.Response
	}
	midjResponse := &midjResponseWithStatus.Response

	// 文档：https://github.com/novicezk/midjourney-proxy/blob/main/docs/api.md
	//1-提交成功
	// 21-任务已存在（处理中或者有结果了） {"code":21,"description":"任务已存在","result":"0741798445574458","properties":{"status":"SUCCESS","imageUrl":"https://xxxx"}}
	// 22-排队中 {"code":22,"description":"排队中，前面还有1个任务","result":"0741798445574458","properties":{"numberOfQueues":1,"discordInstanceId":"1118138338562560102"}}
	// 23-队列已满，请稍后再试 {"code":23,"description":"队列已满，请稍后尝试","result":"14001929738841620","properties":{"discordInstanceId":"1118138338562560102"}}
	// 24-prompt包含敏感词 {"code":24,"description":"可能包含敏感词","properties":{"promptEn":"nude body","bannedWord":"nude"}}
	// other: 提交错误，description为错误描述
	if midjResponse.Code == 3 {
		//无实例账号自动禁用渠道（No available account instance）
		channel, err := model.GetChannelById(relayInfo.ChannelId, true)
		if err != nil {
			common.SysLog("get_channel_null: " + err.Error())
		} else if channel.GetAutoBan() && common.AutomaticDisableChannelEnabled {
			model.UpdateChannelStatus(relayInfo.ChannelId, "", 2, "No available account instance")
		}
	}
	accepted := midjourneyResponseAccepted(midjResponse.Code)
	stateUnknown := midjourneyResponseStateUnknown(midjResponseWithStatus.StatusCode, midjResponse.Code)
	if stateUnknown {
		// An explicit 5xx without a provider result cannot prove rejection. Keep
		// the unbound reservation for the stale reconciler/manual review.
		reservationOwned = false
	} else {
		taskQuota := 0
		if accepted && consumeQuota {
			taskQuota = priceData.Quota
		}
		midjourneyTask := &model.Midjourney{
			UserId:      relayInfo.UserId,
			Code:        midjResponse.Code,
			Action:      midjRequest.Action,
			MjId:        midjResponse.Result,
			Prompt:      midjRequest.Prompt,
			Description: midjResponse.Description,
			SubmitTime:  time.Now().UnixNano() / int64(time.Millisecond),
			ChannelId:   relayInfo.ChannelId,
			Quota:       taskQuota,
			Group:       relayInfo.UsingGroup,
			Progress:    "0%",
		}
		if !accepted {
			midjourneyTask.FailReason = midjResponse.Description
			midjourneyTask.Status = model.MidjourneyStatusFailure
			midjourneyTask.Progress = "100%"
		}

		if midjResponse.Code == 21 { //21-任务已存在（处理中或者有结果了）
			if properties, ok := midjResponse.Properties.(map[string]interface{}); ok {
				if imageUrl, ok := properties["imageUrl"].(string); ok {
					midjourneyTask.ImageUrl = imageUrl
				}
				if status, ok := properties["status"].(string); ok {
					midjourneyTask.Status = status
					if status == model.MidjourneyStatusSuccess || status == model.MidjourneyStatusFailure {
						midjourneyTask.Progress = "100%"
						midjourneyTask.StartTime = time.Now().UnixNano() / int64(time.Millisecond)
						midjourneyTask.FinishTime = midjourneyTask.StartTime
					}
					if status == model.MidjourneyStatusFailure {
						midjourneyTask.FailReason = midjResponse.Description
					}
				}
			}
			//修改返回值
			if midjRequest.Action != constant.MjActionInPaint && midjRequest.Action != constant.MjActionCustomZoom {
				newBody := strings.Replace(string(responseBody), `"code":21`, `"code":1`, -1)
				responseBody = []byte(newBody)
				midjResponse.Code = 1
			}
		}
		if midjResponse.Code == 1 && midjRequest.Action == constant.MjActionUpload {
			midjourneyTask.Progress = "100%"
			midjourneyTask.Status = model.MidjourneyStatusSuccess
			midjourneyTask.FinishTime = time.Now().UnixNano() / int64(time.Millisecond)
		}

		if accepted && relayInfo.Billing != nil {
			midjourneyTask.PrivateData = midjourneyBillingPrivateData(relayInfo)
			reservationOwned = false
			if bindErr := model.CreateMidjourneyWithBillingReservation(midjourneyTask, relayInfo.RequestId, taskQuota, relayInfo.TokenKey); bindErr != nil {
				common.SysError("bind Midjourney billing error: " + bindErr.Error())
				// The upstream accepted the job. Charge the exact accepted amount if
				// durable binding failed; never refund an untracked running task.
				if settleErr := service.SettleBilling(c, relayInfo, taskQuota); settleErr != nil {
					common.SysError("settle unbound Midjourney billing error: " + settleErr.Error())
				}
				recordMidjourneyConsumption(c, relayInfo, midjRequest.Action, midjResponse.Result)
				return &dto.MidjourneyResponse{Code: 4, Description: "insert_midjourney_task_failed"}
			}
		} else if err := midjourneyTask.Insert(); err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "insert_midjourney_task_failed",
			}
		}

		if accepted && consumeQuota {
			recordMidjourneyConsumption(c, relayInfo, midjRequest.Action, midjResponse.Result)
		}
		if accepted && relayInfo.Billing != nil {
			switch midjourneyTask.Status {
			case model.MidjourneyStatusSuccess:
				service.SettleMidjourneyTaskQuota(c, midjourneyTask, taskQuota, "Midjourney 提交即成功结算")
			case model.MidjourneyStatusFailure:
				service.RefundMidjourneyTaskQuota(c, midjourneyTask, midjourneyTask.FailReason)
			}
		}
	}

	if midjResponse.Code == 22 { //22-排队中，说明任务已存在
		//修改返回值
		newBody := strings.Replace(string(responseBody), `"code":22`, `"code":1`, -1)
		responseBody = []byte(newBody)
	}
	//resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
	bodyReader := io.NopCloser(bytes.NewBuffer(responseBody))

	//for k, v := range resp.Header {
	//	c.Writer.Header().Set(k, v[0])
	//}
	c.Writer.WriteHeader(midjResponseWithStatus.StatusCode)

	_, err = io.Copy(c.Writer, bodyReader)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "copy_response_body_failed",
		}
	}
	err = bodyReader.Close()
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "close_response_body_failed",
		}
	}
	return nil
}

type taskChangeParams struct {
	ID     string
	Action string
	Index  int
}

func getMjRequestPath(path string) string {
	requestURL := path
	if strings.Contains(requestURL, "/mj-") {
		urls := strings.Split(requestURL, "/mj/")
		if len(urls) < 2 {
			return requestURL
		}
		requestURL = "/mj/" + urls[1]
	}
	return requestURL
}
