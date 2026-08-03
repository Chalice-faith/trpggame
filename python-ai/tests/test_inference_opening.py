from __future__ import annotations

import unittest

from fastapi.testclient import TestClient

from app.main import create_app
from app.routers.inference import (
    OPENING_ACTION_PROMPT,
    OPENING_RETRIEVAL_QUERY,
    OpeningNarrativeError,
    OpeningNarrativeService,
    StartGameRequest,
    get_opening_narrative_service,
)
from app.services.context_builder import AssembledContext
from app.services.retriever import RetrievalError


class RecordingOpeningService:
    def __init__(
        self,
        narrative: str = "你站在古宅门前。",
        error: Exception | None = None,
    ) -> None:
        self.narrative = narrative
        self.error = error
        self.requests: list[StartGameRequest] = []

    async def generate(self, request: StartGameRequest) -> str:
        self.requests.append(request)
        if self.error is not None:
            raise self.error
        return self.narrative


class OpeningNarrativeServiceTests(unittest.IsolatedAsyncioTestCase):
    async def test_orchestrates_retrieval_context_and_generation(self):
        calls: dict[str, object] = {}

        async def retriever(query: str, script_id: int) -> list[str]:
            calls["retrieval"] = (query, script_id)
            return ["古宅入口被暴雨笼罩。"]

        def context_builder(action: str, **context) -> AssembledContext:
            calls["context"] = (action, context)
            return AssembledContext(
                system_prompt="system context",
                user_prompt="opening prompt",
                recent_history=(),
            )

        async def generator(prompt: str, system_prompt: str) -> str:
            calls["generation"] = (prompt, system_prompt)
            return "  你在暴雨中抵达古宅。  "

        service = OpeningNarrativeService(
            retriever=retriever,
            context_builder=context_builder,
            generator=generator,
        )
        request = StartGameRequest(
            room_id=7,
            script_id=11,
            character_id=13,
            user_id=17,
        )

        result = await service.generate(request)

        self.assertEqual(result, "你在暴雨中抵达古宅。")
        self.assertEqual(calls["retrieval"], (OPENING_RETRIEVAL_QUERY, 11))
        action, context = calls["context"]
        self.assertEqual(action, OPENING_ACTION_PROMPT)
        self.assertEqual(context["rag_chunks"], ["古宅入口被暴雨笼罩。"])
        self.assertEqual(context["character_profile"], {"character_id": 13})
        self.assertEqual(
            context["player_state"],
            {"room_id": 7, "user_id": 17},
        )
        self.assertEqual(
            calls["generation"],
            ("opening prompt", "system context"),
        )

    async def test_rejects_empty_retrieval_and_empty_narrative(self):
        async def empty_retriever(query: str, script_id: int) -> list[str]:
            return []

        request = StartGameRequest(
            room_id=1,
            script_id=2,
            character_id=3,
            user_id=4,
        )
        with self.assertRaisesRegex(OpeningNarrativeError, "no retrievable"):
            await OpeningNarrativeService(retriever=empty_retriever).generate(request)

        async def retriever(query: str, script_id: int) -> list[str]:
            return ["开场"]

        async def empty_generator(prompt: str, system_prompt: str) -> str:
            return "  "

        with self.assertRaisesRegex(OpeningNarrativeError, "empty narrative"):
            await OpeningNarrativeService(
                retriever=retriever,
                generator=empty_generator,
            ).generate(request)

    async def test_wraps_retrieval_failure(self):
        async def fail(query: str, script_id: int) -> list[str]:
            raise RetrievalError("Milvus unavailable")

        request = StartGameRequest(
            room_id=1,
            script_id=2,
            character_id=3,
            user_id=4,
        )
        with self.assertRaisesRegex(OpeningNarrativeError, "retrieval failed") as raised:
            await OpeningNarrativeService(retriever=fail).generate(request)
        self.assertIsInstance(raised.exception.__cause__, RetrievalError)


class OpeningInferenceEndpointTests(unittest.TestCase):
    def test_endpoint_returns_generated_narrative(self):
        service = RecordingOpeningService()
        app = create_app()
        app.dependency_overrides[get_opening_narrative_service] = lambda: service

        with TestClient(app) as client:
            response = client.post(
                "/api/v1/ai/inference/start",
                json=self._request_body(),
                headers=self._headers(),
            )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json(), {"narrative": "你站在古宅门前。"})
        self.assertEqual(service.requests[0].script_id, 2)

    def test_endpoint_requires_internal_secret(self):
        app = create_app()
        with TestClient(app) as client:
            response = client.post(
                "/api/v1/ai/inference/start",
                json=self._request_body(),
            )

        self.assertEqual(response.status_code, 401)

    def test_endpoint_rejects_non_positive_ids(self):
        body = self._request_body()
        body["room_id"] = 0
        app = create_app()
        with TestClient(app) as client:
            response = client.post(
                "/api/v1/ai/inference/start",
                json=body,
                headers=self._headers(),
            )

        self.assertEqual(response.status_code, 422)

    def test_endpoint_maps_service_failure_to_503(self):
        service = RecordingOpeningService(error=OpeningNarrativeError("failed"))
        app = create_app()
        app.dependency_overrides[get_opening_narrative_service] = lambda: service

        with TestClient(app) as client:
            response = client.post(
                "/api/v1/ai/inference/start",
                json=self._request_body(),
                headers=self._headers(),
            )

        self.assertEqual(response.status_code, 503)
        self.assertEqual(
            response.json()["detail"],
            "opening narrative generation unavailable",
        )

    @staticmethod
    def _request_body() -> dict[str, int]:
        return {
            "room_id": 1,
            "script_id": 2,
            "character_id": 3,
            "user_id": 4,
        }

    @staticmethod
    def _headers() -> dict[str, str]:
        return {"X-Internal-Secret": "dev-internal-secret-change-in-production"}


if __name__ == "__main__":
    unittest.main()
