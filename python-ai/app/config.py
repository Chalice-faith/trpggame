"""应用配置管理 — 环境变量 + 默认值"""

from pydantic_settings import BaseSettings
from pydantic import model_validator, Field
from urllib.parse import urlsplit, urlunsplit, quote


class Settings(BaseSettings):
    # 服务配置
    app_name: str = "TRPG AI Service"
    app_version: str = "0.1.0"
    debug: bool = True
    environment: str = "development"
    enabled: bool = True
    embedding_prewarm: bool = False
    host: str = "0.0.0.0"
    port: int = 8000

    # Milvus 向量数据库
    milvus_host: str = "localhost"
    milvus_port: int = 19530
    milvus_collection_name: str = "script_chunks"

    # Redis
    redis_url: str = "redis://localhost:6379/0"
    redis_password: str = ""

    # DeepSeek 大模型 API
    deepseek_api_key: str = ""
    deepseek_api_base: str = "https://api.deepseek.com"
    deepseek_model: str = "deepseek-v4-flash"

    # Embedding 模型
    embedding_model: str = "BAAI/bge-large-zh-v1.5"
    embedding_dimension: int = 1024
    embedding_batch_size: int = 32

    # MinIO (对象存储)
    minio_endpoint: str = "localhost:9000"
    minio_access_key: str = "minioadmin"
    minio_secret_key: str = "minioadmin"
    minio_bucket: str = "trpg-scripts"
    minio_secure: bool = False

    # Go 内部回调
    go_callback_base_url: str = "http://localhost:8080/api/v1/internal"
    internal_shared_secret: str = "dev-internal-secret-change-in-production"
    parse_task_timeout: int = 600

    # LLM 参数
    llm_temperature: float = 0.7
    llm_max_tokens: int = 4096
    llm_timeout: int = 120

    # RAG 参数
    rag_top_k: int = 20
    rag_mmr_top_n: int = 5

    # 摘要记忆参数
    summary_trigger_rounds: int = 5
    max_recent_rounds: int = 10
    context_input_budget: int = Field(default=24000, ge=4000, le=200000)
    context_key_event_budget: int = Field(default=2000, ge=0, le=20000)
    context_summary_budget: int = Field(default=3000, ge=500, le=20000)
    context_window_tokens: int = Field(default=32768, ge=8000)
    context_safety_tokens: int = Field(default=1024, ge=0)

    @model_validator(mode="after")
    def validate_environment(self):
        if self.redis_password:
            parts = urlsplit(self.redis_url)
            authority = f":{quote(self.redis_password, safe='')}@{parts.hostname}:{parts.port or 6379}"
            self.redis_url = urlunsplit((parts.scheme, authority, parts.path, parts.query, parts.fragment))
        if self.environment not in {"development", "test", "production"}:
            raise ValueError("invalid environment")
        if self.environment == "production":
            if self.debug:
                raise ValueError("production requires debug=false")
            for field, minimum in (("internal_shared_secret", 32), ("minio_secret_key", 16)):
                value = getattr(self, field)
                if len(value) < minimum or any(word in value.lower() for word in ("dev-", "replace-with", "change-in-production")):
                    raise ValueError(f"production requires a strong {field}")
            if self.minio_access_key in {"admin", "minioadmin"}:
                raise ValueError("production requires a MinIO business user")
            if self.enabled and not self.deepseek_api_key.strip():
                raise ValueError("enabled AI requires deepseek_api_key in production")
        return self

    class Config:
        env_prefix = "TRPG_AI_"
        env_file = ".env"
        env_file_encoding = "utf-8"


settings = Settings()
