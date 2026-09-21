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
