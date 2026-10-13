import {
  createContext,
  memo,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent,
} from "react";
import "./fold-avatar.css";
import { FoldAvatarTeam, type FoldTeamAppearance } from "./fold-avatar-team";
import { FoldAvatarEngineer } from "./fold-avatar-engineer";
import { FoldAvatarClay } from "./fold-avatar-clay";
import { followAvatarPointer } from "./fold-avatar-gaze";
import { FoldAvatarMotion } from "./fold-avatar-motion";

import type { FoldAvatarState } from "./fold-avatar-state";
export type { FoldAvatarState } from "./fold-avatar-state";

export const avatarPalettes = [
  {
    name: "Butter",
    color: "#F2CC58",
    light: "#FFF1B2",
    dark: "#D29A28",
    shadow: "#936014",
    background: "#FAF2D8",
  },
  {
    name: "Tangerine",
    color: "#EE8B42",
    light: "#FFC284",
    dark: "#C5662D",
    shadow: "#853C1D",
    background: "#FCEBDD",
  },
  {
    name: "Coral",
    color: "#ED817B",
    light: "#FFD3BD",
    dark: "#C7525B",
    shadow: "#813445",
    background: "#F9E5E1",
  },
  {
    name: "Orchid",
    color: "#BA97DA",
    light: "#EBDAFF",
    dark: "#9270BC",
    shadow: "#5A4482",
    background: "#F0E8F6",
  },
  {
    name: "Lagoon",
    color: "#69BABA",
    light: "#C5EBDA",
    dark: "#408F98",
    shadow: "#28636C",
    background: "#E3F0E9",
  },
  {
    name: "Pistachio",
    color: "#ADB96A",
    light: "#E4EBC0",
    dark: "#809248",
    shadow: "#586132",
    background: "#EEF0DE",
  },
  {
    name: "Periwinkle",
    color: "#8BACE1",
    light: "#D3E8FF",
    dark: "#627EC1",
    shadow: "#40588B",
    background: "#E6ECF8",
  },
  {
    name: "Cocoa",
    color: "#B8947E",
    light: "#EAD2BA",
    dark: "#8D6C58",
    shadow: "#604534",
    background: "#F1E9E0",
  },
] as const;

function hashAgentId(agentId: string) {
  let hash = 2166136261;
  // Mention/member rows prefix the same actor ID with its kind.
  for (const character of agentId.replace(/^agent:/, "")) {
    hash ^= character.codePointAt(0) ?? 0;
    hash = Math.imul(hash, 16777619);
  }
  return hash >>> 0;
}

export function getAvatarPaletteIndex(agentId: string) {
  return hashAgentId(agentId) % avatarPalettes.length;
}

/** Product motion is opt-in at the onboarding boundary; other screens stay static. */
export const FoldAvatarMotionContext = createContext(false);

export const FoldAvatarPaletteContext = createContext<
  Readonly<Record<string, number>>
>({});

export type FoldAvatarProps = {
  appearance?: "engineer" | FoldTeamAppearance;
  agentId: string;
  accessory?: "secretary";
  state?: FoldAvatarState;
  size?: number;
  paletteIndex?: number;
  interactive?: boolean;
  followPointer?: boolean;
  compactMotion?: "blink" | "greet";
  motion?: "ambient" | "expressive" | "still";
  motionIntensity?: "full" | "quiet";
  paused?: boolean;
  replayKey?: number;
  className?: string;
  label?: string;
  onBoop?: () => void;
  onVisibilityChange?: (visible: boolean) => void;
};

export const FoldAvatar = memo(function FoldAvatar({
  appearance,
  agentId,
  accessory,
  state = "idle",
  size = 160,
  paletteIndex,
  interactive = false,
  followPointer = false,
  compactMotion,
  motion = "ambient",
  motionIntensity = "full",
  paused = false,
  replayKey = 0,
  className,
  label,
  onBoop,
  onVisibilityChange,
}: FoldAvatarProps) {
  const motionAllowed = useContext(FoldAvatarMotionContext);
  const paletteOverrides = useContext(FoldAvatarPaletteContext);
  const resolvedPaletteIndex =
    paletteIndex ??
    paletteOverrides[agentId.replace(/^agent:/, "")] ??
    getAvatarPaletteIndex(agentId);
  const id = useId().replace(/:/g, "");
  const rootRef = useRef<HTMLSpanElement & HTMLButtonElement>(null);
  const boopTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [booping, setBooping] = useState(false);
  const [isVisible, setIsVisible] = useState(false);
  const [isPageVisible, setIsPageVisible] = useState(true);
  const [reducedMotion, setReducedMotion] = useState(false);
  const palette =
    avatarPalettes[
      ((resolvedPaletteIndex % avatarPalettes.length) + avatarPalettes.length) %
        avatarPalettes.length
    ];
  const isCompact = size <= 40;
  const watchesMotion =
    motionAllowed &&
    motion !== "still" &&
    !paused &&
    (!isCompact ||
      interactive ||
      followPointer ||
      compactMotion !== undefined ||
      motion === "expressive");
  const isPaused =
    !watchesMotion || !isVisible || !isPageVisible || reducedMotion;
  const rootStyle = {
    width: size,
    height: size,
    "--fold-blink-delay": `-${(hashAgentId(agentId) % 4900) / 1000}s`,
    "--fold-breathe-delay": `-${(hashAgentId(agentId) % 2700) / 1000}s`,
    "--fold-compact-blink-duration": `${7 + (hashAgentId(agentId) % 3000) / 1000}s`,
    "--fold-gaze-x": "0px",
    "--fold-gaze-y": "0px",
  } as CSSProperties;

  useEffect(() => {
    // Static list avatars need no visibility observers or document listeners.
    if (!watchesMotion && !onVisibilityChange) return;
    setIsVisible(false);
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    const syncMotion = () => setReducedMotion(media.matches);
    const syncPage = () => setIsPageVisible(!document.hidden);
    syncMotion();
    syncPage();
    media.addEventListener("change", syncMotion);
    document.addEventListener("visibilitychange", syncPage);

    const observer = new IntersectionObserver(
      ([entry]) => {
        setIsVisible(entry.isIntersecting);
        onVisibilityChange?.(entry.isIntersecting);
      },
      { rootMargin: onVisibilityChange ? "0px" : "40px" },
    );
    if (rootRef.current) observer.observe(rootRef.current);

    return () => {
      media.removeEventListener("change", syncMotion);
      document.removeEventListener("visibilitychange", syncPage);
      observer.disconnect();
      onVisibilityChange?.(false);
    };
  }, [interactive, onVisibilityChange, watchesMotion]);

  useEffect(() => {
    if (!followPointer || isPaused || !rootRef.current) return;
    return followAvatarPointer(rootRef.current);
  }, [followPointer, isPaused]);

  useEffect(
    () => () => {
      if (boopTimerRef.current) clearTimeout(boopTimerRef.current);
    },
    [],
  );

  const moveGaze = (
    event: PointerEvent<HTMLButtonElement | HTMLSpanElement>,
  ) => {
    if (!interactive || isPaused || event.pointerType === "touch") return;
    const rect = event.currentTarget.getBoundingClientRect();
    const x = Math.max(
      -6,
      Math.min(2, ((event.clientX - rect.left) / rect.width - 0.5) * 12),
    );
    const y = Math.max(
      -1,
      Math.min(5, ((event.clientY - rect.top) / rect.height - 0.5) * 8),
    );
    event.currentTarget.style.setProperty("--fold-gaze-x", `${x}px`);
    event.currentTarget.style.setProperty("--fold-gaze-y", `${y}px`);
  };

  const resetGaze = () => {
    rootRef.current?.style.setProperty("--fold-gaze-x", "0px");
    rootRef.current?.style.setProperty("--fold-gaze-y", "0px");
  };

  const boop = () => {
    if (isPaused) {
      onBoop?.();
      return;
    }
    if (booping) return;
    setBooping(true);
    boopTimerRef.current = setTimeout(() => setBooping(false), 760);
    onBoop?.();
  };

  const illustration =
    appearance === "engineer" ? (
      <FoldAvatarEngineer />
    ) : appearance ? (
      <FoldAvatarTeam appearance={appearance} />
    ) : motion !== "ambient" ? (
      <FoldAvatarMotion
        key={`${replayKey}-${isCompact}`}
        compact={isCompact}
        id={id}
        palette={palette}
        paused={isPaused}
        quiet={motionIntensity === "quiet"}
        state={state}
      />
    ) : (
      <FoldAvatarClay
        id={id}
        palette={palette}
        originalColor={palette.name === "Tangerine"}
        replayKey={replayKey}
        accessory={accessory}
      />
    );

  const sharedProps = {
    ref: rootRef,
    className: ["fold-avatar", className].filter(Boolean).join(" "),
    style: rootStyle,
    "data-state": state,
    "data-compact": isCompact || undefined,
    "data-compact-motion": isCompact ? compactMotion : undefined,
    "data-paused": isPaused || undefined,
    "data-booping": booping || undefined,
    onPointerMove: interactive && !followPointer ? moveGaze : undefined,
    onPointerLeave: interactive && !followPointer ? resetGaze : undefined,
  };

  return interactive ? (
    <button
      {...sharedProps}
      type="button"
      aria-label={
        label ?? `Fold, ${state.replace("-", " ")}. Give a gentle boop.`
      }
      onClick={boop}
    >
      {illustration}
    </button>
  ) : (
    <span
      {...sharedProps}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      {illustration}
    </span>
  );
});
