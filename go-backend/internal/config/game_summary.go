package config

import "fmt"

type GameSummaryConfig struct {
	Enabled        bool `mapstructure:"enabled"`
	TriggerActions int  `mapstructure:"trigger_actions"`
	PollIntervalMS int  `mapstructure:"poll_interval_ms"`
	TimeoutSeconds int  `mapstructure:"timeout_seconds"`
	LeaseSeconds   int  `mapstructure:"lease_seconds"`
}

func (c GameSummaryConfig) Validate() error {
	if c.TriggerActions < 1 || c.TriggerActions > 50 || c.PollIntervalMS < 100 || c.PollIntervalMS > 60000 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 300 || c.LeaseSeconds <= c.TimeoutSeconds || c.LeaseSeconds > 600 {
		return fmt.Errorf("invalid game_summary worker bounds")
	}
	return nil
}
