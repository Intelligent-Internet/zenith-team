import { useCallback, useEffect, useRef, useState } from "react";
import {
  ConnectionQuality,
  createAudioAnalyser,
  Room,
  RoomEvent,
  Track,
  TrackEvent,
  type LocalAudioTrack,
  type LocalVideoTrack,
  type RemoteVideoTrack,
} from "livekit-client";
import { startHuddleDiagnostics } from "./huddle-diagnostics";

export type HuddleMedia = { url: string; token: string };
export type HuddleVideo = {
  id: string;
  identity: string;
  name: string;
  source: Track.Source.Camera | Track.Source.ScreenShare;
  local: boolean;
  track: LocalVideoTrack | RemoteVideoTrack;
};
export type HuddleParticipant = {
  identity: string;
  name: string;
  local: boolean;
  /** A LiveKit agent participant, such as Helia. */
  agent: boolean;
  speaking: boolean;
  micMuted: boolean;
};

export function useHuddleRoom(
  media: HuddleMedia | null,
  microphoneDeviceId?: string,
  initialDevices?: {
    microphoneEnabled: boolean;
    cameraEnabled: boolean;
    cameraDeviceId?: string;
  },
) {
  const roomRef = useRef<Room | null>(null);
  const [connected, setConnected] = useState(false);
  const [disconnected, setDisconnected] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  const [connectionQuality, setConnectionQuality] = useState<ConnectionQuality>(
    ConnectionQuality.Unknown,
  );
  const [muted, setMuted] = useState(true);
  const [audioBlocked, setAudioBlocked] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [videoError, setVideoError] = useState("");
  const [cameraEnabled, setCameraEnabled] = useState(false);
  const [screenSharing, setScreenSharing] = useState(false);
  const [videos, setVideos] = useState<HuddleVideo[]>([]);
  const [activeSpeakerId, setActiveSpeakerId] = useState("");
  const [members, setMembers] = useState<HuddleParticipant[]>([]);
  const localAnalyserRef = useRef<ReturnType<
    typeof createAudioAnalyser
  > | null>(null);

  // Read on animation frames by speaking indicators, so it never re-renders.
  // The local microphone is measured directly for instant feedback; remote
  // levels come from the SFU's speaker updates.
  const getAudioLevel = useCallback((identity: string) => {
    const room = roomRef.current;
    if (!room) return 0;
    if (identity === room.localParticipant.identity) {
      if (!room.localParticipant.isMicrophoneEnabled) return 0;
      return (
        localAnalyserRef.current?.calculateVolume() ??
        room.localParticipant.audioLevel
      );
    }
    return room.getParticipantByIdentity(identity)?.audioLevel ?? 0;
  }, []);

  // The analyser reads a cloned track, so it still hears the microphone while
  // the published track is muted. It stays on this device and only powers the
  // "you are muted" hint.
  const getLocalInputLevel = useCallback(
    () => localAnalyserRef.current?.calculateVolume() ?? 0,
    [],
  );

  const enableAudio = useCallback(async () => {
    const room = roomRef.current;
    if (!room) return;
    try {
      await room.startAudio();
      setAudioBlocked(!room.canPlaybackAudio);
    } catch (cause) {
      setError(
        cause instanceof Error ? cause : new Error("Audio playback failed"),
      );
    }
  }, []);

  const leave = useCallback(async () => {
    const room = roomRef.current;
    roomRef.current = null;
    if (room) await room.disconnect();
    setConnected(false);
  }, []);

  // Rejects when the microphone cannot start (permission revoked, device
  // unplugged). The displayed state always follows the actual track.
  const toggleMute = useCallback(async () => {
    const room = roomRef.current;
    if (!room) return;
    try {
      await room.localParticipant.setMicrophoneEnabled(
        !room.localParticipant.isMicrophoneEnabled,
      );
    } finally {
      const next = !room.localParticipant.isMicrophoneEnabled;
      setMuted(next);
      setMembers((rows) =>
        rows.map((row) => (row.local ? { ...row, micMuted: next } : row)),
      );
    }
  }, []);

  const switchMicrophone = useCallback(async (deviceId: string) => {
    const room = roomRef.current;
    if (!room) return;
    const switched = await room.switchActiveDevice(
      "audioinput",
      deviceId || "default",
      false,
    );
    if (!switched) throw new Error("Could not switch microphone.");
  }, []);

  const toggleCamera = useCallback(async (deviceId?: string) => {
    const room = roomRef.current;
    if (!room) return;
    setVideoError("");
    try {
      await room.localParticipant.setCameraEnabled(
        !room.localParticipant.isCameraEnabled,
        deviceId ? { deviceId: { exact: deviceId } } : undefined,
      );
      setCameraEnabled(room.localParticipant.isCameraEnabled);
    } catch (cause) {
      setVideoError(
        cause instanceof Error ? cause.message : "Could not start camera.",
      );
    }
  }, []);

  const switchCamera = useCallback(async (deviceId: string) => {
    const room = roomRef.current;
    if (!room) return false;
    setVideoError("");
    try {
      const switched = await room.switchActiveDevice(
        "videoinput",
        deviceId || "default",
        false,
      );
      if (!switched) throw new Error("Could not switch camera.");
      return true;
    } catch (cause) {
      setVideoError(
        cause instanceof Error ? cause.message : "Could not switch camera.",
      );
      return false;
    }
  }, []);

  const toggleScreenShare = useCallback(async () => {
    const room = roomRef.current;
    if (!room) return;
    setVideoError("");
    try {
      // Audio is offered, not forced: the browser picker (or the desktop
      // picker's Share audio box) decides whether any audio is captured.
      await room.localParticipant.setScreenShareEnabled(
        !room.localParticipant.isScreenShareEnabled,
        { audio: true },
      );
      setScreenSharing(room.localParticipant.isScreenShareEnabled);
    } catch (cause) {
      // Canceling the browser or system picker leaves the call running.
      if (
        cause instanceof Error &&
        (cause.name === "NotAllowedError" || cause.name === "AbortError")
      )
        return;
      setVideoError(
        cause instanceof Error ? cause.message : "Could not share screen.",
      );
    }
  }, []);

  useEffect(() => {
    if (!media) return;
    let cancelled = false;
    const room = new Room({
      adaptiveStream: true,
      dynacast: true,
      audioCaptureDefaults: microphoneDeviceId
        ? { deviceId: { exact: microphoneDeviceId } }
        : undefined,
    });
    const audioElements = new Set<HTMLMediaElement>();
    const diagnostics = startHuddleDiagnostics(room);
    roomRef.current = room;
    setError(null);
    const syncVideo = () => {
      const rows: HuddleVideo[] = [];
      for (const participant of [
        room.localParticipant,
        ...room.remoteParticipants.values(),
      ]) {
        for (const publication of participant.videoTrackPublications.values()) {
          const track = publication.videoTrack;
          if (
            !track ||
            track.isMuted ||
            (publication.source !== Track.Source.Camera &&
              publication.source !== Track.Source.ScreenShare)
          )
            continue;
          rows.push({
            id: `${participant.identity}:${publication.source}`,
            identity: participant.identity,
            name: participant.name || "Participant",
            source: publication.source,
            local: participant === room.localParticipant,
            track,
          });
        }
      }
      setVideos(rows);
      setCameraEnabled(room.localParticipant.isCameraEnabled);
      setScreenSharing(room.localParticipant.isScreenShareEnabled);
      syncMembers();
    };
    const syncMembers = () => {
      setMembers(
        [room.localParticipant, ...room.remoteParticipants.values()]
          .filter(
            (participant) =>
              !(
                participant.isAgent && participant.identity.startsWith("notes:")
              ),
          )
          .map((participant) => ({
            identity: participant.identity,
            name: participant.name || "Participant",
            local: participant === room.localParticipant,
            agent: participant.isAgent,
            speaking: participant.isSpeaking,
            micMuted: !participant.isMicrophoneEnabled,
          })),
      );
    };
    let analysedTrack: LocalAudioTrack | undefined;
    const releaseLocalAnalyser = () => {
      const analyser = localAnalyserRef.current;
      localAnalyserRef.current = null;
      analysedTrack?.off(TrackEvent.Restarted, restartLocalAnalyser);
      analysedTrack = undefined;
      void analyser?.cleanup().catch(() => {});
    };
    const startLocalAnalyser = (track: LocalAudioTrack) => {
      analysedTrack = track;
      track.on(TrackEvent.Restarted, restartLocalAnalyser);
      try {
        localAnalyserRef.current = createAudioAnalyser(track, {
          cloneTrack: true,
          smoothingTimeConstant: 0.6,
          minDecibels: -85,
          maxDecibels: -25,
        });
      } catch {
        // Speaking rings fall back to server-reported levels.
      }
    };
    // A device switch replaces the underlying MediaStreamTrack.
    function restartLocalAnalyser() {
      const track = analysedTrack;
      releaseLocalAnalyser();
      if (track) startLocalAnalyser(track);
    }
    const syncLocalAnalyser = () => {
      const track = room.localParticipant.getTrackPublication(
        Track.Source.Microphone,
      )?.audioTrack as LocalAudioTrack | undefined;
      if (track === analysedTrack) return;
      releaseLocalAnalyser();
      if (track) startLocalAnalyser(track);
    };
    room.on(RoomEvent.TrackSubscribed, syncVideo);
    room.on(RoomEvent.TrackUnsubscribed, syncVideo);
    room.on(RoomEvent.TrackMuted, syncVideo);
    room.on(RoomEvent.TrackUnmuted, syncVideo);
    room.on(RoomEvent.LocalTrackPublished, syncVideo);
    room.on(RoomEvent.LocalTrackUnpublished, syncVideo);
    room.on(RoomEvent.ParticipantDisconnected, syncVideo);
    room.on(RoomEvent.ParticipantConnected, syncMembers);
    room.on(RoomEvent.ParticipantNameChanged, syncMembers);
    room.on(RoomEvent.TrackPublished, syncMembers);
    room.on(RoomEvent.TrackUnpublished, syncMembers);
    room.on(RoomEvent.LocalTrackPublished, syncLocalAnalyser);
    room.on(RoomEvent.LocalTrackUnpublished, syncLocalAnalyser);
    room.on(RoomEvent.ActiveSpeakersChanged, (speakers) => {
      setActiveSpeakerId(speakers[0]?.identity ?? "");
      syncMembers();
    });
    const onDisconnected = () => {
      diagnostics.stop();
      setConnected(false);
      setDisconnected(true);
      setReconnecting(false);
      setAudioBlocked(false);
      setVideos([]);
      setCameraEnabled(false);
      setScreenSharing(false);
      setMembers([]);
      releaseLocalAnalyser();
      for (const element of audioElements) element.remove();
      audioElements.clear();
    };
    const onConnectionQualityChanged = (
      quality: ConnectionQuality,
      participant: { identity: string },
    ) => {
      if (participant.identity === room.localParticipant.identity) {
        setConnectionQuality(quality);
      }
    };
    room.on(RoomEvent.Disconnected, onDisconnected);
    room.on(RoomEvent.Reconnecting, () => {
      setConnected(false);
      setReconnecting(true);
    });
    room.on(RoomEvent.Reconnected, () => {
      setConnected(true);
      setReconnecting(false);
    });
    room.on(RoomEvent.ConnectionQualityChanged, onConnectionQualityChanged);
    room.on(RoomEvent.AudioPlaybackStatusChanged, () =>
      setAudioBlocked(!room.canPlaybackAudio),
    );
    room.on(RoomEvent.TrackSubscribed, (track) => {
      if (track.kind !== Track.Kind.Audio) return;
      const element = track.attach();
      element.addEventListener("playing", diagnostics.audioPlaying, {
        once: true,
      });
      element.autoplay = true;
      document.body.appendChild(element);
      audioElements.add(element);
    });
    room.on(RoomEvent.TrackUnsubscribed, (track) => {
      if (track.kind !== Track.Kind.Audio) return;
      for (const element of track.detach()) {
        element.remove();
        audioElements.delete(element);
      }
    });
    // LiveKit pushes refreshed tokens to connected participants and the SDK
    // reconnects with the latest one, so the call needs no client-side refresh.
    void room
      .connect(media.url, media.token)
      .then(async () => {
        if (cancelled) return room.disconnect();
        // The lobby's explicit choices apply once, not on reconnect or rerender.
        try {
          await room.localParticipant.setMicrophoneEnabled(
            initialDevices?.microphoneEnabled ?? false,
          );
        } catch {
          if (!cancelled)
            setVideoError(
              "Could not enable your microphone. Check device permissions and try again.",
            );
        }
        if (cancelled) return room.disconnect();
        if (initialDevices?.cameraEnabled) {
          try {
            await room.localParticipant.setCameraEnabled(
              true,
              initialDevices.cameraDeviceId
                ? { deviceId: { exact: initialDevices.cameraDeviceId } }
                : undefined,
            );
          } catch {
            if (!cancelled)
              setVideoError(
                "Could not enable your camera. Check device permissions and try again.",
              );
          }
        }
        if (cancelled) return room.disconnect();
        setMuted(!room.localParticipant.isMicrophoneEnabled);
        setCameraEnabled(room.localParticipant.isCameraEnabled);
        setConnected(true);
        setDisconnected(false);
        setAudioBlocked(!room.canPlaybackAudio);
        syncVideo();
      })
      .catch((cause: unknown) => {
        if (!cancelled)
          setError(
            cause instanceof Error
              ? cause
              : new Error("Huddle connection failed"),
          );
      });
    return () => {
      cancelled = true;
      diagnostics.stop();
      room.removeAllListeners();
      releaseLocalAnalyser();
      for (const element of audioElements) element.remove();
      audioElements.clear();
      void room.disconnect();
      if (roomRef.current === room) roomRef.current = null;
      setConnected(false);
      setReconnecting(false);
      setConnectionQuality(ConnectionQuality.Unknown);
      setVideos([]);
      setMembers([]);
    };
  }, [media]);

  return {
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
    activeSpeakerId,
    getAudioLevel,
    getLocalInputLevel,
    leave,
    toggleMute,
    switchMicrophone,
    toggleCamera,
    switchCamera,
    toggleScreenShare,
    enableAudio,
  };
}
