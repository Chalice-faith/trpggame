package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"trpggame/internal/model"
	"trpggame/internal/repo"
	"trpggame/internal/ws"
)

type roomMutationRepoStub struct{ record *repo.RoomRecord }

func (s *roomMutationRepoStub) Create(context.Context, uint, uint, string, string, int, time.Time) (*repo.RoomRecord, error) {
	return s.record, nil
}
func (s *roomMutationRepoStub) List(context.Context, uint) ([]model.GameRoom, error) { return nil, nil }
func (s *roomMutationRepoStub) Get(context.Context, uint, uint) (*repo.RoomRecord, error) {
	return s.record, nil
}
func (s *roomMutationRepoStub) Join(context.Context, uint, string, time.Time) (*repo.RoomRecord, error) {
	return s.record, nil
}
func (s *roomMutationRepoStub) SelectCharacter(context.Context, uint, uint, uint, uint64) (*repo.RoomRecord, error) {
	return s.record, nil
}
func (s *roomMutationRepoStub) SetReady(context.Context, uint, uint, bool, uint64) (*repo.RoomRecord, error) {
	return s.record, nil
}
func (s *roomMutationRepoStub) Leave(context.Context, uint, uint, uint64, time.Time) (*repo.RoomRecord, error) {
	return s.record, nil
}
func (s *roomMutationRepoStub) Remove(context.Context, uint, uint, uint, uint64, time.Time) (*repo.RoomRecord, error) {
	return s.record, nil
}
func (s *roomMutationRepoStub) Transfer(context.Context, uint, uint, uint, uint64) (*repo.RoomRecord, error) {
	return s.record, nil
}
func (s *roomMutationRepoStub) Start(context.Context, uint, uint, uint64) (*repo.RoomRecord, error) {
	return s.record, nil
}

type roomMutationRecorder struct{ mutations []RoomMutation }

func (r *roomMutationRecorder) PublishRoomMutation(mutation RoomMutation) {
	r.mutations = append(r.mutations, mutation)
}

func TestRoomServicePublishesCommittedMutationAndRevocation(t *testing.T) {
	record := &repo.RoomRecord{Room: model.GameRoom{ID: 41, Version: 2}, Mutation: "room_member_left", AffectedUserID: 8}
	service := NewRoomService(&roomMutationRepoStub{record: record})
	recorder := &roomMutationRecorder{}
	service.SetMutationPublisher(recorder)

	result, err := service.Leave(context.Background(), 8, 41, 1)
	if err != nil || result.ID != 41 || len(recorder.mutations) != 1 {
		t.Fatalf("leave = (%#v, %v), mutations=%#v", result, err, recorder.mutations)
	}
	mutation := recorder.mutations[0]
	if mutation.Type != "room_member_left" || mutation.AffectedUserID != 8 || !mutation.RevokeAccess || mutation.Snapshot != result {
		t.Fatalf("mutation = %#v", mutation)
	}
}

func TestRoomServiceDoesNotPublishIdempotentMutation(t *testing.T) {
	record := &repo.RoomRecord{Room: model.GameRoom{ID: 41, Version: 2}}
	service := NewRoomService(&roomMutationRepoStub{record: record})
	recorder := &roomMutationRecorder{}
	service.SetMutationPublisher(recorder)

	if _, err := service.SetReady(context.Background(), 7, 41, true, 2); err != nil {
		t.Fatal(err)
	}
	if len(recorder.mutations) != 0 {
		t.Fatalf("idempotent operation published %#v", recorder.mutations)
	}
}

func TestRoomRealtimeBroadcastsSnapshotWithRoomMessageType(t *testing.T) {
	hub := ws.NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)
	clients := []*ws.Client{ws.NewClient(hub, nil, 7, 41), ws.NewClient(hub, nil, 8, 41)}
	for _, client := range clients {
		if !hub.Register(client) {
			t.Fatal("register failed")
		}
		select {
		case <-client.Send:
		case <-time.After(time.Second):
			t.Fatal("subscribed timeout")
		}
	}
	NewRoomRealtime(hub).PublishRoomMutation(RoomMutation{
		Type: "room_ready_changed", Snapshot: &RoomSnapshot{RoomSummary: RoomSummary{ID: 41, Version: 3}},
	})
	for _, client := range clients {
		select {
		case raw := <-client.Send:
			var message ws.Message
			if err := json.Unmarshal(raw, &message); err != nil || message.Type != ws.MsgRoomReadyChanged || message.Seq != 1 {
				t.Fatalf("message = %#v, err=%v", message, err)
			}
		case <-time.After(time.Second):
			t.Fatal("room event timeout")
		}
	}
}

func TestRoomRealtimePublishesMultiplayerStartupSequence(t *testing.T) {
	hub := ws.NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)
	client := ws.NewClient(hub, nil, 7, 41)
	if !hub.Register(client) {
		t.Fatal("register failed")
	}
	select {
	case <-client.Send:
	case <-time.After(time.Second):
		t.Fatal("subscribed timeout")
	}
	deadline := time.Date(2026, 9, 22, 8, 2, 0, 0, time.UTC)
	realtime := NewRoomRealtime(hub)
	realtime.PublishRoomMutation(RoomMutation{
		Type: "game_started", Snapshot: &RoomSnapshot{RoomSummary: RoomSummary{ID: 41, Version: 4}},
	})
	realtime.PublishMultiplayerRuntime(&model.MultiplayerRuntimeSnapshot{
		Version: 2, RoomID: 41, Status: model.RoomStatusPlaying,
		Generation:  "bc624606-57a3-49c5-bf51-f5a04e5f299f",
		CurrentTurn: 0, RoundNumber: 0, TurnOrder: []uint{7, 8}, CurrentActorID: 7,
		DeadlineAt: &deadline,
		Players: []model.MultiplayerRuntimePlayer{
			{UserID: 7, CharacterID: 101}, {UserID: 8, CharacterID: 102},
		},
		RecentMessages: []model.RuntimeMessage{{Role: "assistant", Content: "opening"}},
	})

	wantTypes := []ws.MessageType{
		ws.MsgGameStarted, ws.MsgGameRuntimeSnapshot, ws.MsgNarrativeComplete, ws.MsgTurnStart,
	}
	for index, wantType := range wantTypes {
		select {
		case raw := <-client.Send:
			var message ws.Message
			if err := json.Unmarshal(raw, &message); err != nil || message.Type != wantType || message.Seq != int64(index+1) {
				t.Fatalf("message %d = %#v, err=%v", index, message, err)
			}
			if message.Type == ws.MsgTurnStart {
				var turn ws.TurnStartData
				if err := json.Unmarshal(message.Data, &turn); err != nil || turn.CurrentActorID != 7 || turn.DeadlineAt != deadline.Format(time.RFC3339Nano) {
					t.Fatalf("turn_start=%#v err=%v", turn, err)
				}
			}
			if message.Type == ws.MsgNarrativeComplete {
				var narrative ws.NarrativeCompleteData
				if err := json.Unmarshal(message.Data, &narrative); err != nil || narrative.Generation != "bc624606-57a3-49c5-bf51-f5a04e5f299f" {
					t.Fatalf("narrative_complete=%#v err=%v", narrative, err)
				}
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for %s", wantType)
		}
	}
}

func TestRoomRealtimePublishesMultiplayerPauseResumeAndEnd(t *testing.T) {
	hub := ws.NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)
	client := ws.NewClient(hub, nil, 7, 41)
	if !hub.Register(client) {
		t.Fatal("register failed")
	}
	select {
	case <-client.Send:
	case <-time.After(time.Second):
		t.Fatal("subscribed timeout")
	}
	realtime := NewRoomRealtime(hub)
	deadline := time.Date(2026, 9, 23, 10, 1, 0, 0, time.UTC)
	realtime.PublishMultiplayerLifecycle(&model.MultiplayerRuntimeSnapshot{
		RoomID: 41, Generation: "bc624606-57a3-49c5-bf51-f5a04e5f299f", Status: model.RoomStatusPaused,
		CurrentTurn: 4, RoundNumber: 2, CurrentActorID: 7,
	})
	realtime.PublishMultiplayerLifecycle(&model.MultiplayerRuntimeSnapshot{
		RoomID: 41, Generation: "32624606-57a3-49c5-bf51-f5a04e5f299f", Status: model.RoomStatusPlaying,
		CurrentTurn: 4, RoundNumber: 2, CurrentActorID: 7, DeadlineAt: &deadline,
	})
	realtime.PublishMultiplayerEnded(41, "42624606-57a3-49c5-bf51-f5a04e5f299f", 4, 2)
	want := []ws.MessageType{
		ws.MsgGameStatusChanged,
		ws.MsgGameStatusChanged, ws.MsgTurnStart,
		ws.MsgGameEnded,
	}
	for index, messageType := range want {
		select {
		case raw := <-client.Send:
			var message ws.Message
			if err := json.Unmarshal(raw, &message); err != nil || message.Type != messageType || message.Seq != int64(index+1) {
				t.Fatalf("message %d = %#v, err=%v", index, message, err)
			}
			if message.Type == ws.MsgGameStatusChanged && index == 1 {
				var status ws.GameStatusChangedData
				if err := json.Unmarshal(message.Data, &status); err != nil || status.Status != string(model.RoomStatusPlaying) ||
					status.DeadlineAt == nil || *status.DeadlineAt != deadline.Format(time.RFC3339Nano) {
					t.Fatalf("game status = %#v, err=%v", status, err)
				}
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for %s", messageType)
		}
	}
}

func TestRoomRealtimeBroadcastsMultiplayerActionToEveryClient(t *testing.T) {
	hub := ws.NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)
	clients := []*ws.Client{ws.NewClient(hub, nil, 7, 41), ws.NewClient(hub, nil, 8, 41)}
	for _, client := range clients {
		if !hub.Register(client) {
			t.Fatal("register failed")
		}
		select {
		case <-client.Send:
		case <-time.After(time.Second):
			t.Fatal("subscribed timeout")
		}
	}
	deadline := time.Now().UTC().Add(time.Minute)
	result := &SubmitGameActionResult{
		Narrative: "门开了", Generation: "bc624606-57a3-49c5-bf51-f5a04e5f299f",
		CurrentTurn: 1, CurrentActorID: 8, DeadlineAt: &deadline,
		MultiplayerEffects: &MultiplayerActionEffects{Players: []MultiplayerPlayerEffects{
			{UserID: 8, PlayerStateChanges: map[string]string{"hp": "5"}},
		}},
	}
	requestID := "550e8400-e29b-41d4-a716-446655440000"
	realtime := NewRoomRealtime(hub)
	for _, event := range []GameActionStreamEvent{
		{Type: "action_started", Generation: result.Generation, CurrentTurn: 0, PlayerID: 7},
		{Type: "narrative_chunk", Generation: result.Generation, CurrentTurn: 0, Content: "门"},
		{Type: "status_update", Generation: result.Generation, CurrentTurn: 1, PlayerID: 8, Result: result},
		{Type: "narrative_complete", Generation: result.Generation, CurrentTurn: 1, Result: result},
		{Type: "turn_start", Generation: result.Generation, CurrentTurn: 1, PlayerID: 8, Result: result},
	} {
		event.Multiplayer = true
		realtime.PublishMultiplayerActionEvent(41, requestID, event)
	}
	want := []ws.MessageType{ws.MsgActionStarted, ws.MsgNarrativeChunk, ws.MsgStatusUpdate, ws.MsgNarrativeComplete, ws.MsgTurnStart}
	for _, client := range clients {
		for index, messageType := range want {
			select {
			case raw := <-client.Send:
				var message ws.Message
				if err := json.Unmarshal(raw, &message); err != nil || message.Type != messageType ||
					message.Seq != int64(index+1) || message.RequestID != requestID {
					t.Fatalf("client %d event %d = %#v, err=%v", client.UserID, index, message, err)
				}
				if messageType == ws.MsgStatusUpdate {
					var update ws.StatusUpdateData
					if err := json.Unmarshal(message.Data, &update); err != nil || update.PlayerID != 8 || update.Generation != result.Generation {
						t.Fatalf("status update = %#v, err=%v", update, err)
					}
				}
			case <-time.After(time.Second):
				t.Fatalf("client %d missed %s", client.UserID, messageType)
			}
		}
	}
}
