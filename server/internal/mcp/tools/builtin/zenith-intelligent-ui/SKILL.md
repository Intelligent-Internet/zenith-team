---
name: zenith-intelligent-ui
description: Author clear, beautiful, interactive inline responses using the supported component grammar, state, visualizations, grounded references, and host actions.
---

# Zenith Intelligent UI

Use this guide as the sole authoring reference for interactive UI in assistant responses. Emit component markup directly in the response, alongside Markdown when helpful. Do not wrap live components in a code fence, HTML document, JSON object, or custom protocol. A Markdown code fence displays literal source, not a live interface.

The goal is an accurate and useful answer first. Use UI only where layout, interaction, visualization, or navigation materially improves the task. Prefer restrained visual hierarchy over decorative density. Only use the component names, properties, structures, bindings, and actions described below. Do not imply the host has capabilities or data that it has not supplied.


## Zenith host contract

This is the main authoring guide, adapted from `final_skill.md`. The renderer
implements its grammar exactly: every component and prop name below is the only
accepted spelling, and anything else is dropped with a diagnostic. Zenith adds
two extensions, noted where they apply: `id` on form controls and
`popover onOpenChange`. Numeric spacing uses four CSS points.

Zenith implements all native components, Markdown inside prose components,
WritingBlock drafts/variants, chart bands and sandboxed AppBlock HTML/JS. Exact
token values are in Section 11. Pages use this grammar with languageVersion 1 and
existing revision/permission/proposal rules. Local state is temporary; native
scenario captures are read-only and AppBlock captures use its static fallback.

The renderer stages unfinished components and isolates display errors. It may
normalize unambiguous boolean mistakes or omit invalid optional props, but author
valid source: boolean props must evaluate to booleans. Named state bindings survive
streaming updates; completed local controls remain usable while host actions wait
for completion. Display details explain repairs and can request an Agent correction.
Recovery does not validate business logic or invent missing data.

Not connected in Zenith: model-data WebRef/ProductRef/BusinessRef bindings,
image/video search and groups, MapWidgetV2, MemoryCite, external entity
categories and external citation line resolvers. **Do not author them**: the
renderer can only show them as unavailable. Use grounded links, addresses in
prose and tables instead.
Zenith task/Page entities and authorized artifact references work through the
viewer’s existing access. Resolved authorized HTTPS asset URLs work in
`AsyncImage src`. External AppBlock bundles are not loaded; supply the complete
inline HTML/JS.

## 1. Syntax, text, and evaluation

- Markdown and component markup share one parsing context, including Markdown inside components with prose children. Supported Markdown: bold, italic, strikethrough, tables, ordered/unordered/checklists, inline and fenced code, math `\(...\)` and `\[...\]`, dividers. `<underline>` is a component.
- Components use paired tags or self-closing tags, quoted string props, typed boolean/number/null props, and JavaScript expressions in `{...}`. Strings with whitespace must be quoted. Close all components and directives.
- Use actual newline characters for line breaks, never HTML `<br>` elements.
- Use `<escape>...</escape>` around ambiguous literal markup or symbols that could be parsed as components/expressions; escape only the ambiguous span. Code spans/fences already protect code. Literal XML, HTML and JSON outside a code or raw block should be escaped.
- `{#if condition}...{:else if other}...{:else}...{/if}` conditionally renders elements. `{#each values as item, index}...{/each}` repeats components. `<>...</>` groups siblings without a layout wrapper.
- `{@module ...}` declares pure, hook-free helpers; `{@body ...}` declares local state, hooks and callbacks before the UI consuming them.
- Hooks available: `DIL.useState(initial, {key}?)` (state lives in keyed slots that survive streaming re-parses; the declared name is the default key), `DIL.useMemo`, `DIL.useCallback`, `DIL.useRef`, `DIL.useId`, `DIL.useTheme`, `DIL.useBreakpoint`, `DIL.useEffect`. Call hooks only at the unconditional top level of `{@body ...}` in a consistent order. Never call hooks inside markup, conditionals, loops, callbacks or nested functions. Dependencies are arrays. Effects may update local state; they must not trigger host actions.
- Expressions are bounded, not arbitrary JavaScript execution. No browser DOM APIs, networking, filesystem, browser storage, timers, workers, dynamic imports, `eval`, `Function`, or `globalThis` in ordinary expressions. Raw AppBlock code has its own isolated contract.
- Available helpers are pure and deterministic: `Math` (`min,max,round,floor,ceil,trunc,sign,abs,sqrt,cbrt,pow,hypot,exp,log,log2,log10`, trigonometry, `PI`, `E`); `Number(...)`, `Number.isFinite/isInteger/isNaN/parseInt/parseFloat`, `parseInt`, `parseFloat`; `String(...)`, `String.fromCharCode`; `Array(n)`, `Array.from`, `Array.isArray`, `Array.of`; `Object.keys/values/entries/fromEntries/assign`; `JSON.parse/stringify`; `Date.UTC`, `Date.parse` and `new Date(explicitValue)`. Arrays: `map,filter,find,findIndex,findLast,findLastIndex,some,every,forEach,reduce,flat,flatMap,concat,slice,join,includes,indexOf,lastIndexOf,at,sort,reverse,fill,push,pop,shift,unshift` (`sort`/`reverse` return copies). Strings: `slice,substring,split,trim,trimStart,trimEnd,toUpperCase,toLowerCase,includes,startsWith,endsWith,indexOf,lastIndexOf,charAt,charCodeAt,codePointAt,at,padStart,padEnd,repeat,replace,replaceAll` (text patterns only), `localeCompare`. Numbers: `toFixed,toPrecision,toString(radix),toLocaleString`. Spread works in arrays, objects and calls. `Math.random`, `Date.now`, `new Date()` without a value, `Set`, `Map`, `Intl` constructors and regular expressions are unavailable; use arrays/objects and `toLocaleString`.
- Use state only when a user's interaction affects the view. Derive computed values rather than duplicate them in state. Prefer simple callbacks and small bounded data structures. Provide stable keys on repeated items when a compatible component accepts `key`.
- For dynamic LaTeX, use `<math value={String.raw`...`} />`; use static Markdown math delimiters for static equations.
- Typography and text props are constrained; no arbitrary CSS or unlisted HTML attributes.

Example — live when emitted outside the fence:

```jsx
{@body
const [quantity, setQuantity] = DIL.useState(2);
const total = Math.max(0, Number(quantity) || 0) * 25;
}
<box border radius="xl" padding={4} gap={3}>
  <title size="lg">Estimate</title>
  <label>Quantity</label>
  <input inputType="number" value={quantity} onChange={setQuantity}/>
  <title size="xl" tabularNums>{total} credits</title>
  <button onClick={() => setQuantity(Math.max(0, Number(quantity) + 1))}>Add one</button>
</box>
```

## 2. Component grammar and nesting

Properties below are exact supported names. Optional properties may be omitted. Do not pass shared layout properties to components that do not explicitly inherit them.

### Text components

| Component | Props | Children |
|---|---|---|
| `badge` | `color,label,maxWidth,onHover,padding,pill,size,variant,weight` | inline |
| `bold` | `weight` | inline |
| `caption` | `color,maxLines,textAlign,truncate,weight` | inline |
| `code` | none | inline |
| `italic` | none | inline |
| `label` | `color,htmlFor,size,textAlign,truncate,weight,width` | inline |
| `math` | `block,value` | none |
| `strikethrough` | none | inline |
| `text` | `color,highlight,inline,italic,lineThrough,maxLines,minLines,preserveWhitespace,size,strong,tabularNums,textAlign,truncate,underline,weight,width` | inline |
| `title` | `color,italic,maxLines,size,strong,tabularNums,textAlign,truncate,weight,width` | inline |
| `underline` | none | inline |
| `blockquote` | none | generic |

`inline` means inline phrasing/formatting/Markdown. `generic` means inline or block Markdown and components. Avoid oversized headings and badge-as-heading decoration. Use `tabularNums` for changing numeric output.

### Layout components

The **shared layout props** apply **only** to `box`, `col`, `flow-item`, `pressable`, `row`, and `form`:

`align,background,border,clip,direction,disableAutoSpacing,flex,flush,gap,height,justify,key,maxHeight,maxWidth,minHeight,minWidth,padding,radius,role,scrollable,theme,width,wrap`.

| Component | Additional/own props | Direct children |
|---|---|---|
| `box` | shared + `aspectRatio,columnGap,maxSize,minSize,rowGap,size` | generic |
| `col` | shared + `aspectRatio,columnGap,maxSize,minSize,size` | generic |
| `row` | shared + `aspectRatio,maxSize,minSize,rowGap,size` | generic |
| `flow-item` | shared + `aspectMode,aspectRatio,rowSpan,span` | generic; only within `flow` |
| `pressable` | shared + `aspectRatio,disabled,inline,maxSize,minSize,onClick,size,tooltip` | generic |
| `form` | shared + `aspectRatio,maxSize,minSize,onSubmit,size` | generic |
| `card` | `background,disableAutoSpacing,gap,height,justify,padding,shadow,size,theme,variant,width` | generic |
| `carousel` | `activeIndex,flush,gap,onChange,showArrows,snap,snapAlign,visibleItems` | `carousel-item`, `carousel-media-item` |
| `carousel-item` | `aspectRatio,background,border,gap,height,key,maxHeight,maxWidth,minHeight,minWidth,padding,radius,variant,width` | generic |
| `carousel-media-item` | `aspectRatio,background,border,gap,height,key,maxHeight,maxWidth,media,minHeight,minWidth,padding,radius,size,variant,width` | generic |
| `divider` | `color,flush,size,spacing` | none |
| `flow` | `columns,gap,layout,rows` | `flow-item` |
| `grid` | `columnGap,columns,gap,minChildWidth,rowGap,rows` | `grid-item` |
| `grid-item` | `rowSpan,span` | generic |
| `list` | `connector,gap,marker,maxMarkerSize,start` | `list-item`, generic |
| `list-item` | `description,disabled,label,marker,markerSize` | generic |
| `popover` | `hoverOpenDelay,open,showOnHover` (Zenith: `onOpenChange`) | `popover-trigger`, `popover-content` |
| `popover-trigger` | `block,onClick` | generic |
| `popover-content` | `align,showCloseButton,side,sideOffset` | generic |
| `spacer` | `minSize` | none |
| `table` | `columnSizing,dividers,emptyLabel,stickyHeader` | `table-section`, `table-row` |
| `table-section` | `header,rowDivider` | `table-row` |
| `table-row` | `header,label,labelColSpan` | `table-cell` |
| `table-cell` | `align,colSpan,header,padding,rowSpan,vAlign,width` | generic |

Direct-child requirements are structural: `grid-item` inside `grid`; `flow-item` inside `flow`; `table-cell` inside `table-row`; and native carousel and popover children as specified. Do not insert arbitrary wrapper components between the required parent and direct child. `card` does not take generic `border` or `radius`; use supported `variant` and `background`.

Spacing properties such as `gap`, `rowGap`, `columnGap`, `padding`, `spacing` accept numeric spacing units (1 = 4 points), and where overrides are supported, literal lengths `px`, `rem`, `pt`, `%` or `"0"`. Avoid assuming arbitrary CSS.

### Input and interaction components

| Component | Props | Children |
|---|---|---|
| `button` | `block,color,disabled,label,loading,onClick,pill,size,submit,uniform,variant` | inline |
| `checkbox` | `checked,defaultChecked,disabled,label,lineThrough,name,onChange,required,shape,value` | generic |
| `date-picker` | `align,block,clearable,defaultValue,disabled,max,min,onChange,pill,placeholder,required,side,size,value,variant` | none |
| `input` | `autoSelect,defaultValue,disabled,fontSize,inputType,invalid,mask,name,onChange,pill,placeholder,required,size,value,variant` | none |
| `radio-group` | `defaultValue,direction,disabled,label,name,onChange,required,value` | `radio` |
| `radio` | `disabled,id,required,value` | generic |
| `segmented-control` | `defaultValue,disabled,label,onChange,options,pill,size,value,variant` | none |
| `select` | `align,block,clearable,defaultValue,disabled,icon,name,onChange,options,pill,placeholder,required,side,size,value,variant` | none |
| `slider` | `defaultValue,disabled,marks,max,min,name,onChange,required,sliderStyle,step,value` | none |
| `textarea` | `autoResize,autoSelect,defaultValue,disabled,invalid,maxRows,name,onChange,placeholder,required,rows,size,value,variant` | none |

- Controlled inputs receive `value` plus `onChange`; checkboxes receive `checked`. The change callback receives **the new value**, not a DOM event. Checkbox receives a boolean. Slider receives its new value. Buttons and pressables use zero-argument `onClick`; `form.onSubmit()` takes no argument. `carousel.onChange(nextIndex)`, `badge.onHover(hovered)`, `popover-trigger.onClick()` follow their named signatures.
- Do not pass `type` to `input`; use `inputType`. `radio-group` takes nested `<radio value="...">...</radio>` children, **not** `options`. `segmented-control` and `select` do accept `options`.
- Write `<label>Text</label>` directly before a control: Zenith links the two for assistive technology. To label a control elsewhere, give the control an `id` (Zenith extension on `input`, `textarea`, `select`, `date-picker`, `slider`, `checkbox`, `segmented-control`, `radio-group`) and point `label htmlFor` at it. `radio-group` and `segmented-control` also take their own `label`.
- `button` combines `color` (`primary` default, `secondary`, `danger`, `success`, `warning`, `info`) with `variant` (`solid` default, `soft`, `outline`, `ghost`). `badge` defaults to a soft neutral tone.
- `checkbox` defaults to `lineThrough={false}`; enabling it strikes checked labels.
- A button with `submit` submits its enclosing `form`. Buttons and pressable surfaces must do what they advertise. Do not bind fixed values to no-op callbacks or add decorative controls.
- Use reasonable defaults, bounded numeric calculations, validation/invalid states, clear reset behavior and truthful local-vs-remote messaging. Do not claim that a local state change saved remote data.

Example — form with real interactions:

```jsx
{@body
const [name, setName] = DIL.useState("");
const [plan, setPlan] = DIL.useState("basic");
const [submitted, setSubmitted] = DIL.useState(false);
const valid = name.trim().length > 0;
}
<form gap={3} onSubmit={() => setSubmitted(true)}>
  <title size="lg">Configure your plan</title>
  <label>Name</label>
  <input value={name} onChange={(next) => { setName(next); setSubmitted(false); }} invalid={!valid} placeholder="Enter your name" />
  <radio-group value={plan} onChange={(next) => { setPlan(next); setSubmitted(false); }}>
    <radio value="basic">Basic</radio>
    <radio value="premium">Premium</radio>
  </radio-group>
  <button submit disabled={!valid}>Preview selection</button>
  {#if submitted && valid}<text>Local preview: {name} chose {plan}.</text>{/if}
</form>
```

### Media primitives

| Component | Props | Rules |
|---|---|---|
| `icon` | `color,inline,name,size` | Lucide names listed in [icons](references/icons.generated.md), mapped to Zenith’s Iconsax catalog, or Zenith Iconsax names |
| `favicon` | `alt,frame,inline,size,url` | Use a supported URL |
| `svg` | `color,fill,height,preserveAspectRatio,size,stroke,strokeLinecap,strokeLinejoin,strokeWidth,viewBox,width,xmlns` | Native constrained SVG children; state-derived attributes allowed; no event handlers inside SVG |

Use only names in [icons](references/icons.generated.md): Lucide names with a Zenith equivalent, or Zenith’s own Iconsax and brand names. An unknown name is omitted with a diagnostic rather than drawn as another glyph, so a control must never depend on an icon alone. Do not invent icon names. Keep SVG clicks in an external `button` or `pressable`, never on native SVG paths/shapes. Do not embed scripts, foreign HTML or external resources in SVG.

### Server-linked, references, and links

| Component | Props | Rules |
|---|---|---|
| `Cite` | `params,ref,refs` | Existing source references only |
| `CodeCite` | `citation_id,line_range_end,line_range_start,tool` | Existing code source lines only |
| `Entity` | `address,category,disambig,location,query,ref,value` | In Zenith: `category="task"` or `"page"`, the item's real ID as `ref`, display text as `value`; other categories are not connected |
| `FileCite` | `line_range_end,line_range_start,ref` | Actual file ref and evidenced line range |
| `FileNavList` | `items` | Actual file navigation refs |
| `FollowUp` | `query,source,value` | Real suggested follow-up query |
| `Link` | `ref,title,url` | Generic children; use `url`, not `href` |

Never invent references, citation IDs, file paths, businesses, media refs, URLs or memory evidence. Where the host has not supplied a required capability, omit the component or describe the limitation accurately.

### Raw-content blocks

| Component | Props | Children |
|---|---|---|
| `CodeBlock` | `editable,interactive,language,model_previewable` | Raw source body, not a `code` property |
| `WritingBlock` | `bcc,cc,id,options,recipient,subject,variant` | Raw Markdown/basic formatting only |
| `AppBlock` | `app_block_id,bind,bundle_version,entrypoint,icon,language,title,variant` | Raw HTML/JS; separate visualization execution environment |

- `CodeBlock` displays code. Mermaid code blocks preview diagrams. For generic flowcharts use normal step nodes `A[Step]` and decision nodes `B{Decision?}`.
- `WritingBlock` is for practical standalone material intended to be copied or edited: email, chat message, social post, memo, document description. Use a unique five-digit `id` for a new artifact and preserve the **same** ID for a revision of that artifact. Variants: `email`, `chat_message`, `social_post`, `document`, `standard`. For emails, populate known intended `recipient`, `cc`, `bcc`; never invent email addresses. `subject` applies to email only. Do not use WritingBlock for general explanation, programming, math, recipes, or fiction. If presenting multiple versions, the default draft is first in `options` and is also the primary raw draft. Do not put DIL component tags inside WritingBlock.
- `AppBlock` is appropriate for custom visualizations not achievable with native components. Preserve a useful static visualization when its host binding is unavailable. It is a separate raw content environment, **not** a way to execute arbitrary JS in ordinary DIL expressions.

## 3. Visualizations

### AsyncImage

Props: `alt,aspectRatio,fallback,fit,frame,height,maxHeight,maxWidth,minHeight,minWidth,radius,src,width`.

In Zenith, supply `src`: an **unchanged HTTPS image URL** that a tool or an authorized Zenith asset already resolved. Do not construct, alter or guess the URL. Use `fallback` for text shown if the image fails. Image search (`query`), media refs (`ref`), `AsyncImageGroup` and `AsyncVideo` need providers Zenith does not connect; do not author them.

### Chart

`<Chart content={...} />` takes **one object**, not a string or an external chart-library object.

- Bar/line/scatter: `{chartType:"bar"|"line"|"scatter",xKey:"x",series:[{dataKey:"y",label:"..."}],data:[{x:"...",y:1}]}`.
- Pie: `{chartType:"pie",nameKey:"name",valueKey:"value",series:[{dataKey:"value"}],data:[{name:"A",value:10}]}`.
- Optional: `xAxisLabel,axisLabel,valueSuffix,yAxisMin,yAxisMax`; `xAxisScale:"linear"` only for numeric X on a line chart; `layout:"vertical"` for bars. Series may include `valueFormat:"compact",valuePrefix,valueSuffix`, and line band lower/upper bounds or `lowerKey`/`upperKey`.
- Keep quantities as numbers, categorical labels/identifiers (years treated as labels, postal codes, IDs) as strings. Do not clip data with invented axis bounds. Show units and label illustrative data honestly. The renderer chooses chart colors.

```jsx
<Chart content={{chartType:"line",xKey:"day",series:[{dataKey:"latency",label:"P95 latency",valueSuffix:" ms"}],data:[{day:"Mon",latency:320},{day:"Tue",latency:270},{day:"Wed",latency:240}]}} />
```

## 5. Host actions and interaction restrictions

Only the following direct actions are available, and only from a genuine user interaction on a control with an event callback (such as `button` or `pressable`):

```text
GenUI.openUrl(url: string)
GenUI.copy(text: string)
GenUI.issueNewTurn(query: string)
GenUI.openEntityDetail({category:"task"|"page",refId:string})
```

- Call a host action synchronously **inside the click/change handler**, not during render, mount, an effect or a computed expression. Do not invent other host calls.
- `issueNewTurn` submits a new message via the current authorized conversation; it cannot send attachments or metadata.
- `openEntityDetail` opens Zenith tasks and Pages: `GenUI.openEntityDetail({category:"task"|"page",refId:realId})`. Product, business and other categories are not connected. Do not invent an ID or claim success before host feedback.
- A UI component does not grant access to external websites, files, location, personal data, email, calendars, or remote writes. Never promise that a host action completed merely because a button was clicked.
- Keep state and observable effects consistent. If a button only copies text, call it “Copy”; if it only changes local state, say “Preview locally” rather than “Save”.

Example:

```jsx
<row gap={2} wrap>
  <button onClick={() => GenUI.copy("Example text")}>Copy text</button>
  <button variant="outline" onClick={() => GenUI.issueNewTurn("Explain this in more detail")}>Ask for detail</button>
</row>
```

## 6. AppBlock bridge

AppBlock is a raw HTML/JS visualization block, not ordinary component syntax. For normal interactive responses, keep the controls in native DIL and pass only necessary value/callback bindings to AppBlock: `bind={{value,setValue}}`. Inside raw JS, read `globalThis.__bindings?.value` from `globalThis.__effect?.(() => { ... })`. Only a direct user interaction **inside the visualization** may call `globalThis.__bindings?.setValue?.(nextValue)`. **Never call that setter from an effect.** Every access to `__bindings` and every invocation of `__effect` must optional-chain the global, even after another guard. Include an intelligible static visualization if bindings/effects are unavailable.

A custom AppBlock is justified for an interactive visualization that native components cannot adequately express, not as a generic replacement for inputs, cards or charts. Its execution must remain in the host's supported sandbox, with no assumed access to privileged services. Do not treat AppBlock's raw JS privileges as available to `{@body}` expressions.

## 7. Layout, content strategy and visual hierarchy

- **Choose the right medium:** prose for short explanatory answers; a table for exact aligned comparisons; cards for a small set of distinct choices; a form only if gathering multiple independent values is essential; charts for trends or distributions; grounded addresses and links for places (maps are not connected in Zenith); carousels for browsable media or a small series of panels; SVG for bounded static diagrams; AppBlock for genuinely custom visuals.
- **Hierarchy:** answer the question near the top. Use a small appropriate title, succinct context, and then meaningful information or controls. Avoid repeated decorative section banners and oversized headings.
- **Spacing:** one clear primary container or semantic groups. Use deliberate `gap`, consistent padding and only as many nested boxes as necessary. Favor one column on narrow screens, compact rows for peer controls, grids for equal metrics and tables for dense precise comparison.
- **Color:** neutral surfaces by default, selective semantic colors for meaningful statuses; do not wrap conclusions in colored summary boxes. One standout badge at most when important, not as ordinary metadata or heading decoration.
- **Text:** keep paragraphs cohesive, readable and directly useful. Avoid stacked one-sentence paragraphs, verbose labels and redundant captions. Distinguish units, data provenance, assumptions and illustrative numbers.
- **Inputs:** don't show a large form when one conversational question suffices. Display results that change as values change. Include sensible defaults, minimum/maximum, disabled states and reset where useful. Use validated data and handle empty/invalid input.
- **Images:** use media only when it adds information, not decoration. Prefer compact multi-image groups/carousels to full-width imagery unless a larger image is central. Prefer a 16:9 ratio for a full-width image. In collections, keep title and description type sizes restrained and consistent. Image list items should use plain text links, not artificial buttons.
- **Responsive/accessibility:** aim for logical reading order, native controls, clear labels, text alternatives, keyboard-usable interactions where provided by native components, sufficient contrast and labels that do not rely on color alone. Do not invent unsupported aria/DOM props to achieve this; respect the component schema.
- **Truthfulness:** differentiate sourced facts from user-provided inputs and hypothetical data; never imply real-time availability, verified rating, a saved record or successful remote action without evidence.

## 8. Streaming, identity, and limitations

- Parse and display valid completed markup. An unfinished expression/attribute is incomplete, not silently repaired into executable JavaScript. Interactions should only be active once complete and authorized.
- Keep outputs bounded and cohesive: avoid deeply nested UI, huge local arrays, large heavy computation, repeated server media queries, or content that obscures the answer. Practical response limits include 256 KiB source, nesting depth 32, array sizes 1,000 and bounded evaluation work.
- Stable UI identity matters when using repeated stateful elements. Only use `key` on components that document it; for complex repeated layouts, key a compatible container inside the list.
- No access to arbitrary internet, storage, host filesystem, DOM or APIs through normal inline expressions. The host and tool declarations determine what references and external operations are actually available.
- A source-only response must be clearly in a code block. Do not accidentally render a demonstration of raw markup unless the user wants it live.

## 9. Quick reference patterns

### Native grid

```jsx
<grid columns={2} gap={3}>
  <grid-item><box padding={3} background="surface-secondary" radius="lg"><title size="lg">Alpha</title><text>First result</text></box></grid-item>
  <grid-item><box padding={3} background="surface-secondary" radius="lg"><title size="lg">Beta</title><text>Second result</text></box></grid-item>
</grid>
```

### Native carousel with controlled index

```jsx
{@body const [index,setIndex] = DIL.useState(0);}
<carousel activeIndex={index} onChange={setIndex} showArrows>
  <carousel-item><box padding={4}><title size="md">First</title><text>Panel one</text></box></carousel-item>
  <carousel-item><box padding={4}><title size="md">Second</title><text>Panel two</text></box></carousel-item>
</carousel>
```

### Native popover

```jsx
{@body const [open,setOpen] = DIL.useState(false);}
<popover open={open}>
  <popover-trigger onClick={() => setOpen(!open)}><text>Show explanation</text></popover-trigger>
  <popover-content side="bottom" showCloseButton>
    <text>This is additional context.</text>
  </popover-content>
</popover>
```

### Dynamic SVG with an external control

```jsx
{@body const [level,setLevel] = DIL.useState(50);}
<box gap={3}>
  <svg viewBox="0 0 240 36" width="100%" height="36px">
    <rect x="0" y="7" width="240" height="22" rx="11" fill="#CBD5E1"/>
    <rect x="0" y="7" width={level * 2.4} height="22" rx="11" fill="#2563EB"/>
  </svg>
  <slider min={0} max={100} value={level} onChange={setLevel}/>
  <text>{level}%</text>
</box>
```

### Dynamic table

```jsx
{@body const rows = [{name:"A",value:12},{name:"B",value:18}];}
<table>
  <table-row><table-cell header>Item</table-cell><table-cell header align="end">Value</table-cell></table-row>
  {#each rows as item}<table-row><table-cell>{item.name}</table-cell><table-cell align="end">{item.value}</table-cell></table-row>{/each}
</table>
```

## 10. Final validation checklist

Before sending, verify all of the following:

1. **Useful answer:** Does the visible response itself answer the user's request, rather than depending on a visualization or interaction alone?
2. **Valid grammar:** Declarations before use; all tags, fragments and directives closed; ambiguous literals escaped; raw blocks kept raw; Markdown parsed intentionally.
3. **Exact API:** Every component and prop exists above. Layout props inherit only where documented. `grid`, `flow`, table, carousel, radio-group and popover have correct direct children.
4. **Correct hooks:** Unconditional top-level `{@body}` hook calls, stable order, bounded computation, no browser globals in ordinary expressions.
5. **Real interaction:** Every input state and callback is wired correctly; changes affect the view; controls are not fake; submits/resets have appropriate effects and labels.
6. **Data validity:** No invented source IDs, product/business refs, URLs, ratings, opening hours, reservations, map points, chart observations, or file links. Bound fields match the actual declared schema.
7. **Media and visualizations:** Proper image source mode, distinct server-backed media occurrences, appropriate chart schema and units, grounded mapping, constrained SVG, permitted AppBlock bindings.
8. **Host security:** Actions only on direct interaction; no actions on mount/effect; no fabricated remote success or host permission.
9. **Usability:** Clear hierarchy, mobile-friendly structure, accessible labels, semantic color use, sensible density, and concise explanatory text.
10. **Limits and streaming:** Keep the view compact, bounded, stable and complete, with no assumption that partially authored source will be auto-fixed or executed.



---


## 11. Exact prop types and defaults


The component inventory in Section 2 is the authoritative **prop-name allowlist**. The rules here further constrain values. A prop being present in the allowlist does not mean every JavaScript value or CSS string is accepted. Omit undocumented values rather than inventing them. **Do not claim an unspecified default is guaranteed.**

| Prop family | Type or authoring contract | Explicitly documented behavior |
|---|---|---|
| `padding`, `gap`, `rowGap`, `columnGap`, `spacing` | Number in spacing units; supported literal length strings (`px`, `rem`, `pt`, `%`, `"0"`) | `1` corresponds to 4 points; lengths are accepted only on supported spacing overrides |
| `width`, `height`, `minWidth`, `minHeight`, `maxWidth`, `maxHeight`, `minSize`, `maxSize` | Supported dimensions or size tokens | Do not assume free-form CSS |
| `border`, `radius`, `background`, `theme`, `variant`, `color`, `size`, `weight`, `shadow` | Supported semantic tokens for that exact component | Do not invent a cross-component enum; supported tokens can differ by component |
| `align`, `justify`, `direction`, `wrap`, `flex`, `clip`, `scrollable` | Layout-aligned values on components with those props | Layout shared props apply only to the six listed components |
| `checked`, `defaultChecked`, `disabled`, `required`, `loading`, `submit`, `pill`, `block`, `clearable`, `autoSelect`, `tabularNums`, `stickyHeader` | Boolean where indicated by intended semantics | Checkbox `lineThrough` is `false` by default |
| `value`, `defaultValue` | Input-dependent string, number, boolean, or date value | A controlled value requires the corresponding functioning `onChange` |
| `inputType` | Input-type identifier supported by the host, e.g. `"number"` | Use `inputType`, not `type` |
| `options` | Array of `{label, value}` option objects for `select` and `segmented-control` | `radio-group` instead requires `radio` children |
| `min`, `max`, `step`, `marks` | Component-specific numeric/range configuration | Keep slider bounds coherent and input values in range |
| `date-picker.value`, `date-picker.defaultValue`, `date-picker.min`, `date-picker.max` | Date representations accepted by picker; examples use `YYYY-MM-DD` | Do not invent timezone/date coercion rules |
| `textAlign`, `maxLines`, `minLines`, `truncate`, `preserveWhitespace` | Text-only layout and presentation values | Avoid unsupported CSS props |
| `onChange`, `onClick`, `onSubmit`, `onHover` | Function with event-specific signature in Section 2 | Change callbacks receive new values, not DOM events |
| `Chart.content` | One JSON-compatible chart object | Chart renderer controls plotting and colors |
| `AsyncImage.src` | One unchanged, already resolved HTTPS URL (Section 3) | Do not construct or alter URLs |
| `carousel.activeIndex` | Numeric index | Treat `onChange(nextIndex)` as source of changes |

**Token values in Zenith.** Use only these for the props that take tokens:

| Prop | Values |
|---|---|
| `color` | `default`, `primary`, `secondary`, `subtle`, `neutral`, `info`, `success`, `danger`, `warning`, `accent` |
| `background` | `background`, `surface`, `surface-secondary`, `transparent`, `blue`, `purple`, `yellow`, `green`, `red`, `info`, `success`, `warning`, `danger`, `accent` |
| `variant` | `default`, `solid`, `soft`, `subtle`, `outline`, `ghost`; WritingBlock: `standard`, `email`, `chat_message`, `social_post`, `document`; AppBlock: `inline` |
| `size` | `xs`, `sm`, `md`, `lg`, `xl`, `2xl`, `3xl`, or a dimension where the component sizes a box |
| `weight` | `normal`, `medium`, `semibold`, `bold`, or 100–900 |
| `radius` | `none`, `sm`, `md`, `lg`, `xl`, `2xl`, `full` |
| `align` | `start`, `end`, `center`, `stretch`, `baseline` (`table-cell` and `textAlign` also take `left`, `right`, `justify`) |
| `justify` | `start`, `end`, `center`, `between`, `around`, `evenly` |
| `direction` | `horizontal`, `vertical` |
| `theme` | `light`, `dark`, `inherit` |
| `inputType` | `text`, `number`, `email`, `password`, `search`, `url`, `tel` |
| `side` | `top`, `right`, `bottom`, `left` |
| dimensions | numbers (0–2000) or `px`/`pt`/`rem`/`em`/`%`/`vh`/`vw` lengths, `auto`, `fit-content`; `aspectRatio` as `16/9`, `16:9` or a number |

`useBreakpoint()` returns `sm` (under 640px), `md` (under 1024px) or `lg`, measured on the containing view. Layout containers space their children by default; `disableAutoSpacing` removes that.

**Documented defaults and precise constraints:** `checkbox.lineThrough` defaults to `false`. A host-resolved component occurrence resolves once per authored occurrence. Server-backed data is not dynamically re-resolved from changing client-side state. Do not manufacture default values for every optional prop, exact breakpoint widths, line-height or CSS defaults, or unspecified `useBreakpoint()` return shapes. Prefer omitting those props to taking a guess. The renderer must document additional type-specific enum values it elects to support.

### Core callback signatures

```text
badge.onHover(hovered: boolean)
button.onClick()
carousel.onChange(nextIndex)
checkbox.onChange(checked: boolean)
date-picker.onChange(nextValue)
form.onSubmit()
input.onChange(nextValue)
popover-trigger.onClick()
pressable.onClick()
radio-group.onChange(nextValue)
segmented-control.onChange(nextValue)
select.onChange(nextValue)
slider.onChange(nextValue)
textarea.onChange(nextValue)
```

Use `onClick={() => ...}` only on supported click controls. In particular, no event handlers on `svg` or `AsyncImage`. Do not assume setter-updater-function semantics unless tested; direct setters with computed new values are sufficient.


## 12. Streaming and presentation details


- Streaming allows completed child nodes to appear inside still-open surrounding components. Rendering a completed child does **not** imply the full message has finished.
- Incomplete tags, attribute values, expressions, and directives must remain withheld; never auto-complete them into executable expressions, improvise closing text, or run guessed handlers.
- A control appears once its opening tag is complete. Completed controls may change **local state** while the rest of the message streams; host actions (`GenUI.*`), effects and AppBlock scripts wait until the **entire assistant message is complete**. Never rely on an effect or host action running mid-stream.
- Hook declarations and their consumers must stay in one cohesive view. Do not split dependent UI into unrelated transcript segments, where the state scope or partial rendering behavior would differ.
- Final malformed or incomplete markup should surface a diagnostic and source-view recovery rather than invent a missing expression or action. Do not rely on the runtime repairing an incorrect source tree.
- A visible status message that reads “saved”, “sent”, or “booked” must be tied to actual confirmed host results, not to markup completion or local state alone.
- Treat maximum source length (256 KiB), nesting depth (32), array length (1000), and evaluation budget as hard safety limits. Prefer smaller components, bounded datasets, and nonrecursive calculation.


**Forms.** Ask conversationally for one essential missing fact. Use a form only when the task cannot reasonably proceed without several independent facts *and* collecting them together is easier than a brief conversation *and* the visual footprint is worthwhile. At most one form per response. Permit partial submission unless completion of a field is absolutely required.

**Media.** Prefer grouped/carousel imagery rather than oversized full-width images. For genuinely full-width media prefer 16:9. Image groups must contain independent visual evidence, not near-duplicates. In side-by-side image lists, use ordinary links rather than button-style navigation, keep all titles/descriptions the same relatively small text size, use bold rather than oversized fonts to mark titles, and prefer portrait images adjacent to long descriptions. Do not query the same image once inside a loop over different items; author distinct server-resolved occurrences.

**Badges.** Use sparingly, generally for a single notable status. Neutral gray is the default; color only when status semantics warrant it. Never use badges as list ordinals, headings, metadata decoration, or repeated shared-property labels.

**Summary/recommendation presentation.** Avoid colorful highlighted conclusion boxes, green-bordered winner boxes, and unneeded decorative callouts. Explain recommendations directly. Keep useful text understandable without rendering a widget. For precise tabular comparison use a table; don't duplicate equivalent cards and tables.

**Text and spacing.** Use the smallest heading size sufficient to establish hierarchy. In multi-column views keep titles and body relatively compact. Favor neutral surfaces, a consistent spacing scale, readable units and contrast. Avoid one-sentence stacks, gratuitous section splits, wide controls for tiny tasks, and interaction when text is easier.

**Sources and actions.** Give plain links for browsing choices. Never invent a download sandbox path or declare an operation completed without a successful host response. Use visualizations to supplement—not replace—the useful explanatory answer. Cite evidence near the actual claim.


## 13. End-to-end examples


The examples below intentionally exercise supported native components and small state. Their data is illustrative unless declared otherwise. Copy the code **inside** each fence into a live response to render. Each example is independently scoped; do not combine independent `{@body}` declarations into one view without resolving variable names.

### A. Responsive analytical dashboard: switch metrics and compare a trend

```jsx
{@body
const [series,setSeries] = DIL.useState("requests");
const rows=[{day:"Mon",requests:120,errors:4},{day:"Tue",requests:145,errors:3},{day:"Wed",requests:139,errors:7},{day:"Thu",requests:172,errors:2},{day:"Fri",requests:194,errors:5}];
const total=rows.reduce((a,r)=>a+r[series],0);
}
<box border radius="xl" padding={4} gap={3}>
  <row justify="between" align="center" wrap gap={2}>
    <col gap={1}><title size="xl">Service overview</title><caption>Illustrative five-day data</caption></col>
    <segmented-control value={series} onChange={setSeries} options={[{label:"Requests",value:"requests"},{label:"Errors",value:"errors"}]}/>
  </row>
  <grid columns={2} gap={2}>
    <grid-item><box background="surface-secondary" padding={3} radius="lg"><caption>Total selected</caption><title size="xl" tabularNums>{total}</title></box></grid-item>
    <grid-item><box background="surface-secondary" padding={3} radius="lg"><caption>Last day</caption><title size="xl" tabularNums>{rows[4][series]}</title></box></grid-item>
  </grid>
  <Chart content={{chartType:"line",xKey:"day",series:[{dataKey:series,label:series==="requests"?"Requests":"Errors"}],data:rows}}/>
  <table>
    <table-row><table-cell header>Day</table-cell><table-cell header align="end">Value</table-cell></table-row>
    {#each rows as r}<table-row><table-cell>{r.day}</table-cell><table-cell align="end">{r[series]}</table-cell></table-row>{/each}
  </table>
</box>
```

### B. Validated local estimate with controls, errors, and reset

```jsx
{@body
const [name,setName]=DIL.useState("");
const [qty,setQty]=DIL.useState(2);
const [tier,setTier]=DIL.useState("standard");
const [support,setSupport]=DIL.useState(false);
const [submitted,setSubmitted]=DIL.useState(false);
const count=Math.max(1,Math.min(20,Number(qty)||1));
const amount=count*(tier==="premium"?40:25)+(support?15:0);
const valid=name.trim().length>0;
}
<box border radius="xl" padding={4} gap={3}>
  <title size="xl">Estimate builder</title>
  <caption>Illustrative prices; nothing is charged or saved remotely</caption>
  <label>Name</label>
  <input value={name} onChange={(v)=>{setName(v);setSubmitted(false);}} invalid={!valid} placeholder="Contact name"/>
  <label>Quantity</label>
  <slider value={count} min={1} max={20} step={1} onChange={(v)=>{setQty(v);setSubmitted(false);}}/>
  <row justify="between"><text>Items</text><text tabularNums>{count}</text></row>
  <radio-group value={tier} onChange={(v)=>{setTier(v);setSubmitted(false);}}>
    <radio value="standard">Standard · 25 each</radio>
    <radio value="premium">Premium · 40 each</radio>
  </radio-group>
  <checkbox checked={support} onChange={(v)=>{setSupport(v);setSubmitted(false);}}>Premium support (+15)</checkbox>
  <box background="surface-secondary" padding={3} radius="lg" gap={1}><caption>Estimated total</caption><title size="2xl" tabularNums>{amount} credits</title></box>
  {#if !valid}<text size="sm" color="secondary">Enter a name to prepare a local preview.</text>{/if}
  <row gap={2} wrap>
    <button disabled={!valid} onClick={()=>setSubmitted(true)}>Prepare preview</button>
    <button color="secondary" variant="outline" onClick={()=>{setName("");setQty(2);setTier("standard");setSupport(false);setSubmitted(false);}}>Reset</button>
  </row>
  {#if submitted}<text>Local preview prepared for {name}: {amount} credits. No external request was made.</text>{/if}
</box>
```


## 14. AppBlock example

### H. Native control with AppBlock visualization bridge

Raw AppBlock execution is a **separately host-enabled** capability. The internal raw HTML and JS must supply a meaningful static fallback, and read/write bindings via the exact bridge below. For ordinary answers, the native DIL slider owns the value; AppBlock visualizes it.

```jsx
{@body const [level,setLevel]=DIL.useState(40);}
<box gap={3}>
  <label>Level: {level}</label>
  <slider min={0} max={100} value={level} onChange={setLevel}/>
  <AppBlock title="Level visualization" language="html" bind={{value:level,setValue:setLevel}}>
    <div style="padding:16px;border-radius:12px;background:#eef2f7;font-family:system-ui">
      <div style="height:16px;background:#d1d5db;border-radius:8px;overflow:hidden">
        <div id="meter" style="width:40%;height:100%;background:#4f46e5"></div>
      </div>
      <div id="caption" style="margin-top:8px">Illustrative level: 40</div>
    </div>
    <script>
      globalThis.__effect?.(() => {
        const raw = globalThis.__bindings?.value;
        const level = Math.max(0,Math.min(100,Number(raw ?? 40)));
        const meter=document.getElementById('meter');
        const caption=document.getElementById('caption');
        if(meter) meter.style.width=level+'%';
        if(caption) caption.textContent='Illustrative level: '+level;
      });
    </script>
  </AppBlock>
</box>
```

This example's raw script runs **only inside the AppBlock sandbox**, never ordinary DIL expressions. It is illustrative and requires actual host support for the embedded script and binding bridge; do not claim that it has been executed unless tested.


## 15. Final acceptance checks


Before treating any example as *runnable* rather than *illustrative syntax*, check it against the active implementation:

1. **Parse:** No unsupported props or children; tags/directives closed; raw block boundaries respected; expressions compile within the restricted runtime.
2. **Initialize:** Every state read has an unconditional top-level declaration; no hooks inside loops or conditionals; first render is coherent.
3. **Interact:** Every control changes visible state and advertised actions operate; no disabled-by-mistake, no inert submit handler; reset returns to default.
4. **Corner cases:** Zero/empty, max/min, toggling back, repeated clicks, game over, missing optional source fields and missing host integrations.
5. **Visualize:** Chart series keys correspond to the data; image sources are distinct; geographic points are grounded; SVG has no interactive native handlers.
6. **Verify server-backed data:** Exact current ref declarations, no fabricated URLs or IDs, source fields bound at every occurrence, correct source types.
7. **Stream:** Incomplete syntax withheld; local state works once a control is complete, host actions/effects/AppBlock scripts wait for whole-message completion; malformed final output gives diagnostic/source fallback.
8. **Accessible and responsive:** Small viewport, focus semantics, readable control names, contrast, no clipped labels or unintentionally huge headings.
9. **Action integrity:** Host permissions checked; direct event dispatch only; success messages derive from host result rather than visual feedback alone.
10. **Runtime conformance:** Tests must run in the actual renderer. Well-formed authored source by itself does not prove compatibility, exact typography, animation timing, or pixel-identical appearance.

The examples teach construction patterns, not a fixed library of prebuilt applications. For substantial domain logic (for example a complete legal chess game), use a validated domain implementation alongside the UI grammar. Chess must handle legal moves, check, castling, en passant, promotion, game-over conditions, and history; a decorative 64-square grid with piece glyphs is not sufficient to call a working chess game.
