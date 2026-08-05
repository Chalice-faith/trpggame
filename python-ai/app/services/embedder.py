"""使用 BGE 生成文本向量，并将剧本片段幂等写入 Milvus。"""

from __future__ import annotations

import math
from collections.abc import Sequence
from threading import Lock
from typing import Any, Protocol

from app.config import settings
from app.services.chunker import Chunk

EMBEDDING_DIMENSION = 1_024
CONTENT_MAX_LENGTH = 65_535
CHAPTER_TITLE_MAX_LENGTH = 1_024


class EmbeddingError(RuntimeError):
    """文本向量生成或校验失败。"""


class VectorStoreError(RuntimeError):
    """Milvus Collection 初始化或写入失败。"""


class Encoder(Protocol):
    def encode(
        self,
        sentences: list[str],
        *,
        batch_size: int,
        normalize_embeddings: bool,
        convert_to_numpy: bool,
        show_progress_bar: bool,
    ) -> Any: ...


class VectorStore(Protocol):
    def ensure_collection(self, dimension: int) -> None: ...

    def delete_script(self, script_id: int) -> None: ...

    def insert(self, records: list[dict[str, object]]) -> None: ...


_encoder: Encoder | None = None
_encoder_lock = Lock()


def create_collection(*, store: VectorStore | None = None) -> None:
    """创建并加载剧本向量 Collection；已存在时校验向量维度。"""

    target_store = store or _MilvusStore()
    try:
        target_store.ensure_collection(settings.embedding_dimension)
    except VectorStoreError:
        raise
    except Exception as exc:
        raise VectorStoreError("failed to initialize Milvus collection") from exc


def delete_script_chunks(
    script_id: int,
    *,
    store: VectorStore | None = None,
) -> None:
    """删除指定剧本的全部旧向量，供重试和删除剧本流程复用。"""

    if script_id <= 0:
        raise ValueError("script_id must be positive")

    target_store = store or _MilvusStore()
    create_collection(store=target_store)
    try:
        target_store.delete_script(script_id)
    except VectorStoreError:
        raise
    except Exception as exc:
        raise VectorStoreError(
            f"failed to delete vectors for script {script_id}"
        ) from exc


def embed_and_store(
    chunks: list[Chunk],
    *,
    encoder: Encoder | None = None,
    store: VectorStore | None = None,
    batch_size: int | None = None,
) -> int:
    """批量向量化并写入 Milvus，返回成功写入的片段数量。

    所有向量生成并通过维度校验后，才会删除涉及剧本的旧数据。这样模型
    加载或编码失败时，不会破坏已经可用的索引。
    """

    if not chunks:
        return 0

    effective_batch_size = batch_size or settings.embedding_batch_size
    if effective_batch_size <= 0:
        raise ValueError("batch_size must be positive")

    _validate_chunks(chunks)
    target_encoder = encoder or _get_encoder()
    target_store = store or _MilvusStore()
    records = _build_records(
        chunks,
        target_encoder,
        batch_size=effective_batch_size,
        dimension=settings.embedding_dimension,
    )

    create_collection(store=target_store)
    script_ids = sorted({chunk.script_id for chunk in chunks})
    try:
        for script_id in script_ids:
            target_store.delete_script(script_id)
        for offset in range(0, len(records), effective_batch_size):
            target_store.insert(records[offset : offset + effective_batch_size])
    except VectorStoreError:
        raise
    except Exception as exc:
        raise VectorStoreError("failed to replace script vectors in Milvus") from exc

    return len(records)


def _get_encoder() -> Encoder:
    global _encoder

    if _encoder is None:
        with _encoder_lock:
            if _encoder is None:
                try:
                    from sentence_transformers import SentenceTransformer

                    _encoder = SentenceTransformer(settings.embedding_model)
                except Exception as exc:
                    raise EmbeddingError(
                        f"failed to load embedding model {settings.embedding_model!r}"
                    ) from exc
    return _encoder


def _validate_chunks(chunks: Sequence[Chunk]) -> None:
    seen_indexes: set[tuple[int, int]] = set()
    for chunk in chunks:
        if chunk.script_id <= 0:
            raise ValueError("chunk script_id must be positive")
        if chunk.index < 0:
            raise ValueError("chunk index must be non-negative")
        if not chunk.content.strip():
            raise ValueError("chunk content must not be blank")

        identity = (chunk.script_id, chunk.index)
        if identity in seen_indexes:
            raise ValueError(
                f"duplicate chunk index {chunk.index} for script {chunk.script_id}"
            )
        seen_indexes.add(identity)


def _build_records(
    chunks: list[Chunk],
    encoder: Encoder,
    *,
    batch_size: int,
    dimension: int,
) -> list[dict[str, object]]:
    try:
        encoded = encoder.encode(
            [chunk.content for chunk in chunks],
            batch_size=batch_size,
            normalize_embeddings=True,
            convert_to_numpy=True,
            show_progress_bar=False,
        )
    except Exception as exc:
        raise EmbeddingError("failed to generate chunk embeddings") from exc

    vectors = list(encoded)
    if len(vectors) != len(chunks):
        raise EmbeddingError(
            f"embedding count mismatch: expected {len(chunks)}, got {len(vectors)}"
        )

    records: list[dict[str, object]] = []
    for chunk, raw_vector in zip(chunks, vectors, strict=True):
        vector = _validate_vector(raw_vector, dimension)
        metadata = chunk.metadata
        chapter_title = str(metadata.get("chapter_title", ""))
        if len(chapter_title) > CHAPTER_TITLE_MAX_LENGTH:
            raise ValueError("chapter_title exceeds Milvus VARCHAR limit")
        if len(chunk.content) > CONTENT_MAX_LENGTH:
            raise ValueError("chunk content exceeds Milvus VARCHAR limit")

        records.append(
            {
                "script_id": chunk.script_id,
                "chunk_index": chunk.index,
                "chapter_index": _required_int(metadata, "chapter_index"),
                "chapter_title": chapter_title,
                "page_start": _required_int(metadata, "page_start"),
                "page_end": _required_int(metadata, "page_end"),
                "content": chunk.content,
                "embedding": vector,
            }
        )
    return records


def _validate_vector(raw_vector: Any, dimension: int) -> list[float]:
    try:
        vector = [float(value) for value in raw_vector]
    except (TypeError, ValueError) as exc:
        raise EmbeddingError("embedding contains a non-numeric value") from exc

    if len(vector) != dimension:
        raise EmbeddingError(
            f"embedding dimension mismatch: expected {dimension}, got {len(vector)}"
        )
    if not all(math.isfinite(value) for value in vector):
        raise EmbeddingError("embedding contains NaN or infinity")
    return vector


def _required_int(metadata: dict[str, object], key: str) -> int:
    if key not in metadata:
        raise ValueError(f"chunk metadata is missing {key!r}")
    value = metadata[key]
    if isinstance(value, bool):
        raise ValueError(f"chunk metadata {key!r} must be an integer")
    try:
        return int(value)
    except (TypeError, ValueError) as exc:
        raise ValueError(f"chunk metadata {key!r} must be an integer") from exc


class _MilvusStore:
    """基于 pymilvus ORM 的轻量适配层，避免模块导入时连接外部服务。"""

    def __init__(self) -> None:
        self._collection: Any | None = None

    def ensure_collection(self, dimension: int) -> None:
        try:
            from pymilvus import (
                Collection,
                CollectionSchema,
                DataType,
                FieldSchema,
                connections,
                utility,
            )

            connections.connect(
                alias="default",
                host=settings.milvus_host,
                port=str(settings.milvus_port),
            )
            name = settings.milvus_collection_name
            if utility.has_collection(name):
                collection = Collection(name)
                embedding_field = next(
                    (
                        field
                        for field in collection.schema.fields
                        if field.name == "embedding"
                    ),
                    None,
                )
                actual_dimension = (
                    int(embedding_field.params.get("dim", 0))
                    if embedding_field is not None
                    else 0
                )
                if actual_dimension != dimension:
                    raise VectorStoreError(
                        "existing Milvus collection has embedding dimension "
                        f"{actual_dimension}, expected {dimension}"
                    )
            else:
                fields = [
                    FieldSchema(
                        name="id",
                        dtype=DataType.INT64,
                        is_primary=True,
                        auto_id=True,
                    ),
                    FieldSchema(name="script_id", dtype=DataType.INT64),
                    FieldSchema(name="chunk_index", dtype=DataType.INT64),
                    FieldSchema(name="chapter_index", dtype=DataType.INT64),
                    FieldSchema(
                        name="chapter_title",
                        dtype=DataType.VARCHAR,
                        max_length=CHAPTER_TITLE_MAX_LENGTH,
                    ),
                    FieldSchema(name="page_start", dtype=DataType.INT64),
                    FieldSchema(name="page_end", dtype=DataType.INT64),
                    FieldSchema(
                        name="content",
                        dtype=DataType.VARCHAR,
                        max_length=CONTENT_MAX_LENGTH,
                    ),
                    FieldSchema(
                        name="embedding",
                        dtype=DataType.FLOAT_VECTOR,
                        dim=dimension,
                    ),
                ]
                collection = Collection(
                    name=name,
                    schema=CollectionSchema(
                        fields=fields,
                        description="TRPG script chunks",
                        enable_dynamic_field=False,
                    ),
                )
                collection.create_index(
                    field_name="embedding",
                    index_params={
                        "index_type": "HNSW",
                        "metric_type": "COSINE",
                        "params": {"M": 16, "efConstruction": 200},
                    },
                )

            collection.load()
            self._collection = collection
        except VectorStoreError:
            raise
        except Exception as exc:
            raise VectorStoreError("failed to initialize Milvus collection") from exc

    def delete_script(self, script_id: int) -> None:
        collection = self._require_collection()
        try:
            collection.delete(expr=f"script_id == {script_id}")
            collection.flush()
        except Exception as exc:
            raise VectorStoreError(
                f"failed to delete vectors for script {script_id}"
            ) from exc

    def insert(self, records: list[dict[str, object]]) -> None:
        collection = self._require_collection()
        try:
            collection.insert(records)
            collection.flush()
        except Exception as exc:
            raise VectorStoreError("failed to insert vectors into Milvus") from exc

    def _require_collection(self) -> Any:
        if self._collection is None:
            raise VectorStoreError("Milvus collection has not been initialized")
        return self._collection
