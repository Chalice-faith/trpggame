from __future__ import annotations

import unittest
from unittest.mock import patch

from app.services import dice


class DiceTests(unittest.TestCase):
    @patch("app.services.dice.secrets.randbelow")
    def test_roll_functions_use_secure_random_source_and_one_based_ranges(self, randbelow):
        randbelow.side_effect = [0, 19, 0, 99]

        self.assertEqual(dice.roll_d20(), 1)
        self.assertEqual(dice.roll_d20(), 20)
        self.assertEqual(dice.roll_d100(), 1)
        self.assertEqual(dice.roll_d100(), 100)
        self.assertEqual(
            randbelow.call_args_list,
            [
                unittest.mock.call(20),
                unittest.mock.call(20),
                unittest.mock.call(100),
                unittest.mock.call(100),
            ],
        )

    @patch("app.services.dice.roll_d20", return_value=15)
    def test_d20_normalizes_type_and_compares_target(self, roll_d20):
        result = dice.check(" d20 ", 10)

        self.assertTrue(result["success"])
        self.assertEqual(result["type"], "D20")
        self.assertEqual(result["result"], 15)
        self.assertIn("D20 = 15", result["description"])

    @patch("app.services.dice.roll_d20", return_value=20)
    def test_d20_natural_twenty_is_critical_success(self, roll_d20):
        result = dice.check("D20", 20)

        self.assertTrue(result["success"])
        self.assertTrue(result["critical_hit"])
        self.assertFalse(result["critical_miss"])
        self.assertIn("大成功", result["description"])

    @patch("app.services.dice.roll_d20", return_value=1)
    def test_d20_natural_one_overrides_low_target(self, roll_d20):
        result = dice.check("D20", 1)

        self.assertFalse(result["success"])
        self.assertTrue(result["critical_miss"])
        self.assertIn("大失败", result["description"])

    @patch("app.services.dice.roll_d100", return_value=96)
    def test_d100_high_critical_overrides_target(self, roll_d100):
        result = dice.check("D100", 100)

        self.assertTrue(result["success"])
        self.assertTrue(result["critical_hit"])

    @patch("app.services.dice.roll_d100", return_value=5)
    def test_d100_low_critical_overrides_target(self, roll_d100):
        result = dice.check("D100", 1)

        self.assertFalse(result["success"])
        self.assertTrue(result["critical_miss"])

    def test_rejects_unsupported_dice_type(self):
        for dice_type in ("D6", "", None):
            with self.subTest(dice_type=dice_type):
                with self.assertRaises(ValueError):
                    dice.check(dice_type, 1)  # type: ignore[arg-type]

    def test_rejects_target_outside_dice_range_or_non_integer(self):
        cases = [
            ("D20", 0),
            ("D20", 21),
            ("D100", 101),
            ("D100", True),
            ("D100", 10.5),
        ]
        for dice_type, target in cases:
            with self.subTest(dice_type=dice_type, target=target):
                with self.assertRaises(ValueError):
                    dice.check(dice_type, target)  # type: ignore[arg-type]


if __name__ == "__main__":
    unittest.main()
