# Release image, built by GoReleaser from its prebuilt binaries
# (<os>/<arch>/asynqui in the build context). To build from source, use
# Dockerfile.dev. The image has no shell and so no HEALTHCHECK. Use the HTTP
# probes instead.

# Multi-arch index digest.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

ARG TARGETPLATFORM
COPY --chmod=0555 ${TARGETPLATFORM}/asynqui /usr/bin/asynqui

# The image's nonroot user, numeric so runAsNonRoot can verify it.
USER 65532:65532
# Set as an env var rather than CMD, so passing flags such as -log-level
# doesn't drop it.
ENV ASYNQUI_CONFIG=/etc/asynqui/config.yaml
EXPOSE 8080
ENTRYPOINT ["/usr/bin/asynqui"]
