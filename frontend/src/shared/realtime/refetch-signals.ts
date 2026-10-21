import { zenithTeamRealtimeClient } from "@/shared/realtime/realtime-client.service";

type RefetchReason = "focus" | "visible" | "online" | "recovery";

export function subscribeToRefetchSignals(
  refetch: (reason: RefetchReason) => void,
) {
  let scheduled: number | undefined;
  let pendingReason: RefetchReason = "focus";
  const schedule = (reason: RefetchReason) => {
    if (scheduled === undefined || reason !== "focus") pendingReason = reason;
    if (scheduled !== undefined) return;
    scheduled = window.setTimeout(() => {
      scheduled = undefined;
      refetch(pendingReason);
    }, 0);
  };
  const onVisibilityChange = () => {
    if (document.visibilityState === "visible") schedule("visible");
  };
  const onFocus = () => schedule("focus");
  const onOnline = () => schedule("online");

  window.addEventListener("focus", onFocus);
  window.addEventListener("online", onOnline);
  document.addEventListener("visibilitychange", onVisibilityChange);
  const removeRecovery = zenithTeamRealtimeClient.onRecovery(() =>
    schedule("recovery"),
  );

  return () => {
    if (scheduled !== undefined) window.clearTimeout(scheduled);
    window.removeEventListener("focus", onFocus);
    window.removeEventListener("online", onOnline);
    document.removeEventListener("visibilitychange", onVisibilityChange);
    removeRecovery();
  };
}
