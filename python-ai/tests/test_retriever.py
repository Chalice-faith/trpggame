from __future__ import annotations

import unittest

from app.config import settings
from app.services.retriever import RetrievalError, SearchCandidate, retrieve


def vector(*values: float) -> list[float]:
    result = [0.0] * settings.embedding_dimension
    result[: len(values)] = values
    return result


class FakeEncoder:
    def __init__(self, query_vector: list[float] | None = None) -> None:
        self.query_vector = query_vector or vector(1.0)
        self.calls: list[dict[str, object]] = []

    def encode(self, sentences: list[str], **options: object):
        self.calls.append({"sentences": sentences, **options})
        return [self.query_vector]


class FakeStore:
    def __init__(
        self,
        candidates: list[SearchCandidate] | None = None,
        error: Exception | None = None,
    ) -> None:
        self.candidates = candidates or []
        self.error = error
        self.calls: list[dict[str, object]] = []

    def search(
        self,
        query_vector: list[float],
        *,
        script_id: int,
        limit: int,
    ) -> list[SearchCandidate]:
        self.calls.append(
            {
                "query_vector": query_vector,
                "script_id": script_id,
                "limit": limit,
            }
        )
        if self.error is not None:
            raise self.error
        return self.candidates


class RetrieverTests(unittest.IsolatedAsyncioTestCase):
    async def test_retrieves_candidates_and_applies_mmr_diversity(self):
        encoder = FakeEncoder(vector(1.0, 0.0, 0.0))
        store = FakeStore(
            [
                SearchCandidate("最相关", vector(1.0, 0.0, 0.0), 1.0),
                SearchCandidate("相似片段", vector(0.95, 0.312, 0.0), 0.95),
                SearchCandidate("多样片段", vector(0.7, 0.0, 0.714), 0.7),
            ]
        )

        result = await retrieve(
            "调查古宅",
            42,
            top_k=20,
            mmr_top_n=2,
            mmr_lambda=0.3,
            encoder=encoder,
            store=store,
        )

        self.assertEqual(result, ["最相关", "多样片段"])
        self.assertEqual(store.calls[0]["script_id"], 42)
        self.assertEqual(store.calls[0]["limit"], 20)
        self.assertEqual(encoder.calls[0]["sentences"], ["调查古宅"])
        self.assertTrue(encoder.calls[0]["normalize_embeddings"])

    async def test_default_mmr_weight_prefers_relevance(self):
        store = FakeStore(
            [
                SearchCandidate("最相关", vector(1.0, 0.0, 0.0), 1.0),
                SearchCandidate("次相关", vector(0.95, 0.312, 0.0), 0.95),
                SearchCandidate("更多样", vector(0.7, 0.0, 0.714), 0.7),
            ]
        )

        result = await retrieve(
            "调查古宅",
            42,
            mmr_top_n=2,
            encoder=FakeEncoder(vector(1.0, 0.0, 0.0)),
            store=store,
        )

        self.assertEqual(result, ["最相关", "次相关"])

    async def test_returns_all_available_candidates_when_limit_is_larger(self):
        store = FakeStore(
            [
                SearchCandidate("第一段", vector(1.0), 1.0),
                SearchCandidate("第二段", vector(0.0, 1.0), 0.5),
            ]
        )

        result = await retrieve(
            "查询",
            3,
            mmr_top_n=5,
            encoder=FakeEncoder(),
            store=store,
        )

        self.assertEqual(result, ["第一段", "第二段"])

    async def test_returns_empty_list_when_store_has_no_candidates(self):
        result = await retrieve(
            "查询",
            3,
            encoder=FakeEncoder(),
            store=FakeStore(),
        )

        self.assertEqual(result, [])

    async def test_wraps_store_failure(self):
        with self.assertRaisesRegex(RetrievalError, "script 7") as raised:
            await retrieve(
                "查询",
                7,
                encoder=FakeEncoder(),
                store=FakeStore(error=OSError("Milvus unavailable")),
            )

        self.assertIsInstance(raised.exception.__cause__, OSError)

    async def test_rejects_invalid_options_before_dependencies_are_called(self):
        cases = [
            {"query": "", "script_id": 1},
            {"query": "查询", "script_id": 0},
            {"query": "查询", "script_id": 1, "top_k": 0},
            {"query": "查询", "script_id": 1, "mmr_top_n": 0},
            {"query": "查询", "script_id": 1, "mmr_lambda": 1.1},
        ]

        for options in cases:
            with self.subTest(options=options):
                encoder = FakeEncoder()
                store = FakeStore()
                with self.assertRaises(ValueError):
                    await retrieve(encoder=encoder, store=store, **options)
                self.assertEqual(encoder.calls, [])
                self.assertEqual(store.calls, [])

    async def test_rejects_invalid_candidate_embedding(self):
        store = FakeStore([SearchCandidate("片段", [1.0, 0.0], 1.0)])

        with self.assertRaisesRegex(RetrievalError, "invalid embedding"):
            await retrieve(
                "查询",
                3,
                encoder=FakeEncoder(),
                store=store,
            )


if __name__ == "__main__":
    unittest.main()
