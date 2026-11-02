# G3 查询原始证据 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为受控 G3 查询保留私有、不可覆盖的原始响应，使后续汇总能离线重跑现有断言。

**Architecture:** 实现已确认[双门禁设计](../specs/2026-10-10-observability-two-gates-design.md)的原始材料/只读重算边界。独立 acceptance-only `queryevidence` 包记录 HTTP GET 响应并提供无网络回放；既有 correlation 验证逻辑和生产 Langfuse client 保持单一实现。这里只交付材料采集/回放，不声称完整 G3/PG/物理门禁完成。

**Tech Stack:** Go 标准库、现有 Langfuse read client、临时文件与 httptest；无新增依赖。

## 📋 Global Constraints

- 使用既有 `.worktrees/observability-master-integration-1009`，指定集成 master `8fdbe6b` 不变；不改产品、Dockerfile、依赖、CI、SQL、ACP/YHC、settlement 或 250ms/30s 原预算。
- 旧测试函数/辅助函数、13项固定登记与 `2ee4c24bfd43621dae7acc73445fece17e73f5ae` 守恒基线不变。新包测试默认执行，另明确验证；旧三包守恒结果不宣称覆盖新包。
- 新响应只存于新 claim 下 `correlation-queries/`，目录0700、文件0600、O_EXCL；旧目录/证据、主/旧 dirty、10-09历史checkpoint不动。不上传、不打印原始响应/凭据。
- 仅受控 fixture 查询；不新增真实 Agent/provider 或生产遥测输出。请求/响应 headers、Authorization、Cookie、请求 body、环境、原始 error 字符串均不写入记录。响应原始 bytes 是本地私有验收材料，不进入 Loki/Trace 产品流。
- 仅 GET；固定三个 literal loopback HTTP origin，Grafana datasource series/query_range、Langfuse traces/observations read API、Prometheus query。拒绝其他origin/path、userinfo/fragment、自定义Host、非空请求body；无redirect跟随、无回放网络fallback。
- 响应上限4MiB、全记录响应bytes总上限64MiB、最多4096条、URL最多4096字节、manifest最多16MiB；超限/读取/落盘/transport失败 sticky，不能Seal成功。保留已经写出的失败材料，重跑用新claim，不删除/重写。
- 测试只用 GOMAXPROCS=2/-p1、独立cache、临时文件和httptest loopback；无真实Docker/PG/native/provider/HTTP服务或旧claim。日志唯一命名+noclobber，保存实际命令/output/exit，失败不覆盖。
- Git每次含transitive工具调用均清GIT_*，禁system/global config与replacement objects。提交仅本地，不push/Ready/合入；OpenSpec3.2保持未勾选。

## 📦 文件与接口

| 文件 | 职责 |
| --- | --- |
| `test/acceptance/observability/queryevidence/record.go` | 私有独占文件、bounded GET recorder、attempt/Seal |
| `test/acceptance/observability/queryevidence/replay.go` | 只读严格载入、hash/目录完整性与最终attempt回放 |
| `test/acceptance/observability/queryevidence/client.go` | context-owned transport 注入，不使用全局可变 client |
| `test/acceptance/observability/queryevidence/record_test.go`、`replay_test.go`、`client_test.go` | public API/真实文件/httptest行为测试 |
| `test/acceptance/observability/correlation.go` | 原查询循环接 recorder，原断言完成后Seal，再原PASS writer |
| `test/acceptance/observability/correlation_queries.go` | GET使用context transport，无注入时旧client语义 |

root另维护本计划、原实施计划/当前checkpoint、独立ledger/brief/report/review；实现者只改上表八个Go文件及自己的私有报告。

### Task 1: 私有 GET 记录、最终 attempt 回放与 correlation 接线

**Interfaces:** 下列API是本任务与后续汇总的测试seams；沿用已确认工具文件/HTTP边界，不测试内部调用计数或手填PASS。

```go
type Binding struct {
    Project string `json:"project"`
    SnapshotSHA256 string `json:"snapshot_sha256"`
    Origins map[string]string `json:"origins"`
}
// Origins恰为grafana/langfuse/prometheus；三个不同的http://literal-loopback:port。
// SnapshotSHA256为sha256:加64位lowercase hex，绑定correlation-snapshot.json原bytes。
func NewRecorder(directory string, binding Binding, upstream http.RoundTripper) (*Recorder, error)
func (r *Recorder) BeginAttempt() error
func (r *Recorder) RoundTrip(request *http.Request) (*http.Response, error)
func (r *Recorder) Seal() error
func OpenReplay(directory string, binding Binding) (*Replay, error)
func (r *Replay) RoundTrip(request *http.Request) (*http.Response, error)
func (r *Replay) AssertConsumed() error
func WithTransport(ctx context.Context, transport http.RoundTripper) context.Context
func HTTPClient(ctx context.Context, fallback *http.Client) *http.Client
```

`HTTPClient`复制client，保留原Timeout/CheckRedirect，仅在context携带transport时覆盖Transport。Recorder/Replay各自串行化状态，禁止跨attempt并发改变；RoundTrip允许并发调用但通过互斥锁串行完成，不持有无限响应集合。Recorder创建前验证binding和canonical absolute parent，不跟随symlink；已有目录拒绝。Project非空、最多128字节且只含lowercase字母/数字/连字符。Origins无query/path/userinfo/fragment、canonical port1..65535，主机必须literal loopback；不存在默认origin或环境加载。

记录布局：`000001.body`保存原始bytes；`000001.json`保存schema `observability.query_response.v1`、sequence、attempt、method、URL、status、started_at/finished_at、body_bytes/body_sha256。只用固定序号生成basename，不从URL派生路径。transport/read/oversize失败记录使用固定failure code和至多4MiB已读bytes，返回error、latch失败；若落盘也失败，保留已写部分且不能Seal。HTTP非200完整响应保留供pending/诊断，但不能出现在成功最终attempt。关闭原Body，向当前旧调用者返回等值bytes/status的独立Body；不写任何headers。

`BeginAttempt`递增attempt；未Begin、空attempt再次Begin、Seal后调用均失败。`Seal`只在无sticky error、当前attempt有完整200响应时独占写 `manifest.json`：schema `observability.query_manifest.v1`、binding、successful_attempt和所有响应metadata文件的固定sequence/name/SHA256清单。manifest自身不表示G3通过；只有旧完整验证返回成功后caller才Seal。失败manifest/目录不清理，不生成新PASS。

`OpenReplay`先核对expected binding而不是信任manifest；bounded regular文件、canonical目录、严格JSON（unknown字段/尾随JSON拒绝）、顺序连续1..N、attempt从1单调连续、成功attempt为最后且非空、status/time/bytes/hash、exact文件清单均核验，连历史pending响应也重算hash。拒绝缺失、多余、symlink/FIFO、超限、路径逃逸/错basename、不完整/被替换材料。读取过程中不写文件；受信任本地fixture模型不宣称防宿主TOCTOU/同时篡改所有材料攻击。

Replay仅提供successful_attempt的原始响应。以exact method+URL匹配并按同URL sequence FIFO消费，允许不同URL次序（原PromQL map迭代非确定性）。不读取/请求任何upstream；未知/重复请求、canceled context失败，AssertConsumed拒绝未消费项。返回空Header和原status/bytes。后续汇总还须重建source proofs、用现有G3断言消费回放及与build/fixture绑定；本任务不以summary/manifest代替它。

- [x] Step 1: 第一个vertical slice测试NewRecorder→BeginAttempt→http.Client.Get→Seal→OpenReplay→Get→AssertConsumed。使用三个httptest服务（或一服务及两个不请求的临时监听origin）、literal body `{"status":"success"}`、literal snapshot摘要 `sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad`。先保存缺API编译RED，再最小实现获得GREEN。以回放公有GET验证等值响应；目录快照保留每文件name/bytes/mode/size/mtime，不断言内部map形状。

```go
ctx := context.Background()
recorder, err := queryevidence.NewRecorder(directory, binding, http.DefaultTransport)
if err != nil { t.Fatal(err) }
if err := recorder.BeginAttempt(); err != nil { t.Fatal(err) }
client := queryevidence.HTTPClient(queryevidence.WithTransport(ctx, recorder), &http.Client{Timeout: time.Second})
response, err := client.Get(binding.Origins["prometheus"] + "/api/v1/query?query=up")
if err != nil { t.Fatal(err) }
body, err := io.ReadAll(response.Body)
response.Body.Close()
if err != nil || string(body) != `{"status":"success"}` { t.Fatalf("body: %q %v", body, err) }
if err := recorder.Seal(); err != nil { t.Fatal(err) }
// 关闭真实server，再通过OpenReplay和HTTPClient读取同URL；仍得到上述literal body/status。
```

- [x] Step 2: 逐个public行为slice完善record限制：真实BasicAuth请求仍到httptest，落盘不含header/credential；404 pending与第二成功attempt均保留、Replay只消费最后attempt；已有目录/Seal再次/Seal后请求/未Begin/empty attempt拒绝，目录不被覆盖。不同URL可换序，同URL重复响应按实际sequence FIFO。实际redirect响应被原CheckRedirect拒绝跟随，不能Seal为成功。加入body read error、transport error、单body/总bytes/count/URL限制，所有失败保留并阻止Seal。只在external RoundTripper边界模拟read/transport失败，不mock内部helpers；characterization GREEN如实记录。

- [x] Step 3: 逐个public回放反例：错project/snapshot/origin、缺manifest/body/meta、raw/meta内容替换、历史404内容替换、乱序/重复sequence、错attempt/成功attempt、错误hash/size/status/time、unknown/trailing JSON、多余文件、symlink/FIFO/oversized输入、非法URL/path/userinfo/Host/nonGET/body、未消费/重复/未知请求、context取消。断言实际error且文件snapshot不变，不将fixture自己失败或编译错误称作行为RED。可读取公开证据格式构造独立literal坏材料，不能只调用私有validator。

- [x] Step 4: 接线只改指定两文件。加入如下helper，原 `correlationHTTP()`不变，correlationGET只将`.Do`的client换为helper；LF NewClient同样用helper。Recorder在source proofs与events编译成功后创建，绑定原snapshot checksum和已校验ports。每轮开始BeginAttempt，原完整Loki/LF/PromQL断言结束后Seal，再继续原identities/原writeEvidence及原JSON；不改变poll deadline/interval、不改旧测试body。

```go
func correlationHTTPFor(ctx context.Context) *http.Client {
    return queryevidence.HTTPClient(ctx, correlationHTTP())
}
// runCorrelation: 紧邻原lf.NewClient前插入。
recorder, err := queryevidence.NewRecorder(filepath.Join(output, "correlation-queries"), queryevidence.Binding{
    Project: project, SnapshotSHA256: artifactdomain.Checksum(raw),
    Origins: map[string]string{
        "grafana": "http://127.0.0.1:" + values["SWITCHBACK_GRAFANA_PORT"],
        "langfuse": "http://127.0.0.1:" + values["SWITCHBACK_LANGFUSE_PORT"],
        "prometheus": "http://127.0.0.1:" + values["SWITCHBACK_PROMETHEUS_PORT"],
    },
}, http.DefaultTransport)
if err != nil { return err }
ctx = queryevidence.WithTransport(ctx, recorder)
// 原for循环的pending=false之前：
if err := recorder.BeginAttempt(); err != nil { return err }
// 原完整loop成功break以后、构造identities之前：
if err := recorder.Seal(); err != nil { return err }
```

- [x] Step 5: 只读自审作用域与旧函数/JSON/SQL净差异。新库测试至少覆盖三个实际route、context client copy保持timeout/redirect/fallback、并发GET数据一致与race；report明确实际完整runCorrelation需PG/source fixture，本次未执行，不把transport自测称为G3物理验收。仅必要httptest需要狭窄sandbox升级；第一次环境失败日志保留，cache/env独立。

```bash
GOMAXPROCS=2 go test -p=1 -race -count=1 ./test/acceptance/observability/queryevidence ./test/acceptance/observability
GOMAXPROCS=2 go test -p=1 -count=1 ./test/acceptance/observability/queryevidence ./test/acceptance/observability ./test/acceptance/observabilitygroups ./internal/architecture
GOMAXPROCS=2 scripts/check-observability-test-groups 2ee4c24bfd43621dae7acc73445fece17e73f5ae
GOMAXPROCS=2 go vet -p=1 ./test/acceptance/observability/queryevidence ./test/acceptance/observability
git diff --check
```

- [x] Step 6: stage仅八个Go文件，本地提交 `test: retain controlled correlation query evidence`，候选tree严格匹配正式验证，报告写独立query1011路径。root从开始前task base生成完整review package，fresh reviewer输出Spec/quality，修复Critical/Important并复审；root独立精确候选复验/旧证据保全，公共checkpoint仅记录本工具子交付。全分支/Ready审查仍等原Task3物理门禁与capture处置。

## 🔍 本轮交付结果

受测代码 `74132abb43605e920fc3d79dc405221fe4ba405c`、tree `af92da4d6c4e185a3ff7d71b5160c642f38feb2a`。已接入受控查询的私有留档与 HTTP 离线回放；原查询断言和产品逻辑未变。独立复审 Approved，无 Critical/Important；保留相对路径负例的非阻塞覆盖建议及历史执行偏差，供最终整分支审查处理。

root 精确候选两包 race、四包默认回归、固定旧基线守恒及五组编译、vet/格式/差异检查全部 exit0；另在真实路径 TMPDIR 下完整执行新包 default/race，均 exit0。独立复验发现的 macOS 临时目录别名问题已修正测试夹具，并验证真实 RED/GREEN；原失败和旧证据保留。验证仅用临时文件/httptest，不是完整 G3 或物理验收。

## 📍 后续仍开放

原始SQL/source loader事实与新manifest的完整离线G3重算入口、scrape/dashboard/alert原始材料、loss/retention便携核验、源码/实际fixture来源链、统一双门禁汇总与真实组合回归仍在原Task3。本轮不会勾OpenSpec3.2或解决/降级capture_cancelled。
