package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/q191201771/lalmax/sfu/signaling"
)

type nvrSFUTicket struct {
	identity signaling.Identity
	expires  time.Time
}
type nvrSFUTickets struct {
	mu      sync.Mutex
	closed  bool
	policy  *nvrSFUPolicy
	tickets map[string]nvrSFUTicket
}

func newNVRSFUTickets(policy *nvrSFUPolicy) *nvrSFUTickets {
	return &nvrSFUTickets{policy: policy, tickets: make(map[string]nvrSFUTicket)}
}

// Issue accepts only server-authenticated user data and returns a 30-second,
// single-use random ticket; user passwords are never embedded in it.
func (t *nvrSFUTickets) Issue(ctx context.Context, user *model.User) (string, error) {
	if ctx == nil {
		return "", errors.New("invalid context")
	}
	identity, err := nvrSFUIdentityForUser(user)
	if err != nil {
		return "", err
	}
	identity.RoomGeneration = t.policy.generation
	if err = t.policy.check(ctx, identity.Principal); err != nil {
		return "", err
	}
	var value [32]byte
	if _, err = rand.Read(value[:]); err != nil {
		return "", err
	}
	ticket := hex.EncodeToString(value[:])
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return "", errors.New("SFU closed")
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	for key, entry := range t.tickets {
		if !entry.expires.After(now) {
			delete(t.tickets, key)
		}
	}
	if len(t.tickets) >= 256 {
		return "", errors.New("SFU ticket limit reached")
	}
	t.tickets[ticket] = nvrSFUTicket{identity: identity, expires: now.Add(30 * time.Second)}
	return ticket, nil
}
func (t *nvrSFUTickets) consume(ctx context.Context, value string) (signaling.Identity, error) {
	if len(value) != 64 {
		return signaling.Identity{}, errors.New("invalid ticket")
	}
	t.mu.Lock()
	entry, ok := t.tickets[value]
	delete(t.tickets, value)
	closed := t.closed
	t.mu.Unlock()
	if !ok || closed || !entry.expires.After(time.Now()) {
		return signaling.Identity{}, errors.New("invalid ticket")
	}
	if err := t.policy.check(ctx, entry.identity.Principal); err != nil {
		return signaling.Identity{}, err
	}
	return entry.identity, nil
}
func (t *nvrSFUTickets) Close() { t.mu.Lock(); t.closed = true; clear(t.tickets); t.mu.Unlock() }
