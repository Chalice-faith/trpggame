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
	"009_remove_foreign_keys.sql",
}

// historicalMigrationChecksums 允许已部署的旧版本继续执行后续迁移。
// 这些值仅对应曾发布、且包含外键的历史迁移；其他校验差异仍会被拒绝。
var historicalMigrationChecksums = map[string]map[string]struct{}{
	"002_create_scripts.sql": {
		"d8c4359b82e1210cf3fe8df7d7c531ff2a1251f3dbfcade99eb102e41d8bebea": {},
		"2fe51a840f449d8861021eaff6ac8e904f254931baa2b90636460d60e7d5a2d5": {},
	},
	"003_create_script_characters.sql": {
		"43eda40d0432f45ae01e0750f7b3cda00e28a397e4c3228d13c2b89c450ce10f": {},
		"185af08bd3c2865bb8eae9dd15e71afb7ebfeec135511b4a7082c9b38741c37d": {},
	},
	"005_create_game_rooms.sql": {
		"12056c0e8daa915bc2f6f340c207b94c6cb842fbf5e11595b665f4bde1425cb1": {},
		"0c3d304d498053cea945ad98503a2cbfc208b25dc5ca692ad7f6a2490ca6457e": {},
	},
	"006_create_room_players.sql": {
		"6f156d21d69a4295dc6cee2340232b8e80fd71a6fba6bb1b3f9428c6cd5e0ec2": {},
		"be6c5690ea14e9e1f36132947446f95ce90a06bc23420ebc732ff0bb9b7284f4": {},
	},
	"007_create_game_saves.sql": {
		"6df3e168901e91936a3b7bb87137ad4f9958ce0992a064e35d6c6da900d343f":  {},
		"c7b7b5536ff432f631782b0957b9a786e9a4c2f253fc74418a6468f5bdf8020c": {},
	},
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
			if !strings.EqualFold(recorded, checksum) && !isHistoricalMigrationChecksum(name, recorded) {
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
	case "009_remove_foreign_keys.sql":
		err = removeLegacyForeignKeysOnConnection(ctx, connection)
	default:
		_, err = connection.ExecContext(ctx, body)
	}
	if err != nil {
		return fmt.Errorf("apply migrations: execute %s: %w", name, err)
	}
	return nil
}

func isHistoricalMigrationChecksum(name, checksum string) bool {
	acceptedChecksums, exists := historicalMigrationChecksums[name]
	if !exists {
		return false
	}
	_, exists = acceptedChecksums[strings.ToLower(checksum)]
	return exists
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
