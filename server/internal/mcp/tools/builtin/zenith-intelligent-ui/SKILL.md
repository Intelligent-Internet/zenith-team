---
name: zenith-intelligent-ui
description: Author inline interactive responses and Zenith Team Pages with component markup, bounded state and callbacks.
---

# Zenith Intelligent UI

Write recognized components directly in an assistant response. No outer wrapper,
Markdown fence, presentation tool or saved Page is required. A fenced code block
always displays code. Keep explanatory prose outside the UI only when it helps.

```text
{@body const [quantity, setQuantity] = DIL.useState(2);}
<box border radius="xl" padding={4} gap={3}>
  <title size="lg">Estimate</title>
  <input type="number" value={quantity} onChange={setQuantity} />
  <text>{quantity * 25} credits</text>
  <button onClick={() => setQuantity(quantity + 1)}>Add one</button>
</box>
```

Tags can be adjacent, nested or spread across lines. Use quoted strings, boolean
flags and braced expressions. Child text is literal text; use typography tags for
formatting inside components. Markdown outside components remains Markdown.
Escaped tags and code spans are literal examples.

The retired Scope/Stack/Metric and bind/action authoring format is not the inline
language. Do not emit a `zenith-ui` fence or wrap a response in a protocol marker.

## State and expressions

Declare local values in `{@body ...}` and pure helpers in `{@module ...}`.
Use `DIL.useState`, `useMemo`, `useCallback`, `useRef`, `useId`, `useTheme`,
`useBreakpoint` and `useEffect`. State is private to this mounted view and is not
saved to the transcript or Page. Effects may update local state; they cannot
invoke host actions. Dependencies are arrays. Hooks belong at render time.

Use `{expression}`, `{#if condition}...{:else if otherCondition}...{:else}...{/if}`,
`{#each items as item, index}...{/each}` and fragments `<>...</>`.
Expressions support bounded arithmetic, comparisons, conditionals, object/array
literals, field access, template strings, functions, arrow callbacks and supported
array/string methods. `Math.min/max/round/floor/ceil/abs/sqrt/pow`, `JSON`,
`Object.keys/values/entries` and scalar conversions are available.
There are no imports, browser globals, DOM access, timers, networking, storage,
loops, eval or arbitrary JavaScript execution. Unsupported constructs produce a
source disclosure and diagnostic. Keep computations small.

## Components

- Layout: box, card, row, col, grid, grid-item, flow, flow-item, spacer, divider,
  list, list-item, table, table-section, table-row, table-cell.
- Typography: text, title, caption, label, bold, italic, underline,
  strikethrough, blockquote, code, badge, math.
- Controls: input, textarea, checkbox, date-picker, slider, select,
  segmented-control, radio-group, radio, button, pressable, form.
- Containers: carousel, carousel-item, carousel-media-item, popover,
  popover-trigger, popover-content.
- Content: icon, svg, favicon, AsyncImage, Chart, Link, CodeBlock, WritingBlock,
  FollowUp. `escape` displays its children literally.

Layouts accept bounded spacing units (`padding={4}` means 16 px), `gap`,
`rowGap`, `columnGap`, `width`, `height`, min/max dimensions, `flex`, `align`,
`justify`, `wrap`, `radius`, `border` and semantic backgrounds/colors.
Backgrounds accept `background` (alias of `surface`), `surface`,
`surface-secondary`, `transparent`, `blue`, `purple`, `yellow`, `green`, `red`,
`info`, `success`, `warning`, `danger`, and `accent`. Named backgrounds are
theme-aware tinted surfaces; they do not recolor child buttons or badges.
Typography
uses xs/sm/md/lg/xl/2xl/3xl sizes and named weights. Use Iconsax names; familiar
aliases such as plus, minus and sliders-horizontal map to Iconsax.

Inputs pass their new value directly to `onChange`; checkbox passes a boolean,
slider and numeric inputs pass a number. Buttons accept children and `onClick`;
form accepts `onSubmit`, and a button with `submit` submits that form.
Select and segmented-control accept `{label, value}` options.

```text
<Chart content={{chartType:"line", xKey:"day", series:[{dataKey:"ms",label:"Latency",valueSuffix:" ms"}], data:[{day:"Mon",ms:320},{day:"Tue",ms:270}]}} />
```

Charts support bar, line, area, scatter and pie, with a readable data table.
Use real HTTPS image URLs for AsyncImage; a search query is not an image source.
CodeBlock and WritingBlock contain raw text. SVG uses a constrained element and
attribute set and cannot embed scripts, foreign HTML or external resources.

## Host capabilities and evidence

`GenUI.copy(text)`, `GenUI.openUrl(url)` and `GenUI.issueNewTurn(text)` require an
explicit user interaction. issueNewTurn uses the current chat's normal send path;
it is unavailable outside an authorized chat host. Never call host actions on
mount. A component grants no permission or access to a resource.

Use real Zenith references and sources where the view supplies them. Do not
manufacture grounded citation IDs, file references, memory entries, map search
results or ecosystem widgets. Cite, CodeCite, Entity, FileCite, FileNavList,
MemoryCite, AsyncImageGroup, AsyncVideo, MapWidgetV2 and AppBlock show explicit
unavailable states when their required host capability is not connected.
Entity with category="task" or category="page" and a real UUID value opens the
corresponding Zenith item after an authorized API read. openEntityDetail accepts
that same {category, value} object. Other categories and launchEcosystemWidget
report unavailable until the host supplies those capabilities. Do not promise a successful operation from a
visual label alone.

## Streaming and limits

The token-aware parser displays completed children in open components. An
unfinished attribute or expression is withheld; it is never executed as repaired
JavaScript. Controls remain disabled until the complete message arrives.
Malformed or incomplete final output offers Show source. Source is limited to
256 KiB, nesting to 32, arrays to 1000 items and evaluation to a bounded work
budget. Keep each response cohesive and avoid splitting a state declaration and
its consumers across independent transcript text segments.

## Pages

The same inline grammar is accepted by `page_create` and source operations with
`languageVersion: 1`. Supply exactly one of source, content or markdown.
Interactive source is stored in a native `uiDocument` block; its source is the
canonical definition, not a second editable authority. Plain Markdown continues
to produce normal editable Page blocks.

Read before editing. Use real Project, Page, Task, Workflow and Asset identifiers
returned by the authorized host. Read-only, comparison and snapshot views disable
callbacks. Local state is temporary; saved definitions retain authored defaults.
Do not describe a saved Page as a capture of a viewer's temporary form selections.
Scenario capture is unavailable for Pages containing inline components, including
included sources, until captures can preserve those inputs. Keep the saved Page
as the source definition.

Use Edit block source to change an inline document. The server retains the selected
block's identity and checks the existing Page revision, permission and proposal
rules. Native envelopes returned for ordinary Page nodes retain their explicit
IDs; do not invent or borrow IDs. Agent structural edits still use explicit
insert, remove or move operations. Never claim an edit or capture was saved until
its save response succeeds.
