package repo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"trpggame/internal/model"
)

func TestRedisGameStateRepoInitializeSoloRoom(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repository, err := NewRedisGameStateRepo(client, DefaultGameRuntimeTTL)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}

	ctx := context.Background()
	staleKeys := soloRuntimeCleanupKeys(41, 7)
	for _, key := range staleKeys {
		server.Set(key, "stale")
	}
	err = repository.InitializeSoloRoom(ctx, &model.SoloRuntimeState{
		RoomID:  41,
		UserID:  7,
		Status:  model.RoomStatusPlaying,
		Turn:    0,
		Summary: "  ",
		PlayerState: map[string]string{
			"max_hp": "10",
			"hp":     "10",
			"san":    "60",
		},
		Opening: model.RuntimeMessage{Role: " assistant ", Content: " 古宅的大门缓缓打开。 "},
	})
	if err != nil {
		t.Fatalf("initialize solo room: %v", err)
	}

	assertRedisString(t, server, "room:41:status", "playing")
	assertRedisString(t, server, "room:41:turn", "0")
	assertRedisString(t, server, "room:41:summary", "")
	gotTurnOrder, err := server.List("room:41:turn_order")
	if err != nil {
		t.Fatalf("read turn order: %v", err)
	}
	if got := gotTurnOrder; len(got) != 1 || got[0] != "7" {
		t.Fatalf("turn order = %#v, want [7]", got)
	}
	if got := server.HGet("room:41:player:7", "hp"); got != "10" {
		t.Fatalf("hp = %q, want 10", got)
	}
	rounds, err := server.List("room:41:rounds")
	if err != nil {
		t.Fatalf("read rounds: %v", err)
	}
	if len(rounds) != 1 {
		t.Fatalf("rounds = %#v, want one opening message", rounds)
	}
	var opening model.RuntimeMessage
	if err := json.Unmarshal([]byte(rounds[0]), &opening); err != nil {
		t.Fatalf("decode opening: %v", err)
	}
	if opening.Role != "assistant" || opening.Content != "古宅的大门缓缓打开。" {
		t.Fatalf("opening = %#v", opening)
	}
	for _, key := range runtimeKeys(41, 7) {
		if ttl := server.TTL(key); ttl != DefaultGameRuntimeTTL {
			t.Fatalf("TTL(%s) = %s, want %s", key, ttl, DefaultGameRuntimeTTL)
		}
	}
	for _, key := range staleKeys[len(runtimeKeys(41, 7)):] {
		if server.Exists(key) {
			t.Fatalf("stale optional runtime key still exists: %s", key)
		}
	}
}

func TestRedisGameStateRepoRejectsInvalidStateWithoutMutation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repository, err := NewRedisGameStateRepo(client, DefaultGameRuntimeTTL)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}

	tests := []*model.SoloRuntimeState{
		nil,
		{RoomID: 41, UserID: 7, Status: model.RoomStatusWaiting, PlayerState: map[string]string{"hp": "10"}, Opening: model.RuntimeMessage{Role: "assistant", Content: "opening"}},
		{RoomID: 41, UserID: 7, Status: model.RoomStatusPlaying, PlayerState: nil, Opening: model.RuntimeMessage{Role: "assistant", Content: "opening"}},
		{RoomID: 41, UserID: 7, Status: model.RoomStatusPlaying, PlayerState: map[string]string{"hp": ""}, Opening: model.RuntimeMessage{Role: "assistant", Content: "opening"}},
		{RoomID: 41, UserID: 7, Status: model.RoomStatusPlaying, PlayerState: map[string]string{"hp": "unknown"}, Opening: model.RuntimeMessage{Role: "assistant", Content: "opening"}},
		{RoomID: 41, UserID: 7, Status: model.RoomStatusPlaying, PlayerState: map[string]string{"hp": "10"}, Opening: model.RuntimeMessage{Role: "user", Content: "opening"}},
		{RoomID: 41, UserID: 7, Status: model.RoomStatusPlaying, PlayerState: map[string]string{"hp": "10"}, Opening: model.RuntimeMessage{Role: "assistant", Content: "  "}},
	}
	for index, state := range tests {
		err := repository.InitializeSoloRoom(context.Background(), state)
		if !errors.Is(err, ErrInvalidGameRuntimeState) {
			t.Fatalf("case %d error = %v, want invalid state", index, err)
		}
	}
	if got := server.Keys(); len(got) != 0 {
		t.Fatalf("invalid states mutated Redis keys: %#v", got)
	}
}

func TestRedisGameStateRepoDeleteSoloRoom(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repository, err := NewRedisGameStateRepo(client, DefaultGameRuntimeTTL)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	for _, key := range runtimeKeys(41, 7) {
		server.Set(key, "stale")
	}
	server.HSet(actionResultsKey(41), "request", "cached")
	server.SAdd(itemStateKey(41, 7), "火把|2|旧火把")
	server.HSet(buffStateKey(41, 7), "中毒", "3")
	server.Set("room:42:status", "playing")

	if err := repository.DeleteSoloRoom(context.Background(), 41, 7); err != nil {
		t.Fatalf("delete solo room: %v", err)
	}
	if err := repository.DeleteSoloRoom(context.Background(), 41, 7); err != nil {
		t.Fatalf("repeat delete solo room: %v", err)
	}
	for _, key := range runtimeKeys(41, 7) {
		if server.Exists(key) {
			t.Fatalf("runtime key still exists: %s", key)
		}
	}
	if server.Exists(actionResultsKey(41)) {
		t.Fatal("runtime action cache still exists")
	}
	if server.Exists(itemStateKey(41, 7)) || server.Exists(buffStateKey(41, 7)) {
		t.Fatal("runtime item or buff state still exists")
	}
	if !server.Exists("room:42:status") {
		t.Fatal("cleanup deleted another room")
	}
}

func TestRedisGameStateRepoDeleteSoloRoomValidatesIdentity(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repository, err := NewRedisGameStateRepo(client, DefaultGameRuntimeTTL)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	for _, identity := range [][2]uint{{0, 7}, {41, 0}} {
		err := repository.DeleteSoloRoom(context.Background(), identity[0], identity[1])
		if !errors.Is(err, ErrInvalidGameRuntimeState) {
			t.Fatalf("identity %#v error = %v", identity, err)
		}
	}
}

func TestRedisGameStateRepoWrapsRedisFailure(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	repository, err := NewRedisGameStateRepo(client, DefaultGameRuntimeTTL)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	server.Close()

	err = repository.InitializeSoloRoom(context.Background(), validSoloRuntimeState())
	if !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("error = %v, want runtime unavailable", err)
	}
}

func TestNewRedisGameStateRepoValidatesConfiguration(t *testing.T) {
	if _, err := NewRedisGameStateRepo(nil, DefaultGameRuntimeTTL); !errors.Is(err, ErrInvalidGameRuntimeState) {
		t.Fatalf("nil client error = %v", err)
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	for _, ttl := range []time.Duration{0, -time.Second, 1500 * time.Millisecond} {
		if _, err := NewRedisGameStateRepo(client, ttl); !errors.Is(err, ErrInvalidGameRuntimeState) {
			t.Fatalf("TTL %s error = %v", ttl, err)
		}
	}
}

func TestRedisGameStateRepoCommitActionAppliesAndReplaysIdempotently(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.SAdd(itemStateKey(41, 7), "火把|2|旧火把")
	mutation := validActionMutation(0, "11111111-1111-4111-8111-111111111111", "look")
	mutation.PlayerStateChanges = map[string]string{"hp": "8", "location": "书房"}
	mutation.ItemMutations = []model.RuntimeItemMutation{
		{Name: "银钥匙", QuantityDelta: 2, Description: "生|锈"},
		{Name: "火把", QuantityDelta: -1},
	}
	mutation.BuffMutations = []model.RuntimeBuffMutation{{Name: "中毒", Duration: 3}}

	result, err := repository.CommitAction(context.Background(), mutation)
	if err != nil {
		t.Fatalf("commit action: %v", err)
	}
	if result.Duplicate || result.CurrentTurn != 1 || string(result.ResponseJSON) != `{"narrative":"发现书房"}` {
		t.Fatalf("commit result = %#v", result)
	}
	assertRedisString(t, server, "room:41:turn", "1")
	if got := server.HGet("room:41:player:7", "hp"); got != "8" {
		t.Fatalf("hp = %q, want 8", got)
	}
	assertInventory(t, server, map[string]string{
		"火把": "1|旧火把", "银钥匙": "2|生|锈",
	})
	if got := server.HGet(buffStateKey(41, 7), "中毒"); got != "3" {
		t.Fatalf("buff duration = %q, want 3", got)
	}
	rounds, err := server.List("room:41:rounds")
	if err != nil {
		t.Fatalf("read rounds: %v", err)
	}
	assertRoundMessages(t, rounds, []string{"发现书房", "我查看书房", "opening"})
	if ttl := server.TTL(actionResultsKey(41)); ttl != DefaultGameRuntimeTTL {
		t.Fatalf("action results TTL = %s", ttl)
	}
	if ttl := server.TTL(itemStateKey(41, 7)); ttl != DefaultGameRuntimeTTL {
		t.Fatalf("item state TTL = %s", ttl)
	}
	if ttl := server.TTL(buffStateKey(41, 7)); ttl != DefaultGameRuntimeTTL {
		t.Fatalf("buff state TTL = %s", ttl)
	}

	mutation.PlayerStateChanges = map[string]string{"hp": "1"}
	mutation.ResponseJSON = json.RawMessage(`{"narrative":"different"}`)
	replayed, err := repository.CommitAction(context.Background(), mutation)
	if err != nil {
		t.Fatalf("replay action: %v", err)
	}
	if !replayed.Duplicate || replayed.CurrentTurn != 1 ||
		string(replayed.ResponseJSON) != `{"narrative":"发现书房"}` {
		t.Fatalf("replayed result = %#v", replayed)
	}
	if got := server.HGet("room:41:player:7", "hp"); got != "8" {
		t.Fatalf("duplicate changed hp to %q", got)
	}
	assertInventory(t, server, map[string]string{
		"火把": "1|旧火把", "银钥匙": "2|生|锈",
	})
}

func TestRedisGameStateRepoCommitActionRejectsReusedRequestID(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	requestID := "22222222-2222-4222-8222-222222222222"
	if _, err := repository.CommitAction(
		context.Background(),
		validActionMutation(0, requestID, "look"),
	); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	_, err := repository.CommitAction(
		context.Background(),
		validActionMutation(0, requestID, "attack"),
	)
	if !errors.Is(err, ErrActionIdempotencyConflict) {
		t.Fatalf("reused request ID error = %v", err)
	}
	assertRedisString(t, server, "room:41:turn", "1")
}

func TestRedisGameStateRepoFindActionResultSupportsPreflightReplay(t *testing.T) {
	_, repository := initializedRuntimeRepository(t)
	mutation := validActionMutation(0, "22222222-2222-4222-8222-222222222223", "look")
	if _, err := repository.CommitAction(context.Background(), mutation); err != nil {
		t.Fatalf("commit action: %v", err)
	}

	result, found, err := repository.FindActionResult(
		context.Background(),
		41,
		mutation.RequestID,
		mutation.RequestFingerprint,
	)
	if err != nil {
		t.Fatalf("find action result: %v", err)
	}
	if !found || result == nil || !result.Duplicate || result.CurrentTurn != 1 ||
		string(result.ResponseJSON) != `{"narrative":"发现书房"}` {
		t.Fatalf("found result = (%#v, %v)", result, found)
	}

	missing, found, err := repository.FindActionResult(
		context.Background(),
		41,
		"22222222-2222-4222-8222-222222222224",
		mutation.RequestFingerprint,
	)
	if err != nil || found || missing != nil {
		t.Fatalf("missing result = (%#v, %v, %v)", missing, found, err)
	}
}

func TestRedisGameStateRepoFindActionResultRejectsFingerprintReuse(t *testing.T) {
	_, repository := initializedRuntimeRepository(t)
	mutation := validActionMutation(0, "22222222-2222-4222-8222-222222222225", "look")
	if _, err := repository.CommitAction(context.Background(), mutation); err != nil {
		t.Fatalf("commit action: %v", err)
	}
	otherFingerprint := sha256.Sum256([]byte("different action"))
	_, _, err := repository.FindActionResult(
		context.Background(),
		41,
		mutation.RequestID,
		fmt.Sprintf("%x", otherFingerprint),
	)
	if !errors.Is(err, ErrActionIdempotencyConflict) {
		t.Fatalf("fingerprint reuse error = %v", err)
	}
}

func TestRedisGameStateRepoCommitActionUsesTurnCAS(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	mutation := validActionMutation(3, "33333333-3333-4333-8333-333333333333", "look")

	result, err := repository.CommitAction(context.Background(), mutation)
	if !errors.Is(err, ErrGameRuntimeConflict) {
		t.Fatalf("stale turn error = %v", err)
	}
	if result == nil || result.CurrentTurn != 0 {
		t.Fatalf("conflict result = %#v", result)
	}
	assertRedisString(t, server, "room:41:turn", "0")
	if server.Exists(actionResultsKey(41)) {
		t.Fatal("conflict created idempotency state")
	}
}

func TestRedisGameStateRepoCommitActionRejectsInsufficientItemsAtomically(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.SAdd(itemStateKey(41, 7), "火把|1|旧火把")
	mutation := validActionMutation(0, "33333333-3333-4333-8333-333333333334", "use-two")
	mutation.PlayerStateChanges = map[string]string{"hp": "1"}
	mutation.ItemMutations = []model.RuntimeItemMutation{{Name: "火把", QuantityDelta: -2}}
	mutation.BuffMutations = []model.RuntimeBuffMutation{{Name: "中毒", Duration: 3}}

	_, err := repository.CommitAction(context.Background(), mutation)
	if !errors.Is(err, ErrInsufficientItemQuantity) {
		t.Fatalf("insufficient item error = %v", err)
	}
	assertRedisString(t, server, "room:41:turn", "0")
	if got := server.HGet("room:41:player:7", "hp"); got != "10" {
		t.Fatalf("failed item action changed hp to %q", got)
	}
	assertInventory(t, server, map[string]string{"火把": "1|旧火把"})
	if server.Exists(buffStateKey(41, 7)) || server.Exists(actionResultsKey(41)) {
		t.Fatal("failed item action created buff or idempotency state")
	}
}

func TestRedisGameStateRepoCommitActionRejectsNonPlayingRoom(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.Set("room:41:status", "paused")

	_, err := repository.CommitAction(
		context.Background(),
		validActionMutation(0, "44444444-4444-4444-8444-444444444444", "look"),
	)
	if !errors.Is(err, ErrGameRuntimeNotPlaying) {
		t.Fatalf("paused room error = %v", err)
	}
	assertRedisString(t, server, "room:41:turn", "0")
}

func TestRedisGameStateRepoCommitActionTrimsRecentMessages(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	for turn := 0; turn < 6; turn++ {
		mutation := validActionMutation(
			turn,
			fmt.Sprintf("55555555-5555-4555-8555-%012d", turn),
			fmt.Sprintf("action-%d", turn),
		)
		mutation.Messages[0].Content = fmt.Sprintf("user-%d", turn)
		mutation.Messages[1].Content = fmt.Sprintf("assistant-%d", turn)
		if _, err := repository.CommitAction(context.Background(), mutation); err != nil {
			t.Fatalf("turn %d commit: %v", turn, err)
		}
	}
	rounds, err := server.List("room:41:rounds")
	if err != nil {
		t.Fatalf("read rounds: %v", err)
	}
	if len(rounds) != 10 {
		t.Fatalf("round count = %d, want 10", len(rounds))
	}
	assertRoundMessages(t, rounds[:2], []string{"assistant-5", "user-5"})
}

func TestRedisGameStateRepoCommitActionSerializesConcurrentTurns(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	mutations := []*model.ActionRuntimeMutation{
		validActionMutation(0, "66666666-6666-4666-8666-666666666661", "left"),
		validActionMutation(0, "66666666-6666-4666-8666-666666666662", "right"),
	}
	mutations[0].ItemMutations = []model.RuntimeItemMutation{{Name: "左门钥匙", QuantityDelta: 1}}
	mutations[1].ItemMutations = []model.RuntimeItemMutation{{Name: "右门钥匙", QuantityDelta: 1}}
	type outcome struct {
		result *ActionCommitResult
		err    error
	}
	outcomes := make(chan outcome, len(mutations))
	var wait sync.WaitGroup
	for _, mutation := range mutations {
		wait.Add(1)
		go func(candidate *model.ActionRuntimeMutation) {
			defer wait.Done()
			result, err := repository.CommitAction(context.Background(), candidate)
			outcomes <- outcome{result: result, err: err}
		}(mutation)
	}
	wait.Wait()
	close(outcomes)

	applied := 0
	conflicted := 0
	for outcome := range outcomes {
		switch {
		case outcome.err == nil && outcome.result != nil && outcome.result.CurrentTurn == 1:
			applied++
		case errors.Is(outcome.err, ErrGameRuntimeConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent outcome: %#v", outcome)
		}
	}
	if applied != 1 || conflicted != 1 {
		t.Fatalf("applied = %d, conflicted = %d", applied, conflicted)
	}
	members, err := server.Members(itemStateKey(41, 7))
	if err != nil || len(members) != 1 {
		t.Fatalf("concurrent inventory = %#v, error = %v", members, err)
	}
}

func TestRedisGameStateRepoCommitActionRejectsInvalidContractWithoutMutation(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	valid := func() *model.ActionRuntimeMutation {
		return validActionMutation(0, "77777777-7777-4777-8777-777777777777", "look")
	}
	tests := []*model.ActionRuntimeMutation{
		nil,
		func() *model.ActionRuntimeMutation { value := valid(); value.ExpectedTurn = -1; return value }(),
		func() *model.ActionRuntimeMutation { value := valid(); value.RequestID = "not-uuid"; return value }(),
		func() *model.ActionRuntimeMutation {
			value := valid()
			value.RequestFingerprint = "short"
			return value
		}(),
		func() *model.ActionRuntimeMutation {
			value := valid()
			value.ResponseJSON = json.RawMessage(`invalid`)
			return value
		}(),
		func() *model.ActionRuntimeMutation {
			value := valid()
			value.Messages[0].Role = "assistant"
			return value
		}(),
		func() *model.ActionRuntimeMutation {
			value := valid()
			value.PlayerStateChanges = map[string]string{"character_id": "99"}
			return value
		}(),
		func() *model.ActionRuntimeMutation {
			value := valid()
			value.PlayerStateChanges = map[string]string{"hp": "unknown"}
			return value
		}(),
		func() *model.ActionRuntimeMutation {
			value := valid()
			value.ItemMutations = []model.RuntimeItemMutation{{Name: "钥匙|伪造", QuantityDelta: 1}}
			return value
		}(),
		func() *model.ActionRuntimeMutation {
			value := valid()
			value.ItemMutations = []model.RuntimeItemMutation{{Name: "钥匙", QuantityDelta: -1, Description: "不应携带描述"}}
			return value
		}(),
		func() *model.ActionRuntimeMutation {
			value := valid()
			value.BuffMutations = []model.RuntimeBuffMutation{{Name: "中毒", Duration: 0}}
			return value
		}(),
	}
	for index, mutation := range tests {
		if _, err := repository.CommitAction(context.Background(), mutation); !errors.Is(err, ErrInvalidGameRuntimeState) {
			t.Fatalf("case %d error = %v", index, err)
		}
	}
	assertRedisString(t, server, "room:41:turn", "0")
	if server.Exists(actionResultsKey(41)) {
		t.Fatal("invalid action created idempotency state")
	}
}

func TestRedisGameStateRepoCommitActionRequiresCompleteRuntime(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.Del("room:41:player:7")
	_, err := repository.CommitAction(
		context.Background(),
		validActionMutation(0, "88888888-8888-4888-8888-888888888888", "look"),
	)
	if !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("incomplete runtime error = %v", err)
	}
}

func TestRedisGameStateRepoCommitActionDoesNotPartiallyMutateMalformedRuntime(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.Del("room:41:rounds")
	server.Set("room:41:rounds", "wrong-type")
	mutation := validActionMutation(0, "88888888-8888-4888-8888-888888888889", "look")
	mutation.PlayerStateChanges = map[string]string{"hp": "1"}

	_, err := repository.CommitAction(context.Background(), mutation)
	if !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("malformed runtime error = %v", err)
	}
	if got := server.HGet("room:41:player:7", "hp"); got != "10" {
		t.Fatalf("malformed runtime partially changed hp to %q", got)
	}
	assertRedisString(t, server, "room:41:turn", "0")
}

func TestRedisGameStateRepoCommitActionRejectsMalformedInventoryWithoutMutation(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.SAdd(itemStateKey(41, 7), "malformed-item")
	mutation := validActionMutation(0, "88888888-8888-4888-8888-888888888890", "look")
	mutation.PlayerStateChanges = map[string]string{"hp": "1"}

	_, err := repository.CommitAction(context.Background(), mutation)
	if !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("malformed inventory error = %v", err)
	}
	if got := server.HGet("room:41:player:7", "hp"); got != "10" {
		t.Fatalf("malformed inventory partially changed hp to %q", got)
	}
	assertRedisString(t, server, "room:41:turn", "0")
}

func TestRedisGameStateRepoCommitActionRejectsMalformedBuffsWithoutMutation(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.HSet(buffStateKey(41, 7), "中毒", "invalid")
	mutation := validActionMutation(0, "88888888-8888-4888-8888-888888888891", "look")
	mutation.PlayerStateChanges = map[string]string{"hp": "1"}

	_, err := repository.CommitAction(context.Background(), mutation)
	if !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("malformed buffs error = %v", err)
	}
	if got := server.HGet("room:41:player:7", "hp"); got != "10" {
		t.Fatalf("malformed buffs partially changed hp to %q", got)
	}
	assertRedisString(t, server, "room:41:turn", "0")
}

func TestRedisGameStateRepoCommitActionWrapsRedisFailure(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.Close()
	_, err := repository.CommitAction(
		context.Background(),
		validActionMutation(0, "99999999-9999-4999-8999-999999999999", "look"),
	)
	if !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("Redis failure error = %v", err)
	}
}

func TestRedisGameStateRepoCaptureSoloRoom(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.Set("room:41:summary", "玩家进入书房")
	mutation := validActionMutation(0, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "capture")
	mutation.PlayerStateChanges = map[string]string{"hp": "8", "location": "书房"}
	mutation.ItemMutations = []model.RuntimeItemMutation{
		{Name: "钥匙", QuantityDelta: 1, Description: "黄铜钥匙"},
		{Name: "火把", QuantityDelta: 2, Description: "旧火把"},
	}
	mutation.BuffMutations = []model.RuntimeBuffMutation{{Name: "中毒", Duration: 3}}
	if _, err := repository.CommitAction(context.Background(), mutation); err != nil {
		t.Fatalf("commit action: %v", err)
	}
	server.Set("room:41:status", "paused")

	snapshot, err := repository.CaptureSoloRoom(context.Background(), 41, 7)
	if err != nil {
		t.Fatalf("capture solo room: %v", err)
	}
	if snapshot.Version != model.SoloRuntimeSnapshotVersion || snapshot.RoomID != 41 || snapshot.UserID != 7 ||
		snapshot.Status != model.RoomStatusPaused || snapshot.Turn != 1 ||
		!reflect.DeepEqual(snapshot.TurnOrder, []uint{7}) || snapshot.Summary != "玩家进入书房" ||
		snapshot.PlayerState["hp"] != "8" || snapshot.PlayerState["location"] != "书房" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if !reflect.DeepEqual(snapshot.Items, []model.RuntimeItem{
		{Name: "火把", Quantity: 2, Description: "旧火把"},
		{Name: "钥匙", Quantity: 1, Description: "黄铜钥匙"},
	}) {
		t.Fatalf("snapshot items = %#v", snapshot.Items)
	}
	if !reflect.DeepEqual(snapshot.Buffs, []model.RuntimeBuff{{Name: "中毒", Duration: 3}}) {
		t.Fatalf("snapshot buffs = %#v", snapshot.Buffs)
	}
	if len(snapshot.RecentMessages) != 3 || snapshot.RecentMessages[0].Role != "assistant" ||
		snapshot.RecentMessages[1].Role != "user" || snapshot.RecentMessages[2].Content != "opening" {
		t.Fatalf("snapshot messages = %#v", snapshot.RecentMessages)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if bytes.Contains(encoded, []byte("summary")) || bytes.Contains(encoded, []byte("recent_messages")) ||
		bytes.Contains(encoded, []byte("room_id")) || bytes.Contains(encoded, []byte("user_id")) {
		t.Fatalf("redis_snapshot contains separately persisted fields: %s", encoded)
	}
}

func TestRedisGameStateRepoRestoreSoloRoomAtomicallyReplacesRuntime(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.HSet(actionResultsKey(41), "old-request", `{"fingerprint":"old","response":{}}`)
	server.SAdd(itemStateKey(41, 7), "旧物|1|应删除")
	server.HSet(buffStateKey(41, 7), "旧Buff", "9")
	snapshot := validRuntimeSnapshot()

	if err := repository.RestoreSoloRoom(context.Background(), snapshot); err != nil {
		t.Fatalf("restore solo room: %v", err)
	}
	if server.Exists(actionResultsKey(41)) {
		t.Fatal("restore retained action idempotency cache")
	}
	restored, err := repository.CaptureSoloRoom(context.Background(), 41, 7)
	if err != nil {
		t.Fatalf("capture restored room: %v", err)
	}
	if !reflect.DeepEqual(restored, snapshot) {
		t.Fatalf("restored snapshot = %#v, want %#v", restored, snapshot)
	}
	for _, key := range soloRuntimeCleanupKeys(41, 7) {
		if key == actionResultsKey(41) {
			continue
		}
		if ttl := server.TTL(key); ttl != DefaultGameRuntimeTTL {
			t.Fatalf("TTL(%s) = %s, want %s", key, ttl, DefaultGameRuntimeTTL)
		}
	}
}

func TestRedisGameStateRepoRestoreSoloRoomDeletesStaleOptionalState(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.HSet(actionResultsKey(41), "old-request", "cached")
	server.SAdd(itemStateKey(41, 7), "旧物|1|应删除")
	server.HSet(buffStateKey(41, 7), "旧Buff", "9")
	snapshot := validRuntimeSnapshot()
	snapshot.Items = []model.RuntimeItem{}
	snapshot.Buffs = []model.RuntimeBuff{}

	if err := repository.RestoreSoloRoom(context.Background(), snapshot); err != nil {
		t.Fatalf("restore solo room: %v", err)
	}
	if server.Exists(actionResultsKey(41)) || server.Exists(itemStateKey(41, 7)) ||
		server.Exists(buffStateKey(41, 7)) {
		t.Fatal("restore retained stale optional runtime state")
	}
}

func TestRedisGameStateRepoCaptureSoloRoomRejectsMalformedRuntime(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*miniredis.Miniredis)
	}{
		{"missing status", func(server *miniredis.Miniredis) { server.Del("room:41:status") }},
		{"invalid status", func(server *miniredis.Miniredis) { server.Set("room:41:status", "ended") }},
		{"invalid turn", func(server *miniredis.Miniredis) { server.Set("room:41:turn", "bad") }},
		{"wrong player type", func(server *miniredis.Miniredis) {
			server.Del("room:41:player:7")
			server.Set("room:41:player:7", "bad")
		}},
		{"malformed item", func(server *miniredis.Miniredis) { server.SAdd(itemStateKey(41, 7), "bad") }},
		{"malformed buff", func(server *miniredis.Miniredis) { server.HSet(buffStateKey(41, 7), "中毒", "bad") }},
		{"malformed message", func(server *miniredis.Miniredis) { server.Lpush("room:41:rounds", `{"role":"assistant"}`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, repository := initializedRuntimeRepository(t)
			test.mutate(server)
			if _, err := repository.CaptureSoloRoom(context.Background(), 41, 7); !errors.Is(err, ErrGameRuntimeUnavailable) {
				t.Fatalf("capture error = %v", err)
			}
		})
	}
}

func TestRedisGameStateRepoRestoreSoloRoomRejectsInvalidSnapshotWithoutMutation(t *testing.T) {
	tests := []func(*model.SoloRuntimeSnapshot){
		func(value *model.SoloRuntimeSnapshot) { value.Version = 0 },
		func(value *model.SoloRuntimeSnapshot) { value.RoomID = 0 },
		func(value *model.SoloRuntimeSnapshot) { value.UserID = 0 },
		func(value *model.SoloRuntimeSnapshot) { value.Status = model.RoomStatusEnded },
		func(value *model.SoloRuntimeSnapshot) { value.Turn = -1 },
		func(value *model.SoloRuntimeSnapshot) { value.TurnOrder = []uint{8} },
		func(value *model.SoloRuntimeSnapshot) { value.PlayerState = map[string]string{"hp": "bad"} },
		func(value *model.SoloRuntimeSnapshot) { value.RecentMessages[0].Role = "user" },
		func(value *model.SoloRuntimeSnapshot) {
			value.Items = append(value.Items, value.Items[0])
		},
		func(value *model.SoloRuntimeSnapshot) {
			value.Buffs = append(value.Buffs, value.Buffs[0])
		},
	}
	for index, mutate := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			server, repository := initializedRuntimeRepository(t)
			before, err := server.Get("room:41:turn")
			if err != nil {
				t.Fatalf("read turn before restore: %v", err)
			}
			snapshot := validRuntimeSnapshot()
			mutate(snapshot)
			if err := repository.RestoreSoloRoom(context.Background(), snapshot); !errors.Is(err, ErrInvalidGameRuntimeState) {
				t.Fatalf("restore error = %v", err)
			}
			after, err := server.Get("room:41:turn")
			if err != nil || after != before {
				t.Fatalf("invalid restore mutated turn: before=%q after=%q error=%v", before, after, err)
			}
		})
	}
}

func TestRedisGameStateRepoCaptureIsConsistentWithConcurrentAction(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		_, repository := initializedRuntimeRepository(t)
		mutation := validActionMutation(
			0,
			fmt.Sprintf("bbbbbbbb-bbbb-4bbb-8bbb-%012d", iteration),
			fmt.Sprintf("snapshot-race-%d", iteration),
		)
		mutation.PlayerStateChanges = map[string]string{"hp": "8"}
		mutation.ItemMutations = []model.RuntimeItemMutation{{Name: "钥匙", QuantityDelta: 1}}
		start := make(chan struct{})
		var wait sync.WaitGroup
		var snapshot *model.SoloRuntimeSnapshot
		var captureErr error
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			snapshot, captureErr = repository.CaptureSoloRoom(context.Background(), 41, 7)
		}()
		go func() {
			defer wait.Done()
			<-start
			_, _ = repository.CommitAction(context.Background(), mutation)
		}()
		close(start)
		wait.Wait()
		if captureErr != nil {
			t.Fatalf("iteration %d capture error: %v", iteration, captureErr)
		}
		before := snapshot.Turn == 0 && snapshot.PlayerState["hp"] == "10" &&
			len(snapshot.Items) == 0 && len(snapshot.RecentMessages) == 1
		after := snapshot.Turn == 1 && snapshot.PlayerState["hp"] == "8" &&
			len(snapshot.Items) == 1 && len(snapshot.RecentMessages) == 3
		if !before && !after {
			t.Fatalf("iteration %d captured torn state: %#v", iteration, snapshot)
		}
	}
}

func TestRedisGameStateRepoSnapshotMethodsWrapRedisFailure(t *testing.T) {
	server, repository := initializedRuntimeRepository(t)
	server.Close()
	if _, err := repository.CaptureSoloRoom(context.Background(), 41, 7); !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("capture Redis failure = %v", err)
	}
	if err := repository.RestoreSoloRoom(context.Background(), validRuntimeSnapshot()); !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("restore Redis failure = %v", err)
	}
}

func validRuntimeSnapshot() *model.SoloRuntimeSnapshot {
	return &model.SoloRuntimeSnapshot{
		Version:   model.SoloRuntimeSnapshotVersion,
		RoomID:    41,
		UserID:    7,
		Status:    model.RoomStatusPaused,
		Turn:      3,
		TurnOrder: []uint{7},
		PlayerState: map[string]string{
			"hp": "8", "location": "书房",
		},
		Items:   []model.RuntimeItem{{Name: "钥匙", Quantity: 1, Description: "黄铜钥匙"}},
		Buffs:   []model.RuntimeBuff{{Name: "中毒", Duration: 2}},
		Summary: "玩家已经进入书房。",
		RecentMessages: []model.RuntimeMessage{
			{Role: "assistant", Content: "你找到了一把钥匙。"},
			{Role: "user", Content: "我调查书房。"},
			{Role: "assistant", Content: "你进入了古宅。"},
		},
	}
}

func validSoloRuntimeState() *model.SoloRuntimeState {
	return &model.SoloRuntimeState{
		RoomID:      41,
		UserID:      7,
		Status:      model.RoomStatusPlaying,
		PlayerState: map[string]string{"hp": "10"},
		Opening:     model.RuntimeMessage{Role: "assistant", Content: "opening"},
	}
}

func initializedRuntimeRepository(t *testing.T) (*miniredis.Miniredis, *RedisGameStateRepo) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repository, err := NewRedisGameStateRepo(client, DefaultGameRuntimeTTL)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	if err := repository.InitializeSoloRoom(context.Background(), validSoloRuntimeState()); err != nil {
		t.Fatalf("initialize runtime: %v", err)
	}
	return server, repository
}

func validActionMutation(turn int, requestID, fingerprintSource string) *model.ActionRuntimeMutation {
	fingerprint := sha256.Sum256([]byte(fingerprintSource))
	return &model.ActionRuntimeMutation{
		RoomID:             41,
		UserID:             7,
		ExpectedTurn:       turn,
		RequestID:          requestID,
		RequestFingerprint: fmt.Sprintf("%x", fingerprint),
		Messages: []model.RuntimeMessage{
			{Role: "user", Content: "我查看书房"},
			{Role: "assistant", Content: "发现书房"},
		},
		ResponseJSON: json.RawMessage(`{"narrative":"发现书房"}`),
	}
}

func assertRoundMessages(t *testing.T, encoded []string, want []string) {
	t.Helper()
	if len(encoded) != len(want) {
		t.Fatalf("message count = %d, want %d", len(encoded), len(want))
	}
	for index, raw := range encoded {
		var message model.RuntimeMessage
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			t.Fatalf("decode message %d: %v", index, err)
		}
		if message.Content != want[index] {
			t.Fatalf("message %d = %q, want %q", index, message.Content, want[index])
		}
	}
}

func assertInventory(t *testing.T, server *miniredis.Miniredis, want map[string]string) {
	t.Helper()
	members, err := server.Members(itemStateKey(41, 7))
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	got := make(map[string]string, len(members))
	for _, member := range members {
		parts := strings.SplitN(member, "|", 2)
		if len(parts) != 2 {
			t.Fatalf("malformed inventory member: %q", member)
		}
		got[parts[0]] = parts[1]
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory = %#v, want %#v", got, want)
	}
}

func assertRedisString(t *testing.T, server *miniredis.Miniredis, key, want string) {
	t.Helper()
	got, err := server.Get(key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}
