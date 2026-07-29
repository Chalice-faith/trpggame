"""剧本解析任务受理、后台编排与 Go 状态回调。"""

from __future__ import annotations

import logging
import hashlib
import hmac
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Literal, Protocol

import httpx
from fastapi import (
    APIRouter,
    BackgroundTasks,
    Depends,
    Header,
    HTTPException,
    Path as PathParameter,
    status,
)
from pydantic import BaseModel, Field, field_validator

from app.config import settings
from app.services.chunker import Chunk, chunk_pdf
from app.services.embedder import (
    EmbeddingError,
    VectorStoreError,
    delete_script_chunks,
    embed_and_store,
)
from app.services.object_storage import (
    InvalidScriptObjectKey,
    ScriptObjectDownloadError,
    ScriptObjectStorage,
)
from app.services.pdf_parser import (
    ExtractedPDF,
    NoExtractableTextError,
    PDFExtractionError,
    PasswordProtectedPDFError,
    extract_pdf,
)
from app.services.text_cleaner import clean_pdf

logger = logging.getLogger(__name__)
router = APIRouter()

ScriptStatus = Literal["ready", "failed"]


class ParseScriptRequest(BaseModel):
    script_id: int = Field(gt=0)
    file_path: str = Field(min_length=1, max_length=1_024)

    @field_validator("file_path")
    @classmethod
    def normalize_file_path(cls, value: str) -> str:
        value = value.strip()
        if not value:
            raise ValueError("file_path must not be blank")
        return value


class ParseScriptResponse(BaseModel):
    success: bool
    message: str


class DeleteVectorsResponse(BaseModel):
    success: bool
    message: str


@dataclass(frozen=True, slots=True)
class ScriptProcessingResult:
    success: bool
    chunk_count: int = 0
    error_message: str = ""


class StatusCallback(Protocol):
    def update(
        self,
        script_id: int,
        script_status: ScriptStatus,
        *,
        chunk_count: int = 0,
        error_message: str = "",
    ) -> None: ...


class HTTPClient(Protocol):
    def post(
        self,
        url: str,
        *,
        json: dict[str, object],
        headers: dict[str, str],
    ) -> httpx.Response: ...


class GoStatusCallback:
    """使用共享密钥调用 Go 内部剧本状态接口。"""

    def __init__(
        self,
        client: HTTPClient | None = None,
        *,
        base_url: str | None = None,
        shared_secret: str | None = None,
    ) -> None:
        self._client = client
        self._base_url = (base_url or settings.go_callback_base_url).rstrip("/")
        self._shared_secret = (
            shared_secret
            if shared_secret is not None
            else settings.internal_shared_secret
        )
        if not self._base_url:
            raise ValueError("Go callback base URL must not be empty")
        if not self._shared_secret:
            raise ValueError("internal shared secret must not be empty")

    def update(
        self,
        script_id: int,
        script_status: ScriptStatus,
        *,
        chunk_count: int = 0,
        error_message: str = "",
    ) -> None:
        if script_id <= 0:
            raise ValueError("script_id must be positive")
        if script_status not in {"ready", "failed"}:
            raise ValueError("unsupported script status")

        payload: dict[str, object] = {
            "status": script_status,
            "chunk_count": chunk_count,
            "error_message": error_message,
        }
        url = f"{self._base_url}/scripts/{script_id}/status"
        headers = {"X-Internal-Secret": self._shared_secret}

        try:
            if self._client is not None:
                response = self._client.post(url, json=payload, headers=headers)
            else:
                with httpx.Client(timeout=10.0) as client:
                    response = client.post(url, json=payload, headers=headers)
            response.raise_for_status()
        except httpx.HTTPError as exc:
            raise RuntimeError(
                f"failed to update status for script {script_id}"
            ) from exc


class EmptyChunkResultError(RuntimeError):
    """清洗后的 PDF 未生成任何可索引片段。"""


class ScriptPipeline:
    """同步执行单个剧本任务，由 FastAPI 后台线程调用。"""

    def __init__(
        self,
        *,
        storage: ScriptObjectStorage | None = None,
        extractor: Callable[[str | Path], ExtractedPDF] = extract_pdf,
        cleaner: Callable[[ExtractedPDF], ExtractedPDF] = clean_pdf,
        chunker: Callable[[ExtractedPDF, int], list[Chunk]] = chunk_pdf,
        embedder: Callable[[list[Chunk]], int] = embed_and_store,
        callback: StatusCallback | None = None,
    ) -> None:
        self._storage = storage or ScriptObjectStorage()
        self._extractor = extractor
        self._cleaner = cleaner
        self._chunker = chunker
        self._embedder = embedder
        self._callback = callback or GoStatusCallback()

    def process(self, script_id: int, object_key: str) -> ScriptProcessingResult:
        try:
            _validate_task_object(script_id, object_key)
            with self._storage.download_to_temp(object_key) as pdf_path:
                document = self._extractor(pdf_path)
                cleaned_document = self._cleaner(document)
                chunks = self._chunker(cleaned_document, script_id)
                if not chunks:
                    raise EmptyChunkResultError(
                        "cleaned PDF produced no indexable chunks"
                    )
                stored_count = self._embedder(chunks)
                if stored_count != len(chunks):
                    raise VectorStoreError(
                        f"stored {stored_count} of {len(chunks)} script chunks"
                    )
        except Exception as exc:
            error_message = _public_error_message(exc)
            logger.exception("script %s processing failed", script_id)
            self._notify_failure(script_id, error_message)
            return ScriptProcessingResult(
                success=False,
                error_message=error_message,
            )

        try:
            self._callback.update(
                script_id,
                "ready",
                chunk_count=stored_count,
            )
        except Exception:
            logger.exception("script %s ready callback failed", script_id)
            return ScriptProcessingResult(
                success=False,
                chunk_count=stored_count,
                error_message="剧本已完成索引，但状态回写失败",
            )

        return ScriptProcessingResult(success=True, chunk_count=stored_count)

    def _notify_failure(self, script_id: int, error_message: str) -> None:
        try:
            self._callback.update(
                script_id,
                "failed",
                error_message=error_message,
            )
        except Exception:
            logger.exception("script %s failure callback failed", script_id)


def get_script_pipeline() -> ScriptPipeline:
    return ScriptPipeline()


def get_vector_cleaner() -> Callable[[int], None]:
    return delete_script_chunks


def require_internal_secret(
    provided_secret: str | None = Header(
        default=None,
        alias="X-Internal-Secret",
    ),
) -> None:
    expected_secret = settings.internal_shared_secret
    if not expected_secret:
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail="internal authentication is not configured",
        )

    provided_digest = hashlib.sha256((provided_secret or "").encode()).digest()
    expected_digest = hashlib.sha256(expected_secret.encode()).digest()
    if not hmac.compare_digest(provided_digest, expected_digest):
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="invalid internal credentials",
        )


@router.post(
    "/parse-script",
    response_model=ParseScriptResponse,
    status_code=status.HTTP_200_OK,
    dependencies=[Depends(require_internal_secret)],
)
async def parse_script(
    request: ParseScriptRequest,
    background_tasks: BackgroundTasks,
    pipeline: ScriptPipeline = Depends(get_script_pipeline),
) -> ParseScriptResponse:
    """受理解析任务并立即返回；耗时处理在响应后的后台线程中执行。"""

    background_tasks.add_task(
        pipeline.process,
        request.script_id,
        request.file_path,
    )
    return ParseScriptResponse(success=True, message="script parsing accepted")


@router.delete(
    "/scripts/{script_id}/vectors",
    response_model=DeleteVectorsResponse,
    dependencies=[Depends(require_internal_secret)],
)
def delete_script_vectors(
    script_id: int = PathParameter(gt=0),
    cleaner: Callable[[int], None] = Depends(get_vector_cleaner),
) -> DeleteVectorsResponse:
    """幂等删除指定剧本的全部 Milvus 向量。"""

    try:
        cleaner(script_id)
    except Exception as exc:
        logger.exception("script %s vector cleanup failed", script_id)
        raise HTTPException(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            detail="vector cleanup failed",
        ) from exc
    return DeleteVectorsResponse(
        success=True,
        message="script vectors deleted",
    )


def _public_error_message(exc: Exception) -> str:
    if isinstance(exc, InvalidScriptObjectKey):
        return "剧本文件路径无效"
    if isinstance(exc, ScriptObjectDownloadError):
        return "剧本文件下载失败"
    if isinstance(exc, PasswordProtectedPDFError):
        return "PDF 已加密，暂不支持解析"
    if isinstance(exc, NoExtractableTextError):
        return "PDF 未包含可提取文本，扫描件暂不支持"
    if isinstance(exc, PDFExtractionError):
        return "PDF 解析失败"
    if isinstance(exc, EmptyChunkResultError):
        return "PDF 清洗后没有可索引内容"
    if isinstance(exc, EmbeddingError):
        return "文本向量化失败"
    if isinstance(exc, VectorStoreError):
        return "向量索引写入失败"
    return "剧本解析失败"


def _validate_task_object(script_id: int, object_key: str) -> None:
    parts = PurePosixPath(object_key).parts
    if (
        script_id <= 0
        or len(parts) != 4
        or parts[0] != "scripts"
        or not parts[2].isdigit()
        or int(parts[2]) != script_id
    ):
        raise InvalidScriptObjectKey(
            "script_id does not match the MinIO object key"
        )
