# Boardly cloud API server image.
#
# Build from the repository root; server/Dockerfile.dockerignore trims the
# BuildKit context to the Go module:
#
#   docker build -f server/Dockerfile -t boardly-server .
#
# The runtime stage is distroless static: no shell, no package manager, and a
# non-root user. Configuration is entirely environment-driven (documented in
# deploy/README.md) and PostgreSQL migrations run automatically at startup.

FROM golang:1.26-alpine AS build
WORKDIR /src/server

# Cache module downloads independently of source edits.
COPY server/go.mod server/go.sum ./
RUN go mod download

COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/boardly-server ./cmd/server

# Distroless has no shell or curl, so the container healthcheck is a tiny
# static probe that GETs /readyz and exits non-zero unless it returns 200.
RUN mkdir -p /out/probe && printf '%s\n' \
    'package main' \
    '' \
    'import (' \
    '	"net/http"' \
    '	"os"' \
    '	"time"' \
    ')' \
    '' \
    'func main() {' \
    '	url := os.Getenv("APP_READYZ_URL")' \
    '	if url == "" {' \
    '		url = "http://127.0.0.1:8788/readyz"' \
    '	}' \
    '	client := &http.Client{Timeout: 3 * time.Second}' \
    '	response, err := client.Get(url)' \
    '	if err != nil {' \
    '		os.Exit(1)' \
    '	}' \
    '	defer func() { _ = response.Body.Close() }()' \
    '	if response.StatusCode != http.StatusOK {' \
    '		os.Exit(1)' \
    '	}' \
    '}' \
    > /out/probe/main.go \
    && cd /out/probe \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
       -o /out/boardly-readyz main.go

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/boardly-server /usr/local/bin/boardly-server
COPY --from=build /out/boardly-readyz /usr/local/bin/boardly-readyz

EXPOSE 8788
USER nonroot
ENTRYPOINT ["/usr/local/bin/boardly-server"]
