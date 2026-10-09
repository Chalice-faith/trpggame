from __future__ import annotations

import json
import unittest

import httpx

from app.services.llm_client import (
    DeepSeekClient,
    LLMAPIError,
    LLMConfigurationError,
)


class DeepSeekClientTests(unittest.IsolatedAsyncioTestCase):
    async def test_chat_returns_content_and_sends_expected_contract(self):
        captured_request: httpx.Request | None = None

        async def handler(request: httpx.Request) -> httpx.Response:
            nonlocal captured_request
            captured_request = request
            return httpx.Response(
                200,
                json={"choices": [{"message": {"content": "古宅的大门缓缓开启。"}}]},
            )

        client = self._client(handler)
        result = await client.chat("开始游戏", "你是一名 TRPG 主持人")

        self.assertEqual(result, "古宅的大门缓缓开启。")
        self.assertIsNotNone(captured_request)
        assert captured_request is not None
        self.assertEqual(
            str(captured_request.url),
            "https://deepseek.example.test/chat/completions",
        )
        self.assertEqual(captured_request.headers["authorization"], "Bearer test-key")
        payload = json.loads(captured_request.content)
        self.assertEqual(payload["model"], "deepseek-test")
        self.assertEqual(payload["temperature"], 0.2)
        self.assertEqual(payload["max_tokens"], 128)
        self.assertFalse(payload["stream"])
        self.assertEqual(
            payload["messages"],
            [
                {"role": "system", "content": "你是一名 TRPG 主持人"},
                {"role": "user", "content": "开始游戏"},
            ],
        )

    async def test_chat_stream_yields_text_until_done(self):
        async def handler(request: httpx.Request) -> httpx.Response:
            payload = json.loads(request.content)
            self.assertTrue(payload["stream"])
            body = "\n".join(
                [
                    ': keep-alive',
                    'data: {"choices":[{"delta":{"content":"你"}}]}',
                    'data: {"choices":[{"delta":{"content":"好"}}]}',
                    'data: {"choices":[{"delta":{},"finish_reason":"stop"}]}',
                    'data: [DONE]',
                    '',
                ]
            )
            return httpx.Response(
                200,
                headers={"content-type": "text/event-stream"},
                content=body.encode("utf-8"),
            )

        chunks = [chunk async for chunk in self._client(handler).chat_stream("继续")]

        self.assertEqual(chunks, ["你", "好"])

    async def test_missing_api_key_fails_before_network_request(self):
        requested = False

        async def handler(request: httpx.Request) -> httpx.Response:
            nonlocal requested
            requested = True
            return httpx.Response(200)

        client = self._client(handler, api_key="")

        with self.assertRaisesRegex(LLMConfigurationError, "DEEPSEEK_API_KEY"):
            await client.chat("开始")
        self.assertFalse(requested)

    async def test_http_error_exposes_status_and_api_message(self):
        async def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                401,
                json={"error": {"code": "unauthorized", "message": "invalid key"}},
            )

        with self.assertRaisesRegex(LLMAPIError, "invalid key") as raised:
            await self._client(handler).chat("开始")

        self.assertEqual(raised.exception.status_code, 401)

    async def test_invalid_stream_json_raises_domain_error(self):
        async def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                200,
                headers={"content-type": "text/event-stream"},
                content=b"data: not-json\n\n",
            )

        with self.assertRaisesRegex(LLMAPIError, "invalid JSON"):
            async for _ in self._client(handler).chat_stream("继续"):
                self.fail("invalid stream must not yield content")

    async def test_http_200_upstream_error_preserves_provider_status(self):
        async def handler(request):
            return httpx.Response(200, json={"id": "generation-test", "error": {
                "message": "Service temporarily overloaded", "code": 503}})

        with self.assertRaisesRegex(LLMAPIError, "temporarily overloaded") as raised:
            await self._client(handler).chat("开始")
        self.assertEqual(raised.exception.status_code, 503)

    async def test_stream_http_200_json_error_is_not_empty_success(self):
        async def handler(request):
            return httpx.Response(200, json={"error": {"message": "quota exhausted", "code": 429}})

        with self.assertRaisesRegex(LLMAPIError, "quota exhausted") as raised:
            async for _ in self._client(handler).chat_stream("继续"):
                self.fail("failed provider must not yield narrative")
        self.assertEqual(raised.exception.status_code, 429)

    async def test_stream_error_after_partial_content_raises(self):
        async def handler(request):
            return httpx.Response(200, headers={"content-type": "text/event-stream"},
                content='data: {"choices":[{"delta":{"content":"partial"}}]}\n\ndata: {"error":{"message":"overloaded","code":503}}\n\n')

        chunks = []
        with self.assertRaisesRegex(LLMAPIError, "overloaded") as raised:
            async for chunk in self._client(handler).chat_stream("继续"):
                chunks.append(chunk)
        self.assertEqual(chunks, ["partial"])
        self.assertEqual(raised.exception.status_code, 503)

    async def test_non_http_provider_error_code_remains_unspecified(self):
        async def handler(request):
            return httpx.Response(200, json={"error": {"message": "provider failed", "code": "provider_error"}})

        with self.assertRaisesRegex(LLMAPIError, "provider failed") as raised:
            await self._client(handler).chat("开始")
        self.assertIsNone(raised.exception.status_code)

    async def test_usage_logging_contains_only_counters_and_estimate(self):
        async def handler(request):
            return httpx.Response(200, json={"choices": [{"message": {"content": "ok"}}],
                "usage": {"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15,
                    "private_metadata": "do-not-log"}})

        with self.assertLogs("app.services.llm_client", level="INFO") as logged:
            self.assertEqual(await self._client(handler).chat("private player action"), "ok")
        record = json.loads(logged.output[0].split("llm_usage ", 1)[1])
        self.assertEqual(record["prompt_tokens"], 12)
        self.assertGreater(record["estimated_input_tokens"], 12)
        self.assertNotIn("private player action", logged.output[0])
        self.assertNotIn("do-not-log", logged.output[0])

    async def test_stream_accepts_usage_only_final_event(self):
        async def handler(request):
            return httpx.Response(200, headers={"content-type": "text/event-stream"}, content=
                'data: {"choices":[{"delta":{"content":"ok"}}]}\n\ndata: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":1,"total_tokens":13}}\n\ndata: [DONE]\n\n')

        with self.assertLogs("app.services.llm_client", level="INFO") as logged:
            chunks = [chunk async for chunk in self._client(handler).chat_stream("继续")]
        self.assertEqual(chunks, ["ok"])
        self.assertIn('"prompt_tokens": 12', logged.output[0])

    async def test_complete_parses_tool_calls_and_sends_function_tools(self):
        captured_payload: dict[str, object] = {}

        async def handler(request: httpx.Request) -> httpx.Response:
            captured_payload.update(json.loads(request.content))
            return httpx.Response(
                200,
                json={
                    "choices": [
                        {
                            "message": {
                                "content": None,
                                "tool_calls": [
                                    {
                                        "id": "call-1",
                                        "type": "function",
                                        "function": {
                                            "name": "roll_dice",
                                            "arguments": '{"dice_type":"D20","target":10,"reason":"侦查"}',
                                        },
                                    }
                                ],
                            }
                        }
                    ]
                },
            )

        function = {
            "name": "roll_dice",
            "description": "server dice",
            "parameters": {"type": "object"},
        }
        completion = await self._client(handler).complete(
            "我检查书架",
            "你是主持人",
            [function],
        )

        self.assertEqual(completion.content, "")
        self.assertEqual(completion.tool_calls[0].id, "call-1")
        self.assertEqual(completion.tool_calls[0].name, "roll_dice")
        self.assertEqual(
            captured_payload["tools"],
            [{"type": "function", "function": function}],
        )
        self.assertEqual(captured_payload["tool_choice"], "auto")

    async def test_complete_rejects_malformed_tool_call(self):
        async def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                200,
                json={
                    "choices": [
                        {
                            "message": {
                                "content": None,
                                "tool_calls": [
                                    {
                                        "id": "call-1",
                                        "type": "function",
                                        "function": {
                                            "name": "roll_dice",
                                            "arguments": {},
                                        },
                                    }
                                ],
                            }
                        }
                    ]
                },
            )

        with self.assertRaisesRegex(LLMAPIError, "arguments must be JSON text"):
            await self._client(handler).complete("侦查", functions=[{"name": "roll_dice"}])

    def _client(
        self,
        handler,
        *,
        api_key: str = "test-key",
    ) -> DeepSeekClient:
        return DeepSeekClient(
            api_key=api_key,
            api_base="https://deepseek.example.test/",
            model="deepseek-test",
            temperature=0.2,
            max_tokens=128,
            timeout=5,
            transport=httpx.MockTransport(handler),
        )


if __name__ == "__main__":
    unittest.main()
