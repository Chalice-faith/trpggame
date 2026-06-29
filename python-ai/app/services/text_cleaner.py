"""文本清洗 — 去页眉页脚/页码/空行压缩/特殊字符过滤"""


def clean(text: str) -> str:
    """
    清洗提取的 PDF 文本：
    - 去除页眉页脚
    - 去除页码
    - 压缩连续空行
    - 过滤特殊字符/乱码

    Args:
        text: 原始提取文本

    Returns:
        清洗后的文本
    """
    # TODO: Phase 1 M1.3 实现
    raise NotImplementedError("text_cleaner.clean not implemented")
