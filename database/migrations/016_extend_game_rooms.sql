-- M2.4 多人房间码、版本与回合时限；旧单人房间保持 room_code 为 NULL。

ALTER TABLE game_rooms
    ADD COLUMN room_code CHAR(8) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER is_solo,
    ADD COLUMN version BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER room_code,
    ADD COLUMN turn_timeout_seconds SMALLINT UNSIGNED NOT NULL DEFAULT 120 AFTER version,
    ADD UNIQUE KEY uk_game_rooms_room_code (room_code),
    ADD KEY idx_game_rooms_owner_status_id (owner_id, status, id),
    ADD CONSTRAINT chk_game_rooms_version CHECK (version > 0),
    ADD CONSTRAINT chk_game_rooms_turn_timeout CHECK (turn_timeout_seconds BETWEEN 30 AND 600),
    ADD CONSTRAINT chk_game_rooms_multiplayer_code CHECK (is_solo = TRUE OR room_code IS NOT NULL);
