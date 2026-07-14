# 渠道路由 2.0 运维与滚动升级

## 目标

本文说明渠道路由 2.0、异步计费 v2、计费投影和上游凭证加密在多节点环境中的安全启用顺序。它不替代数据库备份、变更审批或生产灰度流程。

## 发布前检查

1. 先备份主数据库和日志数据库，并确认可以恢复。
2. 所有节点使用同一主数据库、日志数据库和共享 Redis。
3. 只有主节点执行迁移；从节点通过 Schema 等待变量等待迁移完成。
4. `BATCH_UPDATE_ENABLED` 必须为 `false`，否则异步计费 v2 会保持关闭。
5. 同一集群的 `ASYNC_BILLING_V2_ROLLOUT_EPOCH` 和 `ASYNC_BILLING_V2_FLEET_STABLE_SECONDS` 必须一致。
6. 不要在首轮升级中同时删除旧配置、旧 API、旧密钥或旧数据。

## 两阶段滚动升级

### 第一阶段：迁移并让全体节点具备读取能力

1. 升级前在所有节点显式设置 `ASYNC_BILLING_V2_ENABLED=false`，并设置相同且明确的 rollout epoch。这样整个滚动窗口都保持 legacy writer，不依赖旧心跳恰好存活。
2. 先升级主节点，使其完成渠道路由、异步计费、统计投影和日志投影 Schema 迁移。
3. 逐个升级其余节点。每个节点必须使用相同的 rollout epoch，并持续上报 async billing protocol v2 能力；确认旧节点心跳已经过期。
4. 从节点启动时默认最多等待 60 秒。`0` 表示只检查一次；大型数据库可按实际迁移时长提高以下值，但不得设为负数：
   - `ROUTING_SCHEMA_READY_WAIT_SECONDS`
   - `ASYNC_BILLING_SCHEMA_READY_WAIT_SECONDS`
   - `BILLING_STATS_PROJECTION_SCHEMA_READY_WAIT_SECONDS`
   - `BILLING_LOG_PROJECTION_SCHEMA_READY_WAIT_SECONDS`
   - `BILLING_LOG_SCHEMA_READY_WAIT_SECONDS`
5. 确认全部节点均为 v2、Schema ready 且 `BATCH_UPDATE_ENABLED=false` 后，再在所有节点启用 `ASYNC_BILLING_V2_ENABLED=true` 并滚动重启。
6. 全部存活节点连续稳定至少 35 秒后，Fleet Gate 才允许 v2 writer 生效。节点集合、进程 incarnation、协议版本或 rollout epoch 发生变化时，稳定窗口会重新开始。

### 第二阶段：启用凭证密文 v2

1. 生成 32 字节随机 AES 密钥并使用标准 Base64 编码。密钥必须由密钥管理系统分发，禁止提交到 Git、日志或工单正文。
2. 先在所有节点配置同一个 `ROUTING_CREDENTIAL_ENCRYPTION_KEY_RING`，但保持 `ROUTING_CREDENTIAL_ENCRYPTION_WRITE_VERSION=1`。
3. 确认全部节点已重启并能读取当前密钥环后，再把所有节点的 write version 切换为 `2`。
4. 渠道路由成本同步维护任务会按有界分页、CAS 方式把旧凭证重加密为当前密钥。如果渠道路由未启用，scheduled cost sync 不会运行，必须由具备操作权限的管理员调用 `POST /api/channel-routing/v2/costs/sync` 手动触发。
5. 至少连续检查两轮：所有轮次都要求 `credential_conflicts=0`，且后一轮必须为 `credentials_rotated=0`，才表示当前密钥迁移已收敛。
6. 轮换密钥时，把旧 current 移入 `previous`，把新密钥设为 current。最多保留 4 个 previous key；确认没有旧密文前不得删除旧密钥，且不得因 key ring 上线而删除仍被 Redis 或其他旧密文使用的 `CRYPTO_SECRET`。

密钥环格式：

```json
{
  "current": {
    "id": "key-2026-01",
    "key_base64": "<base64-32-byte-key>"
  },
  "previous": []
}
```

## 异步计费恢复与保留期

- `ASYNC_BILLING_RECEIPT_RETENTION_DAYS` 默认 365 天，运行时会限制在 30 到 3650 天。
- 只有财务结算、缓存同步、统计投影和日志投影都已持久完成的 receipt 才能被清理。
- Redis、日志库或投影暂时不可用时，恢复 Worker 会保留持久待办并指数退避；不要通过直接删表或缩短保留期清理积压。
- 人工计费复核只接受带 ETag/CAS、幂等键和可信上游证据的操作。无法证明上游拒绝或发送前失败时，不得自动退款。

## 回滚

1. 紧急停止新 v2 发送时设置 `ASYNC_BILLING_V2_ENABLED=false` 并滚动重启；已落盘的恢复、投影和审计记录必须继续保留。
2. 不要回退数据库 Schema，也不要删除 v2 表、列或唯一索引。旧二进制只能在其仍兼容这些增量字段时临时回退。
3. 凭证写入已切换到 v2 后，如需回退 writer，可把 write version 改回 `1`，但必须继续保留完整 key ring，确保现有 v2 密文仍可读。
4. rollback 期间可先把全部节点设为 `ASYNC_BILLING_V2_ENABLED=false`，或统一切换到一个新的协调 epoch 让 Fleet Gate fail closed；不得让节点使用不同 epoch。

## 正式发布验收

推送稳定 tag 会并行启动二进制 Release、Electron 和多架构 Docker 工作流。三条工作流全部成功前，不得宣布发布完成或结束变更窗口。

最终必须核验：

1. tag 指向已合入 `main` 的精确 merge commit，且该 commit 的 `VERSION` 与 tag 完全一致。
2. GitHub Release 已发布且 10 个必需资产及校验文件齐全。
3. GHCR 的版本 tag 与 `latest` 指向同一 manifest digest，并包含 `linux/amd64` 和 `linux/arm64`。
4. 两个架构镜像均通过 Cosign 身份校验、SPDX SBOM 和 SLSA provenance 校验。
5. OCI version/revision 标签、容器 `--version`、`/api/status` 和 default 前端 build revision 均与 tag/merge commit 一致。
6. 任一工作流失败时，保持 Goal 和发布窗口未完成，修复或按同一 tag 的安全恢复流程重跑后，再从头核验全部证据。
