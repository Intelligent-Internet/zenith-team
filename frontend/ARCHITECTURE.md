# Zenith Team frontend architecture

The frontend is one pnpm workspace for the Vite web app, Electron desktop app,
and Cloudflare Pages proxy. Keep framework and API choices aligned with
`package.json`, `AGENTS.md`, and the server's generated OpenAPI contract.

## Ownership

- `src/app/` composes routes, providers, workspace chrome, and realtime cache
  updates. Session routing and sign-out orchestration live here. Shell controls
  can be composed by feature pages; reusable data access does not live here.
- `src/features/` owns product workflows, their screens, and feature-specific
  models. Keep small features flat. Split large features by cohesive behavior,
  not by arbitrary file-count limits. Cross-feature composition belongs in the
  screen that owns the workflow; shared domain contracts belong below it.
- `src/shared/` owns UI primitives, domain data access, session state, desktop
  adapters, and other code used by several workflows. It does not import from
  `app/` or `features/`. Stateful product chrome such as the profile menu belongs
  in `app/shell/`, not `shared/ui/`.
- `src/state/` owns the Redux store and UI state slices. RTK Query owns server
  state; slices own drafts, selections, navigation, and local presentation.
- `src/lib/` contains the axios transport and its low-level auth/error helpers.
- `electron/main/` and `electron/preload/` own native execution and the bridge.
  `electron/shared/desktop-bridge.types.ts` owns their renderer-facing contract.
  That contract may reference API DTO types, but not renderer implementation.
  `src/shared/desktop/` adapts the bridge and supplies browser fallbacks.
- `functions/` is the hosted same-origin API/WebSocket proxy.

## Data access

There is one `boardlyApi`, one reducer, one middleware, and one shared RTK cache.
Domain endpoint modules in `src/shared/api/` inject into that instance:
`conversation-api`, `company-api`, `analytics-api`, `activity-api`, `task-api`,
`resource-api`, `agent-api`, and `agent-runtime-api`. Feature-specific endpoint
modules continue to inject into the same instance. Import hooks and cache
utilities from their owning domain module; keep endpoint names and cache tags
stable when moving code. Cache updaters import the owners of the entries they
update. Endpoint injection must run before those entries are used.

Transport remains `fakeBaseQuery` plus `queryFn` calling the axios services.
`generated/api.generated.ts` is generated from `docs/api/spec/`; API aliases
must derive from it. Do not duplicate server-owned shapes.

Session data and subscriptions live in `shared/session/`. Route selection
belongs in `app/session/session-routing.ts`; adding a route must not make the
shared session depend on a feature screen.

## Large feature organization

`features/project-canvas/` groups related implementation and colocated CSS:

- `api/`: canvas endpoints, scope paths, and synchronization signals.
- `canvas/`: spatial canvas, layout workers, library, and result cards.
- `documents/`: collaborative documents, blocks, pages, history, and export.
- `review/`: annotations, comment navigation, and review workspaces.
- `previews/`: file/media previews, thumbnails, and preview workers.

Use direct module imports instead of a barrel that eagerly loads every viewer.
Worker URLs and CSS imports remain relative to their owning files. Document
references load file previews without importing back into the document editor.

`chat-panel/hooks/use-agent-realtime-chat.ts` orchestrates connection and React
lifecycle. Transcript conversion lives in `model/agent-chat-transcript.ts`,
pending/streaming reconciliation in `model/agent-chat-pending-history.ts`,
and transcript reads/uploads in `api/agent-chat-history.ts` and
`api/agent-chat-uploads.ts`. Keep pure transformations out of network hooks.

## Dependency checks and verification

`pnpm check:dependencies` rejects local import cycles (including type-only and
literal dynamic imports), unresolved local imports, shared-to-app/feature
imports, Electron-contract-to-renderer-implementation imports, and direct
renderer imports of Electron main/preload code. Computed import paths are listed
for manual review; external packages and generated output are outside the graph.
Worker entry points execute separately and are not module-import cycle edges.

`pnpm lint` includes that check, so the existing frontend CI lint step enforces
it. Use `pnpm typecheck`, `pnpm build`, and relevant manual browser/Electron
checks after changes. Do not add or run frontend test suites; follow AGENTS.md.
