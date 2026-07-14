package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

var (
	ErrMidjourneyIdentityInvalid   = errors.New("invalid Midjourney task identity")
	ErrMidjourneyIdentityAmbiguous = errors.New("ambiguous Midjourney task identity")
)

const midjourneyPublicIDBatchLimit = 1_000

type Midjourney struct {
	Id                        int    `json:"id"`
	Code                      int    `json:"code"`
	UserId                    int    `json:"user_id" gorm:"index"`
	Action                    string `json:"action" gorm:"type:varchar(40);index"`
	MjId                      string `json:"mj_id" gorm:"index"`
	UpstreamTaskID            string `json:"-" gorm:"type:varchar(191);index"`
	RoutingCredentialID       int    `json:"-" gorm:"index"`
	ChannelGeneration         string `json:"-" gorm:"type:varchar(32);index"`
	Prompt                    string `json:"prompt"`
	PromptEn                  string `json:"prompt_en"`
	Description               string `json:"description"`
	State                     string `json:"state"`
	SubmitTime                int64  `json:"submit_time" gorm:"index"`
	StartTime                 int64  `json:"start_time" gorm:"index"`
	FinishTime                int64  `json:"finish_time" gorm:"index"`
	ImageUrl                  string `json:"image_url"`
	VideoUrl                  string `json:"video_url"`
	VideoUrls                 string `json:"video_urls"`
	Status                    string `json:"status" gorm:"type:varchar(20);index"`
	Progress                  string `json:"progress" gorm:"type:varchar(30);index"`
	FailReason                string `json:"fail_reason"`
	ChannelId                 int    `json:"channel_id" gorm:"index"`
	Quota                     int    `json:"quota"`
	DurableQuota              int    `json:"-" gorm:"column:durable_quota"`
	Group                     string `json:"-" gorm:"type:varchar(64)"`
	BillingSource             string `json:"-" gorm:"type:varchar(32)"`
	BillingProtocolVersion    int    `json:"-"`
	AsyncBillingReservationID int64  `json:"-" gorm:"index"`
	SubscriptionId            int    `json:"-"`
	TokenId                   int    `json:"-"`
	NodeName                  string `json:"-" gorm:"type:varchar(128)"`
	Buttons                   string `json:"buttons"`
	Properties                string `json:"properties"`
	BillingAuditPayload       string `json:"-" gorm:"type:text"`
}

func (midjourney *Midjourney) EffectiveBillingQuota() int {
	if midjourney != nil && midjourney.BillingProtocolVersion == TaskBillingProtocolVersion {
		return midjourney.DurableQuota
	}
	if midjourney == nil {
		return 0
	}
	return midjourney.Quota
}

func (midjourney *Midjourney) IsolateV2BillingFromLegacyPollers(quota int) error {
	if midjourney == nil || midjourney.BillingProtocolVersion != TaskBillingProtocolVersion ||
		quota < 0 || quota > common.MaxQuota {
		return errors.New("invalid v2 Midjourney billing isolation")
	}
	midjourney.DurableQuota = quota
	midjourney.Quota = 0
	return nil
}

// GetUpstreamTaskID preserves historical rows that stored the provider and
// public identities in the same column.
func (midjourney *Midjourney) GetUpstreamTaskID() string {
	if midjourney == nil {
		return ""
	}
	if midjourney.UpstreamTaskID != "" {
		return midjourney.UpstreamTaskID
	}
	return midjourney.MjId
}

// TaskQueryParams 用于包含所有搜索条件的结构体，可以根据需求添加更多字段
type TaskQueryParams struct {
	ChannelID      string
	MjID           string
	StartTimestamp string
	EndTimestamp   string
}

func GetAllUserTask(userId int, startIdx int, num int, queryParams TaskQueryParams) []*Midjourney {
	var tasks []*Midjourney
	var err error

	// 初始化查询构建器
	query := DB.Where("user_id = ?", userId)

	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		// 假设您已将前端传来的时间戳转换为数据库所需的时间格式，并处理了时间戳的验证和解析
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func GetAllTasks(startIdx int, num int, queryParams TaskQueryParams) []*Midjourney {
	var tasks []*Midjourney
	var err error

	// 初始化查询构建器
	query := DB

	// 添加过滤条件
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func GetAllUnFinishTasks() []*Midjourney {
	tasks, err := FindUnfinishedMidjourneyTasks(5_000)
	if err != nil {
		return nil
	}
	return tasks
}

func FindUnfinishedMidjourneyTasks(limit int) ([]*Midjourney, error) {
	if limit < 1 || limit > 5_000 {
		limit = 5_000
	}
	var tasks []*Midjourney
	err := DB.Where("progress != ?", "100%").Order("id asc").Limit(limit).Find(&tasks).Error
	return tasks, err
}

// HasUnfinishedMidjourneyTasks reports whether at least one Midjourney task is
// still in progress. It is a cheap existence check (LIMIT 1) used to decide
// whether the midjourney_poll system task needs to run; when no task is pending
// the scheduler skips creating a row entirely.
func HasUnfinishedMidjourneyTasks() bool {
	var id int
	err := DB.Model(&Midjourney{}).
		Where("progress != ?", "100%").
		Limit(1).
		Pluck("id", &id).Error
	return err == nil && id != 0
}

func GetByOnlyMJId(mjId string) *Midjourney {
	mj, err := FindMidjourneyByPublicIDAnyUser(mjId)
	if err != nil {
		return nil
	}
	return mj
}

func GetByMJId(userId int, mjId string) *Midjourney {
	mj, err := FindMidjourneyByPublicID(userId, mjId)
	if err != nil {
		return nil
	}
	return mj
}

func FindMidjourneyByPublicID(userId int, taskID string) (*Midjourney, error) {
	if userId <= 0 || strings.TrimSpace(taskID) == "" {
		return nil, ErrMidjourneyIdentityInvalid
	}
	return findUniqueMidjourney(DB.Where("user_id = ? AND mj_id = ?", userId, taskID))
}

func FindMidjourneyByPublicIDAnyUser(taskID string) (*Midjourney, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, ErrMidjourneyIdentityInvalid
	}
	return findUniqueMidjourney(DB.Where("mj_id = ?", taskID))
}

func FindMidjourneyByUpstreamID(upstreamTaskID string) (*Midjourney, error) {
	if strings.TrimSpace(upstreamTaskID) == "" {
		return nil, ErrMidjourneyIdentityInvalid
	}
	return findUniqueMidjourney(DB.Where(
		"upstream_task_id = ? OR (upstream_task_id = ? AND mj_id = ?)",
		upstreamTaskID, "", upstreamTaskID,
	))
}

func findUniqueMidjourney(query *gorm.DB) (*Midjourney, error) {
	if query == nil {
		return nil, ErrMidjourneyIdentityInvalid
	}
	var tasks []Midjourney
	if err := query.Order("id asc").Limit(2).Find(&tasks).Error; err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if len(tasks) > 1 {
		return nil, ErrMidjourneyIdentityAmbiguous
	}
	return &tasks[0], nil
}

func GetByMJIds(userId int, mjIds []string) []*Midjourney {
	var mj []*Midjourney
	var err error
	err = DB.Where("user_id = ? and mj_id in (?)", userId, mjIds).Find(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func FindMidjourneysByPublicIDs(userId int, taskIDs []string) ([]*Midjourney, error) {
	if userId <= 0 || len(taskIDs) > midjourneyPublicIDBatchLimit {
		return nil, ErrMidjourneyIdentityInvalid
	}
	if len(taskIDs) == 0 {
		return []*Midjourney{}, nil
	}
	seen := make(map[string]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		taskID = strings.TrimSpace(taskID)
		if taskID == "" {
			return nil, ErrMidjourneyIdentityInvalid
		}
		if _, exists := seen[taskID]; exists {
			return nil, ErrMidjourneyIdentityAmbiguous
		}
		seen[taskID] = struct{}{}
	}
	var tasks []*Midjourney
	if err := DB.Where("user_id = ? AND mj_id IN ?", userId, taskIDs).Order("id asc").Find(&tasks).Error; err != nil {
		return nil, err
	}
	matched := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if task == nil {
			return nil, ErrMidjourneyIdentityInvalid
		}
		if _, exists := matched[task.MjId]; exists {
			return nil, ErrMidjourneyIdentityAmbiguous
		}
		matched[task.MjId] = struct{}{}
	}
	return tasks, nil
}

func GetMjByuId(id int) *Midjourney {
	var mj *Midjourney
	var err error
	err = DB.Where("id = ?", id).First(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func UpdateProgress(id int, progress string) error {
	return DB.Model(&Midjourney{}).Where("id = ?", id).Update("progress", progress).Error
}

func (midjourney *Midjourney) Insert() error {
	var err error
	err = DB.Create(midjourney).Error
	return err
}

func (midjourney *Midjourney) Update() error {
	var err error
	err = DB.Save(midjourney).Error
	return err
}

// UpdateWithStatus performs a conditional UPDATE guarded by fromStatus (CAS).
// Returns (true, nil) if this caller won the update, (false, nil) if
// another process already moved the task out of fromStatus.
// UpdateWithStatus performs a conditional UPDATE guarded by fromStatus (CAS).
// Uses Model().Select("*").Updates() to avoid GORM Save()'s INSERT fallback.
func (midjourney *Midjourney) UpdateWithStatus(fromStatus string) (bool, error) {
	result := DB.Model(midjourney).Where("status = ?", fromStatus).Select("*").Updates(midjourney)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func MjBulkUpdateByTaskIds(taskIDs []int, params map[string]any) error {
	return DB.Model(&Midjourney{}).
		Where("id in (?)", taskIDs).
		Updates(params).Error
}

func UpdateMidjourneyByID(id int, params map[string]any) error {
	if id <= 0 || len(params) == 0 {
		return nil
	}
	return DB.Model(&Midjourney{}).Where("id = ?", id).Updates(params).Error
}

// CountAllTasks returns total midjourney tasks for admin query
func CountAllTasks(queryParams TaskQueryParams) int64 {
	var total int64
	query := DB.Model(&Midjourney{})
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	_ = query.Count(&total).Error
	return total
}

// CountAllUserTask returns total midjourney tasks for user
func CountAllUserTask(userId int, queryParams TaskQueryParams) int64 {
	var total int64
	query := DB.Model(&Midjourney{}).Where("user_id = ?", userId)
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	_ = query.Count(&total).Error
	return total
}
