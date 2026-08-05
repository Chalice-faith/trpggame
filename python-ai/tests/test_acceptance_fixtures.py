from __future__ import annotations

import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from app.services.pdf_parser import NoExtractableTextError, extract_pdf
from app.services.text_cleaner import clean_pdf


class AcceptanceFixtureTests(unittest.TestCase):
    def test_generator_creates_and_verifies_required_pdf_samples(self):
        workspace_root = Path(__file__).resolve().parents[2]
        generator = workspace_root / "scripts" / "generate-m13-fixtures.py"

        with tempfile.TemporaryDirectory() as temp_dir:
            output_dir = Path(temp_dir) / "fixtures"
            result = subprocess.run(
                [
                    sys.executable,
                    str(generator),
                    "--output",
                    str(output_dir),
                    "--verify",
                ],
                cwd=workspace_root,
                check=False,
                capture_output=True,
                text=True,
                encoding="utf-8",
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("M1.3 fixture verification passed.", result.stdout)

            normal = extract_pdf(output_dir / "normal-text.pdf")
            self.assertEqual(normal.page_count, 3)

            repeated = clean_pdf(
                extract_pdf(output_dir / "repeated-header-footer.pdf")
            )
            self.assertEqual(
                [page.page_number for page in repeated.pages],
                [1, 2, 3],
            )
            self.assertNotIn("M1.3 Acceptance Campaign", repeated.text)
            self.assertNotIn("Page 1 of 3", repeated.text)

            with self.assertRaises(NoExtractableTextError):
                extract_pdf(output_dir / "blank-scan.pdf")


if __name__ == "__main__":
    unittest.main()
