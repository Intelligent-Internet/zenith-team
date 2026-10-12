import unittest
import asyncio
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock, patch
from livekit import rtc
from mixer import MicrophoneMixer, admitted_microphone


class MixerAdmissionTests(unittest.TestCase):
    def test_only_authorized_human_microphones(self):
        human = SimpleNamespace(
            identity="human", kind=rtc.ParticipantKind.PARTICIPANT_KIND_STANDARD
        )
        agent = SimpleNamespace(
            identity="agent", kind=rtc.ParticipantKind.PARTICIPANT_KIND_AGENT
        )
        mic = SimpleNamespace(source=rtc.TrackSource.SOURCE_MICROPHONE, muted=False)
        camera = SimpleNamespace(source=rtc.TrackSource.SOURCE_CAMERA, muted=False)
        self.assertTrue(admitted_microphone(human, mic, {"human"}))
        self.assertFalse(admitted_microphone(human, mic, set()))
        self.assertFalse(admitted_microphone(agent, mic, {"agent"}))
        self.assertFalse(admitted_microphone(human, camera, {"human"}))
        mic.muted = True
        self.assertFalse(admitted_microphone(human, mic, {"human"}))


class MixerFramesTests(unittest.IsolatedAsyncioTestCase):
    async def test_audio_activity_veto_precedes_model_transcription(self):
        from array import array

        mixer = MicrophoneMixer(SimpleNamespace(remote_participants={}), [])
        try:
            quiet = rtc.AudioFrame(array("h", [0] * 480).tobytes(), 24000, 1, 480)
            loud = rtc.AudioFrame(array("h", [-900] * 480).tobytes(), 24000, 1, 480)

            async def stream():
                yield SimpleNamespace(frame=quiet)
                yield SimpleNamespace(frame=loud)

            frames = mixer.frames(stream())
            initial = mixer.last_activity
            with patch("mixer.time.monotonic", return_value=initial + 3):
                self.assertIs(await anext(frames), quiet)
                self.assertEqual(mixer.last_activity, initial)
                self.assertIs(await anext(frames), loud)
                self.assertEqual(mixer.last_activity, initial + 3)
            await frames.aclose()
        finally:
            await mixer.aclose()

    async def test_replaced_microphone_closes_old_stream_and_revocation_removes_new(
        self,
    ):
        publication = SimpleNamespace(
            sid="mic",
            source=rtc.TrackSource.SOURCE_MICROPHONE,
            muted=False,
            track=object(),
            set_subscribed=Mock(),
        )
        participant = SimpleNamespace(
            identity="human",
            kind=rtc.ParticipantKind.PARTICIPANT_KIND_STANDARD,
            track_publications={"mic": publication},
        )
        room = SimpleNamespace(remote_participants={"human": participant})
        old_stream = SimpleNamespace(aclose=AsyncMock())
        new_stream = SimpleNamespace(aclose=AsyncMock())
        native_mixer = SimpleNamespace(
            add_stream=Mock(), remove_stream=Mock(), aclose=AsyncMock()
        )

        async def until(predicate):
            async with asyncio.timeout(1):
                while not predicate():
                    await asyncio.sleep(0.01)

        with (
            patch("mixer.rtc.AudioMixer", return_value=native_mixer),
            patch(
                "mixer.rtc.AudioStream", side_effect=[old_stream, new_stream]
            ) as streams,
        ):
            mixer = MicrophoneMixer(room, ["human"])
            try:
                await until(lambda: streams.call_count == 1)
                publication.track = object()
                await until(lambda: streams.call_count == 2)
                old_stream.aclose.assert_awaited_once()
                mixer.admitted.clear()
                await until(lambda: not mixer.streams)
                new_stream.aclose.assert_awaited_once()
                publication.set_subscribed.assert_called_with(False)
                self.assertEqual(native_mixer.remove_stream.call_count, 2)
            finally:
                await mixer.aclose()

    async def test_two_four_eight_simultaneous_inputs(self):
        from array import array
        import asyncio

        for count in (2, 4, 8):
            mixer = rtc.AudioMixer(24000, 1, blocksize=480)

            async def frames():
                yield rtc.AudioFrame(array("h", [1000] * 480).tobytes(), 24000, 1, 480)

            for _ in range(count):
                mixer.add_stream(frames())
            result = await asyncio.wait_for(mixer.__anext__(), 1)
            self.assertEqual(list(result.data), [count * 1000] * 480)
            await mixer.aclose()
