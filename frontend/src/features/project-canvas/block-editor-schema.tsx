import { DocumentOutline } from "./document-outline";
import { BlockSettings, useBlockEditable } from "./block-settings";
import { DocumentLinks } from "./document-links";
import { PageReportMode } from "./page-report-context";
import { BlockViewSelect } from "./block-view-select";
import "./document-blocks.css";
import {
  createContext,
  lazy,
  Suspense,
  useContext,
  useEffect,
  useId,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  Extension,
  Node,
  mergeAttributes,
  NodeViewWrapper,
  NodeViewContent,
  ReactNodeViewRenderer,
  EditorContent,
  useEditor,
  type NodeViewProps,
  type JSONContent,
} from "@tiptap/react";
import { Plugin, type Transaction } from "@tiptap/pm/state";
import { AddMarkStep, RemoveMarkStep, ReplaceStep } from "@tiptap/pm/transform";
import {
  WorkBlock,
  OutputCollectionBlock,
  WorkBlockScope,
  WorkBlockMode,
} from "./work-blocks";
import { WorkflowBlock } from "./workflow-block";
import { Link, useNavigate } from "react-router";
import { Icon } from "@/shared/ui/icon";
import { useTheme } from "next-themes";
import {
  Slice,
  Fragment,
  type Node as ProseMirrorNode,
} from "@tiptap/pm/model";
import { ySyncPluginKey, yUndoPluginKey } from "@tiptap/y-tiptap";
import type { UndoManager } from "yjs";
import { preserveBlockIdentityOnUndo } from "./document-identities";
import StarterKit from "@tiptap/starter-kit";
import CodeBlock from "@tiptap/extension-code-block";
import { ChatSourceCode } from "@/shared/chat/chat-source-preview";
import { TaskItem, TaskList } from "@tiptap/extension-list";
import { TableKit } from "@tiptap/extension-table";
import Image from "@tiptap/extension-image";
import Highlight from "@tiptap/extension-highlight";
import TextAlign from "@tiptap/extension-text-align";
import Placeholder from "@tiptap/extension-placeholder";
import {
  useCanvasBodyQuery,
  useCanvasDocumentQuery,
  useCanvasHistoryQuery,
  useGetCanvasQuery,
  type CanvasScope,
} from "./canvas-api";
import type { components } from "@/shared/api/generated/api.generated";
import { MarkdownView } from "./markdown-view";
import type { DocumentScope } from "./document-scope";
import {
  WikiPageLink,
  WikiArtifactRef,
} from "../company-wiki/wiki-reference-nodes";
import { WikiArtifactsProvider } from "../company-wiki/wiki-artifacts";
import "katex/dist/katex.min.css";

export type Block = components["schemas"]["CanvasBlock"];
const BlockScope = createContext<DocumentScope | null>(null);
export function BlockScopeProvider({
  value,
  content,
  children,
}: {
  value: DocumentScope;
  content?: JSONContent;
  children: ReactNode;
}) {
  return (
    <BlockScope.Provider value={value}>
      <WikiArtifactsProvider companyId={value.companyId} content={content}>
        {children}
      </WikiArtifactsProvider>
    </BlockScope.Provider>
  );
}
const BlockLive = createContext(false);
export const BlockLiveProvider = BlockLive.Provider;
export const SnapshotDocuments = createContext<Record<string, Block> | null>(
  null,
);
const ReferenceTrail = createContext<string[]>([]);
const ReferenceViewer = lazy(() =>
  import("./artifact-viewer").then((module) => ({
    default: module.ArtifactViewer,
  })),
);

const blockTypes = [
  "paragraph",
  "heading",
  "blockquote",
  "bulletList",
  "orderedList",
  "listItem",
  "taskList",
  "taskItem",
  "codeBlock",
  "mermaid",
  "horizontalRule",
  "image",
  "table",
  "tableRow",
  "tableCell",
  "tableHeader",
  "callout",
  "toggle",
  "toggleSummary",
  "toggleContent",
  "columns",
  "column",
  "artifactReference",
  "taskReference",
  "taskCollection",
  "outputCollection",
  "workflowReference",
  "fileBlock",
  "audioBlock",
  "videoBlock",
  "equation",
  "tableOfContents",
];

function onlyInlineChanges(tr: Transaction) {
  return tr.steps.every((step, index) => {
    if (step instanceof AddMarkStep || step instanceof RemoveMarkStep)
      return true;
    if (
      !(step instanceof ReplaceStep) ||
      step.slice.openStart ||
      step.slice.openEnd
    )
      return false;
    let textOnly = true;
    step.slice.content.forEach((node) => {
      if (!node.isText) textOnly = false;
    });
    return (
      textOnly &&
      tr.docs[index]
        .resolve(step.from)
        .sameParent(tr.docs[index].resolve(step.to))
    );
  });
}

function validBlockIdentities(doc: ProseMirrorNode) {
  const seen = new Set<string>();
  let valid = true;
  doc.descendants((node) => {
    if (!valid) return false;
    if (!blockTypes.includes(node.type.name)) return;
    const id = node.attrs.blockId as string | null;
    if (!id || seen.has(id)) valid = false;
    else seen.add(id);
  });
  return valid;
}

function BlockIdentities(
  ids?: string[],
  reserve?: (count?: number, retry?: boolean) => void,
) {
  return Extension.create({
    name: "blockIdentities",
    addGlobalAttributes() {
      return [
        {
          types: blockTypes,
          attributes: {
            agentProtected: {
              default: false,
              parseHTML: (element) =>
                element.getAttribute("data-agent-protected") === "true",
              renderHTML: (attrs) =>
                attrs.agentProtected ? { "data-agent-protected": "true" } : {},
            },
            sourceVersionIds: { default: [], rendered: false },
            capturedNeedsRefresh: {
              default: null,
              renderHTML: (attrs) =>
                attrs.capturedNeedsRefresh
                  ? {
                      "data-source-freshness": "needs_refresh",
                      title:
                        "Narrative needed refresh when this presentation was captured.",
                    }
                  : {},
            },
            blockId: {
              default: null,
              parseHTML: (element) => element.getAttribute("data-block-id"),
              renderHTML: (attrs) =>
                attrs.blockId
                  ? {
                      "data-block-id": attrs.blockId,
                      id: `block-${attrs.blockId}`,
                    }
                  : {},
            },
          },
        },
      ];
    },
    addProseMirrorPlugins() {
      if (!ids) return [];
      return [
        new Plugin<boolean>({
          view(view) {
            const manager: UndoManager | undefined = yUndoPluginKey.getState(
              view.state,
            )?.undoManager;
            if (!manager) return {};
            return { destroy: preserveBlockIdentityOnUndo(manager) };
          },
          state: {
            init: (_, state) => validBlockIdentities(state.doc),
            apply: (tr, valid) =>
              !tr.docChanged || onlyInlineChanges(tr)
                ? valid
                : validBlockIdentities(tr.doc),
          },
          props: {
            transformPasted(slice) {
              const clear = (node: ProseMirrorNode): ProseMirrorNode => {
                if (node.isText) return node;
                const children: ProseMirrorNode[] = [];
                node.content.forEach((child) => children.push(clear(child)));
                return node.type.create(
                  {
                    ...node.attrs,
                    ...(blockTypes.includes(node.type.name)
                      ? { blockId: null }
                      : {}),
                  },
                  Fragment.fromArray(children),
                  node.marks,
                );
              };
              const children: ProseMirrorNode[] = [];
              slice.content.forEach((node) => children.push(clear(node)));
              return new Slice(
                Fragment.fromArray(children),
                slice.openStart,
                slice.openEnd,
              );
            },
          },
          // Undo may preserve a paragraph while removing its ID. Inline edits
          // can skip the repair scan only while all existing IDs remain valid.
          appendTransaction(transactions, _old, state) {
            if (
              !transactions.some((tx) => tx.docChanged) ||
              (this.getState(state) && transactions.every(onlyInlineChanges)) ||
              transactions.some((tx) => {
                const sync = tx.getMeta(ySyncPluginKey);
                return sync?.isChangeOrigin && !sync.isUndoRedoOperation;
              })
            )
              return null;
            const seen = new Set<string>();
            const tr = state.tr;
            state.doc.descendants((node, pos) => {
              if (!blockTypes.includes(node.type.name)) return;
              const id = node.attrs.blockId as string | null;
              if (!id || seen.has(id)) {
                const next = ids.shift();
                if (next) {
                  tr.setNodeMarkup(pos, undefined, {
                    ...node.attrs,
                    blockId: next,
                  });
                  seen.add(next);
                }
              } else seen.add(id);
            });
            if (tr.docChanged && ids.length < 8) reserve?.(32);
            return tr.docChanged ? tr : null;
          },
          filterTransaction(tr, state) {
            if (
              !tr.docChanged ||
              tr.getMeta(ySyncPluginKey)?.isChangeOrigin ||
              (onlyInlineChanges(tr) && this.getState(state))
            )
              return true;
            const seen = new Set<string>();
            let needed = 0;
            tr.doc.descendants((node) => {
              if (blockTypes.includes(node.type.name)) {
                const id = node.attrs.blockId as string;
                if (!id || seen.has(id)) needed++;
                else seen.add(id);
              }
            });
            if (needed > ids.length) reserve?.(needed, true);
            return needed <= ids.length;
          },
        }),
      ];
    },
  });
}

function MermaidView({ node, editor }: NodeViewProps) {
  const editable = useBlockEditable(editor);
  const report = useContext(PageReportMode);
  const id = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const { resolvedTheme } = useTheme();
  const [svg, setSvg] = useState("");
  const [error, setError] = useState("");
  const [mode, setMode] = useState<"diagram" | "source" | "split">("diagram");
  const view = editable && !report ? mode : "diagram";
  const source = node.textContent;
  useEffect(() => {
    let cancelled = false;
    const timer = setTimeout(() => {
      void import("mermaid")
        .then(async ({ default: mermaid }) => {
          mermaid.initialize({
            startOnLoad: false,
            securityLevel: "strict",
            maxTextSize: 50000,
            suppressErrorRendering: true,
            theme: !report && resolvedTheme === "dark" ? "dark" : "neutral",
          });
          const result = await mermaid.render(
            `diagram-${id}`,
            source || "flowchart LR\n A --> B",
          );
          if (!cancelled) {
            setSvg(result.svg);
            setError("");
          }
        })
        .catch(() => {
          if (!cancelled)
            setError(
              "The diagram has a syntax error. The previous preview is outdated; your source is preserved.",
            );
        });
    }, 350);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [id, source, resolvedTheme, report]);
  return (
    <NodeViewWrapper
      className="pc-has-settings pc-block-diagram"
      data-view={view}
      data-block-id={node.attrs.blockId}
    >
      {!report && editable && (
        <BlockSettings
          label="Diagram options"
          description="Choose how you work with this diagram."
        >
          <BlockViewSelect
            value={mode}
            options={[
              ["diagram", "Preview"],
              ["source", "Edit source"],
              ["split", "Source and preview"],
            ]}
            onChange={(value) => setMode(value as typeof mode)}
          />
        </BlockSettings>
      )}
      {report && !svg && !error && <p role="status">Preparing diagram…</p>}
      {error && (
        <p role="status" contentEditable={false}>
          {error}
        </p>
      )}
      {view !== "source" && (
        <div contentEditable={false} className="pc-diagram-preview">
          <div dangerouslySetInnerHTML={{ __html: svg }} />
        </div>
      )}
      <div hidden={view === "diagram"}>
        <NodeViewContent className="pc-diagram-source" />
      </div>
    </NodeViewWrapper>
  );
}

function CodeView({ node, editor, updateAttributes }: NodeViewProps) {
  const editable = useBlockEditable(editor);
  const report = useContext(PageReportMode);
  const [editing, setEditing] = useState(false);
  const preview = !editable || !editing;
  const [copied, setCopied] = useState(false);
  const ext: Record<string, string> = {
    typescript: "ts",
    javascript: "js",
    python: "py",
    rust: "rs",
    go: "go",
    sql: "sql",
    json: "json",
    html: "html",
    css: "css",
    bash: "sh",
  };
  const filename = String(
    node.attrs.filename || `snippet.${ext[node.attrs.language] || "txt"}`,
  );
  if (report)
    return (
      <NodeViewWrapper
        className="pc-has-settings pc-block-code"
        data-block-id={node.attrs.blockId}
      >
        <pre>{node.textContent}</pre>
      </NodeViewWrapper>
    );
  return (
    <NodeViewWrapper
      className="pc-has-settings pc-block-code"
      data-block-id={node.attrs.blockId}
    >
      {!report && (
        <BlockSettings
          label="Code options"
          description={
            editable ? "Edit the source or copy this code." : "Copy this code."
          }
        >
          {editable && (
            <>
              <label>
                Filename
                <input
                  aria-label="Code filename"
                  placeholder="Optional filename"
                  value={node.attrs.filename || ""}
                  onChange={(event) =>
                    updateAttributes({ filename: event.target.value })
                  }
                />
              </label>
              <button
                aria-pressed={editing}
                onClick={() => setEditing(!editing)}
              >
                {editing ? "Done editing" : "Edit source"}
              </button>
            </>
          )}
          <button
            onClick={() =>
              void navigator.clipboard
                .writeText(node.textContent)
                .then(() => setCopied(true))
                .catch(() => setCopied(false))
            }
          >
            {copied ? "Copied" : "Copy code"}
          </button>
        </BlockSettings>
      )}
      {preview && (
        <div contentEditable={false}>
          <ChatSourceCode
            text={node.textContent}
            file={{
              id: node.attrs.blockId,
              name: filename,
              mediaType: "text/plain",
              url: "",
            }}
          />
        </div>
      )}
      <div hidden={preview}>
        <NodeViewContent className="pc-diagram-source" />
      </div>
    </NodeViewWrapper>
  );
}

function EquationView({ node, updateAttributes, editor }: NodeViewProps) {
  const editable = useBlockEditable(editor);
  const [html, setHTML] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    void import("katex")
      .then((katex) => {
        const result = katex.default.renderToString(
          String(node.attrs.source || "E = mc^2"),
          {
            displayMode: true,
            throwOnError: false,
            trust: false,
            strict: "error",
            maxExpand: 1000,
          },
        );
        if (active) {
          setHTML(result);
          setError("");
        }
      })
      .catch(() => {
        if (active)
          setError(
            "The equation preview is unavailable. Your LaTeX source is preserved.",
          );
      });
    return () => {
      active = false;
    };
  }, [node.attrs.source]);
  return (
    <NodeViewWrapper
      className="pc-has-settings pc-block-equation"
      data-block-id={node.attrs.blockId}
    >
      <div contentEditable={false} dangerouslySetInnerHTML={{ __html: html }} />
      {error && (
        <p role="status">
          {error} <code>{node.attrs.source}</code>
        </p>
      )}
      {editable && (
        <BlockSettings label="Equation options">
          <label>
            Equation
            <input
              aria-label="LaTeX equation"
              value={node.attrs.source ?? ""}
              onChange={(event) =>
                updateAttributes({ source: event.target.value })
              }
            />
          </label>
        </BlockSettings>
      )}
    </NodeViewWrapper>
  );
}

function ReferenceView(props: NodeViewProps) {
  const scope = useContext(BlockScope);
  if (!scope || !("projectId" in scope))
    return (
      <NodeViewWrapper className="pc-block-empty">
        This reference requires a Project.
      </NodeViewWrapper>
    );
  return <ProjectReferenceView {...props} scope={scope} />;
}

function ProjectReferenceView({
  node,
  editor,
  updateAttributes,
  scope,
}: NodeViewProps & { scope: CanvasScope }) {
  const editable = useBlockEditable(editor);
  const live = useContext(BlockLive);
  const capturedDocuments = useContext(SnapshotDocuments);
  const mode = useContext(WorkBlockMode);
  const trail = useContext(ReferenceTrail);
  const navigate = useNavigate();
  const snapshot = useGetCanvasQuery(scope);
  const pinned = String(node.attrs.versionId || "");
  const artifact = snapshot.currentData?.artifacts.find(
    (row) => row.id === node.attrs.artifactId || row.latestVersionId === pinned,
  );
  const follows =
    node.attrs.format === "accepted" &&
    (editable || live) &&
    !capturedDocuments;
  const versionId = follows ? artifact?.acceptedVersionId || "" : pinned;
  const history = useCanvasHistoryQuery(
    {
      ...scope,
      artifactId: artifact?.id || String(node.attrs.artifactId || ""),
    },
    { skip: !artifact?.id && !node.attrs.artifactId },
  );
  const version =
    history.currentData?.versions.find((row) => row.id === versionId) ??
    (artifact?.latestVersionId === versionId
      ? artifact.latestVersion
      : undefined);
  const display = String(node.attrs.display || "card");
  const [open, setExpanded] = useState(
    display === "expanded" || display === "section",
  );
  useEffect(
    () => setExpanded(display === "expanded" || display === "section"),
    [display],
  );
  const readsLivePage =
    node.attrs.format === "live" && (editable || live) && !capturedDocuments;
  const opensCurrentPage =
    readsLivePage || (!!capturedDocuments && node.attrs.format === "live");
  const destination = new URLSearchParams();
  const currentPageId = artifact?.pageId ?? version?.wikiPageId;
  if (opensCurrentPage && currentPageId) {
    destination.set("pageId", currentPageId);
  } else {
    destination.set(
      "artifactId",
      artifact?.id || String(node.attrs.artifactId || ""),
    );
    destination.set("versionId", versionId);
  }
  const sourceLocation =
    opensCurrentPage &&
    !artifact?.pageId &&
    version?.wikiPageId &&
    !version.sourceOutputId
      ? `/wiki/pages/${version.wikiPageId}`
      : `/projects/${scope.projectId}/${opensCurrentPage && currentPageId ? "pages" : "artifacts"}?${destination}`;
  const body = useCanvasBodyQuery(
    { ...scope, versionId },
    {
      skip:
        !!capturedDocuments ||
        readsLivePage ||
        !open ||
        !versionId ||
        version?.sourceType !== "wiki",
    },
  );
  const page = useCanvasDocumentQuery(
    { ...scope, versionId },
    { skip: !readsLivePage || !open || !versionId },
  );
  const content = capturedDocuments
    ? capturedDocuments[
        node.attrs.format === "live" ? `${versionId}:live` : versionId
      ]
    : readsLivePage
      ? page.currentData?.content
      : body.currentData?.content;
  const unavailable =
    history.isError ||
    snapshot.isError ||
    (!capturedDocuments && (readsLivePage ? page.isError : body.isError)) ||
    (history.isSuccess && !version) ||
    (!!capturedDocuments && version?.sourceType === "wiki" && !content);
  const loading =
    !capturedDocuments &&
    (history.isFetching || (snapshot.isFetching && !artifact));
  const sections: { id: string; label: string }[] = [];
  const collect = (block?: Block) => {
    if (!block || sections.length >= 100) return;
    if (block.type === "heading" && typeof block.attrs?.blockId === "string")
      sections.push({ id: block.attrs.blockId, label: blockText(block) });
    block.content?.forEach(collect);
  };
  collect(content);
  return (
    <NodeViewWrapper
      className={`pc-has-settings pc-block-reference pc-reference-${display}`}
      data-block-id={node.attrs.blockId}
      contentEditable={false}
    >
      {editable && (
        <BlockSettings label="Reference options" needed={!versionId}>
          <label>
            Source
            <select
              aria-label="Reference source version"
              value={versionId}
              onChange={(event) => {
                const row = snapshot.currentData?.artifacts.find(
                  (row) => row.latestVersionId === event.target.value,
                );
                updateAttributes({
                  versionId: event.target.value,
                  artifactId: row?.id || "",
                  label: row?.title || "",
                  blockTargetId: "",
                  format:
                    row?.latestVersion.wikiPageId &&
                    !row.latestVersion.sourceOutputId
                      ? "live"
                      : "pinned",
                });
              }}
            >
              <option value="">Choose existing work…</option>
              {versionId &&
                !snapshot.currentData?.artifacts.some(
                  (row) => row.latestVersionId === versionId,
                ) && (
                  <option value={versionId}>
                    Selected version {version?.number}
                  </option>
                )}
              {snapshot.currentData?.artifacts.map((row) => (
                <option key={row.id} value={row.latestVersionId}>
                  {row.title} · v{row.latestVersion.number}
                </option>
              ))}
            </select>
          </label>
          <BlockViewSelect
            value={display}
            options={[
              ["link", "Inline link"],
              ["card", "Card"],
              ["section", "Embedded section"],
              ["expanded", "Featured"],
            ]}
            onChange={(display) => updateAttributes({ display })}
          />
          {sections.length > 0 && (
            <label>
              Section
              <select
                aria-label="Referenced section"
                value={node.attrs.blockTargetId || ""}
                onChange={(event) =>
                  updateAttributes({ blockTargetId: event.target.value })
                }
              >
                <option value="">Whole document</option>
                {sections.map((section) => (
                  <option key={section.id} value={section.id}>
                    {section.label}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label>
            Caption
            <input
              aria-label="Local reference caption"
              maxLength={2000}
              value={node.attrs.caption || ""}
              onChange={(event) =>
                updateAttributes({ caption: event.target.value })
              }
              placeholder="Why this matters…"
            />
          </label>
          <label>
            Updates
            <select
              aria-label="Reference updates"
              value={node.attrs.format || "pinned"}
              onChange={(event) =>
                updateAttributes({ format: event.target.value, versionId })
              }
            >
              <option value="pinned">
                Keep saved version{version ? ` ${version.number}` : ""}
              </option>
              {version?.wikiPageId && !version.sourceOutputId && (
                <option value="live">Follow the live page</option>
              )}
            </select>
          </label>
        </BlockSettings>
      )}
      {loading ? (
        <p role="status" className="pc-block-empty">
          Checking source access…
        </p>
      ) : unavailable ? (
        <p className="pc-block-empty">Source unavailable or access changed.</p>
      ) : !versionId ? (
        <p className="pc-block-empty">Choose a source in Reference options.</p>
      ) : (
        <>
          <div className="pc-reference-heading">
            <Icon name="document-text" className="size-5" />
            <strong>
              <Link to={sourceLocation}>
                {node.attrs.label || artifact?.title || "Referenced content"}
              </Link>
            </strong>
            <button aria-expanded={open} onClick={() => setExpanded(!open)}>
              {open ? "Collapse" : "Expand"}
            </button>
            <button onClick={() => navigate(sourceLocation)}>
              {opensCurrentPage ? "Open current page" : "Open saved version"}
            </button>
          </div>
          {node.attrs.caption && (
            <p className="pc-reference-caption">{node.attrs.caption}</p>
          )}
          {!capturedDocuments &&
            artifact &&
            artifact.latestVersionId !== versionId &&
            !follows &&
            !readsLivePage && (
              <small className="pc-source-warning">
                A newer source version is available. This selection stays
                pinned.
              </small>
            )}
          {open &&
            (trail.includes(versionId) || trail.length >= 2 ? (
              <p>Open the source to explore nested references.</p>
            ) : version?.sourceType !== "wiki" && version ? (
              <Suspense fallback={<p>Loading preview…</p>}>
                <ReferenceViewer scope={scope} version={version} />
              </Suspense>
            ) : content?.type === "doc" ? (
              <ReferenceTrail.Provider value={[...trail, versionId]}>
                <BlockDocument
                  scope={scope}
                  content={content}
                  blockId={String(node.attrs.blockTargetId || "") || undefined}
                  mode={capturedDocuments ? "snapshot" : mode}
                  live={readsLivePage}
                />
              </ReferenceTrail.Provider>
            ) : (
              <MarkdownView>
                {readsLivePage
                  ? page.isFetching
                    ? "Loading page…"
                    : "This page is empty."
                  : body.currentData?.body || "Loading source…"}
              </MarkdownView>
            ))}
          <small>
            {node.attrs.format === "live"
              ? capturedDocuments
                ? "Page at capture"
                : readsLivePage
                  ? "Live page"
                  : "Page version"
              : follows
                ? "Following accepted"
                : "Pinned evidence"}
            {version && node.attrs.format !== "live"
              ? ` · Version ${version.number}`
              : ""}
          </small>
        </>
      )}
    </NodeViewWrapper>
  );
}

function MediaView({ node, editor, updateAttributes }: NodeViewProps) {
  const editable = useBlockEditable(editor);
  const report = useContext(PageReportMode);
  const src = String(node.attrs.src || "");
  const safe = /^https?:\/\//i.test(src);
  return (
    <NodeViewWrapper
      className={`pc-has-settings pc-block-media pc-media-${node.attrs.display || "card"}`}
      data-block-id={node.attrs.blockId}
      contentEditable={false}
    >
      {editable && (
        <BlockSettings label="Media options">
          <BlockViewSelect
            value={node.attrs.display || "card"}
            options={[
              ["card", "Framed"],
              ["section", "Borderless"],
              ["expanded", "Spotlight"],
            ]}
            onChange={(display) => updateAttributes({ display })}
          />
          {node.type.name === "image" && (
            <label>
              Alternative text
              <input
                value={node.attrs.alt || ""}
                onChange={(event) =>
                  updateAttributes({ alt: event.target.value })
                }
                placeholder="Describe this image"
              />
            </label>
          )}
          <label>
            Media URL{" "}
            <input
              type="url"
              aria-label="Media URL"
              value={src}
              placeholder="https://…"
              onChange={(event) =>
                updateAttributes({ src: event.target.value })
              }
            />
          </label>
        </BlockSettings>
      )}
      {!safe && (
        <p className="pc-block-empty">
          Add a media URL to display this resource.
        </p>
      )}
      {safe &&
        (node.type.name === "image" ? (
          <img
            src={src}
            alt={node.attrs.alt || ""}
            width={node.attrs.width || undefined}
            height={node.attrs.height || undefined}
            loading="lazy"
          />
        ) : report ? (
          <a href={src} target="_blank" rel="noopener noreferrer">
            {node.attrs.label || "Open media"}
          </a>
        ) : node.type.name === "audioBlock" ? (
          <audio controls preload="metadata" src={src} />
        ) : node.type.name === "videoBlock" ? (
          <video controls preload="metadata" src={src} />
        ) : (
          <a href={src} target="_blank" rel="noopener noreferrer">
            {node.attrs.label || "Open file"}
          </a>
        ))}
    </NodeViewWrapper>
  );
}

function OutlineView({ editor, node }: NodeViewProps) {
  const [headings, setHeadings] = useState<
    { id: string; text: string; level: number }[]
  >([]);
  useEffect(() => {
    const update = () => {
      const next: typeof headings = [];
      editor.state.doc.descendants((n) => {
        if (n.type.name === "heading")
          next.push({
            id: n.attrs.blockId,
            text: n.textContent,
            level: n.attrs.level,
          });
      });
      setHeadings(next);
    };
    update();
    editor.on("update", update);
    return () => {
      editor.off("update", update);
    };
  }, [editor]);
  return (
    <NodeViewWrapper
      className="pc-block-outline"
      data-block-id={node.attrs.blockId}
      contentEditable={false}
    >
      <strong>On this page</strong>
      {headings.map((row) => (
        <button
          key={row.id}
          style={{ paddingInlineStart: (row.level - 1) * 12 }}
          onClick={() =>
            editor.view.dom
              .querySelector(`[data-block-id="${CSS.escape(row.id)}"]`)
              ?.scrollIntoView({ block: "center", behavior: "smooth" })
          }
        >
          {row.text || "Untitled heading"}
        </button>
      ))}
    </NodeViewWrapper>
  );
}

function container(
  name: string,
  tag: string,
  content: string,
  className: string,
  group = "block",
) {
  return Node.create({
    name,
    group,
    content,
    defining: true,
    draggable: true,
    parseHTML() {
      return [{ tag: `${tag}[data-block-type="${name}"]` }];
    },
    renderHTML({ HTMLAttributes }) {
      return [
        tag,
        mergeAttributes(HTMLAttributes, {
          "data-block-type": name,
          class: className,
        }),
        0,
      ];
    },
  });
}

export function blockExtensions(options: {
  navigate: (location: string) => void;
  collaborative?: boolean;
  companyId?: string;
  ids?: string[];
  reserve?: (count?: number, retry?: boolean) => void;
}) {
  return [
    DocumentLinks.configure({ navigate: options.navigate }),
    StarterKit.configure({
      codeBlock: false,
      undoRedo: options.collaborative ? false : undefined,
      link: { openOnClick: true },
    }),
    CodeBlock.extend({
      draggable: true,
      addAttributes() {
        return { ...this.parent?.(), filename: { default: "" } };
      },
      addNodeView() {
        return ReactNodeViewRenderer(CodeView);
      },
    }),
    WikiPageLink.configure({ companyId: options.companyId ?? "" }),
    WikiArtifactRef.configure({ companyId: options.companyId ?? "" }),
    TaskList,
    TaskItem.configure({ nested: true }),
    TableKit.configure({ table: { resizable: true } }),
    Image.extend({
      addAttributes() {
        return { ...this.parent?.(), display: { default: "card" } };
      },
      addNodeView() {
        return ReactNodeViewRenderer(MediaView);
      },
    }).configure({ allowBase64: false }),
    Highlight,
    TextAlign.configure({ types: ["heading", "paragraph"] }),
    Placeholder.configure({
      placeholder: "Start writing…",
    }),
    BlockIdentities(options.ids, options.reserve),
    container("callout", "aside", "block+", "pc-block-callout"),
    container(
      "toggle",
      "details",
      "toggleSummary toggleContent",
      "pc-block-toggle",
    ),
    container(
      "toggleSummary",
      "summary",
      "inline*",
      "pc-block-toggle-summary",
      "",
    ),
    container("toggleContent", "div", "block+", "pc-block-toggle-content", ""),
    container("columns", "div", "column{2,4}", "pc-block-columns"),
    container("column", "div", "block+", "pc-block-column", ""),
    Node.create({
      name: "mermaid",
      group: "block",
      content: "text*",
      marks: "",
      code: true,
      defining: true,
      draggable: true,
      parseHTML() {
        return [{ tag: 'pre[data-block-type="mermaid"]' }];
      },
      renderHTML({ HTMLAttributes }) {
        return [
          "pre",
          mergeAttributes(HTMLAttributes, { "data-block-type": "mermaid" }),
          0,
        ];
      },
      addNodeView() {
        return ReactNodeViewRenderer(MermaidView);
      },
    }),
    Node.create({
      name: "equation",
      group: "block",
      atom: true,
      draggable: true,
      addAttributes() {
        return { source: { default: "E = mc^2" } };
      },
      parseHTML() {
        return [{ tag: 'div[data-block-type="equation"]' }];
      },
      renderHTML({ HTMLAttributes }) {
        return [
          "div",
          mergeAttributes(HTMLAttributes, { "data-block-type": "equation" }),
        ];
      },
      addNodeView() {
        return ReactNodeViewRenderer(EquationView);
      },
    }),
    Node.create({
      name: "artifactReference",
      group: "block",
      atom: true,
      draggable: true,
      addAttributes() {
        return {
          versionId: { default: "" },
          artifactId: { default: "" },
          format: { default: "pinned" },
          blockTargetId: { default: "" },
          label: { default: "" },
          caption: { default: "" },
          display: { default: "card" },
        };
      },
      parseHTML() {
        return [{ tag: 'div[data-block-type="artifactReference"]' }];
      },
      renderHTML({ HTMLAttributes }) {
        return [
          "div",
          mergeAttributes(HTMLAttributes, {
            "data-block-type": "artifactReference",
          }),
        ];
      },
      addNodeView() {
        return ReactNodeViewRenderer(ReferenceView);
      },
    }),
    ...[
      "taskReference",
      "taskCollection",
      "outputCollection",
      "workflowReference",
    ].map((name) =>
      Node.create({
        name,
        group: "block",
        atom: true,
        draggable: true,
        addAttributes() {
          return {
            workflowKey: { default: "" },
            capturedWorkflow: { default: null },
            taskId: { default: "" },
            filter: { default: "all" },
            display: {
              default: name === "workflowReference" ? "card" : "list",
            },
            capturedTasks: { default: null },
            capturedOutputs: { default: null },
            capturedAt: { default: null },
          };
        },
        parseHTML() {
          return [{ tag: `div[data-block-type="${name}"]` }];
        },
        renderHTML({ HTMLAttributes }) {
          return [
            "div",
            mergeAttributes(HTMLAttributes, { "data-block-type": name }),
          ];
        },
        addNodeView() {
          return ReactNodeViewRenderer(
            name === "outputCollection"
              ? OutputCollectionBlock
              : name === "workflowReference"
                ? WorkflowBlock
                : WorkBlock,
          );
        },
      }),
    ),
    ...["fileBlock", "audioBlock", "videoBlock"].map((name) =>
      Node.create({
        name,
        group: "block",
        atom: true,
        draggable: true,
        addAttributes() {
          return {
            src: { default: "" },
            label: { default: "" },
            display: { default: "card" },
          };
        },
        parseHTML() {
          return [{ tag: `div[data-block-type="${name}"]` }];
        },
        renderHTML({ HTMLAttributes }) {
          return [
            "div",
            mergeAttributes(HTMLAttributes, { "data-block-type": name }),
          ];
        },
        addNodeView() {
          return ReactNodeViewRenderer(MediaView);
        },
      }),
    ),
    Node.create({
      name: "tableOfContents",
      group: "block",
      atom: true,
      parseHTML() {
        return [{ tag: 'nav[data-block-type="tableOfContents"]' }];
      },
      renderHTML({ HTMLAttributes }) {
        return [
          "nav",
          mergeAttributes(HTMLAttributes, {
            "data-block-type": "tableOfContents",
          }),
        ];
      },
      addNodeView() {
        return ReactNodeViewRenderer(OutlineView);
      },
    }),
  ];
}

export function blockText(block: Block): string {
  return (
    block.text ??
    (block.content ?? [])
      .map(blockText)
      .join(block.type === "paragraph" || block.type === "heading" ? "" : "\n")
  );
}
export function findBlock(
  root: Block | undefined,
  id: string,
): Block | undefined {
  if (!root || !id) return undefined;
  if (root.attrs?.blockId === id) return root;
  for (const child of root.content ?? []) {
    const found = findBlock(child, id);
    if (found) return found;
  }
}

// Some addressable children cannot appear directly under a ProseMirror document.
// Their preview keeps the target identity while supplying only the required shell.
function blockPreviewDocument(target?: Block): Block {
  if (!target) return { type: "doc", content: [{ type: "paragraph" }] };
  if (target.type === "doc") return target;
  if (target.type === "column" || target.type === "toggleContent")
    return { type: "doc", content: target.content };
  let block = target;
  if (target.type === "toggleSummary") block = { ...target, type: "paragraph" };
  if (target.type === "listItem")
    block = { type: "bulletList", content: [target] };
  if (target.type === "taskItem")
    block = { type: "taskList", content: [target] };
  if (target.type === "tableCell" || target.type === "tableHeader")
    block = { type: "tableRow", content: [target] };
  if (block.type === "tableRow") block = { type: "table", content: [block] };
  return { type: "doc", content: [block] };
}

function referencedSection(root: Block, id: string): Block | undefined {
  if (root.attrs?.blockId === id) return root;
  const children = root.content ?? [];
  const index = children.findIndex((child) => child.attrs?.blockId === id);
  if (index >= 0) {
    const target = children[index];
    if (target.type !== "heading") return target;
    const remaining = children.slice(index);
    const end = remaining.findIndex(
      (child, i) =>
        i > 0 &&
        child.type === "heading" &&
        Number(child.attrs?.level) <= Number(target.attrs?.level),
    );
    return {
      type: "doc",
      content: end < 0 ? remaining : remaining.slice(0, end),
    };
  }
  for (const child of children) {
    const found = referencedSection(child, id);
    if (found) return found;
  }
}

export function BlockDocument({
  content,
  scope,
  blockId,
  focusId,
  live = false,
  mode = "live",
  showOutline = false,
}: {
  content: Block;
  scope: DocumentScope;
  blockId?: string;
  focusId?: string;
  live?: boolean;
  mode?: "live" | "comparison" | "snapshot";
  showOutline?: boolean;
}) {
  const navigate = useNavigate();
  const target = useMemo(
    () => (blockId ? referencedSection(content, blockId) : content),
    [content, blockId],
  );
  const body = useMemo(() => blockPreviewDocument(target), [target]);
  const editor = useEditor(
    {
      extensions: blockExtensions({ companyId: scope.companyId, navigate }),
      content: body as JSONContent,
      editable: false,
      immediatelyRender: false,
    },
    [body],
  );
  useEffect(() => {
    if (!editor || !focusId) return;
    const frame = requestAnimationFrame(() =>
      editor.view.dom
        .querySelector(`[data-block-id="${CSS.escape(focusId)}"]`)
        ?.scrollIntoView({ block: "center" }),
    );
    return () => cancelAnimationFrame(frame);
  }, [editor, focusId]);
  if (blockId && !target)
    return <p>This content was removed from this version.</p>;
  return (
    <BlockScopeProvider value={scope} content={body}>
      <WorkBlockScope.Provider value={"projectId" in scope ? scope : null}>
        <WorkBlockMode.Provider value={mode}>
          <BlockLiveProvider value={live || mode === "comparison"}>
            {showOutline && <DocumentOutline editor={editor} />}
            <EditorContent
              className="pc-block-editor is-readonly"
              editor={editor}
            />
          </BlockLiveProvider>
        </WorkBlockMode.Provider>
      </WorkBlockScope.Provider>
    </BlockScopeProvider>
  );
}
