# syntax=docker/dockerfile:1

# Build stage runs on the BUILDER's architecture and cross-compiles with the Go
# toolchain, so multi-arch images need no QEMU. The runtime stage has no RUN, so
# nothing ever has to execute a foreign-arch binary during the build.
FROM --platform=$BUILDPLATFORM golang:1.26.8-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# The binary's entire in-module import closure (verified with `go list -deps`).
# Both committed asset sets it embeds — proxy/internal/nativepack/native-pack.generated.json
# and engine/pixel/assets/*.bin.gz — come along with these directories.
COPY engine/ engine/
COPY mem/ mem/
COPY proxy/ proxy/
COPY shared/ shared/

# CGO_ENABLED=0 and -trimpath match scripts/build-release-binaries.mjs.
# -buildvcs=false because .dockerignore keeps .git out of the build context.
# -X main.version stamps `var version = "dev"` in proxy/cmd/caveman-proxy/main.go.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=$VERSION" \
      -o /out/caveman-proxy ./proxy/cmd/caveman-proxy \
 && mkdir -p /out/data

# distroless static: CA roots for outbound provider TLS, no shell, no package
# manager, nothing to exploit. The :nonroot tag already runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
# release-binaries.yml passes the tag, commit, and repository URL.
ARG VERSION=dev
ARG REVISION=unknown
ARG SOURCE=https://github.com/JuliusBrussee/caveman
LABEL org.opencontainers.image.title="caveman-proxy" \
      org.opencontainers.image.description="Caveman local compression proxy and middleware runtime" \
      org.opencontainers.image.source="$SOURCE" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.licenses="BUSL-1.1"
COPY --from=build /out/caveman-proxy /caveman-proxy
# The binary embeds BSL-1.1 Engine code plus MIT pixel and font-licensed atlas
# assets; their license texts and notices ship with every copy.
COPY LICENSE LICENSE.BSL LICENSING.md /licenses/
COPY engine/pixel/NOTICE /licenses/NOTICE.engine-pixel
COPY engine/pixel/assets/SPLEEN_LICENSE.txt engine/pixel/assets/UNIFONT_LICENSE.txt /licenses/
# An empty, correctly-owned /data so a named or anonymous volume inherits uid
# 65532. A BIND mount does NOT inherit it — chown the host directory to 65532
# yourself or the proxy cannot create its SQLite spend store under CAVEMAN_HOME.
COPY --from=build --chown=nonroot:nonroot /out/data /data
# Implied by the :nonroot tag; stated so the security posture is visible here.
USER nonroot
ENV CAVEMAN_HOME=/data \
    CAVEMAN_LISTEN=0.0.0.0:8787
VOLUME /data
EXPOSE 8787
# No HEALTHCHECK instruction: the image has no shell and no curl to run one.
# Probe GET /health/ready (or /health/live) on 8787 from the orchestrator —
# Kubernetes httpGet, an ALB target group, or an external compose checker.
ENTRYPOINT ["/caveman-proxy"]
CMD ["serve"]
