# Public snapshot CI

CI retains Static & Contracts, Go Unit & Race, PostgreSQL Integration,
Production Web, Browser and Required. Required depends on exactly the five
quality jobs and rejects missing, skipped or unsuccessful jobs. PostgreSQL uses
the pinned PG17.10 amd64 digest and requires live connectivity plus complete,
zero-skip integration JSON evidence. Actions remain pinned by commit SHA.

## Local commands

```bash
make publication-check
make python-check
GOMAXPROCS=2 ./scripts/check-observability-test-groups --manifest test/acceptance/observabilitygroups/baseline-v1.json
go test -count=1 ./test/acceptance/observabilitygroups
```

Publication checks tracked contents for legacy identity, private email and the
specific personal path, forbidden environment/key filenames and files over
1 MiB. Secret scanning covers the working tree, including ignored and hidden
source files, excluding only Git metadata. It rejects generic alphanumeric
`sk-` tokens, existing live/prod/kimi formats, AWS access IDs and private-key
headers. Diagnostics contain filenames and fixed messages, never matched
values. Enumeration and scanner errors fail closed. Synthetic characterizations
exercise the failure paths; this is a release hygiene gate, not proof that every
possible credential format or machine-specific path has been identified.

Python runs on Ubuntu 24.04 Python 3.12 with stdlib only. Four independent
processes discover engine, experienceprepare, experiencefreeze and production
LiteLLM callback `test_*.py` files (377 recorded tests: 279/50/23/25). Empty discovery, skips, import errors and test
failures all fail CI. Engine tests use fixtures; callback tests fake LiteLLM and
HTTP. No VM, Docker Engine, PostgreSQL or model provider is started by this gate.
The historical fixed four-image v1 preparation/freeze tests use an exact pinned
Worker Dockerfile fixture; they do not validate the current qualification-image
build or authorize changing historical profile pins.

`deploy/experience/created_layer_observer_assets_test.py` is explicitly excluded
from `python-check`: its optional Compose-render check needs client-side
`docker compose config` output. The two static asset checks and that render
check are outside this gate's coverage. No skipped result from it is counted as
Python success. Native observability runtime acceptance, Docker/VM acceptance,
current image builds and real provider behavior are separate evidence windows.

## History-independent observability preservation

The version-1 portable manifest records package identity, canonical AST token
SHA256 for every protected test/helper, and exact ordinary test sets. The
validator rejects invalid schema, empty/duplicate IDs, invalid digests, missing
functions, body/assertion changes, extra functions and narrowed build tags.
Native and fixture group relations and all five actual compilation groups still
run. The manifest works with one parentless commit and no private Git objects;
CI never substitutes HEAD as its own baseline or skips a missing ancestor.

Private operations retain commit mode and an explicit strict exporter:

```bash
./scripts/check-observability-test-groups BASE_COMMIT
./scripts/check-observability-test-groups --export-manifest BASE_COMMIT output.json
```

Export succeeds only after commit-mode preservation, reviewed additions and
five-group compilation pass. Public manifests contain hashes and a provenance
statement, without private author metadata or Git objects. Updates require review
of the old/new canonical bodies and group delta; CI never refreshes expected data.

The separate `baseline-golden-v1.json` remains unchanged at 502 functions,
exported from the proven strict baseline plus 13 reviewed additions. The active
manifest lineage is explicit: golden 502 → 17 reviewed body replacements plus
287 additions (789) → five reviewed PostgreSQL fixture body replacements plus
one new helper (790) → one reviewed public CI contract test body replacement
(still 790). Ordinary selections remain 296/429 throughout the later repairs.
Provenance binds the reviewed deltas; CI does not refresh its own expected data.
Registration preserves functions and groups, not semantic acceptance: the
recorded full five-package PostgreSQL run remains 622 pass / 34 fail / 0 skip.
The feature contains unfinished execution owners and is not merged into main;
neither registration nor focused repairs establish a green full CI.

The Linux follow-up keeps the historical `observabilitybuild` production CLI's
exact integration-baseline ancestry gate unchanged. Its tests construct a minimal
synthetic Git DAG and use an explicit in-package baseline seam. Compiled fixture
CLIs use a test-only Go source overlay for that baseline; the unmodified compiled
CLI is separately required to reject synthetic history. These tests cover build
mechanics, fake Docker, provenance and default rejection. They do not establish
that the historical default accepts the independent public root, nor constitute
an actual current image-build acceptance run.

The follow-up also rejects duplicate JSON keys recursively before manifest typed
decoding, preserving unknown-field and trailing-data rejection. Engine binding
recognizes only the two exact bridge IPAM Source leaves already published in the
Python bundle contract; native proof and API ordering remain required. Remote
Required success still needs a fresh cloud run of the final commits.
