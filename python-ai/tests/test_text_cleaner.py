from __future__ import annotations

import unittest

from app.services.pdf_parser import ExtractedPDF, ExtractedPage
from app.services.text_cleaner import clean, clean_pdf


class TextCleanerTests(unittest.TestCase):
    def test_removes_repeated_headers_and_numbered_footers(self):
        document = ExtractedPDF(
            pages=(
                ExtractedPage(
                    1,
                    "Adventure Guide\n\nChapter One\nThe story begins.\n\nPage 1 of 3",
                ),
                ExtractedPage(
                    2,
                    "Adventure Guide\n\nChapter Two\nA door opens.\n\nPage 2 of 3",
                ),
                ExtractedPage(
                    3,
                    "Adventure Guide\n\nChapter Three\nThe end.\n\nPage 3 of 3",
                ),
            )
        )

        result = clean_pdf(document)

        self.assertEqual([page.page_number for page in result.pages], [1, 2, 3])
        for page in result.pages:
            self.assertNotIn("Adventure Guide", page.text)
            self.assertNotRegex(page.text, r"Page \d of 3")
        self.assertIn("Chapter Two", result.pages[1].text)

    def test_normalizes_unicode_whitespace_controls_and_blank_lines(self):
        text = "  Ｃｈａｐｔｅｒ\u00a0One\x00  \n\n\n\nFirst\t\tparagraph\ufffd  "

        result = clean(text)

        self.assertEqual(result, "Chapter One\n\nFirst paragraph")

    def test_removes_boundary_page_number_but_preserves_body_reference(self):
        document = ExtractedPDF(
            pages=(
                ExtractedPage(
                    1,
                    "1\nIntroduction\nSee Page 2 for the map.\nPage 2\n- 1 -",
                ),
            )
        )

        result = clean_pdf(document)

        self.assertEqual(
            result.pages[0].text,
            "Introduction\nSee Page 2 for the map.\nPage 2",
        )

    def test_preserves_unique_page_openings(self):
        document = ExtractedPDF(
            pages=(
                ExtractedPage(1, "Chapter One\nFirst scene"),
                ExtractedPage(2, "Chapter Two\nSecond scene"),
                ExtractedPage(3, "Chapter Three\nThird scene"),
            )
        )

        result = clean_pdf(document)

        self.assertTrue(result.pages[0].text.startswith("Chapter One"))
        self.assertTrue(result.pages[1].text.startswith("Chapter Two"))
        self.assertTrue(result.pages[2].text.startswith("Chapter Three"))

    def test_preserves_empty_pages_and_page_mapping(self):
        document = ExtractedPDF(
            pages=(
                ExtractedPage(1, "Content"),
                ExtractedPage(2, " \n \n"),
                ExtractedPage(3, "More content"),
            )
        )

        result = clean_pdf(document)

        self.assertEqual(result.page_count, 3)
        self.assertEqual(result.pages[1].page_number, 2)
        self.assertEqual(result.pages[1].text, "")

    def test_removes_repeated_footer_without_removing_body_duplicate(self):
        document = ExtractedPDF(
            pages=(
                ExtractedPage(1, "Scene One\nCampaign Notes\nCampaign Notes"),
                ExtractedPage(2, "Scene Two\nCampaign Notes\nCampaign Notes"),
                ExtractedPage(3, "Scene Three\nCampaign Notes"),
            )
        )

        result = clean_pdf(document)

        self.assertEqual(result.pages[0].text, "Scene One\nCampaign Notes")
        self.assertEqual(result.pages[1].text, "Scene Two\nCampaign Notes")
        self.assertEqual(result.pages[2].text, "Scene Three")


if __name__ == "__main__":
    unittest.main()
