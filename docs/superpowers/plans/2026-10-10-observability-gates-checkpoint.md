# 可观测性建设检查点

_2026-10-11 · 已完成测试门禁、构建来源工具、cold/matrix 只读检查及查询留档/回放；统一汇总、物理验收与取消问题仍未闭合。_

---

## 📋 本轮完成了什么

- `659027a`：普通 PG 回归与六项原生验收分组，测试不删除、断言不改弱；纯测试和 SQL prepare 仍在原来的入口。真实 Go 构建清单、函数体守恒及 16 个正反例通过，独立复审 Approved。
- `39b4c3a`：新增严格原生入口。环境、容器归属、真实端口或缓存镜像不匹配会在执行前失败；最终必须是六项指定测试实际 run/pass，不能用 SKIP、空包或手填 passed 代替。CI 的原 PG 命令和严格检查器保持原样，仅 static 增加工具自测。独立任务审查 Approved。
- `739e30a`：构建来源工具已实现并独立复审 Approved。可记录并只读核对“指定源码 → Backend / Worker base / controlled Worker 构建 → 实际镜像 ID”；工具回归使用外部 fake Docker，真实构建尚未执行。
- `0208668`：新增冷启动与故障矩阵的只读检查，从原始输入重新计算结果、不读取旧 PASS 作为结论，也不改动证据文件。旧写入入口、断言及 JSON 不变；独立审查 Approved，root 精确候选工具复验通过。
- `74132ab`：新增受控观测查询的私有原始响应留档与 HTTP 离线回放；拒绝材料篡改、缺失和错误绑定，原查询断言不变。独立复审 Approved，root 精确候选工具验证通过；不是完整 G3 离线或物理验收。
- 不改产品 capture、ACP/YHC、WorkerControl/settlement，也不放宽 250ms/30s 原时限。指定集成 master `8fdbe6b` 不变。

## 🔍 历史 PG 验证：39b4c3a

受测 commit 为 `39b4c3a7e960f18b2db07000d41447e331f049fd`，tree 为 `df8d7b25d3a02a94389ed60bd8cb030faff04288`；开始、结束时树干净。后续交接提交只修改文档，不冒称其全新 tree 已再跑测试。

| 验证 | 实际结果 |
| --- | --- |
| 全仓 PG + race，GOMAXPROCS2 / -p1 / integration / count1 | Go exit0；原 strict checker exit0 |
| PG 测试执行 | 1,148 个顶层测试、2,381 个含子测试 PASS；测试级 SKIP/FAIL 均为 0 |
| 包完整性 | 59 包：49 PASS，10 个无测试包的 package-level skip；没有测试级豁免 |
| 普通 `go test -short -count=1 ./...` | exit0，低并发执行 |
| 格式、迁移、生成协议、Worker 镜像契约、vet、secret scan | exit0 |
| OpenSpec 全量严格校验 | 13/13；不代表实现验收完成 |
| strict/native JSON、native preflight 自测 | exit0；native 自测只用合成事件和 fake 外部边界 |
| 无环境的实际 native runner | exit1，明确缺少 LOG_CLIENT，未运行原生测试 |

PG 原始日志 SHA256：`017996498d31f9d853b3b7cc10c2e5bab4ae97b9a6309c3aad24538352e5e07b`。原始 JSONL、完整命令、退出码、摘要、镜像/容器及候选元数据保存在 root 本地私有目录 `.superpowers/private-gates1010/`，未上传轨迹。日志仍含 22 条 WARN/ERROR，分布于 16 个已通过的故障/降级相关用例；本轮未清理这些日志噪声，不声称输出完全无告警。

本轮没有出现 `capture_cancelled` 输出，但没有复现不等于修复。旧失败和 G3/G4/retention 证据的六份冻结 SHA 全部未变，主/旧工作树 dirty 摘要未变。仅删除本轮新建且再次核验归属的 PG b 容器；`switchback-obsv-gates1010b-postgres-data`、此前 a/匿名卷、所有旧卷和日志保留，可恢复。宿主 PG 未动。

## ⚠️ 还没有完成什么

六项真实原生测试、新候选的 G3/G4/实际 retention 和物理 Docker 窗口均未执行/确认。现有 Compose claim 不发布 Alloy/Loki 端口，严格入口会拒绝，不会自动创建或复用资源。旧 claim 兼容性证据不能替代新候选物理验收。

构建来源存在明确缺口：`matrix.go:695-705` 记录运行时 HEAD 与容器 ImageID，`report.go:39-46` 只校验格式；当前记录不能证明 Backend/Worker 镜像由该源码构建。只读双门禁汇总尚未实现，不能凭 SHA 字符串输出通过。

`capture_cancelled` 仍开放；OpenSpec 3.2 未勾选；没有 push、PR Ready、合入、全分支合入审查或真实 Agent/provider 验收。任务审查 Approved 不代表这些事项已完成。守恒检查保留本轮迁移基线 `2ee4c24`，只对获准的13项新增测试/辅助函数登记固定身份、函数体摘要和分组；旧测试变化、未登记新增、登记内容变化仍拒绝，不以当前 HEAD 替换旧基线制造通过。

## 📍 第三阶段当前状态

用户已明确允许仅在验收工具中补“commit + 源码清单摘要 + 构建日志 + 实际镜像 ID”的绑定；不改产品逻辑或 Dockerfile、不覆盖旧记录。[设计补充](../specs/2026-10-10-observability-two-gates-design.md#-第三阶段补充构建来源记录)规定从 exact commit 的源码快照真实构建 Backend、Worker base 和 controlled Worker，再只读核对日志、IID 与实际 fixture。

补充规格已获确认。构建/只读核对工具已完成，受测代码提交 `739e30a83b01314eb5f1980094df7bf8481aed18`，fresh 独立任务复审 Approved，无 Critical/Important；它记录 exact Git 源码快照、三次构建日志/命令、实际 IID/镜像信息，再只读重算核对。首轮审查的输出限长、JSON 接口、私有目录问题，以及次轮发现的超限日志失败记录缺口，均已修复并补入口反例。尚未实现双门禁汇总。

root 在 `739e30a` 实际验证：工具 race（61.328s）、三个默认包、固定 `2ee4c24` 基线的五组编译守恒、vet、格式、脚本语法和任务差异检查均 exit0。仅使用真实临时 Git/文件和外部 fake Docker，**不是实际镜像构建或物理验收**。上方 `39b4c3a` 的全仓 PG 结果只属于历史候选，不转移至本提交。

审计限制仍保留：工具初轮两份中间调试失败日志曾被后续同名输出覆盖，不能恢复；fix1 首次 EOF 是 fake portability 错误，不计有效 RED；三条只读 Git 调用未显式重复 sanitize 的执行纪律偏差也已记录。六份旧冻结验收证据与主/旧工作树 dirty 摘要当前仍匹配，未覆盖。

只读检查子交付也已完成：受测代码 `0208668e95300b7d4f805e6c5b583885a7791e45`、tree `5e252341a82aa2b9d0704c3d7877a966579de23c`。root 独立执行两个工具包 race、三个默认包、固定基线守恒及五组编译、vet、格式、脚本语法和差异检查，均 exit0；fresh 审查无 Critical/Important。验证只用临时 Git/合成文件/真实 CLI 和既有 httptest，不运行真实 Docker、PG、native 或 provider。实现者模块缓存 warning 及此前守恒冲突失败日志保留；不转移历史 PG 结果。

查询留档子交付已完成：受测代码 `74132abb43605e920fc3d79dc405221fe4ba405c`、tree `af92da4d6c4e185a3ff7d71b5160c642f38feb2a`。root 独立两包 race、四包默认回归、固定基线守恒/五组编译、vet/格式/差异检查全部 exit0；另在真实路径临时目录完整执行新包 default/race，也均 exit0。独立复验暴露的 macOS 目录别名问题已修正测试夹具并补真实 RED/GREEN；失败日志、旧证据和执行偏差保留，非阻塞覆盖建议留最终整分支审查。不改产品目录安全校验，未访问真实观测服务。

下一步补齐原始 SQL/source facts 的离线重建，复用原断言完成 G3 离线核验，以及 scrape/dashboard/alert 原始材料和 loss/retention 便携核验；不能用旧 PASS、summary 或新 manifest 代替。随后协调新 claim、实际镜像构建和真实组合回归。架构会话已确认本轮文件与低并发纯工具窗口无交叉占用；真实资源窗口仍须另行具体确认。`capture_cancelled`、OpenSpec 3.2、PR Ready/合入仍未闭合；本轮无实际资源清理或外部发布。文档交接不改受测工具代码，也不冒称新 tree 已跑完整 PG/物理验收。
