// Local integration-test receiver. No camera or account credentials are needed.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
)

func main() {
	var mu sync.Mutex
	var pc *webrtc.PeerConnection
	var packets atomic.Int64
	var generation atomic.Uint64
	var captureMu sync.Mutex
	var capture bytes.Buffer
	var receivedCodec string
	http.HandleFunc("/offer", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if pc != nil {
			_ = pc.Close()
		}
		packets.Store(0)
		current := generation.Add(1)
		captureMu.Lock()
		capture.Reset()
		receivedCodec = ""
		captureMu.Unlock()
		settings := webrtc.SettingEngine{}
		settings.SetIncludeLoopbackCandidate(true)
		settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
		configuration := webrtc.Configuration{}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&configuration); err != nil {
				http.Error(w, "invalid ICE configuration", http.StatusBadRequest)
				return
			}
		}
		if r.URL.Query().Get("relay") == "true" {
			configuration.ICETransportPolicy = webrtc.ICETransportPolicyRelay
		}
		var err error
		codec := webrtc.MimeTypeH264
		if strings.EqualFold(r.URL.Query().Get("codec"), "h265") {
			codec = webrtc.MimeTypeH265
		}
		engine := &webrtc.MediaEngine{}
		if err := engine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: codec, ClockRate: 90000}, PayloadType: 102}, webrtc.RTPCodecTypeVideo); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		pc, err = webrtc.NewAPI(webrtc.WithSettingEngine(settings), webrtc.WithMediaEngine(engine)).NewPeerConnection(configuration)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			var decoder rtp.Depacketizer = &codecs.H264Packet{}
			if track.Codec().MimeType == webrtc.MimeTypeH265 {
				decoder = &codecs.H265Depacketizer{}
			}
			captureMu.Lock()
			receivedCodec = track.Codec().MimeType
			captureMu.Unlock()
			for {
				packet, _, err := track.ReadRTP()
				if err != nil || generation.Load() != current {
					return
				}
				packets.Add(1)
				if payload, err := decoder.Unmarshal(packet.Payload); err == nil {
					captureMu.Lock()
					if capture.Len()+len(payload) <= 16<<20 {
						_, _ = capture.Write(payload)
					}
					captureMu.Unlock()
				}
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
		captureMu.Lock()
		defer captureMu.Unlock()
		stats := map[string]any{"state": state, "packets": packets.Load(), "codec": receivedCodec, "video_bytes": capture.Len()}
		if pc != nil && pc.SCTP() != nil && pc.SCTP().Transport() != nil {
			if pair, err := pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair(); err == nil && pair != nil {
				stats["local_candidate_type"] = pair.Local.Typ.String()
				stats["remote_candidate_type"] = pair.Remote.Typ.String()
			}
		}
		_ = json.NewEncoder(w).Encode(stats)
	})
	http.HandleFunc("/video", func(w http.ResponseWriter, r *http.Request) {
		captureMu.Lock()
		defer captureMu.Unlock()
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(capture.Bytes())
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
