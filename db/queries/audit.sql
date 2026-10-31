-- name: InsertAuditEvent :exec
INSERT INTO platform.audit_events (actor_id, action, resource_type, resource_id, before, after, request_id, ip)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListAuditEventsForResource :many
SELECT id, occurred_at, actor_id, action, resource_type, resource_id, before, after, request_id, ip
FROM platform.audit_events
WHERE resource_type = $1 AND resource_id = $2
ORDER BY id;
