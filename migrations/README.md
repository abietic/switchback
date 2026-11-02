# Database Migrations

Production PostgreSQL migrations use six-digit monotonic names such as
`000001_initial.sql`. Every file contains exact `-- migrate:up` and
`-- migrate:down` markers. `make migration-check` verifies this contract before
any migration runner executes SQL.
