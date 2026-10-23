# Gold-standard authoring and verification

Historical authoring notes from the earlier guide. The bundled SKILL.md, adapted from final_skill.md, supersedes its vocabulary and capability statements.
Use [renderer-support.md](renderer-support.md) and [schema.generated.json](schema.generated.json) for the implemented API. The [playground](../playground.zenith-ui) is the reference example; verify its behavior using the matrix below.

## 7. Composition and visual design

### Start with the user task

Identify the decision or action the interface should support. Choose a small number of meaningful inputs and make the result visible without requiring the user to explore every control.

A useful default composition is:

1. A clear title and one sentence of context.
2. The primary controls, grouped by purpose.
3. The principal result or current state.
4. Supporting detail that can be expanded or switched into view.
5. An appropriate next action and a truthful scope note.

This is a composition pattern, not a mandatory layout. An editorial explanation, comparison, calculator, and operational dashboard should not all look identical.

### Create hierarchy, not decoration

Use one dominant heading. Group related information with spacing before adding more borders. Use cards for distinct sections, not every individual label or value. Keep nested surfaces shallow.

Make the main result visually prominent with `title` and contextual `text` or `caption`. There is no need to invent a `Metric` component. Use concise labels, explicit units, and meaningful defaults.

Prefer a restrained surface palette. Neutral surfaces can hold most content; one tinted surface can emphasize the current result or main interaction. Reserve `warning` and `danger` for conditions that actually warrant those meanings. Do not communicate status through color alone.

Use a consistent spacing rhythm with supported values such as `gap={2}`, `gap={3}`, `padding={4}`, and `padding={5}`. Keep control groups tighter than the spacing between major sections. Do not simulate layout with repeated whitespace or empty text.

### Use progressive disclosure

Keep primary controls visible. Move explanations, secondary parameters, diagnostic details, and long catalogs behind a clearly named disclosure or an appropriate view selector. A disclosure must actually reveal useful content.

A component catalog belongs in a component playground, not at the bottom of every ordinary answer. Do not add a chart, image, carousel, or icon simply to prove that the component exists.

### Be responsive without inventing APIs

Default to a readable stacked layout. Use wrapping rows for short related controls. Use a multi-column grid only when its content remains readable at the host's available width, or when a verified responsive contract permits a single-column fallback.

Do not assume `columns={2}` becomes responsive automatically. Do not guess `useBreakpoint()` return values, responsive prop objects, CSS media queries, Tailwind classes, or viewport APIs. When the breakpoint contract is unknown, a single-column layout is the safer default.

Avoid fixed heights for text-heavy content. Long names, translations, error messages, and zoomed text must not disappear behind visual assumptions. Verify how the renderer handles overflow before relying on scrolling containers or truncation props.

### Copy and information quality

Use the user's terminology and language. Labels should describe what a control changes; buttons should describe the action performed. Prefer “Create local snapshot” to “Submit” when nothing leaves the view.

Distinguish facts, supplied values, estimates, and illustrative data. Show units and rounding where they affect interpretation. Do not invent precise-looking statistics to make a dashboard feel complete.

## 8. Interaction design and complete behavior

Every visible interactive control should have a real, observable effect or a clearly documented unavailable state. Do not include editable-looking controls with constant values and no-op callbacks.

Bind editable text areas and date pickers to state. Keep labels, summaries, and previews synchronized with the selected values. A layout-style selector must change the preview layout, not only repeat its own selected label.

### Form behavior

Use `form onSubmit` and a contained `button submit` for the documented form pattern. Put unrelated reset or navigation actions outside the form unless their non-submit behavior is explicitly documented.

Validate before producing a result or invoking an authorized action. Preserve the user's inputs on failure and show a specific corrective message. Do not assume native browser validation, a `required` prop, or HTML event behavior exists.

The example in the reference playground checks for an empty name using supported comparisons. It does not claim to perform whitespace normalization, email validation, or date validation. More advanced validation requires the relevant supported operations and a defined data contract.

### Boundaries and feedback

At a numeric boundary, clamp the value and make the allowed range visible. Do not let decrement controls create invalid negative values. Avoid showing an action as available when it cannot have an effect; omit it with an explanation when a documented disabled prop is unavailable.

Show feedback next to the relevant action or result. Do not use a success message for an operation that only changed local state. For external work, report success only after an actual successful host response.

When nothing matches a selection or no data is available, provide an explicit empty state. When a result depends on data that has not been supplied, say what is missing instead of filling the gap with fabricated content.

### Durable versus temporary results

Use distinct language for “live preview,” “local snapshot,” “copied,” “request sent,” and “saved.” These are different outcomes. A local snapshot remains temporary and is not a saved Page or backend record.

Avoid simulated loading spinners or progress bars that suggest a real background process. There are no native timers or background network jobs in this contract. A manual step viewer should be labeled as manual.

## 9. Accessibility and usability

Use visible labels for inputs. A nearby `label` is useful visually, but programmatic association and focus behavior still depend on the renderer's documented semantics. Do not claim accessibility conformance from component names alone.

Use descriptive text on action buttons. Do not rely on an icon, color, hover state, or spatial position as the only explanation of an action. Keep errors understandable in text and retain the entered values.

Maintain a sensible reading order and heading hierarchy. Let related controls wrap or stack. Prefer native controls over custom `pressable` imitations of controls when the native component fits.

Provide text equivalents for information conveyed visually. Use meaningful alternative text through the documented image API. Charts should retain their readable data table. A decorative chart is not a substitute for a written conclusion.

Avoid unsupported motion. When a host-backed visualization adds motion through a separately verified capability, respect its accessibility and reduced-motion contract. Do not assume browser motion preferences are accessible in the native expression environment.

Keyboard interaction, focus visibility, focus restoration, screen-reader names, contrast, zoom behavior, and the accessibility of container components must be verified in the rendered host. Do not invent `aria-*`, focus methods, or tab-index props to fill gaps in an undocumented API.

## 10. Reusable composition recipes

| Interface | State and structure | Important completion check |
| --- | --- | --- |
| Calculator or estimator | A few controlled inputs, derived values, a prominent result, assumptions, and reset. | Bounds, zero denominators, units, rounding, and stale snapshots are handled. |
| Comparison | A selector, consistent fields, and a readable difference summary. | All options use the same definitions and units; missing data is explicit. |
| Local form preview | Controlled fields, validation, an explicit preview action, and a separate snapshot. | No control is a no-op; nothing is described as sent or saved. |
| Small dashboard | Labeled data source, summary values, one relevant chart, and details. | All views derive from the same supplied dataset; illustrative data is labeled. |
| Guided explanation | A bounded step index, current explanation, navigation, and visible progress. | The content actually changes; first and last steps behave deliberately. |
| Filtered list | A small supplied dataset, supported filter operations, result count, empty state, and reset. | The evaluator supports the methods used; counts and rendered rows agree. |
| Component playground | A view selector, working examples, and a clearly labeled inventory. | Demonstrations and catalog-only names are distinguished. |

Use these as starting structures, not templates to copy regardless of the task. Reuse a small visual vocabulary rather than making every section a different design experiment.

## 11. Charts and data presentation

`Chart` accepts structured content. The supplied contract supports bar, line, area, scatter, and pie charts, with a readable data table. It establishes the following line and bar content shapes:

```text
<Chart content={{chartType:"line", xKey:"day", series:[{dataKey:"ms",label:"Latency",valueSuffix:" ms"}], data:[{day:"Mon",ms:320},{day:"Tue",ms:270}]}} />
```

```text
<Chart content={{chartType:"bar", xKey:"day", series:[{dataKey:"views",label:"Views"}], data:[{day:"Mon",views:3},{day:"Tue",views:7},{day:"Wed",views:5}]}} />
```

These are illustrative datasets, not retrieved measurements. Label them accordingly in a user-facing demo.

Every `xKey` and series `dataKey` must match the data objects. Numeric series values must actually be numbers. Supply meaningful labels and units. Do not silently mix currencies, time units, percentages, and raw counts on an unexplained scale.

Choose a chart for the question: lines for an ordered trend, bars for a category comparison, and other supported types only when their data semantics and exact configuration are known. The names `area`, `scatter`, and `pie` do not establish their complete configuration schemas. Do not guess those shapes from the line example.

Keep displayed totals, tables, summaries, and chart data consistent. Use the same source data and supported derivations. When no data exists, render an empty-state explanation rather than a misleading blank chart or invented zero-valued observations.

Do not assume custom axes, tooltips, legends, colors, animation, date parsing, or chart callback props. Use only the renderer's documented options.


## 18. Preflight and interaction verification

### Source preflight

Before delivering a new interface, check the following:

- The output mode matches the request: live markup, literal source, or an authorized Page mutation.
- Every component, prop, enum, hook, and method comes from the active contract or a verified renderer reference.
- All identifiers are declared before use; hooks are unconditional and render-time; module helpers are pure.
- Tags, expressions, directives, strings, and raw-text blocks are complete and balanced.
- Numeric bounds, empty values, missing records, zero denominators, and selection validity are handled.
- Every editable-looking control is connected to state; every action has an observable and accurately labeled effect.
- Repeated content uses the intended explicit item/row structure; the nesting and dataset remain small.
- Data, images, citations, entity IDs, and success claims have the appropriate source or are clearly labeled illustrative.
- External actions occur only through explicit user interactions and available authorized capabilities.
- Local state, local snapshots, saved definitions, and server-side records are not conflated.

### Renderer test matrix for the playground

Run these tests in the actual Zenith host before treating the example as production-validated:

| Test | Expected behavior |
| --- | --- |
| Initial render | Controls view; Khoa; quantity 2; total 50 credits; intensity 40%; Card style; Normal priority; no snapshot. |
| Name edit | Live preview changes immediately; any snapshot clears. |
| Quantity edit | Quantity becomes a whole value in 0–99; total remains quantity × 25. |
| Quantity boundaries | Remove is replaced by a minimum message at 0; Add is replaced by a maximum message at 99. |
| Slider | Intensity and preview text stay synchronized within 0–100. |
| Style selector | Card, Compact, and Editorial produce visibly different layouts. |
| Priority radio group | Priority changes independently without invalidating the style selection. |
| Text area | Note remains editable and appears in the preview and subsequent snapshot. |
| Date picker | Selected value round-trips through the documented date format; clearing behavior is checked against the renderer contract. |
| Preview checkbox | Hides or shows the live preview while preserving form inputs. |
| Form submit | Nonempty name produces a local snapshot; no external request or save is performed. |
| Empty name | Submission shows the corrective message and does not produce a snapshot. This check is not whitespace normalization. |
| Snapshot invalidation | Editing any scenario input clears the previous snapshot. |
| Reset form | Restores all form defaults and clears the attempted-submit state and snapshot. |
| View switching | The correct view is shown; mounted state remains consistent with the host's rendering model. |
| Step viewer | Advances through the three real explanations and returns to the first step deliberately. |
| Inline disclosure | The implementation note opens and closes; it is not treated as a popover. |
| Chart | Bar labels and values match the sample data; its readable table remains available. |
| Raw CodeBlock | Displays the code text rather than interpreting it as an expression or UI. |
| Narrow and zoomed views | Text and controls remain usable; change the sample grid to one column where necessary. |
| Keyboard and assistive technology | Labels, native control semantics, focus behavior, reading order, and chart alternatives are usable in the actual host. |
| Read-only Page | Callbacks are disabled, while the default content and local-scope explanation remain useful. |
| Remount and saved definition | Initial authored defaults are restored as specified; no prior local snapshot is represented as persisted data. |

Static checks do not establish runtime support. Standard JavaScript syntax checks do not prove that a bounded evaluator accepts the same syntax. Report these kinds of verification separately.

## 19. Troubleshooting

| Symptom | Check and repair |
| --- | --- |
| UI appears as code | Remove a Markdown fence only when live UI was requested; otherwise literal source is the correct result. Confirm that the host supports inline markup. |
| Show source or an unsupported-construct diagnostic appears | Inspect the first diagnostic, balance source, and replace unsupported syntax rather than guessing a repair. |
| A variable is undefined | Keep its declaration and consumers in the same source segment; check spelling and scope. |
| An input cannot be edited | Remove constant controlled values and no-op callbacks; bind the actual local value and setter. |
| A selection disappears | Ensure the current value belongs to that control's options; separate unrelated selectors' state. |
| A result does not match the inputs | Derive it from current state or explicitly capture and invalidate a snapshot. |
| A component renders an unavailable state | Check its required host capability and authorized reference; do not fake a substitute reference. |
| A conditional card is being called a popover | Rename it as an inline disclosure or implement the verified native popover API. |
| A next button changes only a counter | Change meaningful displayed content or label the control as a counter, not a carousel. |
| A chart is empty or inconsistent | Check keys, numeric values, data availability, and the exact chart-type schema. |
| A layout overflows | Simplify to one column, permit documented wrapping, shorten labels, and avoid unverified dimensions. |
| A Page save fails | Preserve the proposed source, report the failure, and reread revision/permission information before another authorized edit. |
| The UI claims that data was saved | Verify the successful host response; otherwise change the language to describe only the actual local result. |
