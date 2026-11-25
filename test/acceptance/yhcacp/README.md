# YHC ACP compatibility smoke

The smoke runs a pinned `yhc serve acp` subprocess through Switchback's ACP
client. A temporary home and WorkingCopy isolate agent state. A local HTTP
fixture supplies synthetic OpenAI Responses. The launcher passes a freshly
generated fixture credential through the child environment, and the fixture
checks that credential on every request.

## Run

Use a trusted YHC checkout containing the ACP `--disable-long-session-services`
flag. Build for the host running the smoke using the toolchain pinned by YHC.

```sh
run_dir=$(mktemp -d)
yhc_source=/absolute/path/to/yhc
(cd "$yhc_source" && GOENV=off GOTOOLCHAIN=go1.27.0 CGO_ENABLED=0 \
  go build -trimpath -buildvcs=false -o "$run_dir/yhc" ./cmd/yhc)
"$run_dir/yhc" serve acp --help | grep -- --disable-long-session-services
shasum -a 256 "$run_dir/yhc"
```

From the Switchback checkout, set the trusted binary's absolute path and the
SHA-256 produced by the build. The smoke checks the digest before launching it.

```sh
SWITCHBACK_TEST_YHC_BINARY="$run_dir/yhc" \
SWITCHBACK_TEST_YHC_SHA256='<sha256 from the build>' \
go test -tags=yhc_acp_smoke -v -count=3 ./test/acceptance/yhcacp -run '^TestYHCACPReportAndRework$'
```

To enforce loopback-only networking on macOS, compile the test before entering
the sandbox. The same environment variables are required for the test process.

```sh
go test -tags=yhc_acp_smoke -c -o "$run_dir/yhc-acp.test" ./test/acceptance/yhcacp
SWITCHBACK_TEST_YHC_BINARY="$run_dir/yhc" \
SWITCHBACK_TEST_YHC_SHA256='<sha256 from the build>' \
/usr/bin/sandbox-exec -p '(version 1) (allow default) (deny network*)
  (allow network-bind (local ip "localhost:*"))
  (allow network-inbound (local ip "localhost:*"))
  (allow network-outbound (remote ip "localhost:*"))' \
  "$run_dir/yhc-acp.test" -test.v -test.count=3 -test.run '^TestYHCACPReportAndRework$'
```

## Acceptance boundary

The smoke checks ACP v1 negotiation, initial report, rework in the same
resident session, and exact session resume in a fresh subprocess. Both follow-up
requests must carry the previous report verbatim in assistant history. Each
prompt must yield the expected report text and a single `end_turn`. The raw
ACP trace must omit the credential and contain one `session/resume` request.

The managed run applies the flag to both subprocesses and requires three
successful foreground requests and zero background requests. It then checks
that a synthetic provider `409` emits one `acp_prompt_failed` update. The ACP
adapter uses the YHC error notification for the active session to reject an
otherwise successful `end_turn`. The default run requires
three foreground requests and at least one background memory extraction as a
positive control. The fixture counts these requests separately and rejects
unrelated calls. Both runs use disabled tools and `YHC_SIMPLE=1`.

The ACP smoke exercises a synthetic Responses endpoint. The next section covers production Worker wiring and Task acceptance.

## Isolated Worker, Argus and Accept

The full acceptance runs a fresh Task through the normal production Backend,
Worker Shim and Web API. Its synthetic reports and NAV evidence are generated
locally. Use an idle, dedicated Linux/ARM64 Docker Engine with classic
overlay2 backed by XFS project quotas and at least 10 GiB of writable-layer
capacity. Install `xfs_io`, `xfs_quota` and Python 3.11 or later on that host.

Build the ordinary Switchback Backend, Worker and production Web images
from the Switchback revision under test. Use an existing local PostgreSQL image.
Build Linux/ARM64 YHC from a reviewed revision containing
`serve acp --disable-long-session-services`, and Linux/ARM64 Argus from a
reviewed revision containing `report-review`. Keep the source revisions and
the separately computed binary SHA-256 values with the run evidence. The
Argus reference used for this profile is
[`fad5df26c17d656dad20923c92ff57e5f14951fc`](https://github.com/abietic/argus/commit/fad5df26c17d656dad20923c92ff57e5f14951fc).

Build an optional derivative of the Worker image using an existing local
base image and the verified YHC binary. The derivative uses a separate
LiteLLM alias for the Responses route.

```sh
# Run from the Switchback checkout on the qualified Docker host.
context=$(mktemp -d)
install -m 0555 "$yhc_binary" "$context/yhc"
install -m 0644 images/worker/litellm/yhc-config.yaml "$context/yhc-config.yaml"
docker build --pull=false --network=none -f images/worker/Dockerfile.yhc \
  --build-arg WORKER_BASE="$worker_base_image" --build-arg YHC_SHA256="$yhc_sha256" \
  -t switchback-worker-yhc:offline "$context"
```

If the Engine requires a named base for `FROM`, use a local tag for the verified
base image and keep its image ID or digest with the evidence.

```sh
sudo ./scripts/run-yhc-report-review-acceptance \
  --engine-id "$engine_id" --output-parent "$private_output_parent" \
  --worker-image switchback-worker-yhc:offline --backend-image "$backend_image" \
  --web-image "$web_image" --postgres-image "$postgres_image" \
  --argus-binary "$argus_binary" --argus-sha256 "$argus_sha256" \
  --yhc-sha256 "$yhc_sha256"
```

The runner enables `SWITCHBACK_YHC_AGENT_PROFILE_ENABLED=1` on the isolated
Backend and selects `deepseek-yhc-first-report-v1` through the direct Task API.
The profile requires the existing first-report relay, the qualified Engine and
Worker delivery with Assistant and Runtime replacement disabled. It uses
the same capacity admission and 10 GiB hard limit as the Codex first-report
profile. YHC's launcher and local proxy endpoint are fixed by the server.
The mounted private `.yhc` directory keeps transcripts out of frozen
WorkingCopy results. The default deployment continues to select Codex.

The test checks the actual Worker binary, XFS limit and internal-only network.
Both turns run under the same resident YHC process and ACP session. It verifies
the two frozen reports, their Trace digests and one applied model observation
per turn. The first Argus receipt has three findings, and all three appear in
the Rework prompt. The revised report has a passing receipt and the first
Artifact retains its original bytes. The fixed actor uses the production Web
same-origin API to Accept the current delivery and reads the completed
projection again.

Only the loopback fake Responses upstream receives requests. Each delivery
consumes the relay's permit once, and a duplicate to the relay returns HTTP
`409` before reaching the fake upstream. The runner resets the fixture permit
between deliveries and checks for exactly two fake requests. It cleans up its
own containers, network and volumes and writes `summary.json` in the private
evidence directory after cleanup. Keep that directory local.
