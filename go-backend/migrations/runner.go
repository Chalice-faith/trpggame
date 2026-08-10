package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const migrationLockName = "trpggame:migrations"

const createMigrationTableSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version VARCHAR(128) NOT NULL,
    checksum CHAR(64) NOT NULL,
    applied_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`

const listAppliedMigrationsSQL = `SELECT version, checksum FROM schema_migrations ORDER BY version`

const recordMigrationSQL = `INSERT INTO schema_migrations (version, checksum) VALUES (?, ?)`

const scriptChunkColumnQuery = `
SELECT DATA_TYPE, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'scripts'
  AND COLUMN_NAME = 'chunk_count'`

//go:embed *.sql
var migrationFiles embed.FS

var orderedMigrationNames = []string{
	"001_create_users.sql",
	"002_create_scripts.sql",
	"003_create_script_characters.sql",
	"004_add_script_chunk_count.sql",
	"005_create_game_rooms.sql",
	"006_create_room_players.sql",
	"007_create_game_saves.sql",
	"008_add_auto_save_uniqueness.sql",
}

// Apply 在单个 MySQL advisory lock 下按顺序应用并记录所有数据库迁移。
// 每个 DDL 都必须可在“DDL 已提交但迁移记录尚未写入”的崩溃恢复场景下安全重试。
func Apply(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return errors.New("apply migrations: nil database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("apply migrations: access SQL database: %w", err)
	}
	connection, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: acquire connection: %w", err)
	}
	defer connection.Close()

	var locked int
	if err := connection.QueryRowContext(ctx, "SELECT GET_LOCK(?, 10)", migrationLockName).Scan(&locked); err != nil {
		return fmt.Errorf("apply migrations: acquire lock: %w", err)
	}
	if locked != 1 {
		return errors.New("apply migrations: migration lock unavailable")
	}
	defer releaseMigrationLock(ctx, connection)

	if _, err := connection.ExecContext(ctx, createMigrationTableSQL); err != nil {
		return fmt.Errorf("apply migrations: create migration table: %w", err)
	}
	applied, err := readAppliedMigrations(ctx, connection)
	if err != nil {
		return err
	}
	for _, name := range orderedMigrationNames {
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			return fmt.Errorf("apply migrations: read %s: %w", name, err)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(body))
		if recorded, exists := applied[name]; exists {
			if !strings.EqualFold(recorded, checksum) {
				return fmt.Errorf("apply migrations: checksum mismatch for %s", name)
			}
			continue
		}
		if err := applyMigration(ctx, connection, name, string(body)); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, recordMigrationSQL, name, checksum); err != nil {
			return fmt.Errorf("apply migrations: record %s: %w", name, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, connection *sql.Conn, name, body string) error {
	var err error
	switch name {
	case "004_add_script_chunk_count.sql":
		err = ensureScriptChunkCount(ctx, connection, body)
	case "008_add_auto_save_uniqueness.sql":
		err = ensureAutoSaveUniquenessOnConnection(ctx, connection)
	default:
		_, err = connection.ExecContext(ctx, body)
	}
	if err != nil {
		return fmt.Errorf("apply migrations: execute %s: %w", name, err)
	}
	return nil
}

func ensureScriptChunkCount(ctx context.Context, connection *sql.Conn, migrationSQL string) error {
	var dataType, columnType, nullable string
	var defaultValue sql.NullString
	err := connection.QueryRowContext(ctx, scriptChunkColumnQuery).
		Scan(&dataType, &columnType, &nullable, &defaultValue)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = connection.ExecContext(ctx, migrationSQL)
		return err
	case err != nil:
		return fmt.Errorf("inspect scripts.chunk_count: %w", err)
	case !strings.EqualFold(dataType, "int") || !strings.EqualFold(columnType, "int unsigned") ||
		!strings.EqualFold(nullable, "NO") || !defaultValue.Valid || defaultValue.String != "0":
		return errors.New("scripts.chunk_count has an incompatible definition")
	default:
		return nil
	}
}

func readAppliedMigrations(ctx context.Context, connection *sql.Conn) (map[string]string, error) {
	rows, err := connection.QueryContext(ctx, listAppliedMigrationsSQL)
	if err != nil {
		return nil, fmt.Errorf("apply migrations: list applied migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[string]string)
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("apply migrations: decode migration record: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("apply migrations: iterate migration records: %w", err)
	}
	return applied, nil
}

func releaseMigrationLock(ctx context.Context, connection *sql.Conn) {
	releaseContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	var released int
	_ = connection.QueryRowContext(releaseContext, "SELECT RELEASE_LOCK(?)", migrationLockName).Scan(&released)
}
