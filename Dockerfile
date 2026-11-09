# syntax=docker/dockerfile:1

# ---------- build ----------
FROM golang:1.21-bookworm AS builder

# go-sqlite3 needs cgo
ENV CGO_ENABLED=1
ENV GOFLAGS=-mod=mod

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/x-ui main.go

# ---------- runtime ----------
FROM debian:bookworm-slim

# Pinned Xray core. Do NOT use the panel's "switch version" feature with this
# image: it writes into the container layer and silently reverts on recreate.
# Bump these two values together (hash from release asset Xray-linux-64.zip.dgst).
ARG XRAY_VERSION=26.3.27
ARG XRAY_SHA256=23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata curl unzip python3 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /usr/local/x-ui
COPY --from=builder /out/x-ui ./x-ui
COPY deploy/provision.py /usr/local/x-ui/provision.py

# Core archive is vendored (verified against the upstream .dgst) so that builds
# and migrations are reproducible even when GitHub's release CDN is unreachable.
COPY vendor/xray/Xray-linux-64.zip /tmp/xray.zip
RUN set -eux; \
    echo "${XRAY_SHA256}  /tmp/xray.zip" | sha256sum -c -; \
    mkdir -p ./bin; \
    unzip -q /tmp/xray.zip -d ./bin; \
    mv ./bin/xray ./bin/xray-linux-amd64; \
    rm -f /tmp/xray.zip; \
    chmod 755 ./x-ui ./bin/xray-linux-amd64 ./provision.py; \
    ./bin/xray-linux-amd64 -version; \
    mkdir -p /etc/x-ui && chmod 700 /etc/x-ui

ENV TZ=Asia/Shanghai \
    XUI_LOG_LEVEL=info

VOLUME ["/etc/x-ui"]

# panel port, VMess/VLESS inbounds and Hysteria2 (UDP) - informational only with host networking
EXPOSE 4433 8443 3389 3389/udp

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -s -o /dev/null "http://127.0.0.1:${XUI_HEALTHCHECK_PORT:-4433}/" || exit 1

STOPSIGNAL SIGTERM
CMD ["./x-ui"]
