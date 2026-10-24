// Closed names and outcomes prevent authored content or private values from
// entering diagnostics. Keep only the latest entry per name; PerformanceObserver
// can collect the stream without an ever-growing browser timeline.
type Measurement =
  | "evaluation"
  | "input-to-frame"
  | "first-useful-preview"
  | "stale-edit-rejected"
  | "stale-edit-recovery";
type Outcome = "ready" | "failed" | "unavailable" | "recovered" | "closed";
export function uiNow(): number {
  return typeof performance === "undefined" ? 0 : performance.now();
}
export function measureUI(
  name: Measurement,
  start: number,
  outcome: Outcome,
): void {
  if (typeof performance === "undefined" || !Number.isFinite(start)) return;
  const end = performance.now();
  if (start < 0 || start > end) return;
  const key = `zenith.intelligent-ui.${name}`;
  try {
    performance.clearMeasures(key);
    performance.measure(key, { start, end, detail: { outcome } });
  } catch {
    // Diagnostics must not change behavior on older or restricted clients.
  }
}

// Two frame callbacks provide a paint opportunity after the committed view.
// This measures time to a frame, not physical display or assistive-tech latency.
export function measureUIFrame(
  name: "input-to-frame" | "first-useful-preview",
  start: number,
  current: () => boolean,
  recorded: () => void = () => {},
): () => void {
  if (typeof requestAnimationFrame === "undefined") return () => {};
  let cancelled = false;
  let frame = requestAnimationFrame(() => {
    if (cancelled || !current()) return;
    frame = requestAnimationFrame(() => {
      if (!cancelled && current()) {
        measureUI(name, start, "ready");
        recorded();
      }
    });
  });
  return () => {
    cancelled = true;
    cancelAnimationFrame(frame);
  };
}

// Session-local recovery timing. Only an acknowledged application completes a
// recovery; reading or restoring a draft is not evidence that it was saved.
export function createSourceRecoveryMeasurement(
  now = uiNow,
  record: typeof measureUI = measureUI,
) {
  let started: number | undefined;
  const finish = (outcome: "recovered" | "closed") => {
    if (started === undefined) return;
    record("stale-edit-recovery", started, outcome);
    started = undefined;
  };
  return {
    stale() {
      const at = now();
      started ??= at;
      record("stale-edit-rejected", at, "failed");
    },
    applied: () => finish("recovered"),
    close: () => finish("closed"),
  };
}
