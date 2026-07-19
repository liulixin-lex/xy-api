package service

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// BillingSession wraps one request's durable pre-consume, settle and refund
// lifecycle. Financial mutations are delegated to model.BillingReservation so
// wallet/subscription and token changes share one database transaction.
type BillingSession struct {
	relayInfo        *relaycommon.RelayInfo
	funding          FundingSource
	preConsumedQuota int
	tokenConsumed    int
	extraReserved    int
	settled          bool
	refunded         bool
	liabilityPending bool
	mu               sync.Mutex
}

// Settle commits the actual request quota exactly once.
func (s *BillingSession) Settle(actualQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refunded {
		return model.ErrBillingReservationFinalized
	}
	// Settle is called only after billable output exists. Even if persisting the
	// intent hits a transient database failure, generic error cleanup must not
	// refund usage that has already been delivered.
	s.liabilityPending = true
	if err := persistBillingSettlementIntent(s.relayInfo.RequestId, actualQuota); err != nil {
		return err
	}

	delta := actualQuota - s.preConsumedQuota
	wasSettled := s.settled
	var reservation *model.BillingReservation
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		reservation, err = model.SettleBillingReservation(s.relayInfo.RequestId, actualQuota, s.relayInfo.TokenKey)
		if err == nil ||
			errors.Is(err, model.ErrBillingReservationConflict) ||
			errors.Is(err, model.ErrBillingReservationFinalized) ||
			errors.Is(err, model.ErrBillingReservationNotFound) ||
			errors.Is(err, model.ErrInsufficientUserQuota) ||
			errors.Is(err, model.ErrInsufficientTokenQuota) ||
			errors.Is(err, model.ErrInsufficientSubscriptionQuota) ||
			errors.Is(err, model.ErrQuotaOverflow) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	if err != nil {
		if failureCode, deterministic := model.BillingSettlementFailureCode(err); deterministic {
			if markErr := persistBillingSettlementShortfall(s.relayInfo.RequestId, actualQuota, failureCode); markErr != nil {
				return errors.Join(err, fmt.Errorf("persist billing settlement shortfall: %w", markErr))
			}
		}
		return err
	}
	if !wasSettled && s.funding.Source() == BillingSourceSubscription {
		s.relayInfo.SubscriptionPostDelta += int64(delta)
	}
	s.preConsumedQuota = reservation.ReservedQuota
	s.tokenConsumed = reservation.TokenReserved
	s.settled = true
	s.liabilityPending = false
	return nil
}

// Refund synchronously reverses an open reservation. The database state keeps
// the operation retryable after a transient error or process restart.
func (s *BillingSession) Refund(c *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled || s.refunded || !s.needsRefundLocked() {
		return
	}

	logger.LogInfo(c, fmt.Sprintf("用户 %d 请求失败, 返还预扣费（token_quota=%s, funding=%s）",
		s.relayInfo.UserId,
		logger.FormatQuota(s.tokenConsumed),
		s.funding.Source(),
	))
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		_, err = model.RefundBillingReservation(s.relayInfo.RequestId, s.relayInfo.TokenKey)
		if err == nil ||
			errors.Is(err, model.ErrBillingReservationConflict) ||
			errors.Is(err, model.ErrBillingReservationFinalized) ||
			errors.Is(err, model.ErrBillingReservationNotFound) ||
			errors.Is(err, model.ErrQuotaOverflow) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	if err != nil {
		logger.LogError(c, "返还持久化预扣费失败: "+err.Error())
		return
	}
	s.refunded = true
}

func (s *BillingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needsRefundLocked()
}

func (s *BillingSession) needsRefundLocked() bool {
	// A zero-quota request still owns a durable reservation that must reach a
	// terminal state when downstream handling fails.
	return !s.settled && !s.refunded && !s.liabilityPending
}

func (s *BillingSession) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preConsumedQuota
}

func (s *BillingSession) Reserve(targetQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled || s.refunded || targetQuota <= s.preConsumedQuota {
		return nil
	}

	previousQuota := s.preConsumedQuota
	var reservation *model.BillingReservation
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		reservation, err = model.ExtendBillingReservation(s.relayInfo.RequestId, targetQuota, s.relayInfo.TokenKey)
		if err == nil {
			break
		}
		if _, deterministic := model.BillingSettlementFailureCode(err); deterministic ||
			errors.Is(err, model.ErrBillingReservationConflict) ||
			errors.Is(err, model.ErrBillingReservationFinalized) ||
			errors.Is(err, model.ErrBillingReservationNotFound) ||
			errors.Is(err, model.ErrQuotaOverflow) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	if err != nil {
		// The provider has already emitted this usage. Preserve the exact target
		// and prevent the controller's generic error cleanup from refunding it.
		s.liabilityPending = true
		if failureCode, deterministic := model.BillingSettlementFailureCode(err); deterministic {
			if markErr := persistBillingSettlementShortfall(s.relayInfo.RequestId, targetQuota, failureCode); markErr != nil {
				return errors.Join(err, fmt.Errorf("persist realtime billing shortfall: %w", markErr))
			}
		} else if intentErr := persistUnboundBillingSettlementLiability(s.relayInfo.RequestId, targetQuota); intentErr != nil {
			return errors.Join(err, fmt.Errorf("persist realtime settlement intent: %w", intentErr))
		}
		return err
	}
	s.preConsumedQuota = reservation.ReservedQuota
	s.tokenConsumed = reservation.TokenReserved
	s.extraReserved += reservation.ReservedQuota - previousQuota
	s.syncRelayInfo()
	return nil
}

func persistBillingSettlementIntent(requestId string, targetQuota int) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = model.PrepareBillingReservationSettlement(requestId, targetQuota)
		if err == nil ||
			errors.Is(err, model.ErrBillingReservationConflict) ||
			errors.Is(err, model.ErrBillingReservationFinalized) ||
			errors.Is(err, model.ErrBillingReservationNotFound) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	return err
}

func persistUnboundBillingSettlementLiability(requestId string, targetQuota int) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = model.AdvanceUnboundBillingReservationSettlementIntent(requestId, targetQuota)
		if err == nil ||
			errors.Is(err, model.ErrBillingReservationConflict) ||
			errors.Is(err, model.ErrBillingReservationFinalized) ||
			errors.Is(err, model.ErrBillingReservationNotFound) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	return err
}

func persistBillingSettlementShortfall(requestId string, targetQuota int, failureCode string) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		_, err = model.MarkBillingReservationSettlementShortfall(requestId, targetQuota, failureCode)
		if err == nil ||
			errors.Is(err, model.ErrBillingReservationConflict) ||
			errors.Is(err, model.ErrBillingReservationFinalized) ||
			errors.Is(err, model.ErrBillingReservationNotFound) ||
			errors.Is(err, model.ErrBillingAccountNotFound) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	return err
}

func (s *BillingSession) preConsume(c *gin.Context, quota int) *types.NewAPIError {
	if quota < 0 {
		return types.NewError(errors.New("pre-consume quota cannot be negative"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if quota > 0 {
		logger.LogInfo(c, fmt.Sprintf("用户 %d 需要预扣费 %s (funding=%s)", s.relayInfo.UserId, logger.FormatQuota(quota), s.funding.Source()))
	}

	// The former trust bypass left no durable liability when a process crashed
	// before settlement. Always reserve the bounded estimate; this makes the
	// database the authority for every billable request.
	result, err := model.PreConsumeBillingReservation(model.BillingReservationInput{
		RequestId:     s.relayInfo.RequestId,
		UserId:        s.relayInfo.UserId,
		TokenId:       s.relayInfo.TokenId,
		FundingSource: s.funding.Source(),
		Quota:         quota,
		SkipToken:     s.relayInfo.IsPlayground,
	}, s.relayInfo.TokenKey)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrInsufficientTokenQuota):
			return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		case errors.Is(err, model.ErrInsufficientUserQuota):
			return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		case errors.Is(err, model.ErrNoActiveSubscription), errors.Is(err, model.ErrInsufficientSubscriptionQuota):
			return types.NewErrorWithStatusCode(fmt.Errorf("订阅额度不足或未配置订阅: %w", err), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		default:
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
	}

	s.preConsumedQuota = result.Reservation.ReservedQuota
	s.tokenConsumed = result.Reservation.TokenReserved
	switch funding := s.funding.(type) {
	case *SubscriptionFunding:
		funding.subscriptionId = result.Reservation.SubscriptionId
		funding.preConsumed = int64(result.Reservation.ReservedQuota)
		funding.AmountTotal = result.SubscriptionAmountTotal
		funding.AmountUsedAfter = result.SubscriptionAmountUsedAfter
		funding.PlanId = result.SubscriptionPlanId
		funding.PlanTitle = result.SubscriptionPlanTitle
	}
	s.syncRelayInfo()
	return nil
}

func (s *BillingSession) syncRelayInfo() {
	info := s.relayInfo
	info.FinalPreConsumedQuota = s.preConsumedQuota
	info.BillingSource = s.funding.Source()

	if subscription, ok := s.funding.(*SubscriptionFunding); ok {
		info.SubscriptionId = subscription.subscriptionId
		info.SubscriptionPreConsumed = subscription.preConsumed + int64(s.extraReserved)
		info.SubscriptionPostDelta = 0
		info.SubscriptionAmountTotal = subscription.AmountTotal
		info.SubscriptionAmountUsedAfterPreConsume = subscription.AmountUsedAfter + int64(s.extraReserved)
		info.SubscriptionPlanId = subscription.PlanId
		info.SubscriptionPlanTitle = subscription.PlanTitle
	} else {
		info.SubscriptionId = 0
		info.SubscriptionPreConsumed = 0
	}
}

// NewBillingSession selects the configured funding source and creates its
// durable reservation. Subscription-first fallback remains compatible with
// the existing preference contract.
func NewBillingSession(c *gin.Context, relayInfo *relaycommon.RelayInfo, preConsumedQuota int) (*BillingSession, *types.NewAPIError) {
	if relayInfo == nil {
		return nil, types.NewError(errors.New("relayInfo is nil"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if preConsumedQuota < 0 {
		return nil, types.NewError(errors.New("preConsumedQuota cannot be negative"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if relayInfo.RequestId == "" {
		relayInfo.RequestId = common.NewRequestId()
	}

	preference := common.NormalizeBillingPreference(relayInfo.UserSetting.BillingPreference)
	tryWallet := func() (*BillingSession, *types.NewAPIError) {
		session := &BillingSession{
			relayInfo: relayInfo,
			funding:   &WalletFunding{},
		}
		if apiErr := session.preConsume(c, preConsumedQuota); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	trySubscription := func() (*BillingSession, *types.NewAPIError) {
		subscriptionQuota := int64(preConsumedQuota)
		if subscriptionQuota <= 0 {
			subscriptionQuota = 1
		}
		session := &BillingSession{
			relayInfo: relayInfo,
			funding:   &SubscriptionFunding{},
		}
		if apiErr := session.preConsume(c, int(subscriptionQuota)); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	switch preference {
	case "subscription_only":
		return trySubscription()
	case "wallet_only":
		return tryWallet()
	case "wallet_first":
		session, apiErr := tryWallet()
		if apiErr != nil {
			if apiErr.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
				return trySubscription()
			}
			return nil, apiErr
		}
		return session, nil
	case "subscription_first":
		fallthrough
	default:
		hasSubscription, err := model.HasActiveUserSubscription(relayInfo.UserId)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if !hasSubscription {
			return tryWallet()
		}
		session, apiErr := trySubscription()
		if apiErr == nil {
			return session, nil
		}
		if apiErr.GetErrorCode() != types.ErrorCodeInsufficientUserQuota {
			return nil, apiErr
		}
		allowWalletOverflow, err := model.UserActiveSubscriptionsAllowWalletOverflow(relayInfo.UserId)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if allowWalletOverflow {
			return tryWallet()
		}
		return nil, apiErr
	}
}
