from __future__ import annotations

import json
import unittest

import httpx

from app.services.llm_client import (
    GLMClient,
    LLMAPIError,
    LLMConfigurationError,
)


class GLMClientTests(unittest.IsolatedAsyncioTestCase):
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
            "https://glm.example.test/api/paas/v4/chat/completions",
        )
        self.assertEqual(captured_request.headers["authorization"], "Bearer test-key")
        payload = json.loads(captured_request.content)
        self.assertEqual(payload["model"], "glm-test")
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

        with self.assertRaisesRegex(LLMConfigurationError, "GLM_API_KEY"):
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

    def _client(
        self,
        handler,
        *,
        api_key: str = "test-key",
    ) -> GLMClient:
        return GLMClient(
            api_key=api_key,
            api_base="https://glm.example.test/api/paas/v4/",
            model="glm-test",
            temperature=0.2,
            max_tokens=128,
            timeout=5,
            transport=httpx.MockTransport(handler),
        )


if __name__ == "__main__":
    unittest.main()
