# relay-api runbook

Each entry: the symptom, how to confirm it, and what to do.

## The migration Job failed (Argo CD sync failed, "SyncFailed" on relay-api)

- **Confirm**: `kubectl -n relay logs job/relay-api-migrate`. The failing migration and the PostgreSQL
  error are in the last `migration` log lines.
- **Impact**: none on serving traffic. The Deployments are ordered after the Job, so the previous release
  keeps running against the last good schema. Each migration runs in its own transaction, so nothing is half-applied.
- **Fix**: correct the migration in a new relay-api release (migrations are forward-only; never edit an
  applied one) and sync again. To back out instead, set `image.digest` back to the previous release: the
  live Deployments never changed, so Argo CD reports Synced at once and skips auto-sync. The failed Job
  stays until the next sync, so run `argocd app sync relay-api` to replace it with a green one. Do not
  delete the Application's `.operation` by hand: that orphans the running operation. Use
  `argocd app terminate-op relay-api` if one is stuck. `relay-migrate status` (`kubectl -n relay exec deploy/relay-api -- /relay-migrate status`)
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

## Staff get `401` or `403`

- **`401` for everyone:** the API cannot validate tokens. Check `RELAY_OIDC_ISSUER` equals the token's `iss`
  exactly (Keycloak's public URL, `KC_HOSTNAME`), and that the pod can reach `RELAY_OIDC_JWKS_URL`
  (NetworkPolicy `relay-api-api` allows Keycloak :8080). Debug logs say `token rejected` with a class
  (`expired`, `issuer mismatch`, `audience`, `signature or algorithm`, `not-before`), never the token.
  After a Keycloak key rotation no restart is needed; an unknown `kid` triggers a JWKS refetch.
- **`403` for one person, on everything:** their account is disabled. They appear in `GET /staff-users`;
  an admin grants a role on a show (`PUT /shows/{showId}/grants/{staffUserId}`), which enables them.
- **`403` on one show:** they lack the role the operation needs on that show (docs/access.md).
- **No admin exists** (fresh install): `relay-api bootstrap-admin --subject <id>` after that person signs in
  once. It refuses when an admin exists; recovery from a lost admin is a database update by the operator
  (`UPDATE platform.staff_users SET network_admin = true, enabled = true WHERE ...`) plus a manual
  `platform.audit_events` row.
