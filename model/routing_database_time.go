package model

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

var ErrRoutingDatabaseTime = errors.New("channel routing database time is unavailable")

func RoutingDatabaseNowMsContext(ctx context.Context) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return routingDatabaseNowMs(DB.WithContext(ctx))
}

func routingDatabaseNowMs(tx *gorm.DB) (int64, error) {
	if tx == nil || tx.Dialector == nil {
		return 0, ErrRoutingDatabaseTime
	}
	query := ""
	switch tx.Dialector.Name() {
	case "sqlite":
		query = "SELECT CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)"
	case "mysql":
		query = "SELECT CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3)) * 1000 AS SIGNED)"
	case "postgres":
		query = "SELECT CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000 AS BIGINT)"
	default:
		return 0, fmt.Errorf("%w: unsupported database dialect %q", ErrRoutingDatabaseTime, tx.Dialector.Name())
	}
	var nowMs int64
	if err := tx.Raw(query).Scan(&nowMs).Error; err != nil {
		return 0, fmt.Errorf("%w: %v", ErrRoutingDatabaseTime, err)
	}
	if nowMs <= 0 {
		return 0, ErrRoutingDatabaseTime
	}
	return nowMs, nil
}
