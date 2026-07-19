package setting

import "sync"

// PaymentConfigurationLock keeps provider credentials and their related
// merchant/account settings as one in-memory snapshot across requests and
// background option reloads.
var paymentConfigurationLock sync.RWMutex

func LockPaymentConfigurationForRead() func() {
	paymentConfigurationLock.RLock()
	return paymentConfigurationLock.RUnlock
}

func LockPaymentConfigurationForUpdate() func() {
	paymentConfigurationLock.Lock()
	return paymentConfigurationLock.Unlock
}
