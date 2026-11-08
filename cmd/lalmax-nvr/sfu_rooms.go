package main

import (
	"context"
	"sort"
	"sync"

	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/q191201771/lalmax/sfu"
)

var nvrSFURoomLimits = sfu.RoomLimits{MaxParticipants: 32, MaxPublicationsPerParticipant: 3, MaxSubscriptions: 256, MaxWatchers: 32}

// nvrSFURoomAdmin owns the set of NVR rooms. The default room is retained for
// existing clients; additional rooms are explicit, bounded, and ephemeral.
type nvrSFURoomAdmin struct {
	mu      sync.Mutex
	manager *sfu.Manager
	policy  *nvrSFUPolicy
	entries map[string]*sfu.Room
	closed  bool
}

func newNVRSFURoomAdmin(m *sfu.Manager, room *sfu.Room, policy *nvrSFUPolicy) *nvrSFURoomAdmin {
	return &nvrSFURoomAdmin{manager: m, policy: policy, entries: map[string]*sfu.Room{"default": room}}
}
func validNVRSFURoomID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func (a *nvrSFURoomAdmin) authorize(ctx context.Context, principal string) error {
	if err := a.policy.check(ctx, principal); err != nil {
		return sfubridge.ErrDenied
	}
	return nil
}
func (a *nvrSFURoomAdmin) resolve(id string) (sfu.Key, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	room, ok := a.entries[id]
	if !ok || a.closed {
		return sfu.Key{}, "", false
	}
	snap := room.Snapshot()
	return snap.Key, snap.Generation, true
}
func (a *nvrSFURoomAdmin) ListRooms(ctx context.Context, principal string) ([]sfu.Snapshot, error) {
	if err := a.authorize(ctx, principal); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, sfubridge.ErrClosed
	}
	out := make([]sfu.Snapshot, 0, len(a.entries))
	for _, r := range a.entries {
		out = append(out, r.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.ID < out[j].Key.ID })
	return out, nil
}
func (a *nvrSFURoomAdmin) CreateRoom(ctx context.Context, principal, id string) (sfu.Snapshot, error) {
	if err := a.authorize(ctx, principal); err != nil {
		return sfu.Snapshot{}, err
	}
	if !validNVRSFURoomID(id) {
		return sfu.Snapshot{}, sfu.ErrInvalidID
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return sfu.Snapshot{}, sfubridge.ErrClosed
	}
	if _, exists := a.entries[id]; exists {
		return sfu.Snapshot{}, sfu.ErrRoomExists
	}
	if len(a.entries) >= 32 {
		return sfu.Snapshot{}, sfubridge.ErrLimit
	}
	room, err := a.manager.CreateWithLimits(sfu.Key{Namespace: nvrSFURoom.Namespace, ID: id}, nvrSFURoomLimits)
	if err != nil {
		return sfu.Snapshot{}, err
	}
	a.entries[id] = room
	return room.Snapshot(), nil
}
func (a *nvrSFURoomAdmin) CloseRoom(ctx context.Context, principal, id, generation string) error {
	if err := a.authorize(ctx, principal); err != nil {
		return err
	}
	if !validNVRSFURoomID(id) || generation == "" || id == "default" {
		return sfu.ErrInvalidID
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return sfubridge.ErrClosed
	}
	room, ok := a.entries[id]
	if !ok || room.Snapshot().Generation != generation {
		return sfu.ErrRoomNotFound
	}
	if err := a.manager.Close(sfu.Key{Namespace: nvrSFURoom.Namespace, ID: id}, generation); err != nil {
		return err
	}
	delete(a.entries, id)
	return nil
}
func (a *nvrSFURoomAdmin) closeAll() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	var result error
	for id, room := range a.entries {
		if err := a.manager.Close(sfu.Key{Namespace: nvrSFURoom.Namespace, ID: id}, room.Snapshot().Generation); err != nil {
			result = err
		}
		delete(a.entries, id)
	}
	return result
}
