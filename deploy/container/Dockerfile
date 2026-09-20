# syntax=docker/dockerfile:1
# Build context is prepared by deploy/container/build.sh and contains ./tparun
# and ./tpa. The runtime image contains no source or Go toolchain.
FROM golang:1.26-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81 AS build
ARG TPARUN_REVISION=unknown
ARG TPARUN_VERSION=0.1.1
WORKDIR /src/tparun
COPY tparun/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/eugen252009/tpahub/internal/version.BuildRevision=${TPARUN_REVISION}" -o /out/tparun ./cmd/tparun
WORKDIR /src/tpa
COPY tpa/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tpa .

FROM debian:bookworm-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171
ARG TPARUN_REVISION=unknown
ARG TPARUN_VERSION=0.1.1
LABEL org.opencontainers.image.title="TPA.run self-hosted" \
      org.opencontainers.image.version="${TPARUN_VERSION}" \
      org.opencontainers.image.revision="${TPARUN_REVISION}"
RUN apt-get update \
    && apt-get install --no-install-recommends -y ca-certificates curl gnupg util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 tparun \
    && useradd --uid 10001 --gid 10001 --home-dir /config/home --create-home --shell /usr/sbin/nologin tparun \
    && mkdir -p /config /data \
    && chown -R 10001:10001 /config /data
COPY --from=build /out/tparun /usr/local/bin/tparun
COPY --from=build /out/tpa /usr/local/bin/tpa
COPY tparun/deploy/container/entrypoint.sh /usr/local/bin/tparun-entrypoint
RUN chmod 0755 /usr/local/bin/tparun /usr/local/bin/tpa /usr/local/bin/tparun-entrypoint
ENV HOME=/config/home \
    TPARUN_PROFILE=self-hosted \
    TPARUN_LISTEN=0.0.0.0:8080 \
    TPARUN_DATA_DIR=/data \
    TPA_BIN=/usr/local/bin/tpa \
    GNUPGHOME=/config/gnupg \
    TPARUN_REGISTRATION_ENABLED=true \
    TPARUN_CAPABILITY_SOURCE=local \
    TPARUN_BILLING=none \
    TPARUN_BILLING_PROVIDER=none \
    TPARUN_DELIVERY_MODE=metrics-only \
    TPARUN_EGRESS_ENFORCEMENT=none
VOLUME ["/config", "/data"]
EXPOSE 8080
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/tparun-entrypoint"]
CMD ["serve"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=6 CMD ["/usr/bin/curl", "-fsS", "http://127.0.0.1:8080/ready"]
