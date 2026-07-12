package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrRoutingControlLeaseInvalid = errors.New("invalid routing control lease")
	ErrRoutingControlLeaseLost    = errors.New("routing control lease lost")
)

type RoutingControlLease struct {
	LeaseName       string `json:"lease_name" gorm:"type:varchar(64);primaryKey"`
	HolderID        string `json:"holder_id" gorm:"type:varchar(128);index;not null"`
	LeaseToken      string `json:"-" gorm:"type:char(32);index;not null"`
	LeaseUntilMs    int64  `json:"lease_until_ms" gorm:"bigint;index;not null"`
	LastCompletedMs int64  `json:"last_completed_ms" gorm:"bigint;index;not null"`
	FencingToken    int64  `json:"fencing_token" gorm:"bigint;not null"`
	UpdatedTimeMs   int64  `json:"updated_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingControlLease) TableName() string {
	return "routing_control_leases"
}

func TryAcquireRoutingControlLeaseContext(
	ctx context.Context,
	leaseName string,
	holderID string,
	nowMs int64,
	ttlMs int64,
	minimumIntervalMs int64,
	force bool,
) (RoutingControlLease, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingControlLeaseText(leaseName, 64) || !validRoutingControlLeaseText(holderID, 128) ||
		nowMs <= 0 || ttlMs <= 0 || nowMs > math.MaxInt64-ttlMs || minimumIntervalMs < 0 {
		return RoutingControlLease{}, false, ErrRoutingControlLeaseInvalid
	}
	var acquired RoutingControlLease
	acquiredOK := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		placeholder := RoutingControlLease{LeaseName: leaseName}
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "lease_name"}},
			DoNothing: true,
		}).Create(&placeholder).Error; err != nil {
			return err
		}
		var current RoutingControlLease
		if err := lockForUpdate(tx.WithContext(ctx)).Where("lease_name = ?", leaseName).First(&current).Error; err != nil {
			return err
		}
		if current.LeaseUntilMs > nowMs {
			acquired = current
			return nil
		}
		if !force && minimumIntervalMs > 0 && current.LastCompletedMs > 0 {
			minimumCompletedMs := int64(0)
			if nowMs > minimumIntervalMs {
				minimumCompletedMs = nowMs - minimumIntervalMs
			}
			if current.LastCompletedMs > minimumCompletedMs {
				acquired = current
				return nil
			}
		}
		if current.FencingToken == math.MaxInt64 {
			return ErrRoutingControlLeaseInvalid
		}
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return err
		}
		leaseToken := hex.EncodeToString(token[:])
		result := tx.WithContext(ctx).Model(&RoutingControlLease{}).
			Where("lease_name = ? AND lease_until_ms <= ?", leaseName, nowMs).
			Updates(map[string]any{
				"holder_id":       holderID,
				"lease_token":     leaseToken,
				"lease_until_ms":  nowMs + ttlMs,
				"fencing_token":   current.FencingToken + 1,
				"updated_time_ms": nowMs,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		if err := tx.WithContext(ctx).Where("lease_name = ?", leaseName).First(&acquired).Error; err != nil {
			return err
		}
		acquiredOK = true
		return nil
	})
	return acquired, acquiredOK, err
}

func CompleteRoutingControlLeaseContext(ctx context.Context, lease RoutingControlLease, completedAtMs int64) error {
	return finishRoutingControlLeaseContext(ctx, lease, completedAtMs, true)
}

func ReleaseRoutingControlLeaseContext(ctx context.Context, lease RoutingControlLease, releasedAtMs int64) error {
	return finishRoutingControlLeaseContext(ctx, lease, releasedAtMs, false)
}

func finishRoutingControlLeaseContext(
	ctx context.Context,
	lease RoutingControlLease,
	finishedAtMs int64,
	completed bool,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingControlLeaseText(lease.LeaseName, 64) || !validRoutingControlLeaseText(lease.HolderID, 128) ||
		len(lease.LeaseToken) != 32 || finishedAtMs <= 0 {
		return ErrRoutingControlLeaseInvalid
	}
	updates := map[string]any{
		"holder_id":       "",
		"lease_token":     "",
		"lease_until_ms":  0,
		"updated_time_ms": finishedAtMs,
	}
	if completed {
		updates["last_completed_ms"] = finishedAtMs
	}
	result := DB.WithContext(ctx).Model(&RoutingControlLease{}).
		Where("lease_name = ? AND holder_id = ? AND lease_token = ? AND fencing_token = ?",
			lease.LeaseName, lease.HolderID, lease.LeaseToken, lease.FencingToken,
		).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRoutingControlLeaseLost
	}
	return nil
}

func validRoutingControlLeaseText(value string, maxRunes int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes
}

func DeleteRoutingControlLeasesByPrefixBeforeContext(
	ctx context.Context,
	prefix string,
	cutoffUpdatedMs int64,
	nowMs int64,
	batchSize int,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingControlLeaseText(prefix, 48) || strings.ContainsAny(prefix, "%_") || cutoffUpdatedMs <= 0 || nowMs <= 0 {
		return 0, ErrRoutingControlLeaseInvalid
	}
	if batchSize < 1 {
		batchSize = 100
	}
	if batchSize > 1_000 {
		batchSize = 1_000
	}
	deleted := int64(0)
	for {
		var names []string
		if err := DB.WithContext(ctx).Model(&RoutingControlLease{}).
			Where("lease_name LIKE ? AND lease_until_ms <= ? AND updated_time_ms < ?", prefix+"%", nowMs, cutoffUpdatedMs).
			Order("lease_name ASC").Limit(batchSize).
			Pluck("lease_name", &names).Error; err != nil {
			return deleted, err
		}
		if len(names) == 0 {
			return deleted, nil
		}
		result := DB.WithContext(ctx).
			Where("lease_name IN ? AND lease_name LIKE ? AND lease_until_ms <= ? AND updated_time_ms < ?", names, prefix+"%", nowMs, cutoffUpdatedMs).
			Delete(&RoutingControlLease{})
		deleted += result.RowsAffected
		if result.Error != nil {
			return deleted, result.Error
		}
		if len(names) < batchSize {
			return deleted, nil
		}
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
	}
}
