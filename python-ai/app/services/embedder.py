"""文本向量化 — 调用 Embedding 模型 + Milvus 存储"""

from app.services.chunker import Chunk


def create_collection() -> None:
    """创建 Milvus Collection（不存在时）"""
    # TODO: Phase 1 M1.3 实现
    raise NotImplementedError("embedder.create_collection not implemented")


def embed_and_store(chunks: list[Chunk]) -> int:
    """
    批量向量化并存入 Milvus。

    Args:
        chunks: 文本片段列表

    Returns:
        成功写入的向量数量
    """
    # TODO: Phase 1 M1.3 实现
    raise NotImplementedError("embedder.embed_and_store not implemented")
