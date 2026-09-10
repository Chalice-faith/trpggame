package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestPresenceRepoConnectionIDCASLifecycle(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repository, err := NewPresenceRepo(client, DefaultPresenceTTL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	becameOnline, err := repository.Claim(ctx, 7, "old-connection")
	if err != nil || !becameOnline {
		t.Fatalf("first Claim() = (%v, %v)", becameOnline, err)
	}
	if value, _ := server.Get(presenceKey(7)); value != "old-connection" {
		t.Fatalf("lease value = %q", value)
	}
	if ttl := server.TTL(presenceKey(7)); ttl != DefaultPresenceTTL {
		t.Fatalf("TTL = %s", ttl)
	}

	server.FastForward(20 * time.Second)
	becameOnline, err = repository.Claim(ctx, 7, "new-connection")
	if err != nil || becameOnline {
		t.Fatalf("replacement Claim() = (%v, %v)", becameOnline, err)
	}
	server.FastForward(10 * time.Second)
	if refreshed, err := repository.Refresh(ctx, 7, "old-connection"); err != nil || refreshed {
		t.Fatalf("old Refresh() = (%v, %v)", refreshed, err)
	}
	if released, err := repository.Release(ctx, 7, "old-connection"); err != nil || released {
		t.Fatalf("old Release() = (%v, %v)", released, err)
	}
	if value, _ := server.Get(presenceKey(7)); value != "new-connection" {
		t.Fatalf("old connection changed lease to %q", value)
	}

	if refreshed, err := repository.Refresh(ctx, 7, "new-connection"); err != nil || !refreshed {
		t.Fatalf("new Refresh() = (%v, %v)", refreshed, err)
	}
	if ttl := server.TTL(presenceKey(7)); ttl != DefaultPresenceTTL {
		t.Fatalf("refreshed TTL = %s", ttl)
	}
	if released, err := repository.Release(ctx, 7, "new-connection"); err != nil || !released {
		t.Fatalf("new Release() = (%v, %v)", released, err)
	}
	if server.Exists(presenceKey(7)) {
		t.Fatal("lease still exists after release")
	}
}

func TestPresenceRepoStatusesRespectExpiryAndRedisErrors(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	repository, _ := NewPresenceRepo(client, time.Second)
	ctx := context.Background()
	_, _ = repository.Claim(ctx, 7, "connection-7")

	statuses, err := repository.Statuses(ctx, []uint{7, 8})
	if err != nil || !statuses[7] || statuses[8] {
		t.Fatalf("Statuses() = (%#v, %v)", statuses, err)
	}
	server.FastForward(2 * time.Second)
	statuses, err = repository.Statuses(ctx, []uint{7})
	if err != nil || statuses[7] {
		t.Fatalf("expired Statuses() = (%#v, %v)", statuses, err)
	}
	server.Close()
	if _, err := repository.Statuses(ctx, []uint{7}); err == nil {
		t.Fatal("Statuses() succeeded after Redis closed")
	}
}

func TestNewPresenceRepoValidatesConfiguration(t *testing.T) {
	if _, err := NewPresenceRepo(nil, time.Second); !errors.Is(err, ErrInvalidPresenceLease) {
		t.Fatalf("nil client error = %v", err)
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	if _, err := NewPresenceRepo(client, 0); !errors.Is(err, ErrInvalidPresenceLease) {
		t.Fatalf("zero TTL error = %v", err)
	}
}
