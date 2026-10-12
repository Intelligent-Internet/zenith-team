"""Exercise the pinned public SDK against a local protocol peer, with no paid API."""

import asyncio
import unittest
import json
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock

from aiohttp import web, ClientSession
from livekit.agents import Agent, AgentSession, APIConnectOptions
from livekit.plugins.openai.realtime import GPTLiveModel

from bridge import TypedBridge
from context_tools import ContextTools
from work_notices import WorkNotices


class SDKContractTests(unittest.IsolatedAsyncioTestCase):
    async def test_registered_context_tool_executes_and_returns_on_backend_channel(
        self,
    ):
        await self.run_tool_case(
            "get_company_context",
            {},
            {"items": [{"title": "Verified company"}]},
            "tools",
        )

    async def test_registered_proposal_tool_saves_only_a_draft(self):
        await self.run_tool_case(
            "propose_task_work",
            {"projectId": "project-1", "title": "Fix login", "brief": "Verify login"},
            {"id": "draft-1", "state": "pending"},
            "proposals",
        )

    async def test_registered_leave_tool_uses_own_attempt_channel(self):
        await self.run_tool_case("leave_meeting", {}, {"left": True}, "leave")

    async def test_work_status_discovers_only_this_calls_submissions(self):
        await self.run_tool_case(
            "get_work_status",
            {},
            {"items": [{"id": "proposal-1", "work": {"state": "waiting_for_runtime"}}]},
            "tools",
        )

    async def test_work_result_uses_proposal_identity_not_arbitrary_run(self):
        await self.run_tool_case(
            "get_work_result",
            {"proposalId": "proposal-1"},
            {
                "items": [
                    {"work": {"state": "completed", "resultText": "Verified answer"}}
                ]
            },
            "tools",
        )

    async def run_tool_case(self, tool_name, arguments, result, path):
        events = asyncio.Queue()
        sockets = asyncio.Queue()

        async def peer(request):
            ws = web.WebSocketResponse()
            await ws.prepare(request)
            await sockets.put(ws)
            async for message in ws:
                event = message.json()
                await events.put(event)
                if event["type"] == "session.start":
                    await ws.send_json(
                        {"type": "session.started", "session": {"id": "test-tools"}}
                    )
                elif event["type"] == "session.close":
                    await ws.send_json(
                        {"type": "session.closed", "usage": {"seconds": 1}}
                    )
            return ws

        app = web.Application()
        app.router.add_get("/{path:.*}", peer)
        runner = web.AppRunner(app)
        await runner.setup()
        import socket

        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
        listener.setblocking(False)
        await web.SockSite(runner, listener).start()
        control = SimpleNamespace(call=AsyncMock(return_value=result))
        tools = ContextTools(control, asyncio.Event())
        try:
            async with ClientSession() as transport:
                model = GPTLiveModel(
                    api_key="local-test",
                    base_url=f"http://127.0.0.1:{port}/v1",
                    http_session=transport,
                    delegation="responses",
                    responses_options={"model": "gpt-5.6-luna"},
                    conn_options=APIConnectOptions(max_retry=0),
                )
                session = AgentSession(llm=model)
                try:
                    await session.start(
                        agent=Agent(
                            instructions="Read-only test", tools=tools.functions()
                        ),
                        record=False,
                        session_host=False,
                    )
                    start = await asyncio.wait_for(events.get(), 3)
                    definitions = start["session"]["delegation"]["responses"]["tools"]
                    self.assertEqual(
                        {tool["name"] for tool in definitions},
                        {
                            "leave_meeting",
                            "get_company_context",
                            "list_projects",
                            "search_tasks",
                            "get_task",
                            "propose_task_work",
                            "get_work_status",
                            "get_work_result",
                        },
                    )
                    ws = await asyncio.wait_for(sockets.get(), 3)
                    for event in (
                        {
                            "type": "response.created",
                            "response": {"id": "response-test"},
                        },
                        {
                            "type": "response.output_item.done",
                            "item": {
                                "type": "function_call",
                                "id": "function-test",
                                "call_id": "call-test",
                                "name": tool_name,
                                "arguments": json.dumps(arguments),
                                "status": "completed",
                            },
                        },
                        {
                            "type": "response.completed",
                            "response": {"id": "response-test"},
                        },
                    ):
                        await ws.send_json(
                            {
                                "type": "response.event",
                                "delegation_id": None,
                                "event": event,
                            }
                        )

                    async def wait_output():
                        while True:
                            event = await events.get()
                            if (
                                event.get("item", {}).get("type")
                                == "function_call_output"
                            ):
                                return event["item"]

                    output = await asyncio.wait_for(wait_output(), 5)
                    self.assertEqual(output["call_id"], "call-test")
                    self.assertEqual(json.loads(output["output"]), result)
                    control.call.assert_awaited_once()
                    self.assertEqual(control.call.call_args.args[0], path)
                finally:
                    await session.aclose()
        finally:
            await runner.cleanup()

    async def test_typed_input_uses_the_active_duplex_socket(self):
        await self.run_input_case(proactive=False)

    async def test_work_notice_uses_the_active_voice_socket_after_checkpoint(self):
        await self.run_input_case(proactive=True)

    async def run_input_case(self, *, proactive):
        events = asyncio.Queue()

        async def peer(request):
            socket = web.WebSocketResponse()
            await socket.prepare(request)
            async for message in socket:
                event = message.json()
                await events.put(event)
                if event["type"] == "session.start":
                    await socket.send_json(
                        {"type": "session.started", "session": {"id": "test-session"}}
                    )
                elif event["type"] == "session.close":
                    await socket.send_json(
                        {"type": "session.closed", "usage": {"seconds": 1}}
                    )
            return socket

        app = web.Application()
        app.router.add_get("/{path:.*}", peer)
        runner = web.AppRunner(app)
        await runner.setup()
        # SockSite exposes a test-owned port without depending on runner internals.
        self.addAsyncCleanup(runner.cleanup)
        import socket

        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
        listener.setblocking(False)
        site = web.SockSite(runner, listener)
        await site.start()
        async with ClientSession() as transport:
            model = GPTLiveModel(
                api_key="local-test",
                base_url=f"http://127.0.0.1:{port}/v1",
                http_session=transport,
                conn_options=APIConnectOptions(max_retry=0),
            )
            session = AgentSession(llm=model)
            agent = Agent(instructions="You are Helia.")
            try:
                await session.start(agent=agent, record=False, session_host=False)
                start = await asyncio.wait_for(events.get(), 3)
                self.assertEqual(start["type"], "session.start")
                bridge = TypedBridge(agent.duplex_session, 0)
                if proactive:
                    clock = Mock(return_value=0)
                    notices = WorkNotices(session, bridge, asyncio.Event(), clock=clock)
                    notice = {
                        "id": "notice-test",
                        "taskKey": "TASK-1",
                        "state": "completed",
                    }
                    notices.offer([notice])
                    clock.return_value = 3
                    claim = AsyncMock(return_value={"claimedWorkNotice": notice})
                    await notices.deliver(claim)
                    claim.assert_awaited_once_with("notice-test")
                    while True:
                        event = await asyncio.wait_for(events.get(), 3)
                        if event["type"] == "session.commentary.append":
                            self.assertIn("TASK-1", json.dumps(event))
                            self.assertIn(
                                "Do not claim the Task is done", json.dumps(event)
                            )
                            break
                    return
                bridge.enqueue(
                    [{"id": "i", "seq": 1, "text": "https://example.test/a?b=2"}]
                )
                sent = []
                while len(sent) < 2:
                    event = await asyncio.wait_for(events.get(), 3)
                    if event["type"].startswith("response."):
                        sent.append(event)
                self.assertEqual(
                    [event["type"] for event in sent],
                    ["response.item.create", "response.create"],
                )
                self.assertIn(
                    "https://example.test/a?b=2", sent[0]["item"]["content"][0]["text"]
                )
            finally:
                await session.aclose()
