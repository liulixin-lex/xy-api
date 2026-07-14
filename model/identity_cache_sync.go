package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const identityCacheSyncMutationTimeout = 3 * time.Second

// IdentityCacheSync coalesces cache invalidations by logical subject. The row
// is written in the same transaction as the authoritative mutation and is
// removed only after the matching version has been synchronized to Redis.
type IdentityCacheSync struct {
	SubjectKey    string         `json:"subject_key" gorm:"type:varchar(191);primaryKey"`
	EpochKey      string         `json:"epoch_key" gorm:"type:varchar(191);not null"`
	CacheKey      string         `json:"cache_key" gorm:"type:varchar(191);not null"`
	Version       int64          `json:"version" gorm:"not null"`
	Attempts      int            `json:"attempts" gorm:"not null"`
	NextRetryMs   int64          `json:"next_retry_ms" gorm:"index:idx_identity_cache_sync_pending,priority:1;index:idx_identity_cache_sync_live_pending,priority:2"`
	LastError     string         `json:"last_error,omitempty" gorm:"type:varchar(1024)"`
	CreatedTimeMs int64          `json:"created_time_ms" gorm:"bigint;not null"`
	UpdatedTimeMs int64          `json:"updated_time_ms" gorm:"bigint;not null;index:idx_identity_cache_sync_pending,priority:2;index:idx_identity_cache_sync_live_pending,priority:3"`
	DeletedAt     gorm.DeletedAt `json:"-" gorm:"index:idx_identity_cache_sync_live_pending,priority:1"`
}

func (IdentityCacheSync) TableName() string {
	return "identity_cache_syncs"
}

func queueIdentityCacheSyncTx(
	tx *gorm.DB,
	subjectKey string,
	epochKey string,
	cacheKey string,
	now time.Time,
) (int64, error) {
	if tx == nil || strings.TrimSpace(subjectKey) == "" || strings.TrimSpace(epochKey) == "" ||
		strings.TrimSpace(cacheKey) == "" {
		return 0, errors.New("identity cache sync target is invalid")
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	record := IdentityCacheSync{
		SubjectKey: subjectKey, EpochKey: epochKey, CacheKey: cacheKey, Version: 1,
		NextRetryMs: 0, CreatedTimeMs: nowMs, UpdatedTimeMs: nowMs,
	}
	err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "subject_key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"epoch_key": epochKey,
			"cache_key": cacheKey,
			"version": gorm.Expr("? + ?", clause.Column{
				Table: (IdentityCacheSync{}).TableName(), Name: "version",
			}, 1),
			"attempts":        0,
			"next_retry_ms":   0,
			"last_error":      "",
			"updated_time_ms": nowMs,
			"deleted_at":      nil,
		}),
	}).Create(&record).Error
	if err != nil {
		return 0, err
	}
	var persisted IdentityCacheSync
	if err := tx.Select("subject_key", "version").Where("subject_key = ?", subjectKey).First(&persisted).Error; err != nil {
		return 0, err
	}
	if persisted.Version <= 0 {
		return 0, errors.New("identity cache sync version is invalid")
	}
	return persisted.Version, nil
}

func queueUserCacheSyncTx(tx *gorm.DB, userID int, now time.Time) (int64, error) {
	if userID <= 0 {
		return 0, errors.New("user cache sync target is invalid")
	}
	return queueIdentityCacheSyncTx(
		tx, getUserCacheKey(userID), getUserCacheEpochKey(userID), getUserCacheKey(userID), now,
	)
}

func queueTokenCacheSyncTx(tx *gorm.DB, key string, now time.Time) (int64, error) {
	if strings.TrimSpace(key) == "" {
		return 0, errors.New("token cache sync target is invalid")
	}
	cacheKey := getTokenCacheKey(key)
	return queueIdentityCacheSyncTx(tx, cacheKey, getTokenCacheEpochKey(key), cacheKey, now)
}

func acknowledgeIdentityCacheSyncVersion(
	ctx context.Context,
	subjectKey string,
	version int64,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(subjectKey) == "" || version <= 0 {
		return errors.New("identity cache sync acknowledgement is invalid")
	}
	detachedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), identityCacheSyncMutationTimeout)
	defer cancel()
	return DB.WithContext(detachedCtx).
		Where("subject_key = ? AND version = ?", subjectKey, version).
		Delete(&IdentityCacheSync{}).Error
}

func retryIdentityCacheSyncVersion(
	ctx context.Context,
	subjectKey string,
	version int64,
	attempts int,
	now time.Time,
	cause error,
) error {
	if cause == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	shift := attempts
	if shift < 0 {
		shift = 0
	} else if shift > 6 {
		shift = 6
	}
	delay := time.Second * time.Duration(1<<shift)
	detachedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), identityCacheSyncMutationTimeout)
	defer cancel()
	updated := DB.WithContext(detachedCtx).Model(&IdentityCacheSync{}).
		Where("subject_key = ? AND version = ?", subjectKey, version).
		Updates(map[string]any{
			"attempts":        gorm.Expr("attempts + ?", 1),
			"next_retry_ms":   now.Add(delay).UnixMilli(),
			"last_error":      boundedAsyncBillingError(cause.Error()),
			"updated_time_ms": now.UnixMilli(),
		})
	return errors.Join(cause, updated.Error)
}

func completeIdentityCacheSyncAfterMutation(
	ctx context.Context,
	subjectKey string,
	version int64,
	now time.Time,
	cacheErr error,
) error {
	if cacheErr != nil {
		return retryIdentityCacheSyncVersion(ctx, subjectKey, version, 0, now, cacheErr)
	}
	return acknowledgeIdentityCacheSyncVersion(ctx, subjectKey, version)
}

// SyncIdentityCacheSubject invalidates one committed subject and acknowledges
// only the version that was observed before the Redis operation. A concurrent
// newer mutation therefore remains pending for another pass.
func SyncIdentityCacheSubject(ctx context.Context, subjectKey string, now time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	var record IdentityCacheSync
	err := DB.WithContext(ctx).Where("subject_key = ?", subjectKey).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !common.RedisEnabled {
		return retryIdentityCacheSyncVersion(
			ctx, record.SubjectKey, record.Version, record.Attempts, now,
			errors.New("redis is disabled"),
		)
	}
	if err := common.RedisBumpCacheEpochAndDeleteContext(ctx, record.EpochKey, record.CacheKey); err != nil {
		return retryIdentityCacheSyncVersion(ctx, record.SubjectKey, record.Version, record.Attempts, now, err)
	}
	return acknowledgeIdentityCacheSyncVersion(ctx, record.SubjectKey, record.Version)
}

func SyncUserCacheAfterCommit(ctx context.Context, userID int, now time.Time) error {
	if userID <= 0 {
		return errors.New("user cache sync target is invalid")
	}
	return SyncIdentityCacheSubject(ctx, getUserCacheKey(userID), now)
}

func syncUserCacheAfterCommitBestEffort(userID int, reason string) {
	if !common.RedisEnabled {
		return
	}
	if err := SyncUserCacheAfterCommit(context.Background(), userID, time.Now()); err != nil {
		common.SysError(fmt.Sprintf("%s committed but durable cache sync is pending: %s", reason, err.Error()))
	}
}

func syncTokenCacheAfterCommitBestEffort(key string, reason string) {
	if !common.RedisEnabled {
		return
	}
	if err := SyncIdentityCacheSubject(context.Background(), getTokenCacheKey(key), time.Now()); err != nil {
		common.SysError(fmt.Sprintf("%s committed but durable cache sync is pending: %s", reason, err.Error()))
	}
}

func syncTokenCachesAfterCommitBestEffort(keys []string, reason string) {
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		syncTokenCacheAfterCommitBestEffort(key, reason)
	}
}

func FindPendingIdentityCacheSyncSubjectsContext(ctx context.Context, now time.Time, limit int) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil {
		return []string{}, nil
	}
	db := DB.WithContext(ctx)
	if !db.Migrator().HasTable(&IdentityCacheSync{}) {
		return []string{}, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var subjects []string
	err := db.Model(&IdentityCacheSync{}).
		Where("next_retry_ms <= ?", now.UnixMilli()).
		Order("next_retry_ms asc, updated_time_ms asc, subject_key asc").
		Limit(limit).
		Pluck("subject_key", &subjects).Error
	return subjects, err
}

func FindPendingIdentityCacheSyncSubjects(now time.Time, limit int) ([]string, error) {
	return FindPendingIdentityCacheSyncSubjectsContext(context.Background(), now, limit)
}

func HasPendingIdentityCacheSyncContext(ctx context.Context, now time.Time) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil || !common.RedisEnabled {
		return false
	}
	db := DB.WithContext(ctx)
	if !db.Migrator().HasTable(&IdentityCacheSync{}) {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	var record IdentityCacheSync
	query := db.Select("subject_key").Where("next_retry_ms <= ?", now.UnixMilli()).Limit(1).Find(&record)
	return query.Error == nil && query.RowsAffected == 1
}

func HasPendingIdentityCacheSync(now time.Time) bool {
	return HasPendingIdentityCacheSyncContext(context.Background(), now)
}
