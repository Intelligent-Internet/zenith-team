import { useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { useReactFlow, useViewport, ViewportPortal } from "@xyflow/react";
import { createPortal } from "react-dom";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import * as Popover from "@radix-ui/react-popover";
import { personInitials } from "@/shared/ui/person-initials";
import { ArrowRight, MousePointer } from "@/shared/ui/icons";
import { apiClient, getApiErrorCode } from "@/lib/api-client";
import type { components } from "@/shared/api/generated/api.generated";
import { boardPath, type BoardScope, type BoardItem } from "./board-api";
import { boardlyRealtimeClient } from "@/shared/realtime/realtime-client.service";
import { usePresenceHeartbeat } from "@/shared/canvas/use-presence-heartbeat";
type Participant = components["schemas"]["BoardParticipant"];
type Preview = components["schemas"]["BoardDragPreview"];
type Rect = components["schemas"]["BoardRect"];
function ParticipantAvatar({ participant }: { participant: Participant }) {
  return (
    <span
      className={`wb-avatar${participant.actor.kind === "agent" ? " wb-agent-avatar" : ""}`}
    >
      {participant.actor.avatarUrl ? (
        <img src={participant.actor.avatarUrl} alt="" />
      ) : (
        personInitials(participant.name)
      )}
    </span>
  );
}
const peerColors = ["#7c3aed", "#0284c7", "#c2410c", "#059669"];
// A participant keeps one color however the list reorders.
function peerColor(id: string) {
  let hash = 0;
  for (const char of id) hash = (hash * 31 + char.charCodeAt(0)) | 0;
  return peerColors[Math.abs(hash) % peerColors.length];
}
const sameView = (a?: Rect, b?: Rect) =>
  a === b ||
  (!!a &&
    !!b &&
    Math.round(a.x) === Math.round(b.x) &&
    Math.round(a.y) === Math.round(b.y) &&
    Math.round(a.width) === Math.round(b.width) &&
    Math.round(a.height) === Math.round(b.height));
const clamp = (value: number, low: number, high: number) =>
  Math.min(high, Math.max(low, value));
const center = (rect: Rect) => ({
  x: rect.x + rect.width / 2,
  y: rect.y + rect.height / 2,
});
const reducedMotion = () =>
  window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
/** One stop of an Agent's cursor while it shows what it just placed. */
type TourStop = {
  id: string;
  x: number;
  y: number;
  width: number;
  height: number;
};
const sameIds = (a: string[], b: string[]) =>
  a.length === b.length && a.every((id, index) => id === b[index]);
const samePreviews = (a: Preview[], b: Preview[]) =>
  a.length === b.length &&
  a.every(
    (preview, index) =>
      preview.id === b[index].id &&
      Math.round(preview.x) === Math.round(b[index].x) &&
      Math.round(preview.y) === Math.round(b[index].y),
  );
export function BoardPresence({
  scope,
  headerTarget,
  surface,
  selection,
  dragPreviews,
  items,
}: {
  scope: BoardScope;
  headerTarget: HTMLElement | null;
  surface: RefObject<HTMLDivElement | null>;
  selection: string[];
  /** Read on each heartbeat; dragging updates it without re-rendering. */
  dragPreviews: RefObject<Preview[]>;
  items: BoardItem[];
}) {
  const flow = useReactFlow(),
    viewport = useViewport();
  const [sessionId] = useState(() => crypto.randomUUID());
  const [participants, setParticipants] = useState<Participant[]>([]);
  const latest = useRef({ viewport, selection });
  latest.current = { viewport, selection };
  const sent = useRef<{
    selection: string[];
    previews: Preview[];
    view?: Rect;
    focused: boolean;
  }>({ selection: [], previews: [], focused: false });
  // The Board area this browser shows, in Board units: an Agent working for
  // this person places its work there.
  const viewOf = (): Rect | undefined => {
    const element = surface.current,
      { x, y, zoom } = latest.current.viewport;
    if (!element?.clientWidth || !element.clientHeight) return undefined;
    return {
      x: clamp(-x / zoom, -1e6, 1e6),
      y: clamp(-y / zoom, -1e6, 1e6),
      width: Math.min(element.clientWidth / zoom, 2e6),
      height: Math.min(element.clientHeight / zoom, 2e6),
    };
  };
  // A full presence room is not an error to repeat every few hundred
  // milliseconds; this session waits and shows no participants meanwhile.
  const fullUntil = useRef(0);
  usePresenceHeartbeat({
    surface,
    room: `${scope.companyId}:${scope.boardId}:${sessionId}`,
    toCanvas: (point) => flow.screenToFlowPosition(point),
    intervalMs: 200,
    idleMs: 5000,
    leaveWhenHidden: true,
    changed: () =>
      !sameIds(latest.current.selection, sent.current.selection) ||
      !samePreviews(dragPreviews.current ?? [], sent.current.previews) ||
      !sameView(viewOf(), sent.current.view) ||
      document.hasFocus() !== sent.current.focused,
    send: async (pointer, signal) => {
      if (Date.now() < fullUntil.current) return;
      const current = latest.current;
      const previews = dragPreviews.current ?? [];
      const view = viewOf(),
        focused = document.hasFocus();
      sent.current = { selection: current.selection, previews, view, focused };
      try {
        const response = await apiClient.put<
          components["schemas"]["BoardPresence"]
        >(
          `${boardPath(scope)}/presence`,
          {
            sessionId,
            ...pointer,
            cameraX: current.viewport.x,
            cameraY: current.viewport.y,
            zoom: current.viewport.zoom,
            selectionIds: current.selection.slice(0, 100),
            dragPreviews: previews.slice(0, 100),
            ...(view ? { view } : {}),
            focused,
          },
          { signal },
        );
        setParticipants((previous) =>
          response.sessions
            .filter((row) => row.sessionId !== sessionId)
            .map((row) => {
              const newer = previous.find(
                (peer) =>
                  peer.sessionId === row.sessionId &&
                  peer.updatedAt > row.updatedAt,
              );
              return newer ?? row;
            }),
        );
      } catch (error) {
        if (signal.aborted) return;
        if (getApiErrorCode(error) === "whiteboard_presence_full")
          fullUntil.current = Date.now() + 15000;
        setParticipants([]);
      }
    },
    // While the page unloads, only a keepalive request still arrives. It
    // goes through the API client, so it carries the same credentials.
    leave: (options) =>
      void apiClient
        .put(
          `${boardPath(scope)}/presence`,
          {
            sessionId,
            x: 0,
            y: 0,
            zoom: 1,
            pointing: false,
            selectionIds: [],
            dragPreviews: [],
            leave: true,
          },
          options?.keepalive
            ? { adapter: "fetch", fetchOptions: { keepalive: true } }
            : undefined,
        )
        .catch(() => {}),
  });
  useEffect(
    () =>
      boardlyRealtimeClient.onEvent((event) => {
        if (
          event.companyId !== scope.companyId ||
          !event.type.startsWith("whiteboard.presence") ||
          !("boardId" in event.payload) ||
          event.payload.boardId !== scope.boardId
        )
          return;
        const peer = event.payload.session as Participant | undefined;
        if (
          !peer ||
          typeof peer.sessionId !== "string" ||
          peer.sessionId === sessionId ||
          typeof peer.name !== "string" ||
          !peer.actor ||
          (peer.actor.kind !== "user" && peer.actor.kind !== "agent") ||
          !Number.isFinite(peer.x) ||
          !Number.isFinite(peer.y)
        )
          return;
        setParticipants((previous) => {
          const current = previous.find(
            (row) => row.sessionId === peer.sessionId,
          );
          if (current && current.updatedAt > peer.updatedAt) return previous;
          const others = previous.filter(
            (row) => row.sessionId !== peer.sessionId,
          );
          return event.payload.leave ? others : [...others, peer];
        });
      }),
    [scope.companyId, scope.boardId, sessionId],
  );
  const itemsById = useMemo(
    () => new Map(items.map((item) => [item.id, item])),
    [items],
  );
  // When an Agent's commit arrives, its cursor visits what it placed, one
  // object after another, the way a person would put them down. Purely
  // local: the objects are already saved.
  const [tours, setTours] = useState<
    Record<string, { stops: TourStop[]; step: number }>
  >({});
  const known = useRef<Set<string> | null>(null);
  const agentsRef = useRef(participants);
  agentsRef.current = participants;
  useEffect(() => {
    const previous = known.current;
    known.current = new Set(items.map((item) => item.id));
    if (!previous || reducedMotion()) return;
    const placed = new Map<string, TourStop[]>();
    for (const item of items) {
      if (previous.has(item.id) || !item.createdByRunId) continue;
      const agent = agentsRef.current.find(
        (peer) => peer.runId === item.createdByRunId,
      );
      if (!agent) continue;
      const stops = placed.get(agent.sessionId) ?? [];
      stops.push({
        id: item.id,
        x: item.x,
        y: item.y,
        width: item.width,
        height: item.height,
      });
      placed.set(agent.sessionId, stops);
    }
    if (!placed.size) return;
    setTours((current) => {
      const next = { ...current };
      for (const [session, stops] of placed)
        next[session] = {
          stops: stops.sort((a, b) => a.y - b.y || a.x - b.x),
          step: 0,
        };
      return next;
    });
  }, [items]);
  useEffect(() => {
    const longest = Math.max(
      0,
      ...Object.values(tours).map((t) => t.stops.length),
    );
    if (!longest) return;
    // Long tours speed up so none lasts much more than 1.5 seconds.
    const timer = setTimeout(
      () =>
        setTours((current) => {
          const next: typeof current = {};
          for (const [session, tour] of Object.entries(current))
            if (tour.step + 1 < tour.stops.length)
              next[session] = { ...tour, step: tour.step + 1 };
          return next;
        }),
      Math.max(60, Math.min(160, 1500 / longest)),
    );
    return () => clearTimeout(timer);
  }, [tours]);
  const cursorOf = (person: Participant) => {
    const tour = tours[person.sessionId],
      stop = tour?.stops[tour.step];
    return stop ? center(stop) : { x: person.x, y: person.y };
  };
  const view = viewOf();
  const userId = useSessionSnapshot().userId;
  const people = [
    ...new Map(
      participants
        .filter((p) => p.actor.kind !== "user" || p.actor.id !== userId)
        .map((p) => [`${p.actor.kind}:${p.actor.id}`, p]),
    ).values(),
  ];
  return (
    <>
      {headerTarget &&
        people.length > 0 &&
        createPortal(
          <Popover.Root>
            <Popover.Trigger asChild>
              <button
                className="wb-participants"
                aria-label={`On this whiteboard: ${people.map((person) => person.name).join(", ")}`}
                title="On this whiteboard"
              >
                <span className="wb-presence-avatars" aria-hidden="true">
                  {people.slice(0, 3).map((person) => (
                    <ParticipantAvatar
                      key={`${person.actor.kind}:${person.actor.id}`}
                      participant={person}
                    />
                  ))}
                </span>
                <span className="wb-presence-name" aria-live="polite">
                  {people[0].name}
                </span>
                {people.length > 1 && <span>+{people.length - 1}</span>}
              </button>
            </Popover.Trigger>
            <Popover.Portal>
              <Popover.Content
                align="end"
                sideOffset={8}
                collisionPadding={12}
                className="wb-presence-popover"
              >
                <h3>On this whiteboard</h3>
                {people.map((person) => (
                  <p key={`${person.actor.kind}:${person.actor.id}`}>
                    <ParticipantAvatar participant={person} />
                    <span>
                      {person.name}
                      {person.actor.kind === "agent" && <small>Agent</small>}
                    </span>
                  </p>
                ))}
              </Popover.Content>
            </Popover.Portal>
          </Popover.Root>,
          headerTarget,
        )}
      <ViewportPortal>
        {participants.map((person) => (
          <div
            key={person.sessionId}
            style={
              {
                "--wb-peer": peerColor(
                  `${person.actor.kind}:${person.actor.id}`,
                ),
              } as React.CSSProperties
            }
          >
            {person.runId && person.workArea && (
              <div
                className="wb-agent-work-area"
                style={{
                  left: person.workArea.x,
                  top: person.workArea.y,
                  width: person.workArea.width,
                  height: person.workArea.height,
                  borderWidth: 2 / viewport.zoom,
                }}
              />
            )}
            {person.runId ? (
              // An Agent's cursor glides between the places it works; the
              // inner element keeps it screen-sized at any zoom.
              <div
                className="wb-peer-cursor wb-peer-cursor--agent"
                style={{
                  transform: `translate(${cursorOf(person).x}px,${cursorOf(person).y}px)`,
                }}
              >
                <div
                  className="wb-peer-cursor__scale"
                  style={{ transform: `scale(${1 / viewport.zoom})` }}
                >
                  <MousePointer className="wb-peer-cursor__icon" size={22} />
                  <span className="wb-peer-cursor__label">
                    {person.name} · Agent
                  </span>
                </div>
              </div>
            ) : (
              person.pointing && (
                <div
                  className="wb-peer-cursor"
                  style={{
                    transform: `translate(${person.x}px,${person.y}px) scale(${1 / viewport.zoom})`,
                  }}
                >
                  <MousePointer className="wb-peer-cursor__icon" size={22} />
                  <span className="wb-peer-cursor__label">{person.name}</span>
                </div>
              )
            )}
            {(tours[person.sessionId]?.stops ?? [])
              .slice(0, (tours[person.sessionId]?.step ?? 0) + 1)
              .map((stop) => (
                <div
                  key={stop.id}
                  className="wb-agent-placed"
                  style={{
                    left: stop.x,
                    top: stop.y,
                    width: stop.width,
                    height: stop.height,
                  }}
                />
              ))}
            {(person.selectionIds ?? []).map((id) => {
              const item = itemsById.get(id),
                drag = (person.dragPreviews ?? []).find(
                  (preview) => preview.id === id,
                );
              return item ? (
                <div
                  key={id}
                  className="wb-peer-selection"
                  style={{
                    left: drag?.x ?? item.x,
                    top: drag?.y ?? item.y,
                    width: item.width,
                    height: item.height,
                  }}
                />
              ) : null;
            })}
          </div>
        ))}
      </ViewportPortal>
      {view &&
        participants
          .filter((person) => person.runId)
          .map((person) => {
            const target = person.workArea
              ? center(person.workArea)
              : cursorOf(person);
            if (
              target.x >= view.x &&
              target.x <= view.x + view.width &&
              target.y >= view.y &&
              target.y <= view.y + view.height
            )
              return null;
            // Point from the visible area's center towards the work.
            const middle = center(view),
              angle = Math.atan2(target.y - middle.y, target.x - middle.x);
            const width = view.width * viewport.zoom,
              height = view.height * viewport.zoom;
            return (
              <button
                key={person.sessionId}
                type="button"
                className="wb-agent-offscreen"
                style={
                  {
                    left: clamp(
                      (target.x - view.x) * viewport.zoom,
                      96,
                      width - 96,
                    ),
                    top: clamp(
                      (target.y - view.y) * viewport.zoom,
                      28,
                      height - 28,
                    ),
                    "--wb-peer": peerColor(
                      `${person.actor.kind}:${person.actor.id}`,
                    ),
                  } as React.CSSProperties
                }
                onClick={() =>
                  void flow.setCenter(target.x, target.y, {
                    zoom: viewport.zoom,
                    duration: reducedMotion() ? 0 : 400,
                  })
                }
              >
                {person.name} is working here
                <ArrowRight
                  size={14}
                  aria-hidden="true"
                  style={{ transform: `rotate(${angle}rad)` }}
                />
              </button>
            );
          })}
    </>
  );
}
