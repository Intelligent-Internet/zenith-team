let lastInteraction = Date.now();
export function notePresenceActivity() {
  lastInteraction = Date.now();
}
export function hasRecentPresenceActivity() {
  return (
    document.visibilityState === "visible" &&
    document.hasFocus() &&
    Date.now() - lastInteraction < 5 * 60_000
  );
}
