# ExecPlan 维护规则

本目录保存长程、可恢复的实施台账。当前渠道路由工作的范围真源是用户本次批准的“Routing v2 完整退场方案”；仓库内的当前发布与切换入口是 `docs/channel/channel-routing-operations.md`，实现状态以当前工作树和最新实际验证证据为准。`channel-routing-2.0-execplan.md` 仅保留为历史实施档案，不能再作为当前 API、兼容策略、成本连接器或发布结论的依据。

每次恢复、Gate 切换、上下文压缩或安全检查点后必须：

1. 重新读取根目录及相关子目录的 `AGENTS.md`。
2. 重新读取用户当前批准的无版本退场方案，并核对 `docs/channel/channel-routing-operations.md` 的同步切换约束。
3. 读取当前代码、当前测试和最新验证证据；历史设计与历史 ExecPlan 只用于理解迁移来源，发生冲突时不得覆盖当前退场方案。
4. 对账 `git status --short --branch`、`git diff`、最近提交和最后一组实际验证输出。
5. 从第一个依赖已满足但尚未完成的行为切片继续，先 RED、再 GREEN。
6. 更新 Progress、Surprises & Discoveries、Decision Log、验证证据和恢复说明。

记录 Gate 证据时必须包含状态、需求编号、基准提交、变更文件、完整命令、退出码、关键输出、数据库矩阵、安全/计费审查、既有失败、新增失败、残余风险和下一动作。未实际运行的验证只能记为 `NOT_RUN`。

禁止使用历史 ExecPlan 合理化缩小或改变最终范围。无版本 API、v2 不兼容退场、渠道配置、连接器清除、正式前端、安全、计费隔离和第 13/14 节验证矩阵仍以用户当前批准方案为完成边界。
