# relay-api

[![ci](https://github.com/gracefulinfra/relay-api/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/gracefulinfra/relay-api/actions/workflows/ci.yml)

Go modular monolith for Relay: catalog, publishing, feeds, community, analytics, and billing modules, with River job dispatch.

Part of **Relay**, a cloud-agnostic podcast network platform built as a portfolio project.
The build is driven by a prompt series; see the prompt index (`prompts/00-INDEX.md` in the planning
workspace) and the architecture decisions in
[relay-contracts/adr](https://github.com/gracefulinfra/relay-contracts/tree/main/adr).

## Status

**P1-01** added the skeleton: config, PostgreSQL and migrations, the HTTP stack (problem+json, request IDs,
tracing, access logs, CORS, auth hooks, idempotency), the ops endpoints, River, and three entrypoints. Every
operation in the pinned contract (relay-contracts `v0.1.0`) answers `501` until its slice implements it, and
every staff operation answers `401` until P1-02 adds Keycloak authentication. See [docs/platform.md](docs/platform.md).

## Quickstart

Prerequisites: Go, Docker, and make (versions in
[relay-contracts/docs/version-matrix.md](https://github.com/gracefulinfra/relay-contracts/blob/main/docs/version-matrix.md)),
plus a [relay-infra](https://github.com/gracefulinfra/relay-infra) checkout for the backing services.

```bash
# In relay-infra: PostgreSQL, S3, Keycloak (add PROFILE=observability for Grafana and Tempo)
make dev

# In relay-api: migrate, then serve the API on :8080 and ops on :9090
make dev
curl localhost:9090/readyz
curl localhost:8080/v0/public/shows   # 501 problem+json: in the contract, not built yet
```

`DEV_DB=k3d make dev` runs against the k3d cluster's database (relay-infra `make up`) through a port-forward
instead. `make dev-worker` runs the River worker alongside the API.

| Target | What it does |
| --- | --- |
| `make dev` / `make dev-worker` | Migrate, then run the API or the worker from source against the dev database |
| `make migrate` | Apply migrations (`ARGS=status` lists them) |
| `make test` | Unit and integration tests with `-race`. Integration tests start PostgreSQL with testcontainers, so they need Docker |
| `make test-unit` | Unit tests only (`-short`) |
| `make lint` | `golangci-lint` with `gosec`, `errorlint`, `revive` |
| `make vuln` | `govulncheck ./...` |
| `make generate` / `make check-generated` | sqlc queries and the 501 stubs; the check fails on drift |
| `make build` | `relay-api`, `relay-worker`, `relay-migrate` into `bin/` |
| `make image` | The image (all three binaries) for your platform |

## Layout

```text
cmd/relay-api/        HTTP API (/v0) and ops port
cmd/relay-worker/     River workers (same image, different entrypoint)
cmd/relay-migrate/    goose runner (the Argo CD Sync-hook Job)
api/                  binds the contract: 501 stubs for relay-contracts gen/go (pinned in go.mod)
internal/platform/    config, db, migrations, http middleware, idempotency, auth hooks, telemetry, health, River
internal/<module>/    catalog, media, publishing, feeds, transcripts, community, analytics, billing, sponsorship, distribution
db/migrations/        goose SQL, embedded in every binary (one schema per module)
db/queries/           sqlc queries
docs/                 platform.md, runbook.md, follow-ups.md
```

## CI

`.github/workflows/ci.yml` runs `lint`, `test` (unit and testcontainers integration tests, `-race`; it fails if the
database tests skip), `vuln`, and `generated` (drift check) on every PR and push. The `image` job builds
`linux/amd64` and `linux/arm64` images on PRs without pushing. On `main` it pushes
`ghcr.io/gracefulinfra/relay-api:<git-sha>`, signs the image with cosign keyless, and attaches a syft SPDX SBOM
as a signed attestation. See
[relay-contracts/docs/ci.md](https://github.com/gracefulinfra/relay-contracts/blob/main/docs/ci.md)
for how to verify it. relay-infra deploys it by digest.
