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

Python runs on Ubuntu 24.04 Python 3.12 with stdlib only. Completed main
discovers only the production LiteLLM callback suite (25 recorded tests). Empty discovery, skips,
import errors and failures fail CI; callback tests fake LiteLLM and HTTP.
The capacity feature branch adds three separate engine/preparation/freeze
suites, which are not present in completed main. Its recorded four-suite total
is 377; that is not main coverage. This is an explicit source boundary, not an
absent-directory fallback or successful skip. No VM, Docker
Engine, PostgreSQL or model provider is started by this Python gate.
Native runtime acceptance, image builds and real provider behavior remain
separate evidence windows.

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

The public main active manifest contains 502 functions: it retains 501 golden
hashes and replaces exactly one reviewed public CI contract test body.
`baseline-golden-v1.json` remains unchanged at 502 functions. Ordinary selections
remain 230/280; all native/fixture relationships remain checked. This replacement
aligns the protected CI contract with the existing publication/Python gates; it
does not add capacity behavior to main. The capacity feature branch has its own
reviewed manifest lineage and unfinished execution owners. Registration protects
bodies and groups; it does not claim full semantic acceptance or a green full CI.

The Linux follow-up keeps the historical `observabilitybuild` production CLI's
exact integration-baseline ancestry gate unchanged. Its tests construct a minimal
synthetic Git DAG and use an explicit in-package baseline seam. Compiled fixture
CLIs use a test-only Go source overlay for that baseline; the unmodified compiled
CLI is separately required to reject synthetic history. These tests cover build
mechanics, fake Docker, provenance and default rejection. They do not establish
that the historical default accepts the independent public root, nor constitute
an actual current image-build acceptance run.

The follow-up also rejects duplicate JSON keys recursively before manifest typed
decoding, preserving unknown-field and trailing-data rejection. Remote Required
success still needs a fresh cloud run of the exact public main commit.
