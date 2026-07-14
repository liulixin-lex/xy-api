package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrSystemInstanceActiveLimit         = errors.New("active system instance limit exceeded")
	ErrSystemInstanceIncarnationConflict = errors.New("active system instance incarnation conflict")
)

const (
	SystemInstanceStatusOnline = "online"
	SystemInstanceStatusStale  = "stale"

	SystemInstanceStaleAfterSeconds int64 = 90
)

type SystemInstance struct {
	NodeName   string `json:"node_name" gorm:"type:varchar(128);primaryKey"`
	Info       string `json:"info" gorm:"type:text"`
	StartedAt  int64  `json:"started_at" gorm:"bigint;index"`
	LastSeenAt int64  `json:"last_seen_at" gorm:"bigint;index"`
	CreatedAt  int64  `json:"created_at" gorm:"bigint;index"`
	UpdatedAt  int64  `json:"updated_at" gorm:"bigint;index"`
}

type SystemInstanceResponse struct {
	NodeName          string `json:"node_name"`
	Status            string `json:"status"`
	StaleAfterSeconds int64  `json:"stale_after_seconds"`
	StartedAt         int64  `json:"started_at"`
	LastSeenAt        int64  `json:"last_seen_at"`
	Info              any    `json:"info"`
}

func (instance *SystemInstance) BeforeCreate(_ *gorm.DB) error {
	now := common.GetTimestamp()
	if instance.CreatedAt == 0 {
		instance.CreatedAt = now
	}
	if instance.UpdatedAt == 0 {
		instance.UpdatedAt = now
	}
	return nil
}

func UpsertSystemInstance(nodeName string, incarnationID string, info any, startedAt int64, lastSeenAt int64) error {
	if DB == nil || nodeName == "" || incarnationID == "" || startedAt <= 0 {
		return errors.New("system instance identity is invalid")
	}
	infoText, err := marshalSystemInstanceInfo(info)
	if err != nil {
		return err
	}
	if systemInstanceIncarnationID(infoText) != incarnationID {
		return errors.New("system instance incarnation payload is invalid")
	}
	if lastSeenAt == 0 {
		lastSeenAt = common.GetTimestamp()
	}
	incoming := &SystemInstance{
		NodeName:   nodeName,
		Info:       infoText,
		StartedAt:  startedAt,
		LastSeenAt: lastSeenAt,
		UpdatedAt:  lastSeenAt,
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		created := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "node_name"}},
			DoNothing: true,
		}).Create(incoming)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 1 {
			return nil
		}
		var existing SystemInstance
		if err := lockForUpdate(tx).Where("node_name = ?", nodeName).First(&existing).Error; err != nil {
			return err
		}
		existingIncarnationID := systemInstanceIncarnationID(existing.Info)
		if existingIncarnationID != incarnationID &&
			existing.LastSeenAt >= lastSeenAt-SystemInstanceStaleAfterSeconds {
			return ErrSystemInstanceIncarnationConflict
		}
		if existingIncarnationID == incarnationID && existing.LastSeenAt > lastSeenAt {
			return nil
		}
		updated := tx.Model(&SystemInstance{}).Where(
			"node_name = ? AND started_at = ? AND last_seen_at = ?",
			existing.NodeName, existing.StartedAt, existing.LastSeenAt,
		).Updates(map[string]any{
			"info": infoText, "started_at": startedAt,
			"last_seen_at": lastSeenAt, "updated_at": lastSeenAt,
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrSystemInstanceIncarnationConflict
		}
		return nil
	})
}

func systemInstanceIncarnationID(info string) string {
	var payload struct {
		Capabilities struct {
			IncarnationID string `json:"async_billing_incarnation_id"`
		} `json:"capabilities"`
	}
	if common.UnmarshalJsonStr(info, &payload) != nil {
		return ""
	}
	return payload.Capabilities.IncarnationID
}

func ListSystemInstances() ([]*SystemInstance, error) {
	var instances []*SystemInstance
	err := DB.Order("last_seen_at desc").Find(&instances).Error
	return instances, err
}

func ListActiveSystemInstances(now int64, limit int) ([]*SystemInstance, error) {
	if now <= 0 {
		now = common.GetTimestamp()
	}
	if limit <= 0 || limit > 10_000 {
		return nil, ErrSystemInstanceActiveLimit
	}
	var instances []*SystemInstance
	err := DB.Where("last_seen_at >= ?", now-SystemInstanceStaleAfterSeconds).
		Order("node_name asc").Limit(limit + 1).Find(&instances).Error
	if err != nil {
		return nil, err
	}
	if len(instances) > limit {
		return nil, ErrSystemInstanceActiveLimit
	}
	return instances, nil
}

func DeleteStaleSystemInstances(now int64) (int64, error) {
	result := DB.Where("last_seen_at < ?", now-SystemInstanceStaleAfterSeconds).Delete(&SystemInstance{})
	return result.RowsAffected, result.Error
}

func DeleteStaleSystemInstance(nodeName string, now int64) (bool, error) {
	result := DB.Where("node_name = ? AND last_seen_at < ?", nodeName, now-SystemInstanceStaleAfterSeconds).Delete(&SystemInstance{})
	return result.RowsAffected > 0, result.Error
}

func (instance *SystemInstance) ToResponse(now int64) SystemInstanceResponse {
	status := SystemInstanceStatusOnline
	if now-instance.LastSeenAt > SystemInstanceStaleAfterSeconds {
		status = SystemInstanceStatusStale
	}
	return SystemInstanceResponse{
		NodeName:          instance.NodeName,
		Status:            status,
		StaleAfterSeconds: SystemInstanceStaleAfterSeconds,
		StartedAt:         instance.StartedAt,
		LastSeenAt:        instance.LastSeenAt,
		Info:              decodeSystemInstanceInfo(instance.Info),
	}
}

func marshalSystemInstanceInfo(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	data, err := common.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeSystemInstanceInfo(data string) any {
	if data == "" {
		return nil
	}
	var value any
	if err := common.UnmarshalJsonStr(data, &value); err != nil {
		return data
	}
	return value
}
