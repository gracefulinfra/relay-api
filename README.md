# relay-api

Go modular monolith for Relay: catalog, publishing, feeds, community, analytics, and billing modules, with River job dispatch.

Part of **Relay**, a cloud-agnostic podcast network platform built as a portfolio project.
The build is driven by a prompt series; see the prompt index (`prompts/00-INDEX.md` in the planning
workspace) and the architecture decisions in
[relay-contracts/adr](https://github.com/gracefulinfra/relay-contracts/tree/main/adr).

## Status

Bootstrapped by **P0-01**. This repo contains only the CI and tooling skeleton; there is no product code yet.

## Quickstart

Prerequisites: see [relay-contracts/docs/version-matrix.md](https://github.com/gracefulinfra/relay-contracts/blob/main/docs/version-matrix.md).

```bash
git clone https://github.com/gracefulinfra/relay-api.git
cd relay-api
make test
make lint
```

| Target | What it does today |
| --- | --- |
| `make test` | `go test -race ./...` (build-info unit tests only) |
| `make lint` | `golangci-lint run` with `gosec`, `errorlint`, `revive` |
| `make vuln` | `govulncheck ./...` |
| `make build` | Builds the smoke binary into `bin/` |
| `make image` | Builds the smoke image for your platform |
| `make dev` | Pending: prints a notice and exits 0 |

## CI

`.github/workflows/ci.yml` runs `lint`, `test`, and `vuln` on every PR and push. The `image` job builds
`linux/amd64` and `linux/arm64` images on PRs without pushing. On `main` it pushes
`ghcr.io/gracefulinfra/relay-api:<git-sha>`, signs the image with cosign keyless, and attaches a syft SPDX SBOM
as a signed attestation. See
[relay-contracts/docs/ci.md](https://github.com/gracefulinfra/relay-contracts/blob/main/docs/ci.md)
for how to verify it.
