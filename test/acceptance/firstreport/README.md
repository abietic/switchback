# Re-run first-report acceptance with a stub upstream

Run on a dedicated, idle Linux Docker Engine with classic `overlay2` on XFS with project quotas. Use Python 3.10+, `findmnt`, `xfs_io` and `xfs_quota`. On macOS, run inside the qualified Lima VM. The runner checks the local Unix socket against the expected Engine ID.

Build the Backend, Worker and production Web images from this checkout and load the PostgreSQL image for the Engine architecture before running. Supply their local tags or digests. Run from the Engine host so Docker can see the checkout and evidence paths used for bind mounts.

```sh
docker build -f images/backend/Dockerfile -t switchback-backend:stub-e2e .
docker build -f images/worker/Dockerfile -t switchback-worker:stub-e2e .
docker build -f images/web/Dockerfile -t switchback-web:stub-e2e .

sudo ./scripts/run-first-report-stub-acceptance \
  --engine-id "$FIRST_REPORT_ENGINE_ID" \
  --backend-image switchback-backend:stub-e2e \
  --worker-image switchback-worker:stub-e2e \
  --web-image switchback-web:stub-e2e \
  --postgres-image "$FIRST_REPORT_POSTGRES_IMAGE"
```

Each invocation creates a fresh internal Docker network, PostgreSQL database, Task and relay permit. The fixture uses only generated credentials and a loopback Chat Completions stub. Container ports stay internal.

The runner reads the 10 GiB XFS hard limit from the live Worker, verifies that the second LiteLLM request gets HTTP `409` and counts exactly one stub request. It reads the formal report from production Web, verifies the Trace manifest and segment digests, and checks for a successful model observation on the same Turn. The Accept request and the fresh `completed` projection both go through the production Web's same-origin API.

The runner prints a private evidence directory. Use `--output-parent` to choose an existing parent outside the checkout. `summary.json` is written after verification and cleanup succeed. The directory retains the report, Trace manifest, final projection and local logs. Repeating the command creates a new evidence directory and permit.
