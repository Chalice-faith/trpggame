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
			file: "005_create_game_rooms.sql",
			required: []string{
				"CREATE TABLE IF NOT EXISTS game_rooms",
				"CHECK (status IN ('waiting', 'playing', 'paused', 'ended'))",
				"CHECK (JSON_TYPE(turn_order) = 'ARRAY')",
				"FOREIGN KEY (script_id) REFERENCES scripts(id)",
				"FOREIGN KEY (owner_id) REFERENCES users(id)",
			},
		},
		{
			file: "006_create_room_players.sql",
			required: []string{
				"CREATE TABLE IF NOT EXISTS room_players",
				"FOREIGN KEY (room_id) REFERENCES game_rooms(id) ON DELETE CASCADE",
				"FOREIGN KEY (character_id) REFERENCES script_characters(id)",
				"UNIQUE KEY idx_room_players_room_user (room_id, user_id)",
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
		})
	}
}
