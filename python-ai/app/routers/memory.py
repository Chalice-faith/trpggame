"""Internal summary generation: returns a candidate, never writes game state."""
from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field
from app.dependencies import require_internal_secret
from app.config import settings
from app.services.context_builder import estimated_tokens
from app.services.summarizer import summarize, SummarizationError

router = APIRouter(dependencies=[Depends(require_internal_secret)])

class SummaryMessage(BaseModel):
    role: str = Field(min_length=1, max_length=20)
    content: str = Field(min_length=1, max_length=65535)

class SummaryRequest(BaseModel):
    previous_summary: str = Field(default="", max_length=65535)
    messages: list[SummaryMessage] = Field(min_length=1, max_length=512)

@router.post("/memory/summary")
async def generate_summary(request: SummaryRequest):
    budget = min(settings.context_input_budget, settings.context_window_tokens - settings.llm_max_tokens - settings.context_safety_tokens) - 1024
    if estimated_tokens(request.model_dump_json()) > budget:
        raise HTTPException(413, "summary source exceeds input budget")
    try:
        candidate = await summarize([message.model_dump() for message in request.messages], previous_summary=request.previous_summary)
    except SummarizationError as exc:
        raise HTTPException(503, "summary generation unavailable") from exc
    return {"summary": candidate}
