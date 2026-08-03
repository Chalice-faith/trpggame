"""服务端真随机 D20/D100 骰子检定。"""

from __future__ import annotations

import secrets
from typing import TypedDict


DICE_SIDES = {
    "D20": 20,
    "D100": 100,
}


class DiceRollResult(TypedDict):
    type: str
    result: int
    target: int
    success: bool
    critical_hit: bool
    critical_miss: bool
    description: str


def roll_d20() -> int:
    """使用密码学安全随机源掷 D20，返回 1-20。"""

    return secrets.randbelow(DICE_SIDES["D20"]) + 1


def roll_d100() -> int:
    """使用密码学安全随机源掷 D100，返回 1-100。"""

    return secrets.randbelow(DICE_SIDES["D100"]) + 1


def check(dice_type: str, target: int) -> DiceRollResult:
    """执行阈值检定，并让大成功/大失败优先于普通结果。"""

    normalized_type = _validate_dice_type(dice_type)
    _validate_target(normalized_type, target)

    if normalized_type == "D20":
        result = roll_d20()
        critical_hit = result == 20
        critical_miss = result == 1
    else:
        result = roll_d100()
        critical_hit = result >= 96
        critical_miss = result <= 5

    success = critical_hit or (not critical_miss and result >= target)
    description = (
        f"{normalized_type} = {result} (目标 {target}) — "
        f"{'成功' if success else '失败'}"
    )
    if critical_hit:
        description += " [大成功!]"
    elif critical_miss:
        description += " [大失败!]"

    return {
        "type": normalized_type,
        "result": result,
        "target": target,
        "success": success,
        "critical_hit": critical_hit,
        "critical_miss": critical_miss,
        "description": description,
    }


def _validate_dice_type(dice_type: str) -> str:
    if not isinstance(dice_type, str):
        raise ValueError("dice_type must be D20 or D100")
    normalized = dice_type.strip().upper()
    if normalized not in DICE_SIDES:
        raise ValueError(f"unsupported dice type: {dice_type!r}")
    return normalized


def _validate_target(dice_type: str, target: int) -> None:
    if isinstance(target, bool) or not isinstance(target, int):
        raise ValueError("target must be an integer")
    maximum = DICE_SIDES[dice_type]
    if not 1 <= target <= maximum:
        raise ValueError(f"target must be between 1 and {maximum} for {dice_type}")
