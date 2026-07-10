# 渠道路由 2.0 设计说明

## 文档地位

本文将用户已批准的 `/opt/临时/渠道路由-企业级重构最终方案.md` 固化为仓库内的实施设计。若本文与该最终方案存在冲突，以最终方案为准，并在实施前修正本文和对应实施计划。

本轮目标不是继续扩展“智能路由 Beta”，而是完成“版本化控制平面 + 进程内确定性数据平面 + 分组成员模型 + 分层健康状态 + 分布式容量协调”的渠道路由 2.0。实施按 Phase 0 至 Phase 5 分阶段完成，每个阶段都必须能独立验证、回滚，并为下一阶段提供稳定契约。

本设计只授权修改仓库代码、测试和文档，以及在本地隔离工作树中执行验证。生产服务器部署、生产数据库迁移和真实流量灰度不在本轮隐含授权内；代码必须提供这些操作所需的安全机制和运维接口，但实际生产执行需要单独授权。

## 方案选择

已评估三种方案：

1. 在现有单体路由上继续修补：短期成本低，但无法解决配置版本、状态作用域、多节点一致性和热路径数据库依赖问题。
2. 版本化控制平面 + 本地数据平面：数据库保存真源，Redis 负责协调和可靠通知，节点原子加载不可变快照，请求在进程内确定性选路。
3. 独立中央路由服务：长期可支持超大规模，但会给每次推理请求增加同步网络跳数和新的中心故障域。

采用方案 2。控制面初期仍运行在当前 Go 进程中，通过租约避免多节点重复任务；数据面不得依赖每请求数据库查询、LLM 判断或中央路由 RPC。Redis 不能成为普通选路的同步单点，仅严格容量池允许按策略执行原子预留。

## 分阶段交付边界

该重构包含多个相互依赖但可独立验收的子项目，按以下顺序推进：

### Phase 0：正确性止血

- 功能关闭时，指标、Breaker、JWT 锁和其他高基数状态仍有 TTL、容量上限和清理路径。
- 设置读取改为锁保护或不可变原子快照。
- 建立统一错误责任分类器；用户错误、网关错误、配置错误、凭据错误、容量错误、上游错误和网络错误分别处理。
- 429 从可靠性 Breaker 移入容量控制，不再打开渠道聚合 Breaker。
- Multi-Key 不再同时写真实 Key 与 `-1` 聚合状态。
- 接入真实 Output Token、generation duration 和 TTFT。
- Retention 真正删除过期数据。
- 后台组件使用 `Start(ctx)` / `Close()`、Ticker、有界队列、退避和 Jitter。
- 定价连接器状态与 Serving Credential / Endpoint 健康分离。

Phase 0 完成前，新的主动选路不得扩大流量占比。

### Phase 1：Observe

- 建立 `RoutingPool`、`RoutingMember`、稳定 `CredentialID` 和对应迁移回填。
- 建立统一遥测输入、聚合和只读快照。
- 新选择器只计算决策，不改变真实选路。
- 提供渠道路由 v2 的只读总览、分组、渠道健康、成本和决策审计 API。
- 完成 SQLite、MySQL 5.7.8+、PostgreSQL 9.6+ 的迁移契约测试。

### Phase 2：Shadow

- 新旧选择器同时计算并记录差异。
- 引入不可变 `PoolSnapshot`、单调 Revision、Transactional Outbox、Redis Stream 通知和周期性版本对账。
- 节点完整拉取、校验、预编译后通过 `atomic.Pointer` 一次切换快照。
- 遥测以 `(node_id, sequence)` 批量增量上报并去重。
- 决策可由快照、策略版本和随机种子重放。

### Phase 3：Canary

- 按 RoutingPool 与 Request ID 做确定性 1%–5% 灰度。
- 启用新的错误分类、容量预留、恢复慢启动和请求内排除已失败目标。
- 根据成功率、TTFT、平台成本和重试放大率执行自动回滚。
- Hedging 保持关闭。

### Phase 4：Balanced

- 全量启用硬约束过滤、SLO 保护带、Weighted P2C 和有界探索。
- 启用主动探测和首字前透明故障转移。
- 上线策略草稿、校验、仿真、审批、发布和回滚。
- 新入口、API 和文案正式使用“渠道路由”，保留旧接口兼容。

### Phase 5：Enterprise SLO

- 引入严格容量租约、共享账号公平份额和多区域状态。
- 引入独立权限、双人审批、多窗口 Error Budget Burn 告警和审计导出。
- 只对高价值、首字前、跨故障域且有额外预算的请求开放一个 Hedging 备用尝试。
- Agent 仅异步提出策略建议，默认不能自动发布。

## 领域模型与状态作用域

核心实体如下：

- `PhysicalChannel`：现有物理渠道、端点、供应商、账号、余额和协议能力。
- `RoutingPool`：与本地 Group 一一对应的路由池。
- `RoutingMember`：物理渠道在某个 RoutingPool 中的成员身份。
- `RoutingPolicyRevision`：不可变策略版本。
- `RoutingPolicyActivation`：策略在 observe、shadow、canary、active 阶段的激活记录。
- `RoutingUpstreamAccount`：真实共享余额与容量的上游账号。
- `RoutingCredentialRef`：稳定凭据身份；Multi-Key 重排或轮换不得改变历史状态归属。
- `RoutingCostSnapshot`：带来源、版本、置信度和有效期的价格快照。
- `RoutingRuntimeCheckpoint`：Breaker、容量和节点版本的冷启动检查点。
- `RoutingDecisionAudit`：白名单式决策解释和尝试时间线。
- `RoutingOperation`：同步、探测、发布、回滚等持久化异步操作。
- `RoutingConfigOutbox`：与策略发布事务一致的事件输出。

同一物理渠道 A 在 `gpt-plus` 和 `gpt-pro` 中必须形成两个独立成员。成员权重、业务 Tier、SLO 覆盖、探索比例、流量上下限、分组内指标、软熔断和决策历史互不覆盖。

以下状态按客观作用域共享：

- 管理员禁用：PhysicalChannel。
- 余额耗尽：UpstreamAccount。
- Serving Key 401/403：Credential。
- DNS/TLS/连接故障：Endpoint × Region。
- 模型或参数不兼容：PoolMember × Model/Capability。
- 分组内性能下降：PoolMember × Model。
- 已尝试目标：Request。

共享资源不因分组隔离而被伪造为独立资源。若多个分组共用同一账号或 Credential，其 RPM、TPM、并发和余额由容量控制器按保底份额与最大份额公平分配。

## 请求数据面

选路发生在协议级请求解析和基础验证之后、渠道特定映射和上游调用之前：

```text
认证 / 本地分组
  → 完整协议解析与校验
  → RequestProfile（能力、Token、媒体、预算、Deadline、区域、幂等性）
  → RouteEngine.Plan
  → 硬约束与健康过滤
  → 容量准入与请求级成本计算
  → SLO 效用评分与保护带
  → Weighted P2C + 有界探索
  → 容量预留与 AttemptPlan
  → SetupContextForSelectedChannel
  → AttemptCoordinator
  → Billing Settlement
  → 统一遥测与审计
```

普通热路径必须满足：

- 不访问数据库。
- 不调用 LLM 或 Agent。
- 不依赖中央路由 RPC。
- 只读取不可变本地快照和有界本机运行状态。
- 随机种子由 Request ID 与策略版本确定，保证重放一致。

## 评分与选择

评分使用绝对 SLO 曲线，不使用候选间 Min-Max：

```text
U(c) = confidence × freshness × slow_start_factor × ∏ utility_i(c)^weight_i
```

指标要求：

- 可用性只统计上游责任错误，使用 30 秒、5 分钟和 1 小时窗口，并采用 Beta 后验下界或 Wilson Lower Bound。
- 流式请求主要使用 TTFT p50/p95/p99；非流式请求使用 headers 与完整响应延迟。
- Token 速度定义为 `output_tokens / generation_seconds`，并标记上游 Usage 或本地估算及其置信度。
- 成本按当前 RequestProfile 计算 Expected、Worst-case 和 Effective Cost；表达式或阶梯计费必须复用 `pkg/billingexpr`。
- 负载综合本地/全局 Inflight、并发、RPM、输入/输出 TPM、预计排队和上游 Remaining/Reset/Retry-After。

选择顺序：

1. 分组、模型、协议和能力硬过滤。
2. PhysicalChannel、Account、Credential、Endpoint、Capability 和 Member 健康过滤。
3. RPM、TPM、并发和预算准入。
4. 形成满足 SLO 的最佳分保护带。
5. 按成员目标份额抽取两个候选。
6. 使用负载调整后的 Power of Two Choices 选出目标。
7. 仅在错误、成本和容量预算允许时保留 1%–3% 探索。

Priority 只有在管理员显式配置业务 Tier 级联时才是硬层级；静态权重只表达目标份额，不能覆盖健康评分。亲和目标必须仍在最佳分保护带内。

## 错误、健康、容量和提交边界

统一分类器返回：

```text
responsibility: caller | gateway | config | credential | capacity | provider | network
scope: request | model | credential | endpoint | pool_member | channel
retryability: never | before_commit | idempotency_required
health_effect: ignore | degrade | open
capacity_effect: none | reduce | cooldown
```

可靠性、容量、凭据、能力、成本连接器和人工状态必须分离：

- 用户 400/413、用户额度不足和本平台异常不降低渠道健康。
- 内容安全拒绝默认不跨渠道重试。
- 模型/参数不兼容只影响 Member × Model/Capability。
- Serving Key 401/403 隔离稳定 Credential ID。
- 定价访问令牌 401 只标记 Cost Connector。
- 402/余额耗尽按真实 UpstreamAccount 作用域排除。
- 429、529、Remaining/Reset 和 Retry-After 进入容量控制。
- DNS/TLS/连接错误作用于 Endpoint × Region。
- 500/502/503/504、首字超时和流损坏进入 Reliability Breaker。

请求状态为：

```text
Planned → Reserved → Sent → ClientCommitted → Completed
```

`ClientCommitted` 后禁止通用跨渠道重试。普通 HTTP 首次写响应头或正文、SSE 首个有效业务事件、Realtime 握手或首个不可重放事件、异步任务可能产生副作用时，均视为已提交。

重试和 Hedging 约束：

- 每个逻辑请求有尝试次数、额外成本和总 Deadline 预算。
- 每个 RoutingPool 有 Retry Token Bucket。
- 同一请求不得再次选择已失败目标。
- 有替代渠道时立即切换，不先 Sleep。
- `Retry-After` 用于目标 Cooldown，不用于长时间阻塞用户请求。
- 有副作用接口需要稳定幂等键，否则模糊状态不自动重试。
- 每次上游尝试记录平台真实成本，用户结算保持一次逻辑请求语义。
- Hedging 默认关闭；Phase 5 也最多启动一个符合预算的备用请求。

## 容量控制

容量模型为分层 Token Bucket：

```text
Physical Account / Credential Global Budget
  ├── RoutingPool 保底份额与最大份额
  └── Node 短租约额度块
```

同时控制 RPM、输入 TPM、输出 TPM、总 Token、并发和平台成本预算。高配额池使用节点额度块，严格小配额池允许逐请求 Redis 原子预留。自适应并发采用 AIMD 或 Gradient Controller 风格，健康时缓慢增加，429/529/TTFT 突增时乘法下降。

Redis 故障时继续使用已发布配置；已租额度只使用到 TTL；禁止新 Half-open 探测；严格池 Fail-closed；高可用池仅能使用策略明确配置的保守本机上限。

## 控制平面与多节点一致性

策略发布流程：

```text
Draft → Validate → Replay/Simulation → Shadow → Canary → Approval → Deploy → Monitor → Rollback
```

一次数据库事务创建不可变 Revision、Activation 和 Outbox。Publisher 将事件写入 Redis Stream；Pub/Sub 只用于快速唤醒。节点收到变更后完整拉取、校验、预编译，使用 `atomic.Pointer` 一次替换快照，并上报当前 Revision。节点还必须周期性对账，不能把 Pub/Sub 当作唯一可靠来源。

遥测热路径只更新分片计数器、可合并 Histogram/Sketch、有界 Ring Buffer 和本机容量状态。每 1–5 秒按 `(node_id, sequence)` 批量上报增量。数据库只保存历史 Rollup、决策/变更审计、冷启动 Checkpoint 和成本历史。

所有后台组件必须可取消、可关闭、可观测，不得使用无退出 Context 的永久 `time.Sleep` 循环。所有 Map、队列和事件缓冲具有 TTL、最大条目数、丢弃策略和 Eviction 指标。

## 成本同步与出站安全

New API 按 UpstreamAccount 合并同步 `/api/pricing` 与 `/api/user/self`，完整接收 Group、Model、Completion、Cache、Image、Audio、Per-request、Billing Expression/Tier、Pricing Version 和余额。

Sub2API 使用 groups、rates、channels、usage 接口。JWT 使用 Singleflight、提前刷新、加密缓存和稳定 Account ID；不能为每个 Channel 创建永久 Lock Map。

价格快照必须包含来源、账号、上游分组、本地/上游模型、Observed/Effective/Expires、版本/哈希、置信度、新鲜度和脱敏同步状态。价格未知或过期绝不能视为零成本；短期 Last-known-good 必须随时间衰减置信度。

出站连接默认 HTTPS，并实现：

- DNS 解析后拒绝 Loopback、Link-local、私网、Multicast 和云元数据地址；私网只能由显式 Egress Policy 放行。
- Dial 固定到验证后的 IP，防止 DNS Rebinding。
- 每次重定向重新验证，默认禁止跨 Host，且不得携带凭证跳转。
- 响应 Content-Type、原始大小和解压大小限制。
- TLS 最低版本、证书校验和可选 CA。
- 统一凭证脱敏、密钥版本与轮换。
- Per-host 并发/限速、共享 HTTP Client、分阶段超时、指数退避和 Full Jitter。

## API、权限与兼容

新 API 使用 `/api/channel-routing/v2`，至少提供 overview、groups、group detail/simulations、channels、decisions、cost-bindings、policy-drafts、rollback、operations 和 events。

- 配置更新使用 Revision/ETag 与 `If-Match`；冲突返回 409 和差异信息。
- 操作请求使用 `Idempotency-Key` 并返回持久化 Operation。
- 历史和决策使用 Cursor Pagination；配置列表使用服务端分页、筛选和 Total。
- SSE 支持 `Last-Event-ID`、序列、缺口重拉、心跳和有界客户端缓冲。
- 权限拆分为 read、operate、write、deploy、sensitive_write 和 audit_export。
- `deployment_stage` 与 `policy_profile` 分离。

兼容要求：

- 新入口和文案统一为“渠道路由”。
- `/smart-routing` 跳转 `/channel-routing/overview`。
- `/api/smart-routing/*` 至少保留两个发布周期，返回 Deprecation、Sunset 和迁移提示。
- 旧侧栏键 `smart_routing` 增加 `channel_routing` 别名。
- 兼容适配器只能转接新服务，不能成为第二个配置真源。

## 前端工作区

前端使用独立、可深链工作区：

- `/channel-routing/overview`
- `/channel-routing/groups`
- `/channel-routing/groups/$id`
- `/channel-routing/channels`
- `/channel-routing/decisions`
- `/channel-routing/costs`
- `/channel-routing/policies`

目录按 `features/channel-routing/{api,shell,overview,groups,channels,decisions,costs,policies,components,hooks,schemas}` 组织。页面、表格列、表单、Sheet 和 Mutation 分离；React Query Key 集中；筛选、分页和时间窗口存入 URL；图表动态加载；整个工作区只建立一个 SSE 连接并以 500ms–1s 批处理更新。

桌面端使用可扫描的数据表和详情面板，移动端使用 Card List。所有状态同时显示图标、文字和原因，不能只依赖颜色。表单使用 React Hook Form + Zod 和字段级错误。支持明暗主题、Reduced Motion、键盘、屏幕阅读器和 WCAG 2.1 AA。所有新文案覆盖 en、zh、fr、ja、ru、vi。现有占位 Agent 页签移除，未来只作为“策略建议”进入变更工作流。

## 数据库与计费约束

- 所有迁移和查询同时支持 SQLite、MySQL 5.7.8+、PostgreSQL 9.6+。
- 优先使用 GORM；原生 SQL 必须处理方言差异和保留字。
- 新 JSON 数据默认使用 TEXT + `common.Marshal` / `common.Unmarshal`，不得在业务代码直接调用 `encoding/json` 的编解码函数。
- 动态/阶梯成本计算修改前必须阅读并遵循 `pkg/billingexpr/expr.md`。
- 所有计费乘数有边界，Quota 转换使用 `common/quota_math.go` 的 Checked 变体并保留饱和审计。
- 路由平台成本与用户计费语义分离，渠道切换不得改变用户正常结算规则。

## 必须固化的测试不变量

1. 候选一定属于当前 RoutingPool。
2. `A-plus` 与 `A-pro` 的成员状态互不覆盖。
3. 物理硬故障按正确作用域继承。
4. 用户错误不降低渠道健康。
5. 单 Key 429 不打开 Channel Aggregate Breaker。
6. ClientCommitted 后不执行通用跨渠道重试。
7. 定价访问令牌失败不自动判定 Serving Key 失败。
8. 配置按单调 Revision 原子生效。
9. 决策可由快照、版本和随机种子重放。
10. 所有缓存都有 TTL 和容量。
11. Redis 故障行为按 RoutingPool 策略可测试。
12. 未知或过期价格绝不作为零成本。
13. 同一请求不会重复选择已失败渠道。
14. 共享容量在多个分组之间不会饥饿。
15. Multi-Key 重排不会串用历史健康状态。

验证还包括确定性单元/表驱动测试、三数据库迁移测试、三节点 Redis 传播和容量测试、网络/协议故障注入、`go test -race`、Heap/Goroutine 差分、独立 Soak Test、Playwright、Axe、视觉回归、明暗主题和响应式检查。普通单元测试不得通过随机循环、Sleep 或伪压力测试模拟性能验证。

## 性能与完成条件

- 本地选路计算 p99 小于 1ms。
- 普通租约模式热路径不访问数据库。
- 配置传播 p99 小于 5 秒。
- 本地严重故障反应小于 1 秒，共享状态收敛小于 5 秒。
- 指标、Breaker、JWT Lock、事件缓冲和审计缓冲均有硬上限。
- 稳态 Heap 与 Goroutine 不随时间线性增长。
- Retry/Hedge 额外请求比例受预算约束。
- 成本同步新鲜度不超过两个正常同步周期。
- DB/Redis 短暂不可用时按已发布降级策略运行。
- 三种数据库兼容验证通过。
- 后端测试、Race 检查、前端 typecheck/lint/build、可访问性和视觉检查全部通过。
- 需求逐项审计能够为最终方案中的每个明确要求给出代码、测试或运行证据。

## 自审结论

- 无 `TBD`、`TODO` 或未选择的核心架构分支。
- 各阶段边界与最终方案一致，Phase 0 是后续所有阶段的强制前置。
- 状态作用域、错误分类、容量控制和成本连接器互不混用。
- 兼容接口与新 API 的真源关系明确。
- 首字后通用流式输出不承诺跨渠道无缝续接；这是协议边界，不得通过实现或文案弱化。
