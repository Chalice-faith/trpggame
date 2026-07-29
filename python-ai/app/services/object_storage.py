"""MinIO 剧本对象下载与临时文件生命周期管理。"""

from __future__ import annotations

import os
import tempfile
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path, PurePosixPath
from typing import Protocol

from minio import Minio

from app.config import settings


class MinioDownloadClient(Protocol):
    """下载器依赖的最小 MinIO 客户端接口。"""

    def fget_object(self, bucket_name: str, object_name: str, file_path: str):
        ...


class InvalidScriptObjectKey(ValueError):
    """对象键不符合 scripts/{user_id}/{script_id}/{file}.pdf 约定。"""


class ScriptObjectDownloadError(RuntimeError):
    """从 MinIO 下载剧本失败。"""


class ScriptObjectStorage:
    """下载 MinIO 剧本，并确保临时文件最终被清理。"""

    def __init__(
        self,
        client: MinioDownloadClient | None = None,
        bucket: str | None = None,
        temp_dir: str | Path | None = None,
    ) -> None:
        self._client = (
            client
            if client is not None
            else Minio(
                settings.minio_endpoint,
                access_key=settings.minio_access_key,
                secret_key=settings.minio_secret_key,
                secure=settings.minio_secure,
            )
        )
        selected_bucket = bucket if bucket is not None else settings.minio_bucket
        self._bucket = selected_bucket.strip()
        self._temp_dir = Path(temp_dir) if temp_dir is not None else None

        if not self._bucket:
            raise ValueError("MinIO bucket must not be empty")

    @contextmanager
    def download_to_temp(self, object_name: str) -> Iterator[Path]:
        """下载对象并在退出上下文时删除随机临时 PDF。"""

        _validate_script_object_key(object_name)
        file_descriptor, temp_name = tempfile.mkstemp(
            prefix="trpg-script-",
            suffix=".pdf",
            dir=self._temp_dir,
        )
        os.close(file_descriptor)
        temp_path = Path(temp_name)

        try:
            try:
                self._client.fget_object(
                    self._bucket,
                    object_name,
                    str(temp_path),
                )
            except Exception as exc:
                raise ScriptObjectDownloadError(
                    f"failed to download script object {object_name!r}"
                ) from exc

            yield temp_path
        finally:
            temp_path.unlink(missing_ok=True)


def _validate_script_object_key(object_name: str) -> None:
    if not object_name or "\\" in object_name:
        raise InvalidScriptObjectKey("invalid script object key")

    path = PurePosixPath(object_name)
    parts = path.parts
    if (
        path.is_absolute()
        or len(parts) != 4
        or parts[0] != "scripts"
        or not parts[1].isdigit()
        or int(parts[1]) <= 0
        or not parts[2].isdigit()
        or int(parts[2]) <= 0
        or parts[3] in {"", ".", ".."}
        or not parts[3].lower().endswith(".pdf")
        or parts[3].lower() == ".pdf"
    ):
        raise InvalidScriptObjectKey("invalid script object key")
