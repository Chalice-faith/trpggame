"""按章节和自然边界切分文本，并保留剧本页码来源。"""

from __future__ import annotations

import re
from dataclasses import dataclass, field

from app.services.pdf_parser import ExtractedPDF, ExtractedPage

MIN_CHUNK_SIZE = 500
MAX_CHUNK_SIZE = 2_000
OVERLAP_SIZE = 100

_HEADING_PATTERNS = (
    re.compile(r"^#{1,6}\s+\S.+$"),
    re.compile(
        r"^第[零〇一二三四五六七八九十百千万两\d]+[章幕节卷篇]"
        r"(?:\s*[:：.\-—]?\s*.*)?$"
    ),
    re.compile(r"^(?:序章|楔子|前言|终章|尾声|后记)(?:\s*[:：.\-—]?\s*.*)?$"),
    re.compile(
        r"^(?:chapter|act|part)\s+(?:[0-9ivxlcdm]+|one|two|three|four|five)"
        r"(?:\s*[:：.\-—]?\s*.*)?$",
        re.IGNORECASE,
    ),
    re.compile(r"^(?:prologue|epilogue)(?:\s*[:：.\-—]?\s*.*)?$", re.IGNORECASE),
)
_BREAK_CHARACTERS = frozenset("。！？；.!?;")


@dataclass(frozen=True, slots=True)
class Chunk:
    content: str
    index: int
    script_id: int
    metadata: dict[str, object]


@dataclass(slots=True)
class _Section:
    chapter_index: int
    chapter_title: str
    parts: list[tuple[str, int]] = field(default_factory=list)


@dataclass(frozen=True, slots=True)
class _PageSpan:
    start: int
    end: int
    page_number: int


def chunk_pdf(
    document: ExtractedPDF,
    script_id: int,
    *,
    min_size: int = MIN_CHUNK_SIZE,
    max_size: int = MAX_CHUNK_SIZE,
    overlap: int = OVERLAP_SIZE,
) -> list[Chunk]:
    """优先按章节分组，再将长章节切为带重叠的文本片段。

    ``min_size`` 是自然断句时的目标下限。短章节和章节末尾不会为了满足
    下限而与相邻章节混合，因此可能产生小于该值的片段。
    """

    _validate_options(script_id, min_size, max_size, overlap)
    sections = _split_sections(document)
    chunks: list[Chunk] = []

    for section in sections:
        content, page_spans = _render_section(section)
        for start, end in _chunk_ranges(content, min_size, max_size, overlap):
            chunk_content = content[start:end].strip()
            if not chunk_content:
                continue

            pages = {
                span.page_number
                for span in page_spans
                if span.start < end and span.end > start
            }
            if not pages:
                continue

            chunks.append(
                Chunk(
                    content=chunk_content,
                    index=len(chunks),
                    script_id=script_id,
                    metadata={
                        "chapter_index": section.chapter_index,
                        "chapter_title": section.chapter_title,
                        "page_start": min(pages),
                        "page_end": max(pages),
                        "char_count": len(chunk_content),
                    },
                )
            )

    return chunks


def chunk_text(text: str, script_id: int) -> list[Chunk]:
    """兼容纯文本调用；纯文本的来源页统一记为第 1 页。"""

    document = ExtractedPDF(pages=(ExtractedPage(page_number=1, text=text),))
    return chunk_pdf(document, script_id)


def _validate_options(
    script_id: int,
    min_size: int,
    max_size: int,
    overlap: int,
) -> None:
    if script_id <= 0:
        raise ValueError("script_id must be positive")
    if min_size <= 0 or max_size < min_size:
        raise ValueError("chunk sizes must satisfy 0 < min_size <= max_size")
    if overlap < 0 or overlap >= min_size:
        raise ValueError("overlap must satisfy 0 <= overlap < min_size")


def _split_sections(document: ExtractedPDF) -> list[_Section]:
    sections: list[_Section] = []
    current = _Section(chapter_index=0, chapter_title="未分章")
    chapter_index = 0

    for page in document.pages:
        for raw_line in page.text.splitlines():
            line = raw_line.strip()
            if not line:
                continue

            if _is_heading(line):
                if current.parts:
                    sections.append(current)
                chapter_index += 1
                current = _Section(
                    chapter_index=chapter_index,
                    chapter_title=_normalize_heading(line),
                )

            current.parts.append((line, page.page_number))

    if current.parts:
        sections.append(current)
    return sections


def _is_heading(line: str) -> bool:
    return any(pattern.fullmatch(line) for pattern in _HEADING_PATTERNS)


def _normalize_heading(line: str) -> str:
    return re.sub(r"^#{1,6}\s+", "", line).strip()


def _render_section(section: _Section) -> tuple[str, tuple[_PageSpan, ...]]:
    pieces: list[str] = []
    spans: list[_PageSpan] = []
    position = 0

    for part, page_number in section.parts:
        if pieces:
            pieces.append("\n")
            position += 1
        start = position
        pieces.append(part)
        position += len(part)
        spans.append(_PageSpan(start=start, end=position, page_number=page_number))

    return "".join(pieces), tuple(spans)


def _chunk_ranges(
    text: str,
    min_size: int,
    max_size: int,
    overlap: int,
) -> list[tuple[int, int]]:
    ranges: list[tuple[int, int]] = []
    start = 0
    text_length = len(text)

    while start < text_length:
        hard_end = min(start + max_size, text_length)
        end = hard_end
        if hard_end < text_length:
            natural_end = _find_natural_break(text, start + min_size, hard_end)
            if natural_end is not None:
                end = natural_end

        ranges.append((start, end))
        if end >= text_length:
            break

        next_start = max(start + 1, end - overlap)
        while next_start < text_length and text[next_start].isspace():
            next_start += 1
        start = next_start

    return ranges


def _find_natural_break(text: str, lower: int, upper: int) -> int | None:
    for index in range(upper - 1, lower - 1, -1):
        if text[index] in _BREAK_CHARACTERS:
            return index + 1
        if text[index] == "\n":
            return index
    return None
