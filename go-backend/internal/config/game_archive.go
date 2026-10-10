package config

import "fmt"

// GameArchiveConfig controls recovery of already enabled rooms, independently of
// any future new-room enable switch. All time values are milliseconds.
type GameArchiveConfig struct {
	PollIntervalMS     int `mapstructure:"poll_interval_ms"`
	BatchSize          int `mapstructure:"batch_size"`
	OperationTimeoutMS int `mapstructure:"operation_timeout_ms"`
	LeaseMS            int `mapstructure:"lease_ms"`
	RetryBaseMS        int `mapstructure:"retry_base_ms"`
	RetryMaxMS         int `mapstructure:"retry_max_ms"`
	ShutdownTimeoutMS  int `mapstructure:"shutdown_timeout_ms"`
}

func DefaultGameArchiveConfig() GameArchiveConfig {
	return GameArchiveConfig{1000, 32, 5000, 30000, 1000, 30000, 7000}
}

func (c GameArchiveConfig) Validate() error {
	if c.PollIntervalMS < 10 || c.PollIntervalMS > 60000 || c.BatchSize < 1 || c.BatchSize > 256 ||
		c.OperationTimeoutMS < 100 || c.OperationTimeoutMS > 60000 || c.LeaseMS < 1000 || c.LeaseMS > 300000 ||
		c.RetryBaseMS < 10 || c.RetryBaseMS > 60000 || c.RetryMaxMS < c.RetryBaseMS || c.RetryMaxMS > 300000 ||
		c.ShutdownTimeoutMS < 100 || c.ShutdownTimeoutMS > 60000 {
		return fmt.Errorf("invalid game_archive worker bounds")
	}
	return nil
}
