import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import { Maximize2, Megaphone, Pause, Play, X } from "@/shared/ui/icons";
import { toast, Toaster } from "@/shared/ui/sonner";

function playerTime(value: number) {
  const seconds = Math.max(0, Math.floor(value || 0));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

export function StudioPlayer({
  source,
  poster,
  label,
  kind,
  onReady,
  onError,
  onActionError,
}: {
  source: string;
  poster?: string;
  label: string;
  kind: "video" | "audio";
  onReady: () => void;
  onError: () => void;
  onActionError?: (error: unknown) => void;
}) {
  const [validPoster, setValidPoster] = useState<string>();
  useEffect(() => {
    setValidPoster(undefined);
    if (!poster || kind !== "video") return;
    const image = new Image();
    image.onload = () => setValidPoster(poster);
    image.src = poster;
    return () => {
      image.onload = null;
    };
  }, [poster, kind]);
  const [expanded, setExpanded] = useState(false);
  const toasterId = useId();
  const toastLayer = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const layer = toastLayer.current;
    if (!expanded || !layer) return;
    layer.showPopover();
    return () => layer.hidePopover();
  }, [expanded]);
  const dialog = useRef<HTMLDialogElement | null>(null);
  const expandButton = useRef<HTMLButtonElement | null>(null);
  const Container = kind === "video" ? "dialog" : "div";
  const element = useRef<HTMLVideoElement & HTMLAudioElement>(null);
  const [playing, setPlaying] = useState(false);
  const [muted, setMuted] = useState(kind === "video");
  const [duration, setDuration] = useState(0);
  const [aspectRatio, setAspectRatio] = useState(16 / 9);
  const [time, setTime] = useState(0);
  const [ready, setReady] = useState(false);
  const notifyError = (message: string) => {
    if (expanded)
      toast.error(message, {
        id: `${toasterId}:${message}`,
        toasterId,
        position: "bottom-right",
      });
    else onActionError?.(new Error(message));
  };
  const togglePlayback = async () => {
    const media = element.current;
    if (!media) return;
    if (!media.paused) media.pause();
    else {
      try {
        await media.play();
      } catch {
        notifyError("Playback could not start. Try again.");
      }
    }
  };
  const mediaProps = {
    ref: element,
    src: source,
    preload: "metadata",
    muted,
    onLoadedMetadata: () => {
      const value = element.current?.duration ?? 0;
      setDuration(Number.isFinite(value) ? value : 0);
      const media = element.current;
      if (
        media instanceof HTMLVideoElement &&
        media.videoWidth &&
        media.videoHeight
      )
        setAspectRatio(media.videoWidth / media.videoHeight);
      setReady(true);
      onReady();
    },
    onDurationChange: () => {
      const value = element.current?.duration ?? 0;
      setDuration(Number.isFinite(value) ? value : 0);
    },
    onTimeUpdate: () => {
      const next = element.current?.currentTime ?? 0;
      setTime(next);
    },
    onPlay: () => setPlaying(true),
    onPause: () => setPlaying(false),
    onEnded: () => setPlaying(false),
    onError,
    "aria-label": label,
  };
  return (
    <Container
      ref={(node: HTMLDialogElement | HTMLDivElement | null) => {
        dialog.current = node instanceof HTMLDialogElement ? node : null;
      }}
      className={`studio-player studio-player--${kind}${playing ? " is-playing" : ""}${expanded ? " is-expanded nodrag nopan nowheel" : ""}`}
      role={expanded ? "dialog" : "group"}
      aria-label={label}
      aria-modal={expanded || undefined}
      onClose={() => {
        element.current?.pause();
        setPlaying(false);
        setExpanded(false);
        requestAnimationFrame(() => expandButton.current?.focus());
      }}
      onKeyDown={(event) => {
        if (!expanded) return;
        event.stopPropagation();
        if (event.key === "Escape") {
          event.preventDefault();
          dialog.current?.close();
        }
      }}
      onPointerDown={(event) => {
        if (expanded) event.stopPropagation();
      }}
      onDoubleClick={(event) => event.stopPropagation()}
      style={
        {
          "--media-ratio": aspectRatio,
          "--media-progress": `${duration ? Math.min(time / duration, 1) * 100 : 0}%`,
        } as CSSProperties
      }
      onClick={(event) => {
        event.stopPropagation();
        if (expanded && event.target === event.currentTarget) {
          const bounds = event.currentTarget.getBoundingClientRect();
          if (
            event.clientX < bounds.left ||
            event.clientX > bounds.right ||
            event.clientY < bounds.top ||
            event.clientY > bounds.bottom
          )
            dialog.current?.close();
        }
      }}
    >
      {kind === "video" ? (
        <video
          {...mediaProps}
          poster={validPoster}
          playsInline
          onClick={() => {
            if (expanded) void togglePlayback();
          }}
        />
      ) : (
        <audio {...mediaProps} />
      )}
      {ready && (
        <>
          <span className="studio-player__duration" aria-hidden>
            {playerTime(duration)}
          </span>
          <button
            className="studio-player__play nodrag nopan"
            type="button"
            aria-label={`${playing ? "Pause" : "Play"} ${label}`}
            onClick={() => void togglePlayback()}
          >
            {playing ? (
              <Pause size={18} aria-hidden />
            ) : (
              <Play size={18} aria-hidden />
            )}
          </button>
        </>
      )}
      {ready && (
        <div className="studio-player__controls nodrag nopan nowheel">
          <input
            type="range"
            min={0}
            max={duration || 0}
            step={0.1}
            value={Math.min(time, duration)}
            disabled={!duration}
            aria-label={`Seek ${label}`}
            aria-valuetext={`${playerTime(time)} of ${playerTime(duration)}`}
            onChange={(event) => {
              const next = Number(event.target.value);
              if (element.current) element.current.currentTime = next;
              setTime(next);
            }}
          />
          <div className="studio-player__toolbar">
            <span className="studio-player__time">
              {playerTime(time)} <span>/ {playerTime(duration)}</span>
            </span>
            <button
              type="button"
              aria-label={muted ? "Unmute" : "Mute"}
              aria-pressed={muted}
              className={muted ? "is-muted" : ""}
              onClick={() => setMuted(!muted)}
            >
              <Megaphone size={17} aria-hidden />
            </button>
            {kind === "video" && (
              <button
                type="button"
                aria-label={`Expand ${label}`}
                ref={expandButton}
                style={expanded ? { display: "none" } : undefined}
                onClick={() => {
                  try {
                    dialog.current?.showModal();
                    setExpanded(true);
                  } catch {
                    notifyError("Video could not be expanded. Try again.");
                  }
                }}
              >
                <Maximize2 size={17} aria-hidden />
              </button>
            )}
          </div>
        </div>
      )}
      {expanded && (
        <button
          className="studio-player__close"
          type="button"
          aria-label="Close expanded video"
          onClick={() => dialog.current?.close()}
        >
          <X size={18} aria-hidden />
        </button>
      )}
      {expanded ? (
        <div
          ref={toastLayer}
          popover="manual"
          className="studio-player__toasts"
        >
          <Toaster
            id={toasterId}
            position="bottom-right"
            closeButton
            richColors
          />
        </div>
      ) : null}
    </Container>
  );
}
