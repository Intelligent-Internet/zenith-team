// Run from the repository root after installing frontend dependencies:
// node server/internal/wiki/testdata/generate-intelligent-yjs.mjs
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { writeFileSync } from "node:fs";
const require = createRequire(
  new URL("../../../../frontend/package.json", import.meta.url),
);
const Y = require("yjs");
let next = 0;
const literal = (value) => ({ op: "literal", value });
const node = (type, attrs = {}, content) => ({
  type,
  attrs: {
    ...(type !== "uiValue"
      ? {
          blockId: `00000000-0000-4000-8000-${String(++next).padStart(12, "0")}`,
        }
      : {}),
    ...attrs,
  },
  ...(content ? { content } : {}),
});
const paragraph = (text) => node("paragraph", {}, [{ type: "text", text }]);
const original = {
  type: "doc",
  content: [
    node(
      "uiScope",
      {
        name: "scenario",
        initial: { quantity: 2, text: "Chào 🌱", selected: "a", enabled: true },
      },
      [
        paragraph("Chào 🌱 team"),
        node("uiStack", { direction: "vertical", gap: "normal" }, [
          paragraph("Stack"),
        ]),
        node("uiGrid", { columns: 2 }, [paragraph("Grid")]),
        node("uiConditional", { when: literal(true) }, [paragraph("Visible")]),
        node(
          "uiRepeat",
          { items: literal([{ id: "one", amount: 2 }]), keyField: "id" },
          [paragraph("Repeated")],
        ),
        node("uiButton", {
          label: "Reset",
          action: { type: "setState", name: "quantity", value: literal(2) },
        }),
        node("uiTextInput", { label: "Name", bind: "text" }),
        node("uiNumberInput", { label: "Quantity", bind: "quantity" }),
        node("uiSelect", {
          label: "Choice",
          bind: "selected",
          options: ["a", "b"],
        }),
        node("uiCheckbox", { label: "Enabled", bind: "enabled" }),
        node("uiSlider", {
          label: "Amount",
          bind: "quantity",
          min: 0,
          max: 10,
          step: 1,
        }),
        node("paragraph", {}, [
          { type: "text", text: "Value: ", marks: [{ type: "bold" }] },
          { type: "text", text: "selected " },
          node("uiValue", { value: { op: "state", name: "quantity" } }),
          { type: "text", text: " unités", marks: [{ type: "italic" }] },
          { type: "text", text: " 🌱" },
        ]),
        node("uiMetric", { label: "Total", value: literal(2.5) }),
        node("uiChart", {
          label: "Progress",
          variant: "line",
          data: literal([{ day: 1, count: 2 }]),
          x: "day",
          y: "count",
        }),
        node("uiDataTable", {
          label: "Rows",
          data: literal([{ name: "One" }]),
          columns: [{ key: "name", label: "Name", type: "string" }],
          pageSize: 10,
        }),
      ],
    ),
  ],
};
original.content.push(node("uiDocument", {catalogVersion:1}, [{type:"text", text:'<box><text>Inline source 🌱</text></box>'}]));
function children(blocks) {
  const output = [];
  let delta = [];
  const flush = () => {
    if (!delta.length) return;
    const text = new Y.XmlText();
    text.applyDelta(delta);
    output.push(text);
    delta = [];
  };
  for (const block of blocks) {
    if (block.type === "text") {
      delta.push({
        insert: block.text,
        attributes: Object.fromEntries(
          (block.marks ?? []).map((mark) => [mark.type, mark.attrs ?? {}]),
        ),
      });
    } else {
      flush();
      output.push(xml(block));
    }
  }
  flush();
  return output;
}
function xml(block) {
  const element = new Y.XmlElement(block.type);
  for (const [key, value] of Object.entries(block.attrs ?? {}))
    element.setAttribute(key, value);
  element.insert(0, children(block.content ?? []));
  return element;
}
const base = new Y.Doc();
base.clientID = 100;
base.getXmlFragment("content").insert(0, original.content.map(xml));
const bytes = Y.encodeStateAsUpdate(base);
const vector = Y.encodeStateVector(base);
const a = new Y.Doc();
Y.applyUpdate(a, bytes);
a.clientID = 200;
const b = new Y.Doc();
Y.applyUpdate(b, bytes);
b.clientID = 300;
a.getXmlFragment("content").get(0).get(0).get(0).insert(8, "creative ");
b.getXmlFragment("content")
  .get(0)
  .get(12)
  .setAttribute("label", "Reviewed total");
const expected = structuredClone(original);
expected.content[0].content[0].content[0].text = "Chào 🌱 creative team";
expected.content[0].content[12].attrs.label = "Reviewed total";
const fixture = {
  yjsVersion: require("yjs/package.json").version,
  original,
  expected,
  base: Buffer.from(bytes).toString("base64"),
  updateA: Buffer.from(Y.encodeStateAsUpdate(a, vector)).toString("base64"),
  updateB: Buffer.from(Y.encodeStateAsUpdate(b, vector)).toString("base64"),
};
const merged = new Y.Doc();
Y.applyUpdate(merged, bytes);
Y.applyUpdate(merged, Buffer.from(fixture.updateA, "base64"));
Y.applyUpdate(merged, Buffer.from(fixture.updateB, "base64"));
assert.equal(
  merged.getXmlFragment("content").get(0).get(0).get(0).toString(),
  expected.content[0].content[0].content[0].text,
);
assert.equal(
  merged.getXmlFragment("content").get(0).get(12).getAttribute("label"),
  expected.content[0].content[12].attrs.label,
);
writeFileSync(
  new URL("intelligent-yjs.json", import.meta.url),
  JSON.stringify(fixture, null, 2) + "\n",
);
