package migrations_test

import (
	"os"
	"strings"
	"testing"
)

func TestM15MigrationsExposeRequiredContracts(t *testing.T) {
	tests := []struct {
		file     string
		required []string
	}{
		{
			file: "002_create_scripts.sql",
			required: []string{
				"CREATE TABLE IF NOT EXISTS scripts",
				"KEY idx_scripts_user_created (user_id, created_at DESC)",
			},
		},
		{
			file: "003_create_script_characters.sql",
			required: []string{
				"CREATE TABLE IF NOT EXISTS script_characters",
				"UNIQUE KEY idx_script_characters_script_name (script_id, name)",
			},
		},
		{
			file: "005_create_game_rooms.sql",
			required: []string{
				"CREATE TABLE IF NOT EXISTS game_rooms",
				"CHECK (status IN ('waiting', 'playing', 'paused', 'ended'))",
				"CHECK (JSON_TYPE(turn_order) = 'ARRAY')",
				"KEY idx_game_rooms_status (status)",
			},
		},
		{
			file: "006_create_room_players.sql",
			required: []string{
				"CREATE TABLE IF NOT EXISTS room_players",
				"UNIQUE KEY idx_room_players_room_user (room_id, user_id)",
				"KEY idx_room_players_order (room_id, player_order)",
			},
		},
		{
			file: "007_create_game_saves.sql",
			required: []string{
				"CREATE TABLE IF NOT EXISTS game_saves",
				"CHECK (JSON_TYPE(redis_snapshot) = 'OBJECT')",
				"CHECK (JSON_TYPE(recent_messages) = 'ARRAY')",
				"KEY idx_game_saves_room_created (room_id, created_at DESC)",
			},
		},
		{
			file: "008_add_auto_save_uniqueness.sql",
			required: []string{
				"GENERATED ALWAYS AS (CASE WHEN is_auto THEN round_number ELSE NULL END) STORED",
				"UNIQUE KEY idx_game_saves_auto_round (room_id, auto_round_number)",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			content, err := os.ReadFile(test.file)
			if err != nil {
				t.Fatalf("read migration: %v", err)
			}
			sql := string(content)
			for _, required := range test.required {
				if !strings.Contains(sql, required) {
					t.Errorf("migration is missing %q", required)
				}
			}
			for _, postgresOnly := range []string{" JSONB", " SERIAL", "TIMESTAMPTZ"} {
				if strings.Contains(strings.ToUpper(sql), postgresOnly) {
					t.Errorf("migration contains PostgreSQL-only token %q", postgresOnly)
				}
			}
			for _, forbidden := range []string{"FOREIGN KEY", "REFERENCES"} {
				if strings.Contains(strings.ToUpper(sql), forbidden) {
					t.Errorf("migration contains database foreign-key token %q", forbidden)
				}
			}
		})
	}
}

func TestMigrationSQLDoesNotCreateForeignKeys(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read migrations directory: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		content, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read migration %s: %v", entry.Name(), err)
		}
		upperSQL := strings.ToUpper(string(content))
		for _, forbidden := range []string{"FOREIGN KEY", "REFERENCES"} {
			if strings.Contains(upperSQL, forbidden) {
				t.Errorf("migration %s contains database foreign-key token %q", entry.Name(), forbidden)
			}
		}
	}
}
