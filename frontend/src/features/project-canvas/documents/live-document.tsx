import { SourceEditor } from "./source-editor";
import { ScenarioCapture } from "./scenario-capture";
import { useCanInvokeDocumentAgents } from "./use-document-agents";
import {
  DocumentWritingPrompt,
  writingActions,
  type WritingRequest,
  type WritingAction,
} from "./document-writing";
import { DocumentOutline } from "./document-outline";
import { toast } from "@/shared/ui/sonner";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { documentScopeKey } from "./document-scope";
import { useNavigate } from "react-router";
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import {
  EditorContent,
  useEditor,
  useEditorState,
  type Editor,
  type JSONContent,
} from "@tiptap/react";
import Collaboration from "@tiptap/extension-collaboration";
import CollaborationCaret from "@tiptap/extension-collaboration-caret";
import { BubbleMenu } from "@tiptap/react/menus";
import { NodeSelection } from "@tiptap/pm/state";
import { documentBlockActions } from "./document-block-actions";
import { blocks, paragraph } from "./document-block-commands";
import { DocumentBlockMenu, handleBlockMenuKey } from "./document-block-menu";
import { documentCapabilities } from "./document-capabilities";
import {
  CircleAlert,
  Bold,
  Italic,
  Undo2,
  RotateCw,
  Download,
  ArrowUp,
  ArrowDown,
  Copy,
  Trash2,
  Link2,
  Plus,
  GripVertical,
  MoreHorizontal,
  MessageAdd,
  Code,
  Check,
  LoaderCircle,
  ShieldCheck,
} from "@/shared/ui/icons";
import {
  blockExtensions,
  BlockScopeProvider,
  BlockLiveProvider,
} from "./block-editor-schema";
import { useBlockDocument } from "./use-block-document";
import { downloadDocument } from "./document-export";
import { supportsDocumentBlock, type DocumentScope } from "./document-scope";
import { WikiLinkSuggestions } from "../../company-wiki/wiki-link-suggestions";
import {
  DocumentBlockDetails,
  PromoteSectionDialog,
  type SourceState,
} from "./document-block-details";
import type { ReviewAnchor } from "../review/annotation-overlay";
import { DocumentTitle } from "./document-title";
import { DocumentMentions } from "./document-mentions";
import { WorkBlockScope } from "./work-blocks";

export function LiveDocument({
  scope,
  versionId,
  onPublish,
  onDirtyChange,
  onPlaceBlock,
  focusId,
  readOnly = false,
  title,
  onRename,
  titleMaxLength,
  titleContent,
  onCommentSelection,
  onAgentThread,
  loadingContent,
  onReady,
  publishLabel = "Publish changes",
  quiet = false,
  pendingChanges = false,
  pendingError = "",
  statusContainer,
  actionsContainer,
  onEditor,
  onAction,
  sources = [],
}: {
  scope: DocumentScope;
  versionId: string;
  readOnly?: boolean;
  onDirtyChange: (dirty: boolean) => void;
  onPublish?: (pageId: string) => Promise<void>;
  onPlaceBlock?: (blockId: string) => Promise<void>;
  focusId?: string;
  title: string;
  onRename?: (title: string, previousTitle: string) => Promise<void>;
  titleMaxLength?: number;
  titleContent?: ReactNode;
  onCommentSelection?: (anchor: ReviewAnchor) => void;
  onAgentThread?: (threadId: string) => void;
  loadingContent?: ReactNode;
  onReady?: () => void;
  publishLabel?: string;
  quiet?: boolean;
  pendingChanges?: boolean;
  pendingError?: string;
  statusContainer?: HTMLElement | null;
  actionsContainer?: HTMLElement | null;
  onAction?: () => void;
  sources?: SourceState[];
  /** Receives the live editor so a host can render its own formatting bar. */
  onEditor?: (editor: Editor | null) => void;
}) {
  const state = useBlockDocument(scope, versionId, readOnly);
  useEffect(() => {
    if ((state.connection && state.ready) || state.error) onReady?.();
  }, [state.connection, state.ready, state.error, onReady]);
  const [saving, setSaving] = useState(false),
    [error, setError] = useState("");
  const [titlePending, setTitlePending] = useState(false);
  const [titleError, setTitleError] = useState("");
  const saveError = state.error || titleError || pendingError;
  const pending = titlePending || pendingChanges;
  const errorToastId = useId();
  const reloadRef = useRef(state.reload);
  reloadRef.current = state.reload;
  const reloadSavedDraft = useCallback(() => {
    if (
      window.confirm(
        "Reload the saved draft? Unsaved edits in this tab will be discarded. Export the native document first if you need to keep them.",
      )
    )
      reloadRef.current();
  }, []);
  const documentError = saveError || error;
  useEffect(() => {
    if (!documentError) return;
    toast.error("Document changes couldn't be saved", {
      id: errorToastId,
      description: documentError,
      duration: Infinity,
      closeButton: true,
      action: {
        label: "Reload saved draft",
        onClick: reloadSavedDraft,
      },
    });
    return () => {
      toast.dismiss(errorToastId);
    };
  }, [documentError, errorToastId, reloadSavedDraft]);

  useEffect(
    () => onDirtyChange(!state.saved || pending),
    [state.saved, pending, onDirtyChange],
  );
  const publish = async () => {
    if (pending || !onPublish) return;
    setSaving(true);
    setError("");
    try {
      if (!(await state.flush.current()))
        throw new Error(
          "Your latest changes haven't reached the server yet. Please retry when connected.",
        );
      await onPublish(state.pageId.current);
    } catch (reason) {
      setError(
        reason instanceof Error
          ? reason.message
          : "Could not publish these changes.",
      );
    } finally {
      setSaving(false);
    }
  };
  const commentOnPage = async (anchor: ReviewAnchor) => {
    setError("");
    try {
      if (!(await state.flush.current()))
        throw new Error("Page changes are not saved");
      onCommentSelection?.(anchor);
    } catch {
      setError(
        "Your latest changes haven't saved. Reconnect before commenting on this selection.",
      );
    }
  };
  const status = (
    <header
      className={`pc-document-status${quiet ? " pc-document-status--quiet" : ""}`}
      data-active={
        !state.saved ||
        pending ||
        !!saveError ||
        !!state.notice ||
        state.people.length > 0
      }
    >
      <span role="status" title={saveError || state.notice || undefined}>
        {saveError ? (
          <CircleAlert size={14} />
        ) : state.saved && !pending ? (
          <Check size={14} />
        ) : (
          <LoaderCircle size={14} className="pc-spin" />
        )}
        <span className="pc-document-status-text">
          {saveError
            ? state.saved && !pending
              ? "Connection issue"
              : "Not saved"
            : state.saved && !pending
              ? "Saved"
              : "Saving…"}
          {state.notice && <> · {state.notice}</>}
        </span>
      </span>
      {documentError && (
        <button type="button" onClick={reloadSavedDraft}>
          Reload saved draft
        </button>
      )}
      <span className="pc-document-people">
        {state.people.map((person) => (
          <span key={person.sessionId} title={person.name}>
            {person.name
              .trim()
              .split(/\s+/)
              .slice(0, 2)
              .map((part) => part[0])
              .join("")}
          </span>
        ))}
      </span>
      {state.canEdit && !quiet && onPublish && (
        <button
          disabled={saving || !state.saved || pending}
          onClick={() => void publish()}
        >
          {saving ? "Preparing…" : publishLabel}
        </button>
      )}
    </header>
  );
  return (
    <section className="pc-live-document">
      {statusContainer === undefined
        ? status
        : statusContainer && createPortal(status, statusContainer)}
      {state.ready && state.reactiveReadOnly && (
        <p role="status" className="px-4 py-2 text-sm text-workspace-muted">
          Reactive editing is temporarily unavailable. You can still read and
          export this document.
        </p>
      )}
      {state.connection && state.ready ? (
        <NativeEditor
          key={state.connection.doc.guid}
          scope={scope}
          connection={state.connection}
          canEdit={state.canEdit}
          intelligentUIAuthoringEnabled={state.intelligentUIAuthoringEnabled}
          onPlaceBlock={onPlaceBlock}
          focusId={focusId}
          title={title}
          onRename={
            onRename
              ? async (value, previous) => {
                  if (!(await state.flush.current()))
                    throw new Error(
                      "The Page content is still waiting to save.",
                    );
                  await onRename(value, previous);
                }
              : undefined
          }
          onTitlePendingChange={setTitlePending}
          onTitleErrorChange={setTitleError}
          actionsContainer={actionsContainer}
          onAction={onAction}
          titleMaxLength={titleMaxLength}
          titleContent={titleContent}
          sources={sources}
          onEditor={onEditor}
          getAcknowledgedSequence={() => state.getSequence.current()}
          pageId={state.pageId.current}
          onAgentThread={onAgentThread}
          versionId={versionId}
          flush={() => state.flush.current()}
          onComment={
            onCommentSelection
              ? (anchor) => void commentOnPage(anchor)
              : undefined
          }
        />
      ) : (
        (loadingContent ?? <p role="status">Opening document…</p>)
      )}
    </section>
  );
}

function NativeEditor({
  scope,
  connection,
  canEdit,
  intelligentUIAuthoringEnabled,
  onTitlePendingChange,
  onTitleErrorChange,
  actionsContainer,
  onAction,
  onPlaceBlock,
  focusId,
  title,
  onRename,
  titleMaxLength,
  titleContent,
  onComment,
  sources,
  versionId,
  onEditor,
  flush,
  pageId,
  onAgentThread,
  getAcknowledgedSequence,
}: {
  scope: DocumentScope;
  connection: NonNullable<ReturnType<typeof useBlockDocument>["connection"]>;
  canEdit: boolean;
  intelligentUIAuthoringEnabled: boolean;
  onTitlePendingChange: (pending: boolean) => void;
  onTitleErrorChange: (error: string) => void;
  actionsContainer?: HTMLElement | null;
  onAction?: () => void;
  onPlaceBlock?: (blockId: string) => Promise<void>;
  focusId?: string;
  title: string;
  onRename?: (title: string, previousTitle: string) => Promise<void>;
  titleMaxLength?: number;
  titleContent?: ReactNode;
  onComment?: (anchor: ReviewAnchor) => void;
  sources: SourceState[];
  versionId: string;
  onEditor?: (editor: Editor | null) => void;
  flush: () => Promise<boolean>;
  pageId: string;
  getAcknowledgedSequence: () => number;
  onAgentThread?: (threadId: string) => void;
}) {
  const navigate = useNavigate();
  const { userId } = useSessionSnapshot();
  const editable = canEdit;
  const canInvokeAgents = useCanInvokeDocumentAgents(
    "projectId" in scope || "pageId" in scope
      ? { ...scope, pageId }
      : undefined,
  );
  const canWriteWithAgent =
    editable && canInvokeAgents && !!pageId && !!onAgentThread;
  const [showCapture, setShowCapture] = useState(false);
  const [sourceBlock, setSourceBlock] = useState<string>();
  const [writing, setWriting] = useState<WritingRequest | null>(null);
  const [writingAgent, setWritingAgent] = useState("");
  const commentShortcut = /Mac|iPhone|iPad/.test(navigator.platform)
    ? "⌘⇧M"
    : "Ctrl+Shift+M";
  const [slash, setSlash] = useState<{
    from: number;
    to: number;
    query: string;
  } | null>(null);
  const [choice, setChoice] = useState(0);
  const [showBlocks, setShowBlocks] = useState(false);
  const [urlMode, setURLMode] = useState<"link" | "image" | null>(null);
  const [url, setURL] = useState("");
  const [imageAlt, setImageAlt] = useState("");
  const [actionError, setActionError] = useState("");
  const [documentContent, setDocumentContent] = useState<JSONContent>();
  const [find, setFind] = useState("");
  const scrollRoot = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<{
    id: string;
    top: number;
    left: number;
  } | null>(null);
  const [blockMenu, setBlockMenu] = useState(false);
  const [detailsBlock, setDetailsBlock] = useState("");
  const [promoteBlock, setPromoteBlock] = useState("");
  const [menuPosition, setMenuPosition] = useState({ top: 0, left: 0 });
  const commandRef = useRef<{
    items: typeof blocks;
    pick: (index: number) => void;
    choice: number;
    open: boolean;
  }>({ items: [], pick: () => {}, choice: 0, open: false });
  const editor = useEditor(
    {
      extensions: [
        documentCapabilities(scope, setActionError),
        ...blockExtensions({
          navigate,
          collaborative: true,
          companyId: scope.companyId,
          ids: connection.ids,
          reserve: connection.reserve,
        }),
        Collaboration.configure({ document: connection.doc, field: "content" }),
        CollaborationCaret.configure({
          provider: { awareness: connection.awareness },
          user: { name: "You", color: "#b46d45" },
        }),
      ],
      editable,
      immediatelyRender: false,
      onSelectionUpdate: ({ editor: e }) => updateSlash(e),
      onUpdate: ({ editor: e }) => {
        updateSlash(e);
        setDocumentContent(e.getJSON());
      },
      onCreate: ({ editor: e }) => setDocumentContent(e.getJSON()),
      editorProps: {
        attributes: {
          "aria-label": "Page content",
          spellcheck: "true",
        },
        handleKeyDown: (_view, event) => {
          return handleBlockMenuKey(
            event,
            commandRef.current,
            setChoice,
            () => {
              setSlash(null);
              setShowBlocks(false);
            },
          );
        },
      },
    },
    [connection],
  );
  function updateSlash(e: Editor) {
    const { selection } = e.state;
    if (
      !selection.empty ||
      !e.isEditable ||
      e.isActive("codeBlock") ||
      e.isActive("mermaid")
    ) {
      setSlash(null);
      return;
    }
    const text = selection.$from.parent.textBetween(
      0,
      selection.$from.parentOffset,
      "\0",
      "\0",
    );
    const match = /(?:^|\s)\/([a-z0-9 ]{0,32})$/i.exec(text);
    if (match) {
      setSlash({
        from: selection.from - match[1].length - 1,
        to: selection.from,
        query: match[1],
      });
      setChoice(0);
    } else setSlash(null);
  }
  useEffect(() => {
    editor?.setEditable(editable);
    if (!editable) {
      setSlash(null);
      setShowBlocks(false);
      setBlockMenu(false);
      setURLMode(null);
      setHover(null);
    }
  }, [editor, editable]);
  useEffect(() => {
    if (!editor || !focusId) return;
    const frame = requestAnimationFrame(() =>
      editor.view.dom
        .querySelector(`[data-block-id="${CSS.escape(focusId)}"]`)
        ?.scrollIntoView({ block: "center" }),
    );
    return () => cancelAnimationFrame(frame);
  }, [editor, focusId]);
  useEffect(() => {
    if (!editor || !onEditor) return;
    onEditor(editor);
    return () => onEditor(null);
  }, [editor, onEditor]);
  const selection = useEditorState({
    editor,
    selector: ({ editor: e }) =>
      e
        ? {
            from: e.state.selection.from,
            depth: e.state.selection.$from.depth,
            bold: e.isActive("bold"),
            italic: e.isActive("italic"),
            table: e.isActive("table"),
            code: e.isActive("codeBlock"),
            language: e.getAttributes("codeBlock").language as
              string | undefined,
          }
        : null,
  });
  useEffect(() => {
    if (!editor || (!slash && !showBlocks && !urlMode)) return;
    const root = scrollRoot.current;
    if (!root) return;
    const position = () => {
      const rect = root.getBoundingClientRect();
      const cursor = editor.view.coordsAtPos(editor.state.selection.from);
      const below = cursor.bottom + 8;
      setMenuPosition({
        top:
          Math.max(rect.top + 8, Math.min(below, rect.bottom - 320)) -
          rect.top +
          root.scrollTop,
        left: Math.max(12, Math.min(cursor.left - rect.left, rect.width - 300)),
      });
    };
    if (slash || showBlocks || urlMode) position();
    root.addEventListener("scroll", position);
    window.addEventListener("resize", position);
    return () => {
      root.removeEventListener("scroll", position);
      window.removeEventListener("resize", position);
    };
  }, [editor, slash, showBlocks, urlMode]);
  useEffect(() => {
    if (!slash && !showBlocks) return;
    scrollRoot.current
      ?.querySelector('.pc-slash-menu [aria-selected="true"]')
      ?.scrollIntoView({ block: "nearest" });
  }, [choice, slash, showBlocks]);
  if (!editor) return null;
  const startWriting = (action: WritingAction, e: Editor) => {
    if (!canWriteWithAgent) return;
    const anchor = documentBlockActions(e, {
      onError: setActionError,
    }).selectionAnchor();
    if (!anchor) return;
    if (action.id === "ask") {
      setWriting({ action, anchor });
      return;
    }
    const { $from, $to, empty } = e.state.selection;
    if (
      !$from.sameParent($to) ||
      !$from.parent.isTextblock ||
      !["paragraph", "heading", "codeBlock"].includes($from.parent.type.name) ||
      $from.parent.content.content.some(
        (node) => !node.isText && node.type.name !== "hardBreak",
      )
    ) {
      setActionError(
        "Select text within one paragraph, heading, or code block to use a writing action.",
      );
      return;
    }
    const blockId = $from.parent.attrs.blockId as string | undefined;
    if (!blockId) {
      setActionError("Wait for this paragraph to save, then try again.");
      return;
    }
    const text = $from.parent.textBetween(
      0,
      $from.parent.content.size,
      "",
      "\n",
    );
    const insert =
      action.id === "generate" ||
      action.id === "continue" ||
      action.id === "summarize";
    setWriting({
      action,
      anchor,
      target: {
        blockId,
        from: empty && !insert ? 0 : $from.parentOffset,
        to: empty && !insert ? $from.parent.content.size : $to.parentOffset,
        text,
      },
    });
  };
  const writingItems: typeof blocks = canWriteWithAgent
    ? writingActions.map((action) => ({
        type: "agentWriting",
        label: action.label,
        keywords: `agent ai ${action.id}`,
        run: (e) => {
          startWriting(action, e);
          return true;
        },
      }))
    : [];
  const items = [...writingItems, ...blocks].filter(
    (block) =>
      supportsDocumentBlock(scope, block.type) &&
      (!slash?.query ||
        `${block.label} ${block.keywords}`
          .toLowerCase()
          .includes(slash.query.toLowerCase())),
  );
  const pick = (index: number) => {
    const block = items[index];
    if (!block || !editor.isEditable) return;
    if (slash) editor.commands.deleteRange({ from: slash.from, to: slash.to });
    block.run(editor);
    setSlash(null);
    setShowBlocks(false);
  };
  commandRef.current = { items, pick, choice, open: !!slash || showBlocks };
  const actions = documentBlockActions(editor, {
    identities: connection,
    onComment,
    onError: setActionError,
  });
  const { top, selectedBlockId, commentSelection } = actions;
  const selectHoveredBlock = () => {
    if (actions.selectBlock(hover?.id)) return true;
    setBlockMenu(false);
    return false;
  };
  const blockAction = (action: "up" | "down" | "duplicate" | "delete") => {
    if (!editable || !hover) return;
    actions.blockAction(hover.id, action);
    setBlockMenu(false);
    setHover(null);
  };
  const exportDocument = (html = false) => {
    let schemaVersion = 1;
    editor.state.doc.descendants((node) => {
      if (
        node.type.name.startsWith("ui") ||
        (node.type.name === "taskCollection" &&
          node.attrs.filter !== null &&
          typeof node.attrs.filter === "object")
      )
        schemaVersion = 2;
    });
    const value = html
      ? `<!doctype html><html><head><meta charset="utf-8"><title>Zenith document</title></head><body>${editor.getHTML()}</body></html>`
      : JSON.stringify({ schemaVersion, content: editor.getJSON() }, null, 2);
    downloadDocument(
      value,
      html ? "document.html" : "document.blocks.json",
      html ? "text/html" : "application/json",
    );
  };
  const locate = (query: string) => {
    setFind(query);
    if (!query) return;
    let pos: number | undefined;
    editor.state.doc.descendants((node, offset) => {
      if (pos !== undefined) return false;
      if (node.isText) {
        const at = node.text?.toLowerCase().indexOf(query.toLowerCase()) ?? -1;
        if (at >= 0) pos = offset + at;
      }
    });
    if (pos !== undefined)
      editor
        .chain()
        .setTextSelection({ from: pos, to: pos + query.length })
        .scrollIntoView()
        .run();
  };
  const documentActions = (
    <div
      className={
        actionsContainer === undefined
          ? "pc-document-menu-panel"
          : "pc-page-actions"
      }
      onClick={(event) => {
        if ((event.target as HTMLElement).closest("button")) {
          event.currentTarget.closest("details")?.removeAttribute("open");
          onAction?.();
        }
      }}
    >
      <button
        className="pc-icon-button"
        aria-label="Undo my changes"
        disabled={!editable}
        onClick={() => editor.chain().focus().undo().run()}
      >
        <Undo2 size={16} /> Undo
      </button>
      <button
        className="pc-icon-button"
        aria-label="Redo my changes"
        disabled={!editable}
        onClick={() => editor.chain().focus().redo().run()}
      >
        <RotateCw size={16} /> Redo
      </button>
      <label>
        Find in document
        <input
          aria-label="Find in document"
          placeholder="Search text…"
          value={find}
          onChange={(event) => locate(event.target.value)}
        />
      </label>
      <button
        disabled={!editable}
        onClick={() => {
          setShowBlocks(true);
          editor.commands.focus();
        }}
      >
        <Plus size={15} /> Insert…
      </button>
      <button
        disabled={!editable}
        onClick={() => {
          const current = top(),
            root = scrollRoot.current;
          if (!current || !root) return;
          const element = editor.view.nodeDOM(current.start);
          if (!(element instanceof HTMLElement)) return;
          const rect = element.getBoundingClientRect(),
            container = root.getBoundingClientRect();
          setHover({
            id: current.node.attrs.blockId,
            top: rect.top - container.top + root.scrollTop,
            left: Math.max(8, rect.left - container.left - 60),
          });
          setBlockMenu(true);
        }}
      >
        Selection options
      </button>
      {"projectId" in scope && (
        <button
          onClick={() => {
            const id = selectedBlockId();
            if (id) setDetailsBlock(id);
          }}
        >
          Sources and protection
        </button>
      )}
      {"projectId" in scope &&
        editable &&
        top()?.node.type.name === "heading" && (
          <button
            onClick={() => {
              const id = top()?.node.attrs.blockId;
              if (id) setPromoteBlock(String(id));
            }}
          >
            Turn section into a Page
          </button>
        )}
      <button
        disabled={!editable}
        onClick={() => {
          setURLMode("image");
          setURL("");
        }}
      >
        Insert image from URL
      </button>
      {editable && pageId && "projectId" in scope && (
        <>
          <button
            onClick={() => {
              const id = selectedBlockId();
              if (id) setSourceBlock(id);
            }}
          >
            Edit block source…
          </button>
          <button onClick={() => setShowCapture(true)}>
            Capture scenario…
          </button>
        </>
      )}
      <button onClick={() => exportDocument()}>
        <Download size={15} /> Export native document
      </button>
      <button onClick={() => exportDocument(true)}>Export HTML</button>
    </div>
  );
  return (
    <BlockScopeProvider value={scope} content={documentContent}>
      <WorkBlockScope.Provider value={"projectId" in scope ? scope : null}>
        <BlockLiveProvider value={true}>
          {sourceBlock && editable && pageId && "projectId" in scope && (
            <SourceEditor
              key={`${pageId}:${sourceBlock}`}
              scope={{ ...scope, pageId }}
              blockId={sourceBlock}
              intelligentUIAuthoringEnabled={intelligentUIAuthoringEnabled}
              editor={editor}
              flush={flush}
              getSequence={getAcknowledgedSequence}
              onClose={() => setSourceBlock(undefined)}
            />
          )}
          {showCapture && editable && pageId && "projectId" in scope && (
            <ScenarioCapture
              key={pageId}
              scope={{ ...scope, pageId }}
              editor={editor}
              flush={flush}
              getSequence={getAcknowledgedSequence}
              onClose={() => setShowCapture(false)}
            />
          )}
          {writing &&
            canWriteWithAgent &&
            ("projectId" in scope || "pageId" in scope) && (
              <DocumentWritingPrompt
                scope={{ ...scope, pageId }}
                request={writing}
                agentId={writingAgent}
                onAgentChange={setWritingAgent}
                flush={flush}
                onClose={() => {
                  setWriting(null);
                  editor.commands.focus();
                }}
                onSubmitted={(id) => onAgentThread?.(id)}
              />
            )}
          {(editable || onComment) && (
            <BubbleMenu
              editor={editor}
              options={{ placement: "top", offset: 8 }}
              className="pc-selection-toolbar"
              shouldShow={({ state }) =>
                (!state.selection.empty || editor.isActive("link")) &&
                !urlMode &&
                !(state.selection instanceof NodeSelection)
              }
            >
              {editable && (
                <>
                  <button
                    aria-label="Bold"
                    aria-pressed={selection?.bold}
                    onClick={() => editor.chain().focus().toggleBold().run()}
                  >
                    <Bold size={16} />
                  </button>
                  <button
                    aria-label="Italic"
                    aria-pressed={selection?.italic}
                    onClick={() => editor.chain().focus().toggleItalic().run()}
                  >
                    <Italic size={16} />
                  </button>
                  <button
                    aria-label="Inline code"
                    onClick={() => editor.chain().focus().toggleCode().run()}
                  >
                    <Code size={16} />
                  </button>
                  <button
                    onClick={() =>
                      editor.chain().focus().toggleHighlight().run()
                    }
                  >
                    Highlight
                  </button>
                  <button
                    aria-label="Add link"
                    onClick={() => {
                      editor.commands.extendMarkRange("link");
                      setURLMode("link");
                      setURL(editor.getAttributes("link").href || "");
                    }}
                  >
                    <Link2 size={16} />
                  </button>
                  {editor.isActive("link") && (
                    <button
                      aria-label="Remove link"
                      onClick={() =>
                        editor
                          .chain()
                          .focus()
                          .extendMarkRange("link")
                          .unsetLink()
                          .run()
                      }
                    >
                      Remove link
                    </button>
                  )}
                </>
              )}
              {editable && writingItems.length > 0 && (
                <select
                  aria-label="Agent writing actions"
                  value=""
                  onChange={(event) => {
                    const action = writingActions.find(
                      (item) => item.id === event.target.value,
                    );
                    if (action) startWriting(action, editor);
                  }}
                >
                  <option value="" disabled>
                    Write with agent…
                  </option>
                  {writingActions.map((action) => (
                    <option value={action.id} key={action.id}>
                      {action.label}
                    </option>
                  ))}
                </select>
              )}
              {onComment && (
                <button
                  title={`Comment (${commentShortcut})`}
                  onClick={commentSelection}
                >
                  <MessageAdd size={16} /> Comment
                </button>
              )}
            </BubbleMenu>
          )}
          {actionError && <p role="alert">{actionError}</p>}
          <div className="pc-native-body">
            <div
              className="pc-block-scroll"
              ref={scrollRoot}
              onPointerLeave={() => {
                if (!blockMenu) setHover(null);
              }}
              onDragOver={(event) => {
                if (
                  editable &&
                  event.dataTransfer.types.includes(
                    "application/x-zenith-block-id",
                  )
                ) {
                  event.preventDefault();
                  event.dataTransfer.dropEffect =
                    event.altKey || event.ctrlKey ? "copy" : "move";
                }
              }}
              onDrop={(event) => {
                const id = event.dataTransfer.getData(
                  "application/x-zenith-block-id",
                );
                if (!editable || !id) return;
                event.preventDefault();
                if (
                  actions.dropBlock(
                    id,
                    {
                      left: event.clientX,
                      top: event.clientY,
                    },
                    event.altKey || event.ctrlKey,
                  )
                ) {
                  setHover(null);
                  setBlockMenu(false);
                }
              }}
              onScroll={() => {
                setHover(null);
                setBlockMenu(false);
              }}
              onKeyDown={(event) => {
                if (
                  onComment &&
                  (event.metaKey || event.ctrlKey) &&
                  event.shiftKey &&
                  !event.altKey &&
                  event.code === "KeyM" &&
                  (editor.view.dom.contains(event.target as Node) || blockMenu)
                ) {
                  event.preventDefault();
                  commentSelection();
                  setBlockMenu(false);
                  return;
                }
                if (
                  event.key === "Escape" &&
                  (slash || showBlocks || blockMenu || urlMode)
                ) {
                  event.preventDefault();
                  event.stopPropagation();
                  setSlash(null);
                  setShowBlocks(false);
                  setBlockMenu(false);
                  setURLMode(null);
                  editor.commands.focus();
                }
              }}
              onPointerMove={(event) => {
                if (!editable || blockMenu) return;
                const root = scrollRoot.current;
                const target = event.target as HTMLElement;
                let block = target.closest<HTMLElement>("[data-block-id]");
                if (!root || !block || !editor.view.dom.contains(block)) return;
                while (
                  block.parentElement &&
                  block.parentElement !== editor.view.dom
                )
                  block = block.parentElement;
                const id = block.dataset.blockId;
                if (!id) return;
                const rect = block.getBoundingClientRect(),
                  container = root.getBoundingClientRect();
                const next = {
                  id,
                  top: rect.top - container.top + root.scrollTop,
                  left: Math.max(4, rect.left - container.left - 60),
                };
                setHover((current) =>
                  current?.id === next.id &&
                  current.top === next.top &&
                  current.left === next.left
                    ? current
                    : next,
                );
              }}
            >
              <DocumentOutline editor={editor} />
              <header className="pc-document-heading">
                {actionsContainer === undefined ? (
                  <div className="pc-editor-actions">
                    <details className="pc-document-menu">
                      <summary
                        className="pc-icon-button"
                        aria-label="Document options"
                      >
                        <MoreHorizontal size={18} />
                      </summary>
                      {documentActions}
                    </details>
                  </div>
                ) : (
                  actionsContainer &&
                  createPortal(documentActions, actionsContainer)
                )}
                {titleContent ? (
                  !editable ? (
                    <h1 className="pc-document-title">{title}</h1>
                  ) : (
                    titleContent
                  )
                ) : (
                  <DocumentTitle
                    title={title}
                    onRename={canEdit ? onRename : undefined}
                    onPendingChange={onTitlePendingChange}
                    onSaveErrorChange={onTitleErrorChange}
                    recoveryKey={`${userId}:page-title:${documentScopeKey(scope)}:${versionId}`}
                    readOnly={!editable}
                    maxLength={titleMaxLength}
                  />
                )}
              </header>
              {"projectId" in scope ? (
                <>
                  <EditorContent
                    className="pc-block-editor"
                    editor={editor}
                    onPointerDown={() => setBlockMenu(false)}
                  />
                  <DocumentMentions editor={editor} scope={scope} />
                </>
              ) : "wikiPageId" in scope ? (
                <WikiLinkSuggestions
                  editor={editor}
                  companyId={scope.companyId}
                  pageId={scope.wikiPageId}
                >
                  <EditorContent
                    className="pc-block-editor"
                    editor={editor}
                    onPointerDown={() => setBlockMenu(false)}
                  />
                </WikiLinkSuggestions>
              ) : (
                <EditorContent
                  className="pc-block-editor"
                  editor={editor}
                  onPointerDown={() => setBlockMenu(false)}
                />
              )}
              {editable && hover && (
                <div
                  className="pc-block-gutter"
                  style={{ top: hover.top, left: hover.left }}
                >
                  <button
                    title="Insert below"
                    aria-label="Insert below"
                    onMouseDown={(event) => event.preventDefault()}
                    onClick={() => {
                      if (!selectHoveredBlock()) return;
                      const current = top();
                      if (!current) return;
                      const pos = current.start + current.node.nodeSize;
                      editor
                        .chain()
                        .insertContentAt(pos, paragraph())
                        .setTextSelection(pos + 1)
                        .focus()
                        .run();
                      setShowBlocks(true);
                      setBlockMenu(false);
                    }}
                  >
                    <Plus size={16} />
                  </button>
                  <button
                    title="Drag to move · Alt/Ctrl-drag to copy · click for actions"
                    aria-label="Move, copy or edit selection"
                    aria-expanded={blockMenu}
                    draggable
                    onDragStart={(event) => {
                      if (!selectHoveredBlock()) {
                        event.preventDefault();
                        return;
                      }
                      event.dataTransfer.setData(
                        "application/x-zenith-block-id",
                        hover.id,
                      );
                      event.dataTransfer.effectAllowed = "copyMove";
                    }}
                    onMouseDown={(event) => event.preventDefault()}
                    onClick={() => {
                      if (selectHoveredBlock()) setBlockMenu(!blockMenu);
                    }}
                  >
                    <GripVertical size={17} />
                  </button>
                </div>
              )}
              {urlMode && (
                <form
                  className="pc-block-url"
                  style={menuPosition}
                  onSubmit={(event) => {
                    event.preventDefault();
                    if (
                      !/^https?:\/\//i.test(url) &&
                      !(urlMode === "link" && /^mailto:/i.test(url))
                    ) {
                      setActionError("Enter an https:// URL.");
                      return;
                    }
                    if (urlMode === "image")
                      editor
                        .chain()
                        .focus()
                        .setImage({ src: url, alt: imageAlt })
                        .run();
                    else
                      editor
                        .chain()
                        .focus()
                        .extendMarkRange("link")
                        .setLink({ href: url })
                        .run();
                    setURLMode(null);
                    setActionError("");
                  }}
                >
                  <input
                    autoFocus
                    aria-label={urlMode === "image" ? "Image URL" : "Link URL"}
                    value={url}
                    onChange={(event) => setURL(event.target.value)}
                    placeholder="https://…"
                  />
                  {urlMode === "image" && (
                    <input
                      aria-label="Image description"
                      placeholder="Describe the image (optional)"
                      value={imageAlt}
                      onChange={(event) => setImageAlt(event.target.value)}
                    />
                  )}
                  {urlMode === "image" && (
                    <small>
                      External images can change. Use an uploaded artifact
                      reference for versioned media.
                    </small>
                  )}
                  <div className="flex justify-end gap-[8px]">
                    <button type="button" onClick={() => setURLMode(null)}>
                      Cancel
                    </button>
                    <button type="submit">Insert</button>
                  </div>
                </form>
              )}
              {(slash || showBlocks) && (
                <DocumentBlockMenu
                  items={items}
                  choice={choice}
                  pick={pick}
                  position={menuPosition}
                />
              )}
              {editable && blockMenu && hover && (
                <div
                  className="pc-block-actions"
                  style={{
                    top: hover.top + 28,
                    left: Math.max(8, hover.left),
                  }}
                  role="toolbar"
                  aria-label="Selection"
                >
                  <span>Selection</span>
                  {onComment && (
                    <button
                      className="pc-block-comment-action"
                      aria-keyshortcuts="Meta+Shift+M Control+Shift+M"
                      onMouseDown={(event) => event.preventDefault()}
                      onClick={() => {
                        onComment({ type: "block", blockId: hover.id });
                        setBlockMenu(false);
                      }}
                    >
                      <MessageAdd size={16} aria-hidden />
                      <span>Comment</span>
                      <kbd aria-hidden>{commentShortcut}</kbd>
                    </button>
                  )}
                  {"projectId" in scope && (
                    <button
                      onClick={() => {
                        if (selectHoveredBlock() && hover) {
                          setDetailsBlock(hover.id);
                          setBlockMenu(false);
                        }
                      }}
                    >
                      Sources and protection
                    </button>
                  )}
                  {"projectId" in scope &&
                    top()?.node.type.name === "heading" && (
                      <button
                        onClick={() => {
                          if (selectHoveredBlock() && hover) {
                            setPromoteBlock(hover.id);
                            setBlockMenu(false);
                          }
                        }}
                      >
                        Turn section into a Page
                      </button>
                    )}
                  <button
                    onClick={() => {
                      if (!selectHoveredBlock()) return;
                      const current = top();
                      if (!current) return;
                      editor
                        .chain()
                        .focus()
                        .updateAttributes(current.node.type.name, {
                          agentProtected: !current.node.attrs.agentProtected,
                        })
                        .run();
                    }}
                  >
                    <ShieldCheck size={14} />{" "}
                    {top()?.node.attrs.agentProtected
                      ? "Allow Agent edits"
                      : "Protect from Agent edits"}
                  </button>
                  <button
                    title="Move up"
                    aria-label="Move up"
                    onClick={() => blockAction("up")}
                  >
                    <ArrowUp size={14} /> Move up
                  </button>
                  <button
                    title="Move down"
                    aria-label="Move down"
                    onClick={() => blockAction("down")}
                  >
                    <ArrowDown size={14} /> Move down
                  </button>
                  <button
                    title="Duplicate"
                    aria-label="Duplicate"
                    onClick={() => blockAction("duplicate")}
                  >
                    <Copy size={14} /> Duplicate
                  </button>
                  <button
                    title="Delete"
                    aria-label="Delete"
                    onClick={() => blockAction("delete")}
                  >
                    <Trash2 size={14} /> Delete
                  </button>
                  <button
                    onClick={() =>
                      editor
                        .chain()
                        .focus()
                        .sinkListItem(
                          editor.isActive("taskItem") ? "taskItem" : "listItem",
                        )
                        .run()
                    }
                  >
                    Indent
                  </button>
                  <button
                    onClick={() =>
                      editor
                        .chain()
                        .focus()
                        .liftListItem(
                          editor.isActive("taskItem") ? "taskItem" : "listItem",
                        )
                        .run()
                    }
                  >
                    Outdent
                  </button>
                  {onPlaceBlock && (
                    <button
                      onClick={() => {
                        const blockId = selectedBlockId();
                        if (blockId)
                          void onPlaceBlock(blockId).catch(() =>
                            setActionError(
                              "Save the document and try placing this selection again.",
                            ),
                          );
                      }}
                    >
                      Place on canvas
                    </button>
                  )}
                  {selection?.code && (
                    <select
                      aria-label="Code language"
                      value={selection.language || ""}
                      onChange={(event) =>
                        editor
                          .chain()
                          .focus()
                          .updateAttributes("codeBlock", {
                            language: event.target.value,
                          })
                          .run()
                      }
                    >
                      {[
                        "",
                        "typescript",
                        "javascript",
                        "python",
                        "go",
                        "rust",
                        "sql",
                        "json",
                        "html",
                        "css",
                        "bash",
                      ].map((lang) => (
                        <option key={lang} value={lang}>
                          {lang || "Plain text"}
                        </option>
                      ))}
                    </select>
                  )}
                  {selection?.table && (
                    <>
                      <button
                        onClick={() =>
                          editor.chain().focus().addRowAfter().run()
                        }
                      >
                        Add row
                      </button>
                      <button
                        onClick={() =>
                          editor.chain().focus().addColumnAfter().run()
                        }
                      >
                        Add column
                      </button>
                      <button
                        onClick={() => editor.chain().focus().deleteRow().run()}
                      >
                        Delete row
                      </button>
                      <button
                        onClick={() =>
                          editor.chain().focus().deleteColumn().run()
                        }
                      >
                        Delete column
                      </button>
                      <button
                        onClick={() =>
                          editor.chain().focus().mergeOrSplit().run()
                        }
                      >
                        Merge / split
                      </button>
                    </>
                  )}
                </div>
              )}
            </div>
          </div>
          {"projectId" in scope && detailsBlock && (
            <DocumentBlockDetails
              editor={editor}
              scope={scope}
              blockId={detailsBlock}
              sources={sources}
              onClose={() => setDetailsBlock("")}
            />
          )}
          {"projectId" in scope && promoteBlock && (
            <PromoteSectionDialog
              editor={editor}
              scope={scope}
              versionId={versionId}
              blockId={promoteBlock}
              flush={flush}
              onClose={() => setPromoteBlock("")}
              onNotice={setActionError}
            />
          )}
        </BlockLiveProvider>
      </WorkBlockScope.Provider>
    </BlockScopeProvider>
  );
}
