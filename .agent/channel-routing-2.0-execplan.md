# 渠道路由 2.0 ExecPlan

## Purpose / Big Picture

把现有“智能路由 Beta”增量演进为已批准的“版本化控制平面 + 进程内确定性数据平面 + 分组成员模型 + 分层健康状态 + 分布式容量协调”的渠道路由 2.0。仓库级交付必须覆盖 Phase 0–5、后端/API/数据库/安全/成本同步/多节点机制、正式 `web/default` 工作区、兼容迁移、测试与发布证据；生产 SSH、真实流量、真实凭证和生产数据库操作不属于实现验证范围。

## Authoritative Inputs

- `/opt/临时/渠道路由-企业级重构最终方案.md`
  - 期望 SHA-256：`e46728b64adbadcde7d3942c192431f83e79f4e5434856c8b7a8bc1852b70fab`
  - 2026-07-11 已校验一致。
- `/opt/临时/渠道路由-2.0-Codex执行契约.md`
- `docs/superpowers/specs/2026-07-10-channel-routing-v2-design.md`
- `docs/superpowers/plans/2026-07-10-channel-routing-phase0-runtime-safety.md`
- `docs/superpowers/plans/2026-07-10-channel-routing-phase0b-error-capacity-multikey.md`
- 根 `AGENTS.md` 与 `web/default/AGENTS.md`

架构已经批准，不重新访谈或改变总体方向。当前事实与旧行号冲突时，以当前代码和实际测试为准增量适配。

## Context and Orientation

- 主仓库：`/opt/xy-api`
- 专用工作树：`/root/.config/superpowers/worktrees/xy-api/feat-channel-routing-v2`
- 分支：`feat/channel-routing-v2`
- 当前 HEAD：`d7b3b3e2`（Phase 0C 实现完成）
- 基线：`main` / `origin/main` = `e868614b`
- 工作树仅有本台账目录 `.agent/` 尚待强制纳入版本控制。
- 该分支相对 main 修改 130 个文件，约 `20235 insertions / 2022 deletions`；正式前端尚无差异。
- 旧并行 Task 4 工作树仍存在，但不作为当前实现真源。

## Progress

- [x] Gate 0：读取批准方案、执行契约、AGENTS、旧会话、设计、现有计划、Git 状态和当前实现。
- [x] Gate 0：校验方案 SHA-256，确认专用 worktree 与干净基线。
- [x] Phase 0A：配置并发安全、有界 metrics/breaker/hot cache/JWT、可取消 SmartRoutingRuntime、Retention。
- [x] Phase 0B：错误证据与分类、Reliability/Capacity 分离、Multi-Key 安全降级、提交边界与流式计费闭环。
- [x] Phase 0C：真实 Output Token、独立 attempt latency/generation duration 与 TTFT 接线；渠道级 TPS 改为真实 Token/s；重试 attempt 遥测隔离。
- [ ] Phase 0D：统一/收敛 `routing_metrics` 与 `perf_metrics` 生命周期语义；修复设置竞态、无界桶、永久 Worker、退避/Jitter/可观测边界；完成 Gate 1 总审计。
- [ ] Phase 1 Observe：Pool、Member、稳定 Credential ID、统一有界遥测、兼容迁移、只读 API、新选择器仅审计。
- [ ] Phase 2 Shadow：双算、差异审计、决策重放、Revision/Outbox/Redis Stream、增量聚合、成本快照、多实例降级。
- [ ] Phase 3 Canary：确定性灰度、自动回滚、容量预留、慢启动、故障注入；Hedging 保持关闭。
- [ ] Phase 4 Balanced：硬约束、绝对 SLO、Weighted P2C、探索、亲和保护、主动探测、首字前切换、策略治理、兼容改名。
- [ ] Phase 5 Enterprise SLO：严格容量租约、多区域、独立 RBAC、双人审批、Error Budget Burn、审计导出、预算 Hedging。
- [ ] Gate 7 前端：七个深链页面、单 SSE、完整状态、六语言、A11y、响应式、明暗主题与视觉验证。
- [ ] 最终验证：三数据库、Redis/多节点、race/vet/build、故障注入、benchmark/soak、前端测试/E2E/Axe/视觉与独立 P0/P1 审查。
- [ ] Git/发布：提交分支、PR、同步最新 main、合入前复验、合入 main、按正常节奏构建下一版镜像。

## Requirement Traceability Matrix

| ID | Requirement | Current evidence | Status / next evidence |
| --- | --- | --- | --- |
| CR-0.1 | 关闭态不分配，所有缓存/Map/锁有 TTL、容量、统计 | Phase 0A 计划、`pkg/routing_*`、Sub2API JWT 测试 | PASS（后续统一遥测仍需复核） |
| CR-0.2 | 设置并发安全，Worker 可取消、可等待，Retention 生效 | Phase 0A 计划、`setting/config`、`SmartRoutingRuntime` | PARTIAL：`perf_metrics_setting`、最终 flush、Context DB 调用、退避/Jitter 尚未纳入 |
| CR-0.3 | 错误责任/作用域/重试/健康/容量分类正确 | Phase 0B 计划、`pkg/routing_error`、Controller/Task tests | PASS |
| CR-0.4 | 429/529 与 Reliability Breaker 分离 | `pkg/routing_hotcache/capacity.go`、Phase 0B tests | PASS |
| CR-0.5 | Multi-Key 不写聚合状态，稳定身份上线前安全降级 | Phase 0B tests | PASS；Phase 1 用 Credential ID 替代降级 |
| CR-0.6 | 真实 Output Token、TTFT、Token/s | `8f43ee5a..d7b3b3e2`；结算边界同步 usage、attempt end、routing bucket、Hot Cache、Selector 与 stream hint 测试 | PASS |
| CR-0.7 | Cost Connector 与 Serving health 分离 | Phase 0B Task 9 tests | PASS |
| CR-1 | Pool/Member/Credential/Observe/三库迁移/只读 API | 尚无 v2 模型/API | PENDING |
| CR-2 | Shadow、Revision、Outbox、Redis Stream、重放、正确可合并分布 | 尚无实现 | PENDING |
| CR-3 | Canary、确定性灰度、自动回滚、容量预留、慢启动 | 尚无实现 | PENDING |
| CR-4 | Balanced 选择器、主动探测、首字前切换、策略发布/回滚 | 尚无 v2 实现 | PENDING |
| CR-5 | Enterprise SLO、严格租约、多区域、RBAC/双人审批、Burn、Hedging | 尚无实现 | PENDING |
| CR-FE | 七页渠道路由工作区、六语言、SSE/A11y/响应式/视觉 | `web/default` 相对 main 无差异 | PENDING |
| CR-COMPAT | `/smart-routing`、旧 API/配置键保留并给迁移提示 | 旧路径仍在；新路径尚无 | PARTIAL |
| CR-SEC | SSRF/DNS rebinding/重定向/TLS/大小/脱敏/凭证轮换 | 现有成本连接器未满足完整威胁模型 | PENDING |
| CR-BILL | 用户只结算一次；逐 attempt 平台成本审计；未知价格非零 | Phase 0B 修复提交边界；v2 成本审计尚无 | PARTIAL |
| CR-GIT | PR、同步 main、合入、镜像构建 | 尚未执行 | PENDING |

## Plan of Work

1. [完成] Phase 0C：结算层真实 usage 与 attempt end、retry reset、渠道 bucket、真实 Token/s、流式 TTFT 评分及旧选择点 stream hint。
2. [当前] 为 Phase 0D 写计划并执行：审计并收敛 `pkg/perf_metrics` 的无界 Map、永久 `time.Sleep` worker、未锁设置和重复采样；补退避/Jitter、生命周期与 Gate 1 验证。
3. 每个后续 Phase 单独写 plan，严格依赖前一 Gate 的稳定 DTO/API/迁移契约；先后端 Observe/Shadow，再正式前端。
4. 每个行为切片先写失败测试并确认 RED，再实现最小根因修复、运行最窄测试、审查并扩大验证。
5. 每个 Gate 结束后更新本台账和追踪矩阵，创建单一职责本地提交；不把旧会话报告当作新鲜证据。

## Validation and Acceptance

### Fresh baseline (2026-07-11)

- `go test ./... -count=1`：exit 0。
- `bun run typecheck`（`web/default`）：exit 0。
- `git diff --check main...HEAD`：exit 0。
- MySQL/PostgreSQL DSN 当前尚未确认；旧 Phase 0 记录为未配置并 SKIP，最终 Gate 必须补齐隔离三库验证或明确真实环境阻塞。

### Phase 0C fresh evidence (2026-07-11)

- Commit range：`80d56a75..d7b3b3e2`，实现提交为 `8f43ee5a`、`28effaff`、`bbc3fa84`、`9972e809`、`8cacf23e`、`85357ecb`、`d0c79658`、`d7b3b3e2`。
- `go test ./relay/common ./service ./pkg/routing_metrics ./pkg/routing_hotcache ./service/routing ./middleware -count=1`：exit 0。
- `go test -race ./relay/common ./pkg/routing_metrics ./pkg/routing_hotcache ./service/routing ./middleware -count=1`：exit 0。
- `go vet ./relay/common ./service ./pkg/routing_metrics ./pkg/routing_hotcache ./service/routing ./middleware`：exit 0。
- `go test ./... -count=1`：exit 0。
- `bun run typecheck`、`bun run build`（`web/default`）：均 exit 0；Phase 0C 无前端源文件差异。
- `GOARCH=386 CGO_ENABLED=0 go test ./relay/common -run TestRelayInfoRoutingObservation -count=1`：exit 0，保护 64 位原子字段对齐。
- JSON wrapper、Testify、受保护标识与 `git diff --check` 审计：均无新增违规。
- 全量 `service -race` 仍会命中既有 `task_polling/logger/model.Task` 竞态；本次相关 service 定向 race 与阶段要求的 race 包均通过，未把既有竞态误报为 Phase 0C 成功证据。

### Required final evidence

- Go：相关包、`go test ./...`、`go vet ./...`、`go build ./...`、相关及尽可能全仓 race。
- DB：SQLite、MySQL 5.7.8+、PostgreSQL 9.6+ 的迁移幂等与行为契约。
- Redis/多节点：Revision、Stream、租约、Half-open、容量与故障降级。
- 安全/故障注入：DNS、TLS、401/403、402、429、529、5xx、首字超时、流中错误、SSRF 与重定向。
- 性能：本地 selector p99、配置传播、Heap/Goroutine 差分、独立 benchmark/soak。
- 前端：typecheck、lint、format、i18n、关键测试、build、E2E、Axe、键盘、明暗主题、320/768/1440 视觉检查。
- 最终独立审查：无未解决 P0/P1；`git diff --check`、敏感信息、临时文件、生成物、无关依赖与受保护标识审计。

## Surprises & Discoveries

- 旧会话只完成 Phase 0A/0B；最后的“已完成”只指 Phase 0B 收尾，不是完整渠道路由 2.0。
- `RoutingChannelMetric` 已有 `OutputTokens`/`GenerationMs` 字段，但 live `RecordClassifiedAttempt` 没有累加 OutputTokens。
- `routing_hotcache.MetricSnapshot.TPS` 仍按 `RequestCount / bucketSeconds` 计算，确认与方案指出的问题一致。
- `RelayInfo.ResetStreamAttemptState` 不建立独立 attempt 起点；渠道级 latency/TTFT 当前使用逻辑请求 `StartTime`，retry 会包含前序渠道耗时。
- Phase 0C 初版把 `atomic.Int64` 内嵌进可按值复制的 `RelayInfo`，`go vet` 报 copylocks；改用结构体首部对齐的原始 `int64` + `sync/atomic` 后，同时满足值复制与 386 对齐。
- 仅在 Controller 返回后取结束时间会把结算/日志数据库耗时混入 generation duration；最终改为在 Text/Audio/Realtime usage 入口、数据库操作之前同步捕获 attempt end。
- 普通 relay loop 已重置 attempt，但 Task submit retry loop 原先遗漏；Phase 0C 将每次真实 Task submit 包装为先 reset 再提交，并用连续两次本地失败保护。
- `pkg/perf_metrics` 已有正确的 Token/s 公式，但使用独立的 model+group 遥测、永久 `time.Sleep` worker、直接读取可变设置和无硬上限 `sync.Map`；不能把它当作 Gate 1 已完成证据。
- `SmartRoutingRuntime` 的 refresh/flush callback 当前吞掉错误、固定周期无退避/Jitter，DB 操作不接收 Context，Shutdown 前没有最终 flush。
- 成本连接器仍允许任意 HTTP/HTTPS 目标并使用默认重定向，尚无 DNS pinning、逐跳校验、TLS/Content-Type/大小策略；属于 Gate 1 后半段安全阻断项。
- 设置更新当前先替换内存再持久化，DB 写失败会留下仅本节点生效的新设置；需在 Phase 0D 加回滚/原子性测试，Revision/ETag 的完整治理留给 Phase 2。
- 渠道删除与 `channelPollingLocks` 仍有生命周期/孤儿状态风险；需在 Gate 1 审计中决定最小兼容清理边界。
- Phase 0B 的完整 race 记录包含既有 logger、task polling 和并行 `gin.SetMode` 竞态；新增路径定向 race 通过。最终 Gate 需要重新判断这些基线竞态是否仍阻止仓库级完整 race 声明。

## Decision Log

- 2026-07-11：继续使用现有专用 worktree/分支，不在 main 上实现，也不新建嵌套 worktree。
- 2026-07-11：批准方案与仓库内设计视为 brainstorming/spec 已通过，不重新请求同一设计批准。
- 2026-07-11：把上一轮“完成”解释为 Phase 0B 完成，完整 Goal 保持 active。
- 2026-07-11：先完成 Gate 1 缺口，再进入 Phase 1；正式前端仍等待 Observe/Shadow DTO/API 稳定。
- 2026-07-11：兼容保留字段名 `TPS`，但其语义在 Phase 0C 修正为真实输出 Token/s；后续 v2 DTO 使用明确 `output_tokens_per_second` 名称。
- 2026-07-11：attempt end 保存为相对 attempt start 的原子 duration，而不是绝对 Unix 时间；这样可保留单调时钟语义、支持确定性测试，并避免本地结算耗时污染上游吞吐。
- 2026-07-11：流式请求仅在 P95 TTFT 为有限正数时优先 TTFT；非流式或无效 TTFT 保持 P95 total latency 兼容行为。

## Idempotence and Recovery

- 所有数据库验证使用临时/隔离库；不连接生产，不执行破坏性迁移。
- 当前专用 worktree 已注册在 Git common dir；恢复时先检查 worktree/branch/HEAD，不重新创建。
- 若测试失败，先分类为新增、既有或环境问题；保存最小复现并只修当前切片。
- 不使用 reset、checkout、stash、rebase、amend 或强推恢复。
- 子代理只处理有界任务；根代理复核 diff 与测试。并行写必须使用独立 worktree和无重叠文件。

## Outcomes & Retrospective

Phase 0C 已完成并有新鲜验证；完整目标仍进行中。下一恢复点是 Phase 0D 第一个设置快照切片，完成 Gate 1 前不得进入 Phase 1。
