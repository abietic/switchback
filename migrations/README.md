# Database Migrations

Production PostgreSQL migrations use six-digit monotonic names such as
`000001_initial.sql`. Every file contains exact `-- migrate:up` and
`-- migrate:down` markers. `make migration-check` verifies this contract before
any migration runner executes SQL.

Migration `000029_runtime_source_plan_binding.sql` pins canonical source-plan
bytes, digest and time on the original consumed execution row. Pins remain
immutable through preparation failure, cleanup and release; rollback refuses
to erase this durable evidence. This binding does not reserve physical growth
debt or grant runtime execution.
