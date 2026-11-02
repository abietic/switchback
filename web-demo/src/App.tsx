import { useEffect, type ReactNode } from 'react';
import { NavLink, Route, Routes } from 'react-router';
import {
  BotMessageSquare,
  Boxes,
  BrainCircuit,
  Gauge,
  Hexagon,
  LayoutDashboard,
  ListTodo,
  Sparkles,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { useEngine, startHeartbeat } from '@/mock/engine';
import Home from '@/views/Home';
import AssistantView from '@/views/AssistantView';
import TasksView from '@/views/TasksView';
import WorkerDetail from '@/views/WorkerDetail';
import ArtifactView from '@/views/ArtifactView';
import WorkspacesView from '@/views/WorkspacesView';
import MemoryView from '@/views/MemoryView';
import SkillsView from '@/views/SkillsView';
import ObservabilityView from '@/views/ObservabilityView';

const nav = [
  { to: '/', label: '工作台', icon: LayoutDashboard, end: true },
  { to: '/assistant', label: 'Assistant 对话', icon: BotMessageSquare },
  { to: '/tasks', label: '任务交付', icon: ListTodo },
  { to: '/workspaces', label: 'Workspaces', icon: Boxes },
  { to: '/memory', label: 'Memory 记忆', icon: BrainCircuit },
  { to: '/skills', label: 'Skills / MCP', icon: Sparkles },
  { to: '/observability', label: '可观测性', icon: Gauge },
];

function Shell({ children }: { children: ReactNode }) {
  const { sessions, tasks } = useEngine();
  useEffect(() => startHeartbeat(), []);
  const waiting = tasks.filter((t) => t.status === 'waiting_user' || t.status === 'delivered').length;
  const running = sessions.filter((s) => s.status === 'running').length;

  return (
    <div className="flex h-full">
      {/* 侧边导航 */}
      <aside className="flex w-52 shrink-0 flex-col border-r border-sidebar-border bg-sidebar-background">
        <div className="flex items-center gap-2.5 px-4 py-4">
          <div className="flex size-8 items-center justify-center rounded-lg bg-teal-500/15 text-teal-400">
            <Hexagon className="size-4.5" />
          </div>
          <div>
            <p className="text-sm font-bold tracking-wide text-foreground">SWITCHBACK</p>
            <p className="text-[10px] text-muted-foreground">端到端需求交付平台 · Demo</p>
          </div>
        </div>
        <nav className="mt-2 flex-1 space-y-0.5 px-2">
          {nav.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              end={n.end}
              className={({ isActive }) =>
                cn(
                  'flex items-center gap-2.5 rounded-lg px-3 py-2 text-[13px] transition-colors',
                  isActive
                    ? 'bg-teal-500/12 font-medium text-teal-300'
                    : 'text-sidebar-foreground hover:bg-sidebar-accent hover:text-foreground',
                )
              }
            >
              <n.icon className="size-4" />
              {n.label}
              {n.to === '/tasks' && waiting > 0 && (
                <span className="ml-auto rounded-full bg-amber-500/20 px-1.5 py-0.5 text-[10px] font-semibold text-amber-300">
                  {waiting}
                </span>
              )}
            </NavLink>
          ))}
        </nav>
        <div className="border-t border-sidebar-border px-4 py-3">
          <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
            <span className={cn('size-1.5 rounded-full', running > 0 ? 'pulse-dot bg-teal-400' : 'bg-zinc-500')} />
            {running > 0 ? `${running} 个 Worker 运行中` : '无运行中的 Worker'}
          </div>
          <p className="mt-1.5 truncate font-mono text-[10px] text-muted-foreground/60">SingleUserLocalProfile · 于海川</p>
        </div>
      </aside>
      <main className="min-w-0 flex-1 overflow-y-auto">{children}</main>
    </div>
  );
}

export default function App() {
  return (
    <Shell>
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/assistant" element={<AssistantView />} />
        <Route path="/tasks" element={<TasksView />} />
        <Route path="/workers/:id" element={<WorkerDetail />} />
        <Route path="/artifacts/:id" element={<ArtifactView />} />
        <Route path="/workspaces" element={<WorkspacesView />} />
        <Route path="/memory" element={<MemoryView />} />
        <Route path="/skills" element={<SkillsView />} />
        <Route path="/observability" element={<ObservabilityView />} />
      </Routes>
    </Shell>
  );
}
