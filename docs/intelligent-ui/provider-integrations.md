# Provider integrations (not connected in Zenith)

Zenith does not connect these providers today. **Do not author anything in this
file in a Zenith response.** It preserves the `final_skill.md` contract so the
renderer and guide can adopt each provider unchanged when a host declares it.
Until then the renderer shows these components as unavailable, which is visible
noise in an answer. Use the alternatives in [SKILL.md](../../server/internal/mcp/tools/builtin/zenith-intelligent-ui/SKILL.md): resolved
HTTPS assets in `AsyncImage src`, grounded links and addresses in prose, tables
for comparisons, and Zenith task/Page entities.

## AsyncImage query and ref

Props: `alt,aspectRatio,fallback,fit,frame,group,height,maxHeight,maxWidth,minHeight,minWidth,mode,query,radius,ref,src,width`.

Supply exactly **one** of `query`, `ref`, `src`:

- `<AsyncImage query="descriptive subject" />` performs a host-side image lookup. The query must be a literal or an exact authorized model-data path, not computed from state or loop variables.
- `<AsyncImage ref="turn0product0" />` requires a real supported image/product/business/news ref.
- `src` takes an **unchanged HTTP(S) image URL obtained from an authorized model-data binding**. Do not construct, alter or guess the URL. Do not combine `src` with `query` or `ref`.
- Each server-backed image occurrence is resolved independently **once per authored occurrence**, not once per iteration. Different items need separately authored images and appropriate matching refs. Use `group` only when repeated imagery is justified.

### AsyncImageGroup and AsyncVideo

`AsyncImageGroup`: `aspectRatio,fallback,layout,mode,numPerQuery,query,ref,refs,size`. Use it when multiple images independently add value, not as decoration. `AsyncVideo` accepts `ref` from a real supported result.

## MapWidgetV2

Props: `answer,arrive_by,carousel,depart_at,fill,groups,groups_title,minHeight,narrative,optimize_stop_order,origin,points,show_route,title,travel_mode`.

Use a map for explicit map requests even with 1–2 places, 3+ geolocatable recommendations in the same area, or practical multi-stop routes. For travel-time/ETA, use `show_route={true}` and `carousel={{visibility:"auto"}}`. For comparisons also shown in separate cards/tables, use `carousel={{visibility:"hidden"}}`. Only `auto`/`hidden` are supported visibility overrides. If a map-side comparison is desired, retain its carousel rather than duplicating cards elsewhere. Put the map near the top when map inspection is central.

`points` contains 1–25 supported locators: `{ref:"turnNbusinessM"}` or `{id:"a",address:"Grounded address",name:"Place"}` or `{id:"a",lat:...,long:...,name:"Place"}`. Do not use name-only guesses or source/news refs as point locators. Coordinates and addresses must be evidenced. A point may include category and relevant `icon`. Do not invent geographic data, route metrics, prices, opening hours, ratings, availability or visit times.

`groups` organizes routes/areas/days with ordered `locations:[{point_id:"a",notes:"...",arrival_time:"..."}]`; link consecutive routes by repeating shared endpoint if the user intends a continuous route. Do not join unrelated breaks/transfers. Choose walking/cycling/driving based on the actual request. Routes may omit point icons for automatic numbering or use appropriate consistent emojis; business recommendations generally omit icons for provider stars unless ratings are unwanted/unavailable. For cost comparisons use equivalent price data across all icons or use category icons instead. Explicit ranking icons require actual ranks for every point. Notes should be short, grounded and useful.

`carousel` may include `known_fields`, `visibility` and `cards` keyed by point ID/ref. Card overrides are limited to `title,label,rating_label,hours_label,image_url` with grounded values. `rating_label` is for ratings, while hours labels distinguish published hours from scheduled visits. `origin="user"` is permitted when supplied user location is an authorized origin; don't duplicate it as a point. Never invent ETA or route duration.

## Model-data bindings, evidence, and source integrity

An external tool must explicitly declare a binding in the **current** context. Only declared `WebRef`, `ProductRef`, and `BusinessRef` fields can be accessed in DIL expressions. Use read-only **literal** member paths, e.g. `turn0search0.url.uri`, `turn0product0.title`; indices only where explicitly declared. Do not compute property names, materialize whole refs, or bind to an undeclared field. Previous answers and historical refs alone are not binding declarations.

Supported structured fields:

```text
WebRef: title?, snippet?, timestamp? (Unix seconds), domain?, url?: {uri}
ProductRef: title?, brand?, description?, displayed_price?, rating?, review_count?, merchant?, product_page?: {uri}
BusinessRef: name?, address?, rating? (opaque object), price_str?, is_open?, phone?, categories?
```

- Products: reuse **the same exact ProductRef** in `<Entity category="product" ref="..." value={...} />`, `<AsyncImage ref="..." />`, and every displayed bound price/title/merchant/link. Prices and titles remain bound; do not copy them to local literals.
- Businesses: reuse the **same BusinessRef** in Entity, image and displayed `.name`/`.address`. Business ratings are opaque; never access a made-up `.value` or `.score` property.
- Web citations link claims to actual sources. For news with declared WebRef, display the bound title and use its bound `url.uri` for article actions. `url.uri` and `product_page.uri` are unchanged URL strings.
- Server-resolved refs/queries should be authored as supported literals or exact declared binding paths. Never derive `AsyncImage.query` from an item inside an iteration. Bind repeated values at each display point rather than embedding a copied name/price in a static array.
- Cite retrieved claims. Do not fabricate citations, file lines, source references or external URLs. Use `Entity` for identifiable works, people, places, businesses or products where appropriate, respecting its exact props and host availability.

An authorized product binding example (illustrative **syntax only**; do not use the placeholder ref without a matching tool declaration):

```jsx
<row gap={3} align="center">
  <AsyncImage ref="turn0product0" width="96px"/>
  <col gap={1}>
    <Entity category="product" ref="turn0product0" value={turn0product0.title}/>
    <text>{turn0product0.displayed_price}</text>
    <Link url={turn0product0.product_page.uri}>Product page</Link>
  </col>
</row>
```

## Server-side resolution constraints


A **server-backed input** is resolved from host/tool data, not evaluated repeatedly as a client-side fetch. Author each occurrence separately. For server-backed props, the input must be a JSON-compatible literal or an **exact authorized model-data binding path** (where that prop supports bindings); it must not depend on a local state value, callback, loop-local variable, expression operator, array map, or template expansion. Keep ref strings from actual returned sources unchanged.

| Component / source field | Safe authoring form | Invalid assumption |
|---|---|---|
| `AsyncImage query` | `<AsyncImage query="single specific subject"/>` | `query={items[i].search}` or `query={queryState}` |
| `AsyncImage ref` | `<AsyncImage ref="turn0product0"/>` after a real returned product ref | Made-up or cross-type refs |
| `AsyncImage src` | `<AsyncImage src={turn0item0.image_url}/>` **only if** the exact image URL field is declared as a supported binding | Hardcoded source URL or constructed `"https://" + ...` |
| `AsyncImageGroup query`, `ref`, `refs` | Static query/ref or exact advertised source path | Client-generated query lists |
| `AsyncVideo ref` | Real video/media ref | Query text pretending to be a video ref |
| `Entity query`, `ref`, `value` | Static query/ref and declared field binding where applicable | Loop-derived server search |
| `Cite`, `FileCite`, `FileNavList`, `CodeCite` | Current real evidence reference and actual ranges | Synthetic IDs or guessed lines |
| `MapWidgetV2.points`, `groups` | Grounded literal addresses/coordinates or exact returned business refs | Computed geography from local inputs |
| `Chart content` | JSON-compatible authored data object, matching chart schema | Free-form chart library config |

`AsyncImage.src` specifically accepts unchanged HTTP(S) image URLs from *model-data bindings*; do not swap in a URL scraped into a local string or concatenate URL fragments. A result's reference may be suitable for `AsyncImage.ref` even if no client-readable image URL is declared. Binding declarations only authorize the explicitly advertised fields and indices. Never infer a field from a schema's optional spelling when a particular ref's declaration omits it. Server-backed refs are not available simply because a similar ref appeared in an earlier answer.

For products/businesses, consistently use the same returned reference in `Entity`, `AsyncImage`, and field bindings. For news with returned WebRef, bind the news headline to `.title` and article link to `.url.uri`. For other ordinary webpage search results, cite the source in prose instead of fabricating recommendation cards. Keep citations adjacent to claims. For a source with a numeric timestamp in seconds use `new Date(ref.timestamp * 1000)`; never mistake seconds for milliseconds. Opaque business rating objects are not numbers.

## Nested map schema


The precise nested shape to author is:

```text
points: [
  { ref: "turnNbusinessM", category?: string, icon?: string, label?: string },
  { id: "p1", address: "Grounded full address", name: "Place", category?: string, icon?: string, label?: string },
  { id: "p2", lat: NUMBER, long: NUMBER, name: "Place", category?: string, icon?: string, label?: string }
]
groups: [
  {
    title?: string,
    travel_mode?: "walking" | "cycling" | "driving",
    locations: [{ point_id: "p1", arrival_time?: string, notes?: string }]
  }
]
carousel: {
  visibility?: "auto" | "hidden",
  known_fields?: string[],
  cards?: {
    "p1": { title?: string, label?: string, rating_label?: string, hours_label?: string, image_url?: string }
  }
}
origin: "user" | "grounded origin"
```

The schema represents authored values; never invent evidence to fill them. Business-reference keys in `cards` use their exact ref instead of `id`. `known_fields` filters provider cards; `[]` suppresses provider metadata, not card overrides. `label` should be grounded and concise (2–3 words) without invented ratings or times, and replaces a category label, not ratings. `rating_label` contains ratings only. If actual planned session/visit time is known, `hours_label` may say `"Session 10 AM"`; distinguish this from ordinary business opening hours. Do not infer "open now" from a scheduled session.

**Map inclusion:** use one map when recommending at least three geolocatable points in one area, or when explicitly asked to show a map even for one/two points. For ETA render a route map with `show_route={true}` and `carousel={{visibility:"auto"}}`; never author an invented travel duration. Itineraries with 2+ practical stops ordinarily show a contiguous route. Use `groups` for days/areas/types where helpful. Routed groups do not connect implicitly: repeat the previous group's endpoint at the next group's start when the itinerary should continue. Keep requested breaks/unsupported transfers disconnected.

**Icon precedence, use the first matching rule for *every* point:** routes → emoji for each or no icons for automatic numbers; cost comparison → comparable price/band for every point only if all costs are grounded, otherwise category emoji for all; explicit ranking → true ranks for all only if all known, otherwise category emoji; business recommendations → omit icons for provider stars, unless any rating missing or ratings unwanted, then category emoji for all; other factual plots → relevant emoji for all. Never mix ranking, price and emoji icon schemes in one map; never use a time as a pin icon. Keep icon text at most 8 characters. Avoid repeating place comparisons in separate cards when map carousel already serves the comparison.

Use only grounded location values; never use placeholders in a live map.
