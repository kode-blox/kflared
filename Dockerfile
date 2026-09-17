# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.27.0

# Builder stage
FROM golang:${GO_VERSION}-trixie AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY VERSION ./
COPY ./api/ ./api/
COPY ./cmd/ ./cmd/
COPY ./internal/ ./internal/

ARG TARGETOS=linux
ARG TARGETARCH
ARG COMMIT
RUN VERSION="$(tr -d '\r\n' < VERSION)" && \
    test -n "$VERSION" && \
    test -n "$COMMIT" && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
        -o /out/kflared ./cmd

# Production stage
FROM debian:trixie-slim AS production

LABEL org.opencontainers.image.authors="Sayak Mukhopadhyay" \
      org.opencontainers.image.url="https://github.com/orgs/kode-blox/packages/container/package/kflared" \
      org.opencontainers.image.documentation="https://kflared.kodeblox.com" \
      org.opencontainers.image.vendor="kode-blox" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="KFlared" \
      org.opencontainers.image.description="Gateway controller for Cloudflare Tunnel" \
      org.opencontainers.image.base.name="docker.io/library/debian:trixie-slim"

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/* && \
    update-ca-certificates

RUN groupadd --gid 1000 app && \
    useradd --uid 1000 --gid app --shell /usr/sbin/nologin --create-home app

WORKDIR /app

COPY --chown=app:app --from=builder /out/kflared ./kflared
COPY LICENSE THIRD_PARTY_NOTICES /usr/share/licenses/kflared/

USER 1000:1000
EXPOSE 8081 8443

ENTRYPOINT ["./kflared"]
