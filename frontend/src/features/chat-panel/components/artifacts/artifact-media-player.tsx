import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import {
  ChevronDown,
  LoaderCircle,
  Maximize2,
  Megaphone,
  Minimize2,
  Music2,
  Pause,
  Play,
} from "@/shared/ui/icons";
import "../../styles/artifact-media-player.css";

export function ArtifactMediaPlayer({
  source,
  format,
  title,
  filename,
  onError,
}: {
  source: string;
  format: "audio" | "video";
  title: string;
  filename: string;
  onError: () => void;
}) {
  const container = useRef<HTMLDivElement>(null);
  const media = useRef<HTMLMediaElement | null>(null);
  const setMediaElement = useCallback((element: HTMLMediaElement | null) => {
    if (!element) media.current?.pause();
    media.current = element;
  }, []);
  const [playing, setPlaying] = useState(false);
  const [buffering, setBuffering] = useState(false);
  const [ready, setReady] = useState(false);
  const [time, setTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [volume, setVolume] = useState(1);
  const [muted, setMuted] = useState(false);
  const [rate, setRate] = useState(1);
  const [fullscreen, setFullscreen] = useState(false);
  const [message, setMessage] = useState("");
  const silent = muted || volume === 0;
  const Media = format === "video" ? "video" : "audio";

  useEffect(() => {
    const updateFullscreen = () =>
      setFullscreen(document.fullscreenElement === container.current);
    document.addEventListener("fullscreenchange", updateFullscreen);
    return () =>
      document.removeEventListener("fullscreenchange", updateFullscreen);
  }, []);

  const togglePlayback = async () => {
    const element = media.current;
    if (!element) return;
    setMessage("");
    if (!element.paused) {
      element.pause();
      return;
    }
    try {
      await element.play();
    } catch {
      if (media.current === element) {
        setBuffering(false);
        setMessage("Playback could not start. Please try again.");
      }
    }
  };

  const toggleMute = () => {
    const element = media.current;
    if (!element) return;
    element.muted = !silent;
    if (silent && element.volume === 0) element.volume = 1;
  };

  const toggleFullscreen = async () => {
    setMessage("");
    try {
      if (document.fullscreenElement === container.current) {
        await document.exitFullscreen();
      } else {
        await container.current?.requestFullscreen();
      }
    } catch {
      setMessage("Fullscreen could not open. Please try again.");
    }
  };

  const readDuration = (element: HTMLMediaElement) => {
    setDuration(Number.isFinite(element.duration) ? element.duration : 0);
  };

  return (
    <div className="artifact-media-stage">
      <div
        ref={container}
        className={`artifact-player artifact-player--${format}`}
        data-playing={playing && !buffering}
        role="group"
        aria-label={`${title} ${format} player`}
      >
        <div className="artifact-player__presentation">
          <Media
            ref={setMediaElement}
            src={source}
            aria-label={title}
            playsInline
            preload="auto"
            onLoadedMetadata={(event) => {
              readDuration(event.currentTarget);
              setReady(true);
            }}
            onDurationChange={(event) => readDuration(event.currentTarget)}
            onTimeUpdate={(event) => setTime(event.currentTarget.currentTime)}
            onPlay={() => setPlaying(true)}
            onPlaying={() => setBuffering(false)}
            onCanPlay={() => setBuffering(false)}
            onWaiting={() => setBuffering(true)}
            onPause={() => {
              setPlaying(false);
              setBuffering(false);
            }}
            onEnded={() => setPlaying(false)}
            onVolumeChange={(event) => {
              setVolume(event.currentTarget.volume);
              setMuted(event.currentTarget.muted);
            }}
            onRateChange={(event) => setRate(event.currentTarget.playbackRate)}
            onError={onError}
          />
          {format === "video" ? (
            <button
              className="artifact-player__screen-toggle"
              type="button"
              aria-label={playing ? "Pause video" : "Play video"}
              disabled={!ready}
              onClick={() => void togglePlayback()}
            >
              {!ready || buffering ? (
                <span className="artifact-player__loading">
                  <LoaderCircle
                    className="artifact-spinner"
                    size={24}
                    aria-hidden
                  />
                </span>
              ) : null}
            </button>
          ) : (
            <div className="artifact-player__audio-heading">
              <div className="artifact-player__artwork" aria-hidden>
                <Music2 size={36} />
              </div>
              <div className="artifact-player__audio-info">
                <span>Audio</span>
                <h2>{title}</h2>
                {filename !== title ? <p>{filename}</p> : null}
              </div>
            </div>
          )}
        </div>
        <div className="artifact-player__controls">
          <button
            className="artifact-player__play"
            type="button"
            aria-label={playing ? "Pause" : "Play"}
            title={playing ? "Pause" : "Play"}
            disabled={!ready}
            onClick={() => void togglePlayback()}
          >
            {!ready || buffering ? (
              <LoaderCircle
                className="artifact-spinner"
                size={20}
                aria-hidden
              />
            ) : playing ? (
              <Pause size={20} aria-hidden />
            ) : (
              <Play size={20} aria-hidden />
            )}
          </button>
          <MediaRange
            label="Playback position"
            value={Math.min(time, duration)}
            max={duration}
            step={0.1}
            valueText={`${mediaTime(time)} of ${mediaTime(duration)}`}
            onChange={(value) => {
              if (media.current) media.current.currentTime = value;
              setTime(value);
            }}
          />
          <span className="artifact-player__time">
            {mediaTime(time)}{" "}
            <span>/ {duration ? mediaTime(duration) : "—:—"}</span>
          </span>
          <div className="artifact-player__volume">
            <button
              type="button"
              aria-label={silent ? "Unmute" : "Mute"}
              title={silent ? "Unmute" : "Mute"}
              aria-pressed={silent}
              onClick={toggleMute}
            >
              <Megaphone size={18} aria-hidden />
            </button>
            <MediaRange
              label="Volume"
              value={silent ? 0 : volume}
              max={1}
              step={0.05}
              valueText={`${Math.round((silent ? 0 : volume) * 100)}%`}
              onChange={(value) => {
                if (!media.current) return;
                media.current.volume = value;
                media.current.muted = value === 0;
              }}
            />
          </div>
          <div className="artifact-player__speed">
            <select
              aria-label="Playback speed"
              title="Playback speed"
              value={rate}
              onChange={(event) => {
                if (media.current)
                  media.current.playbackRate = Number(event.target.value);
              }}
            >
              {[0.5, 0.75, 1, 1.25, 1.5, 2].map((value) => (
                <option key={value} value={value}>
                  {value}×
                </option>
              ))}
            </select>
            <ChevronDown size={12} aria-hidden />
          </div>
          {format === "video" && document.fullscreenEnabled ? (
            <button
              type="button"
              aria-label={fullscreen ? "Exit fullscreen" : "Fullscreen"}
              title={fullscreen ? "Exit fullscreen" : "Fullscreen"}
              onClick={() => void toggleFullscreen()}
            >
              {fullscreen ? (
                <Minimize2 size={18} aria-hidden />
              ) : (
                <Maximize2 size={18} aria-hidden />
              )}
            </button>
          ) : null}
          {message ? (
            <p className="artifact-player__message" role="alert">
              {message}
            </p>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function MediaRange({
  label,
  value,
  max,
  step,
  valueText,
  onChange,
}: {
  label: string;
  value: number;
  max: number;
  step: number;
  valueText: string;
  onChange: (value: number) => void;
}) {
  return (
    <input
      className="artifact-player__range"
      type="range"
      min={0}
      max={max || 1}
      step={step}
      value={value}
      disabled={!max}
      aria-label={label}
      aria-valuetext={valueText}
      style={
        {
          "--media-progress": `${max ? (value / max) * 100 : 0}%`,
        } as CSSProperties
      }
      onChange={(event) => onChange(Number(event.target.value))}
    />
  );
}

function mediaTime(value: number) {
  const seconds = Math.max(0, Math.floor(value));
  const minutes = Math.floor(seconds / 60);
  const remainder = String(seconds % 60).padStart(2, "0");
  return minutes < 60
    ? `${minutes}:${remainder}`
    : `${Math.floor(minutes / 60)}:${String(minutes % 60).padStart(2, "0")}:${remainder}`;
}
