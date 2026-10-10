ALTER TABLE game_saves
  ADD COLUMN summary_timeline_id CHAR(36) NULL,
  ADD COLUMN summary_version BIGINT UNSIGNED NULL,
  ADD COLUMN summary_through_position BIGINT UNSIGNED NULL,
  ADD COLUMN summary_input_hash CHAR(64) NULL,
  ADD COLUMN summary_content_hash CHAR(64) NULL;
