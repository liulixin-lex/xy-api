package service

import (
	"errors"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
)

func BeginWeChatPaymentAuthorization(userID int, tradeNo string) (string, error) {
	if err := EnsurePaymentClusterReady(); err != nil {
		return "", err
	}
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		return "", err
	}
	unlock := setting.LockPaymentConfigurationForRead()
	defer unlock()
	if !setting.IsXorPayMethodEnabled(setting.XorPayMethodJSAPI) {
		return "", ErrPublicPaymentRouteNotFound
	}
	order, err := model.GetPaymentOrderForUser(userID, strings.TrimSpace(tradeNo))
	if err != nil {
		return "", err
	}
	if order.Provider != model.PaymentProviderXorPay || order.PaymentMethod != model.PaymentMethodXorPayJSAPI {
		return "", model.ErrPaymentBrowserAuthorizationInvalid
	}
	configurationVersion, err := model.CurrentPaymentConfigurationVersion()
	if err != nil {
		return "", err
	}
	if order.ConfigurationVersion <= 0 || order.ConfigurationVersion != configurationVersion {
		return "", model.ErrPaymentBrowserAuthorizationInvalid
	}
	if order.ProviderCredentialGeneration == 0 {
		generation := setting.XorPayCredentialGeneration
		if generation <= 0 {
			return "", model.ErrPaymentBrowserAuthorizationInvalid
		}
		if err := model.BindPaymentOrderCredentialGeneration(order.TradeNo, generation, configurationVersion); err != nil {
			return "", err
		}
		order.ProviderCredentialGeneration = generation
	} else {
		available, availabilityErr := model.PaymentCredentialGenerationAvailable(
			order.Provider, order.ProviderCredentialGeneration, order.CreatedAt,
		)
		if availabilityErr != nil {
			return "", availabilityErr
		}
		if !available {
			return "", model.ErrPaymentBrowserAuthorizationInvalid
		}
	}
	credential, err := xorPayCredentialForOrder(order)
	if err != nil {
		return "", err
	}
	callbackOrigin := strings.TrimRight(GetPaymentCallbackAddress(), "/")
	if err := ValidatePaymentCallbackOrigin(callbackOrigin, true); err != nil {
		return "", err
	}
	state, err := common.GenerateRandomCharsKey(48)
	if err != nil {
		return "", err
	}
	if _, err := model.BeginPaymentBrowserAuthorization(userID, order.TradeNo, state); err != nil {
		return "", err
	}
	callbackURL := callbackOrigin + "/api/payment/wechat/authorize/callback?state=" + url.QueryEscape(state)
	endpoint := xorPayBaseURL + "/api/openid/" + url.PathEscape(credential.aid) + "?callback=" + url.QueryEscape(callbackURL)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "xorpay.com" || parsed.User != nil {
		return "", errors.New("invalid payment authorization endpoint")
	}
	return parsed.String(), nil
}

func CompleteWeChatPaymentAuthorization(state, openID string) (*model.PaymentOrder, error) {
	if err := EnsurePaymentClusterReady(); err != nil {
		return nil, err
	}
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		return nil, err
	}
	unlock := setting.LockPaymentConfigurationForRead()
	defer unlock()
	order, err := model.CompletePaymentBrowserAuthorization(state, openID)
	if err != nil {
		return nil, err
	}
	if _, err := model.EnsurePaymentTask(order.ID, model.PaymentTaskOperationCreate, common.GetTimestamp()); err != nil {
		return order, err
	}
	if err := model.WakePaymentCreateTask(order.ID); err != nil {
		return order, err
	}
	notifyPaymentTaskRunner()
	return order, nil
}
