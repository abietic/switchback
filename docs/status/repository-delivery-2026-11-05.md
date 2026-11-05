# 2026-11-05 仓库交付与运行状态

仓库已公开，主线完整 CI 六项通过。未完容量接线保留在[草稿 PR #1](https://github.com/abietic/switchback/pull/1)。旧开发历史仍单独私有保存。

| 验收项 | 当前事实 | 证据 |
| --- | --- | --- |
| 公开主线 | 53f767437042314aafb0b6683d030f272737a38f；独立根、noreply身份 | [主线提交](https://github.com/abietic/switchback/commit/53f767437042314aafb0b6683d030f272737a38f) |
| 完整 CI | Static、Go/race、PG、Web、Browser、Required 均通过 | [运行42055615324](https://github.com/abietic/switchback/actions/runs/42055615324) |
| 保护 | public；strict Required；PR必需；管理员同样受限；禁止强推和删分支；仅squash | GitHub API已读回 |
| 密钥保护 | secret scanning、push protection均enabled | GitHub API已读回 |
| 未完入口 | 后续Turn、返工、续作、恢复继续显式拒绝 | [PR #1](https://github.com/abietic/switchback/pull/1)、RUN-053 |
| 新运行产物 | 20二进制、43条本次构建回执、1178 kit文件、29 bundle文件完成构建与校验 | 私有证据保留；尚无新现场通过收据 |

## CI问题与解决

| 问题 | 修复 | 实际验证 |
| --- | --- | --- |
| CI依赖私有Git祖先 | 可移植exact manifest，保留502条义务及五组编译 | 独立提交图完整CI通过 |
| scanner工具错误 | 显式安装ripgrep并记录版本；错误继续拒绝 | Linux发布审计及scanner回归通过 |
| Linux纳秒夹具 | 正向deadline/checkpoint输入规范为UTC微秒；严格生产校验及断言保持 | 确定性反例、普通/race回归、完整PG通过 |
| stat平台差异 | 分别捕获失败BSD与成功GNU输出，保留0700断言 | 反例模型、Darwin及实际Linux通过 |
| 移动导航旧selector | 使用实际Admin文案；保留滚动/焦点断言 | 完整Browser通过 |

## 下一次实跑

RUN-052新Source仅两个已评审Go文件变化，root为 `sha256:cdd4e3ed05ae5d76de2a4d298d35d9b9e66b57481e325cea34fedd571779bc94`。templates和Engine Python源保持。新kit manifest为 `sha256:d894ec2cf8fbdcd641d7be788aed812c193c6c5c4e449c4792ead35859043d79`，bundle manifest为 `sha256:49d490b7704194aa511a69642d063aaed4bc335845021a0d7142a175792dfbd4`。

完整capacitysnapshot包通过，旧路径fixture唯一skip随后用仅测试overlay实际验证新kit/bundle并通过；不混称单轮零skip。继续完成维护载体、seal、reader重绑，再用具体单次窗口验证Engine→资格→条件Core首启。随后优先首次initial任务→正式报告→页面接受→同Task观测；后续owner留在独立PR。

当前没有新guest执行、模型调用或完整产品验收结果。运行状态见[清单](runtime-checklist.md)和[53项问题表](runtime-issues.tsv)。每次现场执行后更新原RUN-ID、最后成功动作、未知effects和下一动作；离线、CI和现场结果分别记录，旧失败窗口不续跑。
