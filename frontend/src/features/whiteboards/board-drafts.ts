import { readSessionSnapshot } from "@/app/session/session";

let database: Promise<IDBDatabase> | undefined;
function openDrafts() {
  database ??= new Promise((resolve, reject) => {
    const request = indexedDB.open("zenith-whiteboard-drafts", 1);
    request.onupgradeneeded = () =>
      request.result.createObjectStore("drafts", { keyPath: "key" });
    request.onerror = () => {
      database = undefined;
      reject(request.error);
    };
    request.onsuccess = () => {
      request.result.onversionchange = () => {
        request.result.close();
        database = undefined;
      };
      resolve(request.result);
    };
  });
  return database;
}
function currentOwner(key: string) {
  return key.startsWith(`${readSessionSnapshot().userId}:`);
}
export async function readBoardDrafts<T>(
  prefix: string,
): Promise<{ key: string; value: T; touched?: number }[]> {
  if (!currentOwner(prefix)) return [];
  const db = await openDrafts();
  return new Promise((resolve, reject) => {
    const request = db
      .transaction("drafts")
      .objectStore("drafts")
      .getAll(IDBKeyRange.bound(prefix, `${prefix}\uffff`));
    request.onsuccess = () =>
      resolve(currentOwner(prefix) ? request.result : []);
    request.onerror = () => reject(request.error);
  });
}
export async function writeBoardDraft(key: string, value: unknown) {
  const db = await openDrafts();
  if (!currentOwner(key))
    throw new Error("The account changed before the local draft was saved.");
  return new Promise<void>((resolve, reject) => {
    const transaction = db.transaction("drafts", "readwrite");
    if (value === undefined) transaction.objectStore("drafts").delete(key);
    else
      transaction
        .objectStore("drafts")
        .put({ key, value, touched: Date.now() });
    transaction.oncomplete = () => resolve();
    transaction.onabort = transaction.onerror = () => reject(transaction.error);
  });
}
// Recovery may adopt another session's row. Remove only the captured write;
// newer edits from that session must remain available to save or recover.
export async function deleteBoardDraft<T>(
  key: string,
  owned: (value: T) => boolean,
) {
  const db = await openDrafts();
  if (!currentOwner(key))
    throw new Error("The account changed before the local draft was saved.");
  return new Promise<void>((resolve, reject) => {
    const transaction = db.transaction("drafts", "readwrite");
    const store = transaction.objectStore("drafts");
    const request = store.get(key);
    request.onsuccess = () => {
      const row = request.result as { value: T } | undefined;
      if (row && owned(row.value)) store.delete(key);
    };
    transaction.oncomplete = () => resolve();
    transaction.onabort = transaction.onerror = () => reject(transaction.error);
  });
}
export function encodeBoardUpdate(update: Uint8Array): string {
  let binary = "";
  for (let offset = 0; offset < update.length; offset += 8192)
    binary += String.fromCharCode(...update.subarray(offset, offset + 8192));
  return btoa(binary);
}
export function decodeBoardUpdate(value: string): Uint8Array {
  return Uint8Array.from(atob(value), (character) => character.charCodeAt(0));
}
