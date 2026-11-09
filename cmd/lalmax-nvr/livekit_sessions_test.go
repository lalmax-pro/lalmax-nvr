package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
)

func TestNVRLiveKitSessionRetentionAndLimit(t *testing.T) {
	sessions := newNVRLiveKitSessions(nil)
	if err := sessions.register("nvr_first", "user:1", "room", "RM_one"); err != nil {
		t.Fatal(err)
	}
	if err := sessions.register("nvr_first", "user:1", "room", "RM_one"); err == nil {
		t.Fatal("accepted a duplicate session identity")
	}
	now := time.Now()
	old := sessions.items["nvr_first"]
	old.retainUntil = now.Add(-time.Second)
	sessions.items["nvr_first"] = old
	renewed := old
	renewed.retainUntil = now.Add(time.Minute)
	sessions.items["nvr_first"] = renewed
	sessions.pruneExpired("nvr_first", old, now)
	if _, ok := sessions.items["nvr_first"]; !ok {
		t.Fatal("pruned a session renewed after the snapshot")
	}
	sessions.items["nvr_first"] = old
	sessions.pruneExpired("nvr_first", old, now)
	if _, ok := sessions.items["nvr_first"]; ok {
		t.Fatal("retained an expired inactive session")
	}
	for i := 0; i < maxLiveKitSessions; i++ {
		if err := sessions.register(fmt.Sprintf("nvr_%d", i), "user:1", "room", "RM_one"); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions.register("nvr_overflow", "user:1", "room", "RM_one"); err == nil {
		t.Fatal("session capacity was not enforced")
	}
}

func TestNVRLiveKitSessionNamespace(t *testing.T) {
	sessions := newNVRLiveKitSessions(nil)
	if err := sessions.authorize(context.Background(), &auth.ClaimGrants{Identity: "external-client", Video: &auth.VideoGrant{}}, ""); err != nil {
		t.Fatalf("non-NVR signed identities should use native LiveKit policy: %v", err)
	}
	if err := sessions.authorize(context.Background(), &auth.ClaimGrants{Identity: "nvr_unknown", Video: &auth.VideoGrant{Room: "room"}}, ""); err == nil {
		t.Fatal("accepted an unknown reserved NVR identity")
	}
}
