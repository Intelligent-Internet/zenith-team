# Zenith renderer contract and guide gaps

Authoring baseline: repository-root `final_skill.md`. Inline languageVersion remains 1;
the prop/structure schema revision is 5. The browser and server share the same
parser and schema. Native properties have shared name, type and enum validation. Provider-dependent
properties remain recognized but report unavailable without an authorized resolver.
The exact recognized prop names and child structures are in
[schema.generated.json](schema.generated.json), generated from the renderer's
schema by the same command that bundles the server parser.

## Available surfaces

| Surface | Authoring / rendering | Actions and persistence |
| --- | --- | --- |
| Chat and Task chat | Load zenith-intelligent-ui; emit live inline source | Local state; continuation uses the authorized chat host |
| Page `/generate` | Guide injected; compiled source staged as a proposal | Human acceptance required; code-block targets stay literal |
| Page tagging | Guide injected where skill tools are restricted; use source + languageVersion 1 | Existing Page revision, permission and proposal rules |
| Agent Page comments | Same inline renderer and artifact resolver | No inherited chat-send capability |
| Comparison, read-only and scenario capture | Native source rendered with interaction disabled; AppBlock static HTML fallback | No scripts or callbacks; captures do not retain AppBlock internal state |

All eight documented DIL hooks exist. Hook calls must be direct top-level body
declarations or statements. The validator rejects nested/conditional hooks and
module hooks. Programs execute in the bounded interpreter, not JavaScript eval.
Supported pure helpers may be declared in module/body; hooks are not helper APIs.

## Implemented components

- Layout: box, row, col, card, grid, grid-item, flow, flow-item, divider, spacer,
  blockquote, list, list-item, table, table-section, table-row, table-cell.
- Text: text, title, caption, label, bold, italic, underline, strikethrough, code,
  badge, math. KaTeX renders math; String.raw supports literal LaTeX.
- Inputs: button, pressable, form, input, textarea, checkbox, date-picker, slider,
  radio-group/radio, segmented-control, select. Change callbacks receive values.
- Containers: carousel/items and popover/trigger/content. Carousel activeIndex
  controls scroll position and scrolling reports the nearest slide. Popover open
  changes visibility; showOnHover and hoverOpenDelay open an anchored popover, which dismisses on hover leave, Escape or outside interaction. For fully controlled visibility, pair open with the Zenith onOpenChange extension.
- Visuals: icon (Iconsax), constrained svg, favicon, AsyncImage with resolved HTTPS
  sources, Chart, CodeBlock, WritingBlock, AppBlock, escape.
- Host-backed: Entity for task/page, Link.url, FollowUp, and artifact-reference
  Cite/FileCite/CodeCite/FileNavList/Link.ref in authorized chat/Page hosts.

AppBlock supports raw HTML, CSS, JavaScript, DOM, canvas and animation in its own
sandbox. Following the developer guide's sections 13–14 and 20, prefer native
components when sufficient, hybrid composition for a custom visualization, or a
self-contained AppBlock for a cohesive application requiring browser behavior.
Supply its complete inline HTML/CSS/JS implementation; named custom components
and React/JSX are not registered by authoring them. HTML controls and local state
may live entirely inside the block. Optional bind and optional-chained
__bindings/__effect connect outer native state when needed.
It cannot access the product DOM or invoke product actions. External libraries,
remote subresources/connections and persisted application state are unavailable.
AppBlock needs its own reduced-motion-aware animation code; the renderer also
reduces CSS motion. Read-only frames are inert and disable scripts/CSS animation.

## Exact native value rules

- Spacing: numeric 0–24 × 4 pt, or bounded px/pt/rem/em/% strings. Dimensions accept
  finite numbers clamped to 0–2000 and bounded px/pt/rem/em/%/vh/vw strings, auto or
  fit-content. One numeric spacing unit is four CSS points. Explicit CSS lengths retain their units, including pt.
- Sizes: xs/sm/md/lg/xl/2xl/3xl. Weights: normal/medium/semibold/bold.
- Colors: default/secondary/subtle/neutral/info/success/danger/warning/accent.
  Backgrounds additionally support blue/purple/yellow/green/red, surface,
  surface-secondary, transparent and background.
- Justify: start/end/center/between/around/evenly and space-* equivalents.
  Align: start/end/center/stretch/baseline. Direction: horizontal/vertical,
  with row accepted as a horizontal alias.
- useBreakpoint: sm <640, md <1024, lg otherwise, based on the containing view.
- Math: min/max/round/floor/ceil/abs/sqrt/pow/sin/cos; radians for trigonometry.
  No Math.random or browser timers in native expressions.
- Arrays: map/filter/find/some/every/reduce/slice/join/includes/indexOf/sort;
  sort returns a copy. Strings: toUpperCase/toLowerCase/trim/includes/startsWith/
  endsWith/slice/split. Number.toFixed and toLocaleString(locale, options) work.
  JSON.parse/stringify, Object.keys/values/entries, String/Number/Boolean work.
- Limits: 256 KiB source, depth 32, collections 1000, strings 65536, bounded
  evaluation work. Chart supports up to 1000 rows and 12 numeric series.
- SVG supports safe geometry, gradients, local clip paths/masks and local IDs
  isolated per illustration. SVG events, scripts, foreignObject and external
  resource URLs are not supported. Put controls outside SVG.
- Chart: bar/line/area/scatter/pie, xKey/nameKey/valueKey, title, numeric series,
  labels/colors, valuePrefix/valueSuffix, compact formatting, xAxisLabel,
  axisLabel, yAxisMin/yAxisMax, xAxisScale="linear" and vertical bar layout.
  Pie uses the first series. The accessible data table preserves all series.
- CodeBlock uses raw children; editable enables a local editor, Mermaid previews.
  WritingBlock edits/copies a local draft. Neither sends email. HTML previews use the same isolated AppBlock sandbox; there is no general-purpose language runner.
- URLs: images require HTTPS; links permit HTTPS, HTTP and mailto; no credentials
  in URLs. Ref identifiers are authorized Zenith artifact UUIDs, optionally
  prefixed artifact: and suffixed #ordinal. Never invent an identifier.

## Native options and validation

- The generated schema pins accepted property types and enum values. Literal
  properties are checked during parsing; evaluated properties are checked before
  rendering. Invalid values produce a source diagnostic rather than silently
  selecting a fallback. SVG attributes have a separate safe allowlist.
- Structured parents reject raw text/expressions, including SVG placed directly
  in a grid. Branches, loops and fragments preserve the required children.
  Text components accept inline children; leaf controls reject children.
- Module blocks contain pure declarations. Ambient state/host references and
  mutation are rejected. Module functions retain a runtime purity boundary:
  passing a setter or host callback through a helper cannot cause side effects.
- Checkbox checked/defaultChecked control its state; value is the form value.
  Radio-group name/required propagate to radios. Empty numeric inputs emit the
  empty string, not NaN. Every input accepts the Zenith id extension for labels.
- Select options are `{label, value, disabled?}` with scalar values. A cleared
  select has an empty form value and fails required validation. Select/date-picker
  support side=top/right/bottom/left and align=start/center/end, optional icons or
  clear controls, and block/pill/size/variant presentation. Date values are ISO
  YYYY-MM-DD; min/max constrain both direct entry and the calendar.
- Input `type` and `inputType` are equivalent; Link `href` and `url` are equivalent. Conflicting aliases are rejected. Radio-group accepts options with the same scalar values and disabled flags as select, or explicit radio children. Use one form at a time.
- Input mask uses `#` for digits, `A` for letters and `*` for alphanumeric slots;
  other characters are literals. onChange receives the formatted string. Masks
  apply to text inputs, not numeric/date values.
- Slider sliderStyle is default or filled. Marks are `{value,label?}` objects.
  Checkbox shape is square or round. Button uniform uses a square hit area.
- Table columnSizing is auto, fixed, an array of widths, or a map from zero-based
  column indices to widths. dividers is none/rows/columns/all. stickyHeader keeps
  header cells visible in the bounded scroll area. Row/section header values
  propagate to cells; a string section header adds a section label. rowDivider
  overrides row dividers inside a section. Empty branches/loops show emptyLabel.
- Carousel activeIndex/onChange count actual items, including loop-generated
  items. visibleItems accounts for gaps; flush removes container padding and
  item gaps. carousel-media-item.media is an HTTPS src string or AsyncImage props.
- Popover triggers may contain a native button; both the child and trigger
  callbacks run. side/align/sideOffset position content. onOpenChange receives a
  boolean and may update local state, but cannot invoke host actions. Without
  onOpenChange, changes to open synchronize visibility while local dismissal
  remains available. With onOpenChange + open, the author owns visibility.
- Flow layout=masonry uses CSS columns in document order; span greater than one
  spans all columns in that mode. Grid/flow columns sets the column count;
  grid minChildWidth enables responsive auto-fit. aspectMode=fit/fill controls
  image fitting. List connector draws a continuous item connector; marker and
  markerSize/maxMarkerSize control authored item markers.
- Theme light/dark reuses Zenith's existing palette in that subtree; inherit
  keeps the host palette. Variants default/solid/soft/subtle/outline/ghost use
  Zenith surfaces; disableAutoSpacing removes default layout gaps. Icon inline
  and favicon inline/frame control placement and framing.
- CodeBlock editable enables local editing. interactive=false disables editing;
  interactive=true permits interaction in an HTML preview sandbox.
  model_previewable=false hides previews; Mermaid and HTML are previewable.
  Other languages display source without claiming execution.
- WritingBlock displays recipient/cc/bcc/subject metadata and edits/copies a
  local draft. options supports rows, readOnly and showCopyButton. variant=email
  uses the same local email draft surface; no sending is implied.

## Explicitly deferred differences from the v2 guide

| Guide feature | Current difference / authoring fallback |
| --- | --- |
| AsyncImage query/ref, AsyncImageGroup, AsyncVideo | No media search/reference provider yet. Use an already resolved HTTPS asset image with AsyncImage; use ordinary grounded links for other media. |
| MapWidgetV2, routing, place/business/product lookup | No map or external entity provider. Use sourced addresses and links; do not invent routes, travel times, coordinates or prices. |
| MemoryCite and external turnN reference identifiers | No compatible resolver. Cite actual Zenith artifact references or grounded ordinary links. |
| Citation line ranges and FileNavList rich item metadata | Artifact-level/item-level citations work; exact source-line highlighting and arbitrary metadata remain deferred. |
| Entity categories other than task/page | No registered Zenith entity adapter. Use grounded text/links. |
| AppBlock entrypoint/bundle_version/language variants | Inline HTML body or `html` string; no external bundle loader, npm imports or WebGL feature guarantee. |

These deferrals are intentional, not hidden success states. Prefer supported
alternatives; explain a missing capability when it is essential to the request.
Do not label a deferred property as working merely because the parser accepts it.

## Hook signatures and local state

All hooks are available as `DIL.useState`, `DIL.useMemo`, etc. in the native environment. Bare hook calls are not supported. Call them unconditionally
at body top level. They are not available inside AppBlock JavaScript.

| Hook | Contract |
| --- | --- |
| useState(initialValue) | Returns [value, setter]. Initializer callbacks and functional setters are supported. Values must be bounded JSON; setters run in local callbacks or effects, never during render or in module helpers. |
| useMemo(factory, dependencies?) | Returns the pure factory result; an array caches until dependencies change. Omitted dependencies recompute on render. |
| useCallback(callback, dependencies?) | Returns the callback; an array retains it until dependencies change. |
| useRef(initialValue) | Returns a stable local object with current. It is not a DOM reference. |
| useId() | Returns a stable string for label htmlFor and control id. |
| useTheme() | Returns light or dark from the host. |
| useBreakpoint() | Returns sm/md/lg from the containing view's width at 640/1024 px thresholds. |
| useEffect(callback, dependencies?) | Runs after interactive render, with optional local cleanup callback; omitted dependencies run each render. No host actions, timers or network APIs. Read-only/streaming views do not run effects. |

FollowUp `text` supplies the displayed message and submitted text; `query` may
override the submitted text, and `value` remains a supported display/message
fallback. It uses the host's normal send path only after a click. Without a chat
send host it shows an unavailable state. `GenUI.copy` and `GenUI.openUrl` use the
browser only on deliberate interaction. `GenUI.issueNewTurn` requires a chat
host; entity actions require an authorized entity resolver. Errors are surfaced
by the renderer, never converted to successful saves or sends.

## Final guide vocabulary

New authoring uses inputType, Link.url, nested radio children and four-point
numeric spacing. Earlier type/href/options aliases remain supported for saved
content. Spacing accepts the unitless string "0". Attributes accept unquoted
boolean, numeric and null literals; quote strings containing whitespace.
Prose children parse Markdown together with inline components and expressions;
code/raw blocks and SVG text remain literal. Math delimiters protect LaTeX braces.
Required grid, flow, table, radio, carousel and popover items also validate their
parent, including through directives and fragments.

WritingBlock variants are standard, email, chat_message, social_post and document
(default is a retained alias). options may be an array of {label, content} draft
versions; the first content must equal the primary raw draft. Each draft is locally
editable and copyable, switching retains edits, and no variant sends messages.
The existing {rows, readOnly, showCopyButton} options object remains supported.
Line chart series accept lowerKey/upperKey for row-bound bands, or numeric
lower/upper constant bounds. Both bounds are required and lower must not exceed
upper. Bands are shaded and included in the readable data table. Linear X axes
require numeric line-chart X values. Renderer colors remain theme-aware.
