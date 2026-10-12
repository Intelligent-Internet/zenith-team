import { normalizeStudioCatalog } from "./studio-model-catalog";
import type { components } from "./generated/api.generated";
type Access = components["schemas"]["StudioEditorAccessToken"];
export type StudioProject = {
  id: string;
  sessionId: string | null;
  title: string;
  description: string | null;
  isArchived: boolean;
  isPublic: boolean;
  stars?: number;
  version: number;
  createdAt: string;
  updatedAt: string;
  metadataJson: string;
};
export type StudioNodeResponse = {
  id: string;
  projectId: string;
  createdBy: string;
  nodeType: string;
  label: string | null;
  status: string;
  version: number;
  createdAt: string;
  updatedAt: string;
  metadataJson: string;
  outputJson?: string;
};
export type StudioEdgeResponse = {
  id: string;
  projectId: string;
  createdBy: string;
  sourceId: string;
  sourceHandle: string;
  targetId: string;
  targetHandle: string;
  createdAt: string;
};
export type StudioMemberResponse = {
  id: string;
  projectId: string;
  userId: string;
  email: string | null;
  name: string | null;
  avatar: string | null;
  role: "owner" | "editor" | "viewer";
  createdAt: string;
};
export type StudioBootstrap = {
  project: StudioProject;
  nodes: StudioNodeResponse[];
  edges: StudioEdgeResponse[];
  members: StudioMemberResponse[];
};
export type StudioUploadResult = {
  projectId: string;
  assetId: string;
  nodeId: string;
  fileName: string;
  kind: "image" | "video" | "audio";
  mimeType: string;
  sizeBytes: number;
  sha256: string;
};
type Node = StudioNodeResponse;
type Edge = StudioEdgeResponse;
type Member = StudioMemberResponse;
type Bootstrap = StudioBootstrap;
type Project = StudioProject;

type RecordValue = Record<string, unknown>;

function record(value: unknown): RecordValue {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as RecordValue)
    : {};
}

function json(value: unknown): string {
  return JSON.stringify(value ?? {}) ?? "{}";
}

function project(value: unknown): Project {
  const raw = record(value);
  return {
    id: String(raw.id ?? ""),
    sessionId: typeof raw.session_id === "string" ? raw.session_id : null,
    title: String(raw.title ?? "Studio"),
    description: typeof raw.description === "string" ? raw.description : null,
    isArchived: raw.is_archived === true,
    isPublic: raw.is_public === true,
    stars: typeof raw.stars === "number" ? raw.stars : undefined,
    version: typeof raw.version === "number" ? raw.version : 1,
    createdAt: String(raw.created_at ?? new Date().toISOString()),
    updatedAt: String(raw.updated_at ?? new Date().toISOString()),
    metadataJson: json(raw.metadata),
  };
}

function node(value: unknown): Node {
  const raw = record(value);
  return {
    id: String(raw.id ?? ""),
    projectId: String(raw.project_id ?? ""),
    createdBy: String(raw.created_by ?? ""),
    nodeType: String(raw.node_type ?? "text") as Node["nodeType"],
    label: typeof raw.label === "string" ? raw.label : null,
    status: String(raw.status ?? "draft") as Node["status"],
    version: typeof raw.version === "number" ? raw.version : 1,
    createdAt: String(raw.created_at ?? new Date().toISOString()),
    updatedAt: String(raw.updated_at ?? new Date().toISOString()),
    metadataJson: json(raw.metadata),
    ...(raw.output === null || raw.output === undefined
      ? {}
      : { outputJson: json(raw.output) }),
  };
}

function edge(value: unknown): Edge {
  const raw = record(value);
  return {
    id: String(raw.id ?? ""),
    projectId: String(raw.project_id ?? ""),
    createdBy: String(raw.created_by ?? ""),
    sourceId: String(raw.source_id ?? ""),
    sourceHandle: String(raw.source_handle ?? "out"),
    targetId: String(raw.target_id ?? ""),
    targetHandle: String(raw.target_handle ?? "in"),
    createdAt: String(raw.created_at ?? new Date().toISOString()),
  };
}

function member(value: unknown): Member {
  const raw = record(value);
  return {
    id: String(raw.id ?? ""),
    projectId: String(raw.project_id ?? ""),
    userId: String(raw.user_id ?? ""),
    email: typeof raw.email === "string" ? raw.email : null,
    name: typeof raw.name === "string" ? raw.name : null,
    avatar: typeof raw.avatar === "string" ? raw.avatar : null,
    role: String(raw.role ?? "viewer") as Member["role"],
    createdAt: String(raw.created_at ?? new Date().toISOString()),
  };
}

function bootstrap(value: unknown): Bootstrap {
  const raw = record(value);
  return {
    project: project(raw.project),
    nodes: Array.isArray(raw.nodes) ? raw.nodes.map(node) : [],
    edges: Array.isArray(raw.edges) ? raw.edges.map(edge) : [],
    members: Array.isArray(raw.members) ? raw.members.map(member) : [],
  };
}

export type StudioDirectClientOptions = {
  access: Access;
  renewAccess?: () => Promise<Access>;
};

export class StudioRequestError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "StudioRequestError";
    this.status = status;
  }
}

export class StudioReauthenticationError extends StudioRequestError {
  constructor() {
    super(401, "Studio needs you to sign in again.");
    this.name = "StudioReauthenticationError";
  }
}

export class StudioDirectClient {
  private readonly origin: string;
  private access: Access;
  private readonly renewAccess?: () => Promise<Access>;
  private renewal: Promise<Access> | null = null;
  private readonly accessListeners = new Set<(access: Access) => void>();

  constructor(options: StudioDirectClientOptions) {
    this.origin = options.access.origin.replace(/\/+$/, "");
    this.access = options.access;
    this.renewAccess = options.renewAccess;
  }

  subscribeAccess(listener: (access: Access) => void) {
    this.accessListeners.add(listener);
    listener(this.access);
    return () => {
      this.accessListeners.delete(listener);
    };
  }

  refreshAccess(): Promise<Access> {
    if (this.renewal) return this.renewal;
    if (!this.renewAccess)
      return Promise.reject(
        new StudioRequestError(401, "Studio session expired."),
      );
    this.renewal = this.renewAccess()
      .then((access) => {
        if (
          access.origin.replace(/\/+$/, "") !== this.origin ||
          access.accountId !== this.access.accountId
        )
          throw new Error("Studio account changed. Reopen the canvas.");
        this.access = access;
        for (const listener of this.accessListeners) listener(access);
        return access;
      })
      .finally(() => {
        this.renewal = null;
      });
    return this.renewal;
  }

  async ensureAccess(): Promise<Access> {
    if (Date.parse(this.access.expiresAt) > Date.now() + 30_000)
      return this.access;
    try {
      return await this.refreshAccess();
    } catch (error) {
      // A temporary renewal failure must not interrupt a still-valid session.
      if (Date.parse(this.access.expiresAt) > Date.now()) return this.access;
      throw error;
    }
  }

  async getModelCatalog() {
    return normalizeStudioCatalog(
      await this.request("/v1/factory/model-catalog"),
    );
  }

  private async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    init.signal?.throwIfAborted();
    await this.ensureAccess();
    init.signal?.throwIfAborted();
    const attemptedToken = this.access.accessToken;
    try {
      return await this.send<T>(path, init);
    } catch (error) {
      if (!(error instanceof StudioRequestError) || error.status !== 401)
        throw error;
      // Concurrent reads and writes share one renewal. A delayed 401 from an
      // older token can replay with the token another request already renewed.
      if (attemptedToken === this.access.accessToken)
        await this.refreshAccess();
      init.signal?.throwIfAborted();
      return this.send<T>(path, init);
    }
  }

  private async send<T>(path: string, init: RequestInit): Promise<T> {
    const controller = new AbortController();
    const timeout = globalThis.setTimeout(() => controller.abort(), 15_000);
    const abortFromCaller = () => controller.abort();
    init.signal?.addEventListener("abort", abortFromCaller, { once: true });
    if (init.signal?.aborted) controller.abort();
    try {
      const response = await fetch(`${this.origin}${path}`, {
        ...init,
        signal: controller.signal,
        headers: {
          Accept: "application/json",
          Authorization: `Bearer ${this.access.accessToken}`,
          ...(init.body ? { "Content-Type": "application/json" } : {}),
          ...init.headers,
        },
      });
      if (!response.ok) {
        let detail = `Studio request failed (${response.status}).`;
        try {
          const body = record(await response.json());
          detail = String(body.detail ?? body.message ?? detail);
        } catch {
          // Keep the status message when Studio returned a non-JSON error.
        }
        throw new StudioRequestError(response.status, detail);
      }
      if (response.status === 204) return undefined as T;
      const body = await response.text();
      return (body ? JSON.parse(body) : undefined) as T;
    } finally {
      globalThis.clearTimeout(timeout);
      init.signal?.removeEventListener("abort", abortFromCaller);
    }
  }

  async getBootstrap(projectId: string, signal?: AbortSignal) {
    return bootstrap(
      await this.request(`/v1/factory/projects/${projectId}/bootstrap`, {
        signal,
      }),
    );
  }

  /** Upload one already-authorized Team artifact as a native Studio source node. */
  async uploadImageArtifact(
    projectId: string,
    file: Blob,
    fileName: string,
    idempotencyKey: string,
  ): Promise<StudioUploadResult> {
    const bytes = await file.arrayBuffer();
    const digest = await crypto.subtle.digest("SHA-256", bytes);
    const sha256 = Array.from(new Uint8Array(digest), (value) =>
      value.toString(16).padStart(2, "0"),
    ).join("");
    const mimeType = file.type || "image/png";
    const prepared = await this.request<{
      project_id: string;
      asset_id: string;
      status: "pending" | "complete";
      upload_url: string | null;
    }>(
      `/v1/factory/media/projects/${encodeURIComponent(projectId)}/uploads/prepare`,
      {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: JSON.stringify({
          file_name: fileName,
          kind: "image",
          size_bytes: bytes.byteLength,
          mime_type: mimeType,
          sha256,
        }),
      },
    );
    if (prepared.status === "pending") {
      if (!prepared.upload_url)
        throw new Error("Studio upload URL was missing.");
      const upload = await fetch(prepared.upload_url, {
        method: "PUT",
        headers: { "Content-Type": mimeType },
        body: bytes,
      });
      if (!upload.ok)
        throw new Error(`Studio upload failed (${upload.status}).`);
    }
    const completed = await this.request<{
      project_id: string;
      asset_id: string;
      node_id: string;
      file_name: string;
      kind: "image" | "video" | "audio";
      mime_type: string;
      size_bytes: number;
      sha256: string;
    }>(
      `/v1/factory/media/projects/${encodeURIComponent(projectId)}/uploads/${encodeURIComponent(prepared.asset_id)}/complete`,
      { method: "POST" },
    );
    return {
      projectId: completed.project_id,
      assetId: completed.asset_id,
      nodeId: completed.node_id,
      fileName: completed.file_name,
      kind: completed.kind,
      mimeType: completed.mime_type,
      sizeBytes: completed.size_bytes,
      sha256: completed.sha256,
    };
  }

  async updatePosition(nodeId: string, position: { x: number; y: number }) {
    return node(
      await this.request(`/v1/factory/nodes/${nodeId}/position`, {
        method: "PATCH",
        body: JSON.stringify({ position }),
      }),
    );
  }

  async createNode(
    projectId: string,
    payload: { nodeType: string; label?: string; metadata?: RecordValue },
  ) {
    return node(
      await this.request("/v1/factory/nodes", {
        method: "POST",
        body: JSON.stringify({
          project_id: projectId,
          node_type: payload.nodeType,
          label: payload.label ?? null,
          metadata: payload.metadata ?? {},
        }),
      }),
    );
  }

  async updateNode(payload: {
    nodeId: string;
    version: number;
    label?: string;
    metadata?: RecordValue;
  }) {
    return node(
      await this.request(`/v1/factory/nodes/${payload.nodeId}`, {
        method: "PATCH",
        body: JSON.stringify({
          version: payload.version,
          ...(payload.label !== undefined ? { label: payload.label } : {}),
          ...(payload.metadata !== undefined
            ? { metadata: payload.metadata }
            : {}),
        }),
      }),
    );
  }

  async deleteNode(nodeId: string) {
    await this.request(`/v1/factory/nodes/${nodeId}`, {
      method: "DELETE",
    });
  }

  async executeNode(current: StudioNodeResponse): Promise<StudioNodeResponse> {
    // The native execute route returns execution fields, not a complete node.
    const response = await this.request<{
      id: string;
      status: string;
      version: number;
      updated_at: string;
      metadata: RecordValue;
    }>(`/v1/factory/nodes/${encodeURIComponent(current.id)}/execute`, {
      method: "POST",
    });
    return {
      ...current,
      id: response.id,
      status: response.status,
      version: response.version,
      updatedAt: response.updated_at,
      metadataJson: json(response.metadata),
    };
  }

  async restoreOutputVersion(
    current: StudioNodeResponse,
    outputVersionId: string,
  ) {
    return node(
      await this.request(
        `/v1/factory/nodes/${encodeURIComponent(current.id)}/output-versions/${encodeURIComponent(outputVersionId)}/restore`,
        {
          method: "POST",
          body: JSON.stringify({ version: current.version }),
        },
      ),
    );
  }

  async createEdge(
    projectId: string,
    payload: {
      sourceId: string;
      targetId: string;
      sourceHandle?: string;
      targetHandle?: string;
    },
  ) {
    return edge(
      await this.request("/v1/factory/edges", {
        method: "POST",
        body: JSON.stringify({
          project_id: projectId,
          source_id: payload.sourceId,
          target_id: payload.targetId,
          source_handle: payload.sourceHandle ?? "out",
          target_handle: payload.targetHandle ?? "in",
        }),
      }),
    );
  }

  async deleteEdge(edgeId: string) {
    await this.request(`/v1/factory/edges/${edgeId}`, {
      method: "DELETE",
    });
  }

  async applyLayout(
    projectId: string,
    version: number,
    positions: Record<string, { x: number; y: number }>,
  ) {
    await this.request(`/v1/factory/projects/${projectId}/layout`, {
      method: "POST",
      body: JSON.stringify({
        strategy: "elk_layered_balanced",
        version,
        positions,
      }),
    });
    return this.getBootstrap(projectId);
  }
}

export function createStudioDirectClient(
  access: Access,
  renewAccess?: () => Promise<Access>,
) {
  return new StudioDirectClient({
    access,
    renewAccess,
  });
}
