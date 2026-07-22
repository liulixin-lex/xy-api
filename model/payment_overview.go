package model

import "github.com/QuantumNous/new-api/common"

// PaymentOperationsOverview is a compact administrator-only health snapshot.
// It contains counts and ages only; order identifiers and provider diagnostics
// remain in the existing payment audit detail endpoints.
type PaymentOperationsOverview struct {
	PreparingOrders                  int64 `json:"preparing_orders"`
	AwaitingPaymentOrders            int64 `json:"awaiting_payment_orders"`
	ConfirmingOrders                 int64 `json:"confirming_orders"`
	ManualReviewOrders               int64 `json:"manual_review_orders"`
	CreateTaskBacklog                int64 `json:"create_task_backlog"`
	ReconcileTaskBacklog             int64 `json:"reconcile_task_backlog"`
	RunningTasks                     int64 `json:"running_tasks"`
	RetryWaitingTasks                int64 `json:"retry_waiting_tasks"`
	ExpiredTaskLeases                int64 `json:"expired_task_leases"`
	OldestCreateTaskAgeSeconds       int64 `json:"oldest_create_task_age_seconds"`
	UnmatchedPaymentEvents           int64 `json:"unmatched_payment_events"`
	UnprocessedPaymentEvents         int64 `json:"unprocessed_payment_events"`
	OldestUnprocessedEventAgeSeconds int64 `json:"oldest_unprocessed_event_age_seconds"`
	ActiveLimitReservations          int64 `json:"active_limit_reservations"`
	ExpiredActiveLimitReservations   int64 `json:"expired_active_limit_reservations"`
	PaymentConfigurationVersion      int64 `json:"payment_configuration_version"`
}

func GetPaymentOperationsOverview(now int64) (*PaymentOperationsOverview, error) {
	if now <= 0 {
		now = common.GetTimestamp()
	}
	overview := &PaymentOperationsOverview{}
	counts := []struct {
		target *int64
		model  any
		where  string
		args   []any
	}{
		{&overview.PreparingOrders, &PaymentOrder{}, "status IN ? AND start_payload = ?", []any{[]string{PaymentOrderStatusPending, PaymentOrderStatusProcessing}, ""}},
		{&overview.AwaitingPaymentOrders, &PaymentOrder{}, "status = ? AND start_payload <> ?", []any{PaymentOrderStatusPending, ""}},
		{&overview.ConfirmingOrders, &PaymentOrder{}, "status = ?", []any{PaymentOrderStatusPaid}},
		{&overview.ManualReviewOrders, &PaymentOrder{}, "status = ?", []any{PaymentOrderStatusManualReview}},
		{&overview.CreateTaskBacklog, &PaymentTask{}, "operation = ? AND status IN ?", []any{PaymentTaskOperationCreate, []string{PaymentTaskStatusPending, PaymentTaskStatusRunning, PaymentTaskStatusRetryWait}}},
		{&overview.ReconcileTaskBacklog, &PaymentTask{}, "operation = ? AND status IN ?", []any{PaymentTaskOperationReconcile, []string{PaymentTaskStatusPending, PaymentTaskStatusRunning, PaymentTaskStatusRetryWait}}},
		{&overview.RunningTasks, &PaymentTask{}, "status = ?", []any{PaymentTaskStatusRunning}},
		{&overview.RetryWaitingTasks, &PaymentTask{}, "status = ?", []any{PaymentTaskStatusRetryWait}},
		{&overview.ExpiredTaskLeases, &PaymentTask{}, "status = ? AND lease_until > 0 AND lease_until < ?", []any{PaymentTaskStatusRunning, now}},
		{&overview.UnmatchedPaymentEvents, &PaymentEvent{}, "payment_order_id = ? AND status IN ?", []any{0, []string{PaymentEventStatusManualReview, PaymentEventStatusCredentialRevoked}}},
		{&overview.UnprocessedPaymentEvents, &PaymentEvent{}, "status IN ?", []any{[]string{PaymentEventStatusReceived, PaymentEventStatusProcessing, PaymentEventStatusFailed}}},
		{&overview.ActiveLimitReservations, &PaymentLimitReservation{}, "status = ?", []any{PaymentLimitReservationActive}},
		{&overview.ExpiredActiveLimitReservations, &PaymentLimitReservation{}, "status = ? AND expires_at > 0 AND expires_at <= ?", []any{PaymentLimitReservationActive, now}},
	}
	for _, count := range counts {
		if err := DB.Model(count.model).Where(count.where, count.args...).Count(count.target).Error; err != nil {
			return nil, err
		}
	}

	var oldestCreateTask PaymentTask
	if result := DB.Where("operation = ? AND status IN ?", PaymentTaskOperationCreate,
		[]string{PaymentTaskStatusPending, PaymentTaskStatusRunning, PaymentTaskStatusRetryWait}).
		Order("created_at ASC, id ASC").Limit(1).Find(&oldestCreateTask); result.Error != nil {
		return nil, result.Error
	} else if result.RowsAffected > 0 && oldestCreateTask.CreatedAt > 0 && oldestCreateTask.CreatedAt < now {
		overview.OldestCreateTaskAgeSeconds = now - oldestCreateTask.CreatedAt
	}

	var oldestEvent PaymentEvent
	if result := DB.Where("status IN ?", []string{PaymentEventStatusReceived, PaymentEventStatusProcessing, PaymentEventStatusFailed}).
		Order("created_at ASC, id ASC").Limit(1).Find(&oldestEvent); result.Error != nil {
		return nil, result.Error
	} else if result.RowsAffected > 0 && oldestEvent.CreatedAt > 0 && oldestEvent.CreatedAt < now {
		overview.OldestUnprocessedEventAgeSeconds = now - oldestEvent.CreatedAt
	}

	version, err := CurrentPaymentConfigurationVersion()
	if err != nil {
		return nil, err
	}
	overview.PaymentConfigurationVersion = version
	return overview, nil
}
