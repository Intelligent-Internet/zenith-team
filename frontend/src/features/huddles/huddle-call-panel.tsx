import {
  CircleX,
  Clock3,
  Megaphone,
  MessageSquareText,
  Mic,
  MicOff,
  Monitor,
  PhoneOff,
  UserPlus,
  Users,
  Video,
  VideoOff,
  WifiOff,
} from "@/shared/ui/icons";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { ConnectionQuality, Track } from "livekit-client";
import {
  inviteToHuddle,
  listHuddleConnections,
  type HuddleConnection,
  type HuddleMedia,
} from "../discussions/api/huddle-api";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { useHuddleRoom, type HuddleVideo } from "./use-huddle-room";
import { useAudioLevel } from "./use-audio-level";
import { HuddleChatPanel } from "./huddle-chat-panel";
import { HuddleWorkProposals } from "./huddle-work-proposals";
import { HuddleDeviceMenu } from "./huddle-device-menu";
import {
  HuddleParticipantTile,
  HuddleScreenTile,
  huddleInitials,
  huddleTone,
  type HuddleTileModel,
} from "./huddle-tile";
import "./huddles.css";
import { huddleAgentStateLabels, useHeliaStatus } from "./huddle-ai-status";
import { HuddleInviteAgents } from "./huddle-invite-agents";
import { useSetHuddleAIMutation, useVoiceAgentsEnabled } from "./huddle-ai-api";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { boardlyRealtimeClient } from "@/shared/realtime/realtime-client.service";
import { HuddleAgentAvatar } from "./huddle-agent-avatar";
import { HuddleScreenPicker } from "./huddle-screen-picker";
import {
  canPickDesktopScreenShareSource,
  reportDesktopScreenShareState,
  subscribeToDesktopScreenShareActions,
} from "@/app/desktop/desktop-environment.service";

type HuddleCallPanelProps = {
  media: HuddleMedia;
  microphoneDeviceId?: string;
  cameraDeviceId?: string;
  microphoneEnabled?: boolean;
  cameraEnabled?: boolean;
  companyId?: string;
  huddleId?: string;
  chatConversationId?: string;
  deadlineAt?: string;
  onLeave?: () => Promise<void> | void;
  onRejoin?: () => void;
};

type Toast = { id: number; text: string; tone: "join" | "leave" | "warning" };

// The server ends every Huddle at its deadline; members are warned this long
// before it (PRD: pilot duration).
const DEADLINE_WARNING_SECONDS = 5 * 60;

const isMac =
  typeof navigator !== "undefined" &&
  /Mac|iPhone|iPad/.test(navigator.platform);
const shortcut = (key: string) => (isMac ? `⌘${key}` : `Ctrl+${key}`);

function gridColumns(count: number) {
  if (count <= 1) return 1;
  if (count <= 4) return 2;
  if (count <= 9) return 3;
  return 4;
}

function formatElapsed(seconds: number) {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = String(seconds % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}

function useElapsed(running: boolean) {
  const [startedAt, setStartedAt] = useState<number | null>(null);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (running && startedAt === null) setStartedAt(Date.now());
  }, [running, startedAt]);
  useEffect(() => {
    if (startedAt === null) return undefined;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [startedAt]);
  return startedAt === null
    ? null
    : Math.max(0, Math.floor((now - startedAt) / 1000));
}

function microphoneErrorMessage(cause: unknown) {
  if (cause instanceof Error) {
    if (cause.name === "NotAllowedError")
      return "Microphone access is blocked. Allow it in your browser or system settings, then try again.";
    if (cause.name === "NotFoundError")
      return "No microphone was found. Connect one and try again.";
    if (cause.name === "NotReadableError")
      return "Your microphone is in use by another app. Close it and try again.";
    if (cause.message) return cause.message;
  }
  return "Could not turn on your microphone.";
}

function useSecondsUntil(deadlineAt: string | undefined) {
  const deadline = deadlineAt ? Date.parse(deadlineAt) : Number.NaN;
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (Number.isNaN(deadline)) return undefined;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [deadline]);
  return Number.isNaN(deadline)
    ? null
    : Math.max(0, Math.ceil((deadline - now) / 1000));
}

export function HuddleCallPanel({
  media,
  microphoneDeviceId,
  cameraDeviceId,
  microphoneEnabled: initialMicrophoneEnabled = false,
  cameraEnabled: initialCameraEnabled = false,
  companyId,
  huddleId,
  chatConversationId,
  deadlineAt,
  onLeave,
  onRejoin,
}: HuddleCallPanelProps) {
  const {
    connected,
    disconnected,
    reconnecting,
    connectionQuality,
    muted,
    audioBlocked,
    error,
    videoError,
    cameraEnabled,
    screenSharing,
    videos,
    members,
    getAudioLevel,
    getLocalInputLevel,
    leave,
    toggleMute,
    switchMicrophone,
    toggleCamera,
    switchCamera,
    toggleScreenShare,
    enableAudio,
  } = useHuddleRoom(media, microphoneDeviceId, {
    microphoneEnabled: initialMicrophoneEnabled,
    cameraEnabled: initialCameraEnabled,
    cameraDeviceId,
  });
  const [chatOpen, setChatOpen] = useState(true);
  const voiceAgents = useVoiceAgentsEnabled(companyId);
  const [screenPickerOpen, setScreenPickerOpen] = useState(false);
  const [sidebarTab, setSidebarTab] = useState<"chat" | "work">("chat");
  const [microphones, setMicrophones] = useState<MediaDeviceInfo[]>([]);
  const [cameras, setCameras] = useState<MediaDeviceInfo[]>([]);
  const [selectedCamera, setSelectedCamera] = useState(cameraDeviceId ?? "");
  const [selectedMicrophone, setSelectedMicrophone] = useState(
    microphoneDeviceId ?? "",
  );
  const [pinnedKey, setPinnedKey] = useState("");
  // A shared screen focused to fill the call; the rail and chat step aside.
  const [focusedScreenKey, setFocusedScreenKey] = useState("");
  const stageRef = useRef<HTMLDivElement>(null);
  const [pageSize, setPageSize] = useState(9);
  const [pagination, setPagination] = useState({ spotlightKey: "", page: 0 });
  const [microphoneError, setMicrophoneError] = useState("");
  const [participants, setParticipants] = useState<HuddleConnection[]>([]);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [mutedNudge, setMutedNudge] = useState(false);
  const [inviteOpen, setInviteOpen] = useState(false);
  const [inviteSearch, setInviteSearch] = useState("");
  const [inviteBusy, setInviteBusy] = useState("");
  const [inviteError, setInviteError] = useState("");
  const [inviteNotice, setInviteNotice] = useState("");
  const session = useSessionSnapshot();
  const people = useListActiveCompanyMembersQuery(companyId ?? "", {
    skip: !companyId || !inviteOpen,
  });
  const elapsed = useElapsed(connected);
  const secondsLeft = useSecondsUntil(deadlineAt);
  const endingSoon =
    connected &&
    secondsLeft !== null &&
    secondsLeft > 0 &&
    secondsLeft <= DEADLINE_WARNING_SECONDS;
  const deadlineWarnedRef = useRef(false);
  const helia = useHeliaStatus(companyId ?? "", huddleId ?? "");
  const [setVoiceParticipation, voiceMutation] = useSetHuddleAIMutation();
  const [voiceError, setVoiceError] = useState("");
  async function removeVoiceAgent() {
    if (!companyId || !huddleId || !helia.status || voiceMutation.isLoading)
      return;
    setVoiceError("");
    try {
      await setVoiceParticipation({
        companyId,
        huddleId,
        body: {
          enabled: false,
          allowCompanyContext: false,
          expectedRevision: helia.status.revision,
          requestKey: crypto.randomUUID(),
        },
      }).unwrap();
    } catch (cause) {
      setVoiceError(getBoardlyQueryErrorMessage(cause));
    }
    void helia.query.refetch();
  }
  const voiceProfile = helia.status?.agent;
  const voiceName = voiceProfile?.name ?? "Voice agent";
  const voiceStatus = helia.state
    ? huddleAgentStateLabels[helia.state]
    : "Connecting…";
  const heliaJoining = helia.state === "pending" || helia.state === "starting";
  const heliaReady = helia.state === "listening" || helia.state === "speaking";

  useEffect(() => {
    const element = stageRef.current;
    if (!element) return;
    const observer = new ResizeObserver(([entry]) => {
      setPageSize(entry.contentRect.width < 640 ? 4 : 9);
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const refresh = async () => {
      const devices = await navigator.mediaDevices?.enumerateDevices();
      setMicrophones(
        devices?.filter((device) => device.kind === "audioinput") ?? [],
      );
      setCameras(
        devices?.filter((device) => device.kind === "videoinput") ?? [],
      );
    };
    void refresh();
    navigator.mediaDevices?.addEventListener?.("devicechange", refresh);
    return () =>
      navigator.mediaDevices?.removeEventListener?.("devicechange", refresh);
  }, []);
  useEffect(() => {
    if (!companyId || !huddleId) return undefined;
    let active = true;
    let latest = 0;
    const refresh = async () => {
      const request = ++latest;
      try {
        const rows = await listHuddleConnections(companyId, huddleId);
        // Back-to-back signals can resolve out of order; keep the newest read.
        if (active && request === latest) setParticipants(rows);
      } catch {
        // Media remains usable when the roster read is temporarily unavailable.
      }
    };
    // The server signals every reservation, confirmation and departure; the
    // slow poll only covers frames lost while the realtime link was down.
    const removeRealtime = boardlyRealtimeClient.onEvent((event) => {
      if (
        event.companyId === companyId &&
        event.type === "huddle.roster_changed" &&
        (event.payload as { huddleId?: string }).huddleId === huddleId
      )
        void refresh();
    });
    const removeRecovery = boardlyRealtimeClient.onRecovery(
      () => void refresh(),
    );
    void refresh();
    const timer = window.setInterval(() => void refresh(), 30_000);
    return () => {
      active = false;
      window.clearInterval(timer);
      removeRealtime();
      removeRecovery();
    };
  }, [companyId, huddleId]);

  const rosterName = useCallback(
    (identity: string, fallback: string) =>
      participants.find((participant) =>
        identity.endsWith(`:user:${participant.userId}`),
      )?.displayName ?? fallback,
    [participants],
  );

  const tiles = useMemo(() => {
    const cameraOf = new Map<string, HuddleVideo>();
    for (const video of videos) {
      if (video.source === Track.Source.Camera && !video.track.isMuted)
        cameraOf.set(video.identity, video);
    }
    const rows: HuddleTileModel[] = members.map((member) => ({
      key: member.identity,
      identity: member.identity,
      name: member.agent ? voiceName : rosterName(member.identity, member.name),
      local: member.local,
      agent: member.agent,
      agentProfile: member.agent ? voiceProfile : undefined,
      agentStatus: member.agent ? voiceStatus : undefined,
      joining: false,
      speaking: member.speaking,
      micMuted: member.micMuted,
      camera: cameraOf.get(member.identity),
    }));
    // Someone who accepted but has not reached the media room yet.
    const seen = new Set<string>();
    for (const participant of participants) {
      if (participant.status !== "reserved" || seen.has(participant.userId))
        continue;
      seen.add(participant.userId);
      if (
        rows.some((row) => row.identity.endsWith(`:user:${participant.userId}`))
      )
        continue;
      rows.push({
        key: `reserved:${participant.userId}`,
        identity: `reserved:${participant.userId}`,
        name: participant.displayName,
        local: false,
        joining: true,
        speaking: false,
        micMuted: true,
      });
    }
    // The invited Agent has not reached the media room yet.
    if (
      (heliaJoining ||
        (helia.state === "unavailable" &&
          !helia.status?.disabled &&
          voiceProfile)) &&
      !members.some((member) => member.agent)
    )
      rows.push({
        key: "helia:joining",
        identity: "helia:joining",
        name: voiceName,
        local: false,
        agent: true,
        agentProfile: voiceProfile,
        agentStatus: voiceStatus,
        joining: heliaJoining,
        speaking: false,
        micMuted: true,
      });
    return rows;
  }, [
    members,
    participants,
    videos,
    rosterName,
    heliaJoining,
    voiceName,
    voiceStatus,
    helia.state,
    helia.status?.disabled,
    voiceProfile,
  ]);

  const screens = videos.filter(
    (video) =>
      video.source === Track.Source.ScreenShare && !video.track.isMuted,
  );
  const liveCount = members.filter((member) => !member.agent).length;
  const inviteCandidates = (people.currentData?.items ?? []).filter(
    (person) =>
      !person.accountDisabled &&
      person.actor.id !== session.userId &&
      !participants.some(
        (participant) =>
          participant.userId === person.actor.id &&
          (participant.status === "reserved" ||
            participant.status === "connected"),
      ) &&
      `${person.actor.name} ${person.email ?? ""}`
        .toLocaleLowerCase()
        .includes(inviteSearch.trim().toLocaleLowerCase()),
  );

  // Pinning wins; otherwise a shared screen takes the stage.
  const pinnedTile = tiles.find((tile) => tile.key === pinnedKey);
  const pinnedScreen = screens.find(
    (screen) => `screen:${screen.id}` === pinnedKey,
  );
  const spotlightScreen = pinnedScreen ?? (pinnedTile ? undefined : screens[0]);
  const spotlightKey = pinnedTile
    ? pinnedTile.key
    : spotlightScreen
      ? `screen:${spotlightScreen.id}`
      : "";
  const focusedScreen = screens.find(
    (screen) => `screen:${screen.id}` === focusedScreenKey,
  );
  const focusing = Boolean(focusedScreen);
  useEffect(() => {
    if (!focusing) return;
    const exit = (event: KeyboardEvent) => {
      // Esc leaves full screen first; the browser handles that one itself.
      if (event.key === "Escape" && !document.fullscreenElement)
        setFocusedScreenKey("");
    };
    window.addEventListener("keydown", exit);
    return () => window.removeEventListener("keydown", exit);
  }, [focusing]);
  const togglePin = (key: string) =>
    setPinnedKey((current) => (current === key ? "" : key));
  const pageItems = [
    ...screens
      .filter((screen) => `screen:${screen.id}` !== spotlightKey)
      .map((screen) => ({ key: `screen:${screen.id}`, screen })),
    ...tiles
      .filter((tile) => tile.key !== spotlightKey)
      .map((tile) => ({ key: tile.key, tile })),
  ];
  const pageCapacity = spotlightKey ? pageSize - 1 : pageSize;
  const pageCount = Math.max(1, Math.ceil(pageItems.length / pageCapacity));
  const page = Math.min(
    pagination.spotlightKey === spotlightKey ? pagination.page : 0,
    pageCount - 1,
  );
  const visibleItems = pageItems.slice(
    page * pageCapacity,
    (page + 1) * pageCapacity,
  );

  // Join and leave announcements, like Meet's corner toasts.
  const previousMembersRef = useRef<Map<string, string> | null>(null);
  const toastIdRef = useRef(0);
  useEffect(() => {
    if (!connected) {
      previousMembersRef.current = null;
      return;
    }
    const current = new Map(
      members
        .filter((member) => !member.local)
        .map((member) => [
          member.identity,
          member.agent ? voiceName : rosterName(member.identity, member.name),
        ]),
    );
    const previous = previousMembersRef.current;
    previousMembersRef.current = current;
    if (!previous) return;
    const next: Toast[] = [];
    for (const [identity, name] of current)
      if (!previous.has(identity))
        next.push({
          id: ++toastIdRef.current,
          text: `${name} joined`,
          tone: "join",
        });
    for (const [identity, name] of previous)
      if (!current.has(identity))
        next.push({
          id: ++toastIdRef.current,
          text: `${name} left`,
          tone: "leave",
        });
    if (next.length === 0) return;
    setToasts((rows) => [...rows, ...next].slice(-3));
    for (const toast of next)
      window.setTimeout(
        () => setToasts((rows) => rows.filter((row) => row.id !== toast.id)),
        3600,
      );
  }, [connected, members, rosterName, voiceName]);

  useEffect(() => {
    if (!endingSoon || deadlineWarnedRef.current) return;
    deadlineWarnedRef.current = true;
    const minutes = Math.max(1, Math.ceil((secondsLeft ?? 0) / 60));
    const toast: Toast = {
      id: ++toastIdRef.current,
      text: `This call ends in ${minutes} ${minutes === 1 ? "minute" : "minutes"}`,
      tone: "warning",
    };
    setToasts((rows) => [...rows, toast].slice(-3));
    window.setTimeout(
      () => setToasts((rows) => rows.filter((row) => row.id !== toast.id)),
      3600,
    );
  }, [endingSoon, secondsLeft]);

  // "Are you talking? You're muted." Measured locally, never transmitted.
  useEffect(() => {
    if (!muted || !connected) {
      setMutedNudge(false);
      return undefined;
    }
    let loud = 0;
    let quietSince = 0;
    const timer = window.setInterval(() => {
      const level = getLocalInputLevel();
      if (level > 0.14) {
        loud += 1;
        quietSince = 0;
        if (loud >= 6) setMutedNudge(true);
      } else {
        loud = Math.max(0, loud - 1);
        quietSince += 1;
        if (quietSince > 30) setMutedNudge(false);
      }
    }, 150);
    return () => window.clearInterval(timer);
  }, [muted, connected, getLocalInputLevel]);

  const handleLeave = async () => {
    if (onLeave) {
      await onLeave();
      return;
    }
    await leave();
  };

  const toggleMicrophone = async () => {
    try {
      await toggleMute();
      setMicrophoneError("");
    } catch (cause) {
      setMicrophoneError(microphoneErrorMessage(cause));
    }
  };

  // Meet-style shortcuts: toggle microphone and camera.
  const shortcutsRef = useRef({
    toggleMicrophone,
    toggleCamera,
    selectedCamera,
  });
  shortcutsRef.current = { toggleMicrophone, toggleCamera, selectedCamera };

  // The desktop sharing toolbar works the call from over other apps.
  const shareControlsRef = useRef({ toggleScreenShare, screenSharing });
  shareControlsRef.current = { toggleScreenShare, screenSharing };
  useEffect(
    () =>
      subscribeToDesktopScreenShareActions((action) => {
        if (action === "mute") void shortcutsRef.current.toggleMicrophone();
        else if (action === "camera")
          void shortcutsRef.current.toggleCamera(
            shortcutsRef.current.selectedCamera,
          );
        else if (shareControlsRef.current.screenSharing)
          void shareControlsRef.current.toggleScreenShare();
      }),
    [],
  );
  useEffect(() => {
    reportDesktopScreenShareState({
      sharing: screenSharing,
      muted,
      camera: cameraEnabled,
    });
  }, [screenSharing, muted, cameraEnabled]);
  useEffect(
    () => () =>
      reportDesktopScreenShareState({
        sharing: false,
        muted: false,
        camera: false,
      }),
    [],
  );
  useEffect(() => {
    if (!connected) return undefined;
    const onKey = (event: KeyboardEvent) => {
      if (!(isMac ? event.metaKey : event.ctrlKey) || event.altKey) return;
      const key = event.key.toLowerCase();
      if (key === "d") {
        event.preventDefault();
        void shortcutsRef.current.toggleMicrophone();
      } else if (key === "e") {
        event.preventDefault();
        void shortcutsRef.current.toggleCamera(
          shortcutsRef.current.selectedCamera,
        );
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [connected]);

  const localIdentity = members.find((member) => member.local)?.identity;
  const readLocalLevel = useCallback(
    () => (localIdentity ? getAudioLevel(localIdentity) : 0),
    [getAudioLevel, localIdentity],
  );
  const micButtonRef = useRef<HTMLButtonElement>(null);
  useAudioLevel(micButtonRef, muted ? null : readLocalLevel);

  const poorNetwork = connectionQuality === ConnectionQuality.Poor;
  const lostNetwork = connectionQuality === ConnectionQuality.Lost;
  const alone = connected && liveCount <= 1 && tiles.length <= 1;
  const heliaHearsYou = heliaReady && members.some((member) => member.agent);

  const renderTile = (tile: HuddleTileModel, compact: boolean) => (
    <HuddleParticipantTile
      key={tile.key}
      tile={tile}
      compact={compact}
      pinned={pinnedKey === tile.key}
      getAudioLevel={getAudioLevel}
      onPin={() => togglePin(tile.key)}
      onRemoveAgent={
        tile.agent && helia.status?.canModerate && !helia.status.disabled
          ? removeVoiceAgent
          : undefined
      }
      removingAgent={voiceMutation.isLoading}
      agentError={tile.agent ? voiceError : undefined}
    />
  );
  const renderScreen = (screen: HuddleVideo, compact: boolean) => (
    <HuddleScreenTile
      key={`screen:${screen.id}`}
      video={screen}
      name={screen.local ? "You" : rosterName(screen.identity, screen.name)}
      compact={compact}
      pinned={pinnedKey === `screen:${screen.id}`}
      focused={focusedScreen === screen}
      onPin={() => togglePin(`screen:${screen.id}`)}
      onFocus={
        compact
          ? undefined
          : () =>
              setFocusedScreenKey((current) =>
                current === `screen:${screen.id}` ? "" : `screen:${screen.id}`,
              )
      }
      onStopSharing={() => void toggleScreenShare()}
    />
  );
  const renderPage = (compact: boolean) =>
    visibleItems.map((item) =>
      "screen" in item
        ? renderScreen(item.screen, compact)
        : renderTile(item.tile, compact),
    );

  let stage: ReactNode;
  if (!connected && tiles.length === 0) {
    stage = (
      <div className="huddle-stage-empty">
        <div className="huddle-stage-empty-mark">
          <Users size={30} />
        </div>
        <h3>{reconnecting ? "Reconnecting…" : "Getting everyone together"}</h3>
        <p>Connecting your audio and loading the room…</p>
        <span className="huddle-tile-joining-dots" aria-hidden>
          <i />
          <i />
          <i />
        </span>
      </div>
    );
  } else if (focusedScreen) {
    stage = (
      <div className="huddle-focus">{renderScreen(focusedScreen, false)}</div>
    );
  } else if (spotlightKey) {
    stage = (
      <div
        className="huddle-spotlight"
        data-screen={!pinnedTile && Boolean(spotlightScreen)}
      >
        <div className="huddle-spotlight-main">
          {pinnedTile
            ? renderTile(pinnedTile, false)
            : spotlightScreen && renderScreen(spotlightScreen, false)}
        </div>
        {visibleItems.length > 0 && (
          <div className="huddle-rail" aria-label="Other participants">
            {renderPage(true)}
          </div>
        )}
      </div>
    );
  } else {
    const columns = gridColumns(visibleItems.length);
    stage = (
      <div
        className="huddle-grid"
        data-count={visibleItems.length}
        style={
          {
            "--cols": columns,
            "--rows": Math.ceil(visibleItems.length / columns),
          } as CSSProperties
        }
      >
        {renderPage(false)}
      </div>
    );
  }

  const stackMembers = tiles.filter((tile) => !tile.joining && !tile.agent);
  return (
    <section
      className="huddle-call"
      data-chat-open={chatOpen && !focusedScreen}
      data-focus={Boolean(focusedScreen)}
      aria-label="Huddle call"
    >
      <header className="huddle-stage-header">
        <div className="huddle-title">
          <span
            className="huddle-live-dot"
            data-state={error ? "error" : connected ? "live" : "connecting"}
            aria-hidden
          />
          <h2>Huddle</h2>
          {elapsed !== null && (
            <span className="huddle-elapsed" aria-label="Call duration">
              {formatElapsed(elapsed)}
            </span>
          )}
        </div>
        <div className="huddle-header-meta">
          {endingSoon && secondsLeft !== null && (
            <span className="huddle-deadline">
              <Clock3 size={14} />
              Ends in {formatElapsed(secondsLeft)}
            </span>
          )}
          {(poorNetwork || lostNetwork || reconnecting) && (
            <span className="huddle-network" role="status">
              <WifiOff size={14} />
              {reconnecting
                ? "Reconnecting…"
                : lostNetwork
                  ? "Connection lost"
                  : "Unstable network"}
            </span>
          )}
          <span className="huddle-presence" aria-label={`${liveCount} in call`}>
            <span className="huddle-avatar-stack" aria-hidden>
              {stackMembers.slice(0, 3).map((tile) => (
                <span key={tile.key} data-tone={huddleTone(tile.identity)}>
                  {huddleInitials(tile.name)}
                </span>
              ))}
            </span>
            {liveCount} in call
          </span>
        </div>
      </header>

      <div
        ref={stageRef}
        className="huddle-stage"
        data-reconnecting={reconnecting}
      >
        {(error || audioBlocked || disconnected) && (
          <div className="huddle-banners">
            {disconnected && (
              <div className="huddle-call-error" role="alert">
                You disconnected from this call
                {helia.state === "listening" || helia.state === "speaking"
                  ? `, so ${voiceName} can’t hear you.`
                  : "."}
                {onRejoin && (
                  <button
                    type="button"
                    className="underline ml-2"
                    onClick={onRejoin}
                  >
                    Rejoin call
                  </button>
                )}
              </div>
            )}
            {error && (
              <p className="huddle-call-error" role="alert">
                {error.message}
              </p>
            )}
            {audioBlocked && (
              <button
                type="button"
                className="huddle-audio-blocked"
                onClick={() => void enableAudio()}
              >
                <Megaphone size={16} /> Your browser paused call audio. Click to
                hear everyone.
              </button>
            )}
          </div>
        )}
        {stage}
        {pageCount > 1 && (
          <nav className="huddle-pagination" aria-label="Participant pages">
            <button
              type="button"
              disabled={page === 0}
              onClick={() => setPagination({ spotlightKey, page: page - 1 })}
            >
              Previous
            </button>
            <span role="status">
              {page + 1} / {pageCount}
            </span>
            <button
              type="button"
              disabled={page === pageCount - 1}
              onClick={() => setPagination({ spotlightKey, page: page + 1 })}
            >
              Next
            </button>
          </nav>
        )}
        {alone && !spotlightKey && (
          <p className="huddle-alone-hint">
            You’re the only one here. Use Invite members to add people or a
            Voice Agent.
          </p>
        )}
        <div className="huddle-toasts" aria-live="polite">
          {toasts.map((toast) => (
            <div key={toast.id} className="huddle-toast" data-tone={toast.tone}>
              {toast.text}
            </div>
          ))}
        </div>
      </div>

      <aside
        id="huddle-chat-sidebar"
        className="huddle-sidebar"
        hidden={!chatOpen || Boolean(focusedScreen)}
        aria-label="Huddle messages and work proposals"
      >
        <header className="huddle-chat-header">
          <button
            type="button"
            className="huddle-sidebar-choice"
            aria-pressed={sidebarTab === "chat"}
            onClick={() => setSidebarTab("chat")}
          >
            Chat
          </button>
          <button
            type="button"
            className="huddle-sidebar-choice"
            aria-pressed={sidebarTab === "work"}
            onClick={() => setSidebarTab("work")}
          >
            Work proposals
            {helia.status?.proposals?.length
              ? ` (${helia.status.proposals.length})`
              : ""}
          </button>
          <button
            type="button"
            className="huddle-chat-close"
            aria-label="Hide Huddle sidebar"
            onClick={() => setChatOpen(false)}
          >
            <CircleX size={18} />
          </button>
        </header>
        <div className="huddle-sidebar-view" hidden={sidebarTab !== "work"}>
          {companyId && huddleId ? (
            <HuddleWorkProposals
              companyId={companyId}
              huddleId={huddleId}
              proposals={helia.status?.proposals ?? []}
              stale={helia.query.isError || !helia.status}
              onRefresh={() => {
                void helia.query.refetch();
              }}
            />
          ) : (
            <p className="huddle-chat-pending">
              Work proposals are connecting…
            </p>
          )}
        </div>
        <div className="huddle-sidebar-view" hidden={sidebarTab !== "chat"}>
          {companyId && chatConversationId ? (
            <HuddleChatPanel
              key={`${companyId}:${chatConversationId}`}
              companyId={companyId}
              conversationId={chatConversationId}
            />
          ) : (
            <p className="huddle-chat-pending">Chat is connecting…</p>
          )}
        </div>
      </aside>

      <footer className="huddle-controls">
        <div className="huddle-controls-start">
          {mutedNudge ? (
            <button
              type="button"
              className="huddle-muted-nudge"
              onClick={() => void toggleMicrophone()}
            >
              <MicOff size={15} /> You’re muted — click to talk
            </button>
          ) : heliaHearsYou ? (
            <p className="huddle-helia-hint" role="status">
              <span className="huddle-helia-mark" aria-hidden>
                <HuddleAgentAvatar size={24} profile={voiceProfile} />
              </span>
              <span>
                {muted ? "Unmute and say " : "Say "}
                <strong>“{voiceName}, …”</strong> to ask
              </span>
            </p>
          ) : (
            <p className="huddle-mic-hint" role="status">
              {muted ? "Your microphone is off" : "Your microphone is on"}
            </p>
          )}
        </div>
        <div className="huddle-control-dock">
          <div className="huddle-split" data-danger={muted}>
            <button
              ref={micButtonRef}
              type="button"
              onClick={() => void toggleMicrophone()}
              disabled={!connected}
              className="huddle-control huddle-mic-control"
              data-muted={muted}
              aria-pressed={!muted}
              aria-label={muted ? "Unmute microphone" : "Mute microphone"}
              data-tooltip={`${muted ? "Unmute" : "Mute"} (${shortcut("D")})`}
            >
              {muted ? <MicOff size={20} /> : <Mic size={20} />}
            </button>
            <HuddleDeviceMenu
              label="Microphone"
              fallbackName="Microphone"
              devices={microphones}
              selected={selectedMicrophone}
              disabled={!connected}
              onSelect={async (next) => {
                try {
                  await switchMicrophone(next);
                  setSelectedMicrophone(next);
                  setMicrophoneError("");
                } catch (cause) {
                  setMicrophoneError(
                    cause instanceof Error
                      ? cause.message
                      : "Could not switch microphone.",
                  );
                }
              }}
            />
          </div>
          <div className="huddle-split">
            <button
              type="button"
              className="huddle-control"
              data-active={cameraEnabled}
              disabled={!connected}
              aria-pressed={cameraEnabled}
              aria-label={cameraEnabled ? "Turn off camera" : "Turn on camera"}
              data-tooltip={`${cameraEnabled ? "Turn off camera" : "Turn on camera"} (${shortcut("E")})`}
              onClick={() => void toggleCamera(selectedCamera)}
            >
              {cameraEnabled ? <Video size={20} /> : <VideoOff size={20} />}
            </button>
            <HuddleDeviceMenu
              label="Camera"
              fallbackName="Camera"
              devices={cameras}
              selected={selectedCamera}
              disabled={!connected}
              onSelect={async (next) => {
                if (!cameraEnabled || (await switchCamera(next)))
                  setSelectedCamera(next);
              }}
            />
          </div>
          <button
            type="button"
            className="huddle-control"
            data-active={screenSharing}
            disabled={!connected}
            aria-pressed={screenSharing}
            aria-label={screenSharing ? "Stop presenting" : "Share screen"}
            data-tooltip={screenSharing ? "Stop presenting" : "Share screen"}
            onClick={() =>
              !screenSharing && canPickDesktopScreenShareSource()
                ? setScreenPickerOpen(true)
                : void toggleScreenShare()
            }
          >
            <Monitor size={20} />
          </button>
          <button
            type="button"
            className="huddle-control"
            data-active={chatOpen && !focusedScreen}
            aria-label={
              chatOpen && !focusedScreen
                ? "Hide Huddle chat"
                : "Show Huddle chat"
            }
            aria-pressed={chatOpen && !focusedScreen}
            aria-controls="huddle-chat-sidebar"
            data-tooltip="In-call messages"
            onClick={() => {
              // Opening chat while focused brings the call layout back.
              if (focusedScreen) {
                setFocusedScreenKey("");
                setChatOpen(true);
              } else setChatOpen(!chatOpen);
            }}
          >
            <MessageSquareText size={20} />
          </button>
          <div className="huddle-invite-wrap">
            <button
              type="button"
              className="huddle-control"
              disabled={!connected || !companyId || !huddleId}
              aria-label="Invite members"
              aria-expanded={inviteOpen}
              aria-haspopup="dialog"
              data-tooltip="Invite members"
              onClick={() => {
                setInviteOpen(!inviteOpen);
                setInviteError("");
                setInviteNotice("");
              }}
            >
              <UserPlus size={20} />
            </button>
            {inviteOpen && companyId && huddleId && (
              <div
                className="huddle-invite-popover"
                role="dialog"
                aria-label="Invite members to Huddle"
              >
                <div className="huddle-invite-heading">
                  <strong>Invite members</strong>
                  <button
                    type="button"
                    aria-label="Close invitations"
                    onClick={() => setInviteOpen(false)}
                  >
                    <CircleX size={17} />
                  </button>
                </div>
                <input
                  aria-label="Search people and Agents"
                  placeholder="Search people and Agents"
                  value={inviteSearch}
                  onChange={(event) => setInviteSearch(event.target.value)}
                />
                <strong className="huddle-invite-group-title">People</strong>
                <div className="huddle-invite-list">
                  {inviteCandidates.slice(0, 8).map((person) => (
                    <button
                      key={person.actor.id}
                      type="button"
                      disabled={Boolean(inviteBusy)}
                      onClick={() => {
                        setInviteBusy(person.actor.id);
                        setInviteError("");
                        setInviteNotice("");
                        void inviteToHuddle(
                          companyId,
                          huddleId,
                          person.actor.id,
                        )
                          .then(() => {
                            setInviteNotice(`Invited ${person.actor.name}`);
                            setInviteSearch("");
                          })
                          .catch((cause) =>
                            setInviteError(
                              cause instanceof Error
                                ? cause.message
                                : "Could not invite this person.",
                            ),
                          )
                          .finally(() => setInviteBusy(""));
                      }}
                    >
                      <span>{person.actor.name}</span>
                      <UserPlus size={15} />
                    </button>
                  ))}
                  {!people.isLoading && inviteCandidates.length === 0 && (
                    <p>No available team members found.</p>
                  )}
                </div>
                {inviteError && (
                  <p role="alert" className="huddle-invite-error">
                    {inviteError}
                  </p>
                )}
                {inviteNotice && (
                  <p role="status" className="huddle-invite-notice">
                    {inviteNotice}
                  </p>
                )}
                {voiceAgents && (
                  <HuddleInviteAgents
                    companyId={companyId}
                    huddleId={huddleId}
                    search={inviteSearch}
                  />
                )}
              </div>
            )}
          </div>
          <span className="huddle-dock-divider" aria-hidden />
          <button
            type="button"
            onClick={() => void handleLeave()}
            className="huddle-control huddle-control-leave"
            aria-label="Leave Huddle"
            data-tooltip="Leave Huddle"
          >
            <PhoneOff size={20} />
            <span>Leave</span>
          </button>
        </div>
        <div className="huddle-controls-end">
          {(microphoneError || videoError) && (
            <p className="huddle-device-error" role="alert">
              {microphoneError || videoError}
            </p>
          )}
        </div>
      </footer>
      <HuddleScreenPicker
        open={screenPickerOpen && connected && !screenSharing}
        onClose={() => setScreenPickerOpen(false)}
        onShare={toggleScreenShare}
      />
    </section>
  );
}
