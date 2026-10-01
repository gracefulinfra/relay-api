-- name: DeleteExpiredIdempotencyKey :exec
-- Lazy expiry for one key, so a key past its TTL starts fresh even before the cleanup job runs.
DELETE FROM platform.idempotency_keys
WHERE actor = $1 AND method = $2 AND path = $3 AND key = $4 AND expires_at <= now();

-- name: ClaimIdempotencyKey :execrows
-- Claims the key for this request. A concurrent request with the same key blocks here on the unique
-- index until the first transaction ends; 0 rows means the key was already completed.
INSERT INTO platform.idempotency_keys (actor, method, path, key, request_hash, expires_at)
VALUES ($1, $2, $3, $4, $5, now() + sqlc.arg(ttl)::interval)
ON CONFLICT DO NOTHING;

-- name: GetIdempotencyKey :one
SELECT request_hash, response_status, response_headers, response_body
FROM platform.idempotency_keys
WHERE actor = $1 AND method = $2 AND path = $3 AND key = $4
FOR SHARE;

-- name: CompleteIdempotencyKey :exec
UPDATE platform.idempotency_keys
SET response_status = $5, response_headers = $6, response_body = $7
WHERE actor = $1 AND method = $2 AND path = $3 AND key = $4;

-- name: DeleteExpiredIdempotencyKeys :execrows
-- Periodic cleanup (relay-worker), in bounded batches so it never holds long locks.
DELETE FROM platform.idempotency_keys
WHERE ctid IN (
    SELECT ctid FROM platform.idempotency_keys
    WHERE expires_at <= now()
    LIMIT sqlc.arg(batch_size)::int
);
