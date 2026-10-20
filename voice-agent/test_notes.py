import asyncio
import json
import os
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import httpx
from livekit import rtc
from notes import NotesControl, allowed_audio, notes_attempt


class NotesTests(unittest.IsolatedAsyncioTestCase):
    def test_dispatch_is_distinct_from_voice(self):
        value = "00000000-0000-0000-0000-000000000001"
        self.assertEqual(notes_attempt("notes:" + value), value)
        self.assertIsNone(notes_attempt(value))
        with self.assertRaises(ValueError):
            notes_attempt("notes:not-a-uuid")

    def test_audio_admission_includes_only_allowed_published_microphones(self):
        publication = SimpleNamespace(
            kind=rtc.TrackKind.KIND_AUDIO,
            source=rtc.TrackSource.SOURCE_MICROPHONE,
            muted=False,
        )
        participant = SimpleNamespace(identity="helia:invited")
        self.assertTrue(allowed_audio(participant, publication, {participant.identity}))
        self.assertFalse(allowed_audio(participant, publication, set()))
        publication.muted = True
        self.assertFalse(
            allowed_audio(participant, publication, {participant.identity})
        )
        publication.muted = False
        publication.source = rtc.TrackSource.SOURCE_SCREENSHARE_AUDIO
        self.assertFalse(
            allowed_audio(participant, publication, {participant.identity})
        )

    async def test_ambiguous_append_retries_exact_payload_and_serializes(self):
        lease = {
            "leaseExpiresAt": "2099-01-01T00:00:00Z",
            "deadlineAt": "2099-01-01T00:00:00Z",
            "identities": [],
        }
        requests = []

        async def receive(request):
            requests.append(json.loads(request.content))
            if len(requests) == 1:
                raise httpx.ReadTimeout("ambiguous", request=request)
            return httpx.Response(200, json=lease)

        with patch.dict(
            os.environ,
            {
                "ZENITH_API_URL": "https://example.test",
                "HUDDLE_AI_WORKLOAD_SECRET": "test-secret",
            },
        ):
            control = NotesControl("attempt")
        await control.http.aclose()
        control.http = httpx.AsyncClient(transport=httpx.MockTransport(receive))
        control.accept(lease)
        try:
            await asyncio.gather(
                control.append({"text": "repeat", "speaker": "person"}),
                control.append({"text": "repeat", "speaker": "person"}),
            )
            self.assertEqual(requests[0], requests[1])
            self.assertEqual([r["sequence"] for r in requests], [1, 1, 2])
            control.valid_until = 0
            with self.assertRaises(RuntimeError):
                await control.append({})
            self.assertEqual(len(requests), 3)
        finally:
            await control.http.aclose()


class NotesDrainTests(unittest.IsolatedAsyncioTestCase):
    def test_cutoff_trims_partial_frame_and_discards_later_audio(self):
        from notes import cut_notes_audio

        before = b"\x01\x00" * 480
        crossing = b"\x02\x00" * 480
        later = b"\x03\x00" * 480
        pcm, start, end = cut_notes_audio(
            [(0, before), (20, crossing), (40, later)], 30
        )
        self.assertEqual(pcm, before + crossing[:480])
        self.assertEqual((start, end), (0, 30))
        self.assertEqual(cut_notes_audio([(40, later)], 30)[0], b"")

    async def test_stop_without_audio_does_not_commit_empty_buffer(self):
        import time
        from unittest.mock import AsyncMock
        from notes import transcribe_stream

        class EmptySource:
            def __aiter__(self):
                return self

            async def __anext__(self):
                await asyncio.Future()

        ws = EmptySource()
        ws.send_json = AsyncMock()
        control = SimpleNamespace(
            draining=asyncio.Event(),
            valid_until=time.monotonic() + 1,
            lease={"stopAfterMs": 0},
            append=AsyncMock(),
        )
        control.draining.set()
        await asyncio.wait_for(
            transcribe_stream(EmptySource(), "person", control, ws, time.monotonic()), 1
        )
        ws.send_json.assert_not_awaited()
        control.append.assert_not_awaited()

    async def test_stop_commits_tail_and_waits_for_persisted_final_text(self):
        await self.check_finalization(complete=True)

    async def test_unresponsive_provider_hits_deadline_without_success(self):
        await self.check_finalization(complete=False)

    async def check_finalization(self, *, complete):
        import base64
        import time

        import aiohttp
        from aiohttp import web
        from notes import transcribe_stream

        committed = asyncio.Event()
        release = asyncio.Event()
        uploads = []
        writes = []
        frames = asyncio.Queue()

        async def provider(request):
            ws = web.WebSocketResponse()
            await ws.prepare(request)
            async for message in ws:
                event = json.loads(message.data)
                if event["type"] == "input_audio_buffer.append":
                    uploads.append(base64.b64decode(event["audio"]))
                if event["type"] == "input_audio_buffer.commit":
                    await ws.send_json(
                        {"type": "input_audio_buffer.committed", "item_id": "tail"}
                    )
                    committed.set()
                    await release.wait()
                    if not ws.closed and complete:
                        await ws.send_json(
                            {
                                "type": "conversation.item.input_audio_transcription.completed",
                                "item_id": "tail",
                                "transcript": "Final decision",
                            }
                        )
            return ws

        app = web.Application()
        app.router.add_get("/ws", provider)
        runner = web.AppRunner(app)
        await runner.setup()
        site = web.TCPSite(runner, "127.0.0.1", 0)
        await site.start()
        port = site._server.sockets[0].getsockname()[1]

        async def append(body):
            writes.append(body)

        control = SimpleNamespace(
            draining=asyncio.Event(),
            lease={},
            valid_until=time.monotonic() + 2,
            append=append,
        )

        async def audio():
            while True:
                yield await frames.get()

        task = None
        try:
            async with aiohttp.ClientSession() as http:
                async with http.ws_connect(f"http://127.0.0.1:{port}/ws") as ws:
                    task = asyncio.create_task(
                        transcribe_stream(
                            audio(), "person", control, ws, time.monotonic()
                        )
                    )
                    for _ in range(3):
                        await frames.put(
                            SimpleNamespace(
                                frame=SimpleNamespace(
                                    data=memoryview(b"\x01\x00" * 480),
                                    samples_per_channel=480,
                                )
                            )
                        )
                        await asyncio.sleep(0.02)
                    control.lease["stopAfterMs"] = 10000
                    if not complete:
                        control.valid_until = time.monotonic() + 0.15
                    control.draining.set()
                    await asyncio.wait_for(committed.wait(), 1)
                    self.assertFalse(
                        task.done(), "closed before final transcript arrived"
                    )
                    self.assertFalse(any(body.get("text") for body in writes))
                    if complete:
                        release.set()
                        await asyncio.wait_for(task, 1)
                        texts = [body for body in writes if body.get("text")]
                        self.assertEqual(len(texts), 1)
                        self.assertEqual(texts[0]["text"], "Final decision")
                        self.assertEqual(len(uploads), 1)
                        self.assertTrue(uploads[0].startswith(b"\x01\x00" * 1440))
                    else:
                        with self.assertRaises(TimeoutError):
                            await asyncio.wait_for(task, 1)
                        self.assertFalse(any(body.get("text") for body in writes))
        finally:
            release.set()
            if task:
                task.cancel()
                await asyncio.gather(task, return_exceptions=True)
            await runner.cleanup()
