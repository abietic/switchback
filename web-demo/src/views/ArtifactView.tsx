import { Link, useParams } from 'react-router';
import { ArrowLeft, FileCode2, FileText, Fingerprint, HardDrive } from 'lucide-react';
import { Card, SectionTitle, KV } from '@/components/common';
import { DiffView, Markdown } from '@/components/Markdown';
import { artifacts } from '@/mock/engine';
import { workerSessions, tasks } from '@/mock/data';

export default function ArtifactView() {
  const { id } = useParams();
  const art = artifacts.find((a) => a.id === id) ?? artifacts[0];
  const session = workerSessions.find((s) => s.id === art.workerSessionId);
  const task = tasks.find((t) => t.id === art.taskId);

  return (
    <div className="mx-auto max-w-4xl px-6 py-6">
      <Link to={session ? `/workers/${session.id}` : '/'} className="mb-4 inline-flex items-center gap-1.5 text-xs text-teal-400 hover:underline">
        <ArrowLeft className="size-3" /> 返回 {session ? `Worker ${session.id}` : '工作台'}
      </Link>

      <Card className="mb-4 p-5">
        <div className="flex items-center gap-3">
          <div className="flex size-10 items-center justify-center rounded-lg bg-sky-500/15 text-sky-300">
            {art.kind === 'patch' ? <FileCode2 className="size-5" /> : <FileText className="size-5" />}
          </div>
          <div className="min-w-0">
            <h1 className="text-base font-semibold text-foreground">{art.title}</h1>
            <p className="font-mono text-[10px] text-muted-foreground">canonical ArtifactDetailView · {art.id}</p>
          </div>
        </div>
        <div className="mt-4 grid grid-cols-2 gap-x-6 rounded-lg border border-border bg-secondary/30 px-4 py-1 md:grid-cols-4">
          <KV k="digest" v={art.digest} mono />
          <KV k="大小" v={`${art.sizeBytes.toLocaleString()} B`} mono />
          <KV k="来源 Task" v={art.taskId ?? '—'} mono />
          <KV k="生命周期" v="hot（TraceManifest 管理，不自动物理删除）" />
        </div>
        {task && (
          <p className="mt-3 text-xs text-muted-foreground">
            关联：<Link className="text-teal-400 hover:underline" to="/tasks">{task.title}（{task.id}）</Link>
            {session && <> · <Link className="text-teal-400 hover:underline" to={`/workers/${session.id}`}>Worker {session.id}</Link></>}
          </p>
        )}
      </Card>

      <SectionTitle title={art.kind === 'patch' ? 'Patch 内容（unified diff）' : '内容'} sub="Artifact 在 Task/Worker/Workspace 中仅嵌入摘要，统一跳转到此 canonical 视图" />
      <Card className="p-5">
        {art.kind === 'patch' ? <DiffView diff={art.content} /> : art.kind === 'report' ? <Markdown text={art.content} /> : (
          <pre className="overflow-x-auto rounded-lg bg-black/40 p-3 font-mono text-[11px] text-foreground/80">{art.content}</pre>
        )}
      </Card>

      <div className="mt-4 flex items-center gap-4 text-[10px] text-muted-foreground">
        <span className="flex items-center gap-1"><Fingerprint className="size-3" /> 内容寻址 digest，权限过滤后可见</span>
        <span className="flex items-center gap-1"><HardDrive className="size-3" /> 对象存储：P0 本地 ObjectStore adapter</span>
      </div>
    </div>
  );
}
