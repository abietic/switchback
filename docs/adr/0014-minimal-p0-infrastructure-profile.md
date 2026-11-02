# Minimal P0 Infrastructure Profile

P0 uses GitHub, Docker Engine/Compose, PostgreSQL transactional outbox, a local filesystem ObjectStore adapter, LiteLLM, and OpenViking, with Langfuse optional for local observability. GitLab, RocketMQ, an S3-compatible endpoint, and Kubernetes are P1 integration profiles; Jenkins, MongoDB, and archived MinIO are not default dependencies. This keeps P0 focused on real Codex execution, session continuity, Trace, SSE, and report/patch delivery while preserving ports for later infrastructure replacement.
