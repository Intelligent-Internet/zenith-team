import { useEffect, useRef, useState } from "react";
import { ArrowRight } from "@/shared/ui/icons";
import { studioAgents, studioAssets, StudioAction } from "./studio-primitives";
import { useStudioMotion, useStudioVisibility } from "../lib/studio-motion";
import { getStudioHeroPoster } from "../lib/studio-assets";

export function StudioHero({ onGetStarted }: { onGetStarted: () => void }) {
  const video = useRef<HTMLVideoElement>(null);
  const { ref, visible } = useStudioVisibility<HTMLElement>();
  const { enabled } = useStudioMotion();
  const [activeAgent, setActiveAgent] = useState<number | null>(null);
  const [posterUrl] = useState(getStudioHeroPoster);
  const [smallScreen] = useState(
    () => window.matchMedia("(max-width: 767px)").matches,
  );

  useEffect(() => {
    const element = video.current;
    if (!element) return;
    if (!enabled || !visible) {
      element.pause();
      return;
    }

    let disposed = false;
    let frame = 0;
    const poster = new Image();
    poster.src = element.poster;
    // Let the poster paint before video download and decoding compete for work.
    void poster
      .decode()
      .catch(() => {})
      .then(() => {
        if (disposed) return;
        frame = requestAnimationFrame(() => {
          frame = requestAnimationFrame(() => {
            // Retain the poster when autoplay is unavailable.
            void element.play().catch(() => {});
          });
        });
      });
    return () => {
      disposed = true;
      cancelAnimationFrame(frame);
      element.pause();
    };
  }, [enabled, visible]);

  return (
    <section
      ref={ref}
      className="studio-hero"
      aria-labelledby="studio-title"
      id="team"
    >
      <div className="studio-hero-media">
        <div className="studio-hero-scene">
          <video
            ref={video}
            poster={posterUrl}
            src={`${studioAssets}/hero-${smallScreen ? "720p" : "1080p"}.mp4?v=4`}
            width="1920"
            height="1080"
            muted
            loop
            playsInline
            preload="none"
            tabIndex={-1}
            aria-hidden="true"
          />
          <div
            className="absolute inset-0 z-[3] pointer-events-none"
            role="group"
            aria-label="Meet your specialist agents"
          >
            {studioAgents.map((agent, index) => (
              <button
                type="button"
                className={`studio-team-chip studio-team-chip--${agent.tone}`}
                key={agent.role}
                aria-expanded={activeAgent === index}
                aria-controls={`studio-agent-${index}`}
                onClick={() => setActiveAgent(index)}
                onMouseEnter={() => setActiveAgent(index)}
                onMouseLeave={() => setActiveAgent(null)}
                onFocus={() => setActiveAgent(index)}
                onBlur={() => setActiveAgent(null)}
                onKeyDown={(event) => {
                  if (event.key === "Escape") setActiveAgent(null);
                }}
              >
                <span
                  className="studio-role-dot"
                  style={{ background: agent.color }}
                />
                {agent.role}
                <span
                  id={`studio-agent-${index}`}
                  className="studio-agent-tooltip"
                  hidden={activeAgent !== index}
                >
                  {agent.description}
                </span>
              </button>
            ))}
          </div>
        </div>
      </div>
      <div className="studio-container studio-hero-content">
        <h1 id="studio-title">
          Give your idea
          <br />
          <em>room to grow.</em>
        </h1>
        <p>
          A shared plan, specialist agents, and space
          <br className="studio-desktop-break" /> to build what comes next.
        </p>
        <div className="studio-hero-actions">
          <StudioAction onClick={onGetStarted}>Get Zenith Team</StudioAction>
          <a className="studio-button studio-button--subtle" href="#product">
            Take a look inside <ArrowRight size={17} aria-hidden="true" />
          </a>
        </div>
      </div>
    </section>
  );
}
