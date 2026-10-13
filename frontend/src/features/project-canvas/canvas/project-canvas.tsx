import { ArtifactReviewLoading } from "../review/artifact-review-loading";
import { ResultsViewSwitch } from "../documents/results-view-switch";
import type { PreviewOrigin } from "../review/artifact-focus-preview";
import { NodeCommentsContent } from "../documents/node-comments";
import presentationStyles from "./canvas-presentation.module.css";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubTrigger,
  DropdownMenuSubContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
} from "@/shared/ui/dropdown-menu";
import { useSearchParams } from "react-router";
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import {
  ReactFlow,
  ReactFlowProvider,
  applyNodeChanges,
  useReactFlow,
  useViewport,
  type Edge,
  type OnNodesChange,
  type Viewport,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  Plus,
  MoreHorizontal,
  ChevronDown,
  Search,
  Layers,
  LayoutGrid,
  List,
  Maximize2,
  Minimize2,
  Minus,
  Undo2,
  Play,
  ChevronLeft,
  ChevronRight,
  X,
  FileText,
  ArrowUp,
  ArrowUpRight,
  Trash2,
  Check,
  RotateCw,
  LoaderCircle,
  GitBranch,
  Columns3,
  Clock3,
  Workflow,
} from "@/shared/ui/icons";
import { useCanvasFullscreen } from "@/app/shell/workspace-layout-context";
import {
  useGetCanvasQuery,
  useCanvasCommandMutation,
  useCanvasSearchQuery,
  type CanvasArtifact,
  type CanvasCommand,
  type CanvasItem,
  type CanvasScope,
  type CanvasSnapshot,
} from "../api/canvas-api";
import {
  canvasNodeTypes,
  artifactFormat,
  type CanvasNode,
} from "./canvas-nodes";
import { CanvasPresence } from "./canvas-presence";
import { CanvasLibrary } from "./canvas-library";
import type { ExecuteCanvasCommand } from "./add-artifact";
import "./project-canvas.css";
import "../documents/document-workspace.css";
import "../documents/artifact-views.css";
const CanvasViews = lazy(() => import("./canvas-views"));
const ArtifactComparison = lazy(() =>
  import("../documents/artifact-comparison").then((module) => ({
    default: module.ArtifactComparison,
  })),
);
const viewOptions = [
  ["canvas", "Free layout", LayoutGrid],
  ["plan", "Plan", GitBranch],
  ["files", "List", List],
  ["timeline", "Timeline", Clock3],
  ["relationships", "Connections", Workflow],
  ["presentation", "Presentation", Play],
] as const;
type CanvasView = (typeof viewOptions)[number][0];
const AddArtifact = lazy(() =>
  import("./add-artifact").then((module) => ({ default: module.AddArtifact })),
);
const ArtifactFocusPreview = lazy(
  () => import("../review/artifact-focus-preview"),
);
const ArtifactReview = lazy(() =>
  import("../review/artifact-review").then((module) => ({
    default: module.ArtifactReview,
  })),
);
export default function ProjectCanvas(props: CanvasScope) {
  return (
    <ReactFlowProvider>
      <CanvasWorkspace key={props.projectId} {...props} />
    </ReactFlowProvider>
  );
}
function CanvasWorkspace(scope: CanvasScope) {
  const canvasFullscreen = useCanvasFullscreen();
  const query = useGetCanvasQuery(scope);
  const liveSnapshot = query.currentData;
  const [mutate] = useCanvasCommandMutation();
  const flow = useReactFlow<CanvasNode>();
  const [searchParams, setSearchParams] = useSearchParams();
  const surface = useRef<HTMLDivElement>(null);
  const [nodes, setNodes] = useState<CanvasNode[]>([]);
  const [add, setAdd] = useState<{
    initial: "document" | "upload";
    taskId?: string;
  } | null>(null);
  const [review, setReview] = useState<{
    artifactId: string;
    itemId?: string;
    versionId?: string;
    threadId?: string;
    blockId?: string;
    reviewOnly?: boolean;
  }>();
  const [library, setLibrary] = useState(false);
  const [libraryOpen, setLibraryOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [searchTerm, setSearchTerm] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setSearchTerm(search.trim()), 250);
    return () => clearTimeout(timer);
  }, [search]);
  const contentSearch = useCanvasSearchQuery(
    { ...scope, query: searchTerm },
    { skip: searchTerm.length < 2 || !library },
  );
  const [selectedEdge, setSelectedEdge] = useState("");
  const [relationship, setRelationship] = useState("Supports");
  const [filters, setFilters] = useState({
    review: "",
    type: "",
    contributor: "",
    task: "",
  });
  const [collapsed, setCollapsed] = useState<string[]>([]);
  const requestedView = searchParams.get("canvasView");
  const mode: CanvasView =
    viewOptions.find(([id]) => id === requestedView)?.[0] ?? "canvas";
  const setMode = (view: CanvasView) => {
    const next = new URLSearchParams(searchParams);
    next.set("canvasView", view);
    setSearchParams(next, { replace: true });
  };
  const [compare, setCompare] = useState(false);
  const [expandedTask, setExpandedTask] = useState("");
  const openAdd = (initial: "document" | "upload", taskId?: string) => {
    setAdd({
      initial,
      taskId:
        taskId ??
        (mode === "plan" && expandedTask && expandedTask !== "resources"
          ? expandedTask
          : undefined),
    });
  };
  const freeCamera = useRef<Viewport | undefined>(undefined);
  const planCamera = useRef<Viewport | undefined>(undefined);
  const relationshipCamera = useRef<Viewport | undefined>(undefined);
  const [pendingPresentation, setPendingPresentation] = useState<number>();
  const [saving, setSaving] = useState(false);
  const [interacting, setInteracting] = useState(false);
  const interactionBase = useRef<CanvasSnapshot | undefined>(undefined);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState<CanvasCommand>();
  const [undo, setUndo] = useState<CanvasCommand[]>([]);
  const [presentation, setPresentation] = useState<{
    frames: CanvasItem[];
    index: number;
    camera: Viewport;
    scene: CanvasSnapshot;
  }>();
  const snapshot = useMemo(() => {
    if (!presentation || !liveSnapshot) return liveSnapshot;
    const allowed = new Set(liveSnapshot.artifactIds);
    const visible = new Set(liveSnapshot.items.map((row) => row.id));
    return {
      ...presentation.scene,
      canEdit: false,
      canEditDocuments: false,
      canManageFiles: false,
      artifacts: presentation.scene.artifacts.filter((row) =>
        allowed.has(row.id),
      ),
      items: presentation.scene.items
        .filter((row) => visible.has(row.id))
        .map((row) => ({ ...row, versionPolicy: "pinned" })),
      edges: presentation.scene.edges.filter(
        (row) => visible.has(row.source) && visible.has(row.target),
      ),
    };
  }, [presentation, liveSnapshot]);
  const latest = useRef(snapshot);
  latest.current = snapshot;
  const onCommentsOpenChange = useCallback(
    (id: string | null, open: boolean) => {
      setNodes((current) => {
        let changed = false;
        const next = current.map((node) => {
          const commentsOpen = open
            ? node.id === id
            : id === null || node.id === id
              ? false
              : node.data.commentsOpen;
          if (commentsOpen === node.data.commentsOpen) return node;
          changed = true;
          return { ...node, data: { ...node.data, commentsOpen } };
        });
        return changed ? next : current;
      });
    },
    [],
  );
  const beginInteraction = useCallback(() => {
    onCommentsOpenChange(null, false);
    interactionBase.current = latest.current;
    setInteracting(true);
  }, [onCommentsOpenChange]);
  const selected = nodes.filter((node) => node.selected);
  const canEdit = snapshot?.canEdit ?? false;
  // Uploading a file is its own permission (Manage project files), separate
  // from Create and edit tasks; the server checks it on add_artifact and
  // add_version with an attachment.
  const canManageFiles = snapshot?.canManageFiles ?? false;
  const canAdd = canEdit || canManageFiles;
  const artifacts = useMemo(
    () => new Map(snapshot?.artifacts.map((value) => [value.id, value])),
    [snapshot?.artifacts],
  );
  const edges = useMemo<Edge[]>(
    () =>
      (snapshot?.edges ?? []).map((edge) => ({
        id: edge.id,
        selected: edge.id === selectedEdge,
        source: edge.source,
        target: edge.target,
        label: (() => {
          const source = snapshot?.items.find(
            (item) => item.id === edge.source,
          );
          const artifact = snapshot?.artifacts.find(
            (row) => row.id === source?.artifactId,
          );
          return `${edge.label || "Related"}${source?.versionPolicy === "pinned" && source.version?.id && artifact?.latestVersionId !== source.version.id ? " · Source changed—review recommended" : ""}`;
        })(),
        type: "smoothstep",
        style: {
          stroke: "var(--workspace-native-text-faint)",
          strokeWidth: 1.3,
        },
        labelStyle: {
          fill: "var(--workspace-native-text-muted)",
          fontSize: 11,
        },
        labelBgStyle: { fill: "var(--workspace-content-bg)" },
      })),
    [snapshot?.edges, snapshot?.items, snapshot?.artifacts, selectedEdge],
  );
  const execute: ExecuteCanvasCommand = useCallback(
    async (input) => {
      const command = {
        ...input,
        requestKey: input.requestKey ?? crypto.randomUUID(),
      } as CanvasCommand;
      setSaving(true);
      setError("");
      setRetry(undefined);
      try {
        const result = await mutate({ ...scope, command }).unwrap();
        const refreshed = await query.refetch().unwrap();
        // RTK may join a notification fetch that began before this commit.
        // Keep the local gesture until the acknowledged sequence is visible.
        if (refreshed.sequence < result.sequence)
          await query.refetch().unwrap();
        return result;
      } catch (reason) {
        const problem = reason as {
          status?: number;
          message?: string;
          detail?: string;
          data?: { detail?: string };
        };
        const message =
          problem.status === 409
            ? "Someone changed this while you were working. The latest version is loaded; review it and try again."
            : problem.message ||
              problem.detail ||
              problem.data?.detail ||
              "This change could not be saved. Your work is still here; retry when connected.";
        setError(message);
        if (!problem.status || problem.status >= 500) setRetry(command);
        else void query.refetch();
        throw new Error(message);
      } finally {
        setSaving(false);
      }
    },
    [mutate, query.refetch, scope.companyId, scope.projectId],
  );
  const openedLink = useRef("");
  useEffect(() => {
    if (openedLink.current === searchParams.toString()) return;
    const artifactId = searchParams.get("artifactId"),
      wikiPageId = searchParams.get("wikiPageId");
    const found = latest.current?.artifacts.find((row) =>
      artifactId
        ? row.id === artifactId
        : !!wikiPageId && row.latestVersion.wikiPageId === wikiPageId,
    );
    if (found) {
      openedLink.current = searchParams.toString();
      setReview({
        artifactId: found.id,
        versionId: searchParams.get("versionId") ?? found.latestVersionId,
        reviewOnly:
          searchParams.has("versionId") &&
          searchParams.get("mode") !== "document",
      });
    }
  }, [searchParams, snapshot?.artifacts]);
  const onOpen = useCallback(
    (item: CanvasItem, threadId?: string) => {
      onCommentsOpenChange(null, false);
      if (item.artifactId)
        setReview({
          artifactId: item.artifactId,
          itemId: item.id,
          threadId,
          reviewOnly:
            !!threadId ||
            item.versionPolicy === "accepted" ||
            (item.versionPolicy === "pinned" && !!item.version?.wikiPageId),
          versionId:
            item.version?.id ??
            item.pinnedVersionId ??
            latest.current?.artifacts.find((row) => row.id === item.artifactId)
              ?.acceptedVersionId ??
            undefined,
        });
    },
    [onCommentsOpenChange],
  );
  const saveGeometry = useCallback(
    async (
      changes: {
        id: string;
        x: number;
        y: number;
        width: number;
        height: number;
      }[],
    ) => {
      const state = interactionBase.current ?? latest.current;
      if (!state?.canEdit) {
        setInteracting(false);
        return;
      }
      const rows = new Map(state.items.map((row) => [row.id, row]));
      const moves = changes.map((next) => ({
        ...next,
        expectedRevision: rows.get(next.id)!.geometryRevision,
      }));
      const inverse = moves.map((next) => {
        const before = rows.get(next.id)!;
        return {
          id: next.id,
          x: before.x,
          y: before.y,
          width: before.width,
          height: before.height,
          expectedRevision: next.expectedRevision + 1,
        };
      });
      try {
        await execute({ type: "move", moves });
        setUndo((stack) => [
          ...stack.slice(-19),
          { type: "move", moves: inverse, requestKey: crypto.randomUUID() },
        ]);
      } catch {
        /* The save banner keeps the failed command for retry. */
      } finally {
        interactionBase.current = undefined;
        setInteracting(false);
      }
    },
    [execute],
  );
  const onResize = useCallback(
    (
      id: string,
      bounds: { x: number; y: number; width: number; height: number },
    ) => {
      const item = latest.current?.items.find((row) => row.id === id);
      const parent = latest.current?.items.find(
        (row) => row.id === item?.parentFrameId,
      );
      void saveGeometry([
        {
          id,
          ...bounds,
          x: bounds.x + (parent?.x ?? 0),
          y: bounds.y + (parent?.y ?? 0),
        },
      ]);
    },
    [saveGeometry],
  );
  useEffect(() => {
    if (!snapshot || saving || retry || interacting) return;
    const parents = new Map(
      snapshot.items
        .filter((item) => item.type === "frame")
        .map((item) => [item.id, item]),
    );
    setNodes((current) => {
      const previous = new Map(current.map((node) => [node.id, node]));
      return [...snapshot.items]
        .sort((a, b) => Number(b.type === "frame") - Number(a.type === "frame"))
        .map((item) => {
          const before = previous.get(item.id),
            artifact = item.artifactId
              ? artifacts.get(item.artifactId)
              : undefined;
          if (
            before?.data.item === item &&
            before.data.artifact === artifact &&
            before.data.canEdit === canEdit
          )
            return before;
          const parent = item.parentFrameId
            ? parents.get(item.parentFrameId)
            : undefined;
          return {
            id: item.id,
            type: item.type as "artifact" | "frame",
            position: {
              x: item.x - (parent?.x ?? 0),
              y: item.y - (parent?.y ?? 0),
            },
            parentId: parent?.id,
            style: { width: item.width, height: item.height },
            width: item.width,
            height: item.height,
            zIndex: item.type === "frame" ? -1 : 1,
            selected: before?.selected ?? false,
            data: {
              scope,
              item,
              artifact,
              canEdit,
              commentsOpen: before?.data.commentsOpen ?? false,
              commentsDocked: true,
              onCommentsOpenChange,
              onOpen,
              onResize,
              onInteractionStart: beginInteraction,
            },
            ariaLabel: `${item.type === "frame" ? "Frame" : "Artifact"}: ${item.title || artifact?.title || "Untitled"}`,
            dragHandle: item.type === "frame" ? ".pc-frame-label" : undefined,
          };
        });
    });
  }, [
    snapshot,
    artifacts,
    canEdit,
    onCommentsOpenChange,
    onOpen,
    onResize,
    saving,
    retry,
    interacting,
    beginInteraction,
  ]);
  const onNodesChange: OnNodesChange<CanvasNode> = useCallback(
    (changes) => setNodes((current) => applyNodeChanges(changes, current)),
    [],
  );
  const center = () => {
    if (mode !== "canvas")
      return {
        x: 40,
        y:
          Math.max(
            0,
            ...(snapshot?.items.map((item) => item.y + item.height) ?? []),
          ) + 80,
      };
    const bounds = surface.current?.getBoundingClientRect();
    return flow.screenToFlowPosition({
      x: (bounds?.left ?? 0) + (bounds?.width ?? 900) / 2 - 160,
      y: (bounds?.top ?? 0) + (bounds?.height ?? 600) / 2 - 140,
    });
  };
  const openArtifact = useCallback(
    (artifact: CanvasArtifact, versionId?: string, threadId?: string) => {
      setCompare(false);
      setReview({
        artifactId: artifact.id,
        versionId,
        reviewOnly: !!versionId || !!threadId,
        threadId,
      });
    },
    [],
  );
  const placeArtifact = (artifact: CanvasArtifact) => {
    const position = center();
    void execute({
      type: "place",
      versionId: artifact.latestVersionId,
      ...position,
    })
      .then(() => setMode("canvas"))
      .catch(() => {});
  };
  const frames = useMemo(
    () =>
      snapshot?.items
        .filter((row) => row.type === "frame")
        .sort(
          (a, b) =>
            a.stackOrder - b.stackOrder ||
            a.y - b.y ||
            a.x - b.x ||
            a.id.localeCompare(b.id),
        ) ?? [],
    [snapshot?.items],
  );
  const unplaced = useMemo(() => {
    const placed = new Set(snapshot?.items.map((row) => row.artifactId));
    return snapshot?.artifacts.filter((row) => !placed.has(row.id)).length ?? 0;
  }, [snapshot?.items, snapshot?.artifacts]);
  const configure = (item: CanvasItem, changes: Partial<CanvasCommand>) =>
    execute({
      type: "configure",
      id: item.id,
      expectedRevision: item.contentRevision,
      title: item.title,
      viewConfig: item.viewConfig,
      parentFrameId: item.parentFrameId,
      representation: item.representation,
      versionPolicy: item.versionPolicy,
      versionId: item.pinnedVersionId,
      ...changes,
    });
  const [focusedPreview, setFocusedPreview] = useState<{
    itemId: string;
    origin?: PreviewOrigin;
  }>();
  const focusedItem = snapshot?.items.find(
    (item) => item.id === focusedPreview?.itemId,
  );
  const focusedArtifact = focusedItem?.artifactId
    ? artifacts.get(focusedItem.artifactId)
    : undefined;
  const focusedVersion = focusedItem?.version ?? focusedArtifact?.latestVersion;
  const commentsNode = nodes.find((node) => node.data.commentsOpen);
  const commentsVersion =
    commentsNode?.data.item.version ??
    commentsNode?.data.artifact?.latestVersion;
  const selectedItem =
    selected.length === 1
      ? snapshot?.items.find((item) => item.id === selected[0].id)
      : undefined;
  const showFrame = (index: number) => {
    if (!presentation) return;
    const frame = presentation.frames[index];
    setPresentation({ ...presentation, index });
    void flow.fitBounds(
      { x: frame.x, y: frame.y, width: frame.width, height: frame.height },
      { padding: 0.12, duration: 400 },
    );
  };
  const endPresentation = () => {
    if (presentation)
      void flow.setViewport(presentation.camera, { duration: 300 });
    setPresentation(undefined);
  };
  const startPresentation = useCallback(
    (index = 0) => {
      if (!frames.length || !snapshot) return;
      setPresentation({
        frames,
        index,
        camera: flow.getViewport(),
        scene: snapshot,
      });
      void flow.fitBounds(frames[index], { padding: 0.12, duration: 400 });
    },
    [frames, snapshot, flow],
  );
  useEffect(() => {
    if (
      pendingPresentation === undefined ||
      mode !== "canvas" ||
      !flow.viewportInitialized
    )
      return;
    const request = requestAnimationFrame(() => {
      startPresentation(pendingPresentation);
      setPendingPresentation(undefined);
    });
    return () => cancelAnimationFrame(request);
  }, [pendingPresentation, mode, flow.viewportInitialized, startPresentation]);
  const closeReview = () => {
    setReview(undefined);
    const next = new URLSearchParams(searchParams);
    for (const key of [
      "artifactId",
      "versionId",
      "wikiPageId",
      "mode",
      "outputItemId",
    ])
      next.delete(key);
    setSearchParams(next, { replace: true });
  };
  const activeArtifact = review && artifacts.get(review.artifactId);
  return (
    <div
      className={`project-canvas ${mode === "canvas" ? presentationStyles.slateCanvas : ""} ${presentation ? "is-presenting" : ""}`}
    >
      <header
        className={`pc-header ${canvasFullscreen.active ? presentationStyles.fullscreenHeader : ""}`}
      >
        <h2 className="m-0 text-workspace-text text-[length:16px] font-[600]">
          Results
        </h2>
        <ResultsViewSwitch />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              className={presentationStyles["pc-view-picker"]}
              disabled={!!presentation}
              aria-label="Canvas layout"
            >
              {viewOptions.find(([id]) => id === mode)?.[1]}
              <ChevronDown size={14} />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuRadioGroup
              value={mode}
              onValueChange={(value) => setMode(value as CanvasView)}
            >
              {viewOptions.map(([id, label]) => (
                <DropdownMenuRadioItem key={id} value={id}>
                  {label}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
        <div className="pc-header-actions">
          {saving && (
            <span className="pc-save-state" role="status">
              <LoaderCircle size={13} className="pc-spin" /> Saving…
            </span>
          )}
          {canAdd && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button className="pc-text-button" aria-label="Add to canvas">
                  <Plus size={17} /> Add
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {canEdit && (
                  <DropdownMenuItem onSelect={() => openAdd("document")}>
                    <FileText size={16} /> Document
                  </DropdownMenuItem>
                )}
                {canManageFiles && (
                  <DropdownMenuItem onSelect={() => openAdd("upload")}>
                    <ArrowUp size={16} /> Upload file
                  </DropdownMenuItem>
                )}
                {canEdit && mode === "canvas" && (
                  <DropdownMenuItem
                    onSelect={() =>
                      void execute({
                        type: "frame",
                        title: "New frame",
                        width: 780,
                        height: 560,
                        ...center(),
                      }).catch(() => {})
                    }
                  >
                    <LayoutGrid size={16} /> Frame
                  </DropdownMenuItem>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          <button
            type="button"
            className="pc-text-button"
            aria-label={
              canvasFullscreen.active ? "Exit full screen" : "Full screen"
            }
            aria-pressed={canvasFullscreen.active}
            onClick={canvasFullscreen.toggle}
          >
            {canvasFullscreen.active ? (
              <Minimize2 size={17} aria-hidden="true" />
            ) : (
              <Maximize2 size={17} aria-hidden="true" />
            )}
            {canvasFullscreen.active ? "Exit full screen" : "Full screen"}
          </button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                className="pc-icon-button"
                aria-label="Canvas options"
                disabled={!!presentation}
              >
                <MoreHorizontal size={19} />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {mode === "canvas" && (
                <DropdownMenuItem onSelect={() => setLibrary(!library)}>
                  <Search size={16} />{" "}
                  {library ? "Close artifact browser" : "Browse artifacts"}
                </DropdownMenuItem>
              )}
              <DropdownMenuItem onSelect={() => setLibraryOpen(true)}>
                <Layers size={16} /> Library & saved views
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={!snapshot?.artifacts.length}
                onSelect={() => setCompare(true)}
              >
                <Columns3 size={16} /> Compare artifacts
              </DropdownMenuItem>
              {mode === "canvas" && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    disabled={!frames.length}
                    onSelect={() => startPresentation()}
                  >
                    <Play size={16} /> Present frames
                  </DropdownMenuItem>
                  {canEdit && unplaced > 0 && (
                    <DropdownMenuItem
                      disabled={saving}
                      onSelect={() =>
                        void execute({ type: "place_all" })
                          .then(() => {
                            requestAnimationFrame(
                              () =>
                                void flow.fitView({
                                  padding: 0.2,
                                  maxZoom: 0.95,
                                }),
                            );
                          })
                          .catch(() => {})
                      }
                    >
                      Arrange {unplaced} unplaced artifacts
                    </DropdownMenuItem>
                  )}
                  {canEdit && (
                    <DropdownMenuSub>
                      <DropdownMenuSubTrigger>
                        Connection type
                      </DropdownMenuSubTrigger>
                      <DropdownMenuSubContent>
                        <DropdownMenuRadioGroup
                          value={relationship}
                          onValueChange={setRelationship}
                        >
                          {[
                            "Supports",
                            "Derived from",
                            "Uses as input",
                            "Alternative to",
                            "Depends on",
                          ].map((value) => (
                            <DropdownMenuRadioItem key={value} value={value}>
                              {value}
                            </DropdownMenuRadioItem>
                          ))}
                        </DropdownMenuRadioGroup>
                      </DropdownMenuSubContent>
                    </DropdownMenuSub>
                  )}
                </>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>
      {libraryOpen && snapshot && (
        <CanvasLibrary
          scope={scope}
          snapshot={snapshot}
          execute={execute}
          onClose={() => setLibraryOpen(false)}
          view={{
            mode,
            search,
            camera: flow.getViewport(),
            filters,
            collapsed,
          }}
          onView={(config) => {
            if (
              typeof config.mode === "string" &&
              viewOptions.some(([id]) => id === config.mode)
            )
              setMode(config.mode as CanvasView);
            if (typeof config.search === "string") setSearch(config.search);
            if (config.filters && typeof config.filters === "object") {
              const value = config.filters as Record<string, unknown>;
              setFilters({
                review: typeof value.review === "string" ? value.review : "",
                type: typeof value.type === "string" ? value.type : "",
                contributor:
                  typeof value.contributor === "string"
                    ? value.contributor
                    : "",
                task: typeof value.task === "string" ? value.task : "",
              });
            }
            if (Array.isArray(config.collapsed))
              setCollapsed(
                config.collapsed.filter(
                  (id): id is string => typeof id === "string",
                ),
              );
            const camera = config.camera as Viewport | undefined;
            if (
              camera &&
              Number.isFinite(camera.x) &&
              Number.isFinite(camera.y) &&
              Number.isFinite(camera.zoom)
            )
              void flow.setViewport(camera, { duration: 250 });
          }}
        />
      )}
      {error && (
        <div className="pc-save-error" role="alert">
          <span>{error}</span>
          {retry && (
            <button
              disabled={saving}
              onClick={() => void execute(retry).catch(() => {})}
            >
              Retry change
            </button>
          )}
          <button
            aria-label="Dismiss error"
            onClick={() => {
              setError("");
              setRetry(undefined);
            }}
          >
            <X size={16} />
          </button>
        </div>
      )}
      {mode === "plan" && query.isError && (
        <div
          className="m-0 p-[8px_24px] text-[length:11px] text-[var(--pc-muted)] [background:var(--workspace-control-bg)]"
          role="status"
        >
          Artifacts couldn’t be refreshed. Your task plan is still available.{" "}
          <button
            className="pc-text-button"
            onClick={() => void query.refetch()}
          >
            Retry artifacts
          </button>
        </div>
      )}
      {query.isLoading && mode !== "plan" ? (
        <div className="pc-state flex-1 flex flex-col items-center justify-center gap-[16px]">
          <LoaderCircle size={26} className="pc-spin" />
          <h3>Opening your project space</h3>
        </div>
      ) : query.isError && !snapshot && mode !== "plan" ? (
        <div className="pc-state flex-1 flex flex-col items-center justify-center gap-[16px]">
          <Layers size={30} />
          <h3>We couldn’t open this canvas</h3>
          <button className="pc-button" onClick={() => void query.refetch()}>
            <RotateCw size={16} />
            Try again
          </button>
        </div>
      ) : (
        <div className="flex-1 min-h-0 flex overflow-hidden">
          {library && mode === "canvas" && (
            <aside className="pc-library">
              <header>
                <h3>Library</h3>
                <span>{snapshot?.artifacts.length}</span>
                {
                  <button
                    className="pc-icon-button"
                    aria-label="Close library"
                    onClick={() => setLibrary(false)}
                  >
                    <X size={16} />
                  </button>
                }
              </header>
              <label className="pc-search">
                <Search size={17} />
                <input
                  aria-label="Search artifacts"
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                  placeholder="Search titles and document content…"
                />
              </label>
              {searchTerm.length >= 2 && (
                <div className="pc-search-results">
                  {contentSearch.isFetching && <p>Searching documents…</p>}
                  {contentSearch.isError && (
                    <p>Content search is unavailable.</p>
                  )}
                  {contentSearch.currentData?.items.map((hit) => (
                    <button
                      key={hit.versionId}
                      onClick={() =>
                        setReview({
                          artifactId: hit.artifactId,
                          versionId: hit.versionId,
                          blockId: hit.blockId,
                        })
                      }
                    >
                      <strong>{hit.title}</strong>
                      <small>{hit.excerpt || "Open document"}</small>
                    </button>
                  ))}
                </div>
              )}
              <div className="pc-library-filters grid grid-cols-[1fr_1fr] gap-[6px] p-[10px]">
                <select
                  aria-label="Filter artifact type"
                  value={filters.type}
                  onChange={(event) =>
                    setFilters({ ...filters, type: event.target.value })
                  }
                >
                  <option value="">All types</option>
                  <option value="wiki">Documents</option>
                  <option value="file">Files</option>
                  <option value="output">Saved outputs</option>
                </select>
                <select
                  aria-label="Filter contributor"
                  value={filters.contributor}
                  onChange={(event) =>
                    setFilters({ ...filters, contributor: event.target.value })
                  }
                >
                  <option value="">All contributors</option>
                  {[
                    ...new Set(
                      snapshot?.artifacts.map((row) => row.creatorName),
                    ),
                  ].map((name) => (
                    <option key={name}>{name}</option>
                  ))}
                </select>
                <select
                  aria-label="Filter Task"
                  value={filters.task}
                  onChange={(event) =>
                    setFilters({ ...filters, task: event.target.value })
                  }
                >
                  <option value="">All Tasks</option>
                  {[
                    ...new Set(
                      snapshot?.artifacts
                        .map((row) => row.taskId)
                        .filter((id): id is string => !!id),
                    ),
                  ].map((id) => (
                    <option key={id} value={id}>
                      Task {id.slice(0, 8)}
                    </option>
                  ))}
                </select>
              </div>
              <ArtifactLibrary
                artifacts={(snapshot?.artifacts ?? []).filter(
                  (row) =>
                    row.title.toLowerCase().includes(search.toLowerCase()) &&
                    (!filters.review || row.reviewState === filters.review) &&
                    (!filters.type ||
                      row.latestVersion.sourceType === filters.type) &&
                    (!filters.contributor ||
                      row.creatorName === filters.contributor) &&
                    (!filters.task || row.taskId === filters.task),
                )}
                canEdit={canEdit}
                onOpen={openArtifact}
                onPlace={(artifact) =>
                  void execute({
                    type: "place",
                    versionId: artifact.latestVersionId,
                    ...center(),
                  }).catch(() => {})
                }
              />
            </aside>
          )}
          {(snapshot || mode === "plan") && mode !== "canvas" && (
            <Suspense
              fallback={
                <div className="pc-state flex-1 flex flex-col items-center justify-center gap-[16px]">
                  Opening view…
                </div>
              }
            >
              <CanvasViews
                mode={mode}
                scope={scope}
                snapshot={snapshot}
                onOpen={openArtifact}
                onPlace={canEdit ? placeArtifact : undefined}
                planCamera={planCamera}
                relationshipCamera={relationshipCamera}
                onAdd={canAdd ? openAdd : undefined}
                canEditDocuments={snapshot?.canEditDocuments ?? false}
                canManageFiles={canManageFiles}
                expandedTask={expandedTask}
                onExpand={setExpandedTask}
                onTask={(id) => {
                  const next = new URLSearchParams(searchParams);
                  next.set("taskId", id);
                  setSearchParams(next);
                }}
                onStart={(index) => {
                  setPendingPresentation(index);
                  setMode("canvas");
                }}
                onCanvas={() => setMode("canvas")}
              />
            </Suspense>
          )}
          {mode === "canvas" && (
            <div
              className="pc-surface flex-1 min-w-0 min-h-0 relative isolate"
              ref={surface}
              onKeyDownCapture={(event) => {
                if (
                  (event.target as HTMLElement).closest(
                    "input,textarea,select,button",
                  )
                )
                  return;
                if (presentation) {
                  if (event.key === "Escape") {
                    event.preventDefault();
                    endPresentation();
                  }
                  if (
                    event.key === "ArrowRight" &&
                    presentation.index + 1 < presentation.frames.length
                  ) {
                    event.preventDefault();
                    showFrame(presentation.index + 1);
                  }
                  if (event.key === "ArrowLeft" && presentation.index > 0) {
                    event.preventDefault();
                    showFrame(presentation.index - 1);
                  }
                  return;
                }
                if (!(event.target as HTMLElement).closest(".react-flow__node"))
                  return;
                if (event.key === "Enter" && selectedItem) {
                  event.preventDefault();
                  onOpen(selectedItem);
                  return;
                }
                const direction: Record<string, [number, number]> = {
                  ArrowLeft: [-1, 0],
                  ArrowRight: [1, 0],
                  ArrowUp: [0, -1],
                  ArrowDown: [0, 1],
                };
                const delta = direction[event.key];
                if (!delta || !canEdit) return;
                event.preventDefault();
                event.stopPropagation();
                if (saving || event.repeat || !snapshot) return;
                const ids = new Set(selected.map((row) => row.id));
                const moves = snapshot.items
                  .filter(
                    (row) =>
                      ids.has(row.id) ||
                      (row.parentFrameId && ids.has(row.parentFrameId)),
                  )
                  .map((row) => ({
                    id: row.id,
                    x: row.x + delta[0] * (event.shiftKey ? 40 : 10),
                    y: row.y + delta[1] * (event.shiftKey ? 40 : 10),
                    width: row.width,
                    height: row.height,
                  }));
                if (moves.length) void saveGeometry(moves);
              }}
            >
              <ReactFlow<CanvasNode>
                nodes={nodes.map((node) => ({
                  ...node,
                  hidden:
                    !presentation &&
                    !!node.parentId &&
                    collapsed.includes(node.parentId),
                }))}
                edges={edges}
                nodeTypes={canvasNodeTypes}
                onNodesChange={onNodesChange}
                onEdgeClick={(_event, edge) => {
                  setSelectedEdge(edge.id);
                  onCommentsOpenChange(null, false);
                }}
                onPaneClick={() => {
                  setSelectedEdge("");
                  onCommentsOpenChange(null, false);
                }}
                onNodeClick={(_event, node) => {
                  setSelectedEdge("");
                  if (node.type === "frame") onCommentsOpenChange(null, false);
                }}
                onNodeDoubleClick={(event, node) => {
                  if (node.type !== "artifact") return;
                  onCommentsOpenChange(null, false);
                  const bounds = (event.target as Element)
                    .closest(".react-flow__node")
                    ?.getBoundingClientRect();
                  setFocusedPreview({
                    itemId: node.id,
                    origin: bounds
                      ? {
                          x: bounds.x,
                          y: bounds.y,
                          width: bounds.width,
                          height: bounds.height,
                        }
                      : undefined,
                  });
                }}
                onNodeDragStart={beginInteraction}
                onNodeDragStop={(_event, _node, dragged) => {
                  const all = flow.getNodes();
                  const byId = new Map(all.map((row) => [row.id, row]));
                  const affected = new Set(dragged.map((row) => row.id));
                  for (const row of all)
                    if (row.parentId && affected.has(row.parentId))
                      affected.add(row.id);
                  const moves = [...affected].map((id) => {
                    const row = byId.get(id)!;
                    const parent = row.parentId
                      ? byId.get(row.parentId)
                      : undefined;
                    return {
                      id,
                      x: row.position.x + (parent?.position.x ?? 0),
                      y: row.position.y + (parent?.position.y ?? 0),
                      width: row.measured?.width ?? row.width ?? 320,
                      height: row.measured?.height ?? row.height ?? 280,
                    };
                  });
                  void saveGeometry(moves);
                }}
                onConnect={(connection) => {
                  if (connection.source && connection.target)
                    void execute({
                      type: "connect",
                      title: relationship,
                      sourceId: connection.source,
                      targetId: connection.target,
                    }).catch(() => {});
                }}
                nodesDraggable={canEdit && !saving && !retry && !presentation}
                nodesConnectable={canEdit && !saving && !presentation}
                deleteKeyCode={null}
                minZoom={0.1}
                maxZoom={2}
                onlyRenderVisibleElements
                defaultViewport={freeCamera.current}
                onMoveEnd={(_event, viewport) => {
                  if (!presentation) freeCamera.current = viewport;
                }}
                fitView={!freeCamera.current}
                fitViewOptions={{ maxZoom: 0.95, padding: 0.2 }}
                panOnScroll
                zoomOnScroll={false}
                selectionOnDrag
                panOnDrag={[1, 2]}
                selectionKeyCode="Shift"
                multiSelectionKeyCode={["Meta", "Control"]}
                proOptions={{ hideAttribution: true }}
              />
              {snapshot && (
                <CanvasPresence
                  scope={scope}
                  surface={surface}
                  presenting={!!presentation}
                />
              )}
              {!snapshot?.items.length && (
                <div className="pc-empty absolute inset-0 flex items-center justify-center flex-col pointer-events-none p-[92px_24px_75px]">
                  <div className="pc-empty-artifacts" aria-hidden>
                    <div>
                      <FileText size={24} />
                      <span>Ideas take shape</span>
                    </div>
                    <div>
                      <MessageSquarePlaceholder />
                    </div>
                    <div>
                      <Layers size={24} />
                      <span>Work comes together</span>
                    </div>
                  </div>
                  <div className="text-[var(--pc-soft)] text-[length:9px] font-[650] tracking-[1.5px]">
                    A SHARED PLACE FOR WHAT’S NEXT
                  </div>
                  <h3>Make room for your best work.</h3>
                  <p>
                    Gather documents, designs, and decisions.
                    <br />
                    See the whole picture. Work through the details together.
                  </p>
                  {canAdd && (
                    <div>
                      {canEdit && (
                        <button
                          className="pc-primary"
                          onClick={() => openAdd("document")}
                        >
                          <Plus size={17} />
                          Create your first artifact
                        </button>
                      )}
                      {canManageFiles && (
                        <button
                          className={canEdit ? "pc-text-button" : "pc-primary"}
                          onClick={() => openAdd("upload")}
                        >
                          <ArrowUp size={16} />
                          Upload a file
                        </button>
                      )}
                    </div>
                  )}
                </div>
              )}
              {commentsNode && commentsVersion && !presentation && (
                <aside
                  className={`pc-node-comments ${presentationStyles.commentsSidebar}`}
                  aria-label="Artifact comments"
                  onKeyDown={(event) => {
                    if (event.key === "Escape") {
                      event.stopPropagation();
                      onCommentsOpenChange(null, false);
                    }
                  }}
                >
                  <NodeCommentsContent
                    scope={scope}
                    version={commentsVersion}
                    title={
                      commentsNode.data.item.title ||
                      commentsNode.data.artifact?.title ||
                      "Artifact"
                    }
                    onClose={() => onCommentsOpenChange(null, false)}
                    onOpen={(threadId) =>
                      onOpen(commentsNode.data.item, threadId)
                    }
                  />
                </aside>
              )}
              {selectedEdge && canEdit && !presentation && (
                <div className="pc-selection-tools">
                  <span>
                    {snapshot?.edges.find((row) => row.id === selectedEdge)
                      ?.label || "Connection"}
                  </span>
                  <button
                    className="pc-button"
                    disabled={saving}
                    onClick={() => {
                      const edge = snapshot?.edges.find(
                        (row) => row.id === selectedEdge,
                      );
                      if (edge)
                        void execute({
                          type: "disconnect",
                          id: edge.id,
                          expectedRevision: edge.revision,
                        })
                          .then(() => setSelectedEdge(""))
                          .catch(() => {});
                    }}
                  >
                    <Trash2 size={16} />
                    Remove connection
                  </button>
                </div>
              )}
              {selectedItem && !selectedEdge && !presentation && (
                <div
                  className={`pc-selection-tools ${presentationStyles["pc-artifact-selection"]}`}
                  role="toolbar"
                  aria-label="Selected canvas item"
                >
                  {selectedItem.type === "frame" ? (
                    <>
                      <input
                        className={presentationStyles["pc-selection-title"]}
                        aria-label="Frame name"
                        key={selectedItem.id + selectedItem.contentRevision}
                        defaultValue={selectedItem.title}
                        maxLength={240}
                        disabled={!canEdit}
                        onBlur={(event) => {
                          if (event.target.value !== selectedItem.title)
                            void configure(selectedItem, {
                              title: event.target.value,
                            }).catch(() => {});
                        }}
                      />
                      <button
                        className={presentationStyles["pc-selection-secondary"]}
                        onClick={() =>
                          setCollapsed((current) =>
                            current.includes(selectedItem.id)
                              ? current.filter((id) => id !== selectedItem.id)
                              : [...current, selectedItem.id],
                          )
                        }
                      >
                        {collapsed.includes(selectedItem.id)
                          ? "Expand area"
                          : "Collapse area"}
                      </button>
                    </>
                  ) : (
                    <>
                      <button
                        className={presentationStyles["pc-selection-open"]}
                        onClick={() => onOpen(selectedItem)}
                      >
                        <ArrowUpRight size={16} /> Open review
                      </button>
                      {canEdit && selectedItem.artifactId ? (
                        <input
                          className={presentationStyles["pc-selection-title"]}
                          aria-label="Artifact name"
                          key={
                            selectedItem.artifactId +
                            artifacts.get(selectedItem.artifactId)?.revision
                          }
                          defaultValue={
                            artifacts.get(selectedItem.artifactId)?.title
                          }
                          maxLength={240}
                          onBlur={(event) => {
                            const artifact = artifacts.get(
                              selectedItem.artifactId!,
                            );
                            const title = event.target.value.trim();
                            if (artifact && title && title !== artifact.title)
                              void execute({
                                type: "rename",
                                artifactId: artifact.id,
                                versionId: artifact.latestVersionId,
                                expectedRevision: artifact.revision,
                                title,
                              }).catch(() => {});
                          }}
                        />
                      ) : (
                        <span
                          className={presentationStyles["pc-selection-title"]}
                          title={selectedItem.title}
                        >
                          {selectedItem.title || "Untitled artifact"}
                        </span>
                      )}
                      {canEdit && selectedItem.version?.wikiPageId && (
                        <button
                          className={presentationStyles["pc-selection-version"]}
                          aria-label={
                            selectedItem.versionPolicy === "draft"
                              ? "Following live draft. Pin this checkpoint"
                              : "Pinned checkpoint. Follow live draft"
                          }
                          title={
                            selectedItem.versionPolicy === "draft"
                              ? "Switch to pinned checkpoint"
                              : "Follow live draft"
                          }
                          disabled={saving}
                          onClick={() =>
                            void configure(selectedItem, {
                              versionPolicy:
                                selectedItem.versionPolicy === "draft"
                                  ? "pinned"
                                  : "draft",
                              versionId: selectedItem.version?.id,
                            }).catch(() => {})
                          }
                        >
                          <span
                            className={
                              selectedItem.versionPolicy === "draft"
                                ? presentationStyles["pc-live-dot"]
                                : presentationStyles["pc-pin-dot"]
                            }
                          />
                          {selectedItem.versionPolicy === "draft"
                            ? "Live draft"
                            : "Pinned"}
                        </button>
                      )}
                    </>
                  )}
                  {canEdit && (
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <button
                          className={presentationStyles["pc-selection-more"]}
                          aria-label="More item actions"
                        >
                          <MoreHorizontal size={18} />
                        </button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end" side="top">
                        {selectedItem.type === "artifact" &&
                          frames.length > 0 && (
                            <DropdownMenuSub>
                              <DropdownMenuSubTrigger>
                                Move to frame
                              </DropdownMenuSubTrigger>
                              <DropdownMenuSubContent>
                                <DropdownMenuRadioGroup
                                  value={selectedItem.parentFrameId ?? ""}
                                  onValueChange={(value) =>
                                    void configure(selectedItem, {
                                      parentFrameId: value || null,
                                    }).catch(() => {})
                                  }
                                >
                                  <DropdownMenuRadioItem value="">
                                    On canvas
                                  </DropdownMenuRadioItem>
                                  {frames.map((frame) => (
                                    <DropdownMenuRadioItem
                                      key={frame.id}
                                      value={frame.id}
                                    >
                                      {frame.title || "Untitled frame"}
                                    </DropdownMenuRadioItem>
                                  ))}
                                </DropdownMenuRadioGroup>
                              </DropdownMenuSubContent>
                            </DropdownMenuSub>
                          )}
                        <DropdownMenuItem
                          onSelect={() =>
                            void execute({
                              type: "remove",
                              id: selectedItem.id,
                              expectedRevision: selectedItem.contentRevision,
                            }).catch(() => {})
                          }
                        >
                          <Trash2 size={16} /> Remove from canvas
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  )}
                </div>
              )}
              {presentation ? (
                <div className="pc-presentation-bar">
                  <button
                    className="pc-icon-button"
                    aria-label="Previous frame"
                    disabled={presentation.index === 0}
                    onClick={() => showFrame(presentation.index - 1)}
                  >
                    <ChevronLeft size={18} />
                  </button>
                  <span>
                    {presentation.frames[presentation.index].title}{" "}
                    <small>
                      {presentation.index + 1} / {presentation.frames.length}
                    </small>
                  </span>
                  <button
                    className="pc-icon-button"
                    aria-label="Next frame"
                    disabled={
                      presentation.index === presentation.frames.length - 1
                    }
                    onClick={() => showFrame(presentation.index + 1)}
                  >
                    <ChevronRight size={18} />
                  </button>
                  <span className="inline-block w-[1px] h-[20px] m-[0_5px] flex-none [background:var(--pc-line)]" />
                  <button
                    className="pc-icon-button"
                    aria-label="End presentation"
                    onClick={endPresentation}
                  >
                    <X size={18} />
                  </button>
                </div>
              ) : (
                <div className="pc-viewport-tools">
                  <button
                    className="pc-icon-button"
                    title="Undo last layout change"
                    aria-label="Undo last layout change"
                    disabled={!undo.length || saving || !canEdit}
                    onClick={() => {
                      const last = undo.at(-1);
                      if (last)
                        void execute(last)
                          .then(() => setUndo((stack) => stack.slice(0, -1)))
                          .catch(() => {});
                    }}
                  >
                    <Undo2 size={17} />
                  </button>
                  <span className="inline-block w-[1px] h-[20px] m-[0_5px] flex-none [background:var(--pc-line)]" />
                  <ZoomControls />
                </div>
              )}
            </div>
          )}
        </div>
      )}
      <Suspense fallback={null}>
        {compare && snapshot && (
          <ArtifactComparison
            scope={scope}
            artifacts={snapshot.artifacts}
            onOpen={openArtifact}
            onClose={() => setCompare(false)}
          />
        )}
        {add && (
          <AddArtifact
            scope={scope}
            canEditDocuments={snapshot?.canEditDocuments ?? false}
            position={center()}
            initial={add.initial}
            taskId={add.taskId}
            execute={execute}
            onClose={() => setAdd(null)}
          />
        )}
        {focusedItem && focusedVersion && (
          <ArtifactFocusPreview
            scope={scope}
            version={focusedVersion}
            title={focusedItem.title || focusedArtifact?.title || "Artifact"}
            origin={focusedPreview?.origin}
            canvas={surface.current}
            canvasZoom={flow.getZoom()}
            itemId={focusedItem.id}
            onClose={() => setFocusedPreview(undefined)}
          />
        )}
        {review && activeArtifact && (
          <Suspense
            fallback={
              <ArtifactReviewLoading
                title={activeArtifact.title}
                onClose={closeReview}
              />
            }
          >
            <ArtifactReview
              key={`${review.artifactId}:${review.versionId ?? ""}:${review.threadId ?? ""}`}
              scope={scope}
              artifact={activeArtifact}
              item={snapshot?.items.find((row) => row.id === review.itemId)}
              versionId={review.versionId}
              initialOutputItemId={
                searchParams.get("outputItemId") ?? undefined
              }
              threadId={review.threadId}
              blockId={review.blockId}
              reviewOnly={review.reviewOnly}
              canEdit={canEdit}
              canEditDocuments={snapshot?.canEditDocuments ?? false}
              execute={execute}
              onClose={closeReview}
            />
          </Suspense>
        )}
      </Suspense>
    </div>
  );
}
function MessageSquarePlaceholder() {
  return (
    <>
      <Check size={24} />
      <span>
        A little feedback.
        <br />A better next version.
      </span>
    </>
  );
}
function ArtifactLibrary({
  artifacts,
  canEdit,
  onOpen,
  onPlace,
}: {
  artifacts: CanvasArtifact[];
  canEdit: boolean;
  onOpen: (artifact: CanvasArtifact) => void;
  onPlace: (artifact: CanvasArtifact) => void;
}) {
  const [scrollTop, setScrollTop] = useState(0);
  const start = Math.max(0, Math.floor(scrollTop / 84) - 4);
  return (
    <div
      className="overflow-y-auto min-h-0 flex-1"
      onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
    >
      <div
        style={
          {
            height: artifacts.length * 84,
            position: "relative",
          } as CSSProperties
        }
      >
        {artifacts.slice(start, start + 24).map((artifact, index) => {
          const { Icon, label, tone } = artifactFormat(artifact.latestVersion);
          return (
            <div
              className={`pc-library-row pc-tone-${tone}`}
              key={artifact.id}
              style={{
                position: "absolute",
                top: (start + index) * 84,
                insetInline: 0,
                height: 84,
              }}
            >
              <button
                className="pc-library-open"
                onClick={() => onOpen(artifact)}
              >
                <span className="pc-format-icon">
                  <Icon size={20} />
                </span>
                <span>
                  <strong>{artifact.title}</strong>
                  <small>
                    {label} · v{artifact.latestVersion.number}
                    {artifact.openComments
                      ? ` · ${artifact.openComments} comments`
                      : ""}
                  </small>
                </span>
                <ArrowUpRight size={16} />
              </button>
              {canEdit && (
                <button
                  className="pc-icon-button"
                  title="Place a copy on canvas"
                  aria-label={`Place ${artifact.title} on canvas`}
                  onClick={() => onPlace(artifact)}
                >
                  <Plus size={16} />
                </button>
              )}
            </div>
          );
        })}
      </div>
      {artifacts.length === 0 && (
        <p className="p-[24px_18px] text-[length:12px] text-[var(--pc-muted)]">
          No artifacts here yet.
        </p>
      )}
    </div>
  );
}

function ZoomControls() {
  const flow = useReactFlow();
  const viewport = useViewport();
  return (
    <>
      <button
        className="pc-icon-button"
        aria-label="Zoom out"
        onClick={() => void flow.zoomOut({ duration: 180 })}
      >
        <Minus size={17} />
      </button>
      <button
        className="min-w-[48px] text-[length:11px] text-[var(--pc-muted)] [font-variant-numeric:tabular-nums]"
        title="Reset zoom"
        onClick={() => void flow.zoomTo(1, { duration: 180 })}
      >
        {Math.round(viewport.zoom * 100)}%
      </button>
      <button
        className="pc-icon-button"
        aria-label="Zoom in"
        onClick={() => void flow.zoomIn({ duration: 180 })}
      >
        <Plus size={17} />
      </button>
      <span className="inline-block w-[1px] h-[20px] m-[0_5px] flex-none [background:var(--pc-line)]" />
      <button
        className="pc-icon-button"
        aria-label="Fit all artifacts"
        title="Fit all artifacts"
        onClick={() =>
          void flow.fitView({ padding: 0.2, maxZoom: 1, duration: 350 })
        }
      >
        <Maximize2 size={17} />
      </button>
    </>
  );
}
