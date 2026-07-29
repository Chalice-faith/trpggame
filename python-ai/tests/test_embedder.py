from __future__ import annotations

import math
import unittest

from app.services.chunker import Chunk
from app.services.embedder import (
    EMBEDDING_DIMENSION,
    EmbeddingError,
    create_collection,
    delete_script_chunks,
    embed_and_store,
)


class FakeEncoder:
    def __init__(self, vectors: list[list[float]] | None = None) -> None:
        self.vectors = vectors
        self.calls: list[dict[str, object]] = []

    def encode(self, sentences: list[str], **options: object) -> list[list[float]]:
        self.calls.append({"sentences": sentences, **options})
        if self.vectors is not None:
            return self.vectors
        return [
            [float(index % 7) for index in range(EMBEDDING_DIMENSION)]
            for _ in sentences
        ]


class FakeStore:
    def __init__(self) -> None:
        self.dimensions: list[int] = []
        self.deleted_script_ids: list[int] = []
        self.insert_batches: list[list[dict[str, object]]] = []

    def ensure_collection(self, dimension: int) -> None:
        self.dimensions.append(dimension)

    def delete_script(self, script_id: int) -> None:
        self.deleted_script_ids.append(script_id)

    def insert(self, records: list[dict[str, object]]) -> None:
        self.insert_batches.append(records)


def make_chunk(script_id: int, index: int, content: str = "片段内容") -> Chunk:
    return Chunk(
        content=content,
        index=index,
        script_id=script_id,
        metadata={
            "chapter_index": 2,
            "chapter_title": "第二章 测试",
            "page_start": 3,
            "page_end": 4,
        },
    )


class EmbedderTests(unittest.TestCase):
    def test_creates_collection_with_configured_dimension(self):
        store = FakeStore()

        create_collection(store=store)

        self.assertEqual(store.dimensions, [EMBEDDING_DIMENSION])

    def test_embeds_replaces_and_inserts_complete_metadata(self):
        encoder = FakeEncoder()
        store = FakeStore()
        chunks = [make_chunk(8, 0, "第一段"), make_chunk(8, 1, "第二段")]

        count = embed_and_store(chunks, encoder=encoder, store=store)

        self.assertEqual(count, 2)
        self.assertEqual(store.dimensions, [EMBEDDING_DIMENSION])
        self.assertEqual(store.deleted_script_ids, [8])
        self.assertEqual(len(store.insert_batches), 1)
        first = store.insert_batches[0][0]
        self.assertEqual(
            set(first),
            {
                "script_id",
                "chunk_index",
                "chapter_index",
                "chapter_title",
                "page_start",
                "page_end",
                "content",
                "embedding",
            },
        )
        self.assertEqual(first["chapter_title"], "第二章 测试")
        self.assertEqual(len(first["embedding"]), EMBEDDING_DIMENSION)
        self.assertTrue(encoder.calls[0]["normalize_embeddings"])

    def test_batches_inserts_and_deletes_each_script_once(self):
        encoder = FakeEncoder()
        store = FakeStore()
        chunks = [
            make_chunk(2, 0),
            make_chunk(1, 0),
            make_chunk(2, 1),
        ]

        count = embed_and_store(
            chunks,
            encoder=encoder,
            store=store,
            batch_size=2,
        )

        self.assertEqual(count, 3)
        self.assertEqual(store.deleted_script_ids, [1, 2])
        self.assertEqual([len(batch) for batch in store.insert_batches], [2, 1])

    def test_invalid_vector_does_not_delete_existing_data(self):
        encoder = FakeEncoder(vectors=[[0.0] * 12])
        store = FakeStore()

        with self.assertRaisesRegex(EmbeddingError, "dimension mismatch"):
            embed_and_store([make_chunk(6, 0)], encoder=encoder, store=store)

        self.assertEqual(store.deleted_script_ids, [])
        self.assertEqual(store.insert_batches, [])

    def test_rejects_non_finite_vector(self):
        vector = [0.0] * EMBEDDING_DIMENSION
        vector[-1] = math.nan

        with self.assertRaisesRegex(EmbeddingError, "NaN or infinity"):
            embed_and_store(
                [make_chunk(6, 0)],
                encoder=FakeEncoder(vectors=[vector]),
                store=FakeStore(),
            )

    def test_rejects_duplicate_chunk_index(self):
        chunks = [make_chunk(3, 0), make_chunk(3, 0)]

        with self.assertRaisesRegex(ValueError, "duplicate chunk index"):
            embed_and_store(chunks, encoder=FakeEncoder(), store=FakeStore())

    def test_empty_input_does_not_initialize_dependencies(self):
        encoder = FakeEncoder()
        store = FakeStore()

        self.assertEqual(embed_and_store([], encoder=encoder, store=store), 0)
        self.assertEqual(encoder.calls, [])
        self.assertEqual(store.dimensions, [])

    def test_deletes_script_chunks_through_shared_operation(self):
        store = FakeStore()

        delete_script_chunks(15, store=store)

        self.assertEqual(store.dimensions, [EMBEDDING_DIMENSION])
        self.assertEqual(store.deleted_script_ids, [15])


if __name__ == "__main__":
    unittest.main()
