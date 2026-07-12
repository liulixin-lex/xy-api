package service

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
)

type routingCapacityReservationState uint8

const (
	routingCapacityReservationPending routingCapacityReservationState = iota
	routingCapacityReservationCommitted
	routingCapacityReservationFinished
)

type routingCapacityReservationContext struct {
	mu          sync.Mutex
	reservation *channelrouting.Reservation
	state       routingCapacityReservationState
}

func SetRoutingCapacityReservation(c *gin.Context, reservation *channelrouting.Reservation) error {
	if c == nil || reservation == nil {
		return channelrouting.ErrCapacityInvalidInput
	}
	if current, ok := routingCapacityReservationFromContext(c); ok {
		if current.reservation == reservation {
			return nil
		}
		if err := finishRoutingCapacityReservation(current); err != nil {
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
		if err := reservation.reservation.Commit(); err != nil {
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

func finishRoutingCapacityReservationForContext(c *gin.Context) error {
	reservation, ok := routingCapacityReservationFromContext(c)
	if !ok {
		return nil
	}
	err := finishRoutingCapacityReservation(reservation)
	if err == nil {
		current, stillCurrent := routingCapacityReservationFromContext(c)
		if stillCurrent && current == reservation {
			common.SetContextKey(c, constant.ContextKeyRoutingCapacityReserve, nil)
		}
	}
	return err
}

func finishRoutingCapacityReservation(reservation *routingCapacityReservationContext) error {
	if reservation == nil || reservation.reservation == nil {
		return nil
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.state == routingCapacityReservationFinished {
		return nil
	}
	var err error
	switch reservation.state {
	case routingCapacityReservationPending:
		err = reservation.reservation.Cancel()
	case routingCapacityReservationCommitted:
		err = reservation.reservation.Release()
	default:
		err = channelrouting.ErrCapacityReservationTransition
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
