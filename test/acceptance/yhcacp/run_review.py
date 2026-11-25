"""Exercise production delivery paths against generated offline evidence."""
from argparse import ArgumentParser
from hashlib import sha256
import importlib.util
import json
import os
from pathlib import Path
import re
import runpy
import signal
import subprocess
import sys

root = Path(__file__).resolve().parent
harness = runpy.run_path(str(root.parents[2] / "scripts/run-first-report-stub-acceptance"))
spec = importlib.util.spec_from_file_location("review_fixture", root / "fixture.py")
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)
docker = harness["docker"]
require = harness["require"]
wait = harness["wait_for"]
bind = harness["bind"]
PROFILE = "deepseek-yhc-first-report-v1"
EVIDENCE = ("facts.json", "configured-costs.csv", "high-friction.csv", "delayed-execution.csv")

DUPLICATE_REQUEST = """\
from http.client import HTTPConnection
import json
from pathlib import Path
credential = Path("/run/fixture/relay-credential").read_text()
with_connection = HTTPConnection("127.0.0.1", 8080, timeout=5)
try:
    with_connection.request("POST", "/responses", json.dumps({"model": "deepseek-v4-pro"}),
                            {"Authorization": "Bearer " + credential})
    response = with_connection.getresponse()
    print(response.status)
    if b"permit already consumed" not in response.read().lower():
        raise SystemExit("expected the relay's consumed permit response")
finally:
    with_connection.close()
"""


class YHCReviewAcceptance(harness["Acceptance"]):
    def setup(self):
        self.fixture = self.path / "fixture"
        self.fixture.mkdir(mode=0o700)
        fixture.stage(self.fixture)
        binary = self.args.argus_binary
        require(binary.is_absolute() and binary.is_file() and not binary.is_symlink(),
                "provide the absolute path to a pinned Argus executable")
        self.argus = self.path / "argus"
        argus = binary.read_bytes()
        require(sha256(argus).hexdigest() == self.args.argus_sha256, "Argus executable digest mismatch")
        self.argus.write_bytes(argus)
        self.argus.chmod(0o500)
        return super().setup()

    def launch(self, name, *options):
        if name == self.relay:
            old = bind(harness["FIXTURES"] / "stub_upstream.py", "/run/fixture/stub_upstream.py")[1]
            new = bind(root / "fake_responses.py", "/run/fixture/stub_upstream.py")[1]
            require(options.count(old) == 1, "could not select the local Responses fixture")
            reports = [arg for version in (1, 2) for arg in bind(
                self.fixture / f"report-v{version}.md", f"/run/fixture/report-v{version}.md")]
            options = (*reports, *(new if item == old else item for item in options))
        if name == self.backend:
            review_config = self.path / "argus-review.json"
            review_config.write_text(json.dumps({
                "executable": "/opt/argus-review/argus",
                "executable_sha256": self.args.argus_sha256,
                "evidence_dir": "/opt/argus-review/evidence",
                "evidence_sha256": {name: sha256((self.fixture / name).read_bytes()).hexdigest()
                                    for name in EVIDENCE},
            }) + "\n")
            options = ("-e", "SWITCHBACK_YHC_AGENT_PROFILE_ENABLED=1",
                       "-e", "SWITCHBACK_ARGUS_REPORT_REVIEW_CONFIG=/run/fixture/argus-review.json",
                       *bind(review_config, "/run/fixture/argus-review.json"),
                       *bind(self.argus, "/opt/argus-review/argus"),
                       *bind(self.fixture, "/opt/argus-review/evidence"), *options)
        super().launch(name, *options)

    def require_consumed_permit(self):
        require(docker("exec", self.relay, "python3", "-c", DUPLICATE_REQUEST) == "409",
                "production relay did not refuse a duplicate request")

    def persisted_result(self, task, revision):
        for pointer in (self.path / "data" / "argus-report-review").glob("*/*/store/immutable/*-result.json"):
            ref = json.loads(pointer.read_bytes())
            digest = ref["sha256"]
            require(re.fullmatch(r"[0-9a-f]{64}", digest), "result digest is invalid")
            obj = pointer.parent.parent / "artifacts" / "sha256" / digest[:2] / digest
            raw = obj.read_bytes()
            require(len(raw) == ref["size_bytes"] and sha256(raw).hexdigest() == digest and raw.endswith(b"\n"),
                    "durable Argus result bytes failed integrity")
            result = json.loads(raw)
            if result["task_id"] == task and result["task_revision"] == revision:
                return {"raw": raw, "result": result, "digest": digest}
        return None

    def automatic_review(self, task, revision, verdict):
        def current():
            data = self.current_review(task)
            receipt = data["review"]
            if data["task_revision"] == revision and receipt and receipt["expected_task_revision"] == revision:
                require(receipt["verdict"] == verdict, "automatic review returned unexpected verdict")
                return receipt
        return wait(current, "automatic review did not write a receipt", 55)

    def worker_control_state(self, worker):
        require(re.fullmatch(r"[0-9a-f]{64}", worker), "unexpected Worker container identity")
        inspect = json.loads(docker("inspect", worker))[0]
        state = inspect["State"]
        db = json.loads(self.sql(f"""
SELECT jsonb_build_object('runtime',r.runtime_instance_id,'generation',r.generation,
 'status',r.status,'epoch',r.control_channel_epoch,'session_id',s.worker_session_id,
 'active_worker_run',s.active_worker_run_id,'current_runtime_run',r.worker_run_id,
 'stream_attempt',c.stream_attempt,'owner_fence',c.owner_fence,'owner_id',c.owner_id,
 'channel_status',c.status,'process_nonce',c.process_nonce,'terminal_phase',c.active_effect_phase,
 'terminal_turn',c.active_effect_agent_turn_id,'turn_status',t.status,
 'resume_completed_at',extract(epoch FROM c.resume_completed_at),
 'heartbeat_at',extract(epoch FROM h.last_received_at))
FROM runtime_instances r
JOIN worker_sessions s ON s.worker_session_id=r.worker_session_id
JOIN runtime_control_channels c ON c.runtime_instance_id=r.runtime_instance_id
LEFT JOIN agent_turns t ON t.agent_turn_id=c.active_effect_agent_turn_id
LEFT JOIN runtime_heartbeat_observations h ON h.runtime_instance_id=r.runtime_instance_id
 AND h.runtime_generation=r.generation AND h.channel_epoch=r.control_channel_epoch
WHERE r.container_id='{worker}'"""))
        db.update(container=inspect["Id"], pid=state["Pid"], started_at=state["StartedAt"],
                  running=state["Running"], restart_count=inspect["RestartCount"])
        return db

    def current_job(self, task, revision):
        return self.sql(f"SELECT status,attempt FROM argus_report_review_jobs WHERE task_id='{task}' AND task_revision={revision}")

    def current_review(self, task):
        return json.loads(self.request(f"/api/v1/tasks/{task}/delivery-review"))

    def delivered(self, view, previous_revision=0):
        def read():
            projection = self.task_projection(view)
            if projection is None:
                return None
            data = projection["data"]
            require(data["status"] not in ("failed", "cancelled", "waiting_user"),
                    "Task failed before delivering a revision")
            if data["status"] == "delivered" and data["revision"] > previous_revision:
                return data
        return wait(read, "new delivery revision timed out", 85)

    def report(self, task, version):
        require(re.fullmatch(r"task-[a-z0-9]+", task), "unexpected Task identity")
        evidence = json.loads(self.sql(f"""
SELECT jsonb_build_object('run',a.worker_run_id,'turn',a.agent_turn_id,'artifact',a.artifact_id,
 'checksum',a.object_checksum,'trace_checksum',tr.manifest_checksum,'trace_key',tr.manifest_object_key,
 'previous_run',wr.previous_worker_run_id,'trigger',wr.trigger_type)
FROM artifact_refs a JOIN tasks t ON t.task_id=a.task_id AND t.current_worker_run_id=a.worker_run_id
JOIN worker_runs wr ON wr.worker_run_id=a.worker_run_id
JOIN trace_manifests tr ON tr.trace_stream_id=a.trace_stream_id AND tr.worker_run_id=a.worker_run_id
 AND tr.agent_turn_id=a.agent_turn_id
WHERE t.task_id='{task}' AND a.artifact_type='report'"""))
        payload = self.request(f"/api/v1/artifacts/{evidence['artifact']}/content")
        want = (self.fixture / f"report-v{version}.md").read_bytes().strip()
        appendix = (b"\n\nNo files changed.\n\n## Verification Appendix\n"
                    b"- No commands or tests were observed in ACP updates.\n")
        require(payload == want + appendix,
                "production report body differs from frozen fixture")
        require("sha256:" + sha256(payload).hexdigest() == evidence["checksum"], "Artifact SHA-256 mismatch")
        def object_bytes(key, digest):
            data = (self.path / "data").resolve(strict=True)
            path = (data / key).resolve(strict=True)
            require(path.is_relative_to(data / "objects"), "Trace object escaped storage")
            raw = path.read_bytes()
            require(sha256(raw).hexdigest() == digest.removeprefix("sha256:"), "Trace digest mismatch")
            return raw
        trace = json.loads(object_bytes(evidence["trace_key"], evidence["trace_checksum"]))
        require(trace["agent_turn_id"] == evidence["turn"] and trace["worker_run_id"] == evidence["run"],
                "Trace identity differs from report")
        for segment in trace["segments"]:
            object_bytes(segment["uri"].removeprefix("object://"), segment["sha256"])
        (self.path / f"formal-report-v{version}.md").write_bytes(payload)
        self.save(f"formal-report-v{version}.json", evidence)
        return payload, evidence

    def verify_worker(self, worker, version):
        self.quota(worker)
        require(docker("exec", worker, "sha256sum", "/opt/switchback/yhc").split()[0] == self.args.yhc_sha256,
                "actual Worker YHC digest does not match the approved artifact")
        inspected, = json.loads(docker("inspect", worker))
        require(inspected["Config"]["Labels"]["com.switchback.agent-profile"].startswith(PROFILE + "@1:"),
                "unexpected AgentProfile on the actual Worker")
        private, = [mount for mount in inspected["Mounts"] if mount["Destination"] == "/workspace/.yhc"]
        agent_state, = [mount for mount in inspected["Mounts"] if mount["Destination"] == "/var/lib/switchback/agent-state"]
        require(private["Source"] == agent_state["Source"], "YHC private state is outside the AgentState mount")
        routes = docker("exec", worker, "cat", "/proc/net/ipv6_route").splitlines()
        require(not any(line.split()[0:2] == ["0" * 32, "00"] and line.split()[-1] != "lo" for line in routes),
                "Worker has a default IPv6 route")
        processes = [line.split(None, 1) for line in docker("top", worker, "-eo", "pid,comm").splitlines()[1:]]
        yhc, = [int(pid) for pid, name in processes if name == "yhc"]
        state = self.worker_control_state(worker)
        state["yhc_pid"] = yhc
        self.save(f"worker-v{version}.json", state)
        return state

    def verify_delivery(self, task, revision, version, verdict):
        body, artifact = self.report(task, version)
        receipt = self.automatic_review(task, revision, verdict)
        stored = self.persisted_result(task, revision)
        require(stored and receipt["review_result_sha256"] == stored["digest"] and
                receipt["artifact_id"] == artifact["artifact"] and receipt["worker_run_id"] == artifact["run"] and
                receipt["artifact_sha256"] == sha256(body).hexdigest() and
                receipt["review_run_id"] == stored["result"]["review_run_id"], "review receipt binding differs")
        require(self.current_job(task, revision) == "recorded|1", "automatic review was not recorded once")
        self.save(f"switchback-review-v{version}.json", receipt)
        (self.path / f"argus-result-v{version}.json").write_bytes(stored["raw"])
        turn = json.loads(self.sql(f"""
SELECT jsonb_build_object('session',t.agent_session_id,'status',t.status,
 'observation_count',(SELECT count(*) FROM runtime_control_messages m
  WHERE m.direction='event' AND m.effect_status='applied' AND m.envelope->>'message_type'='model.observation'
  AND m.envelope->>'agent_turn_id'=t.agent_turn_id AND m.envelope->>'worker_run_id'=t.worker_run_id))
FROM agent_turns t WHERE t.agent_turn_id='{artifact['turn']}' AND t.worker_run_id='{artifact['run']}'
"""))
        require(turn["status"] == "completed" and turn["observation_count"] == 1,
                "expected a completed report Turn with one applied model observation")
        self.save(f"turn-v{version}.json", turn)
        return body, artifact, receipt, turn

    def check_loop(self, schema):
        command = {"schema_version": "gateway.direct_task_input.v1", "idempotency_key": "offline-yhc-lets-quant",
                   "project_id": "project-local", "workspace_id": "workspace-local",
                   "workspace_revision_id": "workspace-revision-local-v1", "title": "lets-quant independent review with YHC",
                   "goal": "OFFLINE-LETS-QUANT-INITIAL-REPORT. Deliver the first frozen research report.",
                   "target": {"kind": "workspace", "ref": "workspace-revision-local-v1"},
                   "constraints": ["Offline fixture. Return report text only."],
                   "acceptance_criteria": ["Review the report against frozen experiment evidence."],
                   "agent_profile_id": PROFILE, "acceptance_mode": "manual", "user_visible_types": ["report"]}
        created = json.loads(self.request("/api/v1/tasks", command, expected=202))
        view = created["view_ref"]
        require(re.fullmatch(r"/api/v1/views/task_summary/task-[a-z0-9]+", view), "unexpected Task reference")
        task = view.rsplit("/", 1)[1]
        self.save("submitted-task.json", {"task_id": task, "view": view})
        worker, = wait(self.workers, "Backend did not create the authorized Worker", 40)
        wait(lambda: self.service_ready(worker) and f"worker runtime ready profile={PROFILE}" in harness["container_log"](worker),
             "YHC Worker did not become ready", 75)
        first = self.delivered(view)
        before = self.verify_worker(worker, 1)
        body, v1, receipt1, turn1 = self.verify_delivery(task, first["revision"], 1, "rework_required")
        summaries = [finding["summary"] for finding in receipt1["findings"]]
        require(len(summaries) == 3, "expected three independent findings on report one")
        (self.path / "control" / "review-findings.json").write_text(json.dumps(summaries) + "\n")
        print("checkpoint: first frozen report and three Argus findings verified", flush=True)

        self.require_consumed_permit()
        permits = self.path / "permit"
        (permits / "current").rename(permits / "first")
        (permits / "current").mkdir(mode=0o700)

        rework = {"schema_version": "gateway.task_command.v1", "idempotency_key": "offline-yhc-one-rework",
                  "expected_task_revision": first["revision"], "message": "REWORK-RESEARCH-REPORT\n" + "\n".join(summaries)}
        self.request(f"/api/v1/tasks/{task}/rework", rework, expected=202)
        second = self.delivered(view, first["revision"])
        revised, v2, receipt2, turn2 = self.verify_delivery(task, second["revision"], 2, "passed")
        after = self.verify_worker(worker, 2)
        require(self.workers() == [worker], "another Worker was allocated")
        for key in ("container", "pid", "started_at", "restart_count", "session_id", "runtime", "generation", "epoch", "process_nonce", "yhc_pid"):
            require(before[key] == after[key], f"resident Worker identity changed: {key}")
        require(turn1["session"] == turn2["session"], "YHC ACP Session changed on Rework")
        require(v2["previous_run"] == v1["run"] and v2["run"] != v1["run"] and v2["trigger"] == "task_rework",
                "rework did not preserve WorkerRun lineage")
        require(v2["artifact"] != v1["artifact"] and revised != body and not receipt2["findings"], "revised report or review differs")
        require(self.request(f"/api/v1/artifacts/{v1['artifact']}/content") == body, "first frozen Artifact changed")

        refreshed = self.task_projection(view)
        require(refreshed["view_id"] == task and refreshed["data"]["status"] == "delivered" and
                refreshed["data"]["revision"] == second["revision"], "fresh Task view did not point at the reviewed delivery")
        self.save("task-reviewed.json", refreshed)
        self.request(f"/api/v1/tasks/{task}/accept", {
            "schema_version": "gateway.task_command.v1", "idempotency_key": "offline-yhc-accept",
            "expected_task_revision": second["revision"], "message": "Accept the revised synthetic report.",
        }, expected=202)

        def completed():
            projection = self.task_projection(view)
            if projection and projection["view_id"] == task and projection["data"]["status"] == "completed":
                return projection

        after_accept = wait(completed, "fresh Task projection did not show completed")
        fresh = completed()
        require(after_accept["data"]["revision"] > second["revision"] and fresh and
                fresh["data"]["revision"] == after_accept["data"]["revision"],
                "Accept did not persist in the fresh projection")
        require(self.sql(f"SELECT status FROM tasks WHERE task_id='{task}'") == "completed",
                "Accept did not persist in PostgreSQL")
        require(self.request(f"/api/v1/artifacts/{v2['artifact']}/content") == revised,
                "Accept changed the revised Artifact")
        self.save("task-completed.json", after_accept)
        self.require_consumed_permit()

        counts = self.sql("SELECT (SELECT count(*) FROM tasks),(SELECT count(*) FROM worker_runs),"
                          "(SELECT count(*) FROM runtime_instances),(SELECT count(*) FROM delivery_reviews),"
                          "(SELECT count(*) FROM argus_report_review_jobs WHERE status='recorded')")
        require(counts == "1|2|1|2|2", "expected one Task, two linked Runs, one Runtime, and two recorded reviews")
        log = harness["container_log"](self.relay).splitlines()
        require([line for line in log if line.startswith("fixture-upstream-request")] ==
                ["fixture-upstream-request v1 /responses", "fixture-upstream-request v2 /responses"] and
                not any(line.startswith("fixture-refusal") for line in log) and
                all((self.path / "permit" / f"v{version}" / "permit.used").is_file() for version in (1, 2)),
                "expected exactly one fake Responses request per delivery and no other requests")

        return {"schema": schema, "task_id": task, "task_state": "completed", "accepted_revision": after_accept["data"]["revision"],
                "argus_binary_sha256": self.args.argus_sha256, "duplicate_relay_status": 409, "agent_profile": PROFILE,
                "yhc_binary_sha256": self.args.yhc_sha256, "same_resident_worker": True, "same_yhc_process": True,
                "same_acp_session": True, "first_delivery_revision": first["revision"], "second_delivery_revision": second["revision"],
                "first_verdict": receipt1["verdict"], "first_findings": len(summaries),
                "second_verdict": receipt2["verdict"], "second_findings": len(receipt2["findings"]),
                "artifact_v1": v1["checksum"], "artifact_v2": v2["checksum"], "v1_immutable_after_rework": True,
                "trace_v1": v1["trace_checksum"], "trace_v2": v2["trace_checksum"],
                "model_observations": [turn1["observation_count"], turn2["observation_count"]],
                "fake_model_requests": 2, "real_model_requests": 0, "argus_review_jobs": 2, "worker_xfs_limit_gib": 10}


def main():
    parser = ArgumentParser()
    for option in ("engine-id", "worker-image", "backend-image", "postgres-image", "web-image"):
        parser.add_argument("--" + option, required=True)
    parser.add_argument("--output-parent", type=Path, required=True)
    parser.add_argument("--argus-binary", type=Path, required=True)
    for name in ("argus", "yhc"):
        parser.add_argument(f"--{name}-sha256", required=True)
    args = parser.parse_args()
    require(sys.platform == "linux" and os.geteuid() == 0, "requires dedicated Linux host")
    for digest in (args.argus_sha256, args.yhc_sha256):
        require(re.fullmatch(r"[0-9a-f]{64}", digest), "provide lowercase SHA-256 without a prefix")
    os.umask(0o077)
    with harness["engine_lock"]():
        runner = YHCReviewAcceptance(args)
        try:
            runner.preflight()
            result = runner.check_loop(runner.setup())
        finally:
            runner.cleanup()
        runner.save("summary.json", result)
        print("PASS: one completed Task, resident YHC, Argus findings 3 -> 0, duplicate 409, fake=2 real=0", flush=True)


if __name__ == "__main__":
    def terminate(*_):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, terminate)
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(130)
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as error:
        sys.exit(str(error))
