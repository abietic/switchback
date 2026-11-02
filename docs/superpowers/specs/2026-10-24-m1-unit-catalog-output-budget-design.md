# Engine unit 清单 stdout 预算最小修复设计

状态：待接受的候选设计；当前源码与kit14保持原样，不再执行已消费窗口。

## 本次真实结果与根因

kit14四副本已经真实切换并完成2340新leaf校验，五旧对象（含1954文件stage）及三个更早archive完整保留。原Engine actual一次120秒窗口实际1.729684417秒结束，host和guest均unknown。仅preflight一个step，未进入安装/服务启动步骤；镜像、新秘密、新PG/迁移、资格窗口和provider未开始。

本次有两份128进程的完整process/FD inventory记录，以及store扫描与listener inventory。最终被拒绝的固定命令为：

```text
/usr/bin/systemctl list-unit-files --all --type=service --type=socket --no-legend --no-pager --plain
```

`_bounded_run` 所有stdout/stderr共用 `_OUTPUT=16384`。该命令stdout超过16KiB，执行器截留16384-byte前缀、标记truncated、停止其自有进程组，exit -9；原始错误 `fixed command output exceeded bound`，没有timeout，stderr为空。前缀中有230个完整换行，完整输出长度和完整catalogue仍未知，不能用前缀推断已完成覆盖。

本轮2448文件新host审计目录已按原inode/每叶identity/bytes/hash冻结保存。旧host/guest unknown保持，不能改写为“环境无变化”。本轮没有失败后的guest核查、重试、续跑或Stop。

离线入口 `.scratch/runtime-m1-process-state-identity-execution-1024/replay_output_bound.py` 使用实际 `_bounded_run` 与现有fake child/selector/signal seam：捕获的16KiB前缀control通过，前缀加1 synthetic byte稳定触发同一错误。这是buffer边界复现，既不是补全的真实unit catalogue，也不是guest重放。

## 选择与推荐

A（推荐）：仅为 `_unit_catalog` 的两个固定只读列表命令定义256KiB stdout预算；stderr、其他命令、结构化step摘要和所有覆盖/拒绝规则保持。不会牺牲完整清单来绕开本次失败。

B：过滤名称或只列docker/containerd相关units。它会遗漏改名服务的ExecStart，本次不采用。现行代码逐项读取全部unit的ExecStart后才判相关性；[systemctl官方说明](https://www.freedesktop.org/software/systemd/man/latest/systemctl.html) 也区分已安装unit文件/模板和当前已载入units，不能用其中一个代替两份完整列表。

C：提高所有命令和摘要的全局上限。改动范围超过本次需要，本次不采用。

## 精确边界

仅下列exact argv的stdout上限从16384增为262144 bytes：

```text
/usr/bin/systemctl list-unit-files --all --type=service --type=socket --no-legend --no-pager --plain
/usr/bin/systemctl list-units --all --type=service --type=socket --no-legend --no-pager --plain
```

- 使用私有固定argv策略；不提供CLI/env/stdin/caller可选budget，不按basename、前缀、忽略大小写或规范化参数匹配。参数不同、顺序不同或附加过滤时仍是原16KiB。
- 所有命令stderr仍16KiB，其他命令stdout仍16KiB。原`_OUTPUT`、`_text`和step stdout/stderr摘要仍16KiB；receipt/schema/字段格式不变。
- 选中命令只有完整EOF、exit0、无timeout/truncation且原owned-process收敛全部满足后才返回完整raw stdout给parser。超过256KiB仍拒绝，不接受prefix、不分页/重试、不跳过unit。
- `_unit_catalog` 的两份完整列表、最多512个名称、每项属性/ExecStart判定和mask/foreign daemon/已占新unit的拒绝保持。PID/start/全部flags及R/S边界、FD/owner/socket/完整listener检查均不改。
- raw observation单帧1MiB、累计64MiB保持；256KiB stdout按现有base64 wire后仍受这两个原门禁约束。扩大的是两个命令的输入预算，不是取消audit/frame拒绝。
- 原120秒Engine、240秒资格、300秒barrier、非reap身份保留/本地group cleanup、权限、资源、PG/Trust/Start保持。资格进程适配器仍加载exact新Source，不新增公共override或协议版本。

## 整片离线交付与验证

1. 修改前把现有隔离复现落入实际runner回归，保留有效RED。captured prefix、synthetic overflow和完整synthetic列表各自标清，不伪称真实完整catalogue回放。
2. 对两份exact argv验证16KiB+1和256KiB完整返回，256KiB+1拒绝；两个stderr16KiB+1仍拒绝。普通/近似/重排/加参命令的stdout16KiB+1仍拒绝。
3. 将较大synthetic完整unit列表经实际 `_unit_catalog` 调用点验证完整覆盖：512/513名称边界、缺失mask、坏行、非零/timeout/truncated拒绝、改名daemon的ExecStart仍被发现；完整raw保留和16KiBstep摘要分别验证。
4. 运行相应完整离线Engine/资格/Go/helper-source绑定验证，不运行历史native fixtures，不进入guest或操作live副本。接口/receiver/frame若仍会拒绝新预算，先在离线交付中收口，不能在actual中临时放宽。
5. 用原builder一次完成新完整kit、canonical bundle、逐叶/SourceRoot/TemplatesRoot/20格式/43构建收据/17逆输入和真实Go final binding；独立源码/交付/整片复审。旧候选/失败/所有seal保留。
6. 完成新具体保留清单：现行kit14四副本、本次2448叶host stage、四个既有archive都保留；不清理或复用当前stage/claim。具体实际切换仍另行授权。

接受本设计只授权整片源码与离线候选交付，不进入guest、不操作现行副本/stage/archive、不启动或重试环境，不生成秘密或改变PG/Start。预算足以容纳本次完整真实清单尚未被证明；下一次窗口仍须完整事实与所有门禁通过。
