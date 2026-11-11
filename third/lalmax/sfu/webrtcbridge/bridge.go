// Package webrtcbridge connects Pion tracks to the SFU Room RTP path.
package webrtcbridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/rtpforward"
)

var ErrInboundCodecChanged = errors.New("webrtcbridge: inbound codec changed")

type inboundTrack interface {
	ReadRTP() (*rtp.Packet, interceptor.Attributes, error)
	SetReadDeadline(time.Time) error
	Codec() webrtc.RTPCodecParameters
}

// ForwardInbound exclusively reads a Pion remote track into an enabled Room
// publication. The caller must validate codec, media identity and permissions
// before starting, and owns the track and publication lifecycle. Cancellation
// interrupts the blocking read by setting its deadline; do not share the track
// with other readers or concurrently manage its read deadline.
func ForwardInbound(ctx context.Context, remote *webrtc.TrackRemote, room *sfu.Room, participantID, session, publicationID string) error {
	if ctx == nil || remote == nil || room == nil {
		return errors.New("webrtcbridge: nil input")
	}
	return forwardInbound(ctx, remote, room, participantID, session, publicationID, remote.RID(), remote.Codec())
}

// ForwardInboundWithCodec verifies that the transport still uses the codec
// captured by the caller before it enabled the publication's RTP fanout.
func ForwardInboundWithCodec(ctx context.Context, remote *webrtc.TrackRemote, room *sfu.Room, participantID, session, publicationID string, expected webrtc.RTPCodecParameters) error {
	if ctx == nil || remote == nil || room == nil {
		return errors.New("webrtcbridge: nil input")
	}
	return forwardInbound(ctx, remote, room, participantID, session, publicationID, remote.RID(), expected)
}

func forwardInbound(ctx context.Context, remote inboundTrack, room *sfu.Room, participantID, session, publicationID, rid string, expected webrtc.RTPCodecParameters) error {
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
		current := remote.Codec()
		if current.PayloadType != expected.PayloadType || current.MimeType != expected.MimeType || current.ClockRate != expected.ClockRate || current.Channels != expected.Channels || current.SDPFmtpLine != expected.SDPFmtpLine || packet.PayloadType != uint8(expected.PayloadType) {
			return ErrInboundCodecChanged
		}
		if err = room.WritePublicationLayerRTP(participantID, session, publicationID, rid, packet); err != nil {
			return fmt.Errorf("write room RTP: %w", err)
		}
	}
}

// ForwardOutbound writes a Room subscription into an already-negotiated Pion
// local track and closes the subscriber on exit. It strips RTP header
// extensions: extension IDs belong to the inbound connection and cannot be
// reused on another connection. RTP padding is preserved so padding-only
// bandwidth probes remain valid packets. TrackLocalStaticRTP binds outgoing SSRC and PT.
// The caller owns SDP, RTCP feedback, codec compatibility and PC lifecycle.
func ForwardOutbound(ctx context.Context, sub rtpforward.PacketSubscriber, local *webrtc.TrackLocalStaticRTP) error {
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
			clean := cleanOutboundRTP(packet)
			if err := local.WriteRTP(&clean); err != nil {
				return fmt.Errorf("write local RTP: %w", err)
			}
		}
	}
}

func cleanOutboundRTP(packet *rtp.Packet) rtp.Packet {
	return rtp.Packet{Header: rtp.Header{Version: 2, Padding: packet.Padding, PaddingSize: packet.Header.PaddingSize, Marker: packet.Marker, SequenceNumber: packet.SequenceNumber, Timestamp: packet.Timestamp}, Payload: packet.Payload}
}
