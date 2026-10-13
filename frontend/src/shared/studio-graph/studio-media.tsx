import { useEffect, useRef, useState } from "react";
import {
  ImageIcon,
  LoaderCircle,
  Music,
  RefreshCw,
  Video,
} from "@/shared/ui/icons";
import { prepareStudioAudio } from "./studio-audio";
import { StudioPlayer } from "./studio-player";

export function studioMediaIdentity(source: string) {
  try {
    const url = new URL(source);
    const parameters = new Set(
      [...url.searchParams.keys()].map((key) => key.toLowerCase()),
    );
    let signingParameters: string[];
    if (
      parameters.has("x-amz-signature") ||
      parameters.has("x-goog-signature")
    ) {
      const prefix = parameters.has("x-amz-signature") ? "x-amz-" : "x-goog-";
      signingParameters = [
        "algorithm",
        "credential",
        "date",
        "expires",
        "signedheaders",
        "signature",
        "security-token",
      ].map((key) => `${prefix}${key}`);
    } else if (parameters.has("signature") && parameters.has("key-pair-id")) {
      signingParameters = ["signature", "key-pair-id", "policy", "expires"];
    } else if (
      parameters.has("signature") &&
      (parameters.has("googleaccessid") || parameters.has("awsaccesskeyid"))
    ) {
      signingParameters = [
        "signature",
        "googleaccessid",
        "awsaccesskeyid",
        "expires",
        "x-amz-security-token",
      ];
    } else {
      return source;
    }
    // Keep resource versions and image transformations in the media identity.
    const signing = new Set(signingParameters);
    for (const key of [...url.searchParams.keys()])
      if (signing.has(key.toLowerCase())) url.searchParams.delete(key);
    url.searchParams.sort();
    return url.toString();
  } catch {
    return source;
  }
}

// Storage providers put expiry in the signed URL. Do not append cache-busting
// query parameters: those can invalidate the signature itself.
function expiresSoon(source: string) {
  try {
    const params = new URL(source).searchParams;
    const timestamp = params.get("X-Amz-Date") ?? params.get("X-Goog-Date");
    const lifetime =
      params.get("X-Amz-Expires") ?? params.get("X-Goog-Expires");
    if (timestamp && lifetime && /^\d{8}T\d{6}Z$/.test(timestamp)) {
      const date = `${timestamp.slice(0, 4)}-${timestamp.slice(4, 6)}-${timestamp.slice(6, 8)}T${timestamp.slice(9, 11)}:${timestamp.slice(11, 13)}:${timestamp.slice(13, 15)}Z`;
      return Date.parse(date) + Number(lifetime) * 1000 <= Date.now() + 15_000;
    }
    const expires = params.get("Expires");
    return (
      expires !== null &&
      /^\d+$/.test(expires) &&
      Number(expires) * 1000 <= Date.now() + 15_000
    );
  } catch {
    return false;
  }
}

export function StudioMedia({
  source,
  poster,
  label,
  kind,
  format,
  refresh,
  onError,
  onDimensions,
}: {
  source: string;
  poster?: string;
  label: string;
  kind: "image" | "video" | "audio";
  format?: string;
  refresh?: () => Promise<string | undefined>;
  onError?: (error: unknown) => void;
  onDimensions?: (
    dimensions: { width: number; height: number },
    source: string,
  ) => void;
}) {
  const [media, setMedia] = useState<{
    source: string;
    attempt: number;
  } | null>(null);
  const [status, setStatus] = useState<"loading" | "ready" | "error">(
    "loading",
  );
  const generation = useRef(0);
  const refreshed = useRef(false);
  const objectUrl = useRef<string | null>(null);
  const controller = useRef(new AbortController());
  const pending = useRef(false);
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;
  const errorRef = useRef(onError);
  errorRef.current = onError;
  const sourceRef = useRef(source);
  sourceRef.current = source;
  const attemptedSource = useRef("");
  const identity = studioMediaIdentity(source);

  const prepare = async (next: string, current: number, attempt: number) => {
    attemptedSource.current = next;
    const currentController = controller.current;
    const signal = currentController.signal;
    const timer = setTimeout(() => currentController.abort(), 15_000);
    let playable: string;
    try {
      playable =
        kind === "audio"
          ? await prepareStudioAudio(next, format, signal)
          : next;
    } finally {
      clearTimeout(timer);
    }
    if (current !== generation.current) {
      if (playable !== next) URL.revokeObjectURL(playable);
      return;
    }
    if (objectUrl.current) URL.revokeObjectURL(objectUrl.current);
    objectUrl.current = playable !== next ? playable : null;
    setMedia({ source: playable, attempt });
  };

  const recover = async () => {
    if (pending.current) return;
    const current = generation.current;
    controller.current.abort();
    controller.current = new AbortController();
    pending.current = true;
    refreshed.current = true;
    setStatus("loading");
    setMedia(null);
    try {
      const latest = sourceRef.current;
      const next =
        latest !== attemptedSource.current && !expiresSoon(latest)
          ? latest
          : refreshRef.current
            ? await refreshRef.current()
            : latest;
      if (current !== generation.current) return;
      if (!next || expiresSoon(next)) throw new Error("Media URL unavailable");
      await prepare(next, current, current + 1);
    } catch (error: unknown) {
      if (current === generation.current) {
        setStatus("error");
        errorRef.current?.(error);
      }
    } finally {
      if (current === generation.current) pending.current = false;
    }
  };

  useEffect(() => {
    generation.current += 1;
    refreshed.current = false;
    pending.current = false;
    controller.current = new AbortController();
    setStatus("loading");
    setMedia(null);
    const current = generation.current;
    if (expiresSoon(source)) void recover();
    else
      void prepare(source, current, 0).catch(() => {
        if (current === generation.current) void recover();
      });
    return () => {
      generation.current += 1;
      controller.current.abort();
      if (objectUrl.current) URL.revokeObjectURL(objectUrl.current);
      objectUrl.current = null;
    };
  }, [identity, kind, format]);

  const failed = () => {
    if (!refreshed.current && refreshRef.current) void recover();
    else {
      setStatus("error");
      setMedia(null);
      errorRef.current?.(new Error(`${label}: preview could not be loaded.`));
    }
  };
  const ready = () => setStatus("ready");
  const ErrorIcon =
    kind === "image" ? ImageIcon : kind === "video" ? Video : Music;
  return (
    <div
      className="studio-media relative grid place-items-center w-full h-full min-h-0"
      aria-busy={status === "loading"}
      onLoadCapture={(event) => {
        const target = event.target;
        if (
          target instanceof HTMLImageElement &&
          target.naturalWidth &&
          target.naturalHeight
        )
          onDimensions?.(
            { width: target.naturalWidth, height: target.naturalHeight },
            target.currentSrc || target.src,
          );
      }}
      onLoadedMetadataCapture={(event) => {
        const target = event.target;
        if (
          target instanceof HTMLVideoElement &&
          target.videoWidth &&
          target.videoHeight
        )
          onDimensions?.(
            { width: target.videoWidth, height: target.videoHeight },
            target.currentSrc || target.src,
          );
      }}
    >
      {media &&
        (kind === "video" || kind === "audio" ? (
          <StudioPlayer
            key={`${media.source}:${media.attempt}`}
            source={media.source}
            poster={poster}
            label={label}
            kind={kind}
            onReady={ready}
            onError={failed}
            onActionError={onError}
          />
        ) : (
          <img
            key={`${media.source}:${media.attempt}`}
            src={media.source}
            alt={label}
            draggable={false}
            onLoad={ready}
            onError={failed}
            style={{ visibility: status === "ready" ? "visible" : "hidden" }}
          />
        ))}
      {status !== "ready" && (
        <div
          className="studio-media__status absolute inset-0 flex flex-col gap-[8px] items-center justify-center [background:var(--workspace-control-bg)] text-workspace-muted text-[length:12px]"
          role="status"
        >
          {status === "loading" ? (
            <>
              <LoaderCircle
                size={18}
                className="studio-media__spinner"
                aria-hidden
              />
              <span className="sr-only">Loading preview</span>
            </>
          ) : (
            <>
              <ErrorIcon size={20} aria-hidden />
              <span>Preview unavailable</span>
              <button
                className="nodrag nopan"
                type="button"
                onClick={(event) => {
                  event.stopPropagation();
                  void recover();
                }}
              >
                <RefreshCw size={13} aria-hidden />
                Retry
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
