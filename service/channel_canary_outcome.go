package service

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
)

var ErrChannelRoutingCanaryOutcomeInvalid = errors.New("invalid channel routing canary outcome state")

type ChannelRoutingCanarySelection struct {
	Gate              channelrouting.CanaryGate
	WindowSeconds     int
	LatenessSeconds   int
	ExpectedCostUSD   float64
	ExpectedCostKnown bool
}

type channelRoutingCanaryOutcomeContext struct {
	mu            sync.Mutex
	current       *channelRoutingCanaryOutcomeUnit
	activeAttempt bool
}

type channelRoutingCanaryOutcomeUnit struct {
	identity             channelrouting.CanaryWindowIdentity
	cohort               string
	attempts             int64
	allAttemptCostsKnown bool
	totalCostNanoUSD     int64
	pendingCostKnown     bool
	pendingCostNanoUSD   int64
	hasPendingSelection  bool
}

func PrepareChannelRoutingCanarySelection(c *gin.Context, selection ChannelRoutingCanarySelection) error {
	if c == nil || selection.WindowSeconds <= 0 || selection.LatenessSeconds < 0 {
		return ErrChannelRoutingCanaryOutcomeInvalid
	}
	gate := selection.Gate
	cohort := model.RoutingDecisionCohortControl
	if gate.InCanary {
		cohort = model.RoutingDecisionCohortCanary
	}
	identity := channelrouting.CanaryWindowIdentity{
		PoolID: gate.PoolID, ActivationID: gate.ActivationID, PolicyRevision: gate.PolicyRevision,
		TrafficBasisPoints: gate.TrafficBasisPoints, RolloutKey: gate.RolloutKey,
		WindowSeconds: selection.WindowSeconds, LatenessSeconds: selection.LatenessSeconds,
	}
	if identity.PoolID <= 0 || identity.ActivationID <= 0 || identity.PolicyRevision == 0 || identity.RolloutKey == "" {
		return ErrChannelRoutingCanaryOutcomeInvalid
	}
	costNanoUSD := int64(0)
	costKnown := selection.ExpectedCostKnown
	if costKnown {
		var valid bool
		costNanoUSD, valid = channelrouting.CanaryCostNanoUSD(selection.ExpectedCostUSD)
		if !valid {
			costKnown = false
		}
	}
	state, ok := common.GetContextKeyType[*channelRoutingCanaryOutcomeContext](c, constant.ContextKeyRoutingCanaryOutcome)
	if !ok || state == nil {
		state = &channelRoutingCanaryOutcomeContext{}
		common.SetContextKey(c, constant.ContextKeyRoutingCanaryOutcome, state)
	}

	var previous *channelRoutingCanaryOutcomeUnit
	state.mu.Lock()
	if state.activeAttempt {
		state.mu.Unlock()
		return ErrChannelRoutingCanaryOutcomeInvalid
	}
	if state.current != nil && (state.current.identity != identity || state.current.cohort != cohort) {
		previous = state.current
		state.current = nil
	}
	if state.current == nil {
		state.current = &channelRoutingCanaryOutcomeUnit{
			identity: identity, cohort: cohort, allAttemptCostsKnown: true,
		}
	}
	state.current.pendingCostKnown = costKnown
	state.current.pendingCostNanoUSD = costNanoUSD
	state.current.hasPendingSelection = true
	state.mu.Unlock()

	if previous != nil {
		return recordChannelRoutingCanaryOutcomeUnit(previous, false, true, 0, time.Now())
	}
	return nil
}

func MarkChannelRoutingCanaryAttemptStarted(c *gin.Context) error {
	if c == nil {
		return nil
	}
	state, ok := common.GetContextKeyType[*channelRoutingCanaryOutcomeContext](c, constant.ContextKeyRoutingCanaryOutcome)
	if !ok || state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	unit := state.current
	if unit == nil || state.activeAttempt || !unit.hasPendingSelection || unit.attempts == math.MaxInt64 {
		return ErrChannelRoutingCanaryOutcomeInvalid
	}
	unit.attempts++
	if unit.pendingCostKnown && unit.allAttemptCostsKnown {
		if unit.totalCostNanoUSD > math.MaxInt64-unit.pendingCostNanoUSD {
			unit.allAttemptCostsKnown = false
			unit.totalCostNanoUSD = 0
		} else {
			unit.totalCostNanoUSD += unit.pendingCostNanoUSD
		}
	} else {
		unit.allAttemptCostsKnown = false
		unit.totalCostNanoUSD = 0
	}
	unit.pendingCostKnown = false
	unit.pendingCostNanoUSD = 0
	unit.hasPendingSelection = false
	state.activeAttempt = true
	return nil
}

func FinishChannelRoutingCanaryAttempt(c *gin.Context) error {
	if c == nil {
		return nil
	}
	state, ok := common.GetContextKeyType[*channelRoutingCanaryOutcomeContext](c, constant.ContextKeyRoutingCanaryOutcome)
	if !ok || state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.activeAttempt {
		return ErrChannelRoutingCanaryOutcomeInvalid
	}
	state.activeAttempt = false
	return nil
}

func FinishChannelRoutingCanaryOutcome(
	c *gin.Context,
	include bool,
	success bool,
	routingFailure bool,
	clientTTFTMilliseconds int64,
	completedAt time.Time,
) error {
	if c == nil {
		return nil
	}
	state, ok := common.GetContextKeyType[*channelRoutingCanaryOutcomeContext](c, constant.ContextKeyRoutingCanaryOutcome)
	if !ok || state == nil {
		return nil
	}
	state.mu.Lock()
	unit := state.current
	state.current = nil
	activeAttempt := state.activeAttempt
	state.activeAttempt = false
	state.mu.Unlock()
	common.SetContextKey(c, constant.ContextKeyRoutingCanaryOutcome, nil)
	if activeAttempt {
		return ErrChannelRoutingCanaryOutcomeInvalid
	}
	if unit == nil || !include {
		return nil
	}
	if !success && unit.attempts == 0 {
		routingFailure = true
	}
	return recordChannelRoutingCanaryOutcomeUnit(unit, success, routingFailure, clientTTFTMilliseconds, completedAt)
}

func recordChannelRoutingCanaryOutcomeUnit(
	unit *channelRoutingCanaryOutcomeUnit,
	success bool,
	routingFailure bool,
	clientTTFTMilliseconds int64,
	completedAt time.Time,
) error {
	if unit == nil || completedAt.IsZero() || unit.attempts < 0 || (success && routingFailure) {
		return ErrChannelRoutingCanaryOutcomeInvalid
	}
	costKnown := unit.allAttemptCostsKnown && unit.attempts > 0
	costNanoUSD := unit.totalCostNanoUSD
	if !costKnown {
		costNanoUSD = 0
	}
	return channelrouting.RecordCanaryLogicalOutcome(channelrouting.CanaryLogicalOutcome{
		Identity: unit.identity, Cohort: unit.cohort, CompletedAt: completedAt,
		Success: success, RoutingFailure: routingFailure, Attempts: unit.attempts,
		CostKnown: costKnown, ExpectedPlatformCostNanoUSD: costNanoUSD,
		ClientTTFTMilliseconds: clientTTFTMilliseconds,
	})
}
