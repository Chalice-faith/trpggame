"""基于 PyMuPDF 的 PDF 文本与页码提取。"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import fitz


class PDFExtractionError(RuntimeError):
    """PDF 无法打开或提取。"""


class PasswordProtectedPDFError(PDFExtractionError):
    """PDF 需要密码。"""


class NoExtractableTextError(PDFExtractionError):
    """PDF 没有可提取的文本，通常是空白或扫描型文件。"""


@dataclass(frozen=True, slots=True)
class ExtractedPage:
    """单页提取结果，页码从 1 开始。"""

    page_number: int
    text: str


@dataclass(frozen=True, slots=True)
class ExtractedPDF:
    """完整 PDF 提取结果。"""

    pages: tuple[ExtractedPage, ...]

    @property
    def page_count(self) -> int:
        return len(self.pages)

    @property
    def text(self) -> str:
        return "\n\n".join(page.text for page in self.pages if page.text)


def extract_pdf(file_path: str | Path) -> ExtractedPDF:
    """逐页提取 PDF 文本，并保留 1-based 页码。"""

    path = Path(file_path)
    if not path.is_file():
        missing_file = FileNotFoundError(f"PDF file does not exist: {path}")
        raise PDFExtractionError(f"failed to open PDF {path.name!r}") from missing_file

    try:
        document = fitz.open(path)
    except (FileNotFoundError, OSError, RuntimeError, ValueError) as exc:
        raise PDFExtractionError(f"failed to open PDF {path.name!r}") from exc

    try:
        if document.needs_pass:
            raise PasswordProtectedPDFError("password-protected PDF is not supported")

        pages = tuple(
            ExtractedPage(
                page_number=index + 1,
                text=_extract_page_text(page),
            )
            for index, page in enumerate(document)
        )
    except PasswordProtectedPDFError:
        raise
    except (RuntimeError, ValueError) as exc:
        raise PDFExtractionError(f"failed to extract PDF {path.name!r}") from exc
    finally:
        document.close()

    if not any(page.text.strip() for page in pages):
        raise NoExtractableTextError(
            "PDF contains no extractable text; OCR is not enabled"
        )

    return ExtractedPDF(pages=pages)


def extract_text(file_path: str | Path) -> str:
    """兼容原有调用方式，仅返回合并后的文本。"""

    return extract_pdf(file_path).text


def _extract_page_text(page: fitz.Page) -> str:
    text = page.get_text("text", sort=True)
    return text.replace("\r\n", "\n").replace("\r", "\n").strip()
