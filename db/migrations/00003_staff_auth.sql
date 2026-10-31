-- P1-02: staff identity, show-scoped role grants, and the audit log. Forward only; every change is
-- expand/contract compatible with the replicas still running the previous release (docs/migrations.md).

-- +goose Up
-- A staff user is keyed by issuer plus subject, never by subject alone (one subject value can exist in
-- two realms). Users are provisioned on first sign-in, disabled until an admin grants a role (or the
-- bootstrap command makes them network admin). Only what authorization and the UI need is stored: no
-- email, no tokens.
CREATE TABLE platform.staff_users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    issuer        text        NOT NULL CHECK (issuer <> ''),
    subject       text        NOT NULL CHECK (subject <> ''),
    display_name  text        NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 200),
    network_admin boolean     NOT NULL DEFAULT false,
    enabled       boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (issuer, subject)
);

-- OpenAPI v0 ShowRole. show_id has no foreign key yet: catalog.shows arrives with P1-03, and a module
-- never reads another module's tables (architecture rule 3); P1-03 validates the show through the
-- catalog interface before a grant is written.
CREATE TABLE platform.show_role_grants (
    staff_user_id uuid        NOT NULL REFERENCES platform.staff_users (id),
    show_id       uuid        NOT NULL,
    role          text        NOT NULL CHECK (role IN ('show_editor', 'producer', 'viewer')),
    granted_by    uuid        REFERENCES platform.staff_users (id),
    granted_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (staff_user_id, show_id)
);
CREATE INDEX show_role_grants_show ON platform.show_role_grants (show_id);

-- Append-only by convention: no code path updates or deletes a row (a database-role restriction is a
-- P1-19 hardening item). before/after hold a field-level diff of non-secret attributes; callers never put tokens, secrets, or personal data
-- in them.
CREATE TABLE platform.audit_events (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    actor_id      uuid,
    action        text        NOT NULL CHECK (action <> ''),
    resource_type text        NOT NULL,
    resource_id   text        NOT NULL,
    before        jsonb,
    after         jsonb,
    request_id    text        NOT NULL DEFAULT '',
    ip            inet
);
CREATE INDEX audit_events_resource ON platform.audit_events (resource_type, resource_id, id DESC);
CREATE INDEX audit_events_actor ON platform.audit_events (actor_id, id DESC);
