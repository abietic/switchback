#!/bin/sh
set -eu

psql --set ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<'SQL'
CREATE DATABASE switchback;
CREATE DATABASE langfuse;
SQL

PGUSER="$POSTGRES_USER" PGDATABASE=switchback /opt/switchback/migrate-postgres.sh
