import { useEffect, useRef, useState, type RefObject } from "react";
import { useReactFlow, useViewport, ViewportPortal } from "@xyflow/react";
import { Cursor, X } from "@/shared/ui/icons";
import { apiClient } from "@/lib/api-client";
import type { components } from "@/shared/api/generated/api.generated";
import { usePresenceHeartbeat } from "@/shared/canvas/use-presence-heartbeat";
import { canvasPath, type CanvasScope } from "../api/canvas-api";
type Session = components["schemas"]["CanvasSession"];
type PresenceInput = components["schemas"]["CanvasPresenceInput"];
const colors = [
  "var(--workspace-native-accent)",
  "var(--workspace-native-info)",
  "var(--workspace-native-success)",
  "var(--workspace-native-danger)",
];
export function CanvasPresence({
  scope,
  surface,
  presenting,
}: {
  scope: CanvasScope;
  surface: RefObject<HTMLDivElement | null>;
  presenting: boolean;
}) {
  const flow = useReactFlow();
  const viewport = useViewport();
  const latest = useRef({ viewport, presenting });
  latest.current = { viewport, presenting };
  const [sessions, setSessions] = useState<Session[]>([]);
  const [following, setFollowing] = useState<string | null>(null);
  const [connected, setConnected] = useState(true);
  const [sessionId] = useState(() => crypto.randomUUID());
  usePresenceHeartbeat({
    surface,
    room: `${scope.companyId}:${scope.projectId}:${sessionId}`,
    toCanvas: (point) => flow.screenToFlowPosition(point),
    intervalMs: 750,
    idleMs: 750,
    send: async (point, signal) => {
      const { viewport: camera, presenting } = latest.current;
      const input: PresenceInput = {
        sessionId,
        ...point,
        cameraX: camera.x,
        cameraY: camera.y,
        zoom: camera.zoom,
        presenting,
      };
      try {
        const result = await apiClient.put<
          components["schemas"]["CanvasPresenceList"]
        >(`${canvasPath(scope)}/presence`, input, { signal });
        setSessions(
          result.items
            .filter((row) => row.sessionId !== sessionId)
            .sort((a, b) => a.sessionId.localeCompare(b.sessionId)),
        );
        setConnected(true);
      } catch {
        if (signal.aborted) return;
        setConnected(false);
        setSessions([]);
      }
    },
    leave: () =>
      void apiClient
        .put(`${canvasPath(scope)}/presence`, {
          sessionId,
          x: 0,
          y: 0,
          cameraX: 0,
          cameraY: 0,
          zoom: 1,
          pointing: false,
          presenting: false,
          leave: true,
        })
        .catch(() => {}),
  });
  useEffect(() => {
    const leader = sessions.find((row) => row.sessionId === following);
    if (leader)
      void flow.setViewport(
        { x: leader.cameraX, y: leader.cameraY, zoom: leader.zoom },
        { duration: 250 },
      );
    else if (following) setFollowing(null);
  }, [sessions, following, flow]);
  return (
    <>
      {(!connected || sessions.length > 0) && (
        <div className="pc-presence">
          <span
            className={`pc-live-indicator ${connected ? "" : "is-offline"}`}
          />
          <span>{connected ? "Live" : "Reconnecting"}</span>
          <div className="pc-avatar-group flex ml-[5px]">
            {sessions.slice(0, 5).map((person, index) => (
              <button
                key={person.sessionId}
                title={`Follow ${person.name}${person.presenting ? " · presenting" : ""}`}
                aria-label={`Follow ${person.name}`}
                className={following === person.sessionId ? "is-following" : ""}
                onClick={() =>
                  setFollowing(
                    following === person.sessionId ? null : person.sessionId,
                  )
                }
                style={{ color: colors[index % colors.length] }}
              >
                {person.name
                  .split(/\s/)
                  .map((word) => word[0])
                  .join("")
                  .slice(0, 2)}
              </button>
            ))}
          </div>
          {sessions.length > 5 && <span>+{sessions.length - 5}</span>}
        </div>
      )}
      {following && (
        <button
          className="absolute top-[75px] right-[20px] z-[5] flex items-center gap-[9px] p-[7px_11px] text-[length:11px] [background:var(--workspace-selected-bg)] text-[var(--pc-accent)] [border:1px_solid_var(--pc-line)] rounded-[7px]"
          onClick={() => setFollowing(null)}
        >
          Following {sessions.find((row) => row.sessionId === following)?.name}
          <X size={14} />
        </button>
      )}
      <ViewportPortal>
        {sessions
          .filter((row) => row.pointing)
          .slice(0, 30)
          .map((person, index) => (
            <div
              key={person.sessionId}
              className="pc-cursor absolute [transform-origin:top_left] flex flex-col items-start pointer-events-none [transition:transform_220ms_linear] z-[20]"
              style={{
                transform: `translate(${person.x}px, ${person.y}px) scale(${1 / viewport.zoom})`,
                color: colors[index % colors.length],
              }}
            >
              <Cursor size={20} />
              <span>{person.name}</span>
            </div>
          ))}
      </ViewportPortal>
    </>
  );
}
