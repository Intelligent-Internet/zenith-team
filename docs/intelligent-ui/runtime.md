# Intelligent UI streaming and recovery

`final_skill.md` is the authoring baseline and its component grammar is the
renderer's grammar: `frontend/src/shared/inline-ui/schema.json` (revision 6)
accepts exactly its prop names, plus two Zenith extensions (`id` on form
controls, `popover onOpenChange`). There are no aliases. The agent-facing
`zenith-intelligent-ui/SKILL.md` teaches only authoring; provider-only sections
(maps, model-data bindings, media search) live in
[provider-integrations.md](provider-integrations.md) and implementation detail in
[renderer-contract.md](renderer-contract.md). Icons are Iconsax Outline names only, with
no translation layer. `make intelligent-ui-check` fails when SKILL.md's prop
tables and the schema drift apart.

The [OpenUI analysis](https://www.openui.com/blog/how-chatgpt-intelligent-ui-works)
is a behavioral reference for native composition, streaming and error isolation.
It is reverse-engineered evidence, not a public OpenAI wire specification.
Zenith uses its existing transcript transport and bounded AST interpreter rather
than evaluating generated JavaScript or adopting ChatGPT's private binary UI
operation protocol. Authored code never receives browser globals, network,
credentials, timers, eval or Function. Server parsing never runs authored code.

| Stage | Zenith behavior |
| --- | --- |
| Catalog validation | Shared schema validates names, props, enums, child structures and hook placement. Preview repair drops unknown/invalid optional props and records diagnostics. Unknown icon names are omitted with `unknown_icon` rather than drawn as a fallback glyph. |
| Prose HTML | A bare component-named HTML tag (`button`, `input`, `select`, `textarea`, `form`, `table`, `label`, `code`, `title`, `svg`, `text`, `blockquote`) in the middle of a sentence with no closing tag is prose about HTML and is emitted as an escaped literal; stray closing tags stay visible. Real UI tags start a line or follow markup, carry attributes or are closed. A mid-sentence partial tag at the stream's end is withheld silently. |
| Streaming syntax | Following the OpenUI model, the source is cut at its last complete construct. Completed siblings and container content render immediately; open containers with content close in the preview. A component appears once its opening tag is complete, so required props are always whole. Unfinished tags, expressions and applications show one accessible preparation state; inside `{#each}`, structured parents and SVG they are withheld instead, so loops never repeat placeholders. Raw blocks withhold a partial closing tag. Complete statements in an unfinished body remain available. |
| Prop recovery | Actual boolean values remain preferred. Empty string, string true/false and numeric 0/1 are normalized for boolean-only props. Invalid disabled/readOnly/loading guards fail closed. Unknown props (including retired aliases) are omitted with `unknown_prop`. No callback, URL, data source or chart observation is invented. |
| Evaluation | Each declaration, expression and component has an independent recovery boundary. A failure retains a previous evaluated node when available; other siblings continue. |
| React rendering | Component boundaries retain the last committed rendering on an exception. A second boundary handles a failed retained rendering. Source or state changes retry the component. |
| State identity | As in the reference runtime, hook state lives in keyed slots that survive every re-parse: `DIL.useState(initial, {key})` names a slot explicitly, otherwise the declared binding name does. Authored repeated keys determine element identity. State remains local to the mounted response. |
| Interaction | Completed controls can change local state during streaming. Host actions, effects and AppBlock scripts wait for completion. Read-only and capture views cannot perform actions. Failed synchronous callbacks roll back hook-state writes. |
| Resource bounds | Each render and effect run has a 1,000,000-step budget plus a 200 ms watchdog; user callbacks have 2,000,000 steps plus a 250 ms watchdog, checked every 256 steps. Budgets are sized from measured programs (a chess legal-move search is 60k–210k steps, ~2–9 ms) and the error names the exhausted limit and suggests `DIL.useMemo`. Call depth, collection size, source length and diagnostic count are bounded. For/of/while loops, break/continue and local assignments are supported. State values are immutable outside their setters. The helper set ([renderer-contract.md](renderer-contract.md)) is pure and deterministic: no randomness or current time; `new` is limited to `Date(value)` and `Array(n)`. Errors name the unsupported function so repair requests are actionable. |
| Application staging | Incomplete AppBlock source never creates an executable iframe. Complete apps remain in opaque-origin, network-denied frames. JSON/local callbacks, inherited visualization theme variables and measured content height cross the existing bridge. |
| Fallback | A nonexecuting static fallback retains prose when the outer renderer fails; expressions and iteration results are not guessed. Fences remain literal. |
| Diagnostics | The response exposes display details and original source. Authorized chat hosts offer an explicit “Ask Agent to repair” action with diagnostics and bounded source. No automatic model call, send, save or permission expansion occurs. |
| Page previews | The same portable parser returns informational streaming diagnostics and warnings using UTF-8 offsets. Final Page compilation/publication remains strict; preview recovery is never evidence that an invalid source was saved. Scenario captures reject unresolved diagnostics. |
| Motion | Reference timings: prose fades in over 0.7s, new rows, list items, grid/flow items and carousel slides slide in over 0.42s, charts draw over 1.8s, each once on insertion (content is memoized so streaming never replays it). Reduced-motion preferences and captures suppress motion; read-only charts render drawn. |
| Accessibility | A `<label>` written directly before a control names it (`htmlFor` + `aria-labelledby`); radio-group and segmented-control labels render visibly; `invalid` shows a danger outline and aria-invalid. `GenUI.copy` announces “Copied” in a live region only after the clipboard write succeeds. |

Recovery is deterministic and bounded. It repairs syntax and presentation
contracts; it cannot establish that a generated game's rules or a calculator's
business logic are correct. Missing external image/search/map/reference
integrations remain deferred: their grammar parses, they render as explicit
unavailable states, and `turnN…` model-data refs report "not connected" rather
than an authoring error. Recovery never synthesizes their results.

Agent prompts inline the skill's marked core (Conversational Authoring, Where
it runs, Quick start; extracted at runtime by `tools.IntelligentUICore`) in
every Run with skill tools, and require loading the full SKILL.md before
writing components; Runs without skill tools receive the full skill. Stored or
external destinations reject component source at the API boundary
(`wiki.ContainsInlineUI`, used by the Wiki, mail draft, calendar and question
handlers; social posts travel through questions).

Verification lives in the server-owned inline runtime/render scripts and Go
source-preview tests. The render script replays every character prefix of every
SKILL.md example (and the chess fixture) in streaming mode and fails on leaked
markup, error states or placeholder floods. The chess retry fixture exercises nested loops, legal
selection, moves, reset and checkmate. Browser checks cover staging, local state
across source updates, sibling isolation, committed-component recovery, native
chess interaction, dark/narrow layouts and the AppBlock state bridge.
