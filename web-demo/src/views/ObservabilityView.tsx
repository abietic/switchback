import { Activity, AlertTriangle, Gauge, Info, Radio } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Card, SectionTitle, fmtTokens } from '@/components/common';
import { modelStats } from '@/mock/data';
import { useEngine } from '@/mock/engine';

const levelCls = { info: 'text-teal-400', warn: 'text-amber-400', error: 'text-red-400' } as const;

export default function ObservabilityView() {
  const { events } = useEngine();
  const totalCost = modelStats.reduce((s, m) => s + m.costCny, 0);
  const totalReq = modelStats.reduce((s, m) => s + m.requests, 0);
  const maxCost = Math.max(...modelStats.map((m) => m.costCny));

  return (
    <div className="mx-auto max-w-6xl px-6 py-6">
      <SectionTitle
        title="可观测性"
        sub="LLMProxy 统一代理模型流量（路由/成本/审计/降级）· EventCenter 统一事件 · 看板从结构化增量投影读取，不扫原始 JSONL"
      />

      {/* 汇总卡片 */}
      <div className="mb-5 grid grid-cols-2 gap-3 md:grid-cols-4">
        {[
          ['今日模型请求', totalReq.toLocaleString(), 'Assistant + Worker 全量'],
          ['今日模型成本', `¥${totalCost.toFixed(1)}`, '按 LLMProxy metadata 归集'],
          ['Worker p95 延迟', '9.2s', 'k2.5-code · 正常区间'],
          ['事件投递成功率', '99.7%', 'EventCenter → Frontier / 飞书'],
        ].map(([k, v, sub]) => (
          <Card key={k} className="p-4">
            <p className="text-[11px] text-muted-foreground">{k}</p>
            <p className="mt-1 font-mono text-xl font-bold text-foreground">{v}</p>
            <p className="mt-0.5 text-[10px] text-muted-foreground">{sub}</p>
          </Card>
        ))}
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        {/* 模型成本 */}
        <Card className="p-4">
          <SectionTitle title="模型路由与成本" sub="路由基线：Assistant→k2-thinking · Worker Coding→k2.5-code · 异常降级→k2" />
          <div className="space-y-4">
            {modelStats.map((m) => (
              <div key={m.model}>
                <div className="flex items-baseline justify-between">
                  <span className="font-mono text-xs font-semibold text-teal-300">{m.model}</span>
                  <span className="font-mono text-[11px] text-muted-foreground">¥{m.costCny.toFixed(1)} · {m.requests.toLocaleString()} 次</span>
                </div>
                <div className="mt-1.5 h-2 overflow-hidden rounded-full bg-secondary">
                  <div className="h-full rounded-full bg-gradient-to-r from-teal-500 to-sky-500" style={{ width: `${(m.costCny / maxCost) * 100}%` }} />
                </div>
                <div className="mt-1 flex gap-4 text-[10px] text-muted-foreground">
                  <span>in {fmtTokens(m.inputTokens)} / out {fmtTokens(m.outputTokens)}</span>
                  <span>p95 {(m.p95LatencyMs / 1000).toFixed(1)}s</span>
                  <span className={cn(m.errorRate > 0.003 ? 'text-amber-400' : '')}>错误率 {(m.errorRate * 100).toFixed(1)}%</span>
                </div>
              </div>
            ))}
          </div>
          <p className="mt-4 flex items-start gap-1.5 rounded-lg border border-border bg-secondary/30 px-3 py-2 text-[10px] leading-4 text-muted-foreground">
            <Gauge className="mt-0.5 size-3 shrink-0" />
            LiteLLM co-process（P0）；LLMGateway 仅作未来可替换基础设施。trace 关联经 request metadata 完成。
          </p>
        </Card>

        {/* 事件流 */}
        <Card className="flex min-h-0 flex-col p-4">
          <SectionTitle
            title="EventCenter 事件流"
            sub="authorized public facts → Frontier projection / DeliveryCardProjection"
            right={<span className="flex items-center gap-1 text-[10px] text-teal-400"><Radio className="size-3" /> SSE live</span>}
          />
          <div className="max-h-[380px] space-y-1.5 overflow-y-auto pr-1">
            {events.map((e) => (
              <div key={e.id} className="fade-slide-in flex items-start gap-2.5 rounded-lg border border-border/70 bg-secondary/20 px-3 py-2">
                <span className={cn('mt-0.5', levelCls[e.level])}>
                  {e.level === 'info' ? <Activity className="size-3" /> : e.level === 'warn' ? <AlertTriangle className="size-3" /> : <AlertTriangle className="size-3" />}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="rounded bg-secondary px-1.5 py-0.5 font-mono text-[9px] text-foreground/70">{e.module}</span>
                    <code className="font-mono text-[10px] text-teal-300/90">{e.kind}</code>
                    <span className="ml-auto font-mono text-[9px] text-muted-foreground">{e.ts}</span>
                  </div>
                  <p className="mt-1 text-[11px] leading-4 text-foreground/80">{e.message}</p>
                </div>
              </div>
            ))}
          </div>
          <p className="mt-3 flex items-start gap-1.5 text-[10px] leading-4 text-muted-foreground">
            <Info className="mt-0.5 size-3 shrink-0" />
            EventCenter 不直接向浏览器输出：浏览器看到的是 Frontier projection 的 SSE 推送；本面板展示平台级事件总线视角。
          </p>
        </Card>
      </div>
    </div>
  );
}
