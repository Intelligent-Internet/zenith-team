import { useEffect, useRef, type RefObject } from "react";

export type PresencePointer = { x: number; y: number; pointing: boolean };

// One presence loop for canvas surfaces: it tracks the pointer in canvas
// coordinates, sends at most one request at a time, sends again within
// `intervalMs` once something changed, and otherwise heartbeats every
// `idleMs`. Hidden tabs stay quiet and expire; unmounting says goodbye, and
// with `leaveWhenHidden` so does hiding or closing the page.
export function usePresenceHeartbeat({
  surface,
  room,
  toCanvas,
  intervalMs,
  idleMs,
  changed,
  send,
  leave,
  leaveWhenHidden = false,
}: {
  surface: RefObject<HTMLElement | null>;
  /** Restarts the loop when the presence room changes. */
  room: string;
  toCanvas: (point: { x: number; y: number }) => { x: number; y: number };
  intervalMs: number;
  idleMs: number;
  /** Reports state other than the pointer that should be sent promptly. */
  changed?: () => boolean;
  send: (pointer: PresencePointer, signal: AbortSignal) => Promise<void>;
  /** `keepalive` is set when the page may be unloading. */
  leave: (options?: { keepalive: boolean }) => void;
  leaveWhenHidden?: boolean;
}) {
  const latest = useRef({ toCanvas, changed, send, leave });
  latest.current = { toCanvas, changed, send, leave };
  useEffect(() => {
    const controller = new AbortController();
    let busy = false,
      stopped = false,
      moved = true,
      lastSent = 0;
    let pointer: PresencePointer = { x: 0, y: 0, pointing: false };
    const element = surface.current;
    const move = (event: PointerEvent) => {
      const next = latest.current.toCanvas({
        x: event.clientX,
        y: event.clientY,
      });
      if (
        pointer.pointing &&
        Math.abs(next.x - pointer.x) < 1 &&
        Math.abs(next.y - pointer.y) < 1
      )
        return;
      pointer = { ...next, pointing: true };
      moved = true;
    };
    const away = () => {
      pointer = { ...pointer, pointing: false };
      moved = true;
    };
    element?.addEventListener("pointermove", move);
    element?.addEventListener("pointerleave", away);
    const tick = async () => {
      if (busy || stopped || document.hidden) return;
      const dirty = moved || !!latest.current.changed?.();
      if (!dirty && Date.now() - lastSent < idleMs) return;
      busy = true;
      moved = false;
      lastSent = Date.now();
      try {
        await latest.current.send(pointer, controller.signal);
      } catch {
        // `send` reports its own failures; the next tick retries.
      } finally {
        busy = false;
      }
    };
    let left = false;
    const hide = () => {
      if (left) return;
      left = true;
      latest.current.leave({ keepalive: true });
    };
    const visibility = () => {
      if (document.hidden) hide();
      else if (left) {
        // Back in view: announce this session again right away.
        left = false;
        moved = true;
        void tick();
      }
    };
    if (leaveWhenHidden) {
      window.addEventListener("pagehide", hide);
      document.addEventListener("visibilitychange", visibility);
    }
    void tick();
    const timer = setInterval(() => void tick(), intervalMs);
    return () => {
      stopped = true;
      controller.abort();
      clearInterval(timer);
      element?.removeEventListener("pointermove", move);
      element?.removeEventListener("pointerleave", away);
      window.removeEventListener("pagehide", hide);
      document.removeEventListener("visibilitychange", visibility);
      latest.current.leave();
    };
  }, [room, surface, intervalMs, idleMs, leaveWhenHidden]);
}
