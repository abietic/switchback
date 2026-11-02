# Local observability implementation tasks

## 1. Collection foundation

- [x] 1.1 Implement bounded safe events, asynchronous Alloy sender and private snapshot-only Prometheus registry with loss/availability tests.
- [x] 1.2 Wire a separate bounded PostgreSQL sampler and internal metrics listener into Backend lifecycle, including real database replay/failure tests.

## 2. Local operational stack

- [x] 2.1 Provision digest-pinned Prometheus, Alloy, Loki and Grafana with seven-day retention, four real dashboards and local alert rules; validate native configuration and isolation.
- [x] 2.2 Add Backend-owned sanitized Worker output collection, committed lifecycle diagnostics and necessary production diagnostic links.

## 3. Physical acceptance

- [x] 3.1 Add and execute G3 real scrape/query/dashboard/alert acceptance, including exact scoped Loki/Langfuse correlation.
- [ ] 3.2 Execute G4 cold-start and paired outage matrix, loss/recovery evidence and actual retention expiry; run final regression gates and independent review.
