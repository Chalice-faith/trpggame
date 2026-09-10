package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type memoryFriendRepo struct {
	mu     sync.Mutex
	nextID uint
	rows   map[[2]uint]model.Friendship
}

func newMemoryFriendRepo() *memoryFriendRepo {
	return &memoryFriendRepo{nextID: 1, rows: map[[2]uint]model.Friendship{}}
}

func (r *memoryFriendRepo) MutatePair(_ context.Context, low, high uint, initial *model.Friendship, mutate repo.FriendshipMutation) (*model.Friendship, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := [2]uint{low, high}
	row, exists := r.rows[key]
	if !exists {
		if initial == nil {
			return nil, false, gorm.ErrRecordNotFound
		}
		row = *initial
		row.ID = r.nextID
		r.nextID++
		row.CreatedAt = time.Now().UTC()
		row.UpdatedAt = row.CreatedAt
		r.rows[key] = row
		copy := row
		return &copy, true, nil
	}
	changed := false
	if mutate != nil {
		var err error
		changed, err = mutate(&row)
		if err != nil {
			return nil, false, err
		}
	}
	if changed {
		row.UpdatedAt = time.Now().UTC()
		r.rows[key] = row
	}
	copy := row
	return &copy, changed, nil
}

func (r *memoryFriendRepo) MutateByID(_ context.Context, id uint, mutate repo.FriendshipMutation) (*model.Friendship, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, row := range r.rows {
		if row.ID != id {
			continue
		}
		changed, err := mutate(&row)
		if err != nil {
			return nil, false, err
		}
		if changed {
			row.UpdatedAt = time.Now().UTC()
			r.rows[key] = row
		}
		copy := row
		return &copy, changed, nil
	}
	return nil, false, gorm.ErrRecordNotFound
}

func (r *memoryFriendRepo) ListRequests(_ context.Context, userID uint, incoming bool, status model.FriendshipStatus, cursor uint, limit int) ([]model.Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := make([]model.Friendship, 0)
	for _, row := range r.rows {
		matchesDirection := row.RequestedBy == userID
		if incoming {
			matchesDirection = row.Includes(userID) && row.RequestedBy != userID
		}
		if matchesDirection && row.Status == status && (cursor == 0 || row.ID < cursor) {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (r *memoryFriendRepo) ListFriends(_ context.Context, userID, cursor uint, limit int) ([]model.Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := make([]model.Friendship, 0)
	for _, row := range r.rows {
		if row.Includes(userID) && row.Status == model.FriendshipStatusAccepted && (cursor == 0 || row.ID < cursor) {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (r *memoryFriendRepo) FindRelationshipsWithPeers(_ context.Context, userID uint, peerIDs []uint) ([]model.Friendship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	peers := map[uint]bool{}
	for _, id := range peerIDs {
		peers[id] = true
	}
	rows := []model.Friendship{}
	for _, row := range r.rows {
		if row.Includes(userID) && peers[row.PeerID(userID)] {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

type memoryFriendUsers struct{ users map[uint]model.User }

func (r memoryFriendUsers) FindActiveByID(_ context.Context, id uint) (*model.User, error) {
	user, ok := r.users[id]
	if !ok || user.DeletedAt.Valid {
		return nil, gorm.ErrRecordNotFound
	}
	return &user, nil
}

func (r memoryFriendUsers) FindActiveByIDs(_ context.Context, ids []uint) ([]model.User, error) {
	users := make([]model.User, 0, len(ids))
	for _, id := range ids {
		if user, ok := r.users[id]; ok && !user.DeletedAt.Valid {
			users = append(users, user)
		}
	}
	return users, nil
}

func (r memoryFriendUsers) SearchActive(_ context.Context, currentUserID uint, keyword string) ([]model.User, error) {
	users := []model.User{}
	for _, user := range r.users {
		if user.ID != currentUserID && (user.Username == keyword || user.Nickname == keyword) {
			users = append(users, user)
		}
	}
	return users, nil
}

type failingPresence struct{}

func (failingPresence) Statuses(context.Context, []uint) (map[uint]PresenceStatus, error) {
	return nil, errors.New("redis unavailable")
}

func friendTestService(presence PresenceProvider) (*FriendService, *memoryFriendRepo) {
	repository := newMemoryFriendRepo()
	users := memoryFriendUsers{users: map[uint]model.User{
		1: {ID: 1, Username: "alice", Nickname: "Alice"},
		2: {ID: 2, Username: "bob", Nickname: "Bob"},
		3: {ID: 3, Username: "carol", Nickname: "Carol"},
	}}
	return NewFriendService(repository, users, presence, nil), repository
}

func TestFriendServiceReverseRequestAutoAcceptsAndRemainsIdempotent(t *testing.T) {
	svc, _ := friendTestService(nil)
	ctx := context.Background()
	first, err := svc.SendRequest(ctx, 1, 2)
	if err != nil || first.Status != model.FriendshipStatusPending || first.RequestedBy != 1 {
		t.Fatalf("first request = %#v, %v", first, err)
	}
	repeated, err := svc.SendRequest(ctx, 1, 2)
	if err != nil || repeated.ID != first.ID || repeated.Status != model.FriendshipStatusPending {
		t.Fatalf("repeated request = %#v, %v", repeated, err)
	}
	reverse, err := svc.SendRequest(ctx, 2, 1)
	if err != nil || reverse.ID != first.ID || reverse.Status != model.FriendshipStatusAccepted || reverse.RequestedBy != 1 {
		t.Fatalf("reverse request = %#v, %v", reverse, err)
	}
	idempotent, err := svc.RespondRequest(ctx, 2, first.ID, true)
	if err != nil || idempotent.Status != model.FriendshipStatusAccepted {
		t.Fatalf("idempotent accept = %#v, %v", idempotent, err)
	}
	acceptedRetry, err := svc.SendRequest(ctx, 1, 2)
	if err != nil || acceptedRetry.ID != first.ID || acceptedRetry.Status != model.FriendshipStatusAccepted {
		t.Fatalf("accepted send retry = %#v, %v", acceptedRetry, err)
	}
}

func TestFriendServiceResponsePermissionsAndConflicts(t *testing.T) {
	svc, _ := friendTestService(nil)
	ctx := context.Background()
	request, err := svc.SendRequest(ctx, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RespondRequest(ctx, 1, request.ID, true); !errors.Is(err, ErrFriendForbidden) {
		t.Fatalf("sender accept error = %v", err)
	}
	if _, err = svc.RespondRequest(ctx, 3, request.ID, true); !errors.Is(err, ErrFriendRequestMissing) {
		t.Fatalf("outsider accept error = %v", err)
	}
	rejected, err := svc.RespondRequest(ctx, 2, request.ID, false)
	if err != nil || rejected.Status != model.FriendshipStatusRejected {
		t.Fatalf("reject = %#v, %v", rejected, err)
	}
	if _, err = svc.RespondRequest(ctx, 2, request.ID, false); err != nil {
		t.Fatalf("repeat reject: %v", err)
	}
	if _, err = svc.RespondRequest(ctx, 2, request.ID, true); !errors.Is(err, ErrFriendStateConflict) {
		t.Fatalf("accept rejected error = %v", err)
	}
	reapplied, err := svc.SendRequest(ctx, 2, 1)
	if err != nil || reapplied.ID != request.ID || reapplied.Status != model.FriendshipStatusPending || reapplied.RequestedBy != 2 {
		t.Fatalf("reapply rejected = %#v, %v", reapplied, err)
	}
}

func TestFriendServiceDeleteAndReapplyReuseRelationship(t *testing.T) {
	svc, _ := friendTestService(nil)
	ctx := context.Background()
	request, _ := svc.SendRequest(ctx, 1, 2)
	accepted, _ := svc.RespondRequest(ctx, 2, request.ID, true)
	if err := svc.DeleteFriend(ctx, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteFriend(ctx, 2, 1); err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	reapplied, err := svc.SendRequest(ctx, 2, 1)
	if err != nil || reapplied.ID != accepted.ID || reapplied.Status != model.FriendshipStatusPending || reapplied.RequestedBy != 2 || reapplied.RespondedAt != nil {
		t.Fatalf("reapply = %#v, %v", reapplied, err)
	}
}

func TestFriendServiceListFriendsUsesUnknownWhenPresenceFails(t *testing.T) {
	svc, _ := friendTestService(failingPresence{})
	ctx := context.Background()
	request, _ := svc.SendRequest(ctx, 1, 2)
	_, _ = svc.RespondRequest(ctx, 2, request.ID, true)
	page, err := svc.ListFriends(ctx, 1, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Presence != PresenceUnknown || page.Items[0].Peer.ID != 2 {
		t.Fatalf("page = %#v", page)
	}
}

func TestFriendServiceRejectsSelfAndMissingTarget(t *testing.T) {
	svc, _ := friendTestService(nil)
	if _, err := svc.SendRequest(context.Background(), 1, 1); !errors.Is(err, ErrCannotFriendSelf) {
		t.Fatalf("self request error = %v", err)
	}
	if _, err := svc.SendRequest(context.Background(), 1, 99); !errors.Is(err, ErrFriendTargetNotFound) {
		t.Fatalf("missing target error = %v", err)
	}
}

func TestFriendServiceRejectsDeleteBeforeAcceptanceAndMissingRequest(t *testing.T) {
	svc, _ := friendTestService(nil)
	request, err := svc.SendRequest(context.Background(), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteFriend(context.Background(), 1, 2); !errors.Is(err, ErrFriendshipNotFound) {
		t.Fatalf("delete pending error = %v", err)
	}
	if _, err := svc.RespondRequest(context.Background(), 2, request.ID+99, true); !errors.Is(err, ErrFriendRequestMissing) {
		t.Fatalf("missing request error = %v", err)
	}
}

func TestFriendServiceValidatesSearchAndListQueries(t *testing.T) {
	svc, _ := friendTestService(nil)
	ctx := context.Background()
	if _, err := svc.SearchUsers(ctx, 1, "   "); !errors.Is(err, ErrInvalidFriendRequest) {
		t.Fatalf("empty search error = %v", err)
	}
	if _, err := svc.ListRequests(ctx, 1, "sideways", model.FriendshipStatusPending, 0, 20); !errors.Is(err, ErrInvalidFriendQuery) {
		t.Fatalf("direction error = %v", err)
	}
	if _, err := svc.ListRequests(ctx, 1, "incoming", model.FriendshipStatusAccepted, 0, 20); !errors.Is(err, ErrInvalidFriendQuery) {
		t.Fatalf("status error = %v", err)
	}
	if _, err := svc.ListFriends(ctx, 1, 0, 101); !errors.Is(err, ErrInvalidFriendQuery) {
		t.Fatalf("limit error = %v", err)
	}
}

func TestFriendServiceRequestCursorHasNoDuplicates(t *testing.T) {
	svc, _ := friendTestService(nil)
	ctx := context.Background()
	first, err := svc.SendRequest(ctx, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.SendRequest(ctx, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	page1, err := svc.ListRequests(ctx, 1, "incoming", model.FriendshipStatusPending, 0, 1)
	if err != nil || len(page1.Items) != 1 || page1.Items[0].ID != second.ID || page1.NextCursor == "" {
		t.Fatalf("page1 = %#v, %v", page1, err)
	}
	cursor, _ := strconv.ParseUint(page1.NextCursor, 10, 64)
	page2, err := svc.ListRequests(ctx, 1, "incoming", model.FriendshipStatusPending, uint(cursor), 1)
	if err != nil || len(page2.Items) != 1 || page2.Items[0].ID != first.ID || page2.Items[0].ID == page1.Items[0].ID {
		t.Fatalf("page2 = %#v, %v", page2, err)
	}
}

func TestFriendServiceSearchResponseDoesNotExposeEmail(t *testing.T) {
	svc, _ := friendTestService(nil)
	items, err := svc.SearchUsers(context.Background(), 1, "bob")
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, %v", items, err)
	}
	body, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "email") || strings.Contains(string(body), "password") {
		t.Fatalf("private fields leaked: %s", body)
	}
}
