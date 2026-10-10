-- M3.1-B: immutable events derived only from archived, committed actions.
CREATE TABLE IF NOT EXISTS key_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    room_id BIGINT UNSIGNED NOT NULL,
    timeline_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    position BIGINT UNSIGNED NOT NULL,
    source_commit_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    event_index SMALLINT UNSIGNED NOT NULL,
    event_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    importance VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    name VARCHAR(200) NOT NULL,
    description VARCHAR(2000) NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_key_events_source_index (source_commit_id, event_index),
    KEY idx_key_events_branch (room_id, timeline_id, position, event_index),
    CONSTRAINT chk_key_event_type CHECK (event_type IN ('trigger_event','character_death')),
    CONSTRAINT chk_key_event_importance CHECK (importance IN ('major','critical'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
