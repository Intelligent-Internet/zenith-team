import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { useSearchParams } from "react-router";
import {
  Background,
  BackgroundVariant,
  ReactFlow,
  ReactFlowProvider,
  SelectionMode,
  useEdgesState,
  useNodesState,
  type ReactFlowInstance,
  type Edge,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Trash2, LockKeyhole } from "@/shared/ui/icons";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { toast } from "@/shared/ui/sonner";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";
import type { normalizeDrawing } from "@/shared/canvas/freehand";
import { downloadDocument } from "@/features/project-canvas/documents/document-export";
import {
  boardPath,
  boardScopeKey,
  useAttachWhiteboardFileMutation,
  useGetWhiteboardQuery,
  useWhiteboardCommandsMutation,
  type BoardScope,
  type BoardCommand,
  type BoardItem,
  type BoardEdge,
} from "./board-api";
import {
  BoardChangeTooLarge,
  BoardSceneSession,
  boardBatchLimit,
  geometryOf,
} from "./board-scene-session";
import { BoardNoteSession, type NoteDraft } from "./board-note-session";
import { deleteBoardDraft, readBoardDrafts } from "./board-drafts";
import { BoardHydration } from "./board-hydration";
import {
  BoardNode,
  BoardNodeContext,
  type WhiteboardFlowNode,
} from "./board-node";
import { useBoardFileImport } from "./board-file-import";
import { boardFrameAround, boardIntoFrames } from "./board-frames";
import { BoardDrawing } from "./board-drawing";
import { BoardPlacement, type PlacementBounds } from "./board-placement";
import {
  BoardClipboard,
  copyCommands,
  selectionWithChildren,
} from "./board-clipboard";
import { BoardPresence } from "./board-presence";
import { BoardShareButton } from "./board-share";
import { BoardTitle } from "./board-title";
import {
  boardCompartmentsHeight,
  boardCompartmentsWidth,
  boardLifelineHead,
  boardShapeDefaults,
  isHollowShape,
  type BoardShapeType,
} from "./board-shape";
import { BoardCompartmentsDialog } from "./board-compartments-dialog";
import { BoardConnectorDialog } from "./board-content-dialogs";
import {
  BoardEdgeMarkers,
  boardEdgeTypes,
  toFlowEdge,
  type BoardFlowEdge,
} from "./board-edge";
import { boardHandles } from "./board-handles";
import { BoardSaveStatus, type BoardSaveState } from "./board-save-status";
import { useBoardSaveFeedback, type BoardLeaves } from "./board-save-feedback";
import { BoardToolbar, type BoardTool } from "./board-toolbar";
import { BoardStyleToolbar } from "./board-style-toolbar";
import { hasTextBackground, textBackground } from "./board-text";
const nodeTypes = { whiteboard: BoardNode };
const errorMessage = (error: unknown) =>
  toast.error(getBoardlyQueryErrorMessage(error));
// A clean note session that is no longer shown is released after this delay,
// so panning back soon reuses it.
const noteReleaseDelay = 30_000;
// Board shortcuts apply on the canvas, never while typing or in a dialog.
const isCanvasKeyTarget = (
  surface: HTMLElement | null,
  target: EventTarget | null,
) =>
  target instanceof Element &&
  !!surface?.contains(target) &&
  !target.closest(
    'input,textarea,[contenteditable]:not([contenteditable="false"]),select,[role="dialog"]',
  );
type EditorProps = {
  scope: BoardScope;
  /** Shown until the Board loads, such as the Artifact title. */
  title?: string;
  /** A smaller header for the chat's side panel. */
  compact?: boolean;
  /** Before the title, such as a way back. */
  leading?: ReactNode;
  /** After Share, such as opening the full view or closing the panel. */
  actions?: ReactNode;
  /** Which navigations close this Board; by default, leaving the page. */
  leaves?: BoardLeaves;
};
export function WhiteboardEditor(props: EditorProps) {
  return (
    <ReactFlowProvider>
      <EditorSurface {...props} />
    </ReactFlowProvider>
  );
}
function EditorSurface({
  scope,
  title = "Whiteboard",
  compact = false,
  leading,
  actions,
  leaves,
}: EditorProps) {
  const [routeParams] = useSearchParams();
  const targetItem = routeParams.get("itemId");
  const focusedItem = useRef<string | null>(null);
  const user = useSessionSnapshot();
  const [presenceTarget, setPresenceTarget] = useState<HTMLDivElement | null>(
    null,
  );
  const query = useGetWhiteboardQuery(scope);
  const snapshot = query.currentData;
  const [submit] = useWhiteboardCommandsMutation();
  const [attachFile] = useAttachWhiteboardFileMutation();
  const hydration = useMemo(
    () => new BoardHydration(boardPath(scope)),
    [scope.companyId, scope.boardId],
  );
  const scene = useMemo(
    () =>
      new BoardSceneSession(
        scope,
        user.userId ?? "",
        (input) => submit({ ...scope, input }).unwrap(),
        {
          attach: (attachmentId) =>
            attachFile({ ...scope, input: { attachmentId } }).unwrap(),
          retireStroke: (id, points) => hydration.rememberStroke(id, points),
        },
      ),
    [
      scope.companyId,
      scope.boardId,
      user.userId,
      submit,
      attachFile,
      hydration,
    ],
  );
  const sceneVersion = useSyncExternalStore(scene.subscribe, scene.snapshot);
  const sessions = useRef(new Map<string, BoardNoteSession>());
  // Mounted notes retain their session; one no longer shown is released once
  // it has nothing left to save.
  const noteRefs = useRef(new Map<BoardNoteSession, number>());
  const noteIdleSince = useRef(new Map<BoardNoteSession, number>());
  const noteReleaseTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );
  // Keys typed between placing a text note and its editor opening.
  const typedAhead = useRef<{ text: string } | null>(null);
  const [orphanNotes, setOrphanNotes] = useState<
    { key: string; value: NoteDraft }[]
  >([]);
  const orphanNotesRef = useRef(orphanNotes);
  const noteRecoveryReady = useRef(Promise.resolve());
  const [recoveringNotes, setRecoveringNotes] = useState(true);
  const [noteRecoveryError, setNoteRecoveryError] = useState("");
  const noteRecoveryErrorRef = useRef("");
  const disposal = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [, setNoteEpoch] = useState(0);
  const surface = useRef<HTMLDivElement>(null);
  const clipboard = useMemo(() => new BoardClipboard(), []);
  const [flow, setFlow] = useState<ReactFlowInstance<
    WhiteboardFlowNode,
    BoardFlowEdge
  > | null>(null);
  const [nodes, setNodes, onNodesChange] = useNodesState<WhiteboardFlowNode>(
    [],
  );
  const [edges, setEdges, onEdgesChange] = useEdgesState<BoardFlowEdge>([]);
  const [tool, setTool] = useState<BoardTool>("move");
  const [trayOpen, setTrayOpen] = useState(false);
  const [textFill, setTextFill] = useState("#00000000");
  const selecting = tool === "move" || tool === "select";
  const [inkColor, setInkColor] = useState("#25252d");
  const [highlightColor, setHighlightColor] = useState("#facc15");
  const [penWidth, setPenWidth] = useState(3);
  const [highlightWidth, setHighlightWidth] = useState(20);
  const width = tool === "highlighter" ? highlightWidth : penWidth;
  const setWidth = tool === "highlighter" ? setHighlightWidth : setPenWidth;
  const [shapeColor, setShapeColor] = useState("#25252d");
  const [shapeFill, setShapeFill] = useState("#00000000");
  const [frameColor, setFrameColor] = useState("#25252d");
  const [frameFill, setFrameFill] = useState("#00000000");
  const shapeWidth = 2;
  // The selection keeps its identity until its contents change; `version`
  // resets the style controls whenever they do.
  const [selection, setSelection] = useState({
    ids: [] as string[],
    version: 0,
  });
  const selected = selection.ids;
  const setSelected = useCallback(
    (ids: string[]) =>
      setSelection((previous) =>
        previous.ids.length === ids.length &&
        previous.ids.every((id, index) => id === ids[index])
          ? previous
          : { ids, version: previous.version + 1 },
      ),
    [],
  );
  const selectedSet = useMemo(() => new Set(selected), [selected]);
  const [stylePreview, setStylePreview] = useState<{
    ids: string[];
    properties: Pick<BoardItem["properties"], "color" | "fill" | "strokeWidth">;
  } | null>(null);
  const firstSelected = useMemo(
    () =>
      selectedSet.size
        ? nodes.find((node) => selectedSet.has(node.id))?.data.item
        : undefined,
    [nodes, selectedSet],
  );
  const selectionProperties = {
    ...firstSelected?.properties,
    ...(selecting &&
    firstSelected &&
    stylePreview?.ids.includes(firstSelected.id)
      ? stylePreview.properties
      : undefined),
  };
  const color = selecting
    ? (selectionProperties.color ?? "#25252d")
    : tool === "shape"
      ? shapeColor
      : tool === "frame"
        ? frameColor
        : tool === "highlighter"
          ? highlightColor
          : inkColor;
  const setColor =
    tool === "shape"
      ? setShapeColor
      : tool === "frame"
        ? setFrameColor
        : tool === "highlighter"
          ? setHighlightColor
          : setInkColor;
  const [shape, setShape] = useState<BoardShapeType>("rectangle");
  const fill = selecting
    ? (selectionProperties.fill ?? "#00000000")
    : tool === "frame"
      ? frameFill
      : shapeFill;
  const styleWidth = selecting
    ? (selectionProperties.strokeWidth ?? 1)
    : tool === "shape"
      ? shapeWidth
      : width;
  useEffect(() => setStylePreview(null), [selected, tool]);
  const [selectedEdges, setSelectedEdges] = useState<string[]>([]);
  const presenceSelection = useMemo(
    () => selected.filter((id) => !id.startsWith("local:")),
    [selected],
  );
  const selectedTextCandidate =
    selecting && selected.length === 1 && selectedEdges.length === 0
      ? nodes.find((node) => node.id === selected[0])?.data.item
      : undefined;
  const selectedText =
    selectedTextCandidate?.type === "text" ||
    selectedTextCandidate?.type === "sticky"
      ? selectedTextCandidate
      : undefined;
  const [edgeEdit, setEdgeEdit] = useState<BoardEdge | null>(null);
  const [compartmentEdit, setCompartmentEdit] = useState<BoardItem | null>(
    null,
  );
  const isLifeline = (id: string | null) =>
    snapshot?.items.find((item) => item.id === id)?.properties.shape ===
    "lifeline";
  // The row below a lifeline's lowest message, or below its head.
  const nextMessageRow = (id: string) => {
    const lifeline = snapshot?.items.find((item) => item.id === id);
    const first = boardLifelineHead(Infinity, lifeline?.properties.head) + 40;
    let row = first;
    for (const edge of snapshot?.edges ?? []) {
      if (edge.deleted || edge.route !== "message" || edge.anchorY == null)
        continue;
      const source = snapshot?.items.find((item) => item.id === edge.sourceId);
      if (!source || !lifeline) continue;
      if (edge.sourceId === id || edge.targetId === id)
        row = Math.max(row, source.y + edge.anchorY - lifeline.y + 40);
    }
    return Math.min(row, Math.max(first, (lifeline?.height ?? 120) - 24));
  };
  // Presence reads drag previews on its heartbeat; dragging never re-renders
  // the editor for them.
  const dragPreviews = useRef<components["schemas"]["BoardDragPreview"][]>([]);
  const dragOrigins = useRef(new Map<string, BoardItem>());
  const nodesBuiltFor = useRef<boolean | null>(null);
  const lastSnapshot = useRef(snapshot);
  lastSnapshot.current = snapshot;
  const accessDenied =
    query.error &&
    "status" in query.error &&
    [401, 403, 404].includes(query.error.status ?? 0);
  const editing = !!snapshot?.board.canEdit && !accessDenied;
  // React Flow re-renders a node whose object changed. Unchanged nodes keep
  // their derived object while the tool and editing state hold; a style
  // preview replaces only the objects it previews.
  const derivedNodes = useRef<{
    key: unknown[];
    cache: WeakMap<WhiteboardFlowNode, WhiteboardFlowNode>;
  }>({ key: [], cache: new WeakMap() });
  const previewIds = useMemo(
    () => (selecting && stylePreview ? new Set(stylePreview.ids) : null),
    [selecting, stylePreview],
  );
  const canvasNodes = useMemo<WhiteboardFlowNode[]>(() => {
    const key = [editing, selecting, tool];
    if (
      key.length !== derivedNodes.current.key.length ||
      key.some((value, index) => value !== derivedNodes.current.key[index])
    )
      derivedNodes.current = { key, cache: new WeakMap() };
    const { cache } = derivedNodes.current;
    const previewed = (node: WhiteboardFlowNode): WhiteboardFlowNode =>
      !stylePreview ||
      !previewIds?.has(node.id) ||
      node.data.item.properties.locked
        ? node
        : {
            ...node,
            data: {
              ...node.data,
              item: {
                ...node.data.item,
                properties: {
                  ...node.data.item.properties,
                  ...stylePreview.properties,
                },
              },
            },
          };
    return nodes.map((node) => {
      const known = cache.get(node);
      if (known) return previewed(known);
      const derived: WhiteboardFlowNode = {
        ...node,
        // Only selected objects intercept drags while drawing a selection box.
        style: {
          ...node.style,
          pointerEvents:
            !selecting ||
            (!node.selected &&
              (tool === "select" || node.data.item.type === "stroke"))
              ? "none"
              : node.style?.pointerEvents,
        },
        draggable: editing && selecting && node.draggable === true,
      };
      cache.set(node, derived);
      return previewed(derived);
    });
  }, [nodes, editing, selecting, tool, previewIds, stylePreview]);
  const scenePending =
    scene.dirty || (snapshot?.sequence ?? 0) < scene.acknowledgedSequence;
  const commit = useCallback(
    (commands: BoardCommand[]) => {
      const current = lastSnapshot.current;
      if (!commands.length || !current?.board.canEdit) return;
      void scene
        .commit(commands, current.items, current.edges)
        .catch(errorMessage);
    },
    [scene],
  );
  const topStackOrder = useCallback(
    () =>
      scene
        .preview(lastSnapshot.current?.items ?? [])
        .reduce((highest, item) => Math.max(highest, item.stackOrder), 0),
    [scene],
  );
  const releaseIdleNotes = useCallback(() => {
    noteReleaseTimer.current = undefined;
    const now = Date.now();
    let waiting = false;
    for (const [key, session] of sessions.current) {
      const since = noteIdleSince.current.get(session);
      // Unsaved or still-saving notes stay; their next state change asks again.
      if (since === undefined || session.hasDraft || session.busy) continue;
      if (now - since < noteReleaseDelay) {
        waiting = true;
        continue;
      }
      noteIdleSince.current.delete(session);
      sessions.current.delete(key);
      session.dispose();
    }
    if (waiting)
      noteReleaseTimer.current = setTimeout(releaseIdleNotes, noteReleaseDelay);
  }, []);
  const scheduleNoteRelease = useCallback(() => {
    noteReleaseTimer.current ??= setTimeout(releaseIdleNotes, noteReleaseDelay);
  }, [releaseIdleNotes]);
  const retainNote = useCallback(
    (session: BoardNoteSession) => {
      noteRefs.current.set(session, (noteRefs.current.get(session) ?? 0) + 1);
      noteIdleSince.current.delete(session);
      return () => {
        const count = (noteRefs.current.get(session) ?? 1) - 1;
        if (count > 0) {
          noteRefs.current.set(session, count);
          return;
        }
        noteRefs.current.delete(session);
        noteIdleSince.current.set(session, Date.now());
        scheduleNoteRelease();
      };
    },
    [scheduleNoteRelease],
  );
  const note = useCallback(
    (item: BoardItem) => {
      const key = `${item.id}:${item.lifecycleRevision}`;
      let session = sessions.current.get(key);
      if (!session) {
        session = new BoardNoteSession(
          scope,
          item.id,
          item.lifecycleRevision,
          user.userId ?? "",
          () => hydration.get(item.id),
          !!lastSnapshot.current?.board.canEdit,
        );
        sessions.current.set(key, session);
        // Copying and draft recovery open sessions that no note shows.
        noteIdleSince.current.set(session, Date.now());
        scheduleNoteRelease();
        // The editor shows save state only; keystrokes re-render the note alone.
        let shown = "";
        let caughtUp = false;
        const created = session;
        created.subscribe(() => {
          const state = `${created.hasDraft}:${created.status}:${created.error}`;
          if (state === shown) return;
          shown = state;
          setNoteEpoch((epoch) => epoch + 1);
          if (noteIdleSince.current.has(created)) scheduleNoteRelease();
          // Text that changed while the note loaded arrives as a delta.
          if (!created.loaded || caughtUp) return;
          caughtUp = true;
          const latest = lastSnapshot.current?.items.find(
            (item) => item.id === created.itemId,
          );
          if (latest && latest.textRevision > created.revision)
            void hydration
              .get(created.itemId, created.stateVector())
              .then((value) => created.merge(value))
              .catch(() => {});
        });
      }
      return session;
    },
    [
      scope.companyId,
      scope.boardId,
      user.userId,
      hydration,
      scheduleNoteRelease,
    ],
  );
  const hydrate = useCallback((id: string) => hydration.get(id), [hydration]);
  useEffect(() => {
    if (!snapshot?.board.id) return;
    let stopped = false;
    const prefix = `${user.userId ?? ""}:${boardScopeKey(scope)}:note:v1:`;
    noteRecoveryReady.current = readBoardDrafts<NoteDraft>(prefix)
      .then(async (rows) => {
        if (stopped) return;
        const current = lastSnapshot.current;
        const items = new Map(current?.items.map((item) => [item.id, item]));
        const orphans: typeof orphanNotes = [];
        const recovering: Promise<void>[] = [];
        for (const row of rows) {
          const [itemId, lifecycle] = row.key.slice(prefix.length).split(":");
          const item = items.get(itemId);
          if (
            !item ||
            item.deleted ||
            item.lifecycleRevision !== Number(lifecycle) ||
            !current?.board.canEdit
          ) {
            orphans.push(row);
          } else {
            // Pending offscreen notes must also recover before Saved.
            recovering.push(note(item).ready);
          }
        }
        orphanNotesRef.current = orphans;
        setOrphanNotes(orphans);
        await Promise.all(recovering);
      })
      .catch(() => {
        if (!stopped) {
          noteRecoveryErrorRef.current =
            "Local note recovery is unavailable. Reopen this Board to recover unsaved notes.";
          setNoteRecoveryError(noteRecoveryErrorRef.current);
        }
      })
      .finally(() => {
        if (!stopped) setRecoveringNotes(false);
      });
    return () => {
      stopped = true;
    };
  }, [snapshot?.board.id, scope.companyId, scope.boardId, user.userId, note]);
  const nodeContext = useMemo(
    () => ({
      scope,
      editable: editing,
      note,
      retainNote,
      hydrate,
      commit,
      editCompartments: setCompartmentEdit,
    }),
    [
      scope.companyId,
      scope.boardId,
      editing,
      note,
      retainNote,
      hydrate,
      commit,
    ],
  );
  useEffect(() => {
    clearTimeout(disposal.current);
    scene.start();
    return () => {
      disposal.current = setTimeout(() => {
        scene.dispose();
        hydration.dispose();
        clearTimeout(noteReleaseTimer.current);
        noteReleaseTimer.current = undefined;
        sessions.current.forEach((session) => session.dispose());
        sessions.current.clear();
        noteRefs.current.clear();
        noteIdleSince.current.clear();
      }, 0);
    };
  }, [scene, hydration]);
  useLayoutEffect(() => {
    if (!snapshot) return;
    scene.acceptSnapshot(snapshot.sequence);
    const pending = scene.pendingIds;
    const reuse = nodesBuiltFor.current === editing;
    nodesBuiltFor.current = editing;
    setNodes((current) => {
      const previous = new Map(current.map((node) => [node.id, node]));
      return scene
        .preview(snapshot.items)
        .filter((item) => !item.deleted)
        .map((item) => {
          const old = previous.get(item.id);
          const local = item.id.startsWith("local:");
          const itemPending = pending.has(item.id) || local;
          const points = scene.points(item.id);
          // An unchanged item keeps its node object, so React Flow skips it.
          if (
            reuse &&
            old &&
            !old.dragging &&
            !old.resizing &&
            old.data.item === item &&
            old.data.pending === itemPending &&
            old.data.points === points
          )
            return old;
          const width = old?.resizing ? (old.width ?? item.width) : item.width;
          const height = old?.resizing
            ? (old.height ?? item.height)
            : item.height;
          return {
            ...old,
            id: item.id,
            type: "whiteboard",
            position: old?.dragging ? old.position : { x: item.x, y: item.y },
            dragging: old?.dragging,
            resizing: old?.resizing,
            data: { item, pending: itemPending, points },
            width,
            height,
            measured: { width, height },
            handles: boardHandles(item, width, height),
            // A lifeline's or fragment's inside lets clicks through to the
            // messages over it; its outline and header still take them.
            style: isHollowShape(item.properties.shape)
              ? { width, height, pointerEvents: "none" }
              : { width, height },
            selected: old?.selected ?? false,
            dragHandle:
              item.type === "sticky" || item.type === "text"
                ? ".wb-node-drag-handle"
                : undefined,
            draggable: editing && !item.properties.locked && !itemPending,
            connectable: editing && item.type !== "frame" && !local,
            selectable: true,
            // Frames and sequence fragments sit under connectors.
            zIndex:
              item.type === "frame" || item.properties.shape === "fragment"
                ? -10
                : item.stackOrder,
          };
        });
    });
    setEdges(
      scene
        .previewEdges(snapshot.edges)
        .filter((edge) => !edge.deleted)
        .map(toFlowEdge),
    );
  }, [snapshot, editing, scene, sceneVersion, setNodes, setEdges]);
  useEffect(() => {
    if (!snapshot) return;
    const items = new Map(snapshot.items.map((item) => [item.id, item]));
    for (const session of sessions.current.values()) {
      const item = items.get(session.itemId);
      if (!item || item.lifecycleRevision !== session.lifecycleRevision) {
        if (session.dirty)
          session.block(
            "This note was deleted or restored. Export or discard your earlier draft.",
          );
        continue;
      }
      if (session.loaded && item.textRevision > session.revision)
        void hydration
          .get(item.id, session.stateVector())
          .then((value) => session.merge(value))
          .catch(() => {});
    }
  }, [snapshot, hydration]);
  const createSelected = useCallback(
    async (commands: BoardCommand[]) => {
      const current = lastSnapshot.current;
      if (!current?.board.canEdit) return;
      const result = await scene.commit(
        boardIntoFrames(commands, current.items),
        current.items,
        current.edges,
      );
      if (!result || !surface.current) return result;
      const created = new Set(
        commands
          .filter((command) => command.type === "create_item")
          .map((command) => result.ids[command.draftId!]),
      );
      const connectors = new Set(
        commands
          .filter((command) => command.type === "create_edge")
          .map((command) => result.ids[command.draftId!]),
      );
      setNodes((nodes) =>
        nodes.map((node) => ({ ...node, selected: created.has(node.id) })),
      );
      setEdges((edges) =>
        edges.map((edge) => ({ ...edge, selected: connectors.has(edge.id) })),
      );
      setSelected([...created]);
      setSelectedEdges([...connectors]);
      return result;
    },
    [scene, setNodes, setEdges, setSelected],
  );
  const fileImport = useBoardFileImport({
    scope,
    enabled: editing,
    toBoardPosition: (point) => flow?.screenToFlowPosition(point),
    onInsert: async (commands) => {
      const current = lastSnapshot.current;
      if (!current) return;
      const stackOrder = topStackOrder();
      setTool("move");
      await createSelected(
        commands.map((command, index) => ({
          ...command,
          geometry: {
            ...command.geometry!,
            stackOrder: stackOrder + index + 1,
          },
        })),
      );
    },
  });
  const noteSessions = [...sessions.current.values()];
  // A note still loading, or failing to load, with a stored draft is unsaved
  // work too.
  const pendingNotes = noteSessions.filter((session) => session.hasDraft);
  const notesDirty = pendingNotes.length > 0;
  const dirty = scene.dirty || notesDirty || fileImport.isUploading;
  const sceneDrafts = [...scene.drafts.values()];
  let saveState: BoardSaveState = "idle";
  if (!editing) saveState = "read-only";
  else if (recoveringNotes) saveState = "recovering";
  else if (
    noteRecoveryError ||
    scene.storageError ||
    orphanNotes.length ||
    sceneDrafts.some(
      (draft) => draft.status === "conflict" || draft.status === "stale",
    ) ||
    pendingNotes.some(
      (session) => session.status === "conflict" || session.status === "failed",
    )
  )
    saveState = "attention";
  else if (
    sceneDrafts.some((draft) => draft.status === "offline") ||
    pendingNotes.some((session) => session.status === "offline")
  )
    saveState = "offline";
  else if (dirty) saveState = "saving";
  const hasPendingChanges = useCallback(
    () =>
      fileImport.hasPendingUpload() ||
      scene.dirty ||
      [...sessions.current.values()].some((session) => session.hasDraft),
    [scene, fileImport.hasPendingUpload],
  );
  const flushPendingChanges = useCallback(async () => {
    await noteRecoveryReady.current;
    if (noteRecoveryErrorRef.current || orphanNotesRef.current.length)
      throw new Error(
        noteRecoveryErrorRef.current ||
          "Export or discard recovered note drafts before saving.",
      );
    do {
      await Promise.all([
        fileImport.flushUploads(),
        scene.flush(),
        ...[...sessions.current.values()]
          .filter((session) => session.hasDraft)
          .map((session) => session.flush()),
      ]);
    } while (hasPendingChanges());
  }, [scene, hasPendingChanges, fileImport.flushUploads]);
  const { feedback, leaveDialog } = useBoardSaveFeedback({
    editable: editing,
    pending: dirty,
    hasPendingChanges,
    flush: flushPendingChanges,
    leaves,
  });
  const selectionChanged = useCallback(
    ({ nodes, edges }: { nodes: WhiteboardFlowNode[]; edges: Edge[] }) => {
      setSelected(nodes.map((node) => node.id));
      setSelectedEdges(edges.map((edge) => edge.id));
    },
    [setSelected],
  );
  useEffect(() => {
    if (!flow || !targetItem || focusedItem.current === targetItem) return;
    const item = snapshot?.items.find((item) => item.id === targetItem);
    if (!item) return;
    focusedItem.current = targetItem;
    setNodes((nodes) =>
      nodes.map((node) => ({
        ...node,
        selected: item.type !== "stroke" && node.id === targetItem,
      })),
    );
    void flow.setCenter(item.x + item.width / 2, item.y + item.height / 2, {
      zoom: 1,
    });
  }, [flow, targetItem, snapshot, setNodes]);
  const chosen = useMemo(
    () => snapshot?.items.filter((item) => selectedSet.has(item.id)) ?? [],
    [snapshot, selectedSet],
  );
  const styleDisabled = useMemo(
    () =>
      selecting &&
      (scenePending ||
        !chosen.some(
          (item) => !item.properties.locked && !item.id.startsWith("local:"),
        )),
    [selecting, scenePending, chosen],
  );
  const remove = useCallback(() => {
    if (!editing || !snapshot || scenePending) return;
    // The focused object or delete button may disappear with the selection.
    surface.current?.focus({ preventScroll: true });
    const byId = new Map(snapshot.items.map((item) => [item.id, item]));
    const depth = (item: BoardItem) => {
      let parent = item.parentFrameId;
      let value = 0;
      while (parent) {
        value++;
        parent = byId.get(parent)?.parentFrameId ?? null;
      }
      return value;
    };
    const edgeSet = new Set(selectedEdges);
    commit([
      ...snapshot.edges
        .filter((edge) => edgeSet.has(edge.id))
        .map<BoardCommand>((edge) => ({
          type: "delete_edge",
          id: edge.id,
          expectedRevision: edge.revision,
        })),
      ...snapshot.items
        .filter((item) => selectedSet.has(item.id) && !item.properties.locked)
        // Delete children first so frame detachment cannot invalidate their guards.
        .sort((a, b) => depth(b) - depth(a))
        .map<BoardCommand>((item) => ({
          type: "delete_item",
          id: item.id,
          expectedRevision: item.contentRevision,
          expectedGeometryRevision: item.geometryRevision,
          expectedTextRevision: item.textRevision,
        })),
    ]);
  }, [editing, snapshot, selectedSet, selectedEdges, commit, scenePending]);
  const undo = useCallback(
    (redo = false) => {
      if (!snapshot || !editing || scenePending) return;
      surface.current?.focus({ preventScroll: true });
      void (
        redo
          ? scene.redo(snapshot.items, snapshot.edges)
          : scene.undo(snapshot.items, snapshot.edges)
      ).catch(errorMessage);
    },
    [snapshot, editing, scene, scenePending],
  );
  useEffect(() => {
    const handles = (event: Event) =>
      isCanvasKeyTarget(surface.current, event.target);
    const copy = (event: ClipboardEvent) => {
      if (!handles(event) || !snapshot || !selected.length) return;
      const items = structuredClone(
        selectionWithChildren(scene.preview(snapshot.items), selected),
      );
      if (items.some((item) => item.id.startsWith("local:"))) {
        event.preventDefault();
        toast.error("Wait for these objects to save before copying.");
        return;
      }
      const content = copyCommands({
        items,
        edges: scene.previewEdges(snapshot.edges),
        content: async (item) => {
          if (item.type === "stroke")
            return {
              points: scene.points(item.id) ?? (await hydrate(item.id)).points,
            };
          if (item.type === "sticky" || item.type === "text") {
            const session = note(item);
            await session.ready;
            if (session.error) throw new Error(session.error);
            return { text: session.text };
          }
          return {};
        },
      }).then((commands) => ({ companyId: scope.companyId, commands }));
      clipboard.copy(event, content);
      void content.catch(errorMessage);
    };
    const paste = (event: ClipboardEvent) => {
      if (event.defaultPrevented || !handles(event) || !editing || !snapshot)
        return;
      const files = Array.from(event.clipboardData?.files ?? []);
      if (files.length) {
        const bounds = surface.current?.getBoundingClientRect();
        if (!bounds) return;
        event.preventDefault();
        fileImport.importFiles(files, {
          x: bounds.left + bounds.width / 2,
          y: bounds.top + bounds.height / 2,
          anchor: "center",
        });
        return;
      }
      const text = event.clipboardData?.getData("text/plain") ?? "";
      if (!clipboard.accepts(text)) return;
      event.preventDefault();
      const stackOrder = topStackOrder();
      setTool("move");
      void clipboard
        .paste(text, scope.companyId, stackOrder)
        .then(async (commands) => {
          // Files copied from another Board are referenced here first. A file
          // this member cannot read is left out rather than failing the paste.
          const files = [
            ...new Set(
              commands.flatMap((command) =>
                command.properties?.attachmentId
                  ? [command.properties.attachmentId]
                  : [],
              ),
            ),
          ];
          const refused = new Set<string>();
          await Promise.all(
            files.map((attachmentId) =>
              attachFile({ ...scope, input: { attachmentId } })
                .unwrap()
                .catch(() => refused.add(attachmentId)),
            ),
          );
          if (refused.size)
            toast.error(
              "Some files could not be added to this Whiteboard and were left out.",
            );
          const kept = commands.filter(
            (command) =>
              !command.properties?.attachmentId ||
              !refused.has(command.properties.attachmentId),
          );
          const createdIds = new Set(
            kept.flatMap((command) =>
              command.draftId ? [command.draftId] : [],
            ),
          );
          return createSelected(
            kept.filter(
              (command) =>
                command.type !== "create_edge" ||
                (createdIds.has(command.sourceRef ?? "") &&
                  createdIds.has(command.targetRef ?? "")),
            ),
          );
        })
        .catch(errorMessage);
    };
    const keydown = (event: KeyboardEvent) => {
      if (!handles(event) || event.isComposing) return;
      // A text note was just placed and its editor is not open yet: typing
      // belongs to it, never to tool shortcuts or the selection. React Flow
      // has already claimed Space for panning, so this ignores defaultPrevented.
      const typing = typedAhead.current;
      if (
        typing &&
        !event.metaKey &&
        !event.ctrlKey &&
        !event.altKey &&
        event.key !== "Escape" &&
        event.key !== "Tab"
      ) {
        event.preventDefault();
        if ([...event.key].length === 1) typing.text += event.key;
        else if (event.key === "Enter") typing.text += "\n";
        else if (event.key === "Backspace")
          typing.text = typing.text.slice(0, -1);
        return;
      }
      if (event.defaultPrevented) return;
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "a") {
        event.preventDefault();
        setTool("move");
        setNodes((nodes) => nodes.map((node) => ({ ...node, selected: true })));
        setEdges((edges) => edges.map((edge) => ({ ...edge, selected: true })));
        return;
      }
      if (event.key === "Delete" || event.key === "Backspace") {
        event.preventDefault();
        remove();
        return;
      }
      if (
        ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(
          event.key,
        ) &&
        editing &&
        selected.length
      ) {
        event.preventDefault();
        const distance = event.shiftKey ? 10 : 1;
        const dx =
          event.key === "ArrowLeft"
            ? -distance
            : event.key === "ArrowRight"
              ? distance
              : 0;
        const dy =
          event.key === "ArrowUp"
            ? -distance
            : event.key === "ArrowDown"
              ? distance
              : 0;
        const pending = scene.pendingIds;
        commit(
          (snapshot?.items ?? [])
            .filter(
              (item) =>
                selectedSet.has(item.id) &&
                !item.properties.locked &&
                !pending.has(item.id),
            )
            .map((item) => ({
              type: "geometry",
              id: item.id,
              expectedRevision: item.geometryRevision,
              geometry: { ...geometryOf(item), x: item.x + dx, y: item.y + dy },
            })),
        );
        return;
      }
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "z") {
        event.preventDefault();
        undo(event.shiftKey);
        return;
      }
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      const shortcut: Record<string, BoardTool> = {
        v: "move",
        s: "select",
        h: "highlighter",
        f: "frame",
        n: "text",
        t: "text",
        p: "pen",
      };
      const next = shortcut[event.key.toLowerCase()];
      if (next && (editing || next === "move" || next === "select")) {
        setTrayOpen(next !== "move" && next !== "select");
        setTool(next);
      }
    };
    window.addEventListener("keydown", keydown);
    window.addEventListener("copy", copy);
    window.addEventListener("paste", paste);
    return () => {
      window.removeEventListener("keydown", keydown);
      window.removeEventListener("copy", copy);
      window.removeEventListener("paste", paste);
    };
  }, [
    remove,
    undo,
    editing,
    selected,
    selectedSet,
    snapshot,
    scene,
    commit,
    setNodes,
    setEdges,
    clipboard,
    attachFile,
    fileImport.importFiles,
    topStackOrder,
    createSelected,
    hydrate,
    note,
    scope.companyId,
  ]);
  const changeTextBackground = (background: string) => {
    if (!selectedText) {
      setTextFill(background);
      return;
    }
    if (
      !editing ||
      scenePending ||
      selectedText.properties.locked ||
      selectedText.id.startsWith("local:") ||
      textBackground(selectedText) === background
    )
      return;
    commit([
      {
        type: "content",
        id: selectedText.id,
        expectedRevision: selectedText.contentRevision,
        properties: {
          ...selectedText.properties,
          fill: background,
          color: hasTextBackground(background) ? "#25252d" : undefined,
        },
      },
    ]);
  };
  // Once the new note has its real ID, select it and open its editor with
  // whatever was typed meanwhile. Another click cancels the hand-off.
  const openNewNote = (item: BoardItem, typing: { text: string }) => {
    const session = note(item);
    void session.ready.then(() => {
      let frames = 0;
      const open = () => {
        if (typedAhead.current !== typing) return;
        const field = surface.current?.querySelector<HTMLTextAreaElement>(
          `[data-board-item="${CSS.escape(item.id)}"] textarea`,
        );
        if (!field || !session.loaded) {
          if (++frames < 60) requestAnimationFrame(open);
          else typedAhead.current = null;
          return;
        }
        typedAhead.current = null;
        if (typing.text) {
          session.edit(session.text + typing.text);
          field.value = session.text;
        }
        field.focus({ preventScroll: true });
        field.setSelectionRange(field.value.length, field.value.length);
      };
      requestAnimationFrame(open);
    });
  };
  const createText = (position: { x: number; y: number }) => {
    const paper = hasTextBackground(textFill);
    const size = paper
      ? { width: 240, height: 190 }
      : { width: 260, height: 130 };
    const draftId = crypto.randomUUID();
    const typing = { text: "" };
    typedAhead.current = typing;
    setTool("move");
    void createSelected([
      {
        type: "create_item",
        draftId,
        itemType: paper ? "sticky" : "text",
        geometry: {
          ...position,
          ...size,
          rotation: 0,
          stackOrder: topStackOrder() + 1,
          parentFrameId: null,
          readingOrder: 0,
        },
        properties: {
          color: paper ? "#25252d" : undefined,
          fill: textFill,
          locked: false,
        },
      },
    ])
      .then((result) => {
        const id = result?.ids[draftId];
        const item = result?.items.find((item) => item.id === id);
        if (item && typedAhead.current === typing) openNewNote(item, typing);
        else if (typedAhead.current === typing) typedAhead.current = null;
      })
      .catch((error: unknown) => {
        if (typedAhead.current === typing) typedAhead.current = null;
        errorMessage(error);
      });
  };
  const drawing = tool === "pen" || tool === "highlighter";
  const completeShape = (bounds: PlacementBounds) => {
    void createSelected([
      {
        type: "create_item",
        draftId: crypto.randomUUID(),
        itemType: "shape",
        geometry: {
          ...bounds,
          rotation: 0,
          stackOrder: topStackOrder() + 1,
          parentFrameId: null,
          readingOrder: 0,
        },
        properties: {
          shape,
          color: shapeColor,
          fill: shapeFill,
          strokeWidth: shapeWidth,
          locked: false,
          ...boardShapeDefaults(shape, shapeFill),
        },
      },
    ]).catch(errorMessage);
    setTool("move");
  };
  const completeFrame = (bounds: PlacementBounds) => {
    if (!snapshot) return;
    const pending = scene.pendingIds;
    const enclosed = snapshot.items.filter(
      (item) =>
        !item.deleted &&
        !item.properties.locked &&
        !pending.has(item.id) &&
        item.rotation === 0 &&
        item.x >= bounds.x &&
        item.y >= bounds.y &&
        item.x + item.width <= bounds.x + bounds.width &&
        item.y + item.height <= bounds.y + bounds.height,
    );
    // A frame drawn inside another joins it and takes over what it encloses
    // there; other frame hierarchies stay together.
    const parent = boardFrameAround(snapshot.items, bounds);
    const children = enclosed.filter(
      (item) =>
        item.id !== parent &&
        (!item.parentFrameId || item.parentFrameId === parent),
    );
    // The frame and one command per child form one batch, as does its undo.
    if (children.length > boardBatchLimit - 1) {
      toast.error(
        `Draw a frame around at most ${(boardBatchLimit - 1).toLocaleString("en-US")} objects at a time.`,
      );
      return;
    }
    const draftId = crypto.randomUUID();
    void createSelected([
      {
        type: "create_item",
        draftId,
        itemType: "frame",
        geometry: {
          ...bounds,
          rotation: 0,
          stackOrder: 0,
          parentFrameId: parent,
          readingOrder: 0,
        },
        properties: {
          title: "Untitled frame",
          color: frameColor,
          fill: frameFill,
          locked: false,
        },
      },
      ...children.map((item): BoardCommand => ({
        type: "geometry",
        id: item.id,
        expectedRevision: item.geometryRevision,
        geometry: geometryOf(item),
        parentRef: draftId,
      })),
    ]).catch(errorMessage);
    setTool("move");
  };
  const completeDrawing = (drawing: ReturnType<typeof normalizeDrawing>) => {
    const bounds = {
      ...drawing.position,
      width: drawing.width,
      height: drawing.height,
    };
    commit([
      {
        type: "create_item",
        draftId: crypto.randomUUID(),
        itemType: "stroke",
        geometry: {
          ...bounds,
          rotation: 0,
          stackOrder: topStackOrder() + 1,
          parentFrameId: boardFrameAround(snapshot?.items ?? [], bounds),
          readingOrder: 0,
        },
        properties: {
          color,
          fill: "#00000000",
          strokeWidth: tool === "highlighter" ? Math.max(12, width) : width,
          brush: tool === "highlighter" ? "highlighter" : "pen",
          locked: false,
        },
        points: drawing.points,
      },
    ]);
  };
  const changeSelectionStyle = (
    properties: Pick<BoardItem["properties"], "color" | "fill" | "strokeWidth">,
  ) => {
    setStylePreview(null);
    if (!editing || scenePending || scene.dirty) return;
    commit(
      chosen
        .filter(
          (item) =>
            !item.properties.locked &&
            !item.id.startsWith("local:") &&
            Object.entries(properties).some(
              ([key, value]) =>
                item.properties[key as keyof typeof properties] !== value,
            ),
        )
        .map((item) => ({
          type: "content",
          id: item.id,
          expectedRevision: item.contentRevision,
          properties: { ...item.properties, ...properties },
        })),
    );
  };
  const align = (axis: "x" | "y") => {
    const items = chosen.filter((item) => !item.properties.locked);
    if (items.length < 2) return;
    const value = Math.min(...items.map((item) => item[axis]));
    commit(
      items.map((item) => ({
        type: "geometry",
        id: item.id,
        expectedRevision: item.geometryRevision,
        geometry: { ...geometryOf(item), [axis]: value },
      })),
    );
  };
  const group = () => {
    // Locked objects keep their position and parent.
    const items = chosen.filter((item) => !item.properties.locked);
    if (!items.length) return;
    const bounds = {
      x: Math.min(...items.map((item) => item.x)) - 30,
      y: Math.min(...items.map((item) => item.y)) - 50,
      width: 0,
      height: 0,
    };
    bounds.width =
      Math.max(...items.map((item) => item.x + item.width)) - bounds.x + 30;
    bounds.height =
      Math.max(...items.map((item) => item.y + item.height)) - bounds.y + 30;
    const id = crypto.randomUUID();
    // The group stays in the frame around it, unless it groups that frame.
    const parent = boardFrameAround(snapshot?.items ?? [], bounds, (frame) =>
      items.some((item) => item.id === frame.id),
    );
    commit([
      {
        type: "create_item",
        draftId: id,
        itemType: "frame",
        geometry: {
          ...bounds,
          rotation: 0,
          stackOrder: 0,
          parentFrameId: parent,
          readingOrder: 0,
        },
        properties: { title: "Group", color, fill: "#00000000", locked: false },
      },
      ...items.map<BoardCommand>((item) => ({
        type: "geometry",
        id: item.id,
        expectedRevision: item.geometryRevision,
        geometry: geometryOf(item),
        parentRef: id,
      })),
    ]);
  };
  const Heading = compact ? "h2" : "h1";
  const headerClass = compact
    ? "wb-editor-header wb-editor-header--compact"
    : "wb-editor-header";
  if (query.isLoading || accessDenied || !snapshot)
    return (
      <section className="wb-editor" aria-label={title}>
        <header className={headerClass}>
          {leading}
          <div className="wb-board-heading">
            <Heading title={title}>{title}</Heading>
          </div>
          {actions ? <div className="wb-header-actions">{actions}</div> : null}
        </header>
        {query.isLoading ? (
          <div className="wb-message" role="status">
            Opening Whiteboard…
          </div>
        ) : (
          <div className="wb-message" role="alert">
            <h2>This Whiteboard is unavailable</h2>
            <p>{getBoardlyQueryErrorMessage(query.error)}</p>
            <p>Unsaved local drafts are retained for this account.</p>
            <button onClick={() => void query.refetch()}>Retry</button>
          </div>
        )}
      </section>
    );
  // Unsaved note drafts that cannot save right now: refused, offline, or
  // waiting for a note that failed to load.
  const recoverableNotes = [...sessions.current].filter(
    ([, session]) =>
      session.hasDraft &&
      (session.status === "conflict" ||
        session.status === "offline" ||
        session.status === "failed"),
  );
  const hasRecovery =
    noteRecoveryError ||
    scene.storageError ||
    orphanNotes.length > 0 ||
    query.isError ||
    sceneDrafts.some((draft) => draft.status !== "saving") ||
    recoverableNotes.length > 0;
  const recovery = hasRecovery ? (
    <>
      {noteRecoveryError && (
        <div className="wb-recovery-item flex flex-wrap gap-[8px] p-[12px_16px] leading-[1.5] wrap-anywhere">
          {noteRecoveryError}
        </div>
      )}
      {orphanNotes.map((row) => (
        <div
          key={row.key}
          className="wb-recovery-item flex flex-wrap gap-[8px] p-[12px_16px] leading-[1.5] wrap-anywhere"
        >
          <span>
            A local note draft was recovered. Its original note was deleted,
            restored, or is now read-only. Export it or discard it; it will not
            be applied automatically.
          </span>
          <button
            onClick={() =>
              downloadDocument(
                row.value.text ?? JSON.stringify(row.value, null, 2),
                row.value.text === undefined
                  ? "whiteboard-recovered-note.json"
                  : "whiteboard-recovered-note.txt",
                row.value.text === undefined
                  ? "application/json"
                  : "text/plain",
              )
            }
          >
            Export recovered note
          </button>
          <button
            onClick={() =>
              void deleteBoardDraft<NoteDraft>(
                row.key,
                (value) => (value.stamp ?? "") === (row.value.stamp ?? ""),
              )
                .then(() => {
                  orphanNotesRef.current = orphanNotesRef.current.filter(
                    (draft) => draft.key !== row.key,
                  );
                  setOrphanNotes(orphanNotesRef.current);
                })
                .catch(errorMessage)
            }
          >
            Discard recovered note
          </button>
        </div>
      ))}
      {recoverableNotes.map(([key, session]) => (
        <div
          key={key}
          className="wb-recovery-item flex flex-wrap gap-[8px] p-[12px_16px] leading-[1.5] wrap-anywhere"
        >
          <span>
            {session.status === "failed"
              ? `A note with unsaved local text could not be loaded. ${session.error}`
              : session.error}
          </span>
          <button
            onClick={() => {
              const draft = session.draftExport();
              downloadDocument(
                draft.content,
                draft.json
                  ? "whiteboard-note-draft.json"
                  : "whiteboard-note-draft.txt",
                draft.json ? "application/json" : "text/plain",
              );
            }}
          >
            Export note draft
          </button>
          <button onClick={() => void session.discard().catch(errorMessage)}>
            Discard note draft
          </button>
          {session.status === "failed" && (
            <button onClick={() => session.retry()}>Retry</button>
          )}
          {session.status === "offline" && (
            <button onClick={() => void session.flush().catch(() => {})}>
              Retry
            </button>
          )}
        </div>
      ))}
      {query.isError && !accessDenied && (
        <div className="wb-recovery-item flex flex-wrap gap-[8px] p-[12px_16px] leading-[1.5] wrap-anywhere">
          Connection interrupted. Changes will be sent when you reconnect.
        </div>
      )}
      {[...scene.drafts]
        .filter(([, draft]) => draft.status !== "saving")
        .map(([key, draft]) => (
          <div
            key={key}
            className="wb-recovery-item flex flex-wrap gap-[8px] p-[12px_16px] leading-[1.5] wrap-anywhere"
          >
            <span>{draft.message}</span>
            <button
              onClick={() =>
                downloadDocument(
                  JSON.stringify(draft.input, null, 2),
                  "whiteboard-unsaved-draft.json",
                  "application/json",
                )
              }
            >
              Export draft
            </button>
            <button onClick={() => void scene.discard(key)}>
              Discard draft
            </button>
            {draft.status === "offline" && (
              <button onClick={() => scene.retry()}>Retry</button>
            )}
            {draft.status === "stale" && (
              <button onClick={() => scene.resend(key)}>Send again</button>
            )}
          </div>
        ))}
      {scene.storageError && (
        <div className="wb-recovery-item flex flex-wrap gap-[8px] p-[12px_16px] leading-[1.5] wrap-anywhere">
          {scene.storageError}
        </div>
      )}
    </>
  ) : null;
  return (
    <BoardNodeContext.Provider value={nodeContext}>
      <section className="wb-editor" aria-label={snapshot.board.title}>
        <header className={headerClass}>
          {leading}
          <div className="wb-board-heading">
            <BoardTitle
              scope={scope}
              title={snapshot.board.title}
              canEdit={snapshot.board.canEdit}
              as={Heading}
            />
            <BoardSaveStatus
              state={saveState}
              feedback={feedback}
              recovery={recovery}
            />
          </div>
          <div className="wb-header-actions">
            <div ref={setPresenceTarget} className="wb-header-presence" />
            {snapshot.board.canShare ? (
              <BoardShareButton
                scope={scope}
                title={snapshot.board.title}
                compact={compact}
              />
            ) : null}
            {actions}
          </div>
        </header>
        {leaveDialog}
        <div className="relative flex flex-1 min-h-0">
          <div
            ref={surface}
            className={`wb-surface ${drawing ? "is-drawing" : ""}`}
            data-tool={tool}
            tabIndex={0}
            aria-label="Whiteboard canvas"
            {...fileImport.handlers}
            onKeyDown={(event) => {
              // Escape puts down the tool and selection on the Board, so a
              // panel around it does not close too. Menus and fields that
              // used it already have marked it.
              if (
                event.key !== "Escape" ||
                event.nativeEvent.isComposing ||
                event.defaultPrevented
              )
                return;
              event.preventDefault();
              if (!isCanvasKeyTarget(surface.current, event.target)) return;
              typedAhead.current = null;
              setTrayOpen(false);
              setTool("move");
              setNodes((nodes) =>
                nodes.map((node) => ({ ...node, selected: false })),
              );
              setEdges((edges) =>
                edges.map((edge) => ({ ...edge, selected: false })),
              );
              setSelected([]);
              setSelectedEdges([]);
            }}
            onPointerDownCapture={(event) => {
              typedAhead.current = null;
              if (
                event.currentTarget.contains(event.target as Node) &&
                !(event.target as Element).closest(
                  "input,textarea,[contenteditable],button,select",
                )
              )
                event.currentTarget.focus({ preventScroll: true });
            }}
          >
            <BoardEdgeMarkers />
            <ReactFlow<WhiteboardFlowNode, BoardFlowEdge>
              nodes={canvasNodes}
              edges={edges}
              nodeTypes={nodeTypes}
              edgeTypes={boardEdgeTypes}
              proOptions={{ hideAttribution: true }}
              onNodesChange={onNodesChange}
              onEdgesChange={onEdgesChange}
              onInit={setFlow}
              onlyRenderVisibleElements
              minZoom={0.1}
              maxZoom={4}
              defaultViewport={{ x: 100, y: 80, zoom: 1 }}
              panOnDrag={tool === "move" ? [0, 1, 2] : [1, 2]}
              panOnScroll
              panOnScrollSpeed={0.8}
              zoomOnScroll={false}
              zoomOnPinch
              selectionKeyCode={selecting ? "Shift" : null}
              selectionOnDrag={tool === "select"}
              selectionMode={SelectionMode.Full}
              multiSelectionKeyCode={["Meta", "Control"]}
              deleteKeyCode={null}
              nodesDraggable={editing && selecting}
              nodesConnectable={editing && selecting}
              elementsSelectable={selecting}
              elevateNodesOnSelect={false}
              onSelectionChange={selectionChanged}
              onPaneClick={(event) => {
                if (editing && tool === "text" && flow)
                  createText(
                    flow.screenToFlowPosition({
                      x: event.clientX,
                      y: event.clientY,
                    }),
                  );
              }}
              isValidConnection={(connection) =>
                connection.source !== connection.target ||
                isLifeline(connection.source)
              }
              onConnect={(connection) => {
                if (!connection.source || !connection.target) return;
                // Between lifelines a connector is a message at the next free
                // row of its source.
                const message =
                  isLifeline(connection.source) &&
                  isLifeline(connection.target);
                commit([
                  {
                    type: "create_edge",
                    draftId: crypto.randomUUID(),
                    sourceRef: connection.source,
                    targetRef: connection.target,
                    label: "",
                    relation: "related",
                    ...(message
                      ? {
                          route: "message" as const,
                          anchorY: nextMessageRow(connection.source),
                        }
                      : {}),
                  },
                ]);
              }}
              onEdgeDoubleClick={(_event, edge) => {
                const current = snapshot.edges.find(
                  (item) => item.id === edge.id,
                );
                if (!editing || !current) return;
                setEdgeEdit(current);
              }}
              onNodeDrag={(_event, _node, dragged) => {
                dragPreviews.current = dragged.map((node) => ({
                  id: node.id,
                  ...node.position,
                }));
              }}
              onNodeDragStart={() => {
                dragOrigins.current = new Map(
                  snapshot.items.map((item) => [item.id, item]),
                );
              }}
              onNodeDragStop={(_event, _node, dragged) => {
                dragPreviews.current = [];
                const origins = dragOrigins.current;
                const moving = new Map(dragged.map((node) => [node.id, node]));
                // A child with an unacknowledged change keeps its position;
                // its revision would be stale.
                const pending = scene.pendingIds;
                const carried = (item: BoardItem) => {
                  let parent = item.parentFrameId;
                  while (parent) {
                    if (moving.has(parent)) return true;
                    parent = origins.get(parent)?.parentFrameId ?? null;
                  }
                  return false;
                };
                // What a moving frame carries stays in it; anything else
                // joins the frame it is dropped in, or leaves its frame.
                const frames = [...origins.values()].filter(
                  (item) =>
                    item.type === "frame" &&
                    !moving.has(item.id) &&
                    !carried(item),
                );
                const commands: BoardCommand[] = [];
                for (const node of dragged) {
                  const item = origins.get(node.id);
                  if (!item) continue;
                  const parentFrameId = carried(item)
                    ? item.parentFrameId
                    : boardFrameAround(frames, {
                        ...node.position,
                        width: item.width,
                        height: item.height,
                      });
                  commands.push({
                    type: "geometry",
                    id: item.id,
                    expectedRevision: item.geometryRevision,
                    geometry: {
                      ...geometryOf(item),
                      ...node.position,
                      parentFrameId,
                    },
                  });
                  if (item.type === "frame")
                    for (const child of [...origins.values()].filter(
                      (child) =>
                        // Locked objects stay put inside the moved frame.
                        !child.properties.locked &&
                        !pending.has(child.id) &&
                        !moving.has(child.id) &&
                        (() => {
                          let parent = child.parentFrameId;
                          while (parent) {
                            if (parent === item.id) return true;
                            if (moving.has(parent)) return false;
                            parent = origins.get(parent)?.parentFrameId ?? null;
                          }
                          return false;
                        })(),
                    ))
                      commands.push({
                        type: "geometry",
                        id: child.id,
                        expectedRevision: child.geometryRevision,
                        geometry: {
                          ...geometryOf(child),
                          x: child.x + node.position.x - item.x,
                          y: child.y + node.position.y - item.y,
                        },
                      });
                }
                const current = lastSnapshot.current;
                if (!commands.length || !current?.board.canEdit) return;
                void scene
                  .commit(commands, current.items, current.edges)
                  .catch((error: unknown) => {
                    // Nothing was recorded for a change refused before
                    // sending, so the dragged objects return to their place.
                    if (error instanceof BoardChangeTooLarge)
                      setNodes((nodes) =>
                        nodes.map((node) => {
                          const item = origins.get(node.id);
                          return moving.has(node.id) && item
                            ? { ...node, position: { x: item.x, y: item.y } }
                            : node;
                        }),
                      );
                    errorMessage(error);
                  });
              }}
            >
              <Background
                variant={BackgroundVariant.Dots}
                gap={24}
                size={1.25}
                color="color-mix(in srgb, var(--workspace-native-text-muted) 55%, var(--workspace-content-bg))"
              />
              <BoardPresence
                headerTarget={presenceTarget}
                scope={scope}
                surface={surface}
                selection={presenceSelection}
                dragPreviews={dragPreviews}
                items={snapshot.items}
              />
            </ReactFlow>
            {fileImport.dragging && (
              <div
                className="wb-image-drop-overlay absolute inset-[12px] z-[30] flex flex-col items-center justify-center gap-[8px] [border:2px_dashed_var(--workspace-native-text-muted)] rounded-[16px] [background:color-mix(in_srgb,_var(--workspace-content-bg)_92%,_transparent)] text-workspace-text pointer-events-none"
                role="status"
              >
                <strong>Drop files here</strong>
                <span>
                  Images appear on the Board; other files become file cards · Up
                  to 100 MB each (images up to 32 MB)
                </span>
              </div>
            )}
            {fileImport.progress && (
              <div
                className="absolute top-[16px] left-[50%] z-[30] max-w-[calc(100%_-_32px)] [transform:translateX(-50%)] p-[10px_16px] [border:1px_solid_var(--workspace-divider)] rounded-[12px] [background:var(--workspace-content-bg)] text-workspace-text text-[length:13px] pointer-events-none"
                role="status"
              >
                {fileImport.progress}
              </div>
            )}
            <BoardDrawing
              surface={surface}
              active={drawing && editing}
              color={color}
              width={tool === "highlighter" ? Math.max(12, width) : width}
              opacity={tool === "highlighter" ? 0.3 : 1}
              onComplete={completeDrawing}
            />
            <BoardPlacement
              surface={surface}
              mode={
                editing && (tool === "shape" || tool === "frame") ? tool : null
              }
              shape={shape}
              color={color}
              fill={fill}
              strokeWidth={shapeWidth}
              onComplete={tool === "shape" ? completeShape : completeFrame}
            />
            <BoardToolbar
              tool={tool}
              onToolChange={setTool}
              trayOpen={editing && !selecting && trayOpen}
              onTrayOpenChange={setTrayOpen}
              readOnlyReason={
                editing
                  ? undefined
                  : "You have view-only access to this Whiteboard."
              }
              canUndo={scene.canUndo && !scenePending}
              canRedo={scene.canRedo && !scenePending}
              onUndo={() => undo()}
              onRedo={() => undo(true)}
            >
              {editing && (selecting || trayOpen) && (
                <BoardStyleToolbar
                  key={selecting ? `selection:${selection.version}` : tool}
                  tool={tool}
                  selectedItemCount={selected.length}
                  selectedConnectorCount={selectedEdges.length}
                  singleTextSelected={Boolean(selectedText)}
                  selectedItemType={
                    selected.length === 1 ? firstSelected?.type : undefined
                  }
                  styleDisabled={styleDisabled}
                  textBackground={
                    selectedText ? textBackground(selectedText) : textFill
                  }
                  onTextBackgroundChange={changeTextBackground}
                  textBackgroundDisabled={
                    Boolean(selectedText) &&
                    (scenePending ||
                      !!selectedText?.properties.locked ||
                      !!selectedText?.id.startsWith("local:"))
                  }
                  color={color}
                  brushColors={{ pen: inkColor, highlighter: highlightColor }}
                  fill={fill}
                  width={styleWidth}
                  shape={shape}
                  onColorChange={
                    selecting
                      ? (color) => changeSelectionStyle({ color })
                      : setColor
                  }
                  onFillChange={
                    selecting
                      ? (fill) => changeSelectionStyle({ fill })
                      : tool === "frame"
                        ? setFrameFill
                        : setShapeFill
                  }
                  onColorPreview={
                    selecting
                      ? (color) =>
                          setStylePreview({
                            ids: selected,
                            properties: { color },
                          })
                      : undefined
                  }
                  onFillPreview={
                    selecting
                      ? (fill) =>
                          setStylePreview({
                            ids: selected,
                            properties: { fill },
                          })
                      : undefined
                  }
                  onWidthChange={
                    selecting
                      ? (strokeWidth) => changeSelectionStyle({ strokeWidth })
                      : setWidth
                  }
                  onShapeChange={setShape}
                  onToolChange={setTool}
                >
                  {selectedEdges.length === 1 && (
                    <>
                      <button
                        onClick={() =>
                          setEdgeEdit(
                            snapshot.edges.find(
                              (edge) => edge.id === selectedEdges[0],
                            ) ?? null,
                          )
                        }
                      >
                        Edit connector
                      </button>
                      <button aria-label="Delete connector" onClick={remove}>
                        <Trash2 size={17} />
                      </button>
                    </>
                  )}
                  {selected.length > 0 && (
                    <>
                      {selected.length > 1 && (
                        <>
                          <button
                            disabled={scenePending}
                            onClick={() => align("x")}
                          >
                            Align left
                          </button>
                          <button
                            disabled={scenePending}
                            onClick={() => align("y")}
                          >
                            Align top
                          </button>
                          <button disabled={scenePending} onClick={group}>
                            Group
                          </button>
                        </>
                      )}
                      <button
                        disabled={scenePending}
                        title="Lock or unlock selected objects"
                        aria-label="Lock or unlock selected objects"
                        onClick={() =>
                          commit(
                            chosen.map((item) => ({
                              type: "content",
                              id: item.id,
                              expectedRevision: item.contentRevision,
                              properties: {
                                ...item.properties,
                                locked: !item.properties.locked,
                              },
                            })),
                          )
                        }
                      >
                        <LockKeyhole size={17} />
                      </button>
                      <button
                        disabled={scenePending}
                        title="Delete selected objects or strokes"
                        aria-label="Delete selected objects or strokes"
                        onClick={remove}
                      >
                        <Trash2 size={17} />
                      </button>
                    </>
                  )}
                </BoardStyleToolbar>
              )}
            </BoardToolbar>
          </div>
        </div>
        {edgeEdit && (
          <BoardConnectorDialog
            key={edgeEdit.id}
            edge={edgeEdit}
            onClose={() => setEdgeEdit(null)}
            onSave={(change) => {
              commit([
                {
                  type: "update_edge",
                  id: edgeEdit.id,
                  expectedRevision: edgeEdit.revision,
                  sourceRef: edgeEdit.sourceId,
                  targetRef: edgeEdit.targetId,
                  ...change,
                },
              ]);
              setEdgeEdit(null);
            }}
          />
        )}
        {compartmentEdit && (
          <BoardCompartmentsDialog
            key={compartmentEdit.id}
            item={compartmentEdit}
            onClose={() => setCompartmentEdit(null)}
            onSave={(change) => {
              const item = compartmentEdit;
              const commands: BoardCommand[] = [
                {
                  type: "content",
                  id: item.id,
                  expectedRevision: item.contentRevision,
                  properties: { ...item.properties, ...change },
                },
              ];
              // A class or entity grows to show every member and column.
              if (item.properties.shape !== "fragment") {
                const width = Math.max(
                  item.width,
                  boardCompartmentsWidth(change.title, change.sections),
                );
                const height = Math.max(
                  item.height,
                  boardCompartmentsHeight(change.sections),
                );
                if (width !== item.width || height !== item.height)
                  commands.push({
                    type: "geometry",
                    id: item.id,
                    expectedRevision: item.geometryRevision,
                    geometry: { ...geometryOf(item), width, height },
                  });
              }
              commit(commands);
              setCompartmentEdit(null);
            }}
          />
        )}
      </section>
    </BoardNodeContext.Provider>
  );
}
