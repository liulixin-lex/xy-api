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
	"github.com/QuantumNous/new-api/model"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
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
		return (c != nil && c.Writer != nil && c.Writer.Written()) ||
			info.SendResponseCount > 0 || info.ReceivedResponseCount > 0 || info.HasSendResponse()
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

func reserveRoutingSerialAttemptAudit(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	channel *model.Channel,
) (*channelrouting.HedgeAttemptAuditReservation, error) {
	if c == nil || info == nil || channel == nil || channel.Id <= 0 || info.RequestId == "" {
		return nil, nil
	}
	revision := service.RoutingHedgePolicyRevision(c)
	poolID := service.RoutingHedgePoolID(c)
	memberID := service.RoutingHedgeMemberID(c)
	if revision == 0 || poolID <= 0 || memberID <= 0 {
		return nil, nil
	}
	cost, costKnown, err := service.ChannelRoutingHedgeCostEstimate(
		c,
		channel.Id,
		info.OriginModelName,
		info.RequestURLPath,
		info.RetryIndex,
	)
	if err != nil {
		return nil, err
	}
	stableNodeID, stableNodeKnown := channelrouting.StableNodeID()
	algorithmVersion := common.GetContextKeyString(c, constant.ContextKeyRoutingAlgorithmVersion)
	if algorithmVersion == "" {
		algorithmVersion = channelrouting.DecisionAlgorithmBalancedV1
	}
	startedAt := info.RoutingAttemptStartTime()
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return channelrouting.ReserveUpstreamAttemptAudit(model.RoutingHedgeAttemptStartSpec{
		DecisionID:        common.GetContextKeyString(c, constant.ContextKeyRoutingDecisionID),
		RequestID:         info.RequestId,
		NodeEpochID:       channelrouting.NodeEpochID(),
		StableNodeID:      stableNodeID,
		StableNodeKnown:   stableNodeKnown,
		PolicyRevision:    revision,
		AlgorithmVersion:  algorithmVersion,
		PoolID:            poolID,
		MemberID:          memberID,
		ChannelID:         channel.Id,
		CredentialID:      service.RoutingHedgeCredentialID(c),
		ModelName:         info.OriginModelName,
		ExecutionMode:     model.RoutingAttemptExecutionSerial,
		AttemptIndex:      info.RetryIndex,
		Role:              model.RoutingAttemptRoleSerial,
		EndpointAuthority: common.GetContextKeyString(c, constant.ContextKeyRoutingEndpointAuthority),
		Region:            common.GetContextKeyString(c, constant.ContextKeyRoutingRegion),
		StartedTimeMs:     startedAt.UnixMilli(),
		Cost:              routingAttemptAuditCostSpec(cost, costKnown),
	})
}

func completeRoutingSerialAttemptAudit(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	channel *model.Channel,
	audit *channelrouting.HedgeAttemptAuditReservation,
	success bool,
	apiErr *types.NewAPIError,
	classification routingerror.Classification,
	upstreamSent bool,
	clientCommitted bool,
	willRetry bool,
) error {
	if audit == nil || info == nil || channel == nil {
		return nil
	}
	completedAt := time.Now()
	completion := model.RoutingHedgeAttemptCompleteSpec{
		Result:          model.RoutingHedgeAttemptResultUpstreamError,
		Winner:          success,
		UpstreamSent:    upstreamSent,
		ClientCommitted: clientCommitted,
		WillRetry:       willRetry,
		FinalAttempt:    !willRetry,
		CompletedTimeMs: completedAt.UnixMilli(),
	}
	if success {
		completion.Result = model.RoutingHedgeAttemptResultSuccess
		completion.HTTPStatus = http.StatusOK
	} else {
		if !upstreamSent {
			completion.Result = model.RoutingHedgeAttemptResultInternalError
		}
		completion.HTTPStatus = sourceRoutingHedgeStatus(apiErr)
		completion.ErrorClassification = classification.Rule
		completion.ErrorResponsibility = string(classification.Responsibility)
		completion.ErrorRetryability = string(classification.Retryability)
		completion.ErrorCode = routingHedgeErrorCode(apiErr)
		if errors.Is(context.Cause(c.Request.Context()), context.Canceled) ||
			errors.Is(context.Cause(c.Request.Context()), context.DeadlineExceeded) {
			completion.Result = model.RoutingHedgeAttemptResultClientCanceled
		}
	}
	if !info.FirstResponseTime.IsZero() {
		completion.FirstByteTimeMs = info.FirstResponseTime.UnixMilli()
	}
	if usage, known := info.RoutingAttemptUsageSnapshot(); known {
		if usageDTO, ok := usage.DTO(); ok {
			actual, err := service.ChannelRoutingHedgeActualCost(
				c,
				channel.Id,
				info.OriginModelName,
				info.RequestURLPath,
				info.RetryIndex,
				usageDTO,
			)
			if err == nil && actual.Known {
				completion.ActualCostKnown = true
				completion.ActualCost = actual.Cost
				completion.ActualPromptTokens = actual.PromptTokens
				completion.ActualCompletionTokens = actual.CompletionTokens
				completion.ActualTotalTokens = actual.TotalTokens
				completion.ActualCacheReadTokens = actual.CacheReadTokens
				completion.ActualCacheWriteTokens = actual.CacheWriteTokens
				completion.ActualCacheWrite1hTokens = actual.CacheWrite1hTokens
			}
		}
	}
	return audit.Complete(completion)
}

func routingAttemptAuditCostSpec(
	cost channelrouting.ShadowCostInput,
	known bool,
) model.RoutingHedgeAttemptCostSpec {
	if !known {
		return model.RoutingHedgeAttemptCostSpec{}
	}
	return model.RoutingHedgeAttemptCostSpec{
		Known:        true,
		ExpectedCost: cost.Cost, WorstCaseCost: cost.WorstCaseCost,
		EffectiveCost: cost.EffectiveCost, Currency: cost.Currency, Unit: cost.Unit,
		PricingBasis: cost.PricingBasis, PricingHash: cost.PricingHash,
		PricingVersion: cost.PricingVersion, ConfidenceScore: cost.ConfidenceScore,
		FreshnessScore: cost.FreshnessScore, ExpectedBreakdown: cost.ExpectedBreakdown,
		WorstSingleBreakdown: cost.WorstSingleBreakdown, ObservedTime: cost.ObservedTime,
		EffectiveTime: cost.EffectiveTime, ExpiresTime: cost.ExpiresTime,
		SourceSyncStatus: cost.SourceSyncStatus, AccountSourceType: cost.AccountSourceType,
		AccountReferenceHash: cost.AccountKeyHash,
	}
}
