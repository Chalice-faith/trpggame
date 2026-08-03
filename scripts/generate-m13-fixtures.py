"""Generate deterministic PDF fixtures for the M1.3 acceptance flow."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

import fitz

WORKSPACE_ROOT = Path(__file__).resolve().parents[1]
PYTHON_AI_ROOT = WORKSPACE_ROOT / "python-ai"
if str(PYTHON_AI_ROOT) not in sys.path:
    sys.path.insert(0, str(PYTHON_AI_ROOT))

from app.services.chunker import chunk_pdf  # noqa: E402
from app.services.pdf_parser import (  # noqa: E402
    NoExtractableTextError,
    extract_pdf,
)
from app.services.text_cleaner import clean_pdf  # noqa: E402

FIXTURE_NAMES = {
    "normal": "normal-text.pdf",
    "repeated": "repeated-header-footer.pdf",
    "blank": "blank-scan.pdf",
}


def generate_fixtures(output_dir: Path) -> dict[str, Path]:
    """Create the three PDF samples required by the M1.3 acceptance plan."""

    output_dir.mkdir(parents=True, exist_ok=True)
    paths = {
        fixture_type: output_dir / filename
        for fixture_type, filename in FIXTURE_NAMES.items()
    }

    _write_normal_pdf(paths["normal"])
    _write_repeated_edges_pdf(paths["repeated"])
    _write_blank_scan_pdf(paths["blank"])
    return paths


def verify_fixtures(paths: dict[str, Path]) -> None:
    """Verify parser, cleaner and chunker expectations for generated samples."""

    normal = clean_pdf(extract_pdf(paths["normal"]))
    normal_chunks = chunk_pdf(normal, script_id=1)
    if normal.page_count != 3 or not normal_chunks:
        raise RuntimeError("normal fixture did not produce pages and chunks")

    repeated = clean_pdf(extract_pdf(paths["repeated"]))
    if [page.page_number for page in repeated.pages] != [1, 2, 3]:
        raise RuntimeError("repeated-edge fixture lost page mapping")
    if any("M1.3 Acceptance Campaign" in page.text for page in repeated.pages):
        raise RuntimeError("repeated header was not removed")
    if any("Page " in page.text for page in repeated.pages):
        raise RuntimeError("page footer was not removed")
    if not chunk_pdf(repeated, script_id=2):
        raise RuntimeError("repeated-edge fixture did not produce chunks")

    try:
        extract_pdf(paths["blank"])
    except NoExtractableTextError:
        pass
    else:
        raise RuntimeError("blank fixture unexpectedly contained extractable text")


def _write_normal_pdf(path: Path) -> None:
    pages = (
        (
            "Chapter One: The Arrival",
            "The investigators arrive at Blackwood Manor during a violent storm. "
            "They find the front door open and a trail of wet footprints in the hall.",
        ),
        (
            "Chapter Two: The Locked Study",
            "A brass key is hidden behind the library portrait. The study contains "
            "a torn journal, a sealed letter, and a clock stopped at midnight.",
        ),
        (
            "Chapter Three: The Cellar",
            "The cellar stairs lead to an old ritual chamber. The investigators "
            "must decide whether to break the circle or complete the unfinished rite.",
        ),
    )
    document = fitz.open()
    try:
        for title, body in pages:
            page = document.new_page()
            page.insert_text((72, 80), title, fontsize=16)
            page.insert_textbox(
                fitz.Rect(72, 112, 520, 720),
                body,
                fontsize=11,
                lineheight=1.4,
            )
        _save_document(document, path)
    finally:
        document.close()


def _write_repeated_edges_pdf(path: Path) -> None:
    document = fitz.open()
    try:
        for page_number in range(1, 4):
            page = document.new_page()
            page.insert_text((72, 48), "M1.3 Acceptance Campaign", fontsize=9)
            page.insert_text(
                (72, 96),
                f"Chapter {page_number}: Scene {page_number}",
                fontsize=16,
            )
            page.insert_textbox(
                fitz.Rect(72, 128, 520, 720),
                (
                    f"Unique scene content for page {page_number}. "
                    "This paragraph must survive repeated header and footer cleanup. "
                    "The page number metadata must remain attached to the text."
                ),
                fontsize=11,
                lineheight=1.4,
            )
            page.insert_text((260, 806), f"Page {page_number} of 3", fontsize=9)
        _save_document(document, path)
    finally:
        document.close()


def _write_blank_scan_pdf(path: Path) -> None:
    document = fitz.open()
    try:
        page = document.new_page()
        page.draw_rect(
            fitz.Rect(72, 72, 520, 720),
            color=(0.65, 0.65, 0.65),
            fill=(0.95, 0.95, 0.95),
        )
        _save_document(document, path)
    finally:
        document.close()


def _save_document(document: fitz.Document, path: Path) -> None:
    path.unlink(missing_ok=True)
    document.save(path)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--output",
        type=Path,
        default=WORKSPACE_ROOT / ".artifacts" / "m13-fixtures",
        help="directory that receives generated PDF files",
    )
    parser.add_argument(
        "--verify",
        action="store_true",
        help="run parser, cleaner and chunker assertions after generation",
    )
    args = parser.parse_args()

    paths = generate_fixtures(args.output.resolve())
    if args.verify:
        verify_fixtures(paths)

    for fixture_type, path in paths.items():
        print(f"{fixture_type}: {path}")
    if args.verify:
        print("M1.3 fixture verification passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
