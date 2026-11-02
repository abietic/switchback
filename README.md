# Switchback

Switchback 是面向需求交付的 Agent 平台，包含 Assistant、隔离 Worker、Task 与产物、事件投影、轨迹和可观测性。

主分支保存已提交实现和可移植进度。新增容量准入／资格切片仍在独立功能分支推进，尚未完成返工、续作、驻留后续 Turn 与恢复入口，不能将其离线测试或候选封存当成完整产品验收。

- [当前进度与问题入口](docs/status/README.md)
- [运行及发布清单](docs/status/runtime-checklist.md)
- [问题跟踪表](docs/status/runtime-issues.tsv)
- [真实运行推进复盘](docs/status/2026-11-02-runtime-progress.md)
- [源码导航](docs/agent-navigation.md)
- [公开快照 CI](docs/ci-public-snapshot.md)
- [本地部署配置](deploy/local/compose.yaml)

后端使用 Go，生产前端位于 `web/`；`web-demo/` 是原型。基础检查：

```sh
make fmt-check migration-check generated-protocol-check worker-image-check
make unit
make race
make vet
make publication-check python-check
cd web
npm ci
npm test
npm run build
```

PostgreSQL 集成测试使用显式配置的独立测试数据库。真实模型调用、Worker 部署和主机存储各有运行前提；普通 CI 通过不代表这些现场验收已完成。

公开快照不包含原开发 Git 历史、本机凭据、原始现场审计或私有执行目录。未完成源码保持独立 PR 和明确失败记录，不通过削弱测试让主分支接受未集成行为。
