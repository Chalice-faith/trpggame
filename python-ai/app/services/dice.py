"""骰子服务 — 服务端真随机 D20/D100"""

import secrets


def roll_d20() -> int:
    """掷 D20（1-20），使用 secrets 模块真随机"""
    return secrets.randbelow(20) + 1


def roll_d100() -> int:
    """掷 D100（1-100），使用 secrets 模块真随机"""
    return secrets.randbelow(100) + 1


def check(dice_type: str, target: int = 0) -> dict:
    """
    执行骰子检定。

    Args:
        dice_type: "D20" 或 "D100"
        target: 目标值，结果 >= target 判定为成功

    Returns:
        {
            "type": "D20",
            "result": 15,
            "target": 10,
            "success": True,
            "critical_hit": False,
            "critical_miss": False,
            "description": "D20 = 15 (目标 10) — 成功"
        }
    """
    result: int
    if dice_type.upper() == "D20":
        result = roll_d20()
        critical_hit = result == 20
        critical_miss = result == 1
    elif dice_type.upper() == "D100":
        result = roll_d100()
        critical_hit = result >= 96
        critical_miss = result <= 5
    else:
        raise ValueError(f"Unsupported dice type: {dice_type}")

    success = result >= target
    description = f"{dice_type} = {result} (目标 {target}) — {'成功' if success else '失败'}"

    if critical_hit:
        description += " [大成功!]"
    elif critical_miss:
        description += " [大失败!]"

    return {
        "type": dice_type.upper(),
        "result": result,
        "target": target,
        "success": success,
        "critical_hit": critical_hit,
        "critical_miss": critical_miss,
        "description": description,
    }
