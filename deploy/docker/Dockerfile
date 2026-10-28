# Build context is the repository root:
#   docker build -f deploy/docker/Dockerfile -t yana-chan-4k:latest .

# ---- frontend -------------------------------------------------------------
# Base images are pinned by digest, not tag: a tag is mutable and a rebuilt
# upstream would silently change what goes into the binary. Refresh these with
#   docker buildx imagetools inspect <image>:<tag> --format '{{.Manifest.Digest}}'
FROM node:24-alpine@sha256:06d7adf3e0ef61a13f1d28682726366f97a15aa1888c934d3ecec86c33918b53 AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ---- backend --------------------------------------------------------------
FROM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS api
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
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
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
