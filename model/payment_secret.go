package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"gorm.io/gorm"
)

const (
	encryptedPaymentOptionPrefixV1 = "enc:v1:"
	encryptedPaymentOptionPrefixV2 = "enc:v2:"
)

var paymentSecretOptionKeys = map[string]struct{}{
	"EpayKey":                     {},
	"EpayIdPrevious":              {},
	"EpayKeyPrevious":             {},
	"StripeApiSecret":             {},
	"StripeWebhookSecret":         {},
	"StripeWebhookSecretPrevious": {},
	"XorPayAppSecret":             {},
	"XorPayAidPrevious":           {},
	"XorPayAppSecretPrevious":     {},
}

var paymentSecretStorageReadiness = struct {
	sync.RWMutex
	keyID string
	ready bool
}{}

func IsPaymentSecretOption(key string) bool {
	_, ok := paymentSecretOptionKeys[key]
	return ok
}

// PaymentSecretEncryptionReady means a dedicated, durable primary key exists.
// Legacy CRYPTO_SECRET/SESSION_SECRET values are decryption-only fallbacks and
// must never be used for newly written payment credentials.
func PaymentSecretEncryptionReady() bool {
	_, ok := primaryPaymentSecretKey()
	return ok
}

// PaymentSecretStorageReady additionally verifies that all stored payment
// credentials are encrypted and decryptable with the configured key set.
func PaymentSecretStorageReady() bool {
	primary, ok := primaryPaymentSecretKey()
	if !ok {
		return false
	}
	paymentSecretStorageReadiness.RLock()
	keyID := paymentSecretStorageReadiness.keyID
	ready := paymentSecretStorageReadiness.ready
	paymentSecretStorageReadiness.RUnlock()
	if keyID == primary.id {
		return ready
	}
	if err := refreshPaymentSecretStorageReadiness(); err != nil {
		return false
	}
	paymentSecretStorageReadiness.RLock()
	ready = paymentSecretStorageReadiness.ready && paymentSecretStorageReadiness.keyID == primary.id
	paymentSecretStorageReadiness.RUnlock()
	return ready
}

func refreshPaymentSecretStorageReadiness() error {
	primary, ok := primaryPaymentSecretKey()
	paymentSecretStorageReadiness.Lock()
	paymentSecretStorageReadiness.keyID = ""
	paymentSecretStorageReadiness.ready = false
	if ok {
		paymentSecretStorageReadiness.keyID = primary.id
	}
	paymentSecretStorageReadiness.Unlock()
	if !ok || DB == nil {
		return nil
	}
	var options []Option
	if err := DB.Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), secretOptionKeyList()).Find(&options).Error; err != nil {
		return err
	}
	for _, option := range options {
		if strings.TrimSpace(option.Value) == "" {
			continue
		}
		keyID, encrypted := paymentOptionV2KeyID(option.Value)
		if !encrypted || keyID != primary.id {
			return nil
		}
		if _, err := decryptPaymentOptionValue(option.Key, option.Value); err != nil {
			return err
		}
	}
	if DB.Migrator().HasTable(&PaymentOrder{}) {
		var orders []PaymentOrder
		if err := DB.Select("id", "trade_no", "start_payload").
			Where("status IN ? AND start_payload <> ?", []string{PaymentOrderStatusPending, PaymentOrderStatusProcessing}, "").
			Find(&orders).Error; err != nil {
			return err
		}
		for _, order := range orders {
			keyID, encrypted := paymentOptionV2KeyID(order.StartPayload)
			if !encrypted || keyID != primary.id {
				return nil
			}
			if _, err := DecryptPaymentOrderStartPayload(order.TradeNo, order.StartPayload); err != nil {
				return err
			}
		}
	}
	paymentSecretStorageReadiness.Lock()
	paymentSecretStorageReadiness.keyID = primary.id
	paymentSecretStorageReadiness.ready = true
	paymentSecretStorageReadiness.Unlock()
	return nil
}

func secretOptionKeyList() []string {
	keys := make([]string, 0, len(paymentSecretOptionKeys))
	for key := range paymentSecretOptionKeys {
		keys = append(keys, key)
	}
	return keys
}

func encryptPaymentOptionValue(key, value string) (string, error) {
	if !IsPaymentSecretOption(key) || value == "" {
		return value, nil
	}
	return encryptPaymentSensitiveValue(key, value)
}

func encryptPaymentSensitiveValue(purpose, value string) (string, error) {
	if strings.TrimSpace(purpose) == "" || value == "" {
		return value, nil
	}
	candidate, ok := primaryPaymentSecretKey()
	if !ok {
		return "", errors.New("PAYMENT_SECRET_KEY is required to store payment credentials")
	}
	block, err := aes.NewCipher(candidate.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(value), []byte(purpose))
	encoded := append(nonce, ciphertext...)
	return fmt.Sprintf("%s%s:%s", encryptedPaymentOptionPrefixV2, candidate.id, base64.RawStdEncoding.EncodeToString(encoded)), nil
}

func paymentOptionV2KeyID(value string) (string, bool) {
	if !strings.HasPrefix(value, encryptedPaymentOptionPrefixV2) {
		return "", false
	}
	rest := strings.TrimPrefix(value, encryptedPaymentOptionPrefixV2)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || len(parts[0]) != 16 || parts[1] == "" {
		return "", false
	}
	return parts[0], true
}

func paymentOptionNeedsRewrap(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	primary, ok := primaryPaymentSecretKey()
	if !ok {
		return false
	}
	keyID, encrypted := paymentOptionV2KeyID(value)
	return !encrypted || keyID != primary.id
}

func decryptPaymentOptionValue(key, value string) (string, error) {
	if !IsPaymentSecretOption(key) || value == "" {
		return value, nil
	}
	return decryptPaymentSensitiveValue(key, value)
}

func decryptPaymentSensitiveValue(purpose, value string) (string, error) {
	if strings.TrimSpace(purpose) == "" || value == "" {
		return value, nil
	}
	switch {
	case strings.HasPrefix(value, encryptedPaymentOptionPrefixV2):
		return decryptPaymentOptionV2(purpose, value)
	case strings.HasPrefix(value, encryptedPaymentOptionPrefixV1):
		return decryptPaymentOptionV1(purpose, value)
	default:
		// Plaintext is retained only for backwards compatibility with old
		// installations; the next successful load with PAYMENT_SECRET_KEY
		// migrates it to v2.
		return value, nil
	}
}

func EncryptPaymentOrderStartPayload(tradeNo, payload string) (string, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" || payload == "" {
		return "", errors.New("invalid payment start payload")
	}
	return encryptPaymentSensitiveValue("PaymentOrder.StartPayload:"+tradeNo, payload)
}

func DecryptPaymentOrderStartPayload(tradeNo, payload string) (string, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" || payload == "" {
		return "", errors.New("invalid payment start payload")
	}
	return decryptPaymentSensitiveValue("PaymentOrder.StartPayload:"+tradeNo, payload)
}

func rewrapPaymentOrderStartPayloadsTx(tx *gorm.DB) error {
	if tx == nil || !tx.Migrator().HasTable(&PaymentOrder{}) {
		return nil
	}
	if _, ok := primaryPaymentSecretKey(); !ok {
		return nil
	}
	if err := tx.Model(&PaymentOrder{}).
		Where("status NOT IN ? AND start_payload <> ?", []string{PaymentOrderStatusPending, PaymentOrderStatusProcessing}, "").
		Update("start_payload", "").Error; err != nil {
		return err
	}
	var orders []PaymentOrder
	if err := tx.Select("id", "trade_no", "start_payload").
		Where("status IN ? AND start_payload <> ?", []string{PaymentOrderStatusPending, PaymentOrderStatusProcessing}, "").
		Find(&orders).Error; err != nil {
		return err
	}
	primary, _ := primaryPaymentSecretKey()
	for _, order := range orders {
		keyID, currentEncryption := paymentOptionV2KeyID(order.StartPayload)
		if currentEncryption && keyID == primary.id {
			continue
		}
		plaintext, err := DecryptPaymentOrderStartPayload(order.TradeNo, order.StartPayload)
		if err != nil {
			return fmt.Errorf("decrypt payment order start payload %d: %w", order.ID, err)
		}
		encrypted, err := EncryptPaymentOrderStartPayload(order.TradeNo, plaintext)
		if err != nil {
			return fmt.Errorf("rewrap payment order start payload %d: %w", order.ID, err)
		}
		result := tx.Model(&PaymentOrder{}).
			Where("id = ? AND start_payload = ? AND status IN ?", order.ID, order.StartPayload,
				[]string{PaymentOrderStatusPending, PaymentOrderStatusProcessing}).
			Update("start_payload", encrypted)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: payment order start payload %d", errPaymentOptionsChangedDuringReload, order.ID)
		}
	}
	return nil
}

type paymentSecretKey struct {
	id  string
	key []byte
}

func primaryPaymentSecretKey() (paymentSecretKey, bool) {
	secret := strings.TrimSpace(os.Getenv("PAYMENT_SECRET_KEY"))
	if len(secret) < 32 {
		return paymentSecretKey{}, false
	}
	return makePaymentSecretKey(secret), true
}

func paymentSecretKeyCandidates(includeLegacy bool) []paymentSecretKey {
	keys := make([]paymentSecretKey, 0, 4)
	seen := make(map[string]struct{})
	appendSecret := func(secret string) {
		secret = strings.TrimSpace(secret)
		if len(secret) < 32 {
			return
		}
		candidate := makePaymentSecretKey(secret)
		if _, exists := seen[candidate.id]; exists {
			return
		}
		seen[candidate.id] = struct{}{}
		keys = append(keys, candidate)
	}
	appendSecret(os.Getenv("PAYMENT_SECRET_KEY"))
	appendSecret(os.Getenv("PAYMENT_SECRET_KEY_PREVIOUS"))
	if includeLegacy {
		appendSecret(os.Getenv("CRYPTO_SECRET"))
		appendSecret(os.Getenv("SESSION_SECRET"))
	}
	return keys
}

func makePaymentSecretKey(secret string) paymentSecretKey {
	digest := sha256.Sum256([]byte(secret))
	return paymentSecretKey{id: fmt.Sprintf("%x", digest[:8]), key: digest[:]}
}

func decryptPaymentOptionV2(key, value string) (string, error) {
	rest := strings.TrimPrefix(value, encryptedPaymentOptionPrefixV2)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || len(parts[0]) != 16 {
		return "", errors.New("invalid encrypted payment option")
	}
	raw, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid encrypted payment option")
	}
	for _, candidate := range paymentSecretKeyCandidates(true) {
		if candidate.id != parts[0] {
			continue
		}
		if plaintext, openErr := openPaymentSecret(candidate.key, key, raw); openErr == nil {
			return plaintext, nil
		}
	}
	return "", errors.New("failed to decrypt payment option")
}

func decryptPaymentOptionV1(key, value string) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, encryptedPaymentOptionPrefixV1))
	if err != nil {
		return "", errors.New("invalid encrypted payment option")
	}
	for _, candidate := range paymentSecretKeyCandidates(true) {
		if plaintext, openErr := openPaymentSecret(candidate.key, key, raw); openErr == nil {
			return plaintext, nil
		}
	}
	return "", errors.New("failed to decrypt payment option")
}

func openPaymentSecret(masterKey []byte, key string, raw []byte) (string, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(raw) <= gcm.NonceSize() {
		return "", errors.New("invalid encrypted payment option")
	}
	plaintext, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(key))
	if err != nil {
		return "", errors.New("failed to decrypt payment option")
	}
	return string(plaintext), nil
}
