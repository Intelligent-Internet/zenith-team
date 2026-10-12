import { useLayoutEffect, useRef, useState } from "react";
import { useStudioMotion } from "./studio-motion";

export function useStudioScrollSteps(count: number) {
  const track = useRef<HTMLElement>(null);
  const pin = useRef<HTMLDivElement>(null);
  const scrollRoot = useRef<HTMLElement | null>(null);
  const distance = useRef(1);
  const canPin = useRef(true);
  const [step, setStep] = useState(0);
  const [pinned, setPinned] = useState(true);
  const { enabled } = useStudioMotion();

  useLayoutEffect(() => {
    const section = track.current;
    const stage = pin.current;
    const root = section?.closest<HTMLElement>("[data-studio-scroll]");
    const content = stage?.firstElementChild;
    if (!section || !stage || !root || !content) return;
    scrollRoot.current = root;
    let frame = 0;

    const updateStep = () => {
      frame = 0;
      if (!canPin.current) return;
      const traveled =
        root.getBoundingClientRect().top - section.getBoundingClientRect().top;
      setStep(
        Math.max(
          0,
          Math.min(count - 1, Math.floor(traveled / distance.current)),
        ),
      );
    };
    const onScroll = () => {
      if (!frame) frame = requestAnimationFrame(updateStep);
    };
    const measure = () => {
      const viewport = root.clientHeight;
      distance.current = Math.max(320, viewport * 0.7);
      section.style.setProperty("--workflow-viewport", `${viewport}px`);
      section.style.setProperty(
        "--workflow-step-distance",
        `${distance.current}px`,
      );
      const styles = getComputedStyle(stage);
      const contentHeight =
        content.getBoundingClientRect().height +
        parseFloat(styles.paddingTop) +
        parseFloat(styles.paddingBottom);
      // A short window or text zoom must never hide the controls below the fold.
      canPin.current = contentHeight <= viewport + 1;
      section.dataset.scrollPinned = String(canPin.current);
      setPinned(canPin.current);
      onScroll();
    };

    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(root);
    observer.observe(content);
    root.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      observer.disconnect();
      root.removeEventListener("scroll", onScroll);
      cancelAnimationFrame(frame);
      scrollRoot.current = null;
    };
  }, [count]);

  const selectStep = (index: number) => {
    const root = scrollRoot.current;
    const section = track.current;
    if (!root || !section || !canPin.current) {
      setStep(index);
      return;
    }
    const start =
      root.scrollTop +
      section.getBoundingClientRect().top -
      root.getBoundingClientRect().top;
    root.scrollTo({
      top: start + (index + 0.15) * distance.current,
      behavior: enabled ? "smooth" : "instant",
    });
  };

  return { track, pin, step, pinned, selectStep };
}
