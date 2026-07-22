package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseWaffoPancakeEnvironment(t *testing.T) {
	for _, test := range []struct {
		raw      string
		want     string
		wantLive bool
		wantErr  bool
	}{
		{raw: "prod", want: "prod", wantLive: true},
		{raw: " PROD ", want: "prod", wantLive: true},
		{raw: "test", want: "test"},
		{raw: "TEST", want: "test"},
		{raw: "", wantErr: true},
		{raw: "sandbox", wantErr: true},
	} {
		t.Run(test.raw, func(t *testing.T) {
			environment, livemode, err := ParseWaffoPancakeEnvironment(test.raw)
			if test.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, environment)
			assert.Equal(t, test.wantLive, livemode)
		})
	}
}

func TestWaffoPancakeQueryEnvironmentMismatchBecomesManualReviewEvidence(t *testing.T) {
	production := true
	tradeNo := "PO_PANCAKE_QUERY_PRODUCTION"
	orderReference := tradeNo
	item := waffoPancakePaymentQueryItem{
		ID: "PAY_test_environment", OrderID: "ORD_test_environment",
		Status: "succeeded", TestMode: true, OrderMerchantExternalID: &orderReference,
	}
	item.SnapshotAmountDetails.Currency = "USD"
	item.SnapshotAmountDetails.Total = "10.00"
	event, err := normalizedWaffoPancakeQueryEvent(&model.PaymentOrder{
		TradeNo: tradeNo, Provider: model.PaymentProviderWaffoPancake, ProviderLivemode: &production,
	}, item, true)
	require.NoError(t, err)
	require.NotNil(t, event.ProviderLivemode)
	assert.False(t, *event.ProviderLivemode)
	assert.True(t, event.ManualReview)
	assert.False(t, event.Paid)
	assert.EqualValues(t, 1000, event.PaidAmountMinor)
	assert.Contains(t, event.NormalizedPayload, `"test_mode":true`)
	assert.Contains(t, event.NormalizedPayload, `"environment_mismatch":true`)
}

func TestPaymentRuntimeConfigurationFingerprintIncludesWaffoPancakeMode(t *testing.T) {
	common.OptionMapRWMutex.Lock()
	optionMapWasNil := common.OptionMap == nil
	if optionMapWasNil {
		common.OptionMap = make(map[string]string)
	}
	original, existed := common.OptionMap["WaffoPancakeTestMode"]
	common.OptionMap["WaffoPancakeTestMode"] = "false"
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if optionMapWasNil {
			common.OptionMap = nil
		} else if existed {
			common.OptionMap["WaffoPancakeTestMode"] = original
		} else {
			delete(common.OptionMap, "WaffoPancakeTestMode")
		}
	})

	productionFingerprint := paymentRuntimeConfigurationFingerprint()
	common.OptionMapRWMutex.Lock()
	common.OptionMap["WaffoPancakeTestMode"] = "true"
	common.OptionMapRWMutex.Unlock()
	assert.NotEqual(t, productionFingerprint, paymentRuntimeConfigurationFingerprint())
}
