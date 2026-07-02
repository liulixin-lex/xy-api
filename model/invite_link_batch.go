package model

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	InviteDescriptionModePreset = "preset"
	InviteDescriptionModeCustom = "custom"

	InviteLinkBatchDescriptionMaxLength = 16 * 1024
	InviteRewardActivityDetailMaxLength = 255
)

type InviteRewardActivity struct {
	ActivityDetail string `json:"activity_detail"`
	Type           string `json:"type"`
	Percent        int    `json:"percent"`
}

type InviteRewardActivities []InviteRewardActivity

func (activities InviteRewardActivities) Value() (driver.Value, error) {
	if len(activities) == 0 {
		return "", nil
	}
	data, err := common.Marshal(activities)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func (activities *InviteRewardActivities) Scan(value interface{}) error {
	if activities == nil {
		return nil
	}
	if value == nil {
		*activities = nil
		return nil
	}

	var data []byte
	switch typed := value.(type) {
	case []byte:
		data = typed
	case string:
		data = []byte(typed)
	default:
		return fmt.Errorf("unsupported invite reward activities value %T", value)
	}
	if strings.TrimSpace(string(data)) == "" {
		*activities = nil
		return nil
	}

	var parsed []InviteRewardActivity
	if err := common.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*activities = NormalizeInviteRewardActivities(parsed)
	return nil
}

func NormalizeInviteRewardActivities(activities InviteRewardActivities) InviteRewardActivities {
	normalized := make(InviteRewardActivities, 0, len(activities))
	for _, activity := range activities {
		ruleType := strings.ToLower(strings.TrimSpace(activity.Type))
		switch ruleType {
		case InviteRewardRuleFirstTopUp, InviteRewardRuleContinuous:
		default:
			ruleType = strings.TrimSpace(activity.Type)
		}
		normalized = append(normalized, InviteRewardActivity{
			ActivityDetail: strings.TrimSpace(activity.ActivityDetail),
			Type:           ruleType,
			Percent:        activity.Percent,
		})
	}
	return normalized
}

func CalculateInviteRewardPercents(activities InviteRewardActivities) (firstTopupPercent int, continuousPercent int) {
	hasFirstTopupActivity := false
	for _, activity := range NormalizeInviteRewardActivities(activities) {
		switch activity.Type {
		case InviteRewardRuleFirstTopUp:
			hasFirstTopupActivity = true
			firstTopupPercent += activity.Percent
		case InviteRewardRuleContinuous:
			continuousPercent += activity.Percent
		}
	}
	if !hasFirstTopupActivity && continuousPercent > 0 {
		firstTopupPercent = continuousPercent
	}
	return firstTopupPercent, continuousPercent
}

type InviteLinkBatch struct {
	Id                      int                    `json:"id"`
	Name                    string                 `json:"name" gorm:"type:varchar(64);index"`
	Code                    string                 `json:"code" gorm:"type:varchar(64);uniqueIndex"`
	BaseLink                string                 `json:"base_link" gorm:"type:varchar(512)"`
	FirstTopupRewardPercent int                    `json:"first_topup_reward_percent" gorm:"type:int"`
	ContinuousRewardPercent int                    `json:"continuous_reward_percent" gorm:"type:int"`
	ActivityRules           InviteRewardActivities `json:"activity_rules" gorm:"type:text;column:activity_rules"`
	StartTime               int64                  `json:"start_time" gorm:"index"`
	EndTime                 int64                  `json:"end_time" gorm:"index"`
	DescriptionMode         string                 `json:"description_mode" gorm:"type:varchar(16)"`
	PresetDescription       string                 `json:"preset_description" gorm:"type:text"`
	CustomDescription       string                 `json:"custom_description" gorm:"type:text"`
	IsActive                bool                   `json:"is_active" gorm:"index"`
	CreatedAt               int64                  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt               int64                  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
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
	batch.ActivityRules = NormalizeInviteRewardActivities(batch.ActivityRules)
	if len(batch.ActivityRules) > 0 {
		batch.FirstTopupRewardPercent, batch.ContinuousRewardPercent = CalculateInviteRewardPercents(batch.ActivityRules)
	}
	switch strings.TrimSpace(batch.DescriptionMode) {
	case InviteDescriptionModeCustom:
		batch.DescriptionMode = InviteDescriptionModeCustom
	default:
		batch.DescriptionMode = InviteDescriptionModePreset
	}
}

func (batch InviteLinkBatch) Validate() error {
	if utf8.RuneCountInString(batch.PresetDescription) > InviteLinkBatchDescriptionMaxLength ||
		utf8.RuneCountInString(batch.CustomDescription) > InviteLinkBatchDescriptionMaxLength {
		return errors.New("activity description is too long")
	}
	for _, activity := range batch.ActivityRules {
		if strings.TrimSpace(activity.ActivityDetail) == "" {
			return errors.New("activity detail is required")
		}
		if utf8.RuneCountInString(activity.ActivityDetail) > InviteRewardActivityDetailMaxLength {
			return errors.New("activity detail is too long")
		}
		if activity.Type != InviteRewardRuleFirstTopUp && activity.Type != InviteRewardRuleContinuous {
			return errors.New("activity type is required")
		}
		if activity.Percent < 0 || activity.Percent > 100 {
			return errors.New("activity reward percent must be between 0 and 100")
		}
	}
	return nil
}

func (batch InviteLinkBatch) EffectiveActivityRules() InviteRewardActivities {
	activities := NormalizeInviteRewardActivities(batch.ActivityRules)
	if len(activities) > 0 {
		return activities
	}

	result := make(InviteRewardActivities, 0, 2)
	if batch.FirstTopupRewardPercent > 0 {
		result = append(result, InviteRewardActivity{
			ActivityDetail: "One-time Referral",
			Type:           InviteRewardRuleFirstTopUp,
			Percent:        normalizeRewardPercent(batch.FirstTopupRewardPercent),
		})
	}
	if batch.ContinuousRewardPercent > 0 {
		result = append(result, InviteRewardActivity{
			ActivityDetail: "Continuous Referral",
			Type:           InviteRewardRuleContinuous,
			Percent:        normalizeRewardPercent(batch.ContinuousRewardPercent),
		})
	}
	return result
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
	if err := batch.Validate(); err != nil {
		return err
	}
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
	if err := batch.Validate(); err != nil {
		return err
	}
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
			"activity_rules":             batch.ActivityRules,
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
	ActivityRules           InviteRewardActivities
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

	activityRules := batch.EffectiveActivityRules()
	firstTopupRewardPercent, continuousRewardPercent := CalculateInviteRewardPercents(activityRules)

	return &InviteLinkBinding{
		InviterId:               inviterId,
		InviteLinkBatchId:       batch.Id,
		FirstTopupRewardPercent: firstTopupRewardPercent,
		ContinuousRewardPercent: continuousRewardPercent,
		ActivityRules:           activityRules,
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
	user.InviteRewardRulesSnapshot = binding.ActivityRules
	user.InviteBoundAt = binding.BoundAt
}
