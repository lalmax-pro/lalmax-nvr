//go:build livekit_capacity

package livekitsfu

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const capacityKey, capacitySecret = "capacity-test", "capacity-test-secret-not-for-production"

type capacityViewer struct {
	start   time.Time
	first   atomic.Int64
	packets atomic.Int64
	bytes   atomic.Int64
}

// TestLiveKitCapacityBaseline measures only the separate server process. The
// local SDK clients exchange synthetic RTP; this is not a decoded-video or
// production-network benchmark.
func TestLiveKitCapacityBaseline(t *testing.T) {
	if os.Getenv("SFU_CAPACITY_CHILD") == "1" {
		runCapacityServer(t)
		return
	}
	if os.Getenv("SFU_CAPACITY_TEST") == "" {
		t.Skip("set SFU_CAPACITY_TEST=1 to run the local capacity baseline")
	}
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps is required to sample server CPU and RSS")
	}
	duration := 8 * time.Second
	if value := os.Getenv("SFU_CAPACITY_DURATION"); value != "" {
		var err error
		duration, err = time.ParseDuration(value)
		if err != nil || duration < 2*time.Second {
			t.Fatal("SFU_CAPACITY_DURATION must be a duration of at least 2s")
		}
	}
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	tcp.Close()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	udp.Close()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd := exec.Command(executable, "-test.run=^TestLiveKitCapacityBaseline$", "-test.timeout=10m")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SFU_CAPACITY_CHILD=1", fmt.Sprintf("SFU_CAPACITY_TCP=%d", port), fmt.Sprintf("SFU_CAPACITY_UDP=%d", udpPort), "SFU_CAPACITY_READY="+ready)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("capacity server exited: %v", err)
			}
		default:
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("capacity server shutdown: %v", err)
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("capacity server did not stop")
			}
		}
	})
	for deadline := time.Now().Add(15 * time.Second); ; {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("capacity server failed to start: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("capacity server readiness timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, count := range []int{1, 4, 7} {
		t.Run(fmt.Sprintf("viewers_%d", count), func(t *testing.T) {
			runCapacityCase(t, fmt.Sprintf("http://127.0.0.1:%d", port), cmd.Process.Pid, count, duration)
		})
	}
}

func runCapacityServer(t *testing.T) {
	port, err := strconv.Atoi(os.Getenv("SFU_CAPACITY_TCP"))
	if err != nil || port < 1 {
		t.Fatal("invalid capacity server TCP port")
	}
	udpPort, err := strconv.Atoi(os.Getenv("SFU_CAPACITY_UDP"))
	if err != nil || udpPort < 1 {
		t.Fatal("invalid capacity server UDP port")
	}
	yaml := fmt.Sprintf("development: false\nport: %d\nbind_addresses: [127.0.0.1]\nkeys:\n  %s: %s\nrtc:\n  node_ip: 127.0.0.1\n  enable_loopback_candidate: true\n  ips:\n    includes: [127.0.0.1/32]\n  tcp_port: 0\n  port_range_start: 0\n  port_range_end: 0\n  udp_port: %d\n", port, capacityKey, capacitySecret, udpPort)
	server, err := NewServer(yaml)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- RunPrepared(ctx, server) }()
	for deadline := time.Now().Add(10 * time.Second); !server.IsRunning(); {
		select {
		case err := <-done:
			t.Fatalf("server failed during startup: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("server failed to start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(os.Getenv("SFU_CAPACITY_READY"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func runCapacityCase(t *testing.T, endpoint string, serverPID, count int, duration time.Duration) {
	t.Helper()
	info := lksdk.ConnectInfo{APIKey: capacityKey, APISecret: capacitySecret, RoomName: fmt.Sprintf("capacity-%d", count), ParticipantIdentity: "publisher"}
	publisher, err := lksdk.ConnectToRoom(endpoint, info, lksdk.NewRoomCallback(), lksdk.WithAutoSubscribe(false))
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Disconnect()
	track, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	// A syntactically valid STAP-A with SPS and a padded IDR supplies a
	// reproducible payload rate; the frame is not intended for decoding.
	payload := []byte{24}
	for _, nalu := range [][]byte{{0x67, 0x42, 0xc0, 0x1f, 0x0f, 0xd9, 0x1f, 0x88, 0x88, 0x84, 0, 0, 3, 0, 4, 0, 0, 3, 0, 0xc8, 0x3c, 0x60, 0xc9, 0x20}, append([]byte{0x65}, make([]byte, 899)...)} {
		var length [2]byte
		binary.BigEndian.PutUint16(length[:], uint16(len(nalu)))
		payload = append(payload, length[:]...)
		payload = append(payload, nalu...)
	}
	producerCtx, stopProducer := context.WithCancel(context.Background())
	defer stopProducer()
	var sent atomic.Int64
	go func() {
		ticker := time.NewTicker(time.Second / 30)
		defer ticker.Stop()
		var sequence uint16
		for {
			select {
			case <-producerCtx.Done():
				return
			case <-ticker.C:
				sequence++
				if track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 3000, Marker: true}, Payload: payload}, nil) == nil {
					sent.Add(1)
				}
			}
		}
	}()
	viewers := make([]*capacityViewer, 0, count)
	for i := 0; i < count; i++ {
		viewer := &capacityViewer{start: time.Now()}
		cb := lksdk.NewRoomCallback()
		cb.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
			go func() {
				for {
					packet, _, err := track.ReadRTP()
					if err != nil {
						return
					}
					viewer.first.CompareAndSwap(0, time.Since(viewer.start).Nanoseconds())
					viewer.packets.Add(1)
					viewer.bytes.Add(int64(len(packet.Payload)))
				}
			}()
		}
		info.ParticipantIdentity = fmt.Sprintf("viewer-%d", i)
		room, err := lksdk.ConnectToRoom(endpoint, info, cb)
		if err != nil {
			t.Fatal(err)
		}
		defer room.Disconnect()
		viewers = append(viewers, viewer)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ready := true
		for _, viewer := range viewers {
			ready = ready && viewer.first.Load() > 0
		}
		if ready {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	startPackets, startBytes := make([]int64, count), make([]int64, count)
	for i, viewer := range viewers {
		if viewer.first.Load() == 0 {
			t.Fatal("viewer did not receive initial RTP")
		}
		startPackets[i], startBytes[i] = viewer.packets.Load(), viewer.bytes.Load()
	}
	started := time.Now()
	startSent := sent.Load()
	var cpuTotal, peakRSS float64
	var samples int
	for time.Since(started) < duration {
		time.Sleep(time.Second)
		cpu, rss, err := sampleCapacityProcess(serverPID)
		if err != nil {
			t.Fatal(err)
		}
		cpuTotal += cpu
		if rss > peakRSS {
			peakRSS = rss
		}
		samples++
	}
	elapsed := time.Since(started).Seconds()
	var packets, bytes int64
	var firstMin, firstMax time.Duration
	for i, viewer := range viewers {
		p, b := viewer.packets.Load()-startPackets[i], viewer.bytes.Load()-startBytes[i]
		if p == 0 {
			t.Fatalf("viewer %d stopped receiving", i)
		}
		packets += p
		bytes += b
		first := time.Duration(viewer.first.Load())
		if firstMin == 0 || first < firstMin {
			firstMin = first
		}
		if first > firstMax {
			firstMax = first
		}
	}
	t.Logf("viewers=%d elapsed=%.1fs source_packets/s=%.1f received_packets/s=%.1f received_payload_Mbit/s=%.3f first_RTP=%s..%s server_ps_CPU_mean=%.1f%% server_RSS_sample_max=%.1fMiB", count, elapsed, float64(sent.Load()-startSent)/elapsed, float64(packets)/elapsed, float64(bytes)*8/elapsed/1e6, firstMin.Round(time.Millisecond), firstMax.Round(time.Millisecond), cpuTotal/float64(samples), peakRSS/1024)
}

func sampleCapacityProcess(pid int) (cpu, rssKiB float64, err error) {
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "%cpu=", "-o", "rss=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected ps output: %q", out)
	}
	if cpu, err = strconv.ParseFloat(fields[0], 64); err != nil {
		return 0, 0, err
	}
	rssKiB, err = strconv.ParseFloat(fields[1], 64)
	return cpu, rssKiB, err
}
