# Zenith Team web app image: the built single-page app behind Caddy, which
# also forwards /api to the server so the app and API share one origin.
#
# Build from the repository root; frontend/Dockerfile.dockerignore trims the
# BuildKit context:
#
#   docker build -f frontend/Dockerfile -t zenith-team-web .
#
# Build arguments are optional VITE_ settings (see frontend/.env.example);
# leave them empty for a self-hosted build.

# The static build is the same on every platform, so it runs natively.
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS build
WORKDIR /src/frontend
ENV ELECTRON_SKIP_BINARY_DOWNLOAD=1
# The production bundle needs more than Node's default heap.
ENV NODE_OPTIONS=--max-old-space-size=4096
RUN corepack enable

COPY frontend/package.json frontend/pnpm-lock.yaml frontend/.npmrc ./
# The web build needs no package install scripts (Electron, native addons).
RUN pnpm install --frozen-lockfile --ignore-scripts

COPY frontend/ ./
ARG VITE_LIVEKIT_URL=
ARG VITE_ZENITH_TEAM_PUBLIC_SITE_ORIGIN=
ARG VITE_ZENITH_TEAM_SUPPORT_EMAIL=
RUN pnpm exec vite build

FROM caddy:2.11-alpine
COPY deploy/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /src/frontend/dist /srv
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/zenith-team/
RUN addgroup -S web && adduser -S -G web web \
    && mkdir -p /config /data && chown -R web:web /config /data
USER web
EXPOSE 8080
