package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	ReferralCookieName = "referral_token"

	ReferralCaptureSourceLink   = "link"
	ReferralCaptureSourceManual = "manual"
	ReferralCaptureSourceLegacy = "legacy"

	DefaultReferralCaptureMaxAgeSeconds = 30 * 24 * 60 * 60

	EmailDomainWhitelistMainstreamMessage = "Please register with a mainstream email provider: gmail.com, 163.com, 126.com, qq.com, icloud.com, 139.com, or outlook.com."
)

var ErrReferralCaptureConsumed = errors.New("referral capture is no longer available")

type ReferralCapture struct {
	Id                      int                    `json:"id"`
	TokenHash               string                 `json:"token_hash" gorm:"type:varchar(64);uniqueIndex"`
	InviterId               int                    `json:"inviter_id" gorm:"type:int;index"`
	AffCode                 string                 `json:"aff_code" gorm:"type:varchar(32);column:aff_code;index"`
	InviteLinkBatchId       int                    `json:"invite_link_batch_id" gorm:"type:int;column:invite_link_batch_id;index"`
	InviteBatchCode         string                 `json:"invite_batch_code" gorm:"type:varchar(64);column:invite_batch_code;index"`
	Source                  string                 `json:"source" gorm:"type:varchar(16);index"`
	FirstTopupRewardPercent int                    `json:"first_topup_reward_percent" gorm:"type:int;column:first_topup_reward_percent"`
	ContinuousRewardPercent int                    `json:"continuous_reward_percent" gorm:"type:int;column:continuous_reward_percent"`
	ActivityRules           InviteRewardActivities `json:"activity_rules" gorm:"type:text;column:activity_rules"`
	ExpiresAt               int64                  `json:"expires_at" gorm:"column:expires_at;index"`
	ConsumedByUserId        int                    `json:"consumed_by_user_id" gorm:"type:int;column:consumed_by_user_id;index"`
	ConsumedAt              int64                  `json:"consumed_at" gorm:"column:consumed_at;index"`
	ClearedAt               int64                  `json:"cleared_at" gorm:"column:cleared_at;index"`
	SupersededAt            int64                  `json:"superseded_at" gorm:"column:superseded_at;index"`
	CreatedAt               int64                  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt               int64                  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

type ReferralCaptureCurrent struct {
	AffCode                 string `json:"aff_code"`
	Source                  string `json:"source"`
	Locked                  bool   `json:"locked"`
	ExpiresAt               int64  `json:"expires_at"`
	InviteLinkBatchId       int    `json:"invite_link_batch_id"`
	InviteBatchCode         string `json:"invite_batch_code"`
	FirstTopupRewardPercent int    `json:"first_topup_reward_percent"`
	ContinuousRewardPercent int    `json:"continuous_reward_percent"`
}

func ReferralTokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return fmt.Sprintf("%x", sum[:])
}

func CreateReferralCapture(batchCode string, affCode string, source string, now int64) (*ReferralCapture, string, error) {
	batch, err := GetInviteLinkBatchByCodeAt(batchCode, now)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", nil
		}
		return nil, "", err
	}
	return createReferralCaptureForBatch(batch, affCode, source, now)
}

func CreateManualReferralCapture(affCode string, now int64) (*ReferralCapture, string, error) {
	batch, err := GetActiveInviteLinkBatchAt(now)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", nil
		}
		return nil, "", err
	}
	return createReferralCaptureForBatch(batch, affCode, ReferralCaptureSourceManual, now)
}

func createReferralCaptureForBatch(batch *InviteLinkBatch, affCode string, source string, now int64) (*ReferralCapture, string, error) {
	if batch == nil {
		return nil, "", nil
	}
	affCode = strings.TrimSpace(affCode)
	if affCode == "" {
		return nil, "", nil
	}
	inviterId, err := GetUserIdByAffCode(affCode)
	if err != nil || inviterId == 0 {
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", err
		}
		return nil, "", nil
	}

	expiresAt := now + DefaultReferralCaptureMaxAgeSeconds
	if batch.EndTime < expiresAt {
		expiresAt = batch.EndTime
	}
	if expiresAt <= now {
		return nil, "", nil
	}

	token, err := common.GenerateRandomCharsKey(48)
	if err != nil {
		return nil, "", err
	}
	activityRules := batch.EffectiveActivityRules()
	firstTopupRewardPercent, continuousRewardPercent := CalculateInviteRewardPercents(activityRules)
	capture := &ReferralCapture{
		TokenHash:               ReferralTokenHash(token),
		InviterId:               inviterId,
		AffCode:                 affCode,
		InviteLinkBatchId:       batch.Id,
		InviteBatchCode:         batch.Code,
		Source:                  normalizeReferralCaptureSource(source),
		FirstTopupRewardPercent: firstTopupRewardPercent,
		ContinuousRewardPercent: continuousRewardPercent,
		ActivityRules:           activityRules,
		ExpiresAt:               expiresAt,
	}
	if err := DB.Create(capture).Error; err != nil {
		return nil, "", err
	}
	return capture, token, nil
}

func normalizeReferralCaptureSource(source string) string {
	switch strings.TrimSpace(source) {
	case ReferralCaptureSourceManual:
		return ReferralCaptureSourceManual
	case ReferralCaptureSourceLegacy:
		return ReferralCaptureSourceLegacy
	default:
		return ReferralCaptureSourceLink
	}
}

func GetValidReferralCaptureByToken(token string, now int64) (*ReferralCapture, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	return GetValidReferralCaptureByTokenHash(ReferralTokenHash(token), now)
}

func GetValidReferralCaptureByTokenHash(tokenHash string, now int64) (*ReferralCapture, error) {
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return nil, nil
	}
	var capture ReferralCapture
	err := DB.Where(
		"token_hash = ? AND expires_at >= ? AND consumed_at = 0 AND cleared_at = 0 AND superseded_at = 0",
		tokenHash,
		now,
	).First(&capture).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &capture, nil
}

func SupersedeReferralCaptureByToken(token string, now int64) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	return DB.Model(&ReferralCapture{}).
		Where("token_hash = ? AND consumed_at = 0 AND cleared_at = 0 AND superseded_at = 0", ReferralTokenHash(token)).
		Update("superseded_at", now).Error
}

func ClearReferralCaptureByTokenHash(tokenHash string, now int64) error {
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return nil
	}
	return DB.Model(&ReferralCapture{}).
		Where("token_hash = ? AND consumed_at = 0 AND cleared_at = 0", tokenHash).
		Update("cleared_at", now).Error
}

func ConsumeReferralCaptureTx(tx *gorm.DB, tokenHash string, userId int, now int64) (bool, error) {
	if tx == nil {
		tx = DB
	}
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" || userId == 0 {
		return false, nil
	}
	result := tx.Model(&ReferralCapture{}).
		Where("token_hash = ? AND expires_at >= ? AND consumed_at = 0 AND cleared_at = 0 AND superseded_at = 0", tokenHash, now).
		Updates(map[string]interface{}{
			"consumed_by_user_id": userId,
			"consumed_at":         now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (capture *ReferralCapture) InviteLinkBinding() *InviteLinkBinding {
	if capture == nil {
		return nil
	}
	return &InviteLinkBinding{
		InviterId:               capture.InviterId,
		InviteLinkBatchId:       capture.InviteLinkBatchId,
		FirstTopupRewardPercent: capture.FirstTopupRewardPercent,
		ContinuousRewardPercent: capture.ContinuousRewardPercent,
		ActivityRules:           capture.ActivityRules,
		BoundAt:                 capture.CreatedAt,
	}
}

func (capture *ReferralCapture) Current() ReferralCaptureCurrent {
	if capture == nil {
		return ReferralCaptureCurrent{}
	}
	return ReferralCaptureCurrent{
		AffCode:                 capture.AffCode,
		Source:                  capture.Source,
		Locked:                  capture.Source == ReferralCaptureSourceLink || capture.Source == ReferralCaptureSourceLegacy,
		ExpiresAt:               capture.ExpiresAt,
		InviteLinkBatchId:       capture.InviteLinkBatchId,
		InviteBatchCode:         capture.InviteBatchCode,
		FirstTopupRewardPercent: capture.FirstTopupRewardPercent,
		ContinuousRewardPercent: capture.ContinuousRewardPercent,
	}
}
