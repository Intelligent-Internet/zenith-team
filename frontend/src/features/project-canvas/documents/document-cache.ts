import type { components } from "@/shared/api/generated/api.generated";
import { readSessionSnapshot } from "@/shared/session/session";

type Document = components["schemas"]["CanvasDocument"];
type CacheRow = { key: string; value: unknown; bytes: number; touched: number };
type Recovery = {
  key: string;
  documentKey: string;
  owner: string;
  leaseUntil: number;
  snapshot: Document;
  update: Uint8Array;
  touched: number;
};

let database: Promise<IDBDatabase> | undefined;
let epoch = 0;
let lastPruned = 0;
const memoryRecoveries = new Map<string, Recovery>();
const currentKey = (key: string) => {
  const userId = readSessionSnapshot().userId;
  return !!userId && key.startsWith(`${userId}:`);
};

function open() {
  if (!database) {
    database = new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("zenith-document-cache", 1);
      request.onupgradeneeded = () => {
        request.result.createObjectStore("snapshots", { keyPath: "key" });
        request.result.createObjectStore("recoveries", { keyPath: "key" });
      };
      request.onsuccess = () => {
        const db = request.result;
        db.onversionchange = () => {
          db.close();
          database = undefined;
        };
        resolve(db);
      };
      request.onerror = () => {
        database = undefined;
        reject(request.error);
      };
      request.onblocked = () => {
        database = undefined;
        reject(new Error("Local document storage is unavailable"));
      };
    });
  }
  return database;
}

function result<T>(request: IDBRequest<T>) {
  return new Promise<T>((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function completed(tx: IDBTransaction) {
  return new Promise<void>((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onabort = tx.onerror = () =>
      reject(tx.error ?? new Error("Local document write failed"));
  });
}

export async function readDocumentCache<T>(
  key: string,
): Promise<T | undefined> {
  if (!currentKey(key)) return undefined;
  const generation = epoch;
  try {
    const db = await open();
    const row = (await result(
      db.transaction("snapshots").objectStore("snapshots").get(key),
    )) as CacheRow | undefined;
    return generation === epoch && currentKey(key)
      ? (row?.value as T | undefined)
      : undefined;
  } catch {
    return undefined;
  }
}

export async function writeDocumentCache(key: string, value: unknown) {
  const generation = epoch;
  const db = await open();
  if (generation !== epoch || !currentKey(key)) return;
  const tx = db.transaction("snapshots", "readwrite");
  const done = completed(tx);
  const store = tx.objectStore("snapshots");
  const bytes = JSON.stringify(value).length * 2;
  const request = store.get(key);
  request.onsuccess = () => {
    const previous = (request.result as CacheRow | undefined)?.value;
    if (
      previous &&
      typeof previous === "object" &&
      "sequence" in previous &&
      value &&
      typeof value === "object" &&
      "sequence" in value &&
      typeof previous.sequence === "number" &&
      typeof value.sequence === "number" &&
      previous.sequence > value.sequence
    )
      return;
    store.put({ key, value, bytes, touched: Date.now() } satisfies CacheRow);
  };
  // Only disposable snapshots are evicted. Pending edits have their own store.
  if (Date.now() - lastPruned > 30000) {
    lastPruned = Date.now();
    const scan = store.getAll();
    scan.onsuccess = () => {
      const rows = (scan.result as CacheRow[]).sort(
        (a, b) => b.touched - a.touched,
      );
      let used = 0;
      rows.forEach((row, index) => {
        used += row.bytes;
        if (used > 32 * 1024 * 1024 || index >= 100) store.delete(row.key);
      });
    };
  }
  await done;
}

async function holdRecoveryLock(key: string, signal: AbortSignal) {
  return new Promise<boolean>((resolve) => {
    void navigator.locks
      .request(
        `zenith-recovery:${key}`,
        { ifAvailable: true },
        async (lock) => {
          resolve(!!lock);
          if (lock && !signal.aborted)
            await new Promise<void>((release) =>
              signal.addEventListener("abort", () => release(), { once: true }),
            );
        },
      )
      .catch(() => resolve(false));
  });
}

export async function removeDocumentCache(key: string) {
  try {
    const db = await open();
    const tx = db.transaction("snapshots", "readwrite");
    const done = completed(tx);
    tx.objectStore("snapshots").delete(key);
    await done;
  } catch {
    /* The network read still enforces access. */
  }
}

export async function clearDocumentCache() {
  epoch++;
  memoryRecoveries.clear();
  try {
    const db = await open();
    const tx = db.transaction(["snapshots", "recoveries"], "readwrite");
    const done = completed(tx);
    tx.objectStore("snapshots").clear();
    tx.objectStore("recoveries").clear();
    await done;
  } catch {
    /* Logout must complete even when browser storage is unavailable. */
  }
}

// Each editor has a separate recovery record. Web Locks prevent another tab
// from adopting live work; leases provide a fallback where Locks are absent.
export async function openDocumentRecovery(
  documentKey: string,
  snapshot: Document,
  signal: AbortSignal,
) {
  const owner = crypto.randomUUID();
  const generation = epoch;
  if (!currentKey(documentKey)) return undefined;
  let db: IDBDatabase | undefined;
  let rows: Recovery[];
  try {
    db = await open();
    rows = (await result(
      db.transaction("recoveries").objectStore("recoveries").getAll(),
    )) as Recovery[];
  } catch {
    db = undefined;
    rows = [];
  }
  rows = [
    ...rows.filter((row) => !memoryRecoveries.has(row.key)),
    ...memoryRecoveries.values(),
  ];
  let recovered: Recovery | undefined;
  for (const candidate of rows
    .filter((row) => row.documentKey === documentKey && row.update.length)
    .sort((a, b) => b.touched - a.touched)) {
    if (signal.aborted || generation !== epoch) return undefined;
    let locked = false;
    if (navigator.locks) {
      locked = await holdRecoveryLock(candidate.key, signal);
      if (!locked) continue;
    }
    if (!db) {
      if (locked || !candidate.owner || candidate.leaseUntil < Date.now())
        recovered = { ...candidate, owner, leaseUntil: Date.now() + 30000 };
      if (recovered) break;
      continue;
    }
    const tx = db.transaction("recoveries", "readwrite");
    const done = completed(tx);
    const store = tx.objectStore("recoveries");
    const request = store.get(candidate.key);
    request.onsuccess = () => {
      const local = memoryRecoveries.get(candidate.key);
      const row = (request.result ?? local) as Recovery | undefined;
      if (row && (locked || !row.owner || row.leaseUntil < Date.now())) {
        recovered = {
          ...(local && local.touched >= row.touched ? local : row),
          owner,
          leaseUntil: Date.now() + 30000,
        };
        store.put(recovered);
      }
    };
    try {
      await done;
    } catch {
      // Keep the claimed draft available in memory/export if browser writes fail.
      db = undefined;
    }
    if (recovered) break;
  }
  const key = recovered?.key ?? crypto.randomUUID();
  if (!recovered && navigator.locks) {
    await holdRecoveryLock(key, signal);
  }
  if (signal.aborted || generation !== epoch || !currentKey(documentKey))
    return undefined;
  if (recovered) memoryRecoveries.set(key, recovered);
  let chain = Promise.resolve();
  const save = (base: Document, update: Uint8Array, release = false) => {
    const current = memoryRecoveries.get(key);
    if (current?.owner && current.owner !== owner)
      return Promise.reject(
        new Error("This draft was recovered by another editor"),
      );
    // Capture immutable inputs before asynchronous work; cleanup can destroy Y.Doc.
    const row: Recovery = {
      key,
      documentKey,
      owner: release ? "" : owner,
      leaseUntil: release ? 0 : Date.now() + 30000,
      snapshot: base,
      update,
      touched: Date.now(),
    };
    if (generation === epoch && currentKey(documentKey)) {
      if (update.length) memoryRecoveries.set(key, row);
      else memoryRecoveries.delete(key);
    }
    chain = chain
      .catch(() => {})
      .then(async () => {
        if (generation !== epoch || !currentKey(documentKey)) return;
        if (!db) {
          if (!update.length) return;
          throw new Error("Local recovery storage is unavailable");
        }
        const tx = db.transaction("recoveries", "readwrite");
        const done = completed(tx);
        const store = tx.objectStore("recoveries");
        const request = store.get(key);
        request.onsuccess = () => {
          const current = request.result as Recovery | undefined;
          if (current && current.owner && current.owner !== owner) {
            tx.abort();
            return;
          }
          if (update.length) store.put(row);
          else store.delete(key);
        };
        await done;
      });
    return chain;
  };
  return { recovered, save, snapshot };
}
