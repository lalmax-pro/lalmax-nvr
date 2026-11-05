package access

import (
	"context"

	"github.com/q191201771/lalmax/sfu"
)

// SubscribedPublications returns metadata for this session's subscriptions to
// a trusted media factory. It rechecks Subscribe policy for each publication.
// It does not authorize general room discovery or deliver events to clients.
func (s *Session) SubscribedPublications(ctx context.Context) ([]sfu.Publication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if ctx == nil {
		return nil, ErrInvalid
	}
	snapshot, err := s.room.SnapshotSession(s.scope.ParticipantID, s.generation)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool)
	for _, sub := range snapshot.Subscriptions {
		if sub.ParticipantID == s.scope.ParticipantID {
			wanted[sub.PublicationID] = true
		}
	}
	pubs := make([]sfu.Publication, 0, len(wanted))
	for _, p := range snapshot.Publications {
		if wanted[p.ID] {
			req := s.scope
			req.Operation = Subscribe
			req.PublicationID = p.ID
			req.Kind = p.Kind
			req.Source = p.Source
			req.Codec = p.Codec
			if err := authorize(ctx, s.policy, req); err != nil {
				return nil, err
			}
			pubs = append(pubs, p)
		}
	}
	return pubs, nil
}
