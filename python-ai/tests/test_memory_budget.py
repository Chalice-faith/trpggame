import unittest
from unittest.mock import patch, AsyncMock
from fastapi.testclient import TestClient
from app.config import Settings
from app.main import create_app
from app.services.context_builder import assemble_context, estimated_tokens, ContextAssemblyError
from app.routers.inference import GameActionRequest, InferenceMemory, ActionInferenceService
from app.services.game_context import GameRuntimeContext


class BudgetTests(unittest.TestCase):
    def test_long_context_fits_and_preserves_state_and_action(self):
        options = dict(action="调查地下室", rag_chunks=["线索" * 10000] * 5,
            recent_history=[{"role": "assistant", "content": "过去剧情" * 2000}] * 40,
            max_recent_messages=40, summary_memory="已找到钥匙。" * 40,
            player_state={"hp": 7}, key_events=["取得钥匙"])
        context = assemble_context(**options)
        self.assertLessEqual(estimated_tokens(context.system_prompt + context.user_prompt), context.budget_report["input_budget"])
        self.assertIn("调查地下室", context.user_prompt)
        self.assertIn('"hp":7', context.system_prompt)
        self.assertTrue(context.budget_report["memory_degraded"])
        self.assertEqual(context, assemble_context(**options))

    def test_oversized_authoritative_state_rejected(self):
        with self.assertRaises(ContextAssemblyError):
            assemble_context("调查", rag_chunks=[], player_state={"items": "物品" * 20000})

    def test_payload_counts_tool_schema_and_output_reservation(self):
        from app.services.llm_client import DeepSeekClient, LLMConfigurationError
        from app.config import settings
        client = DeepSeekClient(api_key="fixture-only")
        with patch.object(settings, "context_window_tokens", 8000):
            with self.assertRaises(LLMConfigurationError):
                client._build_payload("调查", "规则", stream=False, functions=[{"name": "tool", "description": "内容" * 2000}])
        self.assertLess(ActionInferenceService._action_prompt_budget(), settings.context_input_budget - 4096)

    def test_go_memory_overrides_redis_and_keeps_summary_gap(self):
        memory = InferenceMemory(timeline_id="branch", through_position=20, summary_through_position=5, summary_version=1,
            summary="已归档摘要", messages=[{"role": "assistant", "content": f"事实{i}"} for i in range(30)])
        request = GameActionRequest(room_id=1, user_id=2, script_id=3, character_id=4, action="调查", memory_context=memory)
        runtime = GameRuntimeContext("过期 Redis 摘要", (), {}, {})
        context = assemble_context("调查", rag_chunks=[], **ActionInferenceService._memory_options(request, runtime))
        self.assertIn("已归档摘要", context.system_prompt)
        self.assertNotIn("过期 Redis", context.system_prompt)
        self.assertEqual(len(context.recent_history), 30)

    def test_production_refuses_defaults_and_missing_key(self):
        with self.assertRaises(ValueError):
            Settings(environment="production", _env_file=None)
        options = dict(environment="production", debug=False, internal_shared_secret="a"*40,
                       minio_access_key="trpg-user", minio_secret_key="b"*24, _env_file=None)
        with self.assertRaises(ValueError):
            Settings(**options)
        self.assertFalse(Settings(enabled=False, **options).enabled)


class MemoryEndpointTests(unittest.TestCase):
    def test_summary_requires_internal_authentication(self):
        with TestClient(create_app()) as client:
            response = client.post("/api/v1/ai/memory/summary", json={"messages": [{"role": "assistant", "content": "事实"}]})
        self.assertEqual(response.status_code, 401)

    def test_summary_candidate_does_not_write_runtime(self):
        from app.config import settings
        with patch("app.routers.memory.summarize", AsyncMock(return_value="事实"*100)) as generator:
            with TestClient(create_app()) as client:
                response = client.post("/api/v1/ai/memory/summary", headers={"X-Internal-Secret":settings.internal_shared_secret},
                    json={"previous_summary":"旧摘要", "messages":[{"role":"assistant","content":"新事实"}]})
            self.assertEqual(response.status_code, 200)
            generator.assert_awaited_once()

    def test_readiness_failure_is_distinct_from_liveness(self):
        with patch("app.main.readiness", AsyncMock(return_value={"status":"not_ready", "capabilities":{"ai":"missing_key"}})):
            with TestClient(create_app()) as client:
                self.assertEqual(client.get("/health").status_code, 200)
                self.assertEqual(client.get("/ready").status_code, 503)
