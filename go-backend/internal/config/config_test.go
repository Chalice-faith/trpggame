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
