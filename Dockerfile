# ---- Stage 1: Build frontend SPA ----
FROM --platform=$BUILDPLATFORM node:24-slim AS frontend

WORKDIR /build/web

# Install dependencies first (layer cache)
COPY web/package.json web/package-lock.json ./
RUN npm ci

# Copy frontend source and build
COPY web/ ./
RUN npm run build

# ---- Stage 2: Build Go binary ----
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS backend

WORKDIR /build

# Cache go module downloads
COPY go.mod go.sum ./
COPY third/ ./third/
RUN go mod download

# Copy Go source
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY onvif/ ./onvif/

# Copy built frontend into the embed directory
COPY --from=frontend /build/web/dist ./internal/ui/static/

# Static build — no C dependencies, pure Go binary
ENV CGO_ENABLED=0
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
RUN GOOS="$TARGETOS" GOARCH="$TARGETARCH" GOARM="${TARGETVARIANT#v}" \
    go build -ldflags="-s -w -X main.appVersion=${VERSION}" -o /lalmax-nvr ./cmd/lalmax-nvr/

# ---- Stage 3: Minimal runtime image ----
FROM alpine:3.21

RUN apk add --no-cache su-exec ffmpeg

# Default data and config directories
# These can be overridden via volume mounts
ENV NVR_DATA_DIR=/data
ENV NVR_UID=1000
ENV NVR_GID=1000

# Persistent data: recordings, database, config
VOLUME ["/data"]
ENV NVR_UID=1000
ENV NVR_GID=1000

COPY --from=backend /lalmax-nvr /usr/local/bin/lalmax-nvr
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 9090

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["lalmax-nvr", "health"]

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["-config", "/data/lalmax-nvr.yaml"]
