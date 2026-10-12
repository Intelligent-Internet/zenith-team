import { useEffect, useId, useRef, useState } from "react";
import { OnboardingResearchAction } from "./onboarding-research-action";
import body from "@/shared/ui/agent-avatar/assets/fold-andrew/body.webp";
import eyes from "@/shared/ui/agent-avatar/assets/fold-andrew/eyes.webp";
import mouth from "@/shared/ui/agent-avatar/assets/fold-andrew/mouth.webp";
import feet from "@/shared/ui/agent-avatar/assets/fold-andrew/feet.webp";
import wave from "@/shared/ui/agent-avatar/assets/fold-andrew/left.webp";
import closedBook from "@/shared/ui/agent-avatar/assets/fold-andrew/right.webp";
import "../../styles/onboarding-research-character.css";

export type ResearchMotion =
  "idle" | "searching" | "understanding" | "building" | "ready";

// This illustration lives only in onboarding. All loops stop offscreen or when
// the tab is hidden, and reduced motion leaves a complete static pose.
export function OnboardingResearchCharacter({
  motion,
  paused,
}: {
  motion: ResearchMotion;
  paused: boolean;
}) {
  const root = useRef<HTMLDivElement>(null);
  const armMask = `${useId().replace(/:/g, "")}-arm`;
  const [visible, setVisible] = useState(false);
  const [pageVisible, setPageVisible] = useState(true);
  const [reducedMotion, setReducedMotion] = useState(true);
  useEffect(() => {
    const observer = new IntersectionObserver(([entry]) =>
      setVisible(entry.isIntersecting),
    );
    if (root.current) observer.observe(root.current);
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    const syncMotion = () => setReducedMotion(media.matches);
    syncMotion();
    media.addEventListener("change", syncMotion);
    const sync = () => setPageVisible(!window.document.hidden);
    sync();
    window.document.addEventListener("visibilitychange", sync);
    return () => {
      observer.disconnect();
      media.removeEventListener("change", syncMotion);
      window.document.removeEventListener("visibilitychange", sync);
    };
  }, []);
  const writing = motion === "understanding" || motion === "building";
  const layer: ResearchLayer =
    motion === "ready" || motion === "idle"
      ? "pose"
      : writing
        ? "building"
        : "searching";
  const actionPaused = paused || !visible || !pageVisible || reducedMotion;
  return (
    <div
      ref={root}
      className="onboarding-research-character"
      data-motion={motion}
      data-paused={paused || !visible || !pageVisible}
      aria-hidden="true"
    >
      <div className="research-layer" data-current={layer === "pose"}>
        <svg viewBox="0 0 280 240" fill="none" focusable="false">
          <defs>
            {/* The book arm's shoulder curls up past the torso's right edge,
                where it reads as a second, deformed hand at this size. Fading
                it out at the edge lets the arm leave the torso's side. */}
            <linearGradient
              id={`${armMask}-fade`}
              gradientUnits="userSpaceOnUse"
              x1="192"
              x2="200"
            >
              <stop offset="0" stopColor="white" />
              <stop offset="1" stopColor="black" />
            </linearGradient>
            <mask
              id={armMask}
              maskUnits="userSpaceOnUse"
              x="160"
              y="113"
              width="70"
              height="57"
            >
              <rect
                x="160"
                y="113"
                width="70"
                height="57"
                fill={`url(#${armMask}-fade)`}
              />
            </mask>
          </defs>
          {/* Matches the clips' framing: the clips draw the character larger,
              lower and centered on its face, so a cross-fade does not jump. */}
          <g transform="translate(152 224) scale(1.15) translate(-150 -210.67)">
            <g transform="translate(10 0)">
              <image href={feet} x="80" y="161" width="120" height="52" />
              <g className="research-body">
                <g className="research-wave">
                  <image href={wave} x="17" y="106" width="79" height="49" />
                </g>
                <image href={body} x="58" y="35" width="148" height="147" />
                <g className="research-gaze">
                  <g className="research-eyes">
                    <image href={eyes} x="92" y="77" width="80" height="46" />
                  </g>
                </g>
                <image href={mouth} x="119" y="124" width="30" height="16" />
                <image
                  href={closedBook}
                  x="160"
                  y="113"
                  width="70"
                  height="57"
                  mask={`url(#${armMask})`}
                />
              </g>
            </g>
          </g>
        </svg>
      </div>
      {/* Every layer stays mounted, so a switch cross-fades between painted
          frames; a clip loads only its poster until it first plays. */}
      {researchActions.map((action) => (
        <div
          key={action}
          className="research-layer"
          data-current={layer === action}
        >
          <OnboardingResearchAction
            action={action}
            current={layer === action}
            paused={actionPaused || layer !== action}
          />
        </div>
      ))}
    </div>
  );
}

const researchActions = ["searching", "building"] as const;
type ResearchLayer = "pose" | (typeof researchActions)[number];
