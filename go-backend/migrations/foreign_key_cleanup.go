package migrations

import (
	"context"
	"database/sql"
	"fmt"
)

const foreignKeyExistsQuery = `
SELECT COUNT(*)
FROM information_schema.TABLE_CONSTRAINTS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = ?
  AND CONSTRAINT_NAME = ?
  AND CONSTRAINT_TYPE = 'FOREIGN KEY'`

const indexExistsQuery = `
SELECT COUNT(*)
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = ?
  AND INDEX_NAME = ?`

type schemaObject struct {
	table string
	name  string
}

var legacyForeignKeys = []schemaObject{
	{table: "scripts", name: "fk_scripts_user"},
	{table: "script_characters", name: "fk_script_characters_script"},
	{table: "game_rooms", name: "fk_game_rooms_script"},
	{table: "game_rooms", name: "fk_game_rooms_owner"},
	{table: "room_players", name: "fk_room_players_room"},
	{table: "room_players", name: "fk_room_players_user"},
	{table: "room_players", name: "fk_room_players_character"},
	{table: "game_saves", name: "fk_game_saves_room"},
}

var legacyRelationshipIndexes = []schemaObject{
	{table: "script_characters", name: "idx_script_characters_script"},
	{table: "game_rooms", name: "idx_game_rooms_owner_created"},
	{table: "game_rooms", name: "idx_game_rooms_script"},
	{table: "room_players", name: "idx_room_players_user"},
	{table: "room_players", name: "idx_room_players_character"},
}

func removeLegacyForeignKeysOnConnection(ctx context.Context, connection *sql.Conn) error {
	for _, foreignKey := range legacyForeignKeys {
		exists, err := schemaObjectExists(ctx, connection, foreignKeyExistsQuery, foreignKey)
		if err != nil {
			return fmt.Errorf("inspect foreign key %s.%s: %w", foreignKey.table, foreignKey.name, err)
		}
		if !exists {
			continue
		}
		if _, err := connection.ExecContext(ctx, dropForeignKeySQL(foreignKey)); err != nil {
			return fmt.Errorf("drop foreign key %s.%s: %w", foreignKey.table, foreignKey.name, err)
		}
	}

	for _, index := range legacyRelationshipIndexes {
		exists, err := schemaObjectExists(ctx, connection, indexExistsQuery, index)
		if err != nil {
			return fmt.Errorf("inspect relationship index %s.%s: %w", index.table, index.name, err)
		}
		if !exists {
			continue
		}
		if _, err := connection.ExecContext(ctx, dropIndexSQL(index)); err != nil {
			return fmt.Errorf("drop relationship index %s.%s: %w", index.table, index.name, err)
		}
	}
	return nil
}

func schemaObjectExists(ctx context.Context, connection *sql.Conn, query string, object schemaObject) (bool, error) {
	var count int
	if err := connection.QueryRowContext(ctx, query, object.table, object.name).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func dropForeignKeySQL(foreignKey schemaObject) string {
	return fmt.Sprintf("ALTER TABLE `%s` DROP FOREIGN KEY `%s`", foreignKey.table, foreignKey.name)
}

func dropIndexSQL(index schemaObject) string {
	return fmt.Sprintf("ALTER TABLE `%s` DROP INDEX `%s`", index.table, index.name)
}
