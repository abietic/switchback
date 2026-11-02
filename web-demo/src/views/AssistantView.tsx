import { useRef, useState } from 'react';
import { Link } from 'react-router';
import { CheckCircle2, CircleAlert, CircleHelp, FileCheck2, MinusCircle, SendHorizonal } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Card, SectionTitle } from '@/components/common';
import { workspaces } from '@/mock/data';
import type { ChatTurn, ReadinessDimension, ReadinessLevel } from '@/types/switchback';

const ts = () => new Date().toLocaleTimeString('zh-CN', { hour12: false });

const levelMeta: Record<ReadinessLevel, { label: string; cls: string; icon: React.ReactNode }> = {
  known: { label: 'known', cls: 'text-emerald-400', icon: <CheckCircle2 className="size-3.5" /> },
  assumed: { label: 'assumed', cls: 'text-sky-400', icon: <CircleHelp className="size-3.5" /> },
  blocking_unknown: { label: 'blocking', cls: 'text-amber-400', icon: <CircleAlert className="size-3.5" /> },
  not_applicable: { label: 'n/a', cls: 'text-zinc-500', icon: <MinusCircle className="size-3.5" /> },
};

const initialDims: ReadinessDimension[] = [
  { key: 'goal', label: '目标 goal', level: 'blocking_unknown', evidence: '等待用户描述需求' },
  { key: 'deliverable', label: '交付物/验收', level: 'blocking_unknown', evidence: '未明确 report / patch 与验收方式' },
  { key: 'scope', label: '范围 scope/target', level: 'blocking_unknown', evidence: '未指定仓库与模块边界' },
  { key: 'constraints', label: '约束 constraints', level: 'not_applicable', evidence: '—' },
  { key: 'context', label: '上下文/访问', level: 'blocking_unknown', evidence: 'Workspace 未解析（unresolved）' },
  { key: 'risk', label: '风险/审批', level: 'not_applicable', evidence: '—' },
  { key: 'exec', label: '执行策略', level: 'not_applicable', evidence: '—' },
];

interface QScript {
  question: string;
  applies: (answer: string, dims: ReadinessDimension[]) => ReadinessDimension[];
}

const questionScript: QScript[] = [
  {
    question: '收到。先确认最关键的一点：这个需求的最终交付物是什么？一份排查/分析报告（report），还是需要直接改动代码并交付补丁（patch）？验收标准是什么？',
    applies: (a, dims) =>
      dims.map((d) =>
        d.key === 'goal'
          ? { ...d, level: 'known' as const, evidence: `用户：「${a.slice(0, 40)}${a.length > 40 ? '…' : ''}」` }
          : d.key === 'deliverable'
            ? { ...d, level: 'known' as const, evidence: /patch|补丁|改代码|修复/.test(a) ? 'patch · manual acceptance（默认）' : 'report · 验收证据可确定时 auto' }
            : d,
      ),
  },
  {
    question: '明确。第二个问题：作用范围是哪个 Workspace / 仓库、哪个模块？请从右侧选择一个你已获授权的 Workspace —— 执行授权不能靠模型推断，必须显式选择。',
    applies: (a, dims) =>
      dims.map((d) =>
        d.key === 'scope'
          ? { ...d, level: 'known' as const, evidence: `用户指定：${a.slice(0, 50)}` }
          : d.key === 'context'
            ? { ...d, level: 'assumed' as const, evidence: '等待显式 Workspace 选择（右侧）' }
            : d,
      ),
  },
  {
    question: '最后一个问题：有没有硬约束或红线？比如禁止触达生产、禁止外部副作用、截止时间、必须通过的测试集等。没有的话回复"无"即可。',
    applies: (a, dims) =>
      dims.map((d) =>
        d.key === 'constraints'
          ? { ...d, level: 'known' as const, evidence: /无|没有/.test(a) ? '用户确认无额外硬约束' : `约束：${a.slice(0, 50)}` }
          : d.key === 'risk'
            ? { ...d, level: 'assumed' as const, evidence: '低风险可逆操作；外部副作用走显式 ApprovalGrant（default catalog）' }
            : d.key === 'exec'
              ? { ...d, level: 'assumed' as const, evidence: 'Worker：Codex(ACP) · local_standard_v1 · 单 Session 串行' }
              : d,
      ),
  },
];

let turnSeq = 0;
const nt = (t: Omit<ChatTurn, 'id' | 'ts'>): ChatTurn => ({ ...t, id: `turn-${++turnSeq}`, ts: ts() });

export default function AssistantView() {
  const [turns, setTurns] = useState<ChatTurn[]>([
    nt({
      role: 'assistant', kind: 'message',
      text: '你好，我是 Switchback Assistant。我负责把需求澄清成可执行的 Task Spec，再派发给 Worker 执行。请描述你的需求 —— 当前会话 Workspace 未解析，可以先脑暴；派发前必须显式选择 Workspace。',
    }),
  ]);
  const [dims, setDims] = useState<ReadinessDimension[]>(initialDims);
  const [qIndex, setQIndex] = useState(0);
  const [checkpointShown, setCheckpointShown] = useState(false);
  const [workspaceId, setWorkspaceId] = useState<string>('');
  const [dispatched, setDispatched] = useState(false);
  const [input, setInput] = useState('');
  const listRef = useRef<HTMLDivElement>(null);

  const blockingCount = dims.filter((d) => d.level === 'blocking_unknown').length;
  const ready = blockingCount === 0 && workspaceId;

  const scroll = () => setTimeout(() => listRef.current?.scrollTo({ top: 99999, behavior: 'smooth' }), 30);

  const send = () => {
    const text = input.trim();
    if (!text || dispatched) return;
    setInput('');
    const userTurn = nt({ role: 'user', kind: 'message', text });
    const next: ChatTurn[] = [userTurn];

    if (qIndex < questionScript.length) {
      const q = questionScript[qIndex];
      setDims((prev) => q.applies(text, prev));
      const nextQ = qIndex + 1;
      setQIndex(nextQ);
      if (nextQ < questionScript.length) {
        next.push(nt({ role: 'assistant', kind: 'question', text: questionScript[nextQ].question }));
      } else {
        // 三问完成 → ClarificationCheckpoint
        setCheckpointShown(true);
        next.push(
          nt({
            role: 'assistant', kind: 'checkpoint',
            text: '已连续澄清 3 个问题，按 TaskReadinessPolicy 生成 ClarificationCheckpoint 与当前 TaskDraft。你可以：接受假设并创建任务、继续澄清（重置三问预算）、缩小范围或取消。',
          }),
        );
      }
    } else {
      next.push(nt({ role: 'assistant', kind: 'message', text: '已记录补充信息并更新 TaskDraft 与 readiness 证据。（Demo 剧本：继续澄清不会影响已生成的草稿结构）' }));
    }
    setTurns((p) => [...p, ...next]);
    scroll();
  };

  const dispatch = () => {
    setDims((prev) =>
      prev.map((d) =>
        d.key === 'context'
          ? { ...d, level: 'known' as const, evidence: `显式选择 Workspace：${workspaces.find((w) => w.id === workspaceId)?.name}（含 WorkspaceContextResolution + Membership evidence）` }
          : d,
      ),
    );
    setDispatched(true);
    setTurns((p) => [
      ...p,
      nt({
        role: 'system', kind: 'dispatched',
        text: 'TaskSpec validator 通过、无 blocking_unknown、creation_intent_ref 已绑定 → Task 已创建并派发。Worker 正在分配 Session（Demo 复用 wks-a17f）。Assistant 不执行重型任务，进展将通过 EventCenter → Frontier SSE 实时回传。',
      }),
    ]);
    scroll();
  };

  const wsBlocked = dims.find((d) => d.key === 'context')?.level === 'blocking_unknown';

  return (
    <div className="mx-auto grid h-full max-w-6xl grid-cols-1 gap-4 px-6 py-6 lg:grid-cols-[1fr_340px]">
      {/* 对话区 */}
      <div className="flex min-h-[70vh] flex-col">
        <SectionTitle
          title="Assistant 澄清式对话"
          sub="每次只问一个最高优先级阻塞问题 · 三问后出 ClarificationCheckpoint · 不做重型执行"
        />
        <Card className="flex min-h-0 flex-1 flex-col">
          <div ref={listRef} className="flex-1 space-y-4 overflow-y-auto p-4" style={{ maxHeight: '56vh' }}>
            {turns.map((t) => (
              <div key={t.id} className={cn('fade-slide-in flex', t.role === 'user' ? 'justify-end' : 'justify-start')}>
                <div
                  className={cn(
                    'max-w-[85%] rounded-xl border px-3.5 py-2.5 text-[13px] leading-6',
                    t.role === 'user'
                      ? 'border-teal-500/30 bg-teal-500/10 text-teal-50'
                      : t.kind === 'checkpoint'
                        ? 'border-amber-500/30 bg-amber-500/8 text-amber-100'
                        : t.kind === 'dispatched'
                          ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-100'
                          : 'border-border bg-secondary/40 text-foreground/90',
                  )}
                >
                  {t.kind === 'question' && <p className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-teal-400">澄清问题 {Math.min(qIndex + 1, 3)}/3</p>}
                  {t.kind === 'checkpoint' && <p className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-amber-400">ClarificationCheckpoint</p>}
                  {t.kind === 'dispatched' && <p className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-emerald-400">已派发</p>}
                  {t.text}
                  {t.kind === 'dispatched' && (
                    <Link to="/workers/wks-a17f" className="mt-2 inline-block rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-semibold text-emerald-950 hover:bg-emerald-400">
                      打开 WorkerDetail（wks-a17f）→
                    </Link>
                  )}
                  <p className="mt-1 text-right font-mono text-[9px] text-muted-foreground/60">{t.ts}</p>
                </div>
              </div>
            ))}
          </div>

          {/* checkpoint 操作条 */}
          {checkpointShown && !dispatched && (
            <div className="border-t border-border bg-secondary/30 px-4 py-3">
              <div className="flex flex-wrap items-center gap-2">
                <button
                  onClick={dispatch}
                  disabled={!ready}
                  className={cn(
                    'rounded-md px-3.5 py-1.5 text-xs font-semibold transition',
                    ready ? 'bg-teal-500 text-teal-950 hover:bg-teal-400' : 'cursor-not-allowed bg-secondary text-muted-foreground',
                  )}
                >
                  接受假设并创建任务
                </button>
                <button onClick={() => { setCheckpointShown(false); setQIndex(0); setTurns((p) => [...p, nt({ role: 'assistant', kind: 'message', text: '好的，继续澄清。三问预算已重置 —— 请补充你还没讲清楚的部分。' })]); }} className="rounded-md border border-border px-3 py-1.5 text-xs text-foreground/80 hover:bg-accent">
                  继续澄清
                </button>
                <button className="rounded-md border border-border px-3 py-1.5 text-xs text-foreground/80 hover:bg-accent">缩小范围</button>
                <button className="rounded-md px-3 py-1.5 text-xs text-muted-foreground hover:text-red-300">取消</button>
                {!workspaceId && <span className="text-[10px] text-amber-300">需先在右侧显式选择 Workspace</span>}
              </div>
            </div>
          )}

          {/* 输入框 */}
          <div className="border-t border-border p-3">
            <div className="flex items-center gap-2">
              <input
                value={input}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && send()}
                placeholder={dispatched ? '任务已派发，可前往 WorkerDetail 继续交互' : '描述你的需求或回答澄清问题…'}
                disabled={dispatched}
                className="h-9 flex-1 rounded-lg border border-input bg-background px-3 text-[13px] outline-none placeholder:text-muted-foreground/60 focus:border-teal-500/50"
              />
              <button onClick={send} disabled={dispatched} className="flex size-9 items-center justify-center rounded-lg bg-teal-500 text-teal-950 transition hover:bg-teal-400 disabled:opacity-40">
                <SendHorizonal className="size-4" />
              </button>
            </div>
            <p className="mt-1.5 text-[10px] text-muted-foreground">
              消息经 Gateway 同步受理（accepted + conversation_id），Assistant 输出经 EventCenter → Frontier SSE 异步回传
            </p>
          </div>
        </Card>
      </div>

      {/* 右侧：Readiness + Workspace */}
      <div className="space-y-4">
        <Card className="p-4">
          <SectionTitle title="TaskReadinessAssessment" sub="版本化 TaskReadinessPolicy · v3" />
          <div className="space-y-2.5">
            {dims.map((d) => {
              const m = levelMeta[d.level];
              return (
                <div key={d.key} className="flex items-start gap-2">
                  <span className={cn('mt-0.5', m.cls)}>{m.icon}</span>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center justify-between">
                      <span className="text-xs font-medium text-foreground/90">{d.label}</span>
                      <span className={cn('font-mono text-[10px]', m.cls)}>{m.label}</span>
                    </div>
                    <p className="mt-0.5 truncate text-[10px] text-muted-foreground" title={d.evidence}>{d.evidence}</p>
                  </div>
                </div>
              );
            })}
          </div>
          <div className="mt-3 rounded-lg border border-border bg-secondary/30 px-3 py-2 text-[10px] leading-4 text-muted-foreground">
            只有 validator 通过且不存在 blocking_unknown 才能派发；权限 / 生产副作用 / 关键访问缺失不能靠默认假设绕过。
          </div>
        </Card>

        <Card className="p-4">
          <SectionTitle title="Workspace 解析" sub={wsBlocked ? '未解析：禁止读取 Workspace 资源 / 派发任务' : '派发前必须显式选择'} />
          <div className="space-y-2">
            {workspaces.map((w) => (
              <button
                key={w.id}
                onClick={() => setWorkspaceId(w.id)}
                className={cn(
                  'w-full rounded-lg border px-3 py-2 text-left transition',
                  workspaceId === w.id ? 'border-teal-500/60 bg-teal-500/10' : 'border-border bg-secondary/30 hover:border-teal-500/30',
                )}
              >
                <p className="text-xs font-medium text-foreground">{w.name}</p>
                <p className="mt-0.5 line-clamp-1 text-[10px] text-muted-foreground">{w.description}</p>
              </button>
            ))}
          </div>
          <p className="mt-2 flex items-start gap-1.5 text-[10px] leading-4 text-muted-foreground">
            <FileCheck2 className="mt-0.5 size-3 shrink-0" />
            全局默认 / 唯一候选 / 最近活动 / 模型推断只能用于推荐，不能形成执行授权。
          </p>
        </Card>
      </div>
    </div>
  );
}
