"""TRPG 摘要记忆生成与周期触发策略。"""

from __future__ import annotations

import asyncio
import json
import logging
from collections.abc import Awaitable, Callable, Mapping, Sequence
from typing import Any

from app.config import settings
from app.services.llm_client import DeepSeekClient


MIN_SUMMARY_LENGTH = 200
MAX_SUMMARY_LENGTH = 500

SUMMARY_SYSTEM_PROMPT = """你是 TRPG 游戏记忆整理器。
只根据提供的旧摘要和游戏日志重写叙事摘要，不得补充未发生的事实。
必须保留剧情进展、角色状态变化、玩家关键决策和 NPC 关系变化。
合并重复观察和静态环境描写，优先保留不可替代的具体事实。
输出一段连续中文叙事，不要标题、列表、Markdown 或解释。
只写四句话，每句话约 50-70 字，合计约 240 字；长度必须在 200-500 字之间。
合并重复的观察和环境描写，保留具体暗号、名称及关键事实。"""


class SummarizationError(RuntimeError):
    """摘要生成调用或结果异常。"""


class SummaryLengthError(SummarizationError):
    """LLM 返回的摘要长度不满足上下文契约。"""


SummaryGenerator = Callable[[str, str], Awaitable[str]]


def should_summarize(
    round_number: int,
    *,
    trigger_rounds: int | None = None,
) -> bool:
    """判断当前回合是否应触发摘要更新。"""

    interval = settings.summary_trigger_rounds if trigger_rounds is None else trigger_rounds
    if round_number < 0:
        raise ValueError("round_number must not be negative")
    if interval <= 0:
        raise ValueError("trigger_rounds must be positive")
    return round_number > 0 and round_number % interval == 0


async def maybe_summarize(
    round_number: int,
    history: Sequence[Mapping[str, Any]],
    *,
    previous_summary: str = "",
    trigger_rounds: int | None = None,
    generator: SummaryGenerator | None = None,
) -> str | None:
    """在命中摘要周期时生成新摘要，否则不调用 LLM。"""

    if not should_summarize(round_number, trigger_rounds=trigger_rounds):
        return None
    return await summarize(
        history,
        previous_summary=previous_summary,
        generator=generator,
    )


async def summarize(
    history: Sequence[Mapping[str, Any]],
    *,
    previous_summary: str = "",
    generator: SummaryGenerator | None = None,
) -> str:
    """把调用方提供的旧游戏历史压缩为 200-500 字叙事摘要。"""

    messages = _normalize_history(history)
    if not isinstance(previous_summary, str):
        raise ValueError("previous_summary must be a string")
    prompt = _build_prompt(messages, previous_summary.strip())
    target_generator = generator or DeepSeekClient(temperature=0.1).chat
    try:
        # Both attempts share one deadline. Repair only a length violation,
        # using the original sources rather than trusting an invalid candidate.
        async with asyncio.timeout(settings.llm_timeout):
            for attempt in range(2):
                summary = await target_generator(prompt, SUMMARY_SYSTEM_PROMPT)
                if not isinstance(summary, str):
                    raise SummarizationError("summary generator must return a string")
                normalized = summary.strip()
                length = len(normalized)
                if MIN_SUMMARY_LENGTH <= length <= MAX_SUMMARY_LENGTH:
                    return normalized
                logging.getLogger(__name__).warning("summary_length_rejected characters=%d attempt=%d", length, attempt + 1)
                if attempt == 1:
                    raise SummaryLengthError(
                        "summary length must be between "
                        f"{MIN_SUMMARY_LENGTH} and {MAX_SUMMARY_LENGTH} characters, got {length}"
                    )
                prompt += f"\n\n上次输出为 {length} 字，未满足长度约束。请重新根据以上原始来源压缩重复内容，输出约 240 字，严格保持 200-500 字，不要补充来源之外的事实。"
    except SummarizationError:
        raise
    except Exception as exc:
        raise SummarizationError("failed to generate summary memory") from exc


def _normalize_history(
    history: Sequence[Mapping[str, Any]],
) -> list[dict[str, str]]:
    if not history:
        raise ValueError("history must not be empty")

    normalized: list[dict[str, str]] = []
    for index, message in enumerate(history):
        if not isinstance(message, Mapping):
            raise ValueError(f"history item {index} must be an object")
        role = message.get("role")
        content = message.get("content")
        if not isinstance(role, str) or not role.strip():
            raise ValueError(f"history item {index} has invalid role")
        if not isinstance(content, str) or not content.strip():
            raise ValueError(f"history item {index} has invalid content")
        normalized.append(
            {
                "role": role.strip(),
                "content": content.strip(),
            }
        )
    return normalized


def _build_prompt(
    history: list[dict[str, str]],
    previous_summary: str,
) -> str:
    serialized_history = json.dumps(
        history,
        ensure_ascii=False,
        separators=(",", ":"),
    )
    return (
        "请将旧摘要与新增游戏日志合并为新的摘要记忆。\n\n"
        f"旧摘要：\n{previous_summary or '无'}\n\n"
        f"新增游戏日志（JSON）：\n{serialized_history}\n\n"
        "仅输出新的 200-500 字叙事摘要。"
    )
