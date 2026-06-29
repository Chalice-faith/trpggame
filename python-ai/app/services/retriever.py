"""RAG 检索 — Milvus 向量检索 + MMR 重排序"""


async def retrieve(query: str, script_id: int, top_k: int = 20, mmr_top_n: int = 5) -> list[str]:
    """
    检索与 query 最相关的剧本片段：
    1. 向量相似度检索 → Top-K
    2. MMR 重排序 → Top-N

    Args:
        query: 查询文本
        script_id: 剧本 ID
        top_k: 向量检索返回数
        mmr_top_n: MMR 重排序后保留数

    Returns:
        排序后的文本片段列表
    """
    # TODO: Phase 1 M1.4 实现
    raise NotImplementedError("retriever.retrieve not implemented")
