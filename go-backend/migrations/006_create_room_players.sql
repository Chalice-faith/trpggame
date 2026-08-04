-- 006_create_room_players.sql
-- 房间与玩家/预设角色的关联；同一玩家在一个房间内只能出现一次。

CREATE TABLE IF NOT EXISTS room_players (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    room_id         BIGINT UNSIGNED NOT NULL,
    user_id         BIGINT UNSIGNED NOT NULL,
    character_id    BIGINT UNSIGNED,
    player_order    INT UNSIGNED NOT NULL DEFAULT 0,
    is_ready        BOOLEAN NOT NULL DEFAULT FALSE,
    joined_at       DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    CONSTRAINT fk_room_players_room
        FOREIGN KEY (room_id) REFERENCES game_rooms(id) ON DELETE CASCADE,
    CONSTRAINT fk_room_players_user
        FOREIGN KEY (user_id) REFERENCES users(id),
    CONSTRAINT fk_room_players_character
        FOREIGN KEY (character_id) REFERENCES script_characters(id),
    UNIQUE KEY idx_room_players_room_user (room_id, user_id),
    KEY idx_room_players_user (user_id),
    KEY idx_room_players_character (character_id),
    KEY idx_room_players_order (room_id, player_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
