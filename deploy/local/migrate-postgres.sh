#!/bin/sh
set -eu

database=${PGDATABASE:-switchback}
migrations_dir=${SWITCHBACK_MIGRATIONS_DIR:-/opt/switchback/migrations}
psql_args="--set ON_ERROR_STOP=1 --dbname $database"

psql $psql_args <<'SQL'
CREATE TABLE IF NOT EXISTS switchback_schema_migrations (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);
SQL

legacy_applied() {
  version=$1
  case "$version" in
    000001) probe="to_regclass('public.worker_sessions') IS NOT NULL" ;;
    000002) probe="to_regclass('public.artifact_refs') IS NOT NULL" ;;
    000003) probe="to_regclass('public.object_reconciliation_claims') IS NOT NULL" ;;
    000004) probe="to_regclass('public.run_observability_projections') IS NOT NULL" ;;
    000005) probe="to_regclass('public.view_projections') IS NOT NULL" ;;
    000006) probe="to_regclass('public.display_checkpoints') IS NOT NULL" ;;
    000007) probe="to_regclass('public.worker_user_commands') IS NOT NULL" ;;
    000008) probe="to_regclass('public.worker_approval_requests') IS NOT NULL" ;;
    000009)
      probe="EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'run_observability_projections_model_observation_count_check'
          AND pg_get_constraintdef(oid) LIKE '%model_observation_count >= 0%'
      )"
      ;;
    000010) probe="to_regclass('public.assistant_conversations') IS NOT NULL" ;;
    *) return 1 ;;
  esac
  test "$(psql $psql_args --tuples-only --no-align --command "SELECT $probe")" = t
}

set -- "${migrations_dir}"/[0-9][0-9][0-9][0-9][0-9][0-9]_*.sql
if ! test -f "$1"; then
  printf 'no PostgreSQL migrations found in %s\n' "$migrations_dir" >&2
  exit 1
fi

for migration do
  name=${migration##*/}
  version=${name%%_*}
  if test "$(psql $psql_args --tuples-only --no-align \
    --command "SELECT EXISTS (SELECT 1 FROM switchback_schema_migrations WHERE version = '$version')")" = t; then
    continue
  fi
  if legacy_applied "$version"; then
    psql $psql_args --command \
      "INSERT INTO switchback_schema_migrations(version) VALUES ('$version') ON CONFLICT DO NOTHING" >/dev/null
    continue
  fi
  {
    printf '%s\n' 'BEGIN;'
    awk '/^-- migrate:up$/ { enabled=1; next } /^-- migrate:down$/ { enabled=0 } enabled' "$migration"
    printf "INSERT INTO switchback_schema_migrations(version) VALUES ('%s');\n" "$version"
    printf '%s\n' 'COMMIT;'
  } | psql $psql_args >/dev/null
done

printf 'switchback_schema_version=%s\n' \
  "$(psql $psql_args --tuples-only --no-align --command 'SELECT max(version) FROM switchback_schema_migrations')"
