SHELL := /bin/bash

# PostgreSQL integration tests share one parent-owned container and allocate an
# isolated database per dbtest fixture. Bounding package concurrency keeps that
# server's connection and migration load predictable while the gate runs the
# full race suite.
GOFLAGS ?= -p=2
export GOFLAGS

.PHONY: help api-docs api-html api-lint api-bundle api-generate api-types-check server server-local frontend boardly-mcp-dev debug-app gate go-gate go-unit go-integration go-test go-sqlc-check go-shape lint typecheck test-commerce-kit build infra-up infra-down infra-reset go-tools go-fmt go-fmt-check go-lint go-lint-fix fmt bundle-bump bundle-verify bundle-local bundle-check bundle-diff

.DEFAULT_GOAL := help

COMPOSE := docker compose -f docker/compose.yml

# Dev-only server credentials for the optional Docker infrastructure stack.
DEV_ENV := docker/dev.env
# Your own overrides of docker/dev.env, ignored by git; `make server` loads
# it after dev.env. For example HUDDLES_ENABLED='true' turns local Huddles on.
DEV_LOCAL_ENV := docker/dev.local.env
# Huddles need LiveKit, so infra-up starts it only when they are on.
HUDDLES_ENABLED := $(shell set -a; . ./$(DEV_ENV); [ ! -f $(DEV_LOCAL_ENV) ] || . ./$(DEV_LOCAL_ENV); echo "$$HUDDLES_ENABLED")
DEV_BIN_DIR := $(CURDIR)/.dev/bin

# Pinned Go lint/format toolchain, installed repo-locally into .tools/bin so it
# never clobbers a globally installed golangci-lint. Bump the pin and the next
# make invocation reinstalls automatically.
GOLANGCI_LINT_VERSION := v2.12.2
TOOLS_BIN := $(CURDIR)/.tools/bin
GOLANGCI_LINT := $(TOOLS_BIN)/golangci-lint

# API origin the web and desktop clients target in local development.
BOARDLY_WEB_API_URL ?= http://127.0.0.1:8788

help:
	@echo "Local development (run each in its own terminal):"
	@echo "  make server-local Run the Go API with services configured in server/.env (:8788)"
	@echo "  make infra-up    Start PostgreSQL 17 + Redis + Silo (+ LiveKit when HUDDLES_ENABLED='true')"
	@echo "  make server      Run the Go API with docker/dev.env, then your docker/dev.local.env overrides"
	@echo "  make frontend    Run the client app in frontend/ (own workspace)"
	@echo "  make livekit-up | livekit-down | livekit-logs  Manage just the LiveKit media server (Huddles)"
	@echo "  make voice-setup Create voice-agent/.env for the local stack    make voice-doctor  Check it before a call"
	@echo "  make voice-start Run the local voice worker in this terminal (no background process)"
	@echo "  make voice-status | voice-stop  Inspect or gracefully stop this checkout's voice worker"
	@echo "  make boardly-mcp-dev  Build boardly-mcp into .dev/bin for daemon injection"
	@echo "  make infra-down  Stop infra, keep volumes    make infra-reset  Stop and wipe volumes"
	@echo ""
	@echo "Agent debugging (local only):"
	@echo "  make debug-app       Launch the Desktop app in debug mode"
	@echo "  zen daemon logs -f  Follow logs for a daemon in the selected BOARDLY_HOME"
	@echo ""
	@echo "Verification:"
	@echo "  make gate        Full release-quality gate    make go-gate  Go arch/build/vet/test -race"
	@echo "  make go-unit     Go unit lane: no Docker, skips the integration build tag (what CI gates on)"
	@echo "  make go-integration  Only the -tags=integration suites, on a shared PostgreSQL container"
	@echo "  make go-test     Go tests with one shared PostgreSQL container (GO_TEST_ARGS='./path -run TestName')"
	@echo "  make go-fmt      Format all Go code (gofumpt + goimports via golangci-lint)"
	@echo "  make go-lint     Lint Go code    make go-lint-fix  Lint with autofix"
	@echo "  make lint | typecheck | build   (all run in frontend/)"
	@echo "  make api-lint    Validate docs/api/spec/openapi.yaml   make api-docs  Browse it locally"
	@echo "  make api-generate  Regenerate TypeScript API types    make api-types-check  Check drift"
	@echo ""
	@echo "Platform bundles (ADR 0016; your own git credentials, PLUGINS_REPO=<url-or-path> to override):"
	@echo "  make bundle-bump TAG=<plugin>--vX.Y.Z  Pin a boardly-plugins release; summary in .dev/bundle-bump.md"
	@echo "  make tools-prepare  Download verified npm skills into ignored build assets"
	@echo "  make bundle-verify  Re-import every pinned tag and require identical bytes"
	@echo "  make bundle-local   Pin a local checkout for development (PLUGINS=../boardly-plugins PLUGIN=zenith-workflows)"
	@echo "  make bundle-check   Validate a local plugin folder with the server's rules (PLUGINS, PLUGIN as above)"
	@echo "  make bundle-diff    Show what the pin changes against BASE (default origin/main)"
	@echo ""
	@echo "Full walkthrough: docs/quickstart.md"

# Go API from source against `make infra-up`, including Redis and Silo.
server:
	set -a && source ./$(DEV_ENV) && { [ ! -f $(DEV_LOCAL_ENV) ] || source ./$(DEV_LOCAL_ENV); } && set +a && \
	cd server && go run ./cmd/server

# Go API from source with dependencies configured in server/.env. PostgreSQL,
# Redis, and S3-compatible object storage (Silo locally) must all be reachable;
# database migrations apply automatically at startup.
server-local:
	@cd server && set -a && source .env && set +a && exec go run ./cmd/server

# The client app under frontend/ — a separate pnpm workspace (ii-company), so
# run `cd frontend && pnpm install` once before the first start.
frontend:
	cd frontend && pnpm dev

# Desktop development stages boardly-mcp automatically. This target builds a
# standalone copy at a stable, git-ignored path for a manually launched daemon
# processes; point BOARDLY_MCP_EXECUTABLE at this exact binary.
boardly-mcp-dev:
	mkdir -p $(DEV_BIN_DIR)
	cd server && go build -o $(DEV_BIN_DIR)/boardly-mcp ./cmd/mcp

# Launch the Desktop app with its debug configuration. The daemon is detached;
# inspect it separately with `zen daemon logs -f` under the Desktop-owned
# BOARDLY_HOME.
debug-app:
	cd frontend && pnpm electron:dev:debug

# Shape ratchets depguard cannot express. Only rules already at zero live here —
# a rule added while violations exist is a broken gate, not a goal.
go-shape: tools-prepare
	@fail=0; \
	if find server -name doc.go | grep -q .; then \
		echo "doc.go files found; move the package comment atop the primary file:"; \
		find server -name doc.go | sed 's/^/  /'; fail=1; fi; \
	if grep -rn '\.(\*generated\.Queries)' server/internal --include='*.go' | grep -vE ':[0-9]+:\s*//' | grep -q .; then \
		echo "type assertion back to *generated.Queries; pass the concrete handle instead of a narrowed interface:"; \
		grep -rn '\.(\*generated\.Queries)' server/internal --include='*.go' | grep -vE ':[0-9]+:\s*//' | sed 's/^/  /'; fail=1; fi; \
	if grep -rnE '(^|[^A-Za-z0-9_])Postgres[A-Z]' server/internal server/pkg server/cmd --include='*.go' | grep -vE ':[0-9]+:\s*//' | grep -q .; then \
		echo "identifier named for its backend; name it for its role (the package already says PostgreSQL):"; \
		grep -rnE '(^|[^A-Za-z0-9_])Postgres[A-Z]' server/internal server/pkg server/cmd --include='*.go' | grep -vE ':[0-9]+:\s*//' | sed 's/^/  /'; fail=1; fi; \
	if find server -type f -name '*.go' ! -path 'server/tests/testpostgres/*' -exec grep -nH -E 'testcontainers-go/modules/postgres|postgres:[0-9]' {} + | grep -q .; then \
		echo "PostgreSQL testcontainers must be owned by tests/testpostgres; use tests/dbtest from test packages:"; \
		find server -type f -name '*.go' ! -path 'server/tests/testpostgres/*' -exec grep -nH -E 'testcontainers-go/modules/postgres|postgres:[0-9]' {} + | sed 's/^/  /'; fail=1; fi; \
	if ! zen_deps="$$(cd server && go list -deps ./cmd/zen)"; then \
		echo "go list -deps ./cmd/zen failed; the zen CLI link set cannot be checked"; fail=1; \
	elif printf '%s\n' "$$zen_deps" | grep -qx 'boardly.dev/boardly/server/internal/platformbundle'; then \
		echo "cmd/zen links internal/platformbundle; the zen CLI must never link Workflow bundle bytes (ADR 0016):"; \
		(cd server && go list -deps -f '{{$$p := .ImportPath}}{{range .Imports}}{{if eq . "boardly.dev/boardly/server/internal/platformbundle"}}{{$$p}} imports it{{"\n"}}{{end}}{{end}}' ./cmd/zen) | sed 's/^/  /'; fail=1; fi; \
	if find server/internal/mcp/tools/builtin -mindepth 1 -maxdepth 1 ! -name zenith-platform ! -name .DS_Store | grep -q .; then \
		echo "server/internal/mcp/tools/builtin holds only zenith-platform; pinned platform content lives in internal/platformbundle (ADR 0016):"; \
		find server/internal/mcp/tools/builtin -mindepth 1 -maxdepth 1 ! -name zenith-platform ! -name .DS_Store | sed 's/^/  /'; fail=1; fi; \
	exit $$fail

# sqlc.yaml names its input and output directories in two separate keys, and a
# stale one regenerates an empty package rather than failing. Nothing else in
# the gate reads sqlc.yaml, so without this a bad path lands green and the
# breakage surfaces as missing query methods much later. Skipped when sqlc is
# not installed, since it is not needed to build or test the committed output.
go-sqlc-check:
	@if command -v sqlc >/dev/null 2>&1; then \
		check_dir="$$(mktemp -d)"; \
		trap 'rm -rf "$$check_dir"' EXIT; \
		mkdir -p "$$check_dir/server/pkg/db"; \
		cp server/sqlc.yaml "$$check_dir/server/sqlc.yaml"; \
		cp -R server/migrations "$$check_dir/server/migrations"; \
		cp -R server/pkg/db/queries "$$check_dir/server/pkg/db/queries"; \
		cd "$$check_dir/server" && sqlc generate; \
		cd "$(CURDIR)"; \
		diff -ru server/pkg/db/generated "$$check_dir/server/pkg/db/generated" \
			|| { echo "sqlc output is stale: run 'cd server && sqlc generate'"; exit 1; }; \
	else \
		echo "sqlc not installed; skipping generated-code drift check"; \
	fi

# Files named *_integration_test.go carry a `//go:build integration` tag, so they
# compile only when the tag is passed. Untagged runs are the unit lane; adding
# the tag adds those files on top -- it never subtracts, so `go-gate` below still
# runs the complete suite exactly as it always has.
go-gate: go-shape go-sqlc-check
	cd server && go build ./...
	cd server && go vet ./...
	cd server && go run ./tests/testpostgres go test -count=1 -race -tags=integration ./...

# The lane CI gates on. No Docker, no PostgreSQL, no Silo, no Redis: the tagged
# files are not compiled in, so nothing reaches for infrastructure that is not
# there. The tagged vet is not redundant -- a build tag hides those files from
# build, vet, and lint alike, so without it an integration suite could stop
# compiling and no blocking lane would notice until someone ran the tag by hand.
go-unit: go-shape go-sqlc-check
	cd server && go build ./...
	cd server && go vet ./...
	cd server && go vet -tags=integration ./...
	cd server && go test -count=1 -race ./...

# The infrastructure-backed lane, on the shared container. A build tag adds files
# rather than subtracting them, so this necessarily re-runs the unit tests too --
# there is no tag expression that selects the tagged files alone. That overlap is
# the price of the tag split, and it is why this is not on the merge path.
go-integration: tools-prepare
	cd server && go run ./tests/testpostgres go test -count=1 -race -tags=integration ./...

GO_TEST_ARGS ?= ./...

go-test: tools-prepare
	cd server && go run ./tests/testpostgres go test -count=1 -tags=integration $(GO_TEST_ARGS)

# Install the pinned golangci-lint into .tools/bin when missing or outdated.
# GOFLAGS is cleared: the exported -p=2 only bounds integration-test load.
go-tools:
	@installed="$$($(GOLANGCI_LINT) version --short 2>/dev/null || true)"; \
	if [ "$${installed#v}" != "$(patsubst v%,%,$(GOLANGCI_LINT_VERSION))" ]; then \
		echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION) -> $(TOOLS_BIN)"; \
		GOBIN=$(TOOLS_BIN) GOFLAGS= go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	fi

go-fmt: go-tools
	cd server && $(GOLANGCI_LINT) fmt ./...

# Non-mutating: prints diffs and exits 1 when formatting is needed (CI-friendly).
go-fmt-check: go-tools
	cd server && $(GOLANGCI_LINT) fmt --diff ./...

go-lint: go-tools tools-prepare
	cd server && $(GOLANGCI_LINT) run ./...

go-lint-fix: go-tools tools-prepare
	cd server && $(GOLANGCI_LINT) run --fix ./...

# --- Platform bundles (ADR 0016) ---------------------------------------------
# boardly pins each boardly-plugins release it ships: Workflows use a tar and
# lock in internal/platformbundle; managed Tools use a single metadata lock in
# internal/daemon/tool_releases. bundlectl (server/cmd/bundlectl)
# imports, verifies and diffs them with your own git credentials (for GitHub,
# `gh auth setup-git`); it never handles a token. PLUGINS_REPO overrides the
# GitHub repository with another URL or a local checkout, which is read in
# place; a local path is taken relative to this directory.
PLUGINS_REPO ?=
PLUGINS ?= ../boardly-plugins
PLUGIN ?= zenith-workflows
COMPONENT ?= workflows
BUNDLE_OUT_workflows := internal/platformbundle
BUNDLE_OUT_managedTools := internal/daemon/tool_releases
BUNDLE_OUT = $(BUNDLE_OUT_$(COMPONENT))
BASE ?= origin/main
BUNDLE_REPO_FLAG = $(if $(PLUGINS_REPO),--repo $(if $(wildcard $(PLUGINS_REPO)),$(abspath $(PLUGINS_REPO)),$(PLUGINS_REPO)))

.PHONY: tools-prepare
# Resolve public npm bytes before compiling the daemon/CLI. Only the compact
# descriptor pin is committed; generated skills and licenses are git-ignored.
tools-prepare:
	cd server && go run ./cmd/bundlectl prepare

# Pin a release: make bundle-bump TAG=zenith-workflows--v1.2.0. The tag must be
# on main. Rewrites the lock and any Workflow archive; paste .dev/bundle-bump.md
# into the pull request.
bundle-bump:
	@test -n "$(TAG)" || { echo "usage: make bundle-bump TAG=<plugin>--vX.Y.Z [COMPONENT=workflows|managedTools] [PLUGINS_REPO=<url-or-path>]"; exit 2; }
	mkdir -p .dev
	cd server && go run ./cmd/bundlectl import --tag $(TAG) --out $(BUNDLE_OUT) $(BUNDLE_REPO_FLAG) --summary ../.dev/bundle-bump.md

# The provenance check: re-import every pinned tag and require byte-identical
# locks and any Workflow archives.
bundle-verify:
	cd server && go run ./cmd/bundlectl import --verify --out internal/platformbundle $(BUNDLE_REPO_FLAG)
	cd server && go run ./cmd/bundlectl import --verify --out internal/daemon/tool_releases $(BUNDLE_REPO_FLAG)

# Pin a local plugins checkout while iterating on content. The pin names the
# checkout's HEAD and is never committed: the pinned-release tests fail
# until bundle-bump replaces it.
bundle-local:
	cd server && go run ./cmd/bundlectl import --dev --from $(abspath $(PLUGINS)) --plugin $(PLUGIN) --out $(BUNDLE_OUT)

# Validate a local plugin folder with the server's own parser and loader,
# reporting every problem. Pins nothing.
bundle-check:
	cd server && go run ./cmd/bundlectl check $(abspath $(PLUGINS))/plugins/$(PLUGIN)

# What the working tree pin changes against BASE, as Markdown for a pull
# request or $GITHUB_STEP_SUMMARY.
bundle-diff:
	cd server && go run ./cmd/bundlectl diff --base $(BASE) --out $(BUNDLE_OUT) --format markdown

# --- API contract -----------------------------------------------------------
# docs/api/spec/openapi.yaml is the source of truth for the HTTP surface. The Go
# test TestOpenAPISpecMatchesRouteTable (part of `make go-gate`) already fails
# when the spec and the served route table disagree; these targets cover the
# spec's own validity and give it a reader.

# Browse the contract locally. Serves it with a live-reloading viewer at
# http://localhost:8080 — edit a domain file under docs/api/spec/ and the page
# refreshes. This is the one to use while writing the contract.
api-docs:
	cd frontend && REDOCLY_TELEMETRY=off REDOCLY_SUPPRESS_UPDATE_NOTICE=true \
		pnpm exec redocly preview-docs ../docs/api/spec/openapi.yaml \
		--config ../docs/api/spec/redocly.yaml

# Render the contract to one self-contained HTML file you can open, attach, or
# publish. No server and no network once built. Generated, never committed.
api-html:
	cd frontend && REDOCLY_TELEMETRY=off REDOCLY_SUPPRESS_UPDATE_NOTICE=true \
		pnpm exec redocly build-docs ../docs/api/spec/openapi.yaml \
		--config ../docs/api/spec/redocly.yaml \
		-o ../docs/api/spec/openapi.html
	@echo "open docs/api/spec/openapi.html"

# Validate the spec itself: malformed schemas, broken $refs, missing operation
# ids. Runs in CI so a bad spec fails the build rather than the next person to
# generate a client. Follows the $refs from openapi.yaml into docs/api/spec/,
# so this covers every domain file too.
api-lint:
	cd frontend && REDOCLY_TELEMETRY=off REDOCLY_SUPPRESS_UPDATE_NOTICE=true pnpm api:lint

# Flatten the multi-file spec into one document. The split layout is the
# source of truth and is what you edit; a few consumers want a single file —
# older codegen, an upload to a portal, a diff across releases. The output is
# generated, never edited, and never committed.
api-bundle:
	cd frontend && REDOCLY_TELEMETRY=off REDOCLY_SUPPRESS_UPDATE_NOTICE=true \
		pnpm exec redocly bundle ../docs/api/spec/openapi.yaml \
		-o ../docs/api/spec/openapi.bundled.yaml

# Regenerate the committed, runtime-free TypeScript contract. It deliberately
# includes the daemon plane: one generated file mirrors the entire API, while
# handwritten frontend bridges decide which operations the product client uses.
api-generate:
	cd frontend && pnpm api:generate

# Regenerate into an isolated file so a stale contract fails without modifying
# the worktree. This is suitable for CI and leaves api-generate as the explicit
# developer action that updates committed output.
api-types-check:
	@generated="$$(mktemp)"; \
	trap 'rm -f "$$generated"' EXIT; \
	cd frontend && pnpm exec openapi-typescript ../docs/api/spec/openapi.yaml --output "$$generated"; \
	if ! cmp -s src/shared/api/generated/api.generated.ts "$$generated"; then \
		echo "generated API types are stale: run 'make api-generate' and commit the result"; \
		diff -u src/shared/api/generated/api.generated.ts "$$generated" || true; \
		exit 1; \
	fi

# Umbrella formatter; JS/TS formatting can hook in here later.
fmt: go-fmt

lint:
	cd frontend && pnpm lint

typecheck:
	cd frontend && pnpm typecheck

test-commerce-kit:
	cd frontend && pnpm test:commerce-kit

build:
	cd frontend && pnpm build

infra-up:
	$(COMPOSE) up -d --wait --remove-orphans postgres redis silo $(if $(filter true,$(HUDDLES_ENABLED)),livekit,)
	$(COMPOSE) run --rm silo-init

infra-down:
	$(COMPOSE) down

infra-reset:
	$(COMPOSE) down --volumes --remove-orphans

.PHONY: livekit-up livekit-down livekit-logs
# LiveKit starts with `make infra-up`; these manage it on its own.
livekit-up:
	$(COMPOSE) up -d livekit

livekit-down:
	$(COMPOSE) stop livekit

livekit-logs:
	$(COMPOSE) logs -f livekit

.PHONY: voice-setup voice-doctor voice-start voice-status voice-stop voice-gate
voice-setup:
	@cd voice-agent && if [ -f .env ]; then echo "voice-agent/.env already exists; leaving it unchanged."; \
	else (umask 077 && cp .env.example .env) && echo "Created voice-agent/.env. Set OPENAI_API_KEY in it, then run make voice-doctor."; fi
	@cd voice-agent && uv sync --frozen

voice-doctor:
	@cd voice-agent && uv run --frozen python doctor.py

voice-start:
	@cd voice-agent && if [ -f .env ]; then exec uv run --frozen --env-file .env python agent.py dev; else exec uv run --frozen python agent.py dev; fi

voice-status:
	@cd voice-agent && uv run --frozen python local_worker.py status

voice-stop:
	@cd voice-agent && uv run --frozen python local_worker.py stop

voice-gate:
	cd voice-agent && uv run --frozen ruff check .
	cd voice-agent && uv run --frozen ruff format --check .
	cd voice-agent && uv run --frozen python -m unittest -v

gate: api-lint api-types-check voice-gate go-gate lint typecheck test-commerce-kit build
