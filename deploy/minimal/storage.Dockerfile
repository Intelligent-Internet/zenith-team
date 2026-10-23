# Give the new named volume a non-root owner at image build time.
FROM docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z@sha256:635197cb9f36d01bee221d34d1c7d7960f6a95c48b0b6c01d99cd13bdae51a46
RUN mkdir -p /data && chown 65532:65532 /data
ENV HOME=/tmp
USER 65532:65532
