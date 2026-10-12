"""Bounded, retryable capture of the existing live model's audio text only."""

import asyncio


class TranscriptCollector:
    def __init__(self, send):
        self.send = send
        self.entries = []
        self.size = 0
        self.sequence = 0
        self.pending = None
        self.started = False
        self.truncated = False
        self.complete = False
        self.lock = asyncio.Lock()

    def event(self, event):
        if self.complete:
            return
        event_type = event.get("type")
        if event_type == "session.started":
            self.started = True
            return
        speaker = {
            "session.input_transcript.delta": "participants",
            "session.output_transcript.delta": "agent",
        }.get(event_type)
        text = event.get("delta")
        if not speaker or not isinstance(text, str) or not text:
            return
        text = text.replace("\x00", "")
        if not text:
            return
        self.started = True
        size = len(text.encode("utf-8"))
        if self.size + size > 64 * 1024 or len(self.entries) >= 256:
            self.truncated = True
            return
        # Split on Unicode characters, never halfway through encoded bytes.
        for offset in range(0, len(text), 1000):
            part = text[offset : offset + 1000]
            if (
                self.entries
                and self.entries[-1]["speaker"] == speaker
                and len((self.entries[-1]["text"] + part).encode("utf-8")) <= 1000
            ):
                self.entries[-1]["text"] += part
            else:
                self.entries.append({"speaker": speaker, "text": part})
        self.size += size

    async def flush(self):
        async with self.lock:
            if self.complete or not self.started:
                return
            if self.pending is None:
                if not self.entries and self.sequence > 0 and not self.truncated:
                    return
                entries = []
                encoded_budget = 0
                for entry in self.entries[:32]:
                    # Go also escapes HTML characters when hashing the batch.
                    # Six bytes per character bounds both encoders, including
                    # control characters, without touching the actual text.
                    encoded_budget += len(entry["text"]) * 6 + 64
                    if encoded_budget > 20 * 1024:
                        break
                    entries.append(entry)
                self.entries = self.entries[len(entries) :]
                self.size -= sum(
                    len(entry["text"].encode("utf-8")) for entry in entries
                )
                self.pending = {
                    "sequence": self.sequence + 1,
                    "entries": entries,
                    "truncated": self.truncated and not self.entries,
                }
            # Keep the exact batch until acknowledged; events arriving during
            # the request are held separately and cannot mutate a retry.
            receipt = await self.send("transcript", self.pending)
            self.sequence += 1
            self.pending = None
            if receipt.get("truncated"):
                self.complete = True
                self.entries.clear()
                self.size = 0

    async def drain(self):
        await self.flush()
        while self.entries and not self.complete:
            await self.flush()
