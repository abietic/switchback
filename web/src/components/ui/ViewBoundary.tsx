import type { ReactNode } from "react";
import type {
  ReplicaConnectionState,
  ReplicaSyncState,
} from "../../state/clientViewReplica";
import type { FrontierViewStatus } from "../../state/frontierViewController";

interface ViewBoundaryProps {
  children?: ReactNode;
  connection?: ReplicaConnectionState;
  error?: string;
  hasData?: boolean;
  label: string;
  onRetry?: () => void | Promise<void>;
  sync?: ReplicaSyncState;
}

export function isViewInteractionBlocked(sync: ReplicaSyncState): boolean {
  return sync !== "ready";
}

export function isViewMutationEnabled({
  connection,
  error,
  status,
  sync,
}: {
  connection: ReplicaConnectionState;
  error?: string;
  status: FrontierViewStatus;
  sync: ReplicaSyncState;
}): boolean {
  return (
    !error
    && status === "live"
    && connection === "connected"
    && !isViewInteractionBlocked(sync)
  );
}

export function viewBoundaryConnection({
  connection,
  hasData,
  status,
}: {
  connection: ReplicaConnectionState;
  hasData: boolean;
  status: FrontierViewStatus;
}): ReplicaConnectionState {
  if (status === "live") return "connected";
  if (status === "connecting" || status === "reconnecting") {
    return hasData ? "reconnecting" : "connecting";
  }
  return connection;
}

export function ViewBoundary({
  children,
  connection = "connecting",
  error,
  hasData = false,
  label,
  onRetry,
  sync = "empty",
}: ViewBoundaryProps) {
  if (!hasData) {
    if (sync === "client_upgrade_required") {
      return <UpgradeNotice label={label} standalone />;
    }
    if (error) {
      return <ErrorNotice error={error} label={label} onRetry={onRetry} standalone />;
    }
    return <LoadingSkeleton label={label} />;
  }

  return (
    <>
      {sync === "client_upgrade_required" ? <UpgradeNotice label={label} /> : null}
      {sync === "resync_required" ? (
        <ViewNotice
          detail={`Trusted ${label} data remains visible, but mutations are suspended until a replacement snapshot arrives.`}
          label={`Refreshing authoritative ${label} state`}
        />
      ) : null}
      {sync === "ready" && connection === "reconnecting" && !error ? (
        <ViewNotice
          detail={`Trusted ${label} data remains visible while Frontier reconnects.`}
          label={`Reconnecting to live ${label} updates`}
        />
      ) : null}
      {error && sync !== "client_upgrade_required" ? (
        <ErrorNotice error={error} label={label} onRetry={onRetry} />
      ) : null}
      {children}
    </>
  );
}

function LoadingSkeleton({ label }: { label: string }) {
  return (
    <main
      aria-busy="true"
      aria-label={`Loading ${label}`}
      className="home-loading"
      role="status"
    >
      <span aria-hidden="true" className="home-skeleton home-skeleton-wide" />
      <span aria-hidden="true" className="home-skeleton" />
      <span aria-hidden="true" className="home-skeleton" />
      <span className="visually-hidden">Loading {label}</span>
    </main>
  );
}

function UpgradeNotice({ label, standalone = false }: { label: string; standalone?: boolean }) {
  const detail = `Reload Switchback with a compatible client before interacting with this ${label} projection.`;
  return standalone ? (
    <main className="view-state" role="alert">
      <strong>Client update required</strong>
      <p>{detail}</p>
    </main>
  ) : (
    <section className="home-connection-state home-connection-error" role="alert">
      <div>
        <strong>Client update required</strong>
        <span>{detail}</span>
      </div>
    </section>
  );
}

function ErrorNotice({
  error,
  label,
  onRetry,
  standalone = false,
}: {
  error: string;
  label: string;
  onRetry?: () => void | Promise<void>;
  standalone?: boolean;
}) {
  return standalone ? (
    <main className="view-state" role="alert">
      <strong>Unable to load {label}</strong>
      <p>{error}</p>
      {onRetry ? <RetryButton label={label} onRetry={onRetry} /> : null}
    </main>
  ) : (
    <section className="home-connection-state home-connection-error" role="alert">
      <div>
        <strong>Live {label} updates are unavailable</strong>
        <span>{error}</span>
      </div>
      {onRetry ? <RetryButton label={label} onRetry={onRetry} /> : null}
    </section>
  );
}

function RetryButton({
  label,
  onRetry,
}: {
  label: string;
  onRetry: () => void | Promise<void>;
}) {
  return (
    <button
      className="secondary-button"
      onClick={() => void Promise.resolve(onRetry()).catch(() => undefined)}
      type="button"
    >
      Retry {label}
    </button>
  );
}

function ViewNotice({ detail, label }: { detail: string; label: string }) {
  return (
    <section className="home-connection-state" role="status">
      <strong>{label}</strong>
      <span>{detail}</span>
    </section>
  );
}
