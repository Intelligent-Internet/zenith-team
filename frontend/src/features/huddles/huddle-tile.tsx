import {
  Maximize2,
  MicOff,
  Minimize2,
  Monitor,
  Pin,
  PinOff,
  Scan,
} from "@/shared/ui/icons";
import * as ContextMenu from "@radix-ui/react-context-menu";
import { HuddleAgentAvatar } from "./huddle-agent-avatar";
import type { components } from "@/shared/api/generated/api.generated";
import { useCallback, useEffect, useRef } from "react";
import type { HuddleVideo } from "./use-huddle-room";
import { useAudioLevel } from "./use-audio-level";

export type HuddleTileModel = {
  key: string;
  identity: string;
  name: string;
  local: boolean;
  /** An invited voice Agent. */
  agent?: boolean;
  agentProfile?: components["schemas"]["HuddleVoiceIdentity"];
  agentStatus?: string;
  joining: boolean;
  speaking: boolean;
  micMuted: boolean;
  camera?: HuddleVideo;
};

export function huddleInitials(name: string) {
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return "?";
  const first = words[0]?.[0] ?? "";
  const last = words.length > 1 ? (words[words.length - 1]?.[0] ?? "") : "";
  return (first + last).toUpperCase();
}

export function huddleTone(identity: string) {
  let hash = 0;
  for (const char of identity) hash = (hash * 31 + char.charCodeAt(0)) | 0;
  return Math.abs(hash) % 4;
}

function HuddleVideoSurface({ video }: { video: HuddleVideo }) {
  const elementRef = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    const element = elementRef.current;
    if (!element) return;
    video.track.attach(element);
    return () => {
      video.track.detach(element);
      element.srcObject = null;
    };
  }, [video.track]);

  return (
    <video
      ref={elementRef}
      className="huddle-tile-video"
      autoPlay
      playsInline
      muted={video.local}
      data-local={video.local}
      data-source={video.source}
    />
  );
}

function PinButton({
  name,
  pinned,
  onPin,
}: {
  name: string;
  pinned: boolean;
  onPin: () => void;
}) {
  return (
    <button
      type="button"
      className="huddle-tile-pin"
      aria-pressed={pinned}
      aria-label={pinned ? `Unpin ${name}` : `Pin ${name}`}
      onClick={onPin}
    >
      {pinned ? <PinOff size={16} /> : <Pin size={16} />}
    </button>
  );
}

export function HuddleParticipantTile({
  tile,
  pinned,
  compact = false,
  getAudioLevel,
  onPin,
  onRemoveAgent,
  removingAgent = false,
  agentError,
}: {
  tile: HuddleTileModel;
  pinned: boolean;
  compact?: boolean;
  getAudioLevel: (identity: string) => number;
  onPin: () => void;
  onRemoveAgent?: () => Promise<void>;
  removingAgent?: boolean;
  agentError?: string;
}) {
  const tileRef = useRef<HTMLDivElement>(null);
  const { identity, micMuted, joining } = tile;
  const readLevel = useCallback(
    () => getAudioLevel(identity),
    [getAudioLevel, identity],
  );
  useAudioLevel(tileRef, micMuted || joining ? null : readLevel);

  const label = tile.local ? `${tile.name} (You)` : tile.name;
  const portrait = (
    <div
      ref={tileRef}
      tabIndex={onRemoveAgent ? 0 : undefined}
      className="huddle-tile"
      data-speaking={tile.speaking && !micMuted}
      data-muted={micMuted && !joining}
      data-joining={joining}
      data-agent={Boolean(tile.agent)}
      data-video={Boolean(tile.camera)}
      data-compact={compact}
      data-tone={huddleTone(identity)}
      role="group"
      aria-label={`${label}${joining ? ", joining" : micMuted ? ", muted" : tile.speaking ? ", speaking" : ""}`}
    >
      {tile.camera ? (
        <HuddleVideoSurface video={tile.camera} />
      ) : (
        <div className="huddle-tile-portrait" aria-hidden>
          <span className="huddle-tile-ring" />
          <span className="huddle-tile-ring" data-outer />
          <span className="huddle-tile-avatar">
            {tile.agent ? (
              <HuddleAgentAvatar
                profile={tile.agentProfile}
                size={180}
                className="huddle-tile-agent-character"
                state={
                  joining ? "thinking" : tile.speaking ? "working" : "idle"
                }
              />
            ) : (
              huddleInitials(tile.name)
            )}
          </span>
        </div>
      )}
      {!joining && <PinButton name={label} pinned={pinned} onPin={onPin} />}
      <span className="huddle-tile-name" title={label}>
        <span className="flex flex-col min-w-0 gap-[2px]">
          <span className="min-w-0 overflow-hidden text-ellipsis">
            {tile.name}
          </span>
          {tile.agent && tile.agentStatus && (
            <span
              className="text-[var(--muted-foreground)] text-[length:11px] font-[400]"
              role="status"
            >
              {removingAgent ? "Removing…" : tile.agentStatus}
            </span>
          )}
        </span>
        {tile.local && <em>You</em>}
        {tile.agent && !joining && <em data-agent>AI</em>}
        {joining && !tile.agent && <em>Joining…</em>}
        <span className="huddle-tile-audio" aria-hidden>
          {joining ? (
            <span className="huddle-tile-joining-dots inline-flex gap-[3px]">
              <i />
              <i />
              <i />
            </span>
          ) : micMuted ? (
            <MicOff size={14} />
          ) : (
            <span className="huddle-eq flex items-center gap-[2px] h-[12px]">
              <i />
              <i />
              <i />
            </span>
          )}
        </span>
      </span>
      {agentError && (
        <p
          className="absolute top-[12px] left-[12px] right-[12px] z-[4] p-[8px_12px] rounded-[8px] [background:var(--overlay)] text-[var(--foreground)] text-[length:12px]"
          role="alert"
        >
          {agentError}
        </p>
      )}
    </div>
  );
  if (!onRemoveAgent) return portrait;
  return (
    <ContextMenu.Root>
      <ContextMenu.Trigger asChild>{portrait}</ContextMenu.Trigger>
      <ContextMenu.Portal>
        <ContextMenu.Content
          className="huddle-agent-context-menu z-[100] min-w-[160px] p-[6px] [border:1px_solid_var(--border)] rounded-[12px] [background:var(--background)] text-[var(--foreground)] [box-shadow:0_8px_32px_rgb(0_0_0_/_24%)] text-[length:13px]"
          aria-label={`${tile.name} options`}
        >
          <ContextMenu.Item
            disabled={removingAgent}
            onSelect={() => void onRemoveAgent()}
          >
            {removingAgent ? "Removing…" : `Remove ${tile.name}`}
          </ContextMenu.Item>
        </ContextMenu.Content>
      </ContextMenu.Portal>
    </ContextMenu.Root>
  );
}

export function HuddleScreenTile({
  video,
  name,
  pinned,
  compact = false,
  focused = false,
  onPin,
  onFocus,
  onStopSharing,
}: {
  video: HuddleVideo;
  name: string;
  pinned: boolean;
  compact?: boolean;
  focused?: boolean;
  onPin: () => void;
  onFocus?: () => void;
  onStopSharing: () => void;
}) {
  const presenter = video.local ? "You are" : `${name} is`;
  const tileRef = useRef<HTMLDivElement>(null);
  // Full screen shows only the shared screen; Esc or a double-click leaves.
  const toggleFullScreen = () => {
    const tile = tileRef.current;
    if (!tile) return;
    if (document.fullscreenElement === tile) void document.exitFullscreen();
    else void tile.requestFullscreen().catch(() => {});
  };
  const viewing = !video.local && !compact;
  return (
    <div
      ref={tileRef}
      className="huddle-tile huddle-screen-tile"
      data-compact={compact}
      data-local={video.local}
      data-focused={focused}
      role="group"
      aria-label={`${name}’s shared screen`}
      onDoubleClick={viewing ? toggleFullScreen : undefined}
    >
      <HuddleVideoSurface video={video} />
      {video.local && !compact && (
        <div className="huddle-presenting absolute inset-0 z-[2] flex flex-col items-center justify-center gap-[12px] [background:var(--overlay-strong)] [backdrop-filter:blur(6px)] text-center">
          <Monitor size={22} />
          <p>You are presenting to everyone</p>
          <button type="button" onClick={onStopSharing}>
            Stop presenting
          </button>
        </div>
      )}
      {!focused && (
        <PinButton name={`${name}’s screen`} pinned={pinned} onPin={onPin} />
      )}
      {viewing && (
        <div className="huddle-screen-actions">
          {onFocus && (
            <button
              type="button"
              aria-pressed={focused}
              onClick={onFocus}
              data-tooltip={focused ? "Exit focus (Esc)" : "Focus on screen"}
            >
              {focused ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
              {focused ? "Exit focus" : "Focus"}
            </button>
          )}
          <button
            type="button"
            onClick={toggleFullScreen}
            data-tooltip="Full screen (double-click)"
          >
            <Scan size={16} />
            Full screen
          </button>
        </div>
      )}
      <span className="huddle-tile-name">
        <Monitor size={13} />
        {compact ? name : `${presenter} presenting`}
      </span>
    </div>
  );
}
