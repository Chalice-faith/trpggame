from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from app.services.object_storage import (
    InvalidScriptObjectKey,
    ScriptObjectDownloadError,
    ScriptObjectStorage,
)


class FakeMinioClient:
    def __init__(self, content: bytes = b"%PDF-1.7\nsample", error: Exception | None = None):
        self.content = content
        self.error = error
        self.calls: list[tuple[str, str, str]] = []

    def fget_object(self, bucket_name: str, object_name: str, file_path: str):
        self.calls.append((bucket_name, object_name, file_path))
        if self.error is not None:
            raise self.error
        Path(file_path).write_bytes(self.content)


class ScriptObjectStorageTests(unittest.TestCase):
    def test_downloads_object_and_removes_temp_file(self):
        client = FakeMinioClient()
        with tempfile.TemporaryDirectory() as temp_dir:
            storage = ScriptObjectStorage(
                client=client,
                bucket="test-scripts",
                temp_dir=temp_dir,
            )

            with storage.download_to_temp("scripts/7/42/file.pdf") as path:
                self.assertTrue(path.exists())
                self.assertEqual(path.read_bytes(), client.content)
                self.assertEqual(path.parent, Path(temp_dir))
                temp_path = path

            self.assertFalse(temp_path.exists())
            self.assertEqual(
                client.calls[0][:2],
                ("test-scripts", "scripts/7/42/file.pdf"),
            )

    def test_removes_temp_file_when_consumer_raises(self):
        client = FakeMinioClient()
        with tempfile.TemporaryDirectory() as temp_dir:
            storage = ScriptObjectStorage(client=client, temp_dir=temp_dir)

            with self.assertRaisesRegex(RuntimeError, "processing failed"):
                with storage.download_to_temp("scripts/7/42/file.pdf") as path:
                    temp_path = path
                    raise RuntimeError("processing failed")

            self.assertFalse(temp_path.exists())

    def test_wraps_download_error_and_removes_temp_file(self):
        client = FakeMinioClient(error=OSError("MinIO unavailable"))
        with tempfile.TemporaryDirectory() as temp_dir:
            storage = ScriptObjectStorage(client=client, temp_dir=temp_dir)

            with self.assertRaises(ScriptObjectDownloadError) as raised:
                with storage.download_to_temp("scripts/7/42/file.pdf"):
                    self.fail("download context should not be entered")

            self.assertIsInstance(raised.exception.__cause__, OSError)
            self.assertEqual(list(Path(temp_dir).iterdir()), [])

    def test_rejects_invalid_object_keys_before_download(self):
        invalid_keys = [
            "",
            "/scripts/7/42/file.pdf",
            "scripts/7/../file.pdf",
            "scripts/user/42/file.pdf",
            "scripts/7/script/file.pdf",
            r"scripts\7\42\file.pdf",
            "scripts/7/42/file.txt",
            "other/7/42/file.pdf",
        ]

        for object_key in invalid_keys:
            with self.subTest(object_key=object_key):
                client = FakeMinioClient()
                storage = ScriptObjectStorage(client=client)

                with self.assertRaises(InvalidScriptObjectKey):
                    with storage.download_to_temp(object_key):
                        self.fail("invalid key should not enter context")

                self.assertEqual(client.calls, [])

    def test_rejects_empty_bucket(self):
        for bucket in ("", " "):
            with self.subTest(bucket=bucket):
                with self.assertRaisesRegex(ValueError, "bucket"):
                    ScriptObjectStorage(client=FakeMinioClient(), bucket=bucket)


if __name__ == "__main__":
    unittest.main()
