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

func (r *RoomRealtime) PublishMultiplayerActionEvent(roomID uint, requestID string, event GameActionStreamEvent) {
	if r == nil || r.hub == nil || roomID == 0 || !event.Multiplayer || event.Generation == "" {
		return
	}
	var messageType ws.MessageType
	var payload any
	switch event.Type {
	case "action_started":
		messageType = ws.MsgActionStarted
		payload = ws.ActionStartedData{Generation: event.Generation, CurrentTurn: event.CurrentTurn, PlayerID: event.PlayerID}
	case "action_cancelled":
		messageType = ws.MsgActionCancelled
		payload = ws.ActionCancelledData{Generation: event.Generation, CurrentTurn: event.CurrentTurn, PlayerID: event.PlayerID, Reason: event.Reason}
	case "narrative_chunk":
		messageType = ws.MsgNarrativeChunk
		payload = ws.NarrativeChunkData{Generation: event.Generation, CurrentTurn: event.CurrentTurn, Content: event.Content}
	case "dice_roll":
		if event.Result == nil || event.Result.DiceRoll == nil {
			return
		}
		messageType = ws.MsgDiceRoll
		payload = struct {
			Generation  string          `json:"generation"`
			CurrentTurn int             `json:"current_turn"`
			DiceRoll    *ActionDiceRoll `json:"dice_roll"`
		}{event.Generation, event.CurrentTurn, event.Result.DiceRoll}
	case "status_update":
		if event.Result == nil || event.Result.MultiplayerEffects == nil {
			return
		}
		var effects *MultiplayerPlayerEffects
		for index := range event.Result.MultiplayerEffects.Players {
			if event.Result.MultiplayerEffects.Players[index].UserID == event.PlayerID {
				effects = &event.Result.MultiplayerEffects.Players[index]
				break
			}
		}
		if effects == nil {
			return
		}
		messageType = ws.MsgStatusUpdate
		payload = ws.StatusUpdateData{Generation: event.Generation, CurrentTurn: event.CurrentTurn, PlayerID: event.PlayerID, Changes: map[string]any{
			"player_state_changes": effects.PlayerStateChanges, "items": effects.Items, "buffs": effects.Buffs,
		}}
	case "narrative_complete":
		if event.Result == nil {
			return
		}
		messageType = ws.MsgNarrativeComplete
		payload = ws.NarrativeCompleteData{Generation: event.Generation, Narrative: event.Result.Narrative, CurrentTurn: event.Result.CurrentTurn}
	case "turn_skip":
		messageType = ws.MsgTurnSkip
		payload = ws.TurnSkipData{Generation: event.Generation, SkippedUserID: event.PlayerID, CurrentTurn: event.CurrentTurn, Reason: event.Reason}
	case "turn_start":
		if event.Result == nil || event.Result.DeadlineAt == nil {
			return
		}
		messageType = ws.MsgTurnStart
		payload = ws.TurnStartData{Generation: event.Generation, CurrentTurn: event.Result.CurrentTurn,
			RoundNumber: event.Result.RoundNumber, CurrentActorID: event.Result.CurrentActorID,
			DeadlineAt: event.Result.DeadlineAt.UTC().Format(time.RFC3339Nano)}
	default:
		return
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	r.hub.BroadcastToRoomWithRequestID(roomID, messageType, encoded, requestID)
}
