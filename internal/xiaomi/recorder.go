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
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/q191201771/lal/pkg/base"
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
	CameraID     string
	DID          string            // Device ID extracted from xiaomi:// URL (e.g. "655448418")
	Model        string            // Camera model (e.g. ModelC200, ModelC300)
	CloudCfg     XiaomiCloudConfig // Cloud API credentials for MISS URL resolution
	SegmentDur   time.Duration
	MaxBackoff   time.Duration // Deprecated: no longer used, tiered backoff is used instead
	InitBackoff  time.Duration // Deprecated: no longer used, tiered backoff is used instead
	DB           RecordingDB
	ErrReporter  ErrorReporter // Optional: reports detailed errors (e.g. TUTK incompatibility)
	AudioEnabled bool          // Capture and broadcast audio via StreamHub when true
	IdleTimeout  time.Duration
	EventBus     *event.EventBus
	MediaEngine  media.Engine // For feeding frames into lal
}

// XiaomiRecorder records H.264/H.265 video from a Xiaomi camera via MISS protocol.
type XiaomiRecorder struct {
	cfg     XiaomiRecorderConfig
	store   SegmentStore
	metrics *metrics.Metrics

	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	done   chan struct{}

	// lal custom pub session
	pubSession media.CustomizePubSession

	// Codec state (probed from first packets)
	codec   model.Format // "h264" or "h265"
	sps     []byte
	pps     []byte
	vps     []byte // H265 only
	codecOK bool   // true once codec type is determined

	// Audio state (probed from first audio packet)
	audioCodecID uint32 // MISS codec ID for audio (0 = not detected yet)

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
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	return &XiaomiRecorder{
		cfg:     cfg,
		store:   store,
		metrics: m,
		status:  model.StatusStopped,
	}
}

// SetErrorReporter sets the error reporter for vendor error reporting.
func (r *XiaomiRecorder) SetErrorReporter(reporter ErrorReporter) {
	r.cfg.ErrReporter = reporter
}

// Start begins recording from the Xiaomi camera in a background goroutine.
func (r *XiaomiRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.status == model.StatusRecording || r.status == model.StatusReconnecting {
		r.mu.Unlock()
		return fmt.Errorf("recorder for %q already running", r.cfg.CameraID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	r.status = model.StatusRecording
	r.incActive()
	r.streamStart = time.Now()

	// Register with lal if mediaEngine is available
	if r.cfg.MediaEngine != nil {
		pubSession, err := r.cfg.MediaEngine.AddCustomizePubSession(ctx, r.cfg.CameraID)
		if err != nil {
			r.mu.Unlock()
			r.decActive()
			return fmt.Errorf("failed to register with lal: %w", err)
		}
		r.pubSession = pubSession
	}
	r.mu.Unlock()

	go r.run(ctx)
	return nil
}

// Stop terminates the recording goroutine and waits for it to finish.
func (r *XiaomiRecorder) Stop() error {
	r.mu.Lock()
	if r.status == model.StatusStopped {
		r.mu.Unlock()
		return nil
	}
	r.status = model.StatusStopped
	r.decActive()
	if r.cancel != nil {
		r.cancel()
	}
	// Clean up lal pub session
	if r.pubSession != nil && r.cfg.MediaEngine != nil {
		_ = r.cfg.MediaEngine.DelCustomizePubSession(context.Background(), r.pubSession)
		r.pubSession = nil
	}
	r.mu.Unlock()

	<-r.done
	return nil
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
	return r.codec, r.sps, r.pps, r.vps
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
	switch r.codec {
	case model.FormatH264:
		naluType := nalu[0] & 0x1F
		if naluType == 5 && r.sps != nil && r.pps != nil {
			nalus = [][]byte{r.sps, r.pps, nalu}
			isIDR = true
		} else {
			nalus = [][]byte{nalu}
		}
	case model.FormatH265:
		naluType := (nalu[0] >> 1) & 0x3F
		if (naluType == 19 || naluType == 20) && r.vps != nil && r.sps != nil && r.pps != nil {
			nalus = [][]byte{r.vps, r.sps, r.pps, nalu}
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

// reportVendorError checks if the error indicates an unsupported TUTK vendor
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
	// Extract vendor name from error: "miss: unsupported vendor \"tutk\""
	vendor := extractQuotedValue(errMsg)
	msg := fmt.Sprintf("Camera uses unsupported transport vendor %q (TUTK). This camera model is not compatible.", vendor)
	r.cfg.ErrReporter.SetErrorDetail(r.cfg.CameraID, &model.CameraErrorDetail{
		Type:       "tutk_incompatible",
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

// run is the main reconnect loop.
func (r *XiaomiRecorder) run(ctx context.Context) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			xiaomiLogger.Error("PANIC recovered in run", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()
	defer close(r.done)
	defer r.setStatus(model.StatusStopped)

	var retryCount int
	for {
		// Resolve xiaomi://deviceID to miss://... URL via cloud API.
		missURL, err := ResolveMISSURL(r.cfg.CloudCfg, r.cfg.DID, r.cfg.Model)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			retryCount++
			backoff := recorder.TieredBackoffWithJitter(retryCount)
			r.reportVendorError(err)
			xiaomiLogger.Error("failed to resolve MISS URL, retrying", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
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

		err, connected := r.connectAndRecord(ctx, missURL)
		if ctx.Err() != nil {
			return
		}
		if connected {
			retryCount = 0
			r.recordXiaomiReconnect()
		}
		retryCount++
		backoff := recorder.TieredBackoffWithJitter(retryCount)
		xiaomiLogger.Error("connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
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
// Tries HD first; falls back to SD if the camera sends no data on first packet.
func (r *XiaomiRecorder) connectAndRecord(ctx context.Context, missURL string) (error, bool) {
	return r.connectAndRecordWithQuality(ctx, missURL, "hd")
}

// connectAndRecordWithQuality connects to the camera with the given quality ("hd" or "sd").
// If the first packet times out on HD, retries with SD automatically.
func (r *XiaomiRecorder) connectAndRecordWithQuality(ctx context.Context, missURL string, quality string) (error, bool) {
	client, err := NewMISSClient(missURL, r.cfg.IdleTimeout)
	if err != nil {
		return fmt.Errorf("miss connect: %w", err), false
	}
	defer client.Conn.Close()

	if err := client.StartMedia("", quality); err != nil {
		return fmt.Errorf("miss start media: %w", err), false
	}
	defer func() {
		_ = client.StopMedia()
	}()

	r.setStatus(model.StatusRecording)

	// Reset codec probe state for each new connection.
	r.codecOK = false
	r.sps = nil
	r.pps = nil
	r.vps = nil
	r.audioCodecID = 0

	var lastTimestamp uint64
	firstPacket := true

	for {
		select {
		case <-ctx.Done():
			r.closeCurrentSegment()
			return ctx.Err(), true
		default:
		}

		pkt, err := client.ReadPacket()
		if err != nil {
			// If timeout on first packet with HD, try SD fallback.
			if firstPacket && quality == "hd" && isTimeoutError(err) {
				xiaomiLogger.Warn("no HD data from camera, trying SD fallback",
					"camera_id", r.cfg.CameraID, "model", r.cfg.Model)
				client.Conn.Close()
				_ = client.StopMedia()
				return r.connectAndRecordWithQuality(ctx, missURL, "sd")
			}
			r.closeCurrentSegment()
			return fmt.Errorf("miss read: %w", err), false
		}

		firstPacket = false

		// Handle audio packets when AudioEnabled.
		if pkt.CodecID >= 1024 {
			if r.cfg.AudioEnabled {
				r.forwardAudio(pkt.CodecID, pkt.Payload)
			}
			continue
		}

		// Skip other non-video packets.
		if pkt.CodecID != missCodecH264 && pkt.CodecID != missCodecH265 {
			continue
		}

		// Probe codec type from first video packet.
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

	switch r.codec {
	case model.FormatH264:
		r.processH264NALU(nalu, timestamp, lastTimestamp)
	case model.FormatH265:
		r.processH265NALU(nalu, timestamp, lastTimestamp)
	}
}

// processH264NALU handles an H.264 NAL unit.
func (r *XiaomiRecorder) processH264NALU(nalu []byte, timestamp uint64, lastTimestamp *uint64) {
	naluType := nalu[0] & 0x1F
	switch naluType {
	case 7: // SPS
		r.sps = append([]byte(nil), nalu...)
		return
	case 8: // PPS
		r.pps = append([]byte(nil), nalu...)
		return
	case 5: // IDR
		if r.sps == nil || r.pps == nil {
			return
		}
		r.forwardVideo(nalu)
	case 1: // non-IDR
		if r.sps == nil || r.pps == nil {
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
		r.vps = append([]byte(nil), nalu...)
		return
	case 33: // SPS
		r.sps = append([]byte(nil), nalu...)
		return
	case 34: // PPS
		r.pps = append([]byte(nil), nalu...)
		return
	}

	// Only forward VCL NALUs (types 0-31)
	if naluType >= 32 {
		return
	}
	if r.vps == nil || r.sps == nil || r.pps == nil {
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

	pts := time.Since(start).Nanoseconds() * 90000 / int64(time.Second)

	var nalus [][]byte
	switch r.codec {
	case model.FormatH264:
		naluType := nalu[0] & 0x1F
		if naluType == 5 && r.sps != nil && r.pps != nil {
			nalus = [][]byte{r.sps, r.pps, nalu}
		} else {
			nalus = [][]byte{nalu}
		}
	case model.FormatH265:
		naluType := (nalu[0] >> 1) & 0x3F
		if (naluType == 19 || naluType == 20) && r.vps != nil && r.sps != nil && r.pps != nil {
			nalus = [][]byte{r.vps, r.sps, r.pps, nalu}
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
	switch r.codec {
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
	default:
		return "", false
	}
}

// forwardAudio feeds audio data to lal via CustomizePubSession and to HLS consumers via StreamHub.
func (r *XiaomiRecorder) forwardAudio(codecID uint32, payload []byte) {
	if !r.cfg.AudioEnabled {
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

	// Broadcast to HLS consumers via StreamHub
	if r.Hub != nil {
		r.Hub.BroadcastAudio(pts, audioCodec, payload)
	}

	r.mu.Lock()
	pub := r.pubSession
	r.mu.Unlock()
	if pub == nil {
		return
	}

	var pkt base.AvPacket
	switch audioCodec {
	case model.AudioG711:
		pkt = base.AvPacket{
			PayloadType: base.AvPacketPtG711A,
			Timestamp:   pts,
			Pts:         pts,
			Payload:     payload,
		}
	case model.AudioOpus:
		pkt = base.AvPacket{
			PayloadType: base.AvPacketPtOpus,
			Timestamp:   pts,
			Pts:         pts,
			Payload:     payload,
		}
	default:
		return
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
