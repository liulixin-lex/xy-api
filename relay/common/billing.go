package common

import "github.com/gin-gonic/gin"

// BillingSettler 抽象计费会话的生命周期操作。
// 由 service.BillingSession 实现，存储在 RelayInfo 上以避免循环引用。
type BillingSettler interface {
	// Settle 根据实际消耗额度进行结算，计算 delta = actualQuota - preConsumedQuota，
	// 同时调整资金来源（钱包/订阅）和令牌额度。
	Settle(actualQuota int) error

	// Refund 退还所有预扣费额度（资金来源 + 令牌），幂等安全。
	// 同步持久化执行，返回前确保退款已提交。已结算、已退款或已记录实际用量结算意图时不退款。
	Refund(c *gin.Context)

	// NeedsRefund 返回会话是否存在需要退还的预扣状态（未结算、未退款且无待处理实际用量）。
	NeedsRefund() bool

	// GetPreConsumedQuota 返回实际预扣的额度值（信任用户可能为 0）。
	GetPreConsumedQuota() int

	// Reserve 将预扣额度补到目标值；若目标值不高于当前预扣额度则不做任何事。
	Reserve(targetQuota int) error
}
