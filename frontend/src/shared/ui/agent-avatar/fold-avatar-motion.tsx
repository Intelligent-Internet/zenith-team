import { useEffect, useRef, useState } from "react";
import type { FoldAvatarState } from "./fold-avatar";
import "./fold-avatar-motion.css";

const assets = import.meta.glob<string>("./assets/fold-motion/*.webp", {
  eager: true,
  import: "default",
  query: "?url",
});

function poseAsset(
  state: FoldAvatarState,
  kind: "poster" | "motion" | "motion-small",
) {
  return assets[`./assets/fold-motion/${state}-${kind}.webp`];
}

const sheets = new Map<string, Promise<HTMLImageElement>>();
const painters = new Set<(time: number) => boolean>();
let frame: number | null = null;

function paint(time: number) {
  frame = null;
  painters.forEach((draw) => {
    if (!draw(time)) painters.delete(draw);
  });
  if (painters.size) frame = requestAnimationFrame(paint);
}

function subscribe(draw: (time: number) => boolean) {
  painters.add(draw);
  if (frame === null) frame = requestAnimationFrame(paint);
  return () => {
    painters.delete(draw);
    if (!painters.size && frame !== null) {
      cancelAnimationFrame(frame);
      frame = null;
    }
  };
}

function loadSheet(url: string) {
  const cached = sheets.get(url);
  if (cached) return cached;
  const promise = new Promise<HTMLImageElement>((resolve, reject) => {
    const image = new Image();
    image.onload = () => resolve(image);
    image.onerror = () => {
      sheets.delete(url);
      reject(new Error("Avatar motion unavailable"));
    };
    image.src = url;
  });
  sheets.set(url, promise);
  return promise;
}

function channels(hex: string) {
  return [1, 3, 5].map(
    (offset) => parseInt(hex.slice(offset, offset + 2), 16) / 255,
  );
}

type Palette = {
  name: string;
  shadow: string;
  dark: string;
  color: string;
  light: string;
};

/** Match generated cobalt eyelids to the body before applying the Agent palette. */
function PoseColor({ id, palette }: { id: string; palette: Palette }) {
  const tones = [
    "#101626",
    palette.shadow,
    palette.dark,
    palette.color,
    palette.light,
    "#ffffff",
  ].map(channels);
  return (
    <svg className="fold-pose__filters" aria-hidden="true" focusable="false">
      <defs>
        <filter
          id={id}
          colorInterpolationFilters="sRGB"
          x="0"
          y="0"
          width="100%"
          height="100%"
        >
          {/* Opaque intermediates prevent blending from thickening the alpha edges. */}
          <feColorMatrix
            in="SourceGraphic"
            values="1 0 0 0 0  0 1 0 0 0  0 0 1 0 0  0 0 0 0 1"
            result="opaqueSource"
          />
          {/* Lift only red/green below the sampled blue-clay curve; warm eyes stay intact. */}
          <feColorMatrix
            in="opaqueSource"
            values="0 0 .75 0 -.2  0 0 .86 0 -.14  0 0 1 0 0  0 0 0 0 1"
            result="clayToneFloor"
          />
          <feBlend in="opaqueSource" in2="clayToneFloor" mode="lighten" />
          <feComposite in2="SourceAlpha" operator="in" result="material" />
          {palette.name !== "Periwinkle" ? (
            <>
              <feColorMatrix
                in="material"
                values="0 0 0 0 0  0 0 0 0 0  0 0 0 0 0  -6 0 6 0 -.1"
                result="blue"
              />
              <feComposite
                in="blue"
                in2="SourceAlpha"
                operator="in"
                result="clay"
              />
              <feColorMatrix in="material" type="saturate" values="0" />
              <feComponentTransfer>
                <feFuncR
                  type="table"
                  tableValues={tones.map((tone) => tone[0]).join(" ")}
                />
                <feFuncG
                  type="table"
                  tableValues={tones.map((tone) => tone[1]).join(" ")}
                />
                <feFuncB
                  type="table"
                  tableValues={tones.map((tone) => tone[2]).join(" ")}
                />
              </feComponentTransfer>
              <feComposite in2="clay" operator="in" result="colored" />
              <feComposite
                in="material"
                in2="clay"
                operator="out"
                result="face"
              />
              <feComposite
                in="colored"
                in2="face"
                operator="arithmetic"
                k2={1}
                k3={1}
              />
            </>
          ) : null}
        </filter>
      </defs>
    </svg>
  );
}

function PoseFrames({
  compact,
  paused,
  state,
  transition,
}: {
  compact: boolean;
  paused: boolean;
  state: FoldAvatarState;
  transition?: "entering" | "leaving";
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const elapsedRef = useRef(0);
  const [sheet, setSheet] = useState<HTMLImageElement | null>(null);
  const [ready, setReady] = useState(false);
  const once = state === "done" || state === "error" || state === "needs-input";
  const resolution = compact ? 96 : 192;

  useEffect(() => {
    if (paused || sheet) return;
    let cancelled = false;
    void loadSheet(poseAsset(state, compact ? "motion-small" : "motion")).then(
      (image) => {
        if (!cancelled) setSheet(image);
      },
      () => {
        /* Keep the poster when the optional animation cannot load. */
      },
    );
    return () => {
      cancelled = true;
    };
  }, [compact, paused, sheet, state]);

  useEffect(() => {
    const canvas = canvasRef.current;
    const context = canvas?.getContext("2d");
    if (!sheet || !canvas || !context || paused) return;
    // Exported clips are 100 frames, 20 fps, in a 10 × 10 WebP atlas.
    const cell = sheet.naturalWidth / 10;
    const cycle = state === "idle" ? 7000 : 5000;
    const started = performance.now() - elapsedRef.current;
    let previousFrame = -1;
    return subscribe((now) => {
      const elapsed = now - started;
      elapsedRef.current = elapsed;
      const position = once ? elapsed : elapsed % cycle;
      const nextFrame = Math.min(99, Math.floor(position / 50));
      if (nextFrame !== previousFrame) {
        context.clearRect(0, 0, resolution, resolution);
        context.drawImage(
          sheet,
          (nextFrame % 10) * cell,
          Math.floor(nextFrame / 10) * cell,
          cell,
          cell,
          0,
          0,
          resolution,
          resolution,
        );
        canvas.dataset.frame = String(nextFrame);
        if (previousFrame === -1) setReady(true);
        previousFrame = nextFrame;
      }
      return !once || elapsed < 5000;
    });
  }, [once, paused, resolution, sheet, state]);

  return (
    <span
      className="fold-pose__frames"
      data-ready={ready || undefined}
      data-transition={transition}
    >
      <img
        className="fold-pose__poster"
        src={poseAsset(state, "poster")}
        alt=""
        draggable={false}
      />
      <canvas
        className="fold-pose__canvas"
        ref={canvasRef}
        width={resolution}
        height={resolution}
        aria-hidden="true"
      />
    </span>
  );
}

export function FoldAvatarMotion({
  compact,
  id,
  palette,
  paused,
  quiet,
  state,
}: {
  compact: boolean;
  id: string;
  palette: Palette;
  paused: boolean;
  quiet: boolean;
  state: FoldAvatarState;
}) {
  const [pose, setPose] = useState<{
    current: FoldAvatarState;
    previous: FoldAvatarState | null;
  }>({ current: state, previous: null });
  if (pose.current !== state) {
    setPose({ current: state, previous: paused ? null : pose.current });
  }
  useEffect(() => {
    if (!pose.previous) return;
    const timer = window.setTimeout(
      () => setPose((current) => ({ ...current, previous: null })),
      180,
    );
    return () => window.clearTimeout(timer);
  }, [pose.current, pose.previous]);
  const filterId = `${id}-pose-color`;
  return (
    <span className="fold-pose" data-quiet={quiet || undefined}>
      <PoseColor id={filterId} palette={palette} />
      <span className="fold-pose__reaction">
        <span
          className="fold-pose__art"
          style={{ filter: `url(#${filterId})` }}
        >
          {pose.previous ? (
            <PoseFrames
              key={pose.previous}
              compact={compact}
              paused
              state={pose.previous}
              transition="leaving"
            />
          ) : null}
          <PoseFrames
            key={pose.current}
            compact={compact}
            paused={paused || quiet}
            state={pose.current}
            transition={pose.previous ? "entering" : undefined}
          />
        </span>
      </span>
    </span>
  );
}
