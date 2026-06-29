"""文本分片 — 按章节标题分片（500-2000 字符，100 字符重叠）"""

from dataclasses import dataclass


@dataclass
class Chunk:
    content: str
    index: int
    script_id: int
    metadata: dict


def chunk_text(text: str, script_id: int) -> list[Chunk]:
    """
    将清洗后的文本按章节标题分割为重叠片段。

    Args:
        text: 清洗后的文本
        script_id: 剧本 ID

    Returns:
        排序后的 Chunk 列表
    """
    # TODO: Phase 1 M1.3 实现
    raise NotImplementedError("chunker.chunk_text not implemented")
