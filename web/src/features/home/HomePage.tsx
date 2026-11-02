import {
  ArrowRight,
  BotMessageSquare,
  Boxes,
  Clock3,
  HardDrive,
  PackageOpen,
} from "lucide-react";
import { Link } from "react-router";
import { StatusBadge } from "../../components/ui/StatusBadge";
import { ViewBoundary, viewBoundaryConnection } from "../../components/ui/ViewBoundary";
import { productionViews } from "../../projections/productionViewRegistry";
import { useFrontierView } from "../../state/useFrontierView";
import { adaptHome } from "./homeAdapter";

export function HomePage() {
  const { state, status, error, manualRetry } = useFrontierView(productionViews.home());

  const home = state.data ? adaptHome(state.data) : undefined;
  const hasData = Boolean(home);

  return (
    <ViewBoundary
      connection={viewBoundaryConnection({
        connection: state.connection,
        hasData,
        status,
      })}
      error={error}
      hasData={hasData}
      label="Home dashboard"
      onRetry={manualRetry}
      sync={state.sync}
    >
      {home ? <main className="home-page">
      <header className="home-hero">
        <div>
          <span>Production overview</span>
          <h2>Current work, continuity, and delivery evidence</h2>
          <p>Updated {home.updatedAt} from the viewer-scoped Home projection.</p>
        </div>
        <Link className="primary-button" to="/assistant">
          <BotMessageSquare aria-hidden="true" size={16} />
          Start Assistant conversation
        </Link>
      </header>

      <section aria-labelledby="home-attention-title" className="home-section">
        <div className="home-section-heading">
          <div>
            <h2 id="home-attention-title">Needs your attention</h2>
            <p>Waiting and delivered Tasks that require a user decision.</p>
          </div>
          <Link to="/tasks">View all tasks</Link>
        </div>
        {home.attention.length === 0 ? (
          <EmptyCard
            actionHref="/tasks"
            actionLabel="Open task index"
            message="No tasks need your attention."
          />
        ) : (
          <div className="home-card-grid">
            {home.attention.map((task) => (
              <Link className="home-card home-attention-card" key={task.taskId} to={task.href}>
                <div className="home-card-meta">
                  <StatusBadge label={task.statusLabel} tone={task.statusTone} />
                  <time>{task.updatedAt}</time>
                </div>
                <strong>{task.title}</strong>
                <span>{task.workspace} · {task.taskId}</span>
                <small>Open authoritative Task context <ArrowRight aria-hidden="true" size={13} /></small>
              </Link>
            ))}
          </div>
        )}
      </section>

      <div className="home-overview-grid">
        <section aria-labelledby="home-workers-title" className="home-section">
          <div className="home-section-heading">
            <div>
              <h2 id="home-workers-title">Recent Worker sessions</h2>
              <p>Exact session identities and Runtime generations.</p>
            </div>
          </div>
          {home.recentWorkers.length === 0 ? (
            <EmptyCard
              actionHref="/tasks"
              actionLabel="Open task index"
              message="No recent Worker sessions."
            />
          ) : (
            <div className="home-list">
              {home.recentWorkers.map((worker) => (
                <Link className="home-list-row" key={worker.workerSessionId} to={worker.href}>
                  <span className="home-list-icon"><Boxes aria-hidden="true" size={16} /></span>
                  <span>
                    <strong>{worker.taskTitle ?? worker.workerSessionId}</strong>
                    <small>{worker.workerSessionId} · {worker.workspace}</small>
                  </span>
                  <span className="home-list-end">
                    <StatusBadge label={worker.statusLabel} tone={worker.statusTone} />
                    <small>generation {worker.runtimeGeneration} · {worker.updatedAt}</small>
                  </span>
                </Link>
              ))}
            </div>
          )}
        </section>

        <aside aria-labelledby="home-capacity-title" className="home-capacity">
          <div className="panel-title">
            <HardDrive aria-hidden="true" size={17} />
            <h2 id="home-capacity-title">Worker continuity capacity</h2>
          </div>
          <div className="capacity-value">
            <strong>{home.capacity.used}</strong>
            <span>of {home.capacity.limit} sessions</span>
          </div>
          <div
            aria-label={`${home.capacity.percent}% capacity used`}
            aria-valuemax={Math.max(home.capacity.limit, home.capacity.used)}
            aria-valuemin={0}
            aria-valuenow={home.capacity.used}
            className="capacity-track"
            role="progressbar"
          >
            <span style={{ width: `${home.capacity.fillPercent}%` }} />
          </div>
          <p className={home.capacity.overBy > 0 ? "capacity-over" : undefined}>
            {home.capacity.percent}% used
            {home.capacity.overBy > 0 ? ` · ${home.capacity.overBy} over capacity` : ""}
          </p>
        </aside>
      </div>

      <section aria-labelledby="home-deliveries-title" className="home-section">
        <div className="home-section-heading">
          <div>
            <h2 id="home-deliveries-title">Recent deliveries</h2>
            <p>Authoritative acceptance mode and canonical Artifact references.</p>
          </div>
        </div>
        {home.deliveries.length === 0 ? (
          <EmptyCard
            actionHref="/tasks"
            actionLabel="Open task index"
            message="No recent deliveries."
          />
        ) : (
          <div className="home-delivery-list">
            {home.deliveries.map((delivery) => (
              <article className="home-delivery-row" key={delivery.taskId}>
                <span className="home-list-icon"><PackageOpen aria-hidden="true" size={16} /></span>
                <div>
                  <strong>{delivery.title}</strong>
                  <small>
                    {delivery.taskId} · {delivery.workspace} · {delivery.acceptanceMode} acceptance
                  </small>
                </div>
                <StatusBadge label={delivery.statusLabel} tone={delivery.statusTone} />
                <div className="home-delivery-links">
                  {delivery.workerHref ? <Link to={delivery.workerHref}>Worker</Link> : null}
                  {delivery.artifacts.map((artifact) => (
                    <Link key={artifact.artifactId} to={artifact.href}>
                      {artifact.artifactType} {artifact.artifactId}
                    </Link>
                  ))}
                </div>
                <time><Clock3 aria-hidden="true" size={12} /> {delivery.updatedAt}</time>
              </article>
            ))}
          </div>
        )}
      </section>
      </main> : null}
    </ViewBoundary>
  );
}

function EmptyCard({
  actionHref,
  actionLabel,
  message,
}: {
  actionHref: string;
  actionLabel: string;
  message: string;
}) {
  return (
    <div className="home-empty">
      <p>{message}</p>
      <Link to={actionHref}>{actionLabel}</Link>
    </div>
  );
}
