# 渠道路由运维与同步切换

## 目标

本文定义渠道路由从旧版本化运行资源一次性切换到正式无版本资源的操作顺序。该切换不是兼容滚动升级：旧版和新版渠道路由 Worker 不得在同一集群中混跑。

## 发布前检查

1. 备份主数据库和日志数据库，并验证恢复路径。
2. 确认所有节点使用同一主数据库、日志数据库和共享 Redis。
3. 正式前端、内部调用和部署探针已经统一使用 `/api/channel-routing/*`；旧 `/api/channel-routing/v2/*` 不再注册、重定向或代理。
4. 部署配置已使用 `SMART_ROUTING_REQUEST_PROFILE_ENABLED` 和 `ROUTING_ALPHA_DRAINED`，不得保留旧环境变量别名。
5. 新渠道配置回填报告已核对：每个渠道都有明确倍率、来源、确认状态和流量范围；手工配置没有被历史迁移覆盖。
6. 用户预扣、结算、退款、异步任务结算和普通渠道余额查询回归均已通过。
7. 在清除旧管理凭据前完成离线回放、资格比较和回滚演练。凭据清除不可逆，执行后不得通过回滚旧二进制重新启用连接器。

## 同步切换步骤

1. 停止所有旧渠道路由 Worker 和生产者，确认集群中没有旧进程继续续租或写遥测。
2. 触发最终内存指标刷新，等待旧遥测 Stream `routing:v2:telemetry` 的 consumer group 达到 `pending=0`、`lag=0`，记录最后 `last-delivered-id` 作为切换水位。停止向旧配置 Stream `routing:v2:config` 写入；配置状态由数据库 revision 作为切换真源，不复制旧配置消息或 checkpoint。
3. 仅由迁移节点执行数据库迁移和渠道配置回填；其他节点通过 `ROUTING_SCHEMA_READY_WAIT_SECONDS` 等待无版本 Schema marker。该变量不得为负数。
4. 停止并取消旧 `routing_cost_sync` 调度，不再创建新的成本同步 Operation。历史 Operation、成本版本和决策审计只读保留。
5. 建立新资源：
   - Lease：`channel-routing-legacy-reconcile`、`channel-routing-canary-evaluator`、`channel-routing-canary-operations`、`channel-routing-runtime-health-maintenance`。
   - Redis Stream：`channel-routing:config`（配置通知）、`channel-routing:telemetry`（遥测）。
   - Consumer Group：`channel-routing-rollup`。
   - 配置 Checkpoint Scope：`channel-routing:config`；旧 `routing:v2:config` checkpoint 只按历史保留，不作为新版消费游标。
6. 旧生产者已停止且旧 Stream 已完全排空时，新 Stream 从空边界开始接收切换后的消息；数据库 receipt 继续提供最终幂等，切换水位必须写入发布证据，禁止把旧 pending 复制到新 Stream 造成重复。
7. 清理路由连接器中的 NewAPI/Sub2API 管理凭据、自定义 CA、私网信任配置、托管 JWT 和账号健康缓存。不得修改普通渠道 Key、`Channel.Balance`、真实渠道凭据健康、用户钱包、Token、订阅或异步计费数据。
8. 启动新版 Worker，然后启动其余节点。任何节点 Schema 未就绪、配置 revision 冲突或成本基准不可用时必须失败关闭。

## 切换后验证

1. `/api/channel-routing/overview`、`runtime-settings`、`nodes`、`events`、`channel-configurations` 和其余正式接口权限与行为正常。
2. `/api/channel-routing/v2/*` 返回未注册路由结果，不出现 301、302、307、308 或兼容响应。
3. `/api/channel-routing/cost-bindings*` 和 `/api/channel-routing/costs/sync` 返回 `410` 与稳定错误码 `routing_cost_connector_retired`，响应不含绑定、账号或凭据内容。
4. 旧 Lease 不再续租；旧 `routing:v2:config`、`routing:v2:telemetry` 和旧 consumer group 不再产生新写入或 pending；新配置/遥测 Stream 及新 group 的 pending、lag、oldest age 和最后消费水位可观测。
5. 新决策只产生 `channel-routing-balanced`、`channel-routing-canary`、`channel-routing-shadow` 或 `channel-routing-legacy`。历史带版本标识的数据仍可读取、导出和回放。
6. 渠道倍率或流量范围更新后，数据库 revision、config epoch、Outbox、Redis 通知、节点 CAS 刷新和 SSE 缓存失效形成完整链路。
7. 成本未知不按零统计；0 倍率是已知零成本；Hedge、Canary、Strict Capacity 和主动探测保持各自的失败关闭边界。

## 回滚边界

1. 发现门禁失败时先停止全部新版 Worker，禁止同时恢复旧 Worker。
2. 不回退数据库 Schema，不改写不可变历史，不删除新渠道配置或审计记录。
3. 管理凭据清除前可以回滚应用代码；清除后只能修复新版，不能恢复旧成本连接器或从日志、审计、备份以外的位置重建凭据。
4. 用户计费和异步计费使用独立冻结快照，不得因渠道路由回滚修改已提交任务价格。

## 验收记录

发布记录至少保存：数据库迁移结果、旧 Stream 最终水位、新资源创建时间、旧资源静默证据、凭据清理计数、三数据库与 Redis 测试、用户/异步计费回归、前端构建和浏览器矩阵结果。任一项缺失时不得宣布切换完成。
