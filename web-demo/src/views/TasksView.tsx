import { useMemo, useState } from 'react';
import { Link } from 'react-router';
import { ArrowUpRight, RotateCcw, CheckCheck, X } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Card, SectionTitle, StatusBadge, taskStatusMeta } from '@/components/common';
import { acceptTask, reworkTask, useEngine } from '@/mock/engine';
import type { TaskStatus } from '@/types/switchback';

const filters: { key: TaskStatus | 'all'; label: string }[] = [
  { key: 'all', label: '全部' },
  { key: 'queued', label: '排队中' },
  { key: 'running', label: '运行中' },
  { key: 'waiting_user', label: '等待用户' },
  { key: 'delivered', label: '已交付' },
  { key: 'completed', label: '已完成' },
  { key: 'failed', label: '失败' },
  { key: 'cancelled', label: '已取消' },
];

export default function TasksView() {
  const { tasks } = useEngine();
  const [filter, setFilter] = useState<TaskStatus | 'all'>('all');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [feedback, setFeedback] = useState('');

  const list = useMemo(
    () => (filter === 'all' ? tasks : tasks.filter((t) => t.status === filter)),
    [tasks, filter],
  );
  const selected = tasks.find((t) => t.id === selectedId);

  return (
    <div className="mx-auto max-w-6xl px-6 py-6">
      <SectionTitle
        title="任务交付跟踪"
        sub="Task 是轻量交付跟踪记录：状态机是唯一业务权威，TaskDetail 仅作摘要并深链到 WorkerDetail 主交互面"
      />

      {/* 过滤 */}
      <div className="mb-4 flex flex-wrap gap-1.5">
        {filters.map((f) => (
          <button
            key={f.key}
            onClick={() => setFilter(f.key)}
            className={cn(
              'rounded-full border px-3 py-1 text-xs transition',
              filter === f.key
                ? 'border-teal-500/50 bg-teal-500/10 text-teal-300'
                : 'border-border text-muted-foreground hover:border-teal-500/30 hover:text-foreground',
            )}
          >
            {f.label}
            <span className="ml-1.5 font-mono text-[10px] opacity-70">
              {f.key === 'all' ? tasks.length : tasks.filter((t) => t.status === f.key).length}
            </span>
          </button>
        ))}
      </div>

      <Card className="overflow-hidden">
        <table className="w-full text-left text-[13px]">
          <thead>
            <tr className="border-b border-border bg-secondary/40 text-[11px] text-muted-foreground">
              <th className="px-4 py-2.5 font-medium">任务</th>
              <th className="px-3 py-2.5 font-medium">状态</th>
              <th className="px-3 py-2.5 font-medium">Workspace</th>
              <th className="px-3 py-2.5 font-medium">交付物</th>
              <th className="px-3 py-2.5 font-medium">验收</th>
              <th className="px-3 py-2.5 font-medium">rev</th>
              <th className="px-4 py-2.5 text-right font-medium">Worker</th>
            </tr>
          </thead>
          <tbody>
            {list.map((t) => (
              <tr
                key={t.id}
                onClick={() => setSelectedId(t.id)}
                className={cn(
                  'cursor-pointer border-b border-border/60 transition last:border-0 hover:bg-secondary/30',
                  selectedId === t.id && 'bg-teal-500/5',
                )}
              >
                <td className="px-4 py-3">
                  <p className="font-medium text-foreground">{t.title}</p>
                  <p className="font-mono text-[10px] text-muted-foreground">{t.id} · {t.initiator} · {t.updatedAt.slice(5)}</p>
                </td>
                <td className="px-3 py-3"><StatusBadge meta={taskStatusMeta} value={t.status} /></td>
                <td className="px-3 py-3 text-xs text-muted-foreground">{t.workspaceName}</td>
                <td className="px-3 py-3 font-mono text-[11px] text-sky-300">{t.deliverable}</td>
                <td className="px-3 py-3 text-xs text-muted-foreground">{t.acceptanceMode === 'auto' ? '自动' : '人工'}</td>
                <td className="px-3 py-3 font-mono text-[11px] text-muted-foreground">{t.revision}</td>
                <td className="px-4 py-3 text-right">
                  {t.workerSessionId ? (
                    <Link
                      to={`/workers/${t.workerSessionId}`}
                      onClick={(e) => e.stopPropagation()}
                      className="inline-flex items-center gap-1 text-xs text-teal-400 hover:underline"
                    >
                      {t.workerSessionId} <ArrowUpRight className="size-3" />
                    </Link>
                  ) : (
                    <span className="text-[11px] text-muted-foreground">待分配</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>

      {/* TaskDetail 摘要抽屉 */}
      {selected && (
        <div className="fixed inset-0 z-40 flex justify-end bg-black/50" onClick={() => setSelectedId(null)}>
          <div className="h-full w-[420px] overflow-y-auto border-l border-border bg-card p-5" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between">
              <StatusBadge meta={taskStatusMeta} value={selected.status} />
              <button onClick={() => setSelectedId(null)} className="rounded-md p-1 text-muted-foreground hover:bg-accent">
                <X className="size-4" />
              </button>
            </div>
            <h2 className="mt-3 text-base font-semibold text-foreground">{selected.title}</h2>
            <p className="mt-1 font-mono text-[10px] text-muted-foreground">
              {selected.id} · rev {selected.revision} · {selected.acceptanceMode} acceptance
            </p>
            <p className="mt-3 text-[13px] leading-6 text-foreground/85">{selected.summary ?? '（无摘要）'}</p>

            <div className="mt-4 space-y-1 rounded-lg border border-border bg-secondary/30 px-3 py-2">
              <div className="flex justify-between py-1 text-xs"><span className="text-muted-foreground">Workspace</span><span>{selected.workspaceName}</span></div>
              <div className="flex justify-between py-1 text-xs"><span className="text-muted-foreground">发起人 / TaskInitiator</span><span>{selected.initiator}</span></div>
              <div className="flex justify-between py-1 text-xs"><span className="text-muted-foreground">当前 WorkerSession</span><span className="font-mono text-[11px]">{selected.workerSessionId ?? '—'}</span></div>
              <div className="flex justify-between py-1 text-xs"><span className="text-muted-foreground">创建 / 更新</span><span className="font-mono text-[11px]">{selected.createdAt.slice(5)} → {selected.updatedAt.slice(5)}</span></div>
            </div>

            {selected.workerSessionId && (
              <Link
                to={`/workers/${selected.workerSessionId}`}
                className="mt-4 flex items-center justify-center gap-1.5 rounded-lg bg-teal-500 px-4 py-2 text-[13px] font-semibold text-teal-950 hover:bg-teal-400"
              >
                打开 WorkerDetail 主交互面 <ArrowUpRight className="size-3.5" />
              </Link>
            )}

            {/* accept / rework（仅 delivered 且人工验收） */}
            {selected.status === 'delivered' && selected.acceptanceMode === 'manual' && (
              <div className="mt-4 rounded-lg border border-sky-500/25 bg-sky-500/5 p-3">
                <p className="text-xs font-semibold text-sky-200">验收决定（仅发起人可操作）</p>
                <textarea
                  value={feedback}
                  onChange={(e) => setFeedback(e.target.value)}
                  placeholder="返工反馈：说明需要调整的点…"
                  className="mt-2 h-16 w-full rounded-md border border-input bg-background p-2 text-xs outline-none focus:border-sky-500/50"
                />
                <div className="mt-2 flex gap-2">
                  <button
                    onClick={() => { acceptTask(selected.id); }}
                    className="flex items-center gap-1.5 rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-semibold text-emerald-950 hover:bg-emerald-400"
                  >
                    <CheckCheck className="size-3.5" /> 验收通过
                  </button>
                  <button
                    onClick={() => { if (feedback.trim()) { reworkTask(selected.id, feedback.trim()); setFeedback(''); } }}
                    className="flex items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-xs text-foreground/80 hover:bg-accent"
                  >
                    <RotateCcw className="size-3.5" /> 要求返工
                  </button>
                </div>
                <p className="mt-2 text-[10px] text-muted-foreground">命令携带 actor、expected revision 与幂等键；返工创建新 WorkerRun 并回到 running。</p>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
