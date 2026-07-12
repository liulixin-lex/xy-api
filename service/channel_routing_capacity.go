package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
)

const routingCapacityTransitionTimeout = 5 * time.Second
const routingCapacityRenewalMinimum = 100 * time.Millisecond

type routingCapacityReservationState uint8

const (
	routingCapacityReservationPending routingCapacityReservationState = iota
	routingCapacityReservationCommitted
	routingCapacityReservationFinished
)

type routingCapacityReservation interface {
	same(any) bool
	commit(context.Context) error
	cancel(context.Context) error
	release(context.Context) error
}

type localRoutingCapacityReservation struct {
	reservation *channelrouting.Reservation
}

type strictRoutingCapacityReservation struct {
	reservation *channelrouting.StrictCapacityReservation
	leaseTTL    time.Duration
	requestStop context.CancelCauseFunc
	recordError func(error)

	mu            sync.Mutex
	renewalCancel context.CancelFunc
	renewalDone   chan struct{}
}

type routingCapacityReservationContext struct {
	mu          sync.Mutex
	reservation routingCapacityReservation
	adaptive    *channelrouting.AdaptiveConcurrencyLeaseSet
	state       routingCapacityReservationState
}

func SetRoutingCapacityReservation(c *gin.Context, reservation *channelrouting.Reservation) error {
	if reservation == nil {
		return channelrouting.ErrCapacityInvalidInput
	}
	return setRoutingCapacityReservation(c, &localRoutingCapacityReservation{reservation: reservation}, reservation)
}

func SetRoutingStrictCapacityReservation(
	c *gin.Context,
	reservation *channelrouting.StrictCapacityReservation,
) error {
	if reservation == nil {
		return channelrouting.ErrStrictCapacityInvalid
	}
	admission := reservation.Admission()
	leaseTTL := time.Duration(admission.LeaseTTLMillis) * time.Millisecond
	if leaseTTL < time.Second || leaseTTL > 5*time.Minute {
		return channelrouting.ErrStrictCapacityInvalid
	}
	if c == nil || c.Request == nil {
		return channelrouting.ErrStrictCapacityInvalid
	}
	if current, ok := routingCapacityReservationFromContext(c); ok {
		current.mu.Lock()
		sameReservation := current.reservation.same(reservation)
		current.mu.Unlock()
		if sameReservation {
			return nil
		}
	}
	requestContext, requestStop := context.WithCancelCause(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	strictReservation := &strictRoutingCapacityReservation{
		reservation: reservation,
		leaseTTL:    leaseTTL,
		requestStop: requestStop,
		recordError: func(err error) {
			common.SetContextKey(c, constant.ContextKeyRoutingCapacityFailure, err)
		},
	}
	if err := setRoutingCapacityReservation(c, strictReservation, reservation); err != nil {
		requestStop(err)
		return err
	}
	strictReservation.startRenewal()
	return nil
}

func AttachRoutingAdaptiveConcurrency(
	c *gin.Context,
	lease *channelrouting.AdaptiveConcurrencyLeaseSet,
) error {
	if c == nil || lease == nil {
		return channelrouting.ErrAdaptiveConcurrencyInvalid
	}
	reservation, ok := routingCapacityReservationFromContext(c)
	if !ok {
		return channelrouting.ErrAdaptiveConcurrencyInvalid
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.state == routingCapacityReservationFinished {
		return channelrouting.ErrAdaptiveConcurrencyLost
	}
	if reservation.adaptive != nil && reservation.adaptive != lease {
		return channelrouting.ErrAdaptiveConcurrencyConflict
	}
	reservation.adaptive = lease
	common.SetContextKey(c, constant.ContextKeyRoutingAdaptiveConcurrency, lease.Targets())
	return nil
}

func RoutingAdaptiveConcurrencyTargets(c *gin.Context) []channelrouting.AdaptiveConcurrencyTarget {
	if c == nil {
		return nil
	}
	targets, ok := common.GetContextKeyType[[]channelrouting.AdaptiveConcurrencyTarget](
		c,
		constant.ContextKeyRoutingAdaptiveConcurrency,
	)
	if !ok {
		return nil
	}
	return append([]channelrouting.AdaptiveConcurrencyTarget(nil), targets...)
}

func setRoutingCapacityReservation(c *gin.Context, reservation routingCapacityReservation, identity any) error {
	if c == nil || reservation == nil {
		return channelrouting.ErrCapacityInvalidInput
	}
	if current, ok := routingCapacityReservationFromContext(c); ok {
		if current.reservation.same(identity) {
			return nil
		}
		if err := finishRoutingCapacityReservation(c, current); err != nil {
			return err
		}
	}
	common.SetContextKey(c, constant.ContextKeyRoutingCapacityReserve, &routingCapacityReservationContext{
		reservation: reservation,
		state:       routingCapacityReservationPending,
	})
	return nil
}

func CommitRoutingCapacityReservation(c *gin.Context) error {
	reservation, ok := routingCapacityReservationFromContext(c)
	if !ok {
		return nil
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	switch reservation.state {
	case routingCapacityReservationPending:
		ctx, cancel := routingCapacityTransitionContext(c, false)
		defer cancel()
		if err := reservation.reservation.commit(ctx); err != nil {
			return err
		}
		reservation.state = routingCapacityReservationCommitted
		return nil
	case routingCapacityReservationCommitted:
		return nil
	case routingCapacityReservationFinished:
		return channelrouting.ErrCapacityReservationTransition
	default:
		return channelrouting.ErrCapacityReservationTransition
	}
}

func CancelRoutingCapacityReservation(c *gin.Context) error {
	return finishRoutingCapacityReservationForContext(c)
}

func ReleaseRoutingCapacityReservation(c *gin.Context) error {
	return finishRoutingCapacityReservationForContext(c)
}

func RoutingCapacityReservationFailure(c *gin.Context) error {
	if c == nil {
		return nil
	}
	value, exists := common.GetContextKey(c, constant.ContextKeyRoutingCapacityFailure)
	if !exists {
		return nil
	}
	err, _ := value.(error)
	return err
}

func HasRoutingStrictCapacityReservation(c *gin.Context) bool {
	reservation, ok := routingCapacityReservationFromContext(c)
	if !ok {
		return false
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.state == routingCapacityReservationFinished {
		return false
	}
	_, strict := reservation.reservation.(*strictRoutingCapacityReservation)
	return strict
}

func finishRoutingCapacityReservationForContext(c *gin.Context) error {
	reservation, ok := routingCapacityReservationFromContext(c)
	if !ok {
		return nil
	}
	err := finishRoutingCapacityReservation(c, reservation)
	if err == nil {
		current, stillCurrent := routingCapacityReservationFromContext(c)
		if stillCurrent && current == reservation {
			common.SetContextKey(c, constant.ContextKeyRoutingCapacityReserve, nil)
		}
	}
	return err
}

func finishRoutingCapacityReservation(c *gin.Context, reservation *routingCapacityReservationContext) error {
	if reservation == nil || reservation.reservation == nil {
		return nil
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.state == routingCapacityReservationFinished {
		return nil
	}
	ctx, cancel := routingCapacityTransitionContext(c, true)
	defer cancel()
	var err error
	switch reservation.state {
	case routingCapacityReservationPending:
		err = reservation.reservation.cancel(ctx)
	case routingCapacityReservationCommitted:
		err = reservation.reservation.release(ctx)
	default:
		err = channelrouting.ErrCapacityReservationTransition
	}
	if reservation.adaptive != nil {
		err = errors.Join(err, reservation.adaptive.Release())
	}
	if err == nil {
		reservation.state = routingCapacityReservationFinished
	}
	return err
}

func routingCapacityReservationFromContext(c *gin.Context) (*routingCapacityReservationContext, bool) {
	if c == nil {
		return nil, false
	}
	reservation, ok := common.GetContextKeyType[*routingCapacityReservationContext](c, constant.ContextKeyRoutingCapacityReserve)
	return reservation, ok && reservation != nil && reservation.reservation != nil
}

func routingCapacityTransitionContext(c *gin.Context, cleanup bool) (context.Context, context.CancelFunc) {
	parent := context.Background()
	if !cleanup && c != nil && c.Request != nil && c.Request.Context() != nil {
		parent = c.Request.Context()
	}
	return context.WithTimeout(parent, routingCapacityTransitionTimeout)
}

func (reservation *localRoutingCapacityReservation) same(value any) bool {
	other, ok := value.(*channelrouting.Reservation)
	return ok && reservation != nil && reservation.reservation == other
}

func (reservation *localRoutingCapacityReservation) commit(context.Context) error {
	if reservation == nil || reservation.reservation == nil {
		return channelrouting.ErrCapacityReservationTransition
	}
	return reservation.reservation.Commit()
}

func (reservation *localRoutingCapacityReservation) cancel(context.Context) error {
	if reservation == nil || reservation.reservation == nil {
		return channelrouting.ErrCapacityReservationTransition
	}
	return reservation.reservation.Cancel()
}

func (reservation *localRoutingCapacityReservation) release(context.Context) error {
	if reservation == nil || reservation.reservation == nil {
		return channelrouting.ErrCapacityReservationTransition
	}
	return reservation.reservation.Release()
}

func (reservation *strictRoutingCapacityReservation) same(value any) bool {
	other, ok := value.(*channelrouting.StrictCapacityReservation)
	return ok && reservation != nil && reservation.reservation == other
}

func (reservation *strictRoutingCapacityReservation) commit(ctx context.Context) error {
	if reservation == nil || reservation.reservation == nil {
		return channelrouting.ErrStrictCapacityTransition
	}
	if err := reservation.reservation.Commit(ctx); err != nil {
		return err
	}
	reservation.startRenewal()
	return nil
}

func (reservation *strictRoutingCapacityReservation) cancel(ctx context.Context) error {
	if reservation == nil || reservation.reservation == nil {
		return channelrouting.ErrStrictCapacityTransition
	}
	reservation.stopRenewal()
	return reservation.reservation.Cancel(ctx)
}

func (reservation *strictRoutingCapacityReservation) release(ctx context.Context) error {
	if reservation == nil || reservation.reservation == nil {
		return channelrouting.ErrStrictCapacityTransition
	}
	reservation.stopRenewal()
	return reservation.reservation.Release(ctx)
}

func (reservation *strictRoutingCapacityReservation) startRenewal() {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.renewalCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	reservation.renewalCancel = cancel
	reservation.renewalDone = done
	interval, timeout := strictRoutingCapacityRenewalTiming(reservation.leaseTTL)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewCtx, renewCancel := context.WithTimeout(ctx, timeout)
				err := reservation.reservation.Renew(renewCtx, reservation.leaseTTL)
				renewCancel()
				if err != nil {
					failure := fmt.Errorf("channel routing strict capacity renewal failed: %w", err)
					common.SysError(failure.Error())
					if reservation.recordError != nil {
						reservation.recordError(failure)
					}
					if reservation.requestStop != nil {
						reservation.requestStop(failure)
					}
					return
				}
			}
		}
	}()
}

func strictRoutingCapacityRenewalTiming(leaseTTL time.Duration) (time.Duration, time.Duration) {
	interval := leaseTTL / 3
	if interval < routingCapacityRenewalMinimum {
		interval = routingCapacityRenewalMinimum
	}
	timeout := leaseTTL / 3
	if timeout < routingCapacityRenewalMinimum {
		timeout = routingCapacityRenewalMinimum
	}
	if timeout > routingCapacityTransitionTimeout {
		timeout = routingCapacityTransitionTimeout
	}
	return interval, timeout
}

func (reservation *strictRoutingCapacityReservation) stopRenewal() {
	reservation.mu.Lock()
	cancel := reservation.renewalCancel
	done := reservation.renewalDone
	reservation.renewalCancel = nil
	reservation.renewalDone = nil
	reservation.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}
