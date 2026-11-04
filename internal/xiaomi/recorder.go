// SPDX-License-Identifier: MIT
//
// Xiaomi camera recorder implementing model.Recorder and model.CodecParamsProvider interfaces.
// Connects via MISS protocol, probes codec (H264/H265), records to MP4 segments.

package xiaomi

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/observability"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/hevc"
	"go.opentelemetry.io/otel/attribute"
)

var xiaomiLogger = slog.Default().With("component", "xiaomi-recorder")

// SegmentStore abstracts storage operations needed by the recorder.
type SegmentStore interface {
	CreateSegment(cameraID string, format string) (tempPath string, finalPath string, err error)
	CloseSegment(tempPath, finalPath string) error
}

// RecordingDB abstracts database operations needed by the recorder.
type RecordingDB interface {
	InsertRecording(ctx context.Context, r *model.Recording) error
	InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error
}

// ErrorReporter abstracts camera error reporting to avoid circular imports.
// CameraManager satisfies this interface.
type ErrorReporter interface {
	SetErrorDetail(cameraID string, detail *model.CameraErrorDetail)
}

const (
	defaultSegmentDur  = 10 * time.Minute
	defaultMaxBackoff  = 60 * time.Second // Deprecated: no longer used, kept for config backward compatibility
	defaultInitBackoff = 1 * time.Second  // Deprecated: no longer used, kept for config backward compatibility
)

// XiaomiCloudConfig holds Xiaomi cloud API credentials for URL resolution.
type XiaomiCloudConfig struct {
	UserID string
	Token  string
	Region string
}

// XiaomiRecorderConfig holds configuration for the Xiaomi recorder.
type XiaomiRecorderConfig struct {
	CameraID            string
	DID                 string // Device ID extracted from xiaomi:// URL (e.g. "655448418")
	Channel             string
	Quality             string
	Model               string                   // Camera model (e.g. ModelC200, ModelC300)
	CloudCfg            XiaomiCloudConfig        // Credentials for callers without a provider.
	CloudConfigProvider func() XiaomiCloudConfig // Read fresh credentials before each reconnect.
	SegmentDur          time.Duration
	MaxBackoff          time.Duration // Deprecated: no longer used, tiered backoff is used instead
	InitBackoff         time.Duration // Deprecated: no longer used, tiered backoff is used instead
	DB                  RecordingDB
	ErrReporter         ErrorReporter // Optional: reports detailed errors (e.g. TUTK incompatibility)
	AudioEnabled        bool          // Capture and broadcast audio via StreamHub when true
	FirstVideoTimeout   time.Duration
	IdleTimeout         time.Duration
	EventBus            *event.EventBus
	MediaEngine         media.Engine // For feeding frames into lal
}

// XiaomiRecorder records H.264/H.265 video from a Xiaomi camera via MISS protocol.
type XiaomiRecorder struct {
	cfg        XiaomiRecorderConfig
	store      SegmentStore
	metrics    *metrics.Metrics
	cloudCache *recorderCloudCache

	lifecycleMu sync.Mutex // Serializes Start and Stop through cleanup.
	active      bool
	mu          sync.Mutex
	status      model.RecorderStatus
	cancel      context.CancelFunc
	done        chan struct{}
	retryCount  int

	motorMu         sync.Mutex
	motorClient     *MISSClient
	motorTimer      *time.Timer
	motorGeneration uint64
	motorActive     bool
	motorLease      time.Duration

	// lal custom pub session
	pubSession media.CustomizePubSession

	// Codec state (probed from first packets)
	codecMu sync.RWMutex
	codec   model.Format // "h264" or "h265"
	sps     []byte
	pps     []byte
	vps     []byte // H265 only
	codecOK bool   // true once codec type is determined

	// Audio state (probed from first audio packet)
	audioNormalizer missAudioNormalizer
	audioInfoMu     sync.RWMutex
	audioInfoCodec  string
	audioInfoConfig []byte
	audioCodecID    uint32 // MISS codec ID for audio (0 = not detected yet)

	Hub         *model.StreamHub // Frame fan-out to multiple consumers (HLS, WebRTC, etc.)
	streamStart time.Time        // For PTS rebase (used by forwardHLS)
}

// GetHub returns the StreamHub for frame fan-out.
func (r *XiaomiRecorder) GetHub() *model.StreamHub { return r.Hub }

var _ model.Recorder = (*XiaomiRecorder)(nil)
var _ model.CodecParamsProvider = (*XiaomiRecorder)(nil)

// NewXiaomiRecorder creates a new Xiaomi MISS protocol recorder.
func NewXiaomiRecorder(cfg XiaomiRecorderConfig, store SegmentStore, opts ...*metrics.Metrics) *XiaomiRecorder {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	if cfg.SegmentDur == 0 {
		cfg.SegmentDur = defaultSegmentDur
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = defaultMaxBackoff
	}
	if cfg.InitBackoff == 0 {
		cfg.InitBackoff = defaultInitBackoff
	}
	if cfg.FirstVideoTimeout == 0 {
		cfg.FirstVideoTimeout = cs2ReadTimeout
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	return &XiaomiRecorder{
		cfg:        cfg,
		store:      store,
		metrics:    m,
		cloudCache: newRecorderCloudCache(),
		status:     model.StatusStopped,
		motorLease: 2 * time.Second,
	}
}

// SetErrorReporter sets the error reporter for vendor error reporting.
func (r *XiaomiRecorder) SetErrorReporter(reporter ErrorReporter) {
	r.cfg.ErrReporter = reporter
}

// Start begins recording from the Xiaomi camera in a background goroutine.
func (r *XiaomiRecorder) Start(ctx context.Context) error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil || r.pubSession != nil || r.status == model.StatusRecording || r.status == model.StatusReconnecting {
		return fmt.Errorf("recorder for %q already running", r.cfg.CameraID)
	}
	ctx, cancel := context.WithCancel(observability.DetachedContext(ctx))
	if r.cfg.MediaEngine != nil {
		pub, err := r.cfg.MediaEngine.AddCustomizePubSession(ctx, r.cfg.CameraID)
		if err != nil {
			cancel()
			return fmt.Errorf("failed to register with lal: %w", err)
		}
		r.pubSession = pub
	}
	r.cancel = cancel
	r.done = make(chan struct{})
	r.status = model.StatusRecording
	r.streamStart = time.Now()
	r.active = true
	r.incActive()
	go r.run(ctx)
	return nil
}

// Stop waits for the producer before deleting its publish session. Concurrent
// starts/stops serialize so an old goroutine cannot close a new run's channel.
func (r *XiaomiRecorder) Stop() error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	r.stopMotorBeforeClose()
	r.mu.Lock()
	if r.cancel == nil && r.pubSession == nil {
		r.mu.Unlock()
		return nil
	}
	done, cancel, pub := r.done, r.cancel, r.pubSession
	if cancel != nil {
		cancel()
	}
	r.mu.Unlock()
	if done != nil {
		<-done
	}
	if pub != nil && r.cfg.MediaEngine != nil {
		if err := r.cfg.MediaEngine.DelCustomizePubSession(context.Background(), pub); err != nil {
			return fmt.Errorf("failed to remove lal publish session: %w", err)
		}
	}
	r.mu.Lock()
	r.pubSession = nil
	r.cancel = nil
	r.status = model.StatusStopped
	r.mu.Unlock()
	return nil
}

func (r *XiaomiRecorder) finishRun() {
	r.mu.Lock()
	r.status = model.StatusStopped
	if r.active {
		r.active = false
		r.decActive()
	}
	done := r.done
	r.mu.Unlock()
	close(done)
}

// Status returns the current recorder status.
func (r *XiaomiRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *XiaomiRecorder) setStatus(s model.RecorderStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

// CodecParams returns the current codec parameters detected from the stream.
// Implements model.CodecParamsProvider.
func (r *XiaomiRecorder) CodecParams() (codec model.Format, sps, pps, vps []byte) {
	r.codecMu.RLock()
	defer r.codecMu.RUnlock()
	return r.codec, append([]byte(nil), r.sps...), append([]byte(nil), r.pps...), append([]byte(nil), r.vps...)
}

// Codec parameter buffers are replaced rather than modified after publication.
func (r *XiaomiRecorder) codecSnapshot() (codec model.Format, sps, pps, vps []byte) {
	r.codecMu.RLock()
	defer r.codecMu.RUnlock()
	return r.codec, r.sps, r.pps, r.vps
}

func (r *XiaomiRecorder) resetCodec() {
	r.codecMu.Lock()
	defer r.codecMu.Unlock()
	r.codec = ""
	r.codecOK = false
	r.sps, r.pps, r.vps = nil, nil, nil
}

// forwardHLS feeds a video NALU to HLS consumers via StreamHub.
// For IDR frames, the codec parameter sets (SPS/PPS or VPS/SPS/PPS) are prepended.
func (r *XiaomiRecorder) forwardHLS(nalu []byte) {
	if r.Hub == nil {
		return
	}

	pts := time.Since(r.streamStart).Nanoseconds() * 90000 / int64(time.Second)

	var nalus [][]byte
	var isIDR bool
	codec, sps, pps, vps := r.codecSnapshot()
	switch codec {
	case model.FormatH264:
		naluType := nalu[0] & 0x1F
		if naluType == 5 && sps != nil && pps != nil {
			nalus = [][]byte{sps, pps, nalu}
			isIDR = true
		} else {
			nalus = [][]byte{nalu}
		}
	case model.FormatH265:
		naluType := (nalu[0] >> 1) & 0x3F
		if hevc.IsIrapNalu(naluType) && vps != nil && sps != nil && pps != nil {
			nalus = [][]byte{vps, sps, pps, nalu}
			isIDR = true
		} else {
			nalus = [][]byte{nalu}
		}
	default:
		return
	}

	r.Hub.Broadcast(pts, nalus, isIDR)
}

// incActive increments the active recordings gauge if metrics is available.
func (r *XiaomiRecorder) incActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Inc()
	}
}

// decActive decrements the active recordings gauge if metrics is available.
func (r *XiaomiRecorder) decActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Dec()
	}
}

// recordError increments the camera errors counter if metrics is available.
func (r *XiaomiRecorder) recordError(errorType string) {
	if r.metrics != nil {
		r.metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
	}
}

// classifyDisconnectReason maps an error to a disconnect reason label.
func classifyDisconnectReason(err error) string {
	if err == nil {
		return "network"
	}
	msg := err.Error()
	if strings.Contains(msg, "no data") {
		return "idle_timeout"
	}
	if strings.Contains(msg, "EOF") || strings.Contains(msg, "connection closed") {
		return "eof"
	}
	if strings.Contains(msg, "cloud") || strings.Contains(msg, "resolve") {
		return "cloud_resolve"
	}
	return "network"
}

// recordXiaomiDisconnect increments the Xiaomi disconnect counter if metrics is available.
func (r *XiaomiRecorder) recordXiaomiDisconnect(reason string) {
	if r.metrics != nil && r.metrics.XiaomiDisconnects != nil {
		r.metrics.XiaomiDisconnects.WithLabelValues(r.cfg.CameraID, reason).Inc()
	}
}

// recordXiaomiReconnect increments the Xiaomi reconnect counter if metrics is available.
func (r *XiaomiRecorder) recordXiaomiReconnect() {
	if r.metrics != nil && r.metrics.XiaomiReconnects != nil {
		r.metrics.XiaomiReconnects.WithLabelValues(r.cfg.CameraID).Inc()
	}
}

// reportVendorError checks if the error indicates an unsupported transport vendor
// and, if so, reports a detailed CameraErrorDetail via the ErrorReporter.
// This fires on every reconnect attempt so the frontend always has current state.
func (r *XiaomiRecorder) reportVendorError(err error) {
	if r.cfg.ErrReporter == nil || err == nil {
		return
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "unsupported vendor") {
		return
	}
	// Extract the unsupported vendor name from the error.
	vendor := extractQuotedValue(errMsg)
	msg := fmt.Sprintf("Camera uses unsupported transport vendor %q. Supported transports are CS2 and TUTK.", vendor)
	errorType := "unsupported_vendor"
	r.cfg.ErrReporter.SetErrorDetail(r.cfg.CameraID, &model.CameraErrorDetail{
		Type:       errorType,
		Message:    msg,
		DetectedAt: time.Now(),
	})
}

// extractQuotedValue extracts the content between the first pair of double quotes in s.
// Returns empty string if no quotes are found.
func extractQuotedValue(s string) string {
	start := strings.Index(s, `"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(s[start+1:], `"`)
	if end < 0 {
		return ""
	}
	return s[start+1 : start+1+end]
}

// expandBackoff is deprecated: the recorder now uses TieredBackoff from the recorder package.
// Kept for backward compatibility — no longer used in the reconnect loop.
func (r *XiaomiRecorder) expandBackoff(backoff time.Duration) time.Duration {
	half := max(1, int64(backoff/2))
	jitter := time.Duration(rand.Int63n(half))
	backoff = backoff*2 + jitter
	if backoff > r.cfg.MaxBackoff {
		backoff = r.cfg.MaxBackoff
	}
	return backoff
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no media data") || strings.Contains(msg, "no command data")
}

func (r *XiaomiRecorder) currentCloudConfig() XiaomiCloudConfig {
	if r.cfg.CloudConfigProvider != nil {
		return r.cfg.CloudConfigProvider()
	}
	return r.cfg.CloudCfg
}

// run is the main reconnect loop.
func (r *XiaomiRecorder) run(ctx context.Context) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			xiaomiLogger.Error("PANIC recovered in run", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()
	defer r.finishRun()

	var retryCount int
	for {
		// Resolve xiaomi://deviceID to miss://... URL via cloud API.
		resolveCtx, resolveSpan := observability.StartBusinessSpan(ctx, "camera.xiaomi.resolve_stream",
			attribute.String("nvr.camera.id", r.cfg.CameraID),
			attribute.String("nvr.media.protocol", "xiaomi"),
		)
		missURL, err := r.cloudCache.resolve(resolveCtx, r.currentCloudConfig(), r.cfg.DID, r.cfg.Model)
		observability.EndBusinessSpan(resolveSpan, err)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			retryCount++
			backoff := recorder.TieredBackoffWithJitter(retryCount)
			r.reportVendorError(err)
			xiaomiLogger.ErrorContext(resolveCtx, "failed to resolve MISS URL, retrying", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
			r.recordError("cloud_resolve")
			r.recordXiaomiDisconnect("cloud_resolve")
			r.setStatus(model.StatusReconnecting)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}

		r.retryCount = retryCount
		err, connected := r.connectAndRecord(resolveCtx, missURL)
		if ctx.Err() != nil {
			return
		}
		if connected {
			retryCount = 0
			r.recordXiaomiReconnect()
		}
		retryCount++
		backoff := recorder.TieredBackoffWithJitter(retryCount)
		xiaomiLogger.ErrorContext(resolveCtx, "connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
		r.recordError("connection")
		r.recordXiaomiDisconnect(classifyDisconnectReason(err))
		r.setStatus(model.StatusReconnecting)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// connectAndRecord connects to the Xiaomi camera, starts media, and records packets.
// Tries HD first; falls back to SD if the camera sends no video.
func (r *XiaomiRecorder) connectAndRecord(ctx context.Context, missURL string) (error, bool) {
	quality := r.cfg.Quality
	if quality == "" {
		quality = "hd"
	}
	return r.connectAndRecordWithQuality(ctx, missURL, quality)
}

// connectAndRecordWithQuality connects to the camera with the given quality ("hd" or "sd").
// If the first video packet times out on HD, retries with SD automatically.
func (r *XiaomiRecorder) connectAndRecordWithQuality(ctx context.Context, missURL string, quality string) (error, bool) {
	client, err := NewMISSClientContext(ctx, missURL, r.cfg.IdleTimeout)
	if err != nil {
		return fmt.Errorf("miss connect: %w", err), false
	}
	defer client.Conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = client.Conn.Close() })
	defer stopCancel()

	ctx, err = observability.RunBusinessOperation(ctx, "recorder.xiaomi.start_media", func(context.Context) error {
		return client.StartMedia(r.cfg.Channel, quality, r.cfg.AudioEnabled)
	},
		attribute.String("nvr.camera.id", r.cfg.CameraID),
		attribute.String("nvr.stream.id", r.cfg.CameraID),
		attribute.String("nvr.media.protocol", "xiaomi"),
		attribute.String("nvr.media.quality", quality),
		attribute.Int("nvr.connection.retry_count", r.retryCount),
	)
	if err != nil {
		return fmt.Errorf("miss start media: %w", err), false
	}
	defer func() {
		_ = client.StopMedia()
	}()

	r.attachMotorClient(client)
	defer r.detachMotorClient(client)
	r.setStatus(model.StatusRecording)

	// Reset codec probe state for each new connection.
	r.resetCodec()
	r.audioCodecID = 0
	r.audioInfoMu.Lock()
	r.audioInfoCodec, r.audioInfoConfig = "", nil
	r.audioInfoMu.Unlock()
	r.audioNormalizer = missAudioNormalizer{}

	var lastTimestamp uint64
	videoReceived := false
	var videoTimedOut atomic.Bool
	firstVideoTimer := time.AfterFunc(r.cfg.FirstVideoTimeout, func() {
		videoTimedOut.Store(true)
		_ = client.Conn.Close()
	})
	defer firstVideoTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			r.closeCurrentSegment()
			return ctx.Err(), true
		default:
		}

		pkt, err := client.ReadPacket()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err(), videoReceived
			}
			if !videoReceived && videoTimedOut.Load() {
				err = fmt.Errorf("no media data: no video within %v", r.cfg.FirstVideoTimeout)
			}
			// Audio-only traffic must not suppress the first-video deadline.
			if !videoReceived && quality == "hd" && (videoTimedOut.Load() || isTimeoutError(err)) {
				xiaomiLogger.Warn("no HD data from camera, trying SD fallback",
					"camera_id", r.cfg.CameraID, "model", r.cfg.Model)
				client.Conn.Close()
				_ = client.StopMedia()
				return r.connectAndRecordWithQuality(ctx, missURL, "sd")
			}
			r.closeCurrentSegment()
			return fmt.Errorf("miss read: %w", err), videoReceived
		}

		// Handle audio packets when AudioEnabled.
		if pkt.CodecID >= 1024 {
			if r.cfg.AudioEnabled {
				r.forwardAudio(pkt.CodecID, pkt.Payload, pkt.SampleRate())
			}
			continue
		}

		// Skip other non-video packets.
		if pkt.CodecID != missCodecH264 && pkt.CodecID != missCodecH265 {
			continue
		}

		if !videoReceived {
			firstVideoTimer.Stop()
			videoReceived = true
		}
		// Probe codec type from first video packet.
		r.codecMu.Lock()
		if !r.codecOK {
			switch pkt.CodecID {
			case missCodecH264:
				r.codec = model.FormatH264
			case missCodecH265:
				r.codec = model.FormatH265
			}
			r.codecOK = true
			xiaomiLogger.Info("codec detected", "camera_id", r.cfg.CameraID, "codec", r.codec, "quality", quality)
		}
		r.codecMu.Unlock()

		// Parse Annex B NALUs from payload and process each one.
		nalus := splitAnnexBNALUs(pkt.Payload)
		for _, nalu := range nalus {
			r.processNALU(nalu, pkt.Timestamp, &lastTimestamp)
		}
	}
}

// processNALU handles a single NALU extracted from Annex B payload.
func (r *XiaomiRecorder) processNALU(nalu []byte, timestamp uint64, lastTimestamp *uint64) {
	if len(nalu) == 0 {
		return
	}

	codec, _, _, _ := r.codecSnapshot()
	switch codec {
	case model.FormatH264:
		r.processH264NALU(nalu, timestamp, lastTimestamp)
	case model.FormatH265:
		r.processH265NALU(nalu, timestamp, lastTimestamp)
	}
}

// processH264NALU handles an H.264 NAL unit.
func (r *XiaomiRecorder) processH264NALU(nalu []byte, timestamp uint64, lastTimestamp *uint64) {
	_, sps, pps, _ := r.codecSnapshot()
	naluType := nalu[0] & 0x1F
	switch naluType {
	case 7: // SPS
		r.codecMu.Lock()
		r.sps = append([]byte(nil), nalu...)
		r.codecMu.Unlock()
		return
	case 8: // PPS
		r.codecMu.Lock()
		r.pps = append([]byte(nil), nalu...)
		r.codecMu.Unlock()
		return
	case 5: // IDR
		if sps == nil || pps == nil {
			return
		}
		r.forwardVideo(nalu)
	case 1: // non-IDR
		if sps == nil || pps == nil {
			return
		}
		r.forwardVideo(nalu)
	}
}

// processH265NALU handles an H.265/HEVC NAL unit.
func (r *XiaomiRecorder) processH265NALU(nalu []byte, timestamp uint64, lastTimestamp *uint64) {
	naluType := (nalu[0] >> 1) & 0x3F
	switch naluType {
	case 32: // VPS
		r.codecMu.Lock()
		r.vps = append([]byte(nil), nalu...)
		r.codecMu.Unlock()
		return
	case 33: // SPS
		r.codecMu.Lock()
		r.sps = append([]byte(nil), nalu...)
		r.codecMu.Unlock()
		return
	case 34: // PPS
		r.codecMu.Lock()
		r.pps = append([]byte(nil), nalu...)
		r.codecMu.Unlock()
		return
	}

	// Only forward VCL NALUs (types 0-31)
	if naluType >= 32 {
		return
	}
	_, sps, pps, vps := r.codecSnapshot()
	if vps == nil || sps == nil || pps == nil {
		return
	}
	r.forwardVideo(nalu)
}

// forwardVideo feeds a video NALU to lal via CustomizePubSession.
// For IDR frames, the codec parameter sets (SPS/PPS or VPS/SPS/PPS) are prepended.
// Payload is converted to AVCC format (4-byte length prefix per NALU) as required by lal.
func (r *XiaomiRecorder) forwardVideo(nalu []byte) {
	r.mu.Lock()
	pub := r.pubSession
	start := r.streamStart
	r.mu.Unlock()
	if pub == nil {
		return
	}

	pts := time.Since(start).Milliseconds()

	var nalus [][]byte
	codec, sps, pps, vps := r.codecSnapshot()
	switch codec {
	case model.FormatH264:
		naluType := nalu[0] & 0x1F
		if naluType == 5 && sps != nil && pps != nil {
			nalus = [][]byte{sps, pps, nalu}
		} else {
			nalus = [][]byte{nalu}
		}
	case model.FormatH265:
		naluType := (nalu[0] >> 1) & 0x3F
		if hevc.IsIrapNalu(naluType) && vps != nil && sps != nil && pps != nil {
			nalus = [][]byte{vps, sps, pps, nalu}
		} else {
			nalus = [][]byte{nalu}
		}
	default:
		return
	}

	// Convert to AVCC format: each NALU prefixed with 4-byte big-endian length.
	payload := nalusToAVCC(nalus)
	if len(payload) == 0 {
		return
	}

	pkt := base.AvPacket{
		PayloadType: r.avPacketPt(),
		Timestamp:   pts,
		Pts:         pts,
		Payload:     payload,
	}

	if err := pub.FeedAvPacket(pkt); err != nil {
		xiaomiLogger.Error("failed to feed video packet to lal", "camera_id", r.cfg.CameraID, "error", err)
	}
}

// avPacketPt returns the lal payload type for the current codec.
func (r *XiaomiRecorder) avPacketPt() base.AvPacketPt {
	codec, _, _, _ := r.codecSnapshot()
	switch codec {
	case model.FormatH264:
		return base.AvPacketPtAvc
	case model.FormatH265:
		return base.AvPacketPtHevc
	default:
		return 0
	}
}

// nalusToAVCC converts a slice of NALUs to AVCC format (4-byte length prefix per NALU).
func nalusToAVCC(nalus [][]byte) []byte {
	totalSize := 0
	for _, n := range nalus {
		totalSize += 4 + len(n)
	}
	result := make([]byte, 0, totalSize)
	lenBuf := make([]byte, 4)
	for _, n := range nalus {
		binary.BigEndian.PutUint32(lenBuf, uint32(len(n)))
		result = append(result, lenBuf...)
		result = append(result, n...)
	}
	return result
}

// missCodecToAudio maps a MISS audio codec ID to a model.AudioCodec.
// Returns (codec, true) for known codecs, ("", false) for unknown/unsupported.
func missCodecToAudio(codecID uint32) (model.AudioCodec, bool) {
	switch codecID {
	case missCodecPCMA, missCodecPCMU, missCodecPCM:
		return model.AudioG711, true
	case missCodecOPUS:
		return model.AudioOpus, true
	case missCodecAAC:
		return model.AudioAAC, true
	default:
		return "", false
	}
}

// forwardAudio feeds audio data to lal via CustomizePubSession and to HLS consumers via StreamHub.
func (r *XiaomiRecorder) forwardAudio(codecID uint32, payload []byte, sampleRates ...uint32) {
	rate := uint32(8000)
	if len(sampleRates) > 0 {
		rate = sampleRates[0]
	}
	if !r.cfg.AudioEnabled {
		return
	}
	if codecID == missCodecAAC {
		r.forwardLegacyAAC(payload)
		return
	}
	audioCodec, ok := missCodecToAudio(codecID)
	if !ok {
		xiaomiLogger.Warn("skipping unknown audio codec", "camera_id", r.cfg.CameraID, "codec_id", codecID)
		return
	}

	if r.audioCodecID == 0 {
		r.audioCodecID = codecID
		xiaomiLogger.Info("audio codec detected", "camera_id", r.cfg.CameraID, "codec_id", codecID, "codec", audioCodec)
	}

	pts := time.Since(r.streamStart).Nanoseconds() * 90000 / int64(time.Second)

	payload, err := r.audioNormalizer.normalize(codecID, rate, payload)
	if err != nil {
		xiaomiLogger.Warn("invalid audio sample rate or payload", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	if len(payload) == 0 {
		return
	} // Carry an odd 16 kHz sample into the next packet.
	pt, lalPayload, hubPayload, err := missAudioPayload(codecID, payload)
	if err != nil {
		xiaomiLogger.Warn("invalid audio payload", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	if audioCodec == model.AudioG711 {
		r.audioInfoMu.Lock()
		r.audioInfoCodec = "g711"
		r.audioInfoConfig = []byte{0, 0, 0, 0x1f, 0x40}
		r.audioInfoMu.Unlock()
	}
	// StreamHub uses 90 kHz; lal AvPacket uses milliseconds.
	if r.Hub != nil {
		r.Hub.BroadcastAudio(pts, audioCodec, hubPayload)
	}

	r.mu.Lock()
	pub := r.pubSession
	r.mu.Unlock()
	if pub == nil {
		return
	}

	pkt := base.AvPacket{
		PayloadType: pt,
		Timestamp:   pts / 90,
		Pts:         pts / 90,
		Payload:     lalPayload,
	}

	if err := pub.FeedAvPacket(pkt); err != nil {
		xiaomiLogger.Error("failed to feed audio packet to lal", "camera_id", r.cfg.CameraID, "error", err)
	}
}

// closeCurrentSegment finalizes the current MP4 segment.
func (r *XiaomiRecorder) closeCurrentSegment() {
	// No-op: recording is now handled by lal via RTSP pull.
}

// splitAnnexBNALUs splits Annex B formatted data into individual NALUs.
// It finds 00 00 00 01 or 00 00 01 start codes and returns the data between them.
func splitAnnexBNALUs(data []byte) [][]byte {
	var nalus [][]byte
	start := -1 // -1 means we haven't found the first start code yet

	for i := 0; i <= len(data)-3; i++ {
		if data[i] == 0 && data[i+1] == 0 {
			scLen := 0
			if data[i+2] == 1 {
				scLen = 3
			} else if i <= len(data)-4 && data[i+2] == 0 && data[i+3] == 1 {
				scLen = 4
			}
			if scLen > 0 {
				// If we had a previous start, extract the NALU up to this start code.
				if start >= 0 {
					// Trim trailing zeros before the start code.
					end := i
					for end > start && data[end-1] == 0 {
						end--
					}
					if end > start {
						nalus = append(nalus, bytes.Clone(data[start:end]))
					}
				}
				start = i + scLen
				i += scLen - 1
			}
		}
	}

	// Append the last NALU.
	if start >= 0 && start < len(data) {
		nalus = append(nalus, bytes.Clone(data[start:]))
	}

	return nalus
}

// annexBToAVCC converts Annex B formatted H264/H265 NALUs to AVCC format.
// It finds start codes, extracts NALUs, and prepends 4-byte big-endian length to each.
func annexBToAVCC(data []byte) []byte {
	nalus := splitAnnexBNALUs(data)
	if len(nalus) == 0 {
		return nil
	}

	// Calculate total size: sum of (4-byte length + nalu) for each NALU.
	totalSize := 0
	for _, nalu := range nalus {
		totalSize += 4 + len(nalu)
	}

	// Build AVCC buffer.
	result := make([]byte, 0, totalSize)
	lenBuf := make([]byte, 4)
	for _, nalu := range nalus {
		binary.BigEndian.PutUint32(lenBuf, uint32(len(nalu)))
		result = append(result, lenBuf...)
		result = append(result, nalu...)
	}
	return result
}
