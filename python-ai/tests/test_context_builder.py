from __future__ import annotations

import unittest

from app.services.context_builder import (
    BASE_SYSTEM_PROMPT,
    ContextAssemblyError,
    assemble_context,
)


class ContextBuilderTests(unittest.TestCase):
    def test_assembles_all_context_sections(self):
        context = assemble_context(
            "我调查书房里的旧信",
            rag_chunks=["书房位于二楼。", "旧信藏在画像后。"],
            summary_memory="玩家已经进入古宅。",
            recent_history=[
                {"role": "user", "content": "我走上二楼"},
                {"role": "assistant", "content": "木板发出吱呀声。"},
            ],
            player_state={"hp": 8, "location": "二楼"},
            character_profile={"name": "林默", "profession": "调查员"},
        )

        self.assertIn(BASE_SYSTEM_PROMPT, context.system_prompt)
        self.assertIn("书房位于二楼。", context.system_prompt)
        self.assertIn("旧信藏在画像后。", context.system_prompt)
        self.assertIn("玩家已经进入古宅。", context.system_prompt)
        self.assertIn('"hp":8', context.system_prompt)
        self.assertIn('"name":"林默"', context.system_prompt)
        self.assertIn("我走上二楼", context.user_prompt)
        self.assertIn("我调查书房里的旧信", context.user_prompt)

    def test_limits_rag_chunks_to_configured_top_five(self):
        chunks = [f"片段内容 {index}" for index in range(1, 7)]

        context = assemble_context("继续", rag_chunks=chunks)

        self.assertIn("片段内容 5", context.system_prompt)
        self.assertNotIn("片段内容 6", context.system_prompt)

    def test_keeps_only_latest_ten_history_messages(self):
        history = [
            {"role": "user", "content": f"行动 {index}"}
            for index in range(12)
        ]

        context = assemble_context("继续", rag_chunks=[], recent_history=history)

        self.assertEqual(len(context.recent_history), 10)
        self.assertEqual(context.recent_history[0].content, "行动 2")
        self.assertNotIn("行动 0", context.user_prompt)
        self.assertIn("行动 11", context.user_prompt)

    def test_normalizes_text_and_uses_empty_placeholders(self):
        context = assemble_context(
            "  继续前进  ",
            rag_chunks=[],
            recent_history=[{"role": " user ", "content": " 查看房间 "}],
        )

        self.assertIn("暂无可用剧本片段", context.system_prompt)
        self.assertIn("暂无摘要记忆", context.system_prompt)
        self.assertIn("## 当前角色资料\n暂无", context.system_prompt)
        self.assertIn("## 当前角色状态\n暂无", context.system_prompt)
        self.assertEqual(context.recent_history[0].role, "user")
        self.assertIn("继续前进", context.user_prompt)

    def test_allows_explicit_context_limits(self):
        context = assemble_context(
            "继续",
            rag_chunks=["一", "二"],
            recent_history=[
                {"role": "user", "content": "甲"},
                {"role": "assistant", "content": "乙"},
            ],
            max_recent_messages=1,
            max_rag_chunks=1,
        )

        self.assertEqual(len(context.recent_history), 1)
        self.assertIn("一", context.system_prompt)
        self.assertNotIn("### 片段 2", context.system_prompt)

    def test_rejects_invalid_text_limits_and_history(self):
        cases = [
            {"action": "", "rag_chunks": []},
            {"action": "继续", "rag_chunks": [" "]},
            {
                "action": "继续",
                "rag_chunks": [],
                "recent_history": [{"role": "", "content": "内容"}],
            },
            {"action": "继续", "rag_chunks": [], "max_recent_messages": 0},
            {"action": "继续", "rag_chunks": [], "max_rag_chunks": 0},
        ]

        for options in cases:
            with self.subTest(options=options):
                with self.assertRaises(ContextAssemblyError):
                    assemble_context(**options)

    def test_rejects_non_serializable_state(self):
        with self.assertRaisesRegex(ContextAssemblyError, "JSON serializable"):
            assemble_context(
                "继续",
                rag_chunks=[],
                player_state={"inventory": {"不可序列化集合"}},
            )


if __name__ == "__main__":
    unittest.main()
