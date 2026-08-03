from __future__ import annotations

import unittest

from app.services.game_context import GameContextError, RedisGameContextProvider


class FakeRedis:
    def __init__(
        self,
        *,
        summary: object = "",
        rounds: object = None,
        player_state: object = None,
        error: Exception | None = None,
    ) -> None:
        self.summary = summary
        self.rounds = [] if rounds is None else rounds
        self.player_state = {} if player_state is None else player_state
        self.error = error
        self.calls: list[tuple[object, ...]] = []
        self.closed = False

    async def get(self, key: str):
        self.calls.append(("get", key))
        if self.error is not None:
            raise self.error
        return self.summary

    async def lrange(self, key: str, start: int, end: int):
        self.calls.append(("lrange", key, start, end))
        return self.rounds

    async def hgetall(self, key: str):
        self.calls.append(("hgetall", key))
        return self.player_state

    async def aclose(self) -> None:
        self.closed = True


class RedisGameContextProviderTests(unittest.IsolatedAsyncioTestCase):
    async def test_loads_documented_keys_and_restores_chronological_order(self):
        redis = FakeRedis(
            summary=" 已经进入古宅 ",
            rounds=[
                '{"role":"assistant","content":"门自行关闭。"}',
                '{"role":"user","content":"我走进门厅。"}',
            ],
            player_state={"hp": "18", "location": "门厅"},
        )
        factory_calls: list[tuple[str, bool]] = []

        def factory(url: str, *, decode_responses: bool):
            factory_calls.append((url, decode_responses))
            return redis

        provider = RedisGameContextProvider(
            redis_url="redis://example/1",
            max_recent_rounds=10,
            redis_factory=factory,
        )
        result = await provider.load(room_id=7, user_id=11, character_id=13)

        self.assertEqual(factory_calls, [("redis://example/1", True)])
        self.assertEqual(
            redis.calls,
            [
                ("get", "room:7:summary"),
                ("lrange", "room:7:rounds", 0, 9),
                ("hgetall", "room:7:player:11"),
            ],
        )
        self.assertEqual(result.summary_memory, "已经进入古宅")
        self.assertEqual(result.recent_history[0]["role"], "user")
        self.assertEqual(result.recent_history[1]["role"], "assistant")
        self.assertEqual(result.player_state, {"hp": 18, "location": "门厅"})
        self.assertEqual(result.character_profile, {"character_id": 13})
        self.assertTrue(redis.closed)

    async def test_rejects_malformed_round_and_closes_client(self):
        redis = FakeRedis(rounds=["not-json"])
        provider = RedisGameContextProvider(redis_factory=lambda *args, **kwargs: redis)

        with self.assertRaisesRegex(GameContextError, "invalid JSON"):
            await provider.load(1, 2, 3)

        self.assertTrue(redis.closed)

    async def test_wraps_redis_failure_and_closes_client(self):
        redis = FakeRedis(error=OSError("Redis unavailable"))
        provider = RedisGameContextProvider(redis_factory=lambda *args, **kwargs: redis)

        with self.assertRaisesRegex(GameContextError, "failed to load") as raised:
            await provider.load(1, 2, 3)

        self.assertIsInstance(raised.exception.__cause__, OSError)
        self.assertTrue(redis.closed)

    async def test_wraps_client_creation_failure(self):
        def fail_factory(*args, **kwargs):
            raise OSError("invalid Redis endpoint")

        provider = RedisGameContextProvider(redis_factory=fail_factory)

        with self.assertRaisesRegex(GameContextError, "failed to load") as raised:
            await provider.load(1, 2, 3)

        self.assertIsInstance(raised.exception.__cause__, OSError)


if __name__ == "__main__":
    unittest.main()
