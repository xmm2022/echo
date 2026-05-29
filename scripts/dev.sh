#!/usr/bin/env sh
set -eu

CONFIG_PATH="${ECHO_CONFIG_PATH:-config.example.yaml}"

mkdir -p /tmp/echo/data /tmp/echo/ingest /tmp/echo/secrets /tmp/echo/output

export ECHO_ADMIN_TOKEN="${ECHO_ADMIN_TOKEN:-dev-admin-token}"
export ECHO_DATABASE_PATH="${ECHO_DATABASE_PATH:-/tmp/echo/data/echo.db}"
export ECHO_PRODUCER_WORKDIR_ROOT="${ECHO_PRODUCER_WORKDIR_ROOT:-/tmp/echo/ingest}"
export ECHO_PRODUCER_SECRETS_ROOT="${ECHO_PRODUCER_SECRETS_ROOT:-/tmp/echo/secrets}"
export ECHO_OUTPUT_DEFAULTS_BASE_PATH="${ECHO_OUTPUT_DEFAULTS_BASE_PATH:-/tmp/echo/output}"
export ECHO_MANUAL_IMPORT_ROOTS="${ECHO_MANUAL_IMPORT_ROOTS:-}"

exec go run ./cmd/echo serve --config "$CONFIG_PATH"
