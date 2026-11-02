# M1 本轮后人工能力验收指南

_适用于 2026-07-26 本地 `SingleUserLocalProfile`：验证 Worker 真实交付、完整公网/私网直通、驻留会话和单次 Runtime 恢复。_

---

## ✅ 验收结论口径

本轮人工验收按“基础设施 → 真实任务 → 网络 → 恢复 → 证据与隔离”顺序执行。全部必选项通过后，可以确认：

- Switchback Web、Backend、Langfuse 和 PostgreSQL 组成的本地 Compose 主链路可用。
- Task 由真实不可变 Worker 镜像中的 Codex 执行，结果通过 report/patch Artifact 交付。
- 同一存活 WorkerSession 可以继续交互，并保持 WorkingCopy 和 Agent 上下文。
- `trusted_full_network_v1` 允许 Worker 访问公网、RFC1918 私网、宿主服务和实际可路由的 VPN/LAN 服务。
- 一个 eligible Runtime hard failure 最多自动替换一次 Runtime generation，恢复本身不创建新 Task、WorkerSession、WorkerRun 或 AgentSession，也不重放旧 prompt。
- 完整网络并未放宽容器入站和宿主控制边界：Worker 仍不得拥有 Docker Socket、privileged、host network、任意宿主路径或 published host port。

以下项目仍是后续实现或扩展验证，不应被误报为本轮已经完整通过：

| 项目 | 本轮口径 |
|---|---|
| 固定镜像 active-turn 故障注入全矩阵 | 探索项，不作为人工发布门槛 |
| security termination owner 集成 | 尚未完成 |
| 所有真实 Docker 故障分支矩阵 | 尚未完成 |
| 未提供真实企业 VPN 目标的 `vpn_tcp` | 只能记为 skipped，不能宣称企业 VPN 已验收 |

## 📋 验收前准备

所有命令都在仓库根目录执行：

```bash
cd /Users/example/Code/repos/switchback
```

本轮 Compose 已重建并启动。若之后需要重新启动，使用：

```bash
make local-config
make local-up
```

不要使用 `docker compose down -v`；`-v` 会删除命名卷，不属于普通重启。

访问入口：

| 服务 | 地址 |
|---|---|
| Switchback Web | `http://127.0.0.1:4173/` |
| Backend health | `http://127.0.0.1:8080/healthz` |
| Langfuse | `http://127.0.0.1:3000/` |

人工验收前准备一份本地记录，至少包含：

```text
验收时间：
Task ID：
WorkerSession ID：
WorkerRun ID：
AgentSession ID：
AgentTurn ID：
Runtime ID / generation（故障前）：
Runtime ID / generation（恢复后）：
网络报告路径：
report / patch checksum：
Langfuse trace：
最终结论：
```

不得把 provider token、私网凭据、Cookie、SecretLease 内容或业务 payload 复制到验收记录。

## 🩺 第一阶段：Compose 与配置基线

执行：

```bash
curl --fail --silent --show-error http://127.0.0.1:4173/healthz
curl --fail --silent --show-error \
  http://127.0.0.1:4173/api/v1/views/task_summary/index
curl --fail --silent --show-error http://127.0.0.1:8080/healthz
curl --fail --silent --show-error http://127.0.0.1:3000/api/public/health
docker compose \
  --env-file deploy/local/.env \
  -f deploy/local/compose.yaml \
  ps -a
```

检查运行中 Backend 实际使用的固定 Worker 镜像和 feature gate：

```bash
docker compose \
  --env-file deploy/local/.env \
  -f deploy/local/compose.yaml \
  exec -T switchback-backend env |
awk -F= '
  $1 == "SWITCHBACK_WORKER_IMAGE" ||
  $1 == "SWITCHBACK_WORKER_NETWORK" ||
  $1 == "SWITCHBACK_M1_WORKER_DELIVERY_ENABLED" ||
  $1 == "SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED" {
    print
  }
' deploy/local/.env

docker network inspect switchback-m1-worker-control \
  --format 'driver={{.Driver}} internal={{.Internal}} scope={{.Scope}}'

curl --fail --silent --show-error \
  http://127.0.0.1:4173/api/v1/views/task_summary/index |
jq '.data.capacity'
```

通过标准：

| 检查 | 预期 |
|---|---|
| Web health | `{"service":"switchback-web","status":"ok"}` |
| Web 同源 Frontier API | HTTP 200，schema 为 `frontier.projection_snapshot.v1` |
| Backend health | `{"service":"switchback-backend","status":"ok"}` |
| Langfuse health | `status=OK` |
| Compose | 核心服务为 Up；migrate 容器退出码为 0 |
| Worker image | `SWITCHBACK_WORKER_IMAGE` 是 `sha256:` immutable digest |
| Delivery gate | `SWITCHBACK_M1_WORKER_DELIVERY_ENABLED=1` |
| Recovery gate | 默认 `SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED=0`；只在第四阶段受控恢复验收前显式设为 `1` |
| Worker network | `driver=bridge internal=false` |
| Continuity capacity | 要立即创建新 Session 时必须满足 `used < limit` |

任一 health 或 migration 失败时停止后续验收，先保留：

```bash
docker compose \
  --env-file deploy/local/.env \
  -f deploy/local/compose.yaml \
  ps -a

docker compose \
  --env-file deploy/local/.env \
  -f deploy/local/compose.yaml \
  logs --since=15m switchback-backend
```

若 `used >= limit`，新 Task 保持 `queued` 并等待 `session_capacity` 是预期行为，不是 Runtime 启动失败。要立即执行后续主链路，应在页面逐个核对并显式关闭不再需要、允许终止的 WorkerSession，直到 `used < limit`；不要删除数据库记录、WorkingCopy 或 Docker volume。若当前为 `used=12 / limit=10`，至少释放 3 个槽位才能给一个新 Session 留出容量。

## 🧑‍💻 第二阶段：真实 Worker 交付

### 创建最小可审查 Task

1. 打开 Switchback Web。
2. 点击 `Create task`。
3. Title 填写 `Manual acceptance: post-iteration capability`。
4. Agent 选择当前可用的真实模型。
5. Goal 填写：

```text
In the current WorkingCopy, create acceptance/switchback-capability-check.txt
with exactly these two lines:

SWITCHBACK_MANUAL_ACCEPTANCE
WORKER_DELIVERY_OK

Do not modify any other file. Report the file path, content, and SHA-256.
Run git diff --check. Produce a concise stage report and a patch artifact.
Do not publish remote changes.
```

预期：

- Task 按 `queued -> running -> delivered` 推进。
- 页面出现唯一 WorkerSession、WorkerRun 和 AgentTurn。
- Docker 中出现带 `com.switchback.managed=worker-runtime` 标签的动态容器。
- WorkingCopy 中只有目标文件变化；宿主源仓库不应出现该文件。
- report 记录路径、两行内容、SHA-256 和 `git diff --check` 结果。
- patch Artifact 只包含目标文件，并显示 checksum/provenance。
- patch 是 review-only，不会自动写回源仓库。

辅助检查：

```bash
docker ps \
  --filter label=com.switchback.managed=worker-runtime \
  --format 'table {{.ID}}\t{{.Image}}\t{{.Names}}\t{{.Status}}'

test ! -e acceptance/switchback-capability-check.txt
```

### 验证返工和人工接受

在 `Request changes` 输入：

```text
Append exactly one line named REVIEW_CYCLE_OK, run git diff --check again,
and update the report and patch. Do not change any other file.
```

通过标准：

- 仍是同一个 Task 和 WorkerSession。
- 返工按 Task 契约创建新的 WorkerRun/AgentTurn，顶层执行保持串行。
- 新 Artifact 绑定新的 Run/Turn provenance。
- 新 patch 只多出 `REVIEW_CYCLE_OK`。
- 审查完成后点击 `Accept`，Task 进入 `completed`。

若 Artifact 出现 `Checksum verification failed`、内容缺失、错误 provenance 或源仓库被修改，本阶段失败。

### 验证会话继续交互

Task 完成后，在同一 Worker chat 发送：

```text
Reply with exactly FOLLOW_UP_OK and do not change files.
```

通过标准：

- `worker_session_id` 不变。
- Runtime 在普通阶段完成后仍驻留，没有因 idle 自动重建。
- 用户的新动作可以按正常 Task/Run 契约创建后续 Run/Turn。
- Timeline 出现 `FOLLOW_UP_OK`，且没有新文件变化。

## 🌐 第三阶段：完整公网和私网直通

先从本地配置读取 immutable Worker image：

```bash
switchback_worker_image=$(
  awk -F= '$1 == "SWITCHBACK_WORKER_IMAGE" {
    sub(/^[^=]*=/, "")
    print
    exit
  }' deploy/local/.env
)
test -n "$switchback_worker_image"
```

### 基础网络与隔离矩阵

执行：

```bash
SWITCHBACK_WORKER_IMAGE="$switchback_worker_image" \
SWITCHBACK_WORKER_NETWORK=switchback-m1-worker-control \
./scripts/run-network-profile-acceptance
```

脚本会输出：

```text
network_profile_acceptance=/absolute/path/network-profile-acceptance.json
```

检查：

```bash
jq '{
  profile,
  reachability,
  skipped,
  isolation,
  outcome
}' /absolute/path/network-profile-acceptance.json
```

基础通过标准：

- `outcome == "passed"`。
- 已执行的公网 DNS、HTTPS、Go package、npm package 和 raw TCP probe 全为 `true`。
- RFC1918 peer TCP/UDP 和 `host.docker.internal` TCP/UDP 全为 `true`。
- `isolation` 中所有字段为 `true`。
- profile 是 exact `trusted_full_network_v1` revision 1 digest，status 为 `active`。

### 真实企业私网或 VPN

使用一个无敏感 payload、允许 TCP 探测的真实 `host:port`：

```bash
SWITCHBACK_WORKER_IMAGE="$switchback_worker_image" \
SWITCHBACK_WORKER_NETWORK=switchback-m1-worker-control \
SWITCHBACK_VPN_TCP_TARGET='private-service.example:8443' \
./scripts/run-network-profile-acceptance
```

若目标是普通私网而不是 VPN，可使用：

```bash
SWITCHBACK_WORKER_IMAGE="$switchback_worker_image" \
SWITCHBACK_WORKER_NETWORK=switchback-m1-worker-control \
SWITCHBACK_PRIVATE_TCP_TARGET='10.0.0.10:8443' \
./scripts/run-network-profile-acceptance
```

企业私网通过标准：

- 报告中的 `vpn_tcp` 或指定的私网 probe 为 `true`。
- 该 probe 不得出现在 `skipped`。
- 失败时只记录错误类型；不要把真实 endpoint、认证头或响应 payload 写入报告。

这一步验证的是“Docker 当前实际继承了宿主的可路由路径”，不是 Switchback 内建 VPN。若宿主已连 VPN 但 Worker 不通，优先检查 VPN 客户端是否把路由传播到 Docker VM/bridge、宿主防火墙以及目标 ACL。

## ♻️ 第四阶段：单次 Runtime 恢复

本阶段采用已覆盖的低歧义路径：在一个 AgentTurn 已终态、WorkerSession 仍存活时终止当前 Runtime 容器。不要把 active tool call 中途 kill 作为本轮必选门槛。

自动恢复默认关闭。仅在本阶段的受控验收窗口显式开启并重建 Backend：

```bash
SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED=1 make local-up
```

完成验收后恢复默认关闭状态：

```bash
SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED=0 make local-up
```

### 记录故障前 identity

在 Worker detail 页面记录：

- Task、WorkerSession、WorkerRun、AgentSession 和最近 AgentTurn identity
- RuntimeInstance identity、generation 和 ChannelEpoch
- 最近 AgentTurn 的 `started_at`、`deadline_at` 和终态
- 当前 WorkingCopy/Artifact checksum

用 exact WorkerSession label 找容器：

```bash
worker_session_id='<exact-worker-session-id>'

docker ps \
  --filter "label=com.switchback.worker-session-id=$worker_session_id" \
  --format 'table {{.ID}}\t{{.Image}}\t{{.Names}}\t{{.Status}}'
```

必须人工确认只匹配当前目标 Session。复制表格中精确容器 ID 后，再检查：

```bash
docker inspect <exact-container-id> | jq '.[0] | {
  id: .Id,
  name: .Name,
  image: .Image,
  labels: .Config.Labels,
  state: .State.Status
}'
```

### 注入一次 hard failure

只停止已核对的精确容器：

```bash
docker stop <exact-container-id>
```

不要使用 broad filter 自动拼接 `docker stop`，不要删除 Session 数据目录、secret volume 或数据库记录，也不要手工启动旧容器。

在 Worker detail 观察：

- Session 进入 `recovering_runtime`。
- incident phase 从 fencing/cleanup 推进至 provisioning/verifying。
- replacement 健康稳定后，Runtime generation 恰好增加 1。
- 旧 generation 被 fencing，旧容器不再运行。

恢复完成后发送一个新的显式 Turn：

```text
Read acceptance/switchback-capability-check.txt and reply with its exact content
and SHA-256. Do not modify files.
```

通过标准：

| 检查 | 预期 |
|---|---|
| 恢复动作本身 | 不创建新 Task、WorkerSession、WorkerRun、AgentSession 或 AgentTurn |
| Runtime | identity 更新，generation 仅 `N -> N+1` |
| 新用户 Turn | 由用户显式触发，不是旧 prompt 自动重放 |
| WorkingCopy | 恢复前文件和 checksum 保持一致 |
| AgentSession | identity 保持一致 |
| 旧 generation | 不能再提交当前 Artifact、Display 或 control event |
| deadline | 原 deadline 不因恢复而延长 |
| 重复失败 | 不出现 generation `N+2`；应进入可操作的 waiting-user 状态 |

后续显式返工或 follow-up 可能按正常产品契约创建新 WorkerRun；这不等同于 Runtime recovery 自己创建了新 Run。判定时应以“故障发生到 replacement 健康之间是否产生了额外业务 identity”为准。

可选地执行自动基线：

```bash
SWITCHBACK_DEEPSEEK_KEY_FILE=/absolute/path/to/deepseek.key \
SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED=1 \
make runtime-recovery-acceptance
```

当前报告的 `outcome=passed` 只表示已执行的 deterministic tests 与
`idle_between_turns_manual_replacement` fixed-image evidence 通过，并且没有 managed
container/secret volume residue。它还必须诚实记录
`production_recovery_path=false`、`enablement_ready=false` 和非空
`missing_enablement_scenarios`；不得据此打开默认 recovery gate。

## 🔒 第五阶段：隔离、观测与最终收口

### 容器隔离

对恢复后的精确 Worker 容器执行：

```bash
docker inspect <replacement-container-id> | jq '.[0] | {
  privileged: .HostConfig.Privileged,
  network_mode: .HostConfig.NetworkMode,
  binds: .HostConfig.Binds,
  port_bindings: .HostConfig.PortBindings,
  cap_drop: .HostConfig.CapDrop,
  security_opt: .HostConfig.SecurityOpt
}'
```

通过标准：

- `privileged=false`。
- `network_mode` 不是 `host`。
- bind 中没有 `/var/run/docker.sock`，也没有客户端指定的任意宿主路径。
- `port_bindings` 为空；Worker 不直接发布 host/public 入站端口。
- capability drop 和 no-new-privileges 仍启用。

### Langfuse 与 Artifact

1. 打开 Langfuse 的 Switchback M1 项目。
2. 用记录的 WorkerRun/AgentTurn identity 查找 trace。
3. 核对 `worker.run`、`agent.turn` 和 model generation 的关联。
4. 回到 Switchback Web 下载 report/patch，重新核对页面 checksum 与下载内容。

Langfuse 是派生观测面，不是 Task、Trace JSONL 或 Artifact 的权威事实源。Langfuse 暂时不可用时，不应改写业务状态；但本轮“完整人工能力验收”仍应记录为观测项未通过。

### 最终清单

| 必选项 | 结果 |
|---|---|
| Compose health、migration 和 feature gate | ☐ 通过 / ☐ 失败 |
| 真实 Worker Task 交付 | ☐ 通过 / ☐ 失败 |
| report/patch checksum 与 provenance | ☐ 通过 / ☐ 失败 |
| rework、manual accept 和 Session follow-up | ☐ 通过 / ☐ 失败 |
| 公网、RFC1918 peer 和宿主路由 | ☐ 通过 / ☐ 失败 |
| 真实企业 VPN/私网目标 | ☐ 通过 / ☐ 跳过 / ☐ 失败 |
| Runtime 单次 `N -> N+1` 恢复 | ☐ 通过 / ☐ 失败 |
| 无 prompt replay、无额外业务 identity | ☐ 通过 / ☐ 失败 |
| Docker 隔离边界 | ☐ 通过 / ☐ 失败 |
| Langfuse 与 Artifact 最终核对 | ☐ 通过 / ☐ 失败 |

除“真实企业 VPN/私网目标”允许在未提供目标时明确记为跳过外，其他必选项任一失败，本轮不能记为完整通过。

## 🧯 失败留证

失败时不要反复重试覆盖现场。优先保存：

```bash
docker compose \
  --env-file deploy/local/.env \
  -f deploy/local/compose.yaml \
  ps -a

docker compose \
  --env-file deploy/local/.env \
  -f deploy/local/compose.yaml \
  logs --since=30m switchback-backend

docker ps -a \
  --filter label=com.switchback.managed=worker-runtime \
  --no-trunc

docker volume ls \
  --filter label=com.switchback.managed=worker-runtime
```

同时记录：

- 首个失败时间和用户动作
- exact Task/Session/Run/Turn/Runtime/incident identity
- 失败前后的 generation、revision、phase 和 reason
- 页面截图、Artifact checksum 和网络报告路径
- 是否存在外部副作用不确定性
- 是否出现 prompt replay、双 Runtime、generation `N+2` 或残留资源

不要通过手工修改数据库状态、generation、ChannelEpoch、deadline 或 secret 来“修复”验收结果。

## 📚 细分手册

- [M1 Worker delivery 人工验收手册](m1-worker-delivery-manual-acceptance.md)
- [M1 Worker 完整网络人工验收](m1-worker-network-manual-acceptance.md)
- [M1 Worker Runtime 单次恢复人工验收](m1-worker-runtime-recovery-manual-acceptance.md)
- [M1 Worker Runtime 恢复运维手册](m1-worker-runtime-recovery-operations.md)
