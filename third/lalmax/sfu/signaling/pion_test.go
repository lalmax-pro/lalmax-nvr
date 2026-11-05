package signaling

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func testPeer(t *testing.T) *webrtc.PeerConnection {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var ip net.IP
	for _, a := range addresses {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			ip = n.IP.To4()
			break
		}
	}
	if ip == nil {
		t.Skip("WebRTC test requires a non-loopback IPv4 interface")
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	mux := webrtc.NewICEUDPMux(nil, conn)
	t.Cleanup(func() { _ = mux.Close() })
	setting := webrtc.SettingEngine{}
	setting.SetICEUDPMux(mux)
	setting.SetNAT1To1IPs([]string{ip.String()}, webrtc.ICECandidateTypeHost)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(setting)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}
func TestPionNegotiator(t *testing.T) {
	client, server := testPeer(t), testPeer(t)
	if _, err := client.CreateDataChannel("test", nil); err != nil {
		t.Fatal(err)
	}
	offer, err := client.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(client)
	if err = client.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("gather timeout")
	}
	n, err := NewPionNegotiator(server)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	answer, err := n.Answer(ctx, client.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	if _, err = n.Answer(ctx, client.LocalDescription().SDP); !errors.Is(err, ErrNegotiationUsed) {
		t.Fatalf("second offer: %v", err)
	}
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for client.ConnectionState() != webrtc.PeerConnectionStateConnected {
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("not connected: %s / %s", client.ConnectionState(), server.ConnectionState())
		}
	}
}
func TestInvalidOfferAndCancellation(t *testing.T) {
	n, _ := NewPionNegotiator(testPeer(t))
	defer n.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := n.Answer(ctx, "offer"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	n2, _ := NewPionNegotiator(testPeer(t))
	defer n2.Close()
	if _, err := n2.Answer(context.Background(), "invalid sdp"); err == nil {
		t.Fatal("invalid SDP accepted")
	}
}

func TestWebSocketOfferAnswerAndCleanup(t *testing.T) {
	h, _ := newHandler(t)
	server := testPeer(t)
	closed := make(chan struct{}, 1)
	server.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	})
	h.NewNegotiator = func(context.Context, Identity) (Negotiator, error) { return NewPionNegotiator(server) }
	httpServer := httptest.NewServer(h)
	defer httpServer.Close()
	ws, _, err := dial(t, httpServer.URL, "user", "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = readType(t, ws, "snapshot")
	client := testPeer(t)
	if _, err = client.CreateDataChannel("demo", nil); err != nil {
		t.Fatal(err)
	}
	offer, err := client.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(client)
	if err = client.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		t.Fatal("ICE gathering timeout")
	}
	if err = ws.WriteJSON(command{ID: "offer-1", Type: "offer", SDP: client.LocalDescription().SDP}); err != nil {
		t.Fatal(err)
	}
	reply := readType(t, ws, "result")
	if reply.ID != "offer-1" || reply.Error != "" || reply.SDP == "" {
		t.Fatalf("offer response: %+v", reply)
	}
	if err = client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: reply.SDP}); err != nil {
		t.Fatal(err)
	}
	if err = ws.WriteJSON(command{ID: "offer-2", Type: "offer", SDP: client.LocalDescription().SDP}); err != nil {
		t.Fatal(err)
	}
	reply = readType(t, ws, "result")
	if reply.ID != "offer-2" || reply.Error != "operation_failed" {
		t.Fatalf("second offer: %+v", reply)
	}
	if err = ws.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("server peer not closed")
	}
}
