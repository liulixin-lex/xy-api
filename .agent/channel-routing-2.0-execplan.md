# 渠道路由 2.0 ExecPlan（历史归档）

> **归档声明（2026-07-15）**
>
> 本文保留 Phase 0–5 和旧 NewAPI/Sub2API 成本连接器的历史实施记录，不再是当前渠道路由的接口、连接器或发布真源。当前结论由用户批准的“Routing v2 完整退场方案”、`docs/channel/channel-routing-operations.md`、当前工作树和最新实际验证证据共同约束：
>
> - 正式后端 API 统一为 `/api/channel-routing/*`；`/api/channel-routing/v2/*` 不注册、不重定向、不代理。
> - `/api/smart-routing/*` 不再注册；前端 `/smart-routing` 仅保留到 `/channel-routing/overview` 的书签重定向。
> - NewAPI/Sub2API 路由管理连接器和自动成本同步已经退役；`/api/channel-routing/cost-bindings*` 与 `/api/channel-routing/costs/sync` 只返回 `410 Gone`。
> - 当前平台成本使用“系统定价 × 渠道倍率”，旧账号、绑定、成本版本和 Operation 仅按迁移或不可变历史边界保留。
>
> 下文凡与这些结论冲突的描述均为历史状态，不得用于恢复旧 API、旧 Worker、旧管理凭据或旧连接器。

## Purpose / Big Picture

把现有“智能路由 Beta”增量演进为已批准的“版本化控制平面 + 进程内确定性数据平面 + 分组成员模型 + 分层健康状态 + 分布式容量协调”的渠道路由 2.0。仓库级交付必须覆盖 Phase 0–5、后端/API/数据库/安全/成本同步/多节点机制、正式 `web/default` 工作区、兼容迁移、测试与发布证据；生产 SSH、真实流量、真实凭证和生产数据库操作不属于实现验证范围。

## 历史时期的 Authoritative Inputs

以下输入只说明本文形成时的设计背景；当前恢复入口见本文件顶部归档声明和 `.agent/PLANS.md`。

- `/opt/临时/渠道路由-企业级重构最终方案.md`
  - 期望 SHA-256：`e46728b64adbadcde7d3942c192431f83e79f4e5434856c8b7a8bc1852b70fab`
  - 2026-07-13 恢复检查点再次校验一致。
- `/opt/临时/渠道路由-2.0-Codex执行契约.md`
  - SHA-256：`e634a688013deb726a6b2f8e7c9f892eae2d22f4163b6bb63dd6b838fbc28afc`
- `/opt/临时/渠道路由-2.0-Codex提示词套件.md`
  - SHA-256：`243fe4f6d78182420a03fdf44dbee6644c683e4e054d6683640ed2710f1a66c2`
- `docs/superpowers/specs/2026-07-10-channel-routing-v2-design.md`
- `docs/superpowers/plans/2026-07-10-channel-routing-phase0-runtime-safety.md`
- `docs/superpowers/plans/2026-07-10-channel-routing-phase0b-error-capacity-multikey.md`
- `.agent/channel-routing-upstream-account-connectors.md`
  - NewAPI 官方审计基线：`a63364d156cf2a64f1c3d1ee4923d73d5f3222a1`
  - Sub2API 官方审计基线：`30df3f68e4ea6ce493af1d6651d3f668201c14dd`
- 根 `AGENTS.md` 与 `web/default/AGENTS.md`

架构已经批准，不重新访谈或改变总体方向。当前事实与旧行号冲突时，以当前代码和实际测试为准增量适配。

## Context and Orientation

- 主仓库与当前交付工作树：`/opt/xy-api`
- 当前修复分支：`fix/channel-routing-v0.1.12`，从 `main` / `origin/main` 的 `505496280b019d48589b981c9be47dd8fc89c713` 创建；`v0.1.11` 正式 tag 精确指向 `9a89fdf12bde37ceabfb1bf80aad52ee2124307f`。
- `v0.1.11` 已完成 PR、main、tag、Release 与多架构镜像闭环；本轮 `v0.1.12` 只做发布后渠道路由审计修复，不改写上一版本的历史候选证据。
- durable async billing、accepted/terminal 投影、Cost Sources、Manual Reviews、Projection Operations、发布供应链和 SQLite 旧卷升级修复均已集成到当前分支。不得 reset、checkout、stash、rebase、amend 或覆盖并行改动。
- `.agent/` 与阶段计划属于交付台账，提交时需强制纳入版本控制。
- **历史状态，已被退场方案覆盖：** 当时项目仍有 NewAPI / Sub2API 上游账号连接器，v0.1.12 计划只修正其协议和安全边界。当前连接器已退役，不得据此恢复管理凭据、账号同步或自动成本同步。
- 正式 `web/default` 工作区已合并；Typecheck、Lint、七语 i18n、`bun test`（176 pass、0 fail、48 files）、production build、Playwright、Axe、键盘、缩放、响应式和明暗主题验收均已完成。RU/FR 320px Cursor 分页及运行时 `<html lang>` 已完成 8/8 真浏览器回归。
- 基于 `6ca75bda` 的候选镜像与正式 `v0.1.10` SQLite 隔离卷双启动升级门禁均已通过；随后 PR #20、#21、#22 依次合入，`v0.1.11` 正式发布和供应链终验均已完成。

## Progress

- [x] Gate 0：读取批准方案、执行契约、AGENTS、旧会话、设计、现有计划、Git 状态和当前实现。
- [x] Gate 0：校验方案 SHA-256，确认专用 worktree 与干净基线。
- [x] Phase 0A：配置并发安全、有界 metrics/breaker/hot cache/JWT、可取消 SmartRoutingRuntime、Retention。
- [x] Phase 0B：错误证据与分类、Reliability/Capacity 分离、Multi-Key 安全降级、提交边界与流式计费闭环。
- [x] Phase 0C：真实 Output Token、独立 attempt latency/generation duration 与 TTFT 接线；渠道级 TPS 改为真实 Token/s；重试 attempt 遥测隔离。
- [x] Phase 0D：统一/收敛 `routing_metrics` 与 `perf_metrics` 生命周期语义；修复设置竞态、无界桶、永久 Worker、退避/Jitter/可观测边界；完成 Gate 1 总审计。
- [x] Phase 1 Observe：Pool、Member、稳定 Credential ID、统一有界遥测、兼容迁移、只读 API、新选择器仅审计。
- [x] Phase 2 Shadow：双算、差异审计、决策重放、Revision/Outbox/Redis Stream、增量聚合、成本快照、多实例降级。
- [x] Phase 3 Canary：确定性灰度、请求级固定会话、自动回滚、容量预留、慢启动、故障注入；Hedging 保持关闭。
- [x] Phase 4 Balanced：硬约束、绝对 SLO、Weighted P2C、探索、亲和保护、主动探测、首字前切换、策略治理、幂等 Operation、兼容入口和 SSE 已完成。
- [x] Phase 5 Enterprise SLO：严格容量租约、共享份额、区域作用域、独立 RBAC、双人审批、Error Budget、持久审计导出、预算 Hedging、多节点失效语义与成本审计已完成。
- [x] Gate 7 前端：七个深链页面、七语 i18n、SSE/轮询降级、A11y、320/768/1440、200% 缩放、明暗主题、深链、权限与错误恢复已验收并合入。
- [x] 发布终审硬化：Task/MJ durable async billing 的 reservation、send-authorized、accepted handoff、terminal settlement、恢复、manual review、滚动升级门禁和保留期已完成并集成。
- [x] 计费投影：accepted/terminal 统计、外部日志、缓存同步各自独立幂等与故障恢复已完成，三数据库并发/崩溃点证据已补齐。
- [x] **历史交付记录：** Cost Sources 前端曾完成集成与验证；当前无版本退场方案已删除该正式前端，并以渠道倍率与生效成本取代。
- [x] 最终实现验证：当前实现候选已完成全量 Go、SQLite/MySQL 5.7/PostgreSQL 9.6、Redis/ClickHouse 高风险定向契约、定向 race、vet/build、故障矩阵、benchmark、前端和仓库审计；候选镜像真实旧卷升级也已作为独立发布门禁通过。
- [x] Git/发布：PR #20、#21、#22 已合入 main；`v0.1.11` 与 `latest` 多架构镜像、Cosign、SBOM、provenance 和 10 项 Release 资产已完成正式核验。
- [ ] **历史未完成项，现已取消：** v0.1.12 曾计划继续完善 NewAPI/Sub2API 上游账号连接器；当前退场方案不再继续该实现，验收改为确认连接器不可调用、管理凭据已清除且普通渠道余额仍正常。
- [x] v0.1.12 请求计费完整性：NewAPI 未公开工具/search-preview/固定图片附加费，以及 Sub2API display pricing 的 flat priority/flex、1h cache write、OpenAI/Gemini 长上下文和远程 interval 上下文均在请求估算阶段条件降为 unknown。
- [x] v0.1.12 Claude Code 数据面边界：按 Sub2API 官方指纹生成 `standard`/`unknown`/`claude_code`，V1/V2 均携带分类；所有智能、传统、指定与最终 Setup 选路出口在抽样前排除非 Claude Code 流量，策略缓存独立且失败关闭。
- [ ] v0.1.12 最终门禁：等待最新工作树完成相关契约与 Race、`./controller`、全仓测试、Vet/Build、前端矩阵和仓库审计；随后按用户授权提交、同步 main、创建并合入 PR、发布 `v0.1.12`。

## Requirement Traceability Matrix

| ID | Requirement | Current evidence | Status / next evidence |
| --- | --- | --- | --- |
| CR-0.1 | 关闭态不分配，所有缓存/Map/锁有 TTL、容量、统计 | Phase 0A 计划、`pkg/routing_*`、Sub2API JWT 测试 | PASS |
| CR-0.2 | 设置并发安全，Worker 可取消、可等待，Retention 生效 | Gate 1 提交 `bcb6602b`、Runtime/Race/Retention 测试 | PASS |
| CR-0.3 | 错误责任/作用域/重试/健康/容量分类正确 | Phase 0B 计划、`pkg/routing_error`、Controller/Task tests | PASS |
| CR-0.4 | 429/529 与 Reliability Breaker 分离 | `pkg/routing_hotcache/capacity.go`、Phase 0B tests | PASS |
| CR-0.5 | Multi-Key 不写聚合状态，稳定身份上线前安全降级 | Phase 0B tests | PASS；Phase 1 用 Credential ID 替代降级 |
| CR-0.6 | 真实 Output Token、TTFT、Token/s | `8f43ee5a..d7b3b3e2`；结算边界同步 usage、attempt end、routing bucket、Hot Cache、Selector 与 stream hint 测试 | PASS |
| CR-0.7 | Cost Connector 与 Serving health 分离 | Phase 0B Task 9 tests | PASS |
| CR-1 | Pool/Member/Credential/Observe/三库迁移/只读 API | Phase 1 模型、stable rollup、不可变快照、审计、v2 API、三库与 Race 证据 | PASS（Gate 2） |
| CR-2 | Shadow、Revision、Outbox、Redis Stream、重放、正确可合并分布 | Gate 3 实现、三库/Redis/Race/Vet/Build 与独立 P0/P1 审查 | PASS |
| CR-3 | Canary、确定性灰度、自动回滚、容量预留、慢启动 | Canary cohort/outcome/evaluator、Pool-scoped rollback、故障矩阵、跨节点 presence/checkpoint | PASS |
| CR-4 | Balanced 选择器、主动探测、首字前切换、策略发布/回滚 | 真实 Balanced、Replay、Probe、Attempt Coordinator、草稿仿真/发布/回滚 Operation、SSE 与兼容入口 | PASS |
| CR-5 | Enterprise SLO、严格租约、多区域、RBAC/双人审批、Burn、Hedging | strict/local/adaptive capacity、region scope、approval/authz、error budget、audit export、真实预算 hedge | PASS |
| CR-FE | 渠道路由工作区、七语、SSE/A11y/响应式/视觉 | production release-final：投影 18/18、Operations 12/12 响应式明暗矩阵，边界态 3+4，Axe 0；RU/FR 320px Cursor 分页 8/8 无越界且 `<html lang>` 正确 | PASS |
| CR-COMPAT | **历史兼容要求，已被直接切换取代** | 当前仅保留前端 `/smart-routing` 书签重定向；`/api/smart-routing/*` 与 `/api/channel-routing/v2/*` 均不注册 | ARCHIVED |
| CR-SEC | SSRF/DNS rebinding/重定向/TLS/大小/脱敏/凭证轮换 | 受保护 fetch、Probe/Cost 出站约束、错误脱敏、RBAC fail-closed、审计 admin-only 信息和凭证 fencing | PASS |
| CR-BILL | 用户只结算一次；逐 attempt 平台成本审计；未知价格非零 | 同步与异步链均已覆盖；Task/MJ durable reservation、终态结算、恢复、manual review 和 accepted/terminal 独立投影已集成并通过跨库验证 | PASS |
| CR-UPSTREAM-0.1.12 | **历史连接器要求，已取消** | 当前验收为连接器退役、凭据清除、账号健康不再参与运行时、普通渠道余额保持可用 | ARCHIVED |
| CR-GIT | PR、同步 main、合入、镜像构建 | PR #20/#21/#22、tag `v0.1.11`、Release、GHCR、Cosign、SBOM、SLSA 与最终 verifier 均完成 | PASS |

## Plan of Work

1. [完成] Phase 0C：结算层真实 usage 与 attempt end、retry reset、渠道 bucket、真实 Token/s、流式 TTFT 评分及旧选择点 stream hint。
2. [完成] Phase 0D / Gate 1：有界状态、正确分类、容量分离、真实 TTFT/Token/s、Worker 生命周期、安全与最终 flush。
3. [完成] Phase 1 / Gate 2：稳定身份、stable telemetry + rollup、不可变快照、Observe 审计、只读 v2 API、三库与并发验证。
4. [完成] Phase 2 / Gate 3：可合并分布、Shadow 双算/重放、Revision/Activation/Outbox、Redis Stream、node sequence 幂等与增量聚合。
5. [完成] Phase 3 / Gate 4：Canary 真实门控、固定会话、容量/慢启动、结果窗口、原子多 Pool 自动回滚和故障注入。
6. [完成] Phase 4 / Gate 5 与 Phase 5 / Gate 6：Balanced、严格租约、区域、RBAC/审批、Burn、审计导出、预算 Hedge、SSE、兼容入口和完整 attempt timeline 已集成。
7. [完成] Gate 7 前端：七页正式工作区、七语、单连接 SSE 与轮询降级、浏览器/A11y/视觉验收已完成并合入。
8. [完成] 发布终审实现：durable async billing、独立投影、恢复/manual review、Cost Sources、供应链门禁和 SQLite `v0.1.10` 日志表兼容迁移已集成。
9. [完成] 最终实现验证：后端全量与跨库矩阵、前端 production release-final、性能、安全、格式和仓库审计已完成。
10. [完成] 候选镜像与真实升级：基于 `6ca75bda` 构建候选镜像，用全新隔离卷从固定 digest 的正式 `v0.1.10` 初始化；升级、重启幂等、marker/旧日志、新 schema、索引行为、版本、默认前端与日志健康均通过。
11. [完成] Git 与正式发布：PR #20/#21/#22 已合入；`v0.1.11` tag 精确指向 PR #21 merge SHA，Release/GHCR/Cosign/SBOM/SLSA 已由正式 verifier 终验。
12. [已取消/归档] v0.1.12 官方连接器核心实现；当前不得继续读取或调用 NewAPI/Sub2API 管理端点，替代方案见顶部退场结论。
13. [完成] v0.1.12 请求计费与账号连续性：NewAPI 未公开附加费、Sub2API display-only 缺失维度、远程上下文、`/auth/me` 稳定身份、JWT 轮换和匿名 catalog scope 提示已收口。
14. [进行中] v0.1.12 Claude Code 与最终验证：官方请求分类和全选路出口准入已实现；串行完成后端/前端/仓库门禁后，按用户授权提交分支、同步最新 main、创建并合入 PR，再发布和终验 `v0.1.12`。

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
- 当时 MySQL/PostgreSQL DSN 尚未配置并发生 SKIP；后续最终矩阵已使用隔离的 MySQL 5.7.44、PostgreSQL 9.6.24 环境补齐，无遗留阻塞。

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

### Release completion evidence（candidate and remote complete）

- 代码候选：`6ca75bda2695bef3116c8c138f54356200862d10`。后端全量 `go test ./... -count=1`、`go vet ./...`、`go build ./...` PASS；Go test 日志 SHA-256 为 `af34581f10cd646292fa057e2e24217e20b8513e3ecfdc651677d944f1cdf884`。前端 `bun test` 为 176 pass、0 fail、48 files，Typecheck、Default/Classic production build 和目标文件 format/lint 均 PASS。
- 前端终验：RU/FR 320px 在 Manual Reviews 与三类 Projection Cursor 页面共 8/8 PASS；document/body 宽度均为 320，按钮无越界，`<html lang>` 与存储语言均精确为 `ru`/`fr`。浏览器报告 SHA-256 为 `16f2f30f7dcef116a028be621ea1e8ca935d1654be299ac85cf1f45cdd248148`。
- 候选镜像：`xy-api:v0.1.11-candidate-6ca75bda`，本地 image ID 为 `sha256:49e205864cd5b7ac4faa19d8f87026b3a477cdd088b8eb16c9cb4bd19f893818`；OCI labels 精确绑定 version `v0.1.11`、revision `6ca75bda2695bef3116c8c138f54356200862d10` 和受保护仓库 source，容器 `--version` 为 `v0.1.11`。构建日志 SHA-256 为 `31d713dcac4e2b8dd4890e1be44e497e4b710360610929f94b52d8c923505699`。
- 真实升级：固定正式旧镜像 `ghcr.io/liulixin-lex/xy-api@sha256:40b1650c134ec9fe7afad833f2c3b635bf0818ca534e5d12e4ee0f429a80b12d` 初始化全新 SQLite 卷；候选首次启动和重启均通过。marker、旧日志、默认前端、核心表/列、两个唯一索引的结构与实际 NULL/重复行为、前端 build descriptor 和精确迁移日志门禁全部 PASS，容器与卷清理 PASS。升级日志 SHA-256 为 `be67bc480e5e94f90e2bece8b8671093366f429a866cb345a281dd2df741d8e5`，保留阶段证据聚合 SHA-256 为 `f72a37b6577ef1c0d91cbf3cc28ebdf66fa95569014c85bafdde6bab0739658f`。
- Git（complete）：PR #20 合入 `a6bb1ff4`，PR #21 合入 `9a89fdf1` 并成为 `v0.1.11` tag 目标，PR #22 合入 `50549628` 修复 GHCR release auth；当前 main 与 origin/main 均为 `505496280b019d48589b981c9be47dd8fc89c713`。
- Release（complete）：GitHub Release 为非 draft、非 prerelease，严格包含 10 个资产；checksum、版本、revision 与 finalizer 的 stable release complete 门禁均通过。
- GHCR（complete）：`v0.1.11` 与 `latest` 同指向 `sha256:b338fcb52844681c2b03d491a7ef634b10dc5d1204124907e1cdc29c99471ef0`；amd64/arm64、Cosign、双平台 SPDX-2.3 SBOM、SLSA v1 provenance、OCI labels、容器版本、状态接口和前端 revision 均通过终验。
- 正式只读 verifier：脚本 `/opt/临时/verify-v0.1.11-release.sh` 的 SHA-256 为 `8f5bd170651d5c81b34b165e903f309739f71b74147546918378981e1c5a22bd`；最终日志 `/opt/临时/verify-v0.1.11-release-final-mainfix.log` 的 SHA-256 为 `24538bd38f9ec0a473c0de3e418d18c238fba45d30711e5f7e6d6ecae0d248f0`。

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

### Gate 4 / Gate 5 continuation evidence（2026-07-12）

- 提交 `6d525b3e`、`6d04ec97`、`77ee5e4b`、`6b192d37`、`ac99d822`、`569004e3`、`2855ad11`、`fa31b70f`、`bc4f8f51`、`ee50b7b4`、`39049b54`、`fa745fe2` 已覆盖 Canary catch-up/失败语义、Balanced 预计算与真实选路、完整决策审计/Replay、多 Pool 原子回滚、主动探测和兼容重试。
- Gate 5 当时工作树已实现草稿 Balanced 反事实仿真、持久化仿真/发布/回滚 Operation、Current/Revision/Operation API、强 ETag 和同事务 Revision/Activation/Outbox/Operation。
- `go test ./model ./service/channelrouting ./controller ./router -count=1` 在 Gate 6 路由权限切换前 PASS；相关定向 Race、Vet 与 `git diff --check` PASS。Gate 6 并行中间态一度仅因 Router test 仍断言旧权限而失败，不作为最终证据。
- SQLite 旧 `routing_operations` 表真实增量迁移测试 PASS；新增列保持 nullable，旧 Canary Operation 可读且 v1 idempotency hash 不变。
- 独立审查修复：仿真历史变化时旧 Operation 被错误复用；Realtime/WebSocket 握手后仍允许首字超时跨渠道重试；未知价格 Probe 低估为 0.01 美元及高成本 RelayFormat 可能被发送。修复后对应 Controller/Model/Probe 定向测试 PASS。

### Pre-release integrated evidence（2026-07-13）

- 集成 HEAD：`569ffb16`；后端主体 `0962012d`、前端 `18c3c69a` 已合并。发布前追加三项根因修复：流事件原子缓冲提交后恢复底层 `Flush`、日志轮换计数/门闩改为原子状态、主动探测 SQLite 夹具限制单连接以移除非目标锁竞争。
- `GOTOOLCHAIN=go1.26.1 go test ./... -count=1`、`go vet ./...`、`go build ./...`：exit 0。
- `go test -race ./relay/helper ./relay/channel/openai -count=1`：exit 0；真实 Redis Config/Event/Telemetry/Strict Capacity 定向 race：exit 0；真实 Redis Hedge 端到端 race：exit 0。
- SQLite 由全仓测试覆盖；全新隔离库中，MySQL 5.7 与 PostgreSQL 9.6 的 `./model`、`./service/channelrouting` 全包契约在 `-p 1` 下 PASS，覆盖审批/RBAC、操作租约、Replay/Audit、成本、Rollup、Error Budget、Probe、Breaker reset 和迁移；测试库已删除并复核无残留。
- Redis 7：独立测试实例上的 Config Stream、三节点 Event、Telemetry 幂等重投/backlog、Redis block lease/revision fencing 全包 PASS；另一独立实例上的真实 Hedge“secondary wins + 单次结算 + 两条 attempt 审计”PASS。
- 性能：4096 候选动态 Prepared Balanced selector 100000 次、3 轮 p99 为 `525684–568314 ns`；Adaptive Concurrency 为 `6204–8266 ns/op`；Redis block local lease 为 `27219–50145 ns/op`。
- 前端：Bun 单测 21/21、Playwright 14/14、Typecheck、Lint 0 error、七语 i18n missing/extras/untranslated 全 0、Build PASS；Axe、键盘、200% zoom、320/768/1440、明暗主题、RBAC、SSE、幂等、审批/回滚、导出及离线恢复均 PASS。
- 独立审查未发现当时的 P0；所有确认 P1 已修复并完成相关回归。`git diff --check` PASS，未跟踪临时文件为 0。
- 根线复跑：Classic `bun run build` PASS；`GOTOOLCHAIN=go1.26.1 go test ./... -count=1`、`go vet ./...`、`go build ./...` PASS；`git diff --check` PASS。该证据随后由 2026-07-14 最终发布候选矩阵取代。

### v0.1.11 final release-candidate evidence（2026-07-14）

- 当前代码候选及本地候选镜像的源码基线为 `6ca75bda`。`c2de5c5d` 完成 durable channel routing operations，`ea6511af` 合并计费控制台，`c04261ec` 完成发布收口；`100423fc`、`276b79c7`、`5c7f91e7`、`e86f24c4` 依次收口 SQLite 升级、操作反馈、无障碍与测试依赖分类，`2a121fcb`/`4c4acfc3` 收口 Operation 逻辑时钟、迁移修复和 Breaker fencing，`6ca75bda` 关闭多语言移动分页与文档语言无障碍缺口。
- Task/MJ durable async billing 已覆盖 reservation、send-authorized、accepted handoff、terminal settlement、恢复、manual review、客户端幂等、滚动升级协议门禁与保留期；Stateful Task/MJ 固定原渠道和稳定 Credential ID，历史缺失身份时 fail closed。
- accepted/terminal usage、统计、SQL/ClickHouse 外部日志和缓存同步采用独立持久幂等阶段；receipt、冲突隔离、恢复重放、周期审计和 DB lease 已完成三数据库故障/崩溃点验证。
- Cost Sources、Manual Reviews 和 Projection Operations 已完成前后端集成；provider 凭证隔离、切换清理、CA readiness、SSRF/TLS/重定向/私网/响应边界、ETag 冲突、双阶段人工确认和权限降级均有回归证据。
- 真实 `v0.1.10` SQLite 卷首次升级候选暴露 P0：GORM 尝试执行 `ALTER TABLE logs ADD billing_operation_key varchar(191) UNIQUE`，而 SQLite 禁止通过 `ADD COLUMN` 增加 UNIQUE 列。`100423fc` 改为先用无 unique tag 的兼容迁移模型增加 nullable 列，再由正式 `Log` AutoMigrate 创建命名唯一索引，并同时覆盖 `migrateDB`、`migrateDBFast` 和 `migrateLOGDB`。
- SQLite 修复回归覆盖旧日志保留、重启幂等、多个 NULL operation key 可并存、重复非 NULL key 被拒；相关 SQLite `-count=3`、MySQL 5.7 和 PostgreSQL 9.6 迁移契约均 PASS。
- 当前候选后端 `GOTOOLCHAIN=go1.26.1 go test ./... -count=1`、`go vet ./...`、`go build ./...` 均 PASS；SQLite 回归与 MySQL 5.7.44、PostgreSQL 9.6.24、Redis 7.4.9、ClickHouse 24.8.14.39 的高风险定向外部契约均在最终后端基线 `4c4acfc3` 上新鲜 PASS，SKIP 0、FAIL 0；`6ca75bda` 仅修改前端。过宽全域 race 曾因 SQLite migration 超过 10 分钟终止，未发现 race detector 报告，也不作为成功证据。
- 前端 Typecheck、Lint 0 error、`bun test`（176 pass、0 fail、48 files）、changed-scope oxfmt、七语 i18n missing/extras/untranslated 全 0 和 `VITE_REACT_APP_VERSION=v0.1.11 bun run build` 均 PASS。
- production release-final 浏览器证据覆盖投影页 18/18、Operations 12/12 的 320/768/1440 明暗矩阵和边界态 3+4；所有布局无文本/横向溢出，Axe 0。额外 RU/FR 320px 8/8 回归确认共享 Cursor 分页无越界、页面宽度稳定且 `<html lang>` 正确；键盘、200% 等效缩放、reduced-motion 和权限状态均通过。
- 发布供应链实现已集成：Release/Electron 资产不可覆盖、跨工作流串行上传、旧版本不得倒退 `latest`、同版本不同 digest fail closed、子架构及最终 manifest 签名；Action SHA、actionlint、yamllint、shellcheck 和脚本测试均 PASS。
- 所有早期 `e86f24c4`/`4c4acfc3` 候选证据均已被 `6ca75bda` 的干净代码候选、镜像与真实升级证据取代；随后 PR #21 完成 main 合入与 `v0.1.11` tag，PR #22 完成正式镜像发布认证修复，远端发布闭环已由上述 final verifier 证明。

### v0.1.12 upstream account connector audit / acceptance checkpoint（2026-07-15）

本节只记录 `fix/channel-routing-v0.1.12` 的发布后审计修复，不替换、不追溯修改上方 `v0.1.11` 的正式发布证据。

- 官方审计基线固定为 NewAPI `a63364d156cf2a64f1c3d1ee4923d73d5f3222a1` 与 Sub2API `30df3f68e4ea6ce493af1d6651d3f668201c14dd`；Sub2API 自初始审计点 `da85cc7e47882090b115d664afe8e39b37aa7417` 起账号/分组/余额契约未漂移，最新 Alpha Search 变更只调整失败转移状态。详细长期规范见 `.agent/channel-routing-upstream-account-connectors.md`。
- 当前模块不是空白实现：已有 provider-scoped 加密凭据、NewAPI Access Token + User ID、Sub2API JWT/邮箱密码登录、Redis JWT cache/singleflight/fencing、成本同步、余额、分组发现、Test、SSRF/TLS/CA/大小限制和正式前端表单。
- NewAPI 权威链改为：`GET /api/user/self` 读取余额，`GET /api/user/self/groups` 决定账号分组和实际倍率，`GET /api/user/models?group=` 决定该组账号模型，`GET /api/pricing` 只提供价格目录，Gateway API Key 的 `GET /v1/models` 只验证实际可服务模型。最终快照取四方交集。默认公开 pricing 中间件只读取 Dashboard session，不会把 Bearer Access Token 变成账号视图，因此其 `group_ratio`、`usable_group` 和 `enable_groups` 一律不能用于账号授权判断。Gateway `/v1/models` 不公开 token group，只能证明 Key 可服务模型，不能自动证明其 group 与 binding `upstream_group` 一致。
- NewAPI 每个实例的 `GET /api/status.data.quota_per_unit` 是该上游额度换算权威；余额使用 `quota / upstreamQPU`，token 美元价和本地 `BaseRatio` 使用 `upstreamQPU/localQPU` 显式换算。Groups discovery 不依赖 status，Test/同步缺失或非法 QPU 失败关闭。
- NewAPI HTTP 200 `success=false` 以 `Auth-Version` 作为 UserAuth 是否已通过的证据：认证后的业务错误不误标 token 失败，模型业务错误按组隔离，余额业务错误降为 partial。
- NewAPI `ratio=0` 是官方免费组，成本引擎保留显式零倍率并把 token/per-request/expression 结果视为已知零成本；`auto` 分组返回字符串倍率且没有稳定具体倍率，必须失败关闭。缺组、空模型、缺目录价格和非法倍率均不得回退 `1x`。
- NewAPI ratio 模式按官方补齐 cache read、5m/1h cache write 和 audio input/output；请求画像拆分 cache read/write known，结构化识别 `cache_control` 与 `prompt_cache_*`，避免默认 cache-write 价格让普通请求全部 unknown，也不把自动 cache read 伪装成已知。
- NewAPI pricing 模块关闭的 403 已按配置/能力错误处理，不再误标 Access Token；Base URL 现拒绝任意 query、空 query marker 和 fragment，并为七语前端提供明确错误。
- NewAPI 同账号聚合按 Access Token + 自定义 CA + 出站策略分批完整取数，不同管理令牌不再由排序最前的 representative 代表；Gateway `/v1/models` 则按 Gateway Key + CA + 出站策略独立去重。轮换中的失效令牌或坏 Gateway Key 只退避对应 binding，至少一个健康凭据时继续同步共享账号；单个坏组同样只隔离对应 binding。缺 User ID 不再回退 token 派生账号。
- Sub2API 正式契约固定为 JWT `/api/v1/auth/me`、`/groups/available`、`/groups/rates`、`/channels/available`；数字分组 ID、嵌套 `platforms[].groups[]/supported_models[].pricing`、USD/token、USD/request、左开右闭 token interval 和未命中 unknown 均按官方实现处理。旧扁平 shape、JWT `/v1/usage`、空渠道零价格和未知 image/per-request tier 均失败关闭。
- 同账号 Sub2API 网络响应允许共享取数，但元数据一致性校验、目标渠道裁剪和成本展开必须按 binding 分组执行；可识别的坏分组只隔离引用它的 binding，健康分组继续更新。只有 envelope/顶层类型或无法归属到具体分组的结构损坏才允许账号级拒绝。
- Sub2API `/auth/me` 先按 canonical Base URL + 管理凭据 + CA + egress 等价类聚合并复用 profile；目录再按目标分组分批，同组同等价类共享请求，不同组隔离。托管 JWT 重登重试会重新确认 profile。选定分组的倍率 value 单独验证，未选分组的坏 value 不再拖垮健康组；数字名称与另一分组 ID 的 alias 冲突只在真正命中该输入时判 ambiguous，无法归属的非法 map key 仍失败关闭。
- 官方确认 `/channels/available` 是展示目录而非完整 BillingService 契约；现已用 display contract 元数据按请求门控 flat priority/flex、1h cache write、OpenAI/Gemini 长上下文和远程 interval 上下文，NewAPI 未公开工具/search-preview/图片附加费也通过 catalog scope 条件 unknown。历史来源快照缺少对应 contract metadata 时，主选路和 shadow replay 均失败关闭。
- Sub2API `web_search_price_per_call` 缺失时官方默认 `$0.01/call`、显式 `0` 免费，并乘分组倍率；该字段只属于实际 Alpha Search 调用，不能写成所有模型请求共享的固定 `PerRequestCost`。`/v1/alpha/search`、`/alpha/search`、`/backend-api/codex/alpha/search` 及合法基础路径前缀现统一进入未编目附加费门控并返回 unknown，尾斜杠命中且相似路径不误判。
- Sub2API `/auth/me` 的官方正整数用户 ID 已成为同步和所有预览动作的前置身份；canonical Base URL + user ID 在账号分组前合并不同 JWT，认证/非法 profile 失败不创建凭据派生账号。binding 保存官方确认后的不可逆 account-key hash，配置变更清空；因此无历史快照的 backoff 也能精确回连账号。身份已确认后的 groups/pricing 失败与账号 degraded 状态同事务提交；健康 sibling 的快照/账号状态同时 fence 全部当前 failure/backoff，不能出现先 active 后降级的窗口。NewAPI 未认证失败必须由自身或仍当前的已认证 sibling 提供最小确认 fence，stale 确认不能间接建账/降级。旧 token/email 派生历史不做破坏性自动合并。
- Sub2API 官方 JWT 认证失败使用 401；合法 JWT 在 backend mode 下由用户端点 guard 返回 403。后者必须作为 capability/configuration 错误保留旧快照，不得清 JWT cache、触发托管登录或提示更换凭据。
- Sub2API `claude_code_only` 与 `subscription_type` 只在 `/groups/available` 暴露，Groups 动作通过 `group_meta` 保留两者，正式同步按绑定组关联。订阅组使用订阅限额而非账号钱包判定 serving eligibility，因此定时同步保留账号级钱包信息，但清理该渠道的 serving balance 与热缓存，前端必须对管理员解释该语义。快照重建时，已启用成本连接器的 subscription/unknown balance 都禁止回退 legacy `Channel.Balance`，只有 disabled/no-binding 旧渠道保留兼容。`claude_code_only` binding 未声明用途时仍拒绝成本同步；数据面按官方 User-Agent、system/billing block、必需 headers、metadata 及 count-tokens/haiku 探测规则分类，`standard`/`unknown`/`legacy` 在所有选路出口排除专用渠道。
- Sub2API Groups 动作只依赖 `/auth/me` 和 `/groups/available`，不要求专属倍率或可选的 `/channels/available` 已可读取；Test/定时同步继续要求 `/groups/rates` 与完整价格。成功同步会原子 reconcile 当前 channel 的 latest 与成本热缓存，删除权威集合中消失的模型并保留 immutable versions；失败路径保留旧集合。
- Test 与加载分组统一执行 action readiness；Test 要求绑定组并只校验该目标组，加载分组允许空组并走账号级 discovery。Sub2API 两类动作都必须实际解析 `/auth/me` 并验证正整数官方 ID，不能展示“Test 成功”但定时同步必失败，也不能让未绑定坏组拖垮健康 binding 的 Test。两类动作可以读取真实余额验证账号响应，但不写数据库余额、健康状态或热缓存；正式定时同步继续以 binding version/fencing 原子提交余额和价格。
- 阶段性后端证据曾完成相关 NewAPI/Sub2API controller/model 定向测试、Sub2API interval unknown 定向 Race、`go vet ./...` 和 `go build ./...`；阶段性前端证据完成 TypeScript、Oxlint 0 error、渠道路由 12 files / 68 tests、production build、七语 i18n 0 missing/extras/untranslated，以及 320/768/1440、明暗主题、Axe 0、console/page error 0 的浏览器矩阵。
- 上述阶段性证据之后又收口了 NewAPI 账号权威端点与按组隔离，因此最终候选必须在最新工作树上重新串行刷新。此前 `go test ./... -count=1` 约 6 分钟后被 SIGTERM 终止，另一次 `go test ./controller -count=1` 在并发审查期间触发 10 分钟 timeout，二者都不算通过；全仓 Race 没有成功证据，也不得宣称 clean。

v0.1.12 最终验收必须同时满足：

1. NewAPI 契约测试证明所有账号端点发送 Access Token + `New-Api-User`，Gateway `/v1/models` 只发送 Gateway API Key，特殊字符分组正确 URL 编码，匿名 pricing 与账号视图冲突时仍只接受账号分组/按组模型/目录价格/Gateway 可服务模型四方交集。
2. NewAPI 免费组、auto、缺组、空模型、目录缺价、目录额外模型、Gateway 不可服务模型、错误顶层倍率、同账号坏令牌轮换和好坏分组隔离均有 fail-closed/zero-cost 精确回归；不同 Access Token 与不同 Gateway Key 分别完整验证，失败 binding 不覆盖旧快照。
3. NewAPI QPU、cache 5m/1h、audio 默认/显式零、cache read/write known 拆分和 pricing-disabled 403 分类有精确回归；未公开工具/search-preview/图片附加费请求必须 unknown，匿名私有目录缺价提示 `pricing.requireAuth=true`。
4. Sub2API 显式 JWT、邮箱密码登录、401 单次重登、backend-mode 403 不清 JWT/不重登、`/auth/me` 正整数稳定身份/JWT 轮换、身份确认后降级建账、stale failure 不汇总、canonical Base URL 请求前拒绝、严格官方 wire DTO、数字 ID/名称 binding、订阅组余额不适用、enabled connector 的 legacy balance 回退禁止、正式嵌套渠道、`image_output_price` 与 nil 语义、单位换算、interval 边界/空白/gap/有界末段/未命中、冲突指纹、空能力和 `/v1/usage` 隔离均有契约回归。string ID、旧 wrapper alias、rates array/null 和扁平 shape 必须失败关闭。
5. Sub2API display pricing 请求门控覆盖 flat priority/fast、flex 透传不确定、1h cache write、OpenAI/Gemini 长上下文、远程 interval 上下文和 Alpha Search 按次费用；Claude Code 分类、V1/V2 兼容、所有选路出口与缓存失效语义有精确回归。专用 pool 是可选的纵深防御，不再是兼容性准入的发布阻塞。
6. 内联与已保存 binding 的 Test/加载分组在 `/auth/me` 返回正整数 ID 和真实 `balance` 时仍成功，并对 binding 的余额、健康状态、成本快照和余额热缓存保持只读；`/auth/me` 缺/非法 ID 时两类动作都失败。历史空组 Test 在上游请求前被拒绝，空组加载分组仍可发现全部分组并保留 `subscription_type`/`claude_code_only` 元数据。
7. Base URL query/fragment 拒绝、七语错误提示、SSRF/TLS/CA/重定向与凭据脱敏回归通过。
8. 串行运行相关 controller/model 定向测试与相关包 Race、`go test ./controller -count=1`、`go test ./... -count=1`、`go vet ./...`、`go build ./...`；只记录真实 exit 0，过宽全仓 Race 不作为本版本要求或成功证据。
9. 前端 typecheck、lint、渠道路由测试、production build、i18n 与浏览器证据保持通过；最后执行 `git diff --check`、JSON wrapper、凭据/敏感信息、临时文件和无关改动审计。

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
- Phase 0B 的完整 race 记录包含当时的 logger、task polling 和并行 `gin.SetMode` 竞态；发布终审采用高风险定向 race 并全部通过，过宽全域 race 不作为成功证据，也未据此宣称全仓 race clean。
- Gate 5 审查发现仿真 Operation 的评估哈希只包含请求参数，历史样本变化后会复用旧 Operation；现已把完整有界仿真结果哈希纳入评估身份。
- Realtime 首字前重试实现可缓存客户端消息，但批准方案把 WebSocket 握手定义为不可逆边界；最终采用更保守语义，握手后禁止跨渠道重试。
- Active Probe 的未知成本固定值会低估图片/任务类风险；最终改为未知价格 fail-closed，并在真实请求前限制为低成本 RelayFormat。
- 发布前全仓复跑发现 `streamEventBufferWriter.commit` 成功写入后没有向底层 writer Flush；这会让小 SSE 事件滞留，并使取消边界回归测试永久等待。最终在完整写入且无短写后恢复 Flush，所有流式普通测试与 race 通过。
- 流式 race 暴露日志器 `logCount/setupLogWorking` 的既有并发读写；最终使用原子计数和 CAS 门闩，异步轮换任务自行释放门闩，日志格式与轮换阈值不变。
- 主动探测 Operation 测试在全仓高负载下偶发 SQLite 共享缓存锁错误；测试不验证多连接锁竞争，因此把该专用夹具限制为单连接。真实并发契约继续由 MySQL/PostgreSQL、Redis 和专门 lease/race 用例承担。
- 真实旧卷升级发现 SQLite 不允许 `ALTER TABLE ... ADD COLUMN ... UNIQUE`；开发期全新库 AutoMigrate 不会暴露这一兼容风险。最终采用“两阶段迁移”：先增加 nullable 普通列，再由正式模型创建唯一索引，同时用旧日志保留和重启幂等回归保护三条迁移入口。
- v0.1.12 官方审计发现 NewAPI `/api/pricing` 默认走公开 `TryUserAuth`，该中间件只读取 Dashboard session，不解析连接器发送的 Bearer Access Token；因此旧实现看到的 `group_ratio`/`usable_group` 可能只是匿名目录视图，不能代表上游账号权限。
- NewAPI `/api/user/self/groups` 的 `ratio` 是 number|string：数值 `0` 是真实免费组，字符串“自动”代表动态 `auto`。把两者都当成“缺倍率并回退 1”会分别造成免费组高估和动态组伪精确成本。
- Sub2API 的正式用户渠道契约已经变为数字分组 ID 与 `channels[].platforms[].groups[]/supported_models[].pricing`；同时 `/v1/usage` 是 Gateway API Key 自身额度，不是 JWT 账号钱包。预览动作若在读取余额后立即持久化，还会让新建 binding 因 ID=0 fencing 失败并污染已保存 binding 状态。
- Sub2API `/channels/available` 明确是展示目录：flat JSON 无法区分显式渠道价与展示 fallback，也不包含 priority、1h cache write、长上下文和账号开关；把它降为 `derived` 仍会成为 known，不能作为完整性修复。
- NewAPI `/api/pricing` 同样不公开 web/file search、image-generation call 和固定图片请求倍率。价格来源置信度与当前请求收费维度完整性是两条独立轴。
- Sub2API `/auth/me` 同时提供官方用户 ID 和钱包余额；JWT 只是可轮换会话凭据。用 JWT 内容派生持久化账号会割裂余额、健康和历史，因此成功同步改为 Base URL + 官方用户 ID，失败前不创建凭据派生账号。
- Sub2API flat 缺字段会继承未公开基础价，不能猜；命中 interval 后缺字段按官方构造为零，渠道 override 缺 `image_output_price` 也是显式零。这三种 nil 语义必须分别保留。

## Decision Log

- 2026-07-11：继续使用现有专用 worktree/分支，不在 main 上实现，也不新建嵌套 worktree。
- 2026-07-11：批准方案与仓库内设计视为 brainstorming/spec 已通过，不重新请求同一设计批准。
- 2026-07-11：把上一轮“完成”解释为 Phase 0B 完成，完整 Goal 保持 active。
- 2026-07-11：先完成 Gate 1 缺口，再进入 Phase 1；正式前端仍等待 Observe/Shadow DTO/API 稳定。
- 2026-07-11：兼容保留字段名 `TPS`，但其语义在 Phase 0C 修正为真实输出 Token/s；后续 v2 DTO 使用明确 `output_tokens_per_second` 名称。
- 2026-07-11：attempt end 保存为相对 attempt start 的原子 duration，而不是绝对 Unix 时间；这样可保留单调时钟语义、支持确定性测试，并避免本地结算耗时污染上游吞吐。
- 2026-07-11：流式请求仅在 P95 TTFT 为有限正数时优先 TTFT；非流式或无效 TTFT 保持 P95 total latency 兼容行为。
- 2026-07-12：用户当时明确授权继续直至 PR/main 合入和版本镜像、`latest` 实际发布核验；发布目标随后在 2026-07-13 提升为 `v0.1.11`，完成正式镜像发布前不得结束 Goal。
- 2026-07-12：子 Agent 数量通常维持约 2 条并行线，不把 2 视为硬上限；发现派生第三条线后立即中断并由企业后端线接管共享改动。
- 2026-07-12：Gate 5 与 Gate 6 已在 Policy/Router 文件形成交叉，停止强行拆分提交，改为企业后端统一收口、根代理独立审查后原子提交。
- 2026-07-13：最终总装以全仓行为为边界；发现共享 SSE/Logger 问题时修复基础设施根因，不通过删除测试、放宽断言或串行化生产路径规避。
- 2026-07-13：发布版本提升为 `v0.1.11`；只有合入后的 tag commit 可触发正式发布，`latest` 必须单调更新并与不可变版本 tag 指向同一镜像 digest。
- 2026-07-13：Task/MJ 上游发送后的模糊结果一律保留扣款并进入 manual review；只有可证明的发送前失败或明确拒绝才能自动释放。accepted、terminal、stats、外部日志和缓存同步分别使用持久幂等阶段。
- 2026-07-13：v2 writer 必须在所有在线实例报告协议能力后启用，避免旧 poller 按 legacy 语义重复结算；历史 v1 数据继续兼容且不提前删除字段。
- 2026-07-13：三条子线允许并行，但使用明确文件所有权；根线在交接后统一审查与集成，避免通过局部修复破坏其他链路。
- 2026-07-14：SQLite 旧表新增唯一字段必须使用“普通 nullable 列 + 独立命名唯一索引”的两阶段兼容迁移；真实 `v0.1.10` 卷升级是候选镜像进入 PR 前的强制门禁。
- 2026-07-14：Operation 的资格、租约过期和 CAS 使用调用方同一次 `observedNowMs`，持久化时间以 `max(observedNowMs, created, updated)` 单调推进；创建保持宿主时钟，避免嵌套 SQLite 事务增加 read-before-write 锁升级窗口。
- 2026-07-14：早期 `e86f24c4`/`4c4acfc3` 候选已废弃；`6ca75bda` 候选以固定正式 `v0.1.10` digest 完成首次升级与重启门禁。只有 PR 合入、精确 tag 和全部正式供应链终验完成后才可结束 Goal。
- 2026-07-15：NewAPI 账号权限权威固定为 `/api/user/self/groups` 与按组 `/api/user/models`，`/api/pricing` 永远只作为价格目录；即使上游把 pricing 配为 requireAuth，也不改变这层信任边界。
- 2026-07-15：NewAPI 显式零倍率保留免费组语义，`auto` 因缺少稳定具体分组上下文失败关闭；同账号 binding 采用按组失败隔离，不能让坏组阻断健康组。
- 2026-07-15：NewAPI 同账号不同管理令牌先独立预检再聚合；旧令牌 401 只退避自身 binding，健康令牌继续同步。NewAPI/Sub2API 历史快照缺当前来源 contract metadata 时统一返回 unknown。
- 2026-07-15：Test/加载分组统一为只读预览；只有正式同步事务可以持久化余额和价格。Sub2API 管理 JWT 与 Gateway API Key 继续分栏，JWT 不探测 `/v1/usage`。
- 2026-07-15：NewAPI/Sub2API 展示目录只保存基础价格与来源契约；请求级 surcharge/service tier/cache TTL/长上下文/remote interval 完整性由估算层单独判定，不能靠 `confidence=derived` 代替 fail-closed。
- 2026-07-15：Sub2API 持久化账号优先使用 `/auth/me` 用户 ID，显式 JWT 轮换只改变认证 cache/fencing，不改变 AccountID；旧凭据派生历史不做破坏性自动合并。
- 2026-07-15：Sub2API `/auth/me` 按认证/CA/egress 等价类合批并复用到目录请求；分组 alias 冲突在 binding 选择阶段局部判歧义，Test 只验证目标组；账号恢复必须等待同账号当前 backoff 失败解除。未知/custom 请求协议不再声明计费特征完整。
- 2026-07-15：Sub2API Alpha Search 的按次费用不进入普通模型固定快照；在请求画像能无损表达搜索调用次数和重试计费前，独立 `/alpha/search` 路径统一按未编目附加费返回 unknown。

## Idempotence and Recovery

- 所有数据库验证使用临时/隔离库；不连接生产，不执行破坏性迁移。
- 当前专用 worktree 已注册在 Git common dir；恢复时先检查 worktree/branch/HEAD，不重新创建。
- 若测试失败，先分类为新增、既有或环境问题；保存最小复现并只修当前切片。
- 不使用 reset、checkout、stash、rebase、amend 或强推恢复。
- 子代理只处理有界任务；根代理复核 diff 与测试。并行写必须使用独立 worktree和无重叠文件。

## Outcomes & Retrospective

Phase 0–5 控制面/数据面、正式前端、Task/MJ durable async billing、独立投影、Cost Sources、人工复核、供应链门禁、SQLite `v0.1.10` 兼容修复、候选镜像和真实旧卷双启动升级均已完成并通过最终实现矩阵。PR #20/#21/#22、`v0.1.11` tag、10 项 Release 资产以及 GHCR `v0.1.11`/`latest` 的 amd64/arm64、digest、Cosign、SBOM、provenance、容器版本均已正式核验；本轮在 `fix/channel-routing-v0.1.12` 上继续处理发布后审计发现，不追溯改写上述发布结论。

**历史结果说明：** v0.1.12 当时曾把 NewAPI/Sub2API 上游账号连接器的官方信任边界写入实现与长期规范。该结果现已被无版本退场方案覆盖：连接器及自动成本同步不得恢复，当前剩余发布边界必须按 `docs/channel/channel-routing-operations.md` 和退场方案第 13/14 节重新验证，不能沿用本文的旧发布结论。
