-- 003_create_script_characters.sql
-- 剧本预设角色表

CREATE TABLE IF NOT EXISTS script_characters (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    script_id       BIGINT UNSIGNED NOT NULL,
    name            VARCHAR(100) NOT NULL,
    description     TEXT         NOT NULL DEFAULT (''),
    attributes      JSON         NOT NULL DEFAULT (JSON_OBJECT())
                    CHECK (JSON_TYPE(attributes) = 'OBJECT'),
    PRIMARY KEY (id),
    CONSTRAINT fk_script_characters_script
        FOREIGN KEY (script_id) REFERENCES scripts(id) ON DELETE CASCADE,
    KEY idx_script_characters_script (script_id),
    UNIQUE KEY idx_script_characters_script_name (script_id, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
