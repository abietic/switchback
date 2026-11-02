import { useState } from 'react';
import { ArrowUpCircle, Plug, ShieldCheck, Sparkles } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Card, SectionTitle } from '@/components/common';
import { skills as seedSkills } from '@/mock/data';
import type { SkillItem } from '@/types/switchback';

const scopeMeta: Record<SkillItem['scope'], { label: string; cls: string }> = {
  preinstalled: { label: '预装', cls: 'bg-sky-500/15 text-sky-300 border-sky-500/30' },
  user: { label: '用户级', cls: 'bg-teal-500/15 text-teal-300 border-teal-500/30' },
  workspace: { label: '工作区级', cls: 'bg-violet-500/15 text-violet-300 border-violet-500/30' },
  evolution: { label: 'Evolution 生成', cls: 'bg-amber-500/15 text-amber-300 border-amber-500/30' },
};

export default function SkillsView() {
  const [skills, setSkills] = useState<SkillItem[]>(seedSkills);

  const toggle = (id: string) =>
    setSkills((prev) =>
      prev.map((s) => {
        if (s.id !== id) return s;
        if (s.scope === 'evolution' && s.reviewState === 'pending_review' && !s.enabled) return s; // 未审核不可启用
        return { ...s, enabled: !s.enabled };
      }),
    );

  const approveReview = (id: string) =>
    setSkills((prev) => prev.map((s) => (s.id === id ? { ...s, reviewState: 'approved' as const } : s)));

  return (
    <div className="mx-auto max-w-6xl px-6 py-6">
      <SectionTitle
        title="Skills / MCP 能力管理"
        sub="Skilling 连接 Skill Market · 权限/密钥/网络/数据范围显式声明 · 启用、禁用、升级、回滚与审计"
      />
      <div className="grid gap-3 md:grid-cols-2">
        {skills.map((s) => {
          const upgradable = s.version !== s.latestVersion;
          const pendingReview = s.reviewState === 'pending_review';
          return (
            <Card key={s.id} className={cn('p-4 transition', !s.enabled && 'opacity-70')}>
              <div className="flex items-center gap-2">
                <div className={cn('flex size-8 items-center justify-center rounded-lg', s.kind === 'mcp' ? 'bg-violet-500/15 text-violet-300' : 'bg-teal-500/15 text-teal-300')}>
                  {s.kind === 'mcp' ? <Plug className="size-4" /> : <Sparkles className="size-4" />}
                </div>
                <div className="min-w-0">
                  <p className="text-sm font-semibold text-foreground">{s.name}</p>
                  <p className="font-mono text-[10px] text-muted-foreground">
                    v{s.version}
                    {upgradable && <span className="text-amber-300"> → v{s.latestVersion} 可升级</span>}
                  </p>
                </div>
                <span className={cn('ml-auto rounded-full border px-2 py-0.5 text-[10px]', scopeMeta[s.scope].cls)}>{scopeMeta[s.scope].label}</span>
              </div>

              <p className="mt-2.5 text-xs leading-5 text-muted-foreground">{s.description}</p>

              <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
                <ShieldCheck className="size-3 text-muted-foreground" />
                {s.permissions.map((p) => (
                  <code key={p} className="rounded bg-secondary px-1.5 py-0.5 font-mono text-[10px] text-foreground/70">{p}</code>
                ))}
              </div>

              {pendingReview && (
                <div className="mt-3 rounded-lg border border-amber-500/30 bg-amber-500/8 px-3 py-2">
                  <p className="text-[11px] text-amber-200">Evolution 生成的 Skill 必须先完成人工内容/安全审核才能发布与启用。</p>
                  <button onClick={() => approveReview(s.id)} className="mt-2 rounded-md bg-amber-500 px-2.5 py-1 text-[11px] font-semibold text-amber-950 hover:bg-amber-400">
                    标记审核通过
                  </button>
                </div>
              )}

              <div className="mt-3 flex items-center gap-2 border-t border-border pt-3">
                <button
                  onClick={() => toggle(s.id)}
                  disabled={pendingReview && !s.enabled}
                  className={cn(
                    'relative h-5 w-9 rounded-full transition',
                    s.enabled ? 'bg-teal-500' : 'bg-secondary',
                    pendingReview && !s.enabled && 'cursor-not-allowed opacity-40',
                  )}
                >
                  <span className={cn('absolute top-0.5 size-4 rounded-full bg-white transition-all', s.enabled ? 'left-4.5' : 'left-0.5')} />
                </button>
                <span className="text-[11px] text-muted-foreground">{s.enabled ? '已启用' : '已禁用'}</span>
                {upgradable && (
                  <button className="flex items-center gap-1 rounded-md border border-amber-500/40 px-2 py-1 text-[10px] text-amber-300 hover:bg-amber-500/10">
                    <ArrowUpCircle className="size-3" /> 升级
                  </button>
                )}
                <span className="ml-auto text-[10px] text-muted-foreground">{s.installs} 次安装</span>
              </div>
            </Card>
          );
        })}
      </div>
      <p className="mt-4 text-[10px] leading-4 text-muted-foreground">
        非预装 Skill 需用户显式批准（对话中明确要求安装 = 可审计 CapabilityInstallGrant）；Evolution 生成 Skill 审核发布后在批准 scope 内无需重复确认，但仍受平台策略与来源检查约束。
      </p>
    </div>
  );
}
