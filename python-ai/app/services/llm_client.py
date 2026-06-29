"""LLM 调用封装 — GLM-4-Long（同步 + 流式）"""

from collections.abc import AsyncGenerator


async def chat(prompt: str, system_prompt: str = "") -> str:
    """
    同步调用 LLM，返回完整响应。

    Args:
        prompt: 用户提示词
        system_prompt: 系统提示词

    Returns:
        LLM 生成的完整文本
    """
    # TODO: Phase 1 M1.4 实现
    raise NotImplementedError("llm_client.chat not implemented")


async def chat_stream(prompt: str, system_prompt: str = "") -> AsyncGenerator[str, None]:
    """
    流式调用 LLM，逐 token 产出。

    Args:
        prompt: 用户提示词
        system_prompt: 系统提示词

    Yields:
        每次返回一个文本片段（token/短语级）
    """
    # TODO: Phase 1 M1.4 实现
    raise NotImplementedError("llm_client.chat_stream not implemented")
    yield  # make it an async generator
