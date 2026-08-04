package config

import (
	"context"
	"errors"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
)

func TestInitRedisConnectsConfiguredDatabase(t *testing.T) {
	server := miniredis.RunT(t)
	client, err := InitRedis(context.Background(), &RedisConfig{Addr: server.Addr(), DB: 2})
	if err != nil {
		t.Fatalf("InitRedis() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Set(context.Background(), "configured", "yes", 0).Err(); err != nil {
		t.Fatalf("set configured database: %v", err)
	}
	server.Select(2)
	if got, err := server.Get("configured"); err != nil || got != "yes" {
		t.Fatalf("configured database value = %q, error = %v", got, err)
	}
}

func TestInitRedisRejectsInvalidConfiguration(t *testing.T) {
	for _, cfg := range []*RedisConfig{nil, {}, {Addr: "localhost:6379", DB: -1}} {
		if _, err := InitRedis(context.Background(), cfg); !errors.Is(err, ErrInvalidRedisConfig) {
			t.Fatalf("config %#v error = %v", cfg, err)
		}
	}
}

func TestInitRedisReportsConnectionFailure(t *testing.T) {
	server := miniredis.RunT(t)
	address := server.Addr()
	server.Close()
	if _, err := InitRedis(context.Background(), &RedisConfig{Addr: address}); err == nil {
		t.Fatal("InitRedis() error = nil, want connection failure")
	}
}
