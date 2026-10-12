import asyncio
import json
import unittest
from types import SimpleNamespace
from unittest.mock import AsyncMock

import httpx

from context_tools import ContextTools


class ContextToolsTests(unittest.IsolatedAsyncioTestCase):
    def make_tools(self, call=None):
        self.stop = asyncio.Event()
        self.control = SimpleNamespace(
            call=call or AsyncMock(return_value={"items": []})
        )
        return ContextTools(self.control, self.stop)

    async def test_results_are_returned_without_extra_model_call(self):
        tools = self.make_tools()
        result = await tools.lookup("get_company_context", {})
        self.assertEqual(result, {"items": []})
        path, body = self.control.call.call_args.args
        self.assertEqual(path, "tools")
        self.assertEqual(body["tool"], "get_company_context")
        self.assertTrue(body["requestId"])
        self.assertNotIn("companyId", body)
        self.assertEqual(len(tools.functions()), 8)

    async def test_proposal_reuses_key_after_timeout_and_keeps_call_alive(self):
        tools = self.make_tools(
            AsyncMock(
                side_effect=[
                    httpx.ReadTimeout("secret"),
                    {"id": "draft-1", "state": "pending"},
                ]
            )
        )
        args = {"projectId": "project-1", "title": "Fix login", "brief": "Verify login"}
        first = await tools.propose(args)
        self.assertEqual(first["error"], "proposal_unconfirmed")
        self.assertNotIn("secret", str(first))
        second = await tools.propose(args)
        self.assertEqual(second["id"], "draft-1")
        one, two = self.control.call.call_args_list
        self.assertEqual(one.args, two.args)
        self.assertEqual(one.args[0], "proposals")
        self.assertNotIn("userId", one.args[1])
        self.assertNotIn("agentId", one.args[1])
        self.assertFalse(self.stop.is_set())

    async def test_stopped_session_never_proposes(self):
        tools = self.make_tools()
        self.stop.set()
        await tools.propose(
            {"projectId": "project-1", "title": "Fix login", "brief": "Verify login"}
        )
        self.control.call.assert_not_awaited()

    async def test_leave_tool_is_available_without_company_context(self):
        tools = self.make_tools(AsyncMock(return_value={"left": True}))
        functions = tools.functions(allow_company_context=False)
        self.assertEqual(len(functions), 1)
        result = await functions[0]()
        self.assertEqual(json.loads(result), {"left": True})
        self.control.call.assert_awaited_once_with("leave", {})
        self.assertTrue(self.stop.is_set())

    async def test_failed_leave_keeps_session_available_for_retry(self):
        tools = self.make_tools(AsyncMock(side_effect=httpx.ReadTimeout("secret")))
        result = await tools.functions(allow_company_context=False)[0]()
        self.assertIn("leave_unconfirmed", result)
        self.assertNotIn("secret", result)
        self.assertFalse(self.stop.is_set())

    async def test_leave_flushes_transcript_before_revoking_authority(self):
        order = []

        async def flush():
            order.append("flush")

        async def call(path, body):
            order.append(path)
            return {"left": True}

        tools = self.make_tools(call)
        tools.before_leave = flush
        await tools.functions(allow_company_context=False)[0]()
        self.assertEqual(order, ["flush", "leave"])
        self.assertTrue(self.stop.is_set())

    async def test_http_failure_is_safe_and_does_not_end_call(self):
        request = httpx.Request("POST", "http://local/tools")
        for status in (403, 429, 500, 503):
            with self.subTest(status=status):
                error = httpx.HTTPStatusError(
                    "secret detail",
                    request=request,
                    response=httpx.Response(status, request=request),
                )
                tools = self.make_tools(AsyncMock(side_effect=error))
                result = await tools.lookup("search_tasks", {})
                self.assertIn("error", result)
                self.assertNotIn("secret", str(result))
                self.assertFalse(self.stop.is_set())

    async def test_cancel_on_session_stop(self):
        entered = asyncio.Event()
        canceled = asyncio.Event()

        async def call(*args):
            entered.set()
            try:
                await asyncio.Event().wait()
            finally:
                canceled.set()

        tools = self.make_tools(call)
        pending = asyncio.create_task(tools.lookup("get_task", {}))
        await entered.wait()
        self.stop.set()
        result = await pending
        self.assertEqual(result["error"], "session_ended")
        self.assertTrue(canceled.is_set())

    async def test_stopped_session_never_calls_api(self):
        tools = self.make_tools()
        self.stop.set()
        result = await tools.lookup("get_company_context", {})
        self.assertEqual(result["error"], "session_ended")
        self.control.call.assert_not_awaited()

    async def test_timeout_does_not_end_call(self):
        tools = self.make_tools(AsyncMock(side_effect=httpx.ReadTimeout("private URL")))
        result = await tools.lookup("get_task", {})
        self.assertEqual(result["error"], "lookup_unavailable")
        self.assertFalse(self.stop.is_set())
