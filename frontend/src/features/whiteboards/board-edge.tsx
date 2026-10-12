import { memo } from "react";
import {
  BaseEdge,
  EdgeLabelRenderer,
  getSmoothStepPath,
  useInternalNode,
  type Edge,
  type EdgeProps,
} from "@xyflow/react";
import type { BoardEdge } from "./board-api";
import type { WhiteboardFlowNode } from "./board-node";
import {
  boardEdgePoints,
  boardEndLabelPlace,
  boardPathMiddle,
  boardPolylinePath,
  type BoardMarkerType,
  type BoardPoint,
} from "./board-diagram";

export type BoardEdgeData = { edge: BoardEdge; revision: number };
export type BoardFlowEdge = Edge<BoardEdgeData, "boardFlow" | "boardRouted">;

const markerIds: Record<BoardMarkerType, string | undefined> = {
  none: undefined,
  arrow: "wb-marker-arrow",
  open: "wb-marker-open",
  triangle: "wb-marker-triangle",
  diamond: "wb-marker-diamond",
  diamond_filled: "wb-marker-diamond-filled",
  one: "wb-marker-one",
  zero_one: "wb-marker-zero-one",
  many: "wb-marker-many",
  zero_many: "wb-marker-zero-many",
};

function markerUrl(marker: BoardMarkerType | undefined) {
  const id = markerIds[marker ?? "none"];
  return id ? `url(#${id})` : undefined;
}

/**
 * The connector markers, defined once beside the canvas and drawn in Board
 * units, so they scale with the line: a filled arrow, an open V, a hollow
 * triangle, hollow and filled diamonds, and crow's feet. Each tip sits at the
 * line's end; start markers turn around.
 */
export function BoardEdgeMarkers() {
  const marker = (
    id: string,
    width: number,
    height: number,
    children: React.ReactNode,
  ) => (
    <marker
      id={id}
      viewBox={`-1 -1 ${width + 2} ${height + 2}`}
      refX={width}
      refY={height / 2}
      markerWidth={width + 2}
      markerHeight={height + 2}
      markerUnits="userSpaceOnUse"
      orient="auto-start-reverse"
    >
      {children}
    </marker>
  );
  return (
    <svg className="wb-edge-markers" aria-hidden="true">
      <defs>
        {marker("wb-marker-arrow", 12, 10, <path d="M0,0 L12,5 L0,10 z" />)}
        {marker(
          "wb-marker-open",
          12,
          10,
          <path className="wb-marker-line" d="M0,0 L12,5 L0,10" />,
        )}
        {marker(
          "wb-marker-triangle",
          14,
          16,
          <path className="wb-marker-hollow" d="M0,0 L14,8 L0,16 z" />,
        )}
        {marker(
          "wb-marker-diamond",
          18,
          12,
          <path className="wb-marker-hollow" d="M0,6 L9,0 L18,6 L9,12 z" />,
        )}
        {marker(
          "wb-marker-diamond-filled",
          18,
          12,
          <path d="M0,6 L9,0 L18,6 L9,12 z" />,
        )}
        {marker(
          "wb-marker-one",
          14,
          12,
          <path className="wb-marker-line" d="M2,0 L2,12 M6,0 L6,12" />,
        )}
        {marker(
          "wb-marker-zero-one",
          24,
          12,
          <>
            <circle className="wb-marker-hollow" cx={6} cy={6} r={4} />
            <path className="wb-marker-line" d="M16,0 L16,12" />
          </>,
        )}
        {marker(
          "wb-marker-many",
          18,
          12,
          <path
            className="wb-marker-line"
            d="M2,0 L2,12 M6,6 L18,0 M6,6 L18,12"
          />,
        )}
        {marker(
          "wb-marker-zero-many",
          26,
          12,
          <>
            <circle className="wb-marker-hollow" cx={6} cy={6} r={4} />
            <path className="wb-marker-line" d="M14,6 L26,0 M14,6 L26,12" />
          </>,
        )}
      </defs>
    </svg>
  );
}

function EdgeLabels({
  edge,
  label,
  beside = false,
  ends,
}: {
  edge: BoardEdge;
  label: BoardPoint;
  // A self message's label sits beside its loop rather than over the line.
  beside?: boolean;
  ends: [BoardPoint, BoardPoint][];
}) {
  const endLabels = [edge.sourceLabel, edge.targetLabel];
  return (
    <EdgeLabelRenderer>
      {edge.label ? (
        <div
          className="wb-edge-label"
          style={{
            transform: `${beside ? "translate(0, -50%)" : "translate(-50%, -100%)"} translate(${label[0]}px, ${label[1]}px)`,
          }}
        >
          {edge.label}
        </div>
      ) : null}
      {ends.map(([tip, from], index) => {
        const text = endLabels[index];
        if (!text) return null;
        const place = boardEndLabelPlace(tip, from);
        return (
          <div
            key={index}
            className="wb-edge-end-label"
            style={{
              transform: `translate(${place.alignX * 100}%, ${place.alignY * 100}%) translate(${place.x}px, ${place.y}px)`,
            }}
          >
            {text}
          </div>
        );
      })}
    </EdgeLabelRenderer>
  );
}

function lineStyle(edge: BoardEdge, style: React.CSSProperties | undefined) {
  return {
    ...style,
    stroke: "var(--workspace-native-accent)",
    strokeWidth: 1.5,
    strokeDasharray: edge.line === "dashed" ? "8 6" : undefined,
  };
}

/**
 * A connector on the flow route, as the Board always drew it: from the
 * source's right handle to the target's left handle, stepping between them.
 */
export const BoardFlowEdgeView = memo(function BoardFlowEdgeView(
  props: EdgeProps<BoardFlowEdge>,
) {
  const edge = props.data!.edge;
  const [path, labelX, labelY] = getSmoothStepPath(props);
  const source: BoardPoint = [props.sourceX, props.sourceY],
    target: BoardPoint = [props.targetX, props.targetY];
  return (
    <>
      <BaseEdge
        id={props.id}
        path={path}
        style={lineStyle(edge, props.style)}
        markerStart={markerUrl(edge.startMarker)}
        markerEnd={markerUrl(edge.endMarker)}
        interactionWidth={16}
      />
      <EdgeLabels
        edge={edge}
        label={[labelX, labelY + 9]}
        ends={[
          [source, [source[0] + 20, source[1]]],
          [target, [target[0] - 20, target[1]]],
        ]}
      />
    </>
  );
});

/**
 * A connector on a straight, elbow or message route, attached by the shapes
 * of its ends rather than its handles, as the server draws it.
 */
export const BoardRoutedEdgeView = memo(function BoardRoutedEdgeView(
  props: EdgeProps<BoardFlowEdge>,
) {
  const edge = props.data!.edge;
  const source = useInternalNode<WhiteboardFlowNode>(props.source);
  const target = useInternalNode<WhiteboardFlowNode>(props.target);
  if (!source || !target) return null;
  const rect = (node: typeof source) => ({
    x: node.internals.positionAbsolute.x,
    y: node.internals.positionAbsolute.y,
    width: node.measured.width ?? node.width ?? 0,
    height: node.measured.height ?? node.height ?? 0,
  });
  const points = boardEdgePoints(
    rect(source),
    rect(target),
    source.data.item.properties.shape,
    target.data.item.properties.shape,
    edge.route,
    edge.anchorY,
    props.source === props.target,
  );
  const self = props.source === props.target;
  const middle = boardPathMiddle(points);
  // A message's label rides just above its line; others sit on the line's
  // middle; a self message's beside its loop.
  const label: BoardPoint = self
    ? [points[1][0] + 6, (points[1][1] + points[2][1]) / 2]
    : edge.route === "message"
      ? [middle[0], middle[1] - 2]
      : [middle[0], middle[1] + 9];
  const last = points.length - 1;
  return (
    <>
      <BaseEdge
        id={props.id}
        path={boardPolylinePath(points)}
        style={lineStyle(edge, props.style)}
        markerStart={markerUrl(edge.startMarker)}
        markerEnd={markerUrl(edge.endMarker)}
        interactionWidth={16}
      />
      <EdgeLabels
        edge={edge}
        label={label}
        beside={self}
        ends={[
          [points[0], points[1]],
          [points[last], points[last - 1]],
        ]}
      />
    </>
  );
});

export const boardEdgeTypes = {
  boardFlow: BoardFlowEdgeView,
  boardRouted: BoardRoutedEdgeView,
};

/** A Board connector as React Flow draws it. */
export function toFlowEdge(edge: BoardEdge): BoardFlowEdge {
  return {
    id: edge.id,
    source: edge.sourceId,
    target: edge.targetId,
    type: edge.route && edge.route !== "flow" ? "boardRouted" : "boardFlow",
    data: { edge, revision: edge.revision },
  };
}
