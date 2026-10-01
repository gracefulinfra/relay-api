#!/usr/bin/env bash
# Run relay-api or relay-worker from source against a local PostgreSQL, applying migrations first.
#
#   scripts/dev.sh api|worker|migrate|env
#
# DEV_DB=compose (default): the relay-infra Compose dev stack (`make dev` in relay-infra, ADR 0009).
#   Credentials come from $RELAY_HOME/dev/.env, which that stack generates.
# DEV_DB=k3d: the k3d cluster's CNPG `relay` database (`make up` in relay-infra), through a
#   kubectl port-forward that this script starts and stops.
#
# Traces go to the collector if one listens on 127.0.0.1:4317 (relay-infra `make dev PROFILE=observability`).
set -euo pipefail

RELAY_HOME="${RELAY_HOME:-$HOME/.relay-local}"
DEV_DB="${DEV_DB:-compose}"
K3D_CONTEXT="${K3D_CONTEXT:-k3d-relay}"
K3D_PORT="${K3D_PORT:-25432}"
cmd="${1:-api}"

die() { echo "dev.sh: $*" >&2; exit 1; }

case "$DEV_DB" in
compose)
  env_file="$RELAY_HOME/dev/.env"
  [ -f "$env_file" ] || die "$env_file not found: run \`make dev\` in relay-infra first"
  # shellcheck disable=SC1090
  RELAY_DB_PASSWORD="$(set -a; source "$env_file"; echo "$RELAY_DB_PASSWORD")"
  export RELAY_DATABASE_URL="postgres://relay:${RELAY_DB_PASSWORD}@127.0.0.1:15432/relay?sslmode=disable"
  ;;
k3d)
  command -v kubectl >/dev/null || die "kubectl is required for DEV_DB=k3d"
  pw="$(kubectl --context "$K3D_CONTEXT" -n relay-db get secret relay-app -o jsonpath='{.data.password}' | base64 -d)" \
    || die "cannot read secret relay-db/relay-app: is the k3d cluster up (relay-infra \`make up\`)?"
  kubectl --context "$K3D_CONTEXT" -n relay-db port-forward svc/relay-rw "$K3D_PORT:5432" >/dev/null &
  pf=$!
  trap 'kill $pf 2>/dev/null || true' EXIT
  for _ in $(seq 1 20); do nc -z 127.0.0.1 "$K3D_PORT" 2>/dev/null && break; sleep 0.5; done
  export RELAY_DATABASE_URL="postgres://relay:${pw}@127.0.0.1:${K3D_PORT}/relay?sslmode=require"
  ;;
*) die "DEV_DB must be compose or k3d" ;;
esac

if nc -z 127.0.0.1 4317 2>/dev/null; then
  export OTEL_TRACES_EXPORTER="${OTEL_TRACES_EXPORTER:-otlp}"
  export OTEL_EXPORTER_OTLP_ENDPOINT="${OTEL_EXPORTER_OTLP_ENDPOINT:-http://127.0.0.1:4317}"
else
  export OTEL_TRACES_EXPORTER="${OTEL_TRACES_EXPORTER:-none}"
fi
export RELAY_CORS_ALLOWED_ORIGINS="${RELAY_CORS_ALLOWED_ORIGINS:-http://localhost:5173}"
export RELAY_LOG_LEVEL="${RELAY_LOG_LEVEL:-debug}"

case "$cmd" in
api | worker)
  go run ./cmd/relay-migrate up
  # relay-worker serves ops on another port so both can run side by side.
  [ "$cmd" = worker ] && export RELAY_OPS_ADDR="${RELAY_OPS_ADDR:-:9091}"
  go run "./cmd/relay-$cmd"
  ;;
migrate) go run ./cmd/relay-migrate "${@:2}" ;;
env) go run ./cmd/relay-api --print-config ;;
*) die "usage: scripts/dev.sh api|worker|migrate [status]|env" ;;
esac
