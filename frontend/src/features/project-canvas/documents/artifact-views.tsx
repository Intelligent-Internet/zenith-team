import { useMemo, useState } from "react";
import { Virtuoso } from "react-virtuoso";
import {
  Background,
  BackgroundVariant,
  Handle,
  Position,
  ReactFlow,
  ReactFlowProvider,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import {
  ArrowUpRight,
  ChevronLeft,
  ChevronRight,
  Clock3,
  Layers,
  Play,
  Search,
} from "@/shared/ui/icons";
import { artifactFormat } from "../canvas/canvas-nodes";
import {
  ArtifactCollection,
  ArtifactState,
  type ArtifactCollectionProps,
} from "../canvas/artifact-collection";
import { GraphControls, type ViewCamera } from "./graph-controls";
import {
  useCanvasTimelineQuery,
  type CanvasArtifact,
  type CanvasItem,
  type CanvasSnapshot,
} from "../api/canvas-api";

export function FilesView(props: ArtifactCollectionProps) {
  const [search, setSearch] = useState("");
  const [format, setFormat] = useState("");
  const [origin, setOrigin] = useState("");
  const [sort, setSort] = useState("updated");
  const formats = useMemo(
    () =>
      [
        ...new Set(
          props.artifacts.map((row) => artifactFormat(row.latestVersion).label),
        ),
      ].sort(),
    [props.artifacts],
  );
  const rows = useMemo(
    () =>
      props.artifacts
        .filter(
          (row) =>
            (!search.trim() ||
              `${row.title} ${row.latestVersion.filename ?? ""} ${row.creatorName}`
                .toLowerCase()
                .includes(search.trim().toLowerCase())) &&
            (!format || artifactFormat(row.latestVersion).label === format) &&
            (!origin || (origin === "tasks" ? !!row.taskId : !row.taskId)),
        )
        .sort((a, b) =>
          sort === "name"
            ? a.title.localeCompare(b.title)
            : sort === "comments"
              ? b.openComments - a.openComments
              : b.updatedAt.localeCompare(a.updatedAt),
        ),
    [props.artifacts, search, format, origin, sort],
  );
  return (
    <div className="pc-files-view">
      <div className="pc-view-toolbar">
        <div>
          <strong>Every artifact, one place</strong>
          <span>
            {rows.length} of {props.artifacts.length} artifacts · Versions stay
            together
          </span>
        </div>
        <label className="pc-search">
          <Search size={16} />
          <input
            aria-label="Search files"
            placeholder="Search files or creators…"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
      </div>
      <div className="pc-view-filters">
        <select
          aria-label="File type"
          value={format}
          onChange={(event) => setFormat(event.target.value)}
        >
          <option value="">All types</option>
          {formats.map((type) => (
            <option key={type}>{type}</option>
          ))}
        </select>

        <select
          aria-label="Artifact origin"
          value={origin}
          onChange={(event) => setOrigin(event.target.value)}
        >
          <option value="">All origins</option>
          <option value="tasks">Task artifacts</option>
          <option value="resources">Project resources</option>
        </select>
        <select
          aria-label="Sort files"
          value={sort}
          onChange={(event) => setSort(event.target.value)}
        >
          <option value="updated">Recently updated</option>
          <option value="name">Name</option>
          <option value="comments">Most comments</option>
        </select>
        {(search || format || origin) && (
          <button
            className="pc-text-button"
            onClick={() => {
              setSearch("");
              setFormat("");
              setOrigin("");
            }}
          >
            Clear filters
          </button>
        )}
      </div>
      <div className="flex-1 min-h-0 p-[0_16px_12px]">
        <ArtifactCollection {...props} artifacts={rows} />
      </div>
    </div>
  );
}
const timelineLabels = {
  created: "Added an artifact",
  version: "Saved a new version",
  accept: "Accepted a version",
  request_changes: "Requested changes",
  reject: "Rejected a version",
  keep_variant: "Kept a variant",
};
export function ArtifactTimeline({
  scope,
  artifacts,
  onOpen,
}: ArtifactCollectionProps) {
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined]);
  const query = useCanvasTimelineQuery({ ...scope, before: cursors.at(-1) });
  const byId = useMemo(
    () => new Map(artifacts.map((row) => [row.id, row])),
    [artifacts],
  );
  const entries = query.isError
    ? []
    : (query.currentData?.items.filter((row) => byId.has(row.artifactId)) ??
      []);
  return (
    <div className="pc-timeline-view">
      <div className="pc-view-toolbar">
        <div>
          <strong>How the work evolved</strong>
          <span>Saved versions and review decisions · Newest first</span>
        </div>
        <Clock3 size={21} />
      </div>
      {query.isError ? (
        <div className="pc-state flex-1 flex flex-col items-center justify-center gap-[16px]">
          <p>The timeline couldn’t be loaded.</p>
          <button className="pc-button" onClick={() => void query.refetch()}>
            Try again
          </button>
        </div>
      ) : query.isFetching && !query.currentData ? (
        <div className="pc-state flex-1 flex flex-col items-center justify-center gap-[16px]">
          Loading activity…
        </div>
      ) : (
        <div className="flex-1 min-h-0 p-[16px_24px]">
          <Virtuoso
            data={entries}
            computeItemKey={(_, row) => row.id}
            components={{
              EmptyPlaceholder: () => (
                <div className="grid [place-content:center] min-h-[160px] p-[24px] text-[var(--pc-soft)] text-[length:12px] text-center leading-[1.7]">
                  No artifact activity on this page.
                </div>
              ),
            }}
            itemContent={(_, entry) => (
              <article className="pc-timeline-entry grid grid-cols-[82px_minmax(0,_1fr)] max-w-[820px] mx-auto">
                <time dateTime={entry.createdAt}>
                  {new Date(entry.createdAt).toLocaleDateString(undefined, {
                    month: "short",
                    day: "numeric",
                  })}
                  <small>
                    {new Date(entry.createdAt).toLocaleTimeString(undefined, {
                      hour: "2-digit",
                      minute: "2-digit",
                    })}
                  </small>
                </time>
                <div>
                  <span className="text-[var(--pc-soft)] text-[length:9px] font-[650] tracking-[1.5px]">
                    {timelineLabels[entry.action]}
                  </span>
                  <button
                    onClick={() => {
                      const artifact = byId.get(entry.artifactId);
                      if (artifact) onOpen(artifact, entry.versionId);
                    }}
                  >
                    <strong>{entry.title}</strong>
                    <ArrowUpRight size={15} />
                  </button>
                  <p>
                    {entry.actorName} · Version {entry.versionNumber}
                  </p>
                  {entry.feedback && <blockquote>{entry.feedback}</blockquote>}
                </div>
              </article>
            )}
          />
        </div>
      )}
      <footer className="flex gap-[18px] items-center justify-center p-[14px] [border-top:1px_solid_var(--pc-line)] text-[var(--pc-muted)] text-[length:11px]">
        <button
          className="pc-button"
          disabled={cursors.length === 1 || query.isFetching}
          onClick={() => setCursors((value) => value.slice(0, -1))}
        >
          <ChevronLeft size={15} />
          Newer
        </button>
        <span>Page {cursors.length}</span>
        <button
          className="pc-button"
          disabled={
            !query.currentData?.nextCursor || query.isFetching || query.isError
          }
          onClick={() =>
            setCursors((value) => [...value, query.currentData!.nextCursor!])
          }
        >
          Older
          <ChevronRight size={15} />
        </button>
      </footer>
    </div>
  );
}
type RelationshipNode = Node<
  { artifact: CanvasArtifact; onOpen: ArtifactCollectionProps["onOpen"] },
  "relationship"
>;
const relationshipTypes = { relationship: RelationshipCard };
function RelationshipCard({ data }: NodeProps<RelationshipNode>) {
  const { Icon, tone, label } = artifactFormat(data.artifact.latestVersion);
  return (
    <div className={`pc-relationship-card pc-tone-${tone}`}>
      <Handle type="target" position={Position.Left} isConnectable={false} />
      <span className="pc-format-icon">
        <Icon size={20} />
      </span>
      <small>{label}</small>
      <button
        className="nodrag nopan"
        onClick={() => data.onOpen(data.artifact)}
      >
        {data.artifact.title}
        <ArrowUpRight size={15} />
      </button>
      <ArtifactState artifact={data.artifact} />
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </div>
  );
}
export function RelationshipMap(
  props: ArtifactCollectionProps & {
    snapshot: CanvasSnapshot;
    camera: ViewCamera;
  },
) {
  const model = useMemo(() => {
    const placements = new Map(
      props.snapshot.items.map((row) => [row.id, row.artifactId]),
    );
    const available = new Set(props.artifacts.map((row) => row.id));
    const connected = new Set<string>();
    const seen = new Set<string>();
    const edges = props.snapshot.edges.flatMap((edge) => {
      const source = placements.get(edge.source),
        target = placements.get(edge.target);
      if (
        !source ||
        !target ||
        source === target ||
        !available.has(source) ||
        !available.has(target)
      )
        return [];
      const key = `${source}:${target}:${edge.label}`;
      if (seen.has(key)) return [];
      seen.add(key);
      connected.add(source);
      connected.add(target);
      return [
        {
          id: edge.id,
          source,
          target,
          label: edge.label,
          type: "smoothstep",
          style: { stroke: "var(--workspace-native-text-faint)" },
          labelStyle: { fill: "var(--workspace-native-text)" },
          labelBgStyle: { fill: "var(--workspace-panel-bg)" },
          markerEnd: { type: "arrowclosed" as const },
        },
      ];
    });
    const rows = props.artifacts
      .filter((row) => connected.has(row.id))
      .sort((a, b) => a.title.localeCompare(b.title));
    const columns = Math.max(1, Math.ceil(Math.sqrt(rows.length)));
    const nodes: RelationshipNode[] = rows.map((artifact, index) => ({
      id: artifact.id,
      type: "relationship",
      position: {
        x: (index % columns) * 410,
        y: Math.floor(index / columns) * 250,
      },
      width: 290,
      height: 166,
      data: { artifact, onOpen: props.onOpen },
    }));
    return { nodes, edges };
  }, [
    props.artifacts,
    props.snapshot.items,
    props.snapshot.edges,
    props.onOpen,
  ]);
  return (
    <div className="pc-plan-view">
      <div className="pc-view-toolbar">
        <div>
          <strong>Connections in the work</strong>
          <span>
            {model.edges.length} recorded connections · Add connections between
            artifacts in Free canvas
          </span>
        </div>
      </div>
      {!model.nodes.length ? (
        <div className="pc-state flex-1 flex flex-col items-center justify-center gap-[16px]">
          <Layers size={28} />
          <h3>No artifact connections yet</h3>
          <p>
            Connect two artifact cards in Free canvas to map their relationship
            here.
          </p>
        </div>
      ) : (
        <ReactFlowProvider>
          <div className="pc-graph-surface relative flex-1 min-h-0 overflow-hidden [background:var(--workspace-content-bg)]">
            <ReactFlow
              nodes={model.nodes}
              edges={model.edges}
              nodeTypes={relationshipTypes}
              nodesDraggable={false}
              nodesConnectable={false}
              deleteKeyCode={null}
              onlyRenderVisibleElements
              panOnScroll
              zoomOnScroll={false}
              minZoom={0.1}
              maxZoom={2}
              defaultViewport={props.camera.current}
              fitView={!props.camera.current}
              onMoveEnd={(_, viewport) => {
                props.camera.current = viewport;
              }}
              proOptions={{ hideAttribution: true }}
            >
              <Background
                variant={BackgroundVariant.Dots}
                gap={24}
                size={1}
                color="var(--workspace-divider-strong)"
              />
            </ReactFlow>
            <GraphControls />
          </div>
        </ReactFlowProvider>
      )}
    </div>
  );
}
export function PresentationView({
  frames,
  artifacts,
  items,
  onStart,
  onCanvas,
}: {
  frames: CanvasItem[];
  artifacts: CanvasArtifact[];
  items: CanvasItem[];
  onStart: (index: number) => void;
  onCanvas: () => void;
}) {
  const byId = new Map(artifacts.map((row) => [row.id, row]));
  return (
    <div className="pc-presentation-view">
      <div className="pc-view-toolbar">
        <div>
          <strong>Walk through the work</strong>
          <span>
            Frames play in canvas order · Each presentation keeps a version
            snapshot
          </span>
        </div>
        <button className="pc-button" onClick={onCanvas}>
          Edit frames in Free canvas
        </button>
      </div>
      {frames.length ? (
        <div className="flex-1 min-h-0 p-[16px_24px]">
          <Virtuoso
            data={frames}
            computeItemKey={(_, row) => row.id}
            itemContent={(index, frame) => {
              const contents = items
                .filter((row) => row.parentFrameId === frame.id)
                .flatMap((row) =>
                  row.artifactId && byId.has(row.artifactId)
                    ? [byId.get(row.artifactId)!]
                    : [],
                );
              return (
                <button
                  className="pc-frame-slide"
                  onClick={() => onStart(index)}
                >
                  <span className="text-[length:25px] text-[var(--pc-soft)] tracking-[-1px]">
                    {String(index + 1).padStart(2, "0")}
                  </span>
                  <div>
                    <strong>{frame.title || `Frame ${index + 1}`}</strong>
                    <p>
                      {contents.length
                        ? contents
                            .slice(0, 3)
                            .map((row) => row.title)
                            .join(" · ")
                        : "No grouped artifacts"}
                    </p>
                  </div>
                  <Play size={22} />
                </button>
              );
            }}
          />
        </div>
      ) : (
        <div className="pc-state flex-1 flex flex-col items-center justify-center gap-[16px]">
          <Play size={30} />
          <h3>Give your work a story</h3>
          <p>Add frames in Free canvas and group artifact cards into them.</p>
          <button className="pc-primary" onClick={onCanvas}>
            Open Free canvas
          </button>
        </div>
      )}
    </div>
  );
}
