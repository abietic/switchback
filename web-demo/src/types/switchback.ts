// Switchback / Switchback 领域模型（Demo 版，与 REQUIREMTENTS.md / docs/contracts 对齐）

export type TaskStatus =
  | 'queued'
  | 'running'
  | 'waiting_user'
  | 'delivered'
  | 'completed'
  | 'failed'
  | 'cancelled';

export type WorkerSessionStatus =
  | 'allocating'
  | 'interactive'
  | 'running'
  | 'recovering'
  | 'waiting_user'
  | 'idle'
  | 'lost'
  | 'closed'
  | 'expired';

export type DisplayPartType =
  | 'text'
  | 'reasoning_summary'
  | 'tool'
  | 'activity'
  | 'approval'
  | 'artifact'
  | 'source'
  | 'error';

export type ReadinessLevel =
  | 'known'
  | 'assumed'
  | 'blocking_unknown'
  | 'not_applicable';

export type TruthStatus = 'candidate' | 'in_review' | 'active' | 'rejected' | 'deprecated';

export interface Workspace {
  id: string;
  name: string;
  repos: string[];
  description: string;
  truthCount: number;
  artifactCount: number;
  activeWorkers: number;
  updatedAt: string;
}

export interface TaskItem {
  id: string;
  title: string;
  status: TaskStatus;
  acceptanceMode: 'auto' | 'manual';
  workspaceId: string;
  workspaceName: string;
  workerSessionId?: string;
  initiator: string;
  revision: number;
  createdAt: string;
  updatedAt: string;
  deliverable: 'report' | 'patch';
  summary?: string;
}

export interface DisplayPart {
  id: string;
  ordinal: number;
  type: DisplayPartType;
  lane: 'durable' | 'transient';
  timestamp: string;
  // text / reasoning_summary
  text?: string;
  // tool
  toolName?: string;
  toolInput?: string;
  toolOutput?: string;
  toolState?: 'running' | 'done' | 'failed';
  // activity
  activityLabel?: string;
  // approval
  approvalId?: string;
  approvalAction?: string;
  approvalRisk?: 'low' | 'medium' | 'high';
  approvalState?: 'pending' | 'approved' | 'rejected';
  // artifact
  artifactRef?: string;
  artifactKind?: 'report' | 'patch' | 'log' | 'trace' | 'image';
  artifactTitle?: string;
  // error
  errorMessage?: string;
}

export interface TraceFrame {
  seq: number;
  ts: string;
  direction: 'in' | 'out' | 'sys';
  kind: string;
  payload: string;
}

export interface WorkerSession {
  id: string;
  title: string;
  status: WorkerSessionStatus;
  workspaceId: string;
  workspaceName: string;
  boundTaskId?: string;
  activeRunId?: string;
  runtimeGeneration: number;
  containerId: string;
  agent: string;
  agentProfile: string;
  networkProfile: string;
  resourceProfile: string;
  createdAt: string;
  lastActiveAt: string;
  idleBudgetHours: number; // 48
  absoluteBudgetDays: number; // 14
  heartbeatSeq: number;
  heartbeatAgeSec: number;
  tokensUsed: number;
  costCny: number;
  parts: DisplayPart[];
  trace: TraceFrame[];
  queuedPrompts: string[];
  reportArtifactId?: string;
  patchArtifactId?: string;
}

export interface ArtifactItem {
  id: string;
  kind: 'report' | 'patch' | 'log' | 'trace' | 'image';
  title: string;
  taskId?: string;
  workerSessionId?: string;
  workspaceName: string;
  createdAt: string;
  sizeBytes: number;
  digest: string;
  content: string; // markdown 或 diff
}

export interface MemoryTruth {
  id: string;
  scope: 'user' | 'workspace' | 'tenant';
  title: string;
  statement: string;
  status: TruthStatus;
  confidence: number;
  version: number;
  source: string;
  evidenceRefs: string[];
  projectedToViking: boolean;
  updatedAt: string;
  reviewNote?: string;
}

export interface SkillItem {
  id: string;
  name: string;
  version: string;
  latestVersion: string;
  kind: 'skill' | 'mcp';
  scope: 'preinstalled' | 'user' | 'workspace' | 'evolution';
  enabled: boolean;
  permissions: string[];
  description: string;
  installs: number;
  reviewState: 'approved' | 'pending_review' | 'builtin';
}

export interface ProxyModelStat {
  model: string;
  requests: number;
  inputTokens: number;
  outputTokens: number;
  costCny: number;
  p95LatencyMs: number;
  errorRate: number;
}

export interface EventLogItem {
  id: string;
  ts: string;
  module: 'Assistant' | 'Worker' | 'Task' | 'EventCenter' | 'Frontier' | 'Delivery' | 'LLMProxy' | 'Evolution';
  kind: string;
  message: string;
  level: 'info' | 'warn' | 'error';
}

export interface ChatTurn {
  id: string;
  role: 'user' | 'assistant' | 'system';
  kind: 'message' | 'question' | 'checkpoint' | 'taskdraft' | 'dispatched';
  text: string;
  ts: string;
}

export interface ReadinessDimension {
  key: string;
  label: string;
  level: ReadinessLevel;
  evidence: string;
}
