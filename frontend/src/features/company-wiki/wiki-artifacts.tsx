import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { JSONContent } from "@tiptap/react";
import * as HoverCard from "@radix-ui/react-hover-card";
import { Link } from "react-router";
import { apiClient } from "@/lib/api-client";
import { zenithTeamApi, executeZenithTeamQuery } from "@/shared/api/zenith-team-api";
import type { components } from "@/shared/api/generated/api.generated";
import type {
  ArtifactRunResult,
  ArtifactType,
  ConversationArtifactPage,
  DeliverableStatus,
} from "@/features/run-deliverables/model/run-deliverable";
import {
  File,
  FileText,
  Image,
  LayoutDashboard,
  LockKeyhole,
  Monitor,
  RefreshCw,
  type IconComponent,
} from "@/shared/ui/icons";
import "./wiki-artifacts.css";

type ResolvedArtifacts = components["schemas"]["ResolvedArtifacts"];

// Mirrors the server's artifactRefPattern (internal/wiki/service.go): an
// Artifact id and an optional 0-based item ordinal. The token carries no
// label, so a private title never enters a body every member can read.
export const artifactRefSource = String.raw`\[\[artifact:([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})(?:#(\d{1,2}))?\]\]`;

export function artifactRefToken(id: string, ordinal: number | null) {
  return ordinal === null
    ? `[[artifact:${id}]]`
    : `[[artifact:${id}#${ordinal}]]`;
}

/** Unique cited Artifact ids, in a stable order for query caching. */
export function citedArtifactIds(body: string) {
  const ids = new Set<string>();
  for (const match of body.matchAll(new RegExp(artifactRefSource, "g")))
    ids.add(match[1].toLowerCase());
  // A page cites at most 50 distinct items, so at most 50 ids.
  return [...ids].sort().slice(0, 50);
}

export const wikiArtifactsApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listCitableArtifacts: builder.query<
      ConversationArtifactPage,
      { companyId: string; search: string }
    >({
      providesTags: [{ type: "Artifact" }, { type: "Deliverable" }],
      queryFn: ({ companyId, search }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<ConversationArtifactPage>(
              `/companies/${encodeURIComponent(companyId)}/artifacts`,
              { params: { limit: 20, search: search || undefined }, signal },
            ),
          "Artifacts could not be loaded.",
        ),
    }),
    resolveWikiArtifacts: builder.query<
      ResolvedArtifacts,
      { companyId: string; ids: string[] }
    >({
      providesTags: [{ type: "Artifact" }, { type: "Deliverable" }],
      // The shared serializer joins arrays with commas; the route takes a
      // repeated `id`.
      queryFn: ({ companyId, ids }, { signal }) =>
        executeZenithTeamQuery(
          () =>
            apiClient.get<ResolvedArtifacts>(
              `/companies/${encodeURIComponent(companyId)}/artifacts/resolve?${ids
                .map((id) => `id=${encodeURIComponent(id)}`)
                .join("&")}`,
              { signal },
            ),
          "Cited artifacts could not be loaded.",
        ),
    }),
  }),
});

export const { useListCitableArtifactsQuery, useResolveWikiArtifactsQuery } =
  wikiArtifactsApi;

type WikiArtifactsValue = {
  companyId: string;
  artifacts: Map<string, ArtifactRunResult>;
  /** Ids still being resolved; they render as a neutral placeholder. */
  pending: Set<string>;
  /** The resolve request failed: unknown ids are "couldn't load", not "unavailable". */
  failed: boolean;
  retry: () => void;
};

const WikiArtifactsContext = createContext<WikiArtifactsValue | null>(null);

/**
 * Resolves every Artifact cited in `body` for the current viewer with one
 * request. Citations the viewer cannot open are absent from the answer and
 * render as unavailable.
 */
export function WikiArtifactsProvider({
  companyId,
  body = "",
  content,
  children,
}: {
  companyId: string;
  body?: string;
  content?: JSONContent;
  children: ReactNode;
}) {
  const refs = new Set(citedArtifactIds(body));
  const collect = (node: JSONContent) => {
    if (node.type === "wikiArtifactRef" && typeof node.attrs?.id === "string")
      refs.add(node.attrs.id.toLowerCase());
    node.content?.forEach(collect);
  };
  if (content) collect(content);
  const key = [...refs].sort().slice(0, 50).join(",");
  const ids = useMemo(() => (key ? key.split(",") : []), [key]);
  const resolved = useResolveWikiArtifactsQuery(
    { companyId, ids },
    { skip: !companyId || ids.length === 0 },
  );
  const items = resolved.currentData?.items;
  const fetching = resolved.isFetching;
  const failed = resolved.isError && !fetching;
  const { refetch } = resolved;
  const value = useMemo<WikiArtifactsValue>(() => {
    const artifacts = new Map(
      (items ?? []).map((item) => [item.artifact.id.toLowerCase(), item]),
    );
    // Keep showing the last answer while a new citation is resolved; only ids
    // that answer does not cover are pending.
    const pending = new Set(
      fetching ? ids.filter((id) => !artifacts.has(id)) : [],
    );
    return {
      companyId,
      artifacts,
      pending,
      failed,
      retry: () => void refetch(),
    };
  }, [companyId, ids, items, fetching, failed, refetch]);
  return (
    <WikiArtifactsContext.Provider value={value}>
      {children}
    </WikiArtifactsContext.Provider>
  );
}

const typeIcons: Record<ArtifactType, IconComponent> = {
  app: Monitor,
  media: Image,
  text: FileText,
  page: FileText,
  file: File,
  whiteboard: LayoutDashboard,
};

const statusLabels: Record<DeliverableStatus, string> = {
  ready_for_review: "Ready for review",
  approved: "Approved",
  changes_requested: "Changes requested",
  superseded: "Superseded",
};

/**
 * One Artifact citation. Viewers who can open the Artifact see its title and
 * the cited item, with a hover card; everyone else sees the same
 * "Unavailable artifact" chip whether it is private, deleted or missing.
 */
export function WikiArtifactChip({
  id,
  ordinal,
  interactive = true,
}: {
  id: string;
  ordinal: number | null;
  interactive?: boolean;
}) {
  const context = useContext(WikiArtifactsContext);
  const key = id.toLowerCase();
  if (!context || context.pending.has(key))
    return (
      <span className="zen-wiki-artifact is-pending" aria-busy="true">
        <FileText size={13} />
        <span>Artifact</span>
      </span>
    );
  const result = context.artifacts.get(key);
  if (!result && context.failed)
    return (
      <button
        type="button"
        className="zen-wiki-artifact is-error"
        title="The artifact could not be loaded. Retry."
        onClick={(event) => {
          event.preventDefault();
          context.retry();
        }}
      >
        <RefreshCw size={13} />
        <span>Couldn’t load artifact · Retry</span>
      </button>
    );
  const item =
    result && ordinal !== null
      ? result.artifact.items.find((entry) => entry.ordinal === ordinal)
      : undefined;
  if (!result || (ordinal !== null && !item))
    return (
      <span
        className="zen-wiki-artifact is-unavailable"
        title="You can't open this artifact, or it no longer exists."
      >
        <LockKeyhole size={13} />
        <span>Unavailable artifact</span>
      </span>
    );
  const { artifact, deliverable } = result;
  const Icon = typeIcons[artifact.type];
  const status = deliverable?.status;
  const content = (
    <>
      <Icon size={13} />
      <span>{artifact.title}</span>
      {item && <small>› {item.displayName}</small>}
      {status && status !== "approved" && <em>{statusLabels[status]}</em>}
    </>
  );
  const chip = interactive ? (
    <Link
      className="zen-wiki-artifact"
      to={`/companies/${context.companyId}/artifacts/${artifact.id}${
        item ? `?item=${item.ordinal}` : ""
      }`}
      // A click inside the app opens the Artifact page here, not the
      // desktop hand-off a shared link gets.
      state={{
        artifactInBrowser: true,
      }}
    >
      {content}
    </Link>
  ) : (
    <span className="zen-wiki-artifact">{content}</span>
  );
  return (
    <HoverCard.Root openDelay={300} closeDelay={80}>
      <HoverCard.Trigger asChild>{chip}</HoverCard.Trigger>
      <HoverCard.Portal>
        <HoverCard.Content
          className="zen-wiki-artifact-card z-[230] grid gap-[4px] w-[min(300px,_calc(100vw_-_24px))] p-[10px_12px] [border:1px_solid_var(--workspace-divider)] rounded-[9px] [background:var(--workspace-overlay-bg)] text-workspace-text [box-shadow:var(--workspace-overlay-shadow)] font-workspace-body text-[length:12.5px] leading-[1.45]"
          side="bottom"
          align="start"
          sideOffset={6}
          collisionPadding={12}
        >
          <span className="flex items-center gap-[5px] text-workspace-muted text-[length:11px]">
            <Icon size={14} /> Artifact
            {status && <> · {statusLabels[status]}</>}
          </span>
          <strong>{artifact.title}</strong>
          {item && (
            <span className="text-workspace-muted">{item.displayName}</span>
          )}
          {artifact.summary && <p>{artifact.summary}</p>}
          <time dateTime={artifact.createdAt}>
            {new Date(artifact.createdAt).toLocaleDateString(undefined, {
              dateStyle: "medium",
            })}
          </time>
        </HoverCard.Content>
      </HoverCard.Portal>
    </HoverCard.Root>
  );
}
