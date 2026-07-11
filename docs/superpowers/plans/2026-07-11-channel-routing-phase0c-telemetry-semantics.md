# 渠道路由 Phase 0C 遥测语义实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让渠道级路由遥测使用当前上游尝试的真实 Output Token、generation duration 和 TTFT，并把兼容字段 `TPS` 从 request/sec 修正为真实 output Token/s。

**Architecture:** 结算层已经获得各协议归一化后的 usage，因此在同步返回 Controller 前把当前 attempt 的最大输出 Token 数写入 `RelayInfo` 的原子字段；retry reset 同时清零该字段并建立独立 attempt 起始时间，避免继续使用逻辑请求 `StartTime` 污染后续渠道。`routing_metrics` 只在 Token 数和 generation duration 均为正时累计吞吐分母，`routing_hotcache` 以 `OutputTokens / GenerationMs` 构造渠道级 Token/s。Selector 保留现有兼容结构，但流式请求优先使用 P95 TTFT，非流式请求继续使用 P95 total latency。

**Tech Stack:** Go 1.22、Gin、GORM v2、`sync/atomic`、Testify、现有 `routing_metrics` / `routing_hotcache` / `service/routing`。

---

## 文件职责

- `relay/common/relay_info.go`：保存并重置当前 attempt 的起始时间与输出 Token 观察值。
- `relay/common/relay_info_test.go`：保护最大值、负值、attempt 时钟与 retry reset 契约。
- `service/text_quota.go`、`service/quota.go`：在已有归一化 usage/结算边界同步发布当前 attempt 输出 Token。
- `service/quota_wss_test.go`：保护缺失 usage 为 0、真实 usage 可被路由遥测读取。
- `pkg/routing_metrics/metrics.go`、`metrics_test.go`：把 attempt Output Token 与对应 generation duration 写入持久化 bucket。
- `pkg/routing_hotcache/cache.go`、`cache_test.go`：合并 Output Token/GenerationMs，并以真实 Token/s 构造兼容 `TPS`。
- `service/routing/types.go`、`selector.go`、`selector_test.go`：把 P95 TTFT 传给纯选择器，并按请求类型选择延迟指标。
- `service/channel_select.go`、`channel_select_test.go`：从 Hot Cache 复制 P95 TTFT，并把流式偏好传给 Selector。
- `middleware/distributor.go`、`distributor_smart_routing_test.go`：在旧选择点用可复用 JSON body 提前记录 `stream` 布尔值；完整协议解析后的选路移动仍属于 Phase 1/4。

### Task 1: 当前 Attempt 的真实输出 Token

**Files:**
- Modify: `relay/common/relay_info.go`
- Modify: `relay/common/relay_info_test.go`
- Modify: `service/text_quota.go`
- Modify: `service/quota.go`
- Modify: `service/quota_wss_test.go`

- [x] **Step 1: 写 RelayInfo attempt 作用域失败测试**

在 `relay/common/relay_info_test.go` 增加：

```go
func TestRelayInfoRoutingOutputTokensAreAttemptScoped(t *testing.T) {
	logicalStart := time.Now().Add(-time.Hour)
	info := &RelayInfo{StartTime: logicalStart}

	info.ObserveRoutingOutputTokens(12)
	info.ObserveRoutingOutputTokens(8)
	info.ObserveRoutingOutputTokens(-1)
	assert.Equal(t, int64(12), info.RoutingOutputTokens())

	info.ResetStreamAttemptState()
	assert.Zero(t, info.RoutingOutputTokens())
	assert.True(t, info.RoutingAttemptStartTime().After(logicalStart))

	info.ObserveRoutingOutputTokens(7)
	assert.Equal(t, int64(7), info.RoutingOutputTokens())
	info.ResetStreamAttemptState()
	assert.Zero(t, info.RoutingOutputTokens())
	assert.True(t, info.RoutingAttemptStartTime().After(logicalStart))
}
```

- [x] **Step 2: 写结算边界发布 usage 的失败测试**

扩展 `TestPostTextConsumeQuotaMissingUsageRetainsReservedQuotaAndRecordsConsumption`：

```go
assert.Zero(t, info.RoutingOutputTokens())
```

新增一个使用现有 `truncate/seedUser/seedToken/seedChannel` fixture 的正向文本 usage 测试。核心调用与断言：

```go
usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 25, TotalTokens: 35}
PostTextConsumeQuota(ctx, info, usage, nil)
assert.Equal(t, int64(25), info.RoutingOutputTokens())
```

在 Realtime 正向 fixture 中给最终 usage 设置 `OutputTokens: 9`，调用 `PostWssConsumeQuota` 后断言：

```go
assert.Equal(t, int64(9), info.RoutingOutputTokens())
```

- [x] **Step 3: 运行测试确认 RED**

Run: `go test ./relay/common ./service -run 'TestRelayInfoRoutingOutputTokensAreAttemptScoped|TestPost(Text|Wss)ConsumeQuota.*RoutingOutputTokens' -count=1`

Expected: FAIL，`RelayInfo` 尚无对应方法，结算路径也未发布输出 Token。

- [x] **Step 4: 实现原子最大值与 retry reset**

在 `relay/common/relay_info.go` 增加 `sync/atomic` import，并在 `RelayInfo` 中加入：

```go
routingAttemptStartTime time.Time
routingOutputTokens     atomic.Int64
```

增加：

```go
func (info *RelayInfo) ObserveRoutingOutputTokens(tokens int64) {
	if info == nil || tokens <= 0 {
		return
	}
	for {
		current := info.routingOutputTokens.Load()
		if tokens <= current || info.routingOutputTokens.CompareAndSwap(current, tokens) {
			return
		}
	}
}

func (info *RelayInfo) RoutingOutputTokens() int64 {
	if info == nil {
		return 0
	}
	return info.routingOutputTokens.Load()
}

func (info *RelayInfo) RoutingAttemptStartTime() time.Time {
	if info == nil {
		return time.Time{}
	}
	if !info.routingAttemptStartTime.IsZero() {
		return info.routingAttemptStartTime
	}
	return info.StartTime
}
```

在 `ResetStreamAttemptState` 重置 stream/counter 状态时加入：

```go
info.routingAttemptStartTime = time.Now()
info.routingOutputTokens.Store(0)
```

- [x] **Step 5: 在已有结算真源同步发布 Token**

在 `PostTextConsumeQuota` 计算 `summary` 后立即写入：

```go
relayInfo.ObserveRoutingOutputTokens(int64(summary.CompletionTokens))
```

在 `PostWssConsumeQuota` 把缺失 usage 归一化为空结构后写入：

```go
relayInfo.ObserveRoutingOutputTokens(int64(usage.OutputTokens))
```

在 `PostAudioConsumeQuota` 入口校验现有非 nil usage 后写入：

```go
relayInfo.ObserveRoutingOutputTokens(int64(usage.CompletionTokens))
```

这些写入必须保持同步；异步 `perfmetrics.RecordRelaySample` 不能作为 Controller 记录 attempt 之前的真源。

- [x] **Step 6: 运行 GREEN 与 Race**

Run: `go test ./relay/common ./service -run 'TestRelayInfoRoutingOutputTokensAreAttemptScoped|TestPost(Text|Wss)ConsumeQuota' -count=1`

Expected: PASS。

Run: `go test -race ./relay/common ./service -run 'TestRelayInfoRoutingOutputTokensAreAttemptScoped|TestPost(Text|Wss)ConsumeQuota' -count=1`

Expected: PASS，无 race report。

- [x] **Step 7: 提交 attempt usage 真源**

```bash
git add relay/common/relay_info.go relay/common/relay_info_test.go service/text_quota.go service/quota.go service/quota_wss_test.go
git commit -m "fix: capture routing attempt output tokens"
```

### Task 2: Routing Bucket 的 Output Token 与 Generation Duration

**Files:**
- Modify: `pkg/routing_metrics/metrics.go`
- Modify: `pkg/routing_metrics/metrics_test.go`

- [x] **Step 1: 写真实吞吐输入失败测试**

在 `pkg/routing_metrics/metrics_test.go` 增加：

```go
func TestRecordClassifiedAttemptCapturesOutputTokensAndGenerationDuration(t *testing.T) {
	enableRoutingMetricsForTest(t)
	logicalStart := time.Now().Add(-10 * time.Second)
	info := &relaycommon.RelayInfo{
		UsingGroup: "vip", OriginModelName: "gpt-test", IsStream: true,
		StartTime: logicalStart,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 28},
	}
	info.ResetStreamAttemptState()
	attemptStart := info.RoutingAttemptStartTime()
	info.FirstResponseTime = attemptStart.Add(500 * time.Millisecond)
	info.ObserveRoutingOutputTokens(150)

	recordTestAttempt(nil, info, 28, nil)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(150), snapshots[0].OutputTokens)
	assert.Less(t, snapshots[0].TotalLatencyMs, int64(2_000))
	assert.Less(t, snapshots[0].TtftP95Ms, int64(1_000))
	assert.Greater(t, snapshots[0].GenerationMs, int64(0))
}

func TestRecordClassifiedAttemptDoesNotAddGenerationWithoutOutputTokens(t *testing.T) {
	enableRoutingMetricsForTest(t)
	logicalStart := time.Now().Add(-10 * time.Second)
	info := &relaycommon.RelayInfo{
		UsingGroup: "vip", OriginModelName: "gpt-test", StartTime: logicalStart,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 29},
	}
	info.ResetStreamAttemptState()

	recordTestAttempt(nil, info, 29, nil)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Zero(t, snapshots[0].OutputTokens)
	assert.Zero(t, snapshots[0].GenerationMs)
}
```

- [x] **Step 2: 运行测试确认 RED**

Run: `go test ./pkg/routing_metrics -run 'TestRecordClassifiedAttempt(CapturesOutputTokens|DoesNotAddGeneration)' -count=1`

Expected: FAIL；当前 live bucket 从不增加 OutputTokens，且无 Token 时仍增加 GenerationMs。

- [x] **Step 3: 把 attempt Token 传入 bucket**

在 `RecordClassifiedAttempt` 中先确定当前 attempt 起点，再计算 latency/TTFT：

```go
attemptStart := info.RoutingAttemptStartTime()
if attemptStart.IsZero() {
	attemptStart = now
}
latencyMs := now.Sub(attemptStart).Milliseconds()
```

把原先所有 `info.StartTime` 参与 attempt latency/TTFT 的计算替换为 `attemptStart`。generation duration 仍从当前 attempt 的 `FirstResponseTime` 计算到结束。随后读取：

```go
outputTokens := info.RoutingOutputTokens()
```

将 `outputTokens` 依次传入 `recordBucket`、`bucket.addLocked`；只在两者都有效时累计：

```go
if outputTokens > 0 && generationMs > 0 {
	b.outputTokens += outputTokens
	b.generationMs += generationMs
}
```

删除原先无条件执行的：

```go
b.generationMs += generationMs
```

Snapshot/Requeue 的已有 OutputTokens/GenerationMs 合并逻辑保持不变。

- [x] **Step 4: 运行 GREEN 与 Race**

Run: `go test ./pkg/routing_metrics -count=1`

Expected: PASS。

Run: `go test -race ./pkg/routing_metrics -count=1`

Expected: PASS，无 race report。

- [x] **Step 5: 提交真实渠道吞吐 bucket**

```bash
git add pkg/routing_metrics/metrics.go pkg/routing_metrics/metrics_test.go
git commit -m "fix: record routing token throughput"
```

### Task 3: Hot Cache Token/s 与流式 TTFT 评分

**Files:**
- Modify: `pkg/routing_hotcache/cache.go`
- Modify: `pkg/routing_hotcache/cache_test.go`
- Modify: `service/routing/types.go`
- Modify: `service/routing/selector.go`
- Modify: `service/routing/selector_test.go`
- Modify: `service/channel_select.go`
- Modify: `service/channel_select_test.go`
- Modify: `middleware/distributor.go`
- Modify: `middleware/distributor_smart_routing_test.go`

- [x] **Step 1: 反转错误 TPS 测试并保护 Delta 合并**

把 `TestLoadMetricSnapshotsBuildsSelectorMetric` 的输入补为：

```go
OutputTokens: 120,
GenerationMs: 2_000,
```

把旧 request/sec 断言替换为：

```go
assert.Equal(t, int64(120), metric.OutputTokens)
assert.Equal(t, int64(2_000), metric.GenerationMs)
assert.InDelta(t, 60.0, metric.TPS, 0.000001)
```

新增同 bucket 两次 delta 合并测试，分别写入 `100 tokens / 1000ms` 与 `50 tokens / 500ms`，断言最终：

```go
assert.Equal(t, int64(150), metric.OutputTokens)
assert.Equal(t, int64(1500), metric.GenerationMs)
assert.InDelta(t, 100.0, metric.TPS, 0.000001)
```

- [x] **Step 2: 写流式 TTFT 选择器失败测试**

在 `service/routing/selector_test.go` 增加：

```go
func TestRankCandidatesUsesTTFTForStreamingLatency(t *testing.T) {
	candidates := []Candidate{
		{Channel: &model.Channel{Id: 1}, Metric: &MetricSnapshot{P95LatencyMs: 900, P95TTFTMs: 100}},
		{Channel: &model.Channel{Id: 2}, Metric: &MetricSnapshot{P95LatencyMs: 200, P95TTFTMs: 500}},
	}
	stream := RankCandidates(candidates, Settings{WeightLatency: 1, PreferTTFT: true})
	nonStream := RankCandidates(candidates, Settings{WeightLatency: 1})

	require.Len(t, stream.Ranked, 2)
	require.Len(t, nonStream.Ranked, 2)
	assert.Equal(t, 1, stream.Ranked[0].Channel.Id)
	assert.Equal(t, 2, nonStream.Ranked[0].Channel.Id)
}
```

在 `service/channel_select_test.go` 的 candidate mapping fixture 中给 `routinghotcache.MetricSnapshot` 设置 `P95TTFTMs: 123`，并断言 `single.Metric.P95TTFTMs == 123`。

- [x] **Step 3: 写旧选择点的 stream 提示失败测试**

在 `middleware/distributor_smart_routing_test.go` 增加：

```go
func TestSetRoutingPromptCostProxyCapturesStreamPreference(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","stream":true}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	setRoutingPromptCostProxy(ctx)

	assert.True(t, common.GetContextKeyBool(ctx, constant.ContextKeyIsStream))
}
```

- [x] **Step 4: 运行测试确认 RED**

Run: `go test ./pkg/routing_hotcache ./service/routing ./service ./middleware -run 'Test(LoadMetricSnapshotsBuildsSelectorMetric|ApplyMetricDeltasMergeTokenThroughput|RankCandidatesUsesTTFTForStreamingLatency|SetRoutingPromptCostProxyCapturesStreamPreference|SmartRoutingCandidates)' -count=1`

Expected: FAIL；当前 TPS 仍为 request/sec、Selector 丢弃 TTFT，旧选择点未记录 stream。

- [x] **Step 5: 以 OutputTokens/GenerationMs 构造兼容 TPS**

扩展 `routinghotcache.MetricSnapshot`：

```go
OutputTokens int64
GenerationMs int64
```

增加：

```go
func outputTokensPerSecond(outputTokens int64, generationMs int64) float64 {
	if outputTokens <= 0 || generationMs <= 0 {
		return 0
	}
	return float64(outputTokens) / (float64(generationMs) / 1000)
}
```

`metricSnapshotFromModel` 保存两个累计字段并用该 helper 生成 `TPS`。`ApplyMetricDeltas` 合并同 bucket 时累计两个字段，再重新计算 `TPS`；不得再使用 RequestCount。

- [x] **Step 6: 让 Selector 按请求类型选择延迟语义**

在 `routing.MetricSnapshot` 增加：

```go
P95TTFTMs float64
```

在 `routing.Settings` 增加：

```go
PreferTTFT bool
```

增加一个纯 helper：

```go
func candidateLatencyMs(metric *MetricSnapshot, settings Settings) float64 {
	if metric == nil {
		return 0
	}
	if settings.PreferTTFT && finitePositive(metric.P95TTFTMs) {
		return metric.P95TTFTMs
	}
	return metric.P95LatencyMs
}
```

`collectScoreBounds` 和 `latencyScore` 使用相同 helper；修改 `latencyScore` 接收 `settings`，避免 bounds 与单候选值使用不同指标。

在 `smartRoutingCandidatesForGroup` 复制：

```go
P95TTFTMs: metric.P95TTFTMs,
```

把 `routingSelectorSettings` 改为接收 `*gin.Context`，并设置：

```go
PreferTTFT: c != nil && common.GetContextKeyBool(c, constant.ContextKeyIsStream),
```

更新当前两个调用点传入各自 context。

- [x] **Step 7: 在旧初选点提前记录 stream 布尔值**

在 `setRoutingPromptCostProxy` 已确认 body 是有效 JSON 后加入：

```go
stream := gjson.GetBytes(body, "stream")
if stream.Exists() {
	common.SetContextKey(c, constant.ContextKeyIsStream, stream.Bool())
}
```

该改动只提供 Phase 0 兼容提示；不得在本任务扩展为完整协议 RequestProfile 或移动 Distributor 调用链。

- [x] **Step 8: 运行 GREEN 与 Race**

Run: `go test ./pkg/routing_hotcache ./service/routing ./service ./middleware -count=1`

Expected: PASS。

Run: `go test -race ./pkg/routing_hotcache ./service/routing ./service ./middleware -run 'Test(LoadMetric|ApplyMetric|RankCandidatesUsesTTFT|SetRoutingPromptCostProxy|SmartRoutingCandidates)' -count=1`

Expected: PASS，无本任务新增 race。

- [x] **Step 9: 提交 Token/s 与 TTFT 语义**

```bash
git add pkg/routing_hotcache/cache.go pkg/routing_hotcache/cache_test.go service/routing/types.go service/routing/selector.go service/routing/selector_test.go service/channel_select.go service/channel_select_test.go middleware/distributor.go middleware/distributor_smart_routing_test.go
git commit -m "fix: use routing token throughput and ttft"
```

### Task 4: Phase 0C 审计与验证记录

**Files:**
- Update: `.agent/channel-routing-2.0-execplan.md`
- Update: `docs/superpowers/plans/2026-07-11-channel-routing-phase0c-telemetry-semantics.md`

- [x] **Step 1: 逐项审计行为**

确认并记录直接证据：

- 每次 attempt 的 Output Token 在结算真源同步发布，较小/负值不能覆盖较大累计值。
- retry 开始时输出 Token 清零并建立独立 attempt 起点；前一渠道 usage 和耗时不污染后一渠道。
- 无真实输出 Token 时不累计 generation duration。
- Hot Cache 的 `TPS` 与 RequestCount 无关，只等于 OutputTokens/GenerationMs。
- 同 bucket 多次 flush/delta 合并保持分子分母相加后再计算。
- 流式请求有 P95 TTFT 时使用 TTFT；非流式和缺失 TTFT 时使用 P95 total latency。
- 初始旧选路与 retry 选路均能读取 stream 提示。
- 未修改 schema、用户计费公式、成本快照或前端。

- [x] **Step 2: 格式化和静态检查**

Run: `gofmt -w relay/common/relay_info.go relay/common/relay_info_test.go service/text_quota.go service/quota.go service/quota_wss_test.go pkg/routing_metrics/metrics.go pkg/routing_metrics/metrics_test.go pkg/routing_hotcache/cache.go pkg/routing_hotcache/cache_test.go service/routing/types.go service/routing/selector.go service/routing/selector_test.go service/channel_select.go service/channel_select_test.go middleware/distributor.go middleware/distributor_smart_routing_test.go`

Expected: exit 0。

Run: `go vet ./relay/common ./service ./pkg/routing_metrics ./pkg/routing_hotcache ./service/routing ./middleware`

Expected: exit 0。

- [x] **Step 3: 新鲜阶段级验证**

Run: `go test ./relay/common ./service ./pkg/routing_metrics ./pkg/routing_hotcache ./service/routing ./middleware -count=1`

Expected: PASS。

Run: `go test -race ./relay/common ./pkg/routing_metrics ./pkg/routing_hotcache ./service/routing ./middleware -count=1`

Expected: PASS，无 race report。

Run: `go test ./... -count=1`

Expected: PASS。

Run: `bun run typecheck`（workdir: `web/default`）

Expected: exit 0。

Run: `bun run build`（workdir: `web/default`，不得与 Go embed 测试并行）

Expected: exit 0。

- [x] **Step 4: 治理和范围审计**

Run: `git diff 80d56a75 -- '*.go' | rg '^\+.*json\.(Marshal|Unmarshal|NewDecoder|NewEncoder)'`

Expected: 无输出。

Run: `git diff 80d56a75 -- '*_test.go' | rg '^\+.*\bt\.(Fatal|Fatalf|FailNow)\b'`

Expected: 无输出。

Run: `git diff 80d56a75 -- . | rg '^-' | rg -i 'QuantumNous|new-api'`

Expected: 无受保护标识删除。

Run: `git diff --check`

Expected: 无输出。

- [x] **Step 5: 更新台账并提交验证记录**

把本计划所有 checkbox 改为 `[x]`；在文末记录 commit range、命令退出状态、无 schema/前端 diff、残余 Phase 0D 风险。把 ExecPlan 的 Phase 0C 标记完成，并把第一个未完成项切到 Phase 0D。

```bash
git add -f .agent/PLANS.md .agent/channel-routing-2.0-execplan.md docs/superpowers/plans/2026-07-11-channel-routing-phase0c-telemetry-semantics.md
git commit -m "docs: record phase 0c routing telemetry"
```

完成本计划后仍不得进入 Phase 1；必须先执行 Phase 0D 并通过完整 Gate 1 审计。

## Execution Record（2026-07-11）

### Commit range

- Base：`80d56a75`（Phase 0B 验证记录）。
- Implementation：`8f43ee5a`、`28effaff`、`bbc3fa84`、`9972e809`、`8cacf23e`、`85357ecb`、`d0c79658`、`d7b3b3e2`。
- 本记录提交将在上述实现之后单独创建。

### 行为证据

- Text、Audio、Realtime 在结算及消费日志数据库操作之前同步发布当前 attempt 的 Output Token 与 attempt end。
- retry reset 同时清零 token/end、重建 attempt 起点；普通 relay 与 Task submit retry 均执行该 reset。
- routing bucket 只在 OutputTokens、GenerationMs 同时为正时成对累计；Drain/Requeue 保持分子分母。
- Hot Cache 保存 OutputTokens/GenerationMs，并按累计值计算 `TPS = OutputTokens / (GenerationMs / 1000)`；RequestCount 不再参与 TPS。
- Selector 的 bounds 与候选评分使用同一延迟 helper；流式优先有效 P95 TTFT，非流式或无效 TTFT 回退 P95 total latency。
- Distributor 从可复用 JSON body 捕获显式 `stream: true/false`，未消费 body，也未扩展为 Phase 1 RequestProfile。
- 本阶段没有 schema、用户计费公式、成本快照或前端源文件变更。

### 新鲜验证

- `go test ./relay/common ./service ./pkg/routing_metrics ./pkg/routing_hotcache ./service/routing ./middleware -count=1`：exit 0。
- `go test -race ./relay/common ./pkg/routing_metrics ./pkg/routing_hotcache ./service/routing ./middleware -count=1`：exit 0。
- `go vet ./relay/common ./service ./pkg/routing_metrics ./pkg/routing_hotcache ./service/routing ./middleware`：exit 0。
- `go test ./... -count=1`：exit 0。
- `bun run typecheck`（`web/default`）：exit 0。
- `bun run build`（`web/default`，在 Go 全仓测试完成后单独运行）：exit 0。
- `GOARCH=386 CGO_ENABLED=0 go test ./relay/common -run TestRelayInfoRoutingObservation -count=1`：exit 0。
- JSON wrapper、Testify、受保护标识、范围与 `git diff --check` 审计：无输出/exit 0。

### 已知基线与下一动作

- 全量 `service -race` 仍命中 Phase 0B 已记录的 `task_polling/logger/model.Task` 既有竞态；本阶段相关 service 定向 race 与要求的其他包 race 均通过。
- MySQL/PostgreSQL DSN 仍未配置；Phase 0C 无 schema 变更，三库 Gate 证据留在 Phase 0D/Gate 1 总验证补齐。
- 下一动作固定为 Phase 0D：`perf_metrics_setting` 快照、perf bucket 有界/线性化、Runtime/Retention、persist-then-publish、Smart Runtime、成本同步 fail-closed 安全边界与 Gate 1 总审计。
