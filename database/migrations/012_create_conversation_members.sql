-- 012_create_conversation_members.sql
-- M2.2 会话成员与用户自己的单调已读水位。

CREATE TABLE IF NOT EXISTS conversation_members (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    conversation_id     BIGINT UNSIGNED NOT NULL,
    user_id             BIGINT UNSIGNED NOT NULL,
    role                VARCHAR(16) NOT NULL DEFAULT 'member',
    status              VARCHAR(16) NOT NULL DEFAULT 'active',
    last_read_seq       BIGINT UNSIGNED NOT NULL DEFAULT 0,
    joined_at           DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    left_at             DATETIME(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_conversation_members_conversation_user (conversation_id, user_id),
    KEY idx_conversation_members_user_status_conversation (user_id, status, conversation_id),
    CONSTRAINT chk_conversation_members_role CHECK (role IN ('member', 'admin', 'owner')),
    CONSTRAINT chk_conversation_members_status CHECK (status IN ('active', 'left'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
