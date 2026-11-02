import { cn } from '@/lib/utils';
import type { ReactNode } from 'react';

export const taskStatusMeta: Record<string, { label: string; cls: string }> = {
  queued: { label: '排队中', cls: 'bg-slate-500/15 text-slate-300 border-slate-500/30' },
  running: { label: '运行中', cls: 'bg-teal-500/15 text-teal-300 border-teal-500/30' },
  waiting_user: { label: '等待用户', cls: 'bg-amber-500/15 text-amber-300 border-amber-500/30' },
  delivered: { label: '已交付', cls: 'bg-sky-500/15 text-sky-300 border-sky-500/30' },
  completed: { label: '已完成', cls: 'bg-emerald-500/15 text-emerald-300 border-emerald-500/30' },
  failed: { label: '失败', cls: 'bg-red-500/15 text-red-300 border-red-500/30' },
  cancelled: { label: '已取消', cls: 'bg-zinc-500/15 text-zinc-400 border-zinc-500/30' },
};

export const sessionStatusMeta: Record<string, { label: string; cls: string; pulse?: boolean }> = {
  allocating: { label: '分配中', cls: 'bg-slate-500/15 text-slate-300 border-slate-500/30', pulse: true },
  interactive: { label: '交互中', cls: 'bg-teal-500/15 text-teal-300 border-teal-500/30', pulse: true },
  running: { label: '运行中', cls: 'bg-teal-500/15 text-teal-300 border-teal-500/30', pulse: true },
  recovering: { label: '恢复中', cls: 'bg-orange-500/15 text-orange-300 border-orange-500/30', pulse: true },
  waiting_user: { label: '等待用户', cls: 'bg-amber-500/15 text-amber-300 border-amber-500/30' },
  idle: { label: '空闲', cls: 'bg-sky-500/15 text-sky-300 border-sky-500/30' },
  lost: { label: '失联', cls: 'bg-red-500/15 text-red-300 border-red-500/30', pulse: true },
  closed: { label: '已关闭', cls: 'bg-zinc-500/15 text-zinc-400 border-zinc-500/30' },
  expired: { label: '已过期', cls: 'bg-zinc-500/15 text-zinc-400 border-zinc-500/30' },
};

export function StatusBadge({ meta, value, className }: { meta: Record<string, { label: string; cls: string; pulse?: boolean }>; value: string; className?: string }) {
  const m = meta[value] ?? { label: value, cls: 'bg-slate-500/15 text-slate-300 border-slate-500/30' };
  return (
    <span className={cn('inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] font-medium leading-4', m.cls, className)}>
      {m.pulse && <span className="pulse-dot size-1.5 rounded-full bg-current" />}
      {m.label}
    </span>
  );
}

export function Card({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('rounded-xl border border-border bg-card', className)}>{children}</div>;
}

export function SectionTitle({ title, sub, right }: { title: string; sub?: string; right?: ReactNode }) {
  return (
    <div className="mb-3 flex items-end justify-between gap-4">
      <div>
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
        {sub && <p className="mt-0.5 text-xs text-muted-foreground">{sub}</p>}
      </div>
      {right}
    </div>
  );
}

export function KV({ k, v, mono }: { k: string; v: ReactNode; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-3 py-1.5 text-xs">
      <span className="shrink-0 text-muted-foreground">{k}</span>
      <span className={cn('text-right text-foreground/90', mono && 'font-mono text-[11px]')}>{v}</span>
    </div>
  );
}

export function Empty({ icon, text }: { icon?: ReactNode; text: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 py-12 text-muted-foreground">
      {icon}
      <p className="text-xs">{text}</p>
    </div>
  );
}

export const fmtTokens = (n: number) =>
  n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${(n / 1000).toFixed(1)}K` : `${n}`;
