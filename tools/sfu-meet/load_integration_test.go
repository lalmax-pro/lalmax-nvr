package main

import (
	"bufio"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
	viewers := []int{1, 4, 7}
	if value := os.Getenv("SFU_LOAD_VIEWERS"); value != "" {
		viewers = nil
		seen := make(map[int]bool)
		for _, item := range strings.Split(value, ",") {
			count, err := strconv.Atoi(strings.TrimSpace(item))
			if err != nil || count < 1 || count > 31 || seen[count] {
				t.Fatal("SFU_LOAD_VIEWERS must be a comma-separated list of unique values from 1 to 31")
			}
			seen[count] = true
			viewers = append(viewers, count)
		}
	}
	packetsPerSecond := loadEnvInt(t, "SFU_LOAD_PPS", 30, 1, 1000)
	payloadBytes := loadEnvInt(t, "SFU_LOAD_PAYLOAD_BYTES", 1000, 100, 1200)
	for _, count := range viewers {
		t.Run(fmt.Sprintf("viewers_%d", count), func(t *testing.T) {
			testDemoSFULoad(t, count, duration, packetsPerSecond, payloadBytes)
		})
	}
}

// TestDemoSFULoadServer runs in a helper process so the parent can measure
// server CPU and RSS without including the synthetic publisher and viewers.
func TestDemoSFULoadServer(t *testing.T) {
	if os.Getenv("SFU_LOAD_SERVER_CHILD") != "1" {
		t.Skip("helper process only")
	}
	participants, err := strconv.Atoi(os.Getenv("SFU_LOAD_SERVER_PARTICIPANTS"))
	if err != nil || participants < 2 || participants > 32 {
		t.Fatal("invalid helper server participant limit")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	handler, err := newDemoHandlerWithCapacity(origin, testSecret, participants, participants)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	fmt.Fprintf(os.Stdout, "SFU_LOAD_SERVER_ADDR=%s\n", origin)
	select {}
}

func startDemoSFULoadServer(t *testing.T, participants int) (string, *process.Process) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.v", "-test.run=^TestDemoSFULoadServer$")
	cmd.Env = append(os.Environ(), "SFU_LOAD_SERVER_CHILD=1", fmt.Sprintf("SFU_LOAD_SERVER_PARTICIPANTS=%d", participants))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "SFU_LOAD_SERVER_ADDR=") {
				ready <- strings.TrimPrefix(line, "SFU_LOAD_SERVER_ADDR=")
				return
			}
		}
		if err := scanner.Err(); err != nil {
			ready <- "scanner error: " + err.Error()
			return
		}
		ready <- "server process exited before becoming ready"
	}()
	var origin string
	select {
	case origin = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for helper SFU server")
	}
	if !strings.HasPrefix(origin, "http://127.0.0.1:") {
		t.Fatalf("helper SFU server did not report a loopback origin: %q", origin)
	}
	serverProcess, err := process.NewProcess(int32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	return origin, serverProcess
}

func loadEnvInt(t *testing.T, name string, fallback, minimum, maximum int) int {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		t.Fatalf("%s must be between %d and %d", name, minimum, maximum)
	}
	return parsed
}

func testDemoSFULoad(t *testing.T, count int, duration time.Duration, packetsPerSecond, payloadBytes int) {
	t.Helper()
	origin, proc := startDemoSFULoadServer(t, count+1)
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
	received := make([]atomic.Uint64, count)
	first := make([]time.Duration, count)
	go func() {
		ticker := time.NewTicker(time.Second / time.Duration(packetsPerSecond))
		defer ticker.Stop()
		payload := make([]byte, payloadBytes)
		payload[0] = 0x65
		var sequence uint16
		var timestamp uint32
		clockStep := uint32(90000 / packetsPerSecond)
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sequence++
				timestamp += clockStep
				if track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, Marker: true, SequenceNumber: sequence, Timestamp: timestamp}, Payload: payload}) == nil {
					sent.Add(1)
				}
			}
		}
	}()
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
	beforeCPU, err := proc.Times()
	if err != nil {
		t.Fatal(err)
	}
	startSent := sent.Load()
	startReceived := make([]uint64, count)
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
	if peakRSS == 0 {
		t.Fatal("process RSS measurement unavailable")
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
	t.Logf("viewers=%d duration=%.2fs payload_bytes=%d target_pps=%d input_packets=%d packets_per_viewer=%d..%d input_pps=%.1f delivered_pps=%.1f aggregate_payload_bps=%.0f server_cpu=%s server_peak_rss_bytes=%d first_rtp_ms=%.1f..%.1f", count, elapsed.Seconds(), payloadBytes, packetsPerSecond, input, minReceived, maxReceived, float64(input)/elapsed.Seconds(), float64(output)/elapsed.Seconds(), float64(output)*float64(payloadBytes)*8/elapsed.Seconds(), cpuResult, peakRSS, float64(minFirst.Microseconds())/1000, float64(maxFirst.Microseconds())/1000)
}
