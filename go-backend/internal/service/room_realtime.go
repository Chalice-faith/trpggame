package service

import (
	"encoding/json"
	"log"
	"time"

	"trpggame/internal/model"
	"trpggame/internal/ws"
)

// RoomRealtime publishes only committed lobby mutations. Delivery is best-effort;
// REST/MySQL remains authoritative and reconnecting clients recover with a snapshot.
type RoomRealtime struct{ hub *ws.Hub }

func NewRoomRealtime(hub *ws.Hub) *RoomRealtime { return &RoomRealtime{hub: hub} }

func (r *RoomRealtime) PublishRoomMutation(mutation RoomMutation) {
	if r == nil || r.hub == nil || mutation.Snapshot == nil || mutation.Type == "" {
		return
	}
	payload, err := json.Marshal(mutation.Snapshot)
	if err != nil {
		log.Printf("room realtime marshal: %v", err)
		return
	}
	messageType := ws.MessageType(mutation.Type)
	if mutation.RevokeAccess && mutation.AffectedUserID != 0 {
		r.hub.BroadcastAndRevoke(mutation.Snapshot.ID, mutation.AffectedUserID, messageType, payload)
		return
	}
	r.hub.BroadcastToRoom(mutation.Snapshot.ID, messageType, payload)
}

// PublishMultiplayerRuntime publishes the activated V2 baseline, opening narrative and first turn.
func (r *RoomRealtime) PublishMultiplayerRuntime(snapshot *model.MultiplayerRuntimeSnapshot) {
	if r == nil || r.hub == nil || snapshot == nil || snapshot.DeadlineAt == nil || len(snapshot.RecentMessages) == 0 {
		return
	}
	runtimePayload, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	r.hub.BroadcastToRoom(snapshot.RoomID, ws.MsgGameRuntimeSnapshot, runtimePayload)
	opening := snapshot.RecentMessages[len(snapshot.RecentMessages)-1]
	narrativePayload, err := json.Marshal(ws.NarrativeCompleteData{
		Generation: snapshot.Generation, Narrative: opening.Content, CurrentTurn: snapshot.CurrentTurn,
	})
	if err == nil {
		r.hub.BroadcastToRoom(snapshot.RoomID, ws.MsgNarrativeComplete, narrativePayload)
	}
	turnPayload, err := json.Marshal(ws.TurnStartData{
		Generation: snapshot.Generation, CurrentTurn: snapshot.CurrentTurn,
		RoundNumber: snapshot.RoundNumber, CurrentActorID: snapshot.CurrentActorID,
		DeadlineAt: snapshot.DeadlineAt.UTC().Format(time.RFC3339Nano),
	})
	if err == nil {
		r.hub.BroadcastToRoom(snapshot.RoomID, ws.MsgTurnStart, turnPayload)
	}
}
