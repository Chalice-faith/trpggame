"""摘要记忆 — 每 N 轮触发，生成 200-500 字叙事摘要"""


async def summarize(history: list[dict]) -> str:
    """
    对最近 N 轮对话生成叙事摘要。

    Args:
        history: 对话历史列表，每项含 role/content

    Returns:
        200-500 字的叙事摘要
    """
    # TODO: Phase 1 M1.4 实现
    raise NotImplementedError("summarizer.summarize not implemented")
