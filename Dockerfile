# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN --mount=type=cache,target=/go/pkg/mod GOPROXY=$GOPROXY go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" \
        -o /out/managed-agent ./cmd/managed-agent

FROM alpine:3.23
ARG VERSION=dev
ARG REVISION=unknown

LABEL org.opencontainers.image.title="Mango" \
      org.opencontainers.image.description="Self-hosted Managed Agents-compatible runtime" \
      org.opencontainers.image.source="https://github.com/yanpgwang/managed-agent-go" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.licenses="Apache-2.0"

RUN apk add --no-cache ca-certificates
COPY --from=build /out/managed-agent /usr/local/bin/managed-agent

RUN addgroup -S managed-agent && adduser -S -G managed-agent managed-agent
USER managed-agent

ENTRYPOINT ["/usr/local/bin/managed-agent"]
