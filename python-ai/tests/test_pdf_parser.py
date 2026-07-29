from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

import fitz

from app.services.pdf_parser import (
    NoExtractableTextError,
    PDFExtractionError,
    PasswordProtectedPDFError,
    extract_pdf,
    extract_text,
)


class PDFParserTests(unittest.TestCase):
    def test_extracts_text_with_one_based_page_numbers(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "story.pdf"
            self._create_text_pdf(path, ["Chapter One", "The second page"])

            result = extract_pdf(path)

            self.assertEqual(result.page_count, 2)
            self.assertEqual(
                [page.page_number for page in result.pages],
                [1, 2],
            )
            self.assertIn("Chapter One", result.pages[0].text)
            self.assertIn("The second page", result.pages[1].text)
            self.assertEqual(result.text, extract_text(path))

    def test_rejects_blank_pdf_as_no_extractable_text(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "blank.pdf"
            document = fitz.open()
            document.new_page()
            document.save(path)
            document.close()

            with self.assertRaises(NoExtractableTextError):
                extract_pdf(path)

    def test_rejects_password_protected_pdf(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "protected.pdf"
            document = fitz.open()
            page = document.new_page()
            page.insert_text((72, 72), "Secret")
            document.save(
                path,
                encryption=fitz.PDF_ENCRYPT_AES_256,
                owner_pw="owner-secret",
                user_pw="user-secret",
            )
            document.close()

            with self.assertRaises(PasswordProtectedPDFError):
                extract_pdf(path)

    def test_wraps_corrupt_pdf_error(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "corrupt.pdf"
            path.write_bytes(b"%PDF-this is not a valid document")

            with self.assertRaises(PDFExtractionError) as raised:
                extract_pdf(path)

            self.assertIsNotNone(raised.exception.__cause__)

    def test_wraps_missing_file_error(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "missing.pdf"

            with self.assertRaises(PDFExtractionError) as raised:
                extract_pdf(path)

            self.assertIsInstance(raised.exception.__cause__, FileNotFoundError)

    @staticmethod
    def _create_text_pdf(path: Path, page_texts: list[str]) -> None:
        document = fitz.open()
        for text in page_texts:
            page = document.new_page()
            page.insert_text((72, 72), text)
        document.save(path)
        document.close()


if __name__ == "__main__":
    unittest.main()
