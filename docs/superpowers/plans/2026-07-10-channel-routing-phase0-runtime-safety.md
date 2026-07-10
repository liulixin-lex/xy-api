# 渠道路由 Phase 0A 运行时安全实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 消除渠道路由关闭或高基数输入下的无界内存增长，使配置读取、运行时 Worker、缓存、Breaker、JWT 登录协调和历史保留具备明确并发与生命周期边界。

**Architecture:** 保留现有兼容 API 和运行路径，先用受 ConfigManager 同一把锁保护的配置快照消除竞态；随后为指标、Breaker、Hot Cache 和 JWT 缓存增加硬容量、TTL 与可观测统计。把永久 `time.Sleep` Goroutine 改为可取消、可等待的 `SmartRoutingRuntime`，并由该运行时执行指标保留清理。

**Tech Stack:** Go 1.22、Gin、GORM v2、`sync/atomic`、`golang.org/x/sync/singleflight`、Testify、SQLite/MySQL/PostgreSQL 兼容测试。

---

## 文件职责

- `setting/config/config.go`：提供在 ConfigManager 锁内复制和替换已注册配置的原子操作。
- `setting/config/config_test.go`：保护配置快照/替换契约。
- `setting/smart_routing_setting/config.go`：所有渠道路由设置读写只通过 ConfigManager 快照进行。
- `setting/smart_routing_setting/config_test.go`：保护归一化、环境覆盖和并发读写。
- `pkg/routing_metrics/metrics.go`：在功能关闭时不建状态；指标桶和 Inflight Key 有硬上限、TTL 与统计。
- `pkg/routing_metrics/metrics_test.go`：保护关闭态、容量、TTL、Drain/Requeue 和 Inflight 释放契约。
- `pkg/routing_breaker/breaker.go`：Breaker 状态有 TTL、最大条目数和 Eviction 统计。
- `pkg/routing_breaker/breaker_test.go`：保护状态机不变，同时验证容量边界。
- `pkg/routing_hotcache/cache.go`：所有 Hot Cache 分类支持按新鲜度清理和硬容量裁剪。
- `pkg/routing_hotcache/cache_test.go`：保护过期与超量条目被删除、最新条目保留。
- `controller/relay.go`、`controller/relay_retry_test.go`：关闭功能时不写 Breaker。
- `controller/smart_routing_sub2api.go`、`controller/smart_routing_task_test.go`：用 Singleflight 取代永久 Lock Map，并限制本机 JWT 缓存。
- `model/routing_model.go`、`model/routing_model_test.go`：提供跨数据库兼容的路由指标保留清理。
- `controller/system_task_handlers.go`、`controller/smart_routing_runtime_test.go`：实现可取消、可等待的运行时循环和保留调度。
- `main.go`：持有并在进程退出时关闭渠道路由运行时。

### Task 1: 配置快照与并发安全

**Files:**
- Modify: `setting/config/config.go`
- Modify: `setting/config/config_test.go`
- Modify: `setting/smart_routing_setting/config.go`
- Modify: `setting/smart_routing_setting/config_test.go`

- [ ] **Step 1: 为 ConfigManager 快照/替换写失败测试**

在 `setting/config/config_test.go` 增加：

```go
func TestConfigManagerSnapshotAndReplaceUseRegisteredValue(t *testing.T) {
	manager := NewConfigManager()
	registered := &testConfigWithMap{Name: "before", Modes: map[string]string{"a": "one"}}
	manager.Register("test", registered)

	var snapshot testConfigWithMap
	require.True(t, manager.Snapshot("test", &snapshot))
	assert.Equal(t, "before", snapshot.Name)

	replacement := testConfigWithMap{Name: "after", Modes: map[string]string{"b": "two"}}
	require.True(t, manager.Replace("test", replacement))
	require.True(t, manager.Snapshot("test", &snapshot))
	assert.Equal(t, "after", snapshot.Name)
	assert.Equal(t, map[string]string{"b": "two"}, snapshot.Modes)
}

func TestConfigManagerSnapshotRejectsMismatchedDestination(t *testing.T) {
	manager := NewConfigManager()
	manager.Register("test", &testConfigWithMap{Name: "value"})

	var wrong string
	assert.False(t, manager.Snapshot("test", &wrong))
	assert.False(t, manager.Replace("test", "wrong"))
}
```

同时把新测试的 import 改为：

```go
import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
```

- [ ] **Step 2: 运行测试确认 RED**

Run: `go test ./setting/config -run 'TestConfigManagerSnapshot' -count=1`

Expected: FAIL，提示 `Snapshot` / `Replace` 未定义。

- [ ] **Step 3: 实现锁内快照与替换**

在 `setting/config/config.go` 的 `Get` 后增加：

```go
func (cm *ConfigManager) Snapshot(name string, destination any) bool {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	registered, ok := cm.configs[name]
	if !ok {
		return false
	}
	target := reflect.ValueOf(destination)
	if target.Kind() != reflect.Ptr || target.IsNil() {
		return false
	}
	source := reflect.ValueOf(registered)
	if source.Kind() == reflect.Ptr {
		source = source.Elem()
	}
	if target.Elem().Type() != source.Type() {
		return false
	}
	target.Elem().Set(source)
	return true
}

func (cm *ConfigManager) Replace(name string, replacement any) bool {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	registered, ok := cm.configs[name]
	if !ok {
		return false
	}
	target := reflect.ValueOf(registered)
	if target.Kind() != reflect.Ptr || target.IsNil() {
		return false
	}
	source := reflect.ValueOf(replacement)
	if source.Kind() == reflect.Ptr {
		if source.IsNil() {
			return false
		}
		source = source.Elem()
	}
	if target.Elem().Type() != source.Type() {
		return false
	}
	target.Elem().Set(source)
	return true
}
```

`Snapshot` 是浅复制；本阶段的 `SmartRoutingSetting` 只含标量，因此没有共享 Map/Slice。后续若有复合策略对象，必须在不可变 Revision 中单独处理。

- [ ] **Step 4: 让 SmartRoutingSetting 只通过 ConfigManager 访问**

将 `setting/smart_routing_setting/config.go` 中直接读写全局变量的函数替换为：

```go
const configName = "smart_routing_setting"

func init() {
	config.GlobalConfig.Register(configName, &smartRoutingSetting)
}

func GetSetting() SmartRoutingSetting {
	setting := defaultSmartRoutingSetting
	if !config.GlobalConfig.Snapshot(configName, &setting) {
		setting = defaultSmartRoutingSetting
	}
	applyEnvOverrides(&setting)
	normalize(&setting)
	return setting
}

func UpdateSetting(setting SmartRoutingSetting) SmartRoutingSetting {
	normalize(&setting)
	config.GlobalConfig.Replace(configName, setting)
	return GetSetting()
}

func ResetForTest() {
	config.GlobalConfig.Replace(configName, defaultSmartRoutingSetting)
}
```

- [ ] **Step 5: 增加并发契约测试**

先在现有 `TestUpdateSettingClampsBreakerAndRetryRanges` 的输入中加入 `RetentionDays: 0`，并增加 `assert.Equal(t, 7, updated.RetentionDays)`；该断言在 `normalize` 尚未处理 Retention 时先失败。

然后在 `setting/smart_routing_setting/config_test.go` 增加：

```go
func TestSettingConcurrentReadWriteKeepsNormalizedSnapshot(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	var wait sync.WaitGroup
	var invalid atomic.Bool
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				if worker%2 == 0 {
					UpdateSetting(SmartRoutingSetting{
						Enabled: true, Mode: ModeBalanced,
						WeightAvailability: 2, WeightLatency: 1,
						WeightThroughput: 1, TopK: 3,
					})
					continue
				}
				snapshot := GetSetting()
				total := snapshot.WeightAvailability + snapshot.WeightLatency +
					snapshot.WeightThroughput + snapshot.WeightCost
				if math.Abs(total-1.0) > 0.000001 {
					invalid.Store(true)
				}
			}
		}(worker)
	}
	wait.Wait()
	assert.False(t, invalid.Load())
}
```

补充 `math`、`sync`、`sync/atomic` import。该测试断言真实快照不变量，不断言锁调用次数，也不从工作 Goroutine 直接调用测试断言。

- [ ] **Step 6: 运行 GREEN 与 Race 验证**

Run: `go test ./setting/config ./setting/smart_routing_setting -count=1`

Expected: PASS。

Run: `go test -race ./setting/config ./setting/smart_routing_setting -count=1`

Expected: PASS，且无 race report。

- [ ] **Step 7: 提交配置并发安全**

```bash
git add setting/config/config.go setting/config/config_test.go setting/smart_routing_setting/config.go setting/smart_routing_setting/config_test.go
git commit -m "fix: make routing settings concurrency safe"
```

### Task 2: 关闭态保护与有界指标收集器

**Files:**
- Modify: `pkg/routing_metrics/metrics.go`
- Modify: `pkg/routing_metrics/metrics_test.go`

- [ ] **Step 1: 写关闭态和容量边界失败测试**

在 `pkg/routing_metrics/metrics_test.go` 增加：

```go
func TestRoutingMetricsDoNotAllocateWhenDisabled(t *testing.T) {
	ResetForTest()
	smart_routing_setting.ResetForTest()
	t.Cleanup(func() {
		ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	info := &relaycommon.RelayInfo{
		UsingGroup: "default", OriginModelName: "gpt-test",
		StartTime: time.Now(), ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 10},
	}
	release := BeginInflight(nil, info, 10)
	RecordAttempt(nil, info, 10, nil)
	release()

	assert.Empty(t, Snapshots())
	assert.Equal(t, int64(0), InflightCount(InflightKey{
		ChannelID: 10, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model: "gpt-test", Group: "default",
	}))
	stats := RuntimeStats()
	assert.Zero(t, stats.Buckets)
	assert.Zero(t, stats.InflightKeys)
}

func TestRoutingMetricsEnforceBucketLimitAndEvictOldest(t *testing.T) {
	ResetForTest()
	configureLimitsForTest(t, Limits{MaxBuckets: 2, BucketTTL: time.Hour, MaxInflightKeys: 2})

	for index := 0; index < 3; index++ {
		recordBucket(bucketKey{
			channelID: index + 1, apiKeyIndex: -1,
			modelName: "gpt-test", group: "default", bucketTs: int64(index + 1),
		}, 10, 0, false, 10, nil)
	}

	stats := RuntimeStats()
	assert.Equal(t, int64(2), stats.Buckets)
	assert.Equal(t, int64(1), stats.BucketEvictions)
	assert.Len(t, Snapshots(), 2)
}

func TestRoutingMetricsDropNewInflightKeyAtLimit(t *testing.T) {
	ResetForTest()
	configureLimitsForTest(t, Limits{MaxBuckets: 2, BucketTTL: time.Hour, MaxInflightKeys: 1})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true, Mode: smart_routing_setting.ModeObserve,
		MetricBucketSec: 60, FlushIntervalMin: 1, SyncIntervalMin: 1, HotcacheRefreshSec: 1,
	})

	first := &relaycommon.RelayInfo{UsingGroup: "default", OriginModelName: "a", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1}}
	second := &relaycommon.RelayInfo{UsingGroup: "default", OriginModelName: "b", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 2}}
	releaseFirst := BeginInflight(nil, first, 1)
	releaseSecond := BeginInflight(nil, second, 2)

	assert.Equal(t, int64(1), RuntimeStats().InflightKeys)
	assert.Equal(t, int64(1), RuntimeStats().InflightDrops)
	releaseSecond()
	releaseFirst()
	assert.Zero(t, RuntimeStats().InflightKeys)
}
```

补充 `smart_routing_setting` import。`configureLimitsForTest` 放在 `_test.go` 内，通过保存并恢复包级 Limits 完成清理，不向生产 API 增加仅供测试的方法。

现有所有调用 `RecordAttempt` / `BeginInflight` 的测试必须通过一个 `_test.go` 内的 `enableRoutingMetricsForTest(t)` 显式启用 Observe 模式；这样关闭态测试与正常收集测试的前置状态不会相互隐式依赖。

- [ ] **Step 2: 运行测试确认 RED**

Run: `go test ./pkg/routing_metrics -run 'TestRoutingMetrics(DoNotAllocate|EnforceBucket|DropNewInflight)' -count=1`

Expected: FAIL，缺少 `Limits`、`RuntimeStats`，且关闭态仍产生快照。

- [ ] **Step 3: 定义运行时限制与统计**

在 `pkg/routing_metrics/metrics.go` 增加：

```go
type Limits struct {
	MaxBuckets     int
	BucketTTL      time.Duration
	MaxInflightKeys int
}

type Stats struct {
	Buckets        int64
	InflightKeys   int64
	BucketEvictions int64
	InflightDrops  int64
}

var defaultLimits = Limits{
	MaxBuckets: 20_000,
	BucketTTL:  10 * time.Minute,
	MaxInflightKeys: 20_000,
}

var limits = defaultLimits
var bucketEntries atomic.Int64
var inflightEntries atomic.Int64
var bucketEvictions atomic.Int64
var inflightDrops atomic.Int64
var bucketMaintenanceMu sync.Mutex
var inflightMaintenanceMu sync.Mutex

func RuntimeStats() Stats {
	return Stats{
		Buckets: bucketEntries.Load(), InflightKeys: inflightEntries.Load(),
		BucketEvictions: bucketEvictions.Load(), InflightDrops: inflightDrops.Load(),
	}
}
```

Limits 的测试配置直接由同包测试保存/恢复 `limits`；生产代码只使用归一化后的默认值。

- [ ] **Step 4: 在入口处阻止关闭态分配**

在 `BeginInflight` 与 `RecordAttempt` 的第一行加入：

```go
if !smart_routing_setting.Enabled() {
	return func() {}
}
```

`RecordAttempt` 的返回类型为 `void`，对应写成：

```go
if !smart_routing_setting.Enabled() {
	return
}
```

- [ ] **Step 5: 实现有界 Bucket 创建与删除**

把 `withWritableBucket` 的 `LoadOrStore` 改为调用 `writableBucket(key)`。核心逻辑为：

```go
func writableBucket(key bucketKey) *bucket {
	if actual, ok := buckets.Load(key); ok {
		return actual.(*bucket)
	}
	bucketMaintenanceMu.Lock()
	defer bucketMaintenanceMu.Unlock()
	if actual, ok := buckets.Load(key); ok {
		return actual.(*bucket)
	}

	pruneExpiredBucketsLocked(key.bucketTs)
	for bucketEntries.Load() >= int64(normalizedLimits().MaxBuckets) {
		if !evictOldestBucketLocked() {
			break
		}
	}
	created := &bucket{}
	buckets.Store(key, created)
	bucketEntries.Add(1)
	return created
}
```

`evictOldestBucketLocked` 必须按 `bucketTs`、Channel、Key、Model、Group 的稳定顺序选择最旧项；锁住 Bucket、设置 `draining=true` 后再 `CompareAndDelete`，成功时减少 `bucketEntries` 并增加 `bucketEvictions`。`DrainSnapshots`、`ClearChannel`、`ResetForTest` 也必须只在实际删除成功时减少计数。

- [ ] **Step 6: 实现有界 Inflight Key**

将 `inflightCounter` 改为：

```go
func inflightCounter(key InflightKey) (*atomic.Int64, bool) {
	if actual, ok := inflight.Load(key); ok {
		return actual.(*atomic.Int64), true
	}
	inflightMaintenanceMu.Lock()
	defer inflightMaintenanceMu.Unlock()
	if actual, ok := inflight.Load(key); ok {
		return actual.(*atomic.Int64), true
	}
	if inflightEntries.Load() >= int64(normalizedLimits().MaxInflightKeys) {
		inflightDrops.Add(1)
		return nil, false
	}
	created := &atomic.Int64{}
	inflight.Store(key, created)
	inflightEntries.Add(1)
	return created, true
}
```

`BeginInflight` 只保存成功获得 Counter 的键；Release 使用 `CompareAndDelete`，并在成功删除时减少 `inflightEntries`。

- [ ] **Step 7: 运行完整指标测试与 Race**

Run: `go test ./pkg/routing_metrics -count=1`

Expected: PASS。

Run: `go test -race ./pkg/routing_metrics -count=1`

Expected: PASS，且无 race report。

- [ ] **Step 8: 提交有界指标收集器**

```bash
git add pkg/routing_metrics/metrics.go pkg/routing_metrics/metrics_test.go
git commit -m "fix: bound routing telemetry state"
```

### Task 3: 有界 Breaker 与 Hot Cache

**Files:**
- Modify: `pkg/routing_breaker/breaker.go`
- Modify: `pkg/routing_breaker/breaker_test.go`
- Modify: `pkg/routing_hotcache/cache.go`
- Modify: `pkg/routing_hotcache/cache_test.go`
- Modify: `controller/relay.go`
- Modify: `controller/relay_retry_test.go`

- [ ] **Step 1: 写 Breaker 容量与 TTL 失败测试**

在 `pkg/routing_breaker/breaker_test.go` 增加：

```go
func TestBreakerEvictsExpiredAndOldestEntriesAtLimit(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}
	breaker := New(Config{
		Consecutive5xxThreshold: 1,
		BaseCooldown: time.Second, MaxCooldown: time.Minute,
		EntryTTL: time.Minute, MaxEntries: 2, Now: clock.Now,
	})

	first := Key{ChannelID: 1, APIKeyIndex: -1, Model: "a", Group: "default"}
	second := Key{ChannelID: 2, APIKeyIndex: -1, Model: "b", Group: "default"}
	third := Key{ChannelID: 3, APIKeyIndex: -1, Model: "c", Group: "default"}
	breaker.OnSuccess(first)
	clock.Advance(30 * time.Second)
	breaker.OnSuccess(second)
	breaker.OnSuccess(third)

	stats := breaker.Stats()
	assert.Equal(t, 2, stats.Entries)
	assert.Equal(t, int64(1), stats.Evictions)
	assert.Equal(t, StateHealthy, breaker.GetSnapshot(first).State)
	assert.Equal(t, 2, breaker.Stats().Entries)

	clock.Advance(2 * time.Minute)
	breaker.OnSuccess(third)
	assert.LessOrEqual(t, breaker.Stats().Entries, 2)
	assert.GreaterOrEqual(t, breaker.Stats().Evictions, int64(2))
}
```

- [ ] **Step 2: 写 Hot Cache 清理失败测试**

在 `pkg/routing_hotcache/cache_test.go` 增加：

```go
func TestHotcachePruneRemovesStaleEntries(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	oldKey := Key{ChannelID: 1, APIKeyIndex: -1, Model: "old", Group: "default"}
	newKey := Key{ChannelID: 2, APIKeyIndex: -1, Model: "new", Group: "default"}
	SetMetric(oldKey, MetricSnapshot{RequestCount: 1, UpdatedUnix: 100})
	SetMetric(newKey, MetricSnapshot{RequestCount: 1, UpdatedUnix: 190})
	SetBreaker(oldKey, BreakerSnapshot{State: "healthy", UpdatedUnix: 100})
	SetBreaker(newKey, BreakerSnapshot{State: "healthy", UpdatedUnix: 190})

	removed := Prune(200, 50)
	assert.Equal(t, 2, removed)
	_, ok := GetMetric(oldKey)
	assert.False(t, ok)
	_, ok = GetBreaker(oldKey)
	assert.False(t, ok)
	_, ok = GetMetric(newKey)
	assert.True(t, ok)
}
```

- [ ] **Step 3: 写关闭态 Breaker 失败测试**

将 `controller/relay_retry_test.go` 中任务/Breaker 测试显式启用设置，并新增：

```go
func TestRecordRoutingBreakerAttemptDoesNothingWhenDisabled(t *testing.T) {
	smart_routing_setting.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
	})

	info := &relaycommon.RelayInfo{
		UsingGroup: "default", OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 99},
	}
	recordRoutingBreakerAttempt(nil, info, 99,
		types.NewErrorWithStatusCode(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway))

	assert.Empty(t, routingbreaker.DirtySnapshots())
	_, ok := routinghotcache.GetBreaker(routinghotcache.Key{
		ChannelID: 99, APIKeyIndex: -1, Model: "gpt-test", Group: "default",
	})
	assert.False(t, ok)
}
```

- [ ] **Step 4: 运行测试确认 RED**

Run: `go test ./pkg/routing_breaker ./pkg/routing_hotcache ./controller -run 'Test(BreakerEvicts|HotcachePrune|RecordRoutingBreakerAttemptDoesNothing)' -count=1`

Expected: FAIL，缺少容量字段、Stats、Prune，且关闭态仍写 Breaker。

- [ ] **Step 5: 为 Breaker 增加硬边界**

扩展 `Config`：

```go
EntryTTL   time.Duration
MaxEntries int
```

扩展 `Breaker`：

```go
evictions int64
```

增加：

```go
type Stats struct {
	Entries   int
	Dirty     int
	Evictions int64
}

func (b *Breaker) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Stats{Entries: len(b.states), Dirty: len(b.dirty), Evictions: b.evictions}
}
```

`DefaultConfig` 使用 `EntryTTL: 30 * time.Minute`、`MaxEntries: 20_000`。`normalizeConfig` 修正非正值。`getOrCreate` 在创建新条目前调用 `pruneLocked(now)`；先删除超 TTL 条目，再按 Healthy → Degraded → Open/Half-open、最后更新时间、Key 稳定排序裁剪到 `MaxEntries-1`。每次 Eviction 同时删除 `states` 和 `dirty` 并增加统计，确保硬上限优先于保留低价值旧状态。

- [ ] **Step 6: 为 Hot Cache 增加 Prune 与分类上限**

在 `pkg/routing_hotcache/cache.go` 增加：

```go
type Limits struct {
	MaxMetricEntries  int
	MaxCostEntries    int
	MaxBreakerEntries int
	MaxHealthEntries  int
}

var limits = Limits{
	MaxMetricEntries: 20_000, MaxCostEntries: 10_000,
	MaxBreakerEntries: 20_000, MaxHealthEntries: 10_000,
}

func Prune(nowUnix int64, staleSeconds int64) int {
	if nowUnix <= 0 || staleSeconds <= 0 {
		return 0
	}
	cutoff := nowUnix - staleSeconds
	cache.Lock()
	defer cache.Unlock()
	removed := 0
	for key, value := range cache.metrics {
		if value.UpdatedUnix > 0 && value.UpdatedUnix < cutoff {
			delete(cache.metrics, key)
			removed++
		}
	}
	for key, value := range cache.costs {
		if value.UpdatedUnix > 0 && value.UpdatedUnix < cutoff {
			delete(cache.costs, key)
			removed++
		}
	}
	for key, value := range cache.breakers {
		if value.UpdatedUnix > 0 && value.UpdatedUnix < cutoff {
			delete(cache.breakers, key)
			removed++
		}
	}
	for key, value := range cache.authFailures {
		if value.UpdatedUnix > 0 && value.UpdatedUnix < cutoff {
			delete(cache.authFailures, key)
			removed++
		}
	}
	for key, value := range cache.balances {
		if value.UpdatedUnix > 0 && value.UpdatedUnix < cutoff {
			delete(cache.balances, key)
			removed++
		}
	}
	removed += trimCacheLocked()
	return removed
}
```

`trimCacheLocked` 对五类 Map 按 `UpdatedUnix` 删除最旧条目直到各自上限。所有 `Set*` 和 `Load*` 在批量写完后调用对应裁剪逻辑，保证即使没有后台 Prune 也不能突破硬上限。

扩展前面的 Hot Cache 测试：同包测试先保存 `limits`，把 `MaxMetricEntries` 设为 2，写入三个不同 Key 后断言只保留更新时间最新的两个，再由 `t.Cleanup` 恢复。不得用 20,000 次循环模拟容量测试。

- [ ] **Step 7: 关闭态直接返回**

在 `recordRoutingBreakerAttempt` 开头读取一次设置并返回：

```go
breakerSetting := smart_routing_setting.GetSetting()
if !breakerSetting.Enabled {
	return
}
```

后续删除重复的 `GetSetting` 和 `if Enabled` 包裹，直接使用该快照同步配置。

- [ ] **Step 8: 运行 GREEN 与 Race**

Run: `go test ./pkg/routing_breaker ./pkg/routing_hotcache ./controller -count=1`

Expected: PASS。

Run: `go test -race ./pkg/routing_breaker ./pkg/routing_hotcache ./controller -count=1`

Expected: PASS，且无 race report。

- [ ] **Step 9: 提交 Breaker/Hot Cache 边界**

```bash
git add pkg/routing_breaker/breaker.go pkg/routing_breaker/breaker_test.go pkg/routing_hotcache/cache.go pkg/routing_hotcache/cache_test.go controller/relay.go controller/relay_retry_test.go
git commit -m "fix: bound routing health caches"
```

### Task 4: Sub2API JWT Singleflight 与缓存上限

**Files:**
- Modify: `controller/smart_routing_sub2api.go`
- Modify: `controller/smart_routing_task_test.go`

- [ ] **Step 1: 写并发登录只执行一次的失败测试**

在 `controller/smart_routing_task_test.go` 增加使用真实 `httptest.Server` 的测试：

```go
func TestRoutingSub2APIJWTCoalescesConcurrentLogin(t *testing.T) {
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	var logins atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"token":"jwt-secret","expires_in":3600}}`)
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{ChannelID: 901, BaseURL: server.URL, UpstreamType: model.RoutingUpstreamTypeSub2API}
	credentials := model.RoutingCredentials{Sub2APIEmail: "admin@example.com", Sub2APIPassword: "secret"}
	start := make(chan struct{})
	type loginResult struct {
		token string
		err   error
	}
	results := make(chan loginResult, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			token, err := routingSub2APIJWT(context.Background(), binding, credentials)
			results <- loginResult{token: token, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		assert.Equal(t, "jwt-secret", result.token)
	}
	assert.Equal(t, int64(1), logins.Load())
}
```

该测试验证真实 HTTP 登录副作用，不检查 Singleflight 内部调用。

- [ ] **Step 2: 写本机 JWT 缓存裁剪失败测试**

```go
func TestRoutingSub2APIJWTCachePrunesExpiredAndOldestEntries(t *testing.T) {
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)
	now := int64(1_000)
	routingSub2APIJWTCache.Lock()
	routingSub2APIJWTCache.values[1] = routingSub2APIJWTCacheEntry{ExpiresAt: now - 1}
	routingSub2APIJWTCache.values[2] = routingSub2APIJWTCacheEntry{ExpiresAt: now + 10}
	routingSub2APIJWTCache.values[3] = routingSub2APIJWTCacheEntry{ExpiresAt: now + 20}
	pruneRoutingSub2APIJWTCacheLocked(now, 1)
	remaining := len(routingSub2APIJWTCache.values)
	_, newestKept := routingSub2APIJWTCache.values[3]
	routingSub2APIJWTCache.Unlock()

	assert.Equal(t, 1, remaining)
	assert.True(t, newestKept)
}
```

- [ ] **Step 3: 运行测试确认 RED**

Run: `go test ./controller -run 'TestRoutingSub2APIJWT(Coalesces|CachePrunes)' -count=1`

Expected: FAIL，永久 Lock Map 仍存在且缺少 Prune helper。

- [ ] **Step 4: 用 Singleflight 取代 Lock Map**

增加 import：

```go
"strconv"

"golang.org/x/sync/singleflight"
```

将缓存结构改为：

```go
var routingSub2APIJWTCache = struct {
	sync.Mutex
	values map[int]routingSub2APIJWTCacheEntry
}{values: map[int]routingSub2APIJWTCacheEntry{}}

var routingSub2APILoginGroup singleflight.Group
```

将本地 Mutex 段替换为：

```go
result, err, _ := routingSub2APILoginGroup.Do(strconv.Itoa(binding.ChannelID), func() (any, error) {
	if token, ok := getRoutingSub2APICachedJWT(ctx, binding.ChannelID); ok {
		return token, nil
	}
	unlockRedis, err := acquireRoutingSub2APIRedisLock(ctx, binding.ChannelID)
	if err != nil {
		return "", err
	}
	if unlockRedis != nil {
		defer unlockRedis()
	}
	if token, ok := getRoutingSub2APICachedJWT(ctx, binding.ChannelID); ok {
		return token, nil
	}
	token, ttl, err := loginRoutingSub2API(ctx, binding, credentials)
	if err != nil {
		return "", err
	}
	setRoutingSub2APICachedJWT(ctx, binding.ChannelID, token, ttl)
	return token, nil
})
if err != nil {
	return "", err
}
return result.(string), nil
```

删除 `routingSub2APILocalLock` 与 `locks` Map。

- [ ] **Step 5: 实现缓存 TTL 与硬容量裁剪**

增加：

```go
const routingSub2APIMaxJWTEntries = 4_096

func pruneRoutingSub2APIJWTCacheLocked(now int64, maxEntries int) {
	for channelID, entry := range routingSub2APIJWTCache.values {
		if entry.ExpiresAt <= now {
			delete(routingSub2APIJWTCache.values, channelID)
		}
	}
	for len(routingSub2APIJWTCache.values) > maxEntries {
		oldestID := 0
		oldestExpiry := int64(math.MaxInt64)
		for channelID, entry := range routingSub2APIJWTCache.values {
			if entry.ExpiresAt < oldestExpiry || (entry.ExpiresAt == oldestExpiry && channelID < oldestID) {
				oldestID = channelID
				oldestExpiry = entry.ExpiresAt
			}
		}
		delete(routingSub2APIJWTCache.values, oldestID)
	}
}
```

`get` 在读前清理过期项；`set` 写入后调用 `pruneRoutingSub2APIJWTCacheLocked(now, routingSub2APIMaxJWTEntries)`。`resetRoutingSub2APITestState` 同时将 `routingSub2APILoginGroup` 重置为零值。

- [ ] **Step 6: 运行 GREEN 与 Race**

Run: `go test ./controller -run 'RoutingSub2API' -count=1`

Expected: PASS。

Run: `go test -race ./controller -run 'RoutingSub2API' -count=1`

Expected: PASS，且并发登录测试只收到一次 HTTP 登录。

- [ ] **Step 7: 提交 JWT 协调修复**

```bash
git add controller/smart_routing_sub2api.go controller/smart_routing_task_test.go
git commit -m "fix: bound sub2api routing auth state"
```

### Task 5: 可取消运行时与 Retention

**Files:**
- Modify: `model/routing_model.go`
- Modify: `model/routing_model_test.go`
- Modify: `controller/system_task_handlers.go`
- Create: `controller/smart_routing_runtime_test.go`
- Modify: `main.go`

- [ ] **Step 1: 写跨数据库 Retention 失败测试**

在 `model/routing_model_test.go` 的 `runRoutingMigrationAndUpsertContract` 中，在已有 metric upsert 后插入新旧两个桶，并断言：

```go
oldMetric := RoutingChannelMetric{
	ChannelID: 44, APIKeyIndex: -1, ModelName: "old-model", Group: "default",
	BucketTs: 100, RequestCount: 1, SuccessCount: 1,
}
newMetric := RoutingChannelMetric{
	ChannelID: 44, APIKeyIndex: -1, ModelName: "new-model", Group: "default",
	BucketTs: 200, RequestCount: 1, SuccessCount: 1,
}
require.NoError(t, db.Create(&oldMetric).Error)
require.NoError(t, db.Create(&newMetric).Error)
deleted, err := DeleteRoutingMetricsBefore(150)
require.NoError(t, err)
assert.Equal(t, int64(1), deleted)
var remaining []RoutingChannelMetric
require.NoError(t, db.Where("channel_id = ?", 44).Order("bucket_ts asc").Find(&remaining).Error)
require.Len(t, remaining, 1)
assert.Equal(t, int64(200), remaining[0].BucketTs)
```

- [ ] **Step 2: 写 Runtime 生命周期失败测试**

创建 `controller/smart_routing_runtime_test.go`：

```go
package controller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmartRoutingRuntimeRunsAndStopsWithoutLeakingWorkers(t *testing.T) {
	refreshRan := make(chan struct{}, 1)
	flushRan := make(chan struct{}, 1)
	var refreshes atomic.Int64
	var flushes atomic.Int64
	deps := smartRoutingRuntimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true, HotcacheRefreshSec: 1, FlushIntervalMin: 1}
		},
		refresh: func(smart_routing_setting.SmartRoutingSetting) {
			refreshes.Add(1)
			refreshRan <- struct{}{}
		},
		flush: func(smart_routing_setting.SmartRoutingSetting) {
			flushes.Add(1)
			flushRan <- struct{}{}
		},
		waitRefresh: func(ctx context.Context, _ time.Duration) bool {
			<-ctx.Done()
			return false
		},
		waitFlush: func(ctx context.Context, _ time.Duration) bool {
			<-ctx.Done()
			return false
		},
	}

	runtime := newSmartRoutingRuntime(context.Background(), deps)
	select {
	case <-refreshRan:
	case <-time.After(time.Second):
		require.FailNow(t, "refresh worker did not start")
	}
	select {
	case <-flushRan:
	case <-time.After(time.Second):
		require.FailNow(t, "flush worker did not start")
	}
	runtime.Close()
	runtime.Close()
	assert.Equal(t, int64(1), refreshes.Load())
	assert.Equal(t, int64(1), flushes.Load())
}

func TestFlushRoutingRuntimeStateAppliesConfiguredRetention(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	smartRoutingRetentionLast.Store(0)
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
		smartRoutingRetentionLast.Store(0)
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true, Mode: smart_routing_setting.ModeObserve,
		RetentionDays: 1, MetricBucketSec: 60, FlushIntervalMin: 1,
		SyncIntervalMin: 1, HotcacheRefreshSec: 1,
	})

	now := common.GetTimestamp()
	require.NoError(t, db.Create(&model.RoutingChannelMetric{
		ChannelID: 1, APIKeyIndex: -1, ModelName: "expired", Group: "default",
		BucketTs: now - 2*24*60*60, RequestCount: 1, SuccessCount: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelMetric{
		ChannelID: 2, APIKeyIndex: -1, ModelName: "fresh", Group: "default",
		BucketTs: now, RequestCount: 1, SuccessCount: 1,
	}).Error)

	summary, err := flushRoutingRuntimeState()
	require.NoError(t, err)
	assert.EqualValues(t, 1, summary["retained_metrics_deleted"])
	var remaining []model.RoutingChannelMetric
	require.NoError(t, db.Order("channel_id asc").Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, "fresh", remaining[0].ModelName)
}
```

补充 `common`、`model`、`routingmetrics` import。第一个测试执行真实循环并用通道观察首次运行；Wait 依赖只替换时钟边界，Context 取消后必须解除阻塞，不断言内部锁或 Goroutine 数。第二个测试使用真实 SQLite/GORM 路径证明设置确实驱动清理。

- [ ] **Step 3: 运行测试确认 RED**

Run: `go test ./model ./controller -run 'TestSmartRoutingRuntimeRunsAndStops|TestFlushRoutingRuntimeStateAppliesConfiguredRetention|TestRoutingModels' -count=1`

Expected: FAIL，缺少 `DeleteRoutingMetricsBefore` 和 Runtime 类型。

- [ ] **Step 4: 实现跨数据库 Retention 删除**

在 `model/routing_model.go` 增加：

```go
func DeleteRoutingMetricsBefore(cutoffTs int64) (int64, error) {
	if cutoffTs <= 0 {
		return 0, nil
	}
	result := DB.Where("bucket_ts < ?", cutoffTs).Delete(&RoutingChannelMetric{})
	return result.RowsAffected, result.Error
}
```

只使用 GORM Where/Delete，确保三种数据库一致。

- [ ] **Step 5: 实现可取消 Runtime**

在 `controller/system_task_handlers.go` 用以下结构替代 `sync.Once` 和两个永久 Goroutine：

```go
type SmartRoutingRuntime struct {
	cancel context.CancelFunc
	wait   sync.WaitGroup
	close  sync.Once
}

type smartRoutingRuntimeDeps struct {
	getSetting  func() smart_routing_setting.SmartRoutingSetting
	refresh     func(smart_routing_setting.SmartRoutingSetting)
	flush       func(smart_routing_setting.SmartRoutingSetting)
	waitRefresh func(context.Context, time.Duration) bool
	waitFlush   func(context.Context, time.Duration) bool
}

func StartSmartRoutingRuntime(parent context.Context) *SmartRoutingRuntime {
	return newSmartRoutingRuntime(parent, smartRoutingRuntimeDeps{
		getSetting: smart_routing_setting.GetSetting,
		refresh: func(setting smart_routing_setting.SmartRoutingSetting) {
			if setting.Enabled {
				syncRoutingBreakerConfigFromSetting(setting)
				_, _ = refreshRoutingHotcacheFromDB()
			}
			routinghotcache.Prune(common.GetTimestamp(), int64(setting.SnapshotStaleSec))
		},
		flush: func(setting smart_routing_setting.SmartRoutingSetting) {
			if setting.Enabled {
				syncRoutingBreakerConfigFromSetting(setting)
				_, _ = flushRoutingRuntimeState()
			}
		},
		waitRefresh: waitRoutingRuntime,
		waitFlush: waitRoutingRuntime,
	})
}

func waitRoutingRuntime(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
```

`newSmartRoutingRuntime` 启动两个循环；每轮只读取一次设置，把同一快照传给任务，再按该快照计算下一间隔。首次任务立即运行，之后等待；`Close` 使用 `sync.Once` 取消 Context 并等待两个循环退出。

- [ ] **Step 6: 把 Retention 接入 Flush**

增加包级 `smartRoutingRetentionLast atomic.Int64`。在 `flushRoutingRuntimeState` 成功持久化后：

```go
setting := smart_routing_setting.GetSetting()
now := common.GetTimestamp()
if setting.RetentionDays > 0 && now-smartRoutingRetentionLast.Load() >= 6*60*60 {
	cutoff := now - int64(setting.RetentionDays)*24*60*60
	if deleted, err := model.DeleteRoutingMetricsBefore(cutoff); err != nil {
		return summary, err
	} else {
		summary["retained_metrics_deleted"] = deleted
		smartRoutingRetentionLast.Store(now)
	}
}
```

`normalize` 将 `RetentionDays < 1` 恢复为默认 7，防止无意关闭清理。

- [ ] **Step 7: 在 main 持有并关闭 Runtime**

将：

```go
controller.StartSmartRoutingRuntime()
```

改为：

```go
routingRuntime := controller.StartSmartRoutingRuntime(context.Background())
```

收到退出信号后、HTTP Server Shutdown 前调用：

```go
routingRuntime.Close()
```

- [ ] **Step 8: 运行 GREEN、Race 与全量测试**

Run: `go test ./model ./controller -count=1`

Expected: PASS；若配置了 `TEST_MYSQL_DSN` / `TEST_POSTGRES_DSN`，外部数据库契约同步执行。

Run: `go test -race ./controller ./pkg/routing_metrics ./pkg/routing_breaker ./pkg/routing_hotcache ./setting/smart_routing_setting -count=1`

Expected: PASS，且无 race report。

Run: `go test ./... -count=1`

Expected: PASS（执行前确保 `web/default/dist` 与 `web/classic/dist` 已由构建生成）。

- [ ] **Step 9: 提交 Runtime 与 Retention**

```bash
git add model/routing_model.go model/routing_model_test.go controller/system_task_handlers.go controller/smart_routing_runtime_test.go main.go
git commit -m "fix: lifecycle routing runtime workers"
```

### Task 6: Phase 0A 需求审计与最终验证

**Files:**
- Modify if needed: files changed in Tasks 1–5
- Update: `docs/superpowers/plans/2026-07-10-channel-routing-phase0-runtime-safety.md`

- [ ] **Step 1: 对照设计逐项审计**

确认并记录证据：

- 功能关闭后 `RecordAttempt`、`BeginInflight`、Breaker 不增加条目。
- 指标 Bucket、Inflight Key、Breaker、Hot Cache、JWT Cache 均有硬上限。
- Bucket、Breaker、Hot Cache、JWT Cache 均有 TTL/过期清理。
- Eviction/Drop 通过 Stats 或运行摘要可观测。
- Setting 并发读写通过同一 ConfigManager 锁。
- Runtime 支持 Context、幂等 Close 和 Wait，不再使用永久 `time.Sleep`。
- `RetentionDays` 能实际删除过期指标，且使用跨数据库兼容 GORM。
- Sub2API 登录不再保留永久 Lock Map。

- [ ] **Step 2: 运行格式和静态检查**

Run: `gofmt -w setting/config/config.go setting/config/config_test.go setting/smart_routing_setting/config.go setting/smart_routing_setting/config_test.go pkg/routing_metrics/metrics.go pkg/routing_metrics/metrics_test.go pkg/routing_breaker/breaker.go pkg/routing_breaker/breaker_test.go pkg/routing_hotcache/cache.go pkg/routing_hotcache/cache_test.go controller/relay.go controller/relay_retry_test.go controller/smart_routing_sub2api.go controller/smart_routing_task_test.go controller/system_task_handlers.go controller/smart_routing_runtime_test.go model/routing_model.go model/routing_model_test.go main.go`

Expected: exit 0。

Run: `go vet ./setting/config ./setting/smart_routing_setting ./pkg/routing_metrics ./pkg/routing_breaker ./pkg/routing_hotcache ./controller ./model`

Expected: exit 0。

- [ ] **Step 3: 运行新鲜全量验证**

Run: `go test ./... -count=1`

Expected: PASS，0 failures。

Run: `go test -race ./setting/config ./setting/smart_routing_setting ./pkg/routing_metrics ./pkg/routing_breaker ./pkg/routing_hotcache ./controller -count=1`

Expected: PASS，0 race reports。

Run: `bun run typecheck`（workdir: `web/default`）

Expected: exit 0；本阶段没有前端改动，但确保分支基线未被破坏。

Run: `bun run build`（workdir: `web/default`）

Expected: exit 0。

- [ ] **Step 4: 检查 Diff 和工作树**

Run: `git diff --check`

Expected: 无输出。

Run: `git status --short`

Expected: 只包含本计划文档的勾选更新（若已提交所有代码）。

- [ ] **Step 5: 提交计划执行记录**

```bash
git add docs/superpowers/plans/2026-07-10-channel-routing-phase0-runtime-safety.md
git commit -m "docs: record phase 0a routing verification"
```

完成本计划后，下一份计划必须是 `Phase 0B：错误责任分类、Reliability/Capacity 分离与 Multi-Key 语义`；不得直接进入前端或扩大路由流量。
