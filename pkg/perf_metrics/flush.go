package perfmetrics

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type drainedBucket struct {
	key      bucketKey
	bucket   *bucket
	counters counters
}

func flushBucketsWith(
	ctx context.Context,
	currentBucket int64,
	includeActive bool,
	persist func(context.Context, *model.PerfMetric) error,
) error {
	drainedBuckets := drainBuckets(currentBucket, includeActive)
	var flushErrors []error
	for index, drained := range drainedBuckets {
		if err := ctx.Err(); err != nil {
			requeueDrainedBuckets(drainedBuckets[index:])
			return errors.Join(append(flushErrors, err)...)
		}

		k := drained.key
		value := drained.counters
		err := persist(ctx, &model.PerfMetric{
			ModelName:      k.model,
			Group:          k.group,
			BucketTs:       k.bucketTs,
			RequestCount:   value.requestCount,
			SuccessCount:   value.successCount,
			TotalLatencyMs: value.totalLatencyMs,
			TtftSumMs:      value.ttftSumMs,
			TtftCount:      value.ttftCount,
			OutputTokens:   value.outputTokens,
			GenerationMs:   value.generationMs,
		})
		if err == nil {
			completeDrainedBucket(drained)
			continue
		}
		requeueDrainedBucket(drained)
		if ctx.Err() == nil {
			common.SysError(fmt.Sprintf("failed to flush perf metric bucket model=%s group=%s bucket=%d: %s", k.model, k.group, k.bucketTs, err.Error()))
		}
		flushErrors = append(flushErrors, fmt.Errorf("flush perf metric bucket model=%s group=%s bucket=%d: %w", k.model, k.group, k.bucketTs, err))
		if ctx.Err() != nil {
			requeueDrainedBuckets(drainedBuckets[index+1:])
			break
		}
	}
	return errors.Join(flushErrors...)
}

func drainBuckets(currentBucket int64, includeActive bool) []drainedBucket {
	drainedBuckets := make([]drainedBucket, 0)
	maintenanceMu.Lock()
	defer maintenanceMu.Unlock()
	hotBuckets.Range(func(key, value any) bool {
		k := key.(bucketKey)
		if !includeActive && k.bucketTs >= currentBucket {
			return true
		}

		bucket := value.(*bucket)
		bucket.mu.Lock()
		if bucket.draining {
			bucket.mu.Unlock()
			return true
		}
		bucket.draining = true
		drained := bucket.counters
		bucket.counters = counters{}
		if drained.requestCount == 0 {
			deleted := hotBuckets.CompareAndDelete(key, value)
			bucket.mu.Unlock()
			if deleted {
				bucketCount.Add(-1)
			}
			return true
		}
		bucket.mu.Unlock()
		drainedBuckets = append(drainedBuckets, drainedBucket{key: k, bucket: bucket, counters: drained})
		return true
	})
	return drainedBuckets
}

func requeueDrainedBuckets(drainedBuckets []drainedBucket) {
	for _, drained := range drainedBuckets {
		requeueDrainedBucket(drained)
	}
}

func requeueDrainedBucket(drained drainedBucket) {
	maintenanceMu.Lock()
	defer maintenanceMu.Unlock()

	actual, ok := hotBuckets.Load(drained.key)
	if !ok {
		b := &bucket{counters: drained.counters}
		hotBuckets.Store(drained.key, b)
		bucketCount.Add(1)
		return
	}
	b := actual.(*bucket)
	b.mu.Lock()
	b.addCountersLocked(drained.counters)
	if b == drained.bucket {
		b.draining = false
	}
	b.mu.Unlock()
}

func completeDrainedBucket(drained drainedBucket) {
	maintenanceMu.Lock()
	defer maintenanceMu.Unlock()

	actual, ok := hotBuckets.Load(drained.key)
	if !ok || actual != drained.bucket {
		return
	}
	b := drained.bucket
	b.mu.Lock()
	if b.counters.requestCount > 0 {
		b.draining = false
		b.mu.Unlock()
		return
	}
	deleted := hotBuckets.CompareAndDelete(drained.key, b)
	b.mu.Unlock()
	if deleted {
		bucketCount.Add(-1)
	}
}
