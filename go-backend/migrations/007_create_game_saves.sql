-- 007_create_game_saves.sql
-- 游戏存档：保存 Redis 运行时状态快照、摘要和最近对话，供 MySQL 持久恢复。

CREATE TABLE IF NOT EXISTS game_saves (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    room_id         BIGINT UNSIGNED NOT NULL,
    save_name       VARCHAR(256) NOT NULL DEFAULT '',
    round_number    INT UNSIGNED NOT NULL DEFAULT 0,
    summary_memory  TEXT NOT NULL DEFAULT (''),
    redis_snapshot  JSON NOT NULL
                    CHECK (JSON_TYPE(redis_snapshot) = 'OBJECT'),
    recent_messages JSON NOT NULL
                    CHECK (JSON_TYPE(recent_messages) = 'ARRAY'),
    is_auto         BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    CONSTRAINT fk_game_saves_room
        FOREIGN KEY (room_id) REFERENCES game_rooms(id),
    KEY idx_game_saves_room_created (room_id, created_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
