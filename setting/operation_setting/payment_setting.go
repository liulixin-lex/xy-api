package operation_setting

import (
	"time"

	"github.com/QuantumNous/new-api/setting/config"
)

var EpayCurrency = "CNY"

// Credential generations bind each order to the exact merchant credential
// used to create it. This lets a stale application node finish an order during
// a planned rotation without making the previous key valid for newer orders.
var EpayCredentialGeneration int64 = 1
var EpayIdPrevious = ""
var EpayKeyPrevious = ""
var EpayPreviousCredentialGeneration int64
var EpayPreviousValidBefore int64
var EpayPreviousExpiresAt int64

func EpayPreviousCredentialActive() bool {
	return EpayPreviousCredentialGeneration > 0 &&
		EpayPreviousExpiresAt > time.Now().Unix() &&
		EpayIdPrevious != "" && EpayKeyPrevious != ""
}

type PaymentSetting struct {
	AmountOptions  []int           `json:"amount_options"`
	AmountDiscount map[int]float64 `json:"amount_discount"` // 充值金额对应的折扣，例如 100 元 0.9 表示 100 元充值享受 9 折优惠

	AffiliateContinuousPercent int `json:"affiliate_continuous_percent"`
	AffiliateFirstTopupPercent int `json:"affiliate_first_topup_percent"`

	ComplianceConfirmed    bool   `json:"compliance_confirmed"`
	ComplianceTermsVersion string `json:"compliance_terms_version"`
	ComplianceConfirmedAt  int64  `json:"compliance_confirmed_at"`
	ComplianceConfirmedBy  int    `json:"compliance_confirmed_by"`
	ComplianceConfirmedIP  string `json:"compliance_confirmed_ip"`
}

const CurrentComplianceTermsVersion = "v1"

// 默认配置
var paymentSetting = PaymentSetting{
	AmountOptions:              []int{10, 20, 50, 100, 200, 500},
	AmountDiscount:             map[int]float64{},
	AffiliateContinuousPercent: 5,
	AffiliateFirstTopupPercent: 30,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("payment_setting", &paymentSetting)
}

func GetPaymentSetting() *PaymentSetting {
	return &paymentSetting
}

func IsPaymentComplianceConfirmed() bool {
	return paymentSetting.ComplianceConfirmed &&
		paymentSetting.ComplianceTermsVersion == CurrentComplianceTermsVersion
}
