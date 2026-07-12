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
- 当前 HEAD：`90b25f8a`（Gate 2 已提交）；Gate 3 已完成验证，等待单独提交。
- 开发基线位于专用分支 `feat/channel-routing-v2`，不在 `/opt/xy-api` 的 main 工作树直接开发。
- `.agent/` 与阶段计划属于交付台账，提交时需强制纳入版本控制。
- 正式 `web/default` 前端尚未开始；Gate 3 DTO/API/权限语义已稳定，后端 Phase 3–5 完成后进入正式工作区实现。
- 旧并行 Task 4 工作树仍存在，但不作为当前实现真源。

## Progress

- [x] Gate 0：读取批准方案、执行契约、AGENTS、旧会话、设计、现有计划、Git 状态和当前实现。
- [x] Gate 0：校验方案 SHA-256，确认专用 worktree 与干净基线。
- [x] Phase 0A：配置并发安全、有界 metrics/breaker/hot cache/JWT、可取消 SmartRoutingRuntime、Retention。
- [x] Phase 0B：错误证据与分类、Reliability/Capacity 分离、Multi-Key 安全降级、提交边界与流式计费闭环。
- [x] Phase 0C：真实 Output Token、独立 attempt latency/generation duration 与 TTFT 接线；渠道级 TPS 改为真实 Token/s；重试 attempt 遥测隔离。
- [x] Phase 0D：统一/收敛 `routing_metrics` 与 `perf_metrics` 生命周期语义；修复设置竞态、无界桶、永久 Worker、退避/Jitter/可观测边界；完成 Gate 1 总审计。
- [x] Phase 1 Observe：Pool、Member、稳定 Credential ID、统一有界遥测、兼容迁移、只读 API、新选择器仅审计。
- [x] Phase 2 Shadow：双算、差异审计、决策重放、Revision/Outbox/Redis Stream、增量聚合、成本快照、多实例降级。
- [ ] Phase 3 Canary：确定性灰度、自动回滚、容量预留、慢启动、故障注入；Hedging 保持关闭。
- [ ] Phase 4 Balanced：硬约束、绝对 SLO、Weighted P2C、探索、亲和保护、主动探测、首字前切换、策略治理、兼容改名。主动探测与提交前串行故障转移切片已实现并验证，Balanced 热路径与治理仍在继续。
- [ ] Phase 5 Enterprise SLO：严格容量租约、多区域、独立 RBAC、双人审批、Error Budget Burn、审计导出、预算 Hedging。
- [ ] Gate 7 前端：七个深链页面、单 SSE、完整状态、六语言、A11y、响应式、明暗主题与视觉验证。
- [ ] 最终验证：三数据库、Redis/多节点、race/vet/build、故障注入、benchmark/soak、前端测试/E2E/Axe/视觉与独立 P0/P1 审查。
- [ ] Git/发布：提交分支、PR、同步最新 main、合入前复验、合入 main、按正常节奏构建下一版镜像。

## Requirement Traceability Matrix

| ID | Requirement | Current evidence | Status / next evidence |
| --- | --- | --- | --- |
| CR-0.1 | 关闭态不分配，所有缓存/Map/锁有 TTL、容量、统计 | Phase 0A 计划、`pkg/routing_*`、Sub2API JWT 测试 | PASS（后续统一遥测仍需复核） |
| CR-0.2 | 设置并发安全，Worker 可取消、可等待，Retention 生效 | Gate 1 提交 `bcb6602b`、Runtime/Race/Retention 测试 | PASS |
| CR-0.3 | 错误责任/作用域/重试/健康/容量分类正确 | Phase 0B 计划、`pkg/routing_error`、Controller/Task tests | PASS |
| CR-0.4 | 429/529 与 Reliability Breaker 分离 | `pkg/routing_hotcache/capacity.go`、Phase 0B tests | PASS |
| CR-0.5 | Multi-Key 不写聚合状态，稳定身份上线前安全降级 | Phase 0B tests | PASS；Phase 1 用 Credential ID 替代降级 |
| CR-0.6 | 真实 Output Token、TTFT、Token/s | `8f43ee5a..d7b3b3e2`；结算边界同步 usage、attempt end、routing bucket、Hot Cache、Selector 与 stream hint 测试 | PASS |
| CR-0.7 | Cost Connector 与 Serving health 分离 | Phase 0B Task 9 tests | PASS |
| CR-1 | Pool/Member/Credential/Observe/三库迁移/只读 API | Phase 1 模型、stable rollup、不可变快照、审计、v2 API、三库与 Race 证据 | PASS（Gate 2） |
| CR-2 | Shadow、Revision、Outbox、Redis Stream、重放、正确可合并分布 | Gate 3 实现、三库/Redis/Race/Vet/Build 与独立 P0/P1 审查 | PASS（等待提交） |
| CR-3 | Canary、确定性灰度、自动回滚、容量预留、慢启动 | 尚无实现 | PENDING |
| CR-4 | Balanced 选择器、主动探测、首字前切换、策略发布/回滚 | 尚无 v2 实现 | PENDING |
| CR-5 | Enterprise SLO、严格租约、多区域、RBAC/双人审批、Burn、Hedging | 尚无实现 | PENDING |
| CR-FE | 七页渠道路由工作区、六语言、SSE/A11y/响应式/视觉 | `web/default` 相对 main 无差异 | PENDING |
| CR-COMPAT | `/smart-routing`、旧 API/配置键保留并给迁移提示 | 旧路径/配置保留，Observe 双算不改变实际 legacy 渠道 | PASS（正式改名提示留 Gate 5/7） |
| CR-SEC | SSRF/DNS rebinding/重定向/TLS/大小/脱敏/凭证轮换 | 现有成本连接器未满足完整威胁模型 | PENDING |
| CR-BILL | 用户只结算一次；逐 attempt 平台成本审计；未知价格非零 | Phase 0B 修复提交边界；v2 成本审计尚无 | PARTIAL |
| CR-GIT | PR、同步 main、合入、镜像构建 | 尚未执行 | PENDING |

## Plan of Work

1. [完成] Phase 0C：结算层真实 usage 与 attempt end、retry reset、渠道 bucket、真实 Token/s、流式 TTFT 评分及旧选择点 stream hint。
2. [完成] Phase 0D / Gate 1：有界状态、正确分类、容量分离、真实 TTFT/Token/s、Worker 生命周期、安全与最终 flush。
3. [完成] Phase 1 / Gate 2：稳定身份、stable telemetry + rollup、不可变快照、Observe 审计、只读 v2 API、三库与并发验证。
4. [完成] Phase 2 / Gate 3：可合并分布、Shadow 双算/重放、Revision/Activation/Outbox、Redis Stream、node sequence 幂等与增量聚合。
5. [当前] Phase 3 / Gate 4 收口与 Phase 4 / Gate 5 并行推进：Canary P1 审查继续；Balanced 主动探测和提交前串行故障转移已完成切片验证，继续接入正式热路径与策略治理。
6. 每个行为切片先写失败测试并确认 RED，再实现最小根因修复、运行最窄测试、审查并扩大验证。
7. 每个 Gate 结束后更新本台账和追踪矩阵，创建单一职责本地提交；不把旧会话报告当作新鲜证据。

### Phase 2 implementation slices

1. 可合并分布：固定 DDSketch codec v1（2% 相对误差、384 bins、1 小时输入上限），保存 latency/TTFT 官方 protobuf；所有解码先做字节、mapping、bin、count、有限值和版本校验。
2. Stable telemetry 接线：热桶维护分布，Snapshot/Drain/Requeue 深拷贝且有项目数/字节上限；Token/s 继续按 `sum(output_tokens) / sum(generation_ms)` 计算。
3. Rollup 与幂等摄取：扩展 nullable sketch 字段，数据库事务内以 `(node_epoch_id, sequence)` Receipt 去重、受检累加计数并在 Go 中合并分布；模糊提交不得生成新 sequence。
4. Snapshot 读取：Repeatable Read 内分页读取原始 Rollup，在 Go 中按 Member+Model 合并 Credential/Bucket；只有 sample coverage 完整时才公开 p50/p95/p99，旧数据不伪造百分位。
5. 版本化控制平面：Policy Head CAS、不可变 Revision、版本化 Pool/Member、Activation、Transactional Outbox、Runtime Checkpoint；回滚只创建更高 Revision。
6. Redis 多节点：配置 Stream 对每个节点广播并由 DB 对账兜底；遥测 Stream 使用 consumer group，DB Receipt 提供最终幂等；Redis 故障时继续 LKG 并走 DB fallback。
7. 版本化成本：按 Upstream Account 聚合同步，不可变成本历史与兼容 latest dual-write，Expected/Worst/Effective Cost、freshness/confidence 语义完整且不改变用户结算。
8. 确定性 Shadow/Replay：白名单 RequestProfile、DB Policy Revision、Snapshot Hash、算法版本以及 `Request ID + Revision + Retry Index` 种子；同一审计重放必须逐项一致，Shadow 不改变 legacy 实际渠道。
9. Gate 3 收口：只读/重放/仿真 API、多节点传播状态、pipeline lag 和降级原因；完成三库、Redis 故障、Race/Vet/Build 与独立 P0/P1 审查后才进入 Canary。

### Phase 3 implementation slices

1. Activation 快照：在同一 Repeatable Read 中校验 Head、Revision 与 Activation；快照携带 Activation ID、Stage 和 Traffic Basis Points，并拒绝 Pool/Activation 阶段冲突。
2. 确定性门控：按 `PoolID + RequestID` 计算稳定 0–9999 bucket；Canary 仅允许 100–500 BP，扩容时旧 cohort 必须保持为子集，Retry/节点变化不得改变 cohort。
3. 请求级固定会话：一次逻辑请求固定 runtime snapshot、revision、pool/member identity；Canary cohort 绕过 legacy affinity，control cohort 完整保留旧行为。
4. Canary 实际选路：复用 Shadow V1 的确定性候选与评分，加入请求内失败目标排除；Phase 4 前不启用 Weighted P2C/探索，Hedging 在策略和运行时双重禁止。
5. 本机软容量：有界、分片、TTL 的 RPM/输入 TPM/输出 TPM/并发预留；Setup/发送失败可取消，结束释放，明确标记 `local_soft`，严格共享租约留 Phase 5。
6. 慢启动：新成员和恢复成员从保守份额线性 Ramp；因子进入 Canary 重放和有效容量，冷节点重启不得瞬间恢复满流量。
7. Canary 结果窗口：按 rollout/cohort 记录逻辑成功、TTFT 可合并分布、成本覆盖和 Retry 放大；使用绝对 checkpoint 与 node sequence，未知数据不伪造通过。
8. 自动回滚：Control Lease 单执行者、持久化 Evaluation/Operation、连续完整窗口触发 Pool-scoped 更高 Revision 回滚；不得整 Revision 覆盖其他 Pool。
9. Gate 4 故障注入：覆盖 DNS/TLS/401/403/402/429/529/5xx/首字超时/提交后错误，并断言最大并发 attempt 始终为 1。

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

### Gate 2 fresh evidence（2026-07-12）

- `go test ./... -count=1`、`go vet ./...`、`go build ./...`：exit 0。
- `go test -race ./model ./service/channelrouting ./pkg/routing_metrics -count=1`：exit 0。
- MySQL 5.7 / PostgreSQL 15 串行真实契约：模型迁移、模型/分组大小写隔离、stable rollup、active/retired credential、keyless、live merge、decision 精确 hash filter：PASS；临时容器已删除。
- 全仓 Race 仍复现既有 `service/task_polling`/`logger` 竞态；本阶段相关包定向 Race 通过。
- 独立 blocker review：无未解决 P0/P1。Phase 2 接管模糊提交去重、DDSketch/HDR 与 DB 聚合热点。

### Gate 3 fresh evidence（2026-07-12）

- `go test ./... -count=1`、`go vet ./...`、`go build ./...`：exit 0。
- `go test -race ./service/channelrouting -count=1`：exit 0；Model 成本历史定向 Race、Middleware Race、Rollup/Distribution 定向 Race 均通过。
- MySQL 5.7.44、PostgreSQL 15.18：Policy/Replay Chunk、Cost V2、Rollup/Receipt、Control Lease、Snapshot、旧非空成本表增量迁移和 Retention 全部 PASS，无 SKIP。
- Redis 7.4.9：多节点 Config Stream、Telemetry commit-before-ack 幂等重投、pending/undelivered/backlog 与 oldest age 全部 PASS。
- 60 KiB Receipt tombstone、4096 Policy Member/Shadow Candidate、约 1 MiB DB 动态分批、最大 Replay Chunk、最终 Flush、多节点传播状态等高风险回归通过。
- 独立 P0/P1 审查发现并修复：成本观测历史无界增长、普通 Policy Head 检查深拷贝完整快照；修复后跨库与 Race 复验通过。
- 全仓首次复跑发现 Middleware fixture 缺少 Policy 迁移，已补齐完整 Policy 控制面模型并通过全仓复跑。
- `git diff --check`、JSON wrapper、敏感信息、临时容器/测试库清理审计：PASS。

### Phase 4 active probe / pre-commit failover slice（2026-07-12）

- Attempt Coordinator 已接入 Relay/Task：单逻辑请求最多一个并发 attempt，Hedging 硬关闭，约束尝试数、总切换 Deadline、额外成本与 Pool 级有界 Retry Token Bucket；首次发送前拒绝会返回明确终止错误，客户端提交后不再切换。
- Active Probe 仅在 Balanced/Enterprise 且显式开启时运行；Open/Half-open 优先、Degraded 次之、Healthy 最后，同层确定性轮换；总并发、Per-host、目标数、Token/成本、租约与本机调度状态均有界，关闭开关会停止排队任务。
- Probe 使用真实渠道测试链但限制输出，不写生产消费日志/路由遥测；过期目标发送前拒绝，Multi-key 使用隔离副本且不推进生产轮询游标，不写聚合 Breaker；结果具备 CAS/Fencing、幂等 ID、分页 API 与保留期清理。
- `go test ./controller ./model ./router ./service ./service/channelrouting ./pkg/routing_error ./setting/smart_routing_setting -count=1`：exit 0。
- `go test -race` 定向覆盖 Attempt、Probe、Runtime、Controller、Model：exit 0；相关包 `go vet`：exit 0。
- MySQL 5.7 与 PostgreSQL 15 隔离容器实跑 Probe/完整 Routing migration contract：PASS；临时容器已删除。
- `go test ./... -count=1`：除根包因工作树缺少 `web/classic/dist` embed 生成物而 setup failed 外，其余所有包 PASS；最终 Gate 在生成前端 dist 后复跑。

## Surprises & Discoveries

- 旧会话只完成 Phase 0A/0B；最后的“已完成”只指 Phase 0B 收尾，不是完整渠道路由 2.0。
- `RoutingChannelMetric` 已有 `OutputTokens`/`GenerationMs` 字段，但 live `RecordClassifiedAttempt` 没有累加 OutputTokens。
- `routing_hotcache.MetricSnapshot.TPS` 仍按 `RequestCount / bucketSeconds` 计算，确认与方案指出的问题一致。
- `RelayInfo.ResetStreamAttemptState` 不建立独立 attempt 起点；渠道级 latency/TTFT 当前使用逻辑请求 `StartTime`，retry 会包含前序渠道耗时。
- Phase 0C 初版把 `atomic.Int64` 内嵌进可按值复制的 `RelayInfo`，`go vet` 报 copylocks；改用结构体首部对齐的原始 `int64` + `sync/atomic` 后，同时满足值复制与 386 对齐。
- 仅在 Controller 返回后取结束时间会把结算/日志数据库耗时混入 generation duration；最终改为在 Text/Audio/Realtime usage 入口、数据库操作之前同步捕获 attempt end。
- 普通 relay loop 已重置 attempt，但 Task submit retry loop 原先遗漏；Phase 0C 将每次真实 Task submit 包装为先 reset 再提交，并用连续两次本地失败保护。
- Gate 1 已把 `pkg/perf_metrics`、Smart Routing Runtime、成本连接器出站安全、设置回滚和最终 flush 的上述问题收口；这些条目保留为历史发现，不再是当前阻塞。
- Gate 2 发现 MySQL 默认 CI collation 会折叠 `VIP`/`vip` 与 `Model-X`/`model-x`；Pool、Rollup 和 Decision 过滤统一改用原始文本 SHA-256 稳定键。
- Stable additive upsert 在“数据库已提交但客户端收到错误”的模糊提交场景仍可能重复计数；Phase 2 必须引入 `node_id + sequence` 幂等协议。
- 近期 Rollup 的 DB `GROUP BY` 在高流量下是可预见热点；Phase 2 使用增量聚合与可合并分布替代持续全窗扫描。
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

Gate 3 已完成并有新鲜三库、Redis、Race、Vet、Build、全仓测试和独立 P0/P1 审查证据；完整目标仍进行中。下一恢复点是 Phase 3 Activation 快照、确定性 Canary 门控和请求级固定会话。
