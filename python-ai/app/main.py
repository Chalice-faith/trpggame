"""TRPG AI 服务 — FastAPI 入口"""

import asyncio
import logging
from contextlib import asynccontextmanager
from fastapi import FastAPI
from fastapi.responses import JSONResponse
from fastapi.middleware.cors import CORSMiddleware

from app.config import settings
from app.routers import inference, script, memory
from app.services.readiness import readiness
from app.services.embedder import _get_encoder


@asynccontextmanager
async def lifespan(app):
    if settings.embedding_prewarm:
        try:
            await asyncio.to_thread(_get_encoder)
        except Exception:
            logging.getLogger(__name__).error("embedding prewarm failed")
    yield


def create_app() -> FastAPI:
    app = FastAPI(
        title=settings.app_name,
        version=settings.app_version,
        debug=settings.debug,
        lifespan=lifespan,
    )

    # CORS 中间件
    app.add_middleware(
        CORSMiddleware,
        allow_origins=["*"],
        allow_credentials=True,
        allow_methods=["*"],
        allow_headers=["*"],
    )

    # 注册路由
    app.include_router(script.router, prefix="/api/v1/ai")
    app.include_router(inference.router, prefix="/api/v1/ai")
    app.include_router(memory.router, prefix="/api/v1/ai")

    @app.get("/ready")
    async def ready():
        result = await readiness()
        return JSONResponse(result, status_code=200 if result["status"] == "ready" else 503)

    @app.get("/health")
    async def health_check():
        return {"status": "ok", "service": settings.app_name, "version": settings.app_version}

    return app


app = create_app()


if __name__ == "__main__":
    import uvicorn
    uvicorn.run("app.main:app", host=settings.host, port=settings.port, reload=settings.debug)
