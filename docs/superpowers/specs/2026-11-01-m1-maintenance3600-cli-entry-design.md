# Maintenance3600 CLI 入口最小修复设计

用户已明确接受先前最小方案并要求完整离线实施、实际入口回归、独立评审与新候选封存；不再重新请求同一设计批准。本设计记录该接受范围，不授予真实执行权限。

## 范围与成功标准

在新 `.scratch/runtime-m1-maintenance3600-cli-entry-1101` 形成完整可核验的 carrier。旧 `.scratch/runtime-m1-fd-maintenance-3600-1031` seal `bfff3ed146a984d398e7fa036a0f180cb232855fd0358387bd3c5a2e44654fff`、失败取证目录和所有历史输入不可修改。唯一生产行为修正是 `carrier/launch_copy.py` 的 header policy 字面量从 `owned_fd_lifecycle_transition_v1` 同步为 `owned_fd_maintenance3600_v1`。保持认证顺序：入口自检和全部 phase 哈希通过后才 import runtime；原 validate_authorization 通过后才 import/call driver。不得提前 import 未认证依赖，也不得绕过 launcher。

新 carrier 精确复制原有限成员，只允许另三份递归元数据相应更新：private-revision-map 的 launch_copy after、artifact-binding 的上述 launcher/map records、disabled authorization 的 artifact binding digest。其余 carrier 文件、Source、templates、kit/bundle、包体和全部动态程序逐字节保持。固定未来执行路径保留原未创建的 `.scratch/runtime-m1-fd-maintenance-3600-execution-1031-01/carrier`；本轮不创建该路径或实际 grant/claim，所有 disabled 字段保持原值。新文档/工具在 carrier 外由最终 seal 绑定。

## 实际入口验证

既保留旧入口 red，也直接调用新入口的真实 main()。测试只允许私有本机 fixture 的路径/时间/授权值建模；launcher 及实际 runtime/driver 源码保持原字节。正确当前 policy 必须越过完整前置校验和真实 validate_authorization，并抵达 driver 调用；在第一条环境方法前设置强拒绝边界，记录入口顺序，禁止任何 VM/guest/服务/网络/凭据访问。优先执行真实 driver 的前两条仅本机校验，在 verify_host_and_running_vm 前截断；若需要替代 driver 只能如实说明测试覆盖边界并另补原认证/Source拒绝验证。

至少覆盖 usage/offline-path/current policy/legacy policy/unknown policy/both schemas/grant policy/digest/environment false/provider true/no_retry false/actions/expired/future/execution path/id/intent/worker selection/phase code drift/unsafe file/name/Source drift/one-use claim 重放；按实际适用边界实现，不用静态 AST 条件测试代替 main()。测试不能创建真实执行目录；fixture grant 不是实际授权。真实 CLI 在离线 carrier 路径用 actual 参数必须拒绝。

## 测量血缘与未变合同

保留原完整规模 generation002 work3097.253166958s、cleanup0.000020042s 的原始身份和封存来源。新报告必须明示未为新 binding 重做完整规模测量。独立证明原60个 measured bindings恰有两处受控差异：launch_copy 的已接受单字面量修复和名义 artifact-binding 元数据；其余58个代码/数据/工具保持；逐字节比较所有定时 runtime/guest/module/程序/profile。新 CLI 的单一常量变化在启动 driver 之前，不能修改维护采样、读取、Stop、归档、copy、Engine、资格或清理行为。测量血缘证明与新入口执行证明分别具名；不得改写旧 measurement/capture/hash 或把它们说成新 binding 的重测。原完整 seal 必须以外部固定 SHA 新鲜只读验证；其结果独立于 CLI 可达性。

全部门禁与预算保持：outer7200、maintenance work3600/cleanup20/total3620（收尾 min(shared+3620,cleanup_start+20)）、Engine120/qualification240/barrier300；argv<65536，frame16384/event1048576，decoded128MiB、wire/journal192MiB、ordinary64MiB、failure1MiB。原权限/PG/Trust/Start、有限动作和 no-retry 保留；不接触环境。继承 Source1101/templates7、kit manifest a0efe562f7374bf104051b7cda706ed0bb621900a768a85ebfd46f4666566eb7、bundle61bc3b24ff1a356d0fd7843af9982d7b68b9b467aa1dca7e653ac3b215154ed3。

## 验证、评审和封存

新独立 verifier 对 parent SHA、全 carrier 精确 delta、原生产 verifier、测量血缘、实际入口回归和 disabled 权限做联合验证。最终 closure 包含全部新 phase 物理文件、本文/计划 context、具名测试/评审/血缘回执和原 seal 的外部摘要。control/report/seal 的排除集合必须是有限精确文件名；文件增加/删除/内容/owner/mode/链接漂移均拒绝。exclusive seal 后将摘要存到外部 append ledger，独立只读 verify(expectedSHA) 再次核对；封存后不改 phase/spec/plan。Task/whole 两次独立评审分别判断 spec 与 quality，修复开放 Critical/Important 后才能完成。

本片成功仅证明 CLI 修复后的离线候选闭合和原未变执行路径的继承证据；真实 Linux/Engine/PG/Core 及产品闭环仍未验收。旧真实窗口已停止，新候选不自动获得运行授权。
