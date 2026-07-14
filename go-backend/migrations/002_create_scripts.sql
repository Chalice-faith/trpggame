-- 002_create_scripts.sql
-- 剧本主表

CREATE TABLE IF NOT EXISTS scripts (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT       NOT NULL REFERENCES users(id),
    title           VARCHAR(200) NOT NULL,
    description     TEXT         NOT NULL DEFAULT '',
    cover_url       VARCHAR(500) NOT NULL DEFAULT '',
    file_path       VARCHAR(500) NOT NULL,
    file_size       BIGINT       NOT NULL DEFAULT 0 CHECK (file_size >= 0),
    status          VARCHAR(20)  NOT NULL DEFAULT 'uploading'
                    CHECK (status IN ('uploading', 'parsing', 'ready', 'failed')),
    parse_error     TEXT         NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_scripts_user_created
    ON scripts(user_id, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_scripts_status
    ON scripts(status)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_scripts_deleted
    ON scripts(deleted_at);

CREATE TRIGGER trg_scripts_updated_at
    BEFORE UPDATE ON scripts
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();
