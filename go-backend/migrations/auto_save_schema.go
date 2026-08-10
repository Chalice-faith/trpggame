package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const autoSaveMigrationLock = "trpggame:migration:008"

const autoSaveColumnQuery = `
SELECT EXTRA, GENERATION_EXPRESSION
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'game_saves'
  AND COLUMN_NAME = 'auto_round_number'`

const autoSaveIndexQuery = `
SELECT NON_UNIQUE, SEQ_IN_INDEX, COLUMN_NAME
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'game_saves'
  AND INDEX_NAME = 'idx_game_saves_auto_round'
ORDER BY SEQ_IN_INDEX`

const addAutoSaveColumnSQL = `ALTER TABLE game_saves
ADD COLUMN auto_round_number INT UNSIGNED
GENERATED ALWAYS AS (CASE WHEN is_auto THEN round_number ELSE NULL END) STORED`

const addAutoSaveIndexSQL = `ALTER TABLE game_saves
ADD UNIQUE KEY idx_game_saves_auto_round (room_id, auto_round_number)`

type autoSaveSchemaState struct {
	columnExists bool
	indexExists  bool
}

// EnsureAutoSaveUniqueness 将 008 迁移幂等应用到已有 MySQL 数据卷。
// 所有检查和 DDL 都固定在持有 advisory lock 的同一条数据库连接上。
func EnsureAutoSaveUniqueness(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return errors.New("ensure automatic save schema: nil database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("ensure automatic save schema: access SQL database: %w", err)
	}
	connection, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("ensure automatic save schema: acquire connection: %w", err)
	}
	defer connection.Close()

	var locked int
	if err := connection.QueryRowContext(
		ctx,
		"SELECT GET_LOCK(?, 10)",
		autoSaveMigrationLock,
	).Scan(&locked); err != nil {
		return fmt.Errorf("ensure automatic save schema: acquire migration lock: %w", err)
	}
	if locked != 1 {
		return errors.New("ensure automatic save schema: migration lock unavailable")
	}
	defer func() {
		releaseContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		var released int
		_ = connection.QueryRowContext(
			releaseContext,
			"SELECT RELEASE_LOCK(?)",
			autoSaveMigrationLock,
		).Scan(&released)
	}()
	return ensureAutoSaveUniquenessOnConnection(ctx, connection)
}

func ensureAutoSaveUniquenessOnConnection(ctx context.Context, connection *sql.Conn) error {
	state, err := inspectAutoSaveSchema(ctx, connection)
	if err != nil {
		return err
	}
	if !state.columnExists {
		if _, err := connection.ExecContext(ctx, addAutoSaveColumnSQL); err != nil {
			return fmt.Errorf("ensure automatic save schema: add generated column: %w", err)
		}
	}
	if !state.indexExists {
		if _, err := connection.ExecContext(ctx, addAutoSaveIndexSQL); err != nil {
			return fmt.Errorf("ensure automatic save schema: add unique index: %w", err)
		}
	}
	return nil
}

func inspectAutoSaveSchema(
	ctx context.Context,
	connection *sql.Conn,
) (*autoSaveSchemaState, error) {
	state := &autoSaveSchemaState{}
	var extra string
	var expression sql.NullString
	err := connection.QueryRowContext(ctx, autoSaveColumnQuery).Scan(&extra, &expression)
	switch {
	case err == nil:
		state.columnExists = true
		normalizedExtra := strings.ToUpper(strings.TrimSpace(extra))
		normalizedExpression := strings.ToLower(strings.TrimSpace(expression.String))
		if !expression.Valid || !strings.Contains(normalizedExtra, "STORED GENERATED") ||
			!strings.Contains(normalizedExpression, "is_auto") ||
			!strings.Contains(normalizedExpression, "round_number") {
			return nil, errors.New("ensure automatic save schema: generated column has an incompatible definition")
		}
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("ensure automatic save schema: inspect generated column: %w", err)
	}

	rows, err := connection.QueryContext(ctx, autoSaveIndexQuery)
	if err != nil {
		return nil, fmt.Errorf("ensure automatic save schema: inspect unique index: %w", err)
	}
	defer rows.Close()
	type indexColumn struct {
		nonUnique int
		sequence  int
		name      string
	}
	columns := make([]indexColumn, 0, 2)
	for rows.Next() {
		var column indexColumn
		if err := rows.Scan(&column.nonUnique, &column.sequence, &column.name); err != nil {
			return nil, fmt.Errorf("ensure automatic save schema: decode unique index: %w", err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ensure automatic save schema: iterate unique index: %w", err)
	}
	if len(columns) > 0 {
		if len(columns) != 2 || columns[0].nonUnique != 0 || columns[0].sequence != 1 ||
			columns[0].name != "room_id" || columns[1].nonUnique != 0 ||
			columns[1].sequence != 2 || columns[1].name != "auto_round_number" {
			return nil, errors.New("ensure automatic save schema: unique index has an incompatible definition")
		}
		state.indexExists = true
	}
	if state.indexExists && !state.columnExists {
		return nil, errors.New("ensure automatic save schema: unique index exists without generated column")
	}
	return state, nil
}
