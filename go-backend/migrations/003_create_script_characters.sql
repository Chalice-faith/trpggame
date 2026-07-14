-- 003_create_script_characters.sql
-- 剧本预设角色表

CREATE TABLE IF NOT EXISTS script_characters (
    id              BIGSERIAL PRIMARY KEY,
    script_id       BIGINT       NOT NULL REFERENCES scripts(id) ON DELETE CASCADE,
    name            VARCHAR(100) NOT NULL,
    description     TEXT         NOT NULL DEFAULT '',
    attributes      JSONB        NOT NULL DEFAULT '{}'::jsonb
                    CHECK (jsonb_typeof(attributes) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_script_characters_script
    ON script_characters(script_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_script_characters_script_name
    ON script_characters(script_id, name);
