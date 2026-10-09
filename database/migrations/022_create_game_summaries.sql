CREATE TABLE IF NOT EXISTS game_summaries (
  timeline_id CHAR(36) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  room_id BIGINT UNSIGNED NOT NULL,
  through_position BIGINT UNSIGNED NOT NULL,
  content TEXT NOT NULL,
  source_hash CHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (timeline_id, version),
  KEY idx_summary_source (room_id, timeline_id, through_position)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
