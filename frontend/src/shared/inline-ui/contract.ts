import schema from "./schema.json";

// Shared by the portable parser and browser: no DOM or renderer dependencies.
export function validateProperties(
  name: string,
  props: Record<string, unknown>,
  partial = false,
) {
  const types = schema.types as Record<string, string>;
  const overrides = schema.componentTypes as Record<
    string,
    Record<string, string>
  >;
  const enums = schema.enums as Record<string, unknown[]>;
  const componentEnums = schema.componentEnums as Record<
    string,
    Record<string, unknown[]>
  >;
  for (const [key, value] of Object.entries(props)) {
    if (value === undefined || value === null) continue;
    const type = overrides[name]?.[key] ?? types[key];
    const actual = Array.isArray(value) ? "array" : typeof value;
    if (
      type &&
      !type
        .split("|")
        .some((t) =>
          t === "callback"
            ? !!value &&
              typeof value === "object" &&
              typeof (value as { invoke?: unknown }).invoke === "function"
            : t === actual,
        )
    )
      throw new Error(`${name}.${key} requires ${type}.`);
    if (typeof value === "number" && !Number.isFinite(value))
      throw new Error(`${name}.${key} requires a finite number.`);
    if (
      [
        "width",
        "height",
        "minWidth",
        "maxWidth",
        "minHeight",
        "maxHeight",
        "minSize",
        "maxSize",
        "minChildWidth",
      ].includes(key) &&
      !validDimension(value)
    )
      throw new Error(`${name}.${key} requires a bounded CSS dimension.`);
    if (
      ["padding", "gap", "rowGap", "columnGap", "spacing"].includes(key) &&
      !(typeof value === "number"
        ? value >= 0 && value <= 24
        : typeof value === "string" &&
          (value === "0" ||
            /^\d{1,4}(?:\.\d{1,2})?(?:px|pt|rem|em|%)$/.test(value)))
    )
      throw new Error(
        `${name}.${key} requires spacing units (0–24) or a CSS length.`,
      );
    if (
      key === "size" &&
      !["xs", "sm", "md", "lg", "xl", "2xl", "3xl"].includes(String(value)) &&
      !validDimension(value)
    )
      throw new Error(`${name}.size requires a size token or dimension.`);
    if (
      key === "weight" &&
      !(typeof value === "number"
        ? value >= 100 && value <= 900
        : ["normal", "medium", "semibold", "bold"].includes(String(value)))
    )
      throw new Error(`${name}.weight requires a font weight.`);
    const choices = componentEnums[name]?.[key] ?? enums[key];
    if (choices && !choices.includes(value))
      throw new Error(`${name}.${key} must be ${choices.join(" | ")}.`);
  }
  for (const [component, primary, alias] of [
    ["input", "type", "inputType"],
    ["Link", "href", "url"],
  ]) {
    if (
      name === component &&
      props[primary] != null &&
      props[alias] != null &&
      props[primary] !== props[alias]
    )
      throw new Error(
        `${name}.${primary} and ${alias} must agree when both are supplied.`,
      );
  }
  if (name === "Chart" && !partial) validateChart(props.content);
  if (
    name === "input" &&
    props.mask &&
    (props.type ?? props.inputType) === "number"
  )
    throw new Error("Input masks require a text input.");
  if (name === "table" && props.columnSizing !== undefined) {
    const sizing = props.columnSizing;
    if (typeof sizing === "string" && !["auto", "fixed"].includes(sizing))
      throw new Error(
        "table.columnSizing must be auto, fixed or column widths.",
      );
    if (sizing && typeof sizing === "object")
      for (const [key, width] of Object.entries(sizing)) {
        if (!/^\d+$/.test(key) || Number(key) > 99 || !validDimension(width))
          throw new Error(
            "table.columnSizing uses zero-based indices and valid widths.",
          );
      }
  }
  if (name === "WritingBlock" && Array.isArray(props.options)) {
    if (!props.options.length || props.options.length > 20)
      throw new Error("WritingBlock.options requires 1–20 drafts.");
    for (const option of props.options) {
      if (
        !option ||
        typeof option !== "object" ||
        typeof option.label !== "string" ||
        typeof option.content !== "string" ||
        option.content.length > 65536
      )
        throw new Error(
          "WritingBlock.options requires {label, content} drafts.",
        );
    }
  } else if (name === "WritingBlock" && props.options) {
    for (const [key, value] of Object.entries(props.options)) {
      if (
        key === "rows"
          ? typeof value !== "number" ||
            !Number.isInteger(value) ||
            value < 3 ||
            value > 30
          : !["readOnly", "showCopyButton"].includes(key) ||
            typeof value !== "boolean"
      )
        throw new Error(`Invalid WritingBlock.options.${key}.`);
    }
  }
  if (
    name === "AsyncImage" &&
    !partial &&
    [props.query, props.ref, props.src].filter(
      (v) => v !== undefined && v !== "",
    ).length !== 1
  )
    throw new Error("AsyncImage requires exactly one of query, ref or src.");
  if (
    (name === "select" ||
      name === "segmented-control" ||
      name === "radio-group") &&
    Array.isArray(props.options)
  ) {
    for (const option of props.options) {
      if (
        !option ||
        typeof option !== "object" ||
        !("label" in option) ||
        !("value" in option) ||
        !["string", "number", "boolean"].includes(typeof option.value) ||
        !(
          "label" in option &&
          ["string", "number"].includes(typeof option.label)
        ) ||
        ("disabled" in option && typeof option.disabled !== "boolean")
      )
        throw new Error(
          `${name}.options requires {label, value, disabled?} objects with scalar values.`,
        );
    }
  }
}

export function validateChart(content: unknown) {
  if (!content || typeof content !== "object" || Array.isArray(content))
    throw new Error("Chart.content requires an object.");
  const c = content as Record<string, any>;
  const keys = [
    "chartType",
    "title",
    "xKey",
    "nameKey",
    "valueKey",
    "series",
    "data",
    "xAxisLabel",
    "axisLabel",
    "valueSuffix",
    "yAxisMin",
    "yAxisMax",
    "xAxisScale",
    "layout",
  ];
  for (const key of Object.keys(c))
    if (!keys.includes(key))
      throw new Error(`Unsupported Chart.content property ${key}.`);
  if (!["bar", "line", "area", "scatter", "pie"].includes(c.chartType))
    throw new Error("Chart.chartType must be bar, line, area, scatter or pie.");
  const x = c.xKey ?? c.nameKey;
  if (typeof x !== "string" || !x)
    throw new Error("Chart requires xKey or nameKey.");
  if (!Array.isArray(c.data) || c.data.length > 1000)
    throw new Error("Chart.data requires up to 1000 rows.");
  const series = c.series ?? (c.valueKey ? [{ dataKey: c.valueKey }] : []);
  if (!Array.isArray(series) || !series.length || series.length > 12)
    throw new Error("Chart requires 1–12 numeric series.");
  for (const item of series) {
    if (!item || typeof item !== "object" || typeof item.dataKey !== "string")
      throw new Error("Chart series require dataKey.");
    for (const [key, value] of Object.entries(item)) {
      if (
        ![
          "dataKey",
          "label",
          "color",
          "valuePrefix",
          "valueSuffix",
          "valueFormat",
          "lowerKey",
          "upperKey",
          "lower",
          "upper",
        ].includes(key) ||
        (["lower", "upper"].includes(key)
          ? typeof value !== "number" || !Number.isFinite(value)
          : typeof value !== "string")
      )
        throw new Error(`Unsupported Chart series property ${key}.`);
    }
    if (
      item.valueFormat !== undefined &&
      !["compact", "standard"].includes(item.valueFormat)
    )
      throw new Error("Chart valueFormat must be compact or standard.");
    const hasLower = item.lowerKey !== undefined || item.lower !== undefined;
    const hasUpper = item.upperKey !== undefined || item.upper !== undefined;
    if (hasLower !== hasUpper || (hasLower && c.chartType !== "line"))
      throw new Error(
        "Chart bands require both lower and upper bounds on a line series.",
      );
    if (hasLower)
      for (const row of c.data) {
        const lower =
          item.lowerKey !== undefined ? row?.[item.lowerKey] : item.lower;
        const upper =
          item.upperKey !== undefined ? row?.[item.upperKey] : item.upper;
        if (
          typeof lower !== "number" ||
          typeof upper !== "number" ||
          !Number.isFinite(lower) ||
          !Number.isFinite(upper) ||
          lower > upper
        )
          throw new Error("Chart band bounds must be finite ordered numbers.");
      }
    for (const row of c.data)
      if (
        !row ||
        !["string", "number"].includes(typeof row[x]) ||
        typeof row[item.dataKey] !== "number" ||
        !Number.isFinite(row[item.dataKey]) ||
        (c.chartType === "pie" && row[item.dataKey] < 0)
      )
        throw new Error(
          "Chart rows require an x value and finite numeric series values.",
        );
  }
  for (const key of ["yAxisMin", "yAxisMax"])
    if (
      c[key] !== undefined &&
      (typeof c[key] !== "number" || !Number.isFinite(c[key]))
    )
      throw new Error(`Chart.${key} requires a finite number.`);
  if (
    c.yAxisMin !== undefined &&
    c.yAxisMax !== undefined &&
    c.yAxisMin >= c.yAxisMax
  )
    throw new Error("Chart.yAxisMin must be below yAxisMax.");
  if (
    c.xAxisScale !== undefined &&
    !["linear", "category"].includes(c.xAxisScale)
  )
    throw new Error("Chart.xAxisScale must be linear or category.");
  if (
    c.xAxisScale === "linear" &&
    (c.chartType !== "line" ||
      c.data.some(
        (row: Record<string, unknown>) =>
          typeof row[x] !== "number" || !Number.isFinite(row[x]),
      ))
  )
    throw new Error(
      "Chart linear X axes require a line chart with numeric X values.",
    );
  if (c.layout !== undefined && !["horizontal", "vertical"].includes(c.layout))
    throw new Error("Chart.layout must be horizontal or vertical.");
}

function validDimension(value: unknown): boolean {
  return typeof value === "number"
    ? Number.isFinite(value) && value >= 0 && value <= 2000
    : typeof value === "string" &&
        /^(?:auto|fit-content|\d{1,4}(?:\.\d{1,2})?(?:px|pt|rem|em|%|vh|vw))$/.test(
          value,
        );
}
