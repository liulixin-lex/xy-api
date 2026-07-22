package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	PaymentCredentialIncidentActionAcknowledge = "acknowledge"
	PaymentCredentialIncidentActionResolve     = "resolve"

	PaymentOperationsActionCredentialIncidentAcknowledge = "payment.credential_incident_acknowledged"
	PaymentOperationsActionCredentialIncidentResolve     = "payment.credential_incident_resolved"
	PaymentOperationsActionStripeBindingRetire           = "payment.stripe_customer_binding_retired"
	PaymentOperationsActionLegacyEpayRetry               = "payment.legacy_epay_event_retried"
	PaymentOperationsActionAdminFulfill                  = "payment.admin_fulfill"
	PaymentOperationsActionAdminReject                   = "payment.admin_reject"
	PaymentOperationsActionAdminVoid                     = "payment.admin_void"
	PaymentOperationsActionAdminExternalRefund           = "payment.admin_external_refund_confirmed"
	PaymentOperationsActionUnmatchedDismiss              = "payment.unmatched_event_dismissed"
	PaymentOperationsActionUnmatchedLink                 = "payment.unmatched_event_linked"
	PaymentOperationsActionDebtResolve                   = "payment.debt_resolved"
)

// PaymentOperationsAudit is an append-only record for privileged payment
// operations. It is committed in the same primary-database transaction as the
// reviewed subject and contains no provider secret or raw webhook payload.
type PaymentOperationsAudit struct {
	ID              int64  `json:"id" gorm:"primaryKey"`
	Action          string `json:"action" gorm:"type:varchar(96);index"`
	AdminID         int    `json:"admin_id" gorm:"index"`
	ActorIP         string `json:"actor_ip" gorm:"type:varchar(64)"`
	PaymentOrderID  int64  `json:"payment_order_id,omitempty" gorm:"index"`
	UserID          int    `json:"user_id,omitempty" gorm:"index"`
	SubjectID       int64  `json:"subject_id,omitempty" gorm:"index"`
	Provider        string `json:"provider,omitempty" gorm:"type:varchar(32);index"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason" gorm:"type:varchar(512)"`
	Metadata        string `json:"metadata,omitempty" gorm:"type:text"`
	CreatedAt       int64  `json:"created_at" gorm:"index"`
}

// PaymentCustomerBindingRetirement preserves the exact active Stripe binding
// that an administrator retired. Active bindings may be deleted only after
// this immutable evidence row has been inserted in the same transaction.
type PaymentCustomerBindingRetirement struct {
	ID                 int64  `json:"id" gorm:"primaryKey"`
	OriginalBindingID  int64  `json:"original_binding_id" gorm:"uniqueIndex"`
	Provider           string `json:"provider" gorm:"type:varchar(32);index"`
	CustomerKey        string `json:"customer_key" gorm:"type:varchar(64);index"`
	UserID             int    `json:"user_id" gorm:"index"`
	BindingCreatedAt   int64  `json:"binding_created_at"`
	BindingUpdatedAt   int64  `json:"binding_updated_at"`
	BindingVersion     int64  `json:"binding_version"`
	UserCustomerBefore string `json:"user_customer_before,omitempty" gorm:"type:varchar(64)"`
	RetiredBy          int    `json:"retired_by" gorm:"index"`
	ActorIP            string `json:"actor_ip" gorm:"type:varchar(64)"`
	Reason             string `json:"reason" gorm:"type:varchar(512)"`
	RetiredAt          int64  `json:"retired_at" gorm:"index"`
}

var ErrPaymentOperationsHistoryImmutable = ErrFinancialHistoryImmutable

func (*PaymentOperationsAudit) BeforeUpdate(_ *gorm.DB) error {
	return ErrPaymentOperationsHistoryImmutable
}

func (*PaymentOperationsAudit) BeforeDelete(_ *gorm.DB) error {
	return ErrPaymentOperationsHistoryImmutable
}

func (*PaymentCustomerBindingRetirement) BeforeUpdate(_ *gorm.DB) error {
	return ErrPaymentOperationsHistoryImmutable
}

func (*PaymentCustomerBindingRetirement) BeforeDelete(_ *gorm.DB) error {
	return ErrPaymentOperationsHistoryImmutable
}

func createPaymentOperationsAuditTx(tx *gorm.DB, audit PaymentOperationsAudit, metadata map[string]interface{}) error {
	if tx == nil {
		return ErrPaymentAuditInvalid
	}
	audit.Action = strings.TrimSpace(audit.Action)
	audit.ActorIP = strings.TrimSpace(audit.ActorIP)
	audit.Reason = strings.TrimSpace(audit.Reason)
	if audit.Action == "" || audit.AdminID <= 0 || audit.ActorIP == "" || len(audit.ActorIP) > 64 ||
		len(audit.Reason) < 8 || len(audit.Reason) > 512 {
		return ErrPaymentAuditInvalid
	}
	if metadata != nil {
		encoded, err := common.Marshal(metadata)
		if err != nil {
			return err
		}
		audit.Metadata = string(encoded)
	}
	if audit.CreatedAt <= 0 {
		audit.CreatedAt = common.GetTimestamp()
	}
	return tx.Create(&audit).Error
}

type PaymentCredentialIncidentActionInput struct {
	TradeNo         string
	ExpectedVersion int64
	AdminID         int
	ActorIP         string
	Action          string
	Reason          string
}

type PaymentCredentialIncidentActionResult struct {
	Order     *PaymentOrder `json:"order"`
	Duplicate bool          `json:"duplicate"`
}

func ReviewPaymentCredentialIncidentByAdmin(input PaymentCredentialIncidentActionInput) (*PaymentCredentialIncidentActionResult, error) {
	input.TradeNo = strings.TrimSpace(input.TradeNo)
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	input.Reason = strings.TrimSpace(input.Reason)
	input.ActorIP = strings.TrimSpace(input.ActorIP)
	if input.TradeNo == "" || len(input.TradeNo) > 128 || input.ExpectedVersion <= 0 || input.AdminID <= 0 ||
		len(input.ActorIP) > 64 || len(input.Reason) < 8 || len(input.Reason) > 512 ||
		(input.Action != PaymentCredentialIncidentActionAcknowledge && input.Action != PaymentCredentialIncidentActionResolve) {
		return nil, ErrPaymentAuditInvalid
	}

	result := &PaymentCredentialIncidentActionResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var order PaymentOrder
		if err := lockForUpdate(tx).Where("trade_no = ?", input.TradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentAuditNotFound
			}
			return err
		}
		result.Order = &order
		payloadBytes, err := common.Marshal(map[string]interface{}{
			"source": "admin", "action": input.Action, "admin_id": input.AdminID,
			"reason": input.Reason, "expected_version": input.ExpectedVersion,
			"trade_no": order.TradeNo, "payment_order_id": order.ID,
			"economic_status": order.Status, "credential_incident_generation": order.CredentialIncidentGeneration,
		})
		if err != nil {
			return err
		}
		payload := string(payloadBytes)
		eventKey := "credential_incident_" + input.Action + ":" + strconv.FormatInt(order.ID, 10) + ":v" + strconv.FormatInt(input.ExpectedVersion, 10)
		duplicate, err := processedAdminActionRetryTx(tx, eventKey, PaymentPayloadDigest(payload))
		if err != nil {
			return err
		}
		if duplicate {
			result.Duplicate = true
			return nil
		}
		if order.Version != input.ExpectedVersion {
			return fmt.Errorf("%w: payment order version changed", ErrPaymentAuditConflict)
		}
		if !order.CredentialIncident || (order.CredentialIncidentState != PaymentCredentialIncidentOpen &&
			order.CredentialIncidentState != PaymentCredentialIncidentAcknowledged) {
			return fmt.Errorf("%w: payment credential incident is not open", ErrPaymentAuditConflict)
		}
		if input.Action == PaymentCredentialIncidentActionAcknowledge && order.CredentialIncidentState != PaymentCredentialIncidentOpen {
			return fmt.Errorf("%w: payment credential incident is already acknowledged", ErrPaymentAuditConflict)
		}

		now := common.GetTimestamp()
		state := PaymentCredentialIncidentAcknowledged
		incidentOpen := true
		eventType := PaymentOperationsActionCredentialIncidentAcknowledge
		if input.Action == PaymentCredentialIncidentActionResolve {
			state = PaymentCredentialIncidentResolved
			incidentOpen = false
			eventType = PaymentOperationsActionCredentialIncidentResolve
		}
		updates := map[string]interface{}{
			"credential_incident": incidentOpen, "credential_incident_state": state,
			"credential_incident_reviewed_at": now, "credential_incident_reviewed_by": input.AdminID,
			"credential_incident_review_note": input.Reason, "updated_at": now,
			"version": gorm.Expr("version + ?", 1),
		}
		updated := tx.Model(&PaymentOrder{}).Where("id = ? AND version = ?", order.ID, input.ExpectedVersion).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return fmt.Errorf("%w: payment order version changed", ErrPaymentAuditConflict)
		}
		event, err := createAdminPaymentEventTx(tx, eventKey, eventType, payload, &order)
		if err != nil {
			return err
		}
		if err := finishPaymentEventTx(tx, event.ID, PaymentEventStatusProcessed, "", order.ID); err != nil {
			return err
		}
		metadata, err := common.Marshal(map[string]interface{}{
			"trade_no": order.TradeNo, "economic_status": order.Status,
			"credential_incident_generation": order.CredentialIncidentGeneration,
		})
		if err != nil {
			return err
		}
		if err := tx.Create(&PaymentOperationsAudit{
			Action: eventType, AdminID: input.AdminID, ActorIP: input.ActorIP,
			PaymentOrderID: order.ID, UserID: order.UserID, SubjectID: order.ID, Provider: order.Provider,
			ExpectedVersion: input.ExpectedVersion, Reason: input.Reason, Metadata: string(metadata), CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		order.CredentialIncident = incidentOpen
		order.CredentialIncidentState = state
		order.CredentialIncidentReviewedAt = now
		order.CredentialIncidentReviewedBy = input.AdminID
		order.CredentialIncidentReviewNote = input.Reason
		order.UpdatedAt = now
		order.Version++
		result.Order = &order
		return nil
	})
	return result, err
}

type RetireStripeCustomerBindingInput struct {
	BindingID       int64
	UserID          int
	ExpectedVersion int64
	AdminID         int
	ActorIP         string
	Reason          string
}

type RetireStripeCustomerBindingResult struct {
	Retirement *PaymentCustomerBindingRetirement `json:"retirement"`
	Duplicate  bool                              `json:"duplicate"`
}

func RetireStripeCustomerBindingByAdmin(input RetireStripeCustomerBindingInput) (*RetireStripeCustomerBindingResult, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	input.ActorIP = strings.TrimSpace(input.ActorIP)
	if input.BindingID <= 0 || input.UserID <= 0 || input.ExpectedVersion <= 0 || input.AdminID <= 0 ||
		len(input.ActorIP) > 64 || len(input.Reason) < 8 || len(input.Reason) > 512 {
		return nil, ErrPaymentAuditInvalid
	}
	result := &RetireStripeCustomerBindingResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var existingRetirement PaymentCustomerBindingRetirement
		existingQuery := tx.Where("original_binding_id = ?", input.BindingID).Limit(1).Find(&existingRetirement)
		if existingQuery.Error != nil {
			return existingQuery.Error
		}
		if existingQuery.RowsAffected > 0 {
			if existingRetirement.UserID != input.UserID || existingRetirement.BindingVersion != input.ExpectedVersion ||
				existingRetirement.RetiredBy != input.AdminID || existingRetirement.Reason != input.Reason {
				return fmt.Errorf("%w: Stripe customer binding retirement payload changed", ErrPaymentAuditConflict)
			}
			result.Retirement = &existingRetirement
			result.Duplicate = true
			return nil
		}

		var user User
		if err := lockForUpdate(tx).Select("id", "stripe_customer").Where("id = ?", input.UserID).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentAuditNotFound
			}
			return err
		}
		var binding PaymentCustomerBinding
		if err := lockForUpdate(tx).Where("id = ?", input.BindingID).First(&binding).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentAuditNotFound
			}
			return err
		}
		if binding.Provider != PaymentProviderStripe || binding.UserID != input.UserID {
			return fmt.Errorf("%w: Stripe customer binding does not belong to the requested user", ErrPaymentAuditConflict)
		}
		publicVersion := binding.Version
		if publicVersion == 0 {
			publicVersion = 1
		}
		if publicVersion != input.ExpectedVersion {
			return fmt.Errorf("%w: Stripe customer binding version changed", ErrPaymentAuditConflict)
		}
		currentCustomer := strings.TrimSpace(user.StripeCustomer)
		if currentCustomer != "" && currentCustomer != binding.CustomerKey {
			return fmt.Errorf("%w: user Stripe customer no longer matches the active binding", ErrPaymentAuditConflict)
		}
		now := common.GetTimestamp()
		retirement := &PaymentCustomerBindingRetirement{
			OriginalBindingID: binding.ID, Provider: binding.Provider, CustomerKey: binding.CustomerKey, UserID: binding.UserID,
			BindingCreatedAt: binding.CreatedAt, BindingUpdatedAt: binding.UpdatedAt, BindingVersion: publicVersion,
			UserCustomerBefore: currentCustomer, RetiredBy: input.AdminID, ActorIP: input.ActorIP,
			Reason: input.Reason, RetiredAt: now,
		}
		if err := tx.Create(retirement).Error; err != nil {
			return err
		}
		deleted := tx.Where("id = ? AND version = ?", binding.ID, binding.Version).Delete(&PaymentCustomerBinding{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected != 1 {
			return fmt.Errorf("%w: Stripe customer binding version changed", ErrPaymentAuditConflict)
		}
		if currentCustomer != "" {
			cleared := tx.Model(&User{}).Where("id = ? AND stripe_customer = ?", user.Id, currentCustomer).Update("stripe_customer", "")
			if cleared.Error != nil {
				return cleared.Error
			}
			if cleared.RowsAffected != 1 {
				return fmt.Errorf("%w: user Stripe customer changed during retirement", ErrPaymentAuditConflict)
			}
		}
		metadata, err := common.Marshal(map[string]interface{}{
			"binding_id": binding.ID, "customer_key": binding.CustomerKey,
		})
		if err != nil {
			return err
		}
		if err := tx.Create(&PaymentOperationsAudit{
			Action: PaymentOperationsActionStripeBindingRetire, AdminID: input.AdminID, ActorIP: input.ActorIP,
			UserID: binding.UserID, SubjectID: binding.ID, Provider: binding.Provider,
			ExpectedVersion: input.ExpectedVersion, Reason: input.Reason, Metadata: string(metadata), CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		result.Retirement = retirement
		return nil
	})
	if err == nil && result.Retirement != nil {
		_ = InvalidateUserCache(result.Retirement.UserID)
	}
	return result, err
}

func ListStripeCustomerBindingsForAdmin(userID int) ([]PaymentCustomerBinding, []PaymentCustomerBindingRetirement, error) {
	if userID <= 0 {
		return nil, nil, ErrPaymentAuditInvalid
	}
	var active []PaymentCustomerBinding
	if err := DB.Where("provider = ? AND user_id = ?", PaymentProviderStripe, userID).Order("id desc").Find(&active).Error; err != nil {
		return nil, nil, err
	}
	for index := range active {
		if active[index].Version == 0 {
			active[index].Version = 1
		}
	}
	var retired []PaymentCustomerBindingRetirement
	if err := DB.Where("provider = ? AND user_id = ?", PaymentProviderStripe, userID).Order("retired_at desc, id desc").Find(&retired).Error; err != nil {
		return nil, nil, err
	}
	return active, retired, nil
}

func markCanonicalPaymentCredentialIncidentsTx(tx *gorm.DB, revocation PaymentCredentialRevocation, now int64, affected map[int64]struct{}) error {
	currentOnlyDisable := paymentProviderUsesCurrentOnlyCredentials(revocation.Provider) &&
		revocation.Generation == 0 && revocation.AllActiveOrders
	if tx == nil || revocation.Provider == "" || (!currentOnlyDisable && revocation.Generation <= 0) || revocation.ValidBefore <= 0 || now <= 0 {
		return errors.New("invalid payment credential incident request")
	}
	query := lockForUpdate(tx).Where("provider = ?", revocation.Provider)
	if currentOnlyDisable {
		dependentQuery, _, err := paymentOrdersDependingOnConfigurationQueryTx(tx, revocation.Provider, revocation.ValidBefore)
		if err != nil {
			return err
		}
		query = lockForUpdate(dependentQuery)
	} else {
		query = query.Where("provider_credential_generation = ? OR (provider_credential_generation = 0 AND created_at <= ?)",
			revocation.Generation, revocation.ValidBefore)
	}
	var orders []PaymentOrder
	if err := query.Find(&orders).Error; err != nil {
		return err
	}
	orderByID := make(map[int64]PaymentOrder, len(orders))
	for _, order := range orders {
		orderByID[order.ID] = order
	}
	if revocation.Provider == PaymentProviderStripe {
		if tx.Migrator().HasTable(&PaymentEvent{}) {
			var linkedOrderIDs []int64
			if err := tx.Model(&PaymentEvent{}).
				Where("provider = ? AND provider_credential_generation = ? AND payment_order_id > 0", PaymentProviderStripe, revocation.Generation).
				Where("paid = ? OR refunded = ? OR disputed = ? OR dispute_resolved = ? OR paid_amount_minor > 0 OR refunded_amount_minor > 0 OR disputed_amount_minor > 0",
					true, true, true, true).
				Distinct().Pluck("payment_order_id", &linkedOrderIDs).Error; err != nil {
				return err
			}
			if len(linkedOrderIDs) > 0 {
				var linkedOrders []PaymentOrder
				if err := lockForUpdate(tx).Where("id IN ? AND provider = ?", linkedOrderIDs, PaymentProviderStripe).Find(&linkedOrders).Error; err != nil {
					return err
				}
				for _, order := range linkedOrders {
					orderByID[order.ID] = order
				}
			}
		}
	}
	if revocation.AllActiveOrders && !currentOnlyDisable {
		var activeOrders []PaymentOrder
		if err := lockForUpdate(tx).Where("provider = ? AND status IN ?", revocation.Provider, paymentInFlightOrderStatuses()).Find(&activeOrders).Error; err != nil {
			return err
		}
		for _, order := range activeOrders {
			orderByID[order.ID] = order
		}
	}
	orders = orders[:0]
	for _, order := range orderByID {
		orders = append(orders, order)
	}
	reason := "provider credential generation revoked; review payment evidence"
	if revocation.Provider == PaymentProviderStripe {
		reason = "Stripe webhook signing credential revoked; review payment evidence"
	} else if currentOnlyDisable {
		reason = "provider current-only credential disabled; no overlap generation exists; review dependent payment evidence"
	}
	activeStatuses := map[string]struct{}{
		PaymentOrderStatusPending: {}, PaymentOrderStatusProcessing: {}, PaymentOrderStatusManualReview: {},
	}
	for index := range orders {
		order := &orders[index]
		if _, seen := affected[order.ID]; seen {
			continue
		}
		affected[order.ID] = struct{}{}
		incidentGeneration := revocation.Generation
		incidentReason := reason
		if order.ProviderCredentialGeneration == 0 && !currentOnlyDisable {
			incidentGeneration = 0
			incidentReason += "; legacy order credential generation is ambiguous"
		}
		if order.CredentialIncident && order.CredentialIncidentGeneration == incidentGeneration &&
			(order.CredentialIncidentState == PaymentCredentialIncidentOpen || order.CredentialIncidentState == PaymentCredentialIncidentAcknowledged) {
			delete(affected, order.ID)
			continue
		}
		updates := map[string]interface{}{
			"credential_incident": true, "credential_incident_state": PaymentCredentialIncidentOpen,
			"credential_incident_generation": incidentGeneration, "credential_incident_reason": incidentReason,
			"credential_incident_at": now, "credential_incident_reviewed_at": 0,
			"credential_incident_reviewed_by": 0, "credential_incident_review_note": "",
			"updated_at": now, "version": gorm.Expr("version + ?", 1),
		}
		if _, active := activeStatuses[order.Status]; active || currentOnlyDisable {
			updates["status"] = PaymentOrderStatusManualReview
			updates["status_reason"] = incidentReason
			updates["start_flow"] = ""
			updates["start_payload"] = ""
			updates["browser_authorization_digest"] = nil
			updates["browser_authorization_payload"] = ""
			updates["browser_authorization_expires_at"] = 0
			updates["browser_authorized_at"] = 0
			order.Status = PaymentOrderStatusManualReview
			order.StatusReason = incidentReason
			order.StartFlow = ""
			order.StartPayload = ""
		}
		if err := tx.Model(&PaymentOrder{}).Where("id = ?", order.ID).Updates(updates).Error; err != nil {
			return err
		}
		if _, active := activeStatuses[order.Status]; active {
			if err := syncPaymentProjectionStatusTx(tx, order); err != nil {
				return err
			}
		}
	}
	return nil
}
