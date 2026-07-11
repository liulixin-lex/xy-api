# 渠道路由 2.0 Phase 1 Observe 实施计划

## 目标

在 Gate 1 正确性基础上建立只读、可解释、不会改变现有选路结果的 Observe 数据面。Phase 1 完成后，系统具备稳定的 Pool、Member、Credential 身份，能够从统一的有界遥测与成本状态生成不可变观察快照，并通过 `/api/channel-routing/v2` 提供只读运维 API。

本阶段不启用新选择器接管流量，不发布策略，不引入 Redis Stream，不做 Canary、P2C、主动探测或 Hedging；这些分别属于 Phase 2–5。

## 权威约束

- `/opt/临时/渠道路由-企业级重构最终方案.md`
- `/opt/临时/渠道路由-2.0-Codex执行契约.md`
- `docs/superpowers/specs/2026-07-10-channel-routing-v2-design.md`
- 根 `AGENTS.md`
- Gate 1 已验证提交 `bcb6602b`

## 需求追踪

| ID | 要求 | Phase 1 证据 |
| --- | --- | --- |
| P1-TOPOLOGY | Pool 与本地 Group 一一对应；同一物理渠道在不同 Pool 中形成独立 Member | 三库幂等回填测试、分组隔离测试 |
| P1-CREDENTIAL | Multi-Key 使用稳定 Credential ID；重排不换 ID，轮换产生新 ID，密钥不落入新表/API/日志 | HMAC 指纹、重排/轮换/重复 Key 测试、敏感信息扫描 |
| P1-SCOPE | Physical Channel、Credential、PoolMember 状态作用域不混淆 | 快照 DTO 与候选解释测试 |
| P1-TELEMETRY | 遥测输入有界、快照不可变、未知/过期数据显式标记 | 队列/快照容量测试、并发 race、覆盖率字段 |
| P1-OBSERVE | 新选择器只计算与审计，不改变 legacy 实际选择 | 对照测试：Observe 开关前后实际 Channel 相同 |
| P1-API | 只读 overview、groups/group detail、channels、costs、decisions API | Router/Auth/API 契约测试、分页与过滤测试 |
| P1-COMPAT | 旧 `/api/smart-routing/*` 与现有设置继续可用 | 旧 Controller 回归测试 |
| P1-DB | SQLite、MySQL 5.7.8+、PostgreSQL 9.6+ 迁移幂等和行为一致 | 真实三库迁移/回填契约 |
| P1-MEMORY | 关闭态不分配；队列、快照和索引均有硬上限 | Stats、drop/eviction 测试、Heap/Goroutine 检查 |

## 数据模型

### `routing_pools`

- `id`：数据库主键。
- `group_name`：本地 Group 的稳定唯一键。
- `display_name`：当前先等于 Group，供后续独立命名。
- `source`：Phase 1 为 `legacy_group`。
- `active`：旧 Group 不再存在时置为 false，不删除历史身份。
- `created_time` / `updated_time`。

### `routing_pool_members`

- 唯一键 `(pool_id, channel_id)`。
- 保存独立 Member ID、物理 Channel ID、legacy priority/weight、active 与时间戳。
- Group 移除时只停用 Member，重新加入时复用原 Member ID。
- 不复制 Physical Channel 的管理员状态、余额或 Credential 健康。

### `routing_credential_refs`

- `id` 为稳定 Credential ID。
- 唯一键 `(channel_id, fingerprint)`。
- `fingerprint` 使用项目 HMAC，不保存原始 Key、可逆密文或可离线验证的裸 SHA。
- `active`、`last_seen_index`、`created_time`、`updated_time`、`retired_time`。
- Key 重排只更新 `last_seen_index`；Key 轮换创建新 ID 并退役旧 ID；重复相同 Key 共用一个客观 Credential ID。

## 工作切片

### Task 1：拓扑与稳定 Credential 身份

- [x] 新增三张表及 GORM 模型。
- [x] 实现单事务幂等 legacy 回填。
- [x] 保留停用记录以维持稳定 ID。
- [x] 将模型加入主迁移与现有三库契约。
- [x] 覆盖分组增删、Key 重排、轮换、重复 Key、回滚与取消。

### Task 2：不可变 Observe 快照

- [x] 新增 `service/channelrouting`，从 DB 真源和现有 Hot Cache 构建只读快照。
- [x] 快照包含 Pool、Member、Credential 映射、物理状态、分组指标、Breaker、Capacity、Cost 与新鲜度。
- [x] 使用 `atomic.Pointer` 发布完整快照；失败不覆盖 last-known-good。
- [x] 构建和发布不在请求热路径访问 DB。
- [x] 统计 Revision、构建时间、年龄、条目数、未知身份与丢弃数。

### Task 3：Observe 决策与有界审计

- [x] 将新选择器计算接到 Observe 路径，仅写观察结果，不改变 legacy 返回 Channel。
- [x] 使用白名单 DTO：不保存 Prompt、正文、API Key、Cookie 或凭据。
- [x] 决策缓冲有容量、TTL/drop 统计和批量持久化。
- [x] 记录 Request/Decision ID、Pool/Model、候选、过滤原因、评分分解、legacy 实际选择与时间。
- [x] 未知错误分类、遥测覆盖率和 credential 覆盖率可计算。

### Task 4：只读 v2 API

- [x] `GET /api/channel-routing/v2/overview`
- [x] `GET /api/channel-routing/v2/groups`
- [x] `GET /api/channel-routing/v2/groups/:id`
- [x] `GET /api/channel-routing/v2/channels`
- [x] `GET /api/channel-routing/v2/costs`
- [x] `GET /api/channel-routing/v2/decisions`
- [x] `GET /api/channel-routing/v2/decisions/:id`
- [x] 列表使用服务端分页或 Cursor，限制 page size，支持稳定排序和必要过滤。
- [x] Phase 1 沿用 `ChannelRead` 权限；独立 RBAC 留到 Phase 5。

### Task 5：生命周期与兼容接线

- [x] 启动迁移后执行 topology reconcile，并发布首个 Observe 快照。
- [x] Smart Routing Runtime 定期刷新快照、批量 flush 决策，支持 Context/Close/Wait/backoff/jitter。
- [x] Channel 创建、更新、删除后安全唤醒回填；失败不破坏现有请求路径。
- [x] 旧 `/api/smart-routing/*` 保持兼容且不改变响应契约。

### Task 6：Gate 2 验证

- [x] 相关普通测试、race、vet、build。
- [x] `go test ./... -count=1`。
- [x] SQLite/MySQL/PostgreSQL 真实迁移幂等和行为契约。
- [x] Observe 前后 legacy 实际选路一致。
- [x] 无跨 Pool 候选、稳定 Credential ID、无密钥泄漏。
- [x] 队列/Map/快照有界，关闭后不继续分配或启动 Worker。
- [x] `git diff --check`、JSON wrapper、Testify、保护标识、临时文件与依赖审计。
- [x] 独立 blocker review 无未解决 P0/P1。

## Gate 2 验证证据（2026-07-12）

- `go test ./... -count=1`、`go vet ./...`、`go build ./...`：PASS。
- `go test -race ./model ./service/channelrouting ./pkg/routing_metrics -count=1`：PASS。
- MySQL 5.7 与 PostgreSQL 15：模型迁移/幂等 upsert/retention、Snapshot active credential 聚合、`VIP`/`vip`、`Model-X`/`model-x`、keyless、live merge、decision hash 精确过滤均 PASS；临时容器已销毁。
- 全仓 Race 仍会命中既有 `service/task_polling` 与 `logger` 竞态；本阶段相关包定向 Race 通过，未把既有问题伪装为新通过证据。
- 独立只读审查最终结论：无未解决 P0/P1。Phase 2 必须解决 additive flush 的模糊提交去重、可合并分布与高流量 DB 聚合热点。

## 外部对标落地

- OpenAI：保留上游 request ID 和 rate-limit header 作为可观测信号；429 使用有上限的随机指数退避，失败请求本身也消耗额度，禁止无预算重试风暴。
- LiteLLM：借鉴 deployment identity、cooldown、fallback 统计和 RPM/TPM/并发准入的分离；不复制其进程内状态作为多节点真源。
- Sub2API：借鉴 Account/Group 独立成员、优先级、并发槽与临时不可调度状态；避免把账号共享容量错误拆成每 Group 独立容量。
- OpenRouter：借鉴 provider preference、data policy/capability 硬约束和 provider fallback 可解释性；不把供应商排序黑盒化。
- Envoy/HAProxy：借鉴 outlier detection、panic threshold、slow start 和可观察的 health state；Gate 2 仅观察，不启用流量接管。

## Gate 2 完成定义

只有以下条件同时满足才可进入 Phase 2：

1. 三类稳定身份与兼容回填在三数据库真实通过。
2. Observe 快照与决策审计有界、可解释、无敏感数据。
3. v2 只读 API 契约稳定并有权限、分页和错误测试。
4. 新计算路径无法改变 legacy 实际选路，且有直接回归证明。
5. 遥测覆盖率、未知分类比例、快照年龄和 runtime drop/eviction 可被 API 观察。
6. 无未解决 P0/P1，工作树和验证证据完整。
