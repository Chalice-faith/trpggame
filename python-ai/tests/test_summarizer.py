from __future__ import annotations

import asyncio
import unittest
from unittest.mock import patch

from app.config import settings

from app.services.summarizer import (
    MAX_SUMMARY_LENGTH,
    MIN_SUMMARY_LENGTH,
    SUMMARY_SYSTEM_PROMPT,
    SummarizationError,
    SummaryLengthError,
    maybe_summarize,
    should_summarize,
    summarize,
)


def summary_of_length(length: int) -> str:
    return "古" * length


class SummarizerTests(unittest.IsolatedAsyncioTestCase):
    async def test_length_repair_preserves_original_sources_and_excludes_invalid_candidate(self):
        prompts = []

        async def generator(prompt, system):
            prompts.append(prompt)
            return "不可信的候选" * 150 if len(prompts) == 1 else summary_of_length(300)

        result = await summarize([{"role": "user", "content": "暗号是青鸦七号"}],
            previous_summary="已进入书房", generator=generator)
        self.assertEqual(len(result), 300)
        self.assertEqual(len(prompts), 2)
        self.assertIn(prompts[0], prompts[1])
        self.assertIn("青鸦七号", prompts[1])
        self.assertNotIn("不可信的候选", prompts[1])

    async def test_two_length_failures_are_bounded(self):
        calls = 0

        async def generator(prompt, system):
            nonlocal calls
            calls += 1
            return "过短"

        with self.assertRaises(SummaryLengthError):
            await summarize([{"role": "user", "content": "继续"}], generator=generator)
        self.assertEqual(calls, 2)

    async def test_length_repair_uses_one_overall_timeout(self):
        calls = 0

        async def generator(prompt, system):
            nonlocal calls
            calls += 1
            if calls == 1:
                return "过短"
            await asyncio.sleep(1)
            return summary_of_length(300)

        with patch.object(settings, "llm_timeout", 0.01):
            with self.assertRaises(SummarizationError) as raised:
                await summarize([{"role": "user", "content": "继续"}], generator=generator)
        self.assertIsInstance(raised.exception.__cause__, TimeoutError)
        self.assertEqual(calls, 2)

    def test_should_summarize_every_five_rounds(self):
        self.assertFalse(should_summarize(0))
        self.assertFalse(should_summarize(4))
        self.assertTrue(should_summarize(5))
        self.assertTrue(should_summarize(10))
        self.assertFalse(should_summarize(11))

    def test_should_summarize_validates_round_and_interval(self):
        with self.assertRaises(ValueError):
            should_summarize(-1)
        with self.assertRaises(ValueError):
            should_summarize(5, trigger_rounds=0)

    async def test_maybe_summarize_does_not_call_generator_off_cycle(self):
        called = False

        async def generator(prompt: str, system_prompt: str) -> str:
            nonlocal called
            called = True
            return summary_of_length(MIN_SUMMARY_LENGTH)

        result = await maybe_summarize(4, [], generator=generator)

        self.assertIsNone(result)
        self.assertFalse(called)

    async def test_summarize_builds_prompt_with_old_summary_and_history(self):
        captured: dict[str, str] = {}

        async def generator(prompt: str, system_prompt: str) -> str:
            captured["prompt"] = prompt
            captured["system_prompt"] = system_prompt
            return f"  {summary_of_length(MIN_SUMMARY_LENGTH)}  "

        result = await summarize(
            [
                {"role": "user", "content": " 我调查书房 "},
                {"role": "assistant", "content": "你发现一封旧信。"},
            ],
            previous_summary="玩家已抵达古宅。",
            generator=generator,
        )

        self.assertEqual(len(result), MIN_SUMMARY_LENGTH)
        self.assertIn("玩家已抵达古宅。", captured["prompt"])
        self.assertIn("我调查书房", captured["prompt"])
        self.assertIn("你发现一封旧信。", captured["prompt"])
        self.assertEqual(captured["system_prompt"], SUMMARY_SYSTEM_PROMPT)

    async def test_accepts_both_summary_length_boundaries(self):
        history = [{"role": "user", "content": "继续"}]
        for length in (MIN_SUMMARY_LENGTH, MAX_SUMMARY_LENGTH):
            with self.subTest(length=length):
                result = await summarize(
                    history,
                    generator=self._generator(summary_of_length(length)),
                )
                self.assertEqual(len(result), length)

    async def test_rejects_summary_outside_length_contract(self):
        history = [{"role": "user", "content": "继续"}]
        for length in (MIN_SUMMARY_LENGTH - 1, MAX_SUMMARY_LENGTH + 1):
            with self.subTest(length=length):
                with self.assertRaises(SummaryLengthError):
                    await summarize(
                        history,
                        generator=self._generator(summary_of_length(length)),
                    )

    async def test_validates_history_before_calling_generator(self):
        invalid_histories = [
            [],
            [{"role": "", "content": "继续"}],
            [{"role": "user", "content": " "}],
            [{"role": "user"}],
        ]
        for history in invalid_histories:
            with self.subTest(history=history):
                with self.assertRaises(ValueError):
                    await summarize(
                        history,
                        generator=self._generator(
                            summary_of_length(MIN_SUMMARY_LENGTH)
                        ),
                    )

    async def test_wraps_generator_failure(self):
        async def generator(prompt: str, system_prompt: str) -> str:
            raise OSError("DeepSeek unavailable")

        with self.assertRaisesRegex(SummarizationError, "generate") as raised:
            await summarize(
                [{"role": "user", "content": "继续"}],
                generator=generator,
            )
        self.assertIsInstance(raised.exception.__cause__, OSError)

    @staticmethod
    def _generator(value: str):
        async def generator(prompt: str, system_prompt: str) -> str:
            return value

        return generator


if __name__ == "__main__":
    unittest.main()
