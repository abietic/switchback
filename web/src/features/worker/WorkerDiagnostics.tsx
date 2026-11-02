import { useEffect, useState } from "react";
import type { WorkerDiagnosticLinks } from "../../api/contracts";
import { fetchWorkerDiagnostics, type WorkerDiagnosticTarget } from "../../api/workerDiagnostics";

export function WorkerDiagnostics(target: WorkerDiagnosticTarget) {
  // Changing any target discards old links synchronously, including in-flight replies.
  return <DiagnosticTarget key={JSON.stringify(target)} {...target} />;
}

function DiagnosticTarget({ sessionId, runId, turnId }: WorkerDiagnosticTarget) {
  const [result, setResult] = useState<WorkerDiagnosticLinks>();
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    fetchWorkerDiagnostics({ sessionId, runId, turnId }, controller.signal).then(
      (next) => { if (current) setResult(next); },
      () => { if (current) setFailed(true); },
    );
    return () => { current = false; controller.abort(); };
  }, [sessionId, runId, turnId]);
  return <section className="side-section metadata-section" aria-label="Diagnostics">
    <h2>Diagnostics</h2>
    {failed ? <p role="status">Diagnostics unavailable.</p> : !result ? <p role="status">Loading diagnostics…</p> : <>
      {result.links.map((link) => <p key={link.kind}><a href={link.url} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">
        {link.kind === "grafana" ? "Grafana · Worker overview" : link.kind === "logs" ? "Logs · selected scope" : "Langfuse"}
      </a></p>)}
      {result.grafana_status === "disabled" ? <p>Diagnostic links disabled.</p> : result.grafana_status === "invalid_config" ? <p>Diagnostic links unavailable: invalid local browser configuration.</p> : <small>Aggregate metrics; logs use the selected Session, Run and Turn. Service availability is not checked.</small>}
      <p><small>Langfuse link unavailable: verified projection identity is not available.</small></p>
    </>}
  </section>;
}
