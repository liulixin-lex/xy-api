package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func ListInviteLinkBatches(c *gin.Context) {
	batches, err := model.ListInviteLinkBatchesWithStats(common.GetTimestamp())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, batches)
}

func CreateInviteLinkBatch(c *gin.Context) {
	var batch model.InviteLinkBatch
	if err := common.DecodeJson(c.Request.Body, &batch); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.CreateInviteLinkBatch(&batch); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, batch)
}

func UpdateInviteLinkBatch(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "invalid invite link batch id")
		return
	}

	var batch model.InviteLinkBatch
	if err := common.DecodeJson(c.Request.Body, &batch); err != nil {
		common.ApiError(c, err)
		return
	}
	batch.Id = id
	if err := model.UpdateInviteLinkBatch(&batch); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, batch)
}

func ActivateInviteLinkBatch(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "invalid invite link batch id")
		return
	}
	if err := model.SetActiveInviteLinkBatch(id); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func GenerateInviteLinkBatchRandomLink(c *gin.Context) {
	code := model.GenerateInviteLinkBatchCode()
	common.ApiSuccess(c, gin.H{
		"code":      code,
		"base_link": model.BuildInviteLinkBatchBaseLink("", code),
	})
}
