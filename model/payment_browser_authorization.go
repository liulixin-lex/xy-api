package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const paymentBrowserAuthorizationTTL = 10 * time.Minute

var (
	ErrPaymentBrowserAuthorizationInvalid  = errors.New("payment browser authorization is invalid")
	ErrPaymentBrowserAuthorizationRequired = errors.New("payment browser authorization is required")
)

func paymentBrowserAuthorizationDigest(state string) (string, error) {
	state = strings.TrimSpace(state)
	if len(state) < 32 || len(state) > 256 {
		return "", ErrPaymentBrowserAuthorizationInvalid
	}
	for _, character := range state {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return "", ErrPaymentBrowserAuthorizationInvalid
	}
	digest := sha256.Sum256([]byte(state))
	return hex.EncodeToString(digest[:]), nil
}

// BeginPaymentBrowserAuthorization binds a one-use high-entropy browser state
// to an authenticated pending JSAPI order. Only the digest is persisted, so a
// database read cannot be used to forge the provider callback.
func BeginPaymentBrowserAuthorization(userID int, tradeNo, state string) (*PaymentOrder, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if userID <= 0 || tradeNo == "" {
		return nil, ErrPaymentBrowserAuthorizationInvalid
	}
	digest, err := paymentBrowserAuthorizationDigest(state)
	if err != nil {
		return nil, err
	}
	now := common.GetTimestamp()
	var order PaymentOrder
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("trade_no = ? AND user_id = ?", tradeNo, userID).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentOrderNotFound
			}
			return err
		}
		if order.Provider != PaymentProviderXorPay || order.PaymentMethod != PaymentMethodXorPayJSAPI ||
			(order.Status != PaymentOrderStatusPending && order.Status != PaymentOrderStatusProcessing) ||
			order.StartPayload != "" || order.ProviderOrderKey != nil ||
			order.BrowserAuthorizationPayload != "" || order.BrowserAuthorizedAt != 0 ||
			(order.ExpiresAt > 0 && order.ExpiresAt <= now) {
			return ErrPaymentBrowserAuthorizationInvalid
		}
		expiresAt := now + int64(paymentBrowserAuthorizationTTL/time.Second)
		if order.ExpiresAt > 0 && expiresAt > order.ExpiresAt {
			expiresAt = order.ExpiresAt
		}
		result := tx.Model(&PaymentOrder{}).
			Where("id = ? AND version = ? AND provider = ? AND payment_method = ?", order.ID, order.Version,
				PaymentProviderXorPay, PaymentMethodXorPayJSAPI).
			Where("status IN ? AND start_payload = ? AND provider_order_key IS NULL",
				[]string{PaymentOrderStatusPending, PaymentOrderStatusProcessing}, "").
			Where("browser_authorization_payload = ? AND browser_authorized_at = ?", "", 0).
			Where("(expires_at = ? OR expires_at > ?)", 0, now).
			Updates(map[string]interface{}{
				"browser_authorization_digest":     digest,
				"browser_authorization_payload":    "",
				"browser_authorization_expires_at": expiresAt,
				"browser_authorized_at":            0,
				"updated_at":                       now,
				"version":                          gorm.Expr("version + ?", 1),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPaymentBrowserAuthorizationInvalid
		}
		order.BrowserAuthorizationDigest = &digest
		order.BrowserAuthorizationPayload = ""
		order.BrowserAuthorizationExpiresAt = expiresAt
		order.BrowserAuthorizedAt = 0
		order.UpdatedAt = now
		order.Version++
		return nil
	})
	return &order, err
}

// CompletePaymentBrowserAuthorization consumes the callback state exactly once
// and stores the OpenID encrypted with the payment key. The plaintext never
// belongs in logs, API responses, browser storage, or payment history.
func CompletePaymentBrowserAuthorization(state, openID string) (*PaymentOrder, error) {
	digest, err := paymentBrowserAuthorizationDigest(state)
	if err != nil {
		return nil, err
	}
	openID = strings.TrimSpace(openID)
	if len(openID) < 8 || len(openID) > 128 {
		return nil, ErrPaymentBrowserAuthorizationInvalid
	}
	for _, character := range openID {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return nil, ErrPaymentBrowserAuthorizationInvalid
	}
	now := common.GetTimestamp()
	var order PaymentOrder
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("browser_authorization_digest = ?", digest).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentBrowserAuthorizationInvalid
			}
			return err
		}
		if order.Provider != PaymentProviderXorPay || order.PaymentMethod != PaymentMethodXorPayJSAPI ||
			(order.Status != PaymentOrderStatusPending && order.Status != PaymentOrderStatusProcessing) ||
			order.StartPayload != "" || order.ProviderOrderKey != nil ||
			order.BrowserAuthorizationExpiresAt <= now ||
			(order.ExpiresAt > 0 && order.ExpiresAt <= now) {
			return ErrPaymentBrowserAuthorizationInvalid
		}
		encrypted, err := EncryptPaymentOrderBrowserAuthorization(order.TradeNo, openID)
		if err != nil {
			return err
		}
		result := tx.Model(&PaymentOrder{}).
			Where("id = ? AND version = ? AND browser_authorization_digest = ?", order.ID, order.Version, digest).
			Where("provider = ? AND payment_method = ?", PaymentProviderXorPay, PaymentMethodXorPayJSAPI).
			Where("status IN ? AND start_payload = ? AND provider_order_key IS NULL",
				[]string{PaymentOrderStatusPending, PaymentOrderStatusProcessing}, "").
			Where("browser_authorization_payload = ? AND browser_authorization_expires_at > ?", "", now).
			Where("(expires_at = ? OR expires_at > ?)", 0, now).
			Updates(map[string]interface{}{
				"browser_authorization_digest":     nil,
				"browser_authorization_payload":    encrypted,
				"browser_authorization_expires_at": 0,
				"browser_authorized_at":            now,
				"updated_at":                       now,
				"version":                          gorm.Expr("version + ?", 1),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPaymentBrowserAuthorizationInvalid
		}
		order.BrowserAuthorizationDigest = nil
		order.BrowserAuthorizationPayload = encrypted
		order.BrowserAuthorizationExpiresAt = 0
		order.BrowserAuthorizedAt = now
		order.UpdatedAt = now
		order.Version++
		return nil
	})
	return &order, err
}

func PaymentOrderBrowserAuthorization(order *PaymentOrder) (string, error) {
	if order == nil || order.Provider != PaymentProviderXorPay || order.PaymentMethod != PaymentMethodXorPayJSAPI {
		return "", ErrPaymentBrowserAuthorizationInvalid
	}
	if strings.TrimSpace(order.BrowserAuthorizationPayload) == "" {
		return "", ErrPaymentBrowserAuthorizationRequired
	}
	return DecryptPaymentOrderBrowserAuthorization(order.TradeNo, order.BrowserAuthorizationPayload)
}

func WakePaymentCreateTask(paymentOrderID int64) error {
	if paymentOrderID <= 0 {
		return ErrPaymentOrderNotFound
	}
	now := common.GetTimestamp()
	return DB.Model(&PaymentTask{}).
		Where("payment_order_id = ? AND operation = ? AND status IN ?", paymentOrderID,
			PaymentTaskOperationCreate, []string{PaymentTaskStatusPending, PaymentTaskStatusRetryWait}).
		Updates(map[string]interface{}{
			"status":          PaymentTaskStatusPending,
			"available_at":    now,
			"last_error_code": "",
			"last_error":      "",
			"updated_at":      now,
		}).Error
}
