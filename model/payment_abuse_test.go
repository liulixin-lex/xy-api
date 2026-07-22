package model

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGeneratePaymentTradeNoPreservesProtectedLegacyReferencePrefix(t *testing.T) {
	fixedTime := time.Date(2026, time.July, 22, 10, 11, 12, 0, time.UTC)
	first, err := generatePaymentTradeNo(fixedTime, bytes.NewReader(make([]byte, 10)))
	require.NoError(t, err)
	second, err := generatePaymentTradeNo(fixedTime.Add(time.Second), bytes.NewReader(make([]byte, 10)))
	require.NoError(t, err)

	assert.Equal(t, "new-api-ref-20260722T101112Z-0000000000000000", first)
	assert.Len(t, first, len(paymentTradeNoPrefix)+33)
	assert.Less(t, first, second, "the UTC timestamp keeps new references time-sortable")
	for _, character := range strings.TrimPrefix(first, paymentTradeNoPrefix) {
		assert.True(t,
			character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character == '-',
			"unexpected trade number character %q", character,
		)
	}
}

func TestGeneratePaymentTradeNoIsConcurrencySafe(t *testing.T) {
	const count = 16
	fixedTime := time.Date(2026, time.July, 22, 10, 11, 12, 0, time.UTC)
	tradeNumbers := make(chan string, count)
	errorsByGenerator := make(chan error, count)
	var waitGroup sync.WaitGroup
	for index := 0; index < count; index++ {
		waitGroup.Add(1)
		go func(entropyByte byte) {
			defer waitGroup.Done()
			tradeNo, err := generatePaymentTradeNo(
				fixedTime,
				bytes.NewReader(bytes.Repeat([]byte{entropyByte}, 10)),
			)
			if err != nil {
				errorsByGenerator <- err
				return
			}
			tradeNumbers <- tradeNo
		}(byte(index + 1))
	}
	waitGroup.Wait()
	close(tradeNumbers)
	close(errorsByGenerator)

	for err := range errorsByGenerator {
		require.NoError(t, err)
	}
	seen := make(map[string]struct{}, count)
	for tradeNo := range tradeNumbers {
		assert.Len(t, tradeNo, len(paymentTradeNoPrefix)+33)
		assert.True(t, strings.HasPrefix(tradeNo, paymentTradeNoPrefix+"20260722T101112Z-"))
		_, duplicate := seen[tradeNo]
		assert.False(t, duplicate, "duplicate concurrent trade number %q", tradeNo)
		seen[tradeNo] = struct{}{}
	}
	assert.Len(t, seen, count)
}

func TestCreatePaymentOrderRetriesTradeNumberCollision(t *testing.T) {
	truncateTables(t)
	const collision = "new-api-ref-20260722T101112Z-0000000000000000"
	const unique = "new-api-ref-20260722T101112Z-0000000000000001"
	require.NoError(t, DB.Create(&PaymentOrder{TradeNo: collision, UserID: 980001, RequestID: "existing"}).Error)

	generated := []string{collision, unique}
	index := 0
	order := &PaymentOrder{UserID: 980002, RequestID: "new"}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return createPaymentOrderWithUniqueTradeNoTx(tx, order, func() (string, error) {
			tradeNo := generated[index]
			index++
			return tradeNo, nil
		})
	}))
	assert.Equal(t, unique, order.TradeNo)
	assert.NotZero(t, order.ID)
}

func createAbuseTestQuote(t *testing.T, userID int, provider string, amount int64, expiresAt int64) *PaymentQuote {
	t.Helper()
	quoteID, err := GeneratePaymentQuoteID()
	require.NoError(t, err)
	quote := &PaymentQuote{
		QuoteID: quoteID, UserID: userID, OrderKind: PaymentOrderKindTopUp,
		Provider: provider, PaymentMethod: "alipay", RequestedAmount: amount,
		CreditQuota: amount * 500, ExpectedAmountMinor: amount * 730, Currency: "CNY",
		PricingSnapshot: fmt.Sprintf(`{"amount":%d}`, amount), ExpiresAt: expiresAt,
	}
	require.NoError(t, CreatePaymentQuote(quote))
	return quote
}

func createStripeAbuseTestQuote(t *testing.T, userID int, amount int64, expiresAt int64) *PaymentQuote {
	t.Helper()
	quoteID, err := GeneratePaymentQuoteID()
	require.NoError(t, err)
	quote := &PaymentQuote{
		QuoteID: quoteID, UserID: userID, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, ProviderLivemode: livePaymentModeForTest(),
		RequestedAmount: amount, CreditQuota: amount * 500, ExpectedAmountMinor: amount * 100, Currency: "USD",
		PricingSnapshot: fmt.Sprintf(`{"amount":%d}`, amount), ExpiresAt: expiresAt,
	}
	require.NoError(t, CreatePaymentQuote(quote))
	return quote
}

func TestPaymentQuoteCapacityIsPerUserAndEnforcesBoundary(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	const firstUser = 974101
	for index := 0; index < PaymentMaxActiveQuotesPerUser; index++ {
		createAbuseTestQuote(t, firstUser, PaymentProviderEpay, int64(index+1), now+3600)
	}

	quoteID, err := GeneratePaymentQuoteID()
	require.NoError(t, err)
	err = CreatePaymentQuote(&PaymentQuote{
		QuoteID: quoteID, UserID: firstUser, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestedAmount: 99,
		CreditQuota: 49500, ExpectedAmountMinor: 72270, Currency: "CNY",
		PricingSnapshot: `{"amount":99}`, ExpiresAt: now + 3600,
	})
	assert.ErrorIs(t, err, ErrPaymentActiveQuoteLimit)

	otherUserQuote := createAbuseTestQuote(t, 974102, PaymentProviderEpay, 1, now+3600)
	assert.NotZero(t, otherUserQuote.ID)
}

func TestPaymentQuoteCleanupRetainsShortAuditWindow(t *testing.T) {
	truncateTables(t)
	const userID = 974103
	now := time.Now().Unix()
	cutoff := now - PaymentQuoteAuditRetentionSeconds
	quotes := []*PaymentQuote{
		{QuoteID: "Q_CLEAN_OLD_CONSUMED", UserID: userID, ConsumedAt: cutoff - 1, ExpiresAt: now + 100, CreatedAt: cutoff - 100},
		{QuoteID: "Q_CLEAN_OLD_EXPIRED", UserID: userID, ExpiresAt: cutoff - 1, CreatedAt: cutoff - 100},
		{QuoteID: "Q_KEEP_RECENT_CONSUMED", UserID: userID, ConsumedAt: cutoff + 1, ExpiresAt: now + 100, CreatedAt: cutoff},
		{QuoteID: "Q_KEEP_RECENT_EXPIRED", UserID: userID, ExpiresAt: cutoff + 1, CreatedAt: cutoff},
	}
	for _, quote := range quotes {
		require.NoError(t, DB.Create(quote).Error)
	}
	createAbuseTestQuote(t, userID, PaymentProviderEpay, 1, now+3600)

	for _, quoteID := range []string{"Q_CLEAN_OLD_CONSUMED", "Q_CLEAN_OLD_EXPIRED"} {
		var quote PaymentQuote
		err := DB.Where("quote_id = ?", quoteID).First(&quote).Error
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	}
	for _, quoteID := range []string{"Q_KEEP_RECENT_CONSUMED", "Q_KEEP_RECENT_EXPIRED"} {
		var count int64
		require.NoError(t, DB.Model(&PaymentQuote{}).Where("quote_id = ?", quoteID).Count(&count).Error)
		assert.EqualValues(t, 1, count)
	}
}

func TestPaymentOrderCapacityIsPerUserAndProviderAndRetriesDoNotCount(t *testing.T) {
	truncateTables(t)
	const userID = 974104
	seedPaymentUser(t, userID, 0)
	seedPaymentUser(t, 974105, 0)
	now := time.Now().Unix()
	orders := make([]*PaymentOrder, 0, PaymentMaxInFlightOrdersPerUserProvider)
	for index := 0; index < PaymentMaxInFlightOrdersPerUserProvider; index++ {
		quote := createAbuseTestQuote(t, userID, PaymentProviderEpay, int64(index+1), now+3600)
		order, err := CreatePaymentOrderFromQuote(userID, quote.QuoteID, fmt.Sprintf("capacity-request-%d", index))
		require.NoError(t, err)
		orders = append(orders, order)
	}

	retry, err := CreatePaymentOrderFromQuote(userID, orders[0].QuoteID, orders[0].RequestID)
	require.NoError(t, err)
	assert.Equal(t, orders[0].ID, retry.ID)

	blockedQuote := createAbuseTestQuote(t, userID, PaymentProviderEpay, 99, now+3600)
	_, err = CreatePaymentOrderFromQuote(userID, blockedQuote.QuoteID, "capacity-blocked")
	assert.ErrorIs(t, err, ErrPaymentInFlightOrderLimit)

	otherProviderQuote := createAbuseTestQuote(t, userID, PaymentProviderXorPay, 1, now+3600)
	otherProviderQuote.PaymentMethod = PaymentMethodXorPayNative
	require.NoError(t, DB.Model(otherProviderQuote).Update("payment_method", PaymentMethodXorPayNative).Error)
	_, err = CreatePaymentOrderFromQuote(userID, otherProviderQuote.QuoteID, "capacity-other-provider")
	require.NoError(t, err)

	otherUserQuote := createAbuseTestQuote(t, 974105, PaymentProviderEpay, 1, now+3600)
	_, err = CreatePaymentOrderFromQuote(974105, otherUserQuote.QuoteID, "capacity-other-user")
	require.NoError(t, err)

	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", orders[0].ID).Update("status", PaymentOrderStatusFailed).Error)
	replacement, err := CreatePaymentOrderFromQuote(userID, blockedQuote.QuoteID, "capacity-blocked")
	require.NoError(t, err)
	assert.NotZero(t, replacement.ID)
}

func TestConcurrentEquivalentQuotesWithSameRequestReturnOneOrder(t *testing.T) {
	truncateTables(t)
	const userID = 974106
	seedPaymentUser(t, userID, 0)
	now := time.Now().Unix()
	firstQuote := createAbuseTestQuote(t, userID, PaymentProviderEpay, 10, now+3600)
	secondQuote := createAbuseTestQuote(t, userID, PaymentProviderEpay, 10, now+3601)

	orders := make([]*PaymentOrder, 2)
	errorsByIndex := make([]error, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index, quote := range []*PaymentQuote{firstQuote, secondQuote} {
		wait.Add(1)
		go func(index int, quoteID string) {
			defer wait.Done()
			<-start
			orders[index], errorsByIndex[index] = CreatePaymentOrderFromQuote(userID, quoteID, "concurrent-compatible-request")
		}(index, quote.QuoteID)
	}
	close(start)
	wait.Wait()

	require.NoError(t, errorsByIndex[0])
	require.NoError(t, errorsByIndex[1])
	require.NotNil(t, orders[0])
	require.NotNil(t, orders[1])
	assert.Equal(t, orders[0].ID, orders[1].ID)
	assert.Equal(t, orders[0].TradeNo, orders[1].TradeNo)

	var orderCount, projectionCount, consumedQuoteCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("user_id = ? AND request_id = ?", userID, "concurrent-compatible-request").Count(&orderCount).Error)
	require.NoError(t, DB.Model(&TopUp{}).Where("user_id = ?", userID).Count(&projectionCount).Error)
	require.NoError(t, DB.Model(&PaymentQuote{}).Where("quote_id IN ? AND consumed_at > 0", []string{firstQuote.QuoteID, secondQuote.QuoteID}).Count(&consumedQuoteCount).Error)
	assert.EqualValues(t, 1, orderCount)
	assert.EqualValues(t, 1, projectionCount)
	assert.EqualValues(t, 2, consumedQuoteCount)
}

func TestConcurrentStripeOrdersWithoutCustomerCreateOnlyOneOrder(t *testing.T) {
	truncateTables(t)
	const userID = 974150
	seedPaymentUser(t, userID, 100)
	now := time.Now().Unix()
	firstQuote := createStripeAbuseTestQuote(t, userID, 10, now+3600)
	secondQuote := createStripeAbuseTestQuote(t, userID, 20, now+3600)

	orders := make([]*PaymentOrder, 2)
	errorsByIndex := make([]error, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index, quote := range []*PaymentQuote{firstQuote, secondQuote} {
		wait.Add(1)
		go func(index int, quoteID string) {
			defer wait.Done()
			<-start
			orders[index], errorsByIndex[index] = CreatePaymentOrderFromQuote(userID, quoteID, fmt.Sprintf("stripe-unbound-%d", index))
		}(index, quote.QuoteID)
	}
	close(start)
	wait.Wait()

	successes := 0
	limited := 0
	var remainingQuote *PaymentQuote
	for index, err := range errorsByIndex {
		if err == nil {
			successes++
			require.NotNil(t, orders[index])
			continue
		}
		if errors.Is(err, ErrPaymentInFlightOrderLimit) {
			limited++
			if index == 0 {
				remainingQuote = firstQuote
			} else {
				remainingQuote = secondQuote
			}
			continue
		}
		require.NoError(t, err)
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, limited)
	require.NotNil(t, remainingQuote)

	var orderCount, consumedQuoteCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("user_id = ? AND provider = ?", userID, PaymentProviderStripe).Count(&orderCount).Error)
	require.NoError(t, DB.Model(&PaymentQuote{}).Where("quote_id IN ? AND consumed_at > 0", []string{firstQuote.QuoteID, secondQuote.QuoteID}).Count(&consumedQuoteCount).Error)
	assert.EqualValues(t, 1, orderCount)
	assert.EqualValues(t, 1, consumedQuoteCount)

	seedStripeCustomerBinding(t, userID, "cus_stripe_order_capacity")
	replacement, err := CreatePaymentOrderFromQuote(userID, remainingQuote.QuoteID, "stripe-after-binding")
	require.NoError(t, err)
	assert.NotZero(t, replacement.ID)
}

func TestExpireDuePaymentOrdersSynchronizesProjectionAndAllowsLatePaid(t *testing.T) {
	truncateTables(t)
	const userID = 974107
	seedPaymentUser(t, userID, 100)
	order := createTopUpPaymentOrder(t, userID, PaymentProviderEpay, "alipay", 7300, 5000)
	now := time.Now().Unix()
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
		"status": PaymentOrderStatusProcessing, "expires_at": now - 1,
		"start_flow": "form_post", "start_payload": "encrypted-test-payload",
	}).Error)

	result, err := ExpireDuePaymentOrders(t.Context(), now, 1)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderExpirySweepResult{Scanned: 1, Expired: 1}, result)

	require.NoError(t, DB.First(order, order.ID).Error)
	assert.Equal(t, PaymentOrderStatusExpired, order.Status)
	assert.Empty(t, order.StartFlow)
	assert.Empty(t, order.StartPayload)
	var projection TopUp
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
	assert.Equal(t, common.TopUpStatusExpired, projection.Status)

	repeat, err := ExpireDuePaymentOrders(t.Context(), now, 10)
	require.NoError(t, err)
	assert.Zero(t, repeat.Scanned)
	assert.Zero(t, repeat.Expired)

	settlement, err := ProcessPaymentEvent(paidPaymentEvent(order, "late-paid-after-sweep"))
	require.NoError(t, err)
	require.NotNil(t, settlement.Order)
	assert.Equal(t, PaymentOrderStatusFulfilled, settlement.Order.Status)
	var user User
	require.NoError(t, DB.First(&user, userID).Error)
	assert.Equal(t, 5100, user.Quota)
}

func TestExpireDuePaymentOrdersHonorsDueBoundaryAndBatchSize(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	orders := []*PaymentOrder{
		{TradeNo: "PO_SWEEP_DUE_BOUNDARY", UserID: 974108, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "sweep-due", Status: PaymentOrderStatusPending, ExpiresAt: now, CreatedAt: now - 100, UpdatedAt: now - 100, Version: 1},
		{TradeNo: "PO_SWEEP_OVERDUE", UserID: 974108, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "sweep-overdue", Status: PaymentOrderStatusProcessing, ExpiresAt: now - 1, CreatedAt: now - 100, UpdatedAt: now - 100, Version: 1},
		{TradeNo: "PO_SWEEP_FUTURE", UserID: 974108, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "sweep-future", Status: PaymentOrderStatusPending, ExpiresAt: now + 1, CreatedAt: now - 100, UpdatedAt: now - 100, Version: 1},
		{TradeNo: "PO_SWEEP_TERMINAL", UserID: 974108, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "sweep-terminal", Status: PaymentOrderStatusFulfilled, ExpiresAt: now - 1, CreatedAt: now - 100, UpdatedAt: now - 100, Version: 1},
	}
	for _, order := range orders {
		require.NoError(t, DB.Create(order).Error)
		require.NoError(t, DB.Create(&TopUp{PaymentOrderId: &order.ID, UserId: order.UserID, TradeNo: order.TradeNo, PaymentMethod: order.PaymentMethod, PaymentProvider: order.Provider, Status: common.TopUpStatusPending, CreateTime: now - 100}).Error)
	}

	first, err := ExpireDuePaymentOrders(t.Context(), now, 1)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderExpirySweepResult{Scanned: 1, Expired: 1}, first)
	second, err := ExpireDuePaymentOrders(t.Context(), now, 1)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderExpirySweepResult{Scanned: 1, Expired: 1}, second)
	third, err := ExpireDuePaymentOrders(t.Context(), now, 10)
	require.NoError(t, err)
	assert.Zero(t, third.Scanned)

	var future, terminal PaymentOrder
	require.NoError(t, DB.Where("trade_no = ?", "PO_SWEEP_FUTURE").First(&future).Error)
	require.NoError(t, DB.Where("trade_no = ?", "PO_SWEEP_TERMINAL").First(&terminal).Error)
	assert.Equal(t, PaymentOrderStatusPending, future.Status)
	assert.Equal(t, PaymentOrderStatusFulfilled, terminal.Status)
}
