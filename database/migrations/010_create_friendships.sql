-- 010_create_friendships.sql
-- M2.1 好友关系：每一对用户只保留一行，方向由 requested_by 表示。

CREATE TABLE IF NOT EXISTS friendships (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_low_id     BIGINT UNSIGNED NOT NULL,
    user_high_id    BIGINT UNSIGNED NOT NULL,
    requested_by    BIGINT UNSIGNED NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'pending',
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
                    ON UPDATE CURRENT_TIMESTAMP(3),
    responded_at    DATETIME(3),
    removed_at      DATETIME(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_friendships_pair (user_low_id, user_high_id),
    KEY idx_friendships_low_status_id (user_low_id, status, id),
    KEY idx_friendships_high_status_id (user_high_id, status, id),
    CONSTRAINT chk_friendships_pair CHECK (user_low_id < user_high_id),
    CONSTRAINT chk_friendships_requester CHECK (requested_by IN (user_low_id, user_high_id)),
    CONSTRAINT chk_friendships_status CHECK (status IN ('pending', 'accepted', 'rejected', 'removed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
