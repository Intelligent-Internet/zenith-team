"""Exercise job authority and cleanup without connecting to paid services."""

import asyncio
import time
import unittest
import uuid
import httpx
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock, patch

from agent import HeliaModel, entrypoint
from bridge import TypedBridge


class EntrypointTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.lease = {
            "agent": {
                "id": str(uuid.uuid4()),
                "name": "Susan",
                "title": "Secretary",
                "instructions": "Summarize decisions",
                "settings": {
                    "enabled": True,
                    "voice": "stone",
                    "greet": False,
                    "participation": "addressed",
                },
            },
            "roomName": "test-room",
            "pendingChat": [],
            "completedSeq": 0,
            "capability": "test-capability",
            "chatCursor": 0,
            "humanIdentities": ["human"],
            "reasoningReserveTicks": 3_000_000_000,
            "greet": False,
        }
        self.control = SimpleNamespace(
            call=AsyncMock(side_effect=self.call),
            http=SimpleNamespace(aclose=AsyncMock()),
        )
        self.ctx = SimpleNamespace(
            job=SimpleNamespace(
                metadata=str(uuid.uuid4()), room=SimpleNamespace(name="test-room")
            ),
            room=SimpleNamespace(
                disconnect=AsyncMock(),
                local_participant=SimpleNamespace(set_name=AsyncMock()),
            ),
            connect=AsyncMock(),
            add_shutdown_callback=Mock(),
            shutdown=Mock(),
        )
        self.model = None
        self.session = SimpleNamespace(
            input=SimpleNamespace(audio=None),
            on=Mock(),
            start=AsyncMock(),
            aclose=AsyncMock(),
            generate_reply=Mock(),
        )
        self.mixer_task = asyncio.create_task(asyncio.Event().wait())
        self.mixer = SimpleNamespace(aclose=AsyncMock(), task=self.mixer_task)
        self.transport = SimpleNamespace(close=AsyncMock())
        self.watchdog = SimpleNamespace(
            renew=Mock(), close=Mock(), cutoff=time.monotonic() + 60
        )

    async def asyncTearDown(self):
        self.mixer_task.cancel()
        await asyncio.gather(self.mixer_task, return_exceptions=True)

    async def call(self, path, body=None):
        if path == "claim":
            return dict(self.lease)
        if path == "heartbeat":
            # The heartbeat describes the configured reserve, whereas Claim
            # deducts spending by earlier attempts from its returned reserve.
            return {**self.lease, "reasoningReserveTicks": 10_000_000_000}
        if path == "chat":
            return []

    def model_factory(
        self,
        counters,
        cursor,
        stopped,
        transport,
        profile,
        allow_company_context=False,
        transcript=None,
    ):
        self.assertEqual(profile["name"], "Susan")
        counters.session_id = "test-model"
        counters.input_tokens = 300_000  # $0.06, leaving less than $0.25 headroom.
        self.model = SimpleNamespace(
            bridge=TypedBridge(Mock(), cursor, auto_flush=False)
        )
        return self.model

    async def run_job(self):
        with (
            patch("agent.Control", return_value=self.control),
            patch("agent.HeliaModel", side_effect=self.model_factory) as model,
            patch("agent.AgentSession", return_value=self.session),
            patch("agent.MicrophoneMixer", return_value=self.mixer),
            patch("agent.LeaseWatchdog", return_value=self.watchdog),
            patch("agent.aiohttp.ClientSession", return_value=self.transport),
        ):
            await asyncio.wait_for(entrypoint(self.ctx), timeout=2)
            return model

    async def test_recovery_does_not_reset_reasoning_allowance(self):
        await self.run_job()
        self.session.aclose.assert_awaited_once()
        self.transport.close.assert_awaited_once()
        self.ctx.room.disconnect.assert_awaited_once()
        self.ctx.room.local_participant.set_name.assert_awaited_once_with("Susan")
        self.ctx.shutdown.assert_called_once_with(reason="reasoning_budget")

    async def test_recoverable_error_does_not_stop_session(self):
        self.lease["reasoningReserveTicks"] = 10_000_000_000

        async def start(**_):
            callbacks = dict(
                self.session.on.call_args_list[i].args
                for i in range(len(self.session.on.call_args_list))
            )
            callbacks["error"](SimpleNamespace(error=SimpleNamespace(recoverable=True)))
            await asyncio.sleep(0)
            self.session.aclose.assert_not_awaited()
            callbacks["error"](
                SimpleNamespace(error=SimpleNamespace(recoverable=False))
            )

        self.session.start.side_effect = start
        await self.run_job()
        self.ctx.shutdown.assert_called_once_with(reason="provider_error")

    async def test_background_error_has_a_reason(self):
        async def failing_chat(path, body=None):
            if path == "chat":
                raise RuntimeError("do not log private response")
            return await self.call(path, body)

        self.control.call.side_effect = failing_chat
        await self.run_job()
        self.ctx.shutdown.assert_called_once_with(reason="internal_error")

    async def test_transient_chat_failure_retries_without_closing_session(self):
        attempts = 0

        async def flaky_chat(path, body=None):
            nonlocal attempts
            if path == "chat":
                attempts += 1
                if attempts == 1:
                    raise httpx.ConnectError("private transport detail")
                self.session.aclose.assert_not_awaited()
            return await self.call(path, body)

        self.control.call.side_effect = flaky_chat
        await self.run_job()
        self.assertEqual(attempts, 2)
        self.ctx.shutdown.assert_called_once_with(reason="reasoning_budget")

    async def test_revoked_chat_does_not_retry(self):
        async def denied(path, body=None):
            if path == "chat":
                response = httpx.Response(
                    403, request=httpx.Request("GET", "http://test/chat")
                )
                response.raise_for_status()
            return await self.call(path, body)

        self.control.call.side_effect = denied
        await self.run_job()
        self.ctx.shutdown.assert_called_once_with(reason="authority_revoked")

    async def test_missing_profile_never_connects_model(self):
        del self.lease["agent"]
        model = await self.run_job()
        model.assert_not_called()
        self.ctx.connect.assert_not_awaited()

    async def test_greeting_ignores_saved_preference(self):
        self.lease["greet"] = True
        await self.run_job()
        self.session.generate_reply.assert_called_once()

    async def test_greeting_requires_server_claim(self):
        self.lease["agent"]["settings"]["greet"] = True
        await self.run_job()
        self.session.generate_reply.assert_not_called()

    async def test_rejected_claim_never_connects_media_or_model(self):
        self.control.call.side_effect = RuntimeError("claim rejected")
        model = await self.run_job()
        model.assert_not_called()
        self.ctx.connect.assert_not_awaited()
        self.control.http.aclose.assert_awaited_once()

    async def test_revoked_lease_after_join_never_opens_model(self):
        self.control.call.side_effect = [
            dict(self.lease),
            RuntimeError("lease revoked"),
        ]
        model = await self.run_job()
        self.ctx.connect.assert_awaited_once()
        model.assert_not_called()
        self.ctx.room.disconnect.assert_awaited_once()

    async def test_mixer_failure_stops_model_and_cleans_up(self):
        self.lease["reasoningReserveTicks"] = 10_000_000_000
        self.mixer_task.cancel()
        await asyncio.gather(self.mixer_task, return_exceptions=True)

        async def broken_input():
            raise RuntimeError("audio input failed")

        self.mixer_task = asyncio.create_task(broken_input())
        self.mixer.task = self.mixer_task
        await self.run_job()
        self.session.aclose.assert_awaited_once()
        self.mixer.aclose.assert_awaited_once()

    async def test_media_cleanup_failure_does_not_drop_final_usage(self):
        self.ctx.room.disconnect.side_effect = RuntimeError("room already closed")
        await self.run_job()
        reports = [
            call.args[1]
            for call in self.control.call.await_args_list
            if call.args[0] == "usage"
        ]
        self.assertTrue(reports[-1]["closed"])
        self.control.http.aclose.assert_awaited_once()
        self.ctx.shutdown.assert_called_once()

    async def test_lifecycle_logs_correlate_call_attempt_and_job(self):
        self.ctx.job.id = "job-test"
        with self.assertLogs("zenith.helia", level="INFO") as captured:
            await self.run_job()
        lifecycle = [
            record
            for record in captured.records
            if record.getMessage().startswith("Voice session")
        ]
        self.assertGreaterEqual(len(lifecycle), 3)
        for record in lifecycle:
            self.assertEqual(record.attempt_id, self.ctx.job.metadata)
            self.assertEqual(record.job_id, "job-test")
            self.assertEqual(record.huddle_id, "test-room")

    async def test_cancelled_job_reports_usage_before_media_cleanup(self):
        self.session.start.side_effect = asyncio.CancelledError()
        with self.assertRaises(asyncio.CancelledError):
            await self.run_job()
        reports = [
            call.args[1]
            for call in self.control.call.await_args_list
            if call.args[0] == "usage"
        ]
        self.assertTrue(reports[-1]["closed"])
        self.assertEqual(reports[-1]["stopReason"], "worker_shutdown")
        self.control.http.aclose.assert_awaited_once()

    async def test_shutdown_closes_model_before_final_transcript_flush(self):
        factory = self.model_factory

        def with_capture(*args, **kwargs):
            result = factory(*args, **kwargs)
            kwargs["transcript"].event(
                {"type": "session.input_transcript.delta", "delta": "captured"}
            )
            return result

        async def control(path, body=None):
            if path == "transcript":
                self.session.aclose.assert_awaited_once()
                return {"truncated": False}
            return await self.call(path, body)

        self.model_factory = with_capture
        self.control.call.side_effect = control
        self.session.start.side_effect = asyncio.CancelledError()
        with self.assertRaises(asyncio.CancelledError):
            await self.run_job()
        paths = [call.args[0] for call in self.control.call.await_args_list]
        self.assertIn("transcript", paths)
        self.assertLess(paths.index("transcript"), paths.index("usage"))


class ModelProfileTests(unittest.TestCase):
    def test_model_receives_saved_voice_and_instructions(self):
        profile = {
            "name": "Lyra",
            "title": "Strategist",
            "instructions": "Explain the tradeoffs.",
            "settings": {"voice": "vesper", "participation": "natural"},
        }
        with patch(
            "agent.openai.realtime.GPTLiveModel.__init__", return_value=None
        ) as initialize:
            HeliaModel(Mock(), 0, Mock(), Mock(), profile)
        self.assertEqual(initialize.call_args.kwargs["voice"], "vesper")
        self.assertIn(
            "Explain the tradeoffs.",
            initialize.call_args.kwargs["responses_options"]["instructions"],
        )

    def test_transcript_uses_existing_public_session_event_hook(self):
        profile = {
            "name": "Eli",
            "title": "Assistant",
            "instructions": "",
            "settings": {"voice": "marin", "participation": "addressed"},
        }
        collector = Mock()
        socket = Mock()
        with (
            patch("agent.openai.realtime.GPTLiveModel.__init__", return_value=None),
            patch("agent.openai.realtime.GPTLiveModel.session", return_value=socket),
        ):
            model = HeliaModel(Mock(), 0, Mock(), Mock(), profile, transcript=collector)
            self.assertIs(model.session(), socket)
        socket.on.assert_any_call("openai_server_event_received", collector.event)
