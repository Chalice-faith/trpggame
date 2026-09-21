package service

import (
	"encoding/json"
	"log"

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
