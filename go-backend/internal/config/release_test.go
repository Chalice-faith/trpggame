package config

import (
	"strings"
	"testing"
)

func TestReleaseRejectsDevelopmentSecrets(t *testing.T) {
	t.Setenv("TRPG_SERVER_MODE", "release")
	if _, err := Load(); err == nil {
		t.Fatal("release accepted development credentials")
	}
}
func TestReleaseLoadsExplicitCredentials(t *testing.T) {
	t.Setenv("TRPG_SERVER_MODE", "release")
	t.Setenv("TRPG_JWT_SECRET", strings.Repeat("J", 40))
	t.Setenv("TRPG_INTERNAL_SHARED_SECRET", strings.Repeat("I", 40))
	t.Setenv("TRPG_DATABASE_USER", "trpg")
	t.Setenv("TRPG_DATABASE_PASSWORD", strings.Repeat("D", 24))
	t.Setenv("TRPG_REDIS_PASSWORD", strings.Repeat("R", 24))
	t.Setenv("TRPG_MINIO_ACCESSKEY", "trpg-scripts-user")
	t.Setenv("TRPG_MINIO_SECRETKEY", strings.Repeat("M", 24))
	t.Setenv("TRPG_WEBSOCKET_ALLOWEDORIGINS", "https://game.example.com")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}
