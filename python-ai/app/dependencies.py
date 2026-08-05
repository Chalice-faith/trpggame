"""FastAPI 路由共享依赖。"""

from __future__ import annotations

import hashlib
import hmac

from fastapi import Header, HTTPException, status

from app.config import settings


def require_internal_secret(
    provided_secret: str | None = Header(
        default=None,
        alias="X-Internal-Secret",
    ),
) -> None:
    """使用常量时间摘要比较验证服务间共享密钥。"""

    expected_secret = settings.internal_shared_secret
    if not expected_secret:
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail="internal authentication is not configured",
        )

    provided_digest = hashlib.sha256((provided_secret or "").encode()).digest()
    expected_digest = hashlib.sha256(expected_secret.encode()).digest()
    if not hmac.compare_digest(provided_digest, expected_digest):
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="invalid internal credentials",
        )
