-- 004_add_script_chunk_count.sql
-- 保存 Python 剧本解析管线最终写入的向量片段数量。

ALTER TABLE scripts
    ADD COLUMN chunk_count INT UNSIGNED NOT NULL DEFAULT 0
    AFTER parse_error;
