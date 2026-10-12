import { Room, RoomEvent, Track } from "livekit-client";

// Local, bounded diagnostics for DevTools. Never retain tokens, identities,
// candidate addresses, or raw WebRTC reports.
export function startHuddleDiagnostics(room: Room) {
  const startedAt = performance.now();
  let stopped = false;
  let sampling = false;
  let firstAudio = false;
  let reconnectStartedAt: number | undefined;
  let reconnectCount = 0;
  let previous = new Map<
    string,
    { received: number; lost: number; dropped: number | null }
  >();
  const names = ["connect", "firstAudioPlayback", "reconnect", "media"];
  for (const name of names) performance.clearMeasures(`zenith.huddle.${name}`);

  const record = (
    name: string,
    start: number,
    detail?: Record<string, number | null>,
  ) => {
    if (stopped) return;
    const key = `zenith.huddle.${name}`;
    performance.clearMeasures(key);
    performance.measure(key, { start, end: performance.now(), detail });
  };
  const onConnected = () => record("connect", startedAt);
  const onReconnecting = () => {
    reconnectStartedAt ??= performance.now();
  };
  const onReconnected = () => {
    if (reconnectStartedAt === undefined) return;
    record("reconnect", reconnectStartedAt, { count: ++reconnectCount });
    reconnectStartedAt = undefined;
  };
  room.on(RoomEvent.Connected, onConnected);
  room.on(RoomEvent.Reconnecting, onReconnecting);
  room.on(RoomEvent.Reconnected, onReconnected);

  const sample = async () => {
    if (stopped || sampling || document.hidden) return;
    sampling = true;
    try {
      const tracks = [...room.remoteParticipants.values()].flatMap(
        (participant) =>
          [...participant.trackPublications.values()].flatMap((publication) =>
            publication.track ? [publication.track] : [],
          ),
      );
      const reports = await Promise.allSettled(
        tracks.map((track) => track.getRTCStatsReport()),
      );
      if (stopped) return;
      const next = new Map<
        string,
        { received: number; lost: number; dropped: number | null }
      >();
      let received = 0;
      let lost = 0;
      let dropped: number | null = null;
      let jitterMs: number | null = null;
      let rttMs: number | null = null;
      for (const result of reports) {
        if (result.status !== "fulfilled") continue;
        result.value?.forEach((stat) => {
          if (
            stat.type === "candidate-pair" &&
            stat.nominated &&
            stat.state === "succeeded" &&
            typeof stat.currentRoundTripTime === "number"
          ) {
            rttMs = Math.max(rttMs ?? 0, stat.currentRoundTripTime * 1000);
          }
          if (stat.type !== "inbound-rtp" || next.has(stat.id)) return;
          const counts = {
            received: stat.packetsReceived ?? 0,
            lost: stat.packetsLost ?? 0,
            dropped:
              typeof stat.framesDropped === "number"
                ? stat.framesDropped
                : null,
          };
          next.set(stat.id, counts);
          const old = previous.get(stat.id);
          if (old) {
            received += Math.max(0, counts.received - old.received);
            lost += Math.max(0, counts.lost - old.lost);
            if (counts.dropped !== null && old.dropped !== null) {
              dropped =
                (dropped ?? 0) + Math.max(0, counts.dropped - old.dropped);
            }
          }
          if (typeof stat.jitter === "number")
            jitterMs = Math.max(jitterMs ?? 0, stat.jitter * 1000);
        });
      }
      previous = next;
      record("media", performance.now(), {
        packetLossPercent:
          received + lost > 0 ? (lost / (received + lost)) * 100 : null,
        maxJitterMs: jitterMs,
        maxRttMs: rttMs,
        framesDroppedSinceLastSample: dropped,
        audioTracks: tracks.filter((track) => track.kind === Track.Kind.Audio)
          .length,
        videoTracks: tracks.filter((track) => track.kind === Track.Kind.Video)
          .length,
      });
    } finally {
      sampling = false;
    }
  };
  const timer = window.setInterval(() => {
    void sample().catch(() => {});
  }, 10000);
  return {
    audioPlaying() {
      if (firstAudio) return;
      firstAudio = true;
      record("firstAudioPlayback", startedAt);
    },
    stop() {
      stopped = true;
      window.clearInterval(timer);
      previous.clear();
      room.off(RoomEvent.Connected, onConnected);
      room.off(RoomEvent.Reconnecting, onReconnecting);
      room.off(RoomEvent.Reconnected, onReconnected);
    },
  };
}
