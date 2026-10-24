// One release-owned catalog supplies compiler metadata, editor registration and
// the agent reference. --check compares bytes without modifying the worktree.
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { spawnSync } from "node:child_process";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const check = process.argv.includes("--check");
const catalog = JSON.parse(
  readFileSync(
    resolve(root, "server/internal/wiki/intelligent_catalog.json"),
    "utf8",
  ),
);
// Only inline documents are offered for new authoring. Native validation retains
// the existing shapes for reference/capture integrity checks.
const activeCatalog = catalog.filter((entry) => entry.nodeType === "uiDocument");
function defaultMatchesRule(value, rule) {
  const string = typeof value === "string";
  const bytes = string ? Buffer.byteLength(value, "utf8") : 0;
  const safe =
    string && !["__proto__", "prototype", "constructor"].includes(value);
  switch (rule.type) {
    case "string":
      return string && bytes >= rule.min && bytes <= rule.max;
    case "name":
      return safe && bytes <= 64 && /^[A-Za-z_][A-Za-z_0-9]*$/.test(value);
    case "field":
      return safe && bytes > 0 && bytes <= 128;
    case "enum":
      return string && rule.values.includes(value);
    case "scalar":
      return (
        value === null ||
        typeof value === "boolean" ||
        (typeof value === "number" && Number.isFinite(value)) ||
        (string && bytes <= 4096)
      );
    case "uuid":
      return (
        string &&
        /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
          value,
        ) &&
        value !== "00000000-0000-0000-0000-000000000000"
      );
    case "expression":
      // Full expression validation is exercised by the server catalog-default check.
      return (
        value &&
        typeof value === "object" &&
        !Array.isArray(value) &&
        typeof value.op === "string"
      );
    case "nameMap":
      return (
        value &&
        typeof value === "object" &&
        !Array.isArray(value) &&
        Object.keys(value).length >= rule.min &&
        Object.keys(value).length <= rule.max &&
        Object.entries(value).every(
          ([key, item]) =>
            defaultMatchesRule(key, { type: "name" }) &&
            defaultMatchesRule(item, rule.entry),
        )
      );
    case "httpsURL":
      if (
        !string ||
        bytes > rule.max ||
        !value.startsWith("https://") ||
        /[\u0000-\u0020]/.test(value)
      )
        return false;
      try {
        const url = new URL(value);
        return !!url.hostname && !url.username && !url.password;
      } catch {
        return false;
      }
    case "variant":
      return (
        value &&
        typeof value === "object" &&
        !Array.isArray(value) &&
        Object.hasOwn(rule.variants, value[rule.discriminator]) &&
        defaultMatchesRule(value, rule.variants[value[rule.discriminator]])
      );
    case "object":
      return (
        value &&
        typeof value === "object" &&
        !Array.isArray(value) &&
        Object.keys(value).every((key) =>
          rule.fields.some((field) => field.property === key),
        ) &&
        rule.fields.every(
          (field) =>
            (!field.required && !(field.property in value)) ||
            defaultMatchesRule(value[field.property], field),
        )
      );
    case "objectArray":
      return (
        Array.isArray(value) &&
        value.length >= rule.min &&
        value.length <= rule.max &&
        value.every((item) =>
          defaultMatchesRule(item, { type: "object", fields: rule.fields }),
        ) &&
        (!rule.uniqueBy ||
          new Set(value.map((item) => item[rule.uniqueBy])).size ===
            value.length)
      );
    case "stringArray":
      return (
        Array.isArray(value) &&
        value.length >= rule.min &&
        value.length <= rule.max &&
        value.every(
          (item) =>
            typeof item === "string" &&
            Buffer.byteLength(item, "utf8") <= rule.itemMaxBytes,
        ) &&
        (!rule.unique || new Set(value).size === value.length)
      );
    case "number":
      return typeof value === "number" && Number.isFinite(value);
    case "positiveNumber":
      return typeof value === "number" && Number.isFinite(value) && value > 0;
    case "integer":
      return Number.isInteger(value) && value >= rule.min && value <= rule.max;
    default:
      return false;
  }
}
function validateRule(rule, properties, owner, depth = 0) {
  if (depth > 8) throw new Error(`Rule nesting exceeds limit: ${owner}`);
  if (
    !properties.includes(rule.property) ||
    !/^[a-z][A-Za-z]*$/.test(rule.property) ||
    ![
      "string",
      "name",
      "field",
      "enum",
      "integer",
      "number",
      "positiveNumber",
      "stringArray",
      "objectArray",
      "object",
      "nameMap",
      "scalar",
      "expression",
      "uuid",
      "variant",
      "httpsURL",
    ].includes(rule.type) ||
    typeof rule.required !== "boolean" ||
    !rule.code
  )
    throw new Error(`Invalid property rule: ${owner}`);
  if (
    ["integer", "string", "stringArray", "objectArray", "nameMap"].includes(
      rule.type,
    ) &&
    (!Number.isInteger(rule.min) ||
      !Number.isInteger(rule.max) ||
      rule.min > rule.max)
  )
    throw new Error(`Invalid property range: ${owner}`);
  if (
    rule.type === "stringArray" &&
    (!Number.isInteger(rule.itemMaxBytes) ||
      rule.itemMaxBytes < 0 ||
      typeof rule.unique !== "boolean")
  )
    throw new Error(`Invalid string array rule: ${owner}`);
  if (
    rule.type === "enum" &&
    (!rule.values?.length ||
      rule.values.some((value) => typeof value !== "string"))
  )
    throw new Error(`Invalid enum: ${owner}`);
  if (rule.type === "httpsURL" && (!Number.isInteger(rule.max) || rule.max < 1))
    throw new Error(`Invalid URL rule: ${owner}`);
  if (rule.type === "variant") {
    if (
      !/^[a-z][A-Za-z]*$/.test(rule.discriminator) ||
      !rule.variantCode ||
      !rule.variants ||
      Array.isArray(rule.variants) ||
      typeof rule.variants !== "object" ||
      !Object.keys(rule.variants).length ||
      Object.keys(rule.variants).length > 32
    )
      throw new Error(`Invalid variant rule: ${owner}`);
    for (const [tag, variant] of Object.entries(rule.variants)) {
      if (
        !/^[a-z][A-Za-z]*$/.test(tag) ||
        !["object", "variant"].includes(variant.type)
      )
        throw new Error(`Invalid variant case: ${owner}`);
      validateRule(variant, ["value"], owner, depth + 1);
    }
  }
  if (rule.type === "nameMap") {
    if (!rule.keyCode || !rule.entry || rule.min < 0 || rule.max > 1000)
      throw new Error(`Invalid named map rule: ${owner}`);
    validateRule(rule.entry, ["value"], owner, depth + 1);
  }
  if (rule.type === "objectArray" || rule.type === "object") {
    if (
      !Array.isArray(rule.fields) ||
      !rule.fields.length ||
      !rule.itemCode ||
      new Set(rule.fields.map((field) => field.property)).size !==
        rule.fields.length ||
      (rule.uniqueBy &&
        !rule.fields.some(
          (field) =>
            field.property === rule.uniqueBy &&
            field.required &&
            ["field", "name", "string", "enum"].includes(field.type),
        ))
    )
      throw new Error(`Invalid object array rule: ${owner}`);
    for (const field of rule.fields)
      validateRule(
        field,
        rule.fields.map((value) => value.property),
        owner,
        depth + 1,
      );
  }
}

function constraintMatchesDefaults(attrs, constraint) {
  if (constraint.type === "disjointKeys")
    return !Object.keys(attrs[constraint.left] ?? {}).some((key) =>
      Object.hasOwn(attrs[constraint.right] ?? {}, key),
    );
  const left = attrs[constraint.left] ?? constraint.defaults[constraint.left];
  const right =
    attrs[constraint.right] ?? constraint.defaults[constraint.right];
  return (
    left === undefined ||
    right === undefined ||
    (constraint.strict ? left < right : left <= right)
  );
}
const names = new Set();
const nodes = new Set();
for (const entry of catalog) {
  if (
    names.has(entry.name) ||
    nodes.has(entry.nodeType) ||
    !/^[A-Z][A-Za-z]+$/.test(entry.name) ||
    !/^ui[A-Z][A-Za-z]+$/.test(entry.nodeType)
  )
    throw new Error("Invalid or duplicate component identity");
  names.add(entry.name);
  nodes.add(entry.nodeType);
  if (
    !["block", "container", "inline"].includes(entry.placement) ||
    entry.children !== (entry.placement === "container") ||
    entry.version !== 1
  )
    throw new Error(`Invalid placement or version: ${entry.name}`);
  if (
    new Set(entry.properties).size !== entry.properties.length ||
    entry.properties.some((property) => !/^[a-z][A-Za-z]*$/.test(property)) ||
    entry.expressions.some((property) => !entry.properties.includes(property))
  )
    throw new Error(`Invalid properties: ${entry.name}`);
  for (const rule of entry.propertyRules)
    validateRule(rule, entry.properties, entry.name);
  if (
    new Set(entry.propertyRules.map((rule) => rule.property)).size !==
    entry.propertyRules.length
  )
    throw new Error(`Duplicate property rule: ${entry.name}`);
  for (const rule of entry.propertyRules) {
    if (!defaultMatchesRule(entry.editorDefaults[rule.property], rule))
      throw new Error(`Invalid editor default: ${entry.name}.${rule.property}`);
  }
  if (
    Object.keys(entry.editorDefaults).sort().join() !==
    [...entry.properties].sort().join()
  )
    throw new Error(
      `Editor defaults differ from allowed properties: ${entry.name}`,
    );
  if (!Array.isArray(entry.constraints))
    throw new Error(`Missing component constraints: ${entry.name}`);
  for (const constraint of entry.constraints) {
    const left = entry.propertyRules.find(
      (rule) => rule.property === constraint.left,
    );
    const right = entry.propertyRules.find(
      (rule) => rule.property === constraint.right,
    );
    if (
      !left ||
      !right ||
      constraint.left === constraint.right ||
      !["numberOrder", "disjointKeys"].includes(constraint.type) ||
      typeof constraint.strict !== "boolean" ||
      !constraint.code ||
      !constraint.repair ||
      !constraint.defaults ||
      typeof constraint.defaults !== "object" ||
      Array.isArray(constraint.defaults)
    )
      throw new Error(`Invalid component constraint: ${entry.name}`);
    if (
      constraint.type === "disjointKeys" &&
      (left.type !== "nameMap" ||
        right.type !== "nameMap" ||
        Object.keys(constraint.defaults).length ||
        constraint.strict)
    )
      throw new Error(`Invalid disjoint-map constraint: ${entry.name}`);
    if (
      constraint.type === "numberOrder" &&
      ![left, right].every((rule) =>
        ["number", "positiveNumber", "integer"].includes(rule.type),
      )
    )
      throw new Error(`Invalid numeric-order constraint: ${entry.name}`);
    for (const [key, value] of Object.entries(constraint.defaults)) {
      if (
        ![constraint.left, constraint.right].includes(key) ||
        !Number.isFinite(value) ||
        !defaultMatchesRule(value, key === constraint.left ? left : right)
      )
        throw new Error(`Invalid constraint default: ${entry.name}`);
    }
    if (!constraintMatchesDefaults(entry.editorDefaults, constraint))
      throw new Error(
        `Editor defaults violate component constraint: ${entry.name}`,
      );
  }
  const authoring = entry.authoring;
  const text = (value) => typeof value === "string" && value.trim().length > 0;
  if (
    !authoring ||
    !authoring.constraints ||
    Array.isArray(authoring.constraints) ||
    typeof authoring.constraints !== "object" ||
    !text(authoring.fallback) ||
    !text(authoring.capture) ||
    ["events", "accessibility", "limits"].some(
      (key) =>
        !Array.isArray(authoring[key]) ||
        authoring[key].some((value) => !text(value)),
    ) ||
    !authoring.accessibility.length ||
    !authoring.limits.length
  )
    throw new Error(`Incomplete authoring contract: ${entry.name}`);
  for (const [property, description] of Object.entries(authoring.constraints)) {
    if (!entry.properties.includes(property) || !text(description))
      throw new Error(
        `Invalid authoring constraint: ${entry.name}.${property}`,
      );
  }
  for (const property of entry.properties) {
    if (
      !entry.propertyRules.some((rule) => rule.property === property) &&
      !entry.expressions.includes(property)
    )
      throw new Error(
        `Property has no executable rule or expression slot: ${entry.name}.${property}`,
      );
  }
}
function format(command, args, input) {
  const result = spawnSync(command, args, {
    input,
    encoding: "utf8",
    cwd: root,
  });
  if (result.error || result.status !== 0)
    throw result.error ?? new Error(result.stderr);
  return result.stdout;
}
function emit(path, content) {
  const target = resolve(root, path);
  if (check) {
    let existing;
    try {
      existing = readFileSync(target, "utf8");
    } catch {
      /* report missing output as drift */
    }
    if (existing !== content)
      throw new Error(`${path} is stale; run make intelligent-ui-generate`);
  } else writeFileSync(target, content);
}
const strings = (values) =>
  values.length ? `[]string{${values.map(JSON.stringify).join(", ")}}` : "nil";
const variantsLiteral = (variants) =>
  variants
    ? `map[string]UIPropertyRule{${Object.entries(variants)
        .map(([tag, rule]) => `${JSON.stringify(tag)}: ${ruleLiteral(rule)}`)
        .join(", ")}}`
    : "nil";
const ruleLiteral = (rule) =>
  `UIPropertyRule{Property: ${JSON.stringify(rule.property)}, Type: ${JSON.stringify(rule.type)}, Code: ${JSON.stringify(rule.code)}, Required: ${rule.required}, Min: ${rule.min ?? 0}, Max: ${rule.max ?? 0}, ItemMaxBytes: ${rule.itemMaxBytes ?? 0}, Unique: ${rule.unique ?? false}, UniqueBy: ${JSON.stringify(rule.uniqueBy ?? "")}, ItemCode: ${JSON.stringify(rule.itemCode ?? "")}, Fields: ${rules(rule.fields ?? [])}, KeyCode: ${JSON.stringify(rule.keyCode ?? "")}, Entry: ${rule.entry ? "&" + ruleLiteral(rule.entry) : "nil"}, Discriminator: ${JSON.stringify(rule.discriminator ?? "")}, VariantCode: ${JSON.stringify(rule.variantCode ?? "")}, Variants: ${variantsLiteral(rule.variants)}, Values: ${strings(rule.values ?? [])}}`;
const rules = (values) =>
  values.length
    ? `[]UIPropertyRule{${values.map(ruleLiteral).join(", ")}}`
    : "nil";
const authoringLiteral = (entry) => {
  const value = entry.authoring;
  const constraints = Object.entries(value.constraints)
    .map(
      ([key, description]) =>
        `${JSON.stringify(key)}: ${JSON.stringify(description)}`,
    )
    .join(", ");
  return `UIAuthoring{Constraints: map[string]string{${constraints}}, Events: ${strings(value.events)}, Accessibility: ${strings(value.accessibility)}, Fallback: ${JSON.stringify(value.fallback)}, Capture: ${JSON.stringify(value.capture)}, Limits: ${strings(value.limits)}}`;
};
const constraintsLiteral = (constraints) =>
  constraints.length
    ? `[]UIConstraint{${constraints
        .map(
          (rule) =>
            `{Type: ${JSON.stringify(rule.type)}, Left: ${JSON.stringify(rule.left)}, Right: ${JSON.stringify(rule.right)}, Code: ${JSON.stringify(rule.code)}, Repair: ${JSON.stringify(rule.repair)}, Strict: ${rule.strict}, Defaults: map[string]float64{${Object.entries(
              rule.defaults,
            )
              .map(([key, value]) => `${JSON.stringify(key)}: ${value}`)
              .join(", ")}}}`,
        )
        .join(", ")}}`
    : "nil";
const componentLiteral = (entry) =>
  `UIComponent{Name: ${JSON.stringify(entry.name)}, NodeType: ${JSON.stringify(entry.nodeType)}, Placement: ${JSON.stringify(entry.placement)}, Children: ${entry.children}, Properties: ${strings(entry.properties)}, Expressions: ${strings(entry.expressions)}, Version: ${entry.version}, Rules: ${rules(entry.propertyRules)}, Constraints: ${constraintsLiteral(entry.constraints)}}`;
const go = `// Code generated by scripts/generate-intelligent-ui.mjs; DO NOT EDIT.
package wiki

// UICatalog returns independent release metadata so callers cannot mutate validation.
func UICatalog() []UIComponent {
 components := make([]UIComponent, 0, ${activeCatalog.length})
 for _, nodeType := range ${strings(activeCatalog.map((entry) => entry.nodeType))} {
  component, _ := uiComponent(nodeType)
  component.Authoring = uiAuthoring(nodeType)
  components = append(components, component)
 }
 return components
}

// Authoring guidance is allocated only for catalog consumers, not node validation.
func uiAuthoring(nodeType string) UIAuthoring {
 switch nodeType {
 ${catalog.map((entry) => `case ${JSON.stringify(entry.nodeType)}: return ${authoringLiteral(entry)}`).join("\n")}
 default: return UIAuthoring{}
 }
}

// Node validation allocates only the requested entry, never the whole catalog.
func uiComponent(nodeType string) (UIComponent, bool) {
 switch nodeType {
 ${catalog.map((entry) => `case ${JSON.stringify(entry.nodeType)}: return ${componentLiteral(entry)}, true`).join("\n")}
 default: return UIComponent{}, false
 }
}

func uiSourceComponent(name string) (UIComponent, bool) {
 switch name {
 ${catalog.map((entry) => `case ${JSON.stringify(entry.name)}: return uiComponent(${JSON.stringify(entry.nodeType)})`).join("\n")}
 default: return UIComponent{}, false
 }
}
`;

emit(
  "server/internal/wiki/intelligent_catalog_generated.go",
  format("gofmt", [], go),
);
const definitions = Object.fromEntries(
  activeCatalog.map((entry) => [
    entry.nodeType,
    {
      children: entry.children,
      inline: entry.placement === "inline",
      version: entry.version,
      attrs: entry.editorDefaults,
      expressions: entry.expressions,
      rules: entry.propertyRules,
      constraints: entry.constraints,
      authoring: entry.authoring,
    },
  ]),
);
const ts = `// Code generated by scripts/generate-intelligent-ui.mjs; DO NOT EDIT.
export type IntelligentPropertyRule = {property: string; type: string; code: string; required: boolean; min?: number; max?: number; itemMaxBytes?: number; unique?: boolean; uniqueBy?: string; itemCode?: string; fields?: readonly IntelligentPropertyRule[]; keyCode?: string; entry?: IntelligentPropertyRule; discriminator?: string; variantCode?: string; variants?: Readonly<Record<string, IntelligentPropertyRule>>; values?: readonly string[]};
export const intelligentDefinitions: Record<string, { children: boolean; inline: boolean; version: number; attrs: Record<string, unknown>; expressions: readonly string[]; rules: readonly IntelligentPropertyRule[]; constraints: readonly {type: string; left: string; right: string; code: string; repair: string; strict: boolean; defaults: Readonly<Record<string, number>>}[]; authoring: {constraints: Record<string, string>; events: readonly string[]; accessibility: readonly string[]; fallback: string; capture: string; limits: readonly string[]} }> = ${JSON.stringify(definitions, null, 2)};
`;
emit(
  "frontend/src/features/project-canvas/documents/intelligent-ui/catalog.generated.ts",
  format(
    resolve(root, "frontend/node_modules/.bin/prettier"),
    ["--parser", "typescript"],
    ts,
  ),
);
function ruleRows(component, rules, prefix = "") {
  return rules.flatMap((rule) => {
    const property = prefix + rule.property;
    let constraint = rule.type;
    if (rule.values) constraint += ": " + rule.values.join(", ");
    else if (rule.min !== undefined)
      constraint += ": " + rule.min + "–" + rule.max;
    if (rule.type === "stringArray")
      constraint +=
        "; each ≤ " +
        rule.itemMaxBytes +
        " UTF-8 bytes; " +
        (rule.unique ? "unique" : "duplicates allowed");
    if (rule.type === "variant")
      constraint +=
        "; " +
        rule.discriminator +
        " selects " +
        Object.keys(rule.variants).join(", ");
    if (rule.type === "httpsURL")
      constraint += "; at most " + rule.max + " UTF-8 bytes";
    if (rule.type === "objectArray")
      constraint +=
        "; closed objects" + (rule.uniqueBy ? "; unique " + rule.uniqueBy : "");
    return [
      `| ${component} | ${property} | ${rule.required ? "yes" : "no"} | ${constraint} | ${rule.code}${rule.variantCode ? " / " + rule.variantCode + " (tag)" : ""}${rule.itemCode ? " / " + rule.itemCode + " (item)" : ""} |`,
      ...ruleRows(
        component,
        rule.fields ?? [],
        property + (rule.type === "objectArray" ? "[]." : "."),
      ),
      ...Object.entries(rule.variants ?? {}).flatMap(([tag, variant]) =>
        ruleRows(component, [
          {
            ...variant,
            property: property + "[" + rule.discriminator + "=" + tag + "]",
          },
        ]),
      ),
      ...(rule.entry
        ? ruleRows(component, [
            { ...rule.entry, property: property + "[name]" },
          ])
        : []),
    ];
  });
}
const markdown = `<!-- Generated by scripts/generate-intelligent-ui.mjs; DO NOT EDIT. -->
# Inline document storage catalog

Language 1, catalog 1. This describes native Page storage, not wrapper tags to
emit in chat. Agents author raw inline components using the zenith-intelligent-ui SKILL.md.
The original Scope/bind/action primitives are retired from the authoring catalog.

| Native type | Properties | Placement |
| --- | --- | --- |
${activeCatalog.map((entry) => `| ${entry.nodeType} | ${entry.properties.join(", ")} | ${entry.placement} |`).join("\n")}

Source is stored in native text children, preserving collaborative character identities.
Incomplete source can be saved as a draft. Proposals and publication require complete
source accepted by the same token-aware parser used by the browser. Source contains declarations as AST
data; the server never executes authored JavaScript. Only the browser's bounded
interpreter evaluates supported expressions.

| Component | Property | Required | Constraint | Diagnostic |
| --- | --- | --- | --- | --- |
${activeCatalog.flatMap((entry) => ruleRows(entry.name, entry.propertyRules)).join("\n")}

Local form state is temporary. Read-only renderers disable callbacks. Grounded
resources require the current host's authorized sources. Unsupported capabilities
remain explicit rather than inventing evidence or implying successful actions.

## Native editor defaults

${activeCatalog.map((entry) => `### ${entry.name}\n\n\`\`\`json\n${JSON.stringify(entry.editorDefaults, null, 2)}\n\`\`\`\n`).join("\n")}
`;

emit(
  "docs/intelligent-ui/storage-catalog.generated.md",
  markdown.trimEnd() + "\n",
);
console.log(
  check
    ? "Intelligent UI catalog outputs match."
    : "Generated Intelligent UI catalog outputs.",
);
