"""剧本解析相关 API"""

from fastapi import APIRouter
from pydantic import BaseModel

router = APIRouter()


class ParseScriptRequest(BaseModel):
    script_id: int
    file_path: str


class ParseScriptResponse(BaseModel):
    success: bool
    message: str


@router.post("/parse-script", response_model=ParseScriptResponse)
async def parse_script(req: ParseScriptRequest):
    """
    解析上传的 PDF 剧本：
    1. 提取文本 (pdf_parser)
    2. 清洗文本 (text_cleaner)
    3. 分片 (chunker)
    4. 向量化并存入 Milvus (embedder)
    """
    # TODO: Phase 1 M1.3 实现完整解析流程
    return ParseScriptResponse(success=True, message="Not implemented yet")
