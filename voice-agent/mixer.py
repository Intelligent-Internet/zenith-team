"""A single input, dynamically restricted to admitted human microphones."""

import asyncio
import time
from livekit import rtc
from livekit.agents.voice import io


def admitted_microphone(participant, publication, admitted):
    return (
        participant.identity in admitted
        and participant.kind != rtc.ParticipantKind.PARTICIPANT_KIND_AGENT
        and publication.source == rtc.TrackSource.SOURCE_MICROPHONE
        and not publication.muted
    )


class MicrophoneMixer(io.AudioInput):
    def __init__(self, room, admitted):
        super().__init__(label="admitted-human-microphones")
        self.room = room
        self.admitted = set(admitted)
        self.mixer = rtc.AudioMixer(24000, 1, blocksize=480, capacity=5)
        self.streams = {}
        self.closed = False
        self.last_activity = time.monotonic()
        self.task = asyncio.create_task(self.reconcile())

    async def __anext__(self):
        return await self.mixer.__anext__()

    async def reconcile(self):
        while not self.closed:
            active = set()
            for participant in self.room.remote_participants.values():
                for publication in participant.track_publications.values():
                    allowed = admitted_microphone(
                        participant, publication, self.admitted
                    )
                    publication.set_subscribed(allowed)
                    if not allowed or not publication.track:
                        continue
                    active.add(publication.sid)
                    previous = self.streams.get(publication.sid)
                    if previous and previous[2] is not publication.track:
                        await self.remove_stream(publication.sid)
                    if publication.sid not in self.streams:
                        stream = rtc.AudioStream(
                            publication.track,
                            sample_rate=24000,
                            num_channels=1,
                            frame_size_ms=20,
                            capacity=5,
                        )
                        frames = self.frames(stream)
                        self.streams[publication.sid] = (
                            stream,
                            frames,
                            publication.track,
                        )
                        self.mixer.add_stream(frames)
            for sid in list(self.streams):
                if sid not in active:
                    await self.remove_stream(sid)
            await asyncio.sleep(0.1)

    async def remove_stream(self, sid):
        stream, frames, _ = self.streams.pop(sid)
        self.mixer.remove_stream(frames)
        await stream.aclose()

    async def frames(self, stream):
        async for event in stream:
            # Conservative local audio veto, not speech recognition: defer notices
            # even before the provider emits a transcript/speaking event. Keep no audio.
            if any(abs(sample) >= 512 for sample in event.frame.data):
                self.last_activity = time.monotonic()
            yield event.frame

    async def aclose(self):
        self.closed = True
        self.task.cancel()
        await asyncio.gather(self.task, return_exceptions=True)
        for sid in list(self.streams):
            await self.remove_stream(sid)
        await self.mixer.aclose()
