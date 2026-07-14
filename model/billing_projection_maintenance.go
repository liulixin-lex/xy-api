package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
)

const billingProjectionRetryBase = 5 * time.Second

func BillingProjectionMaintenanceEnabled() bool {
	return DB != nil && LOG_DB != nil
}

func billingProjectionRetryDelay(attempts int) time.Duration {
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 6 {
		shift = 6
	}
	ceiling := billingProjectionRetryBase * time.Duration(1<<shift)
	minimum := ceiling / 2
	return minimum + common.FullJitter(ceiling-minimum)
}
