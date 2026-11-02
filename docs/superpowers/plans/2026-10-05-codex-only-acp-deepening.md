# Codex-only ACP deepening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把已存在的 ACP 协议和安全机制集中到可复用核心，保持现有 Codex 消费者和可观察行为不变。

**Architecture:** `acpclient` 拥有协议、会话、Trace、文件回调和进程组；`codexacp` 仅拥有固定 launcher、gateway 认证和 Codex update 判定，并显式转发六个 AgentAdapter 方法。三个受信任构造接缝为未来方案 3 留出位置，但本轮不实现 Factory/profile/YHC。

**Tech Stack:** Go；仓库现有 `github.com/coder/acp-go-sdk@v0.13.5`；现有 `golang.org/x/sys/unix`；Go test helper child process。

## Global Constraints

- 不可变开发基线：`72a96845c44c993723c4261a647b351374d0bcbc`；只在 `codex/acp-codex-only-deepening` 的 managed worktree 写入。
- 只修改 `internal/workerruntime/adapter/codexacp/**`、`internal/workerruntime/adapter/acpclient/**`、本主题独立设计/计划文档及 `AGENTS.md` 的一条 ACP 模块导航（已确认无观测侧写入交集）。
- `application.AgentAdapter`、domain/contracts、`shimruntime/**`、WorkerControl、startup outcome、Turn settlement、modelobs、secret registry、镜像/Compose、依赖版本、schema/migration 保持不变。
- 不实现 Factory、registry、profile 配置或 YHC；不接受 Task/WorkerControl 提供的 executable/env/path；不静默回退 Codex。
- 保留 `codexacp.Adapter`、`codexacp.New(trace io.Writer) (*Adapter, error)` 和现有六个公开方法的签名。
- 保留 exact identity/protocol、显式认证、resume 优先/load fallback、一个 Session/active Turn、有界更新、终态/取消、Trace 字节及 marker、记录后发送、文件/permission/terminal 限制、TERM/KILL 顺序和兼容错误文案。
- 不把缺失认证回调解释成无需认证；不把 Codex system-error metadata 解释成所有 ACP Agent 的默认规则。
- 测试通过已批准的 AgentAdapter、ACP stdio/Trace/peer 和受信任构造接缝观察结果，不新增只为测试公开的内部机制。
- 不编辑共享进度、不改主工作树或观测工作树；不自动 merge/rebase/push/修改远端 PR。
- `GOMAXPROCS=2`、`go test -p 1`；只用本地 helper process/临时文件，不运行宿主 Codex、Docker、数据库或真实模型。

---

## 📋 文件与职责

| 文件 | 职责 |
| --- | --- |
| `acpclient/adapter.go` | 单连接/Session/Turn 状态机与 SDK negotiation |
| `acpclient/options.go` | 受信任构造参数和显式认证策略校验 |
| `acpclient/process.go` | 原有 startProcess/Close/进程组机制 |
| `acpclient/rawtee.go` | 原有有界帧记录、脱敏、先记录后交付 |
| `acpclient/client.go` | 原有受限 Workspace 回调 |
| `codexacp/adapter.go` | 固定构造与六方法兼容门面 |
| `codexacp/policy.go` | 固定 launcher/env/UID、gateway 请求、Codex system-error 判定 |
| 两个包的 `*_test.go` | 门面兼容、核心构造与真实 ACP peer 契约 |

设计中的 Mermaid 交接图是结构说明的唯一图源，见[批准设计](../specs/2026-10-05-codex-only-acp-deepening-design.md)。无需重复制作第二幅相同结构图。下面所有既有代码的机械迁移均以不可变基线加 Task 1 的已审查增量为输入，不重写未变化的协议算法。

## ✍️ 实施任务

### Task 1: 在门面接缝固定多 Turn 与取消后复用行为

**Files:**

- Modify/Test: `internal/workerruntime/adapter/codexacp/adapter_test.go`
- 不改生产代码

**Interfaces:**

- Consumes: 现有 `newTestAdapter(t *testing.T, trace io.Writer) *Adapter`、`initializeTestAdapter(t *testing.T, ctx context.Context, adapter *Adapter)` 和 `application.AgentAdapter`
- Produces: `TestAdapterReusesSessionAfterTerminal`；通过 channel 关闭而非内部锁/字段判定的终态后幂等 Cancel characterization

- [x] **Step 1: 加入多 Turn characterization，一次一个 case**

```go
func TestAdapterReusesSessionAfterTerminal(t *testing.T) {
    for _, firstContent := range []string{"finish naturally", "wait for cancel"} {
        t.Run(firstContent, func(t *testing.T) {
            var trace safeTraceBuffer
            adapter := newTestAdapter(t, &trace)
            ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            initializeTestAdapter(t, ctx, adapter)
            sessionID, err := adapter.OpenSession(ctx, application.AgentSessionRequest{
                WorkerSessionID: "worker-session-1", WorkingCopyPath: "/workspace",
            })
            if err != nil { t.Fatal(err) }
            first, err := adapter.Prompt(ctx, application.AgentPromptRequest{
                AgentSessionID: sessionID, AgentTurnID: "turn-first", Content: firstContent,
            })
            if err != nil { t.Fatal(err) }
            if update := <-first; update.Kind != "agent_message_chunk" {
                t.Fatalf("first update = %+v", update)
            }
            want := `{"stop_reason":"end_turn"}`
            if firstContent == "wait for cancel" {
                if err := adapter.Cancel(ctx, sessionID, "turn-first"); err != nil { t.Fatal(err) }
                want = `{"stop_reason":"cancelled"}`
            }
            terminal := <-first
            if terminal.Kind != "terminal" || string(terminal.Payload) != want {
                t.Fatalf("first terminal = %+v, want %s", terminal, want)
            }
            if _, open := <-first; open { t.Fatal("first channel remained open") }
            if err := adapter.Cancel(ctx, sessionID, "turn-first"); err != nil { t.Fatal(err) }
            second, err := adapter.Prompt(ctx, application.AgentPromptRequest{
                AgentSessionID: sessionID, AgentTurnID: "turn-second", Content: "finish naturally",
            })
            if err != nil { t.Fatalf("reuse Session: %v", err) }
            var kinds []string
            for update := range second { kinds = append(kinds, update.Kind) }
            if strings.Join(kinds, ",") != "agent_message_chunk,terminal" {
                t.Fatalf("second kinds = %v", kinds)
            }
        })
    }
}
```

`safeTraceBuffer` 用于 Trace 外部观察，不接触 adapter 内部状态：

```go
type safeTraceBuffer struct {
    mu sync.Mutex
    buffer bytes.Buffer
}
func (b *safeTraceBuffer) Write(p []byte) (int, error) {
    b.mu.Lock(); defer b.mu.Unlock()
    return b.buffer.Write(p)
}
func (b *safeTraceBuffer) Bytes() []byte {
    b.mu.Lock(); defer b.mu.Unlock()
    return bytes.Clone(b.buffer.Bytes())
}
```

将使用 Trace 的 `var trace bytes.Buffer` 机械改为 `var trace safeTraceBuffer`，保留 peer byte buffer，不改变断言。既有 `TestAdapterTreatsCancelAfterQueuedTerminalAsIdempotent` 删除 `adapter.mu`/`activeTurnID` polling；先消费 updates 直到关闭，再调用同 Turn Cancel，并保留 literal kinds 与“无 session/cancel RPC”断言。改名为 `TestAdapterTreatsCancelAfterCompletedTerminalAsIdempotent`，不宣称该测试确定性覆盖尚未消费的排队竞态。核心原有排队终态算法在 Task 2 原样迁移。

- [x] **Step 2: 运行 characterization 并记录真实结果**

Run: `GOMAXPROCS=2 go test -p 1 -count=1 ./internal/workerruntime/adapter/codexacp -run 'TestAdapter(ReusesSessionAfterTerminal|TreatsCancelAfterCompletedTerminalAsIdempotent)'`

Expected: PASS，既有正确行为不应为了制造 RED 而修改。若失败，报告实际输出，暂停提取；不顺手修生产行为。基线普通测试和 race 在 `9c0a493` 已分别 PASS（0.589s / 15.638s）。

- [x] **Step 3: 完整局部验证、格式和提交**

Run: `GOMAXPROCS=2 go test -race -p 1 -count=1 ./internal/workerruntime/adapter/codexacp`；`git diff --check`；`gofmt -w internal/workerruntime/adapter/codexacp/adapter_test.go`

Expected: exit 0，无 race；仅该测试文件改变。

```sh
git add internal/workerruntime/adapter/codexacp/adapter_test.go
git commit -m "test: characterize reusable Codex ACP sessions"
```

### Task 2: 提取 ACP 核心并保持固定 Codex 兼容门面

**Files:**

- Create: `internal/workerruntime/adapter/acpclient/{options,adapter,process,rawtee,client}.go`
- Create/Test: `internal/workerruntime/adapter/acpclient/options_test.go`
- Move/Test: 原 `codexacp/client_test.go` → `acpclient/client_test.go`；原 adapter_test 中三个 rawtee 机制测试 → `acpclient/rawtee_test.go`（Task 3 替换为接缝测试）
- Modify: `internal/workerruntime/adapter/codexacp/adapter.go`
- Create: `internal/workerruntime/adapter/codexacp/policy.go`
- Modify/Test: `internal/workerruntime/adapter/codexacp/adapter_test.go`

**Interfaces:**

- Consumes: `application.AgentAdapter` 及其原有请求/结果类型；Task 1 的外部行为测试
- Produces: `acpclient.Authentication`、`acpclient.Options`、`acpclient.New(options Options) (*Adapter, error)`；`*acpclient.Adapter` 实现不变的六方法 `AgentAdapter`；Codex facade 只组合一个 `*acpclient.Adapter`

- [x] **Step 1: 首个 RED 是缺失核心构造接缝，不是假造行为变化**

在 `package acpclient_test` 写一个最小 tracer：

```go
func TestNewRequiresExplicitAuthentication(t *testing.T) {
    _, err := acpclient.New(acpclient.Options{
        Command: func() *exec.Cmd { return exec.Command("not-started") },
        ProcessName: "fixture-acp", WorkspaceRoot: "/workspace", Trace: io.Discard,
    })
    if err == nil || err.Error() != "ACP authentication policy is required" {
        t.Fatalf("missing authentication = %v", err)
    }
}
```

导入 `testing`、`io`、`os/exec` 和本仓库 `adapter/acpclient`。Run: `GOMAXPROCS=2 go test -p 1 -count=1 ./internal/workerruntime/adapter/acpclient -run TestNewRequiresExplicitAuthentication`；Expected: package/constructor 尚未存在的编译失败，记录实际输出。然后才增加构造和迁移，不横向预写全部测试。

- [x] **Step 2: 写最小受信任构造对象**

`options.go` 的类型和构造函数：

```go
// Authentication is selected by trusted adapter composition, not Task input.
type Authentication struct {
    MethodID string
    Capabilities sdk.AuthCapabilities
    Request func(proxyToken string) (sdk.AuthenticateRequest, error)
}

// Options is internal Go composition. Callers own immutable policy values.
type Options struct {
    Command func() *exec.Cmd
    ProcessName string
    WorkspaceRoot string
    Trace io.Writer
    Authentication Authentication
    IsSystemError func(sdk.SessionNotification) bool
}

func New(options Options) (*Adapter, error) {
    if options.Command == nil || options.Trace == nil || options.WorkspaceRoot == "" || options.ProcessName == "" {
        return nil, fmt.Errorf("ACP adapter process, workspace and Trace are required")
    }
    if _, err := workspaceRelativePath(options.WorkspaceRoot, options.WorkspaceRoot+"/.switchback-check"); err != nil {
        return nil, fmt.Errorf("ACP adapter workspace root is invalid")
    }
    auth := options.Authentication
    if auth.MethodID == "" || strings.TrimSpace(auth.MethodID) != auth.MethodID ||
        strings.ContainsAny(auth.MethodID, "\r\n\x00") || auth.Request == nil {
        return nil, fmt.Errorf("ACP authentication policy is required")
    }
    return &Adapter{options: options, state: stateNew, recorder: newTraceRecorder(options.Trace)}, nil
}
```

`IsSystemError == nil` 表示没有 Agent 专属附加错误规则，公共协议、token、身份、文件、背压检查仍不可豁免。没有 `Authentication.Request` 必须构造失败。不得增加认证 bypass、schema/JSON 配置或更多尚无实际变化点的 callback。

- [x] **Step 3: 原样迁移协议机制，仅替换以下策略引用**

将原 `adapter.go` 的 Adapter 状态机及全部六方法/私有方法迁入 core；移除固定 Codex 常量、`New/newAdapter`、`isCodexSystemError`、runtimesecret/os imports。将 `startProcess` 与 `Close` 原样放到 `process.go`，保留状态/锁、500ms TERM 等待和 1s KILL 确认。字段类型 `processOptions` → `Options`，`command/workspaceRoot/trace` → 对应大写字段；`gatewayAuth` → `authenticationDeclared`。

认证和 update 变化只限以下代码块：

```go
// In Initialize's existing ClientCapabilities literal:
Auth: a.options.Authentication.Capabilities,

// Replace existing response protocol/auth gate, preserving its location:
if response.ProtocolVersion != sdk.ProtocolVersionNumber ||
    !hasAuthMethod(response.AuthMethods, a.options.Authentication.MethodID) {
    a.failClosed()
    return application.AgentCapabilities{}, fmt.Errorf("ACP protocol or %s authentication capability is unsupported", a.options.Authentication.MethodID)
}

// In observeSessionUpdate, at the original classifier location:
if a.options.IsSystemError != nil && a.options.IsSystemError(notification) {
    a.systemError = true
}
```

`Authenticate` 使用以下完整方法，公共 token 校验函数原样迁移；回调错误不暴露 token：

```go
func (a *Adapter) Authenticate(ctx context.Context, proxyToken string) error {
    if err := validateProxyToken(proxyToken); err != nil { return err }
    request, err := a.options.Authentication.Request(proxyToken)
    if err != nil || request.MethodId != a.options.Authentication.MethodID {
        return fmt.Errorf("ACP authentication request is invalid")
    }
    a.mu.Lock()
    if a.state != stateInitialized || !a.authenticationDeclared || a.conn == nil {
        a.mu.Unlock()
        return fmt.Errorf("ACP adapter is not ready for authentication")
    }
    connection := a.conn
    a.recorder.setProxyToken(proxyToken)
    a.mu.Unlock()
    if _, err := connection.Authenticate(ctx, request); err != nil {
        a.failClosed()
        return fmt.Errorf("authenticate ACP %s", a.options.Authentication.MethodID)
    }
    a.mu.Lock()
    defer a.mu.Unlock()
    if a.state != stateInitialized { return fmt.Errorf("ACP authentication result was fenced") }
    a.state = stateAuthenticated
    return nil
}
```

进程错误中唯一动态部分为受信任 `ProcessName`，完整 format 清单：

```go
fmt.Errorf("create %s child", a.options.ProcessName)
fmt.Errorf("open %s stdin", a.options.ProcessName)
fmt.Errorf("open %s stdout", a.options.ProcessName)
fmt.Errorf("start %s child", a.options.ProcessName)
fmt.Errorf("%s process group is unverified", a.options.ProcessName)
fmt.Errorf("%s termination could not be confirmed", a.options.ProcessName)
```

将现有 `rawtee.go`、`client.go` 内容原样迁移，只改 package 声明。原 rawtee/client tests 在本任务先随机制迁移，保持字节/权限 oracle，不删未替代覆盖；下一个独立任务把它们换成公开接缝测试。不得导出 recorder/frameWriter/protocolClient 或降低帧限制。

- [x] **Step 4: Codex facade 不再有状态机或协议实现**

`policy.go` 保留原常量、固定环境和 `isCodexSystemError` 的完整原始函数。固定 command factory 原样迁移为 `fixedCommand() *exec.Cmd`：原 `New` 里 command closure 的函数体，不继承环境。gateway 策略如下：

```go
func gatewayAuthentication() acpclient.Authentication {
    return acpclient.Authentication{
        MethodID: "gateway",
        Capabilities: sdk.AuthCapabilities{Meta: map[string]any{"gateway": true}},
        Request: func(token string) (sdk.AuthenticateRequest, error) {
            return sdk.AuthenticateRequest{
                MethodId: "gateway",
                Meta: map[string]any{"gateway": map[string]any{
                    "baseUrl": proxyBaseURL, "providerName": providerName,
                    "headers": map[string]string{"Authorization": "Bearer " + token},
                }},
            }, nil
        },
    }
}
```

facade `adapter.go` 保留旧测试的受信任构造 seam，但不暴露新 Task 选项：

```go
type processOptions struct {
    command func() *exec.Cmd
    workspaceRoot string
    trace io.Writer
}
type Adapter struct { client *acpclient.Adapter }
var _ application.AgentAdapter = (*Adapter)(nil)

func New(trace io.Writer) (*Adapter, error) {
    return newAdapter(processOptions{command: fixedCommand, workspaceRoot: workspaceRoot, trace: trace})
}
func newAdapter(options processOptions) (*Adapter, error) {
    if options.command == nil || options.trace == nil || options.workspaceRoot == "" {
        return nil, fmt.Errorf("CodexAdapter process, workspace and Trace are required")
    }
    client, err := acpclient.New(acpclient.Options{
        Command: options.command, ProcessName: "codex-acp", WorkspaceRoot: options.workspaceRoot,
        Trace: options.trace, Authentication: gatewayAuthentication(), IsSystemError: isCodexSystemError,
    })
    if err != nil { return nil, fmt.Errorf("CodexAdapter workspace root is invalid") }
    return &Adapter{client: client}, nil
}
func (a *Adapter) Initialize(ctx context.Context, r application.AgentInitializeRequest) (application.AgentCapabilities, error) {
    return a.client.Initialize(ctx, r)
}
func (a *Adapter) Authenticate(ctx context.Context, token string) error { return a.client.Authenticate(ctx, token) }
func (a *Adapter) OpenSession(ctx context.Context, r application.AgentSessionRequest) (string, error) { return a.client.OpenSession(ctx, r) }
func (a *Adapter) Prompt(ctx context.Context, r application.AgentPromptRequest) (<-chan application.AgentUpdate, error) { return a.client.Prompt(ctx, r) }
func (a *Adapter) Cancel(ctx context.Context, sessionID, turnID string) error { return a.client.Cancel(ctx, sessionID, turnID) }
func (a *Adapter) Close(ctx context.Context) error { return a.client.Close(ctx) }
```

以上构造错误映射只用于固定且完整的 Codex policy：当前可失败的 core 输入校验是 Workspace。不得用此映射掩盖实现中额外引入的失败原因，若增加校验应同时让错误准确且兼容。

- [x] **Step 5: 后续构造校验 vertical slices 和局部门禁**

逐例增加 `TestNewRejectsIncompleteOptions`：literal zero/nil Command、Trace、Workspace、ProcessName 均拒绝；relative Workspace 拒绝；合法绝对根可构造后 `Close(context.Background()) == nil`，不启动命令。逐例增加 missing MethodID、nil Request、含 CR/LF/NUL/前后空白 MethodID 拒绝。Test 表的输入必须 literal，不从校验函数推导。

Run: `GOMAXPROCS=2 go test -p 1 -count=1 ./internal/workerruntime/adapter/acpclient ./internal/workerruntime/adapter/codexacp`；`git diff --check`；仅格式化上述文件。

Expected: exit 0；Task 1 lifecycle oracle 仍 PASS；原始帧和文件安全 oracle 不变；`codexacp` 不再出现 recorder/frameWriter/protocolClient/activeTurnID 状态机。

```sh
git add internal/workerruntime/adapter/acpclient internal/workerruntime/adapter/codexacp
git commit -m "refactor: deepen ACP client behind fixed Codex facade"
```

### Task 3: 替换内部机制测试并补齐公开核心安全契约

**Files:**

- Create/Test: `internal/workerruntime/adapter/acpclient/adapter_test.go`（生命周期、认证和 classifier）
- Create/Test: `internal/workerruntime/adapter/acpclient/frames_test.go`（Trace、原始帧和交付顺序）
- Create/Test: `internal/workerruntime/adapter/acpclient/callbacks_test.go`（文件、permission 和 terminal）
- Create/Test: `internal/workerruntime/adapter/acpclient/peer_test.go`（真实 stdio fixture）
- Remove: 被替代的 `internal/workerruntime/adapter/acpclient/{rawtee,client}_test.go`
- Modify/Test: `internal/workerruntime/adapter/codexacp/adapter_test.go`（仅门面策略契约）
- Modify: 本计划及 `docs/superpowers/specs/2026-10-05-codex-only-acp-deepening-design.md` 的当前源码链接/实际验证状态
- Modify: `AGENTS.md`，仅增加一条已与观测 owner 确认归属的 ACP 分工导航；保留其他内容

**Interfaces:**

- Consumes: Task 2 的 `acpclient.Options/Authentication/New` 和六方法 `AgentAdapter`
- Produces: 以 real stdio child 为外部依赖的核心安全/策略 tests；没有导出新生产符号；带 exact HEAD/命令/限制的交接记录

- [x] **Step 1: 添加独立真实 peer，以 public New 为唯一核心构造入口**

用 `package acpclient_test`；测试 child 通过当前 Go test binary 启动，不运行任何宿主 Agent。peer 只实现该测试所需的真实 JSON-RPC，以请求/响应和 filesystem 结果为证据。公共构造/初始化代码：

```go
func newPeerAdapter(t *testing.T, root, scenario string, trace io.Writer) (*acpclient.Adapter, context.Context) {
    t.Helper()
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    t.Cleanup(cancel)
    a, err := acpclient.New(acpclient.Options{
        Command: func() *exec.Cmd {
            cmd := exec.Command(os.Args[0], "-test.run=^TestCorePeerProcess$")
            cmd.Env = append(os.Environ(), "SWITCHBACK_CORE_PEER=1", "SWITCHBACK_CORE_SCENARIO="+scenario)
            return cmd
        },
        ProcessName: "fixture-acp", WorkspaceRoot: root, Trace: trace,
        Authentication: acpclient.Authentication{
            MethodID: "fixture-auth",
            Request: func(token string) (sdk.AuthenticateRequest, error) {
                return sdk.AuthenticateRequest{MethodId: "fixture-auth", Meta: map[string]any{"token": token}}, nil
            },
        },
    })
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() {
        closeCtx, done := context.WithTimeout(context.Background(), time.Second)
        defer done()
        if err := a.Close(closeCtx); err != nil { t.Errorf("close: %v", err) }
    })
    return a, ctx
}
```

`TestCorePeerProcess` 在没有 `SWITCHBACK_CORE_PEER=1` 时 return，这是启动入口，不是空断言行为测试。peer 用 `json.Decoder(os.Stdin)`/`json.Encoder(os.Stdout)` 保留任意 JSON-RPC id。固定 wire fixture：

```json
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true},"authMethods":[{"id":"fixture-auth","name":"Fixture"}]}}
{"jsonrpc":"2.0","id":2,"result":{}}
{"jsonrpc":"2.0","id":3,"result":{"sessionId":"fixture-session"}}
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"peer-ok"}}}}
{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}
```

这里 id 为示例 literal；peer reply 必须用原请求 id，不能假定 SDK id 从 1 开始。child 在 prompt 中可发真实 client callback，并在读取 callback response 后把独立判断编码进 `agent_message_chunk.content.text`。先只实现 lifecycle tracer，运行后再逐个加入下面 scenario，不能一次横向写完所有测试。

公开结果示例：

```go
func TestCorePeerCompletesTypedPrompt(t *testing.T) {
    root := t.TempDir()
    a, ctx := newPeerAdapter(t, root, "normal", io.Discard)
    caps, err := a.Initialize(ctx, application.AgentInitializeRequest{
        WorkerSessionID: "fixture-worker", RuntimeID: "fixture-runtime", Generation: 1, ProtocolVersion: "1",
    })
    if err != nil || caps.ProtocolVersion != "1" || !caps.LoadSession { t.Fatalf("initialize: %+v %v", caps, err) }
    if err := a.Authenticate(ctx, "fixture-proxy-token"); err != nil { t.Fatal(err) }
    sessionID, err := a.OpenSession(ctx, application.AgentSessionRequest{
        WorkerSessionID: "fixture-worker", WorkingCopyPath: root,
    })
    if err != nil || sessionID != "fixture-session" { t.Fatalf("open: %q %v", sessionID, err) }
    updates, err := a.Prompt(ctx, application.AgentPromptRequest{
        AgentSessionID: sessionID, AgentTurnID: "fixture-turn", Content: "normal",
    })
    if err != nil { t.Fatal(err) }
    var kinds []string
    var terminal []byte
    for update := range updates {
        kinds = append(kinds, update.Kind)
        if update.Kind == "terminal" { terminal = update.Payload }
    }
    if strings.Join(kinds, ",") != "agent_message_chunk,terminal" ||
        string(terminal) != `{"stop_reason":"end_turn"}` {
        t.Fatalf("updates = %v, terminal = %s", kinds, terminal)
    }
}
```

tracer 使用同一 root，验证 literal kinds、terminal payload 并消费到 channel 关闭；不从内部字段或默认策略反推预期。

- [x] **Step 2: 按独立 literal oracle 逐个替换机制测试**

每个 scenario 先写公开断言、运行证明相应 fixture 尚未支持/被故障注入时失败，再最小完善 peer；生产代码不为测试新增 hooks。既有正确行为 PASS 的 characterization 如实记录，不制造 RED。

| Scenario | 通过接缝验证的 literal oracle |
| --- | --- |
| fragmented/coalesced stdout | 分割 initialize reply 和多个 update；typed result 正常，Trace 解码后的 frame 等于 peer 发出的完整原文 |
| unknown optional update | `future_update` 原文进入 `acp.unknown_update`，不进入 typed updates，prompt 仍 end_turn |
| trace-before-inbound | Trace writer 记录 update 后，调用者才收到该 typed update；用安全 latch/快照，不读内部 pump |
| trace-fails-before-outbound | Trace 对 authenticate 失败时，peer 没收到 authenticate；Initialize 成功；Authenticate 失败且 token 无落盘；peer 已收请求通过 child 单独 evidence 文件记录 |
| token redaction | 认证 Trace 包含 `[REDACTED:litellm_proxy_token]`，不含 `fixture-proxy-token`，peer 验证收到原 token |
| method mismatch/builder error | 配置 builder 返回不同 MethodId 或 error 时 Authenticate 拒绝，无 authenticate peer traffic，无 token/error 泄漏；无 nil-auth bypass |
| capability mismatch | peer 不声明配置的 fixture-auth 或回复 protocol 2，Initialize fail closed；后续认证/session 拒绝 |
| workspace files | peer 发 `fs/write_text_file` 和按 line=2/limit=1 的 read，得到 `line two`；非法外部、ancestor symlink、final symlink、目录、FIFO 的 read/write 都拒绝，外部文件仍是 `outside-secret` |
| permission/terminal | permission cancelled，无 selected；`terminal/create`、kill、output、release、wait_for_exit 均 error |
| close | helper process exit 经 `Close` 成功确认，重复 Close 幂等，closed 后 Initialize/Prompt 拒绝 |
| classifier locality | 同一 Codex-shaped system-error metadata 在 nil classifier 核心不默认失败；Codex facade 使用其策略时 normalized `error` 且 `code=acp_prompt_failed` |

callback 响应必须由 peer 验证并发送结果，父测试也独立核查合法/非法文件最终内容；不得把 parent 本地直接调用 protocolClient 当成 stdio 验证。Trace/evidence 文件属于测试自己的 `t.TempDir()`，不得读取协作会话证据。对 outbound fail 用 fixture evidence 在 Close 确认子进程退出后读取，不能用 sleep 推定未投递。

所有替代接缝测试通过后才删原 `rawtee_test.go/client_test.go`，消除浅层测试重复，不删安全覆盖。保留纯 constructor 校验 tests。Codex 门面增加 `New(nil)` 兼容文案断言，以及固定 command/env/UID 的受信任构造策略检查；不真的启动固定生产可执行文件。

- [x] **Step 3: 低并发回归、文档与切片提交**

先运行两个包 ordinary/race；经 owner 同步可运行所有 workerruntime 短测试、architecture 短测试和 shim compile/单元（不带 integration tag）。

```sh
GOMAXPROCS=2 go test -p 1 -count=1 ./internal/workerruntime/adapter/acpclient ./internal/workerruntime/adapter/codexacp
GOMAXPROCS=2 go test -race -p 1 -count=1 ./internal/workerruntime/adapter/acpclient ./internal/workerruntime/adapter/codexacp
GOMAXPROCS=2 go test -short -p 1 -count=1 ./internal/workerruntime/... ./internal/architecture
git diff --check
```

Expected: exit 0；需要网络/服务的 test 若未 guard 应报告并停止扩展，不创建环境补跑。不把这些结果宣称为真实 Codex/YHC 或观测物理矩阵验收。

设计源码链接改为实际 current paths，同时保留不可变基线说明；计划勾选以实际审查结果为准。交接记录必须列出实现 HEAD、changed paths、每条测试命令与结果、尚未运行的组合/真实 Agent/远端 CI 门禁。按仓库要求仅给 AGENTS 增加一条准确模块分工；观测 owner 已确认其本轮及下一轮不占用该段。没有新领域术语，不改 CONTEXT，也不改变 AGENTS 的既有业务、安全或生命周期约束。

```sh
git add AGENTS.md internal/workerruntime/adapter/acpclient internal/workerruntime/adapter/codexacp docs/superpowers/plans/2026-10-05-codex-only-acp-deepening.md docs/superpowers/specs/2026-10-05-codex-only-acp-deepening-design.md
git commit -m "test: verify ACP core security through real peer seams"
```

## 🔍 审查与续接门禁

每个任务独立 implementer、task-scoped spec/quality review；Critical/Important 修复后重新 review。最后以不可变 `72a96845..HEAD` 全分支差异进行独立审查，验证消费者无需修改、文件归属与错误/并发语义。缺少 `requesting-code-review` 模板时使用现有 task-reviewer rubric 的全分支版本，不跳过审查。

使用独立临时 ledger，不写历史生产 Web 或观测进度。没有安装 using-git-worktrees/finishing-a-development-branch 技能时以实际 Git worktree/ref 检查和人工授权边界收尾，不自行创建第二工作树或发布分支。

已审查进度：Task 1 `16de970..4f4ae5b`、Task 2 `4f4ae5b..2aca90b`、Task 3 `2aca90b..0d09bf2` 均 Spec/Quality Approved，无 findings。最终全分支 `72a96845..0d09bf2` 独立审查为 Spec/Whole-branch quality Approved，无 Critical/Important/Minor。审查定点确认 `application.AgentAdapter` 的六方法与门面一致，Shim 仍调用 `codexacp.New(trace)`；未重新审计原样迁移机制的全部历史安全性。主会话逐字比较确认 `client.go`/`rawtee.go` 只改 package 声明。

Task 3 以公开 core + 真实 stdio peer 的四文件测试替代 client/rawtee 机制测试，保留原有 facade 生命周期测试。Fixture RED 只证明所需测试场景尚未实现，不是生产缺陷修复；认证/能力两组 fixture RED 曾合并执行，其他已正确的构造、builder 拒绝、Trace 失败和 Close 行为如实记录为 characterization GREEN，不宣称每个场景均独立经历 RED。

本轮结束后交换双方 exact HEAD，先做组合差异评审再协调回归。未来 Factory/profile/YHC 依批准设计的交接条件另行推进；本计划通过不授予那些功能自动实施或合入权限。

## ✅ 本地切片交接（2026-10-05）

- 不可变开发基线：`72a96845c44c993723c4261a647b351374d0bcbc`。
- 源码实现、测试及全分支审查 HEAD：`0d09bf22b000ff3b38b01bb720dc52440f676aad`。后续文档状态闭合提交不改变这些源码；最终分支 HEAD 在跨会话交接时从 Git 读取，不在文件中预写自己的提交哈希。
- 分支：`codex/acp-codex-only-deepening`；工作树：`/Users/example/.codex/worktrees/codex-acp-deepening/Switchback`。
- 观测 owner 只读复核 HEAD：`29098fd8f5dd0e4fdc86f20b41ea97665b30a2a7`；dirty 仅 `.superpowers/sdd/g4-progress.md` 及两个 cancellation diagnostic plans，与本切片无路径交集。其 `capture_cancelled` 根因仍未解决，本轮 ACP 绿测不覆盖该结论。已同步实现 HEAD、门禁及 Go 资源窗口结束状态，最终 HEAD 再单独交接。

主会话在上述源码 HEAD 新跑并读取实际输出，不仅复用 implementer 报告：

| 命令 | 当前本地证据 |
| --- | --- |
| `GOMAXPROCS=2 go test -p 1 -count=1 ./internal/workerruntime/adapter/acpclient ./internal/workerruntime/adapter/codexacp` | PASS，2.165s / 0.534s |
| `GOMAXPROCS=2 go test -race -p 1 -count=1 ./internal/workerruntime/adapter/acpclient ./internal/workerruntime/adapter/codexacp` | PASS，4.258s / 3.027s |
| `GOMAXPROCS=2 go test -short -p 1 -count=1 ./internal/workerruntime/... ./internal/architecture` | 所有含测试包 PASS；包含 shimruntime、modelobs 和 architecture；无 integration tag |
| `GOMAXPROCS=2 go vet -p 1 ./internal/workerruntime/... ./internal/architecture` | exit 0，无输出 |
| `GOMAXPROCS=2 go build -p 1 ./cmd/worker-shim ./cmd/switchback-backend` | exit 0，仅编译，不启动入口 |
| `./scripts/check-format`、`git diff --check 72a96845c44c993723c4261a647b351374d0bcbc..HEAD` | exit 0，无输出 |
| 基线 `client.go` / `rawtee.go` 仅替换 package 后与当前文件 `cmp` | exit 0，逐字等价 |
| 基线到实现 HEAD 的 `AGENTS.md` diff | 1 insertion / 0 deletions，仅已确认归属的导航 |

实际净改动路径（重命名列出原路径与当前路径）：

```text
AGENTS.md
docs/superpowers/plans/2026-10-05-codex-only-acp-deepening.md
docs/superpowers/specs/2026-10-05-codex-only-acp-deepening-design.md
internal/workerruntime/adapter/acpclient/adapter.go
internal/workerruntime/adapter/acpclient/adapter_test.go
internal/workerruntime/adapter/acpclient/callbacks_test.go
internal/workerruntime/adapter/codexacp/client.go -> internal/workerruntime/adapter/acpclient/client.go
internal/workerruntime/adapter/acpclient/frames_test.go
internal/workerruntime/adapter/acpclient/options.go
internal/workerruntime/adapter/acpclient/options_test.go
internal/workerruntime/adapter/acpclient/peer_test.go
internal/workerruntime/adapter/acpclient/process.go
internal/workerruntime/adapter/codexacp/rawtee.go -> internal/workerruntime/adapter/acpclient/rawtee.go
internal/workerruntime/adapter/codexacp/adapter.go
internal/workerruntime/adapter/codexacp/adapter_test.go
internal/workerruntime/adapter/codexacp/client_test.go (deleted; public peer tests replace it)
internal/workerruntime/adapter/codexacp/policy.go
```

消费者、domain/contracts、Shim、modelobs、secret registry、依赖、镜像/Compose 和 schema 未改。主工作树的 UI/测试/进度 dirty 改动及观测工作树均未写入。

验证限制保留：非 root 宿主未执行 UID=10002 分支；child 的分割写入不强制 OS read 边界；终态后 Cancel 测试不构成确定性 queued-terminal 竞态证明；既有 Close “无法确认退出”错误分支未新增动态覆盖，不能表述为已运行验证。本地 helper/短测/race/编译不证明 localproduct/integration、真实 Codex/YHC、Docker/DB/模型、观测物理矩阵、组合回归、远端 CI、合入或部署；这些门禁尚未完成。Factory/profile/YHC 仅规划，未实施。
