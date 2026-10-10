-- 013_create_messages.sql
-- M2.2 持久化消息：conversation seq 排序，client_message_id 保证发送幂等。

CREATE TABLE IF NOT EXISTS messages (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    conversation_id     BIGINT UNSIGNED NOT NULL,
    seq                 BIGINT UNSIGNED NOT NULL,
    sender_id           BIGINT UNSIGNED NOT NULL,
    client_message_id   CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    message_type        VARCHAR(20) NOT NULL,
    content             TEXT NOT NULL,
    metadata            JSON NOT NULL DEFAULT (JSON_OBJECT()),
    created_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_messages_conversation_seq (conversation_id, seq),
    UNIQUE KEY uk_messages_client_id (conversation_id, sender_id, client_message_id),
    CONSTRAINT chk_messages_seq CHECK (seq > 0),
    CONSTRAINT chk_messages_type CHECK (message_type IN ('text', 'system')),
    CONSTRAINT chk_messages_metadata_object CHECK (JSON_TYPE(metadata) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
