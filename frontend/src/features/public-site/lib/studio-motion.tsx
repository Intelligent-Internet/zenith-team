import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";

const MotionContext = createContext({ enabled: false });

export function StudioMotionProvider({ children }: { children: ReactNode }) {
  const [preference, setPreference] = useState(
    () => window.matchMedia("(prefers-reduced-motion: reduce)").matches,
  );
  const [savingData] = useState(() =>
    Boolean(
      (navigator as Navigator & { connection?: { saveData?: boolean } })
        .connection?.saveData,
    ),
  );
  const enabled = !(preference || savingData);

  useEffect(() => {
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setPreference(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  return (
    <MotionContext.Provider value={{ enabled }}>
      {children}
    </MotionContext.Provider>
  );
}

export function useStudioMotion() {
  return useContext(MotionContext);
}

export function useStudioVisibility<T extends HTMLElement>(threshold = 0.15) {
  const ref = useRef<T>(null);
  const [visible, setVisible] = useState(false);
  const [pageVisible, setPageVisible] = useState(!document.hidden);

  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    const observer = new IntersectionObserver(
      ([entry]) =>
        setVisible(
          entry.isIntersecting && entry.intersectionRatio >= threshold,
        ),
      { root: element.closest("[data-studio-scroll]"), threshold },
    );
    observer.observe(element);
    const onVisibility = () => setPageVisible(!document.hidden);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      observer.disconnect();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [threshold]);

  return { ref, visible: visible && pageVisible };
}

export function useStudioReveals(root: RefObject<HTMLDivElement | null>) {
  const { enabled } = useStudioMotion();

  useEffect(() => {
    const container = root.current;
    if (!container) return;
    const elements = Array.from(
      container.querySelectorAll<HTMLElement>("[data-reveal]"),
    );
    if (!enabled) {
      elements.forEach((element) => element.classList.add("is-revealed"));
      return;
    }
    const bounds = container.getBoundingClientRect();
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          entry.target.classList.add("is-revealed");
          observer.unobserve(entry.target);
        }
      },
      { root: container, threshold: 0.12, rootMargin: "0px 0px -20px 0px" },
    );
    // Read every position before changing styles, so reveals share one layout.
    const positions = elements.map((element) => ({
      element,
      inViewport: element.getBoundingClientRect().top < bounds.bottom,
    }));
    for (const { element, inViewport } of positions) {
      if (inViewport) {
        element.classList.add("is-revealed");
      } else {
        element.classList.add("will-reveal");
        observer.observe(element);
      }
    }
    return () => observer.disconnect();
  }, [enabled, root]);
}
