package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFinancialHistoryRecordsRejectUpdateAndDelete(t *testing.T) {
	testCases := []struct {
		name   string
		record interface{}
	}{
		{
			name: "payment configuration audit",
			record: &PaymentConfigurationAudit{
				AdminID: 990101, ActorIP: "192.0.2.101", ChangedKeys: `["StripePriceId"]`,
				PreviousVersion: 1, CommittedVersion: 2, CreatedAt: 1,
			},
		},
		{
			name: "payment ledger",
			record: &PaymentLedgerEntry{
				PaymentOrderID: 990102, PaymentEventID: 990102, UserID: 990102,
				EntryType: "immutable_test", Currency: "USD", CreatedAt: 1,
			},
		},
		{
			name: "quota ledger",
			record: &QuotaLedgerEntry{
				RequestId: "immutable-quota-ledger", Phase: QuotaLedgerPhaseReserve, Revision: 1,
				UserId: 990103,
			},
		},
		{
			name: "billing administrator resolution",
			record: &BillingReservationAdminResolution{
				RequestId: "immutable-admin-resolution", Revision: 1, ExpectedVersion: 1,
				AdminId: 990104, ActorIp: "192.0.2.104", Resolution: BillingReservationAdminRefund,
				Reason: "immutable financial history test",
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require.NoError(t, DB.AutoMigrate(testCase.record))
			require.NoError(t, DB.Create(testCase.record).Error)
			t.Cleanup(func() {
				_ = DB.Session(&gorm.Session{SkipHooks: true}).Delete(testCase.record).Error
			})

			update := DB.Model(testCase.record).Update("created_at", 2)
			assert.ErrorIs(t, update.Error, ErrFinancialHistoryImmutable)
			deletion := DB.Delete(testCase.record)
			assert.ErrorIs(t, deletion.Error, ErrFinancialHistoryImmutable)
		})
	}
}

func TestPaymentConfigurationAuditRequiresActorIP(t *testing.T) {
	_, err := newPaymentConfigurationAudit(PaymentConfigurationAuditInput{
		AdminID: 990105, ChangedKeys: []string{"StripePriceId"}, Reason: "verified configuration change",
	}, 1, 2, 0, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid payment configuration audit input")
}
