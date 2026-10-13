import * as Y from "yjs";
import type { components } from "@/shared/api/generated/api.generated";
import {
  readDocumentCache,
  writeDocumentCache,
  openDocumentRecovery,
} from "./document-cache";

type Document = components["schemas"]["CanvasDocument"];

// Cache mechanics and recovery ownership stay together; the session chooses
// which server snapshot and pending updates to persist and when to release them.
export function documentRecovery(options: {
  userId: string | undefined;
  scopeKey: string;
  versionId: string;
  signal: AbortSignal;
  onError: (message: string) => void;
}) {
  const { userId, scopeKey, versionId, signal, onError } = options;
  const cacheKey = `${userId}:${scopeKey}:live:${versionId}`;
  let lease: Awaited<ReturnType<typeof openDocumentRecovery>>;
  return {
    cacheKey,
    async read() {
      if (!userId) return;
      const entry = await readDocumentCache<Document | { pageKey: string }>(
        cacheKey,
      );
      return entry && "pageKey" in entry
        ? readDocumentCache<Document>(entry.pageKey)
        : entry;
    },
    async open(base: Document) {
      if (!userId) return;
      try {
        lease = await openDocumentRecovery(
          `${userId}:${scopeKey}:${base.pageId}`,
          base,
          signal,
        );
        return lease?.recovered;
      } catch {
        onError(
          "Local recovery is unavailable. Keep unsaved work open or export it before leaving.",
        );
      }
    },
    cache(value: Document) {
      if (!userId) return;
      const pageKey = `${userId}:${scopeKey}:page:${value.pageId}`;
      void (async () => {
        await writeDocumentCache(pageKey, value);
        await writeDocumentCache(cacheKey, { pageKey });
      })().catch(() => {});
    },
    async save(
      snapshot: Document | undefined,
      updates: Uint8Array[],
      release = false,
    ) {
      if (!snapshot || !lease) return;
      await lease.save(
        snapshot,
        updates.length ? Y.mergeUpdates(updates) : new Uint8Array(),
        release,
      );
    },
  };
}
