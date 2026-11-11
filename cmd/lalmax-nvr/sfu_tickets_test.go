package main

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

func TestSFUTicketsSingleUseExpiryAndRevocation(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "nvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Init(ctx); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleSuperAdmin, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	tickets := newNVRSFUTickets(&nvrSFUPolicy{app: &App{db: db, cfg: &config.Config{}}, generation: "room-generation"})
	value, err := tickets.Issue(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if identity, err := tickets.consume(ctx, value); err == nil {
				if identity.RoomGeneration != "room-generation" {
					t.Errorf("ticket lost room generation: %q", identity.RoomGeneration)
				}
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("ticket accepted %d times", successes.Load())
	}
	value, err = tickets.Issue(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	tickets.mu.Lock()
	entry := tickets.tickets[value]
	entry.expires = time.Now().Add(-time.Second)
	tickets.tickets[value] = entry
	tickets.mu.Unlock()
	if _, err = tickets.consume(ctx, value); err == nil {
		t.Fatal("expired ticket accepted")
	}
	value, err = tickets.Issue(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	user.Role = model.RoleUser
	user.UpdatedAt = time.Now()
	if err = db.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err = tickets.consume(ctx, value); err == nil {
		t.Fatal("demoted user accepted")
	}
	tickets.Close()
	if _, err = tickets.Issue(ctx, user); err == nil {
		t.Fatal("closed issuer accepted user")
	}
}
