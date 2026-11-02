# CI/CD-Style Local Container Runtime

The P0 Worker runtime will follow the traditional CI/CD runner pattern: a host-side Worker service schedules a job/session into a Docker-compatible local container, injects workspace, environment, credentials, skills, and agent state, then collects trace, artifacts, and exit status. This mirrors GitHub Actions job containers, GitLab Runner Docker executor, and Jenkins Docker Pipeline while preserving a path to K8s or an internal sandbox later.

## Status

Accepted as draft baseline.
