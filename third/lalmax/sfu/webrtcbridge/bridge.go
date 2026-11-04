// Package webrtcbridge connects Pion tracks to the SFU Room RTP path.
package webrtcbridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/rtpforward"
)

// ForwardInbound exclusively reads a Pion remote track into an enabled Room
// publication. The caller must validate codec, media identity and permissions
// before starting, and owns the track and publication lifecycle. Cancellation
// interrupts the blocking read by setting its deadline; do not share the track
// with other readers or concurrently manage its read deadline.
func ForwardInbound(ctx context.Context, remote *webrtc.TrackRemote, room *sfu.Room, participantID, session, publicationID string) error {
	if ctx == nil || remote == nil || room == nil {
		return errors.New("webrtcbridge: nil input")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = remote.SetReadDeadline(time.Now()) })
	defer stop()
	for {
		packet, _, err := remote.ReadRTP()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read remote RTP: %w", err)
		}
		if err = room.WritePublicationRTP(participantID, session, publicationID, packet); err != nil {
			return fmt.Errorf("write room RTP: %w", err)
		}
	}
}

// ForwardOutbound writes a Room subscription into an already-negotiated Pion
// local track and closes the subscriber on exit. It strips RTP header
// extensions: extension IDs belong to the inbound connection and cannot be
// reused on another connection. TrackLocalStaticRTP binds outgoing SSRC and PT.
// The caller owns SDP, RTCP feedback, codec compatibility and PC lifecycle.
func ForwardOutbound(ctx context.Context, sub *rtpforward.Subscriber, local *webrtc.TrackLocalStaticRTP) error {
	if ctx == nil || sub == nil || local == nil {
		return errors.New("webrtcbridge: nil input")
	}
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case packet, ok := <-sub.Packets():
			if !ok {
				return sub.Err()
			}
			clean := rtp.Packet{Header: rtp.Header{Version: 2, Marker: packet.Marker, SequenceNumber: packet.SequenceNumber, Timestamp: packet.Timestamp}, Payload: packet.Payload}
			if err := local.WriteRTP(&clean); err != nil {
				return fmt.Errorf("write local RTP: %w", err)
			}
		}
	}
}
