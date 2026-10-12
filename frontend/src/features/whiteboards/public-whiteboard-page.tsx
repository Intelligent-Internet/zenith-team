import { useCallback, useEffect, useMemo, useState } from "react";
import { useParams } from "react-router";
import {
  Background,
  BackgroundVariant,
  ReactFlow,
  ReactFlowProvider,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { LockKeyhole } from "@/shared/ui/icons";
import { BoardZoomControls } from "./board-toolbar";
import { Button } from "@/shared/ui/button";
import {
  BoardNode,
  BoardNodeContext,
  type WhiteboardFlowNode,
} from "./board-node";
import {
  whiteboardApi,
  publicBoardPath,
  type BoardHydratedItem,
} from "./board-api";
import { BoardHydration } from "./board-hydration";
import { BoardEdgeMarkers, boardEdgeTypes, toFlowEdge } from "./board-edge";
import { boardHandles } from "./board-handles";
import { isHollowShape } from "./board-shape";
import "./whiteboards.css";
const nodeTypes = { whiteboard: BoardNode };
export function PublicWhiteboardPage() {
  const { token = "" } = useParams();
  return (
    <ReactFlowProvider>
      <PublicBoard token={token} />
    </ReactFlowProvider>
  );
}
function PublicBoard({ token }: { token: string }) {
  // Each poll reads the whole snapshot, so a hidden tab does not poll and a
  // visible one refreshes every few seconds; focus refreshes immediately.
  const query = whiteboardApi.usePublicWhiteboardQuery(token, {
    skip: !token,
    pollingInterval: 15000,
    skipPollingIfUnfocused: true,
    refetchOnFocus: true,
    refetchOnReconnect: true,
  });
  const snapshot = query.currentData;
  // Visible notes and strokes share batched hydration requests, and ink
  // already seen is reused when panning back, as in the editor.
  const [hydration, setHydration] = useState<BoardHydration | null>(null);
  useEffect(() => {
    const created = new BoardHydration(publicBoardPath(token));
    setHydration(created);
    return () => created.dispose();
  }, [token]);
  const hydrate = useCallback(
    (id: string): Promise<BoardHydratedItem> =>
      hydration
        ? hydration.get(id)
        : Promise.reject(new Error("Whiteboard content is unavailable")),
    [hydration],
  );
  const context = useMemo(
    () => ({
      scope: {
        companyId: snapshot?.board.companyId ?? "",
        boardId: snapshot?.board.id ?? "",
      },
      publicToken: token,
      editable: false,
      hydrate,
      commit: () => {},
    }),
    [token, snapshot?.board.companyId, snapshot?.board.id, hydrate],
  );
  const nodes = useMemo<WhiteboardFlowNode[]>(
    () =>
      (snapshot?.items ?? []).map((item) => ({
        id: item.id,
        type: "whiteboard",
        position: { x: item.x, y: item.y },
        data: { item },
        width: item.width,
        height: item.height,
        measured: { width: item.width, height: item.height },
        style: isHollowShape(item.properties.shape)
          ? { width: item.width, height: item.height, pointerEvents: "none" }
          : { width: item.width, height: item.height },
        draggable: false,
        connectable: false,
        zIndex:
          item.type === "frame" || item.properties.shape === "fragment"
            ? -10
            : item.stackOrder,
        handles: boardHandles(item, item.width, item.height),
      })),
    [snapshot?.items],
  );
  const edges = useMemo(
    () => (snapshot?.edges ?? []).map(toFlowEdge),
    [snapshot?.edges],
  );
  if (query.isLoading)
    return (
      <div className="wb-message wb-public-message" role="status">
        Opening whiteboard…
      </div>
    );
  if (query.isError || !snapshot)
    return (
      <div className="wb-message wb-public-message" role="alert">
        <h2>This whiteboard link is unavailable</h2>
        <p>Ask the owner for access, or try again.</p>
        <Button variant="outline" onClick={() => void query.refetch()}>
          Try again
        </Button>
      </div>
    );
  return (
    <BoardNodeContext.Provider value={context}>
      <section
        className="wb-editor wb-public-editor"
        aria-label={snapshot.board.title}
      >
        <header className="wb-editor-header">
          <div className="wb-board-heading">
            <h1 title={snapshot.board.title}>{snapshot.board.title}</h1>
          </div>
          <span className="wb-public-access">
            <LockKeyhole size={15} />
            View only
          </span>
        </header>
        <div className="wb-editor-body">
          <div className="wb-surface">
            <BoardEdgeMarkers />
            <ReactFlow
              proOptions={{ hideAttribution: true }}
              onlyRenderVisibleElements
              nodes={nodes}
              edges={edges}
              nodeTypes={nodeTypes}
              edgeTypes={boardEdgeTypes}
              nodesDraggable={false}
              nodesConnectable={false}
              elevateNodesOnSelect={false}
              edgesReconnectable={false}
              deleteKeyCode={null}
              fitView
              fitViewOptions={{ padding: 0.2, maxZoom: 1 }}
              minZoom={0.01}
              maxZoom={8}
            >
              <Background variant={BackgroundVariant.Dots} gap={24} />
              <BoardZoomControls />
            </ReactFlow>
          </div>
        </div>
      </section>
    </BoardNodeContext.Provider>
  );
}
