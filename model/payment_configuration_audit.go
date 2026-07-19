package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// PaymentConfigurationAudit is the durable, secret-free record of an atomic
// payment gateway configuration mutation. It lives in the primary database so
// the configuration change and its actor evidence commit or roll back together.
type PaymentConfigurationAudit struct {
	ID               int64  `json:"id" gorm:"primaryKey"`
	AdminID          int    `json:"admin_id" gorm:"index"`
	ActorIP          string `json:"actor_ip" gorm:"type:varchar(64)"`
	ChangedKeys      string `json:"changed_keys" gorm:"type:text"`
	RevokedProviders string `json:"revoked_providers" gorm:"type:text"`
	Reason           string `json:"reason,omitempty" gorm:"type:varchar(512)"`
	Emergency        bool   `json:"emergency" gorm:"index"`
	PreviousVersion  int64  `json:"previous_version"`
	CommittedVersion int64  `json:"committed_version" gorm:"index"`
	AffectedOrders   int64  `json:"affected_orders"`
	AffectedEvents   int64  `json:"affected_events"`
	CreatedAt        int64  `json:"created_at" gorm:"index"`
}

type PaymentConfigurationAuditInput struct {
	AdminID          int
	ActorIP          string
	ChangedKeys      []string
	RevokedProviders []string
	Reason           string
}

type PaymentConfigurationPreconditions struct {
	RequireNoActiveEpayOrders   bool
	RequireNoStripeHistory      bool
	RequireStripeWebhookOverlap bool
}

func (input *PaymentConfigurationAuditInput) validate() error {
	if input == nil || input.AdminID <= 0 {
		return errors.New("payment configuration audit actor is required")
	}
	input.ActorIP = strings.TrimSpace(input.ActorIP)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.ActorIP == "" || len(input.ActorIP) > 64 || len(input.Reason) > 512 || len(input.ChangedKeys) == 0 {
		return errors.New("invalid payment configuration audit input")
	}
	if len(input.RevokedProviders) > 0 && len(input.Reason) < 8 {
		return errors.New("payment credential revocation reason must contain 8 to 512 characters")
	}
	return nil
}

func (*PaymentConfigurationAudit) BeforeUpdate(_ *gorm.DB) error {
	return ErrFinancialHistoryImmutable
}

func (*PaymentConfigurationAudit) BeforeDelete(_ *gorm.DB) error {
	return ErrFinancialHistoryImmutable
}

func newPaymentConfigurationAudit(input PaymentConfigurationAuditInput, previousVersion, committedVersion, affectedOrders, affectedEvents int64) (*PaymentConfigurationAudit, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	changedKeys, err := common.Marshal(input.ChangedKeys)
	if err != nil {
		return nil, err
	}
	revokedProviders, err := common.Marshal(input.RevokedProviders)
	if err != nil {
		return nil, err
	}
	return &PaymentConfigurationAudit{
		AdminID: input.AdminID, ActorIP: input.ActorIP,
		ChangedKeys: string(changedKeys), RevokedProviders: string(revokedProviders), Reason: input.Reason,
		Emergency: len(input.RevokedProviders) > 0, PreviousVersion: previousVersion, CommittedVersion: committedVersion,
		AffectedOrders: affectedOrders, AffectedEvents: affectedEvents, CreatedAt: common.GetTimestamp(),
	}, nil
}
