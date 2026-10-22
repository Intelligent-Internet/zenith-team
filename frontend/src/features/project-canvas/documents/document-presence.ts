import * as Y from "yjs";
import { Awareness } from "y-protocols/awareness";
import { apiClient } from "@/lib/api-client";
import type { components } from "@/shared/api/generated/api.generated";
import { blockDocumentPath, type DocumentScope } from "./document-scope";
import { encodeUpdate, decodeUpdate } from "./document-update";

export function startDocumentPresence(options: {
  scope: DocumentScope;
  versionId: string;
  doc: Y.Doc;
  awareness: Awareness;
  enabled: () => boolean;
  onPeople: (people: components["schemas"]["CanvasSession"][]) => void;
}) {
  const { scope, doc, awareness } = options;
  const controller = new AbortController();
  const sessionId = crypto.randomUUID();
  let stopped = false;
  const presence = async () => {
    if (!("pageId" in scope) || !options.enabled() || stopped) return;
    const cursor = awareness.getLocalState()?.cursor;
    try {
      const result = await apiClient.put<
        components["schemas"]["CanvasPresenceList"]
      >(
        `${blockDocumentPath(scope)}/presence`,
        {
          sessionId,
          x: 0,
          y: 0,
          cameraX: 0,
          cameraY: 0,
          zoom: 1,
          pointing: false,
          presenting: false,
          leave: false,
          ...(cursor
            ? {
                cursor: {
                  clientId: doc.clientID,
                  anchor: encodeUpdate(
                    Y.encodeRelativePosition(
                      Y.createRelativePositionFromJSON(cursor.anchor),
                    ),
                  ),
                  head: encodeUpdate(
                    Y.encodeRelativePosition(
                      Y.createRelativePositionFromJSON(cursor.head),
                    ),
                  ),
                },
              }
            : {}),
        },
        { signal: controller.signal },
      );
      if (stopped) return;
      options.onPeople(
        result.items.filter((row) => row.sessionId !== sessionId),
      );
      const old = new Set(
        [...awareness.getStates().keys()].filter((id) => id !== doc.clientID),
      );
      const added: number[] = [],
        updated: number[] = [];
      for (const row of result.items) {
        if (
          !row.cursor ||
          row.sessionId === sessionId ||
          row.cursor.clientId === doc.clientID
        )
          continue;
        try {
          const id = row.cursor.clientId;
          (old.has(id) ? updated : added).push(id);
          old.delete(id);
          awareness.getStates().set(id, {
            user: { name: row.name, color: "#5689cf" },
            cursor: {
              anchor: Y.relativePositionToJSON(
                Y.decodeRelativePosition(decodeUpdate(row.cursor.anchor)),
              ),
              head: Y.relativePositionToJSON(
                Y.decodeRelativePosition(decodeUpdate(row.cursor.head)),
              ),
            },
          });
        } catch {
          /* A malformed peer cursor cannot interrupt document syncing. */
        }
      }
      for (const id of old) awareness.getStates().delete(id);
      const changes = { added, updated, removed: [...old] };
      awareness.emit("change", [changes, "zenith-server"]);
      awareness.emit("update", [changes, "zenith-server"]);
    } catch {
      /* Presence is ephemeral; it never changes saved status. */
    }
  };

  const timer = setInterval(() => void presence(), 1500);
  return () => {
    stopped = true;
    clearInterval(timer);
    controller.abort();
  };
}
