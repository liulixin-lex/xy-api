package service

import "context"

type paymentConfigurationReadLockContextKey struct{}

func withPaymentConfigurationReadLock(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, paymentConfigurationReadLockContextKey{}, true)
}

func paymentConfigurationReadLockHeld(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	held, _ := ctx.Value(paymentConfigurationReadLockContextKey{}).(bool)
	return held
}
