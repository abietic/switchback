#!/bin/sh
# Future first-init only: core database, no observability databases.
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<'SQL'
CREATE DATABASE switchback;
SQL
