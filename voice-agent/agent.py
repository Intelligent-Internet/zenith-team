"""Zenith Team Helia process. The API claim precedes every model connection."""

import asyncio
import contextlib
import logging
import os
import sys
import threading
import time
import uuid
from datetime import datetime, timezone

import aiohttp
import httpx
from livekit import agents
from livekit.agents import Agent, AgentSession, APIConnectOptions, AutoSubscribe
from livekit.agents.voice import room_io
from livekit.plugins import openai

from bridge import TypedBridge, UsageCounters
from mixer import MicrophoneMixer
from profile_prompt import prompts
from context_tools import ContextTools
from transcript import TranscriptCollector
from notes import notes_attempt, run_notes
from work_notices import WorkNotices

log = logging.getLogger("zenith.helia")


class SessionStop(asyncio.Event):
    """Preserve the initiating cause, not the close events produced by cleanup."""

    def __init__(self):
        super().__init__()
        self.reason = ""

    def set(self, reason="internal_error"):
        if not self.is_set():
            self.reason = reason
        super().set()

    def model_error(self, event):
        error = getattr(event, "error", event)
        if getattr(error, "recoverable", False):
            log.warning("Voice session recovering from provider error")
            return
        self.set("provider_error")


def control_failure(error):
    if isinstance(error, httpx.HTTPStatusError):
        if error.response.status_code in (401, 403, 404, 409):
            return "authority_revoked"
        return "control_unavailable"
    if isinstance(error, httpx.HTTPError):
        return "control_unavailable"
    return "internal_error"


def transient_control_error(error):
    return isinstance(error, httpx.TransportError) or (
        isinstance(error, httpx.HTTPStatusError)
        and (error.response.status_code == 429 or error.response.status_code >= 500)
    )


class LeaseWatchdog:
    """The job runs in its own SDK process; a blocked event loop cannot outlive authority."""

    def __init__(self, lease):
        self.done = threading.Event()
        self.cutoff = 0.0
        self.renew(lease)
        self.thread = threading.Thread(target=self.run, daemon=True)
        self.thread.start()

    def renew(self, lease):
        end = min(
            datetime.fromisoformat(lease[key].replace("Z", "+00:00"))
            for key in ("leaseExpiresAt", "deadlineAt")
        )
        remaining = (end - datetime.now(timezone.utc)).total_seconds()
        self.cutoff = time.monotonic() + max(0, remaining - 1)

    def run(self):
        while not self.done.wait(0.1):
            if time.monotonic() >= self.cutoff:
                log.error("Voice session forced shutdown: lease expired")
                os._exit(1)

    def close(self):
        self.done.set()
        self.thread.join(timeout=1)


class Control:
    def __init__(self, attempt):
        self.url = (
            os.environ["ZENITH_API_URL"].rstrip("/")
            + "/api/v1/internal/huddle-ai/"
            + attempt
        )
        # ZENITH_TEAM_HUDDLE_AI_WORKLOAD_SECRET is the old name; deployments may still use it.
        self.token = (
            os.environ.get("HUDDLE_AI_WORKLOAD_SECRET")
            or os.environ["ZENITH_TEAM_HUDDLE_AI_WORKLOAD_SECRET"]
        )
        self.http = httpx.AsyncClient(timeout=4, follow_redirects=False)

    async def call(self, path, body=None):
        response = await self.http.request(
            "GET" if path == "chat" else "POST",
            self.url + "/" + path,
            json=body,
            headers={"Authorization": "Bearer " + self.token},
        )
        response.raise_for_status()
        return response.json() if response.content else None


class HeliaModel(openai.realtime.GPTLiveModel):
    def __init__(
        self,
        counters,
        cursor,
        stop,
        transport,
        profile,
        allow_company_context=False,
        transcript=None,
    ):
        _, reasoning_prompt = prompts(profile, allow_company_context)
        super().__init__(
            http_session=transport,
            voice=profile["settings"]["voice"],
            delegation="responses",
            responses_options={
                "model": "gpt-5.6-luna",
                "instructions": reasoning_prompt,
                "max_output_tokens": 2048,
                "service_tier": "default",
                "parallel_tool_calls": False,
            },
            max_session_duration=None,
            conn_options=APIConnectOptions(max_retry=0, timeout=8),
        )
        self.counters = counters
        self.cursor = cursor
        self.stop = stop
        self.bridge = None
        self.transcript = transcript

    def session(self):
        session = super().session()
        self.bridge = TypedBridge(session, self.cursor, auto_flush=False)
        session.on("openai_server_event_received", self.counters.event)
        session.on("openai_server_event_received", self.bridge.server_event)
        if self.transcript:
            session.on("openai_server_event_received", self.transcript.event)
        session.on("openai_client_event_queued", self.bridge.client_event)
        session.on("error", self.stop.model_error)
        return session


async def request(job: agents.JobRequest):
    try:
        notes = notes_attempt(job.job.metadata)
        if notes:
            await job.accept(name="AI notes", identity="notes:" + notes)
            return
        attempt = str(uuid.UUID(job.job.metadata))
    except ValueError:
        await job.reject()
        return
    await job.accept(name="Voice agent", identity="helia:" + attempt)


async def entrypoint(ctx: agents.JobContext):
    notes = notes_attempt(ctx.job.metadata)
    if notes:
        await run_notes(ctx, notes)
        return
    attempt = str(uuid.UUID(ctx.job.metadata))
    session_log = logging.LoggerAdapter(
        log,
        {
            "attempt_id": attempt,
            "job_id": getattr(ctx.job, "id", ""),
            "huddle_id": ctx.job.room.name.removeprefix("huddle:"),
        },
        merge_extra=True,
    )
    control = Control(attempt)
    transcript = TranscriptCollector(control.call)

    async def save_transcript():
        try:
            await asyncio.wait_for(transcript.drain(), timeout=2)
        except (TimeoutError, httpx.HTTPError):
            session_log.warning("Voice transcript checkpoint unavailable")

    session = None
    transport = None
    mixer = None
    watchdog = None
    tasks = []
    model_closed = True
    counters = UsageCounters()
    stopped = SessionStop()
    session_log.info("Voice session starting")
    ctx.add_shutdown_callback(lambda: shutdown(stopped))
    try:
        # Duplicate dispatch, expired lease, and disabled calls fail before model construction.
        lease = await control.call("claim")
        if not lease.get("captureTranscript", True):
            transcript.complete = True
        profile = lease.get("agent")
        if (
            not profile
            or not profile.get("id")
            or not profile.get("settings", {}).get("enabled")
        ):
            raise RuntimeError("claimed Agent profile is unavailable")
        allow_company_context = lease.get("allowCompanyContext", False)
        live_prompt, _ = prompts(profile, allow_company_context)
        if lease["roomName"] != ctx.job.room.name:
            raise RuntimeError("dispatch room does not match claimed authority")
        pending_chat = lease["pendingChat"]
        completed_seq = lease["completedSeq"]
        # Claim deducts previous attempts; a renewal must not reset this allowance.
        reasoning_reserve_ticks = lease["reasoningReserveTicks"]
        control.token = lease["capability"]
        watchdog = LeaseWatchdog(lease)
        await ctx.connect(auto_subscribe=AutoSubscribe.SUBSCRIBE_NONE)
        await ctx.room.local_participant.set_name(profile["name"])
        # Recheck after media connection, immediately before opening the model socket.
        lease = await control.call("heartbeat", {"chatCursor": lease["chatCursor"]})
        watchdog.renew(lease)
        mixer = MicrophoneMixer(ctx.room, lease["humanIdentities"])

        def input_finished(task):
            if not task.cancelled() and task.exception() is not None:
                session_log.warning(
                    "Voice session stopped: microphone input unavailable"
                )
            stopped.set("microphone_error")

        mixer.task.add_done_callback(input_finished)
        connected_once = False
        trace = aiohttp.TraceConfig()

        async def authorize_socket(*_):
            nonlocal connected_once
            if connected_once:
                raise RuntimeError("model reconnect requires a new controller attempt")
            authority = await control.call(
                "heartbeat", {"chatCursor": lease["chatCursor"]}
            )
            watchdog.renew(authority)
            connected_once = True

        trace.on_request_start.append(authorize_socket)
        transport = aiohttp.ClientSession(trace_configs=[trace])
        model = HeliaModel(
            counters,
            lease["chatCursor"],
            stopped,
            transport,
            profile,
            allow_company_context,
            transcript=transcript,
        )
        context_tools = ContextTools(
            control,
            stopped,
            lambda: stopped.set("agent_left"),
            before_leave=save_transcript,
        )
        session = AgentSession(llm=model, max_tool_steps=3)
        model_closed = False
        session.input.audio = mixer
        session.on("close", lambda *_: stopped.set("provider_closed"))
        session.on("error", stopped.model_error)
        state = {"value": "listening"}
        session.on(
            "agent_state_changed",
            lambda event: state.update(
                value="speaking" if event.new_state == "speaking" else "listening"
            ),
        )
        await session.start(
            agent=Agent(
                instructions=live_prompt,
                tools=context_tools.functions(allow_company_context),
            ),
            record=False,
            session_host=False,
            room=ctx.room,
            room_options=room_io.RoomOptions(
                audio_input=False,
                video_input=False,
                text_input=False,
                text_output=False,
                close_on_disconnect=False,
            ),
        )

        model.bridge.completed_cursor = completed_seq
        model.bridge.restore(pending_chat)
        notices = WorkNotices(
            session, model.bridge, stopped, audio_activity=lambda: mixer.last_activity
        )
        session.on("user_state_changed", notices.activity)
        session.on("agent_state_changed", notices.activity)
        session_log.info("Voice session ready")

        async def checkpoint(cursor, completed):
            updated = await control.call(
                "heartbeat", {"chatCursor": cursor, "completedSeq": completed}
            )
            watchdog.renew(updated)

        async def heartbeat():
            while not stopped.is_set():
                updated = await control.call(
                    "heartbeat",
                    {
                        "chatCursor": model.bridge.cursor,
                        "completedSeq": model.bridge.completed_cursor,
                        "state": state["value"] if counters.session_id else "",
                        "pollWork": allow_company_context and bool(counters.session_id),
                    },
                )
                watchdog.renew(updated)
                notices.offer(updated.get("workNotices", []))
                if updated["greet"]:
                    session.generate_reply(
                        instructions="Briefly greet the people once using your configured name, then listen."
                    )
                mixer.admitted = set(updated["humanIdentities"])
                # Leave cleanup time inside the lease and the room budget deadline.
                if watchdog.cutoff - time.monotonic() < 8:
                    stopped.set("lease_expired")
                    return
                if counters.session_id:
                    await control.call("usage", counters.report())
                await asyncio.sleep(5)

        async def chat():
            while not stopped.is_set():
                model.bridge.enqueue(await control.call("chat"))
                await model.bridge.deliver(checkpoint)
                # Reserve headroom for an in-flight bounded reasoning response.
                reasoning = (
                    (counters.input_tokens - counters.cached_tokens) * 2000
                    + counters.cached_tokens * 200
                    + counters.output_tokens * 12000
                )
                if reasoning >= max(0, reasoning_reserve_ticks - 2500000000):
                    stopped.set("reasoning_budget")
                    return
                if allow_company_context:
                    await notices.deliver(claim_notice)
                await asyncio.sleep(1)

        async def claim_notice(notice_id):
            updated = await control.call(
                "heartbeat",
                {
                    "chatCursor": model.bridge.cursor,
                    "completedSeq": model.bridge.completed_cursor,
                    "claimWorkNotice": notice_id,
                },
            )
            watchdog.renew(updated)
            return updated

        async def deadline():
            while not stopped.is_set():
                if watchdog.cutoff - time.monotonic() < 7:
                    stopped.set("lease_expired")
                    return
                await asyncio.sleep(0.2)

        async def supervise(name, operation):
            while not stopped.is_set():
                try:
                    await operation()
                    return
                except asyncio.CancelledError:
                    raise
                except Exception as error:
                    if (
                        transient_control_error(error)
                        and watchdog.cutoff - time.monotonic() > 8
                    ):
                        session_log.warning(
                            "Voice control retry within lease",
                            extra={"attempt_id": attempt, "operation": name},
                        )
                        await asyncio.sleep(0.5)
                        continue
                    stopped.set(control_failure(error))
                    return

        tasks = [
            asyncio.create_task(supervise("heartbeat", heartbeat)),
            asyncio.create_task(supervise("chat", chat)),
            asyncio.create_task(supervise("deadline", deadline)),
            asyncio.create_task(capture_transcript(transcript, stopped, session_log)),
        ]
        await stopped.wait()
    except asyncio.CancelledError:
        stopped.set("worker_shutdown")
        raise
    except Exception as error:
        # Never log request headers, chat text, provider responses, or credentials.
        stopped.set(control_failure(error))
    finally:
        if not stopped.is_set():
            stopped.set("worker_shutdown")
        session_log.info(
            "Voice session stopping",
            extra={"attempt_id": attempt, "stop_reason": stopped.reason},
        )
        for task in tasks:
            task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)
        if session:
            session.input.audio = None
            with contextlib.suppress(Exception):
                await asyncio.wait_for(session.aclose(), timeout=5.5)
                model_closed = True
        if transport:
            model_closed = (
                await close_resource(transport.close(), "model transport")
                or model_closed
            )
        if watchdog and model_closed:
            watchdog.close()
        # Never delay provider shutdown for text persistence. A revoked/expired
        # attempt leaves its last authorized checkpoint, not a late content write.
        await save_transcript()
        # Persist accounting before media teardown: a disconnected room must not
        # prevent the final receipt from releasing unused budget.
        if counters.session_id:
            try:
                report = counters.report(closed=model_closed)
                if model_closed:
                    report["stopReason"] = stopped.reason
                await control.call("usage", report)
            except httpx.HTTPError:
                session_log.warning(
                    "Voice final usage unavailable; budget remains reserved"
                )
        if mixer:
            await close_resource(mixer.aclose(), "microphone mixer")
        await close_resource(ctx.room.disconnect(), "room")
        await control.http.aclose()
        session_log.info(
            "Voice session finished; worker may remain available for invitations",
            extra={"attempt_id": attempt, "stop_reason": stopped.reason},
        )
        ctx.shutdown(reason=stopped.reason)


async def capture_transcript(transcript, stopped, session_log):
    while not stopped.is_set():
        try:
            await transcript.flush()
        except httpx.HTTPError:
            # Capture failure must not end human audio or the Agent session.
            # Retrying the same sequence is safe after a lost acknowledgement.
            session_log.warning("Voice transcript checkpoint unavailable")
        await asyncio.sleep(2)


async def close_resource(operation, label):
    try:
        await asyncio.wait_for(operation, timeout=2)
        return True
    except Exception:
        log.warning("Helia cleanup incomplete: %s", label)
        return False


async def shutdown(stopped):
    stopped.set("worker_shutdown")


def run_worker(local=None):
    agents.cli.run_app(
        agents.WorkerOptions(
            entrypoint_fnc=entrypoint,
            request_fnc=request,
            agent_name="zenith-helia",
            job_executor_type=agents.JobExecutorType.PROCESS,
            drain_timeout=3900,
            # The SDK first waits 15s for the entrypoint, then cancels it.
            # Leave time for our finally block to close the model and send usage.
            shutdown_process_timeout=35,
            port=8081,
            num_idle_processes=1,
            load_fnc=local.load
            if local
            else lambda server: len(server.active_jobs) / 2,
            load_threshold=1.0,
        )
    )


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "dev":
        from local_worker import LocalWorker, WorkerBusy, require_free_port

        try:
            with LocalWorker() as local:
                require_free_port()
                print(
                    f"Local voice worker PID {local.pid}; foreground logs in this terminal. "
                    "Use make voice-status / make voice-stop from the repository root.",
                    flush=True,
                )
                run_worker(local)
        except WorkerBusy as error:
            print(error, file=sys.stderr)
            raise SystemExit(1) from None
    else:
        run_worker()
