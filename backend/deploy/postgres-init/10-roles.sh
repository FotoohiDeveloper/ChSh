#!/bin/sh
# Runs once, when the database volume is first created.
# POSTGRES_USER (chador_owner) owns the schema and is used only by the migrate step.
# chador_app is what the API and worker connect as: no DDL, no superuser, and
# (after migration) no UPDATE/DELETE on the audit and event tables.
set -eu
APP_PW="$(cat /run/secrets/app_db_password)"
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<SQL
CREATE ROLE chador_app LOGIN PASSWORD '${APP_PW}' NOSUPERUSER NOCREATEDB NOCREATEROLE;
REVOKE ALL ON DATABASE ${POSTGRES_DB} FROM PUBLIC;
GRANT CONNECT ON DATABASE ${POSTGRES_DB} TO chador_app;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SQL
