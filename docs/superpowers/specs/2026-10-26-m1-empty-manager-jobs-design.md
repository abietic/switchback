# M1 空 manager jobs 回执与新的剩余部署窗口设计

状态：待用户确认。本文是失败后本地取证形成的候选设计；尚未修改 guard、构建候选或再次进入 guest。上一授权的单次窗口已经结束。

## 已证明的结果与根因

本次真实入口使用 kit20/bundle1 和封存的 v2 操作器。四次 observer quiescence 检查均完成，记录127个进程、491或493个FD；随后用两次 `renameat2(RENAME_NOREPLACE)` 原样保留旧 observer ELF 和整个配置目录。ELF完整SHA及配置叶文件的inode/metadata得到核验，配置/key内容读取计数均为0，observer仍masked/unloaded，没有服务变更。

保留目标：

- `/opt/switchback/.preserve-observer-unlinked-exe-guard-kit20-1026/switchback-created-layer-observer`
- `/etc/switchback/.preserve-observer-unlinked-exe-guard-kit20-1026/created-layer-observer`

后续 `verify_guest_frozen_ownership` 的失败回执完整、未超时、未截断。内层 `list-units` 是exit0和 `[]\n`；紧接着的 `systemctl list-jobs --all --no-pager --output=json` 是exit0和**零字节stdout**。`OwnershipGuard.manager` 却直接 `json.loads` 后者，于第192行抛出 `JSONDecodeError`。旧fixture把空job结果写成JSON数组，遗漏了本次真实输出形态。

确定性反馈命令：

```sh
/usr/bin/python3 -B .scratch/runtime-m1-unlinked-exe-guard-failure-analysis-1026/replay_manager_failure.py
```

该脚本只回放已保存的两条命令及其原始字节/hash/exit/截断标志，调用实际已执行的 `OwnershipGuard.manager`；无guest、子进程扫描或凭据访问。现有guard稳定RED：1项测试，原样 `JSONDecodeError`。传输丢失及命令失败已由完整wire hash、exit0和两个false状态直接排除，无需生产插桩或再次现场定位。

上游当前 [systemd list-jobs 实现](https://github.com/systemd/systemd/blob/main/src/systemctl/systemctl-list-jobs.c#L61-L75) 对零job有独立的成功返回分支；不同命令不能统一假定空列表必输出JSON。该资料仅解释协议风险，不用上游main版本替代本机回执。当前 [systemctl文档](https://www.freedesktop.org/software/systemd/man/latest/systemctl.html) 也把`show`作为需要机器可读属性时的接口。本方案以本机已观察的有限形态为边界。

Engine、新PG、Native资格采集、凭据内容传输和首启都尚未开始。旧五安装物、四份kit副本和既有stage没有进入保留步骤；不据此声称失败后的现场状态已经再次核验。

## 路线比较

推荐A：保留现有命令和非空严格JSON路径，仅把**经过原命令适配器确认成功的精确零字节stdout**归一为`jobs=[]`。这是对已观察有限形态的修正，不改unit/job权限或原有非空规则。

B：统一改成`list-jobs --no-legend --full --no-pager`，只接受空结果、拒绝全部非空job。协议更简单，但把原来可接受的无关JSON jobs也变成拒绝，扩大运行策略变化。

C：改读D-Bus或manager `show`属性。结构接口更明确，但新增协议和验证面，不适合当前最小片段。

## A的精确边界

- `jobs_raw == b''`时取空列表；不做`strip()`，不把任意空白、错误文本、JSON解码异常或其他异常转成成功。
- 原有非空JSON列表、dict/unit字段、owner jobs拒绝、loaded-unit属性、两次manager一致性检查保持。非空未知格式继续拒绝。
- 必须保留底层exit0、无error、未超时、未截断的命令检查；非零/unknown空输出不得进入这个成功分支。
- unlinked-exe对象身份策略、O_PATH及九项metadata、PID/start/flags/cgroup、全部FD/maps/boot、protected paths/inodes、无白名单等保持。
- 本片仅改新的私有操作器副本、测试、绑定及执行材料。Go/Engine Source、kit20/bundle1、资源/网络/capability、PG/Trust/quota/Start门禁及共享卷保证均不改，不重建kit。

## 新的剩余准备窗口

旧失败phase、entry claim、guest回执和两处observer archive原样保留，不续跑旧入口。

固定新offline phase为`.scratch/runtime-m1-empty-manager-jobs-1026`，新actual phase为`.scratch/runtime-m1-empty-manager-jobs-execution-next`。仍使用v2 binding/grant schema和未变的unlinked对象policy，但绑定新代码、精确路径、actions和新的真人intent；旧grant/phase/claim不得使用。

新copy archive：`archive-empty-manager-jobs-1026-kit-0020-bundle-0001`；新installed archive prefix：`.preserve-empty-manager-jobs-1026-kit-0020`；guest audit：`/var/lib/switchback/empty-manager-jobs-1026-audit`。

完整新窗口按下列顺序执行：

1. 新exact grant、单次claim、现有DeepSeek私有文件的metadata lease及完整Source/kit绑定校验。
2. 重新取得host owner锁，核验完整host facts、现有VM为Running及原存储/父目录边界。
3. **只读核验已保留的observer**：绑定本次真实receipt及完整audit；原两target必须缺席，原bin必须为空；固定archive只有已证明的ELF/配置树，目标inode/类型/owner/mode、ELF SHA/20-byte header和配置各叶metadata必须匹配。原父目录身份须匹配；新archive parent在当前固定路径按root:root/0700/同FS与精确children验证并持有/pin，不能声称历史回执已记录其未公开的parent inode。全程不读配置/key叶内容、不移动它们。再次核验masked/unloaded/jobs、boot与完整process/FD/maps；失败停止，不重新归档observer。
4. 用修正后的manager解析器执行原完整guest frozen ownership和absent-target preflight，再保留原五安装物、四副本及旧stage，独占复制四份kit20/bundle。
5. 一次原120秒Engine部署窗口。只有`engine_ready`才继续原Native权限、镜像、新秘密/新PG/迁移、已选择DeepSeek凭据准备及一次240秒资格→安装→同进程首启；300秒barrier不变。

一个LocalOperations/7200秒总deadline拥有全部步骤；只读observer action替换旧preserve action，保持有限action集合、原transport与普通16KiB streams/两处catalogue例外。新代码必须重新检查所有最大公共字段程序尺寸，不假定旧437字节余量自动成立。

新窗口仍不得启动VM、修改分区或迁移存储、维护/重启系统服务、读取旧数据库、提交Task或调用provider。失败或unknown立即结束，不重试、不续跑、不失败后guest核查、不远端Stop。保留任何已发生的部分效果。

## 离线交付与验收

将本次真实两命令wire作为实际manager回归输入；先RED后GREEN。补齐精确空输出、原JSON空列表、owner/无关jobs、非列表/缺字段、空白/错误文本、非零/timeout/truncated/error、manager前后变化等拒绝与兼容回归，并覆盖两个guard调用点。

已归档observer核验使用真实receipt形态的filesystem fixture，验证完整成功、原路径重新出现、缺失/错误archive、错误inode/header/SHA/metadata、额外内容、parent/boot/manager/proc drift在任何后续写入前停止。配置/key读取spy必须保持0。

保留原151项覆盖并补新组合故障边界；最终代码后执行一次完整suite、verifier、六action最大尺寸校验和独立任务/整体复核。Source/kit/旧证据、HEAD/index不漂移；模板/binding authority false，actual/grant缺席，完整封存后才有资格选择真实入口。

真实成功必须同时有实际Engine `engine_ready`回执及资格首启`installed`结果；离线通过或进程exit0不能替代。完整Task/报告/页面闭环仍需后续单独明确的provider调用范围。

## 待确认的授权方式

可选择“接受A，验证通过后单次实跑”，一次授权上述离线实施、回归/独立复核/封存，全部通过后执行**一个新的剩余准备窗口**。这将包括重新进入guest、保留旧安装物/副本、启动Engine及条件新PG/Core和原Native权限、读取已准备DeepSeek凭据后经stdin传输；不包含模型调用。

也可仅接受A离线实施，完成候选后保持环境入口关闭。当前没有新授权，本文不自动恢复写入或执行。
