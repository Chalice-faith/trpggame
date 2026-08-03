"""TRPG Function Calling 参数契约与执行分发。"""

from __future__ import annotations

import inspect
import json
from collections.abc import Awaitable, Callable, Mapping
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError, model_validator

from app.services import dice


class FunctionCallError(RuntimeError):
    """Function Calling 基础异常。"""


class UnknownFunctionError(FunctionCallError):
    """LLM 请求了未开放的函数。"""


class InvalidFunctionArguments(FunctionCallError):
    """函数参数不是合法 JSON 或不符合契约。"""


class FunctionExecutionError(FunctionCallError):
    """函数处理器缺失、失败或返回非法结果。"""


class _Arguments(BaseModel):
    model_config = ConfigDict(extra="forbid", str_strip_whitespace=True)


class UpdatePlayerStatusArguments(_Arguments):
    player_id: int = Field(gt=0, description="玩家 ID")
    field: Literal["hp", "mp", "san", "ac", "level"]
    value: int
    reason: str = Field(default="", max_length=500)


class AddItemArguments(_Arguments):
    player_id: int = Field(gt=0, description="玩家 ID")
    item_name: str = Field(min_length=1, max_length=200)
    quantity: int = Field(default=1, gt=0)
    description: str = Field(default="", max_length=1_000)


class RemoveItemArguments(_Arguments):
    player_id: int = Field(gt=0, description="玩家 ID")
    item_name: str = Field(min_length=1, max_length=200)
    quantity: int = Field(default=1, gt=0)


class AddBuffArguments(_Arguments):
    player_id: int = Field(gt=0, description="玩家 ID")
    buff_name: str = Field(min_length=1, max_length=200)
    duration: int = Field(gt=0, description="持续回合数")


class SetLocationArguments(_Arguments):
    player_id: int = Field(gt=0, description="玩家 ID")
    location: str = Field(min_length=1, max_length=500)


class TriggerEventArguments(_Arguments):
    event_name: str = Field(min_length=1, max_length=200)
    description: str = Field(min_length=1, max_length=2_000)


class RollDiceArguments(_Arguments):
    dice_type: Literal["D20", "D100"]
    target: int = Field(gt=0)
    reason: str = Field(min_length=1, max_length=500)

    @model_validator(mode="after")
    def validate_target_range(self) -> "RollDiceArguments":
        maximum = 20 if self.dice_type == "D20" else 100
        if self.target > maximum:
            raise ValueError(f"target must not exceed {maximum} for {self.dice_type}")
        return self


_FunctionModel = type[_Arguments]
_FUNCTION_SPECS: dict[str, tuple[str, _FunctionModel]] = {
    "update_player_status": (
        "更新角色的 HP、MP、SAN、AC 或等级数值",
        UpdatePlayerStatusArguments,
    ),
    "add_item": ("向玩家背包添加道具", AddItemArguments),
    "remove_item": ("从玩家背包移除道具", RemoveItemArguments),
    "add_buff": ("向角色添加有持续回合数的增益或减益效果", AddBuffArguments),
    "set_location": ("更新角色当前位置", SetLocationArguments),
    "trigger_event": ("记录关键剧情事件", TriggerEventArguments),
    "roll_dice": ("执行服务端真随机 D20 或 D100 检定", RollDiceArguments),
}


FUNCTIONS = [
    {
        "name": name,
        "description": description,
        "parameters": model.model_json_schema(),
    }
    for name, (description, model) in _FUNCTION_SPECS.items()
]


FunctionHandler = Callable[
    [dict[str, Any]],
    Mapping[str, Any] | Awaitable[Mapping[str, Any]],
]
DiceChecker = Callable[[str, int], dict[str, Any]]


class FunctionCallExecutor:
    """校验 LLM 函数调用并分发到受控处理器。"""

    def __init__(
        self,
        *,
        handlers: Mapping[str, FunctionHandler] | None = None,
        dice_checker: DiceChecker = dice.check,
    ) -> None:
        self._handlers = dict(handlers or {})
        unknown_handlers = set(self._handlers) - set(_FUNCTION_SPECS)
        if unknown_handlers:
            names = ", ".join(sorted(unknown_handlers))
            raise ValueError(f"handlers contain unknown functions: {names}")
        if "roll_dice" in self._handlers:
            raise ValueError("roll_dice uses the server-owned dice checker")
        self._dice_checker = dice_checker

    async def execute(
        self,
        name: str,
        arguments: str | Mapping[str, Any],
    ) -> dict[str, Any]:
        """执行单次函数调用并返回统一结果。"""

        normalized = validate_function_arguments(name, arguments)
        if name == "roll_dice":
            result = self._execute_dice(normalized)
        else:
            result = await self._execute_handler(name, normalized)
        return {
            "name": name,
            "success": True,
            "arguments": normalized,
            "result": result,
        }

    def _execute_dice(self, arguments: dict[str, Any]) -> dict[str, Any]:
        try:
            result = self._dice_checker(
                arguments["dice_type"],
                arguments["target"],
            )
        except Exception as exc:
            raise FunctionExecutionError("roll_dice execution failed") from exc
        if not isinstance(result, dict):
            raise FunctionExecutionError("roll_dice must return an object")
        return {**result, "reason": arguments["reason"]}

    async def _execute_handler(
        self,
        name: str,
        arguments: dict[str, Any],
    ) -> dict[str, Any]:
        handler = self._handlers.get(name)
        if handler is None:
            raise FunctionExecutionError(f"handler is not configured for {name!r}")
        try:
            result = handler(arguments)
            if inspect.isawaitable(result):
                result = await result
        except Exception as exc:
            raise FunctionExecutionError(f"handler failed for {name!r}") from exc
        if not isinstance(result, Mapping):
            raise FunctionExecutionError(
                f"handler for {name!r} must return an object"
            )
        return dict(result)


def validate_function_arguments(
    name: str,
    arguments: str | Mapping[str, Any],
) -> dict[str, Any]:
    """解析并校验单次函数调用参数，返回应用默认值后的字典。"""

    specification = _FUNCTION_SPECS.get(name)
    if specification is None:
        raise UnknownFunctionError(f"unknown function: {name!r}")
    raw_arguments = _parse_arguments(arguments)
    model = specification[1]
    try:
        validated = model.model_validate(raw_arguments)
    except ValidationError as exc:
        raise InvalidFunctionArguments(
            f"invalid arguments for {name!r}: {exc.errors(include_url=False)}"
        ) from exc
    return validated.model_dump()


def _parse_arguments(arguments: str | Mapping[str, Any]) -> dict[str, Any]:
    if isinstance(arguments, str):
        try:
            parsed = json.loads(arguments)
        except json.JSONDecodeError as exc:
            raise InvalidFunctionArguments("function arguments are not valid JSON") from exc
    elif isinstance(arguments, Mapping):
        parsed = dict(arguments)
    else:
        raise InvalidFunctionArguments("function arguments must be JSON or an object")
    if not isinstance(parsed, dict):
        raise InvalidFunctionArguments("function arguments must be a JSON object")
    return parsed


async def execute_function_call(
    name: str,
    arguments: str | Mapping[str, Any],
    *,
    handlers: Mapping[str, FunctionHandler] | None = None,
) -> dict[str, Any]:
    """使用临时执行器完成一次函数调用。"""

    return await FunctionCallExecutor(handlers=handlers).execute(name, arguments)
