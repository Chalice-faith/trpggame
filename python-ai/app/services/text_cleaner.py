"""保留页码映射的 PDF 文本清洗。"""

from __future__ import annotations

import math
import re
import unicodedata
from collections import Counter

from app.services.pdf_parser import ExtractedPDF, ExtractedPage

_HORIZONTAL_WHITESPACE = re.compile(r"[^\S\n]+")
_PAGE_NUMBER_PATTERNS = (
    re.compile(r"^(?:page\s*)?\d+(?:\s*(?:/|of)\s*\d+)?$", re.IGNORECASE),
    re.compile(r"^第\s*\d+\s*页(?:\s*共\s*\d+\s*页)?$"),
    re.compile(r"^[-–—]\s*\d+\s*[-–—]$"),
)
_MAX_RUNNING_TEXT_LENGTH = 120


def clean_pdf(document: ExtractedPDF) -> ExtractedPDF:
    """清洗逐页文本，并保留原始 1-based 页码。"""

    page_lines = [_normalize_lines(page.text) for page in document.pages]
    repeated_headers = _find_repeated_edge_lines(page_lines, from_start=True)
    repeated_footers = _find_repeated_edge_lines(page_lines, from_start=False)

    cleaned_pages = []
    for page, lines in zip(document.pages, page_lines, strict=True):
        lines = list(lines)
        first_index = _first_nonempty_index(lines)
        last_index = _last_nonempty_index(lines)

        if first_index is not None:
            first_line = lines[first_index]
            if (
                _line_key(first_line) in repeated_headers
                or _is_page_number(first_line)
            ):
                lines[first_index] = ""

        if last_index is not None:
            last_line = lines[last_index]
            if (
                _line_key(last_line) in repeated_footers
                or _is_page_number(last_line)
            ):
                lines[last_index] = ""

        cleaned_pages.append(
            ExtractedPage(
                page_number=page.page_number,
                text=_join_clean_lines(lines),
            )
        )

    return ExtractedPDF(pages=tuple(cleaned_pages))


def clean(text: str) -> str:
    """兼容原有调用方式，清洗单段文本并返回字符串。"""

    document = ExtractedPDF(pages=(ExtractedPage(page_number=1, text=text),))
    return clean_pdf(document).text


def _normalize_lines(text: str) -> list[str]:
    text = unicodedata.normalize("NFKC", text)
    text = text.replace("\u00a0", " ").replace("\ufffd", "")
    text = "".join(
        character
        for character in text
        if character in {"\n", "\t"}
        or not unicodedata.category(character).startswith("C")
    )

    return [
        _HORIZONTAL_WHITESPACE.sub(" ", line).strip()
        for line in text.splitlines()
    ]


def _find_repeated_edge_lines(
    pages: list[list[str]],
    *,
    from_start: bool,
) -> set[str]:
    candidates = []
    active_page_count = 0

    for lines in pages:
        index = (
            _first_nonempty_index(lines)
            if from_start
            else _last_nonempty_index(lines)
        )
        if index is None:
            continue

        active_page_count += 1
        line = lines[index]
        if _is_running_text_candidate(line):
            candidates.append(_line_key(line))

    if active_page_count < 2:
        return set()

    minimum_repetitions = max(2, math.ceil(active_page_count * 0.6))
    counts = Counter(candidates)
    return {
        line
        for line, count in counts.items()
        if count >= minimum_repetitions
    }


def _is_running_text_candidate(line: str) -> bool:
    return (
        1 < len(line) <= _MAX_RUNNING_TEXT_LENGTH
        and not _is_page_number(line)
    )


def _is_page_number(line: str) -> bool:
    normalized = _line_key(line)
    return any(pattern.fullmatch(normalized) for pattern in _PAGE_NUMBER_PATTERNS)


def _line_key(line: str) -> str:
    return _HORIZONTAL_WHITESPACE.sub(" ", line).strip().casefold()


def _first_nonempty_index(lines: list[str]) -> int | None:
    return next((index for index, line in enumerate(lines) if line), None)


def _last_nonempty_index(lines: list[str]) -> int | None:
    return next(
        (index for index in range(len(lines) - 1, -1, -1) if lines[index]),
        None,
    )


def _join_clean_lines(lines: list[str]) -> str:
    start = _first_nonempty_index(lines)
    end = _last_nonempty_index(lines)
    if start is None or end is None:
        return ""

    collapsed = []
    previous_was_blank = False
    for line in lines[start : end + 1]:
        if not line:
            if not previous_was_blank:
                collapsed.append("")
            previous_was_blank = True
            continue
        collapsed.append(line)
        previous_was_blank = False

    return "\n".join(collapsed)
