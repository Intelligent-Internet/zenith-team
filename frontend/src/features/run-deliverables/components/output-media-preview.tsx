import { useEffect, useRef, useState } from "react";
import {
  ImageIcon,
  LoaderCircle,
  Play,
  Pause,
  RefreshCcw,
} from "@/shared/ui/icons";

import { runDeliverableService } from "@/features/run-deliverables/api/run-deliverable.service";
import type { RunOutputItem } from "@/features/run-deliverables/model/run-deliverable";
import "@/features/run-deliverables/styles/output-preview.css";

export function outputMediaType(item: RunOutputItem) {
  const type = item.contentType.split(";", 1)[0].trim().toLowerCase();
  if (/^image\/(png|jpeg|gif|webp|avif|svg\+xml)$/.test(type)) return "image";
  if (/^video\/(mp4|webm|quicktime|ogg)$/.test(type)) return "video";
  if (/^audio\/(mpeg|mp3|mp4|wav|x-wav|ogg|webm|flac|aac)$/.test(type))
    return "audio";
  return null;
}

export function isFinalOutputMedia(item: RunOutputItem) {
  return (
    (item.role === "primary" || item.role === "final") &&
    Boolean(outputMediaType(item))
  );
}

export function OutputMediaPreview({
  companyId,
  runId,
  artifactId,
  item,
  thumbnail = false,
  canvas = false,
}: {
  companyId: string;
  runId: string;
  artifactId: string;
  item: RunOutputItem;
  thumbnail?: boolean;
  canvas?: boolean;
}) {
  return (
    <MediaPreview
      key={`${companyId}:${runId}:${item.id}`}
      companyId={companyId}
      runId={runId}
      artifactId={artifactId}
      item={item}
      thumbnail={thumbnail}
      canvas={canvas}
    />
  );
}

function MediaPreview({
  companyId,
  runId,
  artifactId,
  item,
  thumbnail = false,
  canvas = false,
}: {
  companyId: string;
  runId: string;
  artifactId: string;
  item: RunOutputItem;
  thumbnail?: boolean;
  canvas?: boolean;
}) {
  const container = useRef<HTMLDivElement>(null);
  const [url, setUrl] = useState<string>();
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const format = outputMediaType(item);
  const tooLarge = item.sizeBytes > 100 * 1024 * 1024;

  useEffect(() => {
    if (!format || tooLarge) return;
    const controller = new AbortController();
    let objectUrl: string | undefined;
    let observer: IntersectionObserver | undefined;
    let started = false;
    setFailed(false);
    setUrl(undefined);
    const load = async () => {
      if (started || controller.signal.aborted) return;
      started = true;
      observer?.disconnect();
      try {
        const blob = await runDeliverableService.downloadItem(
          companyId,
          runId,
          artifactId,
          item.ordinal,
          controller.signal,
        );
        if (controller.signal.aborted) return;
        if (blob.size > 100 * 1024 * 1024)
          throw new Error("Preview is too large");
        objectUrl = URL.createObjectURL(
          new Blob([blob], { type: item.contentType }),
        );
        setUrl(objectUrl);
      } catch {
        if (!controller.signal.aborted) setFailed(true);
      }
    };
    if (container.current && "IntersectionObserver" in window) {
      observer = new IntersectionObserver(
        (entries) => {
          if (entries.some((entry) => entry.isIntersecting)) void load();
        },
        { rootMargin: "100px" },
      );
      observer.observe(container.current);
    } else {
      void load();
    }
    return () => {
      controller.abort();
      observer?.disconnect();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [
    artifactId,
    companyId,
    runId,
    item.ordinal,
    item.contentType,
    format,
    tooLarge,
    attempt,
  ]);

  if (!format) return null;
  if (thumbnail)
    return (
      <div
        className="studio-source-node__thumbnail output-media"
        ref={container}
      >
        {url ? (
          <img src={url} alt="" draggable={false} />
        ) : (
          <ImageIcon size={22} aria-hidden />
        )}
      </div>
    );
  return (
    <div
      className={`output-media output-media--${format}${canvas ? " studio-source-node__media nopan nodrag nowheel" : ""}`}
      ref={container}
      onClick={canvas ? (event) => event.stopPropagation() : undefined}
      onDoubleClick={canvas ? (event) => event.stopPropagation() : undefined}
      onKeyDown={canvas ? (event) => event.stopPropagation() : undefined}
    >
      {tooLarge ? (
        <p>Download this file to view the full result.</p>
      ) : failed ? (
        <div className="output-media__status" role="status">
          <ImageIcon size={22} aria-hidden />
          <span>Preview could not be loaded.</span>
          <button
            type="button"
            onClick={() => setAttempt((value) => value + 1)}
          >
            <RefreshCcw size={14} aria-hidden /> Retry preview
          </button>
        </div>
      ) : !url ? (
        <div className="output-media__status" role="status">
          <LoaderCircle className="animate-spin" size={22} aria-hidden />
          <span>Loading preview…</span>
        </div>
      ) : format === "image" ? (
        <img src={url} alt={item.label} onError={() => setFailed(true)} />
      ) : canvas ? (
        <CanvasMediaPlayer
          key={`${url}:${attempt}`}
          url={url}
          format={format}
          label={item.label}
          onError={() => setFailed(true)}
        />
      ) : format === "video" ? (
        <video
          src={url}
          controls
          playsInline
          preload="metadata"
          aria-label={item.label}
          onError={() => setFailed(true)}
        />
      ) : (
        <audio
          src={url}
          controls
          preload="metadata"
          aria-label={item.label}
          onError={() => setFailed(true)}
        />
      )}
    </div>
  );
}

function CanvasMediaPlayer({
  url,
  format,
  label,
  onError,
}: {
  url: string;
  format: "audio" | "video";
  label: string;
  onError: () => void;
}) {
  const media = useRef<HTMLMediaElement | null>(null);
  const [playing, setPlaying] = useState(false);
  const [hovered, setHovered] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [playError, setPlayError] = useState(false);
  const Media = format === "video" ? "video" : "audio";
  const progress = duration > 0 ? (currentTime / duration) * 100 : 0;

  return (
    <div
      className={`studio-player studio-player--${format}${playing && !hovered ? " is-controls-hidden" : ""}`}
      onPointerEnter={() => setHovered(true)}
      onPointerMove={() => setHovered(true)}
      onPointerLeave={() => setHovered(false)}
    >
      <Media
        ref={(element) => {
          media.current = element;
        }}
        src={url}
        playsInline
        preload={format === "video" ? "auto" : "metadata"}
        aria-label={label}
        onPlay={() => {
          setPlaying(true);
          setHovered(false);
          setPlayError(false);
        }}
        onPause={() => setPlaying(false)}
        onEnded={() => setPlaying(false)}
        onTimeUpdate={(event) =>
          setCurrentTime(event.currentTarget.currentTime)
        }
        onDurationChange={(event) => {
          const value = event.currentTarget.duration;
          setDuration(Number.isFinite(value) ? value : 0);
        }}
        onError={onError}
      />
      <button
        type="button"
        className="studio-player__toggle"
        aria-label={`${playing ? "Pause" : "Play"} ${label}`}
        onClick={() => {
          const element = media.current;
          if (!element) return;
          if (element.paused) {
            setPlayError(false);
            void element.play().catch(() => setPlayError(true));
          } else element.pause();
        }}
      >
        {playing ? (
          <Pause size={22} aria-hidden />
        ) : (
          <Play size={22} aria-hidden />
        )}
      </button>
      {playError ? (
        <span
          className="absolute z-[2] top-[4px] left-[4px] right-[4px] p-[4px] rounded-[4px] [background:var(--workspace-panel-bg)] text-workspace-text text-[length:10px] text-center"
          role="status"
        >
          Unable to play. Try again.
        </span>
      ) : null}
      <div className="studio-player__transport">
        <div className="studio-player__time">
          <span>{mediaTime(currentTime)}</span>
          <span>{duration > 0 ? mediaTime(duration) : "—:—"}</span>
        </div>
        <input
          type="range"
          min={0}
          max={duration || 1}
          step={0.1}
          value={Math.min(currentTime, duration)}
          disabled={!duration}
          aria-label={`Seek ${label}`}
          aria-valuetext={`${mediaTime(currentTime)} of ${mediaTime(duration)}`}
          style={{
            backgroundImage: `linear-gradient(to right, currentColor ${progress}%, var(--studio-player-track) ${progress}%)`,
          }}
          onChange={(event) => {
            const value = Number(event.target.value);
            if (media.current) media.current.currentTime = value;
            setCurrentTime(value);
          }}
        />
      </div>
    </div>
  );
}

function mediaTime(seconds: number) {
  return `${Math.floor(seconds / 60)}:${String(Math.floor(seconds % 60)).padStart(2, "0")}`;
}
