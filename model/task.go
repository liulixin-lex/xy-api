package model

import (
	"bytes"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	commonRelay "github.com/QuantumNous/new-api/relay/common"

	"gorm.io/gorm"
)

var ErrTaskIdentityAmbiguous = errors.New("task identity is ambiguous")

type TaskStatus string

func (t TaskStatus) ToVideoStatus() string {
	var status string
	switch t {
	case TaskStatusQueued, TaskStatusSubmitted:
		status = dto.VideoStatusQueued
	case TaskStatusInProgress:
		status = dto.VideoStatusInProgress
	case TaskStatusSuccess:
		status = dto.VideoStatusCompleted
	case TaskStatusFailure:
		status = dto.VideoStatusFailed
	default:
		status = dto.VideoStatusUnknown // Default fallback
	}
	return status
}

const (
	// Version 0 is the implicit protocol of rows written before the field was
	// introduced. It remains readable so an upgrade can durably settle tasks
	// that were still in flight when the old binary stopped.
	TaskBillingHistoricalProtocolVersion = 0
	TaskBillingLegacyProtocolVersion     = 1
	TaskBillingProtocolVersion           = 2

	TaskStatusNotStart   TaskStatus = "NOT_START"
	TaskStatusSubmitted             = "SUBMITTED"
	TaskStatusQueued                = "QUEUED"
	TaskStatusInProgress            = "IN_PROGRESS"
	TaskStatusFailure               = "FAILURE"
	TaskStatusSuccess               = "SUCCESS"
	TaskStatusUnknown               = "UNKNOWN"
)

type Task struct {
	ID                        int64                 `json:"id" gorm:"primaryKey"`
	CreatedAt                 int64                 `json:"created_at" gorm:"index"`
	UpdatedAt                 int64                 `json:"updated_at"`
	TaskID                    string                `json:"task_id" gorm:"type:varchar(191);index"` // 第三方id，不一定有/ song id\ Task id
	Platform                  constant.TaskPlatform `json:"platform" gorm:"type:varchar(30);index"` // 平台
	UserId                    int                   `json:"user_id" gorm:"index"`
	Group                     string                `json:"group" gorm:"type:varchar(50)"` // 修正计费用
	ChannelId                 int                   `json:"channel_id" gorm:"index"`
	Quota                     int                   `json:"quota"`
	DurableQuota              int                   `json:"-" gorm:"column:durable_quota"`
	DurablePrivateDataPayload []byte                `json:"-" gorm:"column:durable_private_data"`
	DurablePrivateDataHash    string                `json:"-" gorm:"column:durable_private_data_hash;type:varchar(64)"`
	Action                    string                `json:"action" gorm:"type:varchar(40);index"` // 任务类型, song, lyrics, description-mode
	Status                    TaskStatus            `json:"status" gorm:"type:varchar(20);index"` // 任务状态
	FailReason                string                `json:"fail_reason"`
	SubmitTime                int64                 `json:"submit_time" gorm:"index"`
	StartTime                 int64                 `json:"start_time" gorm:"index"`
	FinishTime                int64                 `json:"finish_time" gorm:"index"`
	Progress                  string                `json:"progress" gorm:"type:varchar(20);index"`
	Properties                Properties            `json:"properties" gorm:"type:json"`
	Username                  string                `json:"username,omitempty" gorm:"-"`
	// 禁止返回给用户，内部可能包含key等隐私信息
	PrivateData TaskPrivateData `json:"-" gorm:"column:private_data;type:json"`
	Data        json.RawMessage `json:"data" gorm:"type:json"`
}

func (t *Task) SetData(data any) {
	b, _ := common.Marshal(data)
	t.Data = json.RawMessage(b)
}

func (t *Task) GetData(v any) error {
	return common.Unmarshal(t.Data, &v)
}

type Properties struct {
	Input             string `json:"input"`
	UpstreamModelName string `json:"upstream_model_name,omitempty"`
	OriginModelName   string `json:"origin_model_name,omitempty"`
}

// GetOriginModelName returns the client-facing model recorded for a task.
// Historical rows may only have the model in their billing snapshot, upstream
// model field, or raw task payload, so stateful authorization uses the same
// ordered fallback everywhere.
func (t *Task) GetOriginModelName() string {
	if t == nil {
		return ""
	}
	if modelName := strings.TrimSpace(t.Properties.OriginModelName); modelName != "" {
		return modelName
	}
	if billingContext := t.EffectiveBillingContext(); billingContext != nil {
		if modelName := strings.TrimSpace(billingContext.OriginModelName); modelName != "" {
			return modelName
		}
	}
	if modelName := strings.TrimSpace(t.Properties.UpstreamModelName); modelName != "" {
		return modelName
	}
	var taskData map[string]any
	if common.Unmarshal(t.Data, &taskData) == nil {
		if modelName, ok := taskData["model"].(string); ok {
			return strings.TrimSpace(modelName)
		}
	}
	return ""
}

func (m *Properties) Scan(val interface{}) error {
	bytesValue, _ := val.([]byte)
	if len(bytesValue) == 0 {
		*m = Properties{}
		return nil
	}
	return common.Unmarshal(bytesValue, m)
}

func (m Properties) Value() (driver.Value, error) {
	if m == (Properties{}) {
		return nil, nil
	}
	return common.Marshal(m)
}

type TaskPrivateData struct {
	BillingProtocolVersion    int   `json:"billing_protocol_version,omitempty"`
	AsyncBillingReservationID int64 `json:"async_billing_reservation_id,omitempty"`
	// Key is retained only for decoding historical rows. New tasks must persist
	// the stable credential identity instead of plaintext credential material.
	Key                      string `json:"key,omitempty"`
	RoutingCredentialID      int    `json:"routing_credential_id,omitempty"`
	RoutingChannelGeneration string `json:"routing_channel_generation,omitempty"`
	UpstreamTaskID           string `json:"upstream_task_id,omitempty"` // 上游真实 task ID
	UpstreamResultURL        string `json:"upstream_result_url,omitempty"`
	// ResultURL is retained only for decoding historical rows. New tasks store
	// provider result locations in UpstreamResultURL and derive the public URL.
	ResultURL string `json:"result_url,omitempty"`
	// 计费上下文：用于异步退款/差额结算（轮询阶段读取）
	BillingSource  string              `json:"billing_source,omitempty"`  // "wallet" 或 "subscription"
	SubscriptionId int                 `json:"subscription_id,omitempty"` // 订阅 ID，用于订阅退款
	TokenId        int                 `json:"token_id,omitempty"`        // 令牌 ID，用于令牌额度退款
	NodeName       string              `json:"node_name,omitempty"`       // 发起任务的节点名，轮询结算阶段据此归属日志而非最后查询节点
	BillingContext *TaskBillingContext `json:"billing_context,omitempty"` // 计费参数快照（用于轮询阶段重新计算）
	// DurableBillingContext is the authoritative v2 snapshot. BillingContext
	// remains a legacy-safe view so an old poller cannot perform settlement.
	DurableBillingContext *TaskBillingContext                `json:"durable_billing_context,omitempty"`
	BillingAudit          *AsyncBillingAcceptedAuditSnapshot `json:"billing_audit,omitempty"`
}

func (t *Task) EffectiveBillingQuota() int {
	if t != nil && t.PrivateData.BillingProtocolVersion == TaskBillingProtocolVersion {
		return t.DurableQuota
	}
	if t == nil {
		return 0
	}
	return t.Quota
}

func (t *Task) EffectiveBillingContext() *TaskBillingContext {
	if t == nil {
		return nil
	}
	if t.PrivateData.BillingProtocolVersion == TaskBillingProtocolVersion &&
		t.PrivateData.DurableBillingContext != nil {
		return t.PrivateData.DurableBillingContext
	}
	return t.PrivateData.BillingContext
}

// IsolateV2BillingFromLegacyPollers keeps the real charge in v2-only fields.
// Old binaries see quota=0 and a per-call snapshot, so terminal polling may
// update task state but cannot mutate balances.
func (t *Task) IsolateV2BillingFromLegacyPollers(quota int) error {
	if t == nil || t.PrivateData.BillingProtocolVersion != TaskBillingProtocolVersion ||
		quota < 0 || quota > common.MaxQuota {
		return errors.New("invalid v2 task billing isolation")
	}
	t.DurableQuota = quota
	t.Quota = 0
	if t.PrivateData.DurableBillingContext == nil && t.PrivateData.BillingContext != nil {
		durable := *t.PrivateData.BillingContext
		durable.OtherRatios = copyTaskBillingRatios(t.PrivateData.BillingContext.OtherRatios)
		t.PrivateData.DurableBillingContext = &durable
	}
	if t.PrivateData.BillingContext != nil {
		legacySafe := *t.PrivateData.BillingContext
		legacySafe.OtherRatios = copyTaskBillingRatios(t.PrivateData.BillingContext.OtherRatios)
		legacySafe.PerCallBilling = true
		t.PrivateData.BillingContext = &legacySafe
	} else {
		t.PrivateData.BillingContext = &TaskBillingContext{PerCallBilling: true}
	}
	return t.freezeV2PrivateData()
}

func (t *Task) freezeV2PrivateData() error {
	if t == nil || t.PrivateData.BillingProtocolVersion != TaskBillingProtocolVersion ||
		t.PrivateData.AsyncBillingReservationID <= 0 {
		return errors.New("invalid v2 task private data")
	}
	payload, err := common.Marshal(t.PrivateData)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	t.DurablePrivateDataPayload = payload
	t.DurablePrivateDataHash = hex.EncodeToString(digest[:])
	return nil
}

func (t *Task) restoreV2PrivateData() error {
	if t == nil || len(t.DurablePrivateDataPayload) == 0 {
		if t != nil && t.PrivateData.BillingProtocolVersion == TaskBillingProtocolVersion {
			return errors.New("v2 task private data snapshot is missing")
		}
		return nil
	}
	legacyUpstreamResultURL := strings.TrimSpace(t.PrivateData.UpstreamResultURL)
	legacyResultURL := strings.TrimSpace(t.PrivateData.ResultURL)
	if len(t.DurablePrivateDataHash) != sha256.Size*2 {
		return errors.New("v2 task private data hash is invalid")
	}
	digest := sha256.Sum256(t.DurablePrivateDataPayload)
	if !strings.EqualFold(t.DurablePrivateDataHash, hex.EncodeToString(digest[:])) {
		return errors.New("v2 task private data hash mismatch")
	}
	var privateData TaskPrivateData
	if err := common.Unmarshal(t.DurablePrivateDataPayload, &privateData); err != nil ||
		privateData.BillingProtocolVersion != TaskBillingProtocolVersion ||
		privateData.AsyncBillingReservationID <= 0 {
		return errors.New("v2 task private data snapshot is invalid")
	}
	// Old pollers rewrite private_data without fields they do not understand.
	// Merge only terminal media locations; billing and credential fields remain
	// authoritative in the hashed durable snapshot.
	if legacyUpstreamResultURL != "" {
		privateData.UpstreamResultURL = legacyUpstreamResultURL
	}
	if legacyResultURL != "" {
		privateData.ResultURL = legacyResultURL
	}
	t.PrivateData = privateData
	return nil
}

func (t *Task) BeforeSave(_ *gorm.DB) error {
	if t != nil && t.PrivateData.BillingProtocolVersion == TaskBillingProtocolVersion {
		return t.freezeV2PrivateData()
	}
	return nil
}

func (t *Task) AfterFind(_ *gorm.DB) error {
	return t.restoreV2PrivateData()
}

func copyTaskBillingRatios(ratios map[string]float64) map[string]float64 {
	if ratios == nil {
		return nil
	}
	copyRatios := make(map[string]float64, len(ratios))
	for key, ratio := range ratios {
		copyRatios[key] = ratio
	}
	return copyRatios
}

// HistoricalPlaintextCredential returns credential material only for rows
// written before the v2 stable-credential protocol. V2 rows must resolve their
// persisted credential identity even if malformed historical data left Key set.
func (p TaskPrivateData) HistoricalPlaintextCredential() (string, bool) {
	if p.RoutingCredentialID != 0 || p.BillingProtocolVersion < 0 ||
		p.BillingProtocolVersion >= TaskBillingProtocolVersion {
		return "", false
	}
	credential := strings.TrimSpace(p.Key)
	return credential, credential != ""
}

// SupportsDurableTaskBillingProtocol identifies versions whose non-terminal
// rows can enter the terminal-operation protocol. A version-0 row that was
// already terminal before upgrade remains historical-closed because its old
// settlement side effect cannot be distinguished from an interrupted one.
// Version 2 additionally requires an accepted-handoff reservation; unknown
// future and malformed versions fail closed.
func SupportsDurableTaskBillingProtocol(version int) bool {
	return version >= TaskBillingHistoricalProtocolVersion && version <= TaskBillingProtocolVersion
}

// TaskBillingContext 记录任务提交时的计费参数，以便轮询阶段可以重新计算额度。
type TaskBillingContext struct {
	ModelPrice      float64            `json:"model_price,omitempty"`       // 模型单价
	GroupRatio      float64            `json:"group_ratio,omitempty"`       // 分组倍率
	ModelRatio      float64            `json:"model_ratio,omitempty"`       // 模型倍率
	OtherRatios     map[string]float64 `json:"other_ratios,omitempty"`      // 附加倍率（时长、分辨率等）
	OriginModelName string             `json:"origin_model_name,omitempty"` // 模型名称，必须为OriginModelName
	PerCallBilling  bool               `json:"per_call_billing,omitempty"`  // 按次计费：跳过轮询阶段的差额结算
}

// GetUpstreamTaskID 获取上游真实 task ID（用于与 provider 通信）
// 旧数据没有 UpstreamTaskID 时，TaskID 本身就是上游 ID
func (t *Task) GetUpstreamTaskID() string {
	if t.PrivateData.UpstreamTaskID != "" {
		return t.PrivateData.UpstreamTaskID
	}
	return t.TaskID
}

// GetUpstreamResultURL returns the private provider result location used by the
// authenticated content proxy. Historical rows may have stored it in ResultURL
// or, before PrivateData existed, in FailReason.
func (t *Task) GetUpstreamResultURL() string {
	if t == nil {
		return ""
	}
	if resultURL := strings.TrimSpace(t.PrivateData.UpstreamResultURL); resultURL != "" {
		return resultURL
	}
	if resultURL := strings.TrimSpace(t.PrivateData.ResultURL); resultURL != "" && !t.isLocalResultProxyURL(resultURL) {
		return resultURL
	}
	legacy := strings.TrimSpace(t.FailReason)
	if (strings.HasPrefix(legacy, "https://") || strings.HasPrefix(legacy, "http://") || strings.HasPrefix(legacy, "data:")) &&
		!t.isLocalResultProxyURL(legacy) {
		return legacy
	}
	return ""
}

func (t *Task) SetUpstreamResultURL(resultURL string) {
	if t == nil {
		return
	}
	t.PrivateData.UpstreamResultURL = strings.TrimSpace(resultURL)
}

// GetResultURL returns only the local authenticated proxy location. Provider
// URLs and embedded media are never part of the public task contract.
func (t *Task) GetResultURL() string {
	if t == nil || t.Status != TaskStatusSuccess || t.Platform == constant.TaskPlatformSuno || strings.TrimSpace(t.TaskID) == "" {
		return ""
	}
	return "/v1/videos/" + url.PathEscape(t.TaskID) + "/content"
}

func (t *Task) isLocalResultProxyURL(resultURL string) bool {
	if t == nil || strings.TrimSpace(t.TaskID) == "" {
		return false
	}
	return strings.Contains(resultURL, "/v1/videos/"+url.PathEscape(t.TaskID)+"/content")
}

// GenerateTaskID 生成对外暴露的 task_xxxx 格式 ID
func GenerateTaskID() string {
	key, _ := common.GenerateRandomCharsKey(32)
	return "task_" + key
}

func (p *TaskPrivateData) Scan(val interface{}) error {
	bytesValue, _ := val.([]byte)
	if len(bytesValue) == 0 {
		return nil
	}
	return common.Unmarshal(bytesValue, p)
}

func (p TaskPrivateData) Value() (driver.Value, error) {
	if (p == TaskPrivateData{}) {
		return nil, nil
	}
	return common.Marshal(p)
}

// SyncTaskQueryParams 用于包含所有搜索条件的结构体，可以根据需求添加更多字段
type SyncTaskQueryParams struct {
	Platform       constant.TaskPlatform
	ChannelID      string
	TaskID         string
	UserID         string
	Action         string
	Status         string
	StartTimestamp int64
	EndTimestamp   int64
	UserIDs        []int
}

func InitTask(platform constant.TaskPlatform, relayInfo *commonRelay.RelayInfo) *Task {
	properties := Properties{}
	privateData := TaskPrivateData{}
	if relayInfo != nil && relayInfo.ChannelMeta != nil {
		privateData.RoutingCredentialID = relayInfo.ChannelMeta.RoutingCredentialID
		if relayInfo.UpstreamModelName != "" {
			properties.UpstreamModelName = relayInfo.UpstreamModelName
		}
		if relayInfo.OriginModelName != "" {
			properties.OriginModelName = relayInfo.OriginModelName
		}
	}

	// 使用预生成的公开 ID（如果有），否则新生成
	taskID := ""
	if relayInfo.TaskRelayInfo != nil && relayInfo.TaskRelayInfo.PublicTaskID != "" {
		taskID = relayInfo.TaskRelayInfo.PublicTaskID
	} else {
		taskID = GenerateTaskID()
	}

	t := &Task{
		TaskID:      taskID,
		UserId:      relayInfo.UserId,
		Group:       relayInfo.UsingGroup,
		SubmitTime:  time.Now().Unix(),
		Status:      TaskStatusNotStart,
		Progress:    "0%",
		ChannelId:   relayInfo.ChannelId,
		Platform:    platform,
		Properties:  properties,
		PrivateData: privateData,
	}
	return t
}

func TaskGetAllUserTask(userId int, startIdx int, num int, queryParams SyncTaskQueryParams) []*Task {
	var tasks []*Task
	var err error

	// 初始化查询构建器
	query := DB.Where("user_id = ?", userId)

	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.StartTimestamp != 0 {
		// 假设您已将前端传来的时间戳转换为数据库所需的时间格式，并处理了时间戳的验证和解析
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Omit("channel_id").Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func TaskGetAllTasks(startIdx int, num int, queryParams SyncTaskQueryParams) []*Task {
	var tasks []*Task
	var err error

	// 初始化查询构建器
	query := DB

	// 添加过滤条件
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.UserID != "" {
		query = query.Where("user_id = ?", queryParams.UserID)
	}
	if len(queryParams.UserIDs) != 0 {
		query = query.Where("user_id in (?)", queryParams.UserIDs)
	}
	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.StartTimestamp != 0 {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func GetTimedOutUnfinishedTasks(cutoffUnix int64, limit int) []*Task {
	var tasks []*Task
	err := DB.Where("progress != ?", "100%").
		Where("status NOT IN ?", []string{TaskStatusFailure, TaskStatusSuccess}).
		Where("submit_time < ?", cutoffUnix).
		Order("submit_time").
		Limit(limit).
		Find(&tasks).Error
	if err != nil {
		return nil
	}
	return tasks
}

func GetAllUnFinishSyncTasks(limit int) []*Task {
	var tasks []*Task
	var err error
	// get all tasks progress is not 100%
	err = DB.Where("progress != ?", "100%").Where("status != ?", TaskStatusFailure).Where("status != ?", TaskStatusSuccess).Limit(limit).Order("id").Find(&tasks).Error
	if err != nil {
		return nil
	}
	return tasks
}

// HasUnfinishedSyncTasks reports whether at least one async (Suno/video) task is
// still in progress. It is a cheap existence check (LIMIT 1) used to decide
// whether the async_task_poll system task needs to run; when no task is pending
// the scheduler skips creating a row entirely.
func HasUnfinishedSyncTasks() bool {
	var id int64
	err := DB.Model(&Task{}).
		Where("progress != ?", "100%").
		Where("status != ?", TaskStatusFailure).
		Where("status != ?", TaskStatusSuccess).
		Limit(1).
		Pluck("id", &id).Error
	return err == nil && id != 0
}

func GetByOnlyTaskId(taskId string) (*Task, bool, error) {
	if taskId == "" {
		return nil, false, nil
	}
	var tasks []*Task
	if err := DB.Where("task_id = ?", taskId).Order("id asc").Limit(2).Find(&tasks).Error; err != nil {
		return nil, false, err
	}
	if len(tasks) > 1 {
		return nil, false, ErrTaskIdentityAmbiguous
	}
	if len(tasks) == 0 {
		return nil, false, nil
	}
	return tasks[0], true, nil
}

func GetByTaskId(userId int, taskId string) (*Task, bool, error) {
	if taskId == "" {
		return nil, false, nil
	}
	var tasks []*Task
	if err := DB.Where("user_id = ? and task_id = ?", userId, taskId).
		Order("id asc").Limit(2).Find(&tasks).Error; err != nil {
		return nil, false, err
	}
	if len(tasks) > 1 {
		return nil, false, ErrTaskIdentityAmbiguous
	}
	if len(tasks) == 0 {
		return nil, false, nil
	}
	return tasks[0], true, nil
}

func GetByTaskIds(userId int, taskIds []any) ([]*Task, error) {
	if len(taskIds) == 0 {
		return nil, nil
	}
	var task []*Task
	var err error
	err = DB.Where("user_id = ? and task_id in (?)", userId, taskIds).
		Order("id asc").Find(&task).Error
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(task))
	for _, item := range task {
		if item == nil || item.TaskID == "" {
			return nil, ErrTaskIdentityAmbiguous
		}
		if _, duplicate := seen[item.TaskID]; duplicate {
			return nil, ErrTaskIdentityAmbiguous
		}
		seen[item.TaskID] = struct{}{}
	}
	return task, nil
}

func (Task *Task) Insert() error {
	var err error
	err = DB.Create(Task).Error
	return err
}

type taskSnapshot struct {
	Status            TaskStatus
	Progress          string
	StartTime         int64
	FinishTime        int64
	FailReason        string
	UpstreamResultURL string
	Data              json.RawMessage
}

func (s taskSnapshot) Equal(other taskSnapshot) bool {
	return s.Status == other.Status &&
		s.Progress == other.Progress &&
		s.StartTime == other.StartTime &&
		s.FinishTime == other.FinishTime &&
		s.FailReason == other.FailReason &&
		s.UpstreamResultURL == other.UpstreamResultURL &&
		bytes.Equal(s.Data, other.Data)
}

func (t *Task) Snapshot() taskSnapshot {
	return taskSnapshot{
		Status:            t.Status,
		Progress:          t.Progress,
		StartTime:         t.StartTime,
		FinishTime:        t.FinishTime,
		FailReason:        t.FailReason,
		UpstreamResultURL: t.PrivateData.UpstreamResultURL,
		Data:              t.Data,
	}
}

func (Task *Task) Update() error {
	var err error
	err = DB.Save(Task).Error
	return err
}

func (t *Task) UpdateQuota() error {
	return DB.Model(t).Update("quota", t.Quota).Error
}

// UpdateWithStatus performs a conditional UPDATE guarded by fromStatus (CAS).
// Returns (true, nil) if this caller won the update, (false, nil) if
// another process already moved the task out of fromStatus.
//
// Uses Model().Select("*").Updates() instead of Save() because GORM's Save
// falls back to INSERT ON CONFLICT when the WHERE-guarded UPDATE matches
// zero rows, which silently bypasses the CAS guard.
func (t *Task) UpdateWithStatus(fromStatus TaskStatus) (bool, error) {
	result := DB.Model(t).Where("status = ?", fromStatus).Select("*").Updates(t)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// TaskBulkUpdate performs an unconditional bulk UPDATE by upstream task_id strings.
// Same caveats as TaskBulkUpdateByID — no CAS guard.
func TaskBulkUpdate(taskIds []string, params map[string]any) error {
	if len(taskIds) == 0 {
		return nil
	}
	return DB.Model(&Task{}).
		Where("task_id in (?)", taskIds).
		Updates(params).Error
}

// TaskBulkUpdateByID performs an unconditional bulk UPDATE by primary key IDs.
// WARNING: This function has NO CAS (Compare-And-Swap) guard — it will overwrite
// any concurrent status changes. DO NOT use in billing/quota lifecycle flows
// (e.g., timeout, success, failure transitions that trigger refunds or settlements).
// For status transitions that involve billing, use Task.UpdateWithStatus() instead.
func TaskBulkUpdateByID(ids []int64, params map[string]any) error {
	if len(ids) == 0 {
		return nil
	}
	return DB.Model(&Task{}).
		Where("id in (?)", ids).
		Updates(params).Error
}

type TaskQuotaUsage struct {
	Mode  string  `json:"mode"`
	Count float64 `json:"count"`
}

// TaskCountAllTasks returns total tasks that match the given query params (admin usage)
func TaskCountAllTasks(queryParams SyncTaskQueryParams) int64 {
	var total int64
	query := DB.Model(&Task{})
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.UserID != "" {
		query = query.Where("user_id = ?", queryParams.UserID)
	}
	if len(queryParams.UserIDs) != 0 {
		query = query.Where("user_id in (?)", queryParams.UserIDs)
	}
	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.StartTimestamp != 0 {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	_ = query.Count(&total).Error
	return total
}

// TaskCountAllUserTask returns total tasks for given user
func TaskCountAllUserTask(userId int, queryParams SyncTaskQueryParams) int64 {
	var total int64
	query := DB.Model(&Task{}).Where("user_id = ?", userId)
	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.StartTimestamp != 0 {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	_ = query.Count(&total).Error
	return total
}
func (t *Task) ToOpenAIVideo() *dto.OpenAIVideo {
	openAIVideo := dto.NewOpenAIVideo()
	openAIVideo.ID = t.TaskID
	openAIVideo.Status = t.Status.ToVideoStatus()
	openAIVideo.Model = t.Properties.OriginModelName
	openAIVideo.SetProgressStr(t.Progress)
	openAIVideo.CreatedAt = t.CreatedAt
	openAIVideo.CompletedAt = t.UpdatedAt
	openAIVideo.SetMetadata("url", t.GetResultURL())
	return openAIVideo
}
