# Zenith renderer contract

Authoring baseline: repository-root `final_skill.md`, whose component grammar the
renderer implements exactly. Inline languageVersion is 1; the schema revision is
6. The browser and server share one parser and one schema
(`frontend/src/shared/inline-ui/schema.json`), bundled into the server parser by
`scripts/generate-inline-parser.mjs`. Every prop name is the final guide's; there are
no aliases. Zenith adds only two extensions: `id` on form controls (a target for
`label htmlFor`) and `popover onOpenChange`.

## Available surfaces

| Surface | Authoring / rendering | Actions and persistence |
| --- | --- | --- |
| Chat and Task chat | Load zenith-intelligent-ui; emit live inline source | Local state; continuation uses the authorized chat host |
| Page `/generate` | Guide injected; compiled source staged as a proposal | Human acceptance required; code-block targets stay literal |
| Page tagging | Guide injected where skill tools are restricted; use source + languageVersion 1 | Existing Page revision, permission and proposal rules |
| Agent Page comments | Same inline renderer and artifact resolver | No inherited chat-send capability |
| Comparison, read-only and scenario capture | Native source rendered with interaction disabled; AppBlock static HTML fallback | No scripts or callbacks; captures do not retain AppBlock internal state |

## Implemented components

- Layout: box, row, col, card, grid, grid-item, flow, flow-item, divider, spacer,
  blockquote, list, list-item, table, table-section, table-row, table-cell.
  Layout containers space children by 8pt unless `gap` or `disableAutoSpacing`
  is set. `card` uses `variant`/`background`, never `border`/`radius`.
- Text: text, title, caption, label, bold, italic, underline, strikethrough, code,
  badge, math. KaTeX renders math; String.raw supports literal LaTeX.
- Inputs: button, pressable, form, input, textarea, checkbox, date-picker, slider,
  radio-group/radio, segmented-control, select. Change callbacks receive values.
  A `<label>` immediately followed by a control names that control
  (`htmlFor`/`aria-labelledby` are wired automatically).
- Containers: carousel/items and popover/trigger/content. Carousel activeIndex
  controls scroll position and scrolling reports the nearest slide. Popover
  `open` changes visibility; showOnHover/hoverOpenDelay open on hover; Escape,
  outside interaction and hover leave dismiss. `onOpenChange(open)` keeps
  authored state in sync with that dismissal.
- Visuals: icon, constrained svg, favicon, AsyncImage `src`, Chart, CodeBlock,
  WritingBlock, AppBlock, escape. Icon names come from
  [icons.generated.md](../../server/internal/mcp/tools/builtin/zenith-intelligent-ui/references/icons.generated.md): Lucide names map to equivalent
  Iconsax glyphs; an unknown name is omitted with an `unknown_icon` diagnostic.
- Host-backed: Entity for Zenith tasks and Pages (`category="task"|"page"`,
  `ref` = the item's ID, `value` = display text), Link.url, FollowUp, and
  artifact-reference Cite/FileCite/CodeCite/FileNavList/Link.ref in authorized
  chat/Page hosts.

AppBlock supports raw HTML, CSS, JavaScript, DOM, canvas and animation in its own
opaque-origin, network-denied sandbox. Prefer native components when
sufficient, hybrid composition for a custom visualization, or a self-contained
AppBlock for a cohesive application requiring browser behavior. Supply the
complete inline HTML/CSS/JS body; named custom components and React/JSX are not
registered by authoring them. Optional `bind` and optional-chained
`__bindings`/`__effect` connect outer native state. It cannot access the product
DOM or invoke product actions; external libraries, remote resources and
persisted state are unavailable. `variant="inline"` is the only variant.

## Exact native value rules

- Spacing: numeric 0–24 × 4 pt, or bounded px/pt/rem/em/% strings, or "0".
  Dimensions accept finite numbers clamped to 0–2000 and bounded
  px/pt/rem/em/%/vh/vw strings, auto or fit-content. aspectRatio accepts
  `16/9`, `16:9` or a number.
- Sizes: xs/sm/md/lg/xl/2xl/3xl. Weights: normal/medium/semibold/bold or 100–900.
- Colors: default/primary/secondary/subtle/neutral/info/success/danger/warning/
  accent. Backgrounds additionally support blue/purple/yellow/green/red,
  surface, surface-secondary, transparent and background.
- Variants: default/solid/soft/subtle/outline/ghost. Buttons combine `color`
  and `variant` (default solid primary); badges default to soft neutral; a
  semantic color tints the whole control, not only its text.
- Justify: start/end/center/between/around/evenly.
  Align: start/end/center/stretch/baseline. Direction: horizontal/vertical.
- useBreakpoint: sm <640, md <1024, lg otherwise, based on the containing view.
- Math: min/max/round/floor/ceil/trunc/sign/abs/sqrt/cbrt/pow/hypot/exp/log/
  log2/log10, sin/cos/tan/asin/acos/atan/atan2 (radians), PI and E.
  No Math.random, Date.now or browser timers: renders stay deterministic.
- Arrays: map/filter/find/findIndex/findLast/findLastIndex/some/every/forEach/
  reduce/flat/flatMap/concat/slice/join/includes/indexOf/lastIndexOf/at/sort/
  reverse/fill/push/pop/shift/unshift. sort and reverse return copies; fill and
  push-family methods only change local, non-state arrays. Array(n),
  Array.from(arrayLike, map?), Array.isArray and Array.of are bounded to 1000.
- Strings: slice/substring/split/trim/trimStart/trimEnd/toUpperCase/toLowerCase/
  includes/startsWith/endsWith/indexOf/lastIndexOf/charAt/charCodeAt/codePointAt/
  at/padStart/padEnd/repeat/replace/replaceAll/localeCompare/concat. replace and
  replaceAll take text patterns and text replacements only. String.fromCharCode.
- Numbers: toFixed, toPrecision, toString(radix), toLocaleString(locale, options);
  Number.isFinite/isInteger/isNaN/parseInt/parseFloat and global parseInt/
  parseFloat/isNaN/isFinite.
- Dates: `new Date(value)` with an explicit timestamp, ISO string or components;
  Date.UTC and Date.parse. Getters (UTC and local), toISOString and
  toLocaleDateString/TimeString/String(locale, options). Local getters and
  locale formatting follow the viewer's timezone; pass `timeZone` for stable
  text. `new Date()` without a value is rejected.
- JSON.parse/stringify, Object.keys/values/entries/fromEntries/assign (assign
  returns a new object), String/Number/Boolean work. Spread works in array and
  object literals and call arguments. Other constructors (Set, Map, Intl) and
  regular expressions are unavailable. Errors name the unsupported function.
- Limits: 256 KiB source, depth 32, collections 1000, strings 65536, bounded
  evaluation work. Chart supports up to 1000 rows and 12 numeric series.
- SVG supports safe geometry, gradients, local clip paths/masks and local IDs
  isolated per illustration. SVG events, scripts, foreignObject and external
  resource URLs are not supported. Put controls outside SVG.
- Chart: exactly the guide's object — chartType bar/line/scatter/pie,
  xKey/nameKey/valueKey, series {dataKey, label, valuePrefix, valueSuffix,
  valueFormat, lowerKey/upperKey or lower/upper on lines}, data, xAxisLabel,
  axisLabel, valueSuffix, yAxisMin/yAxisMax, xAxisScale="linear" and
  layout="vertical" bars. The renderer chooses colors. Series labels name the
  figure; the readable data table preserves all series and bands.
- CodeBlock uses raw children; editable enables a local editor, Mermaid previews.
  HTML previews use the AppBlock sandbox; there is no general language runner.
- URLs: images require HTTPS; links permit HTTPS, HTTP and mailto; no credentials
  in URLs. Ref identifiers are authorized Zenith artifact UUIDs, optionally
  prefixed artifact: and suffixed #ordinal. Never invent an identifier.

## Native options and validation

- The schema pins accepted property names, types and enum values. Literal
  properties are checked during parsing (`invalid_literal`); evaluated
  properties before rendering (`invalid_value`). Unknown properties are
  omitted with `unknown_prop`. Invalid values never select a silent fallback.
- Structured parents reject raw text/expressions, including SVG placed directly
  in a grid. Branches, loops and fragments preserve the required children.
  Text components accept inline children; leaf controls reject children.
- Module blocks contain pure declarations. Ambient state/host references and
  mutation are rejected; passing a setter or host action through a helper cannot
  cause side effects.
- Checkbox checked/defaultChecked control its state; value is the form value.
  Radio-group takes `<radio>` children only; name/required propagate to radios
  and its `label` renders as the group legend. Empty numeric inputs emit the
  empty string, not NaN. `invalid` shows a danger outline and sets aria-invalid.
- Select and segmented-control options are `{label, value, disabled?}` with
  scalar values. A cleared select has an empty value and fails required
  validation. Select/date-picker support side and align, optional icons or clear
  controls, and block/pill/size/variant presentation. Date values are ISO
  YYYY-MM-DD; min/max constrain both direct entry and the calendar.
- Input mask uses `#` for digits, `A` for letters and `*` for alphanumeric slots;
  other characters are literals. onChange receives the formatted string. Masks
  apply to text inputs only.
- Slider sliderStyle is default or filled. Marks are `{value,label?}` objects
  placed at their values. Checkbox shape is square or round. Button `uniform`
  is a square hit area; `loading` shows a spinner beside the label and disables.
- Table columnSizing is auto, fixed, an array of widths, or a map from zero-based
  column indices to widths. dividers is none/rows/columns/all. stickyHeader keeps
  header cells visible in the bounded scroll area. Row/section header values
  propagate to cells; a string section header adds a section label. rowDivider
  overrides row dividers inside a section. Empty branches/loops show emptyLabel.
- Carousel activeIndex/onChange count actual items, including loop-generated
  items. visibleItems accounts for gaps; flush removes container padding and
  item gaps. carousel-media-item.media is an HTTPS src string or AsyncImage props.
- Divider `size` sets thickness, `color` its tone, `spacing` its margin and
  `flush` extends it to the container edge. list-item `label` renders as the
  item heading above its children; `description` follows.
- Flow layout=masonry uses CSS columns in document order. Grid/flow columns sets
  the column count; grid minChildWidth enables responsive auto-fit.
  aspectMode=fit/fill controls image fitting. List connector draws a continuous
  item connector; marker and markerSize/maxMarkerSize control item markers.
- Theme light/dark reuses Zenith's palette in that subtree; inherit keeps the
  host palette.
- WritingBlock variants are standard, email, chat_message, social_post and
  document. `options` is an array of `{label, content}` drafts whose first
  content equals the raw body. Drafts are locally editable and copyable; no
  variant sends messages.

## Deferred integrations

| Guide feature | Status and authoring fallback |
| --- | --- |
| AsyncImage query/ref, AsyncImageGroup, AsyncVideo | Grammar accepted; shown as not connected. Use an already resolved HTTPS image in `src`; ordinary grounded links for other media. |
| MapWidgetV2, routing, place/business/product lookup | Grammar accepted; shown as not connected. Use sourced addresses and links. |
| Model-data bindings (`turnN…` refs) | Report "not connected in Zenith yet" instead of evaluating. |
| MemoryCite | Shown as not connected. Cite Zenith artifacts or grounded links. |
| Citation line ranges, FileNavList rich metadata | Artifact-level citations work; line highlighting is deferred. |
| Entity categories other than task/page | Shown as not connected. Use grounded text/links. |
| AppBlock entrypoint/bundle_version | No external bundle loader; supply the inline body. |

Their full final-guide contracts are preserved in
[provider-integrations.md](provider-integrations.md). The renderer keeps their
grammar so they work unchanged once connected; until then they are explicit
unavailable states, never fabricated results.

## Hook signatures and local state

Hooks are `DIL.*` calls made unconditionally at `{@body}` top level. They are
not available inside AppBlock JavaScript.

| Hook | Contract |
| --- | --- |
| useState(initialValue, {key}?) | Returns [value, setter]. State lives in keyed slots: an explicit `key` (short identifier) wins, then the declared binding name. Initializer callbacks and functional setters work. Values must be bounded JSON; setters run in callbacks or effects only. |
| useMemo(factory, dependencies?) | Returns the pure factory result; an array caches until dependencies change. |
| useCallback(callback, dependencies?) | Returns the callback; an array retains it until dependencies change. |
| useRef(initialValue, {key}?) | Returns a stable local object with current. It is not a DOM reference. |
| useId() | Returns a stable string for label htmlFor and control id. |
| useTheme() | Returns light or dark from the host. |
| useBreakpoint() | Returns sm/md/lg from the containing view's width at 640/1024 px thresholds. |
| useEffect(callback, dependencies?) | Runs after interactive render, with optional cleanup; no host actions, timers or network APIs. Read-only/streaming views do not run effects. |

FollowUp shows `value` and submits `query ?? value` through the host's normal
send path only after a click; without a chat send host it shows an unavailable
state. `GenUI.copy` reports "Copied" only after the clipboard write succeeds.
`GenUI.openUrl` opens HTTPS/HTTP/mailto links on deliberate interaction.
`GenUI.issueNewTurn` requires a chat host. `GenUI.openEntityDetail({category,
refId})` opens Zenith tasks and Pages. Errors are surfaced, never converted to
successful saves or sends.

## Streaming, motion and repair

[runtime.md](runtime.md) records the implementation mapping. Every
update re-parses the whole source and cuts it at the last complete construct:
completed siblings render; open elements with content close in the preview; a
component appears once its opening tag is complete, so its props are always
whole. An unfinished tag shows one preparation placeholder, except inside
`{#each}` bodies, structured parents and SVG, where it is withheld. Raw blocks
withhold a partially received closing tag. Parse recovery records
`unterminated_tag`, `unclosed_block` and `unterminated_braced_value`, which clear
when the source completes.

Hook state survives re-parses through keyed slots. Completed local controls work
during streaming; effects, host actions and application scripts wait for
completion. Each expression and component fails locally, keeping its last
working rendering when possible. Display details expose diagnostics and source;
chat hosts offer an explicit Agent repair request and never send automatically.
User callbacks allow 250,000 steps and a 100 ms deadline; render/effects 50,000.

Motion follows the reference renderer: prose fades in over 0.7s, new rows, list
items, grid/flow items and carousel slides slide in over 0.42s, and charts draw
over 1.8s, each once on insertion. Reduced-motion preferences and captures get
no motion; read-only charts render without drawing.
