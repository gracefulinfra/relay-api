# syntax=docker/dockerfile:1
# Bootstrap smoke image (P0-01). Base images are pinned by digest; Renovate keeps them current.
FROM --platform=$BUILDPLATFORM golang:1.27.1-trixie@sha256:433790e515d27dc6003e847e644cc0af956985cf315c1c58a3b73ee2dd305183 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/gracefulinfra/relay-api/internal/buildinfo.Version=$VERSION -X github.com/gracefulinfra/relay-api/internal/buildinfo.Commit=$COMMIT" \
      -o /out/relay-api ./cmd/relay-api

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/relay-api /relay-api
USER nonroot:nonroot
ENTRYPOINT ["/relay-api"]
