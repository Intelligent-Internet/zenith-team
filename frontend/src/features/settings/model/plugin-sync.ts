import { useEffect, useState } from "react";

/**
 * The time, renewed every minute, for lines that say how long ago the
 * selected computer last reported its plugins.
 */
export function useMinuteClock() {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, []);
  return now;
}

// "just now", "5m ago", "2h ago" or "3d ago"; undefined for an unreadable
// time.
export function syncedAgo(observedAt: string, now: number) {
  const at = Date.parse(observedAt);
  if (Number.isNaN(at)) return undefined;
  const minutes = Math.floor((now - at) / 60_000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}
