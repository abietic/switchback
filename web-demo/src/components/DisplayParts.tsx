import { useMemo, useState } from 'react';
import {
  AlertTriangle,
  Brain,
  CheckCircle2,
  ChevronDown,
  FileCode2,
  FileText,
  Info,
  Loader2,
  ShieldQuestion,
  Wrench,
  XCircle,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import type { DisplayPart } from '@/types/switchback';
import { resolveApproval } from '@/mock/engine';
import { Link } from 'react-router';

/** DisplayPart v1 联合类型的渲染 registry（text/reasoning_summary/tool/activity/approval/artifact/source/error） */

function TextPart({ part }: { part: DisplayPart }) {
  const mine = part.text?.startsWith('【你】') || part.text?.startsWith('【返工反馈】');
  return (
    <div
      className={cn(
        'rounded-lg border px-3.5 py-2.5 text-[13px] leading-6',
        mine
          ? 'border-teal-500/30 bg-teal-500/10 text-teal-100'
          : 'border-border bg-secondary/40 text-foreground/90',
      )}
    >
      {part.text}
    </div>
  );
}

function ReasoningPart({ part }: { part: DisplayPart }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-lg border border-violet-500/20 bg-violet-500/5">
      <button
        onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-violet-300/90"
      >
        <Brain className="size-3.5" />
        <span className="font-medium">推理摘要</span>
        <span className="text-violet-300/50">（仅摘要，不含 chain-of-thought）</span>
        <ChevronDown className={cn('ml-auto size-3.5 transition-transform', open && 'rotate-180')} />
      </button>
      {open && <p className="border-t border-violet-500/15 px-3 py-2 text-xs leading-5 text-violet-200/80">{part.text}</p>}
    </div>
  );
}

function ToolPart({ part }: { part: DisplayPart }) {
  const [open, setOpen] = useState(part.toolState === 'running');
  const stateIcon =
    part.toolState === 'running' ? (
      <Loader2 className="size-3 animate-spin text-teal-400" />
    ) : part.toolState === 'failed' ? (
      <XCircle className="size-3 text-red-400" />
    ) : (
      <CheckCircle2 className="size-3 text-emerald-400" />
    );
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-[hsl(222_18%_8%)]">
      <button onClick={() => setOpen(!open)} className="flex w-full items-center gap-2 px-3 py-2 text-left">
        <Wrench className="size-3.5 text-muted-foreground" />
        <span className="font-mono text-xs text-teal-300">{part.toolName}</span>
        {stateIcon}
        <span className="truncate font-mono text-[11px] text-muted-foreground">{part.toolInput}</span>
        <ChevronDown className={cn('ml-auto size-3.5 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')} />
      </button>
      {open && (
        <div className="border-t border-border px-3 py-2">
          <div className="font-mono text-[11px] leading-5 text-muted-foreground">
            <span className="text-foreground/50">$ </span>
            {part.toolInput}
          </div>
          {part.toolOutput && (
            <pre className="mt-1.5 whitespace-pre-wrap rounded-md bg-black/30 p-2 font-mono text-[11px] leading-5 text-foreground/80">
              {part.toolOutput}
            </pre>
          )}
        </div>
      )}
    </div>
  );
}

function ActivityPart({ part }: { part: DisplayPart }) {
  return (
    <div className="flex items-center gap-2 px-1 text-[11px] text-muted-foreground">
      <Info className="size-3" />
      <span>{part.activityLabel}</span>
      <span className="text-muted-foreground/50">{part.timestamp}</span>
    </div>
  );
}

function ApprovalPart({ part, sessionId }: { part: DisplayPart; sessionId: string }) {
  const riskCls =
    part.approvalRisk === 'high'
      ? 'text-red-300 border-red-500/30 bg-red-500/10'
      : part.approvalRisk === 'medium'
        ? 'text-amber-300 border-amber-500/30 bg-amber-500/10'
        : 'text-emerald-300 border-emerald-500/30 bg-emerald-500/10';
  return (
    <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 p-3">
      <div className="flex items-center gap-2">
        <ShieldQuestion className="size-4 text-amber-300" />
        <span className="text-xs font-semibold text-amber-200">审批请求 · ApprovalGrant</span>
        <span className={cn('rounded-full border px-1.5 py-0.5 text-[10px]', riskCls)}>
          {part.approvalRisk === 'high' ? '高风险' : part.approvalRisk === 'medium' ? '中风险' : '低风险'}
        </span>
        <span className="ml-auto font-mono text-[10px] text-muted-foreground">{part.approvalId}</span>
      </div>
      <p className="mt-2 text-[13px] leading-6 text-foreground/90">{part.approvalAction}</p>
      {part.approvalState === 'pending' ? (
        <div className="mt-3 flex gap-2">
          <button
            onClick={() => resolveApproval(sessionId, part.approvalId!, true)}
            className="rounded-md bg-teal-500 px-3 py-1.5 text-xs font-semibold text-teal-950 transition hover:bg-teal-400"
          >
            批准执行
          </button>
          <button
            onClick={() => resolveApproval(sessionId, part.approvalId!, false)}
            className="rounded-md border border-border bg-secondary px-3 py-1.5 text-xs text-foreground/80 transition hover:bg-accent"
          >
            拒绝
          </button>
          <span className="ml-auto self-center text-[10px] text-muted-foreground">写操作将由 Gateway 重验权限 / revision / 幂等键</span>
        </div>
      ) : (
        <p className={cn('mt-2 text-xs font-medium', part.approvalState === 'approved' ? 'text-emerald-400' : 'text-red-400')}>
          {part.approvalState === 'approved' ? '✓ 已批准' : '✕ 已拒绝'}
        </p>
      )}
    </div>
  );
}

function ArtifactPart({ part }: { part: DisplayPart }) {
  return (
    <Link
      to={`/artifacts/${part.artifactRef}`}
      className="group flex items-center gap-3 rounded-lg border border-sky-500/25 bg-sky-500/5 px-3 py-2.5 transition hover:border-sky-400/50"
    >
      {part.artifactKind === 'patch' ? (
        <FileCode2 className="size-4 text-sky-300" />
      ) : (
        <FileText className="size-4 text-sky-300" />
      )}
      <div className="min-w-0">
        <p className="truncate text-[13px] font-medium text-sky-200 group-hover:underline">{part.artifactTitle}</p>
        <p className="font-mono text-[10px] text-muted-foreground">
          {part.artifactRef} · {part.artifactKind} · 点击打开 canonical Artifact 视图
        </p>
      </div>
    </Link>
  );
}

export function DisplayPartView({ part, sessionId }: { part: DisplayPart; sessionId: string }) {
  switch (part.type) {
    case 'text':
      return <TextPart part={part} />;
    case 'reasoning_summary':
      return <ReasoningPart part={part} />;
    case 'tool':
      return <ToolPart part={part} />;
    case 'activity':
      return <ActivityPart part={part} />;
    case 'approval':
      return <ApprovalPart part={part} sessionId={sessionId} />;
    case 'artifact':
      return <ArtifactPart part={part} />;
    case 'error':
      return (
        <div className="flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-200">
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
          {part.errorMessage}
        </div>
      );
    default:
      return (
        <div className="rounded-md border border-dashed border-border px-3 py-2 text-[11px] text-muted-foreground">
          未知 DisplayPart 类型（{(part as DisplayPart).type}）—— 按契约要求前端提示升级 renderer registry
        </div>
      );
  }
}

/** 时间线：durable parts 顺序渲染，运行中在尾部显示 transient 提示 */
export function Timeline({ parts, sessionId, running }: { parts: DisplayPart[]; sessionId: string; running: boolean }) {
  const grouped = useMemo(() => parts, [parts]);
  return (
    <div className="space-y-3">
      {grouped.map((p) =>
        p.type === 'activity' ? (
          <ActivityPart key={p.id} part={p} />
        ) : (
          <DisplayPartView key={p.id} part={p} sessionId={sessionId} />
        ),
      )}
      {running && (
        <div className="flex items-center gap-2 px-1 text-[11px] text-teal-400/80">
          <Loader2 className="size-3 animate-spin" />
          transient delta 传输中 · durable checkpoint 将在语义边界提交
          <span className="caret-blink inline-block h-3 w-1.5 bg-teal-400/80" />
        </div>
      )}
    </div>
  );
}
