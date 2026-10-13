import { parse, type DefaultTreeAdapterMap } from "parse5";
import { unzipSync, strFromU8 } from "fflate";
import { DOMParser } from "@xmldom/xmldom";
import type { PreviewRequest, PreviewResponse } from "./preview-worker.types";
const escape = (value: string) =>
  value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
const elements = new Set(
  "html head body title style div span p a h1 h2 h3 h4 h5 h6 main section article aside header footer nav ul ol li dl dt dd table thead tbody tfoot tr td th caption colgroup col strong em b i u s blockquote pre code figure figcaption picture img br hr button input label select option textarea details summary small sup sub time mark".split(
    " ",
  ),
);
const voidElements = new Set(["img", "br", "hr", "input", "col"]);
function sanitize(node: DefaultTreeAdapterMap["node"], depth = 0): string {
  if (depth > 80) return "";
  if (node.nodeName === "#text")
    return escape((node as DefaultTreeAdapterMap["textNode"]).value);
  if (!("tagName" in node))
    return "childNodes" in node
      ? node.childNodes.map((child) => sanitize(child, depth + 1)).join("")
      : "";
  const tag = node.tagName.toLowerCase();
  if (!elements.has(tag)) return "";
  if (tag === "style")
    return `<style>${node.childNodes
      .map((child) =>
        child.nodeName === "#text"
          ? (child as DefaultTreeAdapterMap["textNode"]).value
          : "",
      )
      .join("")
      .replace(/<\//g, "<\\/")}</style>`;
  const attrs = node.attrs
    .filter(
      (attr) =>
        [
          "class",
          "id",
          "style",
          "title",
          "alt",
          "width",
          "height",
          "colspan",
          "rowspan",
          "placeholder",
          "value",
          "checked",
          "open",
        ].includes(attr.name) ||
        (attr.name === "href" && attr.value.startsWith("#")) ||
        (attr.name === "src" &&
          /^data:image\/(png|jpeg|gif|webp);base64,/i.test(attr.value)),
    )
    .map((attr) => ` ${attr.name}="${escape(attr.value)}"`)
    .join("");
  return `<${tag}${attrs}${["input", "button", "select", "textarea"].includes(tag) ? " disabled" : ""}>${voidElements.has(tag) ? "" : node.childNodes.map((child) => sanitize(child, depth + 1)).join("") + `</${tag}>`}`;
}
function wordDocument(buffer: ArrayBuffer): string {
  const files = unzipSync(new Uint8Array(buffer), {
    filter: (file) => {
      if (file.name !== "word/document.xml") return false;
      if (file.originalSize > 5 * 1024 * 1024)
        throw new Error("This Word document is too large for reading view.");
      return true;
    },
  });
  if (!files["word/document.xml"])
    throw new Error("This file is not a supported Word document.");
  const xml = strFromU8(files["word/document.xml"]);
  if (/<!DOCTYPE|<!ENTITY/i.test(xml))
    throw new Error("This document contains unsupported XML definitions.");
  const document = new DOMParser().parseFromString(xml, "text/xml");
  const paragraphs = Array.from(document.getElementsByTagName("w:p")).slice(
    0,
    3000,
  );
  return paragraphs
    .map((paragraph) => {
      const style =
        paragraph.getElementsByTagName("w:pStyle")[0]?.getAttribute("w:val") ??
        "";
      const level = /^heading([1-6])$/i.exec(style)?.[1];
      const text = Array.from(paragraph.getElementsByTagName("w:t"))
        .map((node) => node.textContent)
        .join("");
      return `${level ? "#".repeat(Number(level)) + " " : ""}${text}`;
    })
    .join("\n\n")
    .slice(0, 262144);
}
self.onmessage = ({ data }: MessageEvent<PreviewRequest>) => {
  let result: PreviewResponse;
  try {
    if (
      data.buffer.byteLength >
      (data.format === "docx" ? 20 * 1024 * 1024 : 1024 * 1024)
    )
      throw new Error(
        "This file is too large for an inline preview. Download the original to read it.",
      );
    const content =
      data.format === "docx"
        ? wordDocument(data.buffer)
        : new TextDecoder("utf-8", { fatal: true }).decode(data.buffer);
    result = {
      requestId: data.requestId,
      content: data.format === "html" ? sanitize(parse(content)) : content,
    };
  } catch (error) {
    result = {
      requestId: data.requestId,
      error:
        error instanceof Error
          ? error.message
          : "Preview could not be prepared.",
    };
  }
  postMessage(result);
};
