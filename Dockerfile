# Release image, built by GoReleaser (dockers_v2) from the binaries it has
# already built: the build context holds linux/amd64/asynqui and
# linux/arm64/asynqui. To build an image from source, use Dockerfile.dev.
#
# distroless/static has no shell, so there is no HEALTHCHECK; use the HTTP
# probes /health/liveness and /health/readiness (docs/deployment.md).

# gcr.io/distroless/static-debian12:nonroot, multi-arch index.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

ARG TARGETPLATFORM
COPY --chmod=0555 ${TARGETPLATFORM}/asynqui /usr/bin/asynqui

# The image's nonroot user, numeric so runAsNonRoot can verify it.
USER 65532:65532
# Mount the config here. As an env var, it survives extra flags such as
# `docker run ... -log-level debug`; -config still overrides it.
ENV ASYNQUI_CONFIG=/etc/asynqui/config.yaml
EXPOSE 8080
ENTRYPOINT ["/usr/bin/asynqui"]
