# M1 Worker 完整网络人工验收

_用于验证真实不可变 Worker 镜像的公网、私网、宿主与可选企业 VPN 路径，以及保留的容器隔离。_

---

## ✅ 验收目标

一次有效验收必须证明：

- 公网 DNS、HTTPS、Go package、npm package 和 raw TCP 可达。
- 命名 bridge 中的 RFC1918 peer TCP/UDP 可达。
- Linux/Docker Desktop 宿主 TCP/UDP 可经 `host.docker.internal` 到达。
- 提供真实 VPN 目标时，该 TCP endpoint 可达。
- Worker 没有 Docker Socket、privileged/host network、published port、ambient credentials，并启用 capability drop 与 no-new-privileges。

自动化脚本不会记录 endpoint、IP、URL 或 payload，只输出 probe 名称、布尔结果和错误类型。

## 📋 前置条件

- Docker daemon 正在运行。
- 已构建并固定 Worker 镜像。
- `switchback-m1-worker-control` 网络存在且 `internal=false`。
- 本机安装 Go 和 `jq`。
- 若验收企业 VPN，准备一个允许 TCP 连接的真实 `host:port`；该值不会写入报告。

本地镜像和网络通常由以下命令准备：

```bash
make local-env
make local-up
```

从本地环境文件读取固定镜像引用：

```bash
switchback_worker_image=$(
  awk -F= '$1 == "SWITCHBACK_WORKER_IMAGE" { sub(/^[^=]*=/, ""); print; exit }' \
    deploy/local/.env
)
test -n "$switchback_worker_image"
```

## 🚀 执行

不含真实企业 VPN 目标：

```bash
SWITCHBACK_WORKER_IMAGE="$switchback_worker_image" \
SWITCHBACK_WORKER_NETWORK=switchback-m1-worker-control \
./scripts/run-network-profile-acceptance
```

包含真实企业 VPN 目标：

```bash
SWITCHBACK_WORKER_IMAGE="$switchback_worker_image" \
SWITCHBACK_WORKER_NETWORK=switchback-m1-worker-control \
SWITCHBACK_VPN_TCP_TARGET='private-service.example:8443' \
./scripts/run-network-profile-acceptance
```

若需要指定另一个私网 TCP 目标，可额外设置 `SWITCHBACK_PRIVATE_TCP_TARGET=host:port`；默认使用脚本创建的 RFC1918 bridge peer。

脚本成功时输出：

```text
network_profile_acceptance=/absolute/path/network-profile-acceptance.json
```

## 🔍 判定

```bash
jq '{
  profile,
  reachability,
  skipped,
  isolation,
  outcome
}' /absolute/path/network-profile-acceptance.json
```

通过条件：

- `outcome == "passed"`。
- `reachability` 中所有已执行 probe 为 `true`。
- `isolation` 中所有字段为 `true`。
- `profile` 是 exact `trusted_full_network_v1` revision 1 digest 和 `active`。
- 做企业 VPN 验收时，`vpn_tcp` 必须存在于 `reachability` 且为 `true`，不能出现在 `skipped`。

未提供 `SWITCHBACK_VPN_TCP_TARGET` 时，`vpn_tcp` 会出现在 `skipped`。这种结果可以证明公网、RFC1918 peer 和宿主路由，但不能声称企业 VPN 已验收。

## 🧯 失败处理

| 失败项 | 处理 |
| --- | --- |
| `public_dns` / `public_https` | 检查 Docker DNS、宿主代理和防火墙 |
| `go_package_endpoint` / `npm_package_endpoint` | 检查 registry 访问策略、代理和 CA |
| `private_tcp` / `private_udp` | 检查 named bridge、peer 状态和协议监听 |
| `host_tcp` / `host_udp` | 检查 `host-gateway`、宿主服务 bind address 和防火墙 |
| `vpn_tcp` | 检查 VPN 路由是否传播到 Docker VM/bridge；不要把跳过当通过 |
| 任一 `isolation` 为 false | 视为安全回归，禁止启用 profile |

报告默认保存在 `runtime-data/network-profile/run-<UTC timestamp>/`。保留报告用于本机验收证据，但不要在其中补录目标地址或业务 payload。

## 🧪 关闭前验证

网络实测通过后还需运行：

```bash
make verify
openspec validate trusted-full-network-profile --strict
git diff --check
```

人工验收只证明当前 Docker host 的实际路由矩阵，不证明所有 Docker Desktop、Linux、VPN 客户端和企业网络组合都天然可达。
