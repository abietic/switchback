# M1 B3-F 目录身份校验修复设计

## 已确认的需求和范围

用户在上一轮确认的最小修复提案及“新候选集成修复并复审”的下一步后要求“继续推进吧”；本轮将其作为离线实施授权。此前真实授权仅一次，B3-E已消耗并在guard_directory_changed结束。本设计不授予真实执行权限。继续采用现有单次window，不重做架构。

真实回执与独立本地实验在E/report.md、local-filesystem-semantics.json和guard_replay.py：创建marker后目录nlink由2变3，其他身份字段不变；全metadata旧快照比较误拒绝。原HostIO固定nlink且共享可变字典，漏测宿主语义。旧原始guest/ELF失败仍unknown。

## 选择与精确修法

采用已展示的稳定身份方案：末尾重新采样held directory FD，与当前名称解析的完整metadata比对，然后与初始device/inode/uid/mode比对。初始快照显式复制为值；marker仍nlink=1。允许directory nlink随自身创建文件变化。另一种按平台跳过检查的做法增加平台分支，本轮不采用。

```python
meta = dict(call('metadata_fd', window))
# 原directory identity检查、marker single-link检查、写入/fsync/close均保留。
current = dict(call('metadata_fd', window))
require(current == call('metadata_at', parent, leaf) and
        all(current[k] == meta[k] for k in ('device', 'inode', 'uid', 'mode')),
        'guard_directory_changed')
```

此处省略号由既有durable_guard原体原样保留；它不是新增逻辑或实现占位。检查最终路径替换、owner/mode/device/inode改变、文件多链接和每个required原语失败；这些必须继续拒绝，后继transport/guest IO为零。

## Fixture与真实本地文件系统覆盖

HostIO.metadata_fd/metadata_at返回独立dict。目录link模型覆盖稳定值与marker创建增长两种，测试使用真实guard函数，不镜像判断结果。把E的五类回归整合到test_host.py，并验证snapshot不共享、写入后初始值未变化。原WindowGuardTests故障矩阵和全部55既有host方法保留。

新增一个只提供原始文件端口的LocalGuardIO：在/private/tmp内新建0700临时parent，使用os.open/mkdir/stat/fstat/write/fsync/close实现guard端口；所有符号路径映射到该临时parent，绝不指向仓库真实window，不构造NativeHost/NativeSupervisor、不spawn。直接调用F的durable_guard，校验guard成功、marker0600/nlink1、目录稳定身份、FD收尾；临时目录由其拥有者finally清理。对目录nlink不写跨平台固定数值断言，memory case覆盖2→3与不变两类。此测试证明当前本地文件系统原语兼容，不证明native监督、Lima或guest。

## 新候选和来源

- Worktree=/path/to/switchback；HEAD=fbebfe754045bdbf1af40871a5018eed33743011、branch=codex/observability-master-integration-1009、index不变，不stage/commit/push/reset。
- F=.scratch/runtime-m1-deployment-diagnostic-guard-b3f-1021；W=.scratch/runtime-m1-deployment-diagnostic-window-b3e-1021；E=.scratch/runtime-m1-deployment-diagnostic-execution-b3e-1021；P=.scratch/runtime-m1-deployment-diagnostic-carrier-b3e-1021。F以外旧文件只读；prior-carrier-manifest.json固定286项；Git-visible基线1785，仅新增本spec和plan两条登记路径，最终1787。
- 本轮仅离线实现/验证/评审/封存。禁止NativeHost/NativeSupervisor/host CLI/fork/transport/guest/ELF/VM/unit/Engine/PG/Backend/Worker/provider执行；允许memory fixtures和新独立临时目录中的本地guard文件系统测试，不得指向真实window。capability/resource/Trust/quota/keys与三个gates不变且false。
- F runtime/test source仍只有guest.py,host.py,output_mapper.py,manager_forensics.py,test_guest.py,test_host.py,test_output_mapper.py,verify.py八份。output_mapper.py/manager_forensics.py/test_output_mapper.py逐字节复制W；guest只允许四个固定身份literal变化，所有guest业务/资源/协议逻辑不变。ELF仍5658871bytes、host0400、SHA38b02a79d69b87317db97679db04a2df3a75c3ddcf6fe9bedf47b1e2601cb1c5，build-receipt SHAff631c6b4ba3bd96ea8b1a168a3b56cfb33e28255d236353edd871e7153e1127，不build/执行。guest/bootstrap分别<=65536。
- 新身份只用于未执行候选：nonce=36884bd8-2a04-411b-be72-8c732eeec1c0；stage=/var/lib/switchback-deployment-diagnostic1021-b3f；unit=switchback-deployment-diagnostic1021-b3f.service；cgroup=/system.slice/switchback-deployment-diagnostic1021-b3f.service；window=.scratch/runtime-m1-deployment-diagnostic-native-window-b3f-1021必须absent。旧B3-E窗口已消耗，必须保留原样且只含window.once，SHA6f573a55ea9b2fd526a4504142de9fa35ee87700f166f9ac6f074abe4d58ab0c；不得删除/reset/rename或重用旧授权。
- 单次window、RAM cursor、私有worker握手/父fencing、150秒总限含最后1秒cleanup、30/45/45/30步骤上限、32768摘要、pre-fork writable nonblocking pipe/socket校验、noresume/noindependentreadback/noStop/unknown termination全部保留。schema和guest v3不升级。
- marker nlink=1及目录device/inode/uid/mode身份检查保留。own marker写入前后不要求directory nlink恒定；最终held-FD和named metadata必须完整一致，再比较初始稳定身份。required写入/fsync/close失败、取消或超时均关闭窗口，永不从receipt恢复权限。
- 新baseline/来源必须绑定286旧文件、977旧inputs、9份B3-D、814历史seal entries（646+154+14），五份seal的SHA与count严格固定。新verify只读固定输入无selector；不调用旧verifier/native入口。注册两个docs路径；plan进度字节不参与candidate selfhash，最终seal绑定快照。

五个HISTORY输入依次为原B3D134/B3C443/B3Cnative69、W/phase-seal.json154（SHA5c18b54b5b4f933821dfbd34842c3be98a27f73cd4573f572add04c960d58cd2）、E/evidence-seal.json14（SHA1fe8ab0a7ad45e14b9063908e5d5dc3a6863ff31dcc62aa9ce13911685f638b1）。新candidate/八源/source-manifest/release必须重新绑定，identity-only guest变化用完整diff/AST校验，不继承旧native授权。

## 验证和交付

先在未修复的新候选跑guard回归，确认同一guard_directory_changed红灯；修复后只跑一次最终八源完整unittest discover（test_*.py）及固定verify。记录exactargv/env、before/after source/candidate/ELF、stdout/stderr和exit；不将旧92组合证据冒称新全量结果。目录共享模型改变影响大，完整host矩阵必须跑，本轮不以旧回执替代。

SDD独立task review和最强模型whole review；所有C/I修复后覆盖验证并复审。root最终核对旧286与基线、old marker、new window absent，保存最终plan/ledger快照并seal全F目录后逐项读回。交付离线候选、操作清单、验证与评审；新的真实窗口留待单独授权。无Git提交操作。
