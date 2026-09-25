SHELL := /bin/bash
.DEFAULT_GOAL := help

NAME := relay-api
IMAGE ?= ghcr.io/gracefulinfra/$(NAME)
COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
# renovate: datasource=github-releases depName=golangci/golangci-lint
GOLANGCI_LINT_VERSION ?= 2.14.0

.PHONY: help dev test lint vuln build image

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'

dev: ## Local dev loop (pending)
	@echo "PENDING: no local dev loop yet; it arrives with the first product prompt for this repo."

test: ## Run unit tests with the race detector
	go test -race -count=1 ./...

lint: ## golangci-lint (version-checked) and go vet
	@golangci-lint version 2>/dev/null | grep -q "version $(GOLANGCI_LINT_VERSION)" || { \
	  echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required (found: $$(golangci-lint version 2>/dev/null || echo none))."; \
	  echo "Install: https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run ./...

vuln: ## govulncheck (version pinned in go.mod tool directive)
	go tool govulncheck ./...

build: ## Build binaries into bin/
	go build -trimpath -o bin/ ./cmd/...

image: ## Build the image for the local platform and load it into Docker
	docker buildx build --load --build-arg VERSION=dev --build-arg COMMIT=$(COMMIT) -t $(IMAGE):dev .
