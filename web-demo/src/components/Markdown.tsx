import { useMemo, type ReactNode } from 'react';
import { cn } from '@/lib/utils';

/** 轻量 Markdown 渲染（demo 报告用）：标题/段落/列表/表格/围栏代码/行内样式 */

function inline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let rest = text;
  let k = 0;
  while (rest.length) {
    const m = rest.match(/(`[^`]+`)|(\*\*[^*]+\*\*)|(\\?[a-zA-Z0-9_\-./#]+\.(ts|go|md|json))/);
    if (!m || m.index === undefined) {
      out.push(rest);
      break;
    }
    if (m.index > 0) out.push(rest.slice(0, m.index));
    const tok = m[0];
    if (tok.startsWith('`')) {
      out.push(
        <code key={k++} className="rounded bg-secondary px-1 py-0.5 font-mono text-[11px] text-teal-300">
          {tok.slice(1, -1)}
        </code>,
      );
    } else if (tok.startsWith('**')) {
      out.push(
        <strong key={k++} className="font-semibold text-foreground">
          {tok.slice(2, -2)}
        </strong>,
      );
    } else {
      out.push(
        <code key={k++} className="font-mono text-[11px] text-sky-300">
          {tok.replace(/^\\/, '')}
        </code>,
      );
    }
    rest = rest.slice(m.index + tok.length);
  }
  return out;
}

export function Markdown({ text }: { text: string }) {
  const blocks = useMemo(() => {
    const lines = text.split('\n');
    const els: ReactNode[] = [];
    let i = 0;
    let k = 0;
    while (i < lines.length) {
      const line = lines[i];
      if (line.startsWith('```')) {
        const buf: string[] = [];
        i++;
        while (i < lines.length && !lines[i].startsWith('```')) buf.push(lines[i++]);
        i++;
        els.push(
          <pre key={k++} className="my-3 overflow-x-auto rounded-lg border border-border bg-black/40 p-3 font-mono text-[11px] leading-5 text-foreground/85">
            {buf.join('\n')}
          </pre>,
        );
        continue;
      }
      if (line.startsWith('### ')) {
        els.push(<h3 key={k++} className="mt-5 text-[13px] font-semibold text-foreground">{inline(line.slice(4))}</h3>);
      } else if (line.startsWith('## ')) {
        els.push(<h2 key={k++} className="mt-6 border-b border-border pb-1.5 text-sm font-semibold text-foreground">{inline(line.slice(3))}</h2>);
      } else if (line.startsWith('# ')) {
        els.push(<h1 key={k++} className="text-base font-bold text-foreground">{inline(line.slice(2))}</h1>);
      } else if (line.startsWith('|')) {
        const rows: string[][] = [];
        while (i < lines.length && lines[i].startsWith('|')) {
          const cells = lines[i].split('|').slice(1, -1).map((c) => c.trim());
          if (!cells.every((c) => /^:?-+:?$/.test(c))) rows.push(cells);
          i++;
        }
        els.push(
          <div key={k++} className="my-3 overflow-x-auto rounded-lg border border-border">
            <table className="w-full text-xs">
              <thead>
                <tr className="bg-secondary/60">
                  {rows[0]?.map((c, j) => (
                    <th key={j} className="px-3 py-2 text-left font-medium text-foreground/80">{inline(c)}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.slice(1).map((r, ri) => (
                  <tr key={ri} className="border-t border-border">
                    {r.map((c, j) => (
                      <td key={j} className="px-3 py-2 text-muted-foreground">{inline(c)}</td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>,
        );
        continue;
      } else if (/^[-*] /.test(line)) {
        const items: string[] = [];
        while (i < lines.length && /^[-*] /.test(lines[i])) items.push(lines[i++].slice(2));
        els.push(
          <ul key={k++} className="my-2 space-y-1.5">
            {items.map((it, j) => (
              <li key={j} className="flex gap-2 text-[13px] leading-6 text-foreground/85">
                <span className="mt-2.5 size-1 shrink-0 rounded-full bg-teal-400/70" />
                <span>{inline(it)}</span>
              </li>
            ))}
          </ul>,
        );
        continue;
      } else if (line.trim() === '') {
        // skip
      } else {
        els.push(<p key={k++} className="my-2.5 text-[13px] leading-6 text-foreground/85">{inline(line)}</p>);
      }
      i++;
    }
    return els;
  }, [text]);
  return <div>{blocks}</div>;
}

export function DiffView({ diff }: { diff: string }) {
  const lines = diff.split('\n');
  return (
    <div className="overflow-x-auto rounded-lg border border-border bg-black/40">
      <pre className="p-3 font-mono text-[11px] leading-5">
        {lines.map((l, i) => {
          let cls = 'text-foreground/70';
          if (l.startsWith('+++') || l.startsWith('---')) cls = 'text-muted-foreground';
          else if (l.startsWith('@@')) cls = 'diff-hunk';
          else if (l.startsWith('+')) cls = 'diff-add';
          else if (l.startsWith('-')) cls = 'diff-del';
          else if (l.startsWith('diff --git')) cls = 'text-sky-300 font-semibold';
          return (
            <div key={i} className={cn('px-1', cls)}>
              {l || ' '}
            </div>
          );
        })}
      </pre>
    </div>
  );
}
