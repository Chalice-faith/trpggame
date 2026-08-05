"""剧本 RAG 检索：Milvus 候选召回与 MMR 重排序。"""

from __future__ import annotations

import asyncio
import math
from collections.abc import Sequence
from dataclasses import dataclass
from typing import Any, Protocol

from app.config import settings
from app.services.embedder import (
    EmbeddingError,
    Encoder,
    _get_encoder,
    _validate_vector,
)


class RetrievalError(RuntimeError):
    """向量生成、Milvus 查询或候选数据异常。"""


@dataclass(frozen=True, slots=True)
class SearchCandidate:
    """Milvus 返回的候选片段。"""

    content: str
    embedding: list[float]
    score: float


class SearchStore(Protocol):
    def search(
        self,
        query_vector: list[float],
        *,
        script_id: int,
        limit: int,
    ) -> list[SearchCandidate]: ...


async def retrieve(
    query: str,
    script_id: int,
    top_k: int = 20,
    mmr_top_n: int = 5,
    *,
    mmr_lambda: float = 0.7,
    encoder: Encoder | None = None,
    store: SearchStore | None = None,
) -> list[str]:
    """召回与查询最相关的片段，并通过 MMR 降低结果同质性。"""

    _validate_options(
        query=query,
        script_id=script_id,
        top_k=top_k,
        mmr_top_n=mmr_top_n,
        mmr_lambda=mmr_lambda,
    )
    return await asyncio.to_thread(
        _retrieve_sync,
        query,
        script_id,
        top_k,
        mmr_top_n,
        mmr_lambda,
        encoder,
        store,
    )


def _retrieve_sync(
    query: str,
    script_id: int,
    top_k: int,
    mmr_top_n: int,
    mmr_lambda: float,
    encoder: Encoder | None,
    store: SearchStore | None,
) -> list[str]:
    try:
        query_vector = _encode_query(query, encoder or _get_encoder())
        candidates = (store or _MilvusSearchStore()).search(
            query_vector,
            script_id=script_id,
            limit=top_k,
        )
        selected = _maximal_marginal_relevance(
            query_vector,
            candidates,
            limit=mmr_top_n,
            lambda_mult=mmr_lambda,
        )
    except RetrievalError:
        raise
    except EmbeddingError as exc:
        raise RetrievalError("failed to generate retrieval query embedding") from exc
    except Exception as exc:
        raise RetrievalError(
            f"failed to retrieve chunks for script {script_id}"
        ) from exc
    return [candidate.content for candidate in selected]


def _encode_query(query: str, encoder: Encoder) -> list[float]:
    try:
        encoded = encoder.encode(
            [query],
            batch_size=1,
            normalize_embeddings=True,
            convert_to_numpy=True,
            show_progress_bar=False,
        )
        vectors = list(encoded)
    except Exception as exc:
        raise RetrievalError("failed to generate retrieval query embedding") from exc
    if len(vectors) != 1:
        raise RetrievalError(
            f"query embedding count mismatch: expected 1, got {len(vectors)}"
        )
    try:
        return _validate_vector(vectors[0], settings.embedding_dimension)
    except EmbeddingError as exc:
        raise RetrievalError("invalid retrieval query embedding") from exc


def _maximal_marginal_relevance(
    query_vector: Sequence[float],
    candidates: Sequence[SearchCandidate],
    *,
    limit: int,
    lambda_mult: float,
) -> list[SearchCandidate]:
    if not candidates or limit <= 0:
        return []

    validated: list[SearchCandidate] = []
    for candidate in candidates:
        if not candidate.content.strip():
            raise RetrievalError("Milvus candidate content must not be blank")
        try:
            vector = _validate_vector(
                candidate.embedding,
                settings.embedding_dimension,
            )
        except EmbeddingError as exc:
            raise RetrievalError("Milvus candidate has invalid embedding") from exc
        validated.append(
            SearchCandidate(
                content=candidate.content,
                embedding=vector,
                score=candidate.score,
            )
        )

    relevance = [
        _cosine_similarity(query_vector, candidate.embedding)
        for candidate in validated
    ]
    selected_indexes = [max(range(len(validated)), key=relevance.__getitem__)]
    remaining = set(range(len(validated))) - set(selected_indexes)

    while remaining and len(selected_indexes) < min(limit, len(validated)):
        best_index = max(
            remaining,
            key=lambda index: (
                lambda_mult * relevance[index]
                - (1 - lambda_mult)
                * max(
                    _cosine_similarity(
                        validated[index].embedding,
                        validated[selected].embedding,
                    )
                    for selected in selected_indexes
                ),
                -index,
            ),
        )
        selected_indexes.append(best_index)
        remaining.remove(best_index)

    return [validated[index] for index in selected_indexes]


def _cosine_similarity(left: Sequence[float], right: Sequence[float]) -> float:
    if len(left) != len(right):
        raise RetrievalError("embedding dimensions do not match")
    dot_product = sum(a * b for a, b in zip(left, right, strict=True))
    left_norm = math.sqrt(sum(value * value for value in left))
    right_norm = math.sqrt(sum(value * value for value in right))
    if left_norm == 0 or right_norm == 0:
        return 0.0
    return dot_product / (left_norm * right_norm)


def _validate_options(
    *,
    query: str,
    script_id: int,
    top_k: int,
    mmr_top_n: int,
    mmr_lambda: float,
) -> None:
    if not query.strip():
        raise ValueError("query must not be empty")
    if script_id <= 0:
        raise ValueError("script_id must be positive")
    if top_k <= 0:
        raise ValueError("top_k must be positive")
    if mmr_top_n <= 0:
        raise ValueError("mmr_top_n must be positive")
    if not 0 <= mmr_lambda <= 1:
        raise ValueError("mmr_lambda must be between 0 and 1")


class _MilvusSearchStore:
    """基于 pymilvus ORM 查询共享的剧本片段 Collection。"""

    def search(
        self,
        query_vector: list[float],
        *,
        script_id: int,
        limit: int,
    ) -> list[SearchCandidate]:
        try:
            from pymilvus import Collection, connections, utility

            connections.connect(
                alias="default",
                host=settings.milvus_host,
                port=str(settings.milvus_port),
            )
            name = settings.milvus_collection_name
            if not utility.has_collection(name):
                return []

            collection = Collection(name)
            collection.load()
            result = collection.search(
                data=[query_vector],
                anns_field="embedding",
                param={
                    "metric_type": "COSINE",
                    "params": {"ef": max(64, limit)},
                },
                limit=limit,
                expr=f"script_id == {script_id}",
                output_fields=["content", "embedding"],
            )
            hits = result[0] if result else []
            return [self._to_candidate(hit) for hit in hits]
        except RetrievalError:
            raise
        except Exception as exc:
            raise RetrievalError("Milvus similarity search failed") from exc

    @staticmethod
    def _to_candidate(hit: Any) -> SearchCandidate:
        entity = getattr(hit, "entity", None)
        if entity is None:
            raise RetrievalError("Milvus search hit is missing entity data")
        content = entity.get("content")
        embedding = entity.get("embedding")
        if not isinstance(content, str):
            raise RetrievalError("Milvus search hit is missing content")
        if embedding is None:
            raise RetrievalError("Milvus search hit is missing embedding")
        try:
            vector = [float(value) for value in embedding]
            score = float(getattr(hit, "score"))
        except (TypeError, ValueError) as exc:
            raise RetrievalError("Milvus search hit contains invalid data") from exc
        return SearchCandidate(content=content, embedding=vector, score=score)
