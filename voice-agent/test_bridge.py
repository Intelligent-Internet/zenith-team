import unittest
from bridge import TypedBridge, UsageCounters


class Session:
    def __init__(self):
        self.sent = []

    def send_event(self, event):
        self.sent.append(event)


class BridgeTests(unittest.TestCase):
    def test_delegation_reserves_backend_before_response_created(self):
        session = Session()
        bridge = TypedBridge(session, 0)
        bridge.server_event(
            {
                "type": "session.delegation.created",
                "delegation": {"id": "d", "target": "responses"},
            }
        )
        bridge.enqueue([{"id": "i", "seq": 1, "text": "exact value"}])
        self.assertEqual(session.sent, [])
        bridge.server_event(
            {
                "type": "response.event",
                "delegation_id": "d",
                "event": {"type": "response.incomplete"},
            }
        )
        self.assertEqual(session.sent[-1]["type"], "response.create")

    def test_incomplete_backend_releases_queued_chat_and_abandons_its_tools(self):
        session = Session()
        bridge = TypedBridge(session, 0)
        bridge.server_event(
            {
                "type": "response.event",
                "delegation_id": "d",
                "event": {"type": "response.created"},
            }
        )
        bridge.server_event(
            {
                "type": "response.event",
                "delegation_id": "d",
                "event": {
                    "type": "response.output_item.done",
                    "item": {
                        "type": "function_call",
                        "call_id": "abandoned",
                        "status": "completed",
                    },
                },
            }
        )
        bridge.enqueue([{"id": "i", "seq": 1, "text": "next question"}])
        bridge.server_event(
            {
                "type": "response.event",
                "delegation_id": "d",
                "event": {"type": "response.incomplete"},
            }
        )
        self.assertEqual(
            [event["type"] for event in session.sent],
            ["response.item.create", "response.create"],
        )

    def test_tool_call_is_not_completed_typed_work(self):
        session = Session()
        bridge = TypedBridge(session, 0)
        bridge.enqueue([{"id": "i", "seq": 1, "text": "question"}])
        bridge.server_event(
            {"type": "response.event", "event": {"type": "response.created"}}
        )
        bridge.server_event(
            {
                "type": "response.event",
                "event": {
                    "type": "response.output_item.done",
                    "item": {
                        "type": "function_call",
                        "call_id": "c",
                        "status": "completed",
                    },
                },
            }
        )
        bridge.server_event(
            {"type": "response.event", "event": {"type": "response.completed"}}
        )
        self.assertEqual(bridge.completed_cursor, 0)

    def test_busy_backend_keeps_exact_text_and_deduplicates(self):
        session = Session()
        bridge = TypedBridge(session, 3)
        bridge.server_event(
            {
                "type": "response.event",
                "delegation_id": "d",
                "event": {"type": "response.created"},
            }
        )
        item = {
            "id": "item",
            "seq": 4,
            "authorId": "user",
            "displayName": "A",
            "text": "https://example.com/?a=1&b=2",
        }
        bridge.enqueue([item, item])
        self.assertEqual(session.sent, [])
        bridge.server_event(
            {
                "type": "response.event",
                "delegation_id": "d",
                "event": {"type": "response.completed", "response": {"id": "r"}},
            }
        )
        self.assertEqual(
            [e["type"] for e in session.sent],
            ["response.item.create", "response.create"],
        )
        self.assertIn(item["text"], session.sent[0]["item"]["content"][0]["text"])
        self.assertEqual(bridge.cursor, 4)
        bridge.enqueue([item])
        self.assertEqual(len(session.sent), 2)

    def test_pending_tool_result_blocks_continuation(self):
        session = Session()
        bridge = TypedBridge(session, 0)
        bridge.server_event(
            {
                "type": "response.event",
                "event": {
                    "type": "response.output_item.done",
                    "item": {"type": "function_call", "call_id": "c"},
                },
            }
        )
        bridge.enqueue([{"id": "i", "seq": 1, "text": "hello"}])
        self.assertEqual(session.sent, [])
        bridge.client_event(
            {
                "type": "response.item.create",
                "item": {"type": "function_call_output", "call_id": "c"},
            }
        )
        # SDK owns the tool continuation; wait for its completed response.
        bridge.server_event(
            {
                "type": "response.event",
                "event": {"type": "response.completed", "response": {"id": "r"}},
            }
        )
        self.assertEqual(session.sent[-1]["type"], "response.create")

    def test_cumulative_usage_and_response_replay(self):
        usage = UsageCounters()
        for seconds in (10, 10, 20):
            usage.event(
                {"type": "session.usage.updated", "usage": {"seconds": seconds}}
            )
        response = {
            "type": "response.event",
            "event": {
                "type": "response.completed",
                "response": {
                    "id": "r",
                    "usage": {
                        "input_tokens": 5,
                        "output_tokens": 2,
                        "input_tokens_details": {"cached_tokens": 1},
                    },
                },
            },
        }
        usage.event(response)
        usage.event(response)
        self.assertEqual(usage.seconds, 20)
        self.assertEqual(usage.input_tokens, 5)
        self.assertEqual(usage.output_tokens, 2)


if __name__ == "__main__":
    unittest.main()


class DurableBridgeTests(unittest.IsolatedAsyncioTestCase):
    async def test_failed_checkpoint_never_sends_input(self):
        session = Session()
        bridge = TypedBridge(session, 0, auto_flush=False)
        bridge.enqueue([{"id": "i", "seq": 1, "text": "do something"}])

        async def reject(cursor, completed):
            raise RuntimeError("lease revoked")

        with self.assertRaises(RuntimeError):
            await bridge.deliver(reject)
        self.assertEqual(session.sent, [])

    async def test_checkpoint_precedes_delivery(self):
        session = Session()
        bridge = TypedBridge(session, 0, auto_flush=False)
        bridge.enqueue([{"id": "i", "seq": 1, "text": "exact value"}])

        async def checkpoint(cursor, completed):
            self.assertEqual(session.sent, [])
            self.assertEqual((cursor, completed), (1, 0))

        await bridge.deliver(checkpoint)
        self.assertEqual(len(session.sent), 2)


class IncompleteUsageTests(unittest.TestCase):
    def test_usage_without_response_identity_cannot_be_settled(self):
        usage = UsageCounters()
        usage.event(
            {
                "type": "response.event",
                "event": {
                    "type": "response.completed",
                    "response": {"usage": {"input_tokens": 12, "output_tokens": 4}},
                },
            }
        )
        usage.event({"type": "session.closed", "usage": {"seconds": 2}})
        self.assertFalse(usage.report(closed=True)["final"])

    def test_terminal_responses_account_for_partial_usage_once(self):
        for terminal in (
            "response.incomplete",
            "response.failed",
            "response.cancelled",
        ):
            with self.subTest(terminal=terminal):
                usage = UsageCounters()
                usage.event(
                    {
                        "type": "response.event",
                        "delegation_id": "d",
                        "event": {"type": "response.created"},
                    }
                )
                event = {
                    "type": "response.event",
                    "delegation_id": "d",
                    "event": {
                        "type": terminal,
                        "response": {
                            "id": "r",
                            "usage": {
                                "input_tokens": 12,
                                "output_tokens": 4,
                                "input_tokens_details": {"cached_tokens": 3},
                            },
                        },
                    },
                }
                usage.event(event)
                usage.event(event)
                usage.event({"type": "session.closed", "usage": {"seconds": 12}})
                self.assertEqual(
                    (usage.input_tokens, usage.cached_tokens, usage.output_tokens),
                    (12, 3, 4),
                )
                self.assertTrue(usage.report(closed=True)["final"])

    def test_terminal_response_without_usage_remains_uncertain(self):
        usage = UsageCounters()
        usage.event(
            {
                "type": "response.event",
                "event": {"type": "response.incomplete", "response": {"id": "r"}},
            }
        )
        usage.event({"type": "session.closed", "usage": {"seconds": 2}})
        self.assertFalse(usage.report(closed=True)["final"])

    def test_voice_close_with_pending_backend_keeps_reservation_uncertain(self):
        usage = UsageCounters()
        usage.event(
            {
                "type": "response.event",
                "delegation_id": "d",
                "event": {"type": "response.created", "response": {"id": "r"}},
            }
        )
        usage.event({"type": "session.closed", "usage": {"seconds": 12}})
        self.assertFalse(usage.report(closed=True)["final"])
        usage.event(
            {
                "type": "response.event",
                "delegation_id": "d",
                "event": {
                    "type": "response.completed",
                    "response": {
                        "id": "r",
                        "usage": {"input_tokens": 1, "output_tokens": 1},
                    },
                },
            }
        )
        self.assertTrue(usage.report(closed=True)["final"])
