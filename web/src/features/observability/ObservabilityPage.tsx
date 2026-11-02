import { Boxes } from "lucide-react";
import { Link } from "react-router";
import { StatusBadge } from "../../components/ui/StatusBadge";
import { ViewBoundary, viewBoundaryConnection } from "../../components/ui/ViewBoundary";
import { productionViews } from "../../projections/productionViewRegistry";
import { useFrontierView } from "../../state/useFrontierView";
import { adaptHome } from "../home/homeAdapter";

export function ObservabilityPage() {
  const { state, status, error, manualRetry } = useFrontierView(productionViews.home());
  const home = state.data ? adaptHome(state.data) : undefined;
  const hasData = Boolean(home);

  return (
    <ViewBoundary
      connection={viewBoundaryConnection({ connection: state.connection, hasData, status })}
      error={error}
      hasData={hasData}
      label="可观测性"
      onRetry={manualRetry}
      sync={state.sync}
    >
      {home ? (
        <main className="home-page">
          <header className="home-hero">
            <div>
              <h2>选择 Worker 查看诊断</h2>
              <p>从当前授权的最近会话进入 Worker 详情，查看已有诊断入口。</p>
            </div>
          </header>
          <section aria-labelledby="observability-workers-title" className="home-section">
            <div className="home-section-heading">
              <div>
                <h2 id="observability-workers-title">最近 Worker</h2>
                <p>Grafana 展示聚合指标；日志按所选 Session、Run 和 Turn 查询，服务可用性未检查。</p>
                <p>Langfuse 尚无已核验身份时不提供链接。</p>
              </div>
            </div>
            {home.recentWorkers.length === 0 ? (
              <div className="home-empty">
                <p>暂无可查看的 Worker。</p>
                <p>进入 Assistant 或任务交付，显式创建任务后再查看诊断。</p>
                <Link to="/assistant">进入 Assistant</Link>
                <Link to="/tasks">进入任务交付</Link>
              </div>
            ) : (
              <div className="home-list">
                {home.recentWorkers.map((worker) => (
                  <Link className="home-list-row" key={worker.workerSessionId} to={worker.href}>
                    <span className="home-list-icon"><Boxes aria-hidden="true" size={16} /></span>
                    <span>
                      <strong>{worker.taskTitle ?? worker.workerSessionId}</strong>
                      <small>{worker.workerSessionId} · {worker.workspace}</small>
                    </span>
                    <span>
                      <StatusBadge label={worker.statusLabel} tone={worker.statusTone} />
                      <small>Runtime generation {worker.runtimeGeneration} · {worker.updatedAt}</small>
                      <small>查看 Worker 诊断</small>
                    </span>
                  </Link>
                ))}
              </div>
            )}
          </section>
        </main>
      ) : null}
    </ViewBoundary>
  );
}
