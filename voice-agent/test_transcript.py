import unittest
import asyncio
import json

from transcript import TranscriptCollector


class TranscriptTest(unittest.IsolatedAsyncioTestCase):
    async def test_only_audio_transcripts_preserve_repeated_text_and_retry(self):
        calls = []

        async def send(path, body):
            calls.append((path, body))
            if len(calls) == 1:
                raise OSError("lost acknowledgement")
            return {"truncated": False}

        collector = TranscriptCollector(send)
        collector.event({"type": "session.started"})
        collector.event({"type": "session.input_transcript.delta", "delta": " yes"})
        collector.event({"type": "session.input_transcript.delta", "delta": " yes"})
        collector.event({"type": "session.commentary.appended", "delta": "private"})
        with self.assertRaises(OSError):
            await collector.flush()
        collector.event({"type": "session.output_transcript.delta", "delta": " Okay."})
        await collector.flush()
        self.assertEqual(calls[0], calls[1])
        await collector.flush()
        self.assertEqual(
            calls[1][1]["entries"], [{"speaker": "participants", "text": " yes yes"}]
        )
        self.assertEqual(calls[2][1]["sequence"], 2)
        self.assertEqual(
            calls[2][1]["entries"], [{"speaker": "agent", "text": " Okay."}]
        )

    async def test_silence_and_concurrent_flush_do_not_duplicate(self):
        calls = []

        async def send(path, body):
            calls.append(body)
            return {"truncated": False}

        collector = TranscriptCollector(send)
        await collector.flush()
        self.assertEqual(calls, [])
        collector.event({"type": "session.started"})
        await asyncio.gather(collector.flush(), collector.flush())
        self.assertEqual(calls, [{"sequence": 1, "entries": [], "truncated": False}])

    async def test_bounded_queue_reports_truncation(self):
        calls = []

        async def send(path, body):
            calls.append(body)
            return {"truncated": body["truncated"]}

        collector = TranscriptCollector(send)
        collector.event({"type": "session.started"})
        for _ in range(100):
            collector.event(
                {"type": "session.input_transcript.delta", "delta": "a" * 4000}
            )
        await collector.drain()
        self.assertTrue(calls[-1]["truncated"])
        count = len(calls)
        collector.event({"type": "session.input_transcript.delta", "delta": "later"})
        await collector.flush()
        self.assertEqual(len(calls), count)

    async def test_escaped_text_batches_stay_within_api_limit(self):
        calls = []

        async def send(path, body):
            self.assertLessEqual(
                len(json.dumps(body, ensure_ascii=False).encode()), 24 * 1024
            )
            self.assertTrue(
                all(len(entry["text"].encode()) <= 4000 for entry in body["entries"])
            )
            calls.extend(entry["text"] for entry in body["entries"])
            return {"truncated": False}

        collector = TranscriptCollector(send)
        text = "\x01" * 10000 + "<>&" * 1000 + "Xin chào " * 500
        collector.event({"type": "session.input_transcript.delta", "delta": text})
        await collector.drain()
        self.assertEqual("".join(calls), text)
