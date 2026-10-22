// Local integration-test receiver. No camera or account credentials are needed.
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
)

func main() {
	var mu sync.Mutex
	var pc *webrtc.PeerConnection
	var packets atomic.Int64
	http.HandleFunc("/offer", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if pc != nil {
			_ = pc.Close()
		}
		packets.Store(0)
		settings := webrtc.SettingEngine{}
		settings.SetIncludeLoopbackCandidate(true)
		settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
		var err error
		pc, err = webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			for {
				if _, _, err := track.ReadRTP(); err != nil {
					return
				}
				packets.Add(1)
			}
		})
		_, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		gathered := webrtc.GatheringCompletePromise(pc)
		if err = pc.SetLocalDescription(offer); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		<-gathered
		sdp := pc.LocalDescription().SDP
		if r.URL.Query().Get("candidates") == "false" {
			var lines []string
			for _, line := range strings.Split(sdp, "\r\n") {
				if !strings.HasPrefix(line, "a=candidate:") && line != "a=end-of-candidates" {
					lines = append(lines, line)
				}
			}
			sdp = strings.Join(lines, "\r\n")
		}
		w.Header().Set("Content-Type", "application/sdp")
		_, _ = io.WriteString(w, sdp)
	})
	http.HandleFunc("/answer", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if pc == nil {
			http.Error(w, "create offer first", 409)
			return
		}
		if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: string(body)}); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(204)
	})
	http.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		state := "none"
		if pc != nil {
			state = pc.ConnectionState().String()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": state, "packets": packets.Load()})
	})
	http.HandleFunc("/close", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if pc != nil {
			_ = pc.Close()
		}
		w.WriteHeader(204)
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:18084", nil))
}
