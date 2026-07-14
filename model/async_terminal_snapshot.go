package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	asyncTerminalPayloadProtocol       = 1
	maxAsyncTerminalPayloadBytes       = 3 << 20
	maxAsyncTerminalDataBytes          = 2 << 20
	maxAsyncTerminalURLBytes           = 16 << 10
	maxAsyncTerminalTextBytes          = 64 << 10
	maxAsyncTerminalJSONBytes          = 512 << 10
	terminalSnapshotRepairError        = "terminal snapshot repair failed: "
	asyncTerminalSnapshotAuditInterval = 24 * time.Hour
)

type taskTerminalSnapshot struct {
	ProtocolVersion   int             `json:"protocol_version"`
	TaskID            int64           `json:"task_id"`
	Status            TaskStatus      `json:"status"`
	Progress          string          `json:"progress"`
	SubmitTime        int64           `json:"submit_time"`
	StartTime         int64           `json:"start_time"`
	FinishTime        int64           `json:"finish_time"`
	FailReason        string          `json:"fail_reason,omitempty"`
	UpstreamResultURL string          `json:"upstream_result_url,omitempty"`
	ResultURL         string          `json:"result_url,omitempty"`
	Data              json.RawMessage `json:"data,omitempty"`
}

type midjourneyTerminalSnapshot struct {
	ProtocolVersion int    `json:"protocol_version"`
	MidjourneyID    int    `json:"midjourney_id"`
	Code            int    `json:"code"`
	PromptEn        string `json:"prompt_en,omitempty"`
	Description     string `json:"description,omitempty"`
	State           string `json:"state,omitempty"`
	SubmitTime      int64  `json:"submit_time"`
	StartTime       int64  `json:"start_time"`
	FinishTime      int64  `json:"finish_time"`
	ImageURL        string `json:"image_url,omitempty"`
	VideoURL        string `json:"video_url,omitempty"`
	VideoURLs       string `json:"video_urls,omitempty"`
	Status          string `json:"status"`
	Progress        string `json:"progress"`
	FailReason      string `json:"fail_reason,omitempty"`
	Buttons         string `json:"buttons,omitempty"`
	Properties      string `json:"properties,omitempty"`
}

type TerminalSnapshotRepairPage struct {
	NextID   int64
	Scanned  int
	Repaired int
	Failed   int
	Done     bool
}

func freezeTaskTerminalSnapshot(operation *TaskBillingOperation, task *Task) error {
	if operation == nil || task == nil || task.ID <= 0 || operation.TaskID != task.ID ||
		(task.Status != TaskStatusSuccess && task.Status != TaskStatusFailure) || operation.TerminalStatus != task.Status ||
		len(task.Data) > maxAsyncTerminalDataBytes || !utf8.Valid(task.Data) ||
		strings.TrimSpace(task.Progress) != "100%" || len(task.Progress) > 32 ||
		!utf8.ValidString(task.Progress) || strings.ContainsRune(task.Progress, '\x00') ||
		len(task.FailReason) > maxTaskBillingLastErrorBytes || !utf8.ValidString(task.FailReason) ||
		strings.ContainsRune(task.FailReason, '\x00') ||
		len(task.PrivateData.UpstreamResultURL) > maxAsyncTerminalURLBytes ||
		len(task.PrivateData.ResultURL) > maxAsyncTerminalURLBytes ||
		!utf8.ValidString(task.PrivateData.UpstreamResultURL) || !utf8.ValidString(task.PrivateData.ResultURL) ||
		strings.ContainsAny(task.PrivateData.UpstreamResultURL, "\r\n\x00") ||
		strings.ContainsAny(task.PrivateData.ResultURL, "\r\n\x00") {
		return ErrTaskBillingOperationInvariant
	}
	if len(task.Data) > 0 {
		var decoded any
		if err := common.Unmarshal(task.Data, &decoded); err != nil {
			return ErrTaskBillingOperationInvariant
		}
	}
	snapshot := taskTerminalSnapshot{
		ProtocolVersion: asyncTerminalPayloadProtocol,
		TaskID:          task.ID, Status: task.Status, Progress: task.Progress,
		SubmitTime: task.SubmitTime, StartTime: task.StartTime, FinishTime: task.FinishTime,
		FailReason: task.FailReason, UpstreamResultURL: task.PrivateData.UpstreamResultURL,
		ResultURL: task.PrivateData.ResultURL, Data: append(json.RawMessage(nil), task.Data...),
	}
	payload, err := common.Marshal(snapshot)
	if err != nil || len(payload) == 0 || len(payload) > maxAsyncTerminalPayloadBytes || !utf8.Valid(payload) {
		return ErrTaskBillingOperationInvariant
	}
	digest := sha256.Sum256(payload)
	operation.TerminalPayloadProtocol = asyncTerminalPayloadProtocol
	operation.TerminalPayloadHash = hex.EncodeToString(digest[:])
	operation.TerminalPayload = payload
	return nil
}

func thawTaskTerminalSnapshot(operation *TaskBillingOperation) (*taskTerminalSnapshot, error) {
	if operation == nil || operation.TaskID <= 0 || operation.TerminalPayloadProtocol != asyncTerminalPayloadProtocol ||
		len(operation.TerminalPayload) == 0 || len(operation.TerminalPayload) > maxAsyncTerminalPayloadBytes ||
		!utf8.Valid(operation.TerminalPayload) || len(operation.TerminalPayloadHash) != sha256.Size*2 {
		return nil, ErrTaskBillingOperationInvariant
	}
	digest := sha256.Sum256(operation.TerminalPayload)
	if !strings.EqualFold(operation.TerminalPayloadHash, hex.EncodeToString(digest[:])) {
		return nil, ErrTaskBillingOperationInvariant
	}
	var snapshot taskTerminalSnapshot
	if err := common.Unmarshal(operation.TerminalPayload, &snapshot); err != nil ||
		snapshot.ProtocolVersion != asyncTerminalPayloadProtocol || snapshot.TaskID != operation.TaskID ||
		snapshot.Status != operation.TerminalStatus ||
		(snapshot.Status != TaskStatusSuccess && snapshot.Status != TaskStatusFailure) ||
		len(snapshot.Data) > maxAsyncTerminalDataBytes || !utf8.Valid(snapshot.Data) ||
		strings.TrimSpace(snapshot.Progress) != "100%" || len(snapshot.Progress) > 32 ||
		!utf8.ValidString(snapshot.Progress) || strings.ContainsRune(snapshot.Progress, '\x00') ||
		len(snapshot.FailReason) > maxTaskBillingLastErrorBytes || !utf8.ValidString(snapshot.FailReason) ||
		strings.ContainsRune(snapshot.FailReason, '\x00') ||
		len(snapshot.UpstreamResultURL) > maxAsyncTerminalURLBytes || len(snapshot.ResultURL) > maxAsyncTerminalURLBytes ||
		!utf8.ValidString(snapshot.UpstreamResultURL) || !utf8.ValidString(snapshot.ResultURL) ||
		strings.ContainsAny(snapshot.UpstreamResultURL, "\r\n\x00") || strings.ContainsAny(snapshot.ResultURL, "\r\n\x00") {
		return nil, ErrTaskBillingOperationInvariant
	}
	if len(snapshot.Data) > 0 {
		var decoded any
		if err := common.Unmarshal(snapshot.Data, &decoded); err != nil {
			return nil, ErrTaskBillingOperationInvariant
		}
	}
	return &snapshot, nil
}

func repairTaskTerminalSnapshotTx(
	tx *gorm.DB,
	operation *TaskBillingOperation,
	task *Task,
	now time.Time,
) (bool, error) {
	if tx == nil || task == nil || operation == nil {
		return false, ErrTaskBillingOperationInvariant
	}
	backfilled := false
	if operation.TerminalPayloadProtocol == 0 && len(operation.TerminalPayload) == 0 && operation.TerminalPayloadHash == "" {
		if err := freezeTaskTerminalSnapshot(operation, task); err != nil {
			return false, err
		}
		updated := tx.Model(&TaskBillingOperation{}).Where(
			"id = ? AND terminal_payload_protocol = ?", operation.ID, 0,
		).Updates(map[string]any{
			"terminal_payload_protocol": operation.TerminalPayloadProtocol,
			"terminal_payload_hash":     operation.TerminalPayloadHash,
			"terminal_payload":          operation.TerminalPayload,
		})
		if updated.Error != nil {
			return false, updated.Error
		}
		if updated.RowsAffected != 1 {
			return false, ErrTaskBillingOperationInvariant
		}
		backfilled = true
	}
	snapshot, err := thawTaskTerminalSnapshot(operation)
	if err != nil {
		return false, err
	}
	matches := task.Status == snapshot.Status && task.Progress == snapshot.Progress &&
		task.SubmitTime == snapshot.SubmitTime && task.StartTime == snapshot.StartTime && task.FinishTime == snapshot.FinishTime &&
		task.FailReason == snapshot.FailReason && task.PrivateData.UpstreamResultURL == snapshot.UpstreamResultURL &&
		task.PrivateData.ResultURL == snapshot.ResultURL && bytes.Equal(task.Data, snapshot.Data)
	if matches {
		return backfilled, nil
	}
	privateData := task.PrivateData
	privateData.UpstreamResultURL = snapshot.UpstreamResultURL
	privateData.ResultURL = snapshot.ResultURL
	updates := map[string]any{
		"status": snapshot.Status, "progress": snapshot.Progress,
		"submit_time": snapshot.SubmitTime, "start_time": snapshot.StartTime, "finish_time": snapshot.FinishTime,
		"fail_reason": snapshot.FailReason, "private_data": privateData,
		"data": json.RawMessage(append([]byte(nil), snapshot.Data...)), "updated_at": now.Unix(),
	}
	task.PrivateData = privateData
	if task.PrivateData.BillingProtocolVersion == TaskBillingProtocolVersion {
		if err := task.freezeV2PrivateData(); err != nil {
			return false, err
		}
		updates["durable_private_data"] = task.DurablePrivateDataPayload
		updates["durable_private_data_hash"] = task.DurablePrivateDataHash
	}
	if err := tx.Model(&Task{}).Where("id = ?", task.ID).Updates(updates).Error; err != nil {
		return false, err
	}
	task.Status = snapshot.Status
	task.Progress = snapshot.Progress
	task.SubmitTime = snapshot.SubmitTime
	task.StartTime = snapshot.StartTime
	task.FinishTime = snapshot.FinishTime
	task.FailReason = snapshot.FailReason
	task.Data = append(json.RawMessage(nil), snapshot.Data...)
	task.UpdatedAt = now.Unix()
	return true, nil
}

func freezeMidjourneyTerminalSnapshot(operation *MidjourneyBillingOperation, task *Midjourney) error {
	if operation == nil || task == nil || task.Id <= 0 || operation.MidjourneyID != task.Id ||
		(task.Status != "SUCCESS" && task.Status != "FAILURE") || task.Progress != "100%" ||
		operation.TerminalStatus != task.Status {
		return ErrMidjourneyBillingOperationInvariant
	}
	textFields := []string{task.PromptEn, task.Description, task.State, task.FailReason}
	for _, value := range textFields {
		if !utf8.ValidString(value) || len(value) > maxAsyncTerminalTextBytes || strings.ContainsRune(value, '\x00') {
			return ErrMidjourneyBillingOperationInvariant
		}
	}
	urlFields := []string{task.ImageUrl, task.VideoUrl}
	for _, value := range urlFields {
		if !utf8.ValidString(value) || len(value) > maxAsyncTerminalURLBytes || strings.ContainsAny(value, "\r\n\x00") {
			return ErrMidjourneyBillingOperationInvariant
		}
	}
	jsonFields := []string{task.VideoUrls, task.Buttons, task.Properties}
	for _, value := range jsonFields {
		if !utf8.ValidString(value) || len(value) > maxAsyncTerminalJSONBytes || strings.ContainsRune(value, '\x00') {
			return ErrMidjourneyBillingOperationInvariant
		}
	}
	snapshot := midjourneyTerminalSnapshot{
		ProtocolVersion: asyncTerminalPayloadProtocol, MidjourneyID: task.Id,
		Code: task.Code, PromptEn: task.PromptEn, Description: task.Description, State: task.State,
		SubmitTime: task.SubmitTime, StartTime: task.StartTime, FinishTime: task.FinishTime,
		ImageURL: task.ImageUrl, VideoURL: task.VideoUrl, VideoURLs: task.VideoUrls,
		Status: task.Status, Progress: task.Progress, FailReason: task.FailReason,
		Buttons: task.Buttons, Properties: task.Properties,
	}
	payload, err := common.Marshal(snapshot)
	if err != nil || len(payload) == 0 || len(payload) > maxAsyncTerminalPayloadBytes || !utf8.Valid(payload) {
		return ErrMidjourneyBillingOperationInvariant
	}
	digest := sha256.Sum256(payload)
	operation.TerminalPayloadProtocol = asyncTerminalPayloadProtocol
	operation.TerminalPayloadHash = hex.EncodeToString(digest[:])
	operation.TerminalPayload = payload
	return nil
}

func thawMidjourneyTerminalSnapshot(operation *MidjourneyBillingOperation) (*midjourneyTerminalSnapshot, error) {
	if operation == nil || operation.MidjourneyID <= 0 || operation.TerminalPayloadProtocol != asyncTerminalPayloadProtocol ||
		len(operation.TerminalPayload) == 0 || len(operation.TerminalPayload) > maxAsyncTerminalPayloadBytes ||
		!utf8.Valid(operation.TerminalPayload) || len(operation.TerminalPayloadHash) != sha256.Size*2 {
		return nil, ErrMidjourneyBillingOperationInvariant
	}
	digest := sha256.Sum256(operation.TerminalPayload)
	if !strings.EqualFold(operation.TerminalPayloadHash, hex.EncodeToString(digest[:])) {
		return nil, ErrMidjourneyBillingOperationInvariant
	}
	var snapshot midjourneyTerminalSnapshot
	if err := common.Unmarshal(operation.TerminalPayload, &snapshot); err != nil ||
		snapshot.ProtocolVersion != asyncTerminalPayloadProtocol || snapshot.MidjourneyID != operation.MidjourneyID ||
		snapshot.Status != operation.TerminalStatus ||
		(snapshot.Status != "SUCCESS" && snapshot.Status != "FAILURE") || snapshot.Progress != "100%" {
		return nil, ErrMidjourneyBillingOperationInvariant
	}
	textFields := []string{snapshot.PromptEn, snapshot.Description, snapshot.State, snapshot.FailReason}
	for _, value := range textFields {
		if !utf8.ValidString(value) || len(value) > maxAsyncTerminalTextBytes || strings.ContainsRune(value, '\x00') {
			return nil, ErrMidjourneyBillingOperationInvariant
		}
	}
	urlFields := []string{snapshot.ImageURL, snapshot.VideoURL}
	for _, value := range urlFields {
		if !utf8.ValidString(value) || len(value) > maxAsyncTerminalURLBytes || strings.ContainsAny(value, "\r\n\x00") {
			return nil, ErrMidjourneyBillingOperationInvariant
		}
	}
	jsonFields := []string{snapshot.VideoURLs, snapshot.Buttons, snapshot.Properties}
	for _, value := range jsonFields {
		if !utf8.ValidString(value) || len(value) > maxAsyncTerminalJSONBytes || strings.ContainsRune(value, '\x00') {
			return nil, ErrMidjourneyBillingOperationInvariant
		}
	}
	return &snapshot, nil
}

func repairMidjourneyTerminalSnapshotTx(
	tx *gorm.DB,
	operation *MidjourneyBillingOperation,
	task *Midjourney,
) (bool, error) {
	if tx == nil || task == nil || operation == nil {
		return false, ErrMidjourneyBillingOperationInvariant
	}
	backfilled := false
	if operation.TerminalPayloadProtocol == 0 && len(operation.TerminalPayload) == 0 && operation.TerminalPayloadHash == "" {
		if operation.TerminalStatus == "" {
			operation.TerminalStatus = task.Status
		}
		if err := freezeMidjourneyTerminalSnapshot(operation, task); err != nil {
			return false, err
		}
		updated := tx.Model(&MidjourneyBillingOperation{}).Where(
			"id = ? AND terminal_payload_protocol = ?", operation.ID, 0,
		).Updates(map[string]any{
			"terminal_status":           operation.TerminalStatus,
			"terminal_payload_protocol": operation.TerminalPayloadProtocol,
			"terminal_payload_hash":     operation.TerminalPayloadHash,
			"terminal_payload":          operation.TerminalPayload,
		})
		if updated.Error != nil {
			return false, updated.Error
		}
		if updated.RowsAffected != 1 {
			return false, ErrMidjourneyBillingOperationInvariant
		}
		backfilled = true
	}
	snapshot, err := thawMidjourneyTerminalSnapshot(operation)
	if err != nil {
		return false, err
	}
	matches := task.Code == snapshot.Code && task.PromptEn == snapshot.PromptEn &&
		task.Description == snapshot.Description && task.State == snapshot.State && task.SubmitTime == snapshot.SubmitTime &&
		task.StartTime == snapshot.StartTime && task.FinishTime == snapshot.FinishTime && task.ImageUrl == snapshot.ImageURL &&
		task.VideoUrl == snapshot.VideoURL && task.VideoUrls == snapshot.VideoURLs && task.Status == snapshot.Status &&
		task.Progress == snapshot.Progress && task.FailReason == snapshot.FailReason && task.Buttons == snapshot.Buttons &&
		task.Properties == snapshot.Properties
	if matches {
		return backfilled, nil
	}
	updates := map[string]any{
		"code": snapshot.Code, "prompt_en": snapshot.PromptEn, "description": snapshot.Description,
		"state": snapshot.State, "submit_time": snapshot.SubmitTime, "start_time": snapshot.StartTime,
		"finish_time": snapshot.FinishTime, "image_url": snapshot.ImageURL, "video_url": snapshot.VideoURL,
		"video_urls": snapshot.VideoURLs, "status": snapshot.Status, "progress": snapshot.Progress,
		"fail_reason": snapshot.FailReason, "buttons": snapshot.Buttons, "properties": snapshot.Properties,
	}
	if err := tx.Model(&Midjourney{}).Where("id = ?", task.Id).Updates(updates).Error; err != nil {
		return false, err
	}
	task.Code = snapshot.Code
	task.PromptEn = snapshot.PromptEn
	task.Description = snapshot.Description
	task.State = snapshot.State
	task.SubmitTime = snapshot.SubmitTime
	task.StartTime = snapshot.StartTime
	task.FinishTime = snapshot.FinishTime
	task.ImageUrl = snapshot.ImageURL
	task.VideoUrl = snapshot.VideoURL
	task.VideoUrls = snapshot.VideoURLs
	task.Status = snapshot.Status
	task.Progress = snapshot.Progress
	task.FailReason = snapshot.FailReason
	task.Buttons = snapshot.Buttons
	task.Properties = snapshot.Properties
	return true, nil
}

func RepairTaskTerminalSnapshotPage(
	ctx context.Context,
	afterID int64,
	limit int,
	now time.Time,
) (TerminalSnapshotRepairPage, error) {
	page := TerminalSnapshotRepairPage{NextID: afterID}
	if afterID < 0 || limit <= 0 || limit > 1000 {
		return page, ErrTaskBillingOperationInvariant
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	retryLimit := limit / 4
	var retryIDs []int64
	if retryLimit > 0 && afterID > 0 {
		if err := DB.WithContext(ctx).Model(&TaskBillingOperation{}).
			Where("id <= ? AND last_error LIKE ?", afterID, terminalSnapshotRepairError+"%").
			Order("updated_time_ms asc, id asc").Limit(retryLimit).Pluck("id", &retryIDs).Error; err != nil {
			return page, err
		}
	}
	forwardLimit := limit - len(retryIDs)
	var forwardIDs []int64
	if err := DB.WithContext(ctx).Model(&TaskBillingOperation{}).Where("id > ?", afterID).
		Order("id asc").Limit(forwardLimit).Pluck("id", &forwardIDs).Error; err != nil {
		return page, err
	}
	ids := append(retryIDs, forwardIDs...)
	page.Scanned = len(ids)
	page.Done = len(forwardIDs) < forwardLimit
	for _, operationID := range ids {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		if operationID > page.NextID {
			page.NextID = operationID
		}
		repaired := false
		err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var operation TaskBillingOperation
			if err := lockForUpdate(tx).Where("id = ?", operationID).First(&operation).Error; err != nil {
				return err
			}
			var task Task
			if err := lockForUpdate(tx).Where("id = ?", operation.TaskID).First(&task).Error; err != nil {
				return err
			}
			var err error
			repaired, err = repairTaskTerminalSnapshotTx(tx, &operation, &task, now)
			if err != nil {
				return err
			}
			updates := map[string]any{"updated_time_ms": now.UnixMilli()}
			if strings.HasPrefix(operation.LastError, terminalSnapshotRepairError) {
				updates["last_error"] = ""
			}
			return tx.Model(&TaskBillingOperation{}).Where("id = ?", operation.ID).Updates(updates).Error
		})
		if err != nil {
			page.Failed++
			message := boundedTaskBillingError(terminalSnapshotRepairError + err.Error())
			_ = DB.WithContext(context.WithoutCancel(ctx)).Model(&TaskBillingOperation{}).
				Where("id = ?", operationID).Updates(map[string]any{
				"last_error": message, "updated_time_ms": now.UnixMilli(),
			}).Error
			common.SysError(fmt.Sprintf("task terminal snapshot repair failed: operation=%d error=%s", operationID, message))
			continue
		}
		if repaired {
			page.Repaired++
		}
	}
	if page.Done {
		page.NextID = 0
	}
	return page, nil
}

func RepairMidjourneyTerminalSnapshotPage(
	ctx context.Context,
	afterID int64,
	limit int,
	now time.Time,
) (TerminalSnapshotRepairPage, error) {
	page := TerminalSnapshotRepairPage{NextID: afterID}
	if afterID < 0 || limit <= 0 || limit > 1000 {
		return page, ErrMidjourneyBillingOperationInvariant
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	retryLimit := limit / 4
	var retryIDs []int64
	if retryLimit > 0 && afterID > 0 {
		if err := DB.WithContext(ctx).Model(&MidjourneyBillingOperation{}).
			Where("id <= ? AND last_error LIKE ?", afterID, terminalSnapshotRepairError+"%").
			Order("updated_time_ms asc, id asc").Limit(retryLimit).Pluck("id", &retryIDs).Error; err != nil {
			return page, err
		}
	}
	forwardLimit := limit - len(retryIDs)
	var forwardIDs []int64
	if err := DB.WithContext(ctx).Model(&MidjourneyBillingOperation{}).Where("id > ?", afterID).
		Order("id asc").Limit(forwardLimit).Pluck("id", &forwardIDs).Error; err != nil {
		return page, err
	}
	ids := append(retryIDs, forwardIDs...)
	page.Scanned = len(ids)
	page.Done = len(forwardIDs) < forwardLimit
	for _, operationID := range ids {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		if operationID > page.NextID {
			page.NextID = operationID
		}
		repaired := false
		err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var operation MidjourneyBillingOperation
			if err := lockForUpdate(tx).Where("id = ?", operationID).First(&operation).Error; err != nil {
				return err
			}
			var task Midjourney
			if err := lockForUpdate(tx).Where("id = ?", operation.MidjourneyID).First(&task).Error; err != nil {
				return err
			}
			var err error
			repaired, err = repairMidjourneyTerminalSnapshotTx(tx, &operation, &task)
			if err != nil {
				return err
			}
			updates := map[string]any{"updated_time_ms": now.UnixMilli()}
			if strings.HasPrefix(operation.LastError, terminalSnapshotRepairError) {
				updates["last_error"] = ""
			}
			return tx.Model(&MidjourneyBillingOperation{}).Where("id = ?", operation.ID).Updates(updates).Error
		})
		if err != nil {
			page.Failed++
			message := boundedTaskBillingError(terminalSnapshotRepairError + err.Error())
			_ = DB.WithContext(context.WithoutCancel(ctx)).Model(&MidjourneyBillingOperation{}).
				Where("id = ?", operationID).Updates(map[string]any{
				"last_error": message, "updated_time_ms": now.UnixMilli(),
			}).Error
			common.SysError(fmt.Sprintf("Midjourney terminal snapshot repair failed: operation=%d error=%s", operationID, message))
			continue
		}
		if repaired {
			page.Repaired++
		}
	}
	if page.Done {
		page.NextID = 0
	}
	return page, nil
}
