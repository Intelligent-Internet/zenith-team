import type { CSSProperties, ReactNode } from "react";
import { ArrowRight, Lightbulb, X } from "@/shared/ui/icons";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { useStudioMotion } from "@/features/public-site/lib/studio-motion";

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
  const { enabled } = useStudioMotion();
  const agent =
    studioAgents.find((item) => item.role === role) ?? studioAgents[0];
  return (
    <span className={`studio-avatar studio-avatar--${agent.tone}`}>
      <FoldAvatar
        agentId={`studio-${role}`}
        paletteIndex={agent.palette}
        size={size}
        paused={!enabled}
      />
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
