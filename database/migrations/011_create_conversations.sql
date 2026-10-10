-- 011_create_conversations.sql
-- M2.2 会话：私聊双方使用规范化用户 ID 保证唯一，消息序号按会话递增。

CREATE TABLE IF NOT EXISTS conversations (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    type                VARCHAR(16) NOT NULL,
    direct_low_id       BIGINT UNSIGNED,
    direct_high_id      BIGINT UNSIGNED,
    group_id            BIGINT UNSIGNED,
    last_seq            BIGINT UNSIGNED NOT NULL DEFAULT 0,
    last_message_at     DATETIME(3),
    created_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
                        ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_conversations_direct_pair (direct_low_id, direct_high_id),
    UNIQUE KEY uk_conversations_group (group_id),
    KEY idx_conversations_activity (last_message_at, id),
    CONSTRAINT chk_conversations_type CHECK (type IN ('direct', 'group')),
    CONSTRAINT chk_conversations_target CHECK (
        (type = 'direct' AND direct_low_id IS NOT NULL AND direct_high_id IS NOT NULL
         AND direct_low_id < direct_high_id AND group_id IS NULL)
        OR
        (type = 'group' AND direct_low_id IS NULL AND direct_high_id IS NULL
         AND group_id IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
