import { io, type Socket } from "socket.io-client";
import { useCallback, useEffect, useRef, useState } from "react";

const GRAPH_EVENTS = new Set([
  "node.created",
  "node.updated",
  "node.deleted",
  "node.status_changed",
  "node.media_derivatives_ready",
  "edge.created",
  "edge.deleted",
  "project.updated",
]);

type FactoryEvent = {
  event?: string;
  triggered_by?: string;
  data?: Record<string, unknown>;
};

export type StudioCursorUpdate = {
  x?: number;
  y?: number;
  visible?: boolean;
};

export type StudioCollaboratorCursor = {
  cursorId: string;
  userId: string;
  name?: string;
  avatar?: string;
  x: number;
  y: number;
  updatedAt: number;
};

function optionalText(value: unknown) {
  return typeof value === "string" ? value.trim() || undefined : undefined;
}

export function useStudioCollaboration({
  origin,
  accessToken,
  projectId,
  onGraphChanged,
  onAccessExpired,
}: {
  origin: string;
  accessToken: string;
  projectId: string;
  onGraphChanged: () => void;
  onAccessExpired: () => Promise<string>;
}) {
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState(false);
  const [cursors, setCursors] = useState<StudioCollaboratorCursor[]>([]);
  const socketRef = useRef<Socket | null>(null);
  const joinedRef = useRef(false);
  const callback = useRef(onGraphChanged);
  const renewAccess = useRef(onAccessExpired);
  renewAccess.current = onAccessExpired;
  const renewedAfterError = useRef(false);
  const recoveryIdentity = useRef("");
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );

  useEffect(() => {
    callback.current = onGraphChanged;
  }, [onGraphChanged]);

  const emitCursor = useCallback(
    (cursor: StudioCursorUpdate) => {
      const socket = socketRef.current;
      if (!socket?.connected || !joinedRef.current) return false;
      if (
        cursor.visible !== false &&
        (typeof cursor.x !== "number" ||
          typeof cursor.y !== "number" ||
          !Number.isFinite(cursor.x) ||
          !Number.isFinite(cursor.y))
      )
        return false;
      socket.emit("cursor_update", { project_id: projectId, ...cursor });
      return true;
    },
    [projectId],
  );

  useEffect(() => {
    setConnected(false);
    setError(false);
    setCursors([]);
    joinedRef.current = false;
    const identity = `${origin}:${projectId}`;
    if (recoveryIdentity.current !== identity) {
      recoveryIdentity.current = identity;
      renewedAfterError.current = false;
    }
    if (!origin || !accessToken || !projectId) return;
    const socket: Socket = io(`${origin.replace(/\/+$/, "")}/factory`, {
      auth: { token: accessToken },
      transports: ["websocket"],
      reconnection: true,
      reconnectionAttempts: 5,
      reconnectionDelay: 1000,
    });
    socketRef.current = socket;
    let stopped = false;
    let renewing = false;

    const handleConnect = () => {
      setError(false);
      setConnected(false);
      setCursors([]);
      joinedRef.current = false;
      socket.emit("join_project", { project_id: projectId });
    };
    const handleJoined = (payload: { project_id?: string }) => {
      if (payload.project_id === projectId) {
        renewedAfterError.current = false;
        joinedRef.current = true;
        setConnected(true);
        setError(false);
        callback.current();
      }
    };
    const handleDisconnect = () => {
      joinedRef.current = false;
      setConnected(false);
      setCursors([]);
    };
    const handleError = (reason?: { message?: string } | Error | string) => {
      joinedRef.current = false;
      setConnected(false);
      setCursors([]);
      setError(true);
      const message = typeof reason === "string" ? reason : reason?.message;
      const authenticationRejected =
        message === "Connection rejected by server" ||
        Boolean(
          message &&
          /(token|unauthenticated|authentication|expired)/i.test(message),
        );
      if (!authenticationRejected || renewing || renewedAfterError.current)
        return;
      renewing = true;
      renewedAfterError.current = true;
      void renewAccess
        .current()
        .then((token) => {
          if (stopped) return;
          socket.auth = { token };
          socket.disconnect().connect();
        })
        .catch(() => {
          // The editor reports a renewal failure while keeping its canvas mounted.
        })
        .finally(() => {
          renewing = false;
        });
    };
    const handleFactoryEvent = (payload: FactoryEvent) => {
      const data = payload.data;
      if (payload.event === "presence.cursor_left") {
        const cursorId = optionalText(data?.client_id);
        if (cursorId)
          setCursors((current) =>
            current.filter((cursor) => cursor.cursorId !== cursorId),
          );
        return;
      }
      if (payload.event === "presence.cursor") {
        const cursorId = optionalText(data?.client_id);
        const userId =
          optionalText(data?.user_id) ?? optionalText(payload.triggered_by);
        const x = data?.x;
        const y = data?.y;
        if (
          !cursorId ||
          cursorId === socket.id ||
          !userId ||
          typeof x !== "number" ||
          typeof y !== "number" ||
          !Number.isFinite(x) ||
          !Number.isFinite(y)
        )
          return;
        const cursor: StudioCollaboratorCursor = {
          cursorId,
          userId,
          name: optionalText(data?.name),
          avatar: optionalText(data?.avatar),
          x,
          y,
          updatedAt: Date.now(),
        };
        setCursors((current) =>
          [
            cursor,
            ...current.filter((item) => item.cursorId !== cursorId),
          ].slice(0, 32),
        );
        return;
      }
      if (!payload.event || !GRAPH_EVENTS.has(payload.event)) return;
      clearTimeout(refreshTimer.current);
      refreshTimer.current = setTimeout(() => callback.current(), 80);
    };

    socket.on("connect", handleConnect);
    socket.on("joined_project", handleJoined);
    socket.on("disconnect", handleDisconnect);
    socket.on("connect_error", handleError);
    socket.on("error", handleError);
    socket.on("factory_event", handleFactoryEvent);
    socket.io.on("reconnect_failed", handleError);
    if (socket.connected) handleConnect();

    // A missed leave frame must not leave a permanent cursor on the canvas.
    const staleTimer = setInterval(() => {
      const oldest = Date.now() - 20_000;
      setCursors((current) =>
        current.some((cursor) => cursor.updatedAt < oldest)
          ? current.filter((cursor) => cursor.updatedAt >= oldest)
          : current,
      );
    }, 5_000);

    return () => {
      stopped = true;
      clearTimeout(refreshTimer.current);
      clearInterval(staleTimer);
      socket.off("connect", handleConnect);
      socket.off("joined_project", handleJoined);
      socket.off("disconnect", handleDisconnect);
      socket.off("connect_error", handleError);
      socket.off("error", handleError);
      socket.off("factory_event", handleFactoryEvent);
      socket.io.off("reconnect_failed", handleError);
      if (socket.connected) {
        socket.emit("cursor_update", {
          project_id: projectId,
          visible: false,
        });
        socket.emit("leave_project", { project_id: projectId });
      }
      socket.disconnect();
      if (socketRef.current === socket) socketRef.current = null;
      joinedRef.current = false;
      setConnected(false);
      setCursors([]);
    };
  }, [accessToken, origin, projectId]);

  return { connected, error, cursors, emitCursor };
}
