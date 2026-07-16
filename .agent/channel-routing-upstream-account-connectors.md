# 渠道路由上游账号连接器规范

## 背景

渠道路由需要从上游账号同步分组、模型价格和账号余额。当前主要上游是 NewAPI 和 Sub2API，但两者的管理认证与模型调用认证不是同一套凭据：

- NewAPI 使用账号“访问令牌”，并要求同时发送账号 ID。
- Sub2API 使用管理 JWT；JWT 可以直接配置，也可以由邮箱和密码登录获得。
- 网关 API Key 只代表模型调用或 API Key 自身额度，不能替代账号管理凭据，也不能冒充账号钱包余额。

本规范来自以下官方仓库版本的实现与契约测试：

- NewAPI：`a63364d156cf2a64f1c3d1ee4923d73d5f3222a1`
- Sub2API：`30df3f68e4ea6ce493af1d6651d3f668201c14dd`

Sub2API 从初始审计点 `da85cc7e47882090b115d664afe8e39b37aa7417` 到上述基线的账号、余额、分组、倍率、嵌套渠道和 `subscription_type` 契约未发生漂移；新增的 Alpha Search 失败转移状态处理也未改变搜索计费语义。

官方协议变化后，旧版 Sub2API 扁平渠道结构、字符串分组 ID 和 `/v1/usage` 管理 JWT 查询方式已经失效；NewAPI 的 `/api/pricing` 也不能再被当成账号分组权限视图。连接器必须按本规范解析，不能通过宽松兼容把未知价格误判为可用价格。

## 历史实现结论（v0.1.12 审计，连接器已退役）

> Routing v2 退场后，NewAPI / Sub2API 路由专用管理连接器、自动成本同步 Worker、凭据测试与分组发现 API 已从运行时删除。本文继续保留官方协议、安全边界和历史验收基线，供不可变成本历史审计、迁移核验，以及未来评估是否引入全新连接器时使用；当前生产代码不得重新读取旧 binding、管理凭据或上游账号健康来参与选路。

退役前项目曾存在完整的 NewAPI / Sub2API 上游账号连接器，历史能力包括：

- NewAPI Access Token + User ID 管理认证、账号余额、分组、模型和价格同步。
- Sub2API 显式 JWT，或邮箱密码登录后取得 JWT；支持本机/Redis 缓存、singleflight、过期重登和轮换 fencing。
- 凭据加密与掩码、provider scope 清理、SSRF/DNS rebinding/TLS/自定义 CA/响应大小限制和错误脱敏。
- 定时成本同步、手动 Test、加载分组、版本化成本快照、账号余额和正式前端 Cost Sources 表单。

v0.1.12 的工作不是增加另一套并行连接器，而是修正已有实现与官方协议之间的漂移：

- NewAPI 改为“账号分组 + 按组账号模型 + 价格目录 + Gateway Key 可服务模型”四方交叉验证，不再信任 `/api/pricing` 的账号字段，也不把管理 Access Token 的模型视图当成 Gateway Key 的实际服务能力。
- NewAPI 从每个上游实例的 `/api/status` 读取 `quota_per_unit`，余额和 token 倍率不再错误复用本地实例常量。
- NewAPI 明确保留 `0` 倍率免费组，拒绝把 `auto` 动态分组或缺失倍率猜成 `1x`。
- 同一 NewAPI 账号先按管理凭据去重预检，再按 binding 隔离失败；轮换中的坏令牌或坏分组不能阻塞健康 binding 更新。
- Sub2API 改用官方数字分组 ID、嵌套 `platforms[].groups[]/supported_models[].pricing`、正式余额端点和正确价格单位；订阅组按订阅限额判定可用性，账号钱包余额不参与该渠道的余额熔断。
- Sub2API 持久化账号身份改为规范化 Base URL + `/auth/me` 官方用户 ID；显式 JWT 轮换不再拆成新账号，认证失败也不再创建 token 派生账号。
- NewAPI 未公开工具/固定图片附加费，以及 Sub2API 展示目录缺少的 priority/flex、1 小时 cache write、长上下文规则，已经按当前请求画像条件降为 unknown。
- 历史 NewAPI/Sub2API 快照缺少当前 `catalog_scope`/`sub2api_contract` 标记时按来源失败关闭，等待新连接器同步后再参与成本选路。
- `claude_code_only` 已从 `/groups/available` 关联；未显式声明 Claude Code 用途的 binding 不生成成本快照。数据面按 Sub2API 官方请求指纹形成 `standard`、`unknown`、`claude_code` 分类，并在所有选路出口排除不合格流量。
- Test / 加载分组属于只读预览：可以读取余额验证账号响应，但不得提前写数据库或热缓存。

审计同时确认两个长期能力边界：NewAPI `/api/pricing` 不公开工具调用等附加费；Sub2API `/api/v1/channels/available` 是用户展示目录，不是完整 BillingService 报价。当前实现已对已知缺失维度失败关闭。`serves_claude_code` 已成为自动数据面准入边界，但它依据协议指纹而不是账号身份认证；专用 routing pool 仍可作为更强的物理隔离与纵深防御，不能把请求分类描述成客户端身份或授权证明。

## 目标

1. 用正确的管理凭据读取当前上游账号、可用分组、模型价格和钱包余额。
2. 将上游价格统一为当前项目可审计的美元成本快照。
3. 明确区分管理凭据、模型调用 API Key、账号钱包和 API Key 自身额度。
4. 上游协议未知、价格无法可靠换算或响应不完整时失败关闭，并保留上一份有效快照。
5. 凭据始终加密、脱敏、按上游类型隔离，并支持安全轮换。

## 范围

本规范覆盖：

- `routing_channel_bindings` 的 NewAPI / Sub2API 成本来源连接器。
- 分组发现、价格同步、账号钱包余额同步和凭据测试。
- 成本快照中的单位换算、分层表达式和置信度。
- 本机与 Redis 中的 Sub2API JWT 生命周期。

本规范不覆盖：

- 创建、删除或修改上游账号、分组、渠道、API Key。
- 使用管理凭据代理普通模型请求。
- 把 Sub2API `/v1/usage` 的 API Key 配额合并到账号钱包余额。
- 自动完成验证码、TOTP、Turnstile 或其他交互式登录挑战。

## 凭据契约

### NewAPI

管理连接器需要：

- `new_api_access_token`：必需，作为 `Authorization: Bearer <token>`。
- `new_api_user_id`：必需，作为 `New-Api-User: <id>`。

正式 Test 和定时同步还需要：

- `gateway_api_key`：必需，只用于 `GET /v1/models` 的模型服务认证。

`gateway_api_key` 不是 NewAPI 管理访问令牌，不得作为 `/api/user/self`、`/api/user/self/groups` 或 `/api/user/models` 的后备认证；反过来，`new_api_access_token` 也不得代替 Gateway Key 调用 `/v1/models`。“加载分组”只做管理面发现，可不填 Gateway Key；需要生成成本快照的 Test/定时同步必须同时验证两类凭据。缺少必需凭据或用户 ID 时，连接器应在发请求前报告明确的字段错误或未就绪状态，不能等到后台同步后再以模糊的 401 失败。

同一 canonical Base URL + User ID 下存在多个 binding 时，必须分别按“Access Token + 自定义 CA + 出站策略”等价类执行管理认证和账号模型取数；不同 Access Token 不得由排序最前的 representative 代表。Gateway 模型取数另外按“Gateway Key + 自定义 CA + 出站策略”等价类去重。失败凭据只让对应 binding 进入退避，至少一个健康凭据存在时仍继续同步该账号。缺少 User ID、所有管理凭据均未认证或认证失败时，不得创建 token 派生的账号记录。

### Sub2API

管理连接器按以下优先级获取 JWT：

1. `sub2api_token`：显式配置的 JWT，直接使用。
2. `sub2api_email` + `sub2api_password`：调用 `/api/v1/auth/login` 获取 `access_token`。

`gateway_api_key` 不属于管理认证，不能用于 `/api/v1/auth/me`、`/api/v1/groups/*` 或 `/api/v1/channels/available`。

显式 JWT 被拒绝时直接报告认证失败，不使用已保存的邮箱密码静默替换。由连接器登录获得的 JWT 被拒绝时，可以清除精确匹配的缓存并只重登一次，防止无限登录循环。

如果登录返回 2FA、验证码或其他交互挑战而没有 `access_token`，连接器失败关闭，并提示管理员改用已完成登录后取得的显式 JWT。

### 存储与轮换

- 持久凭据使用项目现有 AES-GCM key ring 加密，API 永不返回明文。
- 前端和 API 只显示掩码；错误、日志、缓存 key 和审计事件不得包含令牌、密码、邮箱明文或自定义 CA 内容。
- NewAPI 与 Sub2API 字段按 provider scope 清理，切换 provider 时不得复用另一方凭据。
- Sub2API 托管 JWT 在本机和 Redis 中均以密文保存。
- JWT cache identity 至少包含 binding、Base URL、provider、key version 和凭据指纹。
- 凭据轮换、binding 删除/重建和并发登录必须使用 generation/fencing，旧登录结果不能覆盖新凭据。
- Redis 登录锁和缓存删除必须验证 owner 或精确 token，不能误删其他实例刚刷新的 JWT。

### Sub2API 持久化账号身份

JWT 是可轮换的会话凭据，不是账号身份。正式同步成功读取 `/api/v1/auth/me` 后，持久化 `RoutingUpstreamAccount` 必须优先使用：

```text
canonical Base URL + official user ID
```

官方用户 ID 必须是可规范化为正 `int64` 的数字；缺失、零、负数、小数、溢出或错误类型都失败关闭，不回退 JWT 或邮箱。JWT、邮箱、密码、Gateway API Key、JWT cache fingerprint 和 token 掩码都不能进入持久化账号 key 或 masked identity。相同实例、相同官方用户更换显式 JWT 后必须继续复用同一个 AccountID；不同实例或不同用户必须保持隔离。

在 `/auth/me` 尚未确认官方身份前发生的认证、网络或协议失败，只更新 binding 的同步失败和退避，不创建 token/email 派生的 degraded 账号。JWT cache identity 仍按 binding、凭据版本和 fencing 独立计算，不能与持久化账号 identity 混用。

## 官方端点与响应契约

### NewAPI

| 用途 | 端点 | 认证 | 关键响应 |
| --- | --- | --- | --- |
| 上游额度单位 | `GET /api/status` | 默认公开 | `data.quota_per_unit` |
| 当前账号与余额 | `GET /api/user/self` | `UserAuth`：Access Token + `New-Api-User` | `data.quota`, `data.used_quota` |
| 账号可用分组与实际倍率 | `GET /api/user/self/groups` | `UserAuth`：Access Token + `New-Api-User` | `data[group].ratio`, `data[group].desc` |
| 指定分组的账号可用模型 | `GET /api/user/models?group=<urlencoded-group>` | `UserAuth`：Access Token + `New-Api-User` | `data[]` 模型名 |
| 全局价格目录 | `GET /api/pricing` | `HeaderNavModuleAuth("pricing")`；默认公开 | `data[]` 价格条目；其他顶层字段不作为账号权限 |
| Gateway Key 可服务模型 | `GET /v1/models` | `TokenAuth`：Gateway API Key | `data[].id` 模型名 |

`GET /api/pricing` 的认证语义是本连接器最重要的边界：官方默认配置为 `enabled=true, requireAuth=false`，中间件会走 `TryUserAuth`。`TryUserAuth` 只读取 Dashboard session，不解析 Bearer Access Token，因此连接器即使发送了 Access Token 和 `New-Api-User`，默认仍可能拿到匿名公共价格视图。只有上游显式把 pricing 配成 `requireAuth=true` 时，该端点才走 `UserAuth`；pricing 模块关闭时则返回 403。该 403 是能力/配置错误，不是 Access Token 认证失败，连接器必须读取受限错误体后分类并给出可操作提示。

因此 `/api/pricing` 只能提供价格目录：

- 顶层 `group_ratio`、`usable_group` 不能证明当前账号拥有某个分组，也不能覆盖 `/api/user/self/groups` 的实际倍率。
- 价格条目的 `enable_groups` 是目录能力信息，不能证明当前账号在该分组可使用该模型。
- 公共目录包含的额外模型必须忽略；账号模型未在目录中取得可靠价格时，当前分组失败关闭，不能从其他分组复制或推测价格。
- 若上游把独占/私有分组模型从匿名目录隐藏，必须在上游将 pricing 模块配置为 `requireAuth=true`，使 Access Token 能取得该目录价格；否则连接器会按安全策略把这些模型视为缺价并失败关闭，不会猜测私有价格。
- 匿名目录缺少账号模型且响应没有 `Auth-Version` 时，错误必须明确为目录 scope 不完整，并提示上游管理员启用 `pricing.requireAuth=true`；这不是 Access Token 失效。已认证目录仍缺价时，报告 authenticated catalog 缺价并保留旧快照。
- 目录整体不可用、重复模型价格冲突或协议损坏属于账号级失败；不得用旧的 `1x + group-only` 推测路径继续写入。

`GET /v1/models` 是 Gateway API Key 的实际服务能力视图，会受 token group、token 模型限制和当前渠道能力影响。只有同时出现在管理账号视图与该 Gateway Key 视图中的模型才能进入 binding 快照。但该端点只返回最终模型集，不公开 token group；因此它能证明“Key 可服务此模型”，不能证明“Key 必然使用 binding 中配置的 `upstream_group`”。组归属仍由管理员在创建或配置该 Gateway Key 时保证，连接器不得伪造已自动验证该关系。

NewAPI 的 `quota` 已经是当前剩余额度，`used_quota` 是累计消费。`quota_per_unit` 必须取自同一个上游实例的 `/api/status`，不能使用当前项目本地 `common.QuotaPerUnit` 代替。钱包美元余额必须按以下公式计算：

```text
balance_usd = quota / upstream_quota_per_unit
input_cost_per_million = model_ratio * 1_000_000 / upstream_quota_per_unit
local_base_ratio = model_ratio * local_quota_per_unit / upstream_quota_per_unit
cache_read_cost_per_million = input_cost_per_million * cache_ratio(default 1)
cache_write_5m_cost_per_million = input_cost_per_million * create_cache_ratio(default 1.25)
cache_write_1h_cost_per_million = cache_write_5m_cost_per_million * (6 / 3.75)
audio_input_cost_per_million = input_cost_per_million * audio_ratio(default 1)
audio_output_cost_per_million = input_cost_per_million * audio_ratio(default 1) * audio_completion_ratio(default 1)
```

不得再次减去 `used_quota`。若 `quota` 字段缺失，余额状态是 unknown，不得把缺字段解释成已知 `$0`。正式 Test 和定时同步缺少、为零、负数或非有限 `upstream_quota_per_unit` 时失败关闭；只加载分组不需要形成价格，因此不得依赖 `/api/status`。所有显式 `0` 倍率必须保留，不能被默认值覆盖；所有派生乘积都必须检查溢出和非有限结果。

NewAPI 的 `UserAuth` 成功后会返回 `Auth-Version` 响应头。HTTP 200、`success=false` 必须结合该头分类：存在 `Auth-Version` 说明认证已完成，错误属于业务/数据库响应；缺少该头且消息明确指向 token、用户 ID、封禁或权限问题时才归类为认证错误。单个 `/api/user/models?group=` 的业务错误只隔离对应分组；`/api/user/self` 的业务错误使余额 unknown/partial，但不阻断可靠价格同步。

## NewAPI 交叉验证与映射规则

### 分组和倍率

- `/api/user/self/groups` 是账号可用分组和实际倍率的唯一权威来源，分组名按原始文本精确匹配。
- 不得用无 `UserAuth` 的 `GET /api/user/groups` 替代账号端点；未认证视图不能证明当前 Access Token 所属账号的分组权限。
- `ratio` 是 `number|string` 联合类型，DTO 必须先保留原始 JSON 类型再解析。
- 有限正数表示普通稳定倍率；数值 `0` 是官方支持的免费组，必须保留为显式 `GroupRatio=0`，token、per-request 和表达式成本均为已知零成本，不能回退为 `1`。
- 负数、NaN、无穷或无法解析的数值是无效倍率。
- `auto` 分组返回字符串倍率（官方中文值为“自动”）。它会在实际请求时动态选择具体分组，当前成本快照没有稳定倍率可绑定，因此必须标记为不支持并失败关闭，不能按 `1x`、最低倍率或公共目录倍率估算。
- “加载分组”可以在 `upstream_group` 为空时只读取账号分组；正式 Test 和定时同步必须绑定一个可形成稳定成本的具体分组。

### 按组模型和价格目录

每个 binding 的成本快照必须按以下顺序形成：

```text
/api/user/self/groups 中存在绑定组及稳定倍率
  ∩ /api/user/models?group= 返回的账号可用模型
  ∩ /api/pricing 中存在可归一化价格的同名模型
  ∩ /v1/models 中 Gateway Key 实际可服务的同名模型
  -> 当前 binding 的版本化成本快照
```

- `group` 查询参数必须 URL 编码；模型列表按集合去重并确定性排序。
- 官方对账号不可用分组可能返回 HTTP 200、`success=true`、空 `data`；因此必须先验证分组存在，再把空模型列表视为当前 binding 不可同步。
- 目录中存在而账号模型列表中不存在的模型不能写入；账号模型存在但目录缺价时，当前 binding 整体失败，不能产生部分模型快照。
- 等价管理凭据批次内可以共享 `/self`、`/self/groups`、`/pricing` 和按组模型结果，但不同 Access Token/CA/出站策略必须独立取数与隔离错误；不能只信任排序最前的令牌并让它代表其他凭据。Gateway 模型结果也只能在等价 Gateway Key/CA/出站策略内共享。
- 绑定组不存在、`auto`、空模型或缺价格只影响对应 binding；同账号其他健康分组仍可更新，账号汇总状态记为 partial/degraded。认证失败、公共目录整体不可用或全局协议损坏才是账号级失败。
- Gateway Key 被拒绝或 Gateway 模型与管理/价格交集为空只影响使用该 Key 的 binding；不得用管理模型集绕过服务能力验证。

### NewAPI 请求维度完整性

`/api/pricing` 的 ratio/price 字段不等于完整的每请求报价。官方标准计费还可能叠加 web search、file search、image-generation call 等工具费用；固定图片价格还可能乘以请求尺寸/质量对应的 `ImagePriceRatio`。这些字段没有由当前用户价格目录完整公开。

- `confidence` 只描述价格来源和推导质量，不能替代请求维度完整性；`known=true` 必须覆盖当前请求所有可能收费项。
- `RequestPricingFeaturesKnown` 只能由明确识别的协议 envelope 或专用计费路径产生。未知/custom 路径即使能解析成 JSON object，也必须保持 unknown；不能因为字段形状“看起来像 OpenAI”就假定已覆盖所有附加费。NewAPI catalog scope 与 Sub2API display contract 都必须检查该标记。
- 自动 cache read 的 token 数在上游响应前通常未知。cache write 只有在请求属于已识别协议、JSON 成功解析、没有 `cache_control`、`prompt_cache_key`、`prompt_cache_options`、`prompt_cache_retention`，且不依赖远程会话状态时，才能证明为已知零。
- cache read 与 cache write 的 known 状态必须分开；旧的 combined known 只作为兼容输入。表达式中的 `cr` 与 `cc`/`cc1h` 也必须分别依赖对应状态。
- 启用未公开工具附加费的请求、固定图片动态倍率请求，以及其他无法从目录与 RequestProfile 完整重建的收费维度必须返回 unknown；不能把基础 token 价当成完整 exact 成本。

## Sub2API 官方端点与响应契约

| 用途 | 端点 | 认证 | 关键响应 |
| --- | --- | --- | --- |
| 登录 | `POST /api/v1/auth/login` | 邮箱 + 密码 | `data.access_token`, `data.expires_in` |
| 当前账号 | `GET /api/v1/auth/me` | JWT | `data.id`、`data.email`、`data.username`、`data.balance` |
| 可用分组 | `GET /api/v1/groups/available` | JWT | `data[].id` 为 JSON number，另有 `name`、`platform`、倍率 |
| 用户专属倍率 | `GET /api/v1/groups/rates` | JWT | `data` 为以数字分组 ID 字符串化后的 map |
| 可用渠道与展示价格 | `GET /api/v1/channels/available` | JWT | `data[].platforms[].groups[]` 与 `supported_models[].pricing` |

`/groups/available` 与 `/channels/available` 的分组元数据都包含 `id`、`name`、`platform` 和 `subscription_type`。当前官方 `subscription_type` 只允许 `standard` 与 `subscription`：前者按账号钱包余额扣费，后者按有效订阅及日/周/月限额控制。缺失、空值或任何未知枚举都属于协议漂移，必须失败关闭，不能默认成 `standard`。

以上管理端点使用统一的 `{code,data,message}` envelope，成功条件为官方成功码和结构合法；不能把 HTTP 200 自动视为成功。旧版扁平 `channels[]`、把渠道 `name` 当模型名或把数字分组 ID 强制解码为 string 的 DTO 都不再属于正式契约。

官方 JWT 中间件对缺失、格式错误、过期、撤销、用户停用等认证失败统一返回 401。合法 JWT 在 backend mode 下会先通过认证，再由 `BackendModeUserGuard` 返回 403，表示用户自助管理面被配置关闭。因此 401 才是 JWT 失效信号；这些用户管理端点的 403 必须读取受限错误 envelope 并按 capability/configuration 分类，不能清理 JWT cache、触发邮箱密码重登或提示管理员更换凭据。

`GET /v1/usage` 是 API Key gateway 路由，返回原始 JSON，不使用 `{code,data}` 管理接口 envelope。管理 JWT 不得调用该端点。未来若需要展示 API Key 配额，必须增加独立字段和独立来源类型，例如 `gateway_key_remaining`，不能写入账号 `balance`。

当前连接器只使用官方 `GET /api/v1/auth/me` 读取钱包，不猜测或探测其他 profile 路由。若未来兼容其他 Sub2API 版本，必须先用对应官方仓库和契约测试确认路径与响应，再增加显式、可测试的版本分支。

## Sub2API 映射规则

### 分组

- 分组 ID 使用 `int64` 解析，不能定义为 string 直接解码。
- 绑定的 `upstream_group` 可与官方分组名称或十进制 ID 精确匹配，以兼容现有名称配置和新 ID 配置。
- “加载分组”只依赖 Base URL 和管理凭据，不要求先填 `upstream_group`；新建连接器可先发现账号全部官方分组，再选择绑定值，不能形成“先填正确分组才能加载分组”的循环。
- “加载分组”只调用 `/api/v1/auth/me` 和 `/api/v1/groups/available`；当前响应不展示倍率，因此不需要 `/api/v1/groups/rates`，也不得因为 `/api/v1/channels/available` 未启用、为空或价格暂不可用而阻断分组选择。正式 Test 和定时同步仍必须读取并验证专属倍率与完整渠道价格。
- “加载分组”的 `group_meta` 必须保留官方十进制 ID、名称、平台、`subscription_type` 和 `claude_code_only`，不能只把响应压缩成名称列表后丢失计费/安全属性。前端选项与选中说明必须展示订阅组语义，不能让管理员误以为账号钱包余额会驱动该渠道熔断。
- 正式同步必须按官方数字 ID 关联 `/groups/available` 与 `/channels/available` 的嵌套分组，并校验同一分组的名称、平台和 `subscription_type` 一致。缺字段、未知枚举、同一 ID 元数据冲突或嵌套分组无法回连权威分组时，对应 binding 失败并保留旧快照；不能只收集 alias 后忽略协议漂移。
- 分组索引必须保留 canonical 数字 ID，并只把唯一名称加入快捷 alias。若某个管理员输入同时命中一个分组 ID 和另一个分组名称，只对该输入返回 ambiguous；无关健康分组仍可被选择、测试和同步，不能在 parser 阶段把 alias 冲突升级成账号级失败。
- 同一官方账号的网络取数可以共享，但分组元数据校验、渠道裁剪和价格展开必须按每个 binding 的目标分组独立执行。一个可识别但损坏或漂移的未绑定分组不得阻断健康分组更新；坏分组只让引用它的 binding 失败、退避并保留旧快照。只有 envelope、顶层 `data` 类型或无法安全识别所属分组的结构损坏，才能作为账号级协议失败拒绝整批。
- 为避免隔离导致请求量退化，canonical Base URL、管理凭据、CA 和 egress 策略相同的 binding 应共享一次 `/auth/me` 身份预检，并在同一轮目录取数中复用该 profile 及其真实观测时间；不得用后续目录完成时间伪装余额刚刷新。目标分组相同的 binding 再共享一次完整目录取数。不同分组或不同出站/认证等价类必须分批。托管 JWT 若在复用后收到 401，单次重登重试必须重新读取 `/auth/me` 并更新身份、余额和观测时间，不能沿用旧 profile。`groups/rates` 中可归属到未选分组的非法 value 只隔离该分组，非法/非十进制 key 等无法安全归属的结构错误仍按账号级协议失败处理。
- `/groups/rates` 的倍率按分组 ID 关联；专属倍率优先于分组默认 `rate_multiplier`。
- 绑定分组必须存在。若没有有效的用户专属倍率，官方默认 `rate_multiplier` 必须是有限正数；缺失、零、负数或非有限值均视为协议/数据错误，不能静默按 `1x` 继续。
- 官方峰时倍率依赖上游服务器时区和当前时间。当前用户端点没有提供足够的可复现时区上下文，启用 `peak_rate_enabled` 的分组必须失败关闭，不能只取普通倍率。
- 分组和平台过滤必须在读取模型价格前完成，不能把其他分组或其他平台的价格带入当前绑定。

### 订阅组余额语义

Sub2API 官方将 `subscription_type=standard` 定义为按账号钱包余额计费，`subscription_type=subscription` 定义为按订阅限额控制。`/auth/me.balance` 仍可作为账号级信息存储和展示，但对绑定订阅组的渠道：

- 不得把钱包余额复制为该渠道的 serving balance，也不得因为钱包为零/负数打开余额熔断。
- 正式同步必须清理该渠道已有的数据库余额状态和余额热缓存，保留与模型服务无关的 auth failure 状态。
- 快照重建或进程重启后，已启用成本连接器的渠道在 connector balance 缺失/未知时不得回退历史 `Channel.Balance`；否则订阅组和余额未知的标准组都会被旧值冒充。只有未配置成本连接器或连接器已禁用的旧渠道可保留 legacy balance 兼容。
- binding 从标准组切换为订阅组时必须清理旧钱包 marker；从订阅组切回标准组时，只有新的正式同步取得已知余额后才能重新建立钱包健康信号，不能沿用切换前的旧值。
- 订阅剩余额尚未由当前用户端点可靠公开；不得用钱包余额、Gateway Key quota 或猜测值冒充。后续若引入订阅额度端点，必须使用独立字段和来源类型。
- `subscription_type` 缺失或未知时不得按标准钱包组继续同步，也不得用“账号余额正常”掩盖契约错误。

### 嵌套渠道

官方结构为：

```text
channel
  -> platforms[]
       -> groups[]
       -> supported_models[]
            -> pricing
```

连接器应展开为当前项目的一条模型一份标准化价格。展开后必须按本地模型映射处理，并拒绝同一 binding 下映射到同一本地模型但价格冲突的重复项。

同一本地模型、相同成本字段但来自不同 platform 元数据时可以确定性去重；platform 只用于来源说明，不能单独造成“价格冲突”。去重指纹只能排除明确非计费的 `platform`，`source_billing_mode`、`price_unit` 和未来未知扩展字段仍需参与比较。只要任何实际计费字段、表达式、单位或阶梯不同，就必须拒绝冲突，不能按返回顺序任选一条。

`channels/available` 功能关闭时官方会返回成功 envelope 和空数组。空数组、没有任何有效模型价格或只有无法映射的价格不能作为成功的“零价格”同步，否则会删除上一份有效快照。此类情况应返回能力/协议错误并保留旧快照。

该端点的价格字段是用户界面展示契约，不是完整 BillingService 契约。官方会为缺少渠道定价的模型合成全局目录展示价，并明确该 fallback 不参与真实渠道计费；响应又不公开 `pricing_source`、priority 专价、1 小时 cache write 价、长上下文阈值/倍率及账号级开关。因此：

- token interval 不由展示 fallback 合成，可视为显式渠道区间价；命中区间的 standard/auto/default/scale 和 priority/fast 可按区间价格计算，未命中仍为 unknown。
- interval 或 flat 的 `flex` 只有在确认该候选最终会把 service tier 透传，并应用官方 `0.5x` 后才能 known；候选级透传状态不明确时必须 unknown。
- flat 的普通 tier、无 1 小时 cache write、且未进入长上下文条件时可作为 derived 基础价；flat priority/fast、1 小时 cache write、OpenAI/Gemini 长上下文风险必须按请求降为 unknown。
- 不得直接把整个 flat snapshot 标成 unknown 并中止 binding；应保留 derived 基础快照，在请求估算阶段按实际维度决定 known。长期应推动上游公开 `pricing_source` 和完整有效计费元数据，或提供只读 effective quote 端点。

### Claude Code 限制

Sub2API 的 `claude_code_only` 权威字段来自 `/api/v1/groups/available`；正式嵌套 `/channels/available` 的 group 白名单不包含该字段。连接器必须按数字 ID/名称把两端结果关联，不能只读取展开后的 channel 字段。

`claude_code_only` 绑定未显式设置 `serves_claude_code` 时，正式同步必须失败关闭且保留旧快照。设置后，数据面必须遵循以下准入语义：

- 只对 Claude relay 请求执行 Claude Code 分类；OpenAI chat/responses 等格式即使带相似 User-Agent 仍是 `standard`。
- User-Agent 必须匹配 `(?i)^claude-cli/\d+\.\d+\.\d+`。普通 `/messages` 还必须同时具有官方 system prompt 或 billing attribution block、`X-App`、`anthropic-beta`、`anthropic-version` 和合法 `metadata.user_id`。
- `/messages/count_tokens` 和 `max_tokens=1 + haiku` 探测遵循官方快速路径。User-Agent 匹配但普通消息证据不完整时必须标为 `unknown`，不能乐观放行。
- `standard`、`unknown` 和缺少新字段的历史 `legacy` 请求都必须排除 `serves_claude_code=true` 渠道；`claude_code` 请求可以使用专用渠道，也可以使用普通渠道，避免专用渠道故障时无谓失去健康后备。
- 约束必须覆盖 Shadow、Canary、Balanced、智能候选、传统随机、亲和、指定渠道和最终 Setup 复核。传统随机必须在优先级与权重抽样前过滤，不能先抽到受限渠道再失败。
- 分类写入 V1/V2 共用的可选 `traffic_class`；历史 JSON 缺字段时继续省略，旧 ProfileHash/Replay SnapshotHash 不变。关闭 RequestProfile V2 实验时，V1 仍必须携带分类，安全边界不能依赖实验开关。
- 渠道策略缓存与成本缓存独立，必须有初始化状态、短 TTL 的数据库刷新和 binding CRUD 定向失效。缓存不可用或策略读取失败时，受限准入失败关闭。

该分类复现 Sub2API 当前官方协议指纹，用于流量兼容性约束，不是对客户端主体的密码学认证。需要更强来源隔离的部署仍应把专用渠道放入独立 routing pool，并限制谁能向该入口发起请求。

## 价格单位与计费表达式

### 扁平 token 价格

Sub2API 的 `input_price`、`output_price`、`cache_read_price`、`cache_write_price` 和 `image_output_price` 单位都是 USD/token。例如 `0.000003` 表示 `$3 / 1M tokens`。

```text
input_cost_per_million = input_price * 1_000_000
output_cost_per_million = output_price * 1_000_000
image_output_cost_per_million = image_output_price * 1_000_000
base_ratio = input_price * QuotaPerUnit
completion_ratio = output_price / input_price
```

只有分母为有限正数时才能计算 ratio。所有价格和倍率必须为有限、非负数；NaN、正负无穷或负数均使该价格无效。

缓存价格同样乘以 `1_000_000` 后写入每百万 token 成本。未公开的价格不能用 `1`、最低档或其他猜测值代替。

缺字段的语义必须与官方 resolver 一致：

- flat token 价格缺少 input/output/cache read/cache write 中任一字段时，真实计费会继承未公开的全局基础价，连接器不能猜测，必须拒绝该 flat 价格。
- 命中 token interval 后，区间中缺失的 token 维度按官方构造语义为显式 `0`，不回退 flat 或全局价。
- 只要渠道级 pricing override 存在，缺失 `image_output_price` 就是显式 `0`，不能回退普通 `output_price`；显式零也必须保留。

Sub2API 的 `per_request_price` 单位是 USD/request，标准化快照的 `price_unit` 必须写为 `usd_per_request`；token 和 token interval 才使用 `usd_per_token`。

### Alpha Search 按次费用

Sub2API 的专用 Alpha Search 路径（包括 `/v1/alpha/search`、`/alpha/search` 和 `/backend-api/codex/alpha/search`）会按实际搜索调用计费。官方 `web_search_price_per_call` 语义为：字段缺失时默认 `$0.01 / call`，显式 `0` 表示免费，最终费用再乘当前分组倍率；成功搜索请求记录一次 `WebSearchCalls`。

该字段不能无条件写成模型快照的 `PerRequestCost`，否则普通 chat、responses 或 messages 请求也会被错误加价。当前请求画像和展示目录尚未形成可审计的搜索调用数量、重试/失败转移计费与分组字段快照，因此：

- 任意以独立路径段结尾的 `/alpha/search` 请求都必须标记 `UncataloguedSurchargePossible=true`，并保留 `RequestPricingFeaturesKnown=true`；尾斜杠、合法基础路径和代理前缀必须命中，相似的 `not-alpha/search` 或 `search-preview` 不得误命中。
- Sub2API 基础 token 价格即使完整，这类请求的预估成本也必须返回 unknown；不能把 `$0.01`、显式零或最低可能费用猜成固定请求费。
- 未来只有在成本快照显式保存该分组的 `web_search_price_per_call`，RequestProfile 能表达实际搜索调用次数，并明确重试/失败转移的计费边界后，才可以把该维度转成已知成本。

### token 阶梯价格

官方 `intervals` 的 token 区间执行语义是左开右闭：

```text
total_tokens > min_tokens &&
(max_tokens == null || total_tokens <= max_tokens)
```

当前项目的分层表达式必须：

- 使用真实 `$ / 1M tokens` 系数。
- 使用 `len` 判断输入上下文档位，不能使用会因缓存拆分而变小的 `p`。
- 每个分支使用 `tier("label", ...)` 记录命中档位。
- 保留 input、output、cache read 和 cache write 的可表达价格。
- 先验证区间边界、重叠、数量、表达式大小和价格有限性。官方允许首档 `min_tokens > 0`、区间缺口和有界末档；这些结构不能被错误拒绝。
- Sub2API 在区间未命中时会回退到未由用户端点公开的私有基础价格。生成的表达式使用项目保留 tier 标记该分支；只要请求仍有任何可计费 token，路由成本估算必须返回 unknown，不能接受占位值 `0`，也不能猜测为第一档或最低价。
- 所有计费 token 都为零时，未命中分支可以安全返回已知零成本。

### per-request 与 image 阶梯

Sub2API 的 `per_request` 和 `image` interval 可能按 `tier_label`、图片尺寸或其他请求字段选择。当前请求画像若不能可靠提供同一选择变量，就不能把这些 interval 转成 token 表达式，也不能选择最低价。应将该模型标记为 unknown；后续只有在请求画像和表达式语言能无损映射时再启用。

## 请求与失败语义

- 所有 Base URL 必须是 canonical HTTPS origin 加可选基础路径；禁止 URL userinfo、任何 query 参数和 fragment。账号 identity 规范化 scheme/hostname、DNS 尾点、根路径/尾斜杠和默认 `:443`，保留非默认端口、路径大小写和不同基础路径；连接器端点必须通过结构化 URL 拼接形成，不能把 `/api/...` 直接拼到带 query/fragment 的字符串后。
- 默认拒绝回环、link-local、metadata、私网和 DNS rebinding；私网只能通过显式 CIDR allowlist 开启。
- 自定义 CA 只扩展指定 binding 的信任，不能关闭系统证书校验。
- 限制响应状态、Content-Type、压缩后大小、JSON 深度/节点数和令牌长度。
- 401 和官方明确认证失败的 envelope 只标记成本同步认证失败，不得污染模型服务链路的 serving health。403 必须按端点协议分类；例如 NewAPI `pricing is disabled` 和 Sub2API backend mode 的用户管理面禁用都属于配置/能力错误，不得误标为凭据失效，也不得清 JWT 或重登。
- 网络错误、5xx、响应过大、JSON 不合法、空有效价格和未知协议均视为同步失败，保留上一份有效成本快照。
- 成功同步返回的模型集合是当前 channel 的 latest 权威集合：已移除模型从 latest 数据和该 channel 成本热缓存中原子删除，但不可变历史版本保留。任何映射、协议或持久化失败都必须回滚并保留上一份完整集合。
- 余额失败但价格成功时可记录 partial；认证失败不能降级为 partial。
- Test 和“加载分组”是只读预览。它们可以调用 `/api/user/self` 或 `/api/v1/auth/me` 读取余额以验证认证和响应，但不得调用余额持久化、不得更新 binding 的数据库健康状态、成本快照或余额热缓存；托管 JWT 仍可按正常认证生命周期进入加密缓存。Sub2API Test 必须只校验当前 binding 的目标分组，不能被未绑定坏组拖失败；加载分组继续使用账号级 discovery。只有正式定时同步在 binding 版本仍匹配时提交余额与价格。
- Sub2API Test 和“加载分组”都必须解析并验证 `/auth/me.data.id` 为正 `int64`；成功 envelope 但 ID 缺失、为零、负数、小数、溢出或错误 JSON 类型时都失败，不能先显示成功、再让正式同步失败。
- 已保存 binding 与内联草稿使用同一套 action readiness 校验：Test 要求非空 `upstream_group`，加载分组允许为空；两者都必须在发请求前验证 Base URL、管理凭据和 NewAPI User ID。
- 同账号的凭据认证与分组级错误必须按 binding 隔离；失败 binding 保留旧快照并进入退避，健康 binding 正常更新。binding 需保存由官方身份确认后得到的不可逆 account-key hash，配置/凭据变更时清空，以便没有历史价格快照的 backoff binding 仍能精确回连账号。账号 partial/degraded 汇总必须包含同账号仍启用且处于持久失败/backoff 的 binding，健康 sibling 的下一轮成功不能把账号提前恢复为 active。失败状态与账号降级必须在同一事务提交；健康快照、账号状态与所有当前 failure/backoff fences 也必须原子提交。汇总只能计入经 binding fencing 确认仍当前的失败；NewAPI 未认证失败只有在失败源自证或至少一个仍当前的已认证 sibling 提供最小确认 fence 时才能创建/降级账号，已轮换、禁用或删除的 stale binding 不得间接提供确认。在 `/auth/me` 已确认官方 Sub2API 身份后，后续 groups/pricing 失败可以通过确认身份的当前 binding 创建 degraded 账号；认证失败、缺 ID 或非法 profile 仍不得创建。
- 错误对管理员可定位，但必须经过凭据脱敏；普通用户不得看到 `admin_info` 或上游秘密。
- `confidence=derived` 仍可能参与选路，因此不能用它掩盖缺失收费项。只有当前 RequestProfile 的所有计费维度均可重建时才能返回 `known=true`；否则必须条件降级为 unknown。

## 退役后的保留位置

- 历史 binding 表结构与本地敏感字段清除迁移：`model/routing_model.go`、`model/routing_channel_configuration.go`
- 不可变成本版本与历史读取：`model/routing_cost.go`
- 旧接口 410 Gone：`controller/channel_routing_retired.go`
- 当前渠道倍率与流量范围：`model/routing_channel_configuration.go`、`service/channelrouting/channel_configuration.go`

旧表仅保留一个兼容周期并由本地幂等迁移清除敏感字段；Redis 客户端可用时同步清理旧 JWT/锁 key，客户端未接入时不阻塞数据库初始化，因为退役后已不存在任何缓存读取方。运行时不得重新注册管理连接器、自动同步任务或凭据 API。未来若重新引入 provider，必须作为新设计重新审计本文官方契约，不能复活已退役实现，也不能把 provider 特有字段泄漏到通用选路逻辑。

## 验收标准

1. NewAPI 使用 Access Token + User ID 成功读取 `/api/user/self`、`/api/user/self/groups` 和按组 `/api/user/models`，并使用独立 Gateway API Key 读取 `/v1/models`；管理与服务端点各自发送正确认证头，不互相后备。
2. `/api/pricing` 即使返回与账号冲突的 `group_ratio`、`usable_group`、`enable_groups`，最终分组、倍率和模型仍只服从账号端点、价格目录与 Gateway `/v1/models` 四方交集；Gateway 不可服务的管理模型不得写入。
3. NewAPI 数字 `0` 倍率保持免费组语义并生成已知零成本；`auto`、负倍率、缺组、空模型和账号模型缺目录价格均失败关闭，不按 `1x` 推测。
4. 同一 NewAPI 账号一个旧令牌 401、一个新令牌有效时，两个 Access Token 各自完整验证且只退避旧凭据 binding；一个坏分组、一个健康分组时也只隔离坏 binding，健康 binding 仍写入。不同 Gateway Key 的 `/v1/models` 结果同样不互相代表。
5. NewAPI 余额只取 `quota / upstream_quota_per_unit`；token 价格按上下游 QPU 换算，不减 `used_quota`，缺少 `quota` 或正式同步缺少有效 QPU 时保持 unknown/失败关闭。
6. NewAPI cache read、5m/1h cache write 和 audio input/output 按官方默认及显式零语义归一化；普通无缓存控制请求不会因默认 cache-write 价被误判 unknown，自动 cache read 和显式缓存控制仍失败关闭。
7. NewAPI 含工具附加费、search-preview 固定搜索费、固定图片动态倍率或其他目录未公开维度的请求不能把基础价格标为完整 known；pricing 模块关闭的 403 不被误判为认证失败。
8. 匿名 pricing 缺少账号私有模型且无 `Auth-Version` 时，错误提示目录 scope 不完整并建议 `pricing.requireAuth=true`；开启认证后可同步私有目录，任何失败都保留旧快照。
9. Sub2API 显式 JWT 与邮箱密码登录两种方式都能读取 `/auth/me`、分组、专属倍率和嵌套渠道；交互式登录挑战失败关闭。Test 与加载分组同样拒绝缺失、零、负数、小数、溢出或错误类型的官方用户 ID。401 仍按认证失败处理且托管 JWT 最多重登一次；backend-mode 403 按能力/配置失败处理，不清缓存、不重登、不误报凭据。
10. 相同 canonical Base URL + 相同 `/auth/me` 正整数用户 ID 在显式 JWT 轮换后复用同一 AccountID；Test、加载分组和定时同步都拒绝缺 ID/非法 profile。官方 ID 已确认后的 groups/pricing 失败可创建 fenced degraded 账号，认证、缺 ID 或非法 profile 失败不创建凭据派生账号，账号字段和错误不包含 JWT、JWT 尾部、密码或邮箱。
11. Sub2API 官方 wire DTO 严格要求 `/auth/me`、`groups/available` 和嵌套渠道的 ID 为 JSON number，groups/channels 的 `data` 为数组、rates 的 `data` 为以十进制 ID 为 key 的数值 map；string ID、旧 wrapper alias、rates array/null 和扁平渠道 shape 都被拒绝。管理员填写的 binding 仍可用分组名称或十进制 ID，并能选中同一官方数字分组。`subscription_type` 只接受 `standard`/`subscription`，且两个官方分组端点的 ID、名称、平台和订阅类型必须一致。
12. `$3/MTok` 的官方 `input_price=3e-6` 被归一化为 `input_cost_per_million=3` 和正确 `base_ratio`；`image_output_price` 同样乘以 `1_000_000`，per-request 单位为 `usd_per_request`。
13. flat 缺继承维度失败关闭；命中 interval 后缺字段和渠道 override 缺 `image_output_price` 按官方显式零语义处理。
14. token intervals 生成使用 `len` 的分层表达式，边界命中与官方左开右闭行为一致；合法首段空白、区间 gap 和有界末段不会被误拒。
15. interval 未命中的非零请求成本为 unknown；所有 token 为零时仍是已知零成本；远程上下文无法确定 interval 时同样 unknown。
16. Sub2API 展示价格的请求门控覆盖 flat priority/fast、flex 透传不确定、1h cache write、OpenAI/Gemini 长上下文和独立 Alpha Search 按次费用；这些场景不会因为 `confidence=derived` 而继续成为 known。`/alpha/search` 的正式路径、尾斜杠和合法基础路径前缀进入 unknown，相似路径不误命中。命中 interval 且上下文可确定的安全场景保持可用；历史 NewAPI/Sub2API latest 缺少当前来源 contract metadata 时，在主选路与 shadow replay 中均返回 unknown。
17. `claude_code_only` 从 `/groups/available` 关联；binding 未声明 Claude Code 用途时成本同步失败关闭。官方完整指纹、count-tokens/haiku 探测快速路径、证据不全的 `unknown`、OpenAI 格式隔离、V1/V2 兼容及所有选路出口均有回归；非 `claude_code` 流量不能进入 `serves_claude_code` 渠道。
18. 同一 Sub2API 账号一个分组缺价、坏倍率值、元数据漂移或无渠道而另一个健康时，健康 binding 正常更新，失败 binding 保留旧快照并退避；失败 binding 仍在 backoff 时，健康 sibling 再同步不会提前把账号恢复 active；已经轮换、禁用或删除的失败 binding 不能借健康 binding 间接降级共享账号。同认证/CA/egress 的 binding 共享一次 `/auth/me`，同组等价 binding 再共享一次目录请求，不因隔离退化为逐 binding 重复取数。
19. 未预选分组时仍能加载账号全部分组，Sub2API 分组发现不依赖 `channels/available`；返回的 `group_meta` 保留 `subscription_type` 并在前端解释订阅组钱包余额不参与路由。进程重启/快照重建后订阅组和余额未知的启用连接器都不会回退 legacy `Channel.Balance`；禁用连接器或无连接器的旧渠道仍保留兼容。正式 Test 缺绑定组会在请求前失败，非空组 Test 只验证该目标组。Test/加载分组即使读取账号资料或余额，也不修改数据库或热缓存。数字名称与另一分组 ID 冲突时，只拒绝真正 ambiguous 的 binding 值，不阻断无关健康组。
20. per-request/image 未知阶梯、峰时倍率、空 `channels/available`、无有效价格和计费元数据冲突都失败关闭，不覆盖旧快照。
21. JWT 调用路径不访问 `/v1/usage`；Gateway API Key quota 不写入账号 balance。订阅组清理 channel 钱包 marker，账号钱包为零不会触发该 channel 的余额熔断；切回标准组后必须由新同步重建余额信号。
22. 托管 JWT 过期只重登一次；显式 JWT 不自动替换；轮换期间旧登录不能重新发布。
23. Base URL 含任意 query、空 query marker 或 fragment 都在请求前拒绝；合法 HTTPS 基础路径仍可使用。
24. 凭据不出现在 API 明文、日志、错误、Redis key、测试输出或 Git diff 中。
25. 成功同步的权威模型从 `A+B` 缩为 `A` 时，latest 与热缓存都删除 `B`、历史版本仍保留；失败同步继续保留 `A+B`。已 stale 的失败 binding 不得降级共享账号，健康 binding 也不得把同账号的真实失败重写为 active/success。
26. 定向单元测试、相关包 Race、串行全仓测试、Vet、Build 和前端契约测试通过；无法完成的环境验证明确记录为未验证，且不得据此宣称全仓 Race clean。

## 待确认事项

- Sub2API 部署若启用了 Turnstile、TOTP 或其他交互式登录挑战，运维是否统一要求填写显式 JWT；本模块不会绕过这些安全机制。
- Sub2API `channels/available` 默认是可选能力。上游未开启时，需要由上游管理员启用，还是未来增加其他只读定价端点作为正式后备。
- 是否推动 Sub2API 增加 `pricing_source`、priority、1h cache write、长上下文规则或 effective quote 用户端点；在此之前按上述请求维度门控 unknown。
- 是否把 Claude Code 专用渠道进一步放入独立 routing pool，并对入口增加主体授权；当前协议指纹已形成自动兼容性准入，但不替代客户端身份认证。
- 若旧版本已经产生 token/email 派生的 Sub2API 账号行，后续是否提供只读迁移审计或人工合并工具；不得自动删除或改写不可变成本历史。
- 是否在后续版本增加独立的 Gateway API Key 用量面板；如增加，必须与账号钱包、成本同步和路由健康完全分栏。
