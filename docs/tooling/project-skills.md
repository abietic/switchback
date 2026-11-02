# Project Skills And OpenSpec Tooling

> 状态：v0.2。
>
> 本文记录当前仓库安装的项目级 Agent 技能和 OpenSpec 工作流。技能基线于 2026-07-16 使用 `skills` CLI 1.5.17 刷新；Matt Pocock 技能来自上游 commit `e9fcdf95b402d360f90f1db8d776d5dd450f9234`。

## Installed Project Skills

| Source | Installed Location | Purpose |
| --- | --- | --- |
| OpenSpec | `.codex/skills/openspec-*` | spec-driven proposal、design、specs、tasks、apply、archive、sync。 |
| obra/superpowers | `.agents/skills/{using-superpowers,brainstorming,writing-plans,...}` | 系统化思考、计划、验证、技能编写和多 Agent 方法论。 |
| mattpocock/skills | `.agents/skills/*`，见下节的稳定套件 | 需求澄清、规格与 ticket、实现、测试、诊断、评审、架构治理和交接。 |
| softaworks/agent-toolkit | `.agents/skills/mermaid-diagrams` | Mermaid 图选择和绘制指导。 |
| K-Dense-AI/claude-scientific-skills | `.agents/skills/markdown-mermaid-writing` | Markdown + Mermaid 技术文档写作规范。 |

## Local Commands

```bash
npx -y skills@latest list --json
npx -y @fission-ai/openspec@latest list --json
npx -y @fission-ai/openspec@latest status --change refine-switchback-platform-design --json
npx -y @fission-ai/openspec@latest validate refine-switchback-platform-design
```

## Matt Pocock Stable Baseline

本项目安装上游完整稳定 `engineering` 套件，并安装其依赖和路由会直接使用的稳定 `productivity` 套件：

```text
ask-matt
code-review
codebase-design
diagnosing-bugs
domain-modeling
grill-with-docs
implement
improve-codebase-architecture
prototype
research
resolving-merge-conflicts
setup-matt-pocock-skills
tdd
to-spec
to-tickets
triage
wayfinder
grill-me
grilling
handoff
teach
writing-great-skills
```

升级时明确移除了旧的 `to-prd` 和 `to-issues`，分别由 `to-spec` 和 `to-tickets` 替代。没有安装上游的 `deprecated`、`in-progress`、`personal` 和 `misc` 分类，避免把实验能力、个人工作流和特定技术栈工具混入 Switchback 项目基线。

## Refresh Commands

Use these only when intentionally updating project-level tooling:

```bash
npx -y @fission-ai/openspec@latest update . --tools codex

npx -y skills@latest add obra/superpowers \
  --skill using-superpowers brainstorming writing-plans dispatching-parallel-agents subagent-driven-development verification-before-completion writing-skills \
  --agent codex --copy -y

npx -y skills@latest add mattpocock/skills --list

npx -y skills@latest remove \
  ask-matt code-review codebase-design diagnosing-bugs domain-modeling \
  grill-with-docs implement improve-codebase-architecture prototype research \
  resolving-merge-conflicts setup-matt-pocock-skills tdd to-spec to-tickets \
  triage wayfinder grill-me grilling handoff teach writing-great-skills -y

npx -y skills@latest add mattpocock/skills \
  --skill ask-matt code-review codebase-design diagnosing-bugs domain-modeling \
  grill-with-docs implement improve-codebase-architecture prototype research \
  resolving-merge-conflicts setup-matt-pocock-skills tdd to-spec to-tickets \
  triage wayfinder grill-me grilling handoff teach writing-great-skills \
  --agent codex --copy -y

npx -y skills@latest add softaworks/agent-toolkit \
  --skill mermaid-diagrams --agent codex --copy -y

npx -y skills@latest add K-Dense-AI/claude-scientific-skills \
  --skill markdown-mermaid-writing --agent codex --copy -y
```

## Usage Rules For This Project

- Use OpenSpec for non-trivial capability changes: create proposal, design, specs, and tasks before implementation.
- Use `domain-modeling` when new concepts appear; update `CONTEXT.md` before spreading new names across docs or code.
- Use `grill-with-docs` during requirement clarification, but record unresolved decisions in docs instead of blocking low-risk progress.
- Use `to-spec` and `to-tickets` to turn an agreed design into independently executable work; do not use the removed `to-prd` or `to-issues` names.
- Use `implement` only when a spec or ticket is ready, and keep its `tdd` and `code-review` closeout loops intact.
- Use `codebase-design` when introducing service/module skeletons or changing dependency direction.
- Use Mermaid diagrams for architecture, sequence, state machine, context map, and data model docs.
- Do not treat installed skills as authoritative over source code, tests, config, or current user instructions.

## Current OpenSpec Change

Active change:

```text
openspec/changes/refine-switchback-platform-design
```

It defines five initial capabilities:

- `interaction-assistant`
- `task-worker-runtime`
- `knowledge-evolution`
- `platform-eventing-delivery`
- `model-and-capability-governance`

## Local Runtime Notes

On 2026-07-16, this workstation had `codex-cli 0.144.1` and Docker client 29.6.1 available in `PATH`. The Docker daemon was not running, so local Worker container execution still requires starting Docker Desktop or another Docker-compatible runtime.
