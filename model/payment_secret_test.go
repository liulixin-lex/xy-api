package model

import (
	"errors"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPaymentOptionEncryptionUsesAuthenticatedKeyBinding(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "test-payment-master-key-at-least-32-bytes")
	plaintext := "whsec_example_secret"
	encrypted, err := encryptPaymentOptionValue("StripeWebhookSecret", plaintext)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(encrypted, encryptedPaymentOptionPrefixV2))
	assert.NotContains(t, encrypted, plaintext)

	decrypted, err := decryptPaymentOptionValue("StripeWebhookSecret", encrypted)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)

	_, err = decryptPaymentOptionValue("StripeWebhookSecretPrevious", encrypted)
	assert.Error(t, err)
}

func TestEncryptedPaymentOptionRequiresDurableMasterKey(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "test-payment-master-key-at-least-32-bytes")
	encrypted, err := encryptPaymentOptionValue("XorPayAppSecret", "xorpay-secret")
	require.NoError(t, err)

	t.Setenv("PAYMENT_SECRET_KEY", "")
	t.Setenv("CRYPTO_SECRET", "")
	t.Setenv("SESSION_SECRET", "")
	_, err = decryptPaymentOptionValue("XorPayAppSecret", encrypted)
	assert.Error(t, err)
}

func TestEncryptedPaymentOptionSupportsExplicitKeyRotation(t *testing.T) {
	oldKey := "old-payment-master-key-at-least-32-bytes"
	newKey := "new-payment-master-key-at-least-32-bytes"
	t.Setenv("PAYMENT_SECRET_KEY", oldKey)
	encrypted, err := encryptPaymentOptionValue("EpayKey", "epay-rotation-secret")
	require.NoError(t, err)

	t.Setenv("PAYMENT_SECRET_KEY", newKey)
	t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", oldKey)
	decrypted, err := decryptPaymentOptionValue("EpayKey", encrypted)
	require.NoError(t, err)
	assert.Equal(t, "epay-rotation-secret", decrypted)
	assert.True(t, paymentOptionNeedsRewrap(encrypted))

	rewrapped, err := encryptPaymentOptionValue("EpayKey", decrypted)
	require.NoError(t, err)
	primary, ok := primaryPaymentSecretKey()
	require.True(t, ok)
	keyID, ok := paymentOptionV2KeyID(rewrapped)
	require.True(t, ok)
	assert.Equal(t, primary.id, keyID)
	assert.False(t, paymentOptionNeedsRewrap(rewrapped))
}

func TestPaymentSecretKeyringFingerprintBindsRotationFallbackSlot(t *testing.T) {
	primaryKey := "payment-keyring-primary-key-at-least-32-bytes"
	previousKey := "payment-keyring-previous-key-at-least-32-bytes"
	t.Setenv("PAYMENT_SECRET_KEY", primaryKey)
	t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", "")

	withoutPrevious := PaymentSecretKeyringFingerprint()
	require.Len(t, withoutPrevious, 32)
	assert.NotContains(t, withoutPrevious, primaryKey)
	assert.Equal(t, withoutPrevious, PaymentSecretKeyringFingerprint())

	t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", previousKey)
	withPrevious := PaymentSecretKeyringFingerprint()
	require.Len(t, withPrevious, 32)
	assert.NotEqual(t, withoutPrevious, withPrevious)
	assert.NotContains(t, withPrevious, primaryKey)
	assert.NotContains(t, withPrevious, previousKey)

	t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", "too-short")
	assert.Empty(t, PaymentSecretKeyringFingerprint())

	t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", "  "+primaryKey+"  ")
	assert.Empty(t, PaymentSecretKeyringFingerprint())
}

func TestPaymentOptionEncryptionTreatsEncryptedPrefixAsPlaintextInput(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "test-payment-master-key-at-least-32-bytes")
	plaintext := "enc:v2:not-a-stored-ciphertext"
	encrypted, err := encryptPaymentOptionValue("StripeApiSecret", plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, encrypted)
	decrypted, err := decryptPaymentOptionValue("StripeApiSecret", encrypted)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestPaymentOptionEncryptionRejectsMissingDedicatedKey(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "")
	t.Setenv("CRYPTO_SECRET", "legacy-crypto-secret-at-least-32-bytes")
	assert.False(t, PaymentSecretEncryptionReady())
	_, err := encryptPaymentOptionValue("StripeApiSecret", "sk_test_example")
	assert.Error(t, err)
}

func TestCompatibilityPaymentCredentialsUseEncryptedOptionStorage(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "compatibility-payment-secret-test-key-0001")
	for _, key := range []string{
		"CreemApiKey", "CreemWebhookSecret", "WaffoApiKey", "WaffoPrivateKey",
		"WaffoSandboxApiKey", "WaffoSandboxPrivateKey", "WaffoPancakePrivateKey",
	} {
		t.Run(key, func(t *testing.T) {
			assert.True(t, IsPaymentSecretOption(key))
			encrypted, err := encryptPaymentOptionValue(key, "compatibility-secret-value")
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(encrypted, encryptedPaymentOptionPrefixV2))
			assert.NotContains(t, encrypted, "compatibility-secret-value")
			decrypted, err := decryptPaymentOptionValue(key, encrypted)
			require.NoError(t, err)
			assert.Equal(t, "compatibility-secret-value", decrypted)
		})
	}
}

func TestPaymentOrderStartPayloadEncryptionIsTradeBound(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "payment-start-payload-test-key-at-least-32-bytes")
	payload := `{"flow":"hosted_redirect","url":"https://checkout.stripe.com/c/pay/cs_test_sensitive"}`
	encrypted, err := EncryptPaymentOrderStartPayload("PO_START_PAYLOAD_1", payload)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(encrypted, encryptedPaymentOptionPrefixV2))
	assert.NotContains(t, encrypted, "checkout.stripe.com")
	assert.NotContains(t, encrypted, "cs_test_sensitive")

	decrypted, err := DecryptPaymentOrderStartPayload("PO_START_PAYLOAD_1", encrypted)
	require.NoError(t, err)
	assert.Equal(t, payload, decrypted)
	_, err = DecryptPaymentOrderStartPayload("PO_START_PAYLOAD_2", encrypted)
	assert.Error(t, err)
}

func TestPaymentStartIdentityExpiryAndEncryptedPayloadCommitAtomically(t *testing.T) {
	truncateTables(t)
	t.Setenv("PAYMENT_SECRET_KEY", "payment-start-atomic-test-key-at-least-32-bytes")
	now := common.GetTimestamp()
	newOrder := func(tradeNo, requestID string) *PaymentOrder {
		return &PaymentOrder{
			TradeNo: tradeNo, UserID: 991913, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayNative,
			RequestID: requestID, ExpectedAmountMinor: 100, Currency: "CNY",
			RequestedAmount: 1, CreditQuota: 1, Status: PaymentOrderStatusProcessing,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		}
	}
	committed := newOrder("PO_START_ATOMIC_COMMIT", "start_atomic_commit")
	require.NoError(t, DB.Create(committed).Error)
	payload := `{"flow":"qr","qr_content":"weixin://wxpay/atomic"}`
	expiresAt := now + 3600
	require.NoError(t, SavePaymentOrderStartWithProviderIdentity(
		committed.TradeNo, "qr", payload, expiresAt, "xorpay:AOID_ATOMIC", "",
	))
	require.NoError(t, DB.First(committed, committed.ID).Error)
	require.NotNil(t, committed.ProviderOrderKey)
	assert.Equal(t, "xorpay:AOID_ATOMIC", *committed.ProviderOrderKey)
	assert.Equal(t, expiresAt, committed.ExpiresAt)
	assert.Equal(t, PaymentOrderStatusPending, committed.Status)
	assert.NotContains(t, committed.StartPayload, "wxpay")
	plaintext, err := DecryptPaymentOrderStartPayload(committed.TradeNo, committed.StartPayload)
	require.NoError(t, err)
	assert.Equal(t, payload, plaintext)

	rolledBack := newOrder("PO_START_ATOMIC_ROLLBACK", "start_atomic_rollback")
	require.NoError(t, DB.Create(rolledBack).Error)
	forcedErr := errors.New("forced start snapshot write failure")
	callbackName := "test:payment_start_atomic_rollback"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok {
			return
		}
		if _, writingPayload := updates["start_payload"]; writingPayload {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { require.NoError(t, DB.Callback().Update().Remove(callbackName)) })
	err = SavePaymentOrderStartWithProviderIdentity(
		rolledBack.TradeNo, "qr", payload, expiresAt, "xorpay:AOID_MUST_ROLL_BACK", "",
	)
	assert.ErrorIs(t, err, forcedErr)
	require.NoError(t, DB.First(rolledBack, rolledBack.ID).Error)
	assert.Nil(t, rolledBack.ProviderOrderKey)
	assert.Empty(t, rolledBack.StartPayload)
	assert.Zero(t, rolledBack.ExpiresAt)
	assert.Equal(t, PaymentOrderStatusProcessing, rolledBack.Status)
}

func TestPaymentProviderAuthorityKeysPreserveMaximumStripeObjectIDs(t *testing.T) {
	truncateTables(t)
	t.Setenv("PAYMENT_SECRET_KEY", "payment-authority-key-test-key-at-least-32-bytes")
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "PO_STRIPE_MAX_OBJECT_ID", UserID: 991914, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe,
		RequestID: "stripe_max_object_id", ExpectedAmountMinor: 100, Currency: "USD",
		RequestedAmount: 1, CreditQuota: 1, Status: PaymentOrderStatusProcessing,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)

	rawSessionID := "cs_" + strings.Repeat("s", 252)
	rawPaymentIntentID := "pi_" + strings.Repeat("p", 252)
	providerOrderKey := PaymentProviderStripe + ":" + rawSessionID
	providerPaymentKey := PaymentProviderStripe + ":" + rawPaymentIntentID
	require.Len(t, rawSessionID, 255)
	require.Len(t, rawPaymentIntentID, 255)
	require.LessOrEqual(t, len(providerOrderKey), PaymentProviderAuthorityKeyMaxLength)
	require.LessOrEqual(t, len(providerPaymentKey), PaymentProviderAuthorityKeyMaxLength)
	require.NoError(t, SavePaymentOrderStartWithProviderIdentity(
		order.TradeNo, "hosted_redirect", `{"url":"https://checkout.stripe.com/c/pay/max-id"}`,
		now+3600, providerOrderKey, providerPaymentKey,
	))

	var storedOrder PaymentOrder
	require.NoError(t, DB.First(&storedOrder, order.ID).Error)
	require.NotNil(t, storedOrder.ProviderOrderKey)
	require.NotNil(t, storedOrder.ProviderPaymentKey)
	assert.Equal(t, providerOrderKey, *storedOrder.ProviderOrderKey)
	assert.Equal(t, providerPaymentKey, *storedOrder.ProviderPaymentKey)

	providerResourceKey := PaymentProviderStripe + ":dp_" + strings.Repeat("d", 252)
	input := PaymentEventInput{
		Provider: PaymentProviderStripe, EventKey: "evt_max_object_id", EventType: "manual.review",
		TradeNo: order.TradeNo, ProviderOrderKey: providerOrderKey,
		ProviderPaymentKey: providerPaymentKey, ProviderResourceKey: providerResourceKey,
		ManualReview: true, NormalizedPayload: `{"reason":"boundary"}`,
	}
	require.NoError(t, input.validate())
	event := &PaymentEvent{
		Provider: input.Provider, EventKey: input.EventKey, EventType: input.EventType,
		TradeNo: input.TradeNo, ProviderOrderKey: input.ProviderOrderKey,
		ProviderPaymentKey: input.ProviderPaymentKey, ProviderResourceKey: input.ProviderResourceKey,
		ManualReview: true, PayloadDigest: PaymentPayloadDigest(input.NormalizedPayload),
		NormalizedPayload: input.NormalizedPayload, Status: PaymentEventStatusManualReview,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, DB.Create(event).Error)
	var storedEvent PaymentEvent
	require.NoError(t, DB.First(&storedEvent, event.ID).Error)
	assert.Equal(t, providerOrderKey, storedEvent.ProviderOrderKey)
	assert.Equal(t, providerPaymentKey, storedEvent.ProviderPaymentKey)
	assert.Equal(t, providerResourceKey, storedEvent.ProviderResourceKey)

	tooLong := strings.Repeat("x", PaymentProviderAuthorityKeyMaxLength+1)
	require.Error(t, SavePaymentOrderStartWithProviderIdentity(
		order.TradeNo, "hosted_redirect", `{"url":"https://checkout.stripe.com/c/pay/too-long"}`,
		now+3600, tooLong, "",
	))
	for _, mutate := range []func(*PaymentEventInput){
		func(candidate *PaymentEventInput) { candidate.ProviderOrderKey = tooLong },
		func(candidate *PaymentEventInput) { candidate.ProviderPaymentKey = tooLong },
		func(candidate *PaymentEventInput) { candidate.ProviderResourceKey = tooLong },
	} {
		candidate := input
		mutate(&candidate)
		require.Error(t, candidate.validate())
	}
}

func TestPaymentOrderStartPayloadsMigrateAndRewrapBeforePreviousKeyRemoval(t *testing.T) {
	truncateTables(t)
	oldKey := "old-payment-start-payload-key-at-least-32-bytes"
	newKey := "new-payment-start-payload-key-at-least-32-bytes"
	t.Setenv("PAYMENT_SECRET_KEY", oldKey)
	oldEncrypted, err := EncryptPaymentOrderStartPayload("PO_START_REWRAP_OLD", `{"url":"https://checkout.stripe.com/old"}`)
	require.NoError(t, err)
	now := common.GetTimestamp()
	orders := []PaymentOrder{
		{TradeNo: "PO_START_REWRAP_OLD", UserID: 991910, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderStripe,
			PaymentMethod: PaymentMethodStripe, RequestID: "start_rewrap_old", ExpectedAmountMinor: 100, Currency: "USD",
			RequestedAmount: 1, CreditQuota: 1, StartPayload: oldEncrypted, Status: PaymentOrderStatusPending,
			CreatedAt: now, UpdatedAt: now, Version: 1},
		{TradeNo: "PO_START_REWRAP_PLAINTEXT", UserID: 991911, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderEpay,
			PaymentMethod: "alipay", RequestID: "start_rewrap_plaintext", ExpectedAmountMinor: 100, Currency: "CNY",
			RequestedAmount: 1, CreditQuota: 1, StartPayload: `{"fields":{"sign":"legacy-known-signature"}}`, Status: PaymentOrderStatusProcessing,
			CreatedAt: now, UpdatedAt: now, Version: 1},
		{TradeNo: "PO_START_REWRAP_TERMINAL", UserID: 991912, OrderKind: PaymentOrderKindTopUp, Provider: PaymentProviderXorPay,
			PaymentMethod: PaymentMethodXorPayNative, RequestID: "start_rewrap_terminal", ExpectedAmountMinor: 100, Currency: "CNY",
			RequestedAmount: 1, CreditQuota: 1, StartPayload: `{"qr_content":"terminal-sensitive-qr"}`, Status: PaymentOrderStatusFulfilled,
			CreatedAt: now, UpdatedAt: now, Version: 1},
	}
	require.NoError(t, DB.Create(&orders).Error)

	t.Setenv("PAYMENT_SECRET_KEY", newKey)
	t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", oldKey)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return rewrapPaymentOrderStartPayloadsTx(tx)
	}))

	for _, tradeNo := range []string{"PO_START_REWRAP_OLD", "PO_START_REWRAP_PLAINTEXT"} {
		var stored PaymentOrder
		require.NoError(t, DB.Where("trade_no = ?", tradeNo).First(&stored).Error)
		keyID, encrypted := paymentOptionV2KeyID(stored.StartPayload)
		require.True(t, encrypted)
		primary, ok := primaryPaymentSecretKey()
		require.True(t, ok)
		assert.Equal(t, primary.id, keyID)
		t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", "")
		_, err := DecryptPaymentOrderStartPayload(stored.TradeNo, stored.StartPayload)
		require.NoError(t, err)
		t.Setenv("PAYMENT_SECRET_KEY_PREVIOUS", oldKey)
	}
	var terminal PaymentOrder
	require.NoError(t, DB.Where("trade_no = ?", "PO_START_REWRAP_TERMINAL").First(&terminal).Error)
	assert.Empty(t, terminal.StartPayload)
}
