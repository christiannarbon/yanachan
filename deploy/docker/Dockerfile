# Build context is the repository root:
#   docker build -f deploy/docker/Dockerfile -t yana-chan-4k:latest .

# ---- frontend -------------------------------------------------------------
# Base images are pinned by digest, not tag: a tag is mutable and a rebuilt
# upstream would silently change what goes into the binary. Refresh these with
#   docker buildx imagetools inspect <image>:<tag> --format '{{.Manifest.Digest}}'
FROM node:24-alpine@sha256:e67514e5d0f6c46656005e1b693b2ec9d52e80b641307de684d4a015ba7a4eaf AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ---- backend --------------------------------------------------------------
FROM golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS api
WORKDIR /src/backend
COPY backend/go.mod ./
RUN go mod download
COPY backend/ ./
# The Vue bundle is embedded into the binary, so the runtime image needs no
# web server and no node.
COPY --from=web /src/frontend/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/yana-chan-4k ./cmd/server

# ---- runtime --------------------------------------------------------------
# 3.24, matching the Alpine the node and golang builder images are themselves
# built on. The old 3.21 pin was three releases behind.
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 -h /home/yana yana \
 && mkdir -p /data && chown yana:yana /data
COPY --from=api /out/yana-chan-4k /usr/local/bin/yana-chan-4k

USER yana
ENV GHDASH_ADDR=:19080 \
    GHDASH_STATE_DIR=/data \
    GHDASH_ALLOW_GH_CLI=false
EXPOSE 19080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -q -O- http://127.0.0.1:19080/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/yana-chan-4k"]
