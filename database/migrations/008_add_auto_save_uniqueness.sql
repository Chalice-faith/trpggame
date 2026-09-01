-- 008_add_auto_save_uniqueness.sql
-- 仅自动存档按房间和回合去重；手动存档的生成列为 NULL，可在同一回合创建多份。
ALTER TABLE game_saves
    ADD COLUMN auto_round_number INT UNSIGNED
        GENERATED ALWAYS AS (CASE WHEN is_auto THEN round_number ELSE NULL END) STORED,
    ADD UNIQUE KEY idx_game_saves_auto_round (room_id, auto_round_number);
