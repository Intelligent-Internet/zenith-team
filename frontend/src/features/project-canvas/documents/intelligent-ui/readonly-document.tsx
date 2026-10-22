import {
  createElement,
  memo,
  useLayoutEffect,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import type { components } from "@/shared/api/generated/api.generated";
import { createRegistry, type DocumentView } from "./editor-runtime";
import {
  IntelligentInstanceContext,
  type InstanceContext,
} from "./document-context";
import type { ScopeRuntime } from "./scope-runtime";
import { InlineDocumentBlockView } from "./inline-document-node";
import { ReadonlySurface } from "./render-surface";
import { intelligentDefinitions } from "./catalog.generated";
import { supportsReactiveVersion } from "./versions";

type Block = components["schemas"]["CanvasBlock"];
type NativeRenderer = (
  block: Block,
  parent: ScopeRuntime | undefined,
  view: DocumentView,
) => ReactNode;
export function ReadonlyDocument({
  content,
  instance,
  renderNative,
}: {
  content: Block;
  instance?: InstanceContext;
  renderNative: NativeRenderer;
}) {
  const [view] = useState(() => {
    let document = content;
    let disposed = false;
    return {
      registry: createRegistry(),
      getDocument: () => document,
      isDisposed: () => disposed,
      update(next: Block) {
        disposed = false;
        document = next;
      },
      dispose() {
        disposed = true;
        this.registry.dispose();
      },
    };
  });
  useSyncExternalStore(
    view.registry.subscribe,
    view.registry.getSnapshot,
    view.registry.getSnapshot,
  );
  useLayoutEffect(() => {
    view.update(content);
    view.registry.setParent(instance?.runtime);
    view.registry.update(content);
  }, [view, content, instance?.runtime]);
  useLayoutEffect(() => {
    return instance?.registerView?.(view);
  }, [view, instance?.registerView]);
  useLayoutEffect(() => () => view.dispose(), [view]);
  return (
    <IntelligentInstanceContext.Provider value={instance ?? null}>
      <div className="tiptap" data-readonly-document="">
        <ReadonlyNode
          block={content}
          view={view}
          parent={instance?.runtime}
          renderNative={renderNative}
        />
      </div>
    </IntelligentInstanceContext.Provider>
  );
}

const ReadonlyNode = memo(function ReadonlyNode({
  block,
  view,
  parent,
  renderNative,
}: {
  block: Block;
  view: DocumentView;
  parent?: ScopeRuntime;
  renderNative: NativeRenderer;
}) {
  useSyncExternalStore(
    view.registry.subscribe,
    view.registry.getSnapshot,
    view.registry.getSnapshot,
  );
  const type = block.type ?? "";
  const attrs = block.attrs ?? {};
  const scope =
    type === "uiScope" ? view.registry.get(String(attrs.blockId)) : parent;
  const definition = intelligentDefinitions[type];
  const children = block.content?.map((child, index) => (
    <ReadonlyNode
      key={String(child.attrs?.blockId ?? index)}
      block={child}
      view={view}
      parent={scope}
      renderNative={renderNative}
    />
  ));
  if (definition) {
    if (!supportsReactiveVersion(type, attrs))
      return (
        <p role="status">
          This interactive content requires a compatible version of Zenith Team.
        </p>
      );
    const defaults = { ...definition.attrs, ...attrs };
    return (
      <ReadonlySurface.Provider value={{ children }}>
        <InlineDocumentBlockView
          type={type}
          attrs={defaults}
          source={(block.content ?? [])
            .map((child) => child.text ?? "")
            .join("")}
        />
      </ReadonlySurface.Provider>
    );
  }
  const identity = {
    "data-block-type": type,
    "data-block-id":
      typeof attrs.blockId === "string" ? attrs.blockId : undefined,
  };
  if (type === "doc") return <>{children}</>;
  if (type === "text") {
    let text: ReactNode = block.text ?? "";
    for (const mark of block.marks ?? []) {
      const tag = {
        bold: "strong",
        italic: "em",
        strike: "s",
        underline: "u",
        code: "code",
        highlight: "mark",
      }[mark.type];
      if (tag) text = createElement(tag, {}, text);
      if (mark.type === "link") {
        const href = safeLink(mark.attrs?.href);
        if (href)
          text = (
            <a href={href} target="_blank" rel="noopener noreferrer">
              {text}
            </a>
          );
      }
    }
    return <>{text}</>;
  }
  if (type === "hardBreak") return <br />;
  if (type === "horizontalRule") return <hr {...identity} />;
  const tag = {
    paragraph: "p",
    blockquote: "blockquote",
    bulletList: "ul",
    orderedList: "ol",
    listItem: "li",
    taskList: "ul",
    taskItem: "li",
    table: "table",
    tableRow: "tr",
    tableHeader: "th",
    tableCell: "td",
    callout: "aside",
    toggle: "details",
    toggleSummary: "summary",
    toggleContent: "div",
    columns: "div",
    column: "div",
  }[type];
  const style = ["left", "center", "right", "justify"].includes(
    String(attrs.textAlign),
  )
    ? { textAlign: attrs.textAlign as "left" | "center" | "right" | "justify" }
    : undefined;
  if (type === "heading")
    return createElement(
      `h${Math.min(6, Math.max(1, Number(attrs.level) || 1))}`,
      { ...identity, style },
      children,
    );
  if (tag) {
    const cell = type === "tableCell" || type === "tableHeader";
    return createElement(
      tag,
      {
        ...identity,
        style,
        ...(cell
          ? {
              colSpan: Number(attrs.colspan) || 1,
              rowSpan: Number(attrs.rowspan) || 1,
            }
          : {}),
        ...(type === "orderedList" ? { start: Number(attrs.start) || 1 } : {}),
        ...(type === "taskList" ? { "data-type": "taskList" } : {}),
        ...(type === "taskItem"
          ? { "data-type": "taskItem", "data-checked": attrs.checked === true }
          : {}),
        ...([
          "columns",
          "column",
          "callout",
          "toggle",
          "toggleSummary",
          "toggleContent",
        ].includes(type)
          ? {
              className: {
                columns: "pc-block-columns",
                column: "pc-block-column",
                callout: "pc-block-callout",
                toggle: "pc-block-toggle",
                toggleSummary: "pc-block-toggle-summary",
                toggleContent: "pc-block-toggle-content",
              }[type],
            }
          : {}),
      },
      type === "table" ? (
        <tbody>{children}</tbody>
      ) : type === "taskItem" ? (
        <>
          <input
            type="checkbox"
            checked={attrs.checked === true}
            readOnly
            disabled
          />
          <div>{children}</div>
        </>
      ) : type === "paragraph" && !children?.length ? (
        <br />
      ) : (
        children
      ),
    );
  }
  // Existing native reference/media adapters retain their authorization and
  // capture registration. Only these specialized leaves need an editor host.
  return (
    <ReadonlySurface.Provider value={null}>
      {renderNative(block, parent, view)}
    </ReadonlySurface.Provider>
  );
});

function safeLink(raw: unknown) {
  if (typeof raw !== "string") return undefined;
  try {
    const url = new URL(raw, window.location.origin);
    if (
      ["http:", "https:", "mailto:"].includes(url.protocol) &&
      !url.username &&
      !url.password
    )
      return raw;
  } catch {
    /* Invalid authored links remain plain text. */
  }
}
