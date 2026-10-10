-- One ALTER keeps the old rows and replaces the deduplication scope together.
ALTER TABLE game_saves
    ADD COLUMN timeline_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
    ADD COLUMN memory_position BIGINT UNSIGNED NULL,
    ADD COLUMN auto_timeline_scope VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin
        GENERATED ALWAYS AS (CASE WHEN is_auto THEN COALESCE(timeline_id, 'legacy') ELSE NULL END) STORED,
    DROP INDEX idx_game_saves_auto_round,
    ADD UNIQUE KEY idx_game_saves_auto_timeline_round (room_id, auto_timeline_scope, auto_round_number),
    ADD KEY idx_game_saves_timeline (room_id, timeline_id, memory_position),
    ADD CONSTRAINT chk_game_save_memory_pair CHECK ((timeline_id IS NULL AND memory_position IS NULL) OR (timeline_id IS NOT NULL AND memory_position IS NOT NULL));
