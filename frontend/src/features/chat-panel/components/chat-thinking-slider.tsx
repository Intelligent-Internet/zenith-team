import { useEffect, useRef, useState, type CSSProperties } from "react";
import type { RuntimeModel } from "@/shared/api/types";
import { ChatThinkingGalaxy } from "./chat-thinking-galaxy";

const thinkingParticles = Array.from(
  { length: 36 },
  (_, particle) =>
    ({
      left: `${(particle * 37 + 7) % 100}%`,
      top: `${8 + ((particle * 29 + 17) % 80)}%`,
      animationDelay: `${-((particle * 17) % 90) / 10}s`,
      animationDuration: `${3.8 + ((particle * 13) % 29) / 10}s`,
      "--particle-size": `${1.2 + (particle % 4) * 0.4}px`,
      "--particle-brightness": 0.35 + (particle % 5) * 0.1,
      "--particle-drift-x": `${12 + ((particle * 11) % 23)}px`,
      "--particle-drift-y": `${((particle * 7) % 13) - 6}px`,
    }) as CSSProperties,
);

export function ChatThinkingSlider({
  levels,
  value,
  disabled,
  galaxy,
  onPreview,
  onSave,
}: {
  levels: NonNullable<RuntimeModel["reasoning"]>["levels"];
  value: string;
  disabled: boolean;
  galaxy: boolean;
  onPreview: (value: string | null) => void;
  onSave: (value: string) => Promise<void>;
}) {
  const [draft, setDraft] = useState<number | null>(null);
  const [motionPaused, setMotionPaused] = useState(document.hidden);
  const committing = useRef(false);
  const savedIndex = levels.findIndex((level) => level.value === value);
  const index = draft ?? Math.max(0, savedIndex);
  const selected = levels[index];
  const maximum = levels.length - 1;
  useEffect(() => () => onPreview(null), [onPreview]);
  useEffect(() => {
    const updateVisibility = () => setMotionPaused(document.hidden);
    document.addEventListener("visibilitychange", updateVisibility);
    return () =>
      document.removeEventListener("visibilitychange", updateVisibility);
  }, []);

  async function commit(next: number) {
    if (disabled || committing.current) return;
    const level = levels[next];
    if (!level || level.value === value) {
      setDraft(null);
      onPreview(null);
      return;
    }
    committing.current = true;
    try {
      await onSave(level.value);
    } finally {
      committing.current = false;
      setDraft(null);
      onPreview(null);
    }
  }

  return (
    <div className="chat-thinking-slider">
      <div
        className="chat-thinking-track"
        data-galaxy={galaxy}
        data-motion-paused={motionPaused}
        style={
          {
            "--thinking-progress": maximum > 0 ? index / maximum : 0,
          } as CSSProperties
        }
      >
        <div className="chat-thinking-rail" aria-hidden>
          <div className="chat-thinking-fill">
            <div className="chat-thinking-particles">
              {thinkingParticles.map((style, particle) => (
                <i key={particle} style={style} />
              ))}
            </div>
          </div>
          <ChatThinkingGalaxy active={galaxy && !motionPaused} />
          <div className="chat-thinking-ticks">
            {levels.map((level, tick) => (
              <span key={level.value} data-filled={tick <= index} />
            ))}
          </div>
        </div>
        <div className="chat-thinking-halo" aria-hidden />
        <input
          type="range"
          min={0}
          max={maximum}
          step={1}
          value={index}
          disabled={maximum === 0}
          aria-disabled={disabled}
          aria-label="Thinking level"
          aria-valuetext={selected?.label ?? value}
          onPointerDown={(event) => {
            if (disabled) event.preventDefault();
          }}
          onKeyDown={(event) => {
            if (disabled && event.key !== "Tab" && event.key !== "Escape")
              event.preventDefault();
          }}
          onChange={(event) => {
            if (disabled) return;
            const next = Number(event.currentTarget.value);
            setDraft(next);
            onPreview(levels[next]?.value ?? null);
          }}
          onPointerUp={(event) =>
            void commit(Number(event.currentTarget.value))
          }
          onPointerCancel={() => {
            setDraft(null);
            onPreview(null);
          }}
          onKeyUp={(event) => {
            if (
              [
                "ArrowLeft",
                "ArrowRight",
                "ArrowUp",
                "ArrowDown",
                "Home",
                "End",
                "PageUp",
                "PageDown",
              ].includes(event.key)
            ) {
              void commit(Number(event.currentTarget.value));
            }
          }}
          onBlur={(event) => {
            if (draft !== null) void commit(Number(event.currentTarget.value));
          }}
        />
      </div>
      <div className="chat-thinking-scale" aria-hidden>
        <span>{levels[0]?.label}</span>
        <span>{levels[maximum]?.label}</span>
      </div>
    </div>
  );
}
