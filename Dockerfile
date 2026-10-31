# syntax=docker/dockerfile:1
# One image, three entrypoints: /relay-api (default), /relay-worker, /relay-migrate. The chart picks the
# entrypoint per workload. Base images are pinned by digest; Renovate keeps them current.
FROM --platform=$BUILDPLATFORM golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/gracefulinfra/relay-api/internal/buildinfo.Version=$VERSION -X github.com/gracefulinfra/relay-api/internal/buildinfo.Commit=$COMMIT" \
      -o /out/ ./cmd/relay-api ./cmd/relay-worker ./cmd/relay-migrate

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/ /
USER nonroot:nonroot
EXPOSE 8080 9090
ENTRYPOINT ["/relay-api"]
