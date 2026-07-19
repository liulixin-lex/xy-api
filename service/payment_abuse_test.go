package service

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
)

type idempotencyCountingProvider struct {
	name    string
	creates atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (provider *idempotencyCountingProvider) Name() string { return provider.name }

func (*idempotencyCountingProvider) ValidateMethod(string) error { return nil }

func (provider *idempotencyCountingProvider) Create(ctx context.Context, order *model.PaymentOrder) (*PaymentStart, error) {
	provider.creates.Add(1)
	select {
	case provider.entered <- struct{}{}:
	default:
	}
	select {
	case <-provider.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &PaymentStart{
		Flow: PaymentFlowHostedRedirect, URL: "https://pay.example.test/" + order.TradeNo,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), ProviderOrderKey: provider.name + ":" + order.TradeNo,
	}, nil
}

func (*idempotencyCountingProvider) VerifyWebhook(*http.Request) (*NormalizedPaymentEvent, error) {
	return nil, errors.New("not implemented")
}

func (*idempotencyCountingProvider) Query(context.Context, *model.PaymentOrder) (*NormalizedPaymentEvent, error) {
	return nil, nil
}

func TestConcurrentCompatibleStartsCreateUpstreamPaymentOnce(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	require.NoError(t, model.DB.AutoMigrate(
		&model.Option{}, &model.PaymentQuote{}, &model.PaymentUserGuard{}, &model.PaymentOrder{}, &model.TopUp{},
	))
	require.NoError(t, model.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&model.Option{Key: model.PaymentConfigurationVersionOptionKey, Value: "1"}).Error)

	provider := &idempotencyCountingProvider{
		name: "idempotency-counting-provider", entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	RegisterPaymentProvider(provider)
	const userID = 998821
	now := time.Now().Unix()
	quotes := []*model.PaymentQuote{
		{QuoteID: "Q_IDEMPOTENCY_UPSTREAM_A", UserID: userID, OrderKind: model.PaymentOrderKindTopUp, Provider: provider.name, PaymentMethod: "hosted", RequestedAmount: 10, CreditQuota: 5000, ExpectedAmountMinor: 1000, Currency: "USD", PricingSnapshot: `{"amount":10}`, ExpiresAt: now + 3600},
		{QuoteID: "Q_IDEMPOTENCY_UPSTREAM_B", UserID: userID, OrderKind: model.PaymentOrderKindTopUp, Provider: provider.name, PaymentMethod: "hosted", RequestedAmount: 10, CreditQuota: 5000, ExpectedAmountMinor: 1000, Currency: "USD", PricingSnapshot: `{"amount":10}`, ExpiresAt: now + 3601},
	}
	for _, quote := range quotes {
		require.NoError(t, model.CreatePaymentQuote(quote))
	}
	t.Cleanup(func() {
		model.DB.Where("user_id = ?", userID).Delete(&model.TopUp{})
		model.DB.Where("user_id = ?", userID).Delete(&model.PaymentOrder{})
		model.DB.Where("user_id = ?", userID).Delete(&model.PaymentQuote{})
		model.DB.Where("user_id = ?", userID).Delete(&model.PaymentUserGuard{})
	})

	firstResult := make(chan *PaymentStart, 1)
	firstError := make(chan error, 1)
	go func() {
		start, err := StartPayment(t.Context(), userID, PaymentStartRequest{
			QuoteID: quotes[0].QuoteID, RequestID: "same-client-request",
		})
		firstResult <- start
		firstError <- err
	}()
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider create was not reached")
	}

	second, secondErr := StartPayment(t.Context(), userID, PaymentStartRequest{
		QuoteID: quotes[1].QuoteID, RequestID: "same-client-request",
	})
	require.ErrorIs(t, secondErr, ErrPaymentStateUnknown)
	require.NotNil(t, second)
	assert.Equal(t, PaymentFlowPending, second.Flow)
	close(provider.release)

	first := <-firstResult
	require.NoError(t, <-firstError)
	require.NotNil(t, first)
	assert.Equal(t, first.TradeNo, second.TradeNo)
	assert.EqualValues(t, 1, provider.creates.Load())

	retry, err := StartPayment(t.Context(), userID, PaymentStartRequest{
		QuoteID: quotes[1].QuoteID, RequestID: "same-client-request",
	})
	require.NoError(t, err)
	assert.Equal(t, first.TradeNo, retry.TradeNo)
	assert.Equal(t, first.URL, retry.URL)
	assert.EqualValues(t, 1, provider.creates.Load())
}

func TestPaymentOrderExpirySweepIsRegisteredAsScheduledWork(t *testing.T) {
	handler := paymentOrderExpiryHandler{}
	assert.Equal(t, model.SystemTaskTypePaymentExpiry, handler.Type())
	assert.True(t, handler.Enabled())
	assert.Equal(t, time.Minute, handler.Interval())
}
