package voip

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/pion/rtp"
	maxrtc "github.com/q191201771/lalmax/rtc"
)

const inboundTalkTicketLifetime = 30 * time.Second

type inboundTalk struct {
	browserMu sync.Mutex
	browser   *maxrtc.TalkSession

	// The remaining fields are guarded by Server.requestMu.
	token        string
	tokenExpires time.Time
	lease        time.Time
	attached     bool
	attaching    bool
	generation   uint64
}

func newInboundTalk() *inboundTalk { return &inboundTalk{} }

func (t *inboundTalk) writeRemoteRTP(packet *rtp.Packet) {
	t.browserMu.Lock()
	defer t.browserMu.Unlock()
	if t.browser != nil {
		_ = t.browser.WriteRTP(packet)
	}
}

func (t *inboundTalk) setBrowser(browser *maxrtc.TalkSession) bool {
	t.browserMu.Lock()
	defer t.browserMu.Unlock()
	if t.browser != nil {
		return false
	}
	t.browser = browser
	return true
}

func (t *inboundTalk) closeBrowser() {
	t.browserMu.Lock()
	browser := t.browser
	t.browser = nil
	t.browserMu.Unlock()
	if browser != nil {
		browser.Close()
	}
}

func (s *Server) ClaimInboundTalk(callID string) (string, error) {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	select {
	case <-s.stopChan:
		return "", fmt.Errorf("VoIP is stopped")
	default:
	}
	d := s.dialogManager.Get(callID)
	if d == nil || d.State != DialogStateEstablished || d.PubSession == nil || d.PubSession.Negotiated().Audio == nil || d.talk == nil {
		return "", fmt.Errorf("inbound audio call is not available")
	}
	t := d.talk
	if t.attached || t.attaching {
		return "", fmt.Errorf("call already has a browser")
	}
	if t.token != "" && time.Now().Before(t.tokenExpires) {
		return t.token, nil
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	t.token = hex.EncodeToString(raw[:])
	t.tokenExpires = time.Now().Add(inboundTalkTicketLifetime)
	t.generation++
	return t.token, nil
}

func (s *Server) AttachInboundTalk(ctx context.Context, callID, token, offer string) (string, error) {
	s.requestMu.Lock()
	d := s.dialogManager.Get(callID)
	if d == nil || d.State != DialogStateEstablished || d.PubSession == nil || d.PubSession.Negotiated().Audio == nil || d.talk == nil {
		s.requestMu.Unlock()
		return "", fmt.Errorf("inbound audio call is not available")
	}
	t := d.talk
	if token == "" || subtle.ConstantTimeCompare([]byte(t.token), []byte(token)) != 1 || time.Now().After(t.tokenExpires) {
		s.requestMu.Unlock()
		return "", fmt.Errorf("talk ticket is invalid or expired")
	}
	if t.attached || t.attaching {
		s.requestMu.Unlock()
		return "", fmt.Errorf("call already has a browser")
	}
	t.attaching = true
	generation := t.generation
	pub := d.PubSession
	codec := *pub.Negotiated().Audio
	s.requestMu.Unlock()

	browser, answer, err := s.newTalkSession(ctx, offer, codec,
		func(packet *rtp.Packet) { _ = pub.WriteAudioRTP(packet) },
		func() { s.detachInboundTalk(callID, token) })

	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	t.attaching = false
	current := s.dialogManager.Get(callID)
	if err != nil {
		return "", err
	}
	if current == nil || current.talk != t || current.PubSession != pub || current.State != DialogStateEstablished || t.generation != generation || time.Now().After(t.tokenExpires) {
		browser.Close()
		return "", fmt.Errorf("inbound call changed during browser negotiation")
	}
	if !t.setBrowser(browser) {
		browser.Close()
		return "", fmt.Errorf("call already has a browser")
	}
	t.attached = true
	t.lease = time.Now().Add(45 * time.Second)
	return answer, nil
}

func (s *Server) KeepInboundTalk(callID, token string) bool {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	d := s.dialogManager.Get(callID)
	if d == nil || d.talk == nil || !d.talk.attached || token == "" || subtle.ConstantTimeCompare([]byte(d.talk.token), []byte(token)) != 1 {
		return false
	}
	d.talk.lease = time.Now().Add(45 * time.Second)
	return true
}

func (s *Server) DetachInboundTalk(callID, token string) bool {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	d := s.dialogManager.Get(callID)
	if d == nil || d.talk == nil || token == "" || subtle.ConstantTimeCompare([]byte(d.talk.token), []byte(token)) != 1 {
		return false
	}
	s.resetInboundTalk(d.talk)
	return true
}

func (s *Server) detachInboundTalk(callID, token string) {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	d := s.dialogManager.Get(callID)
	if d != nil && d.talk != nil && subtle.ConstantTimeCompare([]byte(d.talk.token), []byte(token)) == 1 {
		s.resetInboundTalk(d.talk)
	}
}

func (s *Server) resetInboundTalk(t *inboundTalk) {
	t.attached = false
	t.attaching = false
	t.token = ""
	t.tokenExpires = time.Time{}
	t.lease = time.Time{}
	t.generation++
	t.closeBrowser()
}

func (s *Server) runInboundTalk(now time.Time) {
	for _, d := range s.dialogManager.List() {
		t := d.talk
		if t == nil {
			continue
		}
		if t.attached && !now.Before(t.lease) {
			s.resetInboundTalk(t)
		} else if !t.attached && !t.attaching && t.token != "" && !now.Before(t.tokenExpires) {
			t.token = ""
			t.tokenExpires = time.Time{}
			t.generation++
		}
	}
}

// SendDTMF sends a negotiated RFC 4733 digit to either an inbound or outbound SIP peer.
func (s *Server) SendDTMF(callID, key string) error {
	s.requestMu.Lock()
	if c := s.outbound[callID]; c != nil {
		if c.state != DialogStateEstablished || c.pub == nil {
			s.requestMu.Unlock()
			return fmt.Errorf("call is not established")
		}
		pub := c.pub
		if pub.Negotiated().DTMF == nil {
			s.requestMu.Unlock()
			return fmt.Errorf("terminal did not negotiate telephone-event DTMF")
		}
		s.requestMu.Unlock()
		return pub.WriteDTMF(key, 120*time.Millisecond)
	}
	d := s.dialogManager.Get(callID)
	if d == nil || d.State != DialogStateEstablished || d.PubSession == nil {
		s.requestMu.Unlock()
		return fmt.Errorf("call is not established")
	}
	pub := d.PubSession
	if pub.Negotiated().DTMF == nil {
		s.requestMu.Unlock()
		return fmt.Errorf("terminal did not negotiate telephone-event DTMF")
	}
	s.requestMu.Unlock()
	return pub.WriteDTMF(key, 120*time.Millisecond)
}
