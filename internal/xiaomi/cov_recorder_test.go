// SPDX-License-Identifier: MIT
//
// Coverage tests for the Xiaomi recorder: lal integration, the reconnect loop,
// the MISS connect/read pipeline against a fake camera, vendor error reporting
// and the small forwarding helpers.

package xiaomi

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/require"
)

// --- fakes ---------------------------------------------------------------

// covPubSession records everything fed into lal.
type covPubSession struct {
	mu      sync.Mutex
	packets []base.AvPacket
	rtmp    int
	options int
	asc     int
	err     error
}

func (s *covPubSession) FeedAvPacket(p base.AvPacket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.packets = append(s.packets, p)
	return nil
}

func (s *covPubSession) FeedRtmpMsg(base.RtmpMsg) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rtmp++
	return nil
}

func (s *covPubSession) WithOption(func(*base.AvPacketStreamOption)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.options++
}

func (s *covPubSession) FeedAudioSpecificConfig([]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asc++
	return nil
}

func (s *covPubSession) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.packets)
}

func (s *covPubSession) last() base.AvPacket {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.packets[len(s.packets)-1]
}

// covMediaEngine provides the lal custom-publish-session hooks.
type covMediaEngine struct {
	media.Engine
	pub      media.CustomizePubSession
	addErr   error
	delCalls int
}

func (e *covMediaEngine) AddCustomizePubSession(context.Context, string) (media.CustomizePubSession, error) {
	if e.addErr != nil {
		return nil, e.addErr
	}
	return e.pub, nil
}

func (e *covMediaEngine) DelCustomizePubSession(context.Context, media.CustomizePubSession) error {
	e.delCalls++
	return nil
}

// covErrorReporter captures vendor error details.
type covErrorReporter struct {
	mu     sync.Mutex
	detail *model.CameraErrorDetail
	camera string
}

func (r *covErrorReporter) SetErrorDetail(cameraID string, detail *model.CameraErrorDetail) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.camera = cameraID
	r.detail = detail
}

func (r *covErrorReporter) get() (*model.CameraErrorDetail, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.detail, r.camera
}

// covMISSDevice builds a fake TCP camera plus a matching miss:// URL.
func covMISSDevice(t *testing.T) (*covCS2Device, string) {
	t.Helper()
	device := newCovCS2Device(t, "tcp")

	devicePublic, _, err := GenerateKey()
	require.NoError(t, err)
	clientPublic, clientPrivate, err := GenerateKey()
	require.NoError(t, err)

	key, err := CalcSharedKey(hex.EncodeToString(devicePublic), hex.EncodeToString(clientPrivate))
	require.NoError(t, err)
	device.key = key

	missURL := fmt.Sprintf(
		"miss://%s?vendor=cs2&device_public=%s&client_private=%s&client_public=%s&sign=sig-1&transport=tcp&model=%s",
		device.host(),
		hex.EncodeToString(devicePublic),
		hex.EncodeToString(clientPrivate),
		hex.EncodeToString(clientPublic),
		ModelC200,
	)
	return device, missURL
}

// --- lal integration -----------------------------------------------------

func TestCovXiaomiRecorderStartStopWithMediaEngine(t *testing.T) {
	pub := &covPubSession{}
	engine := &covMediaEngine{pub: pub}
	r := NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID:     "cov-cam",
		MediaEngine:  engine,
		AudioEnabled: true,
	}, &noopSegmentStore{})

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, r.Start(ctx))
	require.NotNil(t, r.pubSession)
	require.Equal(t, pub, r.pubSession)
	require.Equal(t, model.StatusRecording, r.Status())

	cancel()
	require.NoError(t, r.Stop())
	require.Equal(t, 1, engine.delCalls)
	require.Nil(t, r.pubSession)
	require.Equal(t, model.StatusStopped, r.Status())

	// Stopping an already stopped recorder is a no-op.
	require.NoError(t, r.Stop())
}

func TestCovXiaomiRecorderStartMediaEngineError(t *testing.T) {
	engine := &covMediaEngine{addErr: errors.New("lal down")}
	r := NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID:    "cov-cam",
		MediaEngine: engine,
	}, &noopSegmentStore{})

	err := r.Start(context.Background())
	require.ErrorContains(t, err, "failed to register with lal")
	require.Nil(t, r.pubSession)
	require.Zero(t, engine.delCalls)
	// Leave the recorder in a stoppable state for other assertions.
	r.status = model.StatusStopped
}

func TestCovXiaomiRecorderSetErrorReporterAndHub(t *testing.T) {
	r := makeTestRecorder(t)
	require.Nil(t, r.GetHub())
	hub := model.NewStreamHub()
	r.Hub = hub
	require.Same(t, hub, r.GetHub())

	reporter := &covErrorReporter{}
	r.SetErrorReporter(reporter)
	require.Same(t, reporter, r.cfg.ErrReporter)
}

// --- reconnect loop ------------------------------------------------------

func TestCovXiaomiRecorderRunResolveErrorCanceledContext(t *testing.T) {
	r := makeTestRecorder(t)
	r.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		r.run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after a canceled context")
	}
	require.Equal(t, model.StatusStopped, r.Status())
}

func TestCovXiaomiRecorderRunBackoffThenCancel(t *testing.T) {
	// The resolver keeps failing (empty cloud credentials), so run() waits out
	// at least one tiered backoff before the canceled context is observed.
	r := makeTestRecorder(t)
	r.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.run(ctx)
		close(done)
	}()

	time.Sleep(2500 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}
}

func TestCovXiaomiRecorderRunConnectError(t *testing.T) {
	// Cloud resolution succeeds (fake account server), but building the MISS
	// client fails because the device key handed out by the cloud is not hex.
	state := &covCloudServer{ssecurity: covCloudSsecurity}
	state.apiHandler = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/app/v2/home/device_list_page":
			covCloudEncrypted(w, r, state.ssecurity, `{"code":0,"message":"ok","result":{"list":[{"did":"655448418","localip":"127.0.0.1:1","model":"chuangmi.camera.046c04","isOnline":true}]}}`)
			return true
		case "/app/v2/device/miss_get_vendor":
			covCloudEncrypted(w, r, state.ssecurity, `{"code":0,"message":"ok","result":{"vendor":{"vendor":4,"vendor_params":{}},"public_key":"zz","sign":"sig"}}`)
			return true
		}
		return false
	}
	covInstallDefaultTransport(t, state)

	r := NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID:    "cov-cam",
		DID:         "655448418",
		CloudCfg:    XiaomiCloudConfig{UserID: "12345", Token: "pass-token", Region: "cn"},
		IdleTimeout: 2 * time.Second,
	}, &noopSegmentStore{})
	r.done = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		r.run(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		for _, req := range state.requests {
			if strings.Contains(req, "miss_get_vendor") {
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancellation")
	}
}

func TestCovXiaomiRecorderConnectAndRecordSuccess(t *testing.T) {
	device, missURL := covMISSDevice(t)
	device.autoMedia = true

	pub := &covPubSession{}
	r := NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID:     "cov-cam",
		Model:        ModelC200,
		AudioEnabled: true,
		IdleTimeout:  5 * time.Second,
	}, &noopSegmentStore{})
	r.Hub = model.NewStreamHub()
	r.pubSession = pub
	r.streamStart = time.Now()

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		err       error
		connected bool
	}
	ch := make(chan result, 1)
	go func() {
		err, connected := r.connectAndRecordWithQuality(ctx, missURL, "hd")
		ch <- result{err: err, connected: connected}
	}()

	require.Eventually(t, func() bool { return pub.count() >= 2 }, 5*time.Second, 5*time.Millisecond)
	cancel()

	select {
	case got := <-ch:
		require.True(t, got.connected)
		require.ErrorIs(t, got.err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("connectAndRecordWithQuality did not return")
	}

	// Both video (AVCC) and audio (G711A) reached lal.
	pub.mu.Lock()
	var hasVideo, hasAudio bool
	for _, p := range pub.packets {
		switch p.PayloadType {
		case base.AvPacketPtAvc:
			hasVideo = true
		case base.AvPacketPtG711A:
			hasAudio = true
		}
	}
	pub.mu.Unlock()
	require.True(t, hasVideo, "expected an AVCC video packet")
	require.True(t, hasAudio, "expected a G711A audio packet")

}

func TestCovMISSClientAPISurface(t *testing.T) {
	device, missURL := covMISSDevice(t)

	client, err := NewMISSClient(missURL, 5*time.Second)
	require.NoError(t, err)
	defer client.Conn.Close()

	require.Contains(t, client.Version(), ModelC200)
	require.NoError(t, client.Conn.SetDeadline(time.Time{}))

	require.NoError(t, client.StartAudio())
	require.NoError(t, client.StopAudio())
	require.NoError(t, client.StartSpeaker())
	require.NoError(t, client.StopSpeaker())
	require.NoError(t, client.StartMedia("1", "sd"))
	require.NoError(t, client.StopMedia())
	require.NoError(t, client.StartMedia("", "auto"))
	require.NoError(t, client.WriteAudio(missCodecPCMA, []byte{1, 2, 3}))

	// Media packets are decrypted with the negotiated key.
	plaintext := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88}
	require.NoError(t, device.sendMediaPacket(missCodecH264, 1000, plaintext))
	pkt, err := client.ReadPacket()
	require.NoError(t, err)
	require.Equal(t, uint32(missCodecH264), pkt.CodecID)
	require.Equal(t, uint64(1000), pkt.Timestamp)
	require.Equal(t, plaintext, pkt.Payload)

	// Speaker codec per model.
	client.model = ModelC200
	require.Equal(t, uint32(missCodecPCMA), client.SpeakerCodec())
	client.model = ModelDafang
	require.Equal(t, uint32(missCodecPCM), client.SpeakerCodec())
	client.model = ModelXiaofang
	require.Equal(t, uint32(missCodecPCM), client.SpeakerCodec())
	client.model = "isa.camera.hlc6"
	require.Equal(t, uint32(missCodecPCM), client.SpeakerCodec())
	client.model = ModelC300
	require.Equal(t, uint32(missCodecOPUS), client.SpeakerCodec())

	// Sample rate from flags.
	pkt.Flags = 0
	require.Equal(t, uint32(8000), pkt.SampleRate())
	pkt.Flags = 1 << 3
	require.Equal(t, uint32(16000), pkt.SampleRate())

	// A truncated media header is rejected by the MISS layer.
	shortHeader := &MISSClient{Conn: &mockMISSConnReadPacket{hdr: make([]byte, 4)}, key: client.key}
	_, err = shortHeader.ReadPacket()
	require.ErrorContains(t, err, "packet header too small")

	// Unknown codecs are reported as unsupported audio.
	_, ok := missCodecToAudio(7)
	require.False(t, ok)
	require.Len(t, nalusToAVCC(nil), 0)
}

func TestCovMISSNewClientErrors(t *testing.T) {
	// Unparsable URL.
	_, err := NewMISSClient("://bad", 0)
	require.Error(t, err)

	// Bad device public key hex.
	_, err = NewMISSClient("miss://127.0.0.1:1?vendor=cs2&device_public=zz&client_private=zz", 0)
	require.Error(t, err)

	// Unsupported vendor.
	_, err = NewMISSClient("miss://127.0.0.1:1?vendor=tutk", 0)
	require.ErrorContains(t, err, "unsupported vendor")

	// Rejected login response closes the connection.
	device, missURL := covMISSDevice(t)
	device.authOK = false
	_, err = NewMISSClient(missURL, 2*time.Second)
	require.ErrorContains(t, err, "miss: auth")
}

func TestCovXiaomiRecorderVendorReporting(t *testing.T) {
	r := makeTestRecorder(t)

	// No reporter configured.
	r.reportVendorError(errors.New(`miss: unsupported vendor "tutk"`))

	reporter := &covErrorReporter{}
	r.SetErrorReporter(reporter)

	// Errors that are not vendor related are ignored.
	r.reportVendorError(nil)
	r.reportVendorError(errors.New("miss connect: timeout"))
	detail, _ := reporter.get()
	require.Nil(t, detail)

	// TUTK incompatibility is reported.
	r.reportVendorError(errors.New(`miss: unsupported vendor "tutk"`))
	detail, camera := reporter.get()
	require.NotNil(t, detail)
	require.Equal(t, "tutk_incompatible", detail.Type)
	require.Contains(t, detail.Message, "tutk")
	require.Equal(t, r.cfg.CameraID, camera)

	// Quoted value extraction edge cases.
	require.Empty(t, extractQuotedValue("no quotes here"))
	require.Empty(t, extractQuotedValue(`one "quote`))
	require.Equal(t, "value", extractQuotedValue(`key "value"`))
}

func TestCovXiaomiRecorderHelpers(t *testing.T) {
	r := makeTestRecorder(t)
	r.cfg.MaxBackoff = 10 * time.Second
	grown := r.expandBackoff(time.Second)
	require.GreaterOrEqual(t, grown, 2*time.Second)
	require.LessOrEqual(t, grown, r.cfg.MaxBackoff)

	r.cfg.MaxBackoff = time.Second
	require.Equal(t, time.Second, r.expandBackoff(2*time.Second))

	require.Equal(t, "network", classifyDisconnectReason(nil))
	require.Equal(t, "idle_timeout", classifyDisconnectReason(errors.New("cs2: no data for 30s")))
	require.Equal(t, "eof", classifyDisconnectReason(errors.New("cs2: EOF")))
	require.Equal(t, "cloud_resolve", classifyDisconnectReason(errors.New("cloud auth failed")))
	require.Equal(t, "network", classifyDisconnectReason(errors.New("reset by peer")))

	require.False(t, isTimeoutError(nil))
	require.True(t, isTimeoutError(errors.New("cs2: no media data for 15s")))
	require.True(t, isTimeoutError(errors.New("cs2: no command data for 15s")))
	require.False(t, isTimeoutError(errors.New("boom")))

	r.closeCurrentSegment()

	// Annex B splitting edge cases.
	require.Empty(t, splitAnnexBNALUs([]byte{0, 0}))
	nalus := splitAnnexBNALUs([]byte{0, 0, 1, 0x09, 0, 0, 0, 1, 0x0a, 0, 0})
	require.Len(t, nalus, 2)
	require.Equal(t, []byte{0x09}, nalus[0])
	require.Equal(t, []byte{0x0a, 0, 0}, nalus[1])
}

func TestCovXiaomiRecorderForwarding(t *testing.T) {
	pub := &covPubSession{}
	r := NewXiaomiRecorder(XiaomiRecorderConfig{CameraID: "cov-cam", AudioEnabled: true}, &noopSegmentStore{})
	r.pubSession = pub
	r.streamStart = time.Now()

	// H.264 IDR prepends SPS/PPS; a P frame is forwarded alone.
	r.codec = model.FormatH264
	r.sps = []byte{0x67, 0x01}
	r.pps = []byte{0x68, 0x02}
	r.forwardVideo([]byte{0x65, 0xaa})
	require.Equal(t, 1, pub.count())
	require.Equal(t, base.AvPacketPtAvc, pub.last().PayloadType)
	payload := pub.last().Payload
	require.Len(t, payload, 4+2+4+2+4+2)

	r.forwardVideo([]byte{0x41, 0xbb})
	require.Equal(t, 2, pub.count())
	require.Len(t, pub.last().Payload, 4+2)

	// H.265 IDR carries VPS/SPS/PPS + the frame.
	r.codec = model.FormatH265
	r.vps = []byte{0x40, 0x01}
	r.sps = []byte{0x42, 0x02}
	r.pps = []byte{0x44, 0x03}
	r.forwardVideo([]byte{0x26, 0xcc})
	require.Equal(t, 3, pub.count())
	require.Equal(t, base.AvPacketPtHevc, pub.last().PayloadType)
	require.Len(t, pub.last().Payload, 4*(4+2))

	r.forwardVideo([]byte{0x02, 0xdd})
	require.Equal(t, 4, pub.count())
	require.Len(t, pub.last().Payload, 4+2)

	// Unknown codec and missing pub session are no-ops.
	r.codec = "unknown"
	r.forwardVideo([]byte{0x41})
	require.Equal(t, 4, pub.count())
	r.pubSession = nil
	r.codec = model.FormatH264
	r.forwardVideo([]byte{0x41})
	require.Equal(t, 4, pub.count())

	// lal feed failures are logged, not fatal.
	r.pubSession = &covPubSession{err: errors.New("feed boom")}
	r.forwardVideo([]byte{0x41, 0x01})

	// Audio paths.
	failingPub := &covPubSession{err: errors.New("feed boom")}
	r.pubSession = failingPub
	r.Hub = model.NewStreamHub()
	r.forwardAudio(missCodecOPUS, []byte{0x01}) // Opus + feed error
	r.forwardAudio(missCodecPCMA, []byte{0x02}) // G711A + feed error
	r.forwardAudio(7, []byte{0x03})             // unknown codec
	r.forwardAudio(missCodecPCMA, []byte{0x04}) // codec already detected
	r.cfg.AudioEnabled = false
	r.forwardAudio(missCodecPCMA, []byte{0x05}) // disabled
	r.cfg.AudioEnabled = true
	r.pubSession = nil
	r.forwardAudio(missCodecPCMA, []byte{0x06}) // no pub session

	// Payload type mapping.
	require.Equal(t, base.AvPacketPtAvc, r.avPacketPt())
	r.codec = model.FormatH265
	require.Equal(t, base.AvPacketPtHevc, r.avPacketPt())
	r.codec = ""
	require.Equal(t, base.AvPacketPt(0), r.avPacketPt())
}

func TestCovXiaomiRecorderNALUProcessing(t *testing.T) {
	r := makeTestRecorder(t)
	var last uint64

	r.processNALU(nil, 0, &last)

	r.codec = model.FormatH264
	r.processH264NALU([]byte{0x67, 0x01}, 0, &last) // SPS
	r.processH264NALU([]byte{0x68, 0x01}, 0, &last) // PPS
	r.processH264NALU([]byte{0x65, 0x01}, 0, &last) // IDR
	r.processH264NALU([]byte{0x41, 0x01}, 0, &last) // non-IDR
	r.sps, r.pps = nil, nil
	r.processH264NALU([]byte{0x65, 0x01}, 0, &last) // IDR without params
	r.processH264NALU([]byte{0x41, 0x01}, 0, &last) // P without params

	r.codec = model.FormatH265
	r.processH265NALU([]byte{0x40, 0x01}, 0, &last) // VPS
	r.processH265NALU([]byte{0x42, 0x01}, 0, &last) // SPS
	r.processH265NALU([]byte{0x44, 0x01}, 0, &last) // PPS
	r.processH265NALU([]byte{0x80, 0x01}, 0, &last) // non-VCL
	r.processH265NALU([]byte{0x26, 0x01}, 0, &last) // IDR
	r.processH265NALU([]byte{0x02, 0x01}, 0, &last) // P
	r.vps = nil
	r.processH265NALU([]byte{0x02, 0x01}, 0, &last) // P without params

	r.codec = ""
	r.processNALU([]byte{0x41, 0x01}, 0, &last) // no codec
}

func TestCovXiaomiRecorderMetricsBranches(t *testing.T) {
	m := metrics.NewMetrics()
	r := NewXiaomiRecorder(XiaomiRecorderConfig{CameraID: "cov-cam"}, &noopSegmentStore{}, m)

	r.incActive()
	r.decActive()
	r.recordError("cloud_resolve")
	r.recordXiaomiDisconnect("cloud_resolve")
	r.recordXiaomiReconnect()
	r.reportVendorError(errors.New("no vendor here"))
}
