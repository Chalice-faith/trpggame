//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

func TestRoomServiceMySQL84CreateJoinAndCapacity(t *testing.T) {
	dsn := os.Getenv("TRPG_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TRPG_TEST_MYSQL_DSN is not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(20)
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, table := range []string{"game_rooms", "room_players", "scripts", "script_characters"} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("required table %s is missing", table)
		}
	}
	suffix := time.Now().UnixNano()
	users := make([]model.User, 7)
	for i := range users {
		users[i] = model.User{Username: fmt.Sprintf("r%d_%d", suffix, i), Email: fmt.Sprintf("r%d_%d@example.test", suffix, i), PasswordHash: "integration"}
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	userIDs := make([]uint, len(users))
	for i := range users {
		userIDs[i] = users[i].ID
	}
	script := model.Script{UserID: users[0].ID, Title: "room integration", FilePath: "test", Status: model.ScriptStatusReady}
	if err := db.Create(&script).Error; err != nil {
		t.Fatal(err)
	}
	soloRoom := model.GameRoom{Name: "Legacy solo shape", ScriptID: script.ID, OwnerID: users[0].ID,
		Status: model.RoomStatusWaiting, MaxPlayers: 1, TurnOrder: []byte("[]"), IsSolo: true}
	if err := db.Create(&soloRoom).Error; err != nil {
		t.Fatalf("solo room compatibility: %v", err)
	}
	characters := make([]model.ScriptCharacter, 4)
	for i := range characters {
		characters[i] = model.ScriptCharacter{ScriptID: script.ID, Name: fmt.Sprintf("Role %d", i), Attributes: "{}"}
	}
	if err := db.Create(&characters).Error; err != nil {
		t.Fatal(err)
	}
	var roomID, roleRaceRoomID uint
	t.Cleanup(func() {
		db.Delete(&model.GameRoom{}, soloRoom.ID)
		if roleRaceRoomID != 0 {
			db.Where("room_id = ?", roleRaceRoomID).Delete(&model.RoomPlayer{})
			db.Delete(&model.GameRoom{}, roleRaceRoomID)
		}
		if roomID != 0 {
			db.Where("room_id = ?", roomID).Delete(&model.RoomPlayer{})
			db.Delete(&model.GameRoom{}, roomID)
		}
		db.Where("script_id = ?", script.ID).Delete(&model.ScriptCharacter{})
		db.Unscoped().Delete(&script)
		db.Unscoped().Where("id IN ?", userIDs).Delete(&model.User{})
	})
	svc := NewRoomService(repo.NewRoomRepo(db))
	ctx := context.Background()
	if _, err := svc.Create(ctx, users[0].ID, script.ID, "Too many", 5); !errors.Is(err, ErrRoomCharactersInsufficient) {
		t.Fatalf("character shortage: %v", err)
	}
	if _, err := svc.Create(ctx, users[1].ID, script.ID, "Not mine", 4); !errors.Is(err, ErrRoomScriptUnavailable) {
		t.Fatalf("foreign script: %v", err)
	}
	created, err := svc.Create(ctx, users[0].ID, script.ID, "Lobby", 4)
	if err != nil {
		t.Fatal(err)
	}
	roomID = created.ID
	if created.Version != 1 || len(created.RoomCode) != 8 || len(created.Members) != 1 || len(created.Characters) != 4 {
		t.Fatalf("create snapshot: %#v", created)
	}
	if _, err := svc.Get(ctx, users[1].ID, roomID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("outsider get: %v", err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for i := 1; i < len(users); i++ {
		userID := users[i].ID
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := svc.Join(ctx, userID, created.RoomCode); results <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	success, full := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrRoomCapacityReached):
			full++
		default:
			t.Fatalf("unexpected concurrent join error: %v", err)
		}
	}
	if success != 3 || full != 3 {
		t.Fatalf("join outcomes: success=%d full=%d", success, full)
	}
	snapshot, err := svc.Get(ctx, users[0].ID, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Members) != 4 || snapshot.Version != 4 {
		t.Fatalf("joined snapshot: %#v", snapshot)
	}
	joinedID := snapshot.Members[1].User.ID
	repeat, err := svc.Join(ctx, joinedID, created.RoomCode)
	if err != nil || repeat.Version != snapshot.Version {
		t.Fatalf("idempotent join: %#v, %v", repeat, err)
	}
	listed, err := svc.List(ctx, joinedID)
	if err != nil || len(listed) != 1 || listed[0].ID != roomID {
		t.Fatalf("joined list: %#v, %v", listed, err)
	}
	ownerRooms, err := svc.List(ctx, users[0].ID)
	if err != nil || len(ownerRooms) != 1 || ownerRooms[0].ID != roomID {
		t.Fatalf("solo leaked into multiplayer list: %#v, %v", ownerRooms, err)
	}
	ownerID := users[0].ID
	if _, err := svc.Leave(ctx, ownerID, roomID, snapshot.Version); !errors.Is(err, ErrRoomOwnerLeave) {
		t.Fatalf("owner leave before transfer: %v", err)
	}
	selected, err := svc.SelectCharacter(ctx, ownerID, roomID, characters[0].ID, snapshot.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SelectCharacter(ctx, joinedID, roomID, characters[0].ID, selected.Version); !errors.Is(err, ErrRoomCharacterTaken) {
		t.Fatalf("duplicate character: %v", err)
	}
	if _, err := svc.SetReady(ctx, ownerID, roomID, true, snapshot.Version); !errors.Is(err, repo.ErrRoomVersionConflict) {
		t.Fatalf("stale version: %v", err)
	}
	selected, err = svc.SelectCharacter(ctx, joinedID, roomID, characters[1].ID, selected.Version)
	if err != nil {
		t.Fatal(err)
	}
	selected, err = svc.SetReady(ctx, ownerID, roomID, true, selected.Version)
	if err != nil {
		t.Fatal(err)
	}
	selected, err = svc.SetReady(ctx, joinedID, roomID, true, selected.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(ctx, ownerID, roomID, selected.Version); !errors.Is(err, ErrRoomStartConditions) {
		t.Fatalf("start while others unready: %v", err)
	}
	transferResults := make(chan struct {
		snapshot *RoomSnapshot
		err      error
	}, 2)
	var transferWG sync.WaitGroup
	for i := 0; i < 2; i++ {
		transferWG.Add(1)
		go func() {
			defer transferWG.Done()
			result, err := svc.Transfer(ctx, ownerID, roomID, joinedID, selected.Version)
			transferResults <- struct {
				snapshot *RoomSnapshot
				err      error
			}{result, err}
		}()
	}
	transferWG.Wait()
	close(transferResults)
	transferWins, transferConflicts := 0, 0
	for result := range transferResults {
		if result.err == nil {
			transferWins++
			selected = result.snapshot
		} else if errors.Is(result.err, repo.ErrRoomVersionConflict) {
			transferConflicts++
		} else {
			t.Fatalf("concurrent transfer: %v", result.err)
		}
	}
	if transferWins != 1 || transferConflicts != 1 || selected.OwnerID != joinedID {
		t.Fatalf("transfer outcomes: wins=%d conflicts=%d snapshot=%#v", transferWins, transferConflicts, selected)
	}
	for _, member := range selected.Members {
		if member.IsReady {
			t.Fatalf("transfer did not clear ready: %#v", member)
		}
	}
	selected, err = svc.Leave(ctx, ownerID, roomID, selected.Version)
	if err != nil || len(selected.Members) != 3 {
		t.Fatalf("former owner leave: %#v, %v", selected, err)
	}
	if _, err := svc.Get(ctx, ownerID, roomID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("former member access: %v", err)
	}
	selected, err = svc.Join(ctx, ownerID, created.RoomCode)
	if err != nil || len(selected.Members) != 4 {
		t.Fatalf("rejoin: %#v, %v", selected, err)
	}
	selected, err = svc.Remove(ctx, joinedID, roomID, ownerID, selected.Version)
	if err != nil || len(selected.Members) != 3 {
		t.Fatalf("remove: %#v, %v", selected, err)
	}
	if _, err := svc.Join(ctx, ownerID, created.RoomCode); !errors.Is(err, ErrRoomRejoinDenied) {
		t.Fatalf("removed rejoin: %v", err)
	}
	for _, member := range selected.Members {
		if member.CharacterID == nil {
			characterID := characters[2].ID
			if member.User.ID != selected.Members[1].User.ID {
				characterID = characters[3].ID
			}
			selected, err = svc.SelectCharacter(ctx, member.User.ID, roomID, characterID, selected.Version)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, member := range selected.Members {
		selected, err = svc.SetReady(ctx, member.User.ID, roomID, true, selected.Version)
		if err != nil {
			t.Fatal(err)
		}
	}
	startResults := make(chan struct {
		snapshot *RoomSnapshot
		err      error
	}, 2)
	var startWG sync.WaitGroup
	for i := 0; i < 2; i++ {
		startWG.Add(1)
		go func() {
			defer startWG.Done()
			result, err := svc.Start(ctx, joinedID, roomID, selected.Version)
			startResults <- struct {
				snapshot *RoomSnapshot
				err      error
			}{result, err}
		}()
	}
	startWG.Wait()
	close(startResults)
	var started *RoomSnapshot
	startWins, startConflicts := 0, 0
	for result := range startResults {
		if result.err == nil {
			startWins++
			started = result.snapshot
		} else if errors.Is(result.err, repo.ErrRoomVersionConflict) {
			startConflicts++
		} else {
			t.Fatalf("concurrent start: %v", result.err)
		}
	}
	if startWins != 1 || startConflicts != 1 || started.Status != model.RoomStatusPlaying || started.Version != selected.Version+1 {
		t.Fatalf("start outcomes: wins=%d conflicts=%d snapshot=%#v", startWins, startConflicts, started)
	}
	if _, err := svc.Leave(ctx, selected.Members[1].User.ID, roomID, started.Version); !errors.Is(err, ErrRoomNotWaiting) {
		t.Fatalf("post-start leave: %v", err)
	}
	roleRoom, err := svc.Create(ctx, ownerID, script.ID, "Role race", 4)
	if err != nil {
		t.Fatal(err)
	}
	roleRaceRoomID = roleRoom.ID
	roleRoom, err = svc.Join(ctx, users[1].ID, roleRoom.RoomCode)
	if err != nil {
		t.Fatal(err)
	}
	roleRoom, err = svc.Join(ctx, users[2].ID, roleRoom.RoomCode)
	if err != nil {
		t.Fatal(err)
	}
	roleResults := make(chan error, 2)
	var roleWG sync.WaitGroup
	for _, actorID := range []uint{users[1].ID, users[2].ID} {
		actorID := actorID
		roleWG.Add(1)
		go func() {
			defer roleWG.Done()
			_, err := svc.SelectCharacter(ctx, actorID, roleRaceRoomID, characters[0].ID, roleRoom.Version)
			roleResults <- err
		}()
	}
	roleWG.Wait()
	close(roleResults)
	roleWins, roleConflicts := 0, 0
	for err := range roleResults {
		if err == nil {
			roleWins++
		} else if errors.Is(err, repo.ErrRoomVersionConflict) {
			roleConflicts++
		} else {
			t.Fatalf("concurrent role choice: %v", err)
		}
	}
	if roleWins != 1 || roleConflicts != 1 {
		t.Fatalf("role outcomes: wins=%d conflicts=%d", roleWins, roleConflicts)
	}
	var assigned int64
	if err := db.Model(&model.RoomPlayer{}).Where("room_id = ? AND character_id = ? AND status = ?", roleRaceRoomID, characters[0].ID, model.RoomPlayerStatusActive).Count(&assigned).Error; err != nil || assigned != 1 {
		t.Fatalf("active unique role count=%d err=%v", assigned, err)
	}
}
