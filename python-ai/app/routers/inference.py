"""AI 推理相关 API"""

from fastapi import APIRouter
from pydantic import BaseModel

router = APIRouter()


class StartGameRequest(BaseModel):
    room_id: int
    script_id: int
    character_id: int
    user_id: int


class NarrativeResponse(BaseModel):
    narrative: str


@router.post("/inference/start", response_model=NarrativeResponse)
async def start_game(req: StartGameRequest):
    """
    生成开场叙事：
    1. 检索剧本开场相关片段 (RAG)
    2. 组装系统提示词 + 角色设定
    3. 调用 LLM 生成开场叙事
    """
    # TODO: Phase 1 M1.4 实现
    return NarrativeResponse(narrative="Not implemented yet")


class GameActionRequest(BaseModel):
    room_id: int
    user_id: int
    action: str
    script_id: int
    character_id: int


class DiceRollData(BaseModel):
    type: str
    result: int
    success: bool


class GameActionResponse(BaseModel):
    narrative: str
    dice_roll: DiceRollData | None = None
    status_changes: dict | None = None


@router.post("/inference/action", response_model=GameActionResponse)
async def process_action(req: GameActionRequest):
    """
    处理玩家行动：
    1. 组装上下文（系统提示词 + 摘要记忆 + 最近 10 轮 + RAG 片段 + 角色状态）
    2. 调用 LLM 推理（含 Function Calling 判断是否需要骰子检定）
    3. 流式返回叙事 + 骰子结果 + 状态变更
    """
    # TODO: Phase 1 M1.4 实现
    return GameActionResponse(narrative="Not implemented yet")
