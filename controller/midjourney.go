package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

// midjourneyPollSummary is the result recorded on a midjourney_poll system task
// row, summarizing one polling pass.
type midjourneyPollSummary struct {
	UnfinishedTasks   int `json:"unfinished_tasks"`
	ChannelsScanned   int `json:"channels_scanned"`
	NullTasksDeferred int `json:"null_tasks_deferred"`
}

// runMidjourneyTaskUpdateOnce performs one Midjourney polling pass synchronously.
// It honors ctx cancellation (the system-task runner cancels it when the lease
// is lost) and, when report is non-nil, reports progress as (processedChannels,
// totalChannels) so the system task surfaces a percentage.
func runMidjourneyTaskUpdateOnce(ctx context.Context, report func(processed, total int)) midjourneyPollSummary {
	summary := midjourneyPollSummary{}
	if ctx == nil {
		ctx = context.Background()
	}

	tasks := model.GetAllUnFinishTasks()
	if len(tasks) == 0 {
		return summary
	}
	summary.UnfinishedTasks = len(tasks)

	logger.LogInfo(ctx, fmt.Sprintf("检测到未完成的任务数有: %v", len(tasks)))
	taskChannelM := make(map[int][]string)
	taskM := make(map[int]map[string][]*model.Midjourney)
	for _, task := range tasks {
		if task.Quota > 0 && task.PrivateData.BillingRequestId == "" {
			reservation, err := model.AdoptLegacyMidjourneyBillingReservation(task.Id)
			if err != nil {
				logger.LogWarn(ctx, fmt.Sprintf("Midjourney task %s billing adoption deferred: %v", task.MjId, err))
				continue
			}
			task.PrivateData.BillingRequestId = reservation.RequestId
		}
		if task.MjId == "" {
			// The upstream may have accepted a request whose response could not be
			// persisted completely. An empty id is ambiguous, so retain the task and
			// reservation for reconciliation/manual review instead of refunding it.
			summary.NullTasksDeferred++
			logger.LogWarn(ctx, fmt.Sprintf("Midjourney task row %d has no upstream id; billing remains pending", task.Id))
			continue
		}
		if taskM[task.ChannelId] == nil {
			taskM[task.ChannelId] = make(map[string][]*model.Midjourney)
		}
		if len(taskM[task.ChannelId][task.MjId]) == 0 {
			taskChannelM[task.ChannelId] = append(taskChannelM[task.ChannelId], task.MjId)
		}
		taskM[task.ChannelId][task.MjId] = append(taskM[task.ChannelId][task.MjId], task)
	}
	if len(taskChannelM) == 0 {
		return summary
	}

	totalChannels := len(taskChannelM)
	processedChannels := 0
	for channelId, taskIds := range taskChannelM {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if report != nil {
			report(processedChannels, totalChannels)
		}
		processedChannels++
		summary.ChannelsScanned++
		logger.LogInfo(ctx, fmt.Sprintf("渠道 #%d 未完成的任务有: %d", channelId, len(taskIds)))
		if len(taskIds) == 0 {
			continue
		}
		midjourneyChannel, err := model.CacheGetChannel(channelId)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("CacheGetChannel: %v", err))
			// A local channel/cache failure says nothing about the upstream job.
			// Leave tasks pending so a later poll can recover safely.
			continue
		}
		requestUrl := fmt.Sprintf("%s/mj/task/list-by-condition", *midjourneyChannel.BaseURL)

		body, err := common.Marshal(map[string]any{
			"ids": taskIds,
		})
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("Get Task marshal body error: %v", err))
			continue
		}
		timeout := time.Second * 15
		requestCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(requestCtx, "POST", requestUrl, bytes.NewBuffer(body))
		if err != nil {
			cancel()
			logger.LogError(ctx, fmt.Sprintf("Get Task error: %v", err))
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("mj-api-secret", midjourneyChannel.Key)
		resp, err := service.GetHttpClient().Do(req)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("Get Task Do req error: %v", err))
			cancel()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			logger.LogError(ctx, fmt.Sprintf("Get Task status code: %d", resp.StatusCode))
			resp.Body.Close()
			cancel()
			continue
		}
		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("Get Mjp Task parse body error: %v", err))
			resp.Body.Close()
			cancel()
			continue
		}
		var responseItems []dto.MidjourneyDto
		err = common.Unmarshal(responseBody, &responseItems)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("Get Mjp Task parse body error2: %v, body: %s", err, string(responseBody)))
			resp.Body.Close()
			cancel()
			continue
		}
		resp.Body.Close()
		req.Body.Close()
		cancel()

		for _, responseItem := range responseItems {
			matchedTasks := taskM[channelId][responseItem.MjId]
			if len(matchedTasks) == 0 {
				logger.LogWarn(ctx, fmt.Sprintf("Midjourney task response ignored: channel_id=%d mj_id=%s is unknown", channelId, responseItem.MjId))
				continue
			}
			for _, task := range matchedTasks {
				updateMidjourneyTaskFromProvider(ctx, task, responseItem)
			}
		}
	}
	if report != nil && (ctx == nil || ctx.Err() == nil) {
		report(totalChannels, totalChannels)
	}
	return summary
}

func updateMidjourneyTaskFromProvider(ctx context.Context, task *model.Midjourney, responseItem dto.MidjourneyDto) {
	if task == nil {
		return
	}
	if task.IsTerminal() {
		if responseItem.Status != "" && responseItem.Status != task.Status {
			logger.LogWarn(ctx, fmt.Sprintf("Midjourney task %s ignored terminal status regression %s -> %s", task.MjId, task.Status, responseItem.Status))
		}
		switch task.Status {
		case model.MidjourneyStatusSuccess:
			service.SettleMidjourneyTaskQuota(ctx, task, task.Quota, "Midjourney 任务成功结算")
		case model.MidjourneyStatusFailure:
			service.RefundMidjourneyTaskQuota(ctx, task, task.FailReason)
		}
		return
	}
	if !checkMjTaskNeedUpdate(task, responseItem) {
		return
	}
	preStatus := task.Status
	task.Code = 1
	task.Progress = responseItem.Progress
	task.PromptEn = responseItem.PromptEn
	task.State = responseItem.State
	if responseItem.SubmitTime != 0 {
		task.SubmitTime = responseItem.SubmitTime
	}
	if responseItem.StartTime != 0 {
		task.StartTime = responseItem.StartTime
	}
	if responseItem.FinishTime != 0 {
		task.FinishTime = responseItem.FinishTime
	}
	task.ImageUrl = responseItem.ImageUrl
	task.Status = responseItem.Status
	task.FailReason = responseItem.FailReason
	if responseItem.Properties != nil {
		propertiesStr, _ := common.Marshal(responseItem.Properties)
		task.Properties = string(propertiesStr)
	}
	if responseItem.Buttons != nil {
		buttonStr, _ := common.Marshal(responseItem.Buttons)
		task.Buttons = string(buttonStr)
	}
	task.VideoUrl = responseItem.VideoUrl
	if len(responseItem.VideoUrls) > 0 {
		videoUrlsStr, err := common.Marshal(responseItem.VideoUrls)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("序列化 VideoUrls 失败: %v", err))
			task.VideoUrls = "[]"
		} else {
			task.VideoUrls = string(videoUrlsStr)
		}
	} else {
		task.VideoUrls = ""
	}

	shouldRefund := responseItem.FailReason != "" || task.Status == model.MidjourneyStatusFailure
	shouldSettle := !shouldRefund && task.Status == model.MidjourneyStatusSuccess
	if shouldRefund {
		logger.LogInfo(ctx, task.MjId+" 构建失败，"+task.FailReason)
		task.Progress = "100%"
		task.Status = model.MidjourneyStatusFailure
	} else if shouldSettle {
		task.Progress = "100%"
		if err := task.PrivateData.RecordBillingTargetQuota(task.Quota); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Midjourney task %s billing intent conflict: %v", task.MjId, err))
			return
		}
	}
	won, err := task.UpdateWithStatus(preStatus)
	if err != nil {
		logger.LogError(ctx, "UpdateMidjourneyTask task error: "+err.Error())
		return
	}
	if !won {
		logger.LogInfo(ctx, fmt.Sprintf("Midjourney task %s already transitioned, skip duplicate billing", task.MjId))
		return
	}
	if shouldSettle {
		service.SettleMidjourneyTaskQuota(ctx, task, task.Quota, "Midjourney 任务成功结算")
	}
	if shouldRefund {
		service.RefundMidjourneyTaskQuota(ctx, task, task.FailReason)
	}
}

func checkMjTaskNeedUpdate(oldTask *model.Midjourney, newTask dto.MidjourneyDto) bool {
	if oldTask.Code != 1 {
		return true
	}
	if oldTask.Progress != newTask.Progress {
		return true
	}
	if oldTask.PromptEn != newTask.PromptEn {
		return true
	}
	if oldTask.State != newTask.State {
		return true
	}
	if oldTask.SubmitTime != newTask.SubmitTime {
		return true
	}
	if oldTask.StartTime != newTask.StartTime {
		return true
	}
	if oldTask.FinishTime != newTask.FinishTime {
		return true
	}
	if oldTask.ImageUrl != newTask.ImageUrl {
		return true
	}
	if oldTask.Status != newTask.Status {
		return true
	}
	if oldTask.FailReason != newTask.FailReason {
		return true
	}
	if oldTask.FinishTime != newTask.FinishTime {
		return true
	}
	if oldTask.Progress != "100%" && newTask.FailReason != "" {
		return true
	}
	// 检查 VideoUrl 是否需要更新
	if oldTask.VideoUrl != newTask.VideoUrl {
		return true
	}
	// 检查 VideoUrls 是否需要更新
	if newTask.VideoUrls != nil && len(newTask.VideoUrls) > 0 {
		newVideoUrlsStr, _ := common.Marshal(newTask.VideoUrls)
		if oldTask.VideoUrls != string(newVideoUrlsStr) {
			return true
		}
	} else if oldTask.VideoUrls != "" {
		// 如果新数据没有 VideoUrls 但旧数据有，需要更新（清空）
		return true
	}

	return false
}

func GetAllMidjourney(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)

	// 解析其他查询参数
	queryParams := model.TaskQueryParams{
		ChannelID:      c.Query("channel_id"),
		MjID:           c.Query("mj_id"),
		StartTimestamp: c.Query("start_timestamp"),
		EndTimestamp:   c.Query("end_timestamp"),
	}

	items := model.GetAllTasks(pageInfo.GetStartIdx(), pageInfo.GetPageSize(), queryParams)
	total := model.CountAllTasks(queryParams)

	if setting.MjForwardUrlEnabled {
		for i, midjourney := range items {
			midjourney.ImageUrl = system_setting.ServerAddress + "/mj/image/" + midjourney.MjId
			items[i] = midjourney
		}
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func GetUserMidjourney(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)

	userId := c.GetInt("id")

	queryParams := model.TaskQueryParams{
		MjID:           c.Query("mj_id"),
		StartTimestamp: c.Query("start_timestamp"),
		EndTimestamp:   c.Query("end_timestamp"),
	}

	items := model.GetAllUserTask(userId, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), queryParams)
	total := model.CountAllUserTask(userId, queryParams)

	if setting.MjForwardUrlEnabled {
		for i, midjourney := range items {
			midjourney.ImageUrl = system_setting.ServerAddress + "/mj/image/" + midjourney.MjId
			items[i] = midjourney
		}
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}
