package main

import (
	"fmt"
	"math"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/shirou/gopsutil/v4/process"
)

// TestDemoSFULoad is opt-in: it creates real Pion peers and sends synthetic
// video RTP, but does not decode or play media and requires no device access.
func TestDemoSFULoad(t *testing.T) {
	if os.Getenv("SFU_LOAD_TEST") != "1" {
		t.Skip("set SFU_LOAD_TEST=1 to run the local transport load baseline")
	}
	duration := 5 * time.Second
	if value := os.Getenv("SFU_LOAD_DURATION"); value != "" {
		var err error
		duration, err = time.ParseDuration(value)
		if err != nil || duration < time.Second || duration > 30*time.Minute {
			t.Fatal("SFU_LOAD_DURATION must be between 1s and 30m")
		}
	}
	for _, count := range []int{1, 4, 7} {
		t.Run(fmt.Sprintf("viewers_%d", count), func(t *testing.T) { testDemoSFULoad(t, count, duration) })
	}
}

func testDemoSFULoad(t *testing.T, count int, duration time.Duration) {
	t.Helper()
	_, origin := setup(t)
	resp := login(t, origin, testSecret, origin)
	if resp.StatusCode != 204 || len(resp.Cookies()) != 1 {
		t.Fatalf("login: %s", resp.Status)
	}
	cookie := resp.Cookies()[0]
	publisher, _, err := dial(t, origin, origin, cookie)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publisher.Close() })
	if err = publisher.WriteJSON(map[string]any{"id": "publish", "type": "publish", "kind": "video", "source": "camera", "codec": "video/H264", "track_id": "video"}); err != nil {
		t.Fatal(err)
	}
	published := readResult(t, publisher, "publish")
	if published.Error != "" || published.Publication == nil {
		t.Fatalf("publish: %+v", published)
	}
	peer := demoPeer(t)
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "load")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := peer.AddTrack(track)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			if _, _, err := sender.ReadRTCP(); err != nil {
				return
			}
		}
	}()
	offer := gatheredOffer(t, peer)
	if err = publisher.WriteJSON(map[string]any{"id": "offer", "type": "offer", "sdp": offer}); err != nil {
		t.Fatal(err)
	}
	answer := readResult(t, publisher, "offer")
	if answer.Error != "" {
		t.Fatalf("publisher offer: %+v", answer)
	}
	if err = peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
		t.Fatal(err)
	}
	var sent atomic.Uint64
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		ticker := time.NewTicker(time.Second / 30)
		defer ticker.Stop()
		payload := make([]byte, 1000)
		payload[0] = 0x65
		var sequence uint16
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sequence++
				if track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, Marker: true, SequenceNumber: sequence, Timestamp: uint32(sequence) * 3000}, Payload: payload}) == nil {
					sent.Add(1)
				}
			}
		}
	}()
	var received [7]atomic.Uint64
	var first [7]time.Duration
	for i := 0; i < count; i++ {
		viewer, _, err := dial(t, origin, origin, cookie)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = viewer.Close() })
		_ = viewer.SetReadDeadline(time.Now().Add(8 * time.Second))
		for {
			var state signalResult
			if err = viewer.ReadJSON(&state); err != nil {
				t.Fatal(err)
			}
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
		started := time.Now()
		if err = viewer.WriteJSON(map[string]any{"id": "subscribe", "type": "subscribe", "publication_id": published.Publication.ID}); err != nil {
			t.Fatal(err)
		}
		if result := readResult(t, viewer, "subscribe"); result.Error != "" {
			t.Fatalf("subscribe: %+v", result)
		}
		viewerPeer := demoPeer(t)
		if _, err = viewerPeer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			t.Fatal(err)
		}
		firstPacket := make(chan struct{}, 1)
		index := i
		viewerPeer.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			for {
				if _, _, err := remote.ReadRTP(); err != nil {
					return
				}
				received[index].Add(1)
				select {
				case firstPacket <- struct{}{}:
				default:
				}
			}
		})
		viewerOffer := gatheredOffer(t, viewerPeer)
		if err = viewer.WriteJSON(map[string]any{"id": "view-offer", "type": "offer", "sdp": viewerOffer}); err != nil {
			t.Fatal(err)
		}
		answer := readResult(t, viewer, "view-offer")
		if answer.Error != "" {
			t.Fatalf("viewer offer: %+v", answer)
		}
		if err = viewerPeer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-firstPacket:
			first[i] = time.Since(started)
		case <-time.After(8 * time.Second):
			t.Fatal("no relayed RTP")
		}
	}
	proc, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	beforeCPU, err := proc.Times()
	if err != nil {
		t.Fatal(err)
	}
	startSent := sent.Load()
	var startReceived [7]uint64
	for i := 0; i < count; i++ {
		startReceived[i] = received[i].Load()
	}
	start := time.Now()
	var peakRSS uint64
	for time.Since(start) < duration {
		if memory, err := proc.MemoryInfo(); err == nil && memory.RSS > peakRSS {
			peakRSS = memory.RSS
		}
		time.Sleep(250 * time.Millisecond)
	}
	elapsed := time.Since(start)
	afterCPU, err := proc.Times()
	if err != nil {
		t.Fatal(err)
	}
	input := sent.Load() - startSent
	if input == 0 {
		t.Fatal("publisher sent no packets during measurement")
	}
	var output uint64
	var minReceived, maxReceived uint64
	var minFirst, maxFirst = first[0], first[0]
	for i := 0; i < count; i++ {
		n := received[i].Load() - startReceived[i]
		if n == 0 {
			t.Fatalf("viewer %d received no packets during measurement", i)
		}
		output += n
		if i == 0 || n < minReceived {
			minReceived = n
		}
		if n > maxReceived {
			maxReceived = n
		}
		if first[i] < minFirst {
			minFirst = first[i]
		}
		if first[i] > maxFirst {
			maxFirst = first[i]
		}
	}
	cpu := (afterCPU.User + afterCPU.System - beforeCPU.User - beforeCPU.System) / elapsed.Seconds() * 100
	cpuResult := "unavailable"
	if !math.IsNaN(cpu) && !math.IsInf(cpu, 0) && cpu >= 0 {
		cpuResult = fmt.Sprintf("%.1f%%", cpu)
	}
	t.Logf("viewers=%d duration=%.2fs input_packets=%d packets_per_viewer=%d..%d input_pps=%.1f delivered_pps=%.1f received_payload_bps=%.0f aggregate_process_cpu=%s aggregate_process_peak_rss_bytes=%d first_rtp_ms=%.1f..%.1f", count, elapsed.Seconds(), input, minReceived, maxReceived, float64(input)/elapsed.Seconds(), float64(output)/elapsed.Seconds(), float64(output)*1000*8/elapsed.Seconds(), cpuResult, peakRSS, float64(minFirst.Microseconds())/1000, float64(maxFirst.Microseconds())/1000)
}
