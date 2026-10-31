-- name: GetStaffUserByIdentity :one
SELECT * FROM platform.staff_users WHERE issuer = $1 AND subject = $2;

-- name: GetStaffUser :one
SELECT * FROM platform.staff_users WHERE id = $1;

-- name: ProvisionStaffUser :one
-- First sign-in: disabled until an admin grants a role. Losing a race to a concurrent first request
-- returns no row; the caller re-reads.
INSERT INTO platform.staff_users (issuer, subject, display_name)
VALUES ($1, $2, $3)
ON CONFLICT (issuer, subject) DO NOTHING
RETURNING *;

-- name: ListStaffUsers :many
SELECT * FROM platform.staff_users
WHERE (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after)::uuid)
ORDER BY id
LIMIT sqlc.arg(max_rows);

-- name: CountNetworkAdmins :one
SELECT count(*) FROM platform.staff_users WHERE network_admin;

-- name: SetNetworkAdmin :execrows
UPDATE platform.staff_users
SET network_admin = true, enabled = true, updated_at = now()
WHERE id = $1;

-- name: EnableStaffUser :execrows
UPDATE platform.staff_users SET enabled = true, updated_at = now() WHERE id = $1 AND NOT enabled;

-- name: ListGrantsForUser :many
SELECT * FROM platform.show_role_grants WHERE staff_user_id = $1 ORDER BY show_id;

-- name: ListGrantsForShow :many
SELECT * FROM platform.show_role_grants
WHERE show_id = sqlc.arg(show_id) AND (sqlc.narg(after)::uuid IS NULL OR staff_user_id > sqlc.narg(after)::uuid)
ORDER BY staff_user_id
LIMIT sqlc.arg(max_rows);

-- name: GetGrant :one
SELECT * FROM platform.show_role_grants WHERE staff_user_id = $1 AND show_id = $2
FOR UPDATE;

-- name: UpsertGrant :one
INSERT INTO platform.show_role_grants (staff_user_id, show_id, role, granted_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (staff_user_id, show_id) DO UPDATE
SET role = EXCLUDED.role, granted_by = EXCLUDED.granted_by, granted_at = now()
RETURNING *;

-- name: DeleteGrant :execrows
DELETE FROM platform.show_role_grants WHERE staff_user_id = $1 AND show_id = $2;
