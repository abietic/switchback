import { useSyncExternalStore } from 'react';
import type {
  DisplayPart,
  EventLogItem,
  TaskItem,
  WorkerSession,
} from '@/types/switchback';
import {
  artifacts,
  seedEvents,
  tasks as seedTasks,
  workerSessions as seedSessions,
} from '@/mock/data';

/**
 * MockFrontier —— 模拟 EventCenter → Frontier projection → SSE 的实时链路。
 * Demo 内所有"实时"行为（timeline 推进、审批、心跳、事件流）都由它驱动。
 */

interface EngineState {
  sessions: WorkerSession[];
  tasks: TaskItem[];
  events: EventLogItem[];
}

let state: EngineState = {
  sessions: seedSessions,
  tasks: seedTasks,
  events: seedEvents,
};

const listeners = new Set<() => void>();
function emit() {
  listeners.forEach((l) => l());
}
function setState(next: EngineState) {
  state = next;
  emit();
}

export function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
export function getState() {
  return state;
}
export function useEngine(): EngineState {
  return useSyncExternalStore(subscribe, getState);
}

let seq = 1000;
const nid = (p: string) => `${p}-${++seq}`;
const now = () =>
  new Date().toLocaleTimeString('zh-CN', { hour12: false });

function pushEvent(e: Omit<EventLogItem, 'id' | 'ts'>) {
  setState({
    ...state,
    events: [{ id: nid('ev'), ts: now(), ...e }, ...state.events].slice(0, 60),
  });
}

function updateSession(id: string, fn: (s: WorkerSession) => WorkerSession) {
  setState({
    ...state,
    sessions: state.sessions.map((s) => (s.id === id ? fn(s) : s)),
  });
}

function updateTask(id: string, fn: (t: TaskItem) => TaskItem) {
  setState({ ...state, tasks: state.tasks.map((t) => (t.id === id ? fn(t) : t)) });
}

function appendPart(sessionId: string, part: Omit<DisplayPart, 'id' | 'ordinal' | 'timestamp'>) {
  updateSession(sessionId, (s) => ({
    ...s,
    lastActiveAt: now(),
    parts: [
      ...s.parts,
      { ...part, id: nid('p'), ordinal: s.parts.length + 1, timestamp: now() },
    ],
  }));
}

// ---------------------------------------------------------------------------
// wks-a17f 的执行剧本：模拟 Codex 继续修复任务，最终在审批通过后交付 patch
// ---------------------------------------------------------------------------

interface ScriptStep {
  delayMs: number;
  run: () => void;
}

let a17fTimer: ReturnType<typeof setTimeout> | null = null;
let a17fStage = 0;

const a17fScript: ScriptStep[] = [
  {
    delayMs: 2500,
    run: () =>
      appendPart('wks-a17f', {
        type: 'tool', lane: 'durable', toolName: 'apply_patch', toolState: 'running',
        toolInput: 'internal/settlement/retry/idem.go',
        toolOutput: '写入 ON CONFLICT 幂等实现…',
      }),
  },
  {
    delayMs: 3000,
    run: () => {
      updateSession('wks-a17f', (s) => ({
        ...s,
        parts: s.parts.map((p) =>
          p.toolName === 'apply_patch' && p.toolState === 'running'
            ? { ...p, toolState: 'done' as const, toolOutput: '已应用 2 处修改：Claim 改用 ON CONFLICT DO NOTHING + RETURNING；新增并发回归测试。' }
            : p,
        ),
      }));
      pushEvent({ module: 'Worker', kind: 'tool.completed', message: 'apply_patch 完成（idem.go / idem_test.go）', level: 'info' });
    },
  },
  {
    delayMs: 1800,
    run: () =>
      appendPart('wks-a17f', {
        type: 'tool', lane: 'durable', toolName: 'shell', toolState: 'running',
        toolInput: 'go test ./internal/settlement/retry/... -race -count=1',
      }),
  },
  {
    delayMs: 4200,
    run: () => {
      updateSession('wks-a17f', (s) => ({
        ...s,
        parts: s.parts.map((p) =>
          p.toolInput?.startsWith('go test') && p.toolState === 'running'
            ? { ...p, toolState: 'done' as const, toolOutput: 'ok  github.local/pay/internal/settlement/retry  1.812s\nPASS: TestClaim_ConcurrentDuplicateNotify（-race 通过）' }
            : p,
        ),
      }));
      pushEvent({ module: 'Worker', kind: 'test.passed', message: 'settlement/retry 单测 -race 全部通过', level: 'info' });
    },
  },
  {
    delayMs: 1500,
    run: () =>
      appendPart('wks-a17f', {
        type: 'approval', lane: 'durable',
        approvalId: 'apv-302',
        approvalAction: '将修复编译进 ChangeSet 并发布 patch Artifact（art-patch-idem 草稿）',
        approvalRisk: 'low',
        approvalState: 'pending',
      }),
  },
];

export function startA17fScript() {
  if (a17fTimer) return;
  const step = () => {
    if (a17fStage >= a17fScript.length) return;
    a17fScript[a17fStage].run();
    a17fStage += 1;
    if (a17fStage < a17fScript.length) {
      a17fTimer = setTimeout(step, a17fScript[a17fStage].delayMs);
    }
  };
  a17fTimer = setTimeout(step, a17fScript[0].delayMs);
}

/** 审批决定：通过则继续交付剧本；拒绝则进入等待 */
export function resolveApproval(sessionId: string, approvalId: string, approved: boolean) {
  updateSession(sessionId, (s) => ({
    ...s,
    status: approved ? 'running' : 'waiting_user',
    parts: s.parts.map((p) =>
      p.approvalId === approvalId
        ? { ...p, approvalState: approved ? ('approved' as const) : ('rejected' as const) }
        : p,
    ),
  }));
  pushEvent({
    module: 'Worker',
    kind: approved ? 'approval.granted' : 'approval.rejected',
    message: `${approvalId} ${approved ? '已通过' : '已拒绝'}（${sessionId}）`,
    level: approved ? 'info' : 'warn',
  });

  if (!approved) {
    appendPart(sessionId, {
      type: 'text', lane: 'durable',
      text: '已收到拒绝决定，暂停该操作。你可以告诉我替代方案，或要求我换一种验证路径。',
    });
    if (sessionId === 'wks-c3d9') {
      updateTask('task-77e0', (t) => ({ ...t, summary: '审批被拒绝，等待进一步指示。', updatedAt: now() }));
    }
    return;
  }

  if (sessionId === 'wks-c3d9') {
    appendPart(sessionId, {
      type: 'tool', lane: 'durable', toolName: 'rocketmq-producer', toolState: 'done',
      toolInput: 'PUBLISH staging/RECON_DELAY_TEST tag=demo-probe',
      toolOutput: '消息已写入，告警链路 8s 内触发，EventCenter 事件 recon.delay.alert 已收到。',
    });
    appendPart(sessionId, {
      type: 'text', lane: 'durable',
      text: '端到端验证通过：测试消息 → 告警规则 → EventCenter → Frontier 推送全链路正常。接下来我会整理 patch 并交付。',
    });
    updateTask('task-77e0', (t) => ({ ...t, status: 'running', summary: '审批通过，端到端验证完成，整理交付中。', updatedAt: now() }));
    return;
  }

  // wks-a17f：发布 patch → delivered
  setTimeout(() => {
    appendPart(sessionId, {
      type: 'artifact', lane: 'durable',
      artifactRef: 'art-patch-idem', artifactKind: 'patch',
      artifactTitle: '重复入账修复 ChangeSet（2 个文件）',
    });
    pushEvent({ module: 'Worker', kind: 'artifact.published', message: 'art-patch-idem 已发布（2 个文件）', level: 'info' });
  }, 1200);
  setTimeout(() => {
    appendPart(sessionId, {
      type: 'text', lane: 'durable',
      text: '修复已完成：并发重复回调现在只有一个投递者能获得 claim，-race 回归测试通过。patch 已交付（task-9f31 → delivered），请验收；如需调整测试覆盖或补充迁移，直接告诉我。',
    });
    updateSession(sessionId, (s) => ({ ...s, status: 'idle', patchArtifactId: 'art-patch-idem' }));
    updateTask('task-9f31', (t) => ({
      ...t, status: 'delivered', revision: t.revision + 1, updatedAt: now(),
      summary: 'patch 已交付，等待验收。',
    }));
    pushEvent({ module: 'Task', kind: 'task.delivered', message: 'task-9f31 进入 delivered，等待发起人验收', level: 'info' });
  }, 2800);
}

/** Worker chat：prompt 立即进入 AgentTurn；运行中则排队 */
export function sendWorkerPrompt(sessionId: string, text: string) {
  const s = state.sessions.find((x) => x.id === sessionId);
  if (!s) return;
  if (s.status === 'running') {
    updateSession(sessionId, (cur) => ({ ...cur, queuedPrompts: [...cur.queuedPrompts, text] }));
    pushEvent({ module: 'Worker', kind: 'turn.queued', message: `消息已排队（${sessionId}），将在当前 AgentTurn 后注入`, level: 'info' });
    return;
  }
  updateSession(sessionId, (cur) => ({ ...cur, status: 'running' }));
  appendPart(sessionId, { type: 'text', lane: 'durable', text: `【你】${text}` });
  setTimeout(() => {
    appendPart(sessionId, {
      type: 'reasoning_summary', lane: 'durable',
      text: '收到新指令。先核对当前 WorkingCopy 状态与未提交变更，再按指令缩小执行范围。',
    });
  }, 1400);
  setTimeout(() => {
    appendPart(sessionId, {
      type: 'text', lane: 'durable',
      text: '（Demo 模拟）已按你的指令调整执行计划，相关变更会合并进当前 ChangeSet 并重新跑回归测试。',
    });
    updateSession(sessionId, (cur) => ({ ...cur, status: 'idle' }));
  }, 3600);
  pushEvent({ module: 'Worker', kind: 'turn.started', message: `新 AgentTurn 已开始（${sessionId}）`, level: 'info' });
}

export function interruptWorker(sessionId: string) {
  updateSession(sessionId, (s) => ({ ...s, status: 'idle' }));
  appendPart(sessionId, {
    type: 'activity', lane: 'durable',
    activityLabel: '已中断当前 AgentTurn（cancelled:user_interrupt）· SessionProcess 不受影响',
  });
  pushEvent({ module: 'Worker', kind: 'turn.cancelled', message: `${sessionId} 当前 Turn 被用户中断`, level: 'warn' });
}

export function steerWorker(sessionId: string, text: string) {
  appendPart(sessionId, {
    type: 'activity', lane: 'durable',
    activityLabel: `Steer 已注入当前 AgentTurn：${text}`,
  });
  pushEvent({ module: 'Worker', kind: 'turn.steered', message: `steer 注入（${sessionId}）`, level: 'info' });
}

/** Task 验收 / 返工 */
export function acceptTask(taskId: string) {
  updateTask(taskId, (t) => ({ ...t, status: 'completed', revision: t.revision + 1, updatedAt: now(), summary: '发起人已验收通过。' }));
  pushEvent({ module: 'Task', kind: 'task.completed', message: `${taskId} 验收通过`, level: 'info' });
}
export function reworkTask(taskId: string, feedback: string) {
  const t = state.tasks.find((x) => x.id === taskId);
  updateTask(taskId, (cur) => ({ ...cur, status: 'running', revision: cur.revision + 1, updatedAt: now(), summary: `返工反馈：${feedback}` }));
  if (t?.workerSessionId) {
    updateSession(t.workerSessionId, (s) => ({ ...s, status: 'running' }));
    appendPart(t.workerSessionId, { type: 'text', lane: 'durable', text: `【返工反馈】${feedback}` });
  }
  pushEvent({ module: 'Task', kind: 'task.rework', message: `${taskId} 返工：新 WorkerRun 已创建`, level: 'warn' });
}

/** 心跳与成本节拍 */
let heartbeatStarted = false;
export function startHeartbeat() {
  if (heartbeatStarted) return;
  heartbeatStarted = true;
  setInterval(() => {
    setState({
      ...state,
      sessions: state.sessions.map((s) =>
        s.status === 'running' || s.status === 'waiting_user' || s.status === 'idle'
          ? {
              ...s,
              heartbeatSeq: s.heartbeatSeq + 1,
              heartbeatAgeSec: s.heartbeatAgeSec < 0 ? s.heartbeatAgeSec : Math.floor(Math.random() * 8) + 2,
              tokensUsed: s.status === 'running' ? s.tokensUsed + Math.floor(Math.random() * 900) : s.tokensUsed,
            }
          : s,
      ),
    });
  }, 5000);
}

export { artifacts };
