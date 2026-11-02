import { useState } from 'react';
import { Link } from 'react-router';
import { Boxes, FolderGit2 } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Card, KV, SectionTitle, StatusBadge, sessionStatusMeta } from '@/components/common';
import { artifacts, memoryTruths, workerSessions, workspaces } from '@/mock/data';

export default function WorkspacesView() {
  const [activeId, setActiveId] = useState(workspaces[0].id);
  const ws = workspaces.find((w) => w.id === activeId)!;
  const wsSessions = workerSessions.filter((s) => s.workspaceId === ws.id);
  const wsArtifacts = artifacts.filter((a) => a.workspaceName === ws.name);
  const wsTruths = memoryTruths.filter((t) => t.scope === 'workspace');

  return (
    <div className="mx-auto max-w-6xl px-6 py-6">
      <SectionTitle
        title="Workspaces"
        sub="聚合持久资源上下文：Repository 快照、Truth、Artifact 与活跃 Worker · 执行授权必须显式解析"
      />
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[280px_1fr]">
        <div className="space-y-2">
          {workspaces.map((w) => (
            <button
              key={w.id}
              onClick={() => setActiveId(w.id)}
              className={cn(
                'w-full rounded-xl border p-3.5 text-left transition',
                activeId === w.id ? 'border-teal-500/60 bg-teal-500/8' : 'border-border bg-card hover:border-teal-500/30',
              )}
            >
              <div className="flex items-center gap-2">
                <Boxes className="size-4 text-teal-400" />
                <span className="text-sm font-medium text-foreground">{w.name}</span>
                {w.activeWorkers > 0 && (
                  <span className="ml-auto rounded-full bg-teal-500/15 px-1.5 py-0.5 text-[10px] font-semibold text-teal-300">
                    {w.activeWorkers} 活跃
                  </span>
                )}
              </div>
              <p className="mt-1.5 line-clamp-2 text-[11px] leading-4 text-muted-foreground">{w.description}</p>
            </button>
          ))}
        </div>

        <div className="space-y-4">
          <Card className="p-4">
            <SectionTitle title={ws.name} sub={`更新于 ${ws.updatedAt}`} />
            <div className="rounded-lg border border-border bg-secondary/30 px-3 py-1">
              {ws.repos.map((r) => (
                <KV key={r} k="Repository（服务端 ref→SHA 快照）" v={<span className="flex items-center gap-1.5"><FolderGit2 className="size-3" />{r}</span>} mono />
              ))}
              <KV k="Truth / Artifact" v={`${ws.truthCount} 条 · ${ws.artifactCount} 个`} />
            </div>
          </Card>

          <Card className="p-4">
            <SectionTitle title="Worker Session" sub="同 Workspace 的交互执行会话" />
            {wsSessions.length === 0 ? (
              <p className="py-4 text-center text-xs text-muted-foreground">暂无会话</p>
            ) : (
              <div className="space-y-2">
                {wsSessions.map((s) => (
                  <Link key={s.id} to={`/workers/${s.id}`} className="flex items-center gap-3 rounded-lg border border-border px-3 py-2.5 transition hover:border-teal-500/40">
                    <StatusBadge meta={sessionStatusMeta} value={s.status} />
                    <span className="min-w-0 flex-1 truncate text-[13px] text-foreground">{s.title}</span>
                    <span className="font-mono text-[10px] text-muted-foreground">{s.id}</span>
                  </Link>
                ))}
              </div>
            )}
          </Card>

          <div className="grid gap-4 md:grid-cols-2">
            <Card className="p-4">
              <SectionTitle title="Artifact 摘要" sub="点击跳转 canonical 视图" />
              <div className="space-y-2">
                {wsArtifacts.length === 0 && <p className="py-3 text-center text-xs text-muted-foreground">暂无</p>}
                {wsArtifacts.map((a) => (
                  <Link key={a.id} to={`/artifacts/${a.id}`} className="block rounded-lg border border-border px-3 py-2 transition hover:border-sky-500/40">
                    <p className="truncate text-xs text-foreground">{a.title}</p>
                    <p className="font-mono text-[10px] text-muted-foreground">{a.kind} · {a.digest}</p>
                  </Link>
                ))}
              </div>
            </Card>
            <Card className="p-4">
              <SectionTitle title="Workspace Truth" sub="Evolution 治理，经 OpenViking 投影检索" />
              <div className="space-y-2">
                {wsTruths.map((t) => (
                  <div key={t.id} className="rounded-lg border border-border px-3 py-2">
                    <p className="text-xs font-medium text-foreground">{t.title}</p>
                    <p className="mt-0.5 line-clamp-2 text-[10px] leading-4 text-muted-foreground">{t.statement}</p>
                    <p className="mt-1 font-mono text-[9px] text-muted-foreground/70">v{t.version} · {t.status} · 置信度 {t.confidence}</p>
                  </div>
                ))}
              </div>
            </Card>
          </div>
        </div>
      </div>
    </div>
  );
}
