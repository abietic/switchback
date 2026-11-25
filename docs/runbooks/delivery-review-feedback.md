# Argus delivery review feedback

Apply migration `000028` before starting a Backend build that includes review feedback. Enable result recording on the local Backend with `SWITCHBACK_DELIVERY_REVIEW_TOKEN_SHA256`. Set it to the lowercase SHA-256 of a randomly generated 32-byte bearer token. Provision the token itself to the trusted reviewer adapter and keep it outside the repository, Worker image and browser. The Compose deployment passes the digest to the Backend.

Send the verified reviewer result to `POST /api/v1/tasks/{task_id}/delivery-review` with `Authorization: Bearer <token>`. The adapter verifies the frozen Argus inputs and stores the complete result and provenance. It supplies the exact delivered Task revision, WorkerRun, report ID, bare report SHA-256, immutable Argus run ID, and bare SHA-256 of that review result. The Backend checks the binding against the current delivery and stores the findings within the deployment's configured tenant, actor and Workspace.

```json
{
  "schema_version": "gateway.delivery_review.v1",
  "expected_task_revision": 4,
  "worker_run_id": "run-example",
  "artifact_id": "artifact-report-example",
  "artifact_sha256": "<64 lowercase hexadecimal characters>",
  "review_run_id": "review-example",
  "review_result_sha256": "<64 lowercase hexadecimal characters>",
  "verdict": "rework_required",
  "findings": [
    {"code": "configuration-omitted", "summary": "Disclose the execution parameters used for this scenario."}
  ]
}
```

Use `passed` with `findings: []`, or `rework_required` with one to twenty findings. A successful request returns the recorded receipt. Resending the same review run and payload returns the same receipt, even after Task acceptance. A conflicting replay, stale delivery or a second result for the same report is rejected.

`GET /api/v1/tasks/{task_id}/delivery-review` returns the current Task revision and its current report's review, or `review: null`. In the production Worker page, select **Check Argus review** under **Delivery review**. A rework replaces the current report's review view as the Task moves to a new delivery. The separate **Accept** and **Request rework** actions remain available through the Task initiator's command path.

## Automatic local Argus review

Apply migration `000029`. Point `SWITCHBACK_ARGUS_REPORT_REVIEW_CONFIG` to an
absolute JSON file inside the Backend. Omitting `profile` selects
`lets-quant-report.v1`. To review a frozen real-market window, set
`profile` to `lets-quant-market-report.v1` and pin an Argus build and
evidence set that support that profile. The Backend rejects unknown profiles.
For Compose, bind-mount the configuration, Argus executable and evidence directory
read-only in a local override. The Backend starts automatic review when this
configuration is present. Use an operator-approved Argus build that provides
`report-review` and pin these exact files using lowercase SHA-256.

```json
{
  "executable": "/opt/argus-review/argus",
  "executable_sha256": "<64 lowercase hexadecimal characters>",
  "evidence_dir": "/opt/argus-review/evidence",
  "evidence_sha256": {
    "facts.json": "<64 lowercase hexadecimal characters>",
    "configured-costs.csv": "<64 lowercase hexadecimal characters>",
    "high-friction.csv": "<64 lowercase hexadecimal characters>",
    "delayed-execution.csv": "<64 lowercase hexadecimal characters>"
  }
}
```

The Backend reconciles committed `TaskStageDelivered.v1` events for current
manual deliveries in that Workspace. Each job binds its revision, WorkerRun,
complete report and approved profile digest before launching the reviewer. The
trusted subprocess receives the staged report and evidence, a fixed command,
and a minimal environment. The Backend verifies the exact result bytes and
records the receipt and job completion in the same transaction.

Preserve the Backend data root across restarts. Argus keeps its scoped immutable
results under `argus-report-review` there. An expired job claim is reclaimed
with the same request and Argus store. Changed profile, executable or evidence digests
produce a new profile identity. Restore the approved configuration to resume
a pending job bound to the previous profile. Review failures leave the delivery
available for human Rework or Accept.
