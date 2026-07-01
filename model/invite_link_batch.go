package model

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	InviteDescriptionModePreset = "preset"
	InviteDescriptionModeCustom = "custom"
)

type InviteLinkBatch struct {
	Id                      int    `json:"id"`
	Name                    string `json:"name" gorm:"type:varchar(64);index"`
	Code                    string `json:"code" gorm:"type:varchar(64);uniqueIndex"`
	BaseLink                string `json:"base_link" gorm:"type:varchar(512)"`
	FirstTopupRewardPercent int    `json:"first_topup_reward_percent" gorm:"type:int"`
	ContinuousRewardPercent int    `json:"continuous_reward_percent" gorm:"type:int"`
	StartTime               int64  `json:"start_time" gorm:"index"`
	EndTime                 int64  `json:"end_time" gorm:"index"`
	DescriptionMode         string `json:"description_mode" gorm:"type:varchar(16)"`
	PresetDescription       string `json:"preset_description" gorm:"type:text"`
	CustomDescription       string `json:"custom_description" gorm:"type:text"`
	IsActive                bool   `json:"is_active" gorm:"index"`
	CreatedAt               int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt               int64  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

type InviteLinkBatchWithStats struct {
	InviteLinkBatch
	UsageCount int64 `json:"usage_count"`
	IsValid    bool  `json:"is_valid"`
}

func (batch InviteLinkBatch) IsValidAt(now int64) bool {
	return batch.StartTime <= now && now <= batch.EndTime
}

func (batch *InviteLinkBatch) Normalize() {
	batch.Name = strings.TrimSpace(batch.Name)
	batch.Code = strings.TrimSpace(batch.Code)
	if batch.Code == "" {
		batch.Code = GenerateInviteLinkBatchCode()
	}
	if batch.BaseLink == "" {
		batch.BaseLink = BuildInviteLinkBatchBaseLink("", batch.Code)
	}
	batch.FirstTopupRewardPercent = normalizeRewardPercent(batch.FirstTopupRewardPercent)
	batch.ContinuousRewardPercent = normalizeRewardPercent(batch.ContinuousRewardPercent)
	switch strings.TrimSpace(batch.DescriptionMode) {
	case InviteDescriptionModeCustom:
		batch.DescriptionMode = InviteDescriptionModeCustom
	default:
		batch.DescriptionMode = InviteDescriptionModePreset
	}
}

func GenerateInviteLinkBatchCode() string {
	return strings.ToLower(common.GetRandomString(10))
}

func BuildInviteLinkBatchBaseLink(origin string, code string) string {
	trimmedOrigin := strings.TrimRight(strings.TrimSpace(origin), "/")
	if trimmedOrigin == "" {
		trimmedOrigin = "/sign-up"
	}
	link, err := url.Parse(trimmedOrigin)
	if err != nil {
		return fmt.Sprintf("/sign-up?invite_batch=%s", url.QueryEscape(code))
	}
	if link.Path == "" || link.Path == "/" {
		link.Path = "/sign-up"
	}
	query := link.Query()
	query.Set("invite_batch", code)
	link.RawQuery = query.Encode()
	return link.String()
}

func BuildInviteLinkForUser(baseLink string, affCode string) string {
	link, err := url.Parse(strings.TrimSpace(baseLink))
	if err != nil {
		return baseLink
	}
	query := link.Query()
	query.Set("aff", affCode)
	link.RawQuery = query.Encode()
	return link.String()
}

func CreateInviteLinkBatch(batch *InviteLinkBatch) error {
	if batch == nil {
		return errors.New("invite link batch is nil")
	}
	batch.Normalize()
	return DB.Transaction(func(tx *gorm.DB) error {
		if batch.IsActive {
			if err := tx.Model(&InviteLinkBatch{}).Where("is_active = ?", true).Update("is_active", false).Error; err != nil {
				return err
			}
		}
		return tx.Create(batch).Error
	})
}

func UpdateInviteLinkBatch(batch *InviteLinkBatch) error {
	if batch == nil || batch.Id == 0 {
		return errors.New("invite link batch id is required")
	}
	batch.Normalize()
	return DB.Transaction(func(tx *gorm.DB) error {
		var existing InviteLinkBatch
		if err := tx.First(&existing, batch.Id).Error; err != nil {
			return err
		}
		if batch.IsActive {
			if err := tx.Model(&InviteLinkBatch{}).Where("id <> ? AND is_active = ?", batch.Id, true).Update("is_active", false).Error; err != nil {
				return err
			}
		}
		return tx.Model(&InviteLinkBatch{}).Where("id = ?", batch.Id).Updates(map[string]interface{}{
			"name":                       batch.Name,
			"code":                       batch.Code,
			"base_link":                  batch.BaseLink,
			"first_topup_reward_percent": batch.FirstTopupRewardPercent,
			"continuous_reward_percent":  batch.ContinuousRewardPercent,
			"start_time":                 batch.StartTime,
			"end_time":                   batch.EndTime,
			"description_mode":           batch.DescriptionMode,
			"preset_description":         batch.PresetDescription,
			"custom_description":         batch.CustomDescription,
			"is_active":                  batch.IsActive,
		}).Error
	})
}

func SetActiveInviteLinkBatch(id int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var batch InviteLinkBatch
		if err := tx.First(&batch, id).Error; err != nil {
			return err
		}
		if err := tx.Model(&InviteLinkBatch{}).Where("is_active = ?", true).Update("is_active", false).Error; err != nil {
			return err
		}
		return tx.Model(&InviteLinkBatch{}).Where("id = ?", id).Update("is_active", true).Error
	})
}

func ListInviteLinkBatchesWithStats(now int64) ([]InviteLinkBatchWithStats, error) {
	var batches []InviteLinkBatch
	if err := DB.Order("is_active desc, id desc").Find(&batches).Error; err != nil {
		return nil, err
	}

	result := make([]InviteLinkBatchWithStats, 0, len(batches))
	for _, batch := range batches {
		var usageCount int64
		if err := DB.Model(&User{}).Where("invite_link_batch_id = ?", batch.Id).Count(&usageCount).Error; err != nil {
			return nil, err
		}
		result = append(result, InviteLinkBatchWithStats{
			InviteLinkBatch: batch,
			UsageCount:      usageCount,
			IsValid:         batch.IsValidAt(now),
		})
	}
	return result, nil
}

func GetActiveInviteLinkBatchAt(now int64) (*InviteLinkBatch, error) {
	var batch InviteLinkBatch
	err := DB.Where("is_active = ?", true).Order("id desc").First(&batch).Error
	if err != nil {
		return nil, err
	}
	if !batch.IsValidAt(now) {
		return nil, gorm.ErrRecordNotFound
	}
	return &batch, nil
}

func GetInviteLinkBatchByCodeAt(code string, now int64) (*InviteLinkBatch, error) {
	var batch InviteLinkBatch
	err := DB.Where("code = ? AND is_active = ?", strings.TrimSpace(code), true).First(&batch).Error
	if err != nil {
		return nil, err
	}
	if !batch.IsValidAt(now) {
		return nil, gorm.ErrRecordNotFound
	}
	return &batch, nil
}

type InviteLinkBinding struct {
	InviterId               int
	InviteLinkBatchId       int
	FirstTopupRewardPercent int
	ContinuousRewardPercent int
	BoundAt                 int64
}

func ResolveInviteLinkBinding(batchCode string, affCode string, now int64) (*InviteLinkBinding, error) {
	batchCode = strings.TrimSpace(batchCode)
	affCode = strings.TrimSpace(affCode)
	if batchCode == "" || affCode == "" {
		return nil, nil
	}

	batch, err := GetInviteLinkBatchByCodeAt(batchCode, now)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	inviterId, err := GetUserIdByAffCode(affCode)
	if err != nil || inviterId == 0 {
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, nil
	}

	return &InviteLinkBinding{
		InviterId:               inviterId,
		InviteLinkBatchId:       batch.Id,
		FirstTopupRewardPercent: normalizeRewardPercent(batch.FirstTopupRewardPercent),
		ContinuousRewardPercent: normalizeRewardPercent(batch.ContinuousRewardPercent),
		BoundAt:                 now,
	}, nil
}

func (user *User) ApplyInviteLinkBinding(binding *InviteLinkBinding) {
	if user == nil || binding == nil {
		return
	}
	user.InviterId = binding.InviterId
	user.InviteLinkBatchId = binding.InviteLinkBatchId
	user.InviteFirstTopupRewardPercent = binding.FirstTopupRewardPercent
	user.InviteContinuousRewardPercent = binding.ContinuousRewardPercent
	user.InviteBoundAt = binding.BoundAt
}
