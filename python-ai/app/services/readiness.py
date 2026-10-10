"""Dependency readiness with bounded probes; never invokes a paid model."""
import asyncio
from uuid import uuid4
from redis import asyncio as redis_asyncio
from minio import Minio
from urllib3 import PoolManager, Timeout
from app.config import settings
from app.services import embedder


async def readiness() -> dict:
    async def redis_probe():
        async with redis_asyncio.from_url(settings.redis_url, socket_connect_timeout=2, socket_timeout=2) as client:
            await client.ping()

    def minio_probe():
        client = Minio(settings.minio_endpoint, access_key=settings.minio_access_key,
                       secret_key=settings.minio_secret_key, secure=settings.minio_secure,
                       http_client=PoolManager(timeout=Timeout(connect=2, read=2), retries=False))
        if not client.bucket_exists(settings.minio_bucket):
            raise RuntimeError("missing bucket")

    def milvus_probe():
        from pymilvus import connections, utility
        alias = "readiness_" + uuid4().hex
        connections.connect(alias=alias, host=settings.milvus_host, port=settings.milvus_port, timeout=2)
        try:
            utility.list_collections(using=alias, timeout=2)
        finally:
            connections.disconnect(alias)

    async def check(name, probe):
        try:
            await asyncio.wait_for(probe(), timeout=3)
            return name, "ready"
        except Exception:
            return name, "unavailable"

    results = await asyncio.gather(check("redis", redis_probe),
        check("minio", lambda: asyncio.to_thread(minio_probe)),
        check("milvus", lambda: asyncio.to_thread(milvus_probe)))
    dependencies = dict(results)
    embedding = embedder.encoder_status()
    configured = bool(settings.deepseek_api_key.strip())
    ai_ready = settings.enabled and configured and embedding != "failed"
    ready = all(value == "ready" for value in dependencies.values()) and (not settings.enabled or ai_ready)
    if settings.embedding_prewarm and embedding != "ready":
        ready = False
    return {"status": "ready" if ready else "not_ready", "dependencies": dependencies,
        "capabilities": {"ai": "disabled" if not settings.enabled else "configured_unverified" if configured else "missing_key",
                         "embedding": embedding}}
