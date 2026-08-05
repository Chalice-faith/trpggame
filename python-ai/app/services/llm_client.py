"""GLM 对话补全封装，支持完整响应与 SSE 流式响应。"""

from __future__ import annotations

import json
from collections.abc import AsyncGenerator, Mapping, Sequence
from dataclasses import dataclass
from typing import Any

import httpx

from app.config import settings


class LLMClientError(RuntimeError):
    """LLM 客户端基础异常。"""


class LLMConfigurationError(LLMClientError):
    """LLM 客户端配置不完整。"""


class LLMAPIError(LLMClientError):
    """GLM API 请求或响应异常。"""

    def __init__(self, message: str, *, status_code: int | None = None):
        super().__init__(message)
        self.status_code = status_code


@dataclass(frozen=True, slots=True)
class ToolCall:
    """A validated function call requested by the model."""

    id: str
    name: str
    arguments: str


@dataclass(frozen=True, slots=True)
class ChatCompletion:
    """A non-streaming completion, including optional function calls."""

    content: str
    tool_calls: tuple[ToolCall, ...] = ()


class GLMClient:
    """通过智谱兼容接口调用 GLM 对话补全 API。"""

    def __init__(
        self,
        *,
        api_key: str | None = None,
        api_base: str | None = None,
        model: str | None = None,
        temperature: float | None = None,
        max_tokens: int | None = None,
        timeout: float | None = None,
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self.api_key = settings.glm_api_key if api_key is None else api_key
        self.api_base = (
            settings.glm_api_base if api_base is None else api_base
        ).rstrip("/")
        self.model = settings.glm_model if model is None else model
        self.temperature = (
            settings.llm_temperature if temperature is None else temperature
        )
        self.max_tokens = settings.llm_max_tokens if max_tokens is None else max_tokens
        self.timeout = settings.llm_timeout if timeout is None else timeout
        self.transport = transport

    async def chat(self, prompt: str, system_prompt: str = "") -> str:
        """调用非流式接口并返回完整文本。"""

        completion = await self.complete(prompt, system_prompt)
        return completion.content

    async def complete(
        self,
        prompt: str,
        system_prompt: str = "",
        functions: Sequence[Mapping[str, Any]] = (),
    ) -> ChatCompletion:
        """Return text and validated tool calls from a non-streaming response."""

        payload = self._build_payload(
            prompt,
            system_prompt,
            stream=False,
            functions=functions,
        )
        try:
            async with self._create_http_client() as client:
                response = await client.post(self._endpoint, json=payload)
        except httpx.HTTPError as exc:
            raise LLMAPIError(f"GLM API request failed: {exc}") from exc

        self._raise_for_status(response)
        data = self._decode_json(response.content)
        try:
            message = data["choices"][0]["message"]
        except (KeyError, IndexError, TypeError) as exc:
            raise LLMAPIError("GLM API response is missing message") from exc
        if not isinstance(message, dict):
            raise LLMAPIError("GLM API message must be an object")

        content = message.get("content")
        if content is None:
            content = ""
        if not isinstance(content, str):
            raise LLMAPIError("GLM API message content must be a string")
        return ChatCompletion(
            content=content,
            tool_calls=self._parse_tool_calls(message.get("tool_calls", [])),
        )

    async def chat_stream(
        self,
        prompt: str,
        system_prompt: str = "",
    ) -> AsyncGenerator[str, None]:
        """调用流式接口并逐段产出文本。"""

        payload = self._build_payload(prompt, system_prompt, stream=True)
        try:
            async with self._create_http_client() as client:
                async with client.stream(
                    "POST",
                    self._endpoint,
                    json=payload,
                ) as response:
                    if response.is_error:
                        await response.aread()
                        self._raise_for_status(response)

                    async for line in response.aiter_lines():
                        data = self._parse_sse_data(line)
                        if data is None:
                            continue
                        if data == "[DONE]":
                            break
                        event = self._decode_json(data.encode("utf-8"))
                        try:
                            content = event["choices"][0]["delta"].get("content")
                        except (KeyError, IndexError, TypeError) as exc:
                            raise LLMAPIError(
                                "GLM API stream event is missing delta content"
                            ) from exc
                        if content is None:
                            continue
                        if not isinstance(content, str):
                            raise LLMAPIError(
                                "GLM API stream content must be a string"
                            )
                        if content:
                            yield content
        except LLMClientError:
            raise
        except httpx.HTTPError as exc:
            raise LLMAPIError(f"GLM API stream failed: {exc}") from exc

    @property
    def _endpoint(self) -> str:
        return f"{self.api_base}/chat/completions"

    def _create_http_client(self) -> httpx.AsyncClient:
        return httpx.AsyncClient(
            headers={
                "Authorization": f"Bearer {self.api_key.strip()}",
                "Content-Type": "application/json",
            },
            timeout=httpx.Timeout(self.timeout),
            transport=self.transport,
        )

    def _build_payload(
        self,
        prompt: str,
        system_prompt: str,
        *,
        stream: bool,
        functions: Sequence[Mapping[str, Any]] = (),
    ) -> dict[str, Any]:
        if not self.api_key.strip():
            raise LLMConfigurationError("TRPG_AI_GLM_API_KEY is required")
        if not self.api_base:
            raise LLMConfigurationError("GLM API base URL is required")
        if not self.model.strip():
            raise LLMConfigurationError("GLM model is required")
        if not prompt.strip():
            raise ValueError("prompt must not be empty")

        messages: list[dict[str, str]] = []
        if system_prompt.strip():
            messages.append({"role": "system", "content": system_prompt})
        messages.append({"role": "user", "content": prompt})
        payload: dict[str, Any] = {
            "model": self.model,
            "messages": messages,
            "temperature": self.temperature,
            "max_tokens": self.max_tokens,
            "stream": stream,
        }
        if functions:
            payload["tools"] = [
                {"type": "function", "function": dict(definition)}
                for definition in functions
            ]
            payload["tool_choice"] = "auto"
        return payload

    @staticmethod
    def _parse_tool_calls(value: Any) -> tuple[ToolCall, ...]:
        if value is None:
            return ()
        if not isinstance(value, list):
            raise LLMAPIError("GLM API tool_calls must be an array")

        parsed: list[ToolCall] = []
        for index, raw_call in enumerate(value):
            if not isinstance(raw_call, dict):
                raise LLMAPIError(f"GLM API tool call {index} must be an object")
            function = raw_call.get("function")
            if raw_call.get("type") != "function" or not isinstance(function, dict):
                raise LLMAPIError(f"GLM API tool call {index} is not a function")
            call_id = raw_call.get("id")
            name = function.get("name")
            arguments = function.get("arguments")
            if not isinstance(call_id, str) or not call_id.strip():
                raise LLMAPIError(f"GLM API tool call {index} has invalid id")
            if not isinstance(name, str) or not name.strip():
                raise LLMAPIError(f"GLM API tool call {index} has invalid name")
            if not isinstance(arguments, str):
                raise LLMAPIError(
                    f"GLM API tool call {index} arguments must be JSON text"
                )
            parsed.append(
                ToolCall(
                    id=call_id.strip(),
                    name=name.strip(),
                    arguments=arguments,
                )
            )
        return tuple(parsed)

    @staticmethod
    def _parse_sse_data(line: str) -> str | None:
        line = line.strip()
        if not line or line.startswith(":"):
            return None
        if not line.startswith("data:"):
            return None
        return line[5:].strip()

    @staticmethod
    def _decode_json(content: bytes) -> dict[str, Any]:
        try:
            data = json.loads(content)
        except (json.JSONDecodeError, UnicodeDecodeError) as exc:
            raise LLMAPIError("GLM API returned invalid JSON") from exc
        if not isinstance(data, dict):
            raise LLMAPIError("GLM API response must be a JSON object")
        return data

    @staticmethod
    def _raise_for_status(response: httpx.Response) -> None:
        if not response.is_error:
            return
        detail = response.text.strip()
        try:
            data = response.json()
            error = data.get("error") if isinstance(data, dict) else None
            if isinstance(error, dict):
                detail = str(error.get("message") or error.get("code") or detail)
            elif isinstance(data, dict):
                detail = str(data.get("message") or data.get("msg") or detail)
        except (json.JSONDecodeError, UnicodeDecodeError):
            pass
        detail = detail or "unknown error"
        raise LLMAPIError(
            f"GLM API returned HTTP {response.status_code}: {detail}",
            status_code=response.status_code,
        )


async def chat(prompt: str, system_prompt: str = "") -> str:
    """使用默认配置调用 GLM 并返回完整文本。"""

    return await GLMClient().chat(prompt, system_prompt)


async def complete(
    prompt: str,
    system_prompt: str = "",
    functions: Sequence[Mapping[str, Any]] = (),
) -> ChatCompletion:
    """Use the default GLM client and return text plus optional tool calls."""

    return await GLMClient().complete(prompt, system_prompt, functions)


async def chat_stream(
    prompt: str,
    system_prompt: str = "",
) -> AsyncGenerator[str, None]:
    """使用默认配置调用 GLM 并逐段产出文本。"""

    async for content in GLMClient().chat_stream(prompt, system_prompt):
        yield content
