package common

import (
	"errors"
	"sync"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
)

var ErrRoutingUpstreamSendState = errors.New("routing upstream send state is invalid")

type RoutingUpstreamSendState struct {
	once sync.Once
	mu   sync.Mutex
	mark func() error
	sent bool
	err  error
}

func NewRoutingUpstreamSendState(mark func() error) *RoutingUpstreamSendState {
	return &RoutingUpstreamSendState{mark: mark}
}

func BindRoutingUpstreamSendState(c *gin.Context, state *RoutingUpstreamSendState) {
	if c == nil {
		return
	}
	basecommon.SetContextKey(c, constant.ContextKeyRoutingUpstreamSend, state)
}

func ClearRoutingUpstreamSendState(c *gin.Context) {
	if c == nil {
		return
	}
	basecommon.SetContextKey(c, constant.ContextKeyRoutingUpstreamSend, nil)
}

func MarkRoutingUpstreamSent(c *gin.Context) error {
	state := RoutingUpstreamSendStateFromContext(c)
	if state == nil {
		return nil
	}
	return state.MarkSent()
}

func RoutingUpstreamSendStateFromContext(c *gin.Context) *RoutingUpstreamSendState {
	if c == nil {
		return nil
	}
	state, _ := basecommon.GetContextKeyType[*RoutingUpstreamSendState](c, constant.ContextKeyRoutingUpstreamSend)
	return state
}

func (state *RoutingUpstreamSendState) MarkSent() error {
	if state == nil {
		return ErrRoutingUpstreamSendState
	}
	state.once.Do(func() {
		var markErr error
		if state.mark != nil {
			markErr = state.mark()
		}
		state.mu.Lock()
		state.err = markErr
		if markErr == nil {
			state.sent = true
		}
		state.mu.Unlock()
	})
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.err
}

func (state *RoutingUpstreamSendState) Sent() bool {
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.sent
}
