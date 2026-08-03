"""AI 开场叙事与玩家行动推理 API。"""

from __future__ import annotations

import json
import logging
from collections.abc import Awaitable, Callable, Mapping, Sequence
from dataclasses import dataclass
from typing import Any, Protocol

from fastapi import APIRouter, Depends, HTTPException, status
from pydantic import BaseModel, Field, field_validator

from app.dependencies import require_internal_secret
from app.services.context_builder import AssembledContext, assemble_context
from app.services.function_calling import (
    FUNCTIONS,
    FunctionCallError,
    FunctionCallExecutor,
    FunctionHandler,
)
from app.services.game_context import (
    GameContextError,
    GameRuntimeContext,
    RedisGameContextProvider,
)
from app.services.llm_client import (
    ChatCompletion,
    LLMClientError,
    chat,
    complete,
)
from app.services.retriever import RetrievalError, retrieve


logger = logging.getLogger(__name__)
router = APIRouter()

OPENING_RETRIEVAL_QUERY = "剧本开场背景、初始场景、玩家登场方式和最先发生的事件"
OPENING_ACTION_PROMPT = (
    "请生成游戏开场叙事：介绍玩家当前可感知的场景与氛围，给出明确但开放的行动入口，"
    "不要提前揭示秘密或替玩家作出决定。"
)


class StartGameRequest(BaseModel):
    room_id: int = Field(gt=0)
    script_id: int = Field(gt=0)
    character_id: int = Field(gt=0)
    user_id: int = Field(gt=0)


class NarrativeResponse(BaseModel):
    narrative: str


class OpeningNarrativeError(RuntimeError):
    """开场叙事编排失败。"""


Retriever = Callable[[str, int], Awaitable[list[str]]]
ContextBuilder = Callable[..., AssembledContext]
NarrativeGenerator = Callable[[str, str], Awaitable[str]]


class OpeningNarrativeService:
    """串联剧本检索、上下文组装和完整叙事生成。"""

    def __init__(
        self,
        *,
        retriever: Retriever = retrieve,
        context_builder: ContextBuilder = assemble_context,
        generator: NarrativeGenerator = chat,
    ) -> None:
        self._retriever = retriever
        self._context_builder = context_builder
        self._generator = generator

    async def generate(self, request: StartGameRequest) -> str:
        try:
            rag_chunks = await self._retriever(
                OPENING_RETRIEVAL_QUERY,
                request.script_id,
            )
            if not rag_chunks:
                raise OpeningNarrativeError(
                    "script has no retrievable opening context"
                )
            context = self._context_builder(
                OPENING_ACTION_PROMPT,
                rag_chunks=rag_chunks,
                character_profile={"character_id": request.character_id},
                player_state={
                    "room_id": request.room_id,
                    "user_id": request.user_id,
                },
            )
            narrative = await self._generator(
                context.user_prompt,
                context.system_prompt,
            )
        except OpeningNarrativeError:
            raise
        except RetrievalError as exc:
            raise OpeningNarrativeError("script retrieval failed") from exc
        except LLMClientError as exc:
            raise OpeningNarrativeError("LLM generation failed") from exc
        except Exception as exc:
            raise OpeningNarrativeError("opening narrative generation failed") from exc

        narrative = narrative.strip()
        if not narrative:
            raise OpeningNarrativeError("LLM returned an empty narrative")
        return narrative


def get_opening_narrative_service() -> OpeningNarrativeService:
    return OpeningNarrativeService()


@router.post(
    "/inference/start",
    response_model=NarrativeResponse,
    dependencies=[Depends(require_internal_secret)],
)
async def start_game(
    request: StartGameRequest,
    service: OpeningNarrativeService = Depends(get_opening_narrative_service),
) -> NarrativeResponse:
    """检索剧本开场上下文并生成开场叙事。"""

    try:
        narrative = await service.generate(request)
    except OpeningNarrativeError as exc:
        logger.exception(
            "opening narrative failed for room=%s script=%s",
            request.room_id,
            request.script_id,
        )
        raise HTTPException(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            detail="opening narrative generation unavailable",
        ) from exc
    return NarrativeResponse(narrative=narrative)


class GameActionRequest(BaseModel):
    room_id: int = Field(gt=0)
    user_id: int = Field(gt=0)
    action: str = Field(min_length=1, max_length=2_000)
    script_id: int = Field(gt=0)
    character_id: int = Field(gt=0)

    @field_validator("action")
    @classmethod
    def normalize_action(cls, value: str) -> str:
        value = value.strip()
        if not value:
            raise ValueError("action must not be empty")
        return value


class DiceRollData(BaseModel):
    type: str
    result: int
    target: int
    success: bool
    critical_hit: bool
    critical_miss: bool
    description: str
    reason: str


class GameActionResponse(BaseModel):
    narrative: str
    dice_roll: DiceRollData | None = None
    status_changes: dict[str, Any] | None = None


class ActionInferenceError(RuntimeError):
    """The player-action inference pipeline could not produce a safe result."""


class GameContextProvider(Protocol):
    async def load(
        self,
        room_id: int,
        user_id: int,
        character_id: int,
    ) -> GameRuntimeContext: ...


CompletionGenerator = Callable[
    [str, str, Sequence[Mapping[str, Any]]],
    Awaitable[ChatCompletion],
]
ExecutorFactory = Callable[[Mapping[str, FunctionHandler]], FunctionCallExecutor]


@dataclass(frozen=True, slots=True)
class ActionInferenceResult:
    narrative: str
    dice_roll: dict[str, Any] | None = None
    status_changes: dict[str, Any] | None = None


class ActionEffectCollector:
    """Collect validated state changes for the later authoritative state layer."""

    _FUNCTION_NAMES = (
        "update_player_status",
        "add_item",
        "remove_item",
        "add_buff",
        "set_location",
        "trigger_event",
    )

    def __init__(self, user_id: int) -> None:
        self._user_id = user_id
        self.calls: list[dict[str, Any]] = []

    def handlers(self) -> dict[str, FunctionHandler]:
        return {
            name: self._make_handler(name)
            for name in self._FUNCTION_NAMES
        }

    def _make_handler(self, name: str) -> FunctionHandler:
        def collect(arguments: dict[str, Any]) -> dict[str, bool]:
            player_id = arguments.get("player_id")
            if player_id is not None and player_id != self._user_id:
                raise ValueError("function call targets another player")
            self.calls.append({"name": name, "arguments": dict(arguments)})
            return {"accepted": True}

        return collect


def _create_executor(
    handlers: Mapping[str, FunctionHandler],
) -> FunctionCallExecutor:
    return FunctionCallExecutor(handlers=handlers)


class ActionInferenceService:
    """Orchestrate RAG, Redis context, tool execution, and final narration."""

    MAX_TOOL_CALLS = 8

    def __init__(
        self,
        *,
        retriever: Retriever = retrieve,
        context_provider: GameContextProvider | None = None,
        context_builder: ContextBuilder = assemble_context,
        completion_generator: CompletionGenerator = complete,
        narrative_generator: NarrativeGenerator = chat,
        executor_factory: ExecutorFactory = _create_executor,
    ) -> None:
        self._retriever = retriever
        self._context_provider = context_provider or RedisGameContextProvider()
        self._context_builder = context_builder
        self._completion_generator = completion_generator
        self._narrative_generator = narrative_generator
        self._executor_factory = executor_factory

    async def infer(self, request: GameActionRequest) -> ActionInferenceResult:
        try:
            rag_chunks = await self._retriever(request.action, request.script_id)
            if not rag_chunks:
                raise ActionInferenceError("script has no retrievable action context")
            runtime = await self._context_provider.load(
                request.room_id,
                request.user_id,
                request.character_id,
            )
            context = self._context_builder(
                request.action,
                rag_chunks=rag_chunks,
                summary_memory=runtime.summary_memory,
                recent_history=runtime.recent_history,
                player_state=runtime.player_state,
                character_profile=runtime.character_profile,
            )
            completion_result = await self._completion_generator(
                context.user_prompt,
                context.system_prompt,
                FUNCTIONS,
            )
            return await self._resolve_completion(
                request,
                context,
                completion_result,
            )
        except ActionInferenceError:
            raise
        except RetrievalError as exc:
            raise ActionInferenceError("script retrieval failed") from exc
        except GameContextError as exc:
            raise ActionInferenceError("game context unavailable") from exc
        except LLMClientError as exc:
            raise ActionInferenceError("LLM generation failed") from exc
        except FunctionCallError as exc:
            raise ActionInferenceError("function call execution failed") from exc
        except Exception as exc:
            raise ActionInferenceError("player action inference failed") from exc

    async def _resolve_completion(
        self,
        request: GameActionRequest,
        context: AssembledContext,
        completion_result: ChatCompletion,
    ) -> ActionInferenceResult:
        tool_calls = completion_result.tool_calls
        if len(tool_calls) > self.MAX_TOOL_CALLS:
            raise ActionInferenceError("LLM requested too many function calls")
        if not tool_calls:
            narrative = completion_result.content.strip()
            if not narrative:
                raise ActionInferenceError("LLM returned an empty narrative")
            return ActionInferenceResult(narrative=narrative)

        collector = ActionEffectCollector(request.user_id)
        executor = self._executor_factory(collector.handlers())
        tool_results: list[dict[str, Any]] = []
        dice_roll: dict[str, Any] | None = None
        for tool_call in tool_calls:
            execution = await executor.execute(tool_call.name, tool_call.arguments)
            tool_results.append({"id": tool_call.id, **execution})
            if tool_call.name == "roll_dice":
                if dice_roll is not None:
                    raise ActionInferenceError("multiple dice rolls are not supported")
                dice_roll = dict(execution["result"])

        narrative_prompt = self._build_result_prompt(
            context.user_prompt,
            tool_results,
        )
        narrative = (
            await self._narrative_generator(
                narrative_prompt,
                context.system_prompt,
            )
        ).strip()
        if not narrative:
            raise ActionInferenceError("LLM returned an empty final narrative")
        status_changes = {"calls": collector.calls} if collector.calls else None
        return ActionInferenceResult(
            narrative=narrative,
            dice_roll=dice_roll,
            status_changes=status_changes,
        )

    @staticmethod
    def _build_result_prompt(
        original_prompt: str,
        tool_results: Sequence[Mapping[str, Any]],
    ) -> str:
        serialized_results = json.dumps(
            list(tool_results),
            ensure_ascii=False,
            separators=(",", ":"),
        )
        return (
            f"{original_prompt}\n\n"
            "以下是服务端已经校验并执行的权威结果（JSON）：\n"
            f"{serialized_results}\n\n"
            "请严格依据这些结果生成最终叙事，不得改变骰子结果或虚构额外状态变更。"
        )


def get_action_inference_service() -> ActionInferenceService:
    return ActionInferenceService()


@router.post(
    "/inference/action",
    response_model=GameActionResponse,
    dependencies=[Depends(require_internal_secret)],
)
async def process_action(
    request: GameActionRequest,
    service: ActionInferenceService = Depends(get_action_inference_service),
) -> GameActionResponse:
    """Process one player action while keeping random outcomes server-owned."""

    try:
        result = await service.infer(request)
    except ActionInferenceError as exc:
        logger.exception(
            "action inference failed for room=%s user=%s script=%s",
            request.room_id,
            request.user_id,
            request.script_id,
        )
        raise HTTPException(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            detail="player action inference unavailable",
        ) from exc
    return GameActionResponse(
        narrative=result.narrative,
        dice_roll=result.dice_roll,
        status_changes=result.status_changes,
    )
