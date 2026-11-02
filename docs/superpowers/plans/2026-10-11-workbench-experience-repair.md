# Workbench 体验修复实施计划

> **For agentic workers:** 按任务逐项内联执行 red → green → 实机复验；不新建委派任务。本计划仅覆盖用户已确认最小闭环的第一项，后端控制、退出、产物与观测各自形成独立诊断/实施切片。

**Goal:** 无产物不无限 Loading，报告到达即显示，切换时不显示旧请求的错误或内容。

**Architecture:** 保持 `ArtifactWorkbench` 公开接口与 production Artifact decoder/review authority。使用 exact Artifact ID 绑定请求与错误；空态优先，内容缓存与真正尚未返回的请求分开。不得从 summary 伪造内容或下载权限。

**Tech Stack:** React、TypeScript、Testing Library、Vitest、Vite；沿用 lockfile，不新增依赖。

**Status:** 本切片实现及真实空态/刷新验收已完成；完整端到端闭环仍未通过。独立预览 `127.0.0.1:44174`，证据 `.scratch/workbench-experience1011.56Rp17/preview-result-r2.json`。

## Global constraints

- 已批准设计：[端到端最小闭环](../specs/2026-10-11-platform-experience-loop-design.md)
- 只修改本会话 `.worktrees/observability-master-integration-1009`，不动主 checkout dirty
- 当前已确认 seam：真实 `ArtifactWorkbench` 公共组件 + HTTP snapshot 边界 + 生产 Worker 页面
- 保留旧 `experience1011` 镜像、容器、失败数据与证据；新预览使用独立端口和构建来源
- 本切片不运行付费模型、不改数据库、不提交/推送/合入
- 仅以组件测试证明组件行为；必须另在真实页面核验无产物断言，不能据此宣布端到端全部通过

---

### Task 1: 无产物显示空态

**Files:**

- Modify: `web/src/features/worker/workbench/ArtifactWorkbench.tsx`
- Test: `web/src/features/worker/workbench/ArtifactWorkbench.test.tsx`

**Interfaces:** 输入 `artifacts: ArtifactSummary[]`；输出真实 DOM 的空态、加载、内容或错误。HTTP 仍由 `loadArtifactPreview(productionViews.artifact(id), { signal })` 提供。

- [x] 写入回归测试并先运行观察失败

```tsx
it("shows an empty state instead of loading when no artifacts exist", () => {
  renderWorkbench({ artifacts: [], onRequestedArtifactHandled: vi.fn() });
  expect(screen.getByText("No artifact")).toBeInTheDocument();
  expect(screen.queryByText("Loading artifact…")).not.toBeInTheDocument();
});
```

运行：`cd web && npm test -- src/features/worker/workbench/ArtifactWorkbench.test.tsx -t 'no artifacts exist'`。修复前应因缺失 `No artifact` 而失败。

- [x] 空态优先于任何 loading/error 比较；最终实现不再保留独立 loading state

```tsx
{!artifact ? <WorkbenchMessage title="No artifact" message="This stage has not published this artifact type." /> :
  error?.artifactID === artifact.artifact_id ? <WorkbenchMessage tone="danger" title="Artifact unavailable" message={error.message} /> :
    !selected ? <WorkbenchLoading /> :
      <ArtifactSurface tab={selectedTab} artifact={selected} resolvedTheme={resolvedTheme} />}
```

- [x] 重跑同一命令，要求 exit 0

### Task 2: 空态后到达的报告可直接查看

**Files:** 同 Task 1。

**Interfaces:** `artifacts` 被新的 owner projection 更新时，现有 tab preference 不得让唯一可用产物继续不可见。

- [x] 覆盖空 projection 后报告到达；使用有效 `online` manifest 验证，不绕过 decoder。最初无效 fixture 产生的失败不计为产品 RED 或 GREEN

```tsx
it("shows the report when it arrives after an empty projection", async () => {
  mockArtifact({
    ...artifact, availability: "online", content: "# Arrived report",
    review_manifest: manifest("online", true, true),
  });
  const handled = vi.fn();
  const view = renderWorkbench({ artifacts: [], onRequestedArtifactHandled: handled });
  view.rerender(<ThemeProvider>
    <ArtifactWorkbench artifacts={[artifact]} onRequestedArtifactHandled={handled} />
  </ThemeProvider>);
  expect(await screen.findByRole("heading", { name: "Arrived report" })).toBeInTheDocument();
});
```

- [x] 使用实际可用类型解析 tab，不改变用户在有 patch 时选择 Files 的偏好

```tsx
const selectedTab = tab === "report" && !report && patch ? "diff"
  : tab !== "report" && !patch && report ? "report" : tab;
const artifact = selectedTab === "report" ? report : patch;
```

Tabs 的 `selected` 和 `ArtifactSurface` 的 `tab` 都使用 `selectedTab`。执行：`npm test -- src/features/worker/workbench/ArtifactWorkbench.test.tsx -t 'arrives after an empty'`，要求先红后绿。

### Task 3: 切换后旧请求不污染当前显示

**Files:** 同 Task 1。

**Interfaces:** mock 只放在 HTTP `fetch` 边界；deferred response 可在 abort 后返回，以验证 UI 不接纳旧内容/错误，而不是依赖 mock 的主动取消。

- [x] 覆盖已缓存报告 A → patch B 失败 → 切回 A、abort 后 B 晚到响应 → 再选择 B、同 ID summary 刷新不取消原请求。完整测试已落实于 `ArtifactWorkbench.test.tsx` 的 `keeps a cached report visible after another artifact fails`、`discards a late response from an aborted artifact request`、`keeps the same artifact request alive across summary projection refreshes`，均有匹配 RED 和最终 GREEN
- [x] 错误绑定 exact ID；删除冗余 loading state，用当前选择是否存在有效 snapshot 判断请求中

```tsx
const [error, setError] = useState<{ artifactID: string; message: string }>();
const artifactID = artifact?.artifact_id;
// 请求依赖 identity，不依赖 projection 中重复创建的 summary 对象。
useEffect(() => {
  if (!artifactID || snapshots[artifactID]) return;
  const controller = new AbortController();
  setError(undefined);
  void loadArtifactPreview(productionViews.artifact(artifactID), { signal: controller.signal })
    .then((outcome) => {
      if (controller.signal.aborted) return;
      if (outcome.kind !== "available") throw new Error("Artifact review is unavailable.");
      setSnapshots((current) => ({ ...current, [artifactID]: outcome.preview }));
    }).catch((cause: unknown) => {
      if (controller.signal.aborted) return;
      setError({ artifactID, message: cause instanceof Error ? cause.message : "Artifact review is unavailable." });
    });
  return () => controller.abort();
}, [artifactID, snapshots]);
```

渲染顺序：无 artifact → exact ID error → 无 snapshot 的加载 → 权威内容。现有校验失败、下载权限和主题测试保持通过。

- [x] 运行 `npm test -- src/features/worker/workbench/ArtifactWorkbench.test.tsx`，25 用例通过

### Task 4: 将本切片接入真实体验

**Files:** 新增独立 `.scratch` 证据与预览构建产物，不修改旧证据；若 OpenSpec CLI 可用，在 `production-web-core-experience/tasks.md` 单独追加本回归记录，不提前关闭 6.5。

- [x] 执行 `npm test`（44 files / 522 tests）与 `npm run build -- --logLevel warn`；完整输出保存为新日志，保留已有 >500 kB chunk 警告
- [x] 用新 Web bundle、独立 loopback 端口连接旧体验 Backend 48080；不替换旧 Web 44173、不重启失败 Worker
- [x] 浏览器打开同一 Worker 的新页面并刷新，均不再出现 `0 artifacts` 加 `Loading artifact`，保存截图；Task/Run 仍为旧后端的 Running，不伪装已修后端
- [x] 核对 125 个当前源码文件的 SHA-256/权限与冻结 r2 清单完全一致；保存完整镜像 build 日志、实际 image ID、端口、测试和 DOM 断言；剩余缺口仍在 evidence 中明确列出
- [x] `git diff --check` 通过，仅有本计划、设计、组件、测试及对应 production Web OpenSpec 的 scoped 变更；未提交代码

## Self-review

本计划只交付无产物反馈与请求身份修复；不声称覆盖已批准设计中的后端/观测全部工作。后续切片先建立控制队列真实调用 RED、退出收敛 RED，再写其独立实施计划，保持逐项接入真实页面的节奏。
