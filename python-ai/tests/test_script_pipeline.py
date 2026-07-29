from __future__ import annotations

import tempfile
import unittest
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator

import httpx
from fastapi.testclient import TestClient

from app.main import create_app
from app.routers.script import (
    GoStatusCallback,
    ScriptPipeline,
    get_script_pipeline,
    get_vector_cleaner,
)
from app.services.chunker import Chunk
from app.services.pdf_parser import (
    ExtractedPDF,
    ExtractedPage,
    NoExtractableTextError,
)


class FakeStorage:
    def __init__(self, events: list[str]) -> None:
        self.events = events

    @contextmanager
    def download_to_temp(self, object_key: str) -> Iterator[Path]:
        self.events.append(f"download:{object_key}")
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "script.pdf"
            path.write_bytes(b"%PDF")
            try:
                yield path
            finally:
                self.events.append("cleanup")


class FakeCallback:
    def __init__(self, *, should_fail: bool = False) -> None:
        self.should_fail = should_fail
        self.calls: list[dict[str, object]] = []

    def update(
        self,
        script_id: int,
        script_status: str,
        *,
        chunk_count: int = 0,
        error_message: str = "",
    ) -> None:
        self.calls.append(
            {
                "script_id": script_id,
                "status": script_status,
                "chunk_count": chunk_count,
                "error_message": error_message,
            }
        )
        if self.should_fail:
            raise RuntimeError("callback unavailable")


class FakeHTTPResponse:
    def raise_for_status(self) -> None:
        return None


class FakeHTTPClient:
    def __init__(self) -> None:
        self.calls: list[dict[str, object]] = []

    def post(
        self,
        url: str,
        *,
        json: dict[str, object],
        headers: dict[str, str],
    ) -> FakeHTTPResponse:
        self.calls.append({"url": url, "json": json, "headers": headers})
        return FakeHTTPResponse()


class ScriptPipelineTests(unittest.TestCase):
    def test_runs_complete_pipeline_and_reports_ready(self):
        events: list[str] = []
        callback = FakeCallback()
        document = ExtractedPDF((ExtractedPage(1, "第一章\n内容"),))
        chunks = [
            Chunk(
                content="第一章\n内容",
                index=0,
                script_id=12,
                metadata={
                    "chapter_index": 1,
                    "chapter_title": "第一章",
                    "page_start": 1,
                    "page_end": 1,
                },
            )
        ]

        def extract(path: str | Path) -> ExtractedPDF:
            self.assertTrue(Path(path).exists())
            events.append("extract")
            return document

        def clean(value: ExtractedPDF) -> ExtractedPDF:
            self.assertIs(value, document)
            events.append("clean")
            return value

        def chunk(value: ExtractedPDF, script_id: int) -> list[Chunk]:
            self.assertIs(value, document)
            self.assertEqual(script_id, 12)
            events.append("chunk")
            return chunks

        def embed(value: list[Chunk]) -> int:
            self.assertIs(value, chunks)
            events.append("embed")
            return 1

        pipeline = ScriptPipeline(
            storage=FakeStorage(events),
            extractor=extract,
            cleaner=clean,
            chunker=chunk,
            embedder=embed,
            callback=callback,
        )

        result = pipeline.process(12, "scripts/1/12/file.pdf")

        self.assertTrue(result.success)
        self.assertEqual(result.chunk_count, 1)
        self.assertEqual(
            events,
            [
                "download:scripts/1/12/file.pdf",
                "extract",
                "clean",
                "chunk",
                "embed",
                "cleanup",
            ],
        )
        self.assertEqual(callback.calls[0]["status"], "ready")
        self.assertEqual(callback.calls[0]["chunk_count"], 1)

    def test_reports_readable_failure_and_cleans_temp_file(self):
        events: list[str] = []
        callback = FakeCallback()

        def fail_extract(path: str | Path) -> ExtractedPDF:
            self.assertTrue(Path(path).exists())
            raise NoExtractableTextError("no text")

        pipeline = ScriptPipeline(
            storage=FakeStorage(events),
            extractor=fail_extract,
            callback=callback,
        )

        result = pipeline.process(4, "scripts/1/4/file.pdf")

        self.assertFalse(result.success)
        self.assertIn("扫描件", result.error_message)
        self.assertEqual(events[-1], "cleanup")
        self.assertEqual(callback.calls[0]["status"], "failed")
        self.assertIn("扫描件", callback.calls[0]["error_message"])

    def test_callback_failure_does_not_escape_background_task(self):
        events: list[str] = []
        callback = FakeCallback(should_fail=True)
        document = ExtractedPDF((ExtractedPage(1, "内容"),))
        chunk = Chunk(
            content="内容",
            index=0,
            script_id=3,
            metadata={
                "chapter_index": 0,
                "chapter_title": "未分章",
                "page_start": 1,
                "page_end": 1,
            },
        )
        pipeline = ScriptPipeline(
            storage=FakeStorage(events),
            extractor=lambda _: document,
            cleaner=lambda value: value,
            chunker=lambda _, __: [chunk],
            embedder=lambda _: 1,
            callback=callback,
        )

        result = pipeline.process(3, "scripts/1/3/file.pdf")

        self.assertFalse(result.success)
        self.assertEqual(result.chunk_count, 1)
        self.assertIn("状态回写失败", result.error_message)

    def test_rejects_object_key_for_a_different_script(self):
        events: list[str] = []
        callback = FakeCallback()
        pipeline = ScriptPipeline(
            storage=FakeStorage(events),
            callback=callback,
        )

        result = pipeline.process(3, "scripts/1/99/file.pdf")

        self.assertFalse(result.success)
        self.assertEqual(result.error_message, "剧本文件路径无效")
        self.assertEqual(events, [])
        self.assertEqual(callback.calls[0]["status"], "failed")

    def test_status_callback_uses_internal_contract(self):
        client = FakeHTTPClient()
        callback = GoStatusCallback(
            client=client,
            base_url="http://go:8080/api/v1/internal/",
            shared_secret="test-secret",
        )

        callback.update(9, "ready", chunk_count=5)

        call = client.calls[0]
        self.assertEqual(
            call["url"],
            "http://go:8080/api/v1/internal/scripts/9/status",
        )
        self.assertEqual(call["headers"], {"X-Internal-Secret": "test-secret"})
        self.assertEqual(
            call["json"],
            {"status": "ready", "chunk_count": 5, "error_message": ""},
        )

    def test_endpoint_returns_accepted_and_schedules_pipeline(self):
        class RecordingPipeline:
            def __init__(self) -> None:
                self.calls: list[tuple[int, str]] = []

            def process(self, script_id: int, object_key: str) -> None:
                self.calls.append((script_id, object_key))

        pipeline = RecordingPipeline()
        app = create_app()
        app.dependency_overrides[get_script_pipeline] = lambda: pipeline

        with TestClient(app) as client:
            response = client.post(
                "/api/v1/ai/parse-script",
                json={
                    "script_id": 21,
                    "file_path": "scripts/2/21/file.pdf",
                },
                headers={"X-Internal-Secret": "dev-internal-secret-change-in-production"},
            )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["success"], True)
        self.assertEqual(pipeline.calls, [(21, "scripts/2/21/file.pdf")])

    def test_endpoint_rejects_invalid_task(self):
        app = create_app()
        with TestClient(app) as client:
            response = client.post(
                "/api/v1/ai/parse-script",
                json={"script_id": 0, "file_path": " "},
                headers={"X-Internal-Secret": "dev-internal-secret-change-in-production"},
            )

        self.assertEqual(response.status_code, 422)

    def test_parse_endpoint_requires_internal_secret(self):
        app = create_app()
        with TestClient(app) as client:
            response = client.post(
                "/api/v1/ai/parse-script",
                json={
                    "script_id": 21,
                    "file_path": "scripts/2/21/file.pdf",
                },
            )

        self.assertEqual(response.status_code, 401)

    def test_delete_vectors_endpoint_calls_cleaner(self):
        cleaned_script_ids: list[int] = []
        app = create_app()
        app.dependency_overrides[get_vector_cleaner] = (
            lambda: cleaned_script_ids.append
        )

        with TestClient(app) as client:
            response = client.delete(
                "/api/v1/ai/scripts/21/vectors",
                headers={"X-Internal-Secret": "dev-internal-secret-change-in-production"},
            )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["success"], True)
        self.assertEqual(cleaned_script_ids, [21])

    def test_delete_vectors_reports_cleanup_failure(self):
        def fail_cleanup(_: int) -> None:
            raise RuntimeError("Milvus unavailable")

        app = create_app()
        app.dependency_overrides[get_vector_cleaner] = lambda: fail_cleanup

        with TestClient(app) as client:
            response = client.delete(
                "/api/v1/ai/scripts/21/vectors",
                headers={"X-Internal-Secret": "dev-internal-secret-change-in-production"},
            )

        self.assertEqual(response.status_code, 503)


if __name__ == "__main__":
    unittest.main()
