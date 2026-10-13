import type { DesktopScreenShareSource } from "../../../electron/shared/desktop-bridge.types";
import { useEffect, useState } from "react";
import {
  DesktopScreenSharePermissionError,
  listDesktopScreenShareSources,
  openDesktopScreenShareSettings,
  selectDesktopScreenShareSource,
} from "@/shared/desktop/desktop-environment.service";
import { Button } from "@/shared/ui/core/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Monitor, MonitorDown } from "@/shared/ui/icons";

type Kind = DesktopScreenShareSource["kind"];

// HuddleScreenPicker lets a person choose a screen or window in the desktop
// app, with previews, before anything is captured. Web uses the browser's
// own picker instead.
export function HuddleScreenPicker({
  open,
  onClose,
  onShare,
}: {
  open: boolean;
  onClose: () => void;
  onShare: () => Promise<void>;
}) {
  const [sources, setSources] = useState<DesktopScreenShareSource[]>([]);
  const [kind, setKind] = useState<Kind>("screen");
  const [selectedId, setSelectedId] = useState("");
  const [loading, setLoading] = useState(false);
  const [sharing, setSharing] = useState(false);
  const [error, setError] = useState("");
  const [blocked, setBlocked] = useState(false);
  const [audioSupported, setAudioSupported] = useState(false);
  const [shareAudio, setShareAudio] = useState(false);

  useEffect(() => {
    if (!open) return;
    let active = true;
    setLoading(true);
    setError("");
    setBlocked(false);
    setSelectedId("");
    listDesktopScreenShareSources()
      .then(({ sources: rows, audio }) => {
        if (!active) return;
        setSources(rows);
        setAudioSupported(audio);
        const screens = rows.filter((row) => row.kind === "screen");
        setKind(screens.length ? "screen" : "window");
        // One display needs no choosing beyond confirming it.
        if (screens.length === 1) setSelectedId(screens[0].id);
      })
      .catch((cause: unknown) => {
        if (!active) return;
        setSources([]);
        setBlocked(cause instanceof DesktopScreenSharePermissionError);
        setError(
          cause instanceof Error
            ? cause.message
            : "Could not list screens and windows to share.",
        );
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [open]);

  const visible = sources.filter((source) => source.kind === kind);
  const share = async (sourceId = selectedId) => {
    if (!sourceId || sharing) return;
    setSharing(true);
    setError("");
    try {
      await selectDesktopScreenShareSource(
        sourceId,
        audioSupported && shareAudio,
      );
      await onShare();
      onClose();
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "Could not share screen.",
      );
    } finally {
      setSharing(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="huddle-dialog huddle-screen-picker">
        <DialogHeader>
          <DialogTitle>Share your screen</DialogTitle>
          <DialogDescription>
            Everyone in this Huddle will see what you choose.
          </DialogDescription>
        </DialogHeader>
        {blocked ? (
          <div className="huddle-screen-permission" role="alert">
            <span className="huddle-screen-permission-icon">
              <MonitorDown size={26} aria-hidden="true" />
            </span>
            <strong>Allow screen recording</strong>
            <p>{error}</p>
            <ol>
              <li>Open System Settings → Privacy &amp; Security.</li>
              <li>In Screen &amp; System Audio Recording, turn Zenith on.</li>
              <li>Quit and reopen Zenith.</li>
            </ol>
            <Button
              className="huddle-action-primary"
              onClick={() => openDesktopScreenShareSettings()}
            >
              Open System Settings
            </Button>
          </div>
        ) : (
          <>
            <div
              className="huddle-screen-picker-tabs"
              role="tablist"
              aria-label="What to share"
            >
              {(["screen", "window"] as const).map((tab) => (
                <button
                  key={tab}
                  type="button"
                  role="tab"
                  aria-selected={kind === tab}
                  onClick={() => {
                    setKind(tab);
                    setSelectedId("");
                  }}
                >
                  {tab === "screen" ? "Entire screen" : "Window"}
                  <span>
                    {sources.filter((source) => source.kind === tab).length ||
                      ""}
                  </span>
                </button>
              ))}
            </div>
            <div className="huddle-screen-picker-grid" role="listbox">
              {loading &&
                [0, 1, 2].map((index) => (
                  <span
                    key={index}
                    className="huddle-screen-picker-skeleton"
                    aria-hidden="true"
                  />
                ))}
              {!loading && !error && visible.length === 0 && (
                <p>
                  {kind === "screen"
                    ? "No screens available to share."
                    : "No windows available to share."}
                </p>
              )}
              {visible.map((source) => (
                <button
                  key={source.id}
                  type="button"
                  role="option"
                  aria-selected={selectedId === source.id}
                  className="huddle-screen-picker-source"
                  onClick={() => setSelectedId(source.id)}
                  onDoubleClick={() => void share(source.id)}
                >
                  <span className="huddle-screen-picker-thumb">
                    {source.thumbnail ? (
                      <img src={source.thumbnail} alt="" />
                    ) : (
                      <Monitor size={28} aria-hidden="true" />
                    )}
                  </span>
                  <span className="huddle-screen-picker-name">
                    {source.appIcon && <img src={source.appIcon} alt="" />}
                    <span title={source.name}>{source.name}</span>
                  </span>
                </button>
              ))}
            </div>
            {error && (
              <p role="alert" className="huddle-screen-picker-error">
                {error}
              </p>
            )}
          </>
        )}
        <DialogFooter>
          {audioSupported && !blocked && (
            <label className="huddle-screen-picker-audio">
              <input
                type="checkbox"
                checked={shareAudio}
                onChange={(event) => setShareAudio(event.target.checked)}
              />
              Share computer audio
            </label>
          )}
          <Button variant="ghost" disabled={sharing} onClick={onClose}>
            {blocked ? "Not now" : "Cancel"}
          </Button>
          {!blocked && (
            <Button
              className="huddle-action-primary"
              disabled={!selectedId || sharing}
              onClick={() => void share()}
            >
              {sharing ? "Sharing…" : "Share"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
