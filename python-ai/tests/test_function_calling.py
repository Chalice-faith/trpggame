from __future__ import annotations

import unittest

from app.services.function_calling import (
    FUNCTIONS,
    FunctionCallExecutor,
    FunctionExecutionError,
    InvalidFunctionArguments,
    UnknownFunctionError,
    execute_function_call,
    validate_function_arguments,
)


EXPECTED_FUNCTIONS = {
    "update_player_status",
    "add_item",
    "remove_item",
    "add_buff",
    "set_location",
    "trigger_event",
    "roll_dice",
}


class FunctionCallingTests(unittest.IsolatedAsyncioTestCase):
    def test_definitions_expose_exactly_seven_unique_object_schemas(self):
        names = [definition["name"] for definition in FUNCTIONS]

        self.assertEqual(len(names), 7)
        self.assertEqual(set(names), EXPECTED_FUNCTIONS)
        self.assertEqual(len(names), len(set(names)))
        for definition in FUNCTIONS:
            with self.subTest(name=definition["name"]):
                self.assertTrue(definition["description"])
                self.assertEqual(definition["parameters"]["type"], "object")
                self.assertFalse(
                    definition["parameters"]["additionalProperties"]
                )

    def test_validation_parses_json_and_applies_defaults(self):
        arguments = validate_function_arguments(
            "add_item",
            '{"player_id": 9, "item_name": " 银钥匙 "}',
        )

        self.assertEqual(
            arguments,
            {
                "player_id": 9,
                "item_name": "银钥匙",
                "quantity": 1,
                "description": "",
            },
        )

    def test_validation_rejects_unknown_invalid_and_extra_arguments(self):
        with self.assertRaises(UnknownFunctionError):
            validate_function_arguments("delete_room", {})
        with self.assertRaisesRegex(InvalidFunctionArguments, "valid JSON"):
            validate_function_arguments("set_location", "not-json")
        with self.assertRaises(InvalidFunctionArguments):
            validate_function_arguments(
                "remove_item",
                {"player_id": 1, "item_name": "钥匙", "quantity": 0},
            )
        with self.assertRaises(InvalidFunctionArguments):
            validate_function_arguments(
                "set_location",
                {"player_id": 1, "location": "书房", "admin": True},
            )

    def test_roll_dice_validates_target_against_dice_type(self):
        with self.assertRaisesRegex(InvalidFunctionArguments, "must not exceed 20"):
            validate_function_arguments(
                "roll_dice",
                {"dice_type": "D20", "target": 21, "reason": "侦查"},
            )

    async def test_executor_uses_server_owned_dice_and_includes_reason(self):
        calls: list[tuple[str, int]] = []

        def fake_check(dice_type: str, target: int):
            calls.append((dice_type, target))
            return {"type": dice_type, "result": 17, "success": True}

        executor = FunctionCallExecutor(dice_checker=fake_check)
        result = await executor.execute(
            "roll_dice",
            {"dice_type": "D20", "target": 12, "reason": "侦查书房"},
        )

        self.assertEqual(calls, [("D20", 12)])
        self.assertTrue(result["success"])
        self.assertEqual(result["result"]["result"], 17)
        self.assertEqual(result["result"]["reason"], "侦查书房")

    async def test_executor_dispatches_normalized_arguments_to_async_handler(self):
        received: list[dict[str, object]] = []

        async def add_item(arguments: dict[str, object]):
            received.append(arguments)
            return {"stored": True}

        result = await execute_function_call(
            "add_item",
            {"player_id": 3, "item_name": "火把"},
            handlers={"add_item": add_item},
        )

        self.assertEqual(received[0]["quantity"], 1)
        self.assertEqual(result["result"], {"stored": True})

    async def test_executor_requires_handler_and_wraps_handler_failure(self):
        executor = FunctionCallExecutor()
        with self.assertRaisesRegex(FunctionExecutionError, "not configured"):
            await executor.execute(
                "set_location",
                {"player_id": 2, "location": "地下室"},
            )

        def fail(arguments: dict[str, object]):
            raise OSError("Redis unavailable")

        executor = FunctionCallExecutor(handlers={"set_location": fail})
        with self.assertRaisesRegex(FunctionExecutionError, "handler failed") as raised:
            await executor.execute(
                "set_location",
                {"player_id": 2, "location": "地下室"},
            )
        self.assertIsInstance(raised.exception.__cause__, OSError)


if __name__ == "__main__":
    unittest.main()
