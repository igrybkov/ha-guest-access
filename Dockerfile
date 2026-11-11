# All build steps run on the build host's platform. Go cross-compiles, and
# CA certs and zoneinfo are arch-independent, so no QEMU is needed.

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/guest-access ./cmd/guest-access

FROM --platform=$BUILDPLATFORM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS certs
RUN apk add --no-cache ca-certificates tzdata

# Supervisor wants io.hass.arch in its own arch names (aarch64, not arm64).
# A scratch stage has no shell to map them, so pick a stage per arch.
FROM scratch AS final-amd64
LABEL io.hass.arch="amd64"

FROM scratch AS final-arm64
LABEL io.hass.arch="aarch64"

# Runs as root on purpose. Supervisor mounts /data and /homeassistant owned
# by root, and a scratch image has no way to chown them at start.
FROM final-${TARGETARCH}
ARG VERSION=dev
LABEL io.hass.type="app" \
      io.hass.version="${VERSION}" \
      io.hass.name="Guest Access" \
      io.hass.description="Share links that let guests control a few things at home" \
      org.opencontainers.image.title="Guest Access" \
      org.opencontainers.image.description="Home Assistant app: share links that let guests control a few things at home" \
      org.opencontainers.image.source="https://github.com/igrybkov/ha-guest-access" \
      org.opencontainers.image.url="https://github.com/igrybkov/ha-guest-access" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=certs /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/guest-access /guest-access
ENV ZONEINFO=/usr/share/zoneinfo
EXPOSE 8099 8888
# Scratch has no curl; the binary probes its own /healthz. Supervisor uses
# the watchdog URL in config.yaml; this is for plain docker runs.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/guest-access", "-healthcheck"]
ENTRYPOINT ["/guest-access"]
