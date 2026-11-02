import { useState } from 'react';
import { BrainCircuit, CheckCircle2, Database, Inbox, XCircle } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Card, SectionTitle } from '@/components/common';
import { memoryTruths } from '@/mock/data';
import type { MemoryTruth, TruthStatus } from '@/types/switchback';

const statusMeta: Record<TruthStatus, { label: string; cls: string }> = {
  candidate: { label: '候选', cls: 'bg-slate-500/15 text-slate-300 border-slate-500/30' },
  in_review: { label: '审核中', cls: 'bg-amber-500/15 text-amber-300 border-amber-500/30' },
  active: { label: '已生效', cls: 'bg-emerald-500/15 text-emerald-300 border-emerald-500/30' },
  rejected: { label: '已拒绝', cls: 'bg-red-500/15 text-red-300 border-red-500/30' },
  deprecated: { label: '已废弃', cls: 'bg-zinc-500/15 text-zinc-400 border-zinc-500/30' },
};

const scopeLabel = { user: '用户级', workspace: '工作区级', tenant: '租户级' } as const;

export default function MemoryView() {
  const [truths, setTruths] = useState<MemoryTruth[]>(memoryTruths);
  const [tab, setTab] = useState<'library' | 'review'>('library');

  const reviewList = truths.filter((t) => t.status === 'candidate' || t.status === 'in_review');
  const library = truths.filter((t) => t.status === 'active' || t.status === 'deprecated');

  const decide = (id: string, approve: boolean) =>
    setTruths((prev) =>
      prev.map((t) =>
        t.id === id
          ? { ...t, status: approve ? 'active' : 'rejected', projectedToViking: approve, version: t.version + 1, reviewNote: approve ? '人工审核通过，active revision 已投影到 OpenViking' : t.reviewNote ?? '人工审核拒绝' }
          : t,
      ),
    );

  return (
    <div className="mx-auto max-w-6xl px-6 py-6">
      <SectionTitle
        title="Memory 记忆治理"
        sub="Evolution 消费轨迹生成 ContextCandidate → RDBMS 治理修订/审核 → active Truth 投影 OpenViking · 可追溯来源"
      />

      <div className="mb-4 flex gap-1.5">
        <button onClick={() => setTab('library')} className={cn('flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs transition', tab === 'library' ? 'border-teal-500/50 bg-teal-500/10 text-teal-300' : 'border-border text-muted-foreground')}>
          <BrainCircuit className="size-3.5" /> MemoryLibrary（{library.length}）
        </button>
        <button onClick={() => setTab('review')} className={cn('flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs transition', tab === 'review' ? 'border-amber-500/50 bg-amber-500/10 text-amber-300' : 'border-border text-muted-foreground')}>
          <Inbox className="size-3.5" /> ReviewInbox（{reviewList.length}）
        </button>
      </div>

      <div className="space-y-3">
        {(tab === 'library' ? library : [...truths].filter((t) => reviewList.includes(t)).concat(truths.filter((t) => t.status === 'rejected'))).map((t) => (
          <Card key={t.id} className="p-4">
            <div className="flex flex-wrap items-center gap-2">
              <span className={cn('rounded-full border px-2 py-0.5 text-[11px] font-medium', statusMeta[t.status].cls)}>{statusMeta[t.status].label}</span>
              <span className="rounded-full border border-border px-2 py-0.5 text-[10px] text-muted-foreground">{scopeLabel[t.scope]}</span>
              <span className="font-mono text-[10px] text-muted-foreground">{t.id} · v{t.version} · 置信度 {t.confidence}</span>
              {t.projectedToViking && (
                <span className="flex items-center gap-1 rounded-full bg-violet-500/10 px-2 py-0.5 text-[10px] text-violet-300">
                  <Database className="size-3" /> 已投影 OpenViking
                </span>
              )}
              <span className="ml-auto text-[10px] text-muted-foreground">{t.updatedAt}</span>
            </div>
            <p className="mt-2 text-sm font-medium text-foreground">{t.title}</p>
            <p className="mt-1 text-[13px] leading-6 text-foreground/85">{t.statement}</p>
            <p className="mt-2 text-[10px] text-muted-foreground">
              来源：{t.source} · 证据：{t.evidenceRefs.map((e) => (
                <code key={e} className="mr-1.5 rounded bg-secondary px-1 py-0.5 font-mono">{e}</code>
              ))}
            </p>
            {t.reviewNote && <p className="mt-1.5 text-[11px] text-muted-foreground/80">审核备注:{t.reviewNote}</p>}
            {(t.status === 'candidate' || t.status === 'in_review') && (
              <div className="mt-3 flex gap-2">
                <button onClick={() => decide(t.id, true)} className="flex items-center gap-1.5 rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-semibold text-emerald-950 hover:bg-emerald-400">
                  <CheckCircle2 className="size-3.5" /> 批准生效
                </button>
                <button onClick={() => decide(t.id, false)} className="flex items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-xs text-foreground/80 hover:bg-accent">
                  <XCircle className="size-3.5" /> 拒绝
                </button>
                <span className="ml-auto self-center text-[10px] text-muted-foreground">批准生成新 ContentRevision 并投影；拒绝保留审计轨迹</span>
              </div>
            )}
          </Card>
        ))}
      </div>
    </div>
  );
}
