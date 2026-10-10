-- 014_create_groups.sql
-- M2.3 群组主表：群主与乐观并发版本由业务事务维护。

CREATE TABLE IF NOT EXISTS `groups` (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name                VARCHAR(80) NOT NULL,
    avatar_url          VARCHAR(2048) NOT NULL DEFAULT '',
    owner_id            BIGINT UNSIGNED NOT NULL,
    version             BIGINT UNSIGNED NOT NULL DEFAULT 1,
    created_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
                        ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_groups_owner_id (owner_id, id),
    CONSTRAINT chk_groups_name CHECK (CHAR_LENGTH(name) BETWEEN 1 AND 80),
    CONSTRAINT chk_groups_version CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
