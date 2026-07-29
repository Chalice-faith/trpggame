"""应用配置管理 — 环境变量 + 默认值"""

from pydantic_settings import BaseSettings


class Settings(BaseSettings):
    # 服务配置
    app_name: str = "TRPG AI Service"
    app_version: str = "0.1.0"
    debug: bool = True
    host: str = "0.0.0.0"
    port: int = 8000

    # Milvus 向量数据库
    milvus_host: str = "localhost"
    milvus_port: int = 19530
    milvus_collection_name: str = "script_chunks"

    # Redis
    redis_url: str = "redis://localhost:6379/0"

    # GLM-4 大模型 API
    glm_api_key: str = ""
    glm_api_base: str = "https://open.bigmodel.cn/api/paas/v4"
    glm_model: str = "glm-4-long"

    # Embedding 模型
    embedding_model: str = "BAAI/bge-large-zh-v1.5"

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

    class Config:
        env_prefix = "TRPG_AI_"
        env_file = ".env"
        env_file_encoding = "utf-8"


settings = Settings()
