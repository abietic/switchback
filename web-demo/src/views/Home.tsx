import { Link } from 'react-router';
import { ArrowRight, BotMessageSquare, CircleCheck, Clock3, PackageOpen, ShieldQuestion } from 'lucide-react';
import { Card, SectionTitle, StatusBadge, fmtTokens, sessionStatusMeta, taskStatusMeta } from '@/components/common';
import { useEngine } from '@/mock/engine';
import { currentUser } from '@/mock/data';

export default function Home() {
  const { sessions, tasks } = useEngine();
  const waiting = tasks.filter((t) => t.status === 'waiting_user' || t.status === 'delivered');
  const recentSessions = [...sessions].slice(0, 4);
  const delivered = tasks.filter((t) => ['delivered', 'completed'].includes(t.status)).slice(0, 4);

  return (
    <div className="mx-auto max-w-6xl px-6 py-6">
      <header className="mb-6 flex items-end justify-between">
        <div>
          <h1 className="text-xl font-bold text-foreground">你好，{currentUser}</h1>
          <p className="mt-1 text-xs text-muted-foreground">
            P0 工作台 · 最近 Worker、待处理审批与近期交付。完整 ActionInbox 属于 P1 范围。
          </p>
        </div>
        <Link
          to="/assistant"
          className="flex items-center gap-2 rounded-lg bg-teal-500 px-4 py-2 text-[13px] font-semibold text-teal-950 transition hover:bg-teal-400"
        >
          <BotMessageSquare className="size-4" />
          发起新需求
        </Link>
      </header>

      {/* 待处理 */}
      <section className="mb-6">
        <SectionTitle title="需要你的处理" sub="waiting_user 与 delivered：审批、验收或返工" />
        {waiting.length === 0 ? (
          <Card className="p-4 text-xs text-muted-foreground">暂无待处理事项。</Card>
        ) : (
          <div className="grid gap-3 md:grid-cols-2">
            {waiting.map((t) => (
              <Card key={t.id} className="p-4">
                <div className="flex items-center gap-2">
                  <StatusBadge meta={taskStatusMeta} value={t.status} />
                  {t.status === 'waiting_user' ? (
                    <ShieldQuestion className="size-3.5 text-amber-300" />
                  ) : (
                    <CircleCheck className="size-3.5 text-sky-300" />
                  )}
                  <span className="font-mono text-[10px] text-muted-foreground">{t.id} · rev {t.revision}</span>
                </div>
                <Link to={t.workerSessionId ? `/workers/${t.workerSessionId}` : '/tasks'} className="mt-2 block text-sm font-medium text-foreground hover:text-teal-300">
                  {t.title}
                </Link>
                <p className="mt-1 line-clamp-2 text-xs leading-5 text-muted-foreground">{t.summary}</p>
                <div className="mt-3 flex items-center gap-2 text-[11px] text-muted-foreground">
                  <span>{t.workspaceName}</span>
                  <span className="ml-auto" />
                  <Link to={t.workerSessionId ? `/workers/${t.workerSessionId}` : '/tasks'} className="flex items-center gap-1 text-teal-400 hover:underline">
                    进入 Worker <ArrowRight className="size-3" />
                  </Link>
                </div>
              </Card>
            ))}
          </div>
        )}
      </section>

      {/* 最近 Worker */}
      <section className="mb-6">
        <SectionTitle
          title="最近 Worker Session"
          sub="identity = worker_session_id · 空闲 48h 回收 · 绝对存活 14 天"
          right={<Link to="/tasks" className="text-xs text-teal-400 hover:underline">全部任务</Link>}
        />
        <div className="grid gap-3 lg:grid-cols-2">
          {recentSessions.map((s) => (
            <Link key={s.id} to={`/workers/${s.id}`}>
              <Card className="h-full p-4 transition hover:border-teal-500/40">
                <div className="flex items-center gap-2">
                  <StatusBadge meta={sessionStatusMeta} value={s.status} />
                  <span className="font-mono text-[10px] text-muted-foreground">{s.id}</span>
                  <span className="ml-auto font-mono text-[10px] text-muted-foreground">gen {s.runtimeGeneration}</span>
                </div>
                <p className="mt-2 text-sm font-medium text-foreground">{s.title}</p>
                <div className="mt-3 grid grid-cols-3 gap-2 text-[11px] text-muted-foreground">
                  <span className="flex items-center gap-1"><Clock3 className="size-3" /> {s.lastActiveAt.slice(5)}</span>
                  <span>{s.workspaceName}</span>
                  <span className="text-right">{fmtTokens(s.tokensUsed)} tok · ¥{s.costCny.toFixed(2)}</span>
                </div>
              </Card>
            </Link>
          ))}
        </div>
      </section>

      {/* 近期交付 */}
      <section>
        <SectionTitle title="近期交付" sub="report / patch 是 P0 对用户承诺的任务产物" />
        <Card className="divide-y divide-border">
          {delivered.map((t) => (
            <div key={t.id} className="flex items-center gap-3 px-4 py-3">
              <PackageOpen className="size-4 shrink-0 text-teal-400" />
              <div className="min-w-0 flex-1">
                <p className="truncate text-[13px] text-foreground">{t.title}</p>
                <p className="font-mono text-[10px] text-muted-foreground">
                  {t.id} · {t.deliverable} · {t.acceptanceMode === 'auto' ? '自动验收' : '人工验收'} · {t.updatedAt.slice(5)}
                </p>
              </div>
              <StatusBadge meta={taskStatusMeta} value={t.status} />
              {t.workerSessionId && (
                <Link to={`/workers/${t.workerSessionId}`} className="text-xs text-teal-400 hover:underline">
                  Worker
                </Link>
              )}
            </div>
          ))}
        </Card>
      </section>
    </div>
  );
}
