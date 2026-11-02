## Why

Artifact review covers the common delivery path, but some users need to inspect, edit and run code directly inside the exact long-lived Worker environment. A browser IDE can provide that escape hatch only if it remains inside WorkerSession lifecycle, network, resource and WorkingCopy authority instead of exposing an unmanaged container port.

## What Changes

- Add an optional Web IDE capability backed by a pinned `code-server` binary in the Worker image, with OpenVSCode Server retained as an adapter fallback.
- Start the IDE on demand as a typed managed `SessionProcess`; do not start it with every Worker Runtime.
- Embed the IDE in the resizable Worker Workbench through a same-origin Gateway PreviewProxy iframe with HTTP and WebSocket support.
- Keep container address, declared port and IDE credentials opaque to the browser; publish no host port.
- Keep the IDE alive across AgentTurn terminal states and stop it on explicit user action, WorkerSession terminality, security termination or Runtime replacement.
- Allow the IDE and Agent to write the same WorkingCopy concurrently in the first version and mark affected results `mixed_origin_possible` instead of claiming Agent-only patch provenance.
- Ship only approved preinstalled extensions initially. A later additive release may let the user install Open VSX extensions directly, while Agent processes remain unable to write the IDE extension directory.
- Persist IDE settings and extension state in WorkerSession storage and expose process state, stop/restart and temporary full-screen controls in WorkerDetail.

## Capabilities

### New Capabilities

- `worker-web-ide`: Define on-demand IDE process lifecycle, proxied browser access, shared-WorkingCopy semantics, extension policy, resource admission and user-visible recovery behavior.

### Modified Capabilities

None.

## Impact

- Affects the Worker image, Runtime Shim managed-process API, RuntimeSupervisor process and port resolution, Gateway authorization, PreviewProxy HTTP/WebSocket transport, WorkerDetail projection and React Workbench.
- Reuses the accepted SessionProcess and PreviewProxy architecture but delivers previously deferred runtime behavior.
- `code-server` increases Worker image size and shares the fixed Runtime cgroup with Codex and LiteLLM; IDE start therefore requires capacity checks and must fail without destabilizing Agent execution.
- The first single-user version does not attribute individual file writes or terminal commands to the IDE. It records process/access lifecycle and marks possible mixed provenance.
- Microsoft VS Code Server is not an implementation option because it is not licensed for hosted service use; Microsoft Marketplace access is also excluded.
- This change follows `worker-workbench-interaction-hardening` and does not block M2 archive.
