SHELL := /bin/bash
.DEFAULT_GOAL := help

NAME := relay-api
IMAGE ?= ghcr.io/gracefulinfra/$(NAME)
COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
# renovate: datasource=github-releases depName=golangci/golangci-lint
GOLANGCI_LINT_VERSION ?= 2.14.0
# sqlc runs from its image so its cgo parser stays out of go.mod. Keep in step with relay-contracts docs/version-matrix.md.
SQLC_IMAGE ?= docker.io/sqlc/sqlc:1.31.1@sha256:70f53171d27b2424e9358869975455a6e955a5aa8e58a998a270a6e34e525537

.PHONY: help dev dev-worker migrate test test-unit lint vuln generate check-generated build image

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

dev: ## Migrate, then run the API on :8080 (ops :9090) against the relay-infra dev stack (DEV_DB=k3d for the cluster)
	scripts/dev.sh api

dev-worker: ## Migrate, then run the River worker (ops :9091)
	scripts/dev.sh worker

migrate: ## Apply migrations to the dev database (ARGS=status to list them)
	scripts/dev.sh migrate $(ARGS)

test: ## Unit and integration tests with the race detector (integration tests need Docker)
	go test -race -count=1 ./...

test-unit: ## Unit tests only (no Docker)
	go test -race -count=1 -short ./...

lint: ## golangci-lint (version-checked) and go vet
	@golangci-lint version 2>/dev/null | grep -q "version $(GOLANGCI_LINT_VERSION)" || { \
	  echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required (found: $$(golangci-lint version 2>/dev/null || echo none))."; \
	  echo "Install: https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run ./...

vuln: ## govulncheck (version pinned in go.mod tool directive)
	go tool govulncheck ./...

generate: ## sqlc queries and the 501 stubs for the pinned contract
	docker run --rm -u "$$(id -u):$$(id -g)" -v "$(CURDIR):/src" -w /src $(SQLC_IMAGE) generate
	go generate ./api/...

check-generated: generate ## Fail if generated code differs from what is committed
	@git diff --exit-code -- internal/platform/idempotency/idempotencydb api/unimplemented.gen.go || { \
	  echo "Generated code is stale: run make generate and commit the result."; exit 1; }

build: ## Build relay-api, relay-worker, and relay-migrate into bin/
	go build -trimpath -o bin/ ./cmd/...

image: ## Build the image for the local platform and load it into Docker
	docker buildx build --load --build-arg VERSION=dev --build-arg COMMIT=$(COMMIT) -t $(IMAGE):dev .
