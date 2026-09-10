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

func TestFriendServiceMySQL84Concurrency(t *testing.T) {
	dsn := os.Getenv("TRPG_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TRPG_TEST_MYSQL_DSN is not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connect mysql: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(32)
	t.Cleanup(func() { _ = sqlDB.Close() })

	suffix := time.Now().UnixNano()
	users := []model.User{
		{Username: fmt.Sprintf("fa%d", suffix), Email: fmt.Sprintf("fa%d@example.test", suffix), PasswordHash: "integration"},
		{Username: fmt.Sprintf("fb%d", suffix), Email: fmt.Sprintf("fb%d@example.test", suffix), PasswordHash: "integration"},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	a, b := users[0].ID, users[1].ID
	t.Cleanup(func() {
		db.Where("user_low_id IN ? OR user_high_id IN ?", []uint{a, b}, []uint{a, b}).Delete(&model.Friendship{})
		db.Unscoped().Where("id IN ?", []uint{a, b}).Delete(&model.User{})
	})

	newService := func() *FriendService {
		return NewFriendService(repo.NewFriendRepo(db), repo.NewUserRepo(db), OfflinePresenceProvider{}, nil)
	}
	clearPair := func(t *testing.T) {
		t.Helper()
		if err := db.Where("user_low_id = ? AND user_high_id = ?", a, b).Delete(&model.Friendship{}).Error; err != nil {
			t.Fatal(err)
		}
	}
	loadPair := func(t *testing.T) model.Friendship {
		t.Helper()
		var row model.Friendship
		if err := db.Where("user_low_id = ? AND user_high_id = ?", a, b).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}

	t.Run("same direction creates one pending row", func(t *testing.T) {
		clearPair(t)
		svc := newService()
		start := make(chan struct{})
		errs := make(chan error, 16)
		var wg sync.WaitGroup
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := svc.SendRequest(context.Background(), a, b)
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent send: %v", err)
			}
		}
		row := loadPair(t)
		if row.Status != model.FriendshipStatusPending || row.RequestedBy != a {
			t.Fatalf("row = %#v", row)
		}
		var count int64
		db.Model(&model.Friendship{}).Where("user_low_id = ? AND user_high_id = ?", a, b).Count(&count)
		if count != 1 {
			t.Fatalf("pair row count = %d", count)
		}
	})

	t.Run("reverse requests converge to accepted", func(t *testing.T) {
		clearPair(t)
		svc := newService()
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, pair := range [][2]uint{{a, b}, {b, a}} {
			pair := pair
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := svc.SendRequest(context.Background(), pair[0], pair[1])
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("reverse request: %v", err)
			}
		}
		if row := loadPair(t); row.Status != model.FriendshipStatusAccepted {
			t.Fatalf("row = %#v", row)
		}
	})

	t.Run("accept reject race has one winner", func(t *testing.T) {
		clearPair(t)
		svc := newService()
		request, err := svc.SendRequest(context.Background(), a, b)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, accept := range []bool{true, false} {
			accept := accept
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := svc.RespondRequest(context.Background(), b, request.ID, accept)
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		successes, conflicts := 0, 0
		for err := range errs {
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrFriendStateConflict):
				conflicts++
			default:
				t.Fatalf("unexpected race error: %v", err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
		}
		row := loadPair(t)
		if row.Status != model.FriendshipStatusAccepted && row.Status != model.FriendshipStatusRejected {
			t.Fatalf("row = %#v", row)
		}
	})

	t.Run("delete and reapply reuse stable row", func(t *testing.T) {
		clearPair(t)
		svc := newService()
		request, _ := svc.SendRequest(context.Background(), a, b)
		_, err := svc.RespondRequest(context.Background(), b, request.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.DeleteFriend(context.Background(), a, b); err != nil {
			t.Fatal(err)
		}
		reapplied, err := svc.SendRequest(context.Background(), b, a)
		if err != nil {
			t.Fatal(err)
		}
		if reapplied.ID != request.ID || reapplied.Status != model.FriendshipStatusPending || reapplied.RequestedBy != b {
			t.Fatalf("reapplied = %#v", reapplied)
		}
	})
}
