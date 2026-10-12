import {
  mergeAttributes,
  Node,
  nodePasteRule,
  ReactNodeViewRenderer,
  NodeViewWrapper,
  type NodeViewProps,
} from "@tiptap/react";
import { Link } from "react-router";
import { useGetWikiPageQuery } from "./wiki-api";
import { WikiArtifactRefView } from "./wiki-artifact-ref-view";

function WikiPageLinkView({ node, extension }: NodeViewProps) {
  const page = useGetWikiPageQuery({
    companyId: String(extension.options.companyId),
    pageId: String(node.attrs.id),
  });
  const available = page.isError ? undefined : page.currentData;
  const title = available
    ? String(node.attrs.label) || available.title
    : page.isFetching
      ? "Loading page…"
      : "Unavailable page";
  return (
    <NodeViewWrapper as="span" className="wk-wiki-link">
      {available ? (
        <Link to={`/wiki/pages/${node.attrs.id}`}>{title}</Link>
      ) : (
        title
      )}
    </NodeViewWrapper>
  );
}

/** A native inline reference to a Company Wiki page. */
export const WikiPageLink = Node.create<{ companyId: string }>({
  addOptions() {
    return { companyId: "" };
  },
  addNodeView() {
    return ReactNodeViewRenderer(WikiPageLinkView, { as: "span" });
  },
  name: "wikiPageLink",
  group: "inline",
  inline: true,
  atom: true,
  selectable: true,

  addAttributes() {
    return {
      id: { default: null, rendered: false },
      label: { default: "", rendered: false },
    };
  },

  parseHTML() {
    return [
      {
        tag: "a[data-wiki-page-id]",
        priority: 100,
        getAttrs: (element) => ({
          id: element.getAttribute("data-wiki-page-id"),
          label: element.textContent ?? "",
        }),
      },
    ];
  },

  renderHTML({ node, HTMLAttributes }) {
    const id = String(node.attrs.id);
    return [
      "a",
      mergeAttributes(HTMLAttributes, {
        href: `/wiki/pages/${id}`,
        "data-wiki-page-id": id,
        class: "wk-wiki-link",
      }),
      String(node.attrs.label) || "Linked page",
    ];
  },

  renderText({ node }) {
    return String(node.attrs.label);
  },
});

/**
 * Citation of an Artifact, or of one of its items, stored label-free as
 * `[[artifact:id]]` / `[[artifact:id#ordinal]]`. The chip is resolved for
 * whoever is looking at it.
 */
export const WikiArtifactRef = Node.create<{ companyId: string }>({
  name: "wikiArtifactRef",
  group: "inline",
  inline: true,
  atom: true,
  selectable: true,

  addOptions() {
    return { companyId: "" };
  },

  addAttributes() {
    return {
      id: { default: null, rendered: false },
      ordinal: { default: null, rendered: false },
    };
  },

  parseHTML() {
    return [
      {
        tag: "span[data-wiki-artifact-id]",
        priority: 100,
        getAttrs: (element) => {
          const ordinal = element.getAttribute("data-wiki-artifact-ordinal");
          return {
            id: element.getAttribute("data-wiki-artifact-id"),
            ordinal: ordinal ? Number(ordinal) : null,
          };
        },
      },
    ];
  },

  renderHTML({ node, HTMLAttributes }) {
    return [
      "span",
      mergeAttributes(HTMLAttributes, {
        "data-wiki-artifact-id": String(node.attrs.id),
        ...(node.attrs.ordinal === null
          ? {}
          : { "data-wiki-artifact-ordinal": String(node.attrs.ordinal) }),
      }),
      "Artifact",
    ];
  },

  renderText() {
    return "Artifact";
  },

  addNodeView() {
    return ReactNodeViewRenderer(WikiArtifactRefView, { as: "span" });
  },

  // Pasting a link to an Artifact of this Company cites it; any other URL,
  // including another Company's Artifact, stays an ordinary link.
  addPasteRules() {
    const companyId = this.options.companyId.toLowerCase();
    return [
      nodePasteRule({
        find: /https?:\/\/\S+?\/companies\/([0-9a-fA-F-]{36})\/artifacts\/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})(?:\?item=(\d{1,2}))?(?![\w?&=#-])/g,
        type: this.type,
        getAttributes: (match) =>
          companyId && match[1].toLowerCase() === companyId
            ? {
                id: match[2].toLowerCase(),
                ordinal: match[3] === undefined ? null : Number(match[3]),
              }
            : null,
      }),
    ];
  },
});
