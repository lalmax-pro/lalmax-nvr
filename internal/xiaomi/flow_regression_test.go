// SPDX-License-Identifier: MIT

package xiaomi

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/stretchr/testify/require"
)

func TestCaptchaContinuationSendsAnswerAndCookie(t *testing.T) {
	t.Helper()
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "captcha"}
	covInstallDefaultTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pass/serviceLoginAuth2" {
			_ = r.ParseForm()
			cookie, err := r.Cookie("ick")
			if r.Form.Get("captCode") == "1234" && err == nil && cookie.Value == "ick-value" {
				state.setAuth2Mode("ok")
			}
		}
		state.ServeHTTP(w, r)
	}))
	_, id, err := SignInWithCaptcha("user@example.com", "secret", "cn")
	require.Error(t, err)
	require.NotEmpty(t, id)
	session, err := LoginWithCaptcha(id, "1234")
	require.NoError(t, err)
	require.Equal(t, "user-1", session.UserID)
}

func TestLalMediaTimestampsUseMilliseconds(t *testing.T) {
	t.Helper()
	pub := &covPubSession{}
	r := NewXiaomiRecorder(XiaomiRecorderConfig{AudioEnabled: true}, nil)
	r.pubSession = pub
	r.streamStart = time.Now().Add(-time.Second)
	r.codec = model.FormatH264
	r.forwardVideo([]byte{0x41, 1})
	r.forwardAudio(missCodecPCMA, []byte{0xd5})
	require.Len(t, pub.packets, 2)
	for _, pkt := range pub.packets {
		require.InDelta(t, 1000, pkt.Timestamp, 100)
		require.Equal(t, pkt.Timestamp, pkt.Pts)
	}
	// Exercise the real lal remuxer, whose RTMP clock is milliseconds.
	var timestamp uint32
	r.pubSession = logic.NewCustomizePubSessionContext("xiaomi-clock").WithOnRtmpMsg(func(msg base.RtmpMsg) { timestamp = msg.Header.TimestampAbs })
	r.forwardAudio(missCodecPCMA, []byte{0xd5})
	require.InDelta(t, 1000, timestamp, 100)
}

func TestMISSAudioUsesActualCodecAndConversion(t *testing.T) {
	t.Helper()
	for _, tt := range []struct {
		name     string
		codec    uint32
		input    []byte
		pt       base.AvPacketPt
		lal, hub []byte
	}{
		{"PCMA", missCodecPCMA, []byte{0xd5}, base.AvPacketPtG711A, []byte{0xd5}, []byte{0xd5}},
		{"PCMU silence", missCodecPCMU, []byte{0xff}, base.AvPacketPtG711U, []byte{0xff}, []byte{0xd5}},
		{"PCM silence and peaks", missCodecPCM, []byte{0, 0, 0xff, 0x7f, 0, 0x80}, base.AvPacketPtG711A, []byte{0xd5, 0xaa, 0x2a}, []byte{0xd5, 0xaa, 0x2a}},
		{"Opus", missCodecOPUS, []byte{0x78, 1, 2}, base.AvPacketPtOpus, []byte{0x78, 1, 2}, []byte{0x78, 1, 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			pt, lal, hub, err := missAudioPayload(tt.codec, tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.pt, pt)
			require.Equal(t, tt.lal, lal)
			require.Equal(t, tt.hub, hub)
		})
	}
	_, _, _, err := missAudioPayload(missCodecPCM, []byte{0})
	require.Error(t, err)
	// Verify the forwarding path also preserves mu-law through lal.
	pub := &covPubSession{}
	r := NewXiaomiRecorder(XiaomiRecorderConfig{AudioEnabled: true}, nil)
	r.pubSession = pub
	r.streamStart = time.Now()
	r.forwardAudio(missCodecPCMU, []byte{0xff})
	require.Equal(t, base.AvPacketPtG711U, pub.last().PayloadType)
}

func TestExistingRecorderReadsRefreshedCredentials(t *testing.T) {
	t.Helper()
	original := currentCloudConfig()
	t.Cleanup(func() {
		SetCloudConfig(config.XiaomiConfig{UserID: original.UserID, Token: original.Token, Region: original.Region})
	})
	SetCloudConfig(config.XiaomiConfig{UserID: "user", Token: "expired", Region: "cn"})
	r := (&XiaomiPlugin{}).NewRecorder(config.CameraConfig{ID: "cam", DID: "1"}, nil, nil).(*XiaomiRecorder)
	SetCloudConfig(config.XiaomiConfig{UserID: "user", Token: "fresh", Region: "sg"})
	require.Equal(t, XiaomiCloudConfig{UserID: "user", Token: "fresh", Region: "sg"}, r.currentCloudConfig())
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = r.currentCloudConfig()
			}
		}()
	}
	for i := 0; i < 100; i++ {
		SetCloudConfig(config.XiaomiConfig{UserID: "user", Token: "fresh", Region: "sg"})
	}
	wg.Wait()
}

func TestMISSResolutionPreservesUnsupportedVendors(t *testing.T) {
	t.Helper()
	for _, tt := range []struct {
		id   int
		name string
	}{{3, "agora"}, {6, "mtp"}, {42, "42"}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			state := &covCloudServer{ssecurity: covCloudSsecurity}
			covCloudAPIFixture(t, state, tt.id, "192.168.1.10")
			raw, err := ResolveMISSURL(XiaomiCloudConfig{UserID: "user", Token: "token", Region: "cn"}, "655448418", "")
			require.NoError(t, err)
			require.Contains(t, raw, "vendor="+tt.name)
			require.NotContains(t, raw, "transport=udp")
		})
	}
}

func TestAudioOnlyConnectionExpiresAtFirstVideoDeadline(t *testing.T) {
	t.Helper()
	device, raw := covMISSDevice(t)
	r := NewXiaomiRecorder(XiaomiRecorderConfig{FirstVideoTimeout: 100 * time.Millisecond, IdleTimeout: 5 * time.Second, AudioEnabled: true}, nil)
	pub := &covPubSession{}
	r.pubSession = pub
	r.streamStart = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				device.mu.Lock()
				ready := len(device.commands) >= 2
				device.mu.Unlock()
				if ready {
					_ = device.sendMediaPacket(missCodecPCMA, 1000, []byte{0xd5})
				}
			}
		}
	}()
	err, connected := r.connectAndRecordWithQuality(ctx, raw, "sd")
	require.Error(t, err)
	require.NoError(t, ctx.Err(), "video deadline must fire even while audio keeps arriving")
	require.False(t, connected)
	require.Positive(t, pub.count(), "audio was received before the deadline")
}
