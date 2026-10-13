import type { components } from "@/shared/api/generated/api.generated";
import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  LoaderCircle,
  Studio,
  Maximize,
  Minimize2,
  RefreshCw,
  X,
} from "@/shared/ui/icons";
import type { ReactFlowInstance } from "@xyflow/react";
import { isApiClientError } from "@/lib/api-client";
import { toast, Toaster } from "@/shared/ui/sonner";
import { operateConversationStudio } from "@/shared/api/studio-editor.service";
import {
  createStudioDirectClient,
  type StudioDirectClient,
  StudioReauthenticationError,
  StudioRequestError,
} from "@/shared/api/studio-direct-client";
import { useStudioCollaboration } from "@/shared/api/studio-collaboration";
import {
  StudioGraph,
  type StudioGraphCapabilities,
  type StudioGraphMutation,
} from "@/shared/studio-graph/studio-graph";
import type {
  StudioBootstrap,
  StudioNodeResponse,
  StudioNodeKind,
} from "@/shared/studio-graph/model";
import {
  normalizeStudioNode,
  canGenerateStudioNode,
  isStudioNodeRunning,
  studioNodeKindLabel,
} from "@/shared/studio-graph/model";
import {
  findStudioModel,
  type StudioModel,
  type StudioModelCatalog,
} from "@/shared/api/studio-model-catalog";
import { StudioNodeSettings } from "./studio-node-settings";
import { importConversationImageArtifacts } from "../model/import-conversation-artifacts";
import { StudioConnectionPanel } from "./studio-connection-panel";
import { StudioStatusArt } from "./studio-status-art";
import { StudioNodePicker } from "./studio-node-picker";
import { StudioToolbar } from "./studio-toolbar";
import { studioModelMetadataPatch, videoRoute } from "../model-settings";
import "./studio-mode-panel.css";

type StudioModeProps = {
  companyId: string;
  conversationId: string;
  runtimeId: string;
  agentId: string;
  onClose: () => void;
  fullPage?: boolean;
};

type StudioContext = components["schemas"]["StudioEditorContext"];

type StudioState = {
  client: StudioDirectClient;
  context: StudioContext;
  projectId: string;
  bootstrap: StudioBootstrap;
};

const studioStateCache = new Map<string, StudioState>();
const studioLoadCache = new Map<string, Promise<StudioState>>();
const studioArtifactImportCache = new Map<string, Promise<StudioBootstrap>>();
const MAX_STUDIO_CACHE_ENTRIES = 12;

function cacheStudioState(key: string, state: StudioState) {
  studioStateCache.delete(key);
  studioStateCache.set(key, state);
  while (studioStateCache.size > MAX_STUDIO_CACHE_ENTRIES) {
    const oldest = studioStateCache.keys().next().value;
    if (oldest === undefined) break;
    studioStateCache.delete(oldest);
  }
}

function studioCacheKey(
  companyId: string,
  conversationId: string,
  runtimeId: string,
) {
  return `${companyId}:${conversationId}:${runtimeId}`;
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

export function StudioModePanel(props: StudioModeProps) {
  const panelRef = useRef<HTMLElement>(null);
  const toasterId = useId();
  const notifyError = useCallback(
    (message: string) => {
      toast.error(message, {
        id: `${toasterId}:${message}`,
        toasterId,
        position: "bottom-right",
      });
    },
    [toasterId],
  );
  const [expanded, setExpanded] = useState(Boolean(props.fullPage));
  // The top layer escapes the chat's clipping without moving or remounting the editor.
  useLayoutEffect(() => {
    const panel = panelRef.current;
    if (!expanded || props.fullPage || !panel) return;
    const workspace = panel.closest(".workspace-shell-content");
    const updateBounds = () => {
      const bounds = workspace?.getBoundingClientRect();
      panel.style.setProperty(
        "--studio-expanded-left",
        `${bounds?.left ?? 0}px`,
      );
      panel.style.setProperty("--studio-expanded-top", `${bounds?.top ?? 0}px`);
      panel.style.setProperty(
        "--studio-expanded-width",
        `${bounds?.width ?? window.innerWidth}px`,
      );
      panel.style.setProperty(
        "--studio-expanded-height",
        `${bounds?.height ?? window.innerHeight}px`,
      );
    };
    updateBounds();
    panel.showPopover();
    const observer = new ResizeObserver(updateBounds);
    if (workspace) observer.observe(workspace);
    window.addEventListener("resize", updateBounds);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", updateBounds);
    };
  }, [expanded, props.fullPage]);
  useEffect(() => {
    if (props.fullPage) panelRef.current?.focus();
  }, [props.fullPage]);
  return (
    <section
      ref={panelRef}
      tabIndex={-1}
      popover={expanded && !props.fullPage ? "manual" : undefined}
      onKeyDownCapture={(event) => {
        if (
          event.target instanceof Element &&
          event.target.closest(
            '.studio-mode-panel__inspector, .studio-mode-panel__tool-picker, .studio-graph, .studio-node-model-menu, .studio-player:modal, .canvas-tool-guide__trigger[data-state="open"], .canvas-tool-guide',
          )
        )
          return;
        if (event.key === "Escape" && expanded && !props.fullPage) {
          event.preventDefault();
          event.stopPropagation();
          setExpanded(false);
        }
      }}
      aria-label="Studio workspace"
      className={`studio-mode-panel studio-mode-shell${expanded ? " studio-mode-panel--full-page" : ""}${expanded && !props.fullPage ? " studio-mode-shell--expanded" : ""}${props.fullPage && !expanded ? " left-auto w-[min(720px,_100%)]" : ""}`}
    >
      <Toaster id={toasterId} position="bottom-right" closeButton richColors />
      {props.runtimeId ? (
        <StudioEditor
          key={`${props.companyId}:${props.conversationId}:${props.runtimeId}:${props.agentId}`}
          {...props}
          fullPage={expanded}
          canToggleExpanded={!props.fullPage}
          onToggleExpanded={() => setExpanded((current) => !current)}
          onError={notifyError}
        />
      ) : (
        <StudioConnectionPanel
          companyId={props.companyId}
          runtimeId=""
          agentId={props.agentId}
          onClose={props.onClose}
          onRetry={props.onClose}
          onError={notifyError}
        />
      )}
    </section>
  );
}

async function loadStudio(
  companyId: string,
  conversationId: string,
  runtimeId: string,
  agentId: string,
): Promise<StudioState> {
  const key = `${studioCacheKey(companyId, conversationId, runtimeId)}:${agentId}`;
  const cached = studioStateCache.get(key);
  if (cached) return cached;
  const pending = studioLoadCache.get(key);
  if (pending) return pending;

  const request = loadStudioUncached(
    companyId,
    conversationId,
    runtimeId,
    agentId,
  )
    .then((state) => {
      cacheStudioState(key, state);
      return state;
    })
    .finally(() => {
      studioLoadCache.delete(key);
    });
  studioLoadCache.set(key, request);
  return request;
}

function importStudioArtifactsInBackground(
  key: string,
  companyId: string,
  conversationId: string,
  state: StudioState,
) {
  const importKey = `${key}:${state.projectId}`;
  const pending = studioArtifactImportCache.get(importKey);
  if (pending) return pending;

  const request = importConversationImageArtifacts({
    companyId,
    conversationId,
    projectId: state.projectId,
    client: state.client,
    bootstrap: state.bootstrap,
  }).finally(() => {
    studioArtifactImportCache.delete(importKey);
  });
  studioArtifactImportCache.set(importKey, request);
  return request;
}

async function resolveStudioContext(
  companyId: string,
  conversationId: string,
  runtimeId: string,
  agentId: string,
) {
  const response = await operateConversationStudio(companyId, conversationId, {
    schemaVersion: 2,
    requestId: crypto.randomUUID(),
    runtimeId,
    operation: "resolveContext",
    payload: {},
  });
  if (
    response.operation !== "resolveContext" ||
    response.data.agentId !== agentId ||
    response.data.runtimeId !== runtimeId
  )
    throw new Error("Invalid Studio context response.");
  return response.data;
}

async function loadStudioUncached(
  companyId: string,
  conversationId: string,
  runtimeId: string,
  agentId: string,
): Promise<StudioState> {
  const resolve = () =>
    resolveStudioContext(companyId, conversationId, runtimeId, agentId);

  let context = await resolve();
  if (!context.binding?.projectId) {
    if (!context.connectionGeneration)
      throw new Error("Studio is not connected to a project.");
    const ensured = await operateConversationStudio(companyId, conversationId, {
      schemaVersion: 2,
      requestId: crypto.randomUUID(),
      runtimeId,
      operation: "ensureBinding",
      payload: {
        title: "Studio canvas",
        description: "Canvas created from Zenith Team",
        expectedBindingId: context.binding?.bindingId ?? null,
        expectedBindingRevision: context.binding?.bindingRevision ?? 0,
        connectionGeneration: context.connectionGeneration,
      },
    });
    if (ensured.operation !== "ensureBinding")
      throw new Error("Studio project could not be created.");
    context = await resolve();
  }

  if (
    !context.binding?.projectId ||
    !context.studioAccessToken?.accessToken ||
    !context.studioAccessToken.origin
  )
    throw new Error("Studio did not return an editor credential.");
  const projectId = context.binding.projectId;
  const client = createStudioDirectClient(
    context.studioAccessToken,
    async () => {
      const fresh = await resolve();
      if (fresh.connectionState === "reauthRequired")
        throw new StudioReauthenticationError();
      if (fresh.binding?.projectId !== projectId)
        throw new Error("Studio project changed. Reopen the canvas.");
      if (!fresh.studioAccessToken || fresh.connectionState !== "ready")
        throw new Error("Studio connection is unavailable. Try again.");
      return fresh.studioAccessToken;
    },
  );
  const bootstrap = await client.getBootstrap(projectId);
  return {
    client,
    context,
    projectId,
    bootstrap,
  };
}

function errorMessage(error: unknown) {
  if (typeof error === "string" && error) return error;
  if (error instanceof DOMException && error.name === "AbortError") {
    return "Studio took too long to respond. Reconnect and try again.";
  }
  return error instanceof Error && error.message
    ? error.message
    : "Studio is unavailable. Reconnect and try again.";
}

function isStudioConnectionError(error: unknown) {
  if (error instanceof StudioRequestError) return error.status === 401;
  if (isApiClientError(error)) return error.status === 401;
  const message = errorMessage(error).toLowerCase();
  return /(invalid.*token|expired.*token|credential|sign-in|unauthor|failed to fetch|network|timed out)/.test(
    message,
  );
}

function nodeMetadata(node: { metadataJson: string }) {
  try {
    return record(JSON.parse(node.metadataJson));
  } catch {
    return {};
  }
}

function StudioLoadingState({ onClose }: { onClose: () => void }) {
  return (
    <section
      className="studio-mode-panel studio-mode-panel--loading"
      aria-busy="true"
      aria-label="Studio mode"
    >
      <header>
        <h2 className="flex items-center gap-[7px] m-0 text-workspace-text [font-family:var(--font-heading)] text-[length:15px] font-[600] leading-[20px]">
          <Studio size={16} aria-hidden />
          Studio Mode
        </h2>
        <div className="studio-mode-panel__actions">
          <button type="button" onClick={onClose} aria-label="Close Studio">
            <X size={16} aria-hidden />
          </button>
        </div>
      </header>
      <div className="studio-mode-panel__loading-body" role="status">
        <StudioStatusArt />
        <div className="studio-mode-panel__loading-status relative flex items-center gap-[6px] text-workspace-muted text-[length:12px] leading-[18px]">
          <LoaderCircle size={13} aria-hidden />
          <span>Loading…</span>
        </div>
      </div>
    </section>
  );
}

function StudioEditor({
  companyId,
  conversationId,
  runtimeId,
  agentId,
  onClose,
  onToggleExpanded,
  onError,
  fullPage,
  canToggleExpanded,
}: StudioModeProps & {
  onToggleExpanded: () => void;
  onError: (message: string) => void;
  canToggleExpanded: boolean;
}) {
  const cacheKey = `${studioCacheKey(companyId, conversationId, runtimeId)}:${agentId}`;
  const [state, setState] = useState<StudioState | null>(
    () => studioStateCache.get(cacheKey) ?? null,
  );
  const [loading, setLoading] = useState(() => !studioStateCache.has(cacheKey));
  const [error, setError] = useState<string | null>(null);
  const [connectionError, setConnectionError] = useState<string | null>(null);
  const [reconnectRequired, setReconnectRequired] = useState(false);
  const [showReconnect, setShowReconnect] = useState(false);
  const [toolsOpen, setToolsOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [arranging, setArranging] = useState(false);
  const [closing, setClosing] = useState(false);
  const [interactionMode, setInteractionMode] = useState<"select" | "pan">(
    "select",
  );
  const flowRef = useRef<ReactFlowInstance | null>(null);
  const handleGraphReady = useCallback((instance: ReactFlowInstance) => {
    flowRef.current = instance;
  }, []);
  const canvasRef = useRef<HTMLDivElement>(null);
  const createInFlight = useRef(false);
  const [inspectedId, setInspectedId] = useState<string | null>(null);
  const [catalog, setCatalog] = useState<StudioModelCatalog | null>(null);
  const [catalogError, setCatalogError] = useState<string | null>(null);
  const [catalogRevision, setCatalogRevision] = useState(0);
  const [drafts, setDraftsState] = useState<
    Record<
      string,
      {
        nodeId: string;
        version: number;
        baseLabel: string;
        baseMetadata: Record<string, unknown>;
        label?: string;
        metadata?: Record<string, unknown>;
      }
    >
  >({});
  const draftsRef = useRef(drafts);
  const setDrafts = useCallback(
    (update: (current: typeof drafts) => typeof drafts) => {
      const next = update(draftsRef.current);
      draftsRef.current = next;
      setDraftsState(next);
    },
    [],
  );
  const savesInFlight = useRef(new Map<string, Promise<StudioNodeResponse>>());
  const [savingIds, setSavingIds] = useState<Set<string>>(new Set());
  const [saveErrors, setSaveErrors] = useState<
    Record<string, string | undefined>
  >({});
  const restoresInFlight = useRef(new Set<string>());
  const [restoringIds, setRestoringIds] = useState<Set<string>>(new Set());
  const [modelSavingIds, setModelSavingIds] = useState<Set<string>>(new Set());
  const generationInFlight = useRef(new Set<string>());
  const [generatingIds, setGeneratingIds] = useState<Set<string>>(new Set());
  const [draftConflicts, setDraftConflicts] = useState<Record<string, boolean>>(
    {},
  );
  const draft = inspectedId ? drafts[inspectedId] : undefined;
  const handleStudioError = useCallback(
    (reason: unknown) => {
      const message = errorMessage(reason);
      setError(message);
      onError(message);
      if (isStudioConnectionError(reason)) setConnectionError(message);
      if (reason instanceof StudioReauthenticationError) {
        setReconnectRequired(true);
        setShowReconnect(true);
        setToolsOpen(false);
      }
    },
    [onError],
  );
  useEffect(() => {
    const client = state?.client;
    if (!client) return;
    let active = true;
    const unsubscribe = client.subscribeAccess((access) => {
      setState((current) => {
        if (!current || current.client !== client) return current;
        if (current.context.studioAccessToken === access) return current;
        const next = {
          ...current,
          context: { ...current.context, studioAccessToken: access },
        };
        cacheStudioState(cacheKey, next);
        return next;
      });
      setConnectionError(null);
      setReconnectRequired(false);
      setShowReconnect(false);
    });
    const renew = () => {
      if (document.visibilityState === "hidden") return;
      void client.ensureAccess().catch((error: unknown) => {
        if (active) handleStudioError(error);
      });
    };
    renew();
    const timer = window.setInterval(renew, 30_000);
    window.addEventListener("focus", renew);
    window.addEventListener("online", renew);
    document.addEventListener("visibilitychange", renew);
    return () => {
      active = false;
      unsubscribe();
      window.clearInterval(timer);
      window.removeEventListener("focus", renew);
      window.removeEventListener("online", renew);
      document.removeEventListener("visibilitychange", renew);
    };
  }, [cacheKey, handleStudioError, state?.client]);
  useEffect(() => {
    if (!state?.client) return;
    let stopped = false;
    setCatalogError(null);
    void state.client
      .getModelCatalog()
      .then((next) => {
        if (!stopped) setCatalog(next);
      })
      .catch((error: unknown) => {
        if (!stopped) {
          setCatalogError(errorMessage(error));
          handleStudioError(error);
        }
      });
    return () => {
      stopped = true;
    };
  }, [catalogRevision, handleStudioError, state?.client]);
  const writeQueue = useRef(Promise.resolve());
  const pendingPositions = useRef(new Map<string, { x: number; y: number }>());
  const syncRevision = useRef(0);
  const syncInFlight = useRef(0);
  const [artifactRefresh, setArtifactRefresh] = useState(0);
  const appliedArtifactRefresh = useRef(0);

  const load = useCallback(
    (force = false) => {
      let stopped = false;
      const cached = studioStateCache.get(cacheKey);
      if (!cached) setLoading(true);
      setError(null);
      setConnectionError(null);
      const request = force
        ? loadStudioUncached(
            companyId,
            conversationId,
            runtimeId,
            agentId,
          ).then((next) => {
            cacheStudioState(cacheKey, next);
            return next;
          })
        : loadStudio(companyId, conversationId, runtimeId, agentId);
      void request
        .then((next) => {
          if (stopped) return;
          setState(next);
          cacheStudioState(cacheKey, next);
          setReconnectRequired(false);
          setShowReconnect(false);

          // Artifact reconciliation can involve several Team and Studio
          // requests. Let the graph paint from the authoritative bootstrap
          // first, then merge imported images when the background work ends.
          void importStudioArtifactsInBackground(
            cacheKey,
            companyId,
            conversationId,
            next,
          )
            .then((bootstrap) => {
              if (stopped || bootstrap === next.bootstrap) return;
              setArtifactRefresh((revision) => revision + 1);
            })
            .catch((reason: unknown) => {
              if (!stopped) onError(errorMessage(reason));
            });
        })
        .catch((reason: unknown) => {
          if (!stopped) {
            handleStudioError(reason);
            // Keep a previously loaded canvas usable while a background refresh fails.
            if (!cached) setState(null);
          }
        })
        .finally(() => {
          if (!stopped) setLoading(false);
        });
      return () => {
        stopped = true;
      };
    },
    [
      cacheKey,
      agentId,
      companyId,
      conversationId,
      handleStudioError,
      onError,
      runtimeId,
    ],
  );

  useEffect(
    () => load(Boolean(studioStateCache.get(cacheKey))),
    [cacheKey, load],
  );

  const sync = useCallback(async () => {
    if (!state) return undefined;
    const revision = ++syncRevision.current;
    syncInFlight.current += 1;
    let next: StudioBootstrap;
    try {
      next = await state.client.getBootstrap(state.projectId);
    } finally {
      syncInFlight.current -= 1;
    }
    setState((current) => {
      if (!current || revision !== syncRevision.current) return current;
      const previousNodes = new Map(
        current.bootstrap.nodes.map((node) => [node.id, node]),
      );
      const nextState = {
        ...current,
        bootstrap: {
          ...next,
          nodes: next.nodes.map((node) => {
            const previous = previousNodes.get(node.id);
            if (
              previous &&
              (previous.version > node.version ||
                (previous.version === node.version &&
                  Date.parse(previous.updatedAt) > Date.parse(node.updatedAt)))
            )
              node = previous;
            const pending = pendingPositions.current.get(node.id);
            return pending
              ? {
                  ...node,
                  metadataJson: JSON.stringify({
                    ...nodeMetadata(node),
                    position: pending,
                  }),
                }
              : node;
          }),
        },
      };
      cacheStudioState(cacheKey, nextState);
      return nextState;
    });
    return next;
  }, [cacheKey, state?.client, state?.projectId]);

  useEffect(() => {
    if (!state || artifactRefresh <= appliedArtifactRefresh.current) return;
    appliedArtifactRefresh.current = artifactRefresh;
    void sync().catch(handleStudioError);
  }, [
    artifactRefresh,
    handleStudioError,
    state?.client,
    state?.projectId,
    sync,
  ]);

  const applyNodeAcknowledgement = useCallback(
    (updated: StudioNodeResponse) => {
      syncRevision.current += 1;
      setState((current) => {
        if (!current || current.projectId !== updated.projectId) return current;
        const previous = current.bootstrap.nodes.find(
          (node) => node.id === updated.id,
        );
        if (
          previous &&
          (previous.version > updated.version ||
            (previous.version === updated.version &&
              Date.parse(previous.updatedAt) > Date.parse(updated.updatedAt)))
        )
          return current;
        const pending = pendingPositions.current.get(updated.id);
        const acknowledged = {
          ...previous,
          ...updated,
          ...(pending
            ? {
                metadataJson: JSON.stringify({
                  ...nodeMetadata(updated),
                  position: pending,
                }),
              }
            : {}),
        };
        const next = {
          ...current,
          bootstrap: {
            ...current.bootstrap,
            nodes: previous
              ? current.bootstrap.nodes.map((node) =>
                  node.id === updated.id ? acknowledged : node,
                )
              : [...current.bootstrap.nodes, acknowledged],
          },
        };
        cacheStudioState(cacheKey, next);
        return next;
      });
    },
    [cacheKey],
  );

  const refreshMedia = useCallback(
    async (nodeId: string) => {
      if (!state?.client) return undefined;
      await state.client.ensureAccess();
      const bootstrap = await state.client.getBootstrap(state.projectId);
      const fresh = bootstrap.nodes.find((node) => node.id === nodeId);
      if (!fresh) throw new Error("Studio media node is no longer available.");
      applyNodeAcknowledgement(fresh);
      return normalizeStudioNode(fresh).mediaUrl;
    },
    [applyNodeAcknowledgement, state?.client, state?.projectId],
  );

  const onGraphChanged = useCallback(() => {
    void sync().catch((error: unknown) => {
      if (isStudioConnectionError(error)) {
        handleStudioError(error);
      } else {
        onError(
          "A collaborator changed the canvas. Reload to see the latest state.",
        );
      }
    });
  }, [handleStudioError, onError, sync]);

  const renewCollaborationAccess = useCallback(async () => {
    if (!state?.client) throw new Error("Studio is unavailable.");
    try {
      return (await state.client.refreshAccess()).accessToken;
    } catch (error) {
      handleStudioError(error);
      throw error;
    }
  }, [handleStudioError, state?.client]);

  const collaboration = useStudioCollaboration({
    origin: state?.context.studioAccessToken?.origin ?? "",
    accessToken: state?.context.studioAccessToken?.accessToken ?? "",
    projectId: state?.projectId ?? "",
    onGraphChanged,
    onAccessExpired: renewCollaborationAccess,
  });

  const trackWrite = useCallback(<T,>(work: () => Promise<T>) => {
    // Studio versions are optimistic-concurrency tokens. Serialize writes in
    // this panel so two quick resize/label commits cannot reuse one token.
    const promise = writeQueue.current.catch(() => undefined).then(work);
    writeQueue.current = promise.then(
      () => undefined,
      () => undefined,
    );
    return promise;
  }, []);

  const bootstrap = state?.bootstrap ?? null;
  // Drafts render immediately; only server acknowledgements enter the saved cache.
  const visibleBootstrap = useMemo(
    () =>
      bootstrap && {
        ...bootstrap,
        nodes: bootstrap.nodes.map((node) => {
          const draft = drafts[node.id];
          return draft
            ? {
                ...node,
                label: draft.label ?? node.label,
                metadataJson: JSON.stringify({
                  ...nodeMetadata(node),
                  ...draft.metadata,
                }),
              }
            : node;
        }),
      },
    [bootstrap, drafts],
  );
  const inspectedRaw = bootstrap?.nodes?.find(
    (node) => node.id === inspectedId,
  );
  const inspected = inspectedRaw ? normalizeStudioNode(inspectedRaw) : null;
  const graphReady = Boolean(state && bootstrap && !reconnectRequired);
  const capabilities: StudioGraphCapabilities = {
    updateNodePosition: graphReady,
    resizeNode: graphReady,
    createNode: graphReady,
    renameNode: graphReady,
    deleteNode: graphReady,
    connectEdge: graphReady,
    disconnectEdge: graphReady,
    layout: graphReady,
  };

  const onPositionCommit = useCallback(
    (nodeId: string, position: { x: number; y: number }) => {
      if (!state) return Promise.reject(new Error("Studio is not ready."));
      pendingPositions.current.set(nodeId, position);
      const write = trackWrite(async () => {
        const updated = await state.client.updatePosition(nodeId, position);
        const needsRefresh = syncInFlight.current > 0;
        // A read started before this acknowledgement must not restore old geometry.
        syncRevision.current += 1;
        if (pendingPositions.current.get(nodeId) === position)
          pendingPositions.current.delete(nodeId);
        setState((current) => {
          if (!current || current.projectId !== state.projectId) return current;
          const pending = pendingPositions.current.get(nodeId);
          const next = {
            ...current,
            bootstrap: {
              ...current.bootstrap,
              nodes: current.bootstrap.nodes.map((node) =>
                node.id === nodeId
                  ? {
                      ...node,
                      version: Math.max(node.version, updated.version),
                      updatedAt:
                        Date.parse(updated.updatedAt) >=
                        Date.parse(node.updatedAt)
                          ? updated.updatedAt
                          : node.updatedAt,
                      metadataJson: JSON.stringify({
                        ...nodeMetadata(node),
                        position:
                          pending ??
                          (Date.parse(updated.updatedAt) >=
                          Date.parse(node.updatedAt)
                            ? position
                            : nodeMetadata(node).position),
                      }),
                    }
                  : node,
              ),
            },
          };
          cacheStudioState(cacheKey, next);
          return next;
        });
        if (needsRefresh) void sync().catch(handleStudioError);
      }).finally(() => {
        if (pendingPositions.current.get(nodeId) === position)
          pendingPositions.current.delete(nodeId);
      });
      return write;
    },
    [
      cacheKey,
      state?.client,
      state?.projectId,
      sync,
      trackWrite,
      handleStudioError,
    ],
  );

  const onMutation = useCallback(
    (mutation: StudioGraphMutation) =>
      trackWrite(async () => {
        if (!state) return;
        const payload = mutation.payload;
        let acknowledgedVersion: number | undefined;
        if (mutation.operation === "createNode") {
          const created = await state.client.createNode(state.projectId, {
            nodeType: String(payload.nodeType ?? "text"),
            label:
              typeof payload.label === "string" ? payload.label : undefined,
            metadata: record(payload.metadata),
          });
          applyNodeAcknowledgement(created);
          setInspectedId(created.id);
        } else if (mutation.operation === "updateNode") {
          const latest = await sync();
          const current = latest?.nodes?.find(
            (node) => node.id === String(payload.nodeId),
          );
          const updated = await state.client.updateNode({
            nodeId: String(payload.nodeId),
            version: current?.version ?? Number(payload.version),
            label:
              typeof payload.label === "string" ? payload.label : undefined,
            metadata: {
              ...(current ? nodeMetadata(current) : {}),
              ...record(payload.metadata),
            },
          });
          applyNodeAcknowledgement(updated);
          acknowledgedVersion = updated.version;
        } else if (mutation.operation === "deleteNode") {
          await state.client.deleteNode(String(payload.nodeId));
        } else if (mutation.operation === "createEdge") {
          await state.client.createEdge(state.projectId, {
            sourceId: String(payload.sourceId),
            targetId: String(payload.targetId),
            sourceHandle:
              typeof payload.sourceHandle === "string"
                ? payload.sourceHandle
                : undefined,
            targetHandle:
              typeof payload.targetHandle === "string"
                ? payload.targetHandle
                : undefined,
          });
        } else if (mutation.operation === "deleteEdge") {
          await state.client.deleteEdge(String(payload.edgeId));
        } else if (mutation.operation === "applyLayout") {
          const latest = await sync();
          await state.client.applyLayout(
            state.projectId,
            latest?.project.version ?? Number(payload.version),
            record(payload.positions) as Record<
              string,
              { x: number; y: number }
            >,
          );
        }
        await sync().catch(handleStudioError);
        return acknowledgedVersion;
      }),
    [state, sync, trackWrite, applyNodeAcknowledgement, handleStudioError],
  );

  const changeNodeModel = useCallback(
    (nodeId: string, model: StudioModel) => {
      const startingDraft = draftsRef.current[nodeId];
      setModelSavingIds((current) => new Set([...current, nodeId]));
      return trackWrite(async () => {
        if (!state) throw new Error("Studio is not ready.");
        const latest = await sync();
        const current = latest?.nodes.find((node) => node.id === nodeId);
        if (!latest || !current)
          throw new Error("This node was removed from the canvas.");
        if (
          normalizeStudioNode(current).readOnly ||
          ["executing", "submitted", "cancelling"].includes(current.status)
        )
          throw new Error("This node cannot change models while generating.");
        const metadata = nodeMetadata(current);
        const previous = findStudioModel(
          catalog?.models[current.nodeType] ?? [],
          metadata.model ?? metadata.model_name,
        );
        const patch = studioModelMetadataPatch(
          model,
          previous,
          metadata,
          videoRoute(metadata, nodeId, latest),
        );
        const updated = await state.client.updateNode({
          nodeId,
          version: current.version,
          metadata: { ...metadata, ...patch },
        });
        applyNodeAcknowledgement(updated);
        setDrafts((currentDrafts) => {
          const draft = currentDrafts[nodeId];
          if (!draft) return currentDrafts;
          const remaining = Object.fromEntries(
            Object.entries(draft.metadata ?? {}).filter(
              ([key, value]) =>
                !Object.hasOwn(patch, key) ||
                JSON.stringify(value) !==
                  JSON.stringify(startingDraft?.metadata?.[key]),
            ),
          );
          if (
            !Object.keys(remaining).length &&
            (draft.label === undefined || draft.label === draft.baseLabel)
          ) {
            const next = { ...currentDrafts };
            delete next[nodeId];
            return next;
          }
          return {
            ...currentDrafts,
            [nodeId]: {
              ...draft,
              version: updated.version,
              baseMetadata: { ...draft.baseMetadata, ...patch },
              metadata: remaining,
            },
          };
        });
        await sync().catch(handleStudioError);
      }).finally(() => {
        setModelSavingIds((current) => {
          const next = new Set(current);
          next.delete(nodeId);
          return next;
        });
      });
    },
    [
      state,
      sync,
      trackWrite,
      catalog,
      applyNodeAcknowledgement,
      handleStudioError,
      setDrafts,
    ],
  );

  const createNode = (
    nodeType: Exclude<StudioNodeKind, "unknown">,
    model?: StudioModel,
  ) => {
    if (createInFlight.current || reconnectRequired) return;
    createInFlight.current = true;
    setCreating(true);
    setToolsOpen(false);
    const bounds = canvasRef.current?.getBoundingClientRect();
    const center =
      bounds && flowRef.current
        ? flowRef.current.screenToFlowPosition({
            x: bounds.left + bounds.width / 2,
            y: bounds.top + bounds.height / 2,
          })
        : { x: 320, y: 240 };
    const width =
      nodeType === "node_group" ? 480 : nodeType === "video" ? 320 : 300;
    const height =
      nodeType === "node_group"
        ? 320
        : nodeType === "video"
          ? 180
          : nodeType === "image" || nodeType === "infographic"
            ? 225
            : 280;
    let position = { x: center.x - width / 2, y: center.y - height / 2 };
    // Place near the viewport center while leaving existing cards unobstructed.
    const existing = (bootstrap?.nodes ?? []).map(normalizeStudioNode);
    for (let attempt = 0; attempt < 24; attempt += 1) {
      const overlaps = existing.some(
        (node) =>
          position.x < node.position.x + (node.width ?? 300) + 24 &&
          position.x + width + 24 > node.position.x &&
          position.y < node.position.y + (node.height ?? 280) + 24 &&
          position.y + height + 24 > node.position.y,
      );
      if (!overlaps) break;
      position = {
        x: center.x - width / 2 + ((attempt % 4) + 1) * (width + 40),
        y: center.y - height / 2 + Math.floor(attempt / 4) * (height + 40),
      };
    }
    void onMutation({
      operation: "createNode",
      payload: {
        nodeType,
        label: studioNodeKindLabel(nodeType),
        metadata: {
          ...(model
            ? {
                model: model.id,
                model_name: model.id,
                provider: model.provider,
                ...(model.defaultVoice ? { voice: model.defaultVoice } : {}),
              }
            : (catalog?.defaults[nodeType] ?? {})),
          position,
          width,
          height,
        },
      },
    })
      .then(() => {
        // Wait for the inspector to take its space before centering the new card.
        requestAnimationFrame(() =>
          requestAnimationFrame(
            () =>
              void flowRef.current?.setCenter(
                position.x + width / 2,
                position.y + height / 2,
                {
                  zoom: Math.max(
                    0.85,
                    Math.min(flowRef.current.getZoom(), 1.2),
                  ),
                  duration: 240,
                },
              ),
          ),
        );
      })
      .catch((reason: unknown) => handleStudioError(reason))
      .finally(() => {
        createInFlight.current = false;
        setCreating(false);
      });
  };

  const applyLayout = () => {
    if (!bootstrap || arranging || reconnectRequired) return;
    setArranging(true);
    let x = 80;
    let y = 80;
    let rowHeight = 0;
    const positions = Object.fromEntries(
      (bootstrap.nodes ?? []).map((raw, index) => {
        const node = normalizeStudioNode(raw);
        if (index > 0 && index % 3 === 0) {
          x = 80;
          y += rowHeight + 80;
          rowHeight = 0;
        }
        const position = { x, y };
        x += (node.width ?? 300) + 80;
        rowHeight = Math.max(rowHeight, node.height ?? 280);
        return [node.id, position];
      }),
    );
    void onMutation({
      operation: "applyLayout",
      payload: { version: bootstrap.project.version, positions },
    })
      .then(() => {
        requestAnimationFrame(() =>
          requestAnimationFrame(
            () =>
              void flowRef.current?.fitView({
                padding: {
                  top: "80px",
                  bottom: "60px",
                  left: "48px",
                  right: "48px",
                },
                maxZoom: 1.35,
                duration: 240,
              }),
          ),
        );
      })
      .catch((reason: unknown) => handleStudioError(reason))
      .finally(() => setArranging(false));
  };

  const inspectNode = useCallback((node: { id: string }) => {
    setInspectedId(node.id);
  }, []);

  const editDraft = (patch: {
    label?: string;
    metadata?: Record<string, unknown>;
  }) => {
    if (!inspected) return;
    setSaveErrors((current) => ({ ...current, [inspected.id]: undefined }));
    setDrafts((current) => ({
      ...current,
      [inspected.id]: {
        ...(current[inspected.id] ?? {
          nodeId: inspected.id,
          version: inspected.raw.version,
          baseLabel: inspected.label,
          baseMetadata: nodeMetadata(inspected.raw),
        }),
        ...patch,
        metadata: { ...current[inspected.id]?.metadata, ...patch.metadata },
      },
    }));
  };
  const discardDraft = (nodeId: string) => {
    setDraftConflicts((current) => ({ ...current, [nodeId]: false }));
    setSaveErrors((current) => ({ ...current, [nodeId]: undefined }));
    setDrafts((current) => {
      const next = { ...current };
      delete next[nodeId];
      return next;
    });
  };

  const saveNodeDraft = useCallback(
    async (nodeId: string, overwrite = false) => {
      if (!state) throw new Error("Studio is not ready.");
      const fresh = await state.client.getBootstrap(state.projectId);
      const selected = fresh.nodes?.find((node) => node.id === nodeId);
      if (!selected) throw new Error("This node was removed from the canvas.");
      if (isStudioNodeRunning(normalizeStudioNode(selected)))
        throw new Error(
          "This node is already generating. Wait for it to finish.",
        );
      const draft = draftsRef.current[nodeId];
      if (draft?.nodeId !== nodeId) return selected;
      const latestMetadata = nodeMetadata(selected);
      const changedMetadata = Object.fromEntries(
        Object.entries(draft.metadata ?? {}).filter(
          ([key, value]) =>
            JSON.stringify(value) !== JSON.stringify(draft.baseMetadata[key]),
        ),
      );
      const labelChanged =
        draft.label !== undefined && draft.label !== draft.baseLabel;
      const conflicts = Object.keys(changedMetadata).filter(
        (key) =>
          JSON.stringify(latestMetadata[key]) !==
            JSON.stringify(draft.baseMetadata[key]) &&
          JSON.stringify(latestMetadata[key]) !==
            JSON.stringify(changedMetadata[key]),
      );
      const latestLabel = normalizeStudioNode(selected).label;
      if (
        labelChanged &&
        latestLabel !== draft.baseLabel &&
        latestLabel !== draft.label
      )
        conflicts.push("name");
      if (conflicts.length && !overwrite) {
        setInspectedId(nodeId);
        setDraftConflicts((current) => ({ ...current, [nodeId]: true }));
        await sync().catch(handleStudioError);
        throw new Error(
          `These fields changed elsewhere: ${conflicts.join(", ")}. Load the latest version or keep your changes.`,
        );
      }
      const metadata = { ...latestMetadata, ...changedMetadata };
      const updated =
        !labelChanged && !Object.keys(changedMetadata).length
          ? selected
          : await state.client.updateNode({
              nodeId,
              version: selected.version,
              label: labelChanged ? draft.label : undefined,
              metadata,
            });
      applyNodeAcknowledgement(updated);
      setDraftConflicts((current) => ({ ...current, [nodeId]: false }));
      setSaveErrors((current) => ({ ...current, [nodeId]: undefined }));
      setDrafts((current) => {
        const next = { ...current };
        const latest = current[nodeId];
        delete next[nodeId];
        if (latest && latest !== draft) {
          // Keep keystrokes entered while this write was in flight, rebased on its acknowledgement.
          const remaining = Object.fromEntries(
            Object.entries(latest.metadata ?? {}).filter(
              ([key, value]) =>
                JSON.stringify(value) !==
                JSON.stringify(
                  Object.hasOwn(draft.metadata ?? {}, key)
                    ? draft.metadata?.[key]
                    : draft.baseMetadata[key],
                ),
            ),
          );
          const label = latest.label !== draft.label ? latest.label : undefined;
          if (label !== undefined || Object.keys(remaining).length)
            next[nodeId] = {
              nodeId,
              version: updated.version,
              baseLabel: normalizeStudioNode(updated).label,
              baseMetadata: nodeMetadata(updated),
              label,
              metadata: remaining,
            };
        }
        return next;
      });
      return updated;
    },
    [state, sync, applyNodeAcknowledgement, handleStudioError, setDrafts],
  );

  const flushSelected = useCallback(
    (nodeId: string, overwrite = false) => {
      const pending = savesInFlight.current.get(nodeId);
      if (pending) return pending;
      setSaveErrors((current) => ({ ...current, [nodeId]: undefined }));
      const write = trackWrite(() => saveNodeDraft(nodeId, overwrite))
        .catch((error: unknown) => {
          setSaveErrors((current) => ({
            ...current,
            [nodeId]: errorMessage(error),
          }));
          throw error;
        })
        .finally(() => {
          savesInFlight.current.delete(nodeId);
          setSavingIds(new Set(savesInFlight.current.keys()));
        });
      savesInFlight.current.set(nodeId, write);
      setSavingIds(new Set(savesInFlight.current.keys()));
      return write;
    },
    [saveNodeDraft, trackWrite],
  );

  useEffect(() => {
    if (reconnectRequired) return;
    const pending = Object.keys(drafts).filter(
      (id) =>
        !savingIds.has(id) &&
        !generatingIds.has(id) &&
        !modelSavingIds.has(id) &&
        !restoringIds.has(id) &&
        !draftConflicts[id] &&
        !saveErrors[id],
    );
    if (!pending.length) return;
    const timer = window.setTimeout(() => {
      for (const id of pending) void flushSelected(id).catch(handleStudioError);
    }, 400);
    return () => window.clearTimeout(timer);
  }, [
    drafts,
    savingIds,
    generatingIds,
    modelSavingIds,
    restoringIds,
    draftConflicts,
    saveErrors,
    reconnectRequired,
    flushSelected,
    handleStudioError,
  ]);

  const restoreOutputVersion = async (
    nodeId: string,
    outputVersionId: string,
  ) => {
    if (!state || reconnectRequired)
      throw new Error("Reconnect Studio to restore a version.");
    if (
      restoresInFlight.current.has(nodeId) ||
      generationInFlight.current.has(nodeId)
    )
      return;
    restoresInFlight.current.add(nodeId);
    setRestoringIds(new Set(restoresInFlight.current));
    try {
      await trackWrite(async () => {
        const selected = await saveNodeDraft(nodeId);
        const updated = await state.client.restoreOutputVersion(
          selected,
          outputVersionId,
        );
        applyNodeAcknowledgement(updated);
      });
    } finally {
      await sync().catch(handleStudioError);
      restoresInFlight.current.delete(nodeId);
      setRestoringIds(new Set(restoresInFlight.current));
    }
  };

  const generateNode = useCallback(
    async (nodeId: string) => {
      if (!state || reconnectRequired)
        throw new Error("Reconnect Studio to generate this node.");
      if (generationInFlight.current.has(nodeId)) return;
      if (restoresInFlight.current.has(nodeId))
        throw new Error("Wait for this version to finish restoring.");
      generationInFlight.current.add(nodeId);
      setGeneratingIds(new Set(generationInFlight.current));
      try {
        await trackWrite(async () => {
          const selected = await saveNodeDraft(nodeId);
          if (!canGenerateStudioNode(normalizeStudioNode(selected)))
            throw new Error("This node does not support generation.");
          const updated = await state.client.executeNode(selected);
          applyNodeAcknowledgement(updated);
        });
      } finally {
        // Reconcile even after an interrupted response; Studio may have accepted it.
        await sync().catch(handleStudioError);
        generationInFlight.current.delete(nodeId);
        setGeneratingIds(new Set(generationInFlight.current));
      }
    },
    [
      state,
      reconnectRequired,
      trackWrite,
      saveNodeDraft,
      applyNodeAcknowledgement,
      sync,
      handleStudioError,
    ],
  );

  const reload = () => {
    load(true);
  };

  const closeStudio = async () => {
    if (closing) return;
    setClosing(true);
    try {
      await trackWrite(async () => {
        for (const id of Object.keys(draftsRef.current))
          await saveNodeDraft(id);
      });
      onClose();
    } catch (error) {
      handleStudioError(error);
    } finally {
      setClosing(false);
    }
  };

  if (loading && !state) return <StudioLoadingState onClose={onClose} />;
  if (!state || !bootstrap)
    return (
      <StudioConnectionPanel
        companyId={companyId}
        runtimeId={runtimeId}
        agentId={agentId}
        reason={connectionError ?? error}
        autoRetryOnAuthenticated={!connectionError}
        onClose={onClose}
        onRetry={reload}
        onError={onError}
      />
    );

  return (
    <section className="studio-mode-panel" aria-label="Studio mode">
      <header>
        <div className="min-w-0">
          <span className="studio-mode-panel__project-icon">
            <Studio size={20} aria-hidden />
          </span>
          <div className="studio-mode-panel__project-copy">
            <span className="studio-mode-panel__eyebrow">Studio</span>
            <strong>{bootstrap.project.title}</strong>
          </div>
        </div>
        <div className="studio-mode-panel__actions">
          {reconnectRequired ? (
            <button
              type="button"
              className="studio-mode-panel__reconnect-action"
              onClick={() => setShowReconnect(true)}
            >
              Reconnect Studio
            </button>
          ) : null}
          {canToggleExpanded ? (
            <button
              type="button"
              onClick={onToggleExpanded}
              aria-label={fullPage ? "Collapse Studio" : "Expand Studio"}
              title={fullPage ? "Collapse Studio" : "Expand Studio"}
            >
              {fullPage ? <Minimize2 size={16} /> : <Maximize size={16} />}
            </button>
          ) : null}
          <button
            type="button"
            onClick={reload}
            aria-label="Reload Studio graph"
          >
            <RefreshCw size={16} />
          </button>
          <button
            type="button"
            onClick={() => void closeStudio()}
            disabled={closing}
            aria-label="Close Studio"
          >
            <X size={16} />
          </button>
        </div>
      </header>
      <div className="studio-mode-panel__body">
        <div
          className="studio-mode-panel__flow"
          ref={canvasRef}
          onPointerDownCapture={(event) => {
            if (
              toolsOpen &&
              event.target instanceof Element &&
              !event.target.closest(
                ".studio-mode-panel__tool-picker, .studio-mode-panel__toolbar",
              )
            )
              setToolsOpen(false);
          }}
          onKeyDown={(event) => {
            if (event.key === "Escape" && toolsOpen) {
              event.preventDefault();
              event.stopPropagation();
              setToolsOpen(false);
            }
          }}
        >
          <StudioToolbar
            toolsOpen={toolsOpen}
            creating={creating}
            arranging={arranging}
            reconnectRequired={reconnectRequired}
            nodeCount={bootstrap.nodes.length}
            interactionMode={interactionMode}
            onTogglePicker={() => setToolsOpen((open) => !open)}
            onInteractionModeChange={setInteractionMode}
            onAddNote={() => createNode("comment")}
            onAddGroup={() => createNode("node_group")}
            onArrange={applyLayout}
          />
          {toolsOpen ? (
            <StudioNodePicker
              catalog={catalog}
              catalogError={catalogError}
              busy={creating || reconnectRequired}
              onCreate={createNode}
              onClose={() => setToolsOpen(false)}
            />
          ) : null}
          {!bootstrap.nodes.length && !toolsOpen && !reconnectRequired ? (
            <StudioNodePicker
              catalog={catalog}
              catalogError={catalogError}
              busy={creating}
              onCreate={createNode}
              welcome
            />
          ) : null}
          <StudioGraph
            bootstrap={visibleBootstrap ?? bootstrap}
            catalog={catalog}
            collaboration={collaboration}
            onReady={handleGraphReady}
            interactionMode={interactionMode}
            capabilities={capabilities}
            onPositionCommit={onPositionCommit}
            onMutation={onMutation}
            onInspect={inspectNode}
            onModelChange={
              reconnectRequired || closing ? undefined : changeNodeModel
            }
            onGenerate={generateNode}
            generatingIds={generatingIds}
            restoringIds={restoringIds}
            generationDisabledReason={
              reconnectRequired
                ? "Reconnect Studio to generate."
                : closing
                  ? "Saving your edits…"
                  : undefined
            }
            onRefreshMedia={refreshMedia}
            onError={handleStudioError}
            onUnsupportedAction={(action) =>
              handleStudioError(`${action} is unavailable.`)
            }
          />
          {showReconnect ? (
            <div
              className="studio-mode-panel__reconnect-overlay absolute inset-0 z-[20] grid place-items-center p-[24px] [background:color-mix(in_srgb,_var(--workspace-content-bg)_65%,_transparent)]"
              role="dialog"
              aria-label="Reconnect Studio"
            >
              <StudioConnectionPanel
                companyId={companyId}
                runtimeId={runtimeId}
                agentId={agentId}
                reason={connectionError}
                autoRetryOnAuthenticated
                onClose={() => setShowReconnect(false)}
                onRetry={reload}
                onError={onError}
              />
            </div>
          ) : null}
        </div>
        {inspected ? (
          <StudioNodeSettings
            key={inspected.id}
            node={inspected}
            bootstrap={bootstrap}
            catalog={catalog}
            catalogError={catalogError}
            onRetryCatalog={() =>
              setCatalogRevision((revision) => revision + 1)
            }
            label={draft?.label ?? inspected.label}
            metadata={{ ...nodeMetadata(inspected.raw), ...draft?.metadata }}
            dirty={Boolean(draft)}
            saving={savingIds.has(inspected.id)}
            saveError={saveErrors[inspected.id]}
            modelSaving={closing || modelSavingIds.has(inspected.id)}
            restoring={restoringIds.has(inspected.id)}
            onRestoreVersion={restoreOutputVersion}
            generationPending={generatingIds.has(inspected.id)}
            onGenerate={generateNode}
            connectionUnavailable={reconnectRequired}
            onLabel={(label) => editDraft({ label })}
            onMetadata={(metadata) => editDraft({ metadata })}
            onSave={async () => {
              await flushSelected(inspected.id);
            }}
            conflict={Boolean(draftConflicts[inspected.id])}
            onKeepDraft={async () => {
              await flushSelected(inspected.id, true);
            }}
            onDiscard={() => {
              discardDraft(inspected.id);
              void sync().catch(handleStudioError);
            }}
            onClose={() => {
              setInspectedId(null);
            }}
            onError={handleStudioError}
          />
        ) : null}
      </div>
    </section>
  );
}
