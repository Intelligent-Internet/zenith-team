"""The public GPTLiveSession event seam; no private SDK state or second model."""

import json
import uuid

TERMINAL_RESPONSES = {
    "response.completed",
    "response.failed",
    "response.cancelled",
    "response.incomplete",
}


class TypedBridge:
    def __init__(self, session, cursor: int, *, auto_flush=True):
        self.auto_flush = auto_flush
        self.completed_cursor = cursor
        self.active_chat_cursor = None
        self.session = session
        self.cursor = cursor
        self.pending = {}
        self.running = set()
        self.tools = {}
        self.request_pending = False

    def enqueue(self, items):
        for item in items:
            if item["seq"] > self.cursor:
                self.pending[item["seq"]] = item
        if self.auto_flush:
            self.flush()

    def server_event(self, event):
        if event.get("type") == "session.delegation.created":
            delegation = event["delegation"]
            if delegation.get("target") == "responses":
                self.running.add(delegation["id"])
        if event.get("type") != "response.event":
            return
        response = event["event"]
        key = event.get("delegation_id")
        if response["type"] == "response.created":
            if key is None and self.request_pending:
                self.active_chat_cursor = self.cursor
            self.running.add(key)
            self.request_pending = False
        elif response["type"] == "response.output_item.done":
            item = response.get("item", {})
            if item.get("type") == "function_call":
                self.tools[item["call_id"]] = key
        elif response["type"] in TERMINAL_RESPONSES:
            self.running.discard(key)
            self.request_pending = False
            if response["type"] != "response.completed":
                # The provider abandons this response's tool calls on failure.
                self.tools = {
                    call: delegation
                    for call, delegation in self.tools.items()
                    if delegation != key
                }
            if key is None and self.active_chat_cursor is not None:
                if response["type"] == "response.completed" and not self.tools:
                    self.completed_cursor = self.active_chat_cursor
                if response["type"] != "response.completed" or not self.tools:
                    self.active_chat_cursor = None
            if self.auto_flush:
                self.flush()

    async def deliver(self, checkpoint):
        if self.running or self.tools or self.request_pending or not self.pending:
            return
        batch = sorted(self.pending)[:10]
        # Durable receipt precedes delivery. Recovery restores uncertain input as context,
        # without issuing the same request or acknowledgment again.
        await checkpoint(batch[-1], self.completed_cursor)
        self.flush()

    def restore(self, items):
        for item in items:
            self.session.send_event(
                {
                    "type": "response.item.create",
                    "event_id": "restore_" + item["id"],
                    "item": {
                        "type": "message",
                        "role": "user",
                        "content": [
                            {
                                "type": "input_text",
                                "text": json.dumps(item, ensure_ascii=False),
                            }
                        ],
                    },
                }
            )
        if items:
            self.session.append_thinking(
                "A prior connection lost an unfinished typed request. Its exact values are restored to backend context. Do not replay an acknowledgment or automatically repeat its work; ask for clarification if needed. Delivery did not prove completion."
            )

    def client_event(self, event):
        if event.get("type") == "response.create":
            self.request_pending = True
        if event.get("type") == "response.item.create":
            item = event.get("item", {})
            if item.get("type") == "function_call_output":
                self.tools.pop(item["call_id"], None)

    def flush(self):
        if self.running or self.tools or self.request_pending or not self.pending:
            return
        # Bound one backend request to 10 persisted chat items. Values stay verbatim.
        batch = sorted(self.pending)[:10]
        for seq in batch:
            item = self.pending.pop(seq)
            self.session.send_event(
                {
                    "type": "response.item.create",
                    "event_id": "chat_" + item["id"],
                    "item": {
                        "type": "message",
                        "role": "user",
                        "content": [
                            {
                                "type": "input_text",
                                "text": json.dumps(item, ensure_ascii=False),
                            }
                        ],
                    },
                }
            )
            self.cursor = seq
        self.request_pending = True
        self.session.send_event(
            {"type": "response.create", "event_id": "chat_response_" + uuid.uuid4().hex}
        )


class UsageCounters:
    def __init__(self):
        self.session_id = None
        self.seconds = 0.0
        self.input_tokens = 0
        self.cached_tokens = 0
        self.output_tokens = 0
        self.responses = set()
        self.voice_closed = False
        self.open_responses = set()
        self.uncertain_response = False

    def event(self, event):
        kind = event.get("type")
        if kind == "session.started":
            self.session_id = event["session"]["id"]
        if kind in ("session.usage.updated", "session.closed"):
            self.seconds = max(self.seconds, event.get("usage", {}).get("seconds", 0))
            self.voice_closed = self.voice_closed or kind == "session.closed"
        if kind == "response.event":
            inner = event["event"]
            key = event.get("delegation_id")
            if inner["type"] == "response.created":
                self.open_responses.add(key)
            elif inner["type"] in TERMINAL_RESPONSES:
                self.open_responses.discard(key)
                if not (inner.get("response") or {}).get("usage"):
                    self.uncertain_response = True
        if kind == "response.event" and event["event"]["type"] in TERMINAL_RESPONSES:
            response = event["event"].get("response") or {}
            identity = response.get("id")
            if not identity:
                # Without a receipt identity the cost cannot be safely deduplicated.
                self.uncertain_response = True
            if identity and identity not in self.responses:
                self.responses.add(identity)
                usage = response.get("usage") or {}
                self.input_tokens += usage.get("input_tokens", 0)
                self.cached_tokens += usage.get("input_tokens_details", {}).get(
                    "cached_tokens", 0
                )
                self.output_tokens += usage.get("output_tokens", 0)

    def report(self, closed=False):
        return {
            "modelSessionId": self.session_id,
            "modelSeconds": self.seconds,
            "inputTokens": self.input_tokens,
            "cachedInputTokens": self.cached_tokens,
            "outputTokens": self.output_tokens,
            "final": self.voice_closed
            and not self.open_responses
            and not self.uncertain_response,
            "closed": closed,
        }
