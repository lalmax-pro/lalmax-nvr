package main

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/livekit/protocol/livekit"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/bridge"
)

type liveKitRoomKey struct{ name, sid string }
type liveKitRoomRegistry struct {
	registry *sfubridge.Registry
	users    int
}
type liveKitBridgeRequest struct {
	room         liveKitRoomKey
	video, audio string
	done         chan struct{}
	status       sfubridge.Status
	err          error
	completed    time.Time
}
type nvrLiveKitBridgeRegistry struct {
	runtime   *nvrLiveKitRuntime
	publisher bridge.Publisher
	grants    *auth.MediaAuthorizer
	outputs   sfubridge.OutputStore
	mu        sync.Mutex
	closed    bool
	rooms     map[liveKitRoomKey]*liveKitRoomRegistry
	byID      map[string]*sfubridge.Registry
	requests  map[bridgeRequestKey]*liveKitBridgeRequest
}

func newNVRLiveKitBridgeRegistry(runtime *nvrLiveKitRuntime, publisher bridge.Publisher, grants *auth.MediaAuthorizer, outputs sfubridge.OutputStore) (*nvrLiveKitBridgeRegistry, error) {
	if runtime == nil || publisher == nil || grants == nil || outputs == nil {
		return nil, sfubridge.ErrInvalid
	}
	return &nvrLiveKitBridgeRegistry{runtime: runtime, publisher: publisher, grants: grants, outputs: outputs, rooms: make(map[liveKitRoomKey]*liveKitRoomRegistry), byID: make(map[string]*sfubridge.Registry), requests: make(map[bridgeRequestKey]*liveKitBridgeRequest)}, nil
}
func (*nvrLiveKitBridgeRegistry) Create(context.Context, string, string) (sfubridge.Status, error) {
	return sfubridge.Status{}, sfubridge.ErrInvalid
}
func (*nvrLiveKitBridgeRegistry) CreateAV(context.Context, string, string, string) (sfubridge.Status, error) {
	return sfubridge.Status{}, sfubridge.ErrInvalid
}

// CreateForRoom binds idempotency to principal, room generation and selected
// track SIDs. An accepted idempotent operation survives client disconnect.
func (m *nvrLiveKitBridgeRegistry) CreateForRoom(ctx context.Context, principal, room, sid, video, audio, key string) (sfubridge.Status, error) {
	if ctx == nil || principal == "" || !validNVRSFURoomID(room) || sid == "" || video == "" || (audio != "" && audio == video) {
		return sfubridge.Status{}, sfubridge.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return sfubridge.Status{}, err
	}
	if err := m.runtime.policy.check(ctx, principal); err != nil {
		return sfubridge.Status{}, sfubridge.ErrDenied
	}
	roomKey := liveKitRoomKey{room, sid}
	if key == "" {
		return m.create(ctx, principal, roomKey, video, audio)
	}
	k := bridgeRequestKey{principal, key}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return sfubridge.Status{}, sfubridge.ErrClosed
	}
	for k, v := range m.requests {
		if !v.completed.IsZero() && time.Since(v.completed) >= bridgeRequestRetention {
			delete(m.requests, k)
		}
	}
	req := m.requests[k]
	if req != nil && (req.room != roomKey || req.video != video || req.audio != audio) {
		m.mu.Unlock()
		return sfubridge.Status{}, sfubridge.ErrIdempotencyConflict
	}
	if req == nil {
		if len(m.requests) >= maxBridgeRequests {
			m.mu.Unlock()
			return sfubridge.Status{}, sfubridge.ErrLimit
		}
		req = &liveKitBridgeRequest{room: roomKey, video: video, audio: audio, done: make(chan struct{})}
		m.requests[k] = req
		operation, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		go func() {
			defer cancel()
			status, err := m.create(operation, principal, roomKey, video, audio)
			m.mu.Lock()
			req.status, req.err, req.completed = status, err, time.Now()
			close(req.done)
			m.mu.Unlock()
		}()
	}
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return sfubridge.Status{}, ctx.Err()
	case <-req.done:
		if req.err != nil {
			return sfubridge.Status{}, req.err
		}
		return m.Get(ctx, principal, req.status.ID)
	}
}
func (m *nvrLiveKitBridgeRegistry) ResolveIdempotent(ctx context.Context, principal, key string) (sfubridge.Status, error) {
	if ctx == nil || principal == "" || key == "" {
		return sfubridge.Status{}, sfubridge.ErrInvalid
	}
	if err := m.runtime.policy.check(ctx, principal); err != nil {
		return sfubridge.Status{}, sfubridge.ErrDenied
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return sfubridge.Status{}, sfubridge.ErrClosed
	}
	req := m.requests[bridgeRequestKey{principal, key}]
	if req == nil || (!req.completed.IsZero() && time.Since(req.completed) >= bridgeRequestRetention) {
		m.mu.Unlock()
		return sfubridge.Status{}, sfubridge.ErrNotFound
	}
	select {
	case <-req.done:
		status, err := req.status, req.err
		m.mu.Unlock()
		if err != nil {
			return sfubridge.Status{}, err
		}
		return m.Get(ctx, principal, status.ID)
	default:
		m.mu.Unlock()
		return sfubridge.Status{}, sfubridge.ErrRequestPending
	}
}
func (m *nvrLiveKitBridgeRegistry) create(ctx context.Context, principal string, key liveKitRoomKey, video, audio string) (sfubridge.Status, error) {
	if err := m.runtime.policy.check(ctx, principal); err != nil {
		return sfubridge.Status{}, sfubridge.ErrDenied
	}
	m.runtime.mu.Lock()
	server, closed := m.runtime.server, m.runtime.closed
	m.runtime.mu.Unlock()
	if closed || server == nil || !server.IsRunning() {
		return sfubridge.Status{}, sfubridge.ErrClosed
	}
	room := server.RoomManager().GetRoom(ctx, livekit.RoomName(key.name))
	if room == nil || string(room.ID()) != key.sid {
		return sfubridge.Status{}, sfu.ErrRoomNotFound
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return sfubridge.Status{}, sfubridge.ErrClosed
	}
	roomRegistry := m.rooms[key]
	if roomRegistry == nil {
		if len(m.rooms) >= 64 {
			m.mu.Unlock()
			return sfubridge.Status{}, sfubridge.ErrLimit
		}
		service, err := newLiveKitBridgeService(m.runtime, key.name, key.sid, m.publisher, m.grants, m.outputs)
		if err != nil {
			m.mu.Unlock()
			return sfubridge.Status{}, err
		}
		registry, err := sfubridge.NewRegistry(service, func(ctx context.Context, r sfubridge.ManagementRequest) error {
			if r.Principal != r.Owner {
				return sfubridge.ErrDenied
			}
			return m.runtime.policy.check(ctx, r.Principal)
		}, 64)
		if err != nil {
			m.mu.Unlock()
			return sfubridge.Status{}, err
		}
		roomRegistry = &liveKitRoomRegistry{registry: registry}
		m.rooms[key] = roomRegistry
	}
	roomRegistry.users++
	registry := roomRegistry.registry
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		roomRegistry.users--
		m.mu.Unlock()
	}()
	var status sfubridge.Status
	var err error
	if audio == "" {
		status, err = registry.Create(ctx, principal, video)
	} else {
		status, err = registry.CreateAV(ctx, principal, video, audio)
	}
	if err != nil {
		return sfubridge.Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.rooms[key] != roomRegistry {
		return sfubridge.Status{}, sfubridge.ErrClosed
	}
	m.byID[status.ID] = registry
	return status, nil
}
func (m *nvrLiveKitBridgeRegistry) find(id string) (*sfubridge.Registry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, sfubridge.ErrClosed
	}
	r := m.byID[id]
	if r == nil {
		return nil, sfubridge.ErrNotFound
	}
	return r, nil
}
func (m *nvrLiveKitBridgeRegistry) Get(ctx context.Context, p, id string) (sfubridge.Status, error) {
	r, e := m.find(id)
	if e != nil {
		return sfubridge.Status{}, e
	}
	return r.Get(ctx, p, id)
}
func (m *nvrLiveKitBridgeRegistry) Playback(ctx context.Context, p, id string) (string, string, error) {
	r, e := m.find(id)
	if e != nil {
		return "", "", e
	}
	return r.Playback(ctx, p, id)
}
func (m *nvrLiveKitBridgeRegistry) Stop(ctx context.Context, p, id string) error {
	r, e := m.find(id)
	if e != nil {
		return e
	}
	return r.Stop(ctx, p, id)
}
func (m *nvrLiveKitBridgeRegistry) Remove(ctx context.Context, p, id string) error {
	r, e := m.find(id)
	if e != nil {
		return e
	}
	if e = r.Remove(ctx, p, id); e != nil {
		return e
	}
	m.mu.Lock()
	if m.byID[id] == r {
		delete(m.byID, id)
	}
	m.mu.Unlock()
	return nil
}
func (m *nvrLiveKitBridgeRegistry) List(ctx context.Context, p string) ([]sfubridge.Status, error) {
	if err := m.runtime.policy.check(ctx, p); err != nil {
		return nil, sfubridge.ErrDenied
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, sfubridge.ErrClosed
	}
	all := make([]*sfubridge.Registry, 0, len(m.rooms))
	for _, r := range m.rooms {
		all = append(all, r.registry)
	}
	m.mu.Unlock()
	var out []sfubridge.Status
	for _, r := range all {
		items, err := r.List(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (m *nvrLiveKitBridgeRegistry) monitor(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if p, ok := m.publisher.(*nvrSFUPublisher); ok && p.engine.Server() != p.server {
				closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = m.Close(closeCtx)
				cancel()
				return
			}
			m.maintain(ctx, time.Now().Add(-5*time.Minute))
		}
	}
}

func (m *nvrLiveKitBridgeRegistry) maintain(ctx context.Context, before time.Time) {
	m.mu.Lock()
	all := make(map[liveKitRoomKey]*liveKitRoomRegistry, len(m.rooms))
	for key, r := range m.rooms {
		all[key] = r
	}
	m.mu.Unlock()
	for key, room := range all {
		r := room.registry
		check, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = r.RevalidateOwners(check, m.runtime.policy.check)
		cancel()
		m.mu.Lock()
		if m.rooms[key] == room && room.users == 0 {
			for _, id := range r.PruneFinishedIDs(before) {
				if m.byID[id] == r {
					delete(m.byID, id)
				}
			}
		}
		empty := m.rooms[key] == room && room.users == 0 && r.Empty()
		if empty {
			delete(m.rooms, key)
		}
		m.mu.Unlock()
		if empty {
			check, cancel := context.WithTimeout(ctx, 3*time.Second)
			_ = r.Close(check)
			cancel()
		}
	}
}
func (m *nvrLiveKitBridgeRegistry) Close(ctx context.Context) error {
	if ctx == nil {
		return sfubridge.ErrInvalid
	}
	m.mu.Lock()
	m.closed = true
	all := make([]*sfubridge.Registry, 0, len(m.rooms))
	for _, r := range m.rooms {
		all = append(all, r.registry)
	}
	m.mu.Unlock()
	var result error
	for _, r := range all {
		result = errors.Join(result, r.Close(ctx))
	}
	return result
}
