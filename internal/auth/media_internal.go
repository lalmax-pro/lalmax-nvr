package auth

import "fmt"

type internalMediaGrant struct{ publish, playback string }

// ReserveInternalStream grants ephemeral credentials without replacing operator
// configuration. Callers must use an unguessable output identity and reserve it
// before accepting a publisher. Configuration reload does not erase this grant.
func (a *MediaAuthorizer) ReserveInternalStream(id, publish, playback string) error {
	if a == nil || id == "" || publish == "" || playback == "" {
		return fmt.Errorf("invalid internal stream grant")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if grant, ok := a.internal[id]; ok && (grant.publish != "" || grant.playback != "") {
		return fmt.Errorf("internal stream already reserved")
	}
	for _, protocols := range a.tokens {
		for _, streams := range protocols {
			if _, exists := streams[id]; exists {
				return fmt.Errorf("stream has configured credentials")
			}
		}
	}
	if a.internal == nil {
		a.internal = map[string]internalMediaGrant{}
	}
	a.internal[id] = internalMediaGrant{publish: publish, playback: playback}
	return nil
}

// RevokeInternalStream keeps a deny-only tombstone until process exit. Deleting
// it would let direct playback fall back to an open global playback policy.
func (a *MediaAuthorizer) RevokeInternalStream(id string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.internal[id]; ok {
		a.internal[id] = internalMediaGrant{}
	}
}

// RestoreInternalStream restores a deny-only historical identity at bootstrap.
// Old credentials cannot become valid merely because the NVR restarted.
func (a *MediaAuthorizer) RestoreInternalStream(id string) {
	if a == nil || id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.internal == nil {
		a.internal = map[string]internalMediaGrant{}
	}
	if _, exists := a.internal[id]; !exists {
		a.internal[id] = internalMediaGrant{}
	}
}

func (a *MediaAuthorizer) InternalPlaybackRequired(id string) bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.internal[id]
	return ok
}
