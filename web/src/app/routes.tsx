import { useEffect, useRef, useState } from "react";
import {
  createBrowserRouter,
  createMemoryRouter,
  matchRoutes,
  useNavigate,
  useParams,
  useLocation,
  useSearchParams,
  type NavigateFunction,
  type RouteObject,
} from "react-router";
import { assistantConversationPath, createAssistantConversation, newAssistantIdempotencyKey } from "../api/assistantCommands";
import { parseWorkerDiagnosticSelection, type WorkerDiagnosticSelection } from "../api/workerDiagnostics";
import {
  submitTaskCommand,
  submitTaskContinuation,
  type TaskCommandRequest,
  type TaskContinuationRequest,
} from "../api/taskCommands";
import {
  GatewayCommandError,
  isDefinitiveCommandFailure,
  submitWorkerCommand,
  type WorkerCommandRequest,
} from "../api/workerCommands";
import { ProductShell } from "../components/layout/ProductShell";
import {
  ViewBoundary,
  isViewMutationEnabled,
  viewBoundaryConnection,
} from "../components/ui/ViewBoundary";
import {
  ArtifactDetailPage,
} from "../features/artifact/ArtifactDetailPage";
import { AssistantConversationPage } from "../features/assistant/AssistantConversationPage";
import { HomePage } from "../features/home/HomePage";
import { ObservabilityPage } from "../features/observability/ObservabilityPage";
import { TasksPage } from "../features/tasks/TasksPage";
import { WorkerDetailPage } from "../features/worker/WorkerDetailPage";
import {
  taskContinuationScopeKey,
  taskDecisionScopeKeyFor,
  workerCommandScopeKey,
} from "../features/worker/useWorkerCommandAuthority";
import { productionViews } from "../projections/productionViewRegistry";
import { useFrontierView } from "../state/useFrontierView";
import { isSwitchbackRouteHandle, type SwitchbackRouteHandle } from "./routeHandles";

const handles = {
  home: { shellMode: "overview", title: "工作台" },
  observability: { shellMode: "overview", title: "可观测性" },
  tasks: { shellMode: "overview", title: "任务交付" },
  assistantStart: { shellMode: "overview", title: "Assistant" },
  assistant: { shellMode: "focused", title: "Assistant" },
  worker: { shellMode: "focused", title: "Worker" },
  artifact: { shellMode: "focused", title: "Artifact" },
} as const satisfies Record<string, SwitchbackRouteHandle>;

type AssistantCreationSettlement = "accepted" | "definitive_failure" | "ambiguous_failure";

export function settleAssistantCreationOperation(
  currentKey: string | undefined,
  operationKey: string,
  settlement: AssistantCreationSettlement,
): { apply: boolean; key: string | undefined } {
  if (currentKey !== operationKey) {
    return { apply: false, key: currentKey };
  }
  return {
    apply: true,
    key: settlement === "ambiguous_failure" ? currentKey : undefined,
  };
}

export const appRoutes: RouteObject[] = [{
  path: "/",
  element: <ProductShell />,
  children: [
    { index: true, element: <HomePage />, handle: handles.home },
    { path: "observability", element: <ObservabilityPage />, handle: handles.observability },
    { path: "tasks", element: <TaskRoute />, handle: handles.tasks },
    { path: "assistant", element: <AssistantStartRoute />, handle: handles.assistantStart },
    { path: "assistant/:conversationId", element: <AssistantRoute />, handle: handles.assistant },
    { path: "workers/:workerSessionId", element: <WorkerRoute />, handle: handles.worker },
    { path: "artifacts/:artifactId", element: <ArtifactRoute />, handle: handles.artifact },
  ],
}];

export function routeHandle(pathname: string): SwitchbackRouteHandle {
  const handle = matchRoutes(appRoutes, pathname)?.at(-1)?.route.handle;
  if (!isSwitchbackRouteHandle(handle)) {
    throw new Error(`No Switchback route handle for ${pathname}`);
  }
  return handle;
}

export function createAppRouter(initialEntries?: string[]) {
  if (initialEntries) {
    return createMemoryRouter(appRoutes, { initialEntries });
  }
  return createBrowserRouter(appRoutes);
}

function toNavigationHandler(navigate: NavigateFunction) {
  return (href: string) => {
    void navigate(href);
  };
}

function TaskRoute() {
  const navigate = useNavigate();
  const onNavigate = toNavigationHandler(navigate);
  const { replica, state, status, error, manualRetry } = useFrontierView(productionViews.taskIndex());
  const mutationsEnabled = isViewMutationEnabled({
    connection: state.connection,
    error,
    status,
    sync: state.sync,
  });
  const hasData = Boolean(state.data);
  return (
    <ViewBoundary
      connection={viewBoundaryConnection({
        connection: state.connection,
        hasData,
        status,
      })}
      error={error}
      hasData={hasData}
      label="Tasks"
      onRetry={manualRetry}
      sync={state.sync}
    >
      {state.data ? (
        <TasksPage
          data={state.data}
          isMutationAuthorized={() => {
            const current = replica.getState();
            return (
              mutationsEnabled
              && current.sync === "ready"
              && current.connection === "connected"
            );
          }}
          mutationsEnabled={mutationsEnabled}
          onNavigate={onNavigate}
        />
      ) : null}
    </ViewBoundary>
  );
}

function AssistantStartRoute() {
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string>();
  const idempotencyKeyRef = useRef<string | undefined>(undefined);
  const operationRef = useRef(0);
  const mountedRef = useRef(false);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      operationRef.current += 1;
    };
  }, []);

  const create = async () => {
    const operation = operationRef.current + 1;
    operationRef.current = operation;
    const idempotencyKey = idempotencyKeyRef.current ?? newAssistantIdempotencyKey();
    idempotencyKeyRef.current = idempotencyKey;
    setCreating(true);
    setError(undefined);
    try {
      const result = await createAssistantConversation(fetch, idempotencyKey);
      const path = assistantConversationPath(result);
      if (!mountedRef.current || operationRef.current !== operation) return;
      const settlement = settleAssistantCreationOperation(
        idempotencyKeyRef.current,
        idempotencyKey,
        "accepted",
      );
      if (!settlement.apply) return;
      idempotencyKeyRef.current = settlement.key;
      await navigate(path);
    } catch (cause) {
      if (!mountedRef.current || operationRef.current !== operation) return;
      const definitiveFailure =
        cause instanceof GatewayCommandError
        && cause.code !== "network_error"
        && cause.code !== "command_service_unavailable";
      const settlement = settleAssistantCreationOperation(
        idempotencyKeyRef.current,
        idempotencyKey,
        definitiveFailure ? "definitive_failure" : "ambiguous_failure",
      );
      if (!settlement.apply) return;
      idempotencyKeyRef.current = settlement.key;
      setError(cause instanceof Error ? cause.message : "Unable to create an Assistant conversation");
      setCreating(false);
    }
  };

  return (
    <main className="assistant-start">
      <div>
        <span>Assistant</span>
        <h2>把需求整理成可执行交付</h2>
        <p>打开此页面不会创建会话。由你明确开始后，Switchback 才提交创建命令。</p>
        <button className="primary-button" disabled={creating} onClick={() => void create()} type="button">
          {creating ? "正在创建…" : "开始 Assistant 对话"}
        </button>
        {error ? <p role="alert">{error}</p> : null}
      </div>
    </main>
  );
}

function AssistantRoute() {
  const { conversationId = "" } = useParams<"conversationId">();
  const navigate = useNavigate();
  const onNavigate = toNavigationHandler(navigate);
  const { replica, state, status, error, manualRetry } = useFrontierView(
    productionViews.assistant(conversationId),
  );
  const mutationsEnabled = isViewMutationEnabled({
    connection: state.connection,
    error,
    status,
    sync: state.sync,
  });
  const execute = async (
    action: string,
    clientCommandId: string,
    command: () => Promise<{ command_id: string }>,
  ) => {
    const current = replica.getState();
    if (
      !mutationsEnabled
      || current.sync !== "ready"
      || current.connection !== "connected"
    ) {
      throw new Error("Assistant mutations require a live authoritative projection");
    }
    await replica.submitCommand({
      clientCommandId,
      action,
      submit: command,
      isDefinitiveFailure: isDefinitiveCommandFailure,
    });
  };
  const hasData = Boolean(state.data);
  return (
    <ViewBoundary
      connection={viewBoundaryConnection({
        connection: state.connection,
        hasData,
        status,
      })}
      error={error}
      hasData={hasData}
      label="Assistant"
      onRetry={manualRetry}
      sync={state.sync}
    >
      {state.data ? (
        <AssistantConversationPage
          actions={state.availableActions}
          data={state.data}
          mutationsEnabled={mutationsEnabled}
          onCommand={execute}
          onNavigate={onNavigate}
          pendingCommands={state.pendingCommands}
          relatedRefs={state.relatedRefs}
        />
      ) : null}
    </ViewBoundary>
  );
}

function WorkerRoute() {
  const { workerSessionId = "" } = useParams<"workerSessionId">();
  const location = useLocation();
  const [searchParams, setSearchParams] = useSearchParams();
  const diagnosticSelection = parseWorkerDiagnosticSelection(location.search);
  const selectDiagnostics = (selection: WorkerDiagnosticSelection) => {
    const next = new URLSearchParams(searchParams);
    for (const key of ["artifact_id", "worker_run_id", "agent_turn_id"]) next.delete(key);
    if (selection.kind === "artifact") next.set("artifact_id", selection.artifactId);
    if (selection.kind === "exact") {
      next.set("worker_run_id", selection.runId);
      if (selection.turnId) next.set("agent_turn_id", selection.turnId);
    }
    setSearchParams(next);
  };
  const {
    replica,
    state,
    status,
    error,
    manualRetry,
    reconcileCommandStale,
  } = useFrontierView(productionViews.worker(workerSessionId));
  const worker = state.data;
  const mutationsEnabled = isViewMutationEnabled({
    connection: state.connection,
    error,
    status,
    sync: state.sync,
  });
  const requireAuthoritativeProjection = () => {
    const current = replica.getState();
    if (
      !worker
      || !mutationsEnabled
      || current.sync !== "ready"
      || current.connection !== "connected"
    ) {
      throw new Error("Worker mutations require a live authoritative projection");
    }
    return worker;
  };
  const execute = async (request: WorkerCommandRequest) => {
    requireAuthoritativeProjection();
    return replica.submitCommand({
      clientCommandId: request.idempotencyKey,
      action: request.action,
      scopeKey: workerCommandScopeKey(request),
      submit: () => submitWorkerCommand(fetch, request),
      isDefinitiveFailure: isDefinitiveCommandFailure,
    });
  };
  const executeTask = async (request: TaskCommandRequest) => {
    const authoritativeWorker = requireAuthoritativeProjection();
    return replica.submitCommand({
      clientCommandId: request.idempotencyKey,
      action: request.action,
      scopeKey: taskDecisionScopeKeyFor(
        request.action,
        request.taskId,
        authoritativeWorker.worker_session_id,
      ),
      submit: () => submitTaskCommand(fetch, request),
      isDefinitiveFailure: isDefinitiveCommandFailure,
    });
  };
  const executeTaskContinuation = async (request: TaskContinuationRequest) => {
    requireAuthoritativeProjection();
    return replica.submitCommand({
      clientCommandId: request.idempotencyKey,
      action: "task.continue",
      scopeKey: taskContinuationScopeKey(request),
      submit: () => submitTaskContinuation(fetch, request),
      isDefinitiveFailure: isDefinitiveCommandFailure,
    });
  };
  const hasData = Boolean(worker);
  return (
    <ViewBoundary
      connection={viewBoundaryConnection({
        connection: state.connection,
        hasData,
        status,
      })}
      error={error}
      hasData={hasData}
      label="Worker detail"
      onRetry={manualRetry}
      sync={state.sync}
    >
      {worker ? (
        <WorkerDetailPage
          key={workerSessionId}
          diagnosticSelection={diagnosticSelection}
          onDiagnosticSelectionChange={selectDiagnostics}
          availableActions={state.availableActions}
          mutationsEnabled={mutationsEnabled}
          onCommand={execute}
          onStale={reconcileCommandStale}
          onTaskCommand={executeTask}
          onTaskContinue={executeTaskContinuation}
          pendingCommands={state.pendingCommands}
          transient={state.transient}
          worker={worker}
        />
      ) : null}
    </ViewBoundary>
  );
}

function ArtifactRoute() {
  const { artifactId = "" } = useParams<"artifactId">();
  const { state, status, error, manualRetry } = useFrontierView(
    productionViews.artifact(artifactId),
  );
  const actionsEnabled = isViewMutationEnabled({
    connection: state.connection,
    error,
    status,
    sync: state.sync,
  });
  const hasData = Boolean(state.data);
  const boundaryConnection = viewBoundaryConnection({
    connection: state.connection,
    hasData,
    status,
  });
  return (
    <ViewBoundary
      connection={boundaryConnection}
      error={error}
      hasData={hasData}
      label="Artifact detail"
      onRetry={manualRetry}
      sync={state.sync}
    >
      {state.data ? (
        <ArtifactDetailPage
          actionStatus={actionsEnabled
            ? undefined
            : artifactActionStatus(state.sync, boundaryConnection)}
          artifact={state.data}
          availableActions={actionsEnabled ? state.availableActions : []}
        />
      ) : null}
    </ViewBoundary>
  );
}

function artifactActionStatus(sync: string, connection: string): string {
  if (sync === "client_upgrade_required") {
    return "Download unavailable until the client is updated.";
  }
  if (sync === "resync_required") {
    return "Download unavailable while Artifact authority resynchronizes.";
  }
  if (connection === "connecting") {
    return "Download unavailable while Artifact authority connects.";
  }
  if (connection === "reconnecting") {
    return "Download unavailable while Artifact authority reconnects.";
  }
  return "Download unavailable while Artifact authority is unavailable.";
}
