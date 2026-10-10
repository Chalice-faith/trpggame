package config

import "testing"

func TestGameArchiveConfigEnvironmentAndBounds(t *testing.T) {
	t.Setenv("TRPG_GAME_ARCHIVE_BATCH_SIZE", "17")
	t.Setenv("TRPG_GAME_ARCHIVE_LEASE_MS", "15000")
	c, err := Load()
	if err != nil || c.GameArchive.BatchSize != 17 || c.GameArchive.LeaseMS != 15000 {
		t.Fatalf("config: %#v %v", c, err)
	}
	t.Setenv("TRPG_GAME_ARCHIVE_LEASE_MS", "999")
	if _, err = Load(); err == nil {
		t.Fatal("invalid lease accepted")
	}
}

func TestGameMemoryCreationFlagDefaultsAndEnvironment(t *testing.T) {
	c, err := Load()
	if err != nil || c.GameMemory.NewRoomsEnabled {
		t.Fatalf("creation enabled by default: %#v %v", c, err)
	}
	t.Setenv("TRPG_GAME_MEMORY_NEW_ROOMS_ENABLED", "true")
	c, err = Load()
	if err != nil || !c.GameMemory.NewRoomsEnabled {
		t.Fatalf("creation flag: %#v %v", c, err)
	}
}
