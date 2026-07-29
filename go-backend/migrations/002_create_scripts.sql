-- 002_create_scripts.sql
-- 剧本主表

CREATE TABLE IF NOT EXISTS scripts (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id         BIGINT UNSIGNED NOT NULL,
    title           VARCHAR(200) NOT NULL,
    description     TEXT         NOT NULL DEFAULT (''),
    cover_url       VARCHAR(500) NOT NULL DEFAULT '',
    file_path       VARCHAR(500) NOT NULL,
    file_size       BIGINT       NOT NULL DEFAULT 0 CHECK (file_size >= 0),
    status          VARCHAR(20)  NOT NULL DEFAULT 'uploading'
                    CHECK (status IN ('uploading', 'parsing', 'ready', 'failed')),
    parse_error     TEXT         NOT NULL DEFAULT (''),
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
                    ON UPDATE CURRENT_TIMESTAMP(3),
    deleted_at      DATETIME(3),
    PRIMARY KEY (id),
    CONSTRAINT fk_scripts_user FOREIGN KEY (user_id) REFERENCES users(id),
    KEY idx_scripts_user_created (user_id, created_at DESC),
    KEY idx_scripts_status (status),
    KEY idx_scripts_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
