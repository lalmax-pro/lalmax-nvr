package main

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/q191201771/lalmax/sfu"
)

// nvrRoomBridge is a room-generation-specific registry. The router never
// accepts a room identity from the caller when resolving a publication.
type nvrRoomBridge interface {
	Create(context.Context, string, string) (sfubridge.Status, error)
	CreateAV(context.Context, string, string, string) (sfubridge.Status, error)
	List(context.Context, string) ([]sfubridge.Status, error)
	Get(context.Context, string, string) (sfubridge.Status, error)
	Playback(context.Context, string, string) (string, string, error)
	Stop(context.Context, string, string) error
	Remove(context.Context, string, string) error
	Close(context.Context) error
	PruneFinished(time.Time)
	RevalidateOwners(context.Context, func(context.Context, string) error) error
}
type nvrRoomBridgeEntry struct {
	room     *sfu.Room
	registry nvrRoomBridge
}
type nvrSFUBridgeRouter struct {
	mu        sync.Mutex
	entries   map[*sfu.Room]*nvrRoomBridgeEntry
	retiring  map[*nvrRoomBridgeEntry]struct{}
	closed    bool
	authorize func(context.Context, string) error
}

func newNVRSFUBridgeRouter(check func(context.Context, string) error) *nvrSFUBridgeRouter {
	return &nvrSFUBridgeRouter{entries: make(map[*sfu.Room]*nvrRoomBridgeEntry), retiring: make(map[*nvrRoomBridgeEntry]struct{}), authorize: check}
}
func (r *nvrSFUBridgeRouter) add(room *sfu.Room, registry nvrRoomBridge) error {
	if room == nil || registry == nil {
		return sfubridge.ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return sfubridge.ErrClosed
	}
	if len(r.entries)+len(r.retiring) >= 64 {
		return sfubridge.ErrLimit
	}
	if _, ok := r.entries[room]; ok {
		return sfubridge.ErrInvalid
	}
	r.entries[room] = &nvrRoomBridgeEntry{room, registry}
	return nil
}

// remove unpublishes the routing entry before closing its registry. Any create
// racing removal is rejected by the recheck in create; Close cancels its work.
func (r *nvrSFUBridgeRouter) remove(ctx context.Context, room *sfu.Room) error {
	r.mu.Lock()
	entry := r.entries[room]
	delete(r.entries, room)
	if entry != nil {
		r.retiring[entry] = struct{}{}
	}
	r.mu.Unlock()
	if entry == nil {
		return nil
	}
	err := entry.registry.Close(ctx)
	if err == nil {
		r.mu.Lock()
		delete(r.retiring, entry)
		r.mu.Unlock()
	}
	return err
}
func (r *nvrSFUBridgeRouter) snapshot() []*nvrRoomBridgeEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	out := make([]*nvrRoomBridgeEntry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	return out
}
func (r *nvrSFUBridgeRouter) Create(ctx context.Context, pubOwner, pub string) (sfubridge.Status, error) {
	return r.create(ctx, pubOwner, pub, "")
}
func (r *nvrSFUBridgeRouter) CreateAV(ctx context.Context, principal, video, audio string) (sfubridge.Status, error) {
	if audio == "" || audio == video {
		return sfubridge.Status{}, sfubridge.ErrInvalid
	}
	return r.create(ctx, principal, video, audio)
}
func (r *nvrSFUBridgeRouter) create(ctx context.Context, principal, pub, audio string) (sfubridge.Status, error) {
	if ctx == nil || principal == "" || pub == "" {
		return sfubridge.Status{}, sfubridge.ErrInvalid
	}
	if err := r.authorize(ctx, principal); err != nil {
		return sfubridge.Status{}, sfubridge.ErrDenied
	}
	var match *nvrRoomBridgeEntry
	for _, entry := range r.snapshot() {
		for _, p := range entry.room.Snapshot().Publications {
			if p.ID == pub {
				if match != nil {
					return sfubridge.Status{}, sfubridge.ErrInvalid
				}
				match = entry
			}
		}
	}
	if match == nil {
		return sfubridge.Status{}, sfu.ErrPublicationNotFound
	}
	var status sfubridge.Status
	var err error
	if audio == "" {
		status, err = match.registry.Create(ctx, principal, pub)
	} else {
		status, err = match.registry.CreateAV(ctx, principal, pub, audio)
	}
	if err != nil {
		return sfubridge.Status{}, err
	}
	r.mu.Lock()
	valid := !r.closed && r.entries[match.room] == match
	r.mu.Unlock()
	if !valid {
		return sfubridge.Status{}, sfubridge.ErrClosed
	}
	return status, nil
}
func (r *nvrSFUBridgeRouter) Get(ctx context.Context, p, id string) (sfubridge.Status, error) {
	for _, e := range r.snapshot() {
		s, err := e.registry.Get(ctx, p, id)
		if !errors.Is(err, sfubridge.ErrNotFound) {
			return s, err
		}
	}
	return sfubridge.Status{}, sfubridge.ErrNotFound
}

// List spans active rooms while keeping each registry's ownership and view
// checks. A removed room is omitted even if its registry is still draining.
func (r *nvrSFUBridgeRouter) List(ctx context.Context, principal string) ([]sfubridge.Status, error) {
	if ctx == nil || principal == "" {
		return nil, sfubridge.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.authorize(ctx, principal); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, sfubridge.ErrDenied
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, sfubridge.ErrClosed
	}
	statuses := make([]sfubridge.Status, 0)
	for _, entry := range r.snapshot() {
		items, err := entry.registry.List(ctx, principal)
		r.mu.Lock()
		valid := !r.closed && r.entries[entry.room] == entry
		r.mu.Unlock()
		if !valid {
			continue
		}
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, items...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	closed = r.closed
	r.mu.Unlock()
	if closed {
		return nil, sfubridge.ErrClosed
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	return statuses, nil
}
func (r *nvrSFUBridgeRouter) Playback(ctx context.Context, p, id string) (string, string, error) {
	for _, e := range r.snapshot() {
		stream, token, err := e.registry.Playback(ctx, p, id)
		if !errors.Is(err, sfubridge.ErrNotFound) {
			return stream, token, err
		}
	}
	return "", "", sfubridge.ErrNotFound
}
func (r *nvrSFUBridgeRouter) Stop(ctx context.Context, p, id string) error {
	for _, e := range r.snapshot() {
		err := e.registry.Stop(ctx, p, id)
		if !errors.Is(err, sfubridge.ErrNotFound) {
			return err
		}
	}
	return sfubridge.ErrNotFound
}
func (r *nvrSFUBridgeRouter) Remove(ctx context.Context, p, id string) error {
	for _, e := range r.snapshot() {
		err := e.registry.Remove(ctx, p, id)
		if !errors.Is(err, sfubridge.ErrNotFound) {
			return err
		}
	}
	return sfubridge.ErrNotFound
}
func (r *nvrSFUBridgeRouter) PruneFinished(before time.Time) {
	for _, e := range r.snapshot() {
		e.registry.PruneFinished(before)
	}
}
func (r *nvrSFUBridgeRouter) RevalidateOwners(ctx context.Context, check func(context.Context, string) error) error {
	for _, e := range r.snapshot() {
		if err := e.registry.RevalidateOwners(ctx, check); err != nil {
			return err
		}
	}
	r.mu.Lock()
	retiring := make([]*nvrRoomBridgeEntry, 0, len(r.retiring))
	for e := range r.retiring {
		retiring = append(retiring, e)
	}
	r.mu.Unlock()
	for _, e := range retiring {
		if err := e.registry.Close(ctx); err != nil {
			return err
		}
		r.mu.Lock()
		delete(r.retiring, e)
		r.mu.Unlock()
	}
	return nil
}
func (r *nvrSFUBridgeRouter) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	entries := make([]*nvrRoomBridgeEntry, 0, len(r.entries))
	for _, e := range r.entries {
		entries = append(entries, e)
	}
	for e := range r.retiring {
		entries = append(entries, e)
	}
	r.mu.Unlock()
	var result error
	for _, e := range entries {
		result = errors.Join(result, e.registry.Close(ctx))
	}
	return result
}
