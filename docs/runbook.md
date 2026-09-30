# relay-api runbook

Each entry: the symptom, how to confirm it, and what to do.

## The migration Job failed (Argo CD sync failed, "SyncFailed" on relay-api)

- **Confirm**: `kubectl -n relay logs job/relay-api-migrate`. The failing migration and the PostgreSQL
  error are in the last `migration` log lines.
- **Impact**: none on serving traffic. The Deployments are ordered after the Job, so the previous release
  keeps running against the last good schema. Each migration runs in its own transaction, so nothing is half-applied.
- **Fix**: correct the migration in a new relay-api release (migrations are forward-only; never edit an
  applied one) and sync again. `relay-migrate status` (`kubectl -n relay exec deploy/relay-api -- /relay-migrate status`)
  shows which versions are applied.

## Pods are not ready (`/readyz` 503)

- **Confirm**: `kubectl -n relay port-forward deploy/relay-api 9090` and `curl localhost:9090/readyz`. The body
  names the failing check.
- `database`: PostgreSQL is unreachable. Check the CNPG cluster (`kubectl -n relay-db get cluster relay`)
  and the NetworkPolicy egress to `relay-db`.
- `migrations: pending migrations`: this binary expects a newer schema than the database has. The migration
  Job did not run or failed: see above.
- `river` (worker only): the River client stopped. The worker log says why. The pod restarts once liveness
  fails, and River resumes its jobs.

## Clients get `409 idempotency-key-in-progress`

A request with the same key was still running after `RELAY_IDEMPOTENCY_LOCK_TIMEOUT`. Clients retry after
`Retry-After`. If it persists, look for long-running requests: `SELECT pid, now() - xact_start, query FROM
pg_stat_activity WHERE application_name = 'relay-api' ORDER BY 2 DESC`.

## `platform.idempotency_keys` keeps growing

The cleanup job is not running. Check the worker is ready and look for `platform.idempotency_cleanup` in
`SELECT state, count(*) FROM river.river_job WHERE kind = 'platform.idempotency_cleanup' GROUP BY 1`.
`relay_idempotency_keys_expired_total` on the worker's `/metrics` should rise over time.
