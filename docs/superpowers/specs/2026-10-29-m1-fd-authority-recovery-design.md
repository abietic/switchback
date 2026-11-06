# M1 FD 稳定性视图与三项已保存恢复设计

用户已通过 `call_61c22041df6e4608b852670d9436026d` 接受 A、授权完整离线交付。批准的原提案按原字节保存在 `.scratch/runtime-m1-fd-authority-recovery-1029/approved-design.md`，SHA256 为 `987384f0e3f30da760368e0d2ef06440e3bc764f23503fb1ba1747158ea879e9`。本文固定实现组织和接口，不扩大该提案语义。

## 范围与证据

新离线 phase 为 `.scratch/runtime-m1-fd-authority-recovery-1029`；唯一未来执行目录为 `.scratch/runtime-m1-fd-authority-recovery-execution-1029-01/carrier`，本片必须保持不存在。

旧离线封存 `a74413a79dc7b80ab85bd33771878d3882b004af4e89e7e7d30963b2738ab2cf` 与单次失败封存 `ab87df31b33c1df7b878f1570653bb924b7b982bd973060f5c5a9a2019d7b42b` 原样保留。后者记录44,200,380 bytes、45,110 frames，journal SHA256 `1d362d502e46cdfccbd8e504e387d3b9484a906775dbcbf7355bb2c823aaaadf`。失败 sample 序列40023、42063中，systemd timerfd换号、timesyncd关闭无关socket足以触发当前整体相等检查。

全量采集、逐样本保护校验和 raw proof 必须保留。旧 `quiet_partial_v1`、owned_pair/containerd_only、Source1095项、templates7项、Go、PG/Trust/Start、quota、权限、120秒维护、120秒Engine、240秒资格、300秒barrier、7200秒driver及现有预算不改变。本片不进入guest、不读key、不调用provider、不安装或启停环境，不修改Git/index及既有tracked改动。

## 两个实现单元

### 共享 FD 视图

新增纯函数模块 `carrier/fd_authority.py`，对两份已经分别通过保护检查的process列表生成两份深拷贝视图。接口为 `authority_process_pair(before, after, *, sampler, roots, objects)`。`sampler` 固定main_pid/start_tick，roots为完整保护路径集合，objects为两份完整保护对象的并集。

仅在新的 `quiet_partial_v2` quiet恢复路径使用。采样器自身完整FD集合保留；其他进程只从稳定性视图移除“路径与(device,inode)均无关”的FD。相关FD不得被隐藏，无法分类或输入不完整时拒绝。不得按PID1、systemd、timerfd、inode1062等名字特判。函数不做IO、不授予权限、不修改输入，其余进程字段完整保留。

initial quiet owner pair必须先完成两次 `_sample` 校验；stopped必须先完成两轮原有保护负例，才能调用共享函数。两处都使用两份对象的并集，不能在单份样本完成前提前归一化。原始before/after、FD/maps及其hash保持原始内容；比较视图与原始证明的语义清楚区分。

### 恢复布局与完整载体

固定接纳var、etc、run三个目录的既有保存；固定待处理两条unit文件及两个kit/bundle目录；API保持双侧缺失。用旧只读内容基线、本次完整journal、两个成功rename及post-identity/父目录回执构造离线布局；新窗口仍必须fresh证明全部位置、identity、内容、archive children、父目录、mount、quiet后才接纳。

新载体维护阶段0Stop、0guest mkdir、4次固定noReplace。宿主两副本和9735-leaf stage、同child单checkpoint/ACK/EOF、一次reload/strict、四新副本复制和原Engine→Native/credential/images/newPG/资格→Core顺序继续保留。缺任何必要历史字段即明确列出缺项，不能猜补或借本次离线授权进入guest。

Builder与独立verifier各自固定完整模块/角色集合、候选树闭包与mode、Source及kit25/bundle0002身份；沿用完整持久化journal、独立failure预算和DurableEvidenceStore。只生成关闭的grant，human/id/time/worker均null，environment/provider=false、no_retry=true。

## 验证与交付

共享函数先用实际捕获数据和保护反例测试；接线后initial与stopped均走真实调用路径，证明验证顺序和raw preservation。覆盖protected path/inode alias、writer/maps、held flags/identity/coverage、PID/start/exe/cgroup/闭包、manager/job/listener/store/mount漂移继续拒绝。

布局覆盖三项接纳、四项移动、五archive、双侧/缺失/内容/父目录异常及每步失败后的精确效果前缀。完整fake T0—T8必须包含本次真实FD波动，不能仅重复稳定样本。模块/程序/argv/expanded/binding、真实候选完整树、零写重建、旧输入保护、独立任务与整片评审、完整封存验核后交付。

本片验收是完整离线候选及回归通过，不是实跑或产品体验闭环通过。任何新的真实恢复仍须对具体封存候选另获授权。
