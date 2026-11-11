# Build context is the repository root:
#   docker build -f deploy/docker/Dockerfile -t yanachan:latest .
# Both builder stages run on the build machine and cross-compile, so an arm64
# image needs no emulation; only the runtime stage is per target.

# ---- frontend -------------------------------------------------------------
# Base images are pinned by digest, not tag: a tag is mutable and a rebuilt
# upstream would silently change what goes into the binary. Refresh these with
#   docker buildx imagetools inspect <image>:<tag> --format '{{.Manifest.Digest}}'
FROM --platform=$BUILDPLATFORM node:24-alpine@sha256:06d7adf3e0ef61a13f1d28682726366f97a15aa1888c934d3ecec86c33918b53 AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ---- backend --------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS api
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# The Vue bundle is embedded into the binary, so the runtime image needs no
# web server and no node.
COPY --from=web /src/frontend/dist ./internal/webui/dist
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags="-s -w -X github.com/christiannarbon/yanachan-4k/backend/internal/buildinfo.Version=${VERSION} -X github.com/christiannarbon/yanachan-4k/backend/internal/buildinfo.Commit=${COMMIT} -X github.com/christiannarbon/yanachan-4k/backend/internal/buildinfo.Date=${BUILD_DATE}" \
    -o /out/yanachan ./cmd/server

# ---- runtime --------------------------------------------------------------
# 3.24, matching the Alpine the node and golang builder images are themselves
# built on. The old 3.21 pin was three releases behind.
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 -h /home/yana yana \
 && mkdir -p /data && chown yana:yana /data
COPY --from=api /out/yanachan /usr/local/bin/yanachan
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/licenses/yanachan/

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="yanachan" \
      org.opencontainers.image.description="Yana-chan 4K: a pull request dashboard" \
      org.opencontainers.image.source="https://github.com/christiannarbon/yanachan" \
      org.opencontainers.image.url="https://github.com/christiannarbon/yanachan" \
      org.opencontainers.image.documentation="https://github.com/christiannarbon/yanachan/tree/main/docs" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

USER yana
ENV GHDASH_ADDR=:19080 \
    GHDASH_STATE_DIR=/data \
    GHDASH_AUTH_MODE=local \
    GHDASH_ALLOW_GH_CLI=false
EXPOSE 19080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -q -O- http://127.0.0.1:19080/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/yanachan"]
