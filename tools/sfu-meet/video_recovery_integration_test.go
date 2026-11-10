package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
	"github.com/stretchr/testify/require"
)

// recoveryLoss drops one viewer RTP sequence and every retransmission of it.
// ICE and RTCP continue to pass through the loopback UDP socket.
type recoveryLoss struct {
	net.PacketConn
	armed     atomic.Bool
	target    atomic.Uint32 // sequence plus one; zero means unset
	discarded atomic.Uint32
	dropped   chan uint16
}

func (c *recoveryLoss) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(b)
		if err != nil {
			return n, addr, err
		}
		if n >= 12 && b[0]>>6 == 2 && b[1]&0x7f >= 96 {
			seq := binary.BigEndian.Uint16(b[2:4])
			value := uint32(seq) + 1
			if c.armed.Load() && c.target.CompareAndSwap(0, value) {
				select {
				case c.dropped <- seq:
				default:
				}
			}
			if c.target.Load() == value {
				c.discarded.Add(1)
				continue
			}
		}
		return n, addr, nil
	}
}

func recoveryViewer(t *testing.T) (*webrtc.PeerConnection, *recoveryLoss) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	loss := &recoveryLoss{PacketConn: conn, dropped: make(chan uint16, 1)}
	mux := webrtc.NewICEUDPMux(nil, loss)
	t.Cleanup(func() { _ = mux.Close() })
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetICEUDPMux(mux)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	return pc, loss
}

// This test uses only synthesized video and offline FFmpeg decoding.
func TestRoomPLIRecoversDecodableVideoAfterUnrecoverableLoss(t *testing.T) {
	if os.Getenv("SFU_VIDEO_RECOVERY_TEST") != "1" {
		t.Skip("set SFU_VIDEO_RECOVERY_TEST=1 to use FFmpeg and libx264")
	}
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "source.h264")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=1:duration=1", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-x264-params", "threads=1:slices=1:keyint=1:repeat-headers=1", "-frames:v", "1", "-an", "-f", "h264", "-y", inputPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	raw, err := os.ReadFile(inputPath)
	require.NoError(t, err)
	reader, err := h264reader.NewReader(bytes.NewReader(raw))
	require.NoError(t, err)
	var sps, pps, idr []byte
	for {
		nal, err := reader.NextNAL()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		switch nal.Data[0] & 31 {
		case 7:
			sps = nal.Data
		case 8:
			pps = nal.Data
		case 5:
			idr = nal.Data
		}
	}
	require.NotEmpty(t, sps)
	require.NotEmpty(t, pps)
	require.NotEmpty(t, idr)
	payloader := &codecs.H264Payloader{}
	payloader.Payload(600, sps)
	payloader.Payload(600, pps)
	payloads := payloader.Payload(600, idr)
	require.Greater(t, len(payloads), 2, "frame must span several RTP packets")

	_, origin := setup(t)
	cookie := login(t, origin, testSecret, origin).Cookies()[0]
	pubWS, _, err := dial(t, origin, origin, cookie)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pubWS.Close() })
	require.NoError(t, pubWS.WriteJSON(map[string]any{"id": "publish", "type": "publish", "kind": "video", "source": "camera", "codec": "video/H264", "track_id": "video"}))
	published := readResult(t, pubWS, "publish")
	require.Empty(t, published.Error)
	require.NotNil(t, published.Publication)
	publisher := demoPeer(t)
	input, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "recovery")
	require.NoError(t, err)
	sender, err := publisher.AddTrack(input)
	require.NoError(t, err)
	plis := make(chan uint32, 20)
	go func() {
		for {
			packets, _, err := sender.ReadRTCP()
			if err != nil {
				return
			}
			for _, p := range packets {
				if pli, ok := p.(*rtcp.PictureLossIndication); ok {
					select {
					case plis <- pli.MediaSSRC:
					default:
					}
				}
			}
		}
	}()
	require.NoError(t, pubWS.WriteJSON(map[string]any{"id": "offer", "type": "offer", "sdp": gatheredOffer(t, publisher)}))
	answer := readResult(t, pubWS, "offer")
	require.Empty(t, answer.Error)
	require.NoError(t, publisher.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}))
	var seq uint16
	sendFrame := func(ts uint32) error {
		for i, payload := range payloads {
			seq++
			if err := input.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, Marker: i == len(payloads)-1}, Payload: payload}); err != nil {
				return err
			}
		}
		return nil
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	warmErrors := make(chan error, 1)
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(40 * time.Millisecond)
		defer ticker.Stop()
		var timestamp uint32
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				timestamp += 3600
				if err := sendFrame(timestamp); err != nil {
					warmErrors <- err
					return
				}
			}
		}
	}()
	t.Cleanup(func() {
		select {
		case <-stopped:
		default:
			close(stop)
			<-stopped
		}
	})
	type receivedVideo struct {
		packets chan *rtp.Packet
		ssrc    chan uint32
	}
	setupViewer := func(pc *webrtc.PeerConnection) receivedVideo {
		ws, _, err := dial(t, origin, origin, cookie)
		require.NoError(t, err)
		t.Cleanup(func() { _ = ws.Close() })
		_ = ws.SetReadDeadline(time.Now().Add(8 * time.Second))
		for {
			var state signalResult
			require.NoError(t, ws.ReadJSON(&state))
			ready := false
			if state.Snapshot != nil {
				for _, p := range state.Snapshot.Publications {
					if p.ID == published.Publication.ID && p.RTPReady {
						ready = true
					}
				}
			}
			if ready {
				break
			}
		}
		require.NoError(t, ws.WriteJSON(map[string]any{"id": "subscribe", "type": "subscribe", "publication_id": published.Publication.ID}))
		result := readResult(t, ws, "subscribe")
		require.Empty(t, result.Error)
		_, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
		require.NoError(t, err)
		got := receivedVideo{packets: make(chan *rtp.Packet, 2048), ssrc: make(chan uint32, 1)}
		pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			select {
			case got.ssrc <- uint32(track.SSRC()):
			default:
			}
			for {
				packet, _, err := track.ReadRTP()
				if err != nil {
					return
				}
				select {
				case got.packets <- packet:
				default:
				}
			}
		})
		require.NoError(t, ws.WriteJSON(map[string]any{"id": "offer", "type": "offer", "sdp": gatheredOffer(t, pc)}))
		result = readResult(t, ws, "offer")
		require.Empty(t, result.Error)
		require.NoError(t, pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: result.SDP}))
		return got
	}
	lossy, loss := recoveryViewer(t)
	bad := setupViewer(lossy)
	good := setupViewer(demoPeer(t))
	var remoteSSRC uint32
	select {
	case remoteSSRC = <-bad.ssrc:
	case <-time.After(5 * time.Second):
		t.Fatal("impaired viewer received no video")
	}
	select {
	case <-good.ssrc:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy viewer received no video")
	}
	close(stop)
	<-stopped
	select {
	case err := <-warmErrors:
		t.Fatal(err)
	default:
	}
	// Let any setup PLI and its rate limit expire before inducing the loss.
	time.Sleep(1100 * time.Millisecond)
	for len(plis) > 0 {
		<-plis
	}
	const damagedTS, recoveryTS = uint32(1000000), uint32(1003600)
	loss.armed.Store(true)
	require.NoError(t, sendFrame(damagedTS))
	select {
	case <-loss.dropped:
	case <-time.After(3 * time.Second):
		t.Fatal("no RTP packet was dropped")
	}
	collect := func(ch <-chan *rtp.Packet, timestamp uint32) []byte {
		t.Helper()
		var h264 codecs.H264Packet
		var frame []byte
		deadline := time.After(5 * time.Second)
		for {
			select {
			case packet := <-ch:
				if packet.Timestamp != timestamp {
					continue
				}
				data, err := h264.Unmarshal(packet.Payload)
				require.NoError(t, err)
				frame = append(frame, data...)
				if packet.Marker {
					return frame
				}
			case <-deadline:
				t.Fatalf("missing frame timestamp %d", timestamp)
			}
		}
	}
	decode := func(name string, frame []byte) {
		t.Helper()
		path := filepath.Join(dir, name+".h264")
		require.NoError(t, os.WriteFile(path, frame, 0600))
		out, err := exec.Command("ffmpeg", "-v", "error", "-xerror", "-f", "h264", "-i", path, "-frames:v", "1", "-f", "null", "-").CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	decode("healthy-during-loss", collect(good.packets, damagedTS))
	damaged := collect(bad.packets, damagedTS)
	damagedPath := filepath.Join(dir, "damaged.h264")
	require.NoError(t, os.WriteFile(damagedPath, damaged, 0600))
	_, err = exec.Command("ffmpeg", "-v", "error", "-xerror", "-f", "h264", "-i", damagedPath, "-frames:v", "1", "-f", "null", "-").CombinedOutput()
	require.Error(t, err, "the missing packet must make the isolated frame undecodable")
	require.Eventually(t, func() bool { return loss.discarded.Load() >= 2 }, 3*time.Second, 10*time.Millisecond, "a retransmission of the lost packet must also be dropped")
	require.NoError(t, lossy.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: remoteSSRC}}))
	select {
	case ssrc := <-plis:
		params := sender.GetParameters()
		require.Len(t, params.Encodings, 1)
		require.Equal(t, uint32(params.Encodings[0].SSRC), ssrc)
	case <-time.After(5 * time.Second):
		t.Fatal("PLI did not reach publisher")
	}
	require.NoError(t, sendFrame(recoveryTS))
	decode("recovered-viewer", collect(bad.packets, recoveryTS))
	decode("healthy-after-loss", collect(good.packets, recoveryTS))
}
