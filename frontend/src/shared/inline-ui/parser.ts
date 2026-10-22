import { parse, parseExpressionAt, tokenizer, type Node } from "acorn";

export type Expression = Node;
export type InlineNode =
  | { type: "text"; value: string; start: number }
  | { type: "expression"; expression: Expression; start: number }
  | { type: "body" | "module"; program: Node; start: number }
  | {
      type: "element";
      name: string;
      props: Record<string, Expression | string | boolean>;
      children: InlineNode[];
      start: number;
      complete: boolean;
    }
  | {
      type: "if";
      expression: Expression;
      children: InlineNode[];
      otherwise: InlineNode[];
      start: number;
    }
  | {
      type: "each";
      expression: Expression;
      item: string;
      index?: string;
      children: InlineNode[];
      start: number;
    };
export type InlineDocument = {
  nodes: InlineNode[];
  complete: boolean;
  diagnostic?: string;
};

export const componentNames = new Set([
  "badge",
  "bold",
  "caption",
  "code",
  "italic",
  "label",
  "math",
  "strikethrough",
  "text",
  "title",
  "underline",
  "favicon",
  "icon",
  "svg",
  "blockquote",
  "box",
  "card",
  "carousel",
  "carousel-item",
  "carousel-media-item",
  "col",
  "divider",
  "flow",
  "flow-item",
  "grid",
  "grid-item",
  "list",
  "list-item",
  "popover",
  "popover-trigger",
  "popover-content",
  "pressable",
  "row",
  "spacer",
  "table",
  "table-section",
  "table-row",
  "table-cell",
  "button",
  "checkbox",
  "date-picker",
  "form",
  "input",
  "radio-group",
  "radio",
  "segmented-control",
  "select",
  "slider",
  "textarea",
  "AsyncImage",
  "AsyncImageGroup",
  "AsyncVideo",
  "Chart",
  "MapWidgetV2",
  "Cite",
  "CodeCite",
  "Entity",
  "FileCite",
  "FileNavList",
  "FollowUp",
  "MemoryCite",
  "Link",
  "AppBlock",
  "CodeBlock",
  "WritingBlock",
  "escape",
]);
const svgNames = new Set([
  "g",
  "path",
  "circle",
  "ellipse",
  "rect",
  "line",
  "polyline",
  "polygon",
  "defs",
  "linearGradient",
  "radialGradient",
  "stop",
  "clipPath",
  "mask",
  "tspan",
]);
const isName = (c = "") =>
  !!c &&
  "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-:".includes(
    c,
  );

class Incomplete extends Error {}
class Reader {
  at = 0;
  count = 0;
  complete = true;
  constructor(readonly source: string) {}
  space() {
    while (
      this.at < this.source.length &&
      " \t\r\n".includes(this.source[this.at])
    )
      this.at++;
  }
  name() {
    const start = this.at;
    while (isName(this.source[this.at])) this.at++;
    return this.source.slice(start, this.at);
  }
  balanced(): string {
    const start = ++this.at;
    const tokens = tokenizer(this.source.slice(start), {
      ecmaVersion: "latest",
    });
    let depth = 0;
    try {
      for (let count = 0; count < 20000; count++) {
        const token = tokens.getToken();
        const label = token.type.label;
        if (label === "eof") throw new Incomplete();
        if (label === "{" || label === "${") depth++;
        if (label === "}") {
          if (depth === 0) {
            this.at = start + token.end;
            return this.source.slice(start, start + token.start);
          }
          depth--;
        }
      }
      throw new Error("Expression exceeds the token limit.");
    } catch (error) {
      if (error instanceof Incomplete) throw error;
      if (
        this.source.indexOf("}", start) < 0 ||
        (error instanceof SyntaxError &&
          error.message.startsWith("Unterminated"))
      )
        throw new Incomplete();
      throw error;
    }
  }
  expression(source: string): Expression {
    const expression = parseExpressionAt(source, 0, { ecmaVersion: "latest" });
    const tail = tokenizer(source.slice(expression.end), {
      ecmaVersion: "latest",
    }).getToken();
    if (tail.type.label !== "eof")
      throw new Error("Unexpected text after expression.");
    return expression;
  }
  // Code spans and fenced blocks are literal Markdown, never component input.
  codeEnd(): number | undefined {
    const marker = this.source[this.at];
    if (marker !== "`" && marker !== "~") return;
    let length = 0;
    while (this.source[this.at + length] === marker) length++;
    const lineStart = this.source.lastIndexOf("\n", this.at - 1) + 1;
    const fence =
      length >= 3 && this.source.slice(lineStart, this.at).trim() === "";
    if (!fence && marker !== "`") return;
    let end = this.at + length;
    while (end < this.source.length) {
      const next = this.source.indexOf(marker.repeat(length), end);
      if (next < 0) return this.source.length;
      if (
        !fence ||
        this.source
          .slice(this.source.lastIndexOf("\n", next - 1) + 1, next)
          .trim() === ""
      ) {
        let close = next + length;
        while (this.source[close] === marker) close++;
        if (
          !fence ||
          this.source
            .slice(
              close,
              this.source.indexOf("\n", close) < 0
                ? undefined
                : this.source.indexOf("\n", close),
            )
            .trim() === ""
        )
          return close;
      }
      end = next + length;
    }
    return this.source.length;
  }
  conditional(
    depth: number,
    start: number,
    expression: Expression,
    svg: boolean,
  ): InlineNode {
    if (depth > 32) throw new Error("UI nesting exceeds 32 levels.");
    const children = this.nodes(depth + 1, "", "if", svg);
    let otherwise: InlineNode[] = [];
    try {
      if (this.source.startsWith("{:", this.at)) {
        const branchStart = this.at;
        const branch = this.balanced().trim();
        if (branch === ":else") {
          otherwise = this.nodes(depth + 1, "", "if", svg);
        } else if (/^:else\s+if\s/.test(branch)) {
          // Parse the next condition as a nested branch sharing this closing tag.
          otherwise = [
            this.conditional(
              depth + 1,
              branchStart,
              this.expression(branch.replace(/^:else\s+if\s+/, "")),
              svg,
            ),
          ];
          return { type: "if", expression, children, otherwise, start };
        } else {
          throw new Error("Expected {:else} or {:else if condition}.");
        }
      }
      if (this.source.startsWith("{/if}", this.at)) this.at += 5;
      else this.complete = false;
    } catch (error) {
      if (!(error instanceof Incomplete)) throw error;
      this.complete = false;
      this.at = this.source.length;
    }
    return { type: "if", expression, children, otherwise, start };
  }
  nodes(depth = 0, parent?: string, stop?: string, svg = false): InlineNode[] {
    if (depth > 32) throw new Error("UI nesting exceeds 32 levels.");
    const nodes: InlineNode[] = [];
    let textStart = this.at;
    const flush = () => {
      if (this.at > textStart)
        nodes.push({
          type: "text",
          value: this.source.slice(textStart, this.at),
          start: textStart,
        });
    };
    while (this.at < this.source.length) {
      if (++this.count > 262144)
        throw new Error("UI exceeds the rendering budget.");
      if (parent !== undefined && this.source.startsWith("</", this.at)) {
        flush();
        return nodes;
      }
      if (
        stop &&
        (this.source.startsWith(`{/${stop}}`, this.at) ||
          this.source.startsWith("{:", this.at))
      ) {
        flush();
        return nodes;
      }
      const literalEnd = this.codeEnd();
      if (literalEnd !== undefined) {
        this.at = literalEnd;
        continue;
      }
      if (this.source[this.at] === "\\") {
        this.at = Math.min(this.source.length, this.at + 2);
        continue;
      }
      if (this.source.startsWith("<!--", this.at)) {
        flush();
        const end = this.source.indexOf("-->", this.at + 4);
        if (end < 0) {
          this.complete = false;
          this.at = this.source.length;
          return nodes;
        }
        this.at = end + 3;
        textStart = this.at;
        continue;
      }
      const start = this.at;
      if (this.source[this.at] === "<") {
        this.at++;
        const name = this.name();
        this.at = start;
        if (
          start + name.length + 1 === this.source.length &&
          (name === "" ||
            [...componentNames].some((component) => component.startsWith(name)))
        ) {
          flush();
          this.complete = false;
          this.at = this.source.length;
          return nodes;
        }
        if (
          componentNames.has(name) ||
          (svg && svgNames.has(name)) ||
          (name === "" && this.source[start + 1] === ">")
        ) {
          flush();
          try {
            nodes.push(this.element(depth, svg));
          } catch (error) {
            if (!(error instanceof Incomplete)) throw error;
            this.complete = false;
            this.at = this.source.length;
          }
          textStart = this.at;
          continue;
        }
      }
      if (
        this.source[this.at] === "{" &&
        (parent !== undefined ||
          this.source.startsWith("{@", this.at) ||
          this.source.startsWith("{#", this.at))
      ) {
        flush();
        try {
          if (
            this.source.startsWith("{@body", this.at) ||
            this.source.startsWith("{@module", this.at)
          ) {
            const type = this.source.startsWith("{@body", this.at)
              ? "body"
              : "module";
            // Scan the JavaScript block after its directive, retaining token-aware braces.
            this.at += type === "body" ? 5 : 7;
            const script = this.balanced();
            nodes.push({
              type,
              program: parse(script, { ecmaVersion: "latest" }),
              start,
            });
          } else if (this.source.startsWith("{#if", this.at)) {
            this.at += 3;
            nodes.push(
              this.conditional(
                depth,
                start,
                this.expression(this.balanced()),
                svg,
              ),
            );
          } else if (this.source.startsWith("{#each", this.at)) {
            this.at += 5;
            const header = this.balanced();
            const tokens = tokenizer(header, { ecmaVersion: "latest" });
            let split = -1;
            let nesting = 0;
            for (;;) {
              const token = tokens.getToken();
              if (token.type.label === "eof") break;
              if (
                nesting === 0 &&
                header.slice(token.start, token.end) === "as"
              ) {
                split = token.start;
                break;
              }
              if (["(", "[", "{"].includes(token.type.label)) nesting++;
              if ([")", "]", "}"].includes(token.type.label)) nesting--;
            }
            if (split < 0) throw new Error("Each requires an item binding.");
            const expression = this.expression(header.slice(0, split));
            const bindings = header
              .slice(split + 2)
              .split(",")
              .map((v) => v.trim());
            if (
              bindings.length > 2 ||
              bindings.some((v) => !v || [...v].some((c) => !isName(c)))
            )
              throw new Error("Invalid each binding.");
            const children = this.nodes(depth + 1, "", "each", svg);
            if (this.source.startsWith("{/each}", this.at)) this.at += 7;
            else this.complete = false;
            nodes.push({
              type: "each",
              expression,
              item: bindings[0],
              index: bindings[1],
              children,
              start,
            });
          } else
            nodes.push({
              type: "expression",
              expression: this.expression(this.balanced()),
              start,
            });
        } catch (error) {
          if (!(error instanceof Incomplete)) {
            const before = this.source.slice(0, start);
            const line = before.split("\n").length;
            const column = start - before.lastIndexOf("\n");
            throw new Error(
              `Invalid UI directive near line ${line}, column ${column}: ${error instanceof Error ? error.message : "Invalid expression."}`,
            );
          }
          this.complete = false;
          this.at = this.source.length;
        }
        textStart = this.at;
        continue;
      }
      this.at++;
    }
    flush();
    return nodes;
  }
  element(depth: number, svg: boolean): InlineNode {
    const start = this.at++;
    const name = this.name();
    const props: Record<string, Expression | string | boolean> =
      Object.create(null);
    for (;;) {
      this.space();
      if (this.at >= this.source.length) throw new Incomplete();
      if (this.source[this.at] === "/" && this.at + 1 === this.source.length)
        throw new Incomplete();
      if (this.source.startsWith("/>", this.at)) {
        this.at += 2;
        return {
          type: "element",
          name,
          props,
          children: [],
          start,
          complete: true,
        };
      }
      if (this.source[this.at] === ">") {
        this.at++;
        break;
      }
      const key = this.name();
      if (!key || key in props)
        throw new Error("Invalid or duplicate component property.");
      this.space();
      if (this.source[this.at] !== "=") {
        props[key] = true;
        continue;
      }
      this.at++;
      this.space();
      if (this.at >= this.source.length) throw new Incomplete();
      if (this.source[this.at] === "{")
        props[key] = this.expression(this.balanced());
      else if (this.source[this.at] === '"' || this.source[this.at] === "'") {
        const quote = this.source[this.at++];
        const from = this.at;
        while (this.at < this.source.length && this.source[this.at] !== quote) {
          if (this.source[this.at] === "\\") this.at++;
          this.at++;
        }
        if (this.at >= this.source.length) throw new Incomplete();
        props[key] = this.source.slice(from, this.at++);
      } else
        throw new Error(
          "Use quoted strings or expressions for component properties.",
        );
    }
    if (["CodeBlock", "WritingBlock", "AppBlock", "escape"].includes(name)) {
      const end = this.source.indexOf(`</${name}>`, this.at);
      const children: InlineNode[] = [
        {
          type: "text",
          value: this.source.slice(this.at, end < 0 ? undefined : end),
          start: this.at,
        },
      ];
      this.at = end < 0 ? this.source.length : end + name.length + 3;
      if (end < 0) this.complete = false;
      return {
        type: "element",
        name,
        props,
        children,
        start,
        complete: end >= 0,
      };
    }
    const children = this.nodes(
      depth + 1,
      name,
      undefined,
      svg || name === "svg",
    );
    let complete = false;
    if (this.source.startsWith("</", this.at)) {
      const closingStart = this.at;
      this.at += 2;
      const closing = this.name();
      this.space();
      if (this.at === this.source.length) {
        this.complete = false;
        return {
          type: "element",
          name,
          props,
          children,
          start,
          complete: false,
        };
      }
      if (closing !== name || this.source[this.at] !== ">")
        throw new Error(`Expected </${name}> at ${closingStart}.`);
      this.at++;
      complete = true;
    } else this.complete = false;
    return { type: "element", name, props, children, start, complete };
  }
}

export function parseInlineUI(source: string): InlineDocument {
  if (utf8Length(source) > 262144)
    return {
      nodes: [],
      complete: false,
      diagnostic: "This response exceeds the interactive size limit.",
    };
  const reader = new Reader(source);
  try {
    const nodes = reader.nodes();
    return { nodes, complete: reader.complete };
  } catch (error) {
    return {
      nodes: [],
      complete: false,
      diagnostic:
        error instanceof Error ? error.message : "Invalid component source.",
    };
  }
}

// Portable across browsers and the server's isolated parser runtime.
function utf8Length(value: string): number {
  let length = 0;
  for (const character of value) {
    const point = character.codePointAt(0)!;
    length += point < 128 ? 1 : point < 2048 ? 2 : point < 65536 ? 3 : 4;
  }
  return length;
}

export function inspectInlineSource(source: string) {
  const result = parseInlineUI(source);
  return {
    complete: result.complete,
    diagnostic: result.diagnostic ?? "",
    interactive: result.nodes.some((node) => node.type !== "text"),
  };
}
