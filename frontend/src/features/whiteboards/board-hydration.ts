import { apiClient } from "@/lib/api-client";
import type { components } from "@/shared/api/generated/api.generated";
import type { BoardHydratedItem } from "./board-api";

type Waiting = {
  resolve: (item: BoardHydratedItem) => void;
  reject: (error: unknown) => void;
};
type Batch = Map<string, Waiting[]>;

// Mounted (visible) objects share one bounded hydration request. No original
// media or note CRDT is included in metadata bootstrap. Stroke points never
// change, so panning back to a stroke reuses its points; a note may send the
// state vector it holds and receive only the missing CRDT update. The member
// editor and the public link share this; the public route ignores vectors.
export class BoardHydration {
  private waiting: Batch = new Map();
  private vectors = new Map<string, string>();
  private strokes = new Map<string, BoardHydratedItem>();
  private timer?: ReturnType<typeof setTimeout>;
  private controller = new AbortController();
  /** `base` is the Board path: the member route or the public link. */
  constructor(private base: string) {}
  get(id: string, stateVector?: string) {
    const stroke = this.strokes.get(id);
    if (stroke) {
      // Refresh recency for the bounded cache.
      this.strokes.delete(id);
      this.strokes.set(id, stroke);
      return Promise.resolve(stroke);
    }
    return new Promise<BoardHydratedItem>((resolve, reject) => {
      this.waiting.set(id, [
        ...(this.waiting.get(id) ?? []),
        { resolve, reject },
      ]);
      // A full-state request for the same note wins over a delta.
      if (stateVector && !this.waiting.get(id)?.slice(0, -1).length)
        this.vectors.set(id, stateVector);
      else this.vectors.delete(id);
      this.timer ??= setTimeout(() => void this.flush(), 0);
    });
  }
  /** Ink this member drew is already known; panning back never fetches it. */
  rememberStroke(id: string, points: NonNullable<BoardHydratedItem["points"]>) {
    this.remember({
      id,
      points,
      text: "",
      textRevision: 0,
      lifecycleRevision: 0,
    });
  }
  private remember(item: BoardHydratedItem) {
    this.strokes.delete(item.id);
    this.strokes.set(item.id, item);
    if (this.strokes.size > 2000)
      this.strokes.delete(this.strokes.keys().next().value!);
  }
  private async flush() {
    this.timer = undefined;
    const batch: Batch = new Map([...this.waiting].slice(0, 100));
    const stateVectors: Record<string, string> = {};
    batch.forEach((_value, key) => {
      this.waiting.delete(key);
      const vector = this.vectors.get(key);
      if (vector) stateVectors[key] = vector;
      this.vectors.delete(key);
    });
    await this.request(batch, stateVectors);
    if (this.waiting.size && !this.controller.signal.aborted)
      this.timer = setTimeout(() => void this.flush(), 0);
  }
  private async request(batch: Batch, vectors: Record<string, string>) {
    const stateVectors = Object.fromEntries(
      Object.entries(vectors).filter(([id]) => batch.has(id)),
    );
    try {
      const response = await apiClient.post<
        components["schemas"]["BoardHydration"]
      >(
        `${this.base}/hydrate`,
        {
          itemIds: [...batch.keys()],
          ...(Object.keys(stateVectors).length ? { stateVectors } : {}),
        },
        { signal: this.controller.signal },
      );
      // An object deleted before this member's snapshot caught up is reported
      // as missing; the rest of the batch still hydrates.
      const missing = new Set(response.missing);
      const items = new Map(response.items.map((item) => [item.id, item]));
      for (const [id, listeners] of batch) {
        const item = items.get(id);
        if (item?.points) this.remember(item);
        listeners.forEach((listener) =>
          item
            ? listener.resolve(item)
            : listener.reject(
                new Error(
                  missing.has(id)
                    ? "This object was deleted."
                    : "This object is no longer available.",
                ),
              ),
        );
      }
    } catch (error) {
      // A batch the server refuses for its size is split and asked again, so
      // one large note cannot leave its neighbours unhydrated.
      const status = (error as { status?: number | null }).status;
      if (
        batch.size > 1 &&
        (status === 413 || status === 422) &&
        !this.controller.signal.aborted
      ) {
        const entries = [...batch];
        const half = Math.ceil(entries.length / 2);
        await this.request(new Map(entries.slice(0, half)), vectors);
        await this.request(new Map(entries.slice(half)), vectors);
        return;
      }
      batch.forEach((listeners) =>
        listeners.forEach((listener) => listener.reject(error)),
      );
    }
  }
  dispose() {
    clearTimeout(this.timer);
    this.controller.abort();
    this.waiting.forEach((listeners) =>
      listeners.forEach((listener) =>
        listener.reject(new Error("Board closed.")),
      ),
    );
    this.waiting.clear();
  }
}
