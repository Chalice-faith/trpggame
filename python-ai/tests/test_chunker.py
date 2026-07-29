from __future__ import annotations

import unittest

from app.services.chunker import chunk_pdf, chunk_text
from app.services.pdf_parser import ExtractedPDF, ExtractedPage


class ChunkerTests(unittest.TestCase):
    def test_splits_chapters_and_preserves_titles(self):
        document = ExtractedPDF(
            pages=(
                ExtractedPage(1, "前言\n背景说明"),
                ExtractedPage(2, "第一章：启程\n酒馆中的相遇"),
                ExtractedPage(3, "## Chapter Two\nThe locked gate"),
            )
        )

        chunks = chunk_pdf(document, 42)

        self.assertEqual(len(chunks), 3)
        self.assertEqual(
            [chunk.metadata["chapter_title"] for chunk in chunks],
            ["前言", "第一章：启程", "Chapter Two"],
        )
        self.assertEqual([chunk.metadata["chapter_index"] for chunk in chunks], [1, 2, 3])
        self.assertEqual([chunk.metadata["page_start"] for chunk in chunks], [1, 2, 3])

    def test_long_chapter_respects_max_size_and_adds_overlap(self):
        sentences = [f"第{i:03d}个事件发生在古堡大厅。" for i in range(180)]
        document = ExtractedPDF(
            pages=(ExtractedPage(1, "第一章 古堡\n" + "".join(sentences)),)
        )

        chunks = chunk_pdf(document, 7)

        self.assertGreater(len(chunks), 1)
        self.assertTrue(all(len(chunk.content) <= 2_000 for chunk in chunks))
        shared_tail = chunks[0].content[-100:].strip()
        self.assertIn(shared_tail, chunks[1].content[:120])
        self.assertEqual([chunk.index for chunk in chunks], list(range(len(chunks))))

    def test_tracks_page_range_for_cross_page_chunks(self):
        document = ExtractedPDF(
            pages=(
                ExtractedPage(4, "第一章 地下城\n" + "甲" * 70),
                ExtractedPage(5, "乙" * 120),
            )
        )

        chunks = chunk_pdf(document, 9, min_size=40, max_size=100, overlap=10)

        self.assertEqual(chunks[0].metadata["page_start"], 4)
        self.assertEqual(chunks[-1].metadata["page_end"], 5)
        self.assertTrue(any(chunk.metadata["page_start"] == 5 for chunk in chunks))
        self.assertTrue(all(chunk.metadata["char_count"] == len(chunk.content) for chunk in chunks))

    def test_does_not_mix_short_adjacent_chapters(self):
        document = ExtractedPDF(
            pages=(
                ExtractedPage(
                    1,
                    "第一章 开端\n短内容\n第二章 转折\n另一段短内容",
                ),
            )
        )

        chunks = chunk_pdf(document, 3)

        self.assertEqual(len(chunks), 2)
        self.assertNotIn("第二章", chunks[0].content)
        self.assertNotIn("第一章", chunks[1].content)

    def test_hard_splits_text_without_natural_boundaries(self):
        document = ExtractedPDF(pages=(ExtractedPage(1, "甲" * 260),))

        chunks = chunk_pdf(document, 5, min_size=50, max_size=100, overlap=10)

        self.assertEqual([len(chunk.content) for chunk in chunks], [100, 100, 80])
        self.assertTrue(all(chunk.metadata["chapter_title"] == "未分章" for chunk in chunks))

    def test_plain_text_wrapper_and_empty_document(self):
        chunks = chunk_text("一段普通文本", 11)

        self.assertEqual(len(chunks), 1)
        self.assertEqual(chunks[0].metadata["page_start"], 1)
        self.assertEqual(chunk_pdf(ExtractedPDF(pages=()), 11), [])

    def test_rejects_invalid_options(self):
        document = ExtractedPDF(pages=(ExtractedPage(1, "内容"),))

        with self.assertRaises(ValueError):
            chunk_pdf(document, 0)
        with self.assertRaises(ValueError):
            chunk_pdf(document, 1, min_size=100, max_size=50)
        with self.assertRaises(ValueError):
            chunk_pdf(document, 1, min_size=100, max_size=200, overlap=100)


if __name__ == "__main__":
    unittest.main()
