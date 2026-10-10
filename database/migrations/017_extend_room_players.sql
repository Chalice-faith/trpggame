-- M2.4 成员生命周期及有效角色唯一性；旧单人玩家默认仍为 active。

ALTER TABLE room_players
    ADD COLUMN status VARCHAR(16) NOT NULL DEFAULT 'active' AFTER is_ready,
    ADD COLUMN left_at DATETIME(3) NULL AFTER joined_at,
    ADD COLUMN active_character_id BIGINT UNSIGNED
        GENERATED ALWAYS AS (CASE WHEN status = 'active' THEN character_id ELSE NULL END) STORED,
    ADD UNIQUE KEY uk_room_players_active_character (room_id, active_character_id),
    ADD KEY idx_room_players_room_status_order (room_id, status, player_order, id),
    ADD KEY idx_room_players_user_status_room (user_id, status, room_id),
    ADD CONSTRAINT chk_room_players_status CHECK (status IN ('active', 'left', 'removed'));
