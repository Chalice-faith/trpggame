-- 005_create_game_rooms.sql
-- 单人/多人游戏房间的持久化主表；MySQL 是房间生命周期的持久权威。

CREATE TABLE IF NOT EXISTS game_rooms (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name            VARCHAR(128) NOT NULL,
    script_id       BIGINT UNSIGNED NOT NULL,
    owner_id        BIGINT UNSIGNED NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'waiting'
                    CHECK (status IN ('waiting', 'playing', 'paused', 'ended')),
    max_players     INT UNSIGNED NOT NULL DEFAULT 1
                    CHECK (max_players BETWEEN 1 AND 20),
    current_turn    INT UNSIGNED NOT NULL DEFAULT 0,
    round_number    INT UNSIGNED NOT NULL DEFAULT 0,
    turn_order      JSON NOT NULL DEFAULT (JSON_ARRAY())
                    CHECK (JSON_TYPE(turn_order) = 'ARRAY'),
    is_solo         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    ended_at        DATETIME(3),
    PRIMARY KEY (id),
    CONSTRAINT fk_game_rooms_script
        FOREIGN KEY (script_id) REFERENCES scripts(id),
    CONSTRAINT fk_game_rooms_owner
        FOREIGN KEY (owner_id) REFERENCES users(id),
    KEY idx_game_rooms_status (status),
    KEY idx_game_rooms_owner_created (owner_id, created_at DESC),
    KEY idx_game_rooms_script (script_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
