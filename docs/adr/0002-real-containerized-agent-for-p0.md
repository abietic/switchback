# Real Containerized Coding Agent For P0

P0 will use a real Coding Agent running inside an isolated container, and the backend will communicate with that runtime over the network. We are not treating a mock Worker as sufficient because the product risk is in real Agent execution, container isolation, session continuity, trace capture, and artifact production, not only in event plumbing.

## Status

Accepted as draft baseline.
