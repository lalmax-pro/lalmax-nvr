package storage

import (
 "context"
 "path/filepath"
 "testing"
)

func TestUserPreferencesAccountIsolationAndUpdate(t *testing.T) {
 ctx := context.Background()
 db, err := New(filepath.Join(t.TempDir(), "preferences.db"))
 if err != nil { t.Fatal(err) }
 defer db.Close()
 if err := db.Init(ctx); err != nil { t.Fatal(err) }
 if value, found, err := db.GetUserPreference(ctx, "alice", "dashboard"); err != nil || found || value != "" {
  t.Fatalf("missing preferences: value=%q found=%v err=%v", value, found, err)
 }
 if err := db.SetUserPreference(ctx, "alice", "dashboard", `{"layout":4,"slots":[null,null,null,null]}`); err != nil { t.Fatal(err) }
 if err := db.SetUserPreference(ctx, "bob", "dashboard", `{"layout":1,"slots":[null]}`); err != nil { t.Fatal(err) }
 if err := db.SetUserPreference(ctx, "alice", "dashboard", `{"layout":1,"slots":["camera-1"]}`); err != nil { t.Fatal(err) }
 if err := db.SetUserPreference(ctx, "alice", "dashboard", `{"layout":1,"slots":["camera-1"]}`); err != nil { t.Fatalf("idempotent repeat must not fail: %v",err) }
 value, found, err := db.GetUserPreference(ctx, "alice", "dashboard")
 if err != nil || !found || value != `{"layout":1,"slots":["camera-1"]}` {
  t.Fatalf("alice preference: value=%q found=%v err=%v", value, found, err)
 }
 value, found, err = db.GetUserPreference(ctx, "bob", "dashboard")
 if err != nil || !found || value != `{"layout":1,"slots":[null]}` {
  t.Fatalf("bob preference: value=%q found=%v err=%v", value, found, err)
 }
}
