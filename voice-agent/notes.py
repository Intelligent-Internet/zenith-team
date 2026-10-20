"""Silent, independently authorized Huddle transcription over published audio."""

import asyncio
import base64
from collections import deque
from datetime import datetime, timezone
import json
import os
import time

import aiohttp
import httpx
from livekit import rtc
from livekit.agents import AutoSubscribe


def notes_attempt(metadata):
    import uuid

    if not metadata.startswith("notes:"):
        return None
    return str(uuid.UUID(metadata.removeprefix("notes:")))


def allowed_audio(participant, publication, identities):
    return (
        participant.identity in identities
        and publication.kind == rtc.TrackKind.KIND_AUDIO
        and publication.source == rtc.TrackSource.SOURCE_MICROPHONE
        and not publication.muted
    )


class NotesControl:
    def __init__(self, attempt):
        self.url = (
            os.environ["ZENITH_API_URL"].rstrip("/")
            + "/api/v1/internal/huddle-notes/"
            + attempt
        )
        self.token = (
            os.environ.get("HUDDLE_AI_WORKLOAD_SECRET")
            or os.environ["BOARDLY_HUDDLE_AI_WORKLOAD_SECRET"]
        )
        self.http = httpx.AsyncClient(timeout=4, follow_redirects=False)
        self.lock = asyncio.Lock()
        self.sequence = 0
        self.lease = None
        self.draining = asyncio.Event()
        self.valid_until = 0

    async def call(self, path, body=None):
        response = await self.http.post(
            self.url + "/" + path,
            json=body,
            headers={"Authorization": "Bearer " + self.token},
        )
        response.raise_for_status()
        return response.json()

    def accept(self, lease):
        now = datetime.now(timezone.utc)
        expiry = min(
            datetime.fromisoformat(lease[key].replace("Z", "+00:00"))
            for key in ("leaseExpiresAt", "deadlineAt")
        )
        self.valid_until = time.monotonic() + max(0, (expiry - now).total_seconds())
        self.lease = lease
        if lease.get("stopAfterMs") is not None:
            self.draining.set()

    async def append(self, body):
        # Freeze the exact sequence/payload across an ambiguous request timeout.
        async with self.lock:
            if body.get("text"):
                body = {**body, "sequence": self.sequence + 1}
            else:
                body = {**body, "sequence": 0}
            for retry in range(3):
                if time.monotonic() >= self.valid_until:
                    raise RuntimeError("notes authority expired")
                try:
                    self.accept(await self.call("append", body))
                    if body["sequence"]:
                        self.sequence = body["sequence"]
                    return
                except (httpx.TimeoutException, httpx.NetworkError):
                    if retry == 2:
                        raise
                    await asyncio.sleep(0.1)


async def transcribe_track(track, speaker, control, http, origin):
    """Only completed committed items are persisted; raw audio never hits disk."""
    async with http.ws_connect(
        "wss://api.openai.com/v1/realtime?intent=transcription",
        headers={"Authorization": "Bearer " + os.environ["OPENAI_API_KEY"]},
        max_msg_size=65536,
        timeout=aiohttp.ClientWSTimeout(ws_receive=20),
    ) as ws:
        await ws.send_json(
            {
                "type": "session.update",
                "session": {
                    "type": "transcription",
                    "audio": {
                        "input": {
                            "format": {"type": "audio/pcm", "rate": 24000},
                            "transcription": {"model": "gpt-live-transcribe"},
                            "turn_detection": None,
                        }
                    },
                },
            }
        )
        while True:
            event = await ws.receive_json(timeout=10)
            if event.get("type") == "error":
                raise RuntimeError("notes provider configuration failed")
            if event.get("type") == "session.updated":
                break
        await control.append({"ready": True})
        stream = rtc.AudioStream(
            track, sample_rate=24000, num_channels=1, frame_size_ms=20, capacity=5
        )
        try:
            await transcribe_stream(stream, speaker, control, ws, origin)
        finally:
            await stream.aclose()


def cut_notes_audio(frames, cutoff):
    """Trim 24 kHz mono PCM to the server's capture cutoff before model upload."""
    pcm = bytearray()
    start_ms = frames[0][0]
    end_ms = start_ms
    for frame_start, data in frames:
        samples = len(data) // 2
        if cutoff is not None:
            samples = min(samples, max(0, (cutoff - frame_start) * 24))
        if samples:
            pcm.extend(data[: samples * 2])
            end_ms = frame_start + samples // 24
    return pcm, start_ms, end_ms


async def transcribe_stream(stream, speaker, control, ws, origin):
    pending = deque()
    items = {}
    chunks = asyncio.Queue(maxsize=2)
    settled = asyncio.Event()
    settled.set()

    async def commit(frames):
        if not frames:
            return
        # Check stop before sending each bounded chunk, including the final tail.
        # Audio stays in RAM until this check; no post-cutoff samples are committed.
        await control.append({})
        cutoff = control.lease.get("stopAfterMs")
        pcm, start_ms, end_ms = cut_notes_audio(frames, cutoff)
        if not pcm:
            return
        if len(pending) + len(items) >= 8:
            raise RuntimeError("notes transcription backlog exceeded")
        # A short tail is padded with silence, never with later microphone audio.
        pcm.extend(bytes(max(0, 4800 - len(pcm))))
        settled.clear()
        pending.append((start_ms, end_ms))
        await ws.send_json(
            {
                "type": "input_audio_buffer.append",
                "audio": base64.b64encode(pcm).decode("ascii"),
            }
        )
        await ws.send_json({"type": "input_audio_buffer.commit"})

    async def audio():
        frames = []
        samples = 0
        iterator = stream.__aiter__()
        stop_task = asyncio.create_task(control.draining.wait())
        next_frame = None
        try:
            while not control.draining.is_set():
                next_frame = asyncio.create_task(anext(iterator))
                done, _ = await asyncio.wait(
                    [next_frame, stop_task], return_when=asyncio.FIRST_COMPLETED
                )
                if stop_task in done and next_frame not in done:
                    break
                try:
                    event = next_frame.result()
                except StopAsyncIteration:
                    if not control.draining.is_set():
                        raise RuntimeError("notes track ended")
                    break
                if time.monotonic() >= control.valid_until:
                    raise RuntimeError("notes authority expired")
                data = event.frame.data.tobytes()
                frame_end = int((time.monotonic() - origin) * 1000)
                frame_start = max(0, frame_end - event.frame.samples_per_channel // 24)
                frames.append((frame_start, data))
                samples += event.frame.samples_per_channel
                if samples >= 24000 * 8:
                    try:
                        chunks.put_nowait(frames)
                    except asyncio.QueueFull:
                        raise RuntimeError("notes audio backlog exceeded")
                    frames, samples = [], 0
            await chunks.put(frames)
            await chunks.put(None)
        finally:
            stop_task.cancel()
            if next_frame is not None:
                next_frame.cancel()
            await asyncio.gather(
                stop_task, *([next_frame] if next_frame else []), return_exceptions=True
            )

    async def text():
        async for message in ws:
            if message.type != aiohttp.WSMsgType.TEXT:
                raise RuntimeError("notes provider disconnected")
            event = json.loads(message.data)
            if (
                event.get("type") == "error"
                or event.get("type")
                == "conversation.item.input_audio_transcription.failed"
            ):
                raise RuntimeError("notes provider failed")
            if event.get("type") == "input_audio_buffer.committed":
                if not pending:
                    raise RuntimeError("unexpected notes commit")
                items[event["item_id"]] = pending.popleft()
            if (
                event.get("type")
                == "conversation.item.input_audio_transcription.completed"
            ):
                interval = items.pop(event["item_id"], None)
                if interval is None:
                    raise RuntimeError("unknown notes transcript item")
                value = event.get("transcript", "").strip()
                if value:
                    if len(value.encode("utf-8")) > 16000:
                        raise RuntimeError("notes segment limit exceeded")
                    await control.append(
                        {
                            "speaker": speaker,
                            "text": value,
                            "startMs": interval[0],
                            "endMs": interval[1],
                        }
                    )
                if not pending and not items:
                    settled.set()
        raise RuntimeError("notes provider disconnected")

    async def send():
        while True:
            frames = await chunks.get()
            if frames is None:
                return
            await commit(frames)

    async def capture():
        async with asyncio.TaskGroup() as group:
            group.create_task(audio())
            group.create_task(send())

    audio_task = asyncio.create_task(capture())
    text_task = asyncio.create_task(text())
    settled_task = None
    try:
        done, _ = await asyncio.wait(
            [audio_task, text_task], return_when=asyncio.FIRST_COMPLETED
        )
        for task in done:
            task.result()
        if audio_task not in done:
            raise RuntimeError("notes provider ended before capture")
        # Capture is now closed; wait only for the already committed final text.
        settled_task = asyncio.create_task(settled.wait())
        async with asyncio.timeout(max(0, control.valid_until - time.monotonic())):
            done, _ = await asyncio.wait(
                [settled_task, text_task], return_when=asyncio.FIRST_COMPLETED
            )
            for task in done:
                task.result()
    finally:
        tasks = [audio_task, text_task] + ([settled_task] if settled_task else [])
        for task in tasks:
            task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)


async def run_notes(ctx, attempt):
    control = NotesControl(attempt)
    streams = {}
    stopping = asyncio.Event()
    origin = time.monotonic()

    async def stop():
        stopping.set()

    ctx.add_shutdown_callback(stop)
    ctx.room.on("disconnected", lambda *_: stopping.set())
    try:
        lease = await control.call("claim")
        if lease["roomName"] != ctx.job.room.name:
            raise RuntimeError("notes room mismatch")
        control.token = lease["capability"]
        control.accept(lease)
        await ctx.connect(auto_subscribe=AutoSubscribe.SUBSCRIBE_NONE)
        await ctx.room.local_participant.set_name("AI notes")
        last_heartbeat = 0
        async with aiohttp.ClientSession() as http:
            while not stopping.is_set():
                if time.monotonic() >= control.valid_until:
                    raise RuntimeError("notes authority expired")
                if time.monotonic() - last_heartbeat >= 3:
                    await control.append({})
                    last_heartbeat = time.monotonic()
                if control.draining.is_set():
                    async with asyncio.timeout(
                        max(0, control.valid_until - time.monotonic())
                    ):
                        await asyncio.gather(*streams.values())
                        await control.append({"finished": True})
                    break
                admitted = set(control.lease["identities"])
                active = set()
                for participant in ctx.room.remote_participants.values():
                    for publication in participant.track_publications.values():
                        allowed = allowed_audio(participant, publication, admitted)
                        publication.set_subscribed(allowed)
                        if not allowed or publication.track is None:
                            continue
                        active.add(publication.sid)
                        if publication.sid not in streams:
                            if len(streams) >= 9:
                                raise RuntimeError("notes track limit exceeded")
                            streams[publication.sid] = asyncio.create_task(
                                transcribe_track(
                                    publication.track,
                                    participant.identity,
                                    control,
                                    http,
                                    origin,
                                )
                            )
                for sid, task in list(streams.items()):
                    if task.done():
                        task.result()
                        if not control.draining.is_set():
                            raise RuntimeError("notes track ended")
                    if sid not in active:
                        task.cancel()
                        await asyncio.gather(task, return_exceptions=True)
                        del streams[sid]
                await asyncio.sleep(0.1)
    finally:
        for task in streams.values():
            task.cancel()
        await asyncio.gather(*streams.values(), return_exceptions=True)
        await control.http.aclose()
        await ctx.room.disconnect()
