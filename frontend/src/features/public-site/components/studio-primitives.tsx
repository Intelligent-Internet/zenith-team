import {
  useEffect,
  useId,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { ArrowRight, Lightbulb, X } from "@/shared/ui/icons";
import { FoldAvatarClay } from "@/shared/ui/agent-avatar/fold-avatar-clay";
import { avatarPalettes } from "@/shared/ui/agent-avatar/fold-avatar-palettes";
import "@/shared/ui/agent-avatar/fold-avatar.css";

export const studioAssets = "/images/company-studio";

export const studioAgents = [
  {
    role: "CEO",
    palette: 1,
    color: "#e8834e",
    tone: "orange",
    description: "Your partner for the big picture.",
  },
  {
    role: "Product",
    palette: 3,
    color: "#a082d8",
    tone: "lilac",
    description: "Find the thing worth building.",
  },
  {
    role: "Engineering",
    palette: 4,
    color: "#49a6ad",
    tone: "teal",
    description: "Give your idea a working version.",
  },
  {
    role: "Marketing",
    palette: 0,
    color: "#edc550",
    tone: "yellow",
    description: "Help the right people find you.",
  },
] as const;

export function StudioAvatar({
  role,
  size = 34,
}: {
  role: string;
  size?: number;
}) {
  const id = useId().replace(/:/g, "");
  const ref = useRef<HTMLSpanElement>(null);
  const [ready, setReady] = useState(false);
  const agent =
    studioAgents.find((item) => item.role === role) ?? studioAgents[0];
  const palette = avatarPalettes[agent.palette];

  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    // Fetch the shared atlas shortly before an avatar scrolls into view.
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (!entry.isIntersecting) return;
        setReady(true);
        observer.disconnect();
      },
      { root: element.closest("[data-studio-scroll]"), rootMargin: "300px" },
    );
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  return (
    <span ref={ref} className={`studio-avatar studio-avatar--${agent.tone}`}>
      <span
        className="fold-avatar"
        style={{ width: size, height: size }}
        data-state="idle"
        data-paused="true"
        data-compact={size <= 40 || undefined}
        aria-hidden="true"
      >
        {ready && (
          <FoldAvatarClay
            id={id}
            palette={palette}
            originalColor={palette.name === "Tangerine"}
            replayKey={0}
          />
        )}
      </span>
    </span>
  );
}

export function StudioArt({
  name,
  className = "",
  alt = "",
  eager = false,
}: {
  name: string;
  className?: string;
  alt?: string;
  eager?: boolean;
}) {
  return (
    <img
      className={className}
      src={`${studioAssets}/${name}.webp`}
      alt={alt}
      loading={eager ? "eager" : "lazy"}
      decoding="async"
    />
  );
}

export function ExampleWindow({
  children,
  className = "",
  label = "Example output",
}: {
  children: ReactNode;
  className?: string;
  label?: string;
}) {
  return (
    <div className={`studio-window ${className}`}>
      <div className="studio-window-bar">
        <span>
          <Lightbulb size={12} aria-hidden="true" />
          {label}
        </span>
        <X size={12} aria-hidden="true" />
      </div>
      {children}
    </div>
  );
}

export function StudioAction({
  children,
  onClick,
  subtle = false,
  className = "",
  disabled = false,
}: {
  children: ReactNode;
  onClick: () => void;
  subtle?: boolean;
  className?: string;
  disabled?: boolean;
}) {
  return (
    <button
      className={`studio-button ${subtle ? "studio-button--subtle" : ""} ${className}`}
      type="button"
      onClick={onClick}
      disabled={disabled}
    >
      {children}
      <ArrowRight size={17} aria-hidden="true" />
    </button>
  );
}

export function revealDelay(delay: number): CSSProperties {
  return { "--reveal-delay": `${delay}ms` } as CSSProperties;
}
