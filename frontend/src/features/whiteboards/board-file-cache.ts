import { apiClient } from "@/lib/api-client";

// Board files are immutable by attachment ID. Mounted nodes share one object
// URL per file variant, and recently unmounted files stay available so panning
// back does not download or decode them again. Idle files are bounded by count
// and bytes; originals leave first, since previews cover ordinary zoom.
type Entry = {
  url: Promise<string>;
  refs: number;
  used: number;
  bytes: number;
  original: boolean;
};
const entries = new Map<string, Entry>();
const retained = 48;
const retainedBytes = 64 * 1024 * 1024;

function evict() {
  const idle = [...entries]
    .filter(([, entry]) => entry.refs === 0)
    .sort(
      (a, b) =>
        Number(b[1].original) - Number(a[1].original) || a[1].used - b[1].used,
    );
  let bytes = idle.reduce((total, [, entry]) => total + entry.bytes, 0);
  for (const [path, entry] of idle) {
    if (entries.size <= retained && bytes <= retainedBytes) break;
    entries.delete(path);
    bytes -= entry.bytes;
    void entry.url.then(URL.revokeObjectURL, () => {});
  }
}

export function acquireBoardFile(path: string) {
  let entry = entries.get(path);
  if (!entry) {
    const created: Entry = {
      url: apiClient.get<Blob>(path, { responseType: "blob" }).then((blob) => {
        created.bytes = blob.size;
        return URL.createObjectURL(blob);
      }),
      refs: 0,
      used: 0,
      bytes: 0,
      original: !path.endsWith("variant=preview"),
    };
    // A failed download is not cached; the next mount retries.
    created.url.then(
      () => {
        if (entries.get(path) === created) evict();
      },
      () => {
        if (entries.get(path) === created) entries.delete(path);
      },
    );
    entries.set(path, created);
    entry = created;
  }
  entry.refs++;
  entry.used = Date.now();
  const current = entry;
  let released = false;
  return {
    url: current.url,
    release() {
      if (released) return;
      released = true;
      current.refs--;
      current.used = Date.now();
      evict();
    },
  };
}

export async function downloadBoardFile(path: string, filename: string) {
  const blob = await apiClient.get<Blob>(path, { responseType: "blob" });
  const url = URL.createObjectURL(blob);
  try {
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    link.click();
  } finally {
    // Some browsers start the download after click returns.
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
}
