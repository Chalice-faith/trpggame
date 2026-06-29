"""Function Calling 定义 + 执行器 — 7 个核心函数"""

# 7 个 Function Calling 函数定义（供 LLM 调用）
FUNCTIONS = [
    {
        "name": "roll_dice",
        "description": "执行骰子检定（D20/D100），用于技能检定、攻击命中、属性判定等场景",
        "parameters": {
            "type": "object",
            "properties": {
                "dice_type": {"type": "string", "enum": ["D20", "D100"], "description": "骰子类型"},
                "target": {"type": "integer", "description": "目标值，不低于此值判定成功"},
                "reason": {"type": "string", "description": "检定原因，用于叙事中描述"}
            },
            "required": ["dice_type", "target", "reason"]
        }
    },
    {
        "name": "update_status",
        "description": "更新角色状态（HP/MP/SAN/属性值变化）",
        "parameters": {
            "type": "object",
            "properties": {
                "player_id": {"type": "integer", "description": "玩家 ID"},
                "changes": {
                    "type": "object",
                    "description": "变更的属性和值，如 {\"hp\": -5, \"mp\": -10}",
                    "additionalProperties": True
                }
            },
            "required": ["player_id", "changes"]
        }
    },
    {
        "name": "add_item",
        "description": "向玩家背包添加道具",
        "parameters": {
            "type": "object",
            "properties": {
                "player_id": {"type": "integer", "description": "玩家 ID"},
                "item_name": {"type": "string", "description": "道具名称"},
                "quantity": {"type": "integer", "default": 1, "description": "数量"}
            },
            "required": ["player_id", "item_name"]
        }
    },
    {
        "name": "remove_item",
        "description": "从玩家背包移除道具",
        "parameters": {
            "type": "object",
            "properties": {
                "player_id": {"type": "integer", "description": "玩家 ID"},
                "item_name": {"type": "string", "description": "道具名称"},
                "quantity": {"type": "integer", "default": 1, "description": "数量"}
            },
            "required": ["player_id", "item_name"]
        }
    },
    {
        "name": "add_buff",
        "description": "向角色添加增益/减益效果",
        "parameters": {
            "type": "object",
            "properties": {
                "player_id": {"type": "integer", "description": "玩家 ID"},
                "buff_name": {"type": "string", "description": "效果名称"},
                "duration_rounds": {"type": "integer", "description": "持续回合数"}
            },
            "required": ["player_id", "buff_name", "duration_rounds"]
        }
    },
    {
        "name": "trigger_event",
        "description": "触发关键剧情事件",
        "parameters": {
            "type": "object",
            "properties": {
                "event_name": {"type": "string", "description": "事件名称"},
                "description": {"type": "string", "description": "事件简述"}
            },
            "required": ["event_name", "description"]
        }
    },
    {
        "name": "save_checkpoint",
        "description": "自动存档检查点",
        "parameters": {
            "type": "object",
            "properties": {
                "reason": {"type": "string", "description": "存档原因"}
            },
            "required": ["reason"]
        }
    }
]


def execute_function_call(name: str, arguments: dict) -> dict:
    """
    执行 LLM 返回的 Function Call。

    Args:
        name: 函数名
        arguments: 函数参数

    Returns:
        执行结果字典
    """
    # TODO: Phase 1 M1.4 实现函数执行逻辑
    raise NotImplementedError(f"Function '{name}' not implemented yet")
