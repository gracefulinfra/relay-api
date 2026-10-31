// Package access implements staff users and show-scoped role grants: GET /me, GET /staff-users, and
// the show grant operations. Authentication and the authorization decision live in staffauth; this
// package is what an admin does with them.
//
// A staff user is provisioned disabled on first sign-in. Putting a grant enables the user; the first
// network admin comes from Bootstrap. Every mutation writes its audit event in the request transaction.
package access

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oapi-codegen/nullable"

	"github.com/gracefulinfra/relay-api/internal/platform/access/accessdb"
	"github.com/gracefulinfra/relay-api/internal/platform/audit"
	"github.com/gracefulinfra/relay-api/internal/platform/auth"
	"github.com/gracefulinfra/relay-api/internal/platform/db"
	"github.com/gracefulinfra/relay-api/internal/platform/problem"
	relayapi "github.com/gracefulinfra/relay-contracts/gen/go/relayapi"
)

const defaultLimit, maxLimit = 25, 100

// Service implements the access operations of the contract.
type Service struct {
	Pool *pgxpool.Pool
	// ShowExists reports whether a show exists. The catalog module supplies it in P1-03; until then it is
	// nil and any show ID is accepted (grants hold no foreign key to a table this module may not read).
	ShowExists func(ctx context.Context, id uuid.UUID) (bool, error)
}

func actorID(ctx context.Context) (uuid.UUID, error) {
	a, ok := auth.ActorFrom(ctx)
	if !ok {
		return uuid.Nil, problem.Err(problem.Unauthorized())
	}
	id, err := uuid.Parse(a.ID)
	if err != nil {
		return uuid.Nil, problem.Err(problem.Unauthorized())
	}
	return id, nil
}

func toStaffUser(u accessdb.PlatformStaffUser) relayapi.StaffUser {
	return relayapi.StaffUser{Id: u.ID, DisplayName: u.DisplayName, NetworkAdmin: u.NetworkAdmin, CreatedAt: u.CreatedAt}
}

func toGrant(g accessdb.PlatformShowRoleGrant) relayapi.ShowRoleGrant {
	return relayapi.ShowRoleGrant{ShowId: g.ShowID, StaffUserId: g.StaffUserID, Role: relayapi.ShowRole(g.Role), GrantedAt: g.GrantedAt}
}

// GetMe implements getMe.
func (s *Service) GetMe(ctx context.Context, _ relayapi.GetMeRequestObject) (relayapi.GetMeResponseObject, error) {
	id, err := actorID(ctx)
	if err != nil {
		return nil, err
	}
	q := accessdb.New(s.Pool)
	u, err := q.GetStaffUser(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get staff user: %w", err)
	}
	grants, err := q.ListGrantsForUser(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list grants: %w", err)
	}
	out := relayapi.GetMe200JSONResponse{StaffUser: toStaffUser(u), Grants: make([]relayapi.ShowRoleGrant, 0, len(grants))}
	for _, g := range grants {
		out.Grants = append(out.Grants, toGrant(g))
	}
	return out, nil
}

// ListStaffUsers implements listStaffUsers. Disabled users are included: an admin finds a new
// sign-in here in order to grant it a role.
func (s *Service) ListStaffUsers(ctx context.Context, req relayapi.ListStaffUsersRequestObject) (relayapi.ListStaffUsersResponseObject, error) {
	after, limit, err := page(req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(s.Pool).ListStaffUsers(ctx, accessdb.ListStaffUsersParams{After: after, MaxRows: int32(limit) + 1}) //nolint:gosec // limit <= 100
	if err != nil {
		return nil, fmt.Errorf("list staff users: %w", err)
	}
	next := nullable.NewNullNullable[string]()
	if len(rows) > limit {
		rows = rows[:limit]
		next = nullable.NewNullableWithValue(encodeCursor(rows[limit-1].ID))
	}
	out := relayapi.ListStaffUsers200JSONResponse{Items: make([]relayapi.StaffUser, 0, len(rows)), NextCursor: next}
	for _, u := range rows {
		out.Items = append(out.Items, toStaffUser(u))
	}
	return out, nil
}

// ListShowGrants implements listShowGrants.
func (s *Service) ListShowGrants(ctx context.Context, req relayapi.ListShowGrantsRequestObject) (relayapi.ListShowGrantsResponseObject, error) {
	after, limit, err := page(req.Params.Cursor, req.Params.Limit)
	if err != nil {
		return nil, err
	}
	if err := s.requireShow(ctx, req.ShowId); err != nil {
		return nil, err
	}
	rows, err := accessdb.New(s.Pool).ListGrantsForShow(ctx, accessdb.ListGrantsForShowParams{ShowID: req.ShowId, After: after, MaxRows: int32(limit) + 1}) //nolint:gosec // limit <= 100
	if err != nil {
		return nil, fmt.Errorf("list show grants: %w", err)
	}
	next := nullable.NewNullNullable[string]()
	if len(rows) > limit {
		rows = rows[:limit]
		next = nullable.NewNullableWithValue(encodeCursor(rows[limit-1].StaffUserID))
	}
	out := relayapi.ListShowGrants200JSONResponse{Items: make([]relayapi.ShowRoleGrant, 0, len(rows)), NextCursor: next}
	for _, g := range rows {
		out.Items = append(out.Items, toGrant(g))
	}
	return out, nil
}

// PutShowGrant implements putShowGrant: create or change a grant, and enable the user.
func (s *Service) PutShowGrant(ctx context.Context, req relayapi.PutShowGrantRequestObject) (relayapi.PutShowGrantResponseObject, error) {
	actor, err := actorID(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil || !validRole(req.Body.Role) {
		return nil, problem.Err(problem.BadRequest("role must be one of show_editor, producer, viewer"))
	}
	if err := s.requireShow(ctx, req.ShowId); err != nil {
		return nil, err
	}
	dbq := db.Querier(ctx, s.Pool)
	q := accessdb.New(dbq)
	target, err := q.GetStaffUser(ctx, req.StaffUserId)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, problem.Err(problem.NotFound("No such staff user."))
	}
	if err != nil {
		return nil, fmt.Errorf("get staff user: %w", err)
	}

	var before map[string]any
	old, err := q.GetGrant(ctx, accessdb.GetGrantParams{StaffUserID: req.StaffUserId, ShowID: req.ShowId})
	switch {
	case err == nil:
		before = map[string]any{"role": old.Role}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("get grant: %w", err)
	}
	g, err := q.UpsertGrant(ctx, accessdb.UpsertGrantParams{
		StaffUserID: req.StaffUserId, ShowID: req.ShowId, Role: string(req.Body.Role),
		GrantedBy: uuid.NullUUID{UUID: actor, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("upsert grant: %w", err)
	}
	if err := audit.Write(ctx, dbq, audit.Event{
		Actor: actor, Action: "show_grant.put", ResourceType: "show_grant", ResourceID: grantResource(req.ShowId, req.StaffUserId),
		Before: before, After: map[string]any{"role": g.Role},
	}); err != nil {
		return nil, err
	}
	if !target.Enabled {
		if _, err := q.EnableStaffUser(ctx, target.ID); err != nil {
			return nil, fmt.Errorf("enable staff user: %w", err)
		}
		if err := audit.Write(ctx, dbq, audit.Event{
			Actor: actor, Action: "staff_user.enable", ResourceType: "staff_user", ResourceID: target.ID.String(),
			Before: map[string]any{"enabled": false}, After: map[string]any{"enabled": true},
		}); err != nil {
			return nil, err
		}
	}
	return relayapi.PutShowGrant200JSONResponse(toGrant(g)), nil
}

// DeleteShowGrant implements deleteShowGrant.
func (s *Service) DeleteShowGrant(ctx context.Context, req relayapi.DeleteShowGrantRequestObject) (relayapi.DeleteShowGrantResponseObject, error) {
	actor, err := actorID(ctx)
	if err != nil {
		return nil, err
	}
	dbq := db.Querier(ctx, s.Pool)
	q := accessdb.New(dbq)
	old, err := q.GetGrant(ctx, accessdb.GetGrantParams{StaffUserID: req.StaffUserId, ShowID: req.ShowId})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, problem.Err(problem.NotFound("No such grant."))
	}
	if err != nil {
		return nil, fmt.Errorf("get grant: %w", err)
	}
	if _, err := q.DeleteGrant(ctx, accessdb.DeleteGrantParams{StaffUserID: req.StaffUserId, ShowID: req.ShowId}); err != nil {
		return nil, fmt.Errorf("delete grant: %w", err)
	}
	if err := audit.Write(ctx, dbq, audit.Event{
		Actor: actor, Action: "show_grant.revoke", ResourceType: "show_grant", ResourceID: grantResource(req.ShowId, req.StaffUserId),
		Before: map[string]any{"role": old.Role},
	}); err != nil {
		return nil, err
	}
	return relayapi.DeleteShowGrant204Response{}, nil
}

func (s *Service) requireShow(ctx context.Context, id uuid.UUID) error {
	if s.ShowExists == nil {
		return nil
	}
	ok, err := s.ShowExists(ctx, id)
	if err != nil {
		return fmt.Errorf("check show: %w", err)
	}
	if !ok {
		return problem.Err(problem.NotFound("No such show."))
	}
	return nil
}

func validRole(r relayapi.ShowRole) bool { return r.Valid() }

func grantResource(show, user uuid.UUID) string { return show.String() + "/" + user.String() }

func page(cursor *string, limit *int) (after uuid.NullUUID, n int, err error) {
	n = defaultLimit
	if limit != nil {
		n = min(max(*limit, 1), maxLimit)
	}
	if cursor != nil && *cursor != "" {
		raw, derr := base64.RawURLEncoding.DecodeString(*cursor)
		id, perr := uuid.FromBytes(raw)
		if derr != nil || perr != nil {
			return after, 0, problem.Err(problem.BadRequest("cursor is not valid"))
		}
		after = uuid.NullUUID{UUID: id, Valid: true}
	}
	return after, n, nil
}

func encodeCursor(id uuid.UUID) string { return base64.RawURLEncoding.EncodeToString(id[:]) }
