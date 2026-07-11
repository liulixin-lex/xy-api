# 渠道路由 Phase 0B 错误、容量与 Multi-Key 语义实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立可复用的错误责任分类器，保留真实上游错误证据，把 429/529 与 Retry-After 从 Reliability Breaker 分离到有界 Capacity Cooldown，并让 Multi-Key 在稳定 Credential ID 上线前不再写入或消费不可信的 index/aggregate 路由状态。

**Architecture:** NewAPIError 同时保留原始 cause、原始上游状态码和对客户端展示的响应状态；pkg/routing_error 以结构化 code、signal、component 和 source status 生成责任、作用域、重试、健康与容量效果。Controller 每次尝试只分类一次，再把同一结果分发给 reliability metrics、Reliability Breaker、Capacity Cooldown 和重试策略。Capacity 复用 routing_hotcache 的有界 Map；Multi-Key 安全降级为不参与 smart-routing metrics/breaker/inflight/capacity，现有 Key 启停状态按 raw Key 重排，跨进程稳定身份留给 Phase 1 RoutingCredentialRef。

**Tech Stack:** Go 1.22、Gin、GORM v2、Testify、现有 common JSON 包装、SQLite/MySQL/PostgreSQL 兼容迁移与测试。

---

## 已选方案与明确边界

### 方案比较

1. 继续按 HTTP 状态码在 Controller 中分散判断：改动少，但状态码映射会继续改变健康语义，Task、内容安全和网络 cause 仍不一致。
2. 只让 Breaker 忽略 429/529：能阻止立即熔断，但 429/529 仍污染 availability，也没有跨请求 cooldown。
3. 统一分类器 + 独立 Capacity Hot Cache + Multi-Key 安全降级：改动较多，但能在不引入 Phase 1 数据模型的前提下闭合错误、可靠性、容量和 Key 作用域。

采用方案 3。

### 本阶段必须完成

- 用户 400/413、用户额度不足、本平台异常和内容安全错误不降低 reliability availability，不进入 Reliability Breaker。
- 状态码映射只改变下游响应状态；分类、metrics 和 capacity 使用 source status。
- 429、529、402 和有效 Retry-After 写入独立、有界、可过期、可观测的 Capacity Cooldown。
- 500、502、503、504、首字前超时、网络错误和响应损坏进入 Reliability Breaker。
- 旧 Breaker 持久化状态通过 semantic version 失效，避免历史 429/529 窗口重新 hydrate。
- Selector availability 使用 ReliabilityRequestCount / ReliabilityFailureCount，不再使用全部请求的 SuccessCount / RequestCount。
- 有替代渠道时不因 Retry-After 睡眠；Retry-After 只冷却失败目标。
- Task submit 没有稳定幂等键时，idempotency_required 错误不自动跨渠道重试。
- 定价 connector 的 401/403 只保留 binding sync error/backoff，不再标记 serving channel auth failure。
- 单 Key 继续使用 APIKeyIndex=-1；Multi-Key 不写、不持久化、不读取 smart-routing metrics/breaker/inflight/capacity。
- Multi-Key replace/reorder 时，现有启停状态按 raw Key 重映射；无法唯一匹配时清零，不把旧 index 状态转给新 Key。

### 本阶段明确不做

- 不创建 RoutingCredentialRef、CredentialID、RoutingPool 或 RoutingMember；这些属于 Phase 1。
- 不把 HMAC、hash 或 fingerprint 塞进 APIKeyIndex。
- 不增加 Capacity 数据库表、Redis 容量租约、RPM/TPM/并发 Token Bucket、AIMD 或公平份额。
- 不解析所有供应商 Remaining/Reset Header；只消费现有 Retry-After。
- 不重写所有 provider adaptor，不修改用户计费、billingexpr、quota saturation 或前端。
- 不部署生产、不执行生产迁移、不扩大主动选路流量。

## 文件职责

- types/error.go、types/error_test.go：保留 cause/source status/display message 三个独立错误事实。
- pkg/routing_error/classifier.go、classifier_test.go：纯错误责任分类矩阵。
- service/error.go、service/error_test.go：状态码映射与 TaskError 转换保真。
- model/routing_model.go、model/routing_model_test.go：Reliability 指标列、Err529、Breaker semantic version 与三数据库契约。
- pkg/routing_metrics/metrics.go、metrics_test.go：分类感知的 reliability 计数；Multi-Key 不记录。
- pkg/routing_hotcache/cache.go、cache_test.go：共享有界容器、统计、清理与 Reliability metric 字段。
- pkg/routing_hotcache/capacity.go、capacity_test.go：独立 Capacity Cooldown 计算、later-deadline 合并与查询 API。
- service/routing/types.go、selector.go、selector_test.go：availability 使用 reliability 样本。
- pkg/routing_breaker/breaker.go、breaker_test.go：只接受 reliability 状态，容量信号不创建或修改 Breaker。
- controller/relay.go、relay_retry_test.go：普通/Task attempt 的唯一分类结果扇出、重试与幂等边界、移除 Retry-After sleep。
- service/channel.go、channel_test.go：分类后的 serving credential 自动禁用边界。
- service/channel.go、service/channel_select.go 及测试：分类安全 overlay、容量硬过滤、Multi-Key 不消费旧路由状态。
- middleware/distributor.go 及测试：Multi-Key 只按 operational enabled state 选 Key。
- relay/common/relay_info.go、relay/relay_task.go 及测试：当前 attempt 的 Multi-Key context 为真源。
- model/channel.go、model/channel_multikey_test.go、controller/channel.go：Multi-Key operational state 按 raw Key 重排。
- controller/system_task_handlers.go、controller/smart_routing_sub2api.go、controller/smart_routing_task_test.go：cost connector health 与 serving health 分离。

### Task 1: 保留错误 cause 与原始上游状态

**Files:**
- Modify: types/error.go
- Create: types/error_test.go
- Modify: service/error.go
- Modify: service/error_test.go

- [x] **Step 1: 写 cause/display message 分离的失败测试**

在 types/error_test.go 增加：

~~~go
package types

import (
    "errors"
    "net"
    "net/http"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestNewAPIErrorHideMessagePreservesTypedCause(t *testing.T) {
    dnsErr := &net.DNSError{Name: "upstream.example.com", IsTimeout: true}
    apiErr := NewError(
        dnsErr,
        ErrorCodeDoRequestFailed,
        ErrOptionWithHideErrMsg("upstream request failed"),
    )

    assert.Equal(t, "upstream request failed", apiErr.Error())
    var extracted *net.DNSError
    require.ErrorAs(t, apiErr, &extracted)
    assert.Same(t, dnsErr, extracted)
}

func TestNewAPIErrorSetMessageDoesNotReplaceCause(t *testing.T) {
    cause := errors.New("internal cause")
    apiErr := NewError(cause, ErrorCodeDoRequestFailed)

    apiErr.SetMessage("public message")

    assert.Equal(t, "public message", apiErr.Error())
    assert.Equal(t, "public message", apiErr.ToOpenAIError().Message)
    assert.ErrorIs(t, apiErr, cause)
    assert.Same(t, cause, apiErr.Cause())
}

func TestNewAPIErrorResponseStatusMappingPreservesSourceStatus(t *testing.T) {
    apiErr := NewErrorWithStatusCode(
        errors.New("rate limited"),
        ErrorCodeBadResponseStatusCode,
        http.StatusTooManyRequests,
    )

    apiErr.SetResponseStatusCode(http.StatusServiceUnavailable)

    assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
    assert.Equal(t, http.StatusTooManyRequests, apiErr.SourceStatusCode())
}
~~~

- [x] **Step 2: 写状态映射保留 source status 的失败测试**

扩展 service/error_test.go：

~~~go
func TestResetStatusCodePreservesSourceStatusCode(t *testing.T) {
    apiErr := types.NewErrorWithStatusCode(
        errors.New("rate limited"),
        types.ErrorCodeBadResponseStatusCode,
        http.StatusTooManyRequests,
    )

    ResetStatusCode(apiErr, `{"429":"503"}`)

    assert.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
    assert.Equal(t, http.StatusTooManyRequests, apiErr.SourceStatusCode())
}

func TestTaskErrorFromAPIErrorKeepsPublicMessageAndWrappedCause(t *testing.T) {
    cause := errors.New("private transport detail")
    apiErr := types.NewError(
        cause,
        types.ErrorCodeDoRequestFailed,
        types.ErrOptionWithStatusCode(http.StatusBadGateway),
        types.ErrOptionWithHideErrMsg("upstream request failed"),
    )

    taskErr := TaskErrorFromAPIError(apiErr)

    require.NotNil(t, taskErr)
    assert.Equal(t, "upstream request failed", taskErr.Message)
    assert.ErrorIs(t, taskErr.Error, cause)
    assert.True(t, taskErr.LocalError)
}
~~~

补充 errors import。TaskErrorFromAPIError 处理的是本平台计费/前置错误，因此必须设置 LocalError=true。

- [x] **Step 3: 运行测试确认 RED**

Run: go test ./types ./service -run 'Test(NewAPIError|ResetStatusCodePreserves|TaskErrorFromAPIErrorKeeps)' -count=1

Expected: FAIL；Cause、SourceStatusCode、SetResponseStatusCode 尚不存在，HideErrMsg 会替换 cause。

- [x] **Step 4: 实现 cause/source/display 三分离**

在 NewAPIError 增加：

~~~go
type NewAPIError struct {
    Err              error
    displayMessage   string
    sourceStatusCode int
    RelayError       any
    skipRetry        bool
    recordErrorLog   *bool
    errorType        ErrorType
    errorCode        ErrorCode
    StatusCode       int
    Metadata         json.RawMessage
}

func (e *NewAPIError) Cause() error {
    if e == nil {
        return nil
    }
    return e.Err
}

func (e *NewAPIError) SourceStatusCode() int {
    if e == nil {
        return 0
    }
    if e.sourceStatusCode > 0 {
        return e.sourceStatusCode
    }
    return e.StatusCode
}

func (e *NewAPIError) SetResponseStatusCode(statusCode int) {
    if e == nil {
        return
    }
    if e.sourceStatusCode == 0 {
        e.sourceStatusCode = e.StatusCode
    }
    e.StatusCode = statusCode
}

func (e *NewAPIError) Error() string {
    if e == nil {
        return ""
    }
    if e.displayMessage != "" {
        return e.displayMessage
    }
    if e.Err == nil {
        return string(e.errorCode)
    }
    return e.Err.Error()
}

func (e *NewAPIError) SetMessage(message string) {
    if e == nil {
        return
    }
    e.displayMessage = message
}

func (e *NewAPIError) Unwrap() error {
    return e.Cause()
}

func (e *NewAPIError) initializeSourceStatus() {
    if e != nil && e.sourceStatusCode == 0 {
        e.sourceStatusCode = e.StatusCode
    }
}
~~~

所有构造器在 options 应用完成后调用 initializeSourceStatus。MaskSensitiveError 改为对 Error() 结果脱敏。ErrOptionWithHideErrMsg 只赋值 displayMessage，不替换 Err。

ToOpenAIError 与 ToClaudeError 在保留原 RelayError 的 type/code/param/metadata 后，以 Error() 覆盖最终 Message，确保 SetMessage 和隐藏消息确实作用于客户端输出。ErrOptionWithStatusCode 在已有 NewAPIError 上应用时不得清空已初始化的 sourceStatusCode。

在 service/error.go：

~~~go
func ResetStatusCode(newApiErr *types.NewAPIError, statusCodeMappingStr string) {
    // 保留现有解析和校验。
    // 命中映射后：
    newApiErr.SetResponseStatusCode(intCode)
}

func TaskErrorFromAPIError(apiErr *types.NewAPIError) *dto.TaskError {
    if apiErr == nil {
        return nil
    }
    return &dto.TaskError{
        Code:       string(apiErr.GetErrorCode()),
        Message:    apiErr.Error(),
        StatusCode: apiErr.StatusCode,
        LocalError: true,
        Error:      apiErr,
    }
}
~~~

- [x] **Step 5: 运行 GREEN**

Run: go test ./types ./service -run 'Test(NewAPIError|ResetStatusCode|TaskErrorFromAPIError)' -count=1

Expected: PASS。

- [x] **Step 6: 提交错误证据保真**

~~~bash
git add types/error.go types/error_test.go service/error.go service/error_test.go
git commit -m "fix: preserve routing error evidence"
~~~

### Task 2: 建立统一错误责任分类器

**Files:**
- Create: pkg/routing_error/classifier.go
- Create: pkg/routing_error/classifier_test.go
- Modify: dto/task.go

- [x] **Step 1: 写完整分类矩阵失败测试**

创建 pkg/routing_error/classifier_test.go，使用确定性表驱动测试：

~~~go
package routingerror

import (
    "errors"
    "net"
    "net/http"
    "testing"

    "github.com/QuantumNous/new-api/common"
    "github.com/QuantumNous/new-api/dto"
    "github.com/QuantumNous/new-api/types"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestClassifyAPIErrorMatrix(t *testing.T) {
    tests := []struct {
        name string
        err  *types.NewAPIError
        ctx  Context
        want Classification
    }{
        {
            name: "caller bad request",
            err: types.NewErrorWithStatusCode(errors.New("bad request"), types.ErrorCodeInvalidRequest, http.StatusBadRequest),
            want: Classification{Responsibility: ResponsibilityCaller, Scope: ScopeRequest, Retryability: RetryNever, HealthEffect: HealthIgnore, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "caller_code"},
        },
        {
            name: "user quota",
            err: types.NewErrorWithStatusCode(errors.New("quota"), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden),
            want: Classification{Responsibility: ResponsibilityCaller, Scope: ScopeRequest, Retryability: RetryNever, HealthEffect: HealthIgnore, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "caller_quota"},
        },
        {
            name: "content safety before generic forbidden",
            err: types.NewErrorWithStatusCode(errors.New("blocked"), types.ErrorCodePromptBlocked, http.StatusForbidden),
            want: Classification{Responsibility: ResponsibilityCaller, Scope: ScopeRequest, Retryability: RetryNever, HealthEffect: HealthIgnore, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "content_safety"},
        },
        {
            name: "serving credential",
            err: types.NewErrorWithStatusCode(errors.New("unauthorized"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized),
            want: Classification{Responsibility: ResponsibilityCredential, Scope: ScopeCredential, Retryability: RetryBeforeCommit, HealthEffect: HealthOpen, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "serving_credential_status"},
        },
        {
            name: "mapped capacity uses source status",
            err: mappedStatusError(t, http.StatusTooManyRequests, http.StatusServiceUnavailable),
            want: Classification{Responsibility: ResponsibilityCapacity, Scope: ScopePoolMember, Retryability: RetryBeforeCommit, HealthEffect: HealthIgnore, CapacityEffect: CapacityCooldown, Component: ComponentServing, Rule: "capacity_status"},
        },
        {
            name: "provider overload 529",
            err: types.NewErrorWithStatusCode(errors.New("overloaded"), types.ErrorCodeBadResponseStatusCode, 529),
            want: Classification{Responsibility: ResponsibilityCapacity, Scope: ScopePoolMember, Retryability: RetryBeforeCommit, HealthEffect: HealthIgnore, CapacityEffect: CapacityCooldown, Component: ComponentServing, Rule: "capacity_status"},
        },
        {
            name: "provider bad gateway",
            err: types.NewErrorWithStatusCode(errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway),
            want: Classification{Responsibility: ResponsibilityProvider, Scope: ScopePoolMember, Retryability: RetryBeforeCommit, HealthEffect: HealthDegrade, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "provider_5xx"},
        },
        {
            name: "first byte timeout",
            err: types.NewErrorWithStatusCode(errors.New("timeout"), types.ErrorCodeFirstByteTimeout, http.StatusGatewayTimeout),
            want: Classification{Responsibility: ResponsibilityNetwork, Scope: ScopeEndpoint, Retryability: RetryBeforeCommit, HealthEffect: HealthDegrade, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "first_byte_timeout"},
        },
        {
            name: "typed dns failure",
            err: types.NewError(&net.DNSError{Name: "upstream.example.com"}, types.ErrorCodeDoRequestFailed),
            want: Classification{Responsibility: ResponsibilityNetwork, Scope: ScopeEndpoint, Retryability: RetryBeforeCommit, HealthEffect: HealthDegrade, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "network_cause"},
        },
        {
            name: "channel model mapping",
            err: types.NewErrorWithStatusCode(errors.New("mapping"), types.ErrorCodeChannelModelMappedError, http.StatusBadRequest),
            want: Classification{Responsibility: ResponsibilityConfig, Scope: ScopeModel, Retryability: RetryBeforeCommit, HealthEffect: HealthOpen, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "model_config"},
        },
        {
            name: "local gateway error",
            err: types.NewError(errors.New("db failed"), types.ErrorCodeQueryDataError),
            want: Classification{Responsibility: ResponsibilityGateway, Scope: ScopeRequest, Retryability: RetryNever, HealthEffect: HealthIgnore, CapacityEffect: CapacityNone, Component: ComponentServing, Rule: "gateway_code"},
        },
        {
            name: "cost connector credential does not affect serving",
            err: types.NewErrorWithStatusCode(errors.New("unauthorized"), types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized),
            ctx: Context{Component: ComponentCostConnector, Operation: OperationSync},
            want: Classification{Responsibility: ResponsibilityCredential, Scope: ScopeCredential, Retryability: RetryBeforeCommit, HealthEffect: HealthOpen, CapacityEffect: CapacityNone, Component: ComponentCostConnector, Rule: "cost_connector_credential"},
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            assert.Equal(t, tt.want, ClassifyAPIError(tt.err, tt.ctx))
        })
    }
}

func TestClassifyTaskErrorRequiresIdempotencyForUpstreamRetry(t *testing.T) {
    taskErr := &dto.TaskError{
        Code:       string(types.ErrorCodeBadResponseStatusCode),
        StatusCode: http.StatusTooManyRequests,
        Error:      errors.New("rate limited"),
    }

    classification := ClassifyTaskError(taskErr, Context{Component: ComponentServing, Operation: OperationTaskSubmit})

    assert.Equal(t, RetryIdempotencyRequired, classification.Retryability)
    assert.Equal(t, CapacityCooldown, classification.CapacityEffect)
}

func TestClassifyTaskLocalErrorNeverAffectsRoutingHealth(t *testing.T) {
    taskErr := &dto.TaskError{
        Code:       string(types.ErrorCodeInsufficientUserQuota),
        StatusCode: http.StatusForbidden,
        LocalError: true,
        Error:      errors.New("quota"),
    }

    classification := ClassifyTaskError(taskErr, Context{Component: ComponentServing, Operation: OperationTaskSubmit})

    assert.Equal(t, ResponsibilityCaller, classification.Responsibility)
    assert.Equal(t, RetryNever, classification.Retryability)
    assert.Equal(t, HealthIgnore, classification.HealthEffect)
}

func TestClassifyProviderFailureWithRetryAfterHasReliabilityAndCapacityEffects(t *testing.T) {
    apiErr := types.NewErrorWithStatusCode(errors.New("temporarily unavailable"), types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
    metadata, err := common.Marshal(map[string]int64{"retry_after_ms": 2500})
    require.NoError(t, err)
    apiErr.Metadata = metadata

    classification := ClassifyAPIError(apiErr, Context{Component: ComponentServing, Operation: OperationRelay})

    assert.Equal(t, ResponsibilityProvider, classification.Responsibility)
    assert.Equal(t, HealthDegrade, classification.HealthEffect)
    assert.Equal(t, CapacityCooldown, classification.CapacityEffect)
}

func TestClassifyExplicitStreamSignalsWithoutAPIError(t *testing.T) {
    corrupted := ClassifyAPIError(nil, Context{Signal: SignalStreamCorruption})
    clientGone := ClassifyAPIError(nil, Context{Signal: SignalClientGone})

    assert.Equal(t, ResponsibilityProvider, corrupted.Responsibility)
    assert.Equal(t, HealthDegrade, corrupted.HealthEffect)
    assert.Equal(t, RetryNever, corrupted.Retryability)
    assert.Equal(t, ResponsibilityCaller, clientGone.Responsibility)
    assert.Equal(t, HealthIgnore, clientGone.HealthEffect)
}
~~~

mappedStatusError 使用 NewErrorWithStatusCode 后调用 SetResponseStatusCode，不访问私有字段。

- [x] **Step 2: 运行测试确认 RED**

Run: go test ./pkg/routing_error -count=1

Expected: FAIL，包和分类类型尚不存在。

- [x] **Step 3: 实现分类类型与稳定规则顺序**

先在 dto/task.go 为 TaskError 增加仅供内部路由语义使用的字段：

~~~go
type TaskError struct {
    Code         string `json:"code"`
    Message      string `json:"message"`
    Data         any    `json:"data"`
    StatusCode   int    `json:"-"`
    LocalError   bool   `json:"-"`
    RetryAfterMs int64  `json:"-"`
    Error        error  `json:"-"`
}
~~~

classifier.go 定义：

~~~go
package routingerror

import (
    "errors"
    "net"
    "net/http"

    "github.com/QuantumNous/new-api/common"
    "github.com/QuantumNous/new-api/dto"
    "github.com/QuantumNous/new-api/types"
)

type Responsibility string
type Scope string
type Retryability string
type HealthEffect string
type CapacityEffect string
type Component string
type Operation string
type Signal string

const (
    ResponsibilityCaller     Responsibility = "caller"
    ResponsibilityGateway    Responsibility = "gateway"
    ResponsibilityConfig     Responsibility = "config"
    ResponsibilityCredential Responsibility = "credential"
    ResponsibilityCapacity   Responsibility = "capacity"
    ResponsibilityProvider   Responsibility = "provider"
    ResponsibilityNetwork    Responsibility = "network"

    ScopeRequest    Scope = "request"
    ScopeModel      Scope = "model"
    ScopeCredential Scope = "credential"
    ScopeEndpoint   Scope = "endpoint"
    ScopePoolMember Scope = "pool_member"
    ScopeChannel    Scope = "channel"

    RetryNever               Retryability = "never"
    RetryBeforeCommit        Retryability = "before_commit"
    RetryIdempotencyRequired Retryability = "idempotency_required"

    HealthIgnore  HealthEffect = "ignore"
    HealthDegrade HealthEffect = "degrade"
    HealthOpen    HealthEffect = "open"

    CapacityNone     CapacityEffect = "none"
    CapacityReduce   CapacityEffect = "reduce"
    CapacityCooldown CapacityEffect = "cooldown"

    ComponentServing       Component = "serving"
    ComponentCostConnector Component = "cost_connector"

    OperationRelay      Operation = "relay"
    OperationTaskSubmit Operation = "task_submit"
    OperationTaskPoll   Operation = "task_poll"

    SignalNone             Signal = ""
    SignalContentSafety    Signal = "content_safety"
    SignalFirstByteTimeout Signal = "first_byte_timeout"
    SignalStreamCorruption Signal = "stream_corruption"
    SignalClientGone       Signal = "client_gone"
)

type Context struct {
    Component Component
    Operation Operation
    Signal    Signal
}

type Classification struct {
    Responsibility Responsibility
    Scope          Scope
    Retryability   Retryability
    HealthEffect   HealthEffect
    CapacityEffect CapacityEffect
    Component      Component
    Rule           string
}

func ClassifyAPIError(apiErr *types.NewAPIError, ctx Context) Classification {
    ctx = normalizeContext(ctx)
    if ctx.Signal == SignalClientGone {
        return result(ctx, ResponsibilityCaller, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "client_gone")
    }
    if ctx.Signal == SignalContentSafety {
        return result(ctx, ResponsibilityCaller, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "content_safety")
    }
    if ctx.Signal == SignalStreamCorruption {
        return result(ctx, ResponsibilityProvider, ScopePoolMember, RetryNever, HealthDegrade, CapacityNone, "stream_corruption")
    }
    if apiErr == nil {
        return result(ctx, "", ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "success")
    }
    finish := func(classification Classification) Classification {
        return withRetryAfterCapacity(apiErr, classification)
    }
    if ctx.Signal == SignalFirstByteTimeout || apiErr.GetErrorCode() == types.ErrorCodeFirstByteTimeout {
        return finish(result(ctx, ResponsibilityNetwork, ScopeEndpoint, RetryBeforeCommit, HealthDegrade, CapacityNone, "first_byte_timeout"))
    }
    if callerCode(apiErr.GetErrorCode()) {
        return finish(result(ctx, ResponsibilityCaller, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, callerRule(apiErr.GetErrorCode())))
    }
    if gatewayCode(apiErr.GetErrorCode()) {
        return finish(result(ctx, ResponsibilityGateway, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "gateway_code"))
    }
    if modelConfigCode(apiErr.GetErrorCode()) {
        return finish(result(ctx, ResponsibilityConfig, ScopeModel, RetryBeforeCommit, HealthOpen, CapacityNone, "model_config"))
    }
    if credentialCode(apiErr.GetErrorCode()) {
        return finish(result(ctx, ResponsibilityCredential, ScopeCredential, RetryBeforeCommit, HealthOpen, CapacityNone, "credential_code"))
    }
    var networkError net.Error
    if errors.As(apiErr.Cause(), &networkError) || apiErr.GetErrorCode() == types.ErrorCodeDoRequestFailed {
        return finish(result(ctx, ResponsibilityNetwork, ScopeEndpoint, RetryBeforeCommit, HealthDegrade, CapacityNone, "network_cause"))
    }
    statusCode := apiErr.SourceStatusCode()
    if ctx.Component == ComponentCostConnector && (statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden) {
        return finish(result(ctx, ResponsibilityCredential, ScopeCredential, RetryBeforeCommit, HealthOpen, CapacityNone, "cost_connector_credential"))
    }
    if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
        return finish(result(ctx, ResponsibilityCredential, ScopeCredential, RetryBeforeCommit, HealthOpen, CapacityNone, "serving_credential_status"))
    }
    if statusCode == http.StatusPaymentRequired || statusCode == http.StatusTooManyRequests || statusCode == 529 {
        return finish(result(ctx, ResponsibilityCapacity, ScopePoolMember, RetryBeforeCommit, HealthIgnore, CapacityCooldown, "capacity_status"))
    }
    if statusCode == http.StatusGatewayTimeout {
        return finish(result(ctx, ResponsibilityNetwork, ScopeEndpoint, RetryBeforeCommit, HealthDegrade, CapacityNone, "gateway_timeout"))
    }
    if statusCode >= 500 && statusCode <= 599 {
        return finish(result(ctx, ResponsibilityProvider, ScopePoolMember, RetryBeforeCommit, HealthDegrade, CapacityNone, "provider_5xx"))
    }
    if statusCode >= 400 && statusCode <= 499 {
        return finish(result(ctx, ResponsibilityCaller, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "caller_status"))
    }
    if responseFailureCode(apiErr.GetErrorCode()) {
        return finish(result(ctx, ResponsibilityProvider, ScopePoolMember, RetryBeforeCommit, HealthDegrade, CapacityNone, "provider_response"))
    }
    return finish(result(ctx, ResponsibilityGateway, ScopeRequest, RetryNever, HealthIgnore, CapacityNone, "conservative_gateway_fallback"))
}

func ClassifyTaskError(taskErr *dto.TaskError, ctx Context) Classification {
    if ctx.Operation == "" {
        ctx.Operation = OperationTaskSubmit
    }
    ctx = normalizeContext(ctx)
    if taskErr == nil {
        return Classification{Component: ctx.Component, HealthEffect: HealthIgnore, CapacityEffect: CapacityNone}
    }
    code := types.ErrorCode(taskErr.Code)
    if code == "" {
        code = types.ErrorCodeBadResponseStatusCode
    }
    cause := taskErr.Error
    if cause == nil {
        cause = errors.New(taskErr.Message)
    }
    apiErr := types.NewErrorWithStatusCode(cause, code, taskErr.StatusCode)
    if taskErr.RetryAfterMs > 0 {
        metadata, _ := common.Marshal(map[string]int64{"retry_after_ms": taskErr.RetryAfterMs})
        apiErr.Metadata = metadata
    }
    classification := ClassifyAPIError(apiErr, ctx)
    if taskErr.LocalError {
        classification.Retryability = RetryNever
        classification.HealthEffect = HealthIgnore
        classification.CapacityEffect = CapacityNone
        classification.Rule = "task_local_" + classification.Rule
        return classification
    }
    if ctx.Operation == OperationTaskSubmit && classification.Retryability == RetryBeforeCommit {
        classification.Retryability = RetryIdempotencyRequired
    }
    return classification
}
~~~

同文件实现 normalizeContext、result、withRetryAfterCapacity、hasRetryAfterMetadata 及 code 集合函数；必须使用 common.Unmarshal 读取 metadata，并使用 switch 精确列出 ErrorCode，不使用文本关键词覆盖结构化语义。callerCode 至少覆盖 invalid_request、read_request_body_failed、bad_request_body、access_denied、sensitive_words_detected、prompt_blocked、violation_fee.grok.csam 与 insufficient_user_quota；gatewayCode 覆盖 token/price/relay-info/SQL/预扣平台错误；modelConfigCode 覆盖模型映射、参数/请求头覆盖和 model_not_found；responseFailureCode 覆盖读取/解析/空响应。任何有效 retry_after_ms 都把已有分类的 CapacityEffect 提升为 CapacityCooldown，但不改变 Responsibility、Scope、HealthEffect 或 Rule。

- [x] **Step 4: 运行 GREEN**

Run: go test ./pkg/routing_error -count=1

Expected: PASS。

- [x] **Step 5: 提交分类器**

~~~bash
git add dto/task.go pkg/routing_error/classifier.go pkg/routing_error/classifier_test.go
git commit -m "feat: classify routing error responsibility"
~~~

### Task 3: 分离 Reliability 指标并切换 Selector availability

**Files:**
- Modify: model/routing_model.go
- Modify: model/routing_model_test.go
- Modify: pkg/routing_hotcache/cache.go
- Modify: pkg/routing_hotcache/cache_test.go
- Modify: pkg/routing_metrics/metrics.go
- Modify: pkg/routing_metrics/metrics_test.go
- Modify: pkg/routing_hotcache/cache.go
- Modify: pkg/routing_hotcache/cache_test.go
- Modify: service/routing/types.go
- Modify: service/routing/selector.go
- Modify: service/routing/selector_test.go
- Modify: service/channel_select.go
- Modify: service/channel_select_test.go

- [x] **Step 1: 写 reliability 计数口径的失败测试**

在 pkg/routing_metrics/metrics_test.go 增加表驱动测试；每个 case 先 enableRoutingMetricsForTest，再构造独立 RelayInfo 和 Classification：

~~~go
func TestRecordClassifiedAttemptSeparatesReliabilityFromCapacityAndCallerErrors(t *testing.T) {
    tests := []struct {
        name           string
        success        bool
        status         int
        classification routingerror.Classification
        want           model.RoutingChannelMetric
    }{
        {
            name:    "success enters reliability sample",
            success: true,
            classification: routingerror.Classification{
                HealthEffect: routingerror.HealthIgnore,
            },
            want: model.RoutingChannelMetric{RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1},
        },
        {
            name:   "provider 502 is reliability failure",
            status: http.StatusBadGateway,
            classification: routingerror.Classification{
                Responsibility: routingerror.ResponsibilityProvider,
                HealthEffect:   routingerror.HealthDegrade,
            },
            want: model.RoutingChannelMetric{RequestCount: 1, ReliabilityRequestCount: 1, ReliabilityFailureCount: 1, Err5xx: 1},
        },
        {
            name:   "network failure is reliability failure",
            status: http.StatusGatewayTimeout,
            classification: routingerror.Classification{
                Responsibility: routingerror.ResponsibilityNetwork,
                HealthEffect:   routingerror.HealthDegrade,
            },
            want: model.RoutingChannelMetric{RequestCount: 1, ReliabilityRequestCount: 1, ReliabilityFailureCount: 1, Err5xx: 1},
        },
        {
            name:   "429 is capacity only",
            status: http.StatusTooManyRequests,
            classification: routingerror.Classification{
                Responsibility: routingerror.ResponsibilityCapacity,
                HealthEffect:   routingerror.HealthIgnore,
                CapacityEffect: routingerror.CapacityCooldown,
            },
            want: model.RoutingChannelMetric{RequestCount: 1, Err429: 1},
        },
        {
            name:   "529 is capacity only and not generic 5xx",
            status: 529,
            classification: routingerror.Classification{
                Responsibility: routingerror.ResponsibilityCapacity,
                HealthEffect:   routingerror.HealthIgnore,
                CapacityEffect: routingerror.CapacityCooldown,
            },
            want: model.RoutingChannelMetric{RequestCount: 1, Err529: 1},
        },
        {
            name:   "caller 400 does not enter reliability sample",
            status: http.StatusBadRequest,
            classification: routingerror.Classification{
                Responsibility: routingerror.ResponsibilityCaller,
                HealthEffect:   routingerror.HealthIgnore,
            },
            want: model.RoutingChannelMetric{RequestCount: 1, Err4xx: 1},
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            enableRoutingMetricsForTest(t)
            info := &relaycommon.RelayInfo{UsingGroup: "default", OriginModelName: "gpt-test", StartTime: time.Now()}
            var apiErr *types.NewAPIError
            if tt.status != 0 {
                apiErr = types.NewErrorWithStatusCode(errors.New("attempt failed"), types.ErrorCodeBadResponseStatusCode, tt.status)
            }

            RecordClassifiedAttempt(nil, info, 101, tt.success, apiErr, tt.classification)

            snapshots := Snapshots()
            require.Len(t, snapshots, 1)
            got := snapshots[0]
            assert.Equal(t, tt.want.RequestCount, got.RequestCount)
            assert.Equal(t, tt.want.SuccessCount, got.SuccessCount)
            assert.Equal(t, tt.want.ReliabilityRequestCount, got.ReliabilityRequestCount)
            assert.Equal(t, tt.want.ReliabilityFailureCount, got.ReliabilityFailureCount)
            assert.Equal(t, tt.want.Err4xx, got.Err4xx)
            assert.Equal(t, tt.want.Err5xx, got.Err5xx)
            assert.Equal(t, tt.want.Err429, got.Err429)
            assert.Equal(t, tt.want.Err529, got.Err529)
        })
    }
}
~~~

补充 TestRecordClassifiedAttemptUsesSourceStatusAndSeparates529：把 source 429 映射为 response 503，再记录 529 与 502，精确断言 Err429=1、Err529=1、Err5xx=1。补充 TestDrainAndRequeuePreserveReliabilityCounters：先 RecordClassifiedAttempt 一次 provider failure，DrainSnapshots 后 RequeueSnapshots，再断言两个 reliability 字段和 Err529/Err429 未丢失。

- [x] **Step 2: 写数据库合并与 selector neutral prior 的失败测试**

扩展 model/routing_model_test.go 中 runRoutingMigrationAndUpsertContract：第一次 metric 写入 ReliabilityRequestCount=2、ReliabilityFailureCount=1、Err529=1，第二次分别写 3、2、2；最终精确断言 5、3、3。该契约由 SQLite 必跑，并在 ROUTING_TEST_MYSQL_DSN / ROUTING_TEST_POSTGRES_DSN 存在时原样运行。

在同一契约中先用下列旧模型创建 routing_channel_metrics 并插入一行，再执行当前 AutoMigrate 两次，验证旧数据不会被回填成伪 reliability：

~~~go
type routingChannelMetricBeforeReliability struct {
    ID           int    `gorm:"primaryKey"`
    ChannelID    int    `gorm:"uniqueIndex:idx_routing_metric_key,priority:1"`
    APIKeyIndex  int    `gorm:"uniqueIndex:idx_routing_metric_key,priority:2"`
    ModelName    string `gorm:"type:varchar(128);uniqueIndex:idx_routing_metric_key,priority:3"`
    Group        string `gorm:"column:group;type:varchar(64);uniqueIndex:idx_routing_metric_key,priority:4"`
    BucketTs     int64  `gorm:"uniqueIndex:idx_routing_metric_key,priority:5"`
    RequestCount int64
    SuccessCount int64
}

func (routingChannelMetricBeforeReliability) TableName() string {
    return "routing_channel_metrics"
}
~~~

迁移后断言 DB.Migrator().HasColumn 对三个新字段均为 true，旧行的 RequestCount/SuccessCount 原值不变，三个新字段均为 0；随后对该旧行 Upsert，确认不会出现 NULL + value。

在 service/routing/selector_test.go 增加：

~~~go
func TestAvailabilityUsesReliabilitySamplesAndLegacyRowsStayNeutral(t *testing.T) {
    settings := Settings{WeightAvailability: 1, MinVolume: 1}
    reliable := testCandidate(1, 1, 100, 1, nil, nil)
    reliable.Metric.ReliabilityRequestCount = 10
    reliable.Metric.ReliabilityFailureCount = 2
    reliable.Metric.RequestCount = 100
    reliable.Metric.SuccessCount = 1

    legacy := testCandidate(2, 0, 100, 1, nil, nil)
    legacy.Metric.ReliabilityRequestCount = 0
    legacy.Metric.ReliabilityFailureCount = 0
    legacy.Metric.RequestCount = 100
    legacy.Metric.SuccessCount = 0

    decision := RankCandidates([]Candidate{reliable, legacy}, settings)

    assert.InDelta(t, 0.8, rankedByID(t, decision, 1).Availability, 0.000001)
    assert.InDelta(t, availabilityNeutralPrior, rankedByID(t, decision, 2).Availability, 0.000001)
}

func TestAvailabilityFloorUsesReliabilityVolumeOnly(t *testing.T) {
    candidate := testCandidate(1, 0, 100, 1, nil, nil)
    candidate.Metric.RequestCount = 1000
    candidate.Metric.SuccessCount = 0
    candidate.Metric.ReliabilityRequestCount = 3
    candidate.Metric.ReliabilityFailureCount = 3

    decision := RankCandidates([]Candidate{candidate}, Settings{
        WeightAvailability: 1,
        MinVolume:          10,
        AvailabilityFloor:  0.99,
    })

    require.Len(t, decision.Ranked, 1)
}
~~~

修改 testCandidate，使默认测试数据同时设置 ReliabilityRequestCount=requests 与 ReliabilityFailureCount=requests-successes；旧测试的含义保持不变。

扩展 pkg/routing_hotcache/cache_test.go::TestLoadMetricSnapshotsBuildsSelectorMetric，输入 reliability 3/1 并断言原样加载。把 service/channel_select_test.go 的首个同优先级选路测试改成旧成功率与 reliability availability 相反的 fixture，断言真实 hotcache → service adapter → selector 链按 reliability 选择，而不是按旧 SuccessCount 或 channel ID。

- [x] **Step 3: 运行测试确认 RED**

Run: go test ./pkg/routing_metrics ./model ./service/routing -run 'Test(RecordClassifiedAttempt|DrainAndRequeuePreserveReliability|RoutingModels|Availability)' -count=1

Expected: FAIL；新字段与 RecordClassifiedAttempt 尚不存在，selector 仍使用 SuccessCount/RequestCount。

- [x] **Step 4: 增加模型、聚合和 Hot Cache 字段**

在 model.RoutingChannelMetric 增加：

~~~go
ReliabilityRequestCount int64 `json:"reliability_request_count" gorm:"not null;default:0"`
ReliabilityFailureCount int64 `json:"reliability_failure_count" gorm:"not null;default:0"`
Err529                  int64 `json:"err_529" gorm:"column:err_529;not null;default:0"`
~~~

UpsertRoutingChannelMetric 的 DoUpdates 使用与现有计数相同的 GORM Expr 累加这三个字段，不写任何方言 SQL。pkg/routing_metrics.bucket、addSnapshotLocked、snapshotLocked 同步增加字段；pkg/routing_hotcache.MetricSnapshot 与 LoadMetricSnapshots 同步携带 reliability 计数。Err529 必须先于 5xx 分支判断，且所有状态统计使用 apiErr.SourceStatusCode()。

核心记录 API：

~~~go
func RecordClassifiedAttempt(
    c *gin.Context,
    info *relaycommon.RelayInfo,
    channelID int,
    success bool,
    apiErr *types.NewAPIError,
    classification routingerror.Classification,
) {
    if !smart_routing_setting.Enabled() {
        return
    }
    now := time.Now()
    key, ok := attemptBucketKey(c, info, channelID, now)
    if !ok {
        return
    }
    // 保留现有 latency/TTFT/generation 计算。
    recordBucket(key, latencyMs, ttftMs, hasTtft, generationMs, success, apiErr, classification)
    // Multi-Key aggregate 分支在 Task 6 删除；本步骤先保持现有兼容行为。
}

func RecordAttempt(c *gin.Context, info *relaycommon.RelayInfo, channelID int, apiErr *types.NewAPIError) {
    classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{
        Component: routingerror.ComponentServing,
        Operation: routingerror.OperationRelay,
    })
    RecordClassifiedAttempt(c, info, channelID, apiErr == nil, apiErr, classification)
}
~~~

bucket.addLocked 的 reliability 逻辑必须严格为：

~~~go
b.requestCount++
if success {
    b.successCount++
    b.reliabilityRequestCount++
} else if (classification.Responsibility == routingerror.ResponsibilityProvider ||
    classification.Responsibility == routingerror.ResponsibilityNetwork) &&
    (classification.HealthEffect == routingerror.HealthDegrade ||
        classification.HealthEffect == routingerror.HealthOpen) {
    b.reliabilityRequestCount++
    b.reliabilityFailureCount++
}
~~~

RecordAttempt 暂时只是兼容入口；Task 7 把生产普通 Relay 迁移到 RecordClassifiedAttempt，Task 8 迁移 Task Relay 后删除该入口，避免生产链重复分类。

保持 UpsertRoutingChannelMetric、Snapshots、DrainSnapshots、RequeueSnapshots 仍以 RequestCount 判断有效性；不能改成 reliability count，否则 capacity-only bucket 会消失。所有直接调用 recordBucket 的既有测试同步补 success 与零值 Classification 参数。

- [x] **Step 5: 切换 Selector availability**

在 pkg/routing_hotcache.MetricSnapshot、service/routing.MetricSnapshot 与 service/channel_select.go 的 Hot Cache 转换中增加 ReliabilityRequestCount / ReliabilityFailureCount。Err529 与其他错误诊断计数一样不进入 Hot Cache；Capacity 不能从历史 rollup 反推。availabilityScore 与 belowAvailabilityFloor 只读取 reliability 字段：

~~~go
func availabilityScore(metric *MetricSnapshot, minVolume int) float64 {
    if metric == nil || metric.ReliabilityRequestCount <= 0 {
        return availabilityNeutralPrior
    }
    if minVolume < 0 {
        minVolume = 0
    }
    if metric.ReliabilityRequestCount < int64(minVolume) {
        return availabilityNeutralPrior
    }
    failures := metric.ReliabilityFailureCount
    if failures < 0 {
        failures = 0
    }
    if failures > metric.ReliabilityRequestCount {
        failures = metric.ReliabilityRequestCount
    }
    return clamp01(1 - float64(failures)/float64(metric.ReliabilityRequestCount))
}

func belowAvailabilityFloor(metric *MetricSnapshot, settings Settings) bool {
    floor := settings.AvailabilityFloor
    if floor <= 0 || floor > 1 || metric == nil || metric.ReliabilityRequestCount <= 0 {
        return false
    }
    minVolume := settings.MinVolume
    if minVolume < 0 {
        minVolume = 0
    }
    if metric.ReliabilityRequestCount < int64(minVolume) {
        return false
    }
    return availabilityScore(metric, 0) < floor
}
~~~

旧行 ReliabilityRequestCount=0 永远使用 neutral prior，不得回退 SuccessCount/RequestCount。

- [x] **Step 6: 运行 GREEN 与三库契约**

Run: go test ./pkg/routing_metrics ./pkg/routing_hotcache ./service/routing ./service ./model -count=1

Expected: PASS；SQLite migration/upsert 契约通过。

Run: go test ./model -run TestRoutingModelsExternalDatabaseCompatibility -count=1

Expected: 未配置 DSN 时明确 SKIP；已配置时 MySQL 与 PostgreSQL 均 PASS。

- [x] **Step 7: 提交 Reliability 口径**

~~~bash
git add model/routing_model.go model/routing_model_test.go pkg/routing_metrics/metrics.go pkg/routing_metrics/metrics_test.go pkg/routing_hotcache/cache.go pkg/routing_hotcache/cache_test.go service/routing/types.go service/routing/selector.go service/routing/selector_test.go service/channel_select.go service/channel_select_test.go
git commit -m "fix: separate routing reliability metrics"
~~~

### Task 4: 增加独立、有界的 Capacity Cooldown

**Files:**
- Create: pkg/routing_hotcache/capacity.go
- Create: pkg/routing_hotcache/capacity_test.go
- Modify: pkg/routing_hotcache/cache.go
- Modify: pkg/routing_hotcache/cache_test.go
- Modify: service/routing/types.go
- Modify: service/routing/selector.go
- Modify: service/routing/selector_test.go
- Modify: service/channel_select.go
- Modify: service/channel_select_test.go

- [x] **Step 1: 写 Capacity Hot Cache 的失败测试**

在 pkg/routing_hotcache/capacity_test.go 增加：

~~~go
func TestRecordCapacityCooldownCapsKeepsLaterDeadlineAndExpiresOnPrune(t *testing.T) {
    ResetForTest()
    t.Cleanup(ResetForTest)
    key := Key{ChannelID: 11, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
    now := time.UnixMilli(100_000)

    _, recorded := RecordCapacityCooldown(key, http.StatusTooManyRequests, 50*time.Second, time.Second, 5*time.Second, now)
    require.True(t, recorded)
    _, recorded = RecordCapacityCooldown(key, http.StatusTooManyRequests, time.Second, time.Second, 5*time.Second, now.Add(time.Second))
    require.True(t, recorded)

    snapshot, ok := GetCapacityCooldown(key)
    require.True(t, ok)
    assert.Equal(t, int64(105_000), snapshot.CooldownUntilUnixMilli)
    assert.True(t, CapacityCooldownActive(key, time.UnixMilli(104_999)))
    assert.False(t, CapacityCooldownActive(key, time.UnixMilli(105_000)))
    assert.Equal(t, 1, Prune(105, 0))
    _, ok = GetCapacityCooldown(key)
    assert.False(t, ok)
}

func TestRecordCapacityCooldownUsesCapacityStatusOrValidRetryAfter(t *testing.T) {
    key := Key{ChannelID: 12, APIKeyIndex: -1, Model: "gpt-test", Group: "default"}
    now := time.UnixMilli(100_000)
    for _, status := range []int{http.StatusPaymentRequired, http.StatusTooManyRequests, 529} {
        ClearCapacityCooldown(key)
        _, recorded := RecordCapacityCooldown(key, status, 0, 1500*time.Millisecond, 5*time.Second, now)
        assert.True(t, recorded)
        snapshot, ok := GetCapacityCooldown(key)
        require.True(t, ok)
        assert.Equal(t, int64(101_500), snapshot.CooldownUntilUnixMilli)
    }

    ClearCapacityCooldown(key)
    _, recorded := RecordCapacityCooldown(key, http.StatusServiceUnavailable, 2*time.Second, time.Second, 5*time.Second, now)
    assert.True(t, recorded)
    ClearCapacityCooldown(key)
    _, recorded = RecordCapacityCooldown(key, http.StatusBadRequest, 0, time.Second, 5*time.Second, now)
    assert.False(t, recorded)
}

func TestCapacityCooldownRespectsHardLimitStatsAndStableEviction(t *testing.T) {
    ResetForTest()
    t.Cleanup(ResetForTest)
    cache.Lock()
    cache.limits = Limits{MaxMetrics: 1, MaxCosts: 1, MaxBreakers: 1, MaxHealth: 1, MaxCapacityCooldowns: 2}
    cache.Unlock()

    for _, channelID := range []int{3, 1, 2} {
        SetCapacityCooldownForTest(Key{ChannelID: channelID, APIKeyIndex: -1, Model: "same", Group: "default"}, CapacityCooldownSnapshot{
            CooldownUntilUnixMilli: 200_000,
            UpdatedUnixMilli:       100_000,
        })
    }

    assert.Equal(t, 2, RuntimeStats().CapacityCooldowns)
    _, one := GetCapacityCooldown(Key{ChannelID: 1, APIKeyIndex: -1, Model: "same", Group: "default"})
    _, two := GetCapacityCooldown(Key{ChannelID: 2, APIKeyIndex: -1, Model: "same", Group: "default"})
    _, three := GetCapacityCooldown(Key{ChannelID: 3, APIKeyIndex: -1, Model: "same", Group: "default"})
    assert.False(t, one)
    assert.True(t, two)
    assert.True(t, three)
}

func TestClearChannelAndResetClearCapacityCooldowns(t *testing.T) {
    ResetForTest()
    first := Key{ChannelID: 1, APIKeyIndex: -1, Model: "a", Group: "default"}
    second := Key{ChannelID: 2, APIKeyIndex: -1, Model: "b", Group: "default"}
    SetCapacityCooldownForTest(first, CapacityCooldownSnapshot{CooldownUntilUnixMilli: 200_000, UpdatedUnixMilli: 100_000})
    SetCapacityCooldownForTest(second, CapacityCooldownSnapshot{CooldownUntilUnixMilli: 200_000, UpdatedUnixMilli: 100_000})

    ClearChannel(1)
    _, ok := GetCapacityCooldown(first)
    assert.False(t, ok)
    _, ok = GetCapacityCooldown(second)
    assert.True(t, ok)

    ResetForTest()
    assert.Equal(t, Stats{}, RuntimeStats())
}
~~~

- [x] **Step 2: 写 Capacity 硬过滤的失败测试**

在 service/routing/selector_test.go 增加：

~~~go
func TestRankCandidatesCapacityCooldownIsHardFilterWithoutBreakerBypass(t *testing.T) {
    cooling := testCandidate(1, 1, 100, 1, nil, &BreakerSnapshot{State: BreakerStateHealthy})
    cooling.Capacity = &CapacityCooldownSnapshot{CooldownUntilUnixMilli: 200_000, UpdatedUnixMilli: 100_000}
    open := testCandidate(2, 1, 100, 1, nil, &BreakerSnapshot{State: BreakerStateOpen, CooldownUntilUnix: 300, UpdatedUnix: 100})

    decision := RankCandidates([]Candidate{cooling, open}, Settings{
        WeightAvailability: 1,
        MaxEjectedPct:      0,
        NowUnix:            150,
        NowUnixMilli:       150_000,
    })

    assert.Equal(t, 1, decision.FilteredCapacity)
    assert.True(t, decision.BreakerBypassed)
    require.Len(t, decision.Ranked, 1)
    assert.Equal(t, 2, decision.Ranked[0].Channel.Id)
}

func TestRankCandidatesRestoresCapacityCandidateAtDeadline(t *testing.T) {
    candidate := testCandidate(1, 1, 100, 1, nil, nil)
    candidate.Capacity = &CapacityCooldownSnapshot{CooldownUntilUnixMilli: 200_000, UpdatedUnixMilli: 100_000}

    decision := RankCandidates([]Candidate{candidate}, Settings{WeightAvailability: 1, NowUnix: 200, NowUnixMilli: 200_000})

    assert.Zero(t, decision.FilteredCapacity)
    require.Len(t, decision.Ranked, 1)
}
~~~

在 service/channel_select_test.go 增加真实候选测试：单 Key active capacity 时 selectSmartChannelForGroup 返回 nil；把 CooldownUntilUnixMilli 改为当前毫秒时间后恢复选择。额外设置 half-open breaker 并验证 capacity active 时不会在 ContextKeyRoutingHalfOpenProbes 中留下 probe。

- [x] **Step 3: 运行测试确认 RED**

Run: go test ./pkg/routing_hotcache ./service/routing ./service -run 'Test.*Capacity' -count=1

Expected: FAIL；Capacity 类型、缓存 Map、Stats 和 selector 字段尚不存在。

- [x] **Step 4: 实现独立 Capacity Map 与冷却计算**

在 pkg/routing_hotcache/capacity.go 增加：

~~~go
type CapacityCooldownSnapshot struct {
    SourceStatusCode       int
    Reason                 string
    RetryAfterMs           int64
    CooldownUntilUnixMilli int64
    UpdatedUnixMilli       int64
}

func RecordCapacityCooldown(
    key Key,
    sourceStatusCode int,
    retryAfter time.Duration,
    baseCooldown time.Duration,
    maxCooldown time.Duration,
    now time.Time,
) (CapacityCooldownSnapshot, bool) {
    capacityStatus := sourceStatusCode == http.StatusPaymentRequired ||
        sourceStatusCode == http.StatusTooManyRequests || sourceStatusCode == 529
    if !capacityStatus && retryAfter <= 0 {
        return CapacityCooldownSnapshot{}, false
    }
    cooldown := retryAfter
    if cooldown <= 0 {
        cooldown = baseCooldown
    }
    if cooldown <= 0 || maxCooldown <= 0 {
        return CapacityCooldownSnapshot{}, false
    }
    if cooldown > maxCooldown {
        cooldown = maxCooldown
    }
    retryAfterMs := retryAfter.Milliseconds()
    if retryAfterMs < 0 {
        retryAfterMs = 0
    }
    snapshot := CapacityCooldownSnapshot{
        SourceStatusCode:       sourceStatusCode,
        Reason:                 "capacity_cooldown",
        RetryAfterMs:           retryAfterMs,
        CooldownUntilUnixMilli: now.Add(cooldown).UnixMilli(),
        UpdatedUnixMilli:       now.UnixMilli(),
    }
    return setCapacityCooldown(key, snapshot)
}

func GetCapacityCooldown(key Key) (CapacityCooldownSnapshot, bool)
func CapacityCooldownActive(key Key, now time.Time) bool
func ClearCapacityCooldown(key Key)
func SetCapacityCooldownForTest(key Key, snapshot CapacityCooldownSnapshot)
~~~

在 cache.go 扩展公共容器字段：

~~~go

type Limits struct {
    MaxMetrics           int
    MaxCosts             int
    MaxBreakers          int
    MaxHealth            int
    MaxCapacityCooldowns int
}

type Stats struct {
    Metrics           int
    Costs             int
    Breakers          int
    CapacityCooldowns int
    AuthFailures      int
    Balances          int
    Evictions         int64
}
~~~

defaultLimits.MaxCapacityCooldowns=20_000，并在 cache 增加 capacityCooldowns map[Key]CapacityCooldownSnapshot。setCapacityCooldown 必须在同一把 cache 锁内完成 later-deadline 合并：

~~~go
func setCapacityCooldown(key Key, snapshot CapacityCooldownSnapshot) (CapacityCooldownSnapshot, bool) {
    if key.ChannelID <= 0 || key.Model == "" || key.Group == "" || snapshot.CooldownUntilUnixMilli <= 0 {
        return CapacityCooldownSnapshot{}, false
    }
    cache.Lock()
    defer cache.Unlock()
    if current, ok := cache.capacityCooldowns[key]; ok && current.CooldownUntilUnixMilli >= snapshot.CooldownUntilUnixMilli {
        return current, true
    }
    cache.capacityCooldowns[key] = snapshot
    cache.limits = normalizedLimits(cache.limits)
    cache.evictions += int64(trimBoundedMap(cache.capacityCooldowns, cache.limits.MaxCapacityCooldowns, capacityUpdatedUnixMilli, keyLess))
    return snapshot, true
}
~~~

Prune 在 staleSeconds 判断之外无条件删除 CooldownUntilUnixMilli <= nowUnix*1000 的 Capacity；活跃 cooldown 不因 UpdatedUnixMilli 很旧而被 SnapshotStaleSec 清除，随后与其他 Map 一样执行稳定 trim。RuntimeStats、ClearChannel、normalizedLimits、ResetForTest 全部覆盖 capacityCooldowns。后到的更短 deadline 不得更新 snapshot，也不得通过反复短 cooldown 延长其 eviction 新鲜度。Capacity 不写数据库、不写 Redis。

- [x] **Step 5: 在 Selector 中建立独立硬过滤**

service/routing/types.go 增加：

~~~go
type CapacityCooldownSnapshot struct {
    SourceStatusCode       int
    CooldownUntilUnixMilli int64
    UpdatedUnixMilli       int64
}

type Candidate struct {
    Channel  *model.Channel
    Metric   *MetricSnapshot
    Cost     *CostSnapshot
    Breaker  *BreakerSnapshot
    Capacity *CapacityCooldownSnapshot
}

type Settings struct {
    // 现有字段不变。
    NowUnix      int64
    NowUnixMilli int64
}

type Decision struct {
    Ranked           []RankedCandidate
    Selected         *RankedCandidate
    Weights          Weights
    BreakerBypassed  bool
    FilteredOpen     int
    FilteredCapacity int
}
~~~

RankCandidates 首先剔除 active capacity，再对剩余候选计算 Breaker open 比例：

~~~go
health := make([]candidateHealth, 0, len(candidates))
filteredCapacity := 0
for i, candidate := range candidates {
    if candidate.Capacity != nil && settings.NowUnixMilli > 0 &&
        candidate.Capacity.CooldownUntilUnixMilli > settings.NowUnixMilli {
        filteredCapacity++
        continue
    }
    degraded, open, hardOpen := classifyBreaker(candidate.Breaker, settings)
    // 保留现有 health 构造与 openCount。
}
breakerBypassed := shouldBypassOpenFilter(openCount, len(health), settings.MaxEjectedPct)
// 返回 Decision 时写入 FilteredCapacity。
~~~

Capacity 不使用 SnapshotStaleSec、不进入 degraded、不受 MaxEjectedPct 绕过、不到期不进入 Ranked，因此后续 acquireRoutingHalfOpenProbe 不会申请本地或 Redis probe。routingSelectorSettings 必须只调用一次 time.Now()，同时设置 NowUnix、NowUnixMilli 与 RandomSeed，避免秒/毫秒快照跨边界不一致。

service/channel_select.go 从 routinghotcache.GetCapacityCooldown(cacheKey) 复制 SourceStatusCode/CooldownUntilUnixMilli/UpdatedUnixMilli 到 Candidate.Capacity；Observe/Shadow 只改变记录的决策，Balanced/Enterprise 才会通过现有调用链改变真实选择。

- [x] **Step 6: 运行 GREEN**

Run: go test ./pkg/routing_hotcache ./service/routing ./service -count=1

Expected: PASS；Capacity limit、TTL、later deadline、Stats、ClearChannel、Reset、硬过滤和不申请 half-open probe 均受保护。

- [x] **Step 7: 提交 Capacity Cooldown 核心**

~~~bash
git add pkg/routing_hotcache/capacity.go pkg/routing_hotcache/capacity_test.go pkg/routing_hotcache/cache.go pkg/routing_hotcache/cache_test.go service/routing/types.go service/routing/selector.go service/routing/selector_test.go service/channel_select.go service/channel_select_test.go
git commit -m "feat: add routing capacity cooldown"
~~~

### Task 5: 让 Breaker 只接收 Reliability，并版本化持久状态

**Files:**
- Modify: pkg/routing_breaker/breaker.go
- Modify: pkg/routing_breaker/breaker_test.go
- Modify: model/routing_model.go
- Modify: model/routing_model_test.go
- Modify: controller/system_task_handlers.go
- Modify: controller/smart_routing_runtime_test.go
- Modify: controller/smart_routing_test.go
- Modify: controller/smart_routing_task_test.go

- [x] **Step 1: 反转 429/529 与 Retry-After 的 Breaker 测试**

在 pkg/routing_breaker/breaker_test.go 删除 TestBreaker429OpensAndHonorsRetryAfter，替换为：

~~~go
func TestBreakerIgnoresCapacityStatusesWithoutCreatingState(t *testing.T) {
    breaker, _, key := testBreaker(t)

    for _, status := range []int{http.StatusPaymentRequired, http.StatusTooManyRequests, 529} {
        snapshot := breaker.RecordHTTPAttempt(key, false, status)
        assert.Equal(t, StateHealthy, snapshot.State)
    }

    assert.Equal(t, Stats{}, breaker.Stats())
}

func TestCapacityStatusDoesNotMutateExistingReliabilityState(t *testing.T) {
    breaker, _, key := testBreaker(t)
    before := breaker.OnReliabilityFailure(key, FailureProvider5xx)

    breaker.RecordHTTPAttempt(key, false, http.StatusTooManyRequests)
    breaker.RecordHTTPAttempt(key, false, 529)

    after := breaker.Peek(key)
    assert.Equal(t, before, after)
    assert.Equal(t, Stats{Entries: 1, Dirty: 1}, breaker.Stats())
}

func TestCapacityStatusDoesNotAdvanceOrReopenHalfOpenReliabilityState(t *testing.T) {
    breaker, clock, key := testBreaker(t)
    for range 5 {
        breaker.OnReliabilityFailure(key, FailureProvider5xx)
    }
    opened := breaker.Peek(key)
    clock.Advance(opened.CooldownUntil.Sub(clock.now))

    breaker.RecordHTTPAttempt(key, false, http.StatusTooManyRequests)

    assert.Equal(t, StateOpen, breaker.Peek(key).State)
}
~~~

把所有原 OnFailure(key, status, retryAfter) 测试迁移为 OnReliabilityFailure(key, FailureProvider5xx) 或 FailureNetwork。新增表驱动 TestBreakerReliabilityHTTPStatuses，覆盖 500、502、503、504；每种状态通过 RecordHTTPAttempt 进入 reliability window。新增 TestNetworkFailuresEnterReliabilityWindow，断言 FailureNetwork 增加 WindowFailures/ConsecutiveFailures，但不增加 Consecutive5xx。

新增 TestResetDefaultKeyDoesNotClearCapacityCooldown：先写独立 Capacity，再 reset 同 Key Breaker，断言 Capacity 仍存在；ClearDefaultChannel 仍通过 routinghotcache.ClearChannel 清理该渠道所有运行状态。

- [x] **Step 2: 写 semantic version 失败测试**

在 model/routing_model_test.go 的三库契约中增加一个 TableName() 返回 routing_breaker_states、但不含 SemanticVersion 的 legacy struct。先创建旧表并插入 UpdatedTime 极大的旧窗口，再 AutoMigrate 当前模型两次：断言新列存在、旧行读取为 0、GetRoutingBreakerStatesForHydration 不返回旧行。随后用较小 UpdatedTime Upsert 当前版本，断言仍能整行覆盖旧窗口；再用更旧的当前版本 Upsert，断言不能覆盖；唯一键行数始终为 1。

在 controller/smart_routing_runtime_test.go 增加：

~~~go
func TestRoutingBreakerModelsToSnapshotsRejectsLegacySemanticVersion(t *testing.T) {
    states := []model.RoutingBreakerState{
        {ChannelID: 1, APIKeyIndex: -1, ModelName: "legacy", Group: "default", State: model.RoutingBreakerStateOpen, SemanticVersion: 0, UpdatedTime: 100},
        {ChannelID: 2, APIKeyIndex: -1, ModelName: "current", Group: "default", State: model.RoutingBreakerStateOpen, SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 100},
    }

    snapshots := routingBreakerModelsToSnapshots(states)

    require.Len(t, snapshots, 1)
    assert.Equal(t, 2, snapshots[0].Key.ChannelID)
}

func TestRefreshRoutingHotcacheHydratesOnlyCurrentBreakerSemanticVersion(t *testing.T) {
    db := setupModelListControllerTestDB(t)
    require.NoError(t, db.AutoMigrate(&model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}, &model.RoutingCostSnapshot{}))
    routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
    routinghotcache.ResetForTest()
    t.Cleanup(func() {
        routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
        routinghotcache.ResetForTest()
    })
    require.NoError(t, db.Create(&[]model.RoutingBreakerState{
        {ChannelID: 10, APIKeyIndex: -1, ModelName: "legacy", Group: "default", State: model.RoutingBreakerStateOpen, SemanticVersion: 0, UpdatedTime: common.GetTimestamp()},
        {ChannelID: 11, APIKeyIndex: -1, ModelName: "current", Group: "default", State: model.RoutingBreakerStateOpen, SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: common.GetTimestamp()},
    }).Error)

    summary, err := refreshRoutingHotcacheFromDB(smart_routing_setting.GetSetting())
    require.NoError(t, err)
    assert.Equal(t, 1, summary["breakers"])
    assert.Equal(t, 1, routingbreaker.RuntimeStats().Entries)
}
~~~

所有仍期望被加载的 controller/smart_routing_task_test.go 与 controller/smart_routing_test.go RoutingBreakerState fixture 显式设置 SemanticVersion=RoutingBreakerSemanticVersion；专门验证 legacy ignore 的 fixture 保持 0。

- [x] **Step 3: 运行测试确认 RED**

Run: go test ./pkg/routing_breaker ./model ./controller -run 'Test(Breaker|RoutingBreaker|RefreshRoutingHotcacheHydratesOnlyCurrent)' -count=1

Expected: FAIL；FailureKind、Peek、semantic version 不存在，429 仍会立即打开 Breaker。

- [x] **Step 4: 收窄 Breaker API**

在 pkg/routing_breaker/breaker.go 定义：

~~~go
type FailureKind string

const (
    FailureProvider5xx FailureKind = "provider_5xx"
    FailureNetwork     FailureKind = "network"
)

func RecordReliabilitySuccess(key Key) Snapshot {
    return defaultBreaker.onSuccess(key).snapshot
}

func RecordReliabilityFailure(key Key, kind FailureKind) Snapshot {
    return defaultBreaker.onReliabilityFailure(key, kind).snapshot
}

func (b *Breaker) OnReliabilityFailure(key Key, kind FailureKind) Snapshot {
    return b.onReliabilityFailure(key, kind).snapshot
}

func (b *Breaker) RecordHTTPAttempt(key Key, success bool, statusCode int) Snapshot {
    if success {
        return b.OnSuccess(key)
    }
    if !isReliabilityHTTPStatus(statusCode) {
        return b.Peek(key)
    }
    return b.OnReliabilityFailure(key, FailureProvider5xx)
}

func (b *Breaker) Peek(key Key) Snapshot {
    b.mu.Lock()
    defer b.mu.Unlock()
    if record, ok := b.states[key]; ok {
        return record.snapshot
    }
    return Snapshot{Key: key, State: StateHealthy}
}
~~~

isReliabilityHTTPStatus 精确接受 500、502、503、504；429、529、402、501、505 和其他非 reliability 状态在 getOrCreate 前返回，不创建、不 advance open、不 prune、不 mark dirty。onReliabilityFailure 保留现有 failure window、degraded、open、half-open 逻辑，但删除 statusCode/retryAfter 参数：FailureProvider5xx 增加 Consecutive5xx，FailureNetwork 将 Consecutive5xx 归零；两者都增加 reliability failure window。open 只使用 b.cooldown(ejectionCount)，failureReason 只返回 half_open_failure、5xx 或 network/failure_rate。

为现有 controller 在 Task 7 完成迁移前保留一个防御性兼容入口：

~~~go
func RecordAttempt(key Key, success bool, statusCode int, _ time.Duration) Snapshot {
    return defaultBreaker.RecordHTTPAttempt(key, success, statusCode)
}
~~~

它必须忽略 Retry-After，且 capacity status 的返回路径使用 Peek，不能通过 GetSnapshot 意外推进状态。

- [x] **Step 5: 增加 Breaker semantic version 并过滤 hydrate**

在 model/routing_model.go 定义：

~~~go
const RoutingBreakerSemanticVersion = 2

type RoutingBreakerState struct {
    // 现有 key/state/window 字段保持不变。
    SemanticVersion int `json:"semantic_version" gorm:"index"`
}
~~~

RoutingBreakerSemanticVersion=2 表示 reliability-only window；历史 NULL/0 整体失效，不能只按 reason=rate_limit 过滤，因为旧 529 已混入 WindowFailures/Consecutive5xx。不要把 semantic version 加入唯一键，也不要使用 default:2：同一业务 key 只能有一行，新版本首次写入要整行替换旧窗口。

UpsertRoutingBreakerState 开始时强制 state.SemanticVersion=RoutingBreakerSemanticVersion，updates map 写 semantic_version；条件更新必须允许旧版本即使 UpdatedTime 更大也被当前版本覆盖：

~~~go
versionWhere := "(semantic_version IS NULL OR semantic_version <> ? OR updated_time <= ?)"
result := breakerKeyWhere().
    Where(versionWhere, RoutingBreakerSemanticVersion, state.UpdatedTime).
    Model(&RoutingBreakerState{}).
    UpdateColumns(updates)
~~~

并发 insert 失败后的第二次条件 update 使用完全相同的 versionWhere。当前版本内部仍保持 UpdatedTime 单调，较旧当前快照不能覆盖较新当前快照。

在 model 层新增：

~~~go
func GetRoutingBreakerStatesForHydration(limit int) ([]RoutingBreakerState, error) {
    if limit <= 0 {
        limit = 5000
    }
    var states []RoutingBreakerState
    err := DB.Where("semantic_version = ?", RoutingBreakerSemanticVersion).
        Order("updated_time desc").
        Limit(limit).
        Find(&states).Error
    return states, err
}
~~~

routingBreakerSnapshotToModel 固定写当前版本。routingBreakerModelsToSnapshots 再做一层 SemanticVersion==current 防御过滤。refreshRoutingHotcacheFromDB 查询改为：

~~~go
breakerStates, err := model.GetRoutingBreakerStatesForHydration(5000)
if err != nil {
    return summary, err
}
accepted := routingBreakerModelsToSnapshots(breakerStates)
retained := routingbreaker.HydrateDefaultSnapshots(accepted)
summary["breakers"] = len(retained)
~~~

HydrateDefaultSnapshots 改为返回 Breaker.Hydrate 实际接受的 []Snapshot。pkg/routing_hotcache.LoadBreakerSnapshots 同样跳过 SemanticVersion != current 的行，避免备用入口重新引入旧语义。不删除旧 DB 行、不执行生产迁移；新 reliability 事件自然以相同唯一键把行更新为当前版本。

- [x] **Step 6: 运行 GREEN 与竞态测试**

Run: go test ./pkg/routing_breaker ./pkg/routing_hotcache ./model ./controller -count=1

Expected: PASS。

Run: go test -race ./pkg/routing_breaker ./controller -count=1

Expected: PASS，且无 race report。

- [x] **Step 7: 提交 Reliability Breaker**

~~~bash
git add pkg/routing_breaker/breaker.go pkg/routing_breaker/breaker_test.go pkg/routing_hotcache/cache.go pkg/routing_hotcache/cache_test.go model/routing_model.go model/routing_model_test.go controller/system_task_handlers.go controller/smart_routing_runtime_test.go controller/smart_routing_test.go controller/smart_routing_task_test.go
git commit -m "fix: isolate routing reliability breaker"
~~~

### Task 6: 禁用不稳定的 Multi-Key smart-routing 状态

**Files:**
- Modify: relay/common/relay_info.go
- Modify: relay/common/relay_info_test.go
- Modify: pkg/routing_metrics/metrics.go
- Modify: pkg/routing_metrics/metrics_test.go
- Modify: pkg/routing_hotcache/cache.go
- Modify: pkg/routing_hotcache/cache_test.go
- Modify: pkg/routing_breaker/breaker_test.go
- Modify: controller/relay.go
- Modify: controller/relay_retry_test.go
- Modify: controller/system_task_handlers.go
- Modify: controller/smart_routing_runtime_test.go
- Modify: service/channel_select.go
- Modify: service/channel_select_test.go
- Modify: middleware/distributor.go
- Modify: middleware/distributor_smart_routing_test.go
- Modify: model/channel.go
- Modify: model/routing_model.go
- Modify: model/routing_model_test.go

- [x] **Step 1: 写当前 attempt 真源与 metrics no-op 失败测试**

在 relay/common/relay_info_test.go 增加：

~~~go
func TestCurrentAttemptIsMultiKeyPrefersContextOverStaleChannelMeta(t *testing.T) {
    ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
    info := &RelayInfo{ChannelMeta: &ChannelMeta{ChannelIsMultiKey: true, ChannelMultiKeyIndex: 9}}

    common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, false)
    common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
    assert.False(t, info.CurrentAttemptIsMultiKey(ctx))

    common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
    common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 2)
    assert.True(t, info.CurrentAttemptIsMultiKey(ctx))
}
~~~

用以下测试替换 pkg/routing_metrics 中现有 Multi-Key aggregate snapshot/inflight 测试：

~~~go
func TestRoutingMetricsIgnoreCurrentMultiKeyAttempt(t *testing.T) {
    enableRoutingMetricsForTest(t)
    ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
    common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
    common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 3)
    info := &relaycommon.RelayInfo{
        UsingGroup:      "vip",
        OriginModelName: "gpt-test",
        StartTime:       time.Now(),
        ChannelMeta:     &relaycommon.ChannelMeta{ChannelIsMultiKey: false},
    }

    release := BeginInflight(ctx, info, 25)
    RecordClassifiedAttempt(ctx, info, 25, true, nil, routingerror.Classification{})
    release()

    assert.Empty(t, Snapshots())
    assert.Equal(t, Stats{}, RuntimeStats())
}

func TestRoutingMetricsUseOnlyMinusOneForCurrentSingleKeyAttempt(t *testing.T) {
    enableRoutingMetricsForTest(t)
    ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
    common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, false)
    common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
    info := &relaycommon.RelayInfo{
        UsingGroup:      "vip",
        OriginModelName: "gpt-test",
        StartTime:       time.Now(),
        ChannelMeta:     &relaycommon.ChannelMeta{ChannelIsMultiKey: true, ChannelMultiKeyIndex: 2},
    }

    release := BeginInflight(ctx, info, 26)
    assert.Equal(t, int64(1), InflightCount(InflightKey{ChannelID: 26, APIKeyIndex: -1, Model: "gpt-test", Group: "vip"}))
    RecordClassifiedAttempt(ctx, info, 26, true, nil, routingerror.Classification{})
    release()

    snapshots := Snapshots()
    require.Len(t, snapshots, 1)
    assert.Equal(t, model.RoutingMetricSingleKeyIndex, snapshots[0].APIKeyIndex)
}
~~~

- [x] **Step 2: 写数据面不消费 legacy index/aggregate 的失败测试**

把 middleware/distributor_smart_routing_test.go 的四个 per-index Breaker/probe 测试替换为：

~~~go
func TestSetupContextForSelectedChannelUsesOperationalMultiKeyStateOnly(t *testing.T) {
    // 构造 [disabled-key, enabled-key]，给 index=1、aggregate=-1 注入 open breaker 和 active capacity。
    // Setup 后仍选择 enabled-key，且 Context 中没有 half-open probe/Redis lease。
}

func TestSetupContextForSelectedChannelResetsSingleKeyMetadata(t *testing.T) {
    ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
    common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
    common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, 7)
    common.SetContextKey(ctx, constant.ContextKeyChannelKey, "stale-key")
    channel := &model.Channel{Id: 2, Key: "single-key", ChannelInfo: model.ChannelInfo{IsMultiKey: false}}

    apiErr := SetupContextForSelectedChannel(ctx, channel, "gpt-test")

    require.Nil(t, apiErr)
    assert.False(t, common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey))
    assert.Equal(t, model.RoutingMetricSingleKeyIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
    assert.Equal(t, "single-key", common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
}
~~~

在 service/channel_select_test.go 增加 TestSmartRoutingCandidatesIgnoreLegacyMetricBreakerInflightAndCapacityForMultiKey：向 aggregate -1 与 positive index 写入旧 metric/breaker/capacity/inflight，Multi-Key candidate 仍无 Metric/Capacity、不会读取 reliability Breaker，但保留 cost；单 Key candidate 继续消费唯一 -1 状态。另加一个 balance marker 断言其作为现有 channel-scope 硬约束仍生效。

在 controller/relay_retry_test.go 把 aggregate Breaker 测试反转为 current Multi-Key attempt 不产生 positive index 或 -1 Breaker；Context 与 stale ChannelMeta 相反，证明使用当前 attempt 真源。

- [x] **Step 3: 写持久化与 hydrate 防线失败测试**

在 model/routing_model_test.go 建立 single 与 Multi-Key Channel 后测试：

~~~go
func TestRoutingPersistenceAcceptsOnlySingleKeyMinusOne(t *testing.T) {
    // single/-1 metric + breaker => 各 1 行。
    // single/+2、multi/-1、multi/+2 => Upsert 返回 nil 但不产生行。
}
~~~

在 controller/smart_routing_runtime_test.go 增加 TestRefreshRoutingHotcacheIgnoresLegacyMultiKeyAndPositiveIndexRows：用 DB.Create 绕过新 Upsert guard，插入 single/-1、single/+1、multi/-1、multi/+1 的历史 metric/current-version breaker；refresh 后 Hot Cache 与 Breaker runtime 只能出现 single/-1，summary 只统计有效行。

- [x] **Step 4: 运行测试确认 RED**

Run: go test ./relay/common ./pkg/routing_metrics ./model ./service ./middleware ./controller -run 'Test(CurrentAttemptIsMultiKey|RoutingMetricsIgnoreCurrentMultiKey|RoutingMetricsUseOnlyMinusOne|SetupContextForSelectedChannel|SmartRoutingCandidatesIgnoreLegacy|RoutingPersistenceAcceptsOnly|RefreshRoutingHotcacheIgnoresLegacy|RecordRouting.*MultiKey)' -count=1

Expected: FAIL；当前 metrics 仍双写，middleware 仍读取 per-index Breaker，持久层仍接受 index/aggregate。

- [x] **Step 5: 建立 current-attempt 总闸门**

在 relay/common/relay_info.go 增加：

~~~go
func (info *RelayInfo) CurrentAttemptIsMultiKey(c *gin.Context) bool {
    if c != nil {
        return common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey)
    }
    return info != nil && info.ChannelMeta != nil && info.ChannelMeta.ChannelIsMultiKey
}
~~~

规则是有 Gin Context 时 Context 为唯一真源；只有无 Context 的纯测试/后台场景才回退 ChannelMeta。

pkg/routing_metrics.BeginInflight 与 RecordClassifiedAttempt 首先检查 CurrentAttemptIsMultiKey，为 true 直接 no-op；删除 positive index 与 -1 aggregate 双写，删除 apiKeyIndex(info)，所有支持的 live state 永远构造 APIKeyIndex=model.RoutingMetricSingleKeyIndex。

controller.recordRoutingBreakerAttempt 首部同样在 CurrentAttemptIsMultiKey 时返回；单 Key 只调用一次 -1，不再 aggregate。Task 7 重构统一 effects 时保留同一闸门。

- [x] **Step 6: 让 Multi-Key Key 选择只看 operational state**

middleware.SetupContextForSelectedChannel 删除 GetNextEnabledKeyFiltered、IsMultiKeyIndexRoutingAdmissible 与 AcquireMultiKeyRoutingHalfOpenProbe 循环，改为：

~~~go
key, index, newAPIError := channel.GetNextEnabledKey()
if newAPIError != nil {
    return newAPIError
}
isMultiKey := channel.ChannelInfo.IsMultiKey
if !isMultiKey {
    index = model.RoutingMetricSingleKeyIndex
}
common.SetContextKey(c, constant.ContextKeyChannelKey, key)
common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, isMultiKey)
common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, index)
~~~

从 service/channel_select.go 删除 IsMultiKeyIndexRoutingAdmissible 与 AcquireMultiKeyRoutingHalfOpenProbe。smartRoutingCandidatesForGroup 始终加载 cost；仅 `!channel.ChannelInfo.IsMultiKey` 时加载 metric、inflight、reliability breaker 与 capacity。applyRoutingHealthMarkers/AffinityAdmissible 停止读取 legacy AuthFailure（该 marker 当前只来自 cost connector），但继续使用 balance 这一现有 channel-scope marker。

- [x] **Step 7: 在 model/flush/refresh 阻止不可信状态**

在 model/channel.go 增加稳定领域判断：

~~~go
func SupportsLegacyRoutingState(channelID int, apiKeyIndex int) bool {
    if channelID <= 0 || apiKeyIndex != RoutingMetricSingleKeyIndex {
        return false
    }
    info, err := CacheGetChannelInfo(channelID)
    return err == nil && info != nil && !info.IsMultiKey
}
~~~

UpsertRoutingChannelMetric 与 UpsertRoutingBreakerState 在业务字段校验后调用该 guard；无效状态安全返回 nil。模型三库契约创建真实 single-key Channel 后再验证 metric/breaker Upsert。

测试调用该 guard 时显式暂存并关闭 common.MemoryCacheEnabled，使 CacheGetChannelInfo 通过当前测试 DB 读取；结束时恢复全局值。生产内存缓存缺少 Channel 时 fail closed，不回退伪造状态。

pkg/routing_hotcache.LoadMetricSnapshots 与 LoadBreakerSnapshots 至少拒绝 APIKeyIndex != -1；LoadBreakerSnapshots 同时保留 Task 5 的 semantic-version guard。

controller.flushRoutingRuntimeState 对 drained metrics/dirty breakers 先按 SupportsLegacyRoutingState 过滤，只持久化和加载有效 slice；无效 runtime state 直接丢弃，不 requeue。refresh 查询先限制 api_key_index=-1，再按 SupportsLegacyRoutingState 过滤 metric 与当前 semantic breaker。routingBreakerModelsToSnapshots 保持纯转换，只额外拒绝 positive index 与错误 semantic version；Multi-Key aggregate 由调用者的 Channel-aware filter 拒绝。summary 使用过滤/实际接受数量。

- [x] **Step 8: 运行 GREEN 与竞态测试**

Run: go test ./relay/common ./pkg/routing_metrics ./pkg/routing_hotcache ./pkg/routing_breaker ./model ./service ./middleware ./controller -count=1

Expected: PASS。

Run: go test -race ./pkg/routing_metrics ./pkg/routing_hotcache ./pkg/routing_breaker ./service ./middleware ./controller -count=1

Expected: PASS，且无 race report。

- [x] **Step 9: 提交 Multi-Key smart-state 安全降级**

~~~bash
git add relay/common/relay_info.go relay/common/relay_info_test.go pkg/routing_metrics/metrics.go pkg/routing_metrics/metrics_test.go pkg/routing_hotcache/cache.go pkg/routing_hotcache/cache_test.go pkg/routing_breaker/breaker_test.go controller/relay.go controller/relay_retry_test.go controller/system_task_handlers.go controller/smart_routing_runtime_test.go service/channel_select.go service/channel_select_test.go middleware/distributor.go middleware/distributor_smart_routing_test.go model/channel.go model/routing_model.go model/routing_model_test.go
git commit -m "fix: disable unstable multi-key routing state"
~~~

### Task 7: 按 raw Key 重排 operational 状态并修复 origin task context

**Files:**
- Modify: model/channel.go
- Create: model/channel_multikey_test.go
- Modify: controller/channel.go
- Create: controller/channel_multikey_update_test.go
- Modify: relay/relay_task.go
- Create: relay/relay_task_test.go

- [x] **Step 1: 写 raw-Key 重排失败测试**

在 model/channel_multikey_test.go 增加：

~~~go
func TestRemapMultiKeyStateByRawKey(t *testing.T) {
    info := ChannelInfo{
        IsMultiKey: true,
        MultiKeyStatusList: map[int]int{
            0: common.ChannelStatusAutoDisabled,
            1: common.ChannelStatusManuallyDisabled,
        },
        MultiKeyDisabledReason: map[int]string{0: "auth", 1: "manual"},
        MultiKeyDisabledTime:   map[int]int64{0: 100, 1: 200},
        MultiKeyPollingIndex:   2,
    }

    info.RemapMultiKeyState(
        []string{"raw-a", "raw-b"},
        []string{"raw-b", "raw-new", "raw-a"},
    )

    assert.Equal(t, map[int]int{0: common.ChannelStatusManuallyDisabled, 2: common.ChannelStatusAutoDisabled}, info.MultiKeyStatusList)
    assert.Equal(t, map[int]string{0: "manual", 2: "auth"}, info.MultiKeyDisabledReason)
    assert.Equal(t, map[int]int64{0: 200, 2: 100}, info.MultiKeyDisabledTime)
    assert.Zero(t, info.MultiKeyPollingIndex)
    assert.NotContains(t, info.MultiKeyStatusList, 1)
}

func TestRemapMultiKeyStateClearsAmbiguousDuplicateMatches(t *testing.T) {
    info := ChannelInfo{
        IsMultiKey:             true,
        MultiKeyStatusList:     map[int]int{0: common.ChannelStatusAutoDisabled, 1: common.ChannelStatusManuallyDisabled, 2: common.ChannelStatusAutoDisabled},
        MultiKeyDisabledReason: map[int]string{0: "dup-a", 1: "dup-b", 2: "unique"},
        MultiKeyDisabledTime:   map[int]int64{0: 10, 1: 20, 2: 30},
    }

    info.RemapMultiKeyState([]string{"dup", "dup", "unique"}, []string{"unique", "dup", "dup"})

    assert.Equal(t, map[int]int{0: common.ChannelStatusAutoDisabled}, info.MultiKeyStatusList)
    assert.Equal(t, map[int]string{0: "unique"}, info.MultiKeyDisabledReason)
    assert.Equal(t, map[int]int64{0: 30}, info.MultiKeyDisabledTime)
}

func TestEnablingMultiKeyRemovesReasonAndTime(t *testing.T) {
    channel := &Channel{Key: "raw-a\nraw-b", ChannelInfo: ChannelInfo{
        IsMultiKey:             true,
        MultiKeyStatusList:     map[int]int{0: common.ChannelStatusAutoDisabled},
        MultiKeyDisabledReason: map[int]string{0: "auth"},
        MultiKeyDisabledTime:   map[int]int64{0: 100},
    }}

    handlerMultiKeyUpdate(channel, "raw-a", common.ChannelStatusEnabled, "")

    assert.NotContains(t, channel.ChannelInfo.MultiKeyStatusList, 0)
    assert.NotContains(t, channel.ChannelInfo.MultiKeyDisabledReason, 0)
    assert.NotContains(t, channel.ChannelInfo.MultiKeyDisabledTime, 0)
}
~~~

在 controller/channel_multikey_update_test.go 创建 raw-a\nraw-b 渠道、禁用 raw-b，再通过 UpdateChannel endpoint 以 key_mode=replace 更新为 raw-b\nraw-new\nraw-a；从 DB 重读后断言 raw-b 状态移动到 index 0，raw-new 默认 enabled，raw-a 保持自身状态。

- [x] **Step 2: 写 origin task context 失败测试**

在 relay/relay_task_test.go 使用 SQLite 表驱动 single/multi 两个 case：创建 origin Task 指向目标 Channel，Context 预置另一个渠道的 stale id/type/base/raw key/isMulti/index，RelayInfo.ChannelMeta=nil。调用 ResolveOriginTask 后断言：

~~~go
assert.Equal(t, target.Id, common.GetContextKeyInt(ctx, constant.ContextKeyChannelId))
assert.Equal(t, target.Type, common.GetContextKeyInt(ctx, constant.ContextKeyChannelType))
assert.Equal(t, target.GetBaseURL(), common.GetContextKeyString(ctx, constant.ContextKeyChannelBaseUrl))
assert.Equal(t, wantKey, common.GetContextKeyString(ctx, constant.ContextKeyChannelKey))
assert.Equal(t, target.ChannelInfo.IsMultiKey, common.GetContextKeyBool(ctx, constant.ContextKeyChannelIsMultiKey))
assert.Equal(t, wantIndex, common.GetContextKeyInt(ctx, constant.ContextKeyChannelMultiKeyIndex))
locked, ok := info.LockedChannel.(*model.Channel)
require.True(t, ok)
assert.Equal(t, target.Id, locked.Id)
~~~

Multi-Key fixture 让 index 0 operational disabled、index 1 enabled，wantKey/index 必须是第二个 Key/1；single case wantIndex=-1。

- [x] **Step 3: 运行测试确认 RED**

Run: go test ./model ./controller ./relay -run 'Test(RemapMultiKeyState|EnablingMultiKey|UpdateChannelRemapsMultiKeyState|ResolveOriginTaskSynchronizes)' -count=1

Expected: FAIL；Channel.Update 仍按 index 截断，ResolveOriginTask 丢弃 index 且不重写 isMulti。

- [x] **Step 4: 实现唯一 raw-Key 映射**

在 model/channel.go 增加纯内存领域方法：

~~~go
func (info *ChannelInfo) RemapMultiKeyState(oldKeys []string, newKeys []string) {
    oldCounts := make(map[string]int, len(oldKeys))
    newCounts := make(map[string]int, len(newKeys))
    oldIndexes := make(map[string]int, len(oldKeys))
    for index, key := range oldKeys {
        oldCounts[key]++
        oldIndexes[key] = index
    }
    for _, key := range newKeys {
        newCounts[key]++
    }

    statuses := make(map[int]int)
    reasons := make(map[int]string)
    disabledTimes := make(map[int]int64)
    for newIndex, key := range newKeys {
        if oldCounts[key] != 1 || newCounts[key] != 1 {
            continue
        }
        oldIndex := oldIndexes[key]
        status, ok := info.MultiKeyStatusList[oldIndex]
        if !ok || (status != common.ChannelStatusManuallyDisabled && status != common.ChannelStatusAutoDisabled) {
            continue
        }
        statuses[newIndex] = status
        if reason, ok := info.MultiKeyDisabledReason[oldIndex]; ok {
            reasons[newIndex] = reason
        }
        if disabledTime, ok := info.MultiKeyDisabledTime[oldIndex]; ok {
            disabledTimes[newIndex] = disabledTime
        }
    }
    info.MultiKeySize = len(newKeys)
    info.MultiKeyStatusList = statuses
    info.MultiKeyDisabledReason = reasons
    info.MultiKeyDisabledTime = disabledTimes
    info.MultiKeyPollingIndex = 0
}
~~~

只按完整解析后的 raw Key 相等匹配，不保存、不输出 raw Key，不使用 hash/HMAC/index 身份。只有 old/new 中都唯一的 Key 才继承 disabled 状态；新增/删除/重复/歧义位置清零。Reason/Time 只能随有效 disabled status 复制。

handlerMultiKeyUpdate 在启用 Key 时同时 delete status/reason/time。Channel.Update 现有越界清理同时覆盖三个 Map，并把越界/负值 polling index 归零。

- [x] **Step 5: 在最终 Key 列表形成后调用重排**

controller.UpdateChannel 完成 append/replace 逻辑后、channel.Update() 前执行：

~~~go
if channel.ChannelInfo.IsMultiKey && channel.Key != "" && channel.Key != originChannel.Key {
    channel.ChannelInfo.RemapMultiKeyState(originChannel.GetKeys(), channel.GetKeys())
}
~~~

若请求没有变更 Key，不重排现有状态；append 模式保留旧唯一 Key 状态，新 Key 默认 enabled。

- [x] **Step 6: 同步 origin task 的完整 credential context**

relay/relay_task.go::ResolveOriginTask 用当前 Context 的 channel id 比较，保留 LockedChannel，并把手工切换改为：

~~~go
if originTask.ChannelId != common.GetContextKeyInt(c, constant.ContextKeyChannelId) {
    key, index, apiErr := ch.GetNextEnabledKey()
    if apiErr != nil {
        return service.TaskErrorWrapperLocal(apiErr, string(apiErr.GetErrorCode()), apiErr.StatusCode)
    }
    isMultiKey := ch.ChannelInfo.IsMultiKey
    if !isMultiKey {
        index = model.RoutingMetricSingleKeyIndex
    }
    common.SetContextKey(c, constant.ContextKeyChannelId, ch.Id)
    common.SetContextKey(c, constant.ContextKeyChannelType, ch.Type)
    common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, ch.GetBaseURL())
    common.SetContextKey(c, constant.ContextKeyChannelKey, key)
    common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, isMultiKey)
    common.SetContextKey(c, constant.ContextKeyChannelMultiKeyIndex, index)
}
~~~

删除对可能为空/过期的 promoted info.ChannelId/ApiKey/ChannelType/ChannelBaseUrl 的手工赋值；RelayTaskSubmit 的 InitChannelMeta(c) 在每次 attempt 从 Context 重建元数据。

- [x] **Step 7: 运行 GREEN**

Run: go test ./model ./controller ./relay -count=1

Expected: PASS。

- [x] **Step 8: 提交 raw-Key operational 状态修复**

~~~bash
git add model/channel.go model/channel_multikey_test.go controller/channel.go controller/channel_multikey_update_test.go relay/relay_task.go relay/relay_task_test.go
git commit -m "fix: remap multi-key state by credential"
~~~

### Task 8: 把普通 Relay 接到统一分类、Reliability 与 Capacity

**Files:**
- Modify: controller/relay.go
- Modify: controller/relay_retry_test.go
- Modify: pkg/routing_metrics/metrics.go
- Modify: pkg/routing_breaker/breaker.go
- Modify: service/channel.go
- Create: service/channel_test.go
- Modify: service/violation_fee.go
- Create: service/violation_fee_test.go
- Modify: setting/operation_setting/status_code_ranges.go
- Modify: setting/operation_setting/status_code_ranges_test.go
- Modify: controller/channel-test.go

- [x] **Step 1: 写同一 classification 扇出的失败测试**

在 controller/relay_retry_test.go 用下列矩阵替换旧 Retry-After Breaker 与 backoff 测试：

~~~go
func singleKeyRoutingAttemptFixture(t *testing.T, channelID int) (*gin.Context, *relaycommon.RelayInfo) {
    t.Helper()
    routingmetrics.ResetForTest()
    t.Cleanup(routingmetrics.ResetForTest)
    ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
    common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, false)
    common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
    common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "vip")
    return ctx, &relaycommon.RelayInfo{
        UsingGroup:      "vip",
        OriginModelName: "gpt-test",
        StartTime:       time.Now(),
        ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: channelID, ChannelIsMultiKey: false},
    }
}

func TestRecordRoutingAttemptEffectsMatrix(t *testing.T) {
    tests := []struct {
        name              string
        sourceStatus      int
        responseStatus    int
        retryAfterMs      int64
        responsibility    routingerror.Responsibility
        health            routingerror.HealthEffect
        capacity          routingerror.CapacityEffect
        wantReliability   bool
        wantCapacity      bool
        wantBreakerReason string
    }{
        {name: "mapped 429 is capacity only", sourceStatus: 429, responseStatus: 503, responsibility: routingerror.ResponsibilityCapacity, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown, wantCapacity: true},
        {name: "529 is capacity only", sourceStatus: 529, responseStatus: 529, responsibility: routingerror.ResponsibilityCapacity, health: routingerror.HealthIgnore, capacity: routingerror.CapacityCooldown, wantCapacity: true},
        {name: "502 is reliability only", sourceStatus: 502, responseStatus: 502, responsibility: routingerror.ResponsibilityProvider, health: routingerror.HealthDegrade, capacity: routingerror.CapacityNone, wantReliability: true, wantBreakerReason: "5xx"},
        {name: "503 retry after has both effects", sourceStatus: 503, responseStatus: 503, retryAfterMs: 2500, responsibility: routingerror.ResponsibilityProvider, health: routingerror.HealthDegrade, capacity: routingerror.CapacityCooldown, wantReliability: true, wantCapacity: true, wantBreakerReason: "5xx"},
        {name: "caller 400 has neither effect", sourceStatus: 400, responseStatus: 400, responsibility: routingerror.ResponsibilityCaller, health: routingerror.HealthIgnore, capacity: routingerror.CapacityNone},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            configureRoutingBreakerAttemptTest(t, true)
            ctx, info := singleKeyRoutingAttemptFixture(t, 71)
            apiErr := types.NewErrorWithStatusCode(errors.New("failed"), types.ErrorCodeBadResponseStatusCode, tt.sourceStatus)
            apiErr.SetResponseStatusCode(tt.responseStatus)
            if tt.retryAfterMs > 0 {
                metadata, err := common.Marshal(map[string]int64{"retry_after_ms": tt.retryAfterMs})
                require.NoError(t, err)
                apiErr.Metadata = metadata
            }
            classification := routingerror.Classification{
                Responsibility: tt.responsibility,
                HealthEffect:   tt.health,
                CapacityEffect: tt.capacity,
                Component:      routingerror.ComponentServing,
            }

            recordRoutingAttemptEffects(ctx, info, 71, false, apiErr, classification)

            key := routinghotcache.Key{ChannelID: 71, APIKeyIndex: -1, Model: "gpt-test", Group: "vip"}
            _, hasCapacity := routinghotcache.GetCapacityCooldown(key)
            assert.Equal(t, tt.wantCapacity, hasCapacity)
            breaker, hasBreaker := routinghotcache.GetBreaker(key)
            assert.Equal(t, tt.wantReliability, hasBreaker)
            if tt.wantBreakerReason != "" && hasBreaker {
                assert.Equal(t, tt.wantBreakerReason, breaker.Reason)
            }
            metrics := routingmetrics.Snapshots()
            require.Len(t, metrics, 1)
            if tt.wantReliability {
                assert.Equal(t, int64(1), metrics[0].ReliabilityFailureCount)
            } else {
                assert.Zero(t, metrics[0].ReliabilityFailureCount)
            }
        })
    }
}
~~~

TestRecordRoutingAttemptEffectsMatrix 的 breaker fixture 把 threshold/degraded 配置设为 1，确保单次 reliability failure 可观察；Capacity deadline 断言 source 429 使用 SourceStatusCode 而不是映射后的 503。

- [x] **Step 2: 写 retry、stream 与内容安全失败测试**

增加：

~~~go
func TestShouldRetryUsesClassificationBeforeStatusOverlay(t *testing.T) {
    ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
    info := &relaycommon.RelayInfo{}
    caller := types.NewErrorWithStatusCode(errors.New("bad request"), types.ErrorCodeInvalidRequest, http.StatusBadRequest)
    timeout := types.NewErrorWithStatusCode(errors.New("timeout"), types.ErrorCodeFirstByteTimeout, http.StatusGatewayTimeout)

    assert.False(t, shouldRetry(ctx, info, caller, routingerror.Classification{Retryability: routingerror.RetryNever}, 1))
    assert.True(t, shouldRetry(ctx, info, timeout, routingerror.Classification{Retryability: routingerror.RetryBeforeCommit}, 1))
}

func TestClassifyRoutingRelayAttemptDistinguishesStreamCorruptionAndClientGone(t *testing.T) {
    corrupted := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
    corrupted.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, errors.New("scanner failed"))
    classification, success := classifyRoutingRelayAttempt(nil, corrupted)
    assert.False(t, success)
    assert.Equal(t, routingerror.ResponsibilityProvider, classification.Responsibility)
    assert.Equal(t, routingerror.HealthDegrade, classification.HealthEffect)

    clientGone := &relaycommon.RelayInfo{StreamStatus: relaycommon.NewStreamStatus()}
    clientGone.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, context.Canceled)
    classification, success = classifyRoutingRelayAttempt(nil, clientGone)
    assert.False(t, success)
    assert.Equal(t, routingerror.ResponsibilityCaller, classification.Responsibility)
    assert.Equal(t, routingerror.HealthIgnore, classification.HealthEffect)
}
~~~

在 service/violation_fee_test.go 增加 TestWrapAsViolationFeePreservesCauseAndSourceStatus：typed net cause、source 403、response 400 经 normalize 后 errors.As、SourceStatusCode 和 public violation code 均保留。

在 service/channel_test.go 增加 serving 401/403 credential 会自动禁用、content-safety 403 与 caller/gateway/config 不会自动禁用的表驱动测试。

- [x] **Step 3: 运行测试确认 RED**

Run: go test ./controller ./service ./setting/operation_setting -run 'Test(RecordRoutingAttemptEffects|ShouldRetryUsesClassification|ClassifyRoutingRelayAttempt|WrapAsViolationFeePreserves|ShouldDisableChannel)' -count=1

Expected: FAIL；Controller 仍分散记录、Retry-After 仍进入 Breaker/sleep、504 仍被硬编码禁止。

- [x] **Step 4: 建立每次 attempt 唯一分类结果**

在 controller/relay.go 增加：

~~~go
func classifyRoutingRelayAttempt(apiErr *types.NewAPIError, info *relaycommon.RelayInfo) (routingerror.Classification, bool) {
    ctx := routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationRelay}
    success := apiErr == nil
    if apiErr != nil && service.HasCSAMViolationMarker(apiErr) {
        ctx.Signal = routingerror.SignalContentSafety
    } else if info != nil && info.StreamStatus != nil {
        switch info.StreamStatus.EndReason {
        case relaycommon.StreamEndReasonFirstByteTimeout:
            ctx.Signal = routingerror.SignalFirstByteTimeout
        case relaycommon.StreamEndReasonClientGone:
            ctx.Signal = routingerror.SignalClientGone
            success = false
        case relaycommon.StreamEndReasonTimeout,
            relaycommon.StreamEndReasonScannerErr,
            relaycommon.StreamEndReasonPanic,
            relaycommon.StreamEndReasonPingFail:
            ctx.Signal = routingerror.SignalStreamCorruption
            success = false
        default:
            if info.StreamStatus.HasErrors() {
                ctx.Signal = routingerror.SignalStreamCorruption
                success = false
            }
        }
    }
    return routingerror.ClassifyAPIError(apiErr, ctx), success
}
~~~

普通 Relay 每次 handler 返回后顺序固定为：

~~~go
if newAPIError == nil {
    newAPIError = streamFirstByteTimeoutError(relayInfo)
}
classification, attemptSuccess := classifyRoutingRelayAttempt(newAPIError, relayInfo)
newAPIError = service.NormalizeViolationFeeError(newAPIError)
recordRoutingAttemptEffects(c, relayInfo, channel.Id, attemptSuccess, newAPIError, classification)
~~~

NormalizeViolationFeeError 必须在 metrics/effects 前完成，但 classification 使用 normalize 前的 typed cause/source 与显式 content-safety signal；全链只生成一次 Classification。

后续错误处理必须继续传递同一结果：

~~~go
processChannelError(c, channelError, newAPIError, classification)
if !shouldRetry(c, relayInfo, newAPIError, classification, common.RetryTimes-retryParam.GetRetry()) {
    break
}
~~~

- [x] **Step 5: 统一分发 metrics、Breaker 与 Capacity**

删除 recordRoutingBreakerAttempt，改为：

~~~go
func recordRoutingAttemptEffects(
    c *gin.Context,
    info *relaycommon.RelayInfo,
    channelID int,
    success bool,
    apiErr *types.NewAPIError,
    classification routingerror.Classification,
) {
    if info == nil || info.OriginModelName == "" || info.CurrentAttemptIsMultiKey(c) {
        return
    }
    routingmetrics.RecordClassifiedAttempt(c, info, channelID, success, apiErr, classification)
    if !smart_routing_setting.Enabled() {
        return
    }
    setting := smart_routing_setting.GetSetting()
    syncRoutingBreakerConfigFromSetting(setting)
    group := info.UsingGroup
    if group == "" {
        group = common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
    }
    if group == "" {
        group = "default"
    }
    key := routingbreaker.Key{ChannelID: channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: info.OriginModelName, Group: group}

    if success {
        routingbreaker.RecordReliabilitySuccess(key)
        return
    }
    if classification.CapacityEffect == routingerror.CapacityCooldown {
        maxCooldown := time.Duration(setting.MaxCooldownSec) * time.Second
        if maxCooldown <= 0 {
            maxCooldown = routingbreaker.DefaultConfig().MaxCooldown
        }
        baseCooldown := time.Duration(setting.BackoffBaseMs429) * time.Millisecond
        if baseCooldown <= 0 {
            baseCooldown = time.Second
        }
        retryAfter := retryAfterFromAPIError(apiErr, maxCooldown)
        routinghotcache.RecordCapacityCooldown(key.HotcacheKey(), apiErr.SourceStatusCode(), retryAfter, baseCooldown, maxCooldown, time.Now())
    }
    switch classification.Responsibility {
    case routingerror.ResponsibilityProvider:
        if classification.HealthEffect == routingerror.HealthDegrade || classification.HealthEffect == routingerror.HealthOpen {
            routingbreaker.RecordReliabilityFailure(key, routingbreaker.FailureProvider5xx)
        }
    case routingerror.ResponsibilityNetwork:
        if classification.HealthEffect == routingerror.HealthDegrade || classification.HealthEffect == routingerror.HealthOpen {
            routingbreaker.RecordReliabilityFailure(key, routingbreaker.FailureNetwork)
        }
    }
}
~~~

在 pkg/routing_breaker/breaker.go 增加稳定转换，避免 Controller 重复四字段映射：

~~~go
func (key Key) HotcacheKey() routinghotcache.Key {
    return routinghotcache.Key{ChannelID: key.ChannelID, APIKeyIndex: key.APIKeyIndex, Model: key.Model, Group: key.Group}
}
~~~

503+Retry-After 先写 Capacity，再写 Reliability；429/529/402 只写 Capacity；成功只更新 Reliability success。Capacity 从不写 aggregate/per-index Multi-Key。

- [x] **Step 6: 按分类边界重写 retry 与自动禁用**

shouldRetry 保留 client committed、次数、specific channel、affinity 与 skip-retry 安全检查，删除 types.IsChannelError 的无条件 true，随后：

~~~go
if classification.Retryability != routingerror.RetryBeforeCommit {
    return false
}
if operation_setting.IsAlwaysSkipRetryCode(apiErr.GetErrorCode()) {
    return false
}
statusCode := apiErr.SourceStatusCode()
if statusCode < 100 || statusCode > 599 {
    return true
}
return operation_setting.ShouldRetryByStatusCode(statusCode)
~~~

默认 AutomaticRetryStatusCodeRanges 改为包含 400–523 与 525–599；alwaysSkipRetryStatusCodes 只保留 408、524，移除 504。Caller/content/gateway 已被 classification RetryNever 拦截，因此开放默认 400 不会重试用户错误，却允许 model/config 400 和首字前 504 按 before_commit 重试。Operation setting 只能在 classification 允许后进一步限制/放行状态，不能把 RetryNever 提升为可重试。

processChannelError 与 ShouldDisableChannel 签名都增加 Classification；只有 ComponentServing + ResponsibilityCredential + ScopeCredential 才继续应用 skip/status/keyword overlay。默认禁用状态为 401 与 403 两个离散 range，不包含 402；content-safety 403 因 caller 分类不会禁用。

删除 sleepRoutingRetryBackoff、routingRetryBackoffDuration 及其测试。Retry-After 只冷却失败目标，有替代渠道时下一轮立即选择，不在切换前 sleep。

- [x] **Step 7: 让 violation normalize 保留证据**

service.WrapAsViolationFeeGrokCSAM 改为：

~~~go
func WrapAsViolationFeeGrokCSAM(apiErr *types.NewAPIError) *types.NewAPIError {
    if apiErr == nil {
        return nil
    }
    wrapped := types.NewErrorWithStatusCode(
        apiErr.Cause(),
        types.ErrorCodeViolationFeeGrokCSAM,
        apiErr.SourceStatusCode(),
        types.ErrOptionWithSkipRetry(),
    )
    wrapped.Metadata = apiErr.Metadata
    wrapped.SetMessage(apiErr.Error())
    wrapped.SetResponseStatusCode(apiErr.StatusCode)
    return wrapped
}
~~~

这一步不得重新解析/序列化 RelayError，也不得用 errors.New 替换 typed cause。

- [x] **Step 8: 运行 GREEN 与关键竞态测试**

Run: go test ./types ./pkg/routing_error ./pkg/routing_metrics ./pkg/routing_breaker ./pkg/routing_hotcache ./service ./setting/operation_setting ./controller -count=1

Expected: PASS。

Run: go test -race ./pkg/routing_metrics ./pkg/routing_breaker ./pkg/routing_hotcache ./service ./controller -count=1

Expected: PASS，且无 race report。

- [x] **Step 9: 提交通用 Relay 接线**

~~~bash
git add controller/relay.go controller/relay_retry_test.go pkg/routing_metrics/metrics.go pkg/routing_breaker/breaker.go service/channel.go service/channel_test.go service/violation_fee.go service/violation_fee_test.go setting/operation_setting/status_code_ranges.go setting/operation_setting/status_code_ranges_test.go controller/channel-test.go
git commit -m "fix: route classified relay outcomes"
~~~

### Task 9: 收紧 Task Submit 幂等边界并分离 Cost Connector health

**Files:**
- Modify: service/error.go
- Modify: service/error_test.go
- Modify: relay/relay_task.go
- Modify: relay/relay_task_test.go
- Modify: controller/relay.go
- Modify: controller/relay_retry_test.go
- Modify: controller/system_task_handlers.go
- Modify: controller/smart_routing_sub2api.go
- Modify: controller/smart_routing_task_test.go
- Modify: controller/smart_routing_test.go
- Modify: pkg/routing_metrics/metrics.go
- Modify: pkg/routing_metrics/metrics_test.go

- [x] **Step 1: 写 Task code、local 与 idempotency 失败测试**

在 controller/relay_retry_test.go 增加：

~~~go
func TestTaskErrorToAPIErrorPreservesOriginalCodeCauseAndRetryAfter(t *testing.T) {
    dnsErr := &net.DNSError{Name: "upstream.example.com"}
    taskErr := &dto.TaskError{
        Code:         string(types.ErrorCodeDoRequestFailed),
        Message:      "dial failed",
        StatusCode:   http.StatusBadGateway,
        RetryAfterMs: 2500,
        Error:        dnsErr,
    }

    apiErr := taskErrorToAPIError(taskErr)

    require.NotNil(t, apiErr)
    assert.Equal(t, types.ErrorCodeDoRequestFailed, apiErr.GetErrorCode())
    assert.Equal(t, http.StatusBadGateway, apiErr.SourceStatusCode())
    var extracted *net.DNSError
    require.ErrorAs(t, apiErr, &extracted)
    assert.Same(t, dnsErr, extracted)
    assert.Equal(t, 2500*time.Millisecond, retryAfterFromAPIError(apiErr, time.Minute))
}

func TestTaskSubmitUpstreamFailuresRequireIdempotencyAndDoNotRetry(t *testing.T) {
    ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
    ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
    ctx.Request.Header.Set("Idempotency-Key", "incoming-key-is-not-stably-forwarded")
    tests := []*dto.TaskError{
        {Code: string(types.ErrorCodeBadResponseStatusCode), StatusCode: http.StatusTooManyRequests, Error: errors.New("rate limited")},
        {Code: string(types.ErrorCodeBadResponseStatusCode), StatusCode: http.StatusBadGateway, Error: errors.New("bad gateway")},
        {Code: string(types.ErrorCodeDoRequestFailed), StatusCode: http.StatusBadGateway, Error: &net.DNSError{Name: "upstream.example.com"}},
    }

    for _, taskErr := range tests {
        classification := routingerror.ClassifyTaskError(taskErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationTaskSubmit})
        assert.Equal(t, routingerror.RetryIdempotencyRequired, classification.Retryability)
        assert.False(t, shouldRetryTaskRelay(ctx, taskErr, classification, 2))
    }
}

func TestTaskLocalErrorDoesNotAffectReliabilityBreakerOrCapacity(t *testing.T) {
    configureRoutingBreakerAttemptTest(t, true)
    ctx, info := singleKeyRoutingAttemptFixture(t, 72)
    taskErr := &dto.TaskError{Code: string(types.ErrorCodeModelPriceError), StatusCode: http.StatusBadRequest, LocalError: true, Error: errors.New("price missing")}
    classification := routingerror.ClassifyTaskError(taskErr, routingerror.Context{Component: routingerror.ComponentServing, Operation: routingerror.OperationTaskSubmit})

    recordRoutingAttemptEffects(ctx, info, 72, false, taskErrorToAPIError(taskErr), classification)

    snapshots := routingmetrics.Snapshots()
    require.Len(t, snapshots, 1)
    assert.Zero(t, snapshots[0].ReliabilityRequestCount)
    assert.Zero(t, snapshots[0].ReliabilityFailureCount)
    assert.Empty(t, routingbreaker.DirtySnapshots())
    _, ok := routinghotcache.GetCapacityCooldown(routinghotcache.Key{ChannelID: 72, APIKeyIndex: -1, Model: "gpt-test", Group: "vip"})
    assert.False(t, ok)
}
~~~

把旧 TestRecordRoutingTaskAttemptCapturesMetricsAndBreaker 改为显式用 ClassifyTaskError 的 provider failure，并断言原始 code 未被替换为 bad_response_status_code。

- [x] **Step 2: 写 RelayTaskSubmit 本地前置错误与 Retry-After 失败测试**

在 relay/relay_task_test.go 增加真实价格错误回归：

~~~go
func TestRelayTaskSubmitMarksModelPriceFailureLocal(t *testing.T) {
    gin.SetMode(gin.TestMode)
    ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
    ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"prompt":"draw a cat","model":"phase0b-unpriced-task"}`))
    ctx.Request.Header.Set("Content-Type", "application/json")
    ctx.Set("platform", strconv.Itoa(constant.ChannelTypeGemini))
    common.SetContextKey(ctx, constant.ContextKeyChannelId, 71)
    common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeGemini)
    common.SetContextKey(ctx, constant.ContextKeyChannelKey, "test-key")
    common.SetContextKey(ctx, constant.ContextKeyChannelBaseUrl, "https://example.invalid")
    common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, false)
    common.SetContextKey(ctx, constant.ContextKeyChannelMultiKeyIndex, model.RoutingMetricSingleKeyIndex)
    info := &relaycommon.RelayInfo{
        UserGroup:       "default",
        UsingGroup:      "default",
        OriginModelName: "phase0b-unpriced-task",
        TaskRelayInfo:   &relaycommon.TaskRelayInfo{},
    }

    _, taskErr := RelayTaskSubmit(ctx, info)

    require.NotNil(t, taskErr)
    assert.Equal(t, string(types.ErrorCodeModelPriceError), taskErr.Code)
    assert.True(t, taskErr.LocalError)
}
~~~

在 service/error_test.go 增加稳定的上游响应转换测试：

~~~go
func TestTaskErrorFromUpstreamResponsePreservesStatusAndRetryAfter(t *testing.T) {
    response := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
    response.Header.Set("Retry-After", "2")

    taskErr := TaskErrorFromUpstreamResponse(response, errors.New("rate limited"), time.Unix(100, 0))

    assert.Equal(t, string(types.ErrorCodeBadResponseStatusCode), taskErr.Code)
    assert.Equal(t, http.StatusTooManyRequests, taskErr.StatusCode)
    assert.Equal(t, int64(2000), taskErr.RetryAfterMs)
    assert.False(t, taskErr.LocalError)
}
~~~

- [x] **Step 3: 运行 Task RED**

Run: go test ./relay ./controller -run 'Test(RelayTaskSubmitMarksModelPriceFailureLocal|RelayTaskSubmitPreservesRetryAfter|TaskErrorToAPIError|TaskSubmitUpstreamFailuresRequireIdempotency|TaskLocalErrorDoesNotAffect)' -count=1

Expected: FAIL；Task code 被重建、价格错误非 local、429 header 未保留且 Task 仍按状态码重试。

- [x] **Step 4: 保留 TaskError 原始证据**

service/error.go 把 parseRetryAfterHeader 导出为 ParseRetryAfterHeader，RelayErrorHandler 与测试统一调用它，并增加可直接测试的协议转换：

~~~go
func TaskErrorFromUpstreamResponse(resp *http.Response, cause error, now time.Time) *dto.TaskError {
    if resp == nil {
        return TaskErrorWrapperLocal(errors.New("upstream response is nil"), string(types.ErrorCodeBadResponse), http.StatusInternalServerError)
    }
    if cause == nil {
        cause = fmt.Errorf("upstream task returned status %d", resp.StatusCode)
    }
    taskErr := TaskErrorWrapper(cause, string(types.ErrorCodeBadResponseStatusCode), resp.StatusCode)
    taskErr.RetryAfterMs = ParseRetryAfterHeader(resp.Header.Get("Retry-After"), now).Milliseconds()
    return taskErr
}
~~~

controller 增加：

~~~go
func taskErrorToAPIError(taskErr *dto.TaskError) *types.NewAPIError {
    if taskErr == nil {
        return nil
    }
    code := types.ErrorCode(taskErr.Code)
    if code == "" {
        code = types.ErrorCodeBadResponseStatusCode
    }
    cause := taskErr.Error
    if cause == nil {
        message := taskErr.Message
        if message == "" {
            message = "task relay failed"
        }
        cause = errors.New(message)
    }
    statusCode := taskErr.StatusCode
    if statusCode == 0 {
        statusCode = http.StatusInternalServerError
    }
    apiErr := types.NewErrorWithStatusCode(cause, code, statusCode)
    if taskErr.RetryAfterMs > 0 {
        metadata, err := common.Marshal(map[string]int64{"retry_after_ms": taskErr.RetryAfterMs})
        if err == nil {
            apiErr.Metadata = metadata
        }
    }
    return apiErr
}
~~~

relay/relay_task.go 的非 200 路径读取 body 后调用 service.TaskErrorFromUpstreamResponse(resp, fmt.Errorf("%s", body), time.Now())。DoRequest transport error使用 ErrorCodeDoRequestFailed 与 502；不得再统一写 fail_to_fetch_task 或在 controller 重建 code。

- [x] **Step 5: 标记所有发送前本地错误**

把以下路径改为 TaskErrorWrapperLocal：origin task DB/不存在/渠道不可用、无可用 Key、model mapping、model price、BuildRequestBody、本地预扣/Reserve/结算前置。保留为非 local 的只有已经进入上游边界的 transport、非 2xx、上游响应读取/解析/缺少 task id 与 adaptor 明确返回的上游拒绝。

TaskErrorFromAPIError 继续使用 Task 1 的原始 code、public message、包装 cause 和 LocalError=true 实现。

- [x] **Step 6: Task 每次 attempt 只分类一次且不自动跨渠道重试**

RelayTask 循环改为：

~~~go
result, taskErr = relay.RelayTaskSubmit(c, relayInfo)
classification := routingerror.ClassifyTaskError(taskErr, routingerror.Context{
    Component: routingerror.ComponentServing,
    Operation: routingerror.OperationTaskSubmit,
})
taskAPIError := taskErrorToAPIError(taskErr)
recordRoutingAttemptEffects(c, relayInfo, channel.Id, taskErr == nil, taskAPIError, classification)
if taskErr == nil {
    break
}
if !taskErr.LocalError {
    processChannelError(c, *types.NewChannelError(
        channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey,
        common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan(),
    ), taskAPIError, classification)
}
if !shouldRetryTaskRelay(c, taskErr, classification, common.RetryTimes-retryParam.GetRetry()) {
    break
}
~~~

shouldRetryTaskRelay 保留 nil/local/次数/specific channel/affinity 检查，最后只返回 classification.Retryability == RetryBeforeCommit。Task submit 的 provider/network/capacity/credential upstream 错误均为 IdempotencyRequired；当前 adaptor 没有稳定生成、持久化并转发的幂等键，因此入站 Idempotency-Key 也不能开启重试。删除原 429、307、5xx、400、408 状态分支。

生产调用迁移完后删除 routingmetrics.RecordAttempt 兼容入口。pkg/routing_metrics/metrics_test.go 增加测试夹具并替换全部旧调用：

~~~go
func recordTestAttempt(c *gin.Context, info *relaycommon.RelayInfo, channelID int, apiErr *types.NewAPIError) {
    classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{
        Component: routingerror.ComponentServing,
        Operation: routingerror.OperationRelay,
    })
    RecordClassifiedAttempt(c, info, channelID, apiErr == nil, apiErr, classification)
}
~~~

更新 controller/smart_routing_test.go 使用 RecordReliabilityFailure，不再依赖 generic Breaker RecordAttempt 制造状态；Breaker 的防御性 legacy wrapper 仅保留在 breaker 包自身契约测试。

- [x] **Step 7: 反转 Cost Connector serving health 测试**

在 controller/smart_routing_task_test.go：

- TestRunRoutingCostSyncTaskMarksAuthFailureOnUnauthorizedUpstream 改为 ...DoesNotMarkServingAuthFailure。
- Sub2API login/HTTP/envelope 401/403 测试只断言 routingAuthError、binding LastSyncError 与 SyncBackoffUntil。
- 成功同步前预置一个 RoutingChannelHealthState.AuthFailure 与 hotcache marker，成功后断言 connector 不清除它。

失败同步核心断言：

~~~go
_, cached := routinghotcache.GetAuthFailure(channelID)
assert.False(t, cached)
var marked int64
require.NoError(t, db.Model(&model.RoutingChannelHealthState{}).
    Where("channel_id = ? AND auth_failure = ?", channelID, true).
    Count(&marked).Error)
assert.Zero(t, marked)

var binding model.RoutingChannelBinding
require.NoError(t, db.Where("channel_id = ?", channelID).First(&binding).Error)
require.NotNil(t, binding.LastSyncError)
assert.Greater(t, binding.SyncBackoffUntil, common.GetTimestamp())
~~~

- [x] **Step 8: 删除 connector 对 serving AuthFailure 的写入**

controller/system_task_handlers.go 与 smart_routing_sub2api.go 删除所有 markRoutingAuthFailure/clearRoutingAuthFailure 调用，并删除这两个 connector-only helper。NewAPI /api/pricing、/api/user/self 及 Sub2API login/groups/rates/channels/usage 的 auth 错误仍返回 typed routingAuthError，runRoutingCostSyncTask 继续把错误写入 binding.LastSyncError 与 SyncBackoffUntil；成功只清 binding 自身 error/backoff，不修改 serving health。

保留 RoutingChannelHealthState 兼容字段与 API，不删除 schema；Task 6 已确保 legacy connector AuthFailure 不再参与 serving selector。

- [x] **Step 9: 运行 GREEN 与联合验证**

Run: go test ./service ./relay ./controller -run 'Test(TaskError|RelayTaskSubmit|TaskSubmitUpstreamFailures|TaskLocalError|RunRoutingCostSyncTask|FetchRoutingCostSnapshotsSub2API|RoutingSub2APIRequest)' -count=1

Expected: PASS。

Run: go test ./types ./pkg/routing_error ./pkg/routing_metrics ./pkg/routing_breaker ./pkg/routing_hotcache ./service ./relay ./controller ./model -count=1

Expected: PASS。

- [x] **Step 10: 提交 Task 与 Connector health 分离**

~~~bash
git add service/error.go service/error_test.go relay/relay_task.go relay/relay_task_test.go controller/relay.go controller/relay_retry_test.go controller/system_task_handlers.go controller/smart_routing_sub2api.go controller/smart_routing_task_test.go controller/smart_routing_test.go pkg/routing_metrics/metrics.go pkg/routing_metrics/metrics_test.go
git commit -m "fix: isolate task and connector health"
~~~

### Task 10: Phase 0B 需求审计与最终验证

**Files:**
- Modify if needed: files changed in Tasks 1–9
- Update: docs/superpowers/plans/2026-07-10-channel-routing-phase0b-error-capacity-multikey.md

- [x] **Step 1: 对照设计逐项审计**

逐项记录代码与测试证据：

- NewAPIError cause/display/source/response status 独立，映射不改变分类依据。
- 每个普通/Task attempt 只分类一次，同一 Classification 分发 metrics、Reliability Breaker、Capacity 与 retry。
- caller/gateway/config/credential/capacity 不进入 reliability denominator；成功与 provider/network failure 才进入。
- 429/529/402/有效 Retry-After 只进入独立 Capacity；503+Retry-After 同时进入 Reliability 与 Capacity。
- Capacity 有 TTL、硬上限、稳定淘汰、Stats、ClearChannel、Reset，且不写 DB/Redis、不 bypass、不 half-open。
- Breaker 只接受 typed Reliability outcome，Retry-After 不改变 cooldown；旧 semantic version 不 hydrate且可被当前版本覆盖。
- availability 只使用 reliability count，旧行 count=0 使用 neutral prior。
- 普通 Relay 首字前 504 可按 classification/operation overlay 重试；client committed、content safety 与 caller error 不重试；切换渠道前不 sleep Retry-After。
- Task local 错误 LocalError=true；Task submit upstream retry 为 idempotency_required，当前不跨渠道自动重试。
- Cost connector auth 只写 binding error/backoff，不写或清 serving AuthFailure；legacy marker 不参与 selector。
- 单 Key live/persisted state 唯一使用 -1；Multi-Key metrics/inflight/breaker/capacity 全 no-op，legacy positive/aggregate 不消费、不 hydrate、不持久化。
- Multi-Key operational status 按 raw Key 唯一匹配重排，重复/新增不继承；origin task context 同步 raw key/isMulti/index。
- 未引入 Credential ID/hash/HMAC、Capacity DB/Redis lease、Token Bucket/AIMD、公平份额、前端改动、生产部署或生产迁移。

- [x] **Step 2: 格式化并运行静态检查**

Run: gofmt -w types/error.go types/error_test.go dto/task.go pkg/routing_error/classifier.go pkg/routing_error/classifier_test.go model/routing_model.go model/routing_model_test.go model/channel.go model/channel_cache.go model/channel_multikey_test.go pkg/routing_metrics/metrics.go pkg/routing_metrics/metrics_test.go pkg/routing_hotcache/cache.go pkg/routing_hotcache/cache_test.go pkg/routing_hotcache/capacity.go pkg/routing_hotcache/capacity_test.go pkg/routing_breaker/breaker.go pkg/routing_breaker/breaker_test.go service/error.go service/error_test.go service/channel.go service/channel_test.go service/violation_fee.go service/violation_fee_test.go service/channel_select.go service/channel_select_test.go service/routing/types.go service/routing/selector.go service/routing/selector_test.go setting/operation_setting/status_code_ranges.go setting/operation_setting/status_code_ranges_test.go middleware/distributor.go middleware/distributor_smart_routing_test.go relay/common/relay_info.go relay/common/relay_info_test.go relay/relay_task.go relay/relay_task_test.go relay/channel/api_request.go relay/channel/task/ali/adaptor.go relay/channel/task/doubao/adaptor.go relay/channel/task/hailuo/adaptor.go relay/channel/task/jimeng/adaptor.go relay/channel/task/kling/adaptor.go relay/channel/task/sora/adaptor.go relay/channel/task/suno/adaptor.go relay/channel/task/vidu/adaptor.go controller/relay.go controller/relay_retry_test.go controller/channel.go controller/channel-test.go controller/channel_multikey_update_test.go controller/system_task_handlers.go controller/smart_routing_runtime_test.go controller/smart_routing_sub2api.go controller/smart_routing_task_test.go controller/smart_routing_test.go

Expected: exit 0。

Run: go vet ./types ./dto ./pkg/routing_error ./pkg/routing_metrics ./pkg/routing_hotcache ./pkg/routing_breaker ./service/routing ./service ./setting/operation_setting ./middleware ./relay/common ./relay ./relay/channel ./relay/channel/task/... ./controller ./model

Expected: exit 0。

- [x] **Step 3: 执行新鲜后端、竞态与前端基线验证**

Run: go test ./... -count=1

Expected: PASS，0 failures。

Run: go test -race ./types ./pkg/routing_error ./pkg/routing_metrics ./pkg/routing_hotcache ./pkg/routing_breaker ./service/routing ./service ./middleware ./relay/common ./relay ./relay/channel ./relay/channel/task/... ./controller -count=1

Expected: PASS，0 race reports。

Run: bun run typecheck（workdir: web/default）

Expected: exit 0；本阶段没有前端改动。

Run: bun run build（workdir: web/default）

Expected: exit 0。

- [x] **Step 4: 执行数据库、JSON、测试质量与治理审计**

Run: go test ./model -run 'TestRoutingModels(AutoMigrateAndMetricUpsert|ExternalDatabaseCompatibility)' -count=1 -v

Expected: SQLite PASS；ROUTING_TEST_MYSQL_DSN / ROUTING_TEST_POSTGRES_DSN 未配置时明确 SKIP，并在验证记录中如实注明；若配置则对应空测试库 PASS。

Run: git diff 6cc7abfa -- '*.go' | rg '^\+.*json\.(Marshal|Unmarshal|NewDecoder|NewEncoder)'

Expected: 无输出；新增 JSON 操作全部使用 common wrapper。json.RawMessage/json.Number 类型引用允许。

Run: git diff 6cc7abfa -- '*_test.go' | rg '^\+.*\bt\.(Fatal|Fatalf|FailNow)\b'

Expected: 无输出；新/重写 Go 测试使用 Testify require/assert。

Run: git diff 6cc7abfa -- . | rg '^-' | rg -i 'QuantumNous|new-api'

Expected: 无输出；若 import 上下文重排造成命中，逐项确认受保护引用在新增行完全原样保留，且没有重命名、替换或删除。

- [x] **Step 5: 检查 Diff、状态与实现范围**

Run: git diff --check

Expected: 无输出。

Run: git status --short

Expected: 只包含本计划文档的 checkbox/验证记录更新；代码提交均已完成。

Run: git log --oneline 6cc7abfa..HEAD

Expected: Tasks 1–9 均有独立、可审查提交，没有生产部署或迁移提交。

- [x] **Step 6: 更新验证记录并提交**

把所有已完成 checkbox 改为 [x]，在文末记录 commit range、各命令退出状态、外部数据库 SKIP/PASS、受保护标识审计与“未部署生产/未执行生产迁移”。

~~~bash
git add -f docs/superpowers/plans/2026-07-10-channel-routing-phase0b-error-capacity-multikey.md
git commit -m "docs: record phase 0b routing verification"
~~~

完成 Phase 0B 后只允许进入 Phase 1 Observe 的独立设计/计划；不得直接扩大主动选路流量。

## Phase 0B 验证记录（2026-07-11）

### 范围与审计结论

- 基准提交：`6cc7abfa`；代码与治理验证范围：`6cc7abfa..1bae9a0e`；分支：`feat/channel-routing-v2`。
- Task 1–10 已逐项对照实现与测试审计。错误证据、单次分类扇出、Reliability denominator、Capacity Cooldown、Breaker semantic version、Relay/Task retry、Cost Connector health、Single-Key/Multi-Key 隔离、raw Key operational 重排与 origin task context 均符合本计划边界。
- 10 个 task adaptor 均在成功响应写入前拒绝空白 upstream task ID；Gemini、Vertex 均先校验 `name` 再编码本地 task ID。
- 未引入 Credential ID/hash/HMAC、Capacity DB/Redis lease、Token Bucket/AIMD、公平份额或前端功能改动；未扩大主动选路流量。
- 计划内兼容 schema 列（Reliability、Err529、Breaker semantic version）已实现并通过 SQLite 契约测试；MySQL、PostgreSQL 因对应 DSN 未设置而 SKIP。本会话未连接生产、未部署，也未对生产执行 DDL/迁移。

### 提交记录

```text
1bae9a0e test: cover multi-key routing isolation
b5d0c86e fix: isolate task and connector health
1569d884 fix: route classified relay outcomes
04290b21 fix: preserve remapped multi-key polling state
2a4a0e78 fix: remap multi-key state by credential
0a2d96b6 fix: preserve routing state eligibility errors
624a32a7 fix: disable unstable multi-key routing state
0185533c fix: isolate routing reliability breaker
d2d83493 fix: refresh capacity on routing retry
e395d059 feat: add routing capacity cooldown
f705451c fix: merge routing metric flush deltas
4950591b fix: separate routing reliability metrics
85d41ef7 feat: classify routing error responsibility
a1e33f83 fix: preserve routing error evidence
deffc0d6 docs: plan phase 0b routing semantics
```

其中 `1bae9a0e` 为独立治理提交：恢复受保护 import，并用 Multi-Key half-open probe 回归断言证明 middleware Setup 不消费 Breaker 状态。

### 最终命令与退出状态

- `gofmt -w types/error.go types/error_test.go dto/task.go pkg/routing_error/classifier.go pkg/routing_error/classifier_test.go model/routing_model.go model/routing_model_test.go model/channel.go model/channel_cache.go model/channel_multikey_test.go pkg/routing_metrics/metrics.go pkg/routing_metrics/metrics_test.go pkg/routing_hotcache/cache.go pkg/routing_hotcache/cache_test.go pkg/routing_hotcache/capacity.go pkg/routing_hotcache/capacity_test.go pkg/routing_breaker/breaker.go pkg/routing_breaker/breaker_test.go service/error.go service/error_test.go service/channel.go service/channel_test.go service/violation_fee.go service/violation_fee_test.go service/channel_select.go service/channel_select_test.go service/routing/types.go service/routing/selector.go service/routing/selector_test.go setting/operation_setting/status_code_ranges.go setting/operation_setting/status_code_ranges_test.go middleware/distributor.go middleware/distributor_smart_routing_test.go relay/common/relay_info.go relay/common/relay_info_test.go relay/relay_task.go relay/relay_task_test.go relay/channel/api_request.go relay/channel/task/ali/adaptor.go relay/channel/task/doubao/adaptor.go relay/channel/task/hailuo/adaptor.go relay/channel/task/jimeng/adaptor.go relay/channel/task/kling/adaptor.go relay/channel/task/sora/adaptor.go relay/channel/task/suno/adaptor.go relay/channel/task/vidu/adaptor.go controller/relay.go controller/relay_retry_test.go controller/channel.go controller/channel-test.go controller/channel_multikey_update_test.go controller/system_task_handlers.go controller/smart_routing_runtime_test.go controller/smart_routing_sub2api.go controller/smart_routing_task_test.go controller/smart_routing_test.go`：exit 0；随后无代码 diff。
- `go vet ./types ./dto ./pkg/routing_error ./pkg/routing_metrics ./pkg/routing_hotcache ./pkg/routing_breaker ./service/routing ./service ./setting/operation_setting ./middleware ./relay/common ./relay ./relay/channel ./relay/channel/task/... ./controller ./model`：exit 0。
- `go test ./... -count=1`：exit 0，全部通过。
- `bun run typecheck`（`web/default`）：exit 0。
- `bun run build`（`web/default`）：exit 0，`built in 31.5s`。
- `go test ./model -run 'TestRoutingModels(AutoMigrateAndMetricUpsert|ExternalDatabaseCompatibility)' -count=1 -v`：exit 0；SQLite PASS；MySQL SKIP（未设置 `ROUTING_TEST_MYSQL_DSN`）；PostgreSQL SKIP（未设置 `ROUTING_TEST_POSTGRES_DSN`）。
- `go test ./relay/channel/task/... -count=1`：exit 0。
- `go test ./relay -run '^TestRelayTaskSubmitRejectsViduSuccessWithoutTaskIDBeforeWritingResponse$' -count=1`：exit 0。
- `git diff 6cc7abfa -- '*.go' | rg '^\+.*json\.(Marshal|Unmarshal|NewDecoder|NewEncoder)'`：exit 1、无输出，表示未新增直接 JSON 编解码调用。
- `git diff 6cc7abfa -- '*_test.go' | rg '^\+.*\bt\.(Fatal|Fatalf|FailNow)\b'`：exit 1、无输出，表示未新增手写 fatal assertion。
- `git diff 6cc7abfa -- . | rg '^-' | rg -i 'QuantumNous|new-api'`：exit 1、无输出，表示未删除受保护标识。
- `rg -n 'RecordAttempt\(' --glob '*.go' --glob '!pkg/routing_breaker/**'`：exit 1、无输出；业务代码无旧 Breaker 入口调用。
- `rg -n 'markRoutingAuthFailure|clearRoutingAuthFailure' --glob '*.go'`：exit 1、无输出。
- `rg -n 'ClassifyTaskError\(' controller/relay.go`：exit 0，仅命中一次（第 840 行）；`ClassifyAPIError` 同样仅由统一 helper 生产一次。
- `git diff --name-only 6cc7abfa..HEAD -- web/default web/classic`：exit 0、无输出；无 frontend diff。
- 部署/显式迁移路径审计 `git diff --name-only 6cc7abfa..HEAD | rg -i '(^|/)(deploy|deployment|migrations?|terraform|ansible|helm|k8s|docker-compose|compose)(/|\.|$)'`：exit 1、无输出。
- `git diff --check`：exit 0。
- `git status --short`：exit 0；更新本文档前工作树为空。
- `git log --oneline 6cc7abfa..HEAD`：exit 0，提交序列如上。

### Race 结果与基线归属

- 扩展后的完整 race 命令 `go test -race ./types ./pkg/routing_error ./pkg/routing_metrics ./pkg/routing_hotcache ./pkg/routing_breaker ./service/routing ./service ./middleware ./relay/common ./relay ./relay/channel ./relay/channel/task/... ./controller -count=1`：exit 1。除 `./service` 与 `./relay/channel` 外其余列出的包均 PASS；`./service` 命中两个已确认既有基线 race：
  - `TestUpdateVideoTasksDefaultSleepDoesNotBlockOtherChannels`：`logger/logger.go:112` 的并发日志状态访问，调用来自 `service/task_polling.go:358-373`。
  - `TestUpdateVideoTasksSlowChannelDoesNotBlockOtherChannels`：测试读取 `model/task.go:124` 的 task ID，同时 GORM 在 `model/task.go:432` 更新同一对象。
- `./relay/channel` 命中 HeaderOverride 并行测试对全局 `gin.SetMode` 的既有基线 race；独立 `count=20` 复现如下。
- `go test -race ./relay/channel -run '^TestProcessHeaderOverride_' -count=20`：exit 1；命中并行测试对全局 `gin.SetMode` 的既有基线 race（`relay/channel/api_request_test.go:85,108,132,155,186,213`）。
- `go test -race ./relay/channel/task/... -count=1`：exit 0，所有 task adaptor 包通过且无 race report。
- `git diff --exit-code 6cc7abfa..HEAD -- logger/logger.go model/task.go service/task_polling.go service/task_polling_test.go relay/channel/api_request_test.go`：exit 0，证明上述 race 源文件与基准完全一致。
- `go test -race ./middleware -run '^TestSetupContextForSelectedChannelUsesOperationalMultiKeyStateOnly$' -count=1`：exit 0。
- `go test -race ./relay -run '^Test(RelayTaskSubmit|ResolveOriginTask)' -count=1`：exit 0。

Race 总命令因此如实记录为非零，而不是 PASS；新增 Multi-Key、Task Submit 与 task adaptor 路径的定向 race 验证均通过。
