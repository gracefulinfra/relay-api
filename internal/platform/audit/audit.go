// Package audit writes platform.audit_events. Every mutating operation records one event in the same
// transaction as its change (db.Querier), so a change and its audit trail commit or roll back together.
//
// Before and After are small field-level maps chosen by the caller. They must never carry tokens,
// secrets, or personal data beyond what the audited resource itself is (a grant's role, not an email).
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/google/uuid"

	"github.com/gracefulinfra/relay-api/internal/platform/audit/auditdb"
	"github.com/gracefulinfra/relay-api/internal/platform/auth"
	"github.com/gracefulinfra/relay-api/internal/platform/db"
	"github.com/gracefulinfra/relay-api/internal/platform/requestid"
)

// Event is one audited action.
type Event struct {
	// Actor is the staff user who acted; uuid.Nil for system actions (for example the bootstrap command).
	Actor        uuid.UUID
	Action       string // dotted verb, for example "show_grant.put"
	ResourceType string
	ResourceID   string
	Before       map[string]any
	After        map[string]any
}

// Write inserts the event using q, which must be the request transaction (db.Querier) for mutating
// requests. The request ID and client address come from ctx.
func Write(ctx context.Context, q db.Queryer, e Event) error {
	before, err := marshal(e.Before)
	if err != nil {
		return err
	}
	after, err := marshal(e.After)
	if err != nil {
		return err
	}
	var ip *netip.Addr
	if a, ok := auth.ClientIPFrom(ctx); ok {
		ip = &a
	}
	err = auditdb.New(q).InsertAuditEvent(ctx, auditdb.InsertAuditEventParams{
		ActorID:      uuid.NullUUID{UUID: e.Actor, Valid: e.Actor != uuid.Nil},
		Action:       e.Action,
		ResourceType: e.ResourceType,
		ResourceID:   e.ResourceID,
		Before:       before,
		After:        after,
		RequestID:    requestid.From(ctx),
		Ip:           ip,
	})
	if err != nil {
		return fmt.Errorf("audit: write %s: %w", e.Action, err)
	}
	return nil
}

func marshal(m map[string]any) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("audit: encode diff: %w", err)
	}
	return b, nil
}
