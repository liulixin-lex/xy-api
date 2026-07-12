package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

type routingAttemptGuard struct {
	mu          sync.Mutex
	policy      channelrouting.AttemptPolicy
	coordinator *channelrouting.AttemptCoordinator
	bypass      bool
}

func newRoutingAttemptGuard(c *gin.Context, info *relaycommon.RelayInfo) *routingAttemptGuard {
	setting := smart_routing_setting.GetSetting()
	if !setting.Enabled || (setting.Mode != smart_routing_setting.ModeBalanced && setting.Mode != smart_routing_setting.ModeEnterpriseSLO) {
		return nil
	}

	maxRetries := common.RetryTimes
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > setting.MaxSwitches {
		maxRetries = setting.MaxSwitches
	}
	now := time.Now()
	requestContext := context.Background()
	if c != nil && c.Request != nil {
		requestContext = c.Request.Context()
	}
	baselineCost := routingAttemptCostUnits(info)
	return &routingAttemptGuard{policy: channelrouting.AttemptPolicy{
		MaxAttempts:          maxRetries + 1,
		Deadline:             channelrouting.AttemptDeadline(requestContext, now, time.Duration(setting.FailoverDeadlineMs)*time.Millisecond),
		ExtraCostBudgetUnits: channelrouting.AttemptExtraCostBudget(baselineCost, setting.RetryExtraCostMultiplier),
		RetryTokenCapacity:   setting.RetryTokenCapacity,
		RetryTokenRefill:     setting.RetryTokenRefillPerSec,
	}}
}

func (guard *routingAttemptGuard) Begin(c *gin.Context, info *relaycommon.RelayInfo) (*channelrouting.AttemptLease, error) {
	if guard == nil {
		return nil, nil
	}
	poolID := 0
	if c != nil {
		poolID = common.GetContextKeyInt(c, constant.ContextKeyRoutingPoolID)
	}
	guard.mu.Lock()
	if guard.bypass {
		guard.mu.Unlock()
		return nil, nil
	}
	if poolID <= 0 {
		coordinator := guard.coordinator
		guard.coordinator = nil
		guard.bypass = true
		guard.mu.Unlock()
		if coordinator != nil {
			coordinator.Complete()
		}
		return nil, nil
	}
	if guard.coordinator == nil {
		guard.coordinator = channelrouting.NewAttemptCoordinator(guard.policy)
	}
	coordinator := guard.coordinator
	guard.mu.Unlock()
	return coordinator.BeginAttempt(channelrouting.AttemptInput{
		PoolID:             poolID,
		EstimatedCostUnits: routingAttemptCostUnits(info),
	})
}

func (guard *routingAttemptGuard) Complete() {
	if guard == nil {
		return
	}
	guard.mu.Lock()
	coordinator := guard.coordinator
	guard.coordinator = nil
	guard.bypass = true
	guard.mu.Unlock()
	if coordinator != nil {
		coordinator.Complete()
	}
}

func routingAttemptCostUnits(info *relaycommon.RelayInfo) int64 {
	if info == nil {
		return 1
	}
	cost := info.PriceData.QuotaToPreConsume
	if info.PriceData.Quota > cost {
		cost = info.PriceData.Quota
	}
	if cost < 1 {
		return 1
	}
	return int64(cost)
}

func routingAttemptClientCommitted(c *gin.Context, info *relaycommon.RelayInfo) bool {
	if info == nil {
		return c != nil && c.Writer != nil && c.Writer.Written()
	}
	if info.RelayFormat == types.RelayFormatOpenAIRealtime {
		return info.ReceivedResponseCount > 0 || info.HasSendResponse()
	}
	if info.IsStream || info.StreamStatus != nil {
		return info.HTTPStreamClientCommitted(c)
	}
	return (c != nil && c.Writer != nil && c.Writer.Written()) ||
		info.SendResponseCount > 0 || info.ReceivedResponseCount > 0 || info.HasSendResponse()
}

func routingAttemptRejectionError(err error) *types.NewAPIError {
	statusCode := http.StatusServiceUnavailable
	if errors.Is(err, channelrouting.ErrAttemptDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		statusCode = http.StatusGatewayTimeout
	}
	return types.NewErrorWithStatusCode(
		fmt.Errorf("channel routing attempt rejected before send: %w", err),
		types.ErrorCodeGetChannelFailed,
		statusCode,
		types.ErrOptionWithSkipRetry(),
	)
}
