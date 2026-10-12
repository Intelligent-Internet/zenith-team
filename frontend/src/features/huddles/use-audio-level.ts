import { useEffect, type RefObject } from "react";

// Writes a smoothed 0–1 `--level` custom property on visible elements,
// so speaking animations follow the voice without re-rendering React.
export function useAudioLevel(
  ref: RefObject<HTMLElement | null>,
  read: (() => number) | null,
) {
  useEffect(() => {
    const element = ref.current;
    if (!element || !read) return undefined;
    let frame = 0;
    let level = 0;
    let visible = false;
    let lastUpdate = 0;
    let previousValue = "";
    const tick = (now: number) => {
      frame = requestAnimationFrame(tick);
      if (now - lastUpdate < 1000 / 30) return;
      lastUpdate = now;
      // Speech sits low on the linear scale; the curve spreads it out.
      const target = Math.min(1, Math.sqrt(Math.max(0, read())) * 1.35);
      // Rise quickly, decay gently — the way a VU meter feels natural.
      level += (target - level) * (target > level ? 0.7 : 0.19);
      if (level < 0.002) level = 0;
      const value = level.toFixed(3);
      if (value !== previousValue) {
        element.style.setProperty("--level", value);
        previousValue = value;
      }
    };
    const updateVisibility = () => {
      cancelAnimationFrame(frame);
      if (visible && !document.hidden) frame = requestAnimationFrame(tick);
    };
    const observer = new IntersectionObserver(([entry]) => {
      visible = entry.isIntersecting;
      updateVisibility();
    });
    observer.observe(element);
    document.addEventListener("visibilitychange", updateVisibility);
    return () => {
      observer.disconnect();
      document.removeEventListener("visibilitychange", updateVisibility);
      cancelAnimationFrame(frame);
      element.style.removeProperty("--level");
    };
  }, [ref, read]);
}
