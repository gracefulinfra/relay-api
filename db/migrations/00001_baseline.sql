-- P1-01 baseline: one schema per relay-api module (conventions, architecture rule 3), and the
-- platform schema for cross-cutting tables. Migrations only move forward: there are no Down sections.
-- Every change must be expand/contract compatible with the replicas still running the previous
-- release (docs/migrations.md).

-- +goose Up
CREATE SCHEMA platform;
CREATE SCHEMA catalog;
CREATE SCHEMA media;
CREATE SCHEMA publishing;
CREATE SCHEMA feeds;
CREATE SCHEMA transcripts;
CREATE SCHEMA community;
CREATE SCHEMA analytics;
CREATE SCHEMA billing;
CREATE SCHEMA sponsorship;
CREATE SCHEMA distribution;

-- Stored responses for the Idempotency-Key header (OpenAPI v0 conventions). A key is scoped to the
-- actor, the method, and the request path, and binds the SHA-256 of the request body. The row is
-- inserted, and completed, in the same transaction as the request's own effects, so an effect and its
-- stored response commit together. Rows expire after RELAY_IDEMPOTENCY_TTL (24 h); expiry never
-- touches business uniqueness constraints, which live on the business tables.
CREATE TABLE platform.idempotency_keys (
    actor            text        NOT NULL,
    method           text        NOT NULL,
    path             text        NOT NULL,
    key              text        NOT NULL,
    request_hash     bytea       NOT NULL CHECK (octet_length(request_hash) = 32),
    response_status  smallint    CHECK (response_status BETWEEN 200 AND 499),
    response_headers jsonb,
    response_body    bytea,
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    PRIMARY KEY (actor, method, path, key),
    CHECK (expires_at > created_at),
    CHECK ((response_status IS NULL) = (response_body IS NULL))
);

CREATE INDEX idempotency_keys_expires_at ON platform.idempotency_keys (expires_at);
