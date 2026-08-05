from __future__ import annotations

import unittest

from fastapi.testclient import TestClient

from app.main import create_app
from app.routers.inference import (
    ActionInferenceError,
    ActionInferenceResult,
    ActionInferenceService,
    GameActionRequest,
    get_action_inference_service,
)
from app.services.context_builder import AssembledContext
from app.services.function_calling import FunctionCallExecutor
from app.services.game_context import GameRuntimeContext
from app.services.llm_client import ChatCompletion, ToolCall


class FakeContextProvider:
    def __init__(self) -> None:
        self.calls: list[tuple[int, int, int]] = []

    async def load(
        self,
        room_id: int,
        user_id: int,
        character_id: int,
    ) -> GameRuntimeContext:
        self.calls.append((room_id, user_id, character_id))
        return GameRuntimeContext(
            summary_memory="玩家已经进入古宅。",
            recent_history=({"role": "assistant", "content": "门厅很安静。"},),
            player_state={"hp": 18, "location": "门厅"},
            character_profile={"character_id": character_id},
        )


class RecordingActionService:
    def __init__(
        self,
        result: ActionInferenceResult | None = None,
        error: Exception | None = None,
    ) -> None:
        self.result = result or ActionInferenceResult(narrative="书架后传来轻响。")
        self.error = error
        self.requests: list[GameActionRequest] = []

    async def infer(self, request: GameActionRequest) -> ActionInferenceResult:
        self.requests.append(request)
        if self.error is not None:
            raise self.error
        return self.result


class ActionInferenceServiceTests(unittest.IsolatedAsyncioTestCase):
    async def test_returns_direct_narrative_without_tools(self):
        calls: dict[str, object] = {}
        provider = FakeContextProvider()

        async def retriever(query: str, script_id: int) -> list[str]:
            calls["retrieval"] = (query, script_id)
            return ["书房藏有机关。"]

        def context_builder(action: str, **context) -> AssembledContext:
            calls["context"] = (action, context)
            return AssembledContext("system", "user prompt", ())

        async def completer(prompt: str, system: str, functions) -> ChatCompletion:
            calls["completion"] = (prompt, system, functions)
            return ChatCompletion(content="  你发现书架底部有擦痕。 ")

        async def unexpected_generator(prompt: str, system: str) -> str:
            self.fail("a direct completion must not trigger a second LLM request")

        service = ActionInferenceService(
            retriever=retriever,
            context_provider=provider,
            context_builder=context_builder,
            completion_generator=completer,
            narrative_generator=unexpected_generator,
        )
        request = self._request()

        result = await service.infer(request)

        self.assertEqual(result.narrative, "你发现书架底部有擦痕。")
        self.assertIsNone(result.dice_roll)
        self.assertIsNone(result.status_changes)
        self.assertEqual(calls["retrieval"], (request.action, request.script_id))
        self.assertEqual(provider.calls, [(1, 2, 4)])
        _, context = calls["context"]
        self.assertEqual(context["summary_memory"], "玩家已经进入古宅。")
        self.assertEqual(context["player_state"]["hp"], 18)
        self.assertEqual(len(calls["completion"][2]), 7)

    async def test_executes_dice_collects_state_change_and_regenerates_narrative(self):
        generated_prompts: list[tuple[str, str]] = []

        async def retriever(query: str, script_id: int) -> list[str]:
            return ["书架藏有线索。"]

        async def completer(prompt: str, system: str, functions) -> ChatCompletion:
            return ChatCompletion(
                content="",
                tool_calls=(
                    ToolCall(
                        id="dice-1",
                        name="roll_dice",
                        arguments='{"dice_type":"D20","target":12,"reason":"侦查书架"}',
                    ),
                    ToolCall(
                        id="item-1",
                        name="add_item",
                        arguments='{"player_id":2,"item_name":"黄铜钥匙"}',
                    ),
                ),
            )

        def executor_factory(handlers):
            return FunctionCallExecutor(
                handlers=handlers,
                dice_checker=lambda dice_type, target: {
                    "type": dice_type,
                    "result": 17,
                    "target": target,
                    "success": True,
                    "critical_hit": False,
                    "critical_miss": False,
                    "description": "D20 = 17 (目标 12) — 成功",
                },
            )

        async def generator(prompt: str, system: str) -> str:
            generated_prompts.append((prompt, system))
            return "你成功找到一把黄铜钥匙。"

        service = ActionInferenceService(
            retriever=retriever,
            context_provider=FakeContextProvider(),
            context_builder=lambda action, **context: AssembledContext(
                "system",
                "user prompt",
                (),
            ),
            completion_generator=completer,
            narrative_generator=generator,
            executor_factory=executor_factory,
        )

        result = await service.infer(self._request())

        self.assertEqual(result.dice_roll["result"], 17)
        self.assertEqual(result.dice_roll["reason"], "侦查书架")
        self.assertEqual(
            result.status_changes,
            {
                "calls": [
                    {
                        "name": "add_item",
                        "arguments": {
                            "player_id": 2,
                            "item_name": "黄铜钥匙",
                            "quantity": 1,
                            "description": "",
                        },
                    }
                ]
            },
        )
        self.assertIn('"result":17', generated_prompts[0][0])
        self.assertIn("不得改变骰子结果", generated_prompts[0][0])

    async def test_rejects_state_change_targeting_another_player(self):
        async def retriever(query: str, script_id: int) -> list[str]:
            return ["context"]

        async def completer(prompt: str, system: str, functions) -> ChatCompletion:
            return ChatCompletion(
                content="",
                tool_calls=(
                    ToolCall(
                        id="bad-1",
                        name="set_location",
                        arguments='{"player_id":999,"location":"密室"}',
                    ),
                ),
            )

        service = ActionInferenceService(
            retriever=retriever,
            context_provider=FakeContextProvider(),
            context_builder=lambda action, **context: AssembledContext(
                "system",
                "user prompt",
                (),
            ),
            completion_generator=completer,
        )

        with self.assertRaisesRegex(ActionInferenceError, "function call execution"):
            await service.infer(self._request())

    @staticmethod
    def _request() -> GameActionRequest:
        return GameActionRequest(
            room_id=1,
            user_id=2,
            action="我检查书架",
            script_id=3,
            character_id=4,
        )


class ActionInferenceEndpointTests(unittest.TestCase):
    def test_endpoint_returns_action_result(self):
        service = RecordingActionService(
            ActionInferenceResult(
                narrative="你发现了机关。",
                dice_roll={
                    "type": "D20",
                    "result": 17,
                    "target": 12,
                    "success": True,
                    "critical_hit": False,
                    "critical_miss": False,
                    "description": "success",
                    "reason": "侦查",
                },
                status_changes={"calls": []},
            )
        )
        app = create_app()
        app.dependency_overrides[get_action_inference_service] = lambda: service

        with TestClient(app) as client:
            response = client.post(
                "/api/v1/ai/inference/action",
                json=self._request_body(),
                headers=self._headers(),
            )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["dice_roll"]["result"], 17)
        self.assertEqual(service.requests[0].action, "我检查书架")

    def test_endpoint_requires_internal_secret_and_valid_action(self):
        app = create_app()
        with TestClient(app) as client:
            unauthorized = client.post(
                "/api/v1/ai/inference/action",
                json=self._request_body(),
            )
            body = self._request_body()
            body["action"] = "   "
            invalid = client.post(
                "/api/v1/ai/inference/action",
                json=body,
                headers=self._headers(),
            )

        self.assertEqual(unauthorized.status_code, 401)
        self.assertEqual(invalid.status_code, 422)

    def test_endpoint_maps_service_failure_to_503(self):
        service = RecordingActionService(error=ActionInferenceError("failed"))
        app = create_app()
        app.dependency_overrides[get_action_inference_service] = lambda: service

        with TestClient(app) as client:
            response = client.post(
                "/api/v1/ai/inference/action",
                json=self._request_body(),
                headers=self._headers(),
            )

        self.assertEqual(response.status_code, 503)
        self.assertEqual(
            response.json()["detail"],
            "player action inference unavailable",
        )

    @staticmethod
    def _request_body() -> dict[str, object]:
        return {
            "room_id": 1,
            "user_id": 2,
            "action": "我检查书架",
            "script_id": 3,
            "character_id": 4,
        }

    @staticmethod
    def _headers() -> dict[str, str]:
        return {"X-Internal-Secret": "dev-internal-secret-change-in-production"}


if __name__ == "__main__":
    unittest.main()
