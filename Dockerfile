# syntax=docker/dockerfile:1.7

FROM golang:1.26.4-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/managed-agent ./cmd/managed-agent

FROM alpine:3.23
RUN apk add --no-cache ca-certificates
COPY --from=build /out/managed-agent /usr/local/bin/managed-agent

RUN addgroup -S managed-agent && adduser -S -G managed-agent managed-agent
USER managed-agent

ENTRYPOINT ["/usr/local/bin/managed-agent"]
