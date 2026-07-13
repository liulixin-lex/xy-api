# 渠道路由 Phase 0D / Gate 1 强化实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 收敛 Phase 0 剩余的指标生命周期、配置原子性、后台运行时、成本同步出站安全与错误持久化缺口，形成可重复验证的 Gate 1 证据，同时保持旧兼容 API 和现有用户计费语义不变。

**Architecture:** `perf_metrics` 保留“最终逻辑请求”语义，但改为锁保护的有界本机桶、可取消运行时和上下文感知的 GORM 持久化；`routing_metrics` 继续表示每次渠道 attempt，二者只共享生命周期/退避基础设施，不合并请求计数。Smart Routing 设置使用 persist-then-publish，后台任务使用 Context、错误返回、Capped Exponential Backoff + 可注入 Full Jitter；成本同步统一走专用、默认拒绝的 HTTPS Egress Client，并在响应边界执行媒体类型、压缩前后大小和错误脱敏约束。

**Tech Stack:** Go 1.22+、Gin、GORM v2、SQLite/MySQL/PostgreSQL、go-redis、`net/http`/`crypto/tls`/`crypto/x509`、Testify。

---

## 范围与强制语义

- 本计划只完成 Phase 0D / Gate 1；不新增 `RoutingPool`、`RoutingMember`、Revision、Outbox、Redis Stream、v2 API、正式前端或任何 Phase 1+ 行为。
- `routing_metrics` 的 `RequestCount` 是**渠道 attempt 数**：一次逻辑请求重试两个渠道时可计为 2。
- `perf_metrics` 的 `RequestCount` 是**最终逻辑请求数**：同一逻辑请求无论重试多少次，只在成功结算或终态失败边界计 1。
- 不把上述两个计数相加、复制、去重或写入同一 Redis/DB bucket；Phase 0D 只统一“有界、可取消、可等待、可退避、可观测”的生命周期契约。
- 删除 `perf_metrics` 的 Redis 每请求写后，多节点的未落库活动桶只在本节点可见；各节点按 `(model, group, bucket_ts)` 以加法 Upsert 汇聚到数据库。该最终一致窗口是 Gate 1 的明确行为，实时跨节点增量/去重属于 Phase 2。
- Smart Routing 设置仍由 `options` 表作为真源；本阶段多节点通过现有 `model.SyncOptions` 轮询收敛。Revision/ETag/Redis 传播不在本计划内。
- 成本同步只更新平台成本快照，不修改 `pkg/billingexpr` 的求值、用户预扣/结算、Quota 换算或 `PriceData.OtherRatios`。同步得到的 Billing Expression 继续作为不可信的 opaque 数据保存。
- 所有 JSON 编解码继续使用 `common.Marshal`、`common.Unmarshal`、`common.DecodeJson`；`encoding/json` 只允许作为 `json.RawMessage` 等类型来源。

## 文件职责总览

- `setting/perf_metrics_setting/config.go`、`config_test.go`：ConfigManager Snapshot/Replace、纯归一化和并发快照。
- `pkg/perf_metrics/types.go`、`store.go`、`metrics.go`、`flush.go`：有界桶、线性化 Drain/Requeue、统计和查询。
- `pkg/perf_metrics/runtime.go`、`runtime_test.go`：Context/Close/Wait、首次执行、退避、Retention 和最终 Flush。
- `model/perf_metric.go`、`perf_metric_test.go`：Context-aware GORM Upsert/Delete。
- `common/backoff.go`、`backoff_test.go`：跨 Perf/Smart/Cost Sync 复用的无溢出 Capped Exponential Backoff + Full Jitter。
- `model/option.go`、`option_test.go`：Bulk option 单事务后按配置命名空间一次发布。
- `setting/smart_routing_setting/config.go`、`config_test.go`：纯归一化与已持久化快照发布。
- `controller/smart_routing.go`、`smart_routing_test.go`：persist-then-publish Controller 契约和 HTTPS Base URL 校验。
- `controller/system_task_handlers.go`、`smart_routing_runtime_test.go`：Smart Runtime 生命周期、Context GORM、错误统计、退避和最终 Flush。
- `model/channel.go`、`model/routing_model.go`、`routing_model_test.go`：Context-aware 路由状态查询/写入、成本失败次数迁移和三数据库契约。
- `service/routing_cost_http.go`、`routing_cost_http_test.go`：专用 Fail-closed HTTPS Egress Client。
- `controller/smart_routing_http.go`、`smart_routing_http_test.go`：JSON Content-Type、Wire/Decoded 双限和 gzip 解码。
- `common/error_sanitize.go`、`error_sanitize_test.go`：统一秘密脱敏、控制字符折叠和长度上限。
- `controller/smart_routing_sub2api.go`、`smart_routing_task_test.go`：统一安全 Client/响应读取/错误状态/持久化 Backoff。
- `main.go`：持有两个 Runtime，在 HTTP 排空后执行最终 Wait/Flush。

### Task 1: `perf_metrics_setting` 原子快照与归一化

**Files:**
- Modify: `setting/perf_metrics_setting/config.go`
- Create: `setting/perf_metrics_setting/config_test.go`

- [x] **RED 1：写默认、非法值与 Retention 语义测试**

新增表驱动测试，精确保护：非法 `BucketTime` 回落 `hour`；`FlushInterval <= 0` 归一化为 1 分钟，超过 1440 分钟归一化为 1440；`RetentionDays < 0` 归一化为 0；`RetentionDays == 0` 保持永久保留；正数原样保留。

```go
func TestNormalizePerfMetricsSetting(t *testing.T) {
	tests := []struct {
		name string
		in   PerfMetricsSetting
		want PerfMetricsSetting
	}{
		{"invalid bucket", PerfMetricsSetting{BucketTime: "week", FlushInterval: 5}, PerfMetricsSetting{BucketTime: "hour", FlushInterval: 5}},
		{"minimum flush", PerfMetricsSetting{BucketTime: "minute", FlushInterval: 0}, PerfMetricsSetting{BucketTime: "minute", FlushInterval: 1}},
		{"maximum flush", PerfMetricsSetting{BucketTime: "5min", FlushInterval: 2000}, PerfMetricsSetting{BucketTime: "5min", FlushInterval: 1440}},
		{"zero retention", PerfMetricsSetting{BucketTime: "hour", FlushInterval: 5, RetentionDays: 0}, PerfMetricsSetting{BucketTime: "hour", FlushInterval: 5, RetentionDays: 0}},
		{"negative retention", PerfMetricsSetting{BucketTime: "hour", FlushInterval: 5, RetentionDays: -1}, PerfMetricsSetting{BucketTime: "hour", FlushInterval: 5, RetentionDays: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { assert.Equal(t, tt.want, Normalize(tt.in)) })
	}
}
```

- [x] **RED 2：写并发读写只观察完整快照测试**

测试交替发布 `minute/1/0` 与 `5min/7/30` 两组设置；读者只允许看到这两组归一化结果，不能看到字段混搭。工作 Goroutine 只写 `atomic.Bool`，断言留在主测试 Goroutine。

- [x] **验证 RED**

Run: `go test ./setting/perf_metrics_setting -count=1`

Expected: FAIL，缺少 `Normalize`、`UpdateSetting`、`ResetForTest`，且现有直接全局读存在 race。

- [x] **GREEN 1：改为 ConfigManager Snapshot/Replace**

实现以下稳定 API；`PerfMetricsSetting` 只有标量，Snapshot 的浅复制满足不可变快照要求。

```go
const configName = "perf_metrics_setting"

func Normalize(setting PerfMetricsSetting) PerfMetricsSetting { /* 按 RED 表驱动规则归一化 */ }

func GetSetting() PerfMetricsSetting {
	setting := defaultPerfMetricsSetting
	if !config.GlobalConfig.Snapshot(configName, &setting) {
		setting = defaultPerfMetricsSetting
	}
	return Normalize(setting)
}

func UpdateSetting(setting PerfMetricsSetting) PerfMetricsSetting {
	setting = Normalize(setting)
	config.GlobalConfig.Replace(configName, setting)
	return GetSetting()
}

func ResetForTest() { config.GlobalConfig.Replace(configName, defaultPerfMetricsSetting) }
```

`GetBucketSeconds`、`GetFlushIntervalMinutes` 必须各自只读取一次 `GetSetting()`，不能再次直接访问包级变量。

- [x] **GREEN 2：运行普通与 Race 测试**

Run: `go test ./setting/perf_metrics_setting -count=1`

Run: `go test -race ./setting/perf_metrics_setting -count=1`

Expected: 全部 PASS，无 race report。

- [x] **建议提交**

```bash
git add setting/perf_metrics_setting/config.go setting/perf_metrics_setting/config_test.go
git commit -m "fix: make perf metric settings atomic"
```

### Task 2: `perf_metrics` 有界线性化 Bucket 与完整 Requeue

**Files:**
- Modify: `pkg/perf_metrics/types.go`
- Create: `pkg/perf_metrics/store.go`
- Modify: `pkg/perf_metrics/metrics.go`
- Modify: `pkg/perf_metrics/flush.go`
- Create: `pkg/perf_metrics/store_test.go`
- Create: `pkg/perf_metrics/flush_test.go`

- [x] **RED 1：写 Drain/Record/Delete 线性化测试**

测试不依赖 Sleep：先取出一个待 Flush batch，在 DB 写完成前向同一 key 再 Record；成功确认旧 batch 后，新样本必须仍在桶中，不能因删除旧空桶而丢失。

```go
batch := store.takeFlushable(cutoff, false)
require.Len(t, batch, 1)
require.True(t, store.record(key, secondSample, now))
store.complete(batch[0], nil)
assert.Equal(t, int64(1), store.snapshot(key).requestCount)
```

- [x] **RED 2：写失败时全字段 Requeue 测试**

样本同时携带 request/success/latency/TTFT/output tokens/generation；注入 Upsert 错误后，全部字段必须回到同一个 bucket，并与 Flush 期间新到样本逐字段相加。不得只恢复 `requestCount`。

- [x] **RED 3：写硬容量、Drop/Eviction 统计测试**

测试 Limits=`MaxBuckets:2, BucketTTL:time.Hour`：第三个更新 bucket 到来时先驱逐最旧的非 flushing 已完成 bucket；若两个 bucket 都是当前 bucket 或正在 flushing，则拒绝新 key。精确断言：

```go
assert.Equal(t, int64(2), stats.Buckets)
assert.Equal(t, int64(1), stats.EvictedBuckets)
assert.Equal(t, int64(1), stats.EvictedSamples)
assert.Equal(t, int64(1), stats.DroppedSamples)
```

- [x] **RED 4：写关闭采集后仍维护旧状态测试**

先启用并写入一个已完成 bucket，再禁用采集，调用 maintenance/flush；旧 bucket 仍应落库并被释放。禁用只阻止新 Record，不能跳过 Flush、Prune 或 Retention。

- [x] **验证 RED**

Run: `go test ./pkg/perf_metrics -run 'Test(BucketStore|FlushCompletedBuckets|DisabledCollection)' -count=1`

Expected: FAIL；当前 `sync.Map` 无容量，Delete 与 Record 不线性化，Flush 循环在禁用时直接跳过。

- [x] **GREEN 1：定义 Store、Limits 和 Stats**

```go
type Limits struct {
	MaxBuckets int
	BucketTTL  time.Duration
}

type Stats struct {
	Buckets        int64
	DroppedSamples int64
	EvictedBuckets int64
	EvictedSamples int64
}

type bucketEntry struct {
	counters    counters
	lastTouched int64
	flushing    bool
}

type bucketStore struct {
	mu      sync.Mutex
	buckets map[bucketKey]*bucketEntry
	limits  Limits
	stats   Stats
}
```

生产默认 `MaxBuckets=20_000`、`BucketTTL=24h`。只在 Store 锁内创建、Drain、Requeue、删除和更新统计；测试通过同包构造自定义 Limits，不增加只供测试的导出生产 API。

- [x] **GREEN 2：实现稳定容量策略**

创建新 key 前先删除 TTL 过期且非 flushing 的条目；仍超限时，只选择 `bucketTs < incoming.bucketTs` 的最旧非 flushing 条目，按 `bucketTs/model/group` 稳定排序驱逐。若没有安全候选则丢弃当前样本并增加 `DroppedSamples`，绝不驱逐正在 Flush 的占位条目。

- [x] **GREEN 3：实现两阶段 Flush batch**

`takeFlushable` 在锁内把 counters 移到 `flushBatch` 并保留 `flushing=true` 占位；DB I/O 在锁外执行；`complete(batch,nil)` 仅在占位仍为空时删除，若期间有 Record 则保留；`complete(batch,err)` 把 batch 全字段加回并清除 flushing。对“DB 已提交但连接返回未知错误”的情况仍采用 at-least-once Requeue，可能重复但不能静默丢失，精确去重留给 Phase 2 sequence/outbox。

- [x] **GREEN 4：Query 改读 Store 快照，RuntimeStats 可观测**

`Query`、`QuerySummaryAll` 不再直接 Range `sync.Map`；统一获取 Store 的不可变 counters 副本。统计只返回计数，不包含 model/group 等高基数标签。

- [x] **GREEN 5：禁用时继续维护**

`Record` 仍在 `Enabled=false` 时早退；Flush/Prune/Cleanup 不以 Enabled 为前置条件。这样从启用切换为禁用时，旧数据可安全落库并释放内存。

- [x] **运行 GREEN 与 Race**

Run: `go test ./pkg/perf_metrics -count=1`

Run: `go test -race ./pkg/perf_metrics -count=1`

Expected: 全部 PASS，无 race report。

- [x] **建议提交**

```bash
git add pkg/perf_metrics/types.go pkg/perf_metrics/store.go pkg/perf_metrics/metrics.go pkg/perf_metrics/flush.go pkg/perf_metrics/store_test.go pkg/perf_metrics/flush_test.go
git commit -m "fix: bound and linearize perf metric buckets"
```

### Task 3: 删除 Perf Redis 请求热路径并固定两套计数语义

**Files:**
- Modify: `pkg/perf_metrics/metrics.go`
- Modify: `pkg/perf_metrics/flush.go`
- Create or Modify: `pkg/perf_metrics/metrics_test.go`
- Modify: `controller/relay.go`
- Modify: `service/quota.go`
- Modify: `service/text_quota.go`

- [x] **RED 1：证明 Record 当前产生 Redis 命令**

测试使用 go-redis 自定义 Hook 记录 Pipeline 命令，并把 client 指向立即拒绝连接的本机地址；调用 `Record` 后期望命令列表为空。当前代码会观察到 `HINCRBY`/`EXPIRE`，因此先 RED。

- [x] **RED 2：固定 logical request 与 attempt 不合并**

在测试/审计说明中保留以下最小不变量：一个 Perf `Record` 只增加逻辑请求桶 1；`routingmetrics.RecordClassifiedAttempt` 的已有测试继续按每次 attempt 增加。不得从 Perf 调用 Routing Metrics，也不得从 Routing Metrics 调用 Perf。

- [x] **验证 RED**

Run: `go test ./pkg/perf_metrics -run 'TestRecordDoesNotWriteRedis|TestPerfMetricsCountsLogicalRequests' -count=1`

Expected: `TestRecordDoesNotWriteRedis` FAIL，Hook 捕获 Redis 命令。

- [x] **GREEN 1：删除 Redis 实现与死代码**

删除 `recordRedis`、`mergeRedisActiveBuckets`、`redisBucketKey`、`redisCounters`、`parseRedisInt`，同时删除不再使用的 `context`、`fmt`、`strconv`、`common` import。`pkg/perf_metrics` 不再引用 `common.RDB`、`HIncrBy`、`Expire`。

- [x] **GREEN 2：Perf Record 改为同步本机写入**

Redis I/O 删除后，三处终态记录不再丢进无生命周期保证的 `gopool`：

```go
perfmetrics.RecordRelaySample(relayInfo, false, 0) // controller/relay.go，终态失败
perfmetrics.RecordRelaySample(relayInfo, true, int64(usage.CompletionTokens)) // service/quota.go
perfmetrics.RecordRelaySample(relayInfo, true, int64(summary.CompletionTokens)) // service/text_quota.go
```

同文件其他与 Perf 无关的 `gopool` 保持不变。同步 Record 确保 HTTP handler 返回前样本已进入有界 Store，Shutdown 后的最终 Flush 不会漏掉尚未调度的异步任务。

- [x] **验证语义与源码边界**

Run: `go test ./pkg/perf_metrics ./pkg/routing_metrics ./controller ./service -run 'Test(RecordDoesNotWriteRedis|PerfMetricsCountsLogicalRequests|RecordClassifiedAttempt)' -count=1`

Run: `rg -n 'HIncrBy|Expire|recordRedis|mergeRedisActiveBuckets|common\.RDB' pkg/perf_metrics`

Expected: 测试 PASS；`rg` 无输出。`routing_metrics` attempt 测试仍独立 PASS。

- [x] **建议提交**

```bash
git add pkg/perf_metrics/metrics.go pkg/perf_metrics/flush.go pkg/perf_metrics/metrics_test.go controller/relay.go service/quota.go service/text_quota.go
git commit -m "fix: remove redis perf metric hot writes"
```

### Task 4: Perf Runtime、Retention、Backoff 与最终 Flush

**Files:**
- Create: `common/backoff.go`
- Create: `common/backoff_test.go`
- Modify: `model/perf_metric.go`
- Create: `model/perf_metric_test.go`
- Create: `pkg/perf_metrics/runtime.go`
- Create: `pkg/perf_metrics/runtime_test.go`
- Modify: `pkg/perf_metrics/flush.go`
- Modify: `main.go`

- [x] **RED 1：写无溢出 Capped Exponential Backoff 测试**

定义并测试：failure 1/2/3 的 ceiling 为 base/2×base/4×base；超过 cap 后恒为 cap；极大 failure 不溢出；注入 `func(max time.Duration) time.Duration { return max }` 可精确验证 ceiling。

- [x] **RED 2：写首次执行、错误退避、恢复和 Close/Wait 测试**

Runtime 启动后无需先等待 interval 即执行一次。前两次 maintenance 返回错误、第三次成功；注入 identity jitter 后 Wait 观察到 `1s, 2s, configured interval`。`Close()` 只发取消且幂等，`Wait(ctx)` 可超时并在 worker 退出后返回最终 Flush 错误。

- [x] **RED 3：写 Retention 0、正数清理、节流和溢出测试**

- `RetentionDays=0`：Delete callback 调用次数为 0。
- `RetentionDays=1`：首次 maintenance 删除 `bucket_ts < now-86400`。
- 6 小时内再次 maintenance：不重复 Delete。
- `RetentionDays=math.MaxInt`：cutoff 计算不溢出、不产生未来 cutoff，安全跳过删除。

- [x] **RED 4：写最终 Flush 包含活动桶测试**

写入当前 bucket，调用 `Close()` 后 `Wait(ctx)`；即使 bucket 尚未 completed，也必须通过 final mode Upsert 一次，并从 Store 移除或在失败时完整 Requeue。

- [x] **验证 RED**

Run: `go test ./common ./model ./pkg/perf_metrics -run 'Test(CappedExponential|PerfRuntime|PerfRetention|PerfFinalFlush)' -count=1`

Expected: FAIL，当前只有永久 `time.Sleep` loop，无 Context/Close/Wait/Backoff/最终 Flush。

- [x] **GREEN 1：实现共享 Backoff API**

```go
type JitterFunc func(time.Duration) time.Duration

func FullJitter(max time.Duration) time.Duration
func CappedExponentialBackoff(failures int, base, cap time.Duration, jitter JitterFunc) time.Duration
```

循环倍增前使用 `current > cap/2` 检查，避免 duration overflow；nil jitter 使用 `math/rand/v2` 的 Full Jitter；注入值被 clamp 到 `[0, ceiling]`。

- [x] **GREEN 2：增加 Context-aware Perf Model 方法**

```go
func UpsertPerfMetricContext(ctx context.Context, metric *PerfMetric) error
func DeletePerfMetricsBeforeContext(ctx context.Context, cutoffTs int64) error
```

旧无 Context wrapper 若仍有调用，只委托 `context.Background()`；生产 Runtime 一律使用 `DB.WithContext(ctx)`。

- [x] **GREEN 3：实现 Runtime 生命周期**

```go
type Runtime struct {
	cancel       context.CancelFunc
	done         chan struct{}
	closeOnce    sync.Once
	finalizeOnce sync.Once
	finalErr     error
}

func Start(parent context.Context) *Runtime
func (r *Runtime) Close()
func (r *Runtime) Wait(ctx context.Context) error
func (r *Runtime) Stats() RuntimeStats
```

单 worker 首次立即 maintenance；失败使用 base=1s、cap=1m 的共享 Backoff，成功后回到归一化 FlushInterval。Stats 至少包含 Runs、Errors、ConsecutiveErrors，只存计数不存原始错误。

- [x] **GREEN 4：实现清理节流与安全 cutoff**

Retention due 条件为 `lastCleanup == 0 || now-lastCleanup >= 6h`。只有 Delete 成功后更新 lastCleanup。`days > nowUnix/86400` 时不执行 Delete；0 永久保留；负数已由设置层归一化为 0。

- [x] **GREEN 5：最终 Flush 与 main 接线**

周期 Flush 只取 completed buckets；`Wait(ctx)` 在 worker 退出后用同一 caller Context 执行一次 `flushAll`（含活动桶）和到期 Cleanup。`main.go` 从 `initApp` 删除旧 `perfmetrics.Init()`，在资源初始化后持有 `perfRuntime := perfmetrics.Start(context.Background())`。

收到退出信号后先 `Close` 两个 Runtime，执行 HTTP `Shutdown` 排空请求，再用独立 15 秒 finalize Context 调用两个 `Wait`，最后才允许 `model.CloseDB()`。

- [x] **运行 GREEN、Race 与 Context 取消测试**

Run: `go test ./common ./model ./pkg/perf_metrics -count=1`

Run: `go test -race ./common ./pkg/perf_metrics ./setting/perf_metrics_setting -count=1`

Expected: PASS；无 goroutine 泄漏、无 race，注入取消后 GORM 返回 `context.Canceled`。

- [x] **建议提交**

```bash
git add common/backoff.go common/backoff_test.go model/perf_metric.go model/perf_metric_test.go pkg/perf_metrics/runtime.go pkg/perf_metrics/runtime_test.go pkg/perf_metrics/flush.go main.go
git commit -m "fix: lifecycle perf metric runtime"
```

### Task 5: Smart Routing 设置 Persist-then-Publish

**Files:**
- Modify: `model/option.go`
- Modify: `model/option_test.go`
- Modify: `setting/smart_routing_setting/config.go`
- Modify: `setting/smart_routing_setting/config_test.go`
- Modify: `controller/smart_routing.go`
- Modify: `controller/smart_routing_test.go`

- [x] **RED 1：写 DB 失败不改变内存/Breaker 测试**

用 GORM callback 对 `options` Create/Update 注入错误。保存前记录 `smart_routing_setting.GetSetting()` 和 `routingbreaker.DefaultConfig()`；调用 `UpdateSmartRoutingSettings` 后断言 HTTP 失败、DB 未部分写入、设置快照与 Breaker 完全不变。

- [x] **RED 2：写环境覆盖不回写 DB 测试**

设置 `SMART_ROUTING_ENABLED=false`，请求保存 `enabled=true`；DB 中 `smart_routing_setting.enabled` 必须是 `true`，本节点 API 返回的 effective setting 为 `false`。环境变量只在 `GetSetting()` 读取时覆盖，不参与 `ConfigToMap`。

- [x] **RED 3：写 Bulk Namespace 原子发布测试**

交替 Bulk 写两组具有不同 Breaker/interval 字段的完整 Map，并发 Snapshot 只能观察旧组或新组，不能观察 map 迭代造成的混合字段。该测试同时在 `-race` 下运行。

- [x] **验证 RED**

Run: `go test ./model ./setting/smart_routing_setting ./controller -run 'Test(UpdateOptionsBulkPublishes|UpdateSmartRoutingSettings|NormalizeSmartRouting)' -count=1`

Expected: FAIL；当前 Controller 在 DB 前调用 `UpdateSetting`，且 Bulk post-commit 逐 key 发布。

- [x] **GREEN 1：导出纯归一化函数**

```go
func NormalizeSetting(setting SmartRoutingSetting) SmartRoutingSetting {
	normalize(&setting)
	return setting
}
```

它不得读取环境变量或修改 ConfigManager；`UpdateSetting` 继续供内部测试/直接发布使用，但实现为 Normalize + Replace。

- [x] **GREEN 2：Bulk options 按命名空间一次发布**

`UpdateOptionsBulk` 保持单个 DB transaction。Commit 成功后先在 `OptionMapRWMutex` 下批量更新 OptionMap，再把 `smart_routing_setting.*` 等分层键按 config namespace 分组，每个 namespace 只调用一次 `GlobalConfig.UpdateFromMap(name, fullMap)`；传统非分层键继续走原后处理。DB transaction 失败时不得触碰 OptionMap 或 ConfigManager。

- [x] **GREEN 3：Controller 严格按顺序执行**

```go
normalized := smart_routing_setting.NormalizeSetting(request) // 纯函数，无 env
values, err := config.ConfigToMap(normalized)
// prefix -> smart_routing_setting.*
if err = model.UpdateOptionsBulk(persisted); err != nil { /* 返回，内存未变 */ }
effective := smart_routing_setting.GetSetting() // post-commit 单次 namespace 发布后读取 env-effective 快照
syncRoutingBreakerConfigFromSetting(effective)
common.ApiSuccess(c, effective)
```

Breaker 只能在 DB 成功和快照发布完成后重配一次。

- [x] **运行 GREEN、Race 与 SQLite 事务测试**

Run: `go test ./model ./setting/smart_routing_setting ./controller -run 'Test(UpdateOptionsBulkPublishes|UpdateSmartRoutingSettings|SettingConcurrent)' -count=1`

Run: `go test -race ./model ./setting/smart_routing_setting ./controller -run 'Test(UpdateOptionsBulkPublishes|UpdateSmartRoutingSettings|SettingConcurrent)' -count=1`

Expected: PASS。远端节点仍由 `loadOptionsFromDatabase -> GlobalConfig.LoadFromDB` 在同一 ConfigManager 锁内加载完整 namespace；不新增 Redis 发布。

- [x] **建议提交**

```bash
git add model/option.go model/option_test.go setting/smart_routing_setting/config.go setting/smart_routing_setting/config_test.go controller/smart_routing.go controller/smart_routing_test.go
git commit -m "fix: persist routing settings before publish"
```

### Task 6: Smart Runtime Context、错误退避与最终 Flush

**Files:**
- Modify: `model/channel.go`
- Modify: `model/routing_model.go`
- Modify: `model/routing_model_test.go`
- Modify: `controller/system_task_handlers.go`
- Modify: `controller/smart_routing_runtime_test.go`
- Modify: `main.go`

- [x] **RED 1：把 Runtime callback 改为 Context/error 契约测试**

测试 deps 的 `refresh`/`flush` 签名为 `func(context.Context, SmartRoutingSetting) error`。阻塞 callback 只等待 `ctx.Done()`；`Close()` 后必须退出，`Wait(ctx)` 返回，不使用 Sleep。

- [x] **RED 2：写 Refresh/Flush 独立错误统计与恢复测试**

分别注入两次错误后成功；identity jitter 下等待序列为 `1s,2s,normal interval`。Stats 精确断言 RefreshErrors/FlushErrors 和 ConsecutiveErrors；成功后 consecutive 归零并记录一次 Recovery。

- [x] **RED 3：写 GORM Context 取消测试**

对 channels/routing metrics 查询注册 GORM callback，阻塞到 `tx.Statement.Context.Done()` 后返回 `ctx.Err()`；取消 Runtime 时 refresh/flush 必须退出，不能等待数据库默认超时。

- [x] **RED 4：写 Smart 最终 Flush 测试**

向 `routingmetrics` 和 Breaker 写入 dirty state，`Close()` + `Wait(ctx)` 后数据库存在对应增量；final flush 只执行一次。即使设置刚被禁用，也要清空已存在 dirty state。

- [x] **验证 RED**

Run: `go test ./model ./controller -run 'TestSmartRoutingRuntime.*(Context|Backoff|Recovery|FinalFlush)|TestRoutingModelContext' -count=1`

Expected: FAIL；当前 callback 无 Context/error，错误被吞，固定周期无退避，Close 内阻塞且无最终 Flush。

- [x] **GREEN 1：增加 Context-aware Model 入口**

新增并由旧 wrapper 委托：

```go
ResolveLegacyRoutingStateEligibilityContext(ctx context.Context, channelID, apiKeyIndex int)
UpsertRoutingChannelMetricContext(ctx context.Context, metric *RoutingChannelMetric) error
UpsertRoutingBreakerStateContext(ctx context.Context, state *RoutingBreakerState) error
DeleteRoutingMetricsBeforeContext(ctx context.Context, cutoff int64) (int64, error)
GetRoutingBreakerStatesForHydrationPageContext(ctx context.Context, ...)
```

所有生产路径使用 `DB.WithContext(ctx)`；内存 Channel Cache 分支保持锁内只读，DB fallback 使用同一 Context。

- [x] **GREEN 2：修改 Flush/Refresh 函数签名**

```go
func flushRoutingRuntimeState(ctx context.Context, setting SmartRoutingSetting) (map[string]any, error)
func refreshRoutingHotcacheFromDB(ctx context.Context, setting SmartRoutingSetting) (map[string]any, error)
```

函数内每个 GORM query、分页、Upsert、Retention Delete 都携带 Context。取消发生在 Drain 后时，未持久化的 metrics/breakers 必须按现有语义 Requeue。

- [x] **GREEN 3：实现两个独立 worker 的 Backoff/Stats**

`smartRoutingRuntimeDeps` 增加 Context/error callback 和 `jitter common.JitterFunc`。Refresh 与 Flush 各自维护 failure streak；base=1s、cap=1m；成功恢复正常配置 interval。`ctx.Err()!=nil` 的退出不计作运行错误。

- [x] **GREEN 4：拆分 Close 与 Wait，最终 Flush 一次**

`Close()` 只 cancel 且幂等；`Wait(ctx)` 等两个 worker 完成后，用调用者 Context 和最新 setting 执行一次 final flush，返回 final error。Stats 不保存原始错误字符串。

- [x] **GREEN 5：main 统一 Shutdown 顺序**

与 Task 4 的 Perf Runtime 一致：两个 Runtime 先 Close，HTTP Shutdown 排空，随后在独立 finalize Context 中 Wait；任何最终 Flush 失败写脱敏系统错误但不得在 DB 关闭后重试。

- [x] **运行 GREEN 与 Race**

Run: `go test ./model ./controller -run 'TestSmartRoutingRuntime|TestFlushRoutingRuntimeState|TestRefreshRoutingHotcache' -count=1`

Run: `go test -race ./pkg/routing_metrics ./pkg/routing_breaker ./pkg/routing_hotcache ./controller -run 'TestSmartRoutingRuntime|TestFlushRoutingRuntimeState|TestRefreshRoutingHotcache' -count=1`

Expected: PASS，无 race；失败后延迟有上限且成功恢复正常 interval。

- [x] **建议提交**

```bash
git add model/channel.go model/routing_model.go model/routing_model_test.go controller/system_task_handlers.go controller/smart_routing_runtime_test.go main.go
git commit -m "fix: harden smart routing runtime lifecycle"
```

### Task 7: 专用 Fail-closed HTTPS Cost Egress Client

**Files:**
- Create: `service/routing_cost_http.go`
- Create: `service/routing_cost_http_test.go`
- Modify: `controller/smart_routing.go`
- Modify: `controller/smart_routing_test.go`
- Modify: `controller/system_task_handlers.go`
- Modify: `controller/smart_routing_sub2api.go`
- Modify: `controller/smart_routing_task_test.go`

- [x] **RED 1：写 URL/IP/DNS 防护表驱动测试**

必须拒绝：HTTP、userinfo、127/8、`::1`、RFC1918、link-local、multicast、`169.254.169.254`、`100.100.100.200`；DNS 返回“一个公网 + 一个私网”时整体拒绝且 Dialer 零调用。所有 DNS 结果均安全时，Dialer 收到的是验证后的 IP:port，不是 hostname。

- [x] **RED 2：写 TLS/Proxy/共享 Transport 测试**

使用测试 CA 签发 `cost.example` 证书并注入 RootCAs：TLS 1.2/1.3 成功，TLS 1.1 失败；设置 `HTTPS_PROXY` 后仍直连注入的安全 Dialer；连续获取 Client 返回同一共享 Transport。不得读取 `common.TLSInsecureSkipVerify`。

- [x] **RED 3：写 Redirect 逐跳测试**

- 同 scheme+同 Host（含 port）的 GET redirect 可继续，但删除 `Authorization`、`Proxy-Authorization`、`Cookie`、`New-Api-User`。
- 跨 Host、HTTPS→HTTP、超过 3 跳、带 body/POST 的 redirect 全部拒绝。
- 每个新 URL 仍由 RoundTripper 做相同 URL/DNS 校验。

- [x] **验证 RED**

Run: `go test ./service ./controller -run 'TestRoutingCost(HTTP|URL|Dialer|TLS|Redirect)|TestValidateRoutingBindingRequestRejectsUnsafeBaseURL' -count=1`

Expected: FAIL；当前每次创建默认 `http.Client`，允许 HTTP/环境代理/默认 redirect，无 DNS pin。

- [x] **GREEN 1：实现专用 Client 工厂与共享实例**

`service/routing_cost_http.go` 使用独立 `http.Transport`：`Proxy=nil`、`DisableCompression=true`、`TLSClientConfig.MinVersion=tls.VersionTLS12`、固定 Connect/TLS/ResponseHeader/Overall timeout、合理 IdleConn 上限。工厂 options 允许测试注入 Resolver、DialContext 和 RootCAs；生产只暴露 `GetRoutingCostHTTPClient()` 共享实例。

- [x] **GREEN 2：实现全结果验证与 Pin Dial**

Dial 前解析 hostname；任一结果属于私网/loopback/link-local/multicast/metadata/special-use 即整次失败。全部通过后按原顺序尝试 `net.JoinHostPort(ip.String(), port)`；TLS SNI/证书验证仍使用原 hostname，禁止 `InsecureSkipVerify`。

- [x] **GREEN 3：Controller Base URL 只接受 HTTPS**

`validateRoutingBaseURL` 把允许 scheme 收紧为 `https`；保留 userinfo 和敏感 query 拒绝。历史 HTTP binding 不迁移、不自动放行，下一次同步以安全错误进入 Backoff。

- [x] **GREEN 4：所有 New API/Sub2API 请求使用共享 Doer**

把 `/api/pricing`、`/api/user/self`、Sub2API login/groups/rates/channels/usage 全部切换到同一 `routingCostHTTPDoer`。测试文件提供仅 `_test.go` 可见的 restore helper，将 `httptest.NewTLSServer().Client()` 注入功能测试；安全测试必须走真实专用 Client，不能使用 bypass。

- [x] **运行 GREEN 与 Race**

Run: `go test ./service ./controller -run 'TestRoutingCost|TestRunRoutingCostSync|TestRoutingSub2API|TestLoadSmartRoutingBindingGroups' -count=1`

Run: `go test -race ./service ./controller -run 'TestRoutingCost(HTTP|Dialer|Redirect)|TestRunRoutingCostSync' -count=1`

Expected: PASS；功能 fixture 全部为 TLS，生产 Client 不受环境代理影响。

- [x] **建议提交**

```bash
git add service/routing_cost_http.go service/routing_cost_http_test.go controller/smart_routing.go controller/smart_routing_test.go controller/system_task_handlers.go controller/smart_routing_sub2api.go controller/smart_routing_task_test.go
git commit -m "fix: secure routing cost sync egress"
```

### Task 8: Cost JSON Content-Type、Wire/Decoded 双限与 gzip

**Files:**
- Create: `controller/smart_routing_http.go`
- Create: `controller/smart_routing_http_test.go`
- Modify: `controller/system_task_handlers.go`
- Modify: `controller/smart_routing_sub2api.go`
- Modify: `controller/smart_routing_task_test.go`

- [x] **RED 1：写 Content-Type 契约测试**

接受 `application/json`、`application/json; charset=utf-8`、`application/problem+json`；拒绝缺失、解析失败、`text/json`、`text/html`。使用 `mime.ParseMediaType`，不要字符串前缀猜测。

- [x] **RED 2：写 Content-Length 与 chunked limit+1 测试**

使用测试小 Limits（例如 wire=8、decoded=16）：声明 `ContentLength=9` 时在读 Body 前失败；`ContentLength=-1` 的 chunked/未知长度通过 `LimitReader(limit+1)` 读到第 9 字节后失败；恰好 limit 成功。

- [x] **RED 3：写 gzip 压缩前后双限测试**

Transport 已 `DisableCompression=true`。先完整读取受 wire limit 约束的压缩字节，再 `gzip.NewReader`，解压输出再受 decoded limit+1 约束。分别测试 wire 超限、解压 bomb 超限、合法 gzip；拒绝 br/deflate/多重 encoding。

- [x] **验证 RED**

Run: `go test ./controller -run 'TestReadRoutingCostJSON' -count=1`

Expected: FAIL；当前只对解码 reader 做单层 LimitReader，未验证 Content-Type，也可能被 Transport 自动解压绕过 wire limit。

- [x] **GREEN 1：实现单一响应读取边界**

```go
type routingJSONLimits struct { WireBytes, DecodedBytes int64 }
func readRoutingCostJSON(resp *http.Response, limits routingJSONLimits) ([]byte, error)
```

生产 Limits 都使用 `maxRatioConfigBytes`；helper 校验正数并安全计算 `limit+1`。`application/json` 或 `application/*+json` 才允许继续。

- [x] **GREEN 2：所有 endpoint 先安全读取再 common.Unmarshal**

替换 `common.DecodeJson(io.LimitReader(...))`：

```go
body, err := readRoutingCostJSON(response, defaultRoutingJSONLimits)
if err != nil { return ..., err }
if err = common.Unmarshal(body, &payload); err != nil { return ..., err }
```

不得把响应正文拼进错误、日志、LastSyncError 或 SystemTask。

- [x] **运行 GREEN 与现有 Cost Sync 测试**

Run: `go test ./controller -run 'Test(ReadRoutingCostJSON|RunRoutingCostSync|RoutingSub2API)' -count=1`

Expected: PASS；application/problem+json 可解析，所有超限路径在分配无界内存前终止。

- [x] **建议提交**

```bash
git add controller/smart_routing_http.go controller/smart_routing_http_test.go controller/system_task_handlers.go controller/smart_routing_sub2api.go controller/smart_routing_task_test.go
git commit -m "fix: bound routing cost sync responses"
```

### Task 9: 统一错误脱敏、持久化失败 Backoff 与任务失败传播

**Files:**
- Create: `common/error_sanitize.go`
- Create: `common/error_sanitize_test.go`
- Modify: `model/routing_model.go`
- Modify: `model/routing_model_test.go`
- Modify: `controller/smart_routing.go`
- Modify: `controller/smart_routing_sub2api.go`
- Modify: `controller/system_task_handlers.go`
- Modify: `controller/smart_routing_test.go`
- Modify: `controller/smart_routing_task_test.go`
- Modify: `controller/system_task.go` only if response defense-in-depth is needed after the persistence tests

- [x] **RED 1：写统一脱敏与长度上限测试**

输入包含 Bearer、Authorization、API key、password、Cookie、带 query URL、已知 credentials、换行和超过 512 rune 的尾部秘密；输出必须不含秘密、不含换行、有效 UTF-8、最多 512 rune。已知 secret 做精确替换，通用 label/URL/IP 再由规则和 `common.MaskSensitiveInfo` 处理。

- [x] **RED 2：写 LastSyncError/API/SystemTask 三出口测试**

上游 TLS fixture 返回含 `pw-secret`/`Bearer sk-secret` 的失败消息：

- DB `LastSyncError` 安全且有长度上限；
- `TestSmartRoutingBinding`/`Load...Groups` API body 不含秘密；
- `routing_cost_sync` SystemTask 的 `error`/`result` 不含秘密。

- [x] **RED 3：写持久化 failure count 驱动 Backoff 测试**

给 binding 预置 `SyncFailureCount=2`，注入固定 now 和 identity jitter；下一次失败后 count=3、Backoff ceiling=4 分钟。极大 count 被饱和且不超过 1 小时 cap；成功同步把 count/error/backoff 全部清零。

- [x] **RED 4：写 binding 状态更新失败令任务失败测试**

GORM callback 只让 `routing_channel_bindings` Updates 返回 forced error；创建并 Claim 一个真实 `routing_cost_sync` SystemTask 后调用 Handler。最终 task 必须是 `failed`，不能把 binding 状态写失败吞掉后标记 succeeded。

- [x] **验证 RED**

Run: `go test ./common ./model ./controller -run 'Test(SanitizeError|RoutingCostSync.*Backoff|RoutingCostSync.*StateUpdate|RoutingCost.*DoesNotExpose)' -count=1`

Expected: FAIL；当前固定 60 秒、无 failure count，binding status Update 错误被忽略。

- [x] **GREEN 1：实现统一 Sanitizer**

```go
const SafeErrorMaxRunes = 512
func SanitizeErrorMessage(message string, secrets ...string) string
```

顺序固定为：`strings.ToValidUTF8` → 已知 secret 替换 → Authorization/token/key/password/cookie label 脱敏 → `MaskSensitiveInfo` → 控制字符折叠 → rune 截断。Controller 使用带 `Unwrap()` 的 safe error wrapper，确保 `errors.Is(context.Canceled)` 和 `routingUpstreamAuthError` 分类不丢失。

- [x] **GREEN 2：增加无业务默认标签的失败次数列**

在 `RoutingChannelBinding` 增加：

```go
SyncFailureCount int `json:"sync_failure_count"`
```

不要加 `gorm:"default:0"`/`not null`，避免旧表跨 PostgreSQL/MySQL AutoMigrate 抖动或加列失败；旧 NULL 扫描为 Go 零值。Binding view 可返回该计数以便管理员诊断。

- [x] **GREEN 3：持久化 Backoff 状态机**

常量 base=1m、cap=1h。失败次数从本次 DB query 得到的 binding 读取、饱和加一，再调用共享 `CappedExponentialBackoff`；Unix 加法做上溢饱和。失败状态和成功清零都使用 `DB.WithContext(ctx).Model(...).Updates(...)`，任一错误立即返回 task-level error。

- [x] **GREEN 4：所有出口只使用 safe message**

`LastSyncError` 存 safe string；API 返回 safe wrapper；`finishSystemTaskHandler` 在写 SystemTask 前再次调用通用 Sanitizer。Summary 只保留 counts/IDs，不加入正文、完整 URL、headers 或 credentials。

- [x] **GREEN 5：扩展三数据库迁移契约**

在 `runRoutingMigrationAndUpsertContract` 先创建不含 `SyncFailureCount` 的 legacy binding 表/行，再 AutoMigrate 两次；断言新列存在、旧行保留、初始读取为 0、更新/清零在 SQLite/MySQL/PostgreSQL 一致。只用 GORM，不写方言 SQL。

- [x] **运行 GREEN、三库与 Race**

Run: `go test ./common ./model ./controller -run 'Test(SanitizeError|RoutingModels|RoutingCostSync|RoutingCost.*DoesNotExpose)' -count=1`

Run: `go test ./model -run 'TestRoutingModels(AutoMigrateAndMetricUpsert|ExternalDatabaseCompatibility)' -count=1 -v`

Expected: SQLite 必须 PASS；设置 `ROUTING_TEST_MYSQL_DSN`、`ROUTING_TEST_POSTGRES_DSN` 时两个隔离空库也必须 PASS，未设置时只能记录 SKIP，不能把 Gate 1 三库证据写成 PASS。

Run: `go test -race ./common ./controller -run 'Test(RoutingCostSync|RoutingCost.*DoesNotExpose)' -count=1`

Expected: PASS，无秘密出现在失败输出。

- [x] **建议提交**

```bash
git add common/error_sanitize.go common/error_sanitize_test.go model/routing_model.go model/routing_model_test.go controller/smart_routing.go controller/smart_routing_sub2api.go controller/system_task_handlers.go controller/smart_routing_test.go controller/smart_routing_task_test.go controller/system_task.go
git commit -m "fix: persist safe routing sync backoff"
```

### Task 10: Gate 1 总验证与治理审计

**Files:**
- Modify only if verification exposes a scoped defect: files changed in Tasks 1–9
- Update verification record/check boxes: `docs/superpowers/plans/2026-07-11-channel-routing-phase0d-gate1-hardening.md`
- Do not modify: `.agent/`, `web/default/`, `web/classic/`

- [x] **验证 1：格式、最窄测试和静态边界**

```bash
gofmt -w common/backoff.go common/backoff_test.go common/error_sanitize.go common/error_sanitize_test.go \
  setting/perf_metrics_setting/config.go setting/perf_metrics_setting/config_test.go \
  pkg/perf_metrics/*.go model/perf_metric.go model/perf_metric_test.go \
  model/option.go model/option_test.go model/channel.go model/routing_model.go model/routing_model_test.go \
  service/routing_cost_http.go service/routing_cost_http_test.go \
  controller/smart_routing*.go controller/system_task_handlers.go main.go
git diff --check
go test ./setting/perf_metrics_setting ./pkg/perf_metrics ./common -count=1
go test ./model ./service ./controller -run 'Test(SmartRouting|RoutingCost|RoutingModels|UpdateOptionsBulk|FlushRoutingRuntime|RefreshRoutingHotcache)' -count=1
```

Expected: 全部 exit 0。

- [x] **验证 2：Race 与生命周期**

```bash
go test -race ./setting/perf_metrics_setting ./pkg/perf_metrics ./common -count=1
go test -race ./pkg/routing_metrics ./pkg/routing_breaker ./pkg/routing_hotcache -count=1
go test -race ./model ./service ./controller -run 'Test(SmartRoutingRuntime|RoutingCost|UpdateOptionsBulk|FlushRoutingRuntime|RefreshRoutingHotcache)' -count=1
```

Expected: 无新增 race。若尝试 `go test -race ./...` 命中已记录的非本阶段 logger/gin/task-polling 基线竞态，必须给出最小复现并明确区分，不得宣称全仓 race PASS。

- [x] **验证 3：SQLite/MySQL/PostgreSQL**

```bash
go test ./model -run 'TestRoutingModels(AutoMigrateAndMetricUpsert|ExternalDatabaseCompatibility)' -count=1 -v
```

Expected: SQLite 必跑；MySQL 5.7.8+ 与 PostgreSQL 9.6+ 使用隔离空库 DSN。两者任一未实际运行时，Gate 1 的“三数据库验证”仍是未满足项。

- [x] **验证 4：Redis/多节点语义**

```bash
rg -n 'HIncrBy|Expire|recordRedis|common\.RDB' pkg/perf_metrics
go test ./controller -run 'TestRoutingSub2API.*(Singleflight|Redis|Cache|Lock)' -count=1
go test ./model ./setting/smart_routing_setting -run 'Test(UpdateOptionsBulkPublishes|SettingConcurrent)' -count=1
```

Expected: 第一个 `rg` 无输出；Redis 只保留 Sub2API JWT/lock 等有 TTL 协调，不承担 Perf 请求计数；多节点设置以 DB transaction + 轮询整组加载收敛。不得声称已有 Phase 2 Redis Stream/Revision 语义。

- [x] **验证 5：安全与失败注入矩阵**

逐项确认测试实际覆盖并通过：Option DB rollback、Perf Upsert failure full requeue、Runtime Context cancel、Smart DB query cancel、DNS mixed/private/metadata、DNS pin、TLS 1.1 拒绝、测试 CA、proxy 禁用、redirect 跨 host/降级/凭证剥离、Content-Type、Content-Length、chunked limit+1、gzip wire/decoded limit、binding status update failure、LastSyncError/API/SystemTask 脱敏。

- [x] **验证 6：全仓构建**

```bash
go test ./... -count=1
go vet ./...
go build ./...
```

Expected: 全部 exit 0。执行前只使用仓库已有前端 dist；本计划不运行或修改正式前端源码。

- [x] **验证 7：治理与范围审计**

```bash
rg -n 'time\.Sleep\(' pkg/perf_metrics controller/system_task_handlers.go
rg -n 'http\.Client\{' controller/system_task_handlers.go controller/smart_routing_sub2api.go
rg -n 'ProxyFromEnvironment|InsecureSkipVerify' service/routing_cost_http.go
rg -n 'json\.(Marshal|Unmarshal)' common/backoff.go common/error_sanitize.go pkg/perf_metrics model/perf_metric.go service/routing_cost_http.go controller/smart_routing_http.go
git diff --name-only | rg '^(web/default|web/classic|\.agent)/'
```

Expected: Runtime 文件无永久 Sleep loop；Cost Controller 不再逐请求创建 Client；专用 Client 不使用环境 proxy/跳过证书；新增业务代码无直接 JSON 编解码；最后一个命令无输出。不得修改或删除受保护的项目/组织标识。

- [x] **验证 8：Gate 1 语义清单**

- 设置读写 race-safe，DB 失败不会产生仅本节点的新 Smart Setting/Breaker。
- `routing_metrics=attempt`、`perf_metrics=logical request`，两套 RequestCount 未合并。
- Perf/Smart worker 可 Context cancel、Close、Wait、首次执行、退避、恢复和最终 Flush。
- 所有新增 Map/桶有硬容量/TTL/Drop/Eviction stats。
- Retention 0 永久保留，正数清理，cutoff 无溢出。
- Cost Connector 与 Serving health 继续分离；成本访问凭据失败不写 Serving auth failure。
- Cost Egress 默认 HTTPS fail-closed，响应和错误均受边界约束。
- 429/529、错误责任、Multi-Key、真实 Output Token/TTFT/Token/s 的 Phase 0B/0C 回归测试继续 PASS。
- 未引入前端、Pool/Member、Revision/Outbox 或 Phase 1+ 实现。

- [x] **建议提交**

```bash
git add docs/superpowers/plans/2026-07-11-channel-routing-phase0d-gate1-hardening.md
git commit -m "test: verify channel routing gate 1"
```

## 2026-07-12 最终验证记录

- 格式与静态边界：当前 Gate 1 变更文件已执行 `gofmt`，`git diff --check` 通过；Perf Redis 热路径、永久 Sleep loop、逐请求 Cost Client、环境 Proxy/TLS 跳过、直接 JSON 编解码及前端/`.agent` 越界扫描均无命中。计划原始 `rg 'Expire'` 会误命中测试名，最终使用 `\.Expire\(` 精确扫描。
- 全仓：`go test ./... -count=1`、`go vet ./...`、`go build ./...` 全部 exit 0。
- Race：Perf/Smart setting、Perf metrics、common、routing metrics/breaker/hotcache 全包 race 通过；model/service/controller 的 Smart Runtime、Cost、Sub2API JWT、Option bulk、Flush/Refresh、三库路由契约等定向 race 通过。
- 已知基线：额外尝试的全量 `go test -race ./service` 命中计划已记录的 `logger.logHelper` 与 task-polling 测试共享对象竞态；本次未修改这些路径，也未把全仓 race 误报为通过。
- 三数据库：SQLite、MySQL 5.7、PostgreSQL 15 的 `TestRoutingModels(AutoMigrateAndMetricUpsert|ExternalDatabaseCompatibility)` 均实际运行并通过；同时覆盖 legacy `sync_failure_count=NULL` 首次失败持久化、旧 breaker 条件 upsert、binding 代际 fencing 与 balance 单调更新。
- Redis/多节点：Sub2API JWT identity、singleflight、锁、TTL/容量、retired tombstone、随机 marker activation CAS、条件 eviction、Redis 故障 fail-closed、显式 token 不重试及 managed JWT 单次重登回归均通过；Perf 不再使用 Redis 请求计数。设置仍按 DB transaction + 轮询整组收敛，未声称 Phase 2 Revision/Stream 语义。
- 独立复核后已关闭的 P1/P2：首次 flush 前 delete/recreate 旧代污染、PostgreSQL 事务内 breaker 冲突、balance 时间倒退、Option CI collation 反向锁序、legacy NULL fencing、managed JWT 泄露、历史 authKey 重激活、跨节点 tombstone/activation 竞态及并发 401 登录风暴。
- 范围：未修改 `web/default`、`web/classic`、`.agent/`，未引入 Pool/Member、Outbox、Redis Stream、v2 API 或其他 Phase 1+ 实现；未 push、未创建 PR、未部署或连接生产服务器。

## 实施完成判定

只有 Tasks 1–10 全部有新鲜测试输出、MySQL/PostgreSQL 未被误报、无未解决 P0/P1、且 `git diff --check`/全仓 test/vet/build 通过，才可把 Gate 1 标记完成。Gate 1 完成只授权进入 Phase 1 Observe 的计划与实施，不代表生产部署、真实流量灰度或完整渠道路由 2.0 完成。
