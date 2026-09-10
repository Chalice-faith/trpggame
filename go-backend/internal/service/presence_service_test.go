package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"trpggame/internal/model"
)

type memoryPresenceLeases struct {
	mu     sync.Mutex
	values map[uint]string
	events []string
}

func newMemoryPresenceLeases() *memoryPresenceLeases {
	return &memoryPresenceLeases{values: map[uint]string{}}
}

func (r *memoryPresenceLeases) Claim(_ context.Context, userID uint, connectionID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, existed := r.values[userID]
	r.values[userID] = connectionID
	r.events = append(r.events, "claim:"+connectionID)
	return !existed, nil
}

func (r *memoryPresenceLeases) Refresh(_ context.Context, userID uint, connectionID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	matched := r.values[userID] == connectionID
	r.events = append(r.events, "refresh:"+connectionID)
	return matched, nil
}

func (r *memoryPresenceLeases) Release(_ context.Context, userID uint, connectionID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	matched := r.values[userID] == connectionID
	if matched {
		delete(r.values, userID)
	}
	r.events = append(r.events, "release:"+connectionID)
	return matched, nil
}

func (r *memoryPresenceLeases) Statuses(_ context.Context, ids []uint) (map[uint]bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[uint]bool, len(ids))
	for _, id := range ids {
		_, result[id] = r.values[id]
	}
	return result, nil
}

type staticAcceptedFriends struct{ ids []uint }

func (r staticAcceptedFriends) ListAcceptedPeerIDs(context.Context, uint) ([]uint, error) {
	return append([]uint(nil), r.ids...), nil
}

type mutableAcceptedFriends struct {
	mu  sync.Mutex
	ids []uint
}

func (r *mutableAcceptedFriends) ListAcceptedPeerIDs(context.Context, uint) ([]uint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint(nil), r.ids...), nil
}

func (r *mutableAcceptedFriends) set(ids ...uint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append([]uint(nil), ids...)
}

type publishedUserEvent struct {
	userID    uint
	eventType string
	data      any
}

type recordingUserPublisher struct {
	mu     sync.Mutex
	events []publishedUserEvent
}

func (p *recordingUserPublisher) PublishUserEvent(userID uint, eventType string, data any) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, publishedUserEvent{userID: userID, eventType: eventType, data: data})
	return true
}

func TestPresenceCoordinatorOrdersTakeoverWithoutOfflineFlicker(t *testing.T) {
	leases := newMemoryPresenceLeases()
	publisher := &recordingUserPublisher{}
	coordinator := NewPresenceCoordinator(leases, staticAcceptedFriends{ids: []uint{8, 9}}, publisher)
	go coordinator.Run()

	coordinator.OnConnected(7, "old")
	coordinator.OnConnected(7, "new")
	coordinator.OnRefreshed(7, "old")
	coordinator.OnDisconnected(7, "old")
	coordinator.OnRefreshed(7, "new")
	coordinator.OnDisconnected(7, "new")
	coordinator.Stop()

	wantLeaseEvents := []string{"claim:old", "claim:new", "refresh:old", "release:old", "refresh:new", "release:new"}
	if len(leases.events) != len(wantLeaseEvents) {
		t.Fatalf("lease events = %#v", leases.events)
	}
	for index := range wantLeaseEvents {
		if leases.events[index] != wantLeaseEvents[index] {
			t.Fatalf("lease events = %#v", leases.events)
		}
	}
	if len(publisher.events) != 4 {
		t.Fatalf("published events = %#v", publisher.events)
	}
	for index, event := range publisher.events {
		data, ok := event.data.(PresenceEventData)
		if !ok || event.eventType != "presence" || data.UserID != 7 {
			t.Fatalf("event %d = %#v", index, event)
		}
		wantStatus := PresenceOnline
		if index >= 2 {
			wantStatus = PresenceOffline
		}
		if data.Status != wantStatus {
			t.Fatalf("event %d status = %q", index, data.Status)
		}
	}
}

func TestRedisPresenceProviderMapsLeaseState(t *testing.T) {
	leases := newMemoryPresenceLeases()
	leases.values[7] = "connection"
	provider := NewRedisPresenceProvider(leases)
	statuses, err := provider.Statuses(context.Background(), []uint{7, 8})
	if err != nil || statuses[7] != PresenceOnline || statuses[8] != PresenceOffline {
		t.Fatalf("Statuses() = (%#v, %v)", statuses, err)
	}
}

func TestRealtimeFriendshipPublisherBuildsBothPeerViews(t *testing.T) {
	users := memoryFriendUsers{users: map[uint]model.User{
		7: {ID: 7, Username: "alice", Nickname: "Alice", Email: "private-a@example.test"},
		8: {ID: 8, Username: "bob", Nickname: "Bob", Email: "private-b@example.test"},
	}}
	transport := &recordingUserPublisher{}
	publisher := NewRealtimeFriendshipPublisher(users, transport)
	now := time.Now().UTC()
	err := publisher.PublishFriendshipUpdated(context.Background(), FriendshipUpdatedEvent{
		FriendshipID: 31, UserLowID: 7, UserHighID: 8, RequestedBy: 7,
		Status: model.FriendshipStatusAccepted, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.events) != 2 {
		t.Fatalf("events = %#v", transport.events)
	}
	first := transport.events[0]
	firstData, ok := first.data.(FriendshipEventData)
	if !ok || first.userID != 7 || first.eventType != "friendship_updated" || firstData.Peer.ID != 8 || firstData.RequestedBy != 7 {
		t.Fatalf("first event = %#v", first)
	}
	secondData := transport.events[1].data.(FriendshipEventData)
	if transport.events[1].userID != 8 || secondData.Peer.ID != 7 {
		t.Fatalf("second event = %#v", transport.events[1])
	}
}

func TestPresenceCoordinatorDrainsConcurrentLifecycleEvents(t *testing.T) {
	leases := newMemoryPresenceLeases()
	coordinator := NewPresenceCoordinator(leases, staticAcceptedFriends{}, &recordingUserPublisher{})
	go coordinator.Run()
	const users = 100
	var waitGroup sync.WaitGroup
	for index := range users {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			userID := uint(index + 1)
			connectionID := fmt.Sprintf("connection-%d", userID)
			coordinator.OnConnected(userID, connectionID)
			coordinator.OnRefreshed(userID, connectionID)
			coordinator.OnDisconnected(userID, connectionID)
		}()
	}
	waitGroup.Wait()
	coordinator.Stop()
	if len(leases.values) != 0 || len(leases.events) != users*3 {
		t.Fatalf("leases=%#v event count=%d", leases.values, len(leases.events))
	}
}

func TestPresenceCoordinatorRechecksAcceptedFriendsBeforeEveryBroadcast(t *testing.T) {
	leases := newMemoryPresenceLeases()
	friends := &mutableAcceptedFriends{ids: []uint{8}}
	publisher := &recordingUserPublisher{}
	coordinator := NewPresenceCoordinator(leases, friends, publisher)

	coordinator.process(presenceEvent{kind: presenceConnected, userID: 7, connectionID: "connection"})
	if len(publisher.events) != 1 {
		t.Fatalf("online events = %#v", publisher.events)
	}
	friends.set()
	coordinator.process(presenceEvent{kind: presenceDisconnected, userID: 7, connectionID: "connection"})
	if len(publisher.events) != 1 {
		t.Fatalf("removed friend received offline event: %#v", publisher.events)
	}
}
