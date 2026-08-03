"""Read-only Redis access for the runtime context used by AI inference."""

from __future__ import annotations

import inspect
import json
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from typing import Any

from redis import asyncio as redis_asyncio

from app.config import settings


class GameContextError(RuntimeError):
    """Runtime game context is unavailable or malformed."""


@dataclass(frozen=True, slots=True)
class GameRuntimeContext:
    summary_memory: str
    recent_history: tuple[dict[str, str], ...]
    player_state: dict[str, Any]
    character_profile: dict[str, Any]


RedisFactory = Callable[..., Any]
_INTEGER_STATE_FIELDS = {"hp", "max_hp", "mp", "max_mp", "san", "ac", "level"}


class RedisGameContextProvider:
    """Load the documented room memory and player-state keys without mutation."""

    def __init__(
        self,
        *,
        redis_url: str | None = None,
        max_recent_rounds: int | None = None,
        redis_factory: RedisFactory = redis_asyncio.from_url,
    ) -> None:
        self._redis_url = settings.redis_url if redis_url is None else redis_url
        self._max_recent_rounds = (
            settings.max_recent_rounds
            if max_recent_rounds is None
            else max_recent_rounds
        )
        self._redis_factory = redis_factory
        if self._max_recent_rounds <= 0:
            raise ValueError("max_recent_rounds must be positive")

    async def load(
        self,
        room_id: int,
        user_id: int,
        character_id: int,
    ) -> GameRuntimeContext:
        client: Any | None = None
        try:
            client = self._redis_factory(self._redis_url, decode_responses=True)
            summary = await client.get(f"room:{room_id}:summary")
            raw_rounds = await client.lrange(
                f"room:{room_id}:rounds",
                0,
                self._max_recent_rounds - 1,
            )
            raw_player_state = await client.hgetall(
                f"room:{room_id}:player:{user_id}"
            )
            return GameRuntimeContext(
                summary_memory=self._parse_summary(summary),
                recent_history=self._parse_rounds(raw_rounds),
                player_state=self._parse_player_state(raw_player_state),
                character_profile={"character_id": character_id},
            )
        except GameContextError:
            raise
        except Exception as exc:
            raise GameContextError("failed to load game context from Redis") from exc
        finally:
            close = getattr(client, "aclose", None) if client is not None else None
            if close is not None:
                closed = close()
                if inspect.isawaitable(closed):
                    await closed

    @staticmethod
    def _parse_summary(value: Any) -> str:
        if value is None:
            return ""
        if not isinstance(value, str):
            raise GameContextError("room summary must be text")
        return value.strip()

    @staticmethod
    def _parse_rounds(value: Any) -> tuple[dict[str, str], ...]:
        if not isinstance(value, (list, tuple)):
            raise GameContextError("room rounds must be a list")
        messages: list[dict[str, str]] = []
        for index, raw_message in enumerate(reversed(value)):
            if not isinstance(raw_message, str):
                raise GameContextError(f"room round {index} must be JSON text")
            try:
                message = json.loads(raw_message)
            except json.JSONDecodeError as exc:
                raise GameContextError(f"room round {index} is invalid JSON") from exc
            if not isinstance(message, dict):
                raise GameContextError(f"room round {index} must be an object")
            role = message.get("role")
            content = message.get("content")
            if not isinstance(role, str) or not role.strip():
                raise GameContextError(f"room round {index} has invalid role")
            if not isinstance(content, str) or not content.strip():
                raise GameContextError(f"room round {index} has invalid content")
            messages.append({"role": role.strip(), "content": content.strip()})
        return tuple(messages)

    @staticmethod
    def _parse_player_state(value: Any) -> dict[str, Any]:
        if not isinstance(value, Mapping):
            raise GameContextError("player state must be a hash")
        state: dict[str, Any] = {}
        for key, raw_value in value.items():
            if not isinstance(key, str) or not isinstance(raw_value, str):
                raise GameContextError("player state fields must be text")
            if key in _INTEGER_STATE_FIELDS:
                try:
                    state[key] = int(raw_value)
                except ValueError as exc:
                    raise GameContextError(
                        f"player state field {key!r} must be an integer"
                    ) from exc
            else:
                state[key] = raw_value
        return state
