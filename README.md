# Switchback

Switchback 是面向需求交付的本地 Agent 平台，包含 Assistant 需求澄清、隔离 Worker 执行、Task 与产物、事件投影、轨迹和可观测性。

项目仍在完成真实环境验收。源码测试、离线候选封存与历史局部运行结果分别记录，不能替代当前版本的完整任务交付验收。

- [当前进度与问题入口](docs/status/README.md)
- [运行及仓库交付清单](docs/status/runtime-checklist.md)
- [51 项问题跟踪表](docs/status/runtime-issues.tsv)
- [真实运行推进复盘](docs/status/2026-11-02-runtime-progress.md)
- [Agent 源码导航](docs/agent-navigation.md)
- [运行环境与部署说明](deploy/experience/README.md)

后端使用 Go，生产前端位于 `web/`；`web-demo/` 是原型。基础本地检查：

```sh
make fmt-check migration-check generated-protocol-check worker-image-check
make unit
make race
make vet
cd web
npm ci
npm test
npm run build
```

PostgreSQL 集成测试需要显式配置独立测试数据库；真实模型调用、Worker 容器部署和主机存储操作各有独立运行前提，不能从普通测试通过推导其已完成。

公开快照保留实现与可移植进度，不包含原开发 Git 历史、本机凭据、原始现场审计或私有执行目录。历史证据可见范围和剩余验收见状态文档。
