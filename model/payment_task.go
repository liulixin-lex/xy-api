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

const (
	PaymentTaskOperationCreate      = "create"
	PaymentTaskOperationReconcile   = "reconcile"
	PaymentTaskOperationMaintenance = "maintenance"

	PaymentTaskStatusPending      = "pending"
	PaymentTaskStatusRunning      = "running"
	PaymentTaskStatusRetryWait    = "retry_wait"
	PaymentTaskStatusSucceeded    = "succeeded"
	PaymentTaskStatusFailed       = "failed"
	PaymentTaskStatusManualReview = "manual_review"

	PaymentTaskPhaseReady               = "ready"
	PaymentTaskPhaseProviderCallStarted = "provider_call_started"
	PaymentTaskPhaseWaiting             = "waiting"
)

var ErrPaymentTaskLeaseLost = errors.New("payment task lease lost")

// PaymentTask is the durable, multi-node work queue for provider creation,
// reconciliation and payment maintenance. A monotonically increasing fence is
// copied to PaymentOrder before a creation worker is allowed to call a
// provider, so an expired worker can never overwrite the result of its
// successor.
type PaymentTask struct {
	ID             int64  `json:"id" gorm:"primaryKey"`
	TaskID         string `json:"task_id" gorm:"type:varchar(192);uniqueIndex"`
	PaymentOrderID int64  `json:"payment_order_id" gorm:"index;uniqueIndex:idx_payment_task_subject,priority:1"`
	Operation      string `json:"operation" gorm:"type:varchar(32);index;uniqueIndex:idx_payment_task_subject,priority:2"`
	Status         string `json:"status" gorm:"type:varchar(32);index"`
	Phase          string `json:"phase" gorm:"type:varchar(48);index"`
	AvailableAt    int64  `json:"available_at" gorm:"index"`
	LeaseOwner     string `json:"lease_owner,omitempty" gorm:"type:varchar(160);index"`
	LeaseUntil     int64  `json:"lease_until" gorm:"index"`
	FenceToken     int64  `json:"fence_token"`
	Attempts       int    `json:"attempts"`
	RecoveryMisses int    `json:"recovery_misses"`
	LastErrorCode  string `json:"last_error_code,omitempty" gorm:"type:varchar(64);index"`
	LastError      string `json:"last_error,omitempty" gorm:"type:varchar(1024)"`
	CreatedAt      int64  `json:"created_at" gorm:"index"`
	UpdatedAt      int64  `json:"updated_at" gorm:"index"`
	FinishedAt     int64  `json:"finished_at" gorm:"index"`
}

func (task *PaymentTask) BeforeCreate(_ *gorm.DB) error {
	now := common.GetTimestamp()
	if task.CreatedAt == 0 {
		task.CreatedAt = now
	}
	if task.UpdatedAt == 0 {
		task.UpdatedAt = now
	}
	if task.Status == "" {
		task.Status = PaymentTaskStatusPending
	}
	if task.Phase == "" {
		task.Phase = PaymentTaskPhaseReady
	}
	if task.AvailableAt == 0 {
		task.AvailableAt = now
	}
	return nil
}

func paymentTaskID(operation string, paymentOrderID int64) string {
	return fmt.Sprintf("payment:%s:%d", operation, paymentOrderID)
}

func ensurePaymentTaskTx(tx *gorm.DB, paymentOrderID int64, operation string, availableAt int64) (*PaymentTask, error) {
	if tx == nil || paymentOrderID < 0 || strings.TrimSpace(operation) == "" {
		return nil, errors.New("invalid payment task")
	}
	if paymentOrderID == 0 && operation != PaymentTaskOperationMaintenance {
		return nil, errors.New("payment task order is required")
	}
	if paymentOrderID > 0 && operation == PaymentTaskOperationMaintenance {
		return nil, errors.New("payment maintenance task cannot target an order")
	}
	if availableAt <= 0 {
		availableAt = common.GetTimestamp()
	}
	task := &PaymentTask{
		TaskID:         paymentTaskID(operation, paymentOrderID),
		PaymentOrderID: paymentOrderID,
		Operation:      operation,
		Status:         PaymentTaskStatusPending,
		Phase:          PaymentTaskPhaseReady,
		AvailableAt:    availableAt,
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "payment_order_id"}, {Name: "operation"}},
		DoNothing: true,
	}).Create(task)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 1 {
		return task, nil
	}
	var existing PaymentTask
	if lookupErr := tx.Where("payment_order_id = ? AND operation = ?", paymentOrderID, operation).First(&existing).Error; lookupErr != nil {
		return nil, lookupErr
	}
	return &existing, nil
}

func EnsurePaymentTask(paymentOrderID int64, operation string, availableAt int64) (*PaymentTask, error) {
	return ensurePaymentTaskTx(DB, paymentOrderID, operation, availableAt)
}

func EnsurePaymentMaintenanceTask() (*PaymentTask, error) {
	return ensurePaymentTaskTx(DB, 0, PaymentTaskOperationMaintenance, common.GetTimestamp())
}

func GetPaymentTaskForOrder(paymentOrderID int64, operation string) (*PaymentTask, error) {
	var task PaymentTask
	err := DB.Where("payment_order_id = ? AND operation = ?", paymentOrderID, operation).First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &task, err
}

// ClaimDuePaymentTasks claims bounded work with compare-and-swap updates. It
// does not rely on SKIP LOCKED, so the same code works on SQLite, MySQL 5.7 and
// PostgreSQL 9.6. Multiple nodes may discover the same candidate, but only one
// conditional update can advance its fence.
func ClaimDuePaymentTasks(ctx context.Context, runnerID string, now int64, lease time.Duration, limit int) ([]*PaymentTask, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runnerID = strings.TrimSpace(runnerID)
	if runnerID == "" {
		return nil, errors.New("payment task runner id is required")
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	leaseSeconds := int64(lease / time.Second)
	if leaseSeconds < 15 {
		leaseSeconds = 15
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 64 {
		limit = 64
	}

	var candidates []PaymentTask
	err := DB.WithContext(ctx).
		Where("((status IN ? AND available_at <= ?) OR (status = ? AND lease_until < ?))",
			[]string{PaymentTaskStatusPending, PaymentTaskStatusRetryWait}, now,
			PaymentTaskStatusRunning, now).
		Order("available_at ASC, id ASC").Limit(limit * 2).Find(&candidates).Error
	if err != nil {
		return nil, err
	}

	claimed := make([]*PaymentTask, 0, limit)
	for i := range candidates {
		if len(claimed) >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return claimed, err
		}
		candidate := candidates[i]
		var task PaymentTask
		claimErr := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			result := tx.Model(&PaymentTask{}).
				Where("id = ? AND fence_token = ?", candidate.ID, candidate.FenceToken).
				Where("((status IN ? AND available_at <= ?) OR (status = ? AND lease_until < ?))",
					[]string{PaymentTaskStatusPending, PaymentTaskStatusRetryWait}, now,
					PaymentTaskStatusRunning, now).
				Updates(map[string]interface{}{
					"status":      PaymentTaskStatusRunning,
					"lease_owner": runnerID,
					"lease_until": now + leaseSeconds,
					"fence_token": gorm.Expr("fence_token + ?", 1),
					"attempts":    gorm.Expr("attempts + ?", 1),
					"updated_at":  now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return nil
			}
			if err := tx.Where("id = ?", candidate.ID).First(&task).Error; err != nil {
				return err
			}
			if task.Operation == PaymentTaskOperationCreate {
				orderUpdate := tx.Model(&PaymentOrder{}).
					Where("id = ? AND creation_fence_token < ?", task.PaymentOrderID, task.FenceToken).
					Updates(map[string]interface{}{
						"creation_fence_token": task.FenceToken,
						"updated_at":           now,
					})
				if orderUpdate.Error != nil {
					return orderUpdate.Error
				}
				if orderUpdate.RowsAffected == 0 {
					return ErrPaymentTaskLeaseLost
				}
			}
			return nil
		})
		if claimErr != nil {
			if errors.Is(claimErr, ErrPaymentTaskLeaseLost) {
				continue
			}
			return claimed, claimErr
		}
		if task.ID != 0 {
			claimed = append(claimed, &task)
		}
	}
	return claimed, nil
}

func paymentTaskLeaseScope(task *PaymentTask, runnerID string) *gorm.DB {
	if task == nil {
		return DB.Where("1 = 0")
	}
	now := common.GetTimestamp()
	return DB.Model(&PaymentTask{}).
		Where("id = ? AND status = ? AND lease_owner = ? AND fence_token = ? AND lease_until >= ?",
			task.ID, PaymentTaskStatusRunning, runnerID, task.FenceToken, now)
}

func assertPaymentTaskLeaseTx(tx *gorm.DB, task *PaymentTask, runnerID string) error {
	if tx == nil || task == nil || task.ID <= 0 || task.FenceToken <= 0 || strings.TrimSpace(runnerID) == "" {
		return ErrPaymentTaskLeaseLost
	}
	var stored PaymentTask
	err := lockForUpdate(tx).Select("id", "status", "lease_owner", "lease_until", "fence_token").
		Where("id = ?", task.ID).First(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrPaymentTaskLeaseLost
	}
	if err != nil {
		return err
	}
	if stored.Status != PaymentTaskStatusRunning || stored.LeaseOwner != strings.TrimSpace(runnerID) ||
		stored.FenceToken != task.FenceToken || stored.LeaseUntil < common.GetTimestamp() {
		return ErrPaymentTaskLeaseLost
	}
	return nil
}

// AssertPaymentTaskLease verifies the current owner and fencing token while
// holding the task row lock for the duration of the transaction. State-changing
// worker operations should call assertPaymentTaskLeaseTx inside the same
// transaction as their durable side effects instead of relying on a separate
// preflight check.
func AssertPaymentTaskLease(task *PaymentTask, runnerID string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		return assertPaymentTaskLeaseTx(tx, task, runnerID)
	})
}

func UpdatePaymentTaskPhase(task *PaymentTask, runnerID, phase string) error {
	phase = strings.TrimSpace(phase)
	if phase == "" {
		return errors.New("payment task phase is required")
	}
	result := paymentTaskLeaseScope(task, runnerID).Updates(map[string]interface{}{
		"phase":      phase,
		"updated_at": common.GetTimestamp(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrPaymentTaskLeaseLost
	}
	task.Phase = phase
	return nil
}

func RetryPaymentTask(task *PaymentTask, runnerID string, availableAt int64, phase, errorCode, errorMessage string, recoveryMisses int) error {
	if availableAt <= common.GetTimestamp() {
		availableAt = common.GetTimestamp() + 1
	}
	if phase == "" {
		phase = task.Phase
	}
	if len(errorMessage) > 1024 {
		errorMessage = errorMessage[:1024]
	}
	result := paymentTaskLeaseScope(task, runnerID).Updates(map[string]interface{}{
		"status":          PaymentTaskStatusRetryWait,
		"phase":           phase,
		"available_at":    availableAt,
		"lease_owner":     "",
		"lease_until":     0,
		"recovery_misses": recoveryMisses,
		"last_error_code": strings.TrimSpace(errorCode),
		"last_error":      strings.TrimSpace(errorMessage),
		"updated_at":      common.GetTimestamp(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrPaymentTaskLeaseLost
	}
	return nil
}

func FinishPaymentTask(task *PaymentTask, runnerID, status, errorCode, errorMessage string) error {
	if status != PaymentTaskStatusSucceeded && status != PaymentTaskStatusFailed && status != PaymentTaskStatusManualReview {
		return errors.New("invalid terminal payment task status")
	}
	if len(errorMessage) > 1024 {
		errorMessage = errorMessage[:1024]
	}
	now := common.GetTimestamp()
	result := paymentTaskLeaseScope(task, runnerID).Updates(map[string]interface{}{
		"status":          status,
		"lease_owner":     "",
		"lease_until":     0,
		"last_error_code": strings.TrimSpace(errorCode),
		"last_error":      strings.TrimSpace(errorMessage),
		"finished_at":     now,
		"updated_at":      now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrPaymentTaskLeaseLost
	}
	return nil
}
