-- M3.1-A: persistent capability state and immutable branch ancestry.
CREATE TABLE IF NOT EXISTS game_memory_states (
    room_id BIGINT UNSIGNED NOT NULL,
    active_timeline_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
    status VARCHAR(16) NOT NULL,
    revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
    active_operation_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (room_id),
    CONSTRAINT chk_game_memory_status CHECK (status IN ('initializing','ready','recovering','blocked','ended'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS game_timelines (
    id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    room_id BIGINT UNSIGNED NOT NULL,
    parent_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
    fork_position BIGINT UNSIGNED NOT NULL DEFAULT 0,
    durable_position BIGINT UNSIGNED NOT NULL DEFAULT 0,
    origin_save_id BIGINT UNSIGNED NULL,
    history_complete BOOLEAN NOT NULL,
    status VARCHAR(16) NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_game_timelines_room (room_id, id),
    CONSTRAINT chk_game_timeline_status CHECK (status IN ('prepared','active','aborted')),
    CONSTRAINT chk_game_timeline_watermark CHECK (durable_position >= fork_position),
    CONSTRAINT chk_game_timeline_root CHECK (parent_id IS NOT NULL OR fork_position = 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
