import { useEffect, useRef, useState } from "react";
import { Button } from "@/shared/ui/button";
import { Check, Download, LoaderCircle, RotateCw } from "@/shared/ui/icons";
import "../styles/artifact-download-button.css";

export function ArtifactDownloadButton({
  onDownload,
  variant = "outline",
}: {
  onDownload: () => Promise<void>;
  variant?: "outline" | "ghost";
}) {
  const [status, setStatus] = useState<"idle" | "loading" | "ready" | "error">(
    "idle",
  );
  const pending = useRef(false);
  const mounted = useRef(true);
  const resetTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      clearTimeout(resetTimer.current);
    };
  }, []);

  const download = async () => {
    if (pending.current) return;
    pending.current = true;
    clearTimeout(resetTimer.current);
    setStatus("loading");
    try {
      await onDownload();
      if (!mounted.current) return;
      setStatus("ready");
      resetTimer.current = setTimeout(() => setStatus("idle"), 3000);
    } catch {
      if (mounted.current) setStatus("error");
    } finally {
      pending.current = false;
    }
  };
  const Icon =
    status === "loading"
      ? LoaderCircle
      : status === "ready"
        ? Check
        : status === "error"
          ? RotateCw
          : Download;
  const label =
    status === "loading"
      ? "Downloading…"
      : status === "ready"
        ? "Ready"
        : status === "error"
          ? "Try again"
          : "Download";
  return (
    <span className="artifact-download-control">
      <Button
        variant={variant}
        className="artifact-download-button"
        data-state={status}
        disabled={status === "loading"}
        aria-busy={status === "loading"}
        onClick={() => void download()}
      >
        <span key={status} className="artifact-download-content">
          <Icon size={16} aria-hidden />
          <span>{label}</span>
        </span>
      </Button>
      <span className="sr-only" role="status">
        {status === "ready"
          ? "File sent to your browser. Check your downloads to save or open it."
          : status === "error"
            ? "Download failed. Please try again."
            : status === "loading"
              ? "Downloading file."
              : ""}
      </span>
    </span>
  );
}
