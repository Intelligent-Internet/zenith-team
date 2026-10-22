# Zenith Team Frontend

React 19, Vite, and Electron-capable frontend for the Zenith Team founder cockpit.

## Canonical Specs

- Frontend architecture: [Frontend architecture](ARCHITECTURE.md)
- Root architecture: `../ARCHITECTURE.md`
- HTTP contracts and generated types: `../docs/api/spec/openapi.yaml` and
  `src/shared/api/generated/api.generated.ts`
- Event contracts: the server's `pkg/protocol` registry and domain payload
  types; routing behavior is documented in `../docs/api/realtime.md`

Client dependencies and Electron packaging are owned by `frontend/package.json`
and `frontend/pnpm-lock.yaml`. Run package commands from `frontend/`; the repository
root is not a JavaScript workspace.

Use Node.js 24, matching CI. Electron 43's installer requires Node.js 22.12
or newer and downloads the development binary on first use.

Panels use native HTML and workspace CSS tokens through `ContentPanel`. Menus
use their Radix portals; floating chat and modal overlays use React portals.
The retired liquid glass packages, filters and geometry observers are removed.

Tailwind is the default for component layout, spacing, typography and states.
Workspace utilities in `src/app/global.css` resolve to the existing theme
variables, so Light/Dark palettes and accessibility preferences remain shared.
Authored CSS is reserved for theme/base definitions and integrations such as
native controls, generated editor/markdown content and complex motion. Follow
the [styling conventions](AGENTS.md#styling-tailwind-by-default) when migrating
existing selectors; preserve their cascade and exact responsive boundaries.

## Implementation Notes

### Production bundles

The public landing page has a separate bootstrap and stylesheet from the signed-in application.
It does not initialize Redux persistence, session recovery, desktop controllers,
or application-only Google Fonts. Links from that entry to other routes perform
a document navigation so those routes retain the normal application bootstrap.
Electron always uses the application bootstrap, including at its root URL.
The landing entry preloads its hero poster, and video playback waits until the
poster has decoded and painted. Keep the poster URL shared with the video when
updating that asset so the preload remains useful.
Public Tailwind utilities scan only the landing components and shared logo;
the font stacks are shared with the application. Static landing avatars use
the Clay renderer directly, and the WebP atlas loads as they approach the viewport.

`public-site/llms.txt` describes public product information and links. The
`/.well-known/ai-catalog.json` built from `public-site/ai-catalog.json`
advertises no callable resources: Company-scoped
MCP tools and authenticated workspace APIs are not anonymous public endpoints.
Keep its entries empty until there is an actual public discovery contract.
Both files are emitted only by builds that set
`VITE_ZENITH_TEAM_PUBLIC_SITE_ORIGIN`.
These files must be served as text/JSON rather than the SPA fallback.

Vite keeps React, state, routing, shared Iconsax assets, editor dependencies and
chart rendering in separate cacheable chunks. Explicit manual chunks do not
absorb their dependencies; feature routes and document viewers retain their
dynamic imports. The large Linux and Google Drive brand marks load as SVG assets
when rendered, and Shiki loads Oniguruma as a separate WebAssembly asset.
Markdown, Mermaid and document blocks share the pinned KaTeX version.

Keep the default 500 kB warning threshold. Some on-demand upstream modules
(including syntax grammars, Mermaid parser, LiveKit, PDF.js and PowerPoint
renderer) ship as single large modules and cannot be subdivided by `manualChunks`.
Assess both the entry's complete static import graph and feature loads when
changing chunk rules; splitting a file alone does not reduce downloaded bytes.

### Desktop Sentry reporting

The Electron main process owns Sentry's DSN, environment, release and transport.
Renderer errors, unhandled rejections and React 19 root errors use the same
reporter through the SDK's sandbox-compatible IPC preload. Native crashes use
Sentry's minidump integration; the generic Electron crash reporter is not started
a second time. Web-only sessions do not initialize Sentry.

Reporting is disabled by default. To enable it, set these values in `.env.local`
for `pnpm electron:dev`, or `.env.dev` for a packaged dev build:

```dotenv
ZENITH_TEAM_DESKTOP_CRASH_REPORTS_ENABLED=true
ZENITH_TEAM_DESKTOP_CRASH_REPORT_PROVIDER=sentry
ZENITH_TEAM_DESKTOP_SENTRY_DSN=https://public-key@your-ingest-host/project-id
```

For a packaged build, set the same values in the shell or in the selected
`.env.dev` or `.env.prod` file. Use a separate Sentry project for each build
variant. `write-release-env.cjs` embeds the public DSN and reporting policy
in packaged builds; no Sentry authentication token belongs in runtime config.
Events identify the desktop environment (`local`, `dev`, `prod`) and release
(`zenith-team-desktop@<package version>`).

The shared `electron/shared/sentry-options.json` disables automatic user identity,
HTTP headers/bodies, cookies, URL query values, AI content, local variables,
source context, breadcrumbs, logs, metrics and trace propagation. Replay and
performance tracing are not enabled. Before sending JavaScript errors, main
also removes request/user/extra fields and applies the existing diagnostic
redaction to error messages. Native minidumps are binary crash diagnostics and
can contain process memory; JavaScript redaction does not sanitize their contents.

Verify in an isolated desktop profile by capturing one synthetic main-process
error and one renderer error, then confirm both in the intended Sentry project's
Issues view.

Desktop builds with Sentry reporting enabled upload renderer source maps when
`SENTRY_AUTH_TOKEN` is present. Export the token in the shell and set
`SENTRY_ORG` / `SENTRY_PROJECT` in the selected `.env.dev` or `.env.prod` file
before running the desktop build. Only the renderer build step uses the token;
keep it out of every env file. No repository workflow uploads source maps.

The Vite plugin uploads hidden source maps with debug IDs, uses the same
`zenith-team-desktop@<package version>` release as the SDK, and removes `.map`
files before packaging. An upload failure fails the build. Missing org/project
configuration fails before bundling when a token is present. Builds without a
token keep source maps disabled. This upload covers Vite renderer bundles; the
Electron main process ships its original CommonJS files. See the
[Electron source-map guide](https://docs.sentry.io/platforms/javascript/guides/electron/sourcemaps/).

### Huddle performance

Huddles render up to nine participant/video tiles (four in a narrow stage),
including the pinned tile or shared screen. Paging detaches off-page video
elements so LiveKit adaptive streaming can pause their video; audio remains
subscribed. Visible speaking indicators update at most 30 times per second.

Optionally set `VITE_LIVEKIT_URL` to the deployment's public LiveKit URL at
build time. The lobby uses it only to warm DNS/TLS, without requesting room
admission or microphone access. Without it, joining still works normally.

Local call diagnostics are available in browser/Electron DevTools:

```js
performance
  .getEntriesByType("measure")
  .filter((entry) => entry.name.startsWith("zenith.huddle."));
```

`connect` and `firstAudioPlayback` durations start when the media room is
created after admission, not when Join is clicked. First audio measures the
first remote audio element's `playing` event, not detected speech. `reconnect`
retains the latest recovery duration and count. `media.detail` contains sampled
inbound packet loss, maximum jitter/RTT when available, and dropped-frame deltas.
Sampling runs every ten seconds while the document is visible; the first sample
establishes counter baselines. Unavailable metrics are null. Only the latest
entry of each kind is retained; diagnostics stay local and contain no tokens,
participant identities, or network addresses.

### Whiteboards

A Whiteboard is an Artifact of type `whiteboard`; its Board id is the
Artifact id. "+ Whiteboard" in a DM, group DM or channel's tab row sends a
message that creates one there, and it opens in the chat's side panel like any
Artifact (`?artifactId=`). Open full view shows it on the Artifact page
(`/companies/:companyId/artifacts/:artifactId`), and a public Artifact link
(`/share/artifacts/:token`) shows it view only. Sharing is the Artifact's.
Closing the panel, opening another Artifact or chat, or leaving the page waits
for pending changes to save.

### Whiteboard files

Drop any file onto an editable Whiteboard to add it at the pointer position,
including after panning or zooming. With the canvas focused, Cmd/Ctrl+V pastes
clipboard files at the center of the visible canvas. Images (PNG, JPEG, GIF,
WebP, up to 32 MiB and 32 Mi pixels) render inline from a bounded preview and
switch to the original when shown larger than the preview; other files (up to
100 MB) become file cards with a download button. Multiple files are offset so
each can be selected. Upload progress participates in the existing
save/navigation guard; placed files use the Board's normal persistence and
undo/redo. Pasting objects copied from another Board of the same company
references the same files when the member can read them there. Requires the
server's member-only
`POST /companies/{companyId}/artifacts/{artifactId}/whiteboard/files`.

New strokes, shapes, notes, text and images use the highest current layer plus
one, including local changes still waiting to save. Selecting an object keeps
its layer order in both the editor and public viewer.

### Client conventions

- **Iconsax is the required UI icon set; other icon libraries are prohibited.**
  Authored symbols matching Iconsax are allowed when its catalog lacks a
  suitable glyph. Follow
  [the mandatory icon instructions](AGENTS.md#iconsax-is-mandatory), including
  the asset workflow and authored-symbol guidelines. When the user wants more
  icons or alternatives, search the [Iconsax Outline catalog](https://app.iconsax.io/?style=outline)
  using the [icon discovery workflow](AGENTS.md#finding-more-icons); the bundled
  subset is not the full catalog. Import Iconsax Outline
  through `src/shared/ui/icons.tsx` (named components)
  and `src/shared/ui/icon.tsx` (product icon names). The Free SVG subset is bundled
  locally for web and offline desktop use; source and license details are in
  `src/assets/iconsax/NOTICE.md`. Preserve brand marks and authored artwork.
  Vite also routes Streamdown's internal icon imports through this adapter;
  emoji-picker navigation uses its `categoryIcons` API and Iconsax CSS masks.
  Review icon imports, SVG provenance and library-rendered controls against
  the icon instructions.
- Build the cockpit as the first usable workspace, not a marketing landing page.
- Treat user chat, selected Agent, tenant context, stream state, and
  decisions as product state.
- Chat conversation actions support private archive/restore for Task chats,
  direct Agent chats, and human/group direct messages. Chat options → Archived
  chats browses saved conversations; active lists and pins exclude them. Filing
  persists on the server per member and keeps Task state, running work and history
  intact. Human/group DM archive only clears the chat from the member's list:
  search still includes it, messages remain writable, and opening it from search,
  a link, or the recipient picker returns the existing conversation to the list.
  A new peer message also brings it back. Channel hide and archive controls keep
  their existing behavior.
- Task and direct Agent chat identify the Agent in the conversation header;
  replies show Run status without repeating its name or avatar. Task messages
  keep the human sender's identity. Their timestamp appears beside Copy below
  the bubble, so hidden metadata never leaves a gap after the sender's name.
  The Thinking label keeps its text shimmer and staggered dots; completed replies
  omit the indicator.
  Active chat headers and the Thinking indicator use the approved Fold pose
  animations through `motion="expressive"`. These are whole-character frames
  with transparent backgrounds, including connected hands and facial motion.
  Headers use 40px avatars and Thinking uses 32px. A visible Thinking or question
  avatar takes the visual focus; the header holds its pose with a quiet breath.
  State changes blend over 180ms without resizing the surrounding layout.
  Active tools mean working, reasoning means thinking, pending requests mean
  needs-input, and failed Runs mean error. An observed Run completion celebrates
  once, then returns to idle; opening completed history never celebrates again.
  Recovered tool errors never trigger the failure expression. Submitting an
  answer shows Thinking immediately and returns to the question if submission
  fails. Empty direct Agent chats have a larger welcome avatar.
  Motion loads on demand, pauses offscreen or in a hidden tab, and honors reduced
  motion. Chat attribution, mentions and navigation use static posters from the
  same artwork. Human avatars keep their existing rendering.
  Running tool rows shimmer. The current activity group keeps its latest tool
  and command or search detail shimmering through gaps between calls, without a
  duplicate Thinking indicator. It switches to its static summary when the Agent
  writes the next response or the Run ends or pauses for input. Earlier groups
  remain static; partial tool output does not end a running call.
  Submitting a question immediately shows the answer being sent. The confirmed
  answer updates the question cache from the response and shows Thinking while
  the continuation is prepared, then yields to the Run's own activity. Failed
  submissions restore the draft for retry.
  Working Run disclosures do not shimmer. They show elapsed wall time from the
  server's immutable Run creation clock, including time queued or waiting. The
  counter survives expanding, collapsing and reopening the chat, and updates
  only while Working.
  Before an Agent response is available, queued, deferred and workspace-waiting
  Runs show their known waiting state in the conversation. Existing replies,
  tool activity, Thinking and pending requests take precedence.
  Run details start expanded while working or waiting for input and collapse
  when the chat turn ends. A new human message starts a new turn; consecutive
  replies from the same Agent stay in one disclosure across backend Runs.
  `ask_human` is an ordinary tool call in that history. Its input form and
  resolved request details sit inside the turn and collapse with the other
  activity, leaving one Run finished header and the final answer. Grouping
  works from message order, without loading resolved requests to link Runs.
  History sync, message acknowledgements and question continuations preserve
  the group and its manual expansion choice. Manually collapsing live work
  shows its header without presenting a progress update as the final reply.
- Manually expanding or collapsing Run details pauses automatic bottom-following
  and preserves the toggle's screen position and keyboard focus. Sending a new
  message or returning to the bottom resumes following; Run inspection must not
  pull the disclosure out from under the pointer.
- Chat model controls reuse the Runtime model catalog warmed by the authenticated
  app shell. Opening the picker keeps cached models and reasoning levels visible
  during refresh. The compact Paper picker centers the regular-weight reasoning
  label above the model name in one wide shared picker button, with a neutral
  elevated surface and a single-column model list. The composer trigger is plain text;
  model-change notices use straight inline dividers with a flat lavender wash
  fading at both ends. Their small Andrew avatar reuses the compact blink motion,
  paused offscreen or in a hidden tab and disabled for reduced motion. The notice
  stays on one line, with its full text available on hover when space is tight.
  The slider keeps its plain thumb, with a light-to-dark gradient and drifting,
  twinkling particles at ordinary levels, and a traveling pixel animation with
  distinct, staggered flicker at the model's highest available level when it is
  Max or Ultra. Motion stops when the picker closes or the document is hidden and
  respects reduced motion. Pointer release and keyboard changes save through the
  existing revision-checked chat settings; model-change notices stay in history.
- Human avatars in channels, group chats and direct messages resolve through
  the same cached profile source as their profile cards. Discussion responses
  omit large avatar data URLs to keep history payloads bounded; message rows,
  thread previews, reactions, members, mentions and the DM navigator use the
  existing session and Company directory instead of fetching each author.
- Task chat catches up incrementally on normal visits. Socket recovery rereads
  all loaded message snapshots, including older pages, because attachment
  changes do not advance the message sequence. It then continues from the
  frozen history head without adding periodic refreshes.
- The workspace titlebar keeps Search, Calendar, Notifications, and Ask CEO.
  Calendar is also available in Explore alongside Mailbox; Team is a primary
  sidebar destination beside Chat, Projects, Agents, and Wiki (also available in
  the mobile bottom navigation).
- The desktop content frame keeps 12px corners, a 10px right/bottom inset, and
  a theme-aware shadow. Preserve this frame when refining the UI inside it;
  mobile content stays full-width and the Whiteboard keeps its canvas styling.
- Whiteboard titles and save feedback share one compact header row. Editing
  saves silently. Cmd/Ctrl+S flushes pending changes and shows Saving, then Saved
  for two seconds after acknowledgement. Leaving with pending edits waits for
  saving before navigation; failures offer Stay or Retry. Closing/reloading a
  window with pending edits retains its unload warning. Offline drafts,
  recovery problems and read-only state remain visible in the header. Save
  errors use the bottom-right toast; draft recovery details and Export/Discard
  actions open from the header's save status without adding banners above the
  canvas.
- Whiteboard two-finger trackpad scrolling pans the canvas in both directions
  at the same speed as Studio. Pinching zooms; scrolling within a note stays
  inside its text editor.
- Whiteboard ink renders in a separate layer at most once per animation frame.
  Mutation receipts keep confirmed geometry visible until metadata catches up;
  newly drawn ink keeps its points through save and undo/redo without reloading.
  Selected resize controls and note drag handles stay visible while saving.
- Whiteboard defaults to Move: dragging empty canvas pans, and dragging an
  unlocked object moves it directly. Notes keep their text editor and drag handle.
  Movable objects and selected groups use the move cursor; canvas panning uses
  the hand cursor. Text, connection and resize controls keep their own cursors.
  The single Select (S) toggle enables box selection of objects and complete ink
  strokes; click it again or press Escape to return to Move. Shift+drag also
  selects a region while in Move. A selected group moves and styles together.
  Cmd/Ctrl+A selects all objects and connectors; Cmd/Ctrl+C/V copies and pastes
  the selection, including frame children, internal connectors, full note text
  and ink. Pasted objects are selected and offset from the originals; one Undo
  removes the pasted group. Native text shortcuts stay inside text inputs.
  Copy takes up to 100 objects/connectors at a time; one edit, and its undo,
  may touch up to 1,000 objects. Clipboard
  data transfers between Boards in the same company; when async clipboard
  writing is unavailable, the native clipboard token works in the open Board.
  Frame (F) is drawn corner to corner with a live size preview, in either
  direction. It adopts fully enclosed, unlocked top-level objects. Escape
  cancels a drag. Click a frame's name in Move or Select to edit it; Enter or
  clicking away saves, and Escape cancels the name edit. Locked and read-only
  frames cannot be renamed. Placement uses crosshairs, text uses an I-beam, and Highlighter
  (H) uses a chisel-tip marker icon and cursor.
  The toolbar starts with Select, a divider, then Pen. Pen opens a drawing tray
  that groups Pen (P) and Highlighter (H), with illustrated tools, a two-row
  color palette, a rounded custom color popover (spectrum, hue, HEX and RGB),
  and visual stroke sizes without a numeric size readout. Each brush keeps
  its own color and width while switching tools; Highlighter starts yellow at
  20 px. Clicking Pen again or clicking outside the drawing controls closes
  the tray while keeping the current brush, color and width active. Pen reopens
  it; P/H select their brush and open it. Another tool closes the tray, and
  Escape returns to Move. Reference cards have been removed; files are added
  by dropping or pasting them (see Whiteboard files).
- Whiteboard bottom tools and zoom controls use the same hover-guide cards as
  Studio, with short descriptions and disabled reasons. Whiteboard guides omit
  shortcut badges and floating action icons. Each preview
  animates the tool’s action while the guide is open, with a static illustration
  for reduced motion. Guides also open on keyboard focus and dismiss on click
  or Escape.
- Whiteboard tool options sit directly above the bottom toolbar. Color presets,
  custom colors and stroke width follow the active tool; selection actions appear
  in that same dock when objects or connectors are selected. Move and an empty
  selection keep the canvas clear of style controls. A single Text tool (T, with
  N retained as an alias) creates plain text or paper notes using a compact
  Background palette: none, yellow, blue, pink or green. The same palette appears
  for a single selected text or note; choosing a background applies immediately
  while preserving its content and identity. Paper uses dark text and 16px corners;
  both styles keep the same text inset and blank drag area, with a subtle selection
  outline without blue corner handles. Changing the background never shifts the text.
  Click to write in Move or Select, drag the blank top edge to move, drag an edge
  to resize, and use Delete/Backspace on the selected object to remove it.
  Shape, frame and multi-object selection options share the Pen tray's rounded surface, circular color swatches
  and custom color picker. Shape and Frame options keep only Stroke and Background colors,
  with aligned swatch rows, plus the shape chooser before shape placement.
  Both tools retain their own colors and preview them while placing an object.
  A single selected shape or frame has no stroke-width row or lock/delete buttons.
  Frames use 16px corners. Other selections retain the visual
  stroke-width choices from Pen. Selection colors and stroke width apply directly without an
  Apply style button. Each change preserves the other properties; custom colors
  preview live and save when the pointer or keyboard gesture ends. Locked objects
  are excluded, and controls wait for pending scene changes before another edit.
  Align and Group appear only for multiple selected objects.
  Selected shapes and frames hide the blue resize outline and corner squares
  while keeping invisible edge and corner targets for resizing.
  Zoom, presence and save-detail popovers share the tray's border,
  corners and shadow. Option panels scroll when vertical space is limited.
- Collection directories share a 1040px content width, a 76px minimum header,
  and the workspace typography tokens. Preserve the shared 1px row dividers
  and straight row edges; use 8px corners for controls and 12px for cards and
  overlays. Feature overrides must not erase the shared list/header borders.
- Chat and Wiki share a sidebar toggle after Forward in the workspace titlebar.
  The collapsed preference is saved per company. When collapsed, hovering the
  Chat or Wiki rail item opens its navigator over the full height of the workspace,
  at the same width as its expanded sidebar, without changing pages; selecting an
  item navigates and closes the popup. Arrow Right/Down opens it from the keyboard,
  and Escape closes it. The current page's sidebar stays mounted when moved into
  the popup, preserving search, scroll and resized width. Mobile keeps its drawers.
  Register future pages in `workspace-sidebar-pages.ts` and wrap their navigator
  in `WorkspacePageSidebar` to reuse this behavior.
- In Light mode, the main content background of every screen is white (`#FFFFFF`),
  including Custom themes. The default Neutral theme uses `#F9F9F9` navigation
  sidebars and white headers, panels, dialogs and inputs across Chat, Settings,
  Projects, Agents and Wiki.
  The palette resolver also owns control, hover, selection and divider colors;
  feature styles must not replace them with their own Light-mode palette.
  Subtle dividers separate panes; inputs and attachments retain faint borders.
  High-contrast mode keeps stronger separators. Dark uses its independent palette.
- Axios `apiClient` is the renderer's HTTP transport. Desktop artifact file
  downloads use a scoped IPC bridge so the main process can revalidate its disk
  cache with the stored session; web downloads continue through `apiClient`.
  RTK Query owns server-state caches;
  feature APIs extend the shared `zenithTeamApi` with `injectEndpoints`. Redux Toolkit
  slices own global UI state and React state owns component-local flows.
- Task chat's **Results** panel reads paginated Outputs, publication URLs, and
  source attachments from `GET /companies/{companyId}/tasks/{taskId}/results`.
  It refreshes through scoped realtime invalidation, including every publication
  transition and reconnect. Output files open in the server-backed file viewer; working
  files from loaded messages use the existing desktop viewer. Image, PDF,
  Markdown, and text previews support downloading; websites use a sandboxed
  iframe with an external-browser fallback. Per-task open state, selection, and
  feedback references persist in the conversation dock alongside drafts.
  New conversations start with the Artifacts pane closed. Explicit open/close
  choices and selected artifacts remain saved per conversation.
- Expanded Task detail uses a compact header and a latest-Run update above the
  original Markdown brief, with a readable line length. It replaces the Project
  header while open. Run completion and Task status remain separate.
  Results and reference attachments have separate sections; recent Activity
  expands to show revision details. The Details sidebar keeps assignment,
  priority, due date and labels visible, with secondary metadata under More
  details. Dependencies use semantic theme colors in Light and Dark modes.
- The Project Task inspector is a resizable 480px drawer over the current view,
  so opening it keeps the board columns and scroll position stable. Narrow
  workspaces show it at full width. Assignment, priority and due date precede
  a six-line expandable brief; the next action or prerequisite stays in the footer.
- Analytics uses URL-backed tabs and loads each section's breakdowns when opened.
  Analytics is the single destination name in navigation and Settings; Explore
  omits the duplicate. Legacy `/settings/usage` links retain their filters and
  translate calendar week/month/year periods to the matching date range.
  Overview defaults to Runs, shows summary metrics and the trend first, and places
  the compact activity calendar below the breakdowns. The calendar opens the
  latest month in the selected range. Advanced filters expand from the toolbar;
  active filters stay visible as removable chips. Unavailable cost is never zero.
- Computers is the machine-management destination in Agents and Settings,
  including setup links. It keeps the `/settings/agents` route; runtime counts
  and provider details describe the connections within each computer.
- Company-resource contracts are adapted at the API boundary. The server emits
  `resource.created` / `resource.updated` / `resource.deleted`; only the
  `ProjectResource` RTK cache tag keeps the retired noun, because renaming it
  would break invalidation. Product copy, routes, feature folders, and Redux
  keys use Agents, Resources, Cockpit, and Skills.
- **Settings → Appearance** uses `next-themes` for device-wide Light/Dark/System
  mode and a Redux UI slice for the active company palette. Theme preferences
  are stored locally per user and company, independently of logout's persisted
  state purge. Neutral, six gradient presets and one custom theme are available,
  with Neutral as the default. Presets and custom colors apply in Light mode. Dark mode uses its
  own fixed palette, independent of the selected theme; System follows the device.
  Custom themes allow gradient start/end, accent and notification colors.
  Surprise me generates and immediately saves a coordinated custom palette,
  preserving the selected font and color mode.
  Existing saved choices are preserved; resets
  select Neutral without changing the font. Gradient image tokens are separate
  from solid fallback colors, and theme previews use the Light-mode resolver. Source
  colors live in `src/app/appearance/`; shared workspace CSS consumes the resolved
  semantic tokens, including separate accent fill, text, and notification colors.
  Light controls use a shared neutral background. Older saved `background` colors
  are ignored without resetting the other theme or font choices.
- **Settings → Appearance → Font** selects the app font, defaulting to System.
  Font preferences share the per-user, per-company local appearance storage and
  synchronize across tabs. Theme resets preserve the selected font. Workspace
  font overrides are shared with auth entry pages; the public landing page keeps
  its system font stack. Bundled fonts and their licenses live in
  `src/app/fonts/`; system fonts use the fonts installed on the device.
- Application code must not import Zustand. The dependency remains temporarily
  because `@xyflow/react` uses it transitively for the Project, Task, and Studio
  canvases; removing it from the dependency graph requires replacing or removing
  `@xyflow/react` in a separate task.
- Text inputs, textareas, and selects use their existing border for pointer and
  keyboard focus, without an additional outline. Compound fields own the outer
  focus feedback through `:focus-within`; invalid fields retain their danger
  border. Buttons, links, and non-text inputs keep a visible keyboard outline
  in the muted text color. Preserve validation and selection styles.
- Enabled buttons, links, menu items, tabs, disclosure controls, and clickable form
  labels use `cursor: pointer` through the shared rules in `src/app/global.css`.
  Use native controls or the appropriate interactive role; custom click targets
  can opt in with `data-clickable="true"`. Keep text-entry and drag cursors for
  their respective interactions, and a non-pointer cursor for disabled controls.
- Do not add TanStack Query or Zod. For work under `frontend/`, use the
  existing checks and do not add or expand tests unless the task explicitly
  requests them.
- Browser messages use the REST conversation command API. Conversation,
  notification, and Workbench updates share one receive-only native WebSocket
  at `/api/v1/ws?company_id={companyId}`.
- API calls use `VITE_API_URL` when set. Without it, Vite dev on port `5173` uses the
  matching loopback host on the Go API port `8788`, such as
  `http://localhost:8788/api/v1` or `http://127.0.0.1:8788/api/v1`;
  static/deployed origins use same-origin `/api/v1`.
  Deployed browser origins ignore loopback overrides such as `localhost` and fall back
  to same-origin so production builds cannot call a developer machine.
- Auth uses the backend in every runtime, including Vite dev. Do not add browser-side
  auth adapters that mint users, sessions, OTPs, refresh tokens, tenant data, or
  company data.
- Auth entry pages share an inset illustration panel and a 440px desktop form with
  44px controls; narrow screens keep a 360px form. The Fold team scene sits below
  an editorial headline; legal copy stays at the foot of the form panel. The shared
  workspace appearance resolver uses the current user's company preferences when
  available, otherwise the last appearance used on this
  device, and Neutral with System font when none has been saved. The last appearance
  stores only visual preferences, survives logout, and synchronizes across tabs;
  it never overrides a signed-in company's own preferences. Light/Dark/System
  follows the device-wide setting, defaulting to Light on a fresh installation.
  Text over the illustration adjusts the theme accent for contrast. Branding,
  headings and controls remain live components; the locally bundled WebP contains
  only the illustration. Narrow screens show the brand and form first and do not
  load the decorative scene. Recovery, verification and invitation context keep
  their task-specific instructions; login and signup use brief action-oriented copy.
- Password auth uses the Go API's cookie-session routes: `/auth/signup`,
  `/auth/login`, `/auth/refresh`, `/auth/logout`, and `/me`. The browser sends
  credentials and the matching `zenith_team_csrf`/`zenith_team_refresh_csrf` value in
  `X-CSRF-Token`; it does not persist an access token in JavaScript storage.
  Inside the desktop shell, password auth instead routes through the Electron
  main-process bridge on the bearer transport. The app's own session lives in
  `<userData>/desktop-session.v1.bin`, encrypted with Electron `safeStorage`;
  the main process renews it and signs it out itself, and the renderer holds
  the access token in memory only. There is no fallback to a plain file: when
  `safeStorage` is unavailable or the store cannot be written, sign-in,
  sign-up, the email code and Google sign-in fail with
  `kind: "session_not_saved"` after the main process signs out the session
  the server just started, and the sign-in screens say so in a few words
  (`getSessionNotSavedMessage` in `src/features/auth/model/auth-api-utils.ts`),
  never as a wrong password. The daemon has a session of
  its own:
  after sign-in, and before each daemon launch that finds none, the main
  process calls `POST /auth/daemon-session` and hands the answer to
  `zen desktop-session install`, which writes `daemon-auth.json` in Desktop's
  private daemon home through the Go `authfile` implementation; sign-out runs
  `zen desktop-session clear`. The daemon is started with
  `ZENITH_TEAM_DESKTOP_SESSION_PATH` naming the app's store, so its Runs are kept
  away from it. The first launch after this change moves the session the app
  used to share with its daemon (`zenith-team-home/auth.json`) into the app's
  store, under the CLI's lock and only once no older daemon still renews it;
  until then the app renews it in place under the same lock, as the CLI
  does, so the person stays signed in. The cookie-session flow remains
  browser-only.
- **Join…** beside **Companies** in the account menu and the company menu in
  **My Account → Your companies** opens an invite-link dialog, including for
  members who already own a company. Pasting an invitation link there or in
  onboarding opens the in-app `/join/:token`
  review in both web and desktop. Only the token is used against the current
  API; the pasted origin is never opened. Acceptance requires confirmation,
  and authentication or account switching returns to the same in-app review.
  Shared `/invite/:token` URLs opened directly in a browser retain the desktop
  bridge.
- Newly joined members continue to `/companies/:companyId/setup-agents` to
  choose their own computer and set up the Company's existing Agents together.
  Names and roles come from the shared roster; this flow writes only the
  viewer's bindings, preserves existing bindings, and excludes archived Agents.
  Provider compatibility and sign-in/offline states are shown before saving.
  Partial failures keep successful bindings and let the member retry the rest.
  A per-user, per-Company local navigation preference resumes an unfinished
  setup after reload; completion or **Set up later** dismisses it. The server's
  bindings remain the authority for setup and execution. Founder onboarding
  and the Company's initial Agent setup stamp keep their existing meaning.
- When email verification is configured, signup and password login for an
  unverified account return a pending flow. The frontend uses
  `/auth/email-verification/verify`, `/resend`, and `/status`; OTP values are
  never generated or validated in browser code.
- Password and Google auth failures from missing `/api` routes must surface the
  product-facing connection message instead of generic login or transport copy.
- Browser Google OAuth is server-owned. The frontend first checks
  `/auth/providers`, then calls `/auth/google/web/start` and follows the returned
  Google authorization URL. Google returns to the Go API callback, which sets the
  cookie session and redirects to `/auth/google/callback` for `/me` hydration.
  Configure Google credentials and callback URLs on the Go server; no Google client
  secret or client ID is read by browser code.
  The public API proxy must also allow the packaged renderer origin
  `zenith-team-app://renderer` with credentials; otherwise the desktop callback page
  reaches Google but the final `/auth/google/callback` API request is blocked by
  CORS and appears as a connection error.
- Zenith Team desktop uses `local`, `dev`, and `prod` environments. `electron:dev`
  runs as `local`; `electron:package` and `electron:make` default to `prod`.
  Dev and prod packages need their server's origin: set
  `ZENITH_TEAM_DESKTOP_DEV_ORIGIN` for `dev` or
  `ZENITH_TEAM_DESKTOP_PROD_ORIGIN` for `prod` (or a non-loopback
  `VITE_API_URL`). The build stops when none is set.
  A packaged local build must set `VITE_API_URL` to the local API origin and
  `ZENITH_TEAM_DESKTOP_API_BASE_URL` to its normalized `/api/v1` base so the
  Electron main process trusts renderer authentication requests.
  App data remains under `Zenith Team Local`, `Zenith Team Dev`, and `Zenith Team` for
  local, dev, and prod respectively. The apps display as `Zenith Team` and
  `Zenith Team Dev`; the stable data directories preserve existing sessions.
  Every variant owns `<userData>/zenith-team-home` and a non-default health port
  (`19714` prod, `19715` dev, `19716` local). Desktop never reads, starts,
  stops, or overwrites the terminal CLI's default `~/.zenith-team` home or port.
  Settings, diagnostics, daemon state, credentials, and update preferences
  remain isolated by each variant's Electron `userData` directory.
  Electron scripts load separate env files: `electron:dev` reads `frontend/.env`
  then `frontend/.env.local`; dev and prod packages read `frontend/.env.dev` or
  `frontend/.env.prod` after `ZENITH_TEAM_DESKTOP_ENV` selects the variant. Shell
  environment variables still take priority over file values.
- Zenith Studio is optional. When `ZENITH_STUDIO_API_URL` is set at build
  time, the packaged CSP permits Studio HTTP and Socket.IO at that origin; it
  must be a credential-free HTTPS origin without paths, query strings,
  fragments or wildcards (packaged `local` builds also permit HTTP loopback).
  This only configures the CSP allowlist; the authenticated editor response
  still selects the Studio connection, and Studio must allow
  `zenith-team-app://renderer` through CORS. With Studio on, assets from the
  hosts in `ZENITH_STUDIO_MEDIA_ORIGINS` (comma separated, default
  `https://storage.googleapis.com`) are permitted by `img-src`, `media-src` and
  `connect-src`. Storage CORS still applies; the CSP does not replace bucket
  access policy.
- Desktop Dev releases (`ZENITH_TEAM_DESKTOP_ENV=dev`) and unpackaged local runs
  expose **View → Toggle Developer Tools**: **Cmd+Option+I** on macOS or
  **Ctrl+Shift+I** on Windows/Linux. A dev build embeds this environment in
  the package, so users can open DevTools without setting any environment
  variables. Packaged production builds omit this menu command.
- Desktop builds must not open on the public web landing page. The Electron root
  route sends unauthenticated users to login, incomplete sessions to onboarding, and
  completed sessions to the app shell.
- Desktop icons live under `electron/assets/icons/` using the Electron Forge
  platform shape: `macos/icon.icns`, `windows/icon.ico`, Linux PNG sizes, and
  shared `icon.png` source material. Keep `macos/tray-template.svg`,
  `macos/tray-template.png`, and `macos/tray-template@2x.png` when regenerating
  the app-icon set; Electron uses the dedicated Fold template PNGs for Zenith
  Team's macOS menu-bar item. Forge selects the
  platform-specific icon in `forge.config.cjs`.
- Desktop package commands:
  - `corepack pnpm electron:dev` starts the local Vite-backed Electron shell.
    On macOS this runs through Electron's development bundle, so the Dock may
    still show the Electron name and icon. Do not use `electron:dev` for Dock
    icon/name QA; use `electron:package` or `electron:make` to verify production
    app identity from the packaged `.icns`. `ZENITH_TEAM_ELECTRON_DEV_ARGS` passes
    extra switches to the dev window, space-separated — for example
    `--remote-debugging-port=9223` so a smoke run can drive the renderer over
    CDP. Only the dev script reads it; packaged apps never do.
    Native macOS notifications require a code-signed app with Electron 43;
    verify notification delivery with a signed package.
  - `corepack pnpm electron:package` cleans `out/`, builds the renderer, and creates
    the local app package.
  - `corepack pnpm electron:make` cleans `out/`, builds the renderer, creates the
    distributable macOS DMG plus ZIP, and writes `.sha256` checksums next to
    the artifacts.
  - macOS arm64 and x64 prod artifacts are named like
    `out/make/dmg/darwin/arm64/Zenith-Team-prod-darwin-arm64-0.1.0.dmg` and
    `out/make/dmg/darwin/x64/Zenith-Team-prod-darwin-x64-0.1.0.dmg`, with matching
    ZIP fallbacks under `out/make/zip/darwin/<arch>/`. Windows and Linux x64
    ZIP artifacts are named like
    `out/make/zip/win32/x64/Zenith-Team-prod-win32-x64-0.1.0.zip` and
    `out/make/zip/linux/x64/Zenith-Team-prod-linux-x64-0.1.0.zip`.
- Desktop `Check for Updates` uses a manual update/download link unless the packaged
  build includes native auto-update release env. Set `ZENITH_TEAM_DESKTOP_UPDATE_URL` to
  override the manual URL; otherwise local builds open `http://127.0.0.1:5173` and
  dev and prod builds open the GitHub Releases page. Signed release builds can enable native
  update checks with `ZENITH_TEAM_DESKTOP_AUTO_UPDATE=true`, a credential-free HTTPS
  native feed, and a release manifest. Static builds use
  `ZENITH_TEAM_DESKTOP_AUTO_UPDATE_FEED_BASE_URL`,
  `ZENITH_TEAM_DESKTOP_AUTO_UPDATE_MANIFEST_URL`, and
  `ZENITH_TEAM_DESKTOP_AUTO_UPDATE_STATIC_FEED=true`. Supported native builds persist the
  installed version and the user's automatic-check preference under Electron
  `userData`, check ten seconds after startup and every four hours, and show a
  blue circular update button at the bottom of the left sidebar, above the account
  avatar, when a newer manifest version exists. Its tooltip names the action and
  version. Electron starts downloading only after the user clicks the button;
  it shows download progress in its tooltip and becomes a restart action when the
  package is ready. The update dialog stays open during that download, reports
  measured progress, and reveals **Install and restart** only after the native updater
  reports the package ready at 100%. Native **Check for Updates**, directly below **Settings** in the
  application menu on macOS or **File** on Windows, works before sign-in and keeps
  the full update flow in the desktop app. A newer release opens one continuous
  update dialog from download through restart. **Settings → Support** mirrors the
  same updater state but is not required to check, download, or install an update.
- Public web desktop download buttons read a public release manifest from
  `VITE_ZENITH_TEAM_DESKTOP_RELEASE_MANIFEST_URL` with `cache: "no-store"` and
  use its immutable, versioned installer URLs. There is no built-in channel:
  without a manifest, or when it cannot be loaded, the buttons stay disabled.
  `VITE_ZENITH_TEAM_DESKTOP_MAC_ARM64_URL`, `VITE_ZENITH_TEAM_DESKTOP_MAC_X64_URL`,
  `VITE_ZENITH_TEAM_DESKTOP_WINDOWS_URL` and `VITE_ZENITH_TEAM_DESKTOP_LINUX_URL`
  override single platforms. Keep signed artifacts out of git; host them in
  release storage whose CORS policy lets the web origin read the manifest.
- Unsigned packages need no secrets: run `corepack pnpm electron:make`. CI's
  `desktop` lane builds unsigned macOS and Linux installers as workflow
  artifacts whenever the desktop packaging changes. Signed builds come from
  the `desktop` job in `.github/workflows/release.yml` (Zenith Team, on `v*`
  tags) and from `.github/workflows/desktop-dev.yml` (Zenith Team Dev, by
  hand from `main`). Each passes the Apple secrets only to its macOS signing
  steps; the Linux job references none.
- macOS signing is opt-in so unsigned local builds still work. Set
  `ZENITH_TEAM_DESKTOP_MAC_SIGN=true` and optionally
  `ZENITH_TEAM_DESKTOP_MAC_SIGN_IDENTITY` to enable signing. Set
  `ZENITH_TEAM_DESKTOP_MAC_NOTARIZE=true` with one notarization credential set:
  `ZENITH_TEAM_DESKTOP_MAC_NOTARY_KEYCHAIN_PROFILE` plus optional
  `ZENITH_TEAM_DESKTOP_MAC_NOTARY_KEYCHAIN`, or
  `ZENITH_TEAM_DESKTOP_MAC_NOTARY_API_KEY`,
  `ZENITH_TEAM_DESKTOP_MAC_NOTARY_API_KEY_ID`,
  `ZENITH_TEAM_DESKTOP_MAC_NOTARY_API_ISSUER`, or
  `ZENITH_TEAM_DESKTOP_MAC_NOTARY_APPLE_ID`,
  `ZENITH_TEAM_DESKTOP_MAC_NOTARY_APP_PASSWORD`,
  `ZENITH_TEAM_DESKTOP_MAC_NOTARY_TEAM_ID`.
  Local `electron:package` and `electron:make` ignore macOS signing/notarization values
  that only come from `.env.prod`; this prevents local packaging from failing on Apple
  notarization state. For a signed release package, set the signing variables in the
  shell command or set `ZENITH_TEAM_DESKTOP_RELEASE_PACKAGE=true` to intentionally use the
  release signing values from `.env.prod`. For the local signed macOS release path,
  run `corepack pnpm electron:make:mac:signed`; it forces `darwin`, signing,
  notarization, and release-package mode.
- Desktop diagnostics write redacted main/renderer events to the app user data logs
  folder, such as `~/Library/Application Support/Zenith Team/logs/zenith-team-main.log` on
  macOS. Electron crash dumps are reported from Electron's `crashDumps` app path.
  Use **Help -> Copy Diagnostics Summary** to copy support-safe app version,
  environment, platform, log path, and crash dump path metadata.
- On Windows, `electron:make` is set up to produce a Squirrel.Windows installer
  plus a zip. Windows desktop builds are not supported yet, because the bundled
  `zen` CLI does not build there.
- Production registers `zenith-team://` for app links while the side-by-side dev
  package registers `zenith-team-dev://`. Google OAuth desktop callbacks use isolated
  loopback ports and route into the existing hash router.
- Public artifact links attempt to open the desktop app once after the link is
  resolved, and offer **Open in app** when the browser requires a click. The
  public viewer stays available in the browser. Desktop opens the same
  `/share/artifacts/:token` route with the selected `item`, without requiring
  sign-in or Company membership. Only this public artifact path and its optional
  item ordinal are accepted by the desktop protocol handler.
  In Desktop, public artifacts use the same full-page reader and workspace shell
  as other artifacts. Without a ready session, the reader keeps the saved app
  appearance and a native draggable title bar, without requiring sign-in.
- Desktop downloads use Electron's native download event, sanitize the suggested
  filename, save to the user's Downloads folder, and log redacted start/finish
  metadata. Renderer code does not receive filesystem access.
- Chat link cards in Desktop load public page metadata and raster thumbnails in
  the main process through a trusted IPC bridge. Requests run on the member's
  computer, without cookies, referrers, or a server preview endpoint. The loader
  checks and pins public DNS addresses on every redirect, accepts standard
  HTTP(S) ports, reads at most 512 KB of HTML and 5 MB per image, and gives each
  preview eight seconds. It runs three previews at a time, coalesces duplicate
  URLs, and caps pending requests at 32. Its in-memory LRU cache holds at most
  100 entries / 24 MiB of estimated string storage, with a 15-minute success TTL
  and a one-minute failure TTL. Cards still load near the viewport, at most
  three per message. Packaged CSP stays intact; web cards retain their existing
  credential-free fetch and URL fallback when CORS prevents metadata retrieval.
- Website result previews in Desktop use an isolated `WebContentsView` inside the
  result panel, with Back/Forward, Reload, and Open external. Each preview gets a
  temporary session with no application preload, Node access, or Zenith Team cookies;
  closing the panel or reloading the app disposes the view and clears its storage.
  Website downloads use the native Save dialog. The native view follows the panel
  bounds and hides behind application dialogs and menus. The web client retains
  its sandboxed iframe, subject to the website's embedding policy.
- Onboarding collects Company information (including its website), sets up
  Andrew with Codex, offers Gmail, Drive and GitHub plugins, then opens a team
  selection screen. Creating Andrew through the Agent API also provisions the
  four starter Agents. The founder can edit their names, roles and instructions,
  add custom Agents, or start with Andrew alone. Confirmation saves the selected
  team and archives unselected Agents through revision-guarded mutations before
  opening an initializing screen. A platform-authored research Run uses the
  bound runtime's advertised Luna model (no reasoning if offered, otherwise its
  lowest supported level). It reads bounded website/reference/plugin sources
  and writes linked Company Wiki pages with `wiki_write`; no user message is
  injected. One onboarding-owned controller above the onboarding and workspace
  routes observes the receipt only until the Run exists: model discovery and the
  start stay explicit (the initializing screen starts or resumes them once),
  and the screen then enters the workspace itself. `initializationPending`
  means the receipt exists and the research has not been started; until then
  the CEO conversation shows a compact status in place of its composer (drafts
  are kept) and the workspace status strip offers **Start research**. After the
  start nothing needs a client. The same strip follows the research Run on
  every page but the Wiki: running with the pages written so far, waiting for
  the computer, ready with **Open overview**, or failed with **Retry** and
  **Review setup**. The Wiki rail item shows an activity dot, the Wiki shows a
  research banner (pages appear through its realtime events), the research
  chat explains that a message waits for the research and offers a new chat,
  and the founder gets an Inbox notice that opens the Wiki. A stale send's
  `conversation_initialization_pending` response preserves the draft and
  explains when it can be sent.
  Team draft edits survive reloads in the same browser session. User and Company profile editors also
  use their revision-guarded backend mutations and apply the authoritative response.
- Owner cockpit hydration uses one Company `bootstrap` composite request when
  the active session carries the owner role; admin/member and older role-less
  sessions retain the member-safe read fan-out. Opening Task chat uses one Task
  `workspace` request to seed its existing Task, Conversation, Message, Run,
  queue, and dependency state; direct chat and incremental history/catch-up
  keep their narrower reads.
- Product UI realtime uses one authenticated native WebSocket at
  `/api/v1/ws?company_id={companyId}`. Company and eligible-user audiences are
  implicit; the renderer sends no application subscription frames. REST owns
  commands and recovery queries. Durable transcript rows arrive as
  `run.message_created`. Same-machine text/thinking scratch is ephemeral: the daemon's
  authenticated loopback preview stream reaches the desktop renderer through
  Electron IPC, never enters RTK Query, and is discarded on terminal state or
  reconnect. Browser and remote clients receive only durable transcript items.
- Authenticated product data refreshes from committed events and recovery, without
  periodic GET polling. The central synchronizer batches invalidations and waits
  for older in-flight responses before refreshing subscribed RTK Query entries;
  cached screens with no subscribers issue no requests. Channel invocation,
  publication, runtime catalog, Schedule, Calendar and Wiki-read signals are
  documented in `../docs/api/realtime.md`. Presence heartbeat writes, bounded
  external sign-in checks and anonymous public artifacts retain their own timers.
  Returning online replaces even a socket still marked open and runs the same
  recovery reads as a reconnect to clear stale chat state after a network change.
  Direct Run lifecycle events also refresh the chat list's queued/running counts.
- Member presence records focus and visibility activity before sending the
  return heartbeat. The first interaction after idle refreshes presence immediately;
  an inactive request already in flight sends a follow-up heartbeat if activity
  resumes before it finishes. Status still comes from the server, and manual Away
  remains in effect until the member changes it.
- Conversation panels hydrate durable history from
  `/conversations/{conversationId}/messages`; do not seed product chat with canned
  replies.
- Model pickers share a five-minute RTK Query catalogue per user, Company,
  runtime and provider setup. Reopening chat/settings reuses both completed
  models and in-flight discovery; roster heartbeats do not restart it. Explicit
  Refresh keeps the existing options visible while discovering again. Setup that
  submits a discovery receipt revalidates it within the server's shorter lifetime.
- Realtime conversation panels reconcile optimistic user bubbles with REST
  receipts and durable history by client request ID; only server acceptance
  establishes saved state. Text sends release the composer immediately and keep
  a separate pending draft until accepted. That draft survives navigation and
  reload for explicit retry with the same request ID; it never sends itself on
  reload. The next draft stays editable while the previous request is in flight,
  and retry never clears it. File sends retain upload progress and attachment
  recovery until accepted.
- Direct messages resolve the caller's existing Company conversation with the selected
  Agent, preserving its history and draft. Each direct-message row's
  action menu pins/unpins the conversation using revision-checked per-user state;
  pinned chats move to Pinned, while the CEO stays in its dedicated top section.
  Every AI worker is an Agent: the root Agent (`reportsTo === null`) is the CEO
  and every other Agent reports to one. Each Agent carries its own `avatarKey`
  and `accentColor`; the client falls back to the generated avatar and the
  default palette when they are null. Task chat uses the Task conversation and
  Task message endpoints. Dedicated deliverable and resource chats remain unavailable
  until their backend contracts exist.
- Creators can rename their direct Agent chats from the conversation actions
  menu, including archived chats. Titles must be nonblank after trimming Unicode
  whitespace and byte-order marks, with at most 120 Unicode runes. Saved titles
  display literally in the list and header; only blank or null titles fall back
  to `Chat with <Agent>`. Rename sends the conversation's `expectedRevision`;
  saving the same title at the current revision is a no-op. Errors retain the
  draft in the open dialog. A stale revision reloads the latest saved title and
  requires an explicit **Save my title** retry. Success updates the cached list
  and header and returns focus to the actions button, or the navigator search
  if the renamed row no longer matches the filter.
- The shared chat artifact panel opens the newest saved artifact when one
  arrives, including Zenith Studio results and other Agent outputs. It replaces
  an older artifact, Studio, or generated-file preview and reopens a closed panel.
  Initial history, metadata refreshes, and loading older pages preserve the
  current selection; users can browse earlier results until another arrives.
- Studio Mode offers Generate on supported nodes without output and in the
  settings panel. Completed nodes show their output without an action footer;
  clicking the node opens settings, including Regenerate. Prompt/settings drafts
  render on the canvas immediately and autosave after 400 ms of idle input,
  retaining newer keystrokes during writes.
  Generate and closing Studio flush pending edits; save failures and concurrent
  edits remain visible with retry/conflict recovery. Only server acknowledgements
  enter the saved cache. Pending, submitted and executing nodes share the
  centered Generating label and sweeping shimmer across the node, with no
  duplicate action footer and a static treatment for reduced motion.
  Collaboration updates reconcile completion. Repeated clicks are blocked while
  pending or running. Uploaded sources, notes, groups and read-only nodes have no
  generation action. The inspector's Versions control navigates
  stored output history and applies a selected version through the native restore
  route with optimistic locking. The Studio API must admit the same editor bearer
  token on execute and output-version restore as on canvas CRUD; deploy that API
  support together with this client change.
- Agent Mail drafts open in the same resizable right panel, with recipients,
  subject, message, editing and Send controls beside the chat. A new draft opens
  automatically; an existing pending draft opens on the first visit. Closing it,
  receiving status updates or loading older messages does not reopen it. The
  inline email card opens it again. The dock saves only group/Run selection ids;
  email content stays in the authorized RTK Query cache. Every entry point uses
  the resolved `mailbox.manage` permission; editing and sending in a Task chat
  also require `project.tasks.write`, independently of approval permissions.
  Denied refetches discard the view, while temporary network or server failures
  keep unsaved edits in the mounted editor and offer a retry.
- Desktop artifact thumbnails, previews and downloads share a disposable disk
  cache in `userData/artifact-cache/v1`. It survives restarts, keeps at most
  500 MiB / 256 files using least-recently-used eviction, and caches files up to
  100 MiB each. Main-process IPC accepts artifact IDs only and uses the stored
  desktop session. Every read revalidates current access through the original
  private download route; a matching attachment ETag returns 304 without reading
  object storage. Missing or corrupt entries download again. Concurrent reads
  of the same route share one request, with independent cancellation. Logout and
  account replacement cancel reads and delete the cache. It grants no offline
  access and is not a backup. Web downloads are unchanged. The updated server
  and desktop must both ship to avoid repeat transfers; older servers still
  download normally. No new environment variables are needed.
- Human direct messages, group chats, and channels use the discussion surface.
  One-to-one human chats show Active / Away at the avatar's lower corner in
  the navigator, including pinned chats, and the conversation header.
  Presence uses the shared Company
  polling batch; unavailable or stale presence hides the indicator. The row's
  tooltip and accessible label include the current status.
  Channel, direct-message and channel-management lists use the shared five-minute
  RTK cache after their last subscriber leaves, so quick route or view switches
  reuse loaded pages. Mutation and realtime invalidation still refresh changed
  lists; cache keys retain the Company, user and discussion-session generation.
  Returning to a recently opened discussion reuses its bounded RTK history and
  cached details while a state check runs in the background. A healthy connection
  and matching access generations are required for this immediate display;
  recovery and access changes revalidate first. Read-marker changes refresh
  personal state without reloading the main message history. History retains its
  existing five-minute, ten-conversation limit and scroll anchors.
  All / Pinned / Artifacts switch the main view. Artifacts fills the conversation
  area with a gallery by default; the shared Grid / List switch offers compact rows.
  Both views search titles, descriptions, and file names across paginated saved work.
  Uploaded images display inline at their original aspect ratio in messages and
  thread replies. Multiple images share a height within each responsive row, with
  widths proportional to their original aspect ratios and no cropped content.
  Image previews have no filename footer; hovering shows the filename and clicking
  opens the resizable right-hand viewer.
  Previews load near the viewport through the authenticated attachment route.
  Unavailable single images and files above the preview size limit use a file card.
  Other uploaded files and shared artifacts use compact cards. Markdown, PDF,
  PowerPoint, images, media, and spreadsheets reuse the artifact renderers;
  draft uploads can also be previewed before sending.
  Opening a shared artifact stays in the current conversation. Opening from the
  gallery keeps its selected view, search, and scroll position beside the viewer.
  Back, Close, and Escape return focus to the opening card. More groups pinning,
  notification preferences, and eligible channel leave actions.
- Artifact sharing keeps recipient search in a fixed field with a floating
  results list. Selecting a person or conversation shows a compact chip and
  explicit Cancel / Share actions. People and conversation grants are separate;
  conversation access includes current and future members. Existing permission
  changes save automatically, removals confirm their scope, and a successful
  chat share offers Open chat. The footer pairs link access with Copy link;
  access explanations live in its menu, and save/copy feedback is temporary.
  Sharing menus use 14px Iconsax chevrons.
- Approvals and questions use the generated company-scoped API contract. RTK Query
  owns their list, detail, statistics, and revision-checked mutation state. The
  central realtime synchronizer invalidates the company-scoped approval and question
  tags after lifecycle events and on reconnect; feature components must not add their
  own socket, focus, or reconnect refetch loops. Company Plan approvals show affected
  projects and tasks from the server's review projection, ordered by dependencies
  and including human work. Task briefs, full plan changes and request metadata
  expand on demand; optional decision notes stay collapsed until requested.
  Server-rendered Markdown remains the fallback for older approvals. The
  originating run has already ended before either request appears.
- Agent quick actions only show backend-backed chat entry points. Do not render
  local-only create, summary, edit, share, delete, or approval-looking buttons in detail
  rails without a matching API contract. Messaging an Agent opens that Agent's direct
  conversation through the existing REST message path. Do not simulate task execution
  locally unless a backend execution command API exists.
- Canvas controls are view/navigation controls only. The DAG palette may expose
  Select and Pan when they are wired to React Flow state; do not render task node
  creation until a backend task creation command exists.
- Home opens the Chat workspace. The retired dashboard widgets and their Redux
  state, Memory browser, Company Resource Registry editor, Mailbox popup,
  legacy landing components, and standalone avatar previews have been removed.
  `/settings/usage` redirects to Analytics; the old Usage and Cost Computes
  components are no longer part of the client.
- Top chrome Search groups navigation actions into Workspace, Tools, and
  Settings, using the same labels and Iconsax icons as product navigation.
  Company actions require an active Company; restricted destinations also
  respect its resolved permissions. Desktop shortcut hints use Cmd on macOS
  and Ctrl on Windows/Linux; the web dialog omits native-only shortcuts.
  Queries search Tasks, Conversations, and Messages on the server and filter
  available Agents locally. Search failures keep available results visible
  and show an explicit warning.
- The side rail opens Chat, Projects, Agents, Wiki, Team, and Analytics.
  Explore contains Workflows, Calendar, Mailbox, Social accounts, Assets,
  and eligible billing/usage links; account settings live in the profile menu.
- Calendar keeps a 270px sidebar and a vertical utility tab rail beside every
  view. Google Calendar, Google Maps, and Contacts open a separate panel with a
  Coming soon state; these previews do not request provider access or load
  external data. Event creation, details and editing open beside the selected
  slot or entry, flip near viewport edges, and use the space above or below when
  neither side fits. The selected slot or entry keeps its hover highlight while
  its popover is open. Notification links anchor to Create. Calendar surfaces
  follow the workspace theme with distinct control, navigation, panel, event and
  popover shadows, keyboard focus return and reduced motion. Event popovers
  dismiss on Escape or outside interaction; utility panels stay open until closed
  or dismissed with Escape.
- The authenticated shell owns the shared WebSocket, the central RTK Query
  cache synchronizer, and the notification presenter. Feature menus may read
  current cache data, but opening or closing one never owns or restarts the
  connection.
- Cockpit composes Company, Agents, Tasks, Runs, Activity, Runtimes, and direct chats at
  the frontend boundary; there is no `/cockpit` or `/company` compatibility request.
- Top chrome must not fall back to browser-draft company names; before `/company` loads,
  use the neutral `Company` label. Auth session mapping must also use `Company` when the
  backend company name is blank, rather than preserving an old browser draft name.
- Opening CEO chat or starting the first build resolves or creates a Company direct chat
  and surfaces a visible realtime
  unavailable message when the backend conversation cannot be created or loaded.
- The profile menu refreshes `/me` and displays the authenticated user plus the
  active Company. Profile and Company editors preserve drafts across a revision
  conflict and can load the authoritative row returned by the API.
- The account menu and Company switcher offer **Add company**, opening a compact
  choice between creating a new Company and joining through an invite link.
  `/companies/new` reuses the Company form for signed-in members and preserves
  the active Company until creation succeeds. Back returns to that Company;
  successful creation switches to the new Company and continues its agent setup.
  Its draft and retry slug are separate from any existing onboarding progress.
- My account uses the same hover/focus avatar editor. PNG, JPEG, or WebP photos
  up to 5 MB are previewed locally, then cropped and re-encoded as a thumbnail
  when saving. The User's `avatarUrl` stores that bounded PNG/JPEG data URL
  (at most 256 × 256 pixels and 96 KiB), so it works across Companies and on
  desktop without Company attachment permissions. Cancel discards the draft;
  saving updates the profile and personal menu together.
- Company settings keeps the compact avatar beside the profile heading and
  shows an edit button on hover or keyboard focus. PNG, JPEG, and WebP images
  up to 5 MB can be previewed and upload when saving; Cancel
  discards the selection. The revision-guarded profile stores an attachment
  reference, and active members see it in the Company switcher. The selected
  upload remains retained until replacement or removal.

## Human task conversations

Tasks marked “Me (I do it myself)” offer **Run task** as their primary action.
Their `execution.agent` assists in the same Task chat: the creating Agent, or
CEO for a member-created Task. The human remains the executor. The chat accepts
messages, preserves pending Questions in progress, and moves completed work to
review. **Mark as Done** remains secondary before execution and becomes the
completion action in review; it also supports work finished outside the chat.
The Agent still needs a binding on the assignee's own computer. Manual completion
does not require an online runtime. Proactive Mode does not close human Tasks.

## Work views

Work keeps three complementary views: **Board** for status, **Next up** for the
next action, and **Dependencies** for a read-only canvas across Agents.
Canvas arrows run from prerequisite to dependent Task. A green edge means that
prerequisite is completed; it does not claim the dependent Task can start.

The canvas lays out each Task once, supports pan/zoom, fit controls, a minimap,
and focusing the selected dependency chain. Search and assignee filters retain
related Tasks as context. Opening a Task keeps the current canvas while showing
the shared Task inspector. Missing data and dependency cycles have explicit
states. Node/edge editing is not supported.

## Product tour

The optional tour covers Chat, Projects, Agents, Wiki, Team, Explore, Workflows,
the top-bar tools, and the account menu in nine steps. It starts from visible
navigation, so an active conversation or a collapsed chat sidebar does not block
it. Finishing returns to the original chat without selecting or sending a prompt.
Every company member can replay it from the account menu's Help dialog.

Tour anchors use dedicated `data-tour-id` attributes. Page headings derive their
anchor from `tourId`, while the page root defines the highlighted region. Wiki
includes its visible sidebar in that region. Keep both targets with their visible
UI when changing the layout; tooltips follow anchors as sidebars and responsive
layouts move them. Unavailable targets are skipped after a bounded wait. Back,
End tour, and Escape remain available, and tour navigation replaces history entries.
Existing per-user, per-company completed/skipped preferences are preserved.

## Verification

Run the existing frontend checks and the generated-contract drift check:

```bash
pnpm --dir frontend lint # Import cycles, layer boundaries, and formatting
pnpm --dir frontend typecheck
pnpm --dir frontend build
pnpm --dir frontend electron:security:scan # Electron fuses, CSP, IPC and permissions
make api-types-check
```

CI's frontend lane runs the same checks, plus `make api-lint`.

The repository-wide release gate is `make gate`. The static checks must also
confirm that production source has no direct Zustand import:

```bash
rg 'from ["'\''](zustand|zustand/vanilla)' frontend/src frontend/electron
```

- Create Project automatically derives an internal key from its title, including Vietnamese
  diacritics and Company-wide duplicate suffixes. The form does not display a key field.
  Keys remain immutable after creation.
  Optional logos and documents use the existing attachment upload flow; submission
  waits for every selected upload to complete. Edit Project replaces the file set in
  one revision-checked transaction. Project documents are managed in Edit Project;
  the Files tab lists only those uploaded Project documents, supports search and
  download, and opens Edit Project through Manage files. Canvas artifacts remain
  in Results/Canvas. The former Documents page redirects to Pages.

- Results opens in Canvas by default, with Gallery as an alternative view under
  the same Project tab (`?view=gallery`). Pages remains
  separate for editable documents. Old `/projects/:id/canvas` links redirect to
  `/projects/:id/artifacts?view=canvas`, preserving source/version parameters and
  existing layouts. Canvas is a spatial view, not a separate content store.

- Pages uses a compact document list with titles, creators and last-edited dates.
  Search and sorting stay in the library while a Page is open. New Page reveals
  a small name/template form; blank Pages default to Untitled. Rows open the
  existing collaborative document editor and support opening in a new tab.

Frontend tests are not maintained or permitted (including Electron tests). Validate
frontend changes with lint, typecheck, build, and relevant manual UI checks.

- Artifacts, uploaded attachments (including images), and workspace file links
  in Agent chat share the resizable right-side file panel and fullscreen shell.
  There is no separate upload modal or floating workspace viewer in Agent chat.
  Source adapters retain authenticated attachment reads and desktop-only workspace
  access; saved Artifacts retain sharing, feedback, and multi-file tabs.
  Fullscreen reserves space for macOS window controls; Escape returns to the panel.
  PDF page navigation stays at the bottom while the document scrolls independently.
  Local links open the panel before path resolution and show loading or retry states.
  PDF, PPTX, XLSX, images, video, and audio use the existing authorized Run export route to load complete bytes into
  the desktop preview (up to 100 MiB); temporary snapshots are removed after
  reading. PDFs use PDF.js, XLSX uses the spreadsheet worker (20 MB input limit),
  and CSV/TSV text previews use a grid. CSV detects comma, semicolon, or tab
  separators from complete records, respecting quoted and multiline cells;
  TSV always uses tabs. Grids display at most 500 rows and 50 columns, with
  shorter rows padded to the detected width. PPTX files render locally with
  `@aiden0z/pptx-renderer`, loaded on demand with bounded ZIP parsing, lazy
  slides/media, slide navigation, and automatic fitting to the viewer width.
  Renderer load failures stay inside the preview with a retry action. Desktop
  and web dev servers use separate Vite dependency caches; PPTX is prebundled
  at startup. Legacy PPT files remain download-only;
  animations and some PowerPoint effects may differ. Saved Artifacts, uploaded
  attachments, and draft uploads share the PPTX renderer. Saved Artifact PDFs
  and spreadsheets also share their respective renderers. This
  requires rebuilding the Electron app, not just deploying the web frontend.
