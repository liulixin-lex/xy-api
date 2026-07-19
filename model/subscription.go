package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/samber/hot"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Subscription duration units
const (
	SubscriptionDurationYear   = "year"
	SubscriptionDurationMonth  = "month"
	SubscriptionDurationDay    = "day"
	SubscriptionDurationHour   = "hour"
	SubscriptionDurationCustom = "custom"
)

// Subscription quota reset period
const (
	SubscriptionResetNever   = "never"
	SubscriptionResetDaily   = "daily"
	SubscriptionResetWeekly  = "weekly"
	SubscriptionResetMonthly = "monthly"
	SubscriptionResetCustom  = "custom"
)

var (
	ErrSubscriptionOrderNotFound           = errors.New("subscription order not found")
	ErrSubscriptionOrderStatusInvalid      = errors.New("subscription order status invalid")
	ErrSubscriptionOrderSnapshotMissing    = errors.New("subscription order snapshot missing; manual review required")
	ErrSubscriptionOrderManualReview       = errors.New("subscription order requires manual review")
	ErrSubscriptionPaymentAmountRequired   = errors.New("verified payment amount is required")
	ErrSubscriptionPaymentAmountMismatch   = errors.New("subscription payment amount mismatch")
	ErrSubscriptionPaymentCurrencyMismatch = errors.New("subscription payment currency mismatch")
	ErrSubscriptionPurchaseLimit           = errors.New("已达到该套餐购买上限")
	ErrSubscriptionBillingInProgress       = errors.New("subscription has an unfinished billing reservation")
)

const (
	SubscriptionOrderStatusManualReview = "manual_review"
	subscriptionPlanSnapshotVersion     = 1
	defaultSubscriptionReservationTTL   = 2 * time.Hour
	maxSubscriptionDurationYears        = 100
	maxSubscriptionDurationMonths       = maxSubscriptionDurationYears * 12
	maxSubscriptionDurationDays         = maxSubscriptionDurationYears*366 + 25
	maxSubscriptionDurationHours        = maxSubscriptionDurationDays * 24
	maxSubscriptionDurationSeconds      = int64(maxSubscriptionDurationDays) * 24 * 60 * 60
	maxSubscriptionProviderPayloadBytes = 64 * 1024
)

const (
	subscriptionPlanCacheNamespace     = "new-api:subscription_plan:v1"
	subscriptionPlanInfoCacheNamespace = "new-api:subscription_plan_info:v1"
)

var (
	subscriptionPlanCacheOnce     sync.Once
	subscriptionPlanInfoCacheOnce sync.Once

	subscriptionPlanCache     *cachex.HybridCache[SubscriptionPlan]
	subscriptionPlanInfoCache *cachex.HybridCache[SubscriptionPlanInfo]
)

func subscriptionPlanCacheTTL() time.Duration {
	ttlSeconds := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_CACHE_TTL", 300)
	if ttlSeconds <= 0 {
		ttlSeconds = 300
	}
	return time.Duration(ttlSeconds) * time.Second
}

func subscriptionPlanInfoCacheTTL() time.Duration {
	ttlSeconds := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_INFO_CACHE_TTL", 120)
	if ttlSeconds <= 0 {
		ttlSeconds = 120
	}
	return time.Duration(ttlSeconds) * time.Second
}

func subscriptionPlanCacheCapacity() int {
	capacity := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_CACHE_CAP", 5000)
	if capacity <= 0 {
		capacity = 5000
	}
	return capacity
}

func subscriptionPlanInfoCacheCapacity() int {
	capacity := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_INFO_CACHE_CAP", 10000)
	if capacity <= 0 {
		capacity = 10000
	}
	return capacity
}

func getSubscriptionPlanCache() *cachex.HybridCache[SubscriptionPlan] {
	subscriptionPlanCacheOnce.Do(func() {
		ttl := subscriptionPlanCacheTTL()
		subscriptionPlanCache = cachex.NewHybridCache[SubscriptionPlan](cachex.HybridCacheConfig[SubscriptionPlan]{
			Namespace: cachex.Namespace(subscriptionPlanCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[SubscriptionPlan]{},
			Memory: func() *hot.HotCache[string, SubscriptionPlan] {
				return hot.NewHotCache[string, SubscriptionPlan](hot.LRU, subscriptionPlanCacheCapacity()).
					WithTTL(ttl).
					WithJanitor().
					Build()
			},
		})
	})
	return subscriptionPlanCache
}

func getSubscriptionPlanInfoCache() *cachex.HybridCache[SubscriptionPlanInfo] {
	subscriptionPlanInfoCacheOnce.Do(func() {
		ttl := subscriptionPlanInfoCacheTTL()
		subscriptionPlanInfoCache = cachex.NewHybridCache[SubscriptionPlanInfo](cachex.HybridCacheConfig[SubscriptionPlanInfo]{
			Namespace: cachex.Namespace(subscriptionPlanInfoCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[SubscriptionPlanInfo]{},
			Memory: func() *hot.HotCache[string, SubscriptionPlanInfo] {
				return hot.NewHotCache[string, SubscriptionPlanInfo](hot.LRU, subscriptionPlanInfoCacheCapacity()).
					WithTTL(ttl).
					WithJanitor().
					Build()
			},
		})
	})
	return subscriptionPlanInfoCache
}

func subscriptionPlanCacheKey(id int) string {
	if id <= 0 {
		return ""
	}
	return strconv.Itoa(id)
}

func InvalidateSubscriptionPlanCache(planId int) {
	if planId <= 0 {
		return
	}
	cache := getSubscriptionPlanCache()
	_, _ = cache.DeleteMany([]string{subscriptionPlanCacheKey(planId)})
	infoCache := getSubscriptionPlanInfoCache()
	_ = infoCache.Purge()
}

// Subscription plan
type SubscriptionPlan struct {
	Id int `json:"id"`

	Title    string `json:"title" gorm:"type:varchar(128);not null"`
	Subtitle string `json:"subtitle" gorm:"type:varchar(255);default:''"`

	// Display money amount (follow existing code style: float64 for money)
	PriceAmount float64 `json:"price_amount" gorm:"type:decimal(10,6);not null;default:0"`
	Currency    string  `json:"currency" gorm:"type:varchar(8);not null;default:'USD'"`

	DurationUnit  string `json:"duration_unit" gorm:"type:varchar(16);not null;default:'month'"`
	DurationValue int    `json:"duration_value" gorm:"type:int;not null;default:1"`
	CustomSeconds int64  `json:"custom_seconds" gorm:"type:bigint;not null;default:0"`

	Enabled   bool `json:"enabled" gorm:"default:true"`
	SortOrder int  `json:"sort_order" gorm:"type:int;default:0"`

	AllowBalancePay *bool `json:"allow_balance_pay"`

	// Allow falling back to wallet balance after subscription quota is exhausted (empty = true)
	AllowWalletOverflow *bool `json:"allow_wallet_overflow"`

	StripePriceId         string `json:"stripe_price_id" gorm:"type:varchar(128);default:''"`
	CreemProductId        string `json:"creem_product_id" gorm:"type:varchar(128);default:''"`
	WaffoPancakeProductId string `json:"waffo_pancake_product_id" gorm:"type:varchar(128);default:''"`

	// Max purchases per user (0 = unlimited)
	MaxPurchasePerUser int `json:"max_purchase_per_user" gorm:"type:int;default:0"`

	// Upgrade user group after purchase (empty = no change)
	UpgradeGroup string `json:"upgrade_group" gorm:"type:varchar(64);default:''"`

	// Downgrade user group on expiry (empty = revert to the group held before purchase)
	DowngradeGroup string `json:"downgrade_group" gorm:"type:varchar(64);default:''"`

	// Total quota (amount in quota units, 0 = unlimited)
	TotalAmount int64 `json:"total_amount" gorm:"type:bigint;not null;default:0"`

	// Quota reset period for plan
	QuotaResetPeriod        string `json:"quota_reset_period" gorm:"type:varchar(16);default:'never'"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds" gorm:"type:bigint;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"bigint"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

func (p *SubscriptionPlan) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	p.CreatedAt = now
	p.UpdatedAt = now
	return nil
}

func (p *SubscriptionPlan) BeforeUpdate(tx *gorm.DB) error {
	p.UpdatedAt = common.GetTimestamp()
	return nil
}

func (p *SubscriptionPlan) NormalizeDefaults() {
	if p.AllowBalancePay == nil {
		p.AllowBalancePay = common.GetPointer(true)
	}
	if p.AllowWalletOverflow == nil {
		p.AllowWalletOverflow = common.GetPointer(true)
	}
	p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency))
	if p.Currency == "" {
		p.Currency = "USD"
	}
	p.DurationUnit = strings.TrimSpace(p.DurationUnit)
	p.QuotaResetPeriod = strings.TrimSpace(p.QuotaResetPeriod)
	if p.QuotaResetPeriod == "" {
		p.QuotaResetPeriod = SubscriptionResetNever
	}
}

// Subscription order (payment -> webhook -> create UserSubscription)
type SubscriptionOrder struct {
	Id               int     `json:"id"`
	UserId           int     `json:"user_id" gorm:"index;uniqueIndex:idx_subscription_balance_request,priority:1"`
	PlanId           int     `json:"plan_id" gorm:"index"`
	PaymentOrderId   *int64  `json:"payment_order_id,omitempty" gorm:"index"`
	BalanceRequestId *string `json:"balance_request_id,omitempty" gorm:"type:varchar(128);uniqueIndex:idx_subscription_balance_request,priority:2"`
	Money            float64 `json:"money"`

	// PlanSnapshot is the immutable entitlement contract used at fulfillment.
	// It is intentionally not returned to users because it may grow as the
	// snapshot version evolves.
	PlanSnapshot        string  `json:"-" gorm:"type:text"`
	ExpectedAmountMinor int64   `json:"expected_amount_minor" gorm:"type:bigint;not null;default:0"`
	PaymentCurrency     string  `json:"payment_currency" gorm:"type:varchar(8);not null;default:''"`
	ReserveUntil        int64   `json:"reserve_until" gorm:"type:bigint;not null;default:0;index"`
	ProviderOrderId     string  `json:"provider_order_id" gorm:"type:varchar(255);default:'';index"`
	ProviderOrderKey    *string `json:"-" gorm:"type:varchar(320);uniqueIndex"`
	ReviewReason        string  `json:"-" gorm:"type:varchar(255);default:''"`

	TradeNo         string `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider string `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	Status          string `json:"status"`
	CreateTime      int64  `json:"create_time"`
	CompleteTime    int64  `json:"complete_time"`

	ProviderPayload string `json:"provider_payload" gorm:"type:text"`
}

// SubscriptionPlanSnapshot freezes every field that affects the entitlement
// delivered by a paid order. Payment completion must never read a mutable plan.
type SubscriptionPlanSnapshot struct {
	Version                 int    `json:"version"`
	PlanId                  int    `json:"plan_id"`
	Title                   string `json:"title"`
	PriceAmount             string `json:"price_amount"`
	Currency                string `json:"currency"`
	DurationUnit            string `json:"duration_unit"`
	DurationValue           int    `json:"duration_value"`
	CustomSeconds           int64  `json:"custom_seconds"`
	MaxPurchasePerUser      int    `json:"max_purchase_per_user"`
	UpgradeGroup            string `json:"upgrade_group"`
	DowngradeGroup          string `json:"downgrade_group"`
	TotalAmount             int64  `json:"total_amount"`
	QuotaResetPeriod        string `json:"quota_reset_period"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds"`
	AllowWalletOverflow     bool   `json:"allow_wallet_overflow"`
}

// SubscriptionPaymentConfirmation contains only normalized, signed provider
// facts. PaidAmountMinor is required for providers whose callback exposes an
// amount (currently Epay and Stripe).
type SubscriptionPaymentConfirmation struct {
	ProviderPayload         string
	ExpectedPaymentProvider string
	ActualPaymentMethod     string
	PaidAmountMinor         *int64
	Currency                string
	ProviderOrderId         string
}

func validSubscriptionCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for _, ch := range currency {
		if ch < 'A' || ch > 'Z' {
			return false
		}
	}
	return true
}

func ValidateSubscriptionPlan(plan *SubscriptionPlan) error {
	if plan == nil {
		return errors.New("套餐不存在")
	}
	if math.IsNaN(plan.PriceAmount) || math.IsInf(plan.PriceAmount, 0) || plan.PriceAmount < 0 || plan.PriceAmount > 9999 {
		return errors.New("套餐价格必须是 0 到 9999 之间的有限数值")
	}
	currency := strings.ToUpper(strings.TrimSpace(plan.Currency))
	if !validSubscriptionCurrency(currency) {
		return errors.New("套餐币种配置无效")
	}
	if plan.MaxPurchasePerUser < 0 {
		return errors.New("购买上限不能为负数")
	}
	if plan.TotalAmount < 0 {
		return errors.New("套餐总额度不能为负数")
	}
	switch strings.TrimSpace(plan.DurationUnit) {
	case SubscriptionDurationYear:
		if plan.DurationValue <= 0 || plan.DurationValue > maxSubscriptionDurationYears {
			return fmt.Errorf("套餐年限必须在 1 到 %d 之间", maxSubscriptionDurationYears)
		}
	case SubscriptionDurationMonth:
		if plan.DurationValue <= 0 || plan.DurationValue > maxSubscriptionDurationMonths {
			return fmt.Errorf("套餐月数必须在 1 到 %d 之间", maxSubscriptionDurationMonths)
		}
	case SubscriptionDurationDay:
		if plan.DurationValue <= 0 || plan.DurationValue > maxSubscriptionDurationDays {
			return fmt.Errorf("套餐天数必须在 1 到 %d 之间", maxSubscriptionDurationDays)
		}
	case SubscriptionDurationHour:
		if plan.DurationValue <= 0 || plan.DurationValue > maxSubscriptionDurationHours {
			return fmt.Errorf("套餐小时数必须在 1 到 %d 之间", maxSubscriptionDurationHours)
		}
	case SubscriptionDurationCustom:
		if plan.CustomSeconds <= 0 || plan.CustomSeconds > maxSubscriptionDurationSeconds {
			return fmt.Errorf("套餐自定义时长必须在 1 到 %d 秒之间", maxSubscriptionDurationSeconds)
		}
	default:
		return errors.New("套餐时长单位无效")
	}
	resetPeriod := strings.TrimSpace(plan.QuotaResetPeriod)
	if resetPeriod == "" {
		resetPeriod = SubscriptionResetNever
	}
	switch resetPeriod {
	case SubscriptionResetNever, SubscriptionResetDaily, SubscriptionResetWeekly, SubscriptionResetMonthly:
	case SubscriptionResetCustom:
		if plan.QuotaResetCustomSeconds <= 0 || plan.QuotaResetCustomSeconds > maxSubscriptionDurationSeconds {
			return fmt.Errorf("自定义重置周期必须在 1 到 %d 秒之间", maxSubscriptionDurationSeconds)
		}
	default:
		return errors.New("套餐额度重置周期无效")
	}
	return nil
}

func ValidateSubscriptionPlanForExternalPayment(plan *SubscriptionPlan) error {
	if err := ValidateSubscriptionPlan(plan); err != nil {
		return err
	}
	if !plan.Enabled {
		return errors.New("套餐未启用")
	}
	if strings.TrimSpace(plan.Title) == "" {
		return errors.New("套餐标题不能为空")
	}
	if plan.PriceAmount < 0.01 {
		return errors.New("套餐金额过低")
	}
	return nil
}

func NewSubscriptionPlanSnapshot(plan *SubscriptionPlan) (*SubscriptionPlanSnapshot, error) {
	if err := ValidateSubscriptionPlan(plan); err != nil {
		return nil, err
	}
	plan.NormalizeDefaults()
	return &SubscriptionPlanSnapshot{
		Version:                 subscriptionPlanSnapshotVersion,
		PlanId:                  plan.Id,
		Title:                   plan.Title,
		PriceAmount:             decimal.NewFromFloat(plan.PriceAmount).String(),
		Currency:                strings.ToUpper(strings.TrimSpace(plan.Currency)),
		DurationUnit:            strings.TrimSpace(plan.DurationUnit),
		DurationValue:           plan.DurationValue,
		CustomSeconds:           plan.CustomSeconds,
		MaxPurchasePerUser:      plan.MaxPurchasePerUser,
		UpgradeGroup:            strings.TrimSpace(plan.UpgradeGroup),
		DowngradeGroup:          strings.TrimSpace(plan.DowngradeGroup),
		TotalAmount:             plan.TotalAmount,
		QuotaResetPeriod:        NormalizeResetPeriod(plan.QuotaResetPeriod),
		QuotaResetCustomSeconds: plan.QuotaResetCustomSeconds,
		AllowWalletOverflow:     *plan.AllowWalletOverflow,
	}, nil
}

func (s *SubscriptionPlanSnapshot) SubscriptionPlan() (*SubscriptionPlan, error) {
	if s == nil || s.Version != subscriptionPlanSnapshotVersion || s.PlanId <= 0 {
		return nil, ErrSubscriptionOrderSnapshotMissing
	}
	price, err := decimal.NewFromString(strings.TrimSpace(s.PriceAmount))
	if err != nil {
		return nil, ErrSubscriptionOrderSnapshotMissing
	}
	priceAmount, _ := price.Float64()
	if math.IsNaN(priceAmount) || math.IsInf(priceAmount, 0) {
		return nil, ErrSubscriptionOrderSnapshotMissing
	}
	allowWalletOverflow := s.AllowWalletOverflow
	plan := &SubscriptionPlan{
		Id:                      s.PlanId,
		Title:                   s.Title,
		PriceAmount:             priceAmount,
		Currency:                strings.ToUpper(strings.TrimSpace(s.Currency)),
		DurationUnit:            strings.TrimSpace(s.DurationUnit),
		DurationValue:           s.DurationValue,
		CustomSeconds:           s.CustomSeconds,
		Enabled:                 true,
		MaxPurchasePerUser:      s.MaxPurchasePerUser,
		UpgradeGroup:            strings.TrimSpace(s.UpgradeGroup),
		DowngradeGroup:          strings.TrimSpace(s.DowngradeGroup),
		TotalAmount:             s.TotalAmount,
		QuotaResetPeriod:        NormalizeResetPeriod(s.QuotaResetPeriod),
		QuotaResetCustomSeconds: s.QuotaResetCustomSeconds,
		AllowWalletOverflow:     &allowWalletOverflow,
	}
	if err := ValidateSubscriptionPlan(plan); err != nil {
		return nil, ErrSubscriptionOrderSnapshotMissing
	}
	return plan, nil
}

func (o *SubscriptionOrder) SetPlanSnapshot(plan *SubscriptionPlan) error {
	if o == nil {
		return errors.New("subscription order is nil")
	}
	snapshot, err := NewSubscriptionPlanSnapshot(plan)
	if err != nil {
		return err
	}
	data, err := common.Marshal(snapshot)
	if err != nil {
		return err
	}
	o.PlanSnapshot = string(data)
	o.PlanId = snapshot.PlanId
	return nil
}

func (o *SubscriptionOrder) planSnapshot() (*SubscriptionPlanSnapshot, error) {
	if o == nil || strings.TrimSpace(o.PlanSnapshot) == "" {
		return nil, ErrSubscriptionOrderSnapshotMissing
	}
	var snapshot SubscriptionPlanSnapshot
	if err := common.Unmarshal([]byte(o.PlanSnapshot), &snapshot); err != nil {
		return nil, ErrSubscriptionOrderSnapshotMissing
	}
	if snapshot.PlanId != o.PlanId {
		return nil, ErrSubscriptionOrderSnapshotMissing
	}
	if _, err := snapshot.SubscriptionPlan(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func SubscriptionAmountToMinor(amount float64) (int64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return 0, errors.New("支付金额无效")
	}
	minor := decimal.NewFromFloat(amount).Mul(decimal.NewFromInt(100)).Round(0)
	if !minor.IsInteger() || minor.IsNegative() || minor.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, errors.New("支付金额超出范围")
	}
	return minor.IntPart(), nil
}

func ParseSubscriptionAmountMinor(amount string) (int64, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(amount))
	if err != nil || d.IsNegative() {
		return 0, errors.New("支付金额无效")
	}
	minor := d.Mul(decimal.NewFromInt(100))
	if !minor.Equal(minor.Round(0)) || minor.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, errors.New("支付金额精度或范围无效")
	}
	return minor.IntPart(), nil
}

func CalculateSubscriptionPaymentAmount(baseAmount float64, multiplier float64) (float64, int64, error) {
	if math.IsNaN(baseAmount) || math.IsInf(baseAmount, 0) || baseAmount <= 0 ||
		math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 {
		return 0, 0, errors.New("支付金额配置无效")
	}
	amount := decimal.NewFromFloat(baseAmount).Mul(decimal.NewFromFloat(multiplier)).Round(2)
	minor := amount.Mul(decimal.NewFromInt(100))
	if minor.IsNegative() || minor.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, 0, errors.New("支付金额超出范围")
	}
	return amount.InexactFloat64(), minor.IntPart(), nil
}

func (o *SubscriptionOrder) Insert() error {
	if o == nil {
		return errors.New("subscription order is nil")
	}
	if o.CreateTime == 0 {
		o.CreateTime = common.GetTimestamp()
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var snapshot *SubscriptionPlanSnapshot
		if strings.TrimSpace(o.PlanSnapshot) == "" {
			var plan SubscriptionPlan
			if err := tx.Where("id = ?", o.PlanId).First(&plan).Error; err != nil {
				return err
			}
			plan.NormalizeDefaults()
			if err := o.SetPlanSnapshot(&plan); err != nil {
				return err
			}
		}
		var err error
		snapshot, err = o.planSnapshot()
		if err != nil {
			return err
		}
		if o.PaymentCurrency == "" {
			o.PaymentCurrency = snapshot.Currency
		}
		o.PaymentCurrency = strings.ToUpper(strings.TrimSpace(o.PaymentCurrency))
		if !validSubscriptionCurrency(o.PaymentCurrency) {
			return errors.New("支付币种配置无效")
		}
		if o.ExpectedAmountMinor == 0 && o.Money > 0 {
			o.ExpectedAmountMinor, err = SubscriptionAmountToMinor(o.Money)
			if err != nil {
				return err
			}
		}
		if o.ExpectedAmountMinor < 0 {
			return errors.New("预期支付金额不能为负数")
		}
		if len(o.ProviderPayload) > maxSubscriptionProviderPayloadBytes {
			return errors.New("支付网关响应过大")
		}
		if o.Status == common.TopUpStatusPending {
			if o.ReserveUntil == 0 {
				o.ReserveUntil = o.CreateTime + int64(defaultSubscriptionReservationTTL/time.Second)
			}
			if o.ReserveUntil <= o.CreateTime {
				return errors.New("订单保留时间无效")
			}
			if err := reserveSubscriptionPurchaseTx(tx, o.UserId, snapshot); err != nil {
				return err
			}
		}
		return tx.Create(o).Error
	})
}

func (o *SubscriptionOrder) Update() error {
	return DB.Save(o).Error
}

func GetSubscriptionOrderByTradeNo(tradeNo string) *SubscriptionOrder {
	if tradeNo == "" {
		return nil
	}
	var order SubscriptionOrder
	if err := DB.Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
		return nil
	}
	return &order
}

func SetSubscriptionOrderProviderOrderId(tradeNo string, expectedPaymentProvider string, providerOrderId string) error {
	tradeNo = strings.TrimSpace(tradeNo)
	providerOrderId = strings.TrimSpace(providerOrderId)
	if tradeNo == "" || expectedPaymentProvider == "" || providerOrderId == "" || len(providerOrderId) > 255 {
		return errors.New("invalid provider order binding")
	}
	providerOrderKey := expectedPaymentProvider + ":" + providerOrderId
	result := DB.Model(&SubscriptionOrder{}).
		Where("trade_no = ? AND payment_provider = ? AND status = ? AND provider_order_id = '' AND provider_order_key IS NULL",
			tradeNo, expectedPaymentProvider, common.TopUpStatusPending).
		Updates(map[string]interface{}{
			"provider_order_id":  providerOrderId,
			"provider_order_key": providerOrderKey,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		var order SubscriptionOrder
		if err := DB.Select("payment_provider", "provider_order_id", "provider_order_key", "status").
			Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			return ErrSubscriptionOrderNotFound
		}
		if order.PaymentProvider == expectedPaymentProvider && order.ProviderOrderId == providerOrderId &&
			order.ProviderOrderKey != nil && *order.ProviderOrderKey == providerOrderKey &&
			(order.Status == common.TopUpStatusPending || order.Status == common.TopUpStatusSuccess) {
			return nil
		}
		return ErrSubscriptionOrderStatusInvalid
	}
	return nil
}

func reserveSubscriptionPurchaseTx(tx *gorm.DB, userId int, snapshot *SubscriptionPlanSnapshot) error {
	if tx == nil || userId <= 0 || snapshot == nil {
		return errors.New("invalid subscription reservation")
	}
	var user User
	if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&user).Error; err != nil {
		return err
	}
	if snapshot.MaxPurchasePerUser <= 0 {
		return nil
	}
	usedSlots, err := countSubscriptionPurchaseSlotsTx(tx, userId, snapshot.PlanId, 0, 0)
	if err != nil {
		return err
	}
	if usedSlots >= int64(snapshot.MaxPurchasePerUser) {
		return ErrSubscriptionPurchaseLimit
	}
	return nil
}

func countSubscriptionPurchaseSlotsTx(tx *gorm.DB, userId int, planId int, excludeOrderId int, excludePaymentOrderId int64) (int64, error) {
	if tx == nil || userId <= 0 || planId <= 0 {
		return 0, errors.New("invalid subscription purchase count")
	}
	var fulfilled int64
	if err := tx.Model(&UserSubscription{}).
		Where("user_id = ? AND plan_id = ?", userId, planId).
		Count(&fulfilled).Error; err != nil {
		return 0, err
	}
	var pending int64
	now := common.GetTimestamp()
	pendingQuery := tx.Model(&SubscriptionOrder{}).
		Where("user_id = ? AND plan_id = ? AND ((status = ? AND reserve_until > ?) OR status = ?)",
			userId, planId, common.TopUpStatusPending, now, SubscriptionOrderStatusManualReview)
	if excludeOrderId > 0 {
		pendingQuery = pendingQuery.Where("id <> ?", excludeOrderId)
	}
	if excludePaymentOrderId > 0 {
		pendingQuery = pendingQuery.Where("(payment_order_id IS NULL OR payment_order_id <> ?)", excludePaymentOrderId)
	}
	if err := pendingQuery.Count(&pending).Error; err != nil {
		return 0, err
	}
	return fulfilled + pending, nil
}

func CountUserSubscriptionPurchasesByPlan(userId int, planId int) (int64, error) {
	if userId <= 0 || planId <= 0 {
		return 0, errors.New("invalid userId or planId")
	}
	return countSubscriptionPurchaseSlotsTx(DB, userId, planId, 0, 0)
}

// User subscription instance
type UserSubscription struct {
	Id             int    `json:"id"`
	UserId         int    `json:"user_id" gorm:"index;index:idx_user_sub_active,priority:1"`
	PlanId         int    `json:"plan_id" gorm:"index"`
	PaymentOrderId *int64 `json:"payment_order_id,omitempty" gorm:"uniqueIndex"`

	AmountTotal int64 `json:"amount_total" gorm:"type:bigint;not null;default:0"`
	AmountUsed  int64 `json:"amount_used" gorm:"type:bigint;not null;default:0"`
	// AmountUsedTotal is cumulative net usage across reset periods. It is the
	// accounting source for payment reversals; periodic quota resets never clear
	// it. Version 0 identifies historical rows whose pre-migration usage may be
	// incomplete and therefore require manual review on risky reversals.
	AmountUsedTotal        int64 `json:"amount_used_total" gorm:"type:bigint;not null;default:0"`
	UsageAccountingVersion int   `json:"-" gorm:"type:int;not null;default:0"`

	StartTime int64  `json:"start_time" gorm:"bigint"`
	EndTime   int64  `json:"end_time" gorm:"bigint;index;index:idx_user_sub_active,priority:3"`
	Status    string `json:"status" gorm:"type:varchar(32);index;index:idx_user_sub_active,priority:2"` // active/expired/cancelled

	Source string `json:"source" gorm:"type:varchar(32);default:'order'"` // order/admin

	LastResetTime int64 `json:"last_reset_time" gorm:"type:bigint;default:0"`
	NextResetTime int64 `json:"next_reset_time" gorm:"type:bigint;default:0;index"`
	// QuotaResetVersion is the monotonic identity of the current quota period.
	// LastResetTime cannot serve as that identity because an administrator may
	// clear usage without advancing the schedule, and multiple resets may occur
	// in the same second.
	QuotaResetVersion int64 `json:"-" gorm:"type:bigint;not null;default:0"`
	// Empty reset fields identify legacy rows that still fall back to the plan.
	// New purchases persist the order snapshot here so later plan edits cannot
	// change an already purchased entitlement.
	QuotaResetPeriod        string `json:"quota_reset_period" gorm:"type:varchar(16);default:''"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds" gorm:"type:bigint;default:0"`

	UpgradeGroup  string `json:"upgrade_group" gorm:"type:varchar(64);default:''"`
	PrevUserGroup string `json:"prev_user_group" gorm:"type:varchar(64);default:''"`

	// Downgrade target group on expiry (snapshot from plan; empty = revert to PrevUserGroup)
	DowngradeGroup string `json:"downgrade_group" gorm:"type:varchar(64);default:''"`

	// Whether wallet fallback is allowed after this subscription's quota is exhausted (snapshot from plan)
	AllowWalletOverflow bool `json:"allow_wallet_overflow"`

	CreatedAt int64 `json:"created_at" gorm:"bigint"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

func (s *UserSubscription) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	s.CreatedAt = now
	s.UpdatedAt = now
	return nil
}

func (s *UserSubscription) BeforeUpdate(tx *gorm.DB) error {
	s.UpdatedAt = common.GetTimestamp()
	return nil
}

type SubscriptionSummary struct {
	Subscription *UserSubscription `json:"subscription"`
}

type SubscriptionResetResult struct {
	PlanId           int    `json:"plan_id"`
	MatchedCount     int    `json:"matched_count"`
	ResetCount       int    `json:"reset_count"`
	UserCount        int    `json:"user_count"`
	AdvanceResetTime bool   `json:"advance_reset_time"`
	PlanTitle        string `json:"-"`
	AffectedUserIds  []int  `json:"-"`
}

func calcPlanEndTime(start time.Time, plan *SubscriptionPlan) (int64, error) {
	if err := ValidateSubscriptionPlan(plan); err != nil {
		return 0, err
	}
	switch strings.TrimSpace(plan.DurationUnit) {
	case SubscriptionDurationYear:
		return start.AddDate(plan.DurationValue, 0, 0).Unix(), nil
	case SubscriptionDurationMonth:
		return start.AddDate(0, plan.DurationValue, 0).Unix(), nil
	case SubscriptionDurationDay:
		return start.Add(time.Duration(plan.DurationValue) * 24 * time.Hour).Unix(), nil
	case SubscriptionDurationHour:
		return start.Add(time.Duration(plan.DurationValue) * time.Hour).Unix(), nil
	case SubscriptionDurationCustom:
		return start.Add(time.Duration(plan.CustomSeconds) * time.Second).Unix(), nil
	default:
		return 0, fmt.Errorf("invalid duration_unit: %s", plan.DurationUnit)
	}
}

func NormalizeResetPeriod(period string) string {
	switch strings.TrimSpace(period) {
	case SubscriptionResetDaily, SubscriptionResetWeekly, SubscriptionResetMonthly, SubscriptionResetCustom:
		return strings.TrimSpace(period)
	default:
		return SubscriptionResetNever
	}
}

func calcNextResetTime(base time.Time, plan *SubscriptionPlan, endUnix int64) int64 {
	if plan == nil {
		return 0
	}
	period := NormalizeResetPeriod(plan.QuotaResetPeriod)
	if period == SubscriptionResetNever {
		return 0
	}
	var next time.Time
	switch period {
	case SubscriptionResetDaily:
		next = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, base.Location()).
			AddDate(0, 0, 1)
	case SubscriptionResetWeekly:
		// Align to next Monday 00:00
		weekday := int(base.Weekday()) // Sunday=0
		// Convert to Monday=1..Sunday=7
		if weekday == 0 {
			weekday = 7
		}
		daysUntil := 8 - weekday
		next = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, base.Location()).
			AddDate(0, 0, daysUntil)
	case SubscriptionResetMonthly:
		// Align to first day of next month 00:00
		next = time.Date(base.Year(), base.Month(), 1, 0, 0, 0, 0, base.Location()).
			AddDate(0, 1, 0)
	case SubscriptionResetCustom:
		if plan.QuotaResetCustomSeconds <= 0 {
			return 0
		}
		next = base.Add(time.Duration(plan.QuotaResetCustomSeconds) * time.Second)
	default:
		return 0
	}
	if endUnix > 0 && next.Unix() > endUnix {
		return 0
	}
	return next.Unix()
}

func GetSubscriptionPlanById(id int) (*SubscriptionPlan, error) {
	return getSubscriptionPlanByIdTx(nil, id)
}

func getSubscriptionPlanByIdTx(tx *gorm.DB, id int) (*SubscriptionPlan, error) {
	if id <= 0 {
		return nil, errors.New("invalid plan id")
	}
	key := subscriptionPlanCacheKey(id)
	if key != "" {
		if cached, found, err := getSubscriptionPlanCache().Get(key); err == nil && found {
			cached.NormalizeDefaults()
			return &cached, nil
		}
	}
	var plan SubscriptionPlan
	query := DB
	if tx != nil {
		query = tx
	}
	if err := query.Where("id = ?", id).First(&plan).Error; err != nil {
		return nil, err
	}
	plan.NormalizeDefaults()
	_ = getSubscriptionPlanCache().SetWithTTL(key, plan, subscriptionPlanCacheTTL())
	return &plan, nil
}

func CountUserSubscriptionsByPlan(userId int, planId int) (int64, error) {
	if userId <= 0 || planId <= 0 {
		return 0, errors.New("invalid userId or planId")
	}
	var count int64
	if err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND plan_id = ?", userId, planId).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func getUserGroupByIdTx(tx *gorm.DB, userId int) (string, error) {
	if userId <= 0 {
		return "", errors.New("invalid userId")
	}
	if tx == nil {
		tx = DB
	}
	var group string
	if err := tx.Model(&User{}).Where("id = ?", userId).Select(commonGroupCol).Find(&group).Error; err != nil {
		return "", err
	}
	return group, nil
}

func downgradeUserGroupForSubscriptionTx(tx *gorm.DB, sub *UserSubscription, now int64) (string, error) {
	if tx == nil || sub == nil {
		return "", errors.New("invalid downgrade args")
	}
	downgradeGroup := strings.TrimSpace(sub.DowngradeGroup)
	upgradeGroup := strings.TrimSpace(sub.UpgradeGroup)
	// Nothing to do if neither an explicit downgrade target nor an upgrade snapshot exists.
	if downgradeGroup == "" && upgradeGroup == "" {
		return "", nil
	}
	currentGroup, err := getUserGroupByIdTx(tx, sub.UserId)
	if err != nil {
		return "", err
	}
	// Recompute the strongest remaining subscription upgrade. Merely keeping the
	// current group can strand a refunded high-tier group when only a lower-tier
	// subscription remains.
	var activeSubs []UserSubscription
	if err := tx.Where("user_id = ? AND status = ? AND end_time > ? AND id <> ? AND upgrade_group <> ''",
		sub.UserId, "active", now, sub.Id).
		Order("start_time desc, id desc").Find(&activeSubs).Error; err != nil {
		return "", err
	}
	bestGroup := ""
	bestRatio := -1.0
	currentIsSubscriptionManaged := currentGroup == upgradeGroup
	for _, active := range activeSubs {
		candidate := strings.TrimSpace(active.UpgradeGroup)
		if candidate == "" {
			continue
		}
		if currentGroup == candidate {
			currentIsSubscriptionManaged = true
		}
		ratio := ratio_setting.GetGroupRatio(candidate)
		if bestGroup == "" || ratio > bestRatio {
			bestGroup = candidate
			bestRatio = ratio
		}
	}
	if !currentIsSubscriptionManaged {
		return "", nil
	}
	if bestGroup != "" {
		if bestGroup == currentGroup {
			return "", nil
		}
		if err := tx.Model(&User{}).Where("id = ?", sub.UserId).Update("group", bestGroup).Error; err != nil {
			return "", err
		}
		return bestGroup, nil
	}
	// Determine the downgrade target: an explicit downgrade group takes precedence,
	// otherwise revert to the group held before purchase (legacy behavior).
	target := downgradeGroup
	if target == "" {
		// Legacy behavior: only revert when the subscription actually elevated the user.
		if currentGroup != upgradeGroup {
			return "", nil
		}
		target = strings.TrimSpace(sub.PrevUserGroup)
	}
	if target == "" || target == currentGroup {
		return "", nil
	}
	if err := tx.Model(&User{}).Where("id = ?", sub.UserId).
		Update("group", target).Error; err != nil {
		return "", err
	}
	return target, nil
}

func CreateUserSubscriptionFromPlanTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, source string) (*UserSubscription, error) {
	return createUserSubscriptionFromPlanTx(tx, userId, plan, source, 0, 0)
}

// CreateUserSubscriptionFromReservedOrderTx fulfills a legacy pending order
// while excluding that order's own reservation from the purchase cap.
func CreateUserSubscriptionFromReservedOrderTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, source string, reservationOrderId int) (*UserSubscription, error) {
	return createUserSubscriptionFromPlanTx(tx, userId, plan, source, reservationOrderId, 0)
}

// CreateUserSubscriptionFromPaymentOrderTx fulfills a canonical payment order
// while excluding its compatibility projection reservation from the cap.
func CreateUserSubscriptionFromPaymentOrderTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, source string, paymentOrderId int64) (*UserSubscription, error) {
	return createUserSubscriptionFromPlanTx(tx, userId, plan, source, 0, paymentOrderId)
}

func createUserSubscriptionFromPlanTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, source string, excludeOrderId int, excludePaymentOrderId int64) (*UserSubscription, error) {
	if tx == nil {
		return nil, errors.New("tx is nil")
	}
	if plan == nil || plan.Id == 0 {
		return nil, errors.New("invalid plan")
	}
	if userId <= 0 {
		return nil, errors.New("invalid user id")
	}
	plan.NormalizeDefaults()
	if err := ValidateSubscriptionPlan(plan); err != nil {
		return nil, err
	}
	// Serialize entitlement creation per user. This closes the race where two
	// paid callbacks both observe the same purchase count and exceed the cap.
	var lockedUser User
	if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&lockedUser).Error; err != nil {
		return nil, err
	}
	if plan.MaxPurchasePerUser > 0 {
		usedSlots, err := countSubscriptionPurchaseSlotsTx(tx, userId, plan.Id, excludeOrderId, excludePaymentOrderId)
		if err != nil {
			return nil, err
		}
		if usedSlots >= int64(plan.MaxPurchasePerUser) {
			return nil, ErrSubscriptionPurchaseLimit
		}
	}
	nowUnix := common.GetTimestamp()
	now := time.Unix(nowUnix, 0)
	endUnix, err := calcPlanEndTime(now, plan)
	if err != nil {
		return nil, err
	}
	resetBase := now
	nextReset := calcNextResetTime(resetBase, plan, endUnix)
	lastReset := int64(0)
	if nextReset > 0 {
		lastReset = now.Unix()
	}
	upgradeGroup := strings.TrimSpace(plan.UpgradeGroup)
	prevGroup := ""
	if upgradeGroup != "" {
		currentGroup, err := getUserGroupByIdTx(tx, userId)
		if err != nil {
			return nil, err
		}
		if currentGroup != upgradeGroup {
			prevGroup = currentGroup
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("group", upgradeGroup).Error; err != nil {
				return nil, err
			}
		}
	}
	allowWalletOverflow := true
	if plan.AllowWalletOverflow != nil {
		allowWalletOverflow = *plan.AllowWalletOverflow
	}
	sub := &UserSubscription{
		UserId:                  userId,
		PlanId:                  plan.Id,
		AmountTotal:             plan.TotalAmount,
		AmountUsed:              0,
		AmountUsedTotal:         0,
		UsageAccountingVersion:  1,
		StartTime:               now.Unix(),
		EndTime:                 endUnix,
		Status:                  "active",
		Source:                  source,
		LastResetTime:           lastReset,
		NextResetTime:           nextReset,
		QuotaResetPeriod:        NormalizeResetPeriod(plan.QuotaResetPeriod),
		QuotaResetCustomSeconds: plan.QuotaResetCustomSeconds,
		UpgradeGroup:            upgradeGroup,
		PrevUserGroup:           prevGroup,
		DowngradeGroup:          strings.TrimSpace(plan.DowngradeGroup),
		AllowWalletOverflow:     allowWalletOverflow,
		CreatedAt:               common.GetTimestamp(),
		UpdatedAt:               common.GetTimestamp(),
	}
	if err := tx.Create(sub).Error; err != nil {
		return nil, err
	}
	return sub, nil
}

// CompleteSubscriptionOrder is kept for providers whose current callback
// contract does not expose a verifiable amount. Epay and Stripe must call
// CompleteSubscriptionOrderVerified so their signed amount and currency are
// checked against the immutable order snapshot.
func CompleteSubscriptionOrder(tradeNo string, providerPayload string, expectedPaymentProvider string, actualPaymentMethod string) error {
	return completeSubscriptionOrder(tradeNo, SubscriptionPaymentConfirmation{
		ProviderPayload:         providerPayload,
		ExpectedPaymentProvider: expectedPaymentProvider,
		ActualPaymentMethod:     actualPaymentMethod,
	})
}

func CompleteSubscriptionOrderVerified(tradeNo string, confirmation SubscriptionPaymentConfirmation) error {
	return completeSubscriptionOrder(tradeNo, confirmation)
}

func completeSubscriptionOrder(tradeNo string, confirmation SubscriptionPaymentConfirmation) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	if len(confirmation.ProviderPayload) > maxSubscriptionProviderPayloadBytes {
		return errors.New("支付网关响应过大")
	}
	if len(strings.TrimSpace(confirmation.ProviderOrderId)) > 255 || len(strings.TrimSpace(confirmation.ActualPaymentMethod)) > 50 {
		return errors.New("支付网关标识超出范围")
	}
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	var logUserId int
	var logPlanTitle string
	var logMoney float64
	var logPaymentMethod string
	var upgradeGroup string
	var manualReviewErr error
	err := DB.Transaction(func(tx *gorm.DB) error {
		var order SubscriptionOrder
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(&order).Error; err != nil {
			return ErrSubscriptionOrderNotFound
		}
		if confirmation.ExpectedPaymentProvider != "" && order.PaymentProvider != confirmation.ExpectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.Status == common.TopUpStatusSuccess {
			return nil
		}
		if order.Status == SubscriptionOrderStatusManualReview {
			return ErrSubscriptionOrderManualReview
		}
		if order.Status != common.TopUpStatusPending {
			return ErrSubscriptionOrderStatusInvalid
		}
		var confirmationProviderOrderKey *string
		if confirmation.ProviderOrderId != "" {
			key := order.PaymentProvider + ":" + confirmation.ProviderOrderId
			confirmationProviderOrderKey = &key
		}

		markManualReview := func(reason string, reviewErr error) error {
			order.Status = SubscriptionOrderStatusManualReview
			order.ReviewReason = reason
			order.CompleteTime = common.GetTimestamp()
			if confirmation.ProviderPayload != "" {
				order.ProviderPayload = confirmation.ProviderPayload
			}
			if confirmation.ProviderOrderId != "" {
				order.ProviderOrderId = confirmation.ProviderOrderId
				order.ProviderOrderKey = confirmationProviderOrderKey
			}
			if err := tx.Save(&order).Error; err != nil {
				return err
			}
			manualReviewErr = reviewErr
			return nil
		}

		snapshot, err := order.planSnapshot()
		if err != nil {
			return markManualReview("missing_or_invalid_plan_snapshot", ErrSubscriptionOrderSnapshotMissing)
		}
		plan, err := snapshot.SubscriptionPlan()
		if err != nil {
			return markManualReview("missing_or_invalid_plan_snapshot", ErrSubscriptionOrderSnapshotMissing)
		}
		requiresVerifiedAmount := order.PaymentProvider == PaymentProviderEpay || order.PaymentProvider == PaymentProviderStripe
		if requiresVerifiedAmount && confirmation.PaidAmountMinor == nil {
			return ErrSubscriptionPaymentAmountRequired
		}
		if confirmation.PaidAmountMinor != nil && *confirmation.PaidAmountMinor != order.ExpectedAmountMinor {
			return markManualReview("paid_amount_mismatch", ErrSubscriptionPaymentAmountMismatch)
		}
		if confirmation.PaidAmountMinor != nil {
			actualCurrency := strings.ToUpper(strings.TrimSpace(confirmation.Currency))
			expectedCurrency := strings.ToUpper(strings.TrimSpace(order.PaymentCurrency))
			if actualCurrency == "" || expectedCurrency == "" || actualCurrency != expectedCurrency {
				return markManualReview("payment_currency_mismatch", ErrSubscriptionPaymentCurrencyMismatch)
			}
		}
		if order.ProviderOrderId != "" || order.ProviderOrderKey != nil {
			if confirmation.ProviderOrderId == "" || order.ProviderOrderId != confirmation.ProviderOrderId ||
				order.ProviderOrderKey == nil || confirmationProviderOrderKey == nil || *order.ProviderOrderKey != *confirmationProviderOrderKey {
				return markManualReview("provider_order_id_mismatch", ErrPaymentMethodMismatch)
			}
		}
		if confirmationProviderOrderKey != nil && order.ProviderOrderKey == nil {
			var duplicateCount int64
			if err := tx.Model(&SubscriptionOrder{}).
				Where("id <> ? AND payment_provider = ? AND (provider_order_key = ? OR provider_order_id = ?)",
					order.Id, order.PaymentProvider, *confirmationProviderOrderKey, confirmation.ProviderOrderId).
				Count(&duplicateCount).Error; err != nil {
				return err
			}
			if duplicateCount > 0 {
				order.Status = SubscriptionOrderStatusManualReview
				order.ReviewReason = "provider_order_reused"
				order.CompleteTime = common.GetTimestamp()
				if confirmation.ProviderPayload != "" {
					order.ProviderPayload = confirmation.ProviderPayload
				}
				if err := tx.Save(&order).Error; err != nil {
					return err
				}
				manualReviewErr = ErrPaymentMethodMismatch
				return nil
			}
		}
		if confirmation.ActualPaymentMethod != "" && order.PaymentMethod != "" && order.PaymentMethod != confirmation.ActualPaymentMethod {
			return markManualReview("payment_method_mismatch", ErrPaymentMethodMismatch)
		}
		upgradeGroup = strings.TrimSpace(plan.UpgradeGroup)
		excludePaymentOrderId := int64(0)
		if order.PaymentOrderId != nil {
			excludePaymentOrderId = *order.PaymentOrderId
		}
		subscription, err := createUserSubscriptionFromPlanTx(tx, order.UserId, plan, "order", order.Id, excludePaymentOrderId)
		if err != nil {
			if errors.Is(err, ErrSubscriptionPurchaseLimit) {
				return markManualReview("purchase_limit_conflict", err)
			}
			return err
		}
		if order.PaymentOrderId != nil {
			if err := tx.Model(subscription).Update("payment_order_id", *order.PaymentOrderId).Error; err != nil {
				return err
			}
		}
		if err := upsertSubscriptionTopUpTx(tx, &order); err != nil {
			return err
		}
		order.Status = common.TopUpStatusSuccess
		order.CompleteTime = common.GetTimestamp()
		order.ReviewReason = ""
		if confirmation.ProviderPayload != "" {
			order.ProviderPayload = confirmation.ProviderPayload
		}
		if confirmation.ProviderOrderId != "" {
			order.ProviderOrderId = confirmation.ProviderOrderId
			order.ProviderOrderKey = confirmationProviderOrderKey
		}
		if confirmation.ActualPaymentMethod != "" {
			order.PaymentMethod = confirmation.ActualPaymentMethod
		}
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		logUserId = order.UserId
		logPlanTitle = plan.Title
		logMoney = order.Money
		logPaymentMethod = order.PaymentMethod
		return nil
	})
	if err != nil {
		return err
	}
	if manualReviewErr != nil {
		return manualReviewErr
	}
	if upgradeGroup != "" && logUserId > 0 {
		_ = UpdateUserGroupCache(logUserId, upgradeGroup)
	}
	if logUserId > 0 {
		msg := fmt.Sprintf("订阅购买成功，套餐: %s，支付金额: %.2f，支付方式: %s", logPlanTitle, logMoney, logPaymentMethod)
		RecordLog(logUserId, LogTypeTopup, msg)
	}
	return nil
}

func upsertSubscriptionTopUpTx(tx *gorm.DB, order *SubscriptionOrder) error {
	if tx == nil || order == nil {
		return errors.New("invalid subscription order")
	}
	now := common.GetTimestamp()
	var topup TopUp
	if err := tx.Where("trade_no = ?", order.TradeNo).First(&topup).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			topup = TopUp{
				UserId:          order.UserId,
				PaymentOrderId:  order.PaymentOrderId,
				Amount:          0,
				Money:           order.Money,
				TradeNo:         order.TradeNo,
				PaymentMethod:   order.PaymentMethod,
				PaymentProvider: order.PaymentProvider,
				CreateTime:      order.CreateTime,
				CompleteTime:    now,
				Status:          common.TopUpStatusSuccess,
			}
			return tx.Create(&topup).Error
		}
		return err
	}
	if topup.PaymentOrderId == nil && order.PaymentOrderId != nil {
		topup.PaymentOrderId = order.PaymentOrderId
	} else if topup.PaymentOrderId != nil && order.PaymentOrderId != nil && *topup.PaymentOrderId != *order.PaymentOrderId {
		return ErrPaymentMethodMismatch
	}
	topup.Money = order.Money
	if topup.PaymentProvider == "" {
		topup.PaymentProvider = order.PaymentProvider
	} else if topup.PaymentProvider != order.PaymentProvider {
		return ErrPaymentMethodMismatch
	}
	if topup.PaymentMethod == "" {
		topup.PaymentMethod = order.PaymentMethod
	} else if topup.PaymentMethod != order.PaymentMethod {
		return ErrPaymentMethodMismatch
	}
	if topup.CreateTime == 0 {
		topup.CreateTime = order.CreateTime
	}
	topup.CompleteTime = now
	topup.Status = common.TopUpStatusSuccess
	return tx.Save(&topup).Error
}

func ExpireSubscriptionOrder(tradeNo string, expectedPaymentProvider string) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var order SubscriptionOrder
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(&order).Error; err != nil {
			return ErrSubscriptionOrderNotFound
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.Status != common.TopUpStatusPending {
			return nil
		}
		order.Status = common.TopUpStatusExpired
		order.CompleteTime = common.GetTimestamp()
		return tx.Save(&order).Error
	})
}

// Admin bind (no payment). Creates a UserSubscription from a plan.
func AdminBindSubscription(userId int, planId int, sourceNote string) (string, error) {
	if userId <= 0 || planId <= 0 {
		return "", errors.New("invalid userId or planId")
	}
	plan, err := GetSubscriptionPlanById(planId)
	if err != nil {
		return "", err
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		_, err := CreateUserSubscriptionFromPlanTx(tx, userId, plan, "admin")
		return err
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(plan.UpgradeGroup) != "" {
		_ = UpdateUserGroupCache(userId, plan.UpgradeGroup)
		return fmt.Sprintf("用户分组将升级到 %s", plan.UpgradeGroup), nil
	}
	return "", nil
}

func calcSubscriptionBalanceQuota(priceAmount float64) (int, error) {
	if priceAmount <= 0 {
		return 0, nil
	}
	if math.IsNaN(priceAmount) || math.IsInf(priceAmount, 0) ||
		math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) || common.QuotaPerUnit <= 0 {
		return 0, errors.New("额度单位配置错误")
	}
	quotaDecimal := decimal.NewFromFloat(priceAmount).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit)).
		Ceil()
	quotaFloat, _ := quotaDecimal.Float64()
	quota, clamp := common.QuotaFromFloatChecked(quotaFloat)
	if clamp != nil {
		return 0, errors.New("套餐余额价格超出可计费范围")
	}
	if quota <= 0 {
		return 0, errors.New("套餐余额价格过低")
	}
	return quota, nil
}

// PurchaseSubscriptionWithBalance creates a subscription by deducting the user's wallet quota.
func PurchaseSubscriptionWithBalance(userId int, planId int, requestId string) error {
	requestId = strings.TrimSpace(requestId)
	if userId <= 0 || planId <= 0 || !isValidSubscriptionBalanceRequestId(requestId) {
		return errors.New("invalid balance subscription purchase request")
	}

	var logPlanTitle string
	var logMoney float64
	var chargedQuota int
	var upgradeGroup string
	duplicate := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).Where("id = ?", userId).First(&user).Error; err != nil {
			return err
		}
		var existing SubscriptionOrder
		existingQuery := tx.Where("user_id = ? AND balance_request_id = ?", userId, requestId).Limit(1).Find(&existing)
		if existingQuery.Error != nil {
			return existingQuery.Error
		}
		if existingQuery.RowsAffected > 0 {
			if existing.PlanId != planId || existing.PaymentProvider != PaymentProviderBalance ||
				existing.PaymentMethod != PaymentMethodBalance || existing.Status != common.TopUpStatusSuccess {
				return errors.New("balance subscription request id was reused with different purchase data")
			}
			duplicate = true
			return nil
		}

		var plan SubscriptionPlan
		if err := tx.Where("id = ?", planId).First(&plan).Error; err != nil {
			return err
		}
		plan.NormalizeDefaults()
		if err := ValidateSubscriptionPlan(&plan); err != nil {
			return err
		}
		if !plan.Enabled {
			return errors.New("套餐未启用")
		}
		if plan.AllowBalancePay != nil && !*plan.AllowBalancePay {
			return errors.New("该套餐不允许使用余额兑换")
		}
		if !strings.EqualFold(strings.TrimSpace(plan.Currency), "USD") {
			return errors.New("余额订阅仅支持 USD 定价套餐")
		}

		requiredQuota, err := calcSubscriptionBalanceQuota(plan.PriceAmount)
		if err != nil {
			return err
		}

		if requiredQuota > 0 && user.Quota < requiredQuota {
			return errors.New("余额不足")
		}
		if requiredQuota > 0 {
			result := tx.Model(&User{}).Where("id = ? AND quota >= ?", userId, requiredQuota).
				Update("quota", gorm.Expr("quota - ?", requiredQuota))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("余额不足")
			}
		}

		if _, err := CreateUserSubscriptionFromPlanTx(tx, userId, &plan, PaymentMethodBalance); err != nil {
			return err
		}

		now := common.GetTimestamp()
		tradeNo := fmt.Sprintf("SUBBALUSR%dNO%s%d", userId, common.GetRandomString(6), time.Now().UnixNano())
		order := &SubscriptionOrder{
			UserId:           userId,
			PlanId:           plan.Id,
			BalanceRequestId: &requestId,
			Money:            plan.PriceAmount,
			TradeNo:          tradeNo,
			PaymentMethod:    PaymentMethodBalance,
			PaymentProvider:  PaymentProviderBalance,
			Status:           common.TopUpStatusSuccess,
			CreateTime:       now,
			CompleteTime:     now,
			ProviderPayload:  fmt.Sprintf("charged_quota=%d", requiredQuota),
		}
		if err := order.SetPlanSnapshot(&plan); err != nil {
			return err
		}
		order.PaymentCurrency = plan.Currency
		order.ExpectedAmountMinor, err = SubscriptionAmountToMinor(plan.PriceAmount)
		if err != nil {
			return err
		}
		if err := tx.Create(order).Error; err != nil {
			return err
		}

		logPlanTitle = plan.Title
		logMoney = plan.PriceAmount
		chargedQuota = requiredQuota
		upgradeGroup = strings.TrimSpace(plan.UpgradeGroup)
		return nil
	})
	if err != nil {
		return err
	}
	if duplicate {
		return nil
	}

	if chargedQuota > 0 {
		if err := cacheDecrUserQuota(userId, int64(chargedQuota)); err != nil {
			common.SysLog("failed to decrease user quota cache after subscription balance purchase: " + err.Error())
		}
	}
	if upgradeGroup != "" {
		_ = UpdateUserGroupCache(userId, upgradeGroup)
	}
	msg := fmt.Sprintf("使用余额购买订阅成功，套餐: %s，支付金额: %.2f，扣除额度: %d", logPlanTitle, logMoney, chargedQuota)
	RecordLog(userId, LogTypeTopup, msg)
	return nil
}

func isValidSubscriptionBalanceRequestId(requestId string) bool {
	if requestId == "" || len(requestId) > 128 {
		return false
	}
	for _, r := range requestId {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// GetAllActiveUserSubscriptions returns all active subscriptions for a user.
func GetAllActiveUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	now := common.GetTimestamp()
	var subs []UserSubscription
	err := DB.Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
		Order("end_time desc, id desc").
		Find(&subs).Error
	if err != nil {
		return nil, err
	}
	return buildSubscriptionSummaries(subs), nil
}

// HasActiveUserSubscription returns whether the user has any active subscription.
// This is a lightweight existence check to avoid heavy pre-consume transactions.
func HasActiveUserSubscription(userId int) (bool, error) {
	if userId <= 0 {
		return false, errors.New("invalid userId")
	}
	now := common.GetTimestamp()
	var count int64
	if err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// UserActiveSubscriptionsAllowWalletOverflow returns whether wallet balance may be used
// after the user's subscription quota is exhausted. A single active subscription that
// disallows wallet overflow (allow_wallet_overflow = false) blocks the fallback.
func UserActiveSubscriptionsAllowWalletOverflow(userId int) (bool, error) {
	if userId <= 0 {
		return false, errors.New("invalid userId")
	}
	now := common.GetTimestamp()
	var strictCount int64
	if err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND status = ? AND end_time > ? AND allow_wallet_overflow = ?",
			userId, "active", now, false).
		Count(&strictCount).Error; err != nil {
		return false, err
	}
	return strictCount == 0, nil
}

// GetAllUserSubscriptions returns all subscriptions (active and expired) for a user.
func GetAllUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	var subs []UserSubscription
	err := DB.Where("user_id = ?", userId).
		Order("end_time desc, id desc").
		Find(&subs).Error
	if err != nil {
		return nil, err
	}
	return buildSubscriptionSummaries(subs), nil
}

func buildSubscriptionSummaries(subs []UserSubscription) []SubscriptionSummary {
	if len(subs) == 0 {
		return []SubscriptionSummary{}
	}
	result := make([]SubscriptionSummary, 0, len(subs))
	for _, sub := range subs {
		subCopy := sub
		result = append(result, SubscriptionSummary{
			Subscription: &subCopy,
		})
	}
	return result
}

// AdminInvalidateUserSubscription marks a user subscription as cancelled and ends it immediately.
func AdminInvalidateUserSubscription(userSubscriptionId int) (string, error) {
	if userSubscriptionId <= 0 {
		return "", errors.New("invalid userSubscriptionId")
	}
	now := common.GetTimestamp()
	cacheGroup := ""
	downgradeGroup := ""
	var userId int
	err := DB.Transaction(func(tx *gorm.DB) error {
		var sub UserSubscription
		if err := lockForUpdate(tx).
			Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
			return err
		}
		userId = sub.UserId
		if err := tx.Model(&sub).Updates(map[string]interface{}{
			"status":     "cancelled",
			"end_time":   now,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		target, err := downgradeUserGroupForSubscriptionTx(tx, &sub, now)
		if err != nil {
			return err
		}
		if target != "" {
			cacheGroup = target
			downgradeGroup = target
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if cacheGroup != "" && userId > 0 {
		_ = UpdateUserGroupCache(userId, cacheGroup)
	}
	if downgradeGroup != "" {
		return fmt.Sprintf("用户分组将回退到 %s", downgradeGroup), nil
	}
	return "", nil
}

// AdminDeleteUserSubscription hard-deletes a user subscription.
func AdminDeleteUserSubscription(userSubscriptionId int) (string, error) {
	if userSubscriptionId <= 0 {
		return "", errors.New("invalid userSubscriptionId")
	}
	now := common.GetTimestamp()
	cacheGroup := ""
	downgradeGroup := ""
	var userId int
	err := DB.Transaction(func(tx *gorm.DB) error {
		var sub UserSubscription
		if err := lockForUpdate(tx).
			Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
			return err
		}
		var unfinishedReservations int64
		if err := tx.Model(&BillingReservation{}).
			Where("subscription_id = ? AND status IN ?", sub.Id, []string{
				BillingReservationStatusInitializing,
				BillingReservationStatusReserved,
			}).
			Count(&unfinishedReservations).Error; err != nil {
			return err
		}
		if unfinishedReservations > 0 {
			return ErrSubscriptionBillingInProgress
		}
		userId = sub.UserId
		target, err := downgradeUserGroupForSubscriptionTx(tx, &sub, now)
		if err != nil {
			return err
		}
		if target != "" {
			cacheGroup = target
			downgradeGroup = target
		}
		if err := tx.Where("id = ?", userSubscriptionId).Delete(&UserSubscription{}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if cacheGroup != "" && userId > 0 {
		_ = UpdateUserGroupCache(userId, cacheGroup)
	}
	if downgradeGroup != "" {
		return fmt.Sprintf("用户分组将回退到 %s", downgradeGroup), nil
	}
	return "", nil
}

func resetUserSubscriptionTx(tx *gorm.DB, sub *UserSubscription, plan *SubscriptionPlan, now int64, advanceResetTime bool) error {
	if tx == nil || sub == nil || plan == nil {
		return errors.New("invalid reset args")
	}
	if err := advanceSubscriptionQuotaResetVersion(sub); err != nil {
		return err
	}
	if sub.AmountUsedTotal < sub.AmountUsed {
		sub.AmountUsedTotal = sub.AmountUsed
	}
	sub.AmountUsed = 0
	if advanceResetTime {
		nextReset := calcNextResetTime(time.Unix(now, 0), plan, sub.EndTime)
		sub.NextResetTime = nextReset
		if nextReset > 0 {
			sub.LastResetTime = now
		} else {
			sub.LastResetTime = 0
		}
	}
	return tx.Save(sub).Error
}

func advanceSubscriptionQuotaResetVersion(sub *UserSubscription) error {
	if sub == nil || sub.QuotaResetVersion < 0 || sub.QuotaResetVersion == math.MaxInt64 {
		return errors.New("subscription quota reset version overflow")
	}
	sub.QuotaResetVersion++
	return nil
}

func buildSubscriptionResetResult(plan *SubscriptionPlan, subs []UserSubscription, advanceResetTime bool) *SubscriptionResetResult {
	userIds := make([]int, 0, len(subs))
	seenUsers := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		if _, ok := seenUsers[sub.UserId]; ok {
			continue
		}
		seenUsers[sub.UserId] = struct{}{}
		userIds = append(userIds, sub.UserId)
	}
	return &SubscriptionResetResult{
		PlanId:           plan.Id,
		MatchedCount:     len(subs),
		ResetCount:       len(subs),
		UserCount:        len(userIds),
		AdvanceResetTime: advanceResetTime,
		PlanTitle:        plan.Title,
		AffectedUserIds:  userIds,
	}
}

func adminResetUserSubscriptionsByPlanTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, now int64, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if tx == nil || plan == nil {
		return nil, errors.New("invalid reset args")
	}
	var subs []UserSubscription
	if err := lockForUpdate(tx).
		Where("user_id = ? AND plan_id = ? AND status = ? AND end_time > ?", userId, plan.Id, "active", now).
		Order("end_time asc, id asc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, errors.New("该用户没有有效的此套餐订阅")
	}
	for i := range subs {
		if err := resetUserSubscriptionTx(tx, &subs[i], plan, now, advanceResetTime); err != nil {
			return nil, err
		}
	}
	return buildSubscriptionResetResult(plan, subs, advanceResetTime), nil
}

func adminResetPlanSubscriptionsTx(tx *gorm.DB, plan *SubscriptionPlan, now int64, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if tx == nil || plan == nil {
		return nil, errors.New("invalid reset args")
	}
	var subs []UserSubscription
	if err := lockForUpdate(tx).
		Where("plan_id = ? AND status = ? AND end_time > ?", plan.Id, "active", now).
		Order("user_id asc, end_time asc, id asc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	for i := range subs {
		if err := resetUserSubscriptionTx(tx, &subs[i], plan, now, advanceResetTime); err != nil {
			return nil, err
		}
	}
	return buildSubscriptionResetResult(plan, subs, advanceResetTime), nil
}

func AdminResetUserSubscriptionsByPlan(userId int, planId int, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if userId <= 0 || planId <= 0 {
		return nil, errors.New("invalid userId or planId")
	}
	var result *SubscriptionResetResult
	now := GetDBTimestamp()
	err := DB.Transaction(func(tx *gorm.DB) error {
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		result, err = adminResetUserSubscriptionsByPlanTx(tx, userId, plan, now, advanceResetTime)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func AdminResetPlanSubscriptions(planId int, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if planId <= 0 {
		return nil, errors.New("invalid planId")
	}
	var result *SubscriptionResetResult
	now := GetDBTimestamp()
	err := DB.Transaction(func(tx *gorm.DB) error {
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		result, err = adminResetPlanSubscriptionsTx(tx, plan, now, advanceResetTime)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type SubscriptionPreConsumeResult struct {
	UserSubscriptionId int
	PreConsumed        int64
	AmountTotal        int64
	AmountUsedBefore   int64
	AmountUsedAfter    int64
}

// ExpireDueSubscriptions marks expired subscriptions and handles group downgrade.
func ExpireDueSubscriptions(limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	now := GetDBTimestamp()
	var subs []UserSubscription
	if err := DB.Where("status = ? AND end_time > 0 AND end_time <= ?", "active", now).
		Order("end_time asc, id asc").
		Limit(limit).
		Find(&subs).Error; err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		return 0, nil
	}
	expiredCount := 0
	userIds := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		if sub.UserId > 0 {
			userIds[sub.UserId] = struct{}{}
		}
	}
	for userId := range userIds {
		cacheGroup := ""
		err := DB.Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&UserSubscription{}).
				Where("user_id = ? AND status = ? AND end_time > 0 AND end_time <= ?", userId, "active", now).
				Updates(map[string]interface{}{
					"status":     "expired",
					"updated_at": common.GetTimestamp(),
				})
			if res.Error != nil {
				return res.Error
			}
			expiredCount += int(res.RowsAffected)

			// If there's an active upgraded subscription, keep current group.
			var activeSub UserSubscription
			activeQuery := tx.Where("user_id = ? AND status = ? AND end_time > ? AND upgrade_group <> ''",
				userId, "active", now).
				Order("end_time desc, id desc").
				Limit(1).
				Find(&activeSub)
			if activeQuery.Error == nil && activeQuery.RowsAffected > 0 {
				return nil
			}

			// Find the most recently expired subscription that defines a group transition
			// (an explicit downgrade target or an upgrade snapshot to revert).
			var lastExpired UserSubscription
			expiredQuery := tx.Where("user_id = ? AND status = ? AND (downgrade_group <> '' OR upgrade_group <> '')",
				userId, "expired").
				Order("end_time desc, id desc").
				Limit(1).
				Find(&lastExpired)
			if expiredQuery.Error != nil || expiredQuery.RowsAffected == 0 {
				return nil
			}
			currentGroup, err := getUserGroupByIdTx(tx, userId)
			if err != nil {
				return err
			}
			// An explicit downgrade group takes precedence; otherwise revert to the
			// group held before purchase (legacy behavior, only when the subscription
			// actually elevated the user).
			target := strings.TrimSpace(lastExpired.DowngradeGroup)
			if target == "" {
				upgradeGroup := strings.TrimSpace(lastExpired.UpgradeGroup)
				prevGroup := strings.TrimSpace(lastExpired.PrevUserGroup)
				if upgradeGroup == "" || prevGroup == "" {
					return nil
				}
				if currentGroup != upgradeGroup {
					return nil
				}
				target = prevGroup
			}
			if target == "" || target == currentGroup {
				return nil
			}
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("group", target).Error; err != nil {
				return err
			}
			cacheGroup = target
			return nil
		})
		if err != nil {
			return expiredCount, err
		}
		if cacheGroup != "" {
			_ = UpdateUserGroupCache(userId, cacheGroup)
		}
	}
	return expiredCount, nil
}

// SubscriptionPreConsumeRecord stores idempotent pre-consume operations per request.
type SubscriptionPreConsumeRecord struct {
	Id                 int    `json:"id"`
	RequestId          string `json:"request_id" gorm:"type:varchar(64);uniqueIndex"`
	UserId             int    `json:"user_id" gorm:"index"`
	UserSubscriptionId int    `json:"user_subscription_id" gorm:"index"`
	PreConsumed        int64  `json:"pre_consumed" gorm:"type:bigint;not null;default:0"`
	Status             string `json:"status" gorm:"type:varchar(32);index"` // consumed/refunded
	CreatedAt          int64  `json:"created_at" gorm:"bigint"`
	UpdatedAt          int64  `json:"updated_at" gorm:"bigint;index"`
}

func (r *SubscriptionPreConsumeRecord) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	r.CreatedAt = now
	r.UpdatedAt = now
	return nil
}

func (r *SubscriptionPreConsumeRecord) BeforeUpdate(tx *gorm.DB) error {
	r.UpdatedAt = common.GetTimestamp()
	return nil
}

func maybeResetUserSubscriptionWithPlanTx(tx *gorm.DB, sub *UserSubscription, plan *SubscriptionPlan, now int64) error {
	if tx == nil || sub == nil || plan == nil {
		return errors.New("invalid reset args")
	}
	if sub.NextResetTime > 0 && sub.NextResetTime > now {
		return nil
	}
	resetPlan := *plan
	if strings.TrimSpace(sub.QuotaResetPeriod) != "" {
		resetPlan.QuotaResetPeriod = NormalizeResetPeriod(sub.QuotaResetPeriod)
		resetPlan.QuotaResetCustomSeconds = sub.QuotaResetCustomSeconds
	}
	if NormalizeResetPeriod(resetPlan.QuotaResetPeriod) == SubscriptionResetNever {
		return nil
	}
	baseUnix := sub.LastResetTime
	if baseUnix <= 0 {
		baseUnix = sub.StartTime
	}
	base := time.Unix(baseUnix, 0)
	next := calcNextResetTime(base, &resetPlan, sub.EndTime)
	advanced := false
	for next > 0 && next <= now {
		advanced = true
		base = time.Unix(next, 0)
		next = calcNextResetTime(base, &resetPlan, sub.EndTime)
	}
	if !advanced {
		if sub.NextResetTime == 0 && next > 0 {
			sub.NextResetTime = next
			sub.LastResetTime = base.Unix()
			return tx.Save(sub).Error
		}
		return nil
	}
	if sub.AmountUsedTotal < sub.AmountUsed {
		sub.AmountUsedTotal = sub.AmountUsed
	}
	if err := advanceSubscriptionQuotaResetVersion(sub); err != nil {
		return err
	}
	sub.AmountUsed = 0
	sub.LastResetTime = base.Unix()
	sub.NextResetTime = next
	return tx.Save(sub).Error
}

func getSubscriptionResetPlanTx(tx *gorm.DB, sub *UserSubscription) (*SubscriptionPlan, error) {
	if sub == nil || sub.PlanId <= 0 {
		return nil, errors.New("invalid subscription reset plan")
	}
	if strings.TrimSpace(sub.QuotaResetPeriod) != "" {
		return &SubscriptionPlan{
			Id:                      sub.PlanId,
			QuotaResetPeriod:        NormalizeResetPeriod(sub.QuotaResetPeriod),
			QuotaResetCustomSeconds: sub.QuotaResetCustomSeconds,
		}, nil
	}
	return getSubscriptionPlanByIdTx(tx, sub.PlanId)
}

// PreConsumeUserSubscription pre-consumes from any active subscription total quota.
func PreConsumeUserSubscription(requestId string, userId int, modelName string, quotaType int, amount int64) (*SubscriptionPreConsumeResult, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	if strings.TrimSpace(requestId) == "" {
		return nil, errors.New("requestId is empty")
	}
	if amount <= 0 {
		return nil, errors.New("amount must be > 0")
	}
	now := GetDBTimestamp()

	returnValue := &SubscriptionPreConsumeResult{}

	err := DB.Transaction(func(tx *gorm.DB) error {
		var existing SubscriptionPreConsumeRecord
		query := tx.Where("request_id = ?", requestId).Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			if existing.Status == "refunded" {
				return errors.New("subscription pre-consume already refunded")
			}
			var sub UserSubscription
			if err := tx.Where("id = ?", existing.UserSubscriptionId).First(&sub).Error; err != nil {
				return err
			}
			returnValue.UserSubscriptionId = sub.Id
			returnValue.PreConsumed = existing.PreConsumed
			returnValue.AmountTotal = sub.AmountTotal
			returnValue.AmountUsedBefore = sub.AmountUsed
			returnValue.AmountUsedAfter = sub.AmountUsed
			return nil
		}

		var subs []UserSubscription
		if err := lockForUpdate(tx).
			Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
			Order("end_time asc, id asc").
			Find(&subs).Error; err != nil {
			return errors.New("no active subscription")
		}
		if len(subs) == 0 {
			return errors.New("no active subscription")
		}
		for _, candidate := range subs {
			sub := candidate
			if sub.AmountUsed < 0 {
				return errors.New("subscription used amount is invalid")
			}
			plan, err := getSubscriptionResetPlanTx(tx, &sub)
			if err != nil {
				return err
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, plan, now); err != nil {
				return err
			}
			usedBefore := sub.AmountUsed
			totalUsedBefore := sub.AmountUsedTotal
			if totalUsedBefore < usedBefore {
				totalUsedBefore = usedBefore
			}
			if amount > math.MaxInt64-usedBefore {
				return errors.New("subscription used amount overflow")
			}
			if amount > math.MaxInt64-totalUsedBefore {
				return errors.New("subscription cumulative used amount overflow")
			}
			usedAfter := usedBefore + amount
			if sub.AmountTotal > 0 {
				if usedAfter > sub.AmountTotal {
					continue
				}
			}
			record := &SubscriptionPreConsumeRecord{
				RequestId:          requestId,
				UserId:             userId,
				UserSubscriptionId: sub.Id,
				PreConsumed:        amount,
				Status:             "consumed",
			}
			createResult := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "request_id"}},
				DoNothing: true,
			}).Create(record)
			if createResult.Error != nil {
				return createResult.Error
			}
			if createResult.RowsAffected == 0 {
				var dup SubscriptionPreConsumeRecord
				if err := tx.Where("request_id = ?", requestId).First(&dup).Error; err == nil {
					if dup.Status == "refunded" {
						return errors.New("subscription pre-consume already refunded")
					}
					returnValue.UserSubscriptionId = sub.Id
					returnValue.PreConsumed = dup.PreConsumed
					returnValue.AmountTotal = sub.AmountTotal
					returnValue.AmountUsedBefore = sub.AmountUsed
					returnValue.AmountUsedAfter = sub.AmountUsed
					return nil
				} else {
					return err
				}
			}
			sub.AmountUsed = usedAfter
			sub.AmountUsedTotal = totalUsedBefore + amount
			if err := tx.Save(&sub).Error; err != nil {
				return err
			}
			returnValue.UserSubscriptionId = sub.Id
			returnValue.PreConsumed = amount
			returnValue.AmountTotal = sub.AmountTotal
			returnValue.AmountUsedBefore = usedBefore
			returnValue.AmountUsedAfter = sub.AmountUsed
			return nil
		}
		return fmt.Errorf("subscription quota insufficient, need=%d", amount)
	})
	if err != nil {
		return nil, err
	}
	return returnValue, nil
}

// RefundSubscriptionPreConsume is idempotent and refunds pre-consumed subscription quota by requestId.
func RefundSubscriptionPreConsume(requestId string) error {
	if strings.TrimSpace(requestId) == "" {
		return errors.New("requestId is empty")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var record SubscriptionPreConsumeRecord
		if err := lockForUpdate(tx).
			Where("request_id = ?", requestId).First(&record).Error; err != nil {
			return err
		}
		if record.Status == "refunded" {
			return nil
		}
		if record.PreConsumed <= 0 {
			record.Status = "refunded"
			return tx.Save(&record).Error
		}
		if err := postConsumeUserSubscriptionDeltaTx(tx, record.UserSubscriptionId, -record.PreConsumed); err != nil {
			return err
		}
		record.Status = "refunded"
		return tx.Save(&record).Error
	})
}

// ResetDueSubscriptions resets subscriptions whose next_reset_time has passed.
func ResetDueSubscriptions(limit int) (int, error) {
	if limit <= 0 {
		limit = 200
	}
	now := GetDBTimestamp()
	var subs []UserSubscription
	if err := DB.Where("next_reset_time > 0 AND next_reset_time <= ? AND status = ?", now, "active").
		Order("next_reset_time asc").
		Limit(limit).
		Find(&subs).Error; err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		return 0, nil
	}
	resetCount := 0
	for _, sub := range subs {
		subCopy := sub
		plan, err := getSubscriptionResetPlanTx(nil, &subCopy)
		if err != nil || plan == nil {
			continue
		}
		err = DB.Transaction(func(tx *gorm.DB) error {
			var locked UserSubscription
			if err := lockForUpdate(tx).
				Where("id = ? AND next_reset_time > 0 AND next_reset_time <= ?", subCopy.Id, now).
				First(&locked).Error; err != nil {
				return nil
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &locked, plan, now); err != nil {
				return err
			}
			resetCount++
			return nil
		})
		if err != nil {
			return resetCount, err
		}
	}
	return resetCount, nil
}

// CleanupSubscriptionPreConsumeRecords removes old idempotency records to keep table small.
func CleanupSubscriptionPreConsumeRecords(olderThanSeconds int64) (int64, error) {
	if olderThanSeconds <= 0 {
		olderThanSeconds = 7 * 24 * 3600
	}
	cutoff := GetDBTimestamp() - olderThanSeconds
	openReservations := DB.Model(&BillingReservation{}).
		Select("request_id").
		Where("status IN ?", []string{BillingReservationStatusInitializing, BillingReservationStatusReserved})
	res := DB.Where("updated_at < ?", cutoff).
		Where("request_id NOT IN (?)", openReservations).
		Delete(&SubscriptionPreConsumeRecord{})
	return res.RowsAffected, res.Error
}

type SubscriptionPlanInfo struct {
	PlanId    int
	PlanTitle string
}

func GetSubscriptionPlanInfoByUserSubscriptionId(userSubscriptionId int) (*SubscriptionPlanInfo, error) {
	if userSubscriptionId <= 0 {
		return nil, errors.New("invalid userSubscriptionId")
	}
	cacheKey := fmt.Sprintf("sub:%d", userSubscriptionId)
	if cached, found, err := getSubscriptionPlanInfoCache().Get(cacheKey); err == nil && found {
		return &cached, nil
	}
	var sub UserSubscription
	if err := DB.Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
		return nil, err
	}
	plan, err := getSubscriptionPlanByIdTx(nil, sub.PlanId)
	if err != nil {
		return nil, err
	}
	info := &SubscriptionPlanInfo{
		PlanId:    sub.PlanId,
		PlanTitle: plan.Title,
	}
	_ = getSubscriptionPlanInfoCache().SetWithTTL(cacheKey, *info, subscriptionPlanInfoCacheTTL())
	return info, nil
}

// Update subscription used amount by delta (positive consume more, negative refund).
func PostConsumeUserSubscriptionDelta(userSubscriptionId int, delta int64) error {
	if userSubscriptionId <= 0 {
		return errors.New("invalid userSubscriptionId")
	}
	if delta == 0 {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		return postConsumeUserSubscriptionDeltaTx(tx, userSubscriptionId, delta)
	})
}

func postConsumeUserSubscriptionDeltaTx(tx *gorm.DB, userSubscriptionId int, delta int64) error {
	if tx == nil || userSubscriptionId <= 0 {
		return errors.New("invalid subscription delta args")
	}
	var sub UserSubscription
	if err := lockForUpdate(tx).
		Where("id = ?", userSubscriptionId).
		First(&sub).Error; err != nil {
		return err
	}
	if delta > 0 && sub.AmountUsed > math.MaxInt64-delta {
		return errors.New("subscription used amount overflow")
	}
	newUsed := sub.AmountUsed + delta
	if newUsed < 0 {
		newUsed = 0
	}
	if sub.AmountTotal > 0 && newUsed > sub.AmountTotal {
		return fmt.Errorf("subscription used exceeds total, used=%d total=%d", newUsed, sub.AmountTotal)
	}
	totalUsed := sub.AmountUsedTotal
	if totalUsed < sub.AmountUsed {
		totalUsed = sub.AmountUsed
	}
	if delta > 0 && totalUsed > math.MaxInt64-delta {
		return errors.New("subscription cumulative used amount overflow")
	}
	newTotalUsed := totalUsed + delta
	if newTotalUsed < 0 {
		return errors.New("subscription cumulative used amount underflow")
	}
	sub.AmountUsed = newUsed
	sub.AmountUsedTotal = newTotalUsed
	return tx.Save(&sub).Error
}
