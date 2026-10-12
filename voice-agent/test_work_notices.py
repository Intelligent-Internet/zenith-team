"""Proactive notices yield to people and are checkpointed before speech."""

import asyncio
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, Mock

from work_notices import WorkNotices


class WorkNoticesTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.clock = Mock(return_value=0)
        self.stopped = asyncio.Event()
        self.session = SimpleNamespace(
            user_state="listening", agent_state="listening", generate_reply=Mock()
        )
        self.bridge = SimpleNamespace(
            pending={}, running=set(), tools={}, request_pending=False
        )
        self.notices = WorkNotices(
            self.session, self.bridge, self.stopped, clock=self.clock
        )
        self.notice = {"id": "notice-1", "state": "completed", "taskKey": "ZEN-1"}
        self.notices.offer([self.notice])
        self.claim = AsyncMock(return_value={"claimedWorkNotice": self.notice})
        self.clock.return_value = 3

    async def test_checkpoint_precedes_interruptible_speech_without_tools(self):
        async def claim(key):
            self.session.generate_reply.assert_not_called()
            self.assertEqual(key, "notice-1")
            return {"claimedWorkNotice": self.notice}

        await self.notices.deliver(claim)
        call = self.session.generate_reply.call_args.kwargs
        self.assertTrue(call["allow_interruptions"])
        self.assertEqual(call["tool_choice"], "none")
        self.assertIn("Do not claim the Task is done", call["instructions"])
        await self.notices.deliver(self.claim)
        self.claim.assert_not_awaited()

    async def test_human_speech_and_quiet_window_block_delivery(self):
        self.session.user_state = "speaking"
        await self.notices.deliver(self.claim)
        self.session.user_state = "listening"
        self.notices.activity()
        self.clock.return_value = 4
        await self.notices.deliver(self.claim)
        self.claim.assert_not_awaited()
        self.clock.return_value = 6
        await self.notices.deliver(self.claim)
        self.claim.assert_awaited_once()

    async def test_chat_tools_and_agent_speech_have_priority(self):
        for field, value in [
            ("pending", {1: {}}),
            ("running", {"r"}),
            ("tools", {"t": "r"}),
            ("request_pending", True),
        ]:
            old = getattr(self.bridge, field)
            setattr(self.bridge, field, value)
            await self.notices.deliver(self.claim)
            setattr(self.bridge, field, old)
        self.session.agent_state = "speaking"
        await self.notices.deliver(self.claim)
        self.claim.assert_not_awaited()

    async def test_speech_or_shutdown_during_claim_drops_notice(self):
        for stop in [False, True]:
            with self.subTest(stop=stop):
                self.setUp()

                async def claim(_):
                    if stop:
                        self.stopped.set()
                    else:
                        self.session.user_state = "speaking"
                    return {"claimedWorkNotice": self.notice}

                await self.notices.deliver(claim)
                self.session.generate_reply.assert_not_called()

    async def test_stale_or_duplicate_claim_does_not_speak(self):
        self.claim.return_value = {}
        await self.notices.deliver(self.claim)
        self.session.generate_reply.assert_not_called()

    async def test_failed_claim_does_not_speak_and_new_poll_can_retry(self):
        self.claim.side_effect = OSError("offline")
        with self.assertRaises(OSError):
            await self.notices.deliver(self.claim)
        self.session.generate_reply.assert_not_called()
        self.claim.side_effect = None
        self.notices.offer([self.notice])
        await self.notices.deliver(self.claim)
        self.session.generate_reply.assert_called_once()

    async def test_cooldown_and_stale_snapshot_prevent_chatter(self):
        await self.notices.deliver(self.claim)
        self.notices.offer([{**self.notice, "id": "notice-2"}])
        self.clock.return_value = 5
        await self.notices.deliver(self.claim)
        self.clock.return_value = 40
        await self.notices.deliver(self.claim)
        self.claim.assert_awaited_once()

    async def test_context_disabled_clears_candidates(self):
        self.notices.offer([])
        await self.notices.deliver(self.claim)
        self.claim.assert_not_awaited()

    async def test_local_audio_activity_blocks_before_transcription_arrives(self):
        self.notices.audio_activity = lambda: 2.5
        await self.notices.deliver(self.claim)
        self.claim.assert_not_awaited()

    async def test_sdk_idle_away_state_does_not_suppress_work_results(self):
        # SDK marks 15 seconds of silence as away; actual audience is API-owned.
        self.session.user_state = "away"
        await self.notices.deliver(self.claim)
        self.claim.assert_awaited_once()
        self.session.generate_reply.assert_called_once()


if __name__ == "__main__":
    unittest.main()
