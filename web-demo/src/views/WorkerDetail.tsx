import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useParams } from 'react-router';
import {
  Activity,
  CircleStop,
  Cpu,
  FileCode2,
  FileText,
  HeartPulse,
  Layers,
  ListOrdered,
  Network,
  ScrollText,
  SendHorizonal,
  Zap,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { KV, SectionTitle, StatusBadge, fmtTokens, sessionStatusMeta, taskStatusMeta } from '@/components/common';
import { Timeline } from '@/components/DisplayParts';
import { DiffView, Markdown } from '@/components/Markdown';
import {
  artifacts,
  interruptWorker,
  sendWorkerPrompt,
  startA17fScript,
  startHeartbeat,
  steerWorker,
  useEngine,
} from '@/mock/engine';

type RightTab = 'report' | 'patch' | 'trace' | 'info';

export default function WorkerDetail() {
  const { id } = useParams();
  const { sessions, tasks } = useEngine();
  const session = sessions.find((s) => s.id === id) ?? sessions[0];
  const task = tasks.find((t) => t.id === session.boundTaskId);

  const [tab, setTab] = useState<RightTab>('info');
  const [draft, setDraft] = useState('');
  const [mode, setMode] = useState<'prompt' | 'steer'>('prompt');
  const timelineRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    startHeartbeat();
    if (session.id === 'wks-a17f') startA17fScript();
  }, [session.id]);

  useEffect(() => {
    timelineRef.current?.scrollTo({ top: 999999, behavior: 'smooth' });
  }, [session.parts.length]);

  const report = artifacts.find((a) => a.id === session.reportArtifactId) ?? artifacts.find((a) => a.taskId === session.boundTaskId && a.kind === 'report');
  const patch = artifacts.find((a) => a.id === session.patchArtifactId) ?? artifacts.find((a) => a.taskId === session.boundTaskId && a.kind === 'patch');

  const running = session.status === 'running';
  const queued = session.queuedPrompts;

  const idlePct = useMemo(() => {
    // demo：根据 lastActiveAt 模拟一个剩余比例
    const map: Record<string, number> = { 'wks-a17f': 0.94, 'wks-b208': 0.62, 'wks-c3d9': 0.78, 'wks-d41e': 0 };
    return map[session.id] ?? 0.5;
  }, [session.id]);

  const send = () => {
    const text = draft.trim();
    if (!text) return;
    setDraft('');
    if (mode === 'steer' && running) steerWorker(session.id, text);
    else sendWorkerPrompt(session.id, text);
  };

  return (
    <div className="flex h-full flex-col">
      {/* 头部状态条 */}
      <header className="border-b border-border bg-card/60 px-6 py-3">
        <div className="flex flex-wrap items-center gap-2.5">
          <StatusBadge meta={sessionStatusMeta} value={session.status} />
          <h1 className="text-sm font-semibold text-foreground">{session.title}</h1>
          <span className="font-mono text-[11px] text-muted-foreground">{session.id}</span>
          {task && (
            <Link to="/tasks" className="flex items-center gap-1.5">
              <StatusBadge meta={taskStatusMeta} value={task.status} />
              <span className="font-mono text-[10px] text-muted-foreground hover:text-teal-300">{task.id} rev{task.revision}</span>
            </Link>
          )}
          <div className="ml-auto flex items-center gap-4 font-mono text-[10px] text-muted-foreground">
            <span className="flex items-center gap-1"><Layers className="size-3" /> gen {session.runtimeGeneration} · {session.containerId}</span>
            <span className="flex items-center gap-1">
              <HeartPulse className={cn('size-3', running ? 'text-teal-400' : 'text-muted-foreground')} />
              {session.heartbeatAgeSec < 0 ? '—' : `#${session.heartbeatSeq} · ${session.heartbeatAgeSec}s 前`}
            </span>
          </div>
        </div>
      </header>

      <div className="grid min-h-0 flex-1 grid-cols-1 lg:grid-cols-[1fr_400px]">
        {/* 左：timeline + composer */}
        <div className="flex min-h-0 flex-col border-r border-border">
          <div ref={timelineRef} className="min-h-0 flex-1 space-y-0 overflow-y-auto px-6 py-4">
            <p className="mb-3 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-muted-foreground">
              <Activity className="size-3" /> Display 双 lane：durable checkpoint 推进 ViewCursor · transient delta 只做低延迟拼接
            </p>
            <Timeline parts={session.parts} sessionId={session.id} running={running} />
          </div>

          {/* 排队消息 */}
          {queued.length > 0 && (
            <div className="border-t border-border bg-secondary/30 px-6 py-2">
              <p className="flex items-center gap-1.5 text-[10px] font-medium text-muted-foreground">
                <ListOrdered className="size-3" /> 已排队（当前 AgentTurn 结束后注入）
              </p>
              {queued.map((q, i) => (
                <p key={i} className="mt-1 truncate text-xs text-foreground/70">· {q}</p>
              ))}
            </div>
          )}

          {/* composer */}
          <div className="border-t border-border bg-card/80 p-3">
            <div className="mb-2 flex items-center gap-1.5 text-[11px]">
              <button
                onClick={() => setMode('prompt')}
                className={cn('rounded-full border px-2.5 py-0.5 transition', mode === 'prompt' ? 'border-teal-500/50 bg-teal-500/10 text-teal-300' : 'border-border text-muted-foreground')}
              >
                Prompt
              </button>
              <button
                onClick={() => setMode('steer')}
                className={cn('flex items-center gap-1 rounded-full border px-2.5 py-0.5 transition', mode === 'steer' ? 'border-violet-500/50 bg-violet-500/10 text-violet-300' : 'border-border text-muted-foreground')}
              >
                <Zap className="size-3" /> Steer
              </button>
              <span className="ml-1 text-[10px] text-muted-foreground">
                {running ? '运行中：prompt 将排队，steer 立即注入当前 Turn' : '直接进入新 AgentTurn，不经过 Assistant 澄清'}
              </span>
              <button
                onClick={() => interruptWorker(session.id)}
                disabled={!running}
                className="ml-auto flex items-center gap-1 rounded-md border border-red-500/40 px-2.5 py-1 text-[11px] text-red-300 transition hover:bg-red-500/10 disabled:opacity-30"
              >
                <CircleStop className="size-3" /> Interrupt
              </button>
            </div>
            <div className="flex items-center gap-2">
              <input
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && send()}
                placeholder={mode === 'steer' ? '向当前 AgentTurn 注入 steer 指令…' : '对 Worker 说话（prompt / 追加要求 / 反馈）…'}
                className="h-9 flex-1 rounded-lg border border-input bg-background px-3 text-[13px] outline-none placeholder:text-muted-foreground/60 focus:border-teal-500/50"
              />
              <button onClick={send} className="flex size-9 items-center justify-center rounded-lg bg-teal-500 text-teal-950 transition hover:bg-teal-400">
                <SendHorizonal className="size-4" />
              </button>
            </div>
          </div>
        </div>

        {/* 右：tabs */}
        <div className="flex min-h-0 flex-col bg-card/40">
          <div className="flex border-b border-border">
            {(
              [
                ['info', '会话', Activity],
                ['report', 'Report', FileText],
                ['patch', 'Patch', FileCode2],
                ['trace', 'Trace', ScrollText],
              ] as const
            ).map(([key, label, Icon]) => (
              <button
                key={key}
                onClick={() => setTab(key)}
                className={cn(
                  'flex flex-1 items-center justify-center gap-1.5 border-b-2 px-2 py-2.5 text-xs transition',
                  tab === key ? 'border-teal-400 text-teal-300' : 'border-transparent text-muted-foreground hover:text-foreground',
                )}
              >
                <Icon className="size-3.5" /> {label}
              </button>
            ))}
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto p-4">
            {tab === 'info' && (
              <div className="space-y-4">
                <div>
                  <SectionTitle title="运行时" />
                  <div className="rounded-lg border border-border bg-secondary/30 px-3 py-1">
                    <KV k="Agent" v={`${session.agent}`} />
                    <KV k="AgentProfile" v={session.agentProfile} mono />
                    <KV k="网络 Profile" v={session.networkProfile} mono />
                    <KV k="资源 Profile" v={session.resourceProfile} mono />
                  </div>
                </div>
                <div>
                  <SectionTitle title="生命周期预算" />
                  <div className="rounded-lg border border-border bg-secondary/30 p-3">
                    <div className="mb-1 flex justify-between text-[11px] text-muted-foreground">
                      <span>idle 回收（{session.idleBudgetHours}h）</span>
                      <span className="font-mono">{Math.round(idlePct * 100)}% 剩余</span>
                    </div>
                    <div className="h-1.5 overflow-hidden rounded-full bg-secondary">
                      <div className="h-full rounded-full bg-teal-500/80" style={{ width: `${idlePct * 100}%` }} />
                    </div>
                    <div className="mb-1 mt-3 flex justify-between text-[11px] text-muted-foreground">
                      <span>absolute 上限（{session.absoluteBudgetDays}d）</span>
                      <span className="font-mono">13.2d 剩余</span>
                    </div>
                    <div className="h-1.5 overflow-hidden rounded-full bg-secondary">
                      <div className="h-full rounded-full bg-sky-500/70" style={{ width: '94%' }} />
                    </div>
                    <p className="mt-2 text-[10px] leading-4 text-muted-foreground">
                      waiting_user 暂停 idle clock，但 24h AgentTurn deadline 与 14d absolute lifetime 不暂停。closed/expired 终态不可复活，续作需显式 continuation。
                    </p>
                  </div>
                </div>
                <div>
                  <SectionTitle title="成本（经 LLMProxy）" />
                  <div className="rounded-lg border border-border bg-secondary/30 px-3 py-1">
                    <KV k="Token 用量" v={fmtTokens(session.tokensUsed)} mono />
                    <KV k="累计成本" v={`¥${session.costCny.toFixed(2)}`} mono />
                    <KV k="模型路由" v="k2.5-code（Worker Coding 基线）" />
                  </div>
                </div>
                {task && (
                  <div>
                    <SectionTitle title="绑定 Task" />
                    <div className="rounded-lg border border-border bg-secondary/30 px-3 py-1">
                      <KV k="Task" v={task.id} mono />
                      <KV k="状态" v={<StatusBadge meta={taskStatusMeta} value={task.status} />} />
                      <KV k="验收" v={task.acceptanceMode === 'auto' ? 'auto（report-only 证据确定）' : 'manual（发起人验收）'} />
                      <KV k="Session 串行" v="单 active Run · queue head 持 dispatch permit" />
                    </div>
                  </div>
                )}
              </div>
            )}

            {tab === 'report' &&
              (report ? (
                <div>
                  <p className="mb-2 font-mono text-[10px] text-muted-foreground">{report.id} · {report.digest} · {report.sizeBytes} B</p>
                  <Markdown text={report.content} />
                </div>
              ) : (
                <EmptyTab text="尚无 report Artifact。report 通常在阶段结果或终态时发布。" />
              ))}

            {tab === 'patch' &&
              (patch ? (
                <div>
                  <p className="mb-2 font-mono text-[10px] text-muted-foreground">{patch.id} · {patch.digest}</p>
                  <DiffView diff={patch.content} />
                  <p className="mt-2 text-[10px] leading-4 text-muted-foreground">
                    P0 默认交付 patch 而非 branch/PR；应用前 Task 状态为 delivered，需发起人验收。
                  </p>
                </div>
              ) : (
                <EmptyTab text="尚无 patch Artifact。可在右侧对话中要求 Worker 交付补丁。" />
              ))}

            {tab === 'trace' && (
              <div>
                <p className="mb-2 flex items-center gap-1.5 text-[10px] text-muted-foreground">
                  <Cpu className="size-3" /> 权威 Trace JSONL（tee 自有界原始帧记录器，与 Langfuse 同步导出）
                </p>
                {session.trace.length === 0 ? (
                  <EmptyTab text="该会话 trace segment 已归档至对象存储（hot→warm）。" />
                ) : (
                  <div className="space-y-1.5">
                    {session.trace.map((f) => (
                      <div key={f.seq} className="rounded-md border border-border bg-black/30 px-2.5 py-1.5 font-mono text-[10px] leading-4">
                        <span className="text-muted-foreground">#{f.seq} {f.ts} </span>
                        <span className={cn(f.direction === 'out' ? 'text-teal-400' : f.direction === 'in' ? 'text-sky-400' : 'text-violet-400')}>
                          {f.direction === 'out' ? '→ acp' : f.direction === 'in' ? '← acp' : '· sys'}
                        </span>
                        <span className="text-foreground/60"> [{f.kind}]</span>
                        <p className="mt-0.5 break-all text-foreground/75">{f.payload}</p>
                      </div>
                    ))}
                  </div>
                )}
                <p className="mt-3 flex items-start gap-1.5 text-[10px] leading-4 text-muted-foreground">
                  <Network className="mt-0.5 size-3 shrink-0" />
                  完整 ACP/Agent frames 按 TracePolicy 写入对象存储 JSONL；transient 浏览器投递不降低证据保真度。
                </p>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

function EmptyTab({ text }: { text: string }) {
  return (
    <div className="flex h-32 items-center justify-center rounded-lg border border-dashed border-border text-[11px] text-muted-foreground">
      {text}
    </div>
  );
}
