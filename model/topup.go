package model

import (
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type TopUp struct {
	Id                    int     `json:"id"`
	PaymentOrderId        *int64  `json:"payment_order_id,omitempty" gorm:"index"`
	UserId                int     `json:"user_id" gorm:"index"`
	Amount                int64   `json:"amount"`
	Money                 float64 `json:"money"`
	TradeNo               string  `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod         string  `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider       string  `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	CreateTime            int64   `json:"create_time"`
	CompleteTime          int64   `json:"complete_time"`
	Status                string  `json:"status"`
	Provider              string  `json:"provider,omitempty" gorm:"-"`
	OrderKind             string  `json:"order_kind,omitempty" gorm:"-"`
	Currency              string  `json:"currency,omitempty" gorm:"-"`
	CreditQuota           int64   `json:"credit_quota,omitempty" gorm:"-"`
	ExpectedAmountMinor   int64   `json:"expected_amount_minor,omitempty" gorm:"-"`
	PaidAmountMinor       int64   `json:"paid_amount_minor,omitempty" gorm:"-"`
	RefundedAmountMinor   int64   `json:"refunded_amount_minor,omitempty" gorm:"-"`
	DisputedAmountMinor   int64   `json:"disputed_amount_minor,omitempty" gorm:"-"`
	ReversedAmountMinor   int64   `json:"reversed_amount_minor,omitempty" gorm:"-"`
	CanonicalOrderVersion int64   `json:"canonical_order_version,omitempty" gorm:"-"`
	StatusReason          string  `json:"status_reason,omitempty" gorm:"-"`
}

const (
	PaymentMethodStripe       = "stripe"
	PaymentMethodCreem        = "creem"
	PaymentMethodWaffo        = "waffo"
	PaymentMethodWaffoPancake = "waffo_pancake"
	PaymentMethodXorPayNative = "xorpay_native"
	PaymentMethodXorPayAlipay = "xorpay_alipay"
	PaymentMethodBalance      = "balance"
)

const (
	PaymentProviderEpay         = "epay"
	PaymentProviderStripe       = "stripe"
	PaymentProviderCreem        = "creem"
	PaymentProviderWaffo        = "waffo"
	PaymentProviderWaffoPancake = "waffo_pancake"
	PaymentProviderXorPay       = "xorpay"
	PaymentProviderBalance      = "balance"
)

var (
	ErrPaymentMethodMismatch = errors.New("payment method mismatch")
	ErrTopUpNotFound         = errors.New("topup not found")
	ErrTopUpStatusInvalid    = errors.New("topup status invalid")
)

func (topUp *TopUp) Insert() error {
	var err error
	err = DB.Create(topUp).Error
	return err
}

func (topUp *TopUp) Update() error {
	var err error
	err = DB.Save(topUp).Error
	return err
}

func GetTopUpById(id int) *TopUp {
	var topUp *TopUp
	var err error
	err = DB.Where("id = ?", id).First(&topUp).Error
	if err != nil {
		return nil
	}
	return topUp
}

func GetTopUpByTradeNo(tradeNo string) *TopUp {
	var topUp *TopUp
	var err error
	err = DB.Where("trade_no = ?", tradeNo).First(&topUp).Error
	if err != nil {
		return nil
	}
	return topUp
}

func UpdatePendingTopUpStatus(tradeNo string, expectedPaymentProvider string, targetStatus string) error {
	if tradeNo == "" {
		return errors.New("未提供支付单号")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if expectedPaymentProvider != "" && topUp.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}

		topUp.Status = targetStatus
		return tx.Save(topUp).Error
	})
}

// topUpQueryWindowSeconds 限制充值记录查询的时间窗口（秒）。
const topUpQueryWindowSeconds int64 = 30 * 24 * 60 * 60

// topUpQueryCutoff 返回允许查询的最早 create_time（秒级 Unix 时间戳）。
func topUpQueryCutoff() int64 {
	return common.GetTimestamp() - topUpQueryWindowSeconds
}

func GetUserTopUps(userId int, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	// Start transaction
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	cutoff := topUpQueryCutoff()

	// Get total count within transaction
	err = tx.Model(&TopUp{}).Where("user_id = ? AND create_time >= ?", userId, cutoff).Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Get paginated topups within same transaction
	err = tx.Where("user_id = ? AND create_time >= ?", userId, cutoff).Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Commit transaction
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	if err := hydrateTopUpPaymentOrders(topups); err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

// GetAllTopUps 获取全平台的充值记录（管理员使用，不限制时间窗口）
func GetAllTopUps(pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if err = tx.Model(&TopUp{}).Count(&total).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	if err := hydrateTopUpPaymentOrders(topups); err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

// searchTopUpCountHardLimit 搜索充值记录时 COUNT 的安全上限，
// 防止对超大表执行无界 COUNT 触发 DoS。
const searchTopUpCountHardLimit = 10000

// SearchUserTopUps 按订单号搜索某用户的充值记录
func SearchUserTopUps(userId int, keyword string, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&TopUp{}).Where("user_id = ? AND create_time >= ?", userId, topUpQueryCutoff())
	if keyword != "" {
		pattern, perr := sanitizeLikePattern(keyword)
		if perr != nil {
			tx.Rollback()
			return nil, 0, perr
		}
		query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
	}

	if err = query.Limit(searchTopUpCountHardLimit).Count(&total).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to count search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	if err := hydrateTopUpPaymentOrders(topups); err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

// SearchAllTopUps 按订单号搜索全平台充值记录（管理员使用，不限制时间窗口）
func SearchAllTopUps(keyword string, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&TopUp{})
	if keyword != "" {
		pattern, perr := sanitizeLikePattern(keyword)
		if perr != nil {
			tx.Rollback()
			return nil, 0, perr
		}
		query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
	}

	if err = query.Limit(searchTopUpCountHardLimit).Count(&total).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to count search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	if err := hydrateTopUpPaymentOrders(topups); err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

func hydrateTopUpPaymentOrders(topups []*TopUp) error {
	if len(topups) == 0 {
		return nil
	}
	orderIDs := make([]int64, 0, len(topups))
	tradeNos := make([]string, 0, len(topups))
	for _, topUp := range topups {
		if topUp == nil {
			continue
		}
		if topUp.PaymentOrderId != nil && *topUp.PaymentOrderId > 0 {
			orderIDs = append(orderIDs, *topUp.PaymentOrderId)
		}
		if topUp.TradeNo != "" {
			tradeNos = append(tradeNos, topUp.TradeNo)
		}
	}
	if len(orderIDs) == 0 && len(tradeNos) == 0 {
		return nil
	}
	var orders []PaymentOrder
	query := DB.Model(&PaymentOrder{})
	if len(orderIDs) > 0 && len(tradeNos) > 0 {
		query = query.Where("id IN ? OR trade_no IN ?", orderIDs, tradeNos)
	} else if len(orderIDs) > 0 {
		query = query.Where("id IN ?", orderIDs)
	} else {
		query = query.Where("trade_no IN ?", tradeNos)
	}
	if err := query.Find(&orders).Error; err != nil {
		return err
	}
	byID := make(map[int64]*PaymentOrder, len(orders))
	byTradeNo := make(map[string]*PaymentOrder, len(orders))
	for i := range orders {
		order := &orders[i]
		byID[order.ID] = order
		byTradeNo[order.TradeNo] = order
	}
	for _, topUp := range topups {
		if topUp == nil {
			continue
		}
		var order *PaymentOrder
		if topUp.PaymentOrderId != nil {
			order = byID[*topUp.PaymentOrderId]
		}
		if order == nil {
			order = byTradeNo[topUp.TradeNo]
		}
		if order == nil {
			continue
		}
		topUp.PaymentOrderId = &order.ID
		topUp.Status = paymentOrderProjectionStatus(order.Status)
		topUp.Provider = order.Provider
		topUp.PaymentProvider = order.Provider
		topUp.OrderKind = order.OrderKind
		topUp.Currency = order.Currency
		topUp.CreditQuota = order.CreditQuota
		topUp.ExpectedAmountMinor = order.ExpectedAmountMinor
		topUp.PaidAmountMinor = order.PaidAmountMinor
		topUp.RefundedAmountMinor = order.RefundedAmountMinor
		topUp.DisputedAmountMinor = order.DisputedAmountMinor
		topUp.ReversedAmountMinor = order.ReversedAmountMinor
		topUp.CanonicalOrderVersion = order.Version
		switch order.Status {
		case PaymentOrderStatusManualReview:
			topUp.StatusReason = "Payment requires manual review"
		case PaymentOrderStatusFailed:
			topUp.StatusReason = "Payment provider reported a failure"
		case PaymentOrderStatusExpired:
			topUp.StatusReason = "Payment session expired"
		case PaymentOrderStatusDebt:
			topUp.StatusReason = "Payment reversal left an outstanding balance"
		}
	}
	return nil
}

func RechargeCreem(referenceId string, customerEmail string, customerName string, callerIp string) (err error) {
	if referenceId == "" {
		return errors.New("未提供支付单号")
	}

	var quota int
	var affiliateReward int
	var affiliateInviterId int
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", referenceId).First(topUp).Error
		if err != nil {
			return errors.New("充值订单不存在")
		}

		if topUp.PaymentProvider != PaymentProviderCreem {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("充值订单状态错误")
		}

		// Creem 直接使用 Amount 作为充值额度（整数）
		quota = topUpQuotaFromDecimal(decimal.NewFromInt(topUp.Amount))
		topUp.CompleteTime = common.GetTimestamp()
		var rewardErr error
		affiliateInviterId, affiliateReward, rewardErr = applyAffiliateTopUpRewardTx(tx, topUp, quota)
		if rewardErr != nil {
			return rewardErr
		}

		topUp.Status = common.TopUpStatusSuccess
		err = tx.Save(topUp).Error
		if err != nil {
			return err
		}

		// 构建更新字段，优先使用邮箱，如果邮箱为空则使用用户名
		updateFields := map[string]interface{}{
			"quota": gorm.Expr("quota + ?", quota),
		}

		// 如果有客户邮箱，尝试更新用户邮箱（仅当用户邮箱为空时）
		if customerEmail != "" {
			// 先检查用户当前邮箱是否为空
			var user User
			err = tx.Where("id = ?", topUp.UserId).First(&user).Error
			if err != nil {
				return err
			}

			// 如果用户邮箱为空，则更新为支付时使用的邮箱
			if user.Email == "" {
				updateFields["email"] = customerEmail
			}
		}

		err = tx.Model(&User{}).Where("id = ?", topUp.UserId).Updates(updateFields).Error
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		common.SysError("creem topup failed: " + err.Error())
		return errors.New("充值失败，请稍后重试")
	}

	RecordTopupLog(topUp.UserId, fmt.Sprintf("使用Creem充值成功，充值额度: %v，支付金额：%.2f", quota, topUp.Money), callerIp, topUp.PaymentMethod, PaymentMethodCreem)
	recordAffiliateTopUpRewardLog(affiliateInviterId, affiliateReward)

	return nil
}

func RechargeWaffo(tradeNo string, callerIp string) (err error) {
	if tradeNo == "" {
		return errors.New("未提供支付单号")
	}

	var quotaToAdd int
	var affiliateReward int
	var affiliateInviterId int
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error
		if err != nil {
			return errors.New("充值订单不存在")
		}

		if topUp.PaymentProvider != PaymentProviderWaffo {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status == common.TopUpStatusSuccess {
			return nil // 幂等：已成功直接返回
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("充值订单状态错误")
		}

		dAmount := decimal.NewFromInt(topUp.Amount)
		dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		quotaToAdd = topUpQuotaFromDecimal(dAmount.Mul(dQuotaPerUnit))
		if quotaToAdd <= 0 {
			return errors.New("无效的充值额度")
		}

		topUp.CompleteTime = common.GetTimestamp()
		var rewardErr error
		affiliateInviterId, affiliateReward, rewardErr = applyAffiliateTopUpRewardTx(tx, topUp, quotaToAdd)
		if rewardErr != nil {
			return rewardErr
		}

		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		if err := tx.Model(&User{}).Where("id = ?", topUp.UserId).Update("quota", gorm.Expr("quota + ?", quotaToAdd)).Error; err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		common.SysError("waffo topup failed: " + err.Error())
		return errors.New("充值失败，请稍后重试")
	}

	if quotaToAdd > 0 {
		RecordTopupLog(topUp.UserId, fmt.Sprintf("Waffo充值成功，充值额度: %v，支付金额: %.2f", logger.FormatQuota(quotaToAdd), topUp.Money), callerIp, topUp.PaymentMethod, PaymentMethodWaffo)
		recordAffiliateTopUpRewardLog(affiliateInviterId, affiliateReward)
	}

	return nil
}

func RechargeWaffoPancake(tradeNo string) (err error) {
	if tradeNo == "" {
		return errors.New("未提供支付单号")
	}

	var quotaToAdd int
	var affiliateReward int
	var affiliateInviterId int
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error
		if err != nil {
			return errors.New("充值订单不存在")
		}

		if topUp.PaymentProvider != PaymentProviderWaffoPancake {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("充值订单状态错误")
		}

		quotaToAdd = topUpQuotaFromDecimal(decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)))
		if quotaToAdd <= 0 {
			return errors.New("无效的充值额度")
		}

		topUp.CompleteTime = common.GetTimestamp()
		var rewardErr error
		affiliateInviterId, affiliateReward, rewardErr = applyAffiliateTopUpRewardTx(tx, topUp, quotaToAdd)
		if rewardErr != nil {
			return rewardErr
		}

		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		if err := tx.Model(&User{}).Where("id = ?", topUp.UserId).Update("quota", gorm.Expr("quota + ?", quotaToAdd)).Error; err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		common.SysError("waffo pancake topup failed: " + err.Error())
		return errors.New("充值失败，请稍后重试")
	}

	if quotaToAdd > 0 {
		RecordLog(topUp.UserId, LogTypeTopup, fmt.Sprintf("Waffo Pancake充值成功，充值额度: %v，支付金额: %.2f", logger.FormatQuota(quotaToAdd), topUp.Money))
		recordAffiliateTopUpRewardLog(affiliateInviterId, affiliateReward)
	}

	return nil
}

func applyAffiliateTopUpRewardTx(tx *gorm.DB, topUp *TopUp, quotaToAdd int) (int, int, error) {
	if topUp == nil || quotaToAdd <= 0 {
		return 0, 0, nil
	}
	quotaToAdd = common.QuotaFromFloat(float64(quotaToAdd))

	var user User
	if err := lockForUpdate(tx).
		Select("id", "inviter_id", "invite_reward_rule", "invite_reward_percent", "invite_link_batch_id", "invite_first_topup_reward_percent", "invite_continuous_reward_percent", "invite_reward_rules_snapshot").
		Where("id = ?", topUp.UserId).
		First(&user).Error; err != nil {
		return 0, 0, err
	}
	if user.InviterId == 0 {
		return 0, 0, nil
	}

	if user.InviteLinkBatchId > 0 {
		return createInviteLinkBatchTopUpRewardTx(tx, topUp, user, quotaToAdd)
	}

	rule := NormalizeInviteRewardRule(user.InviteRewardRule)
	if rule == InviteRewardRuleFirstTopUp {
		var successCount int64
		if err := tx.Model(&TopUp{}).Where("user_id = ? AND status = ? AND amount > ?", topUp.UserId, common.TopUpStatusSuccess, 0).Count(&successCount).Error; err != nil {
			return 0, 0, err
		}
		if successCount != 0 {
			return 0, 0, nil
		}
		claimed, err := claimInviteFirstTopupRewardTx(tx, user.Id)
		if err != nil {
			return 0, 0, err
		}
		if !claimed {
			return 0, 0, nil
		}
	}

	rewardPercent := ResolveInviteRewardPercent(user.InviteRewardRule, user.InviteRewardPercent)
	reward := affiliateRewardQuota(quotaToAdd, rewardPercent)
	if reward <= 0 {
		return 0, 0, nil
	}

	err := tx.Create(&AffiliateRewardRecord{
		InviterId:           user.InviterId,
		InviteeId:           user.Id,
		TopUpId:             topUp.Id,
		InviteRewardRule:    rule,
		InviteRewardPercent: rewardPercent,
		TopUpQuota:          quotaToAdd,
		RewardQuota:         reward,
		Status:              AffiliateRewardStatusAvailable,
		AvailableAt:         common.GetTimestamp(),
		CreatedAt:           common.GetTimestamp(),
	}).Error
	if err != nil {
		return 0, 0, err
	}

	result := tx.Model(&User{}).Where("id = ? AND aff_quota <= ? AND aff_history <= ?", user.InviterId, math.MaxInt32-reward, math.MaxInt32-reward).Updates(map[string]interface{}{
		"aff_quota":   gorm.Expr("aff_quota + ?", reward),
		"aff_history": gorm.Expr("aff_history + ?", reward),
	})
	if result.Error != nil {
		return 0, 0, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, 0, ErrQuotaOverflow
	}

	return user.InviterId, reward, nil
}

func createInviteLinkBatchTopUpRewardTx(tx *gorm.DB, topUp *TopUp, user User, quotaToAdd int) (int, int, error) {
	quotaToAdd = common.QuotaFromFloat(float64(quotaToAdd))
	recordCreatedAt := common.GetTimestamp()
	rewardAvailableAt := topUp.CompleteTime + AffiliateRewardWaitSeconds
	if topUp.CompleteTime <= 0 {
		rewardAvailableAt = recordCreatedAt + AffiliateRewardWaitSeconds
	}

	var successCount int64
	if err := tx.Model(&TopUp{}).Where("user_id = ? AND status = ? AND amount > ?", user.Id, common.TopUpStatusSuccess, 0).Count(&successCount).Error; err != nil {
		return 0, 0, err
	}
	activities := user.InviteRewardRulesSnapshot
	if len(activities) == 0 {
		activities = inviteRewardActivitiesFromSnapshotPercents(
			user.InviteFirstTopupRewardPercent,
			user.InviteContinuousRewardPercent,
		)
	}

	activityType := InviteRewardRuleContinuous
	if successCount == 0 {
		claimed, err := claimInviteFirstTopupRewardTx(tx, user.Id)
		if err != nil {
			return 0, 0, err
		}
		if claimed {
			activityType = InviteRewardRuleFirstTopUp
		}
	}

	applicableActivities := applicableInviteRewardActivities(activities, activityType)
	if len(applicableActivities) == 0 {
		return 0, 0, nil
	}

	totalReward := int64(0)
	for _, activity := range applicableActivities {
		rewardPercent := activity.Percent
		reward := affiliateRewardQuota(quotaToAdd, rewardPercent)
		if reward <= 0 {
			continue
		}

		err := tx.Create(&AffiliateRewardRecord{
			InviterId:           user.InviterId,
			InviteeId:           user.Id,
			TopUpId:             topUp.Id,
			InviteLinkBatchId:   user.InviteLinkBatchId,
			ActivityDetail:      activity.ActivityDetail,
			InviteRewardRule:    activity.Type,
			InviteRewardPercent: rewardPercent,
			TopUpQuota:          quotaToAdd,
			RewardQuota:         reward,
			Status:              AffiliateRewardStatusPending,
			AvailableAt:         rewardAvailableAt,
			CreatedAt:           recordCreatedAt,
		}).Error
		if err != nil {
			return 0, 0, err
		}
		if int64(reward) > int64(common.MaxQuota)-totalReward {
			return 0, 0, ErrQuotaOverflow
		}
		totalReward += int64(reward)
	}

	return user.InviterId, int(totalReward), nil
}

func topUpQuotaFromDecimal(value decimal.Decimal) int {
	f, _ := value.Float64()
	return common.QuotaFromFloat(f)
}

func affiliateRewardQuota(quotaToAdd int, rewardPercent int) int {
	if quotaToAdd <= 0 || rewardPercent <= 0 {
		return 0
	}
	reward := decimal.NewFromInt(int64(quotaToAdd)).
		Mul(decimal.NewFromInt(int64(rewardPercent))).
		Div(decimal.NewFromInt(100))
	return topUpQuotaFromDecimal(reward)
}

func inviteRewardActivitiesFromSnapshotPercents(firstTopupPercent int, continuousPercent int) InviteRewardActivities {
	result := make(InviteRewardActivities, 0, 2)
	if firstTopupPercent > 0 {
		result = append(result, InviteRewardActivity{
			ActivityDetail: "One-time Referral",
			Type:           InviteRewardRuleFirstTopUp,
			Percent:        normalizeRewardPercent(firstTopupPercent),
		})
	}
	if continuousPercent > 0 {
		result = append(result, InviteRewardActivity{
			ActivityDetail: "Continuous Referral",
			Type:           InviteRewardRuleContinuous,
			Percent:        normalizeRewardPercent(continuousPercent),
		})
	}
	return result
}

func applicableInviteRewardActivities(activities InviteRewardActivities, activityType string) InviteRewardActivities {
	activities = NormalizeInviteRewardActivities(activities)
	firstTopupActivities := make(InviteRewardActivities, 0)
	continuousActivities := make(InviteRewardActivities, 0)
	for _, activity := range activities {
		switch activity.Type {
		case InviteRewardRuleFirstTopUp:
			firstTopupActivities = append(firstTopupActivities, activity)
		case InviteRewardRuleContinuous:
			continuousActivities = append(continuousActivities, activity)
		}
	}
	if activityType == InviteRewardRuleFirstTopUp && len(firstTopupActivities) > 0 {
		return firstTopupActivities
	}
	if activityType == InviteRewardRuleFirstTopUp {
		return continuousActivities
	}
	return continuousActivities
}

func claimInviteFirstTopupRewardTx(tx *gorm.DB, userId int) (bool, error) {
	result := tx.Model(&User{}).
		Where("id = ? AND invite_first_topup_rewarded_at = ?", userId, 0).
		Update("invite_first_topup_rewarded_at", common.GetTimestamp())
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func recordAffiliateTopUpRewardLog(inviterId int, reward int) {
	if inviterId == 0 || reward <= 0 {
		return
	}
	RecordLog(inviterId, LogTypeSystem, fmt.Sprintf("推荐用户充值奖励 %s", logger.LogQuota(reward)))
}
