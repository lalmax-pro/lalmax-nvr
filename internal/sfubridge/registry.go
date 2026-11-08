package sfubridge

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("sfu bridge not found")
var ErrNotFinished = errors.New("sfu bridge has not finished")
var ErrIdempotencyConflict = errors.New("sfu bridge idempotency key reused with different publications")

// Action is independently authorized for each management operation.
type Action string

const (
	ActionView     Action = "view"
	ActionStop     Action = "stop"
	ActionPlayback Action = "playback"
	ActionRemove   Action = "remove"
)

// ManagementRequest contains server-side identity and bridge scope. A policy
// must fail closed and not derive Principal from untrusted request parameters.
type ManagementRequest struct {
	Principal, Owner, BridgeID, PublicationID, AudioPublicationID, StreamID string
	Action                                                                  Action
}
type ManagementAuthorizer func(context.Context, ManagementRequest) error

type State string

const (
	StatePending  State = "pending" // accepted; publisher readiness is not yet exposed
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

type Status struct {
	ID, PublicationID, StreamID string
	AudioPublicationID          string
	State                       State
}
type registryEntry struct {
	owner      string
	status     Status
	handle     *Handle
	finishedAt time.Time
}

// Registry keeps bridge handles and credentials on the server. Authentication
// is the embedding application's responsibility. All operations check both
// ownership and a required policy. Terminal entries count toward maxEntries
// until explicitly removed or pruned, bounding memory while preserving failure status.
type Registry struct {
	mu         sync.Mutex
	service    *Service
	authorize  ManagementAuthorizer
	maxEntries int
	creating   int
	closed     bool
	entries    map[string]*registryEntry
}

func NewRegistry(service *Service, authorize ManagementAuthorizer, maxEntries int) (*Registry, error) {
	if service == nil || authorize == nil || maxEntries < 1 || maxEntries > 4096 {
		return nil, ErrInvalid
	}
	return &Registry{service: service, authorize: authorize, maxEntries: maxEntries, entries: make(map[string]*registryEntry)}, nil
}

// Create accepts a trusted authenticated principal. Success means queued;
// use Get to observe terminal failure or stop. The stream ID is server assigned.
func (r *Registry) Create(ctx context.Context, principal, publicationID string) (Status, error) {
	return r.create(ctx, principal, publicationID, "")
}

func (r *Registry) CreateAV(ctx context.Context, principal, videoID, audioID string) (Status, error) {
	if audioID == "" || audioID == videoID {
		return Status{}, ErrInvalid
	}
	return r.create(ctx, principal, videoID, audioID)
}

func (r *Registry) create(ctx context.Context, principal, publicationID, audioID string) (Status, error) {
	if ctx == nil || principal == "" || publicationID == "" {
		return Status{}, ErrInvalid
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return Status{}, ErrClosed
	}
	if len(r.entries)+r.creating >= r.maxEntries {
		r.mu.Unlock()
		return Status{}, ErrLimit
	}
	r.creating++
	r.mu.Unlock()
	var handle *Handle
	var err error
	if audioID == "" {
		handle, err = r.service.Start(ctx, principal, publicationID)
	} else {
		handle, err = r.service.StartAV(ctx, principal, publicationID, audioID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creating--
	if err != nil {
		return Status{}, err
	}
	if r.closed {
		handle.Stop()
		return Status{}, ErrClosed
	}
	status := Status{ID: handle.StreamID, StreamID: handle.StreamID, PublicationID: publicationID, AudioPublicationID: audioID, State: StatePending}
	entry := &registryEntry{owner: principal, status: status, handle: handle}
	r.entries[status.ID] = entry
	go r.observe(entry)
	return status, nil
}
func (r *Registry) observe(entry *registryEntry) {
	err := <-entry.handle.Done()
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry.status.State == StateStopping || errors.Is(err, context.Canceled) {
		entry.status.State = StateStopped
	} else {
		entry.status.State = StateFailed
	}
	// Do not retain the playback secret after the service has revoked the grant.
	entry.handle.PlaybackToken = ""
	entry.finishedAt = time.Now()
}

// PruneFinished removes terminal entries older than before. Active bridges
// are never removed; callers choose a retention window for status inspection.
func (r *Registry) PruneFinished(before time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, entry := range r.entries {
		if !entry.finishedAt.IsZero() && entry.finishedAt.Before(before) {
			delete(r.entries, id)
		}
	}
}
func (r *Registry) allowed(ctx context.Context, principal, id string, action Action) (*registryEntry, error) {
	if ctx == nil || principal == "" || id == "" {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry := r.entries[id]
	// Do not disclose whether another principal's bridge exists.
	if entry == nil || entry.owner != principal {
		return nil, ErrNotFound
	}
	req := ManagementRequest{Principal: principal, Owner: entry.owner, BridgeID: id, PublicationID: entry.status.PublicationID, AudioPublicationID: entry.status.AudioPublicationID, StreamID: entry.status.StreamID, Action: action}
	// Policies can be slow or call other services. Never hold the registry lock
	// during authorization; verify the entry still exists after reacquiring it.
	r.mu.Unlock()
	err := r.authorize(ctx, req)
	r.mu.Lock()
	if r.entries[id] != entry {
		return nil, ErrNotFound
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return entry, nil
}

var ErrDenied = errors.New("sfu bridge management denied")

// List returns only this principal's entries that pass the current view
// policy. Authorization is performed without holding the registry lock.
// Entries may disappear while the list is being built.
func (r *Registry) List(ctx context.Context, principal string) ([]Status, error) {
	if ctx == nil || principal == "" {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	ids := make([]string, 0, len(r.entries))
	for id, entry := range r.entries {
		if entry.owner == principal {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	sort.Strings(ids)
	statuses := make([]Status, 0, len(ids))
	for _, id := range ids {
		r.mu.Lock()
		entry, err := r.allowed(ctx, principal, id, ActionView)
		if err == nil {
			statuses = append(statuses, entry.status)
		}
		r.mu.Unlock()
		if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrDenied) {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return statuses, nil
}

// Get does not return playback credentials or raw runtime errors.
func (r *Registry) Get(ctx context.Context, principal, id string) (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, err := r.allowed(ctx, principal, id, ActionView)
	if err != nil {
		return Status{}, err
	}
	return entry.status, nil
}

// Playback returns a sensitive token; the embedding API must use authenticated
// transport, no-store responses and avoid logging the result.
func (r *Registry) Playback(ctx context.Context, principal, id string) (streamID, token string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, err := r.allowed(ctx, principal, id, ActionPlayback)
	if err != nil {
		return "", "", err
	}
	if entry.status.State != StatePending || entry.handle.PlaybackToken == "" {
		return "", "", ErrNotFound
	}
	return entry.status.StreamID, entry.handle.PlaybackToken, nil
}
func (r *Registry) Stop(ctx context.Context, principal, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, err := r.allowed(ctx, principal, id, ActionStop)
	if err != nil {
		return err
	}
	if entry.status.State == StatePending {
		entry.status.State = StateStopping
		entry.handle.Stop()
	}
	return nil
}
func (r *Registry) Remove(ctx context.Context, principal, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, err := r.allowed(ctx, principal, id, ActionRemove)
	if err != nil {
		return err
	}
	if entry.status.State != StateStopped && entry.status.State != StateFailed {
		return ErrNotFinished
	}
	delete(r.entries, id)
	return nil
}

// Close performs internal cleanup regardless of policy changes. It rejects new
// bridges and waits up to ctx's deadline for all underlying publishers to exit.
func (r *Registry) Close(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalid
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return r.service.Close(ctx)
}

// RevalidateOwners cancels active bridges whose owner no longer passes the
// supplied server-side authorization check. Callers must bound ctx and invoke
// this periodically when identity or role changes must revoke active media.
// Policy callbacks run outside the registry lock. Terminal records are kept.
func (r *Registry) RevalidateOwners(ctx context.Context, check func(context.Context, string) error) error {
	if ctx == nil || check == nil {
		return ErrInvalid
	}
	r.mu.Lock()
	owners := make(map[string]struct{})
	for _, entry := range r.entries {
		if entry.status.State == StatePending {
			owners[entry.owner] = struct{}{}
		}
	}
	r.mu.Unlock()
	for owner := range owners {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := check(ctx, owner); err != nil {
			r.mu.Lock()
			for _, entry := range r.entries {
				if entry.owner == owner && entry.status.State == StatePending {
					entry.status.State = StateStopping
					entry.handle.Stop()
				}
			}
			r.mu.Unlock()
		}
	}
	return ctx.Err()
}
