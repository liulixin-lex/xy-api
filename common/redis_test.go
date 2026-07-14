package common

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

type blockingCacheEpochEvalHook struct {
	started chan struct{}
	once    sync.Once
}

func (hook *blockingCacheEpochEvalHook) BeforeProcess(
	ctx context.Context,
	command redis.Cmder,
) (context.Context, error) {
	if command.Name() != "eval" && command.Name() != "evalsha" {
		return ctx, nil
	}
	hook.once.Do(func() { close(hook.started) })
	<-ctx.Done()
	return ctx, ctx.Err()
}

func (*blockingCacheEpochEvalHook) AfterProcess(context.Context, redis.Cmder) error {
	return nil
}

func (*blockingCacheEpochEvalHook) BeforeProcessPipeline(
	ctx context.Context,
	_ []redis.Cmder,
) (context.Context, error) {
	return ctx, nil
}

func (*blockingCacheEpochEvalHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func TestRedisBumpCacheEpochAndDeleteContextCancelsBlockedEval(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	hook := &blockingCacheEpochEvalHook{started: make(chan struct{})}
	client.AddHook(hook)
	previousClient := RDB
	RDB = client
	t.Cleanup(func() {
		_ = client.Close()
		RDB = previousClient
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- RedisBumpCacheEpochAndDeleteContext(ctx, "cache_epoch:test", "test")
	}()

	select {
	case <-hook.started:
	case <-time.After(time.Second):
		t.Fatal("Redis Eval did not reach the blocking hook")
	}
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Redis Eval did not stop after context cancellation")
	}
}
