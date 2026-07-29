package config

import "testing"

func TestLoadUsesUnderscoreEnvironmentVariables(t *testing.T) {
	t.Setenv("TRPG_AI_TIMEOUT", "321")
	t.Setenv("TRPG_MINIO_MAXUPLOADSIZE", "1024")
	t.Setenv("TRPG_INTERNAL_SHARED_SECRET", "test-internal-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.AI.Timeout != 321 {
		t.Fatalf("AI.Timeout = %d, want 321", cfg.AI.Timeout)
	}
	if cfg.MinIO.MaxUploadSize != 1024 {
		t.Fatalf("MinIO.MaxUploadSize = %d, want 1024", cfg.MinIO.MaxUploadSize)
	}
	if cfg.Internal.SharedSecret != "test-internal-secret" {
		t.Fatalf("Internal.SharedSecret = %q, want %q", cfg.Internal.SharedSecret, "test-internal-secret")
	}
}

func TestDatabaseDSNUsesMySQLFormat(t *testing.T) {
	cfg := DatabaseConfig{
		Host:     "mysql",
		Port:     "3306",
		User:     "trpg",
		Password: "secret",
		DBName:   "trpggame",
		Charset:  "utf8mb4",
		Loc:      "Local",
	}

	got := cfg.DSN()
	want := "trpg:secret@tcp(mysql:3306)/trpggame?charset=utf8mb4&parseTime=True&loc=Local"
	if got != want {
		t.Fatalf("DSN() = %q, want %q", got, want)
	}
}
