package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gracefulinfra/relay-api/internal/platform/access/accessdb"
	"github.com/gracefulinfra/relay-api/internal/platform/audit"
)

// ErrAdminExists is returned by Bootstrap once the network has an admin: the command works exactly once.
var ErrAdminExists = errors.New("a network admin already exists; bootstrap is disabled")

// ErrUnknownUser is returned when the person has not signed in yet, so no staff user exists for them.
var ErrUnknownUser = errors.New("no staff user for that issuer and subject: sign in once first")

// Bootstrap makes the staff user identified by issuer plus subject the first network admin, and enables
// them. It refuses when any network admin exists (the check and the grant run in one transaction that
// holds an advisory lock, so two concurrent runs cannot both succeed) and writes an audit event with no
// actor. This is the only way to create the first admin; every other account stays disabled until an
// admin grants a role.
func Bootstrap(ctx context.Context, pool *pgxpool.Pool, issuer, subject string) (uuid.UUID, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("bootstrap: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// One fixed key serializes concurrent bootstraps; released at commit or rollback.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('relay.bootstrap-admin'))"); err != nil {
		return uuid.Nil, fmt.Errorf("bootstrap: lock: %w", err)
	}
	q := accessdb.New(tx)
	n, err := q.CountNetworkAdmins(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("bootstrap: count: %w", err)
	}
	if n > 0 {
		return uuid.Nil, ErrAdminExists
	}
	u, err := q.GetStaffUserByIdentity(ctx, accessdb.GetStaffUserByIdentityParams{Issuer: issuer, Subject: subject})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrUnknownUser
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("bootstrap: lookup: %w", err)
	}
	if _, err := q.SetNetworkAdmin(ctx, u.ID); err != nil {
		return uuid.Nil, fmt.Errorf("bootstrap: grant: %w", err)
	}
	if err := audit.Write(ctx, tx, audit.Event{
		Action: "staff_user.bootstrap_admin", ResourceType: "staff_user", ResourceID: u.ID.String(),
		Before: map[string]any{"networkAdmin": u.NetworkAdmin, "enabled": u.Enabled},
		After:  map[string]any{"networkAdmin": true, "enabled": true},
	}); err != nil {
		return uuid.Nil, err
	}
	return u.ID, tx.Commit(ctx)
}
