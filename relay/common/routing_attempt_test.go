package common

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingUpstreamSendStateMarksExactlyOnce(t *testing.T) {
	ctx, _ := gin.CreateTestContext(nil)
	var calls atomic.Int64
	state := NewRoutingUpstreamSendState(func() error {
		calls.Add(1)
		return nil
	})
	BindRoutingUpstreamSendState(ctx, state)

	require.NoError(t, MarkRoutingUpstreamSent(ctx))
	require.NoError(t, MarkRoutingUpstreamSent(ctx))
	assert.Equal(t, int64(1), calls.Load())
	assert.True(t, state.Sent())

	ClearRoutingUpstreamSendState(ctx)
	require.NoError(t, MarkRoutingUpstreamSent(ctx))
}

func TestRoutingUpstreamSendStateDoesNotClaimSendWhenTransitionFails(t *testing.T) {
	want := errors.New("transition failed")
	state := NewRoutingUpstreamSendState(func() error { return want })
	assert.ErrorIs(t, state.MarkSent(), want)
	assert.ErrorIs(t, state.MarkSent(), want)
	assert.False(t, state.Sent())
}
