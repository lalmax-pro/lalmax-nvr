package api

import (
	"log/slog"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
)

// StreamHandler is a protocol-agnostic interface for live streaming handlers.
// Each streaming protocol (HLS, WebRTC, FLV, LL-HLS) implements this interface
// so the API layer can start/stop streams without type-switch spaghetti.
type StreamHandler interface {
	// Name returns the protocol identifier (e.g. "hls", "webrtc", "flv", "ll-hls").
	Name() string
	// CanHandle returns true if this handler supports the given codec format.
	CanHandle(codec model.Format) bool
	// StartStream starts a live stream for the given camera using the provided recorder.
	StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error
	// StopStream stops the live stream for the given camera.
	StopStream(camID string) error
}

// StreamStartOptions holds optional parameters for starting a stream.
type StreamStartOptions struct {
	MaxFPS       int
	SubStreamURL string
}

// StreamRegistry manages registered stream handlers and provides
// protocol availability queries per codec format.
type StreamRegistry struct {
	handlers []StreamHandler
}

// NewStreamRegistry creates an empty StreamRegistry.
func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{}
}

// Register adds a stream handler to the registry.
func (r *StreamRegistry) Register(h StreamHandler) {
	r.handlers = append(r.handlers, h)
}

// HandlersForCodec returns all handlers that can handle the given codec format.
func (r *StreamRegistry) HandlersForCodec(codec model.Format) []StreamHandler {
	var result []StreamHandler
	for _, h := range r.handlers {
		if h.CanHandle(codec) {
			result = append(result, h)
		}
	}
	return result
}

// ProtocolsForCodec returns the names of all protocols that support the given codec.
func (r *StreamRegistry) ProtocolsForCodec(codec model.Format) []string {
	var result []string
	for _, h := range r.handlers {
		if h.CanHandle(codec) {
			result = append(result, h.Name())
		}
	}
	return result
}

// ProtocolDetail describes a protocol's availability for the API response.
type ProtocolDetail struct {
	Protocol  string
	Available bool
	Reason    string
	PlayURL   string
	Backend   string
}

// ProtocolsDetailForCodec returns detailed protocol availability for the given codec.
// Each handler contributes its availability and optional reason if unavailable.
func (r *StreamRegistry) ProtocolsDetailForCodec(codec model.Format) []ProtocolDetail {
	var result []ProtocolDetail
	for _, h := range r.handlers {
		if h.CanHandle(codec) {
			result = append(result, ProtocolDetail{
				Protocol:  h.Name(),
				Available: true,
			})
		} else if dis, ok := h.(ConditionalHandler); ok {
			// Handler supports this codec but is conditionally unavailable
			if dis.SupportedCodec(codec) {
				result = append(result, ProtocolDetail{
					Protocol:  h.Name(),
					Available: false,
					Reason:    dis.UnavailabilityReason(codec),
				})
			}
		}
	}
	return result
}

// Handler returns the stream handler by name, or nil if not found.
func (r *StreamRegistry) Handler(name string) StreamHandler {
	for _, h := range r.handlers {
		if h.Name() == name {
			return h
		}
	}
	return nil
}

// --- HLSStreamHandler ---

// HLSStreamHandler implements StreamHandler for HLS live streaming.
// HLS is proxied to the lalmax media engine; this struct exists for protocol
// capability reporting in the StreamRegistry.
type HLSStreamHandler struct{}

// Name returns the protocol identifier for HLS.
func (h *HLSStreamHandler) Name() string { return "hls" }

// CanHandle returns true for H.264 and H.265 formats (HLS supports both).
func (h *HLSStreamHandler) CanHandle(codec model.Format) bool {
	return codec == model.FormatH264 || codec == model.FormatH265
}

func (h *HLSStreamHandler) StartStream(_ string, _ model.Recorder, _ StreamStartOptions) error {
	return nil
}

func (h *HLSStreamHandler) StopStream(_ string) error {
	return nil
}

var streamLogger = slog.Default().With("component", "stream-handler")

// unwrapper is an interface for recorders that delegate to an inner recorder.
// This avoids importing recorder.ONVIFRecorder directly in the stream handler.
type unwrapper interface {
	Delegate() model.Recorder
}

// unwrapDelegate returns the innermost recorder by unwrapping delegate layers (e.g. ONVIF).
// If the recorder is not a delegator, it returns the recorder unchanged.
func unwrapDelegate(rec model.Recorder) model.Recorder {
	for {
		if u, ok := rec.(unwrapper); ok {
			if d := u.Delegate(); d != nil {
				rec = d
				continue
			}
		}
		return rec
	}
}

// getCodecParams extracts codec parameters from a recorder.
// Uses CodecParamsProvider interface first, then falls back to concrete type access.
func getCodecParams(rec model.Recorder) (codec model.Format, sps, pps, vps []byte) {
	if provider, ok := rec.(model.CodecParamsProvider); ok {
		codec, sps, pps, vps = provider.CodecParams()
		if sps != nil && pps != nil {
			return
		}
		// CodecParamsProvider returned empty params — fall through to concrete type switch
	}

	actualRec := unwrapDelegate(rec)
	switch r := actualRec.(type) {
	case *recorder.H264Recorder:
		codec = model.FormatH264
		sps = r.SPS()
		pps = r.PPS()
	case *recorder.H265Recorder:
		codec = model.FormatH265
		vps = r.VPS()
		sps = r.SPS()
		pps = r.PPS()
	}
	return
}

// getAudioParams extracts the audio codec and muxer config from a recorder.
// Returns ("", nil) when the recorder exposes no audio track. The codec string
// is "aac" or "g711"; the config blob is the AudioSpecificConfig (AAC) or the
// recorder's G.711 descriptor.
func getAudioParams(rec model.Recorder) (codec string, config []byte) {
	type audioProvider interface {
		AudioCodec() string
		AudioMuxerConfig() []byte
	}
	if p, ok := unwrapDelegate(rec).(audioProvider); ok {
		return p.AudioCodec(), p.AudioMuxerConfig()
	}
	if p, ok := rec.(audioProvider); ok {
		return p.AudioCodec(), p.AudioMuxerConfig()
	}
	return "", nil
}

// getStreamHub extracts the StreamHub from a recorder.
// Returns nil if the recorder doesn't have a Hub or it's not set.
func getStreamHub(rec model.Recorder) *model.StreamHub {
	if h, ok := rec.(interface{ GetHub() *model.StreamHub }); ok {
		return h.GetHub()
	}
	actualRec := unwrapDelegate(rec)
	switch r := actualRec.(type) {
	case *recorder.H264Recorder:
		return r.Hub
	case *recorder.H265Recorder:
		return r.Hub
	case *recorder.MJPEGRecorder:
		return r.Hub
	case *recorder.HTTPJPEGRecorder:
		return r.Hub
	case *recorder.ONVIFRecorder:
		return r.Hub
	case *xiaomi.XiaomiRecorder:
		return r.Hub
	}
	return nil
}

// SetStreamRegistry sets the stream registry on the handler for protocol queries.
func (h *Handler) SetStreamRegistry(reg *StreamRegistry) {
	h.streamRegistry = reg
}

// --- WebRTCStreamHandler ---

// WebRTCStreamHandler implements StreamHandler for WebRTC WHEP.
// WebRTC streams start/stop on-demand via WHEP POST/DELETE, so StartStream
// and StopStream are no-ops. This registration exists purely for protocol
// discovery (so /api/cameras/{id}/protocols returns the correct list).
type WebRTCStreamHandler struct{}

func (h *WebRTCStreamHandler) Name() string { return "webrtc" }

func (h *WebRTCStreamHandler) CanHandle(codec model.Format) bool {
	return codec == model.FormatH264 // WebRTC only supports H.264
}

func (h *WebRTCStreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	return nil // WebRTC streams start on-demand via WHEP POST
}

func (h *WebRTCStreamHandler) StopStream(camID string) error {
	return nil // WebRTC streams stop via WHEP DELETE
}

// --- FLVStreamHandler ---

// FLVStreamHandler implements StreamHandler for HTTP-FLV.
// FLV streams start/stop on-demand via GET /stream.flv, so StartStream
// and StopStream are no-ops. This registration exists purely for protocol
// discovery (so /api/cameras/{id}/protocols returns the correct list).
type FLVStreamHandler struct{}

func (h *FLVStreamHandler) Name() string { return "flv" }

func (h *FLVStreamHandler) CanHandle(codec model.Format) bool {
	return codec == model.FormatH264 || codec == model.FormatH265
}

func (h *FLVStreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	return nil // FLV streams start on-demand via GET /stream.flv
}

func (h *FLVStreamHandler) StopStream(camID string) error {
	return nil // FLV streams stop when client disconnects
}

// --- WSFLVStreamHandler ---

// WSFLVStreamHandler implements StreamHandler for WebSocket-FLV.
// WS-FLV streams are served by lalmax on-demand, so StartStream and StopStream
// are no-ops. This registration exists for protocol discovery only.
type WSFLVStreamHandler struct{}

func (h *WSFLVStreamHandler) Name() string { return "ws-flv" }

func (h *WSFLVStreamHandler) CanHandle(codec model.Format) bool {
	return codec == model.FormatH264 || codec == model.FormatH265
}

func (h *WSFLVStreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	return nil
}

func (h *WSFLVStreamHandler) StopStream(camID string) error {
	return nil
}

// --- ConditionalHandler interface ---

// ConditionalHandler is an optional interface for StreamHandlers that may be
// unavailable even for supported codecs (e.g., LL-HLS when low-latency is disabled).
type ConditionalHandler interface {
	// SupportedCodec returns true if this handler would normally support the codec,
	// regardless of current availability state.
	SupportedCodec(codec model.Format) bool
	// UnavailabilityReason returns a human-readable reason why the protocol is unavailable.
	UnavailabilityReason(codec model.Format) string
}

// --- WSStreamHandler ---

// WSStreamHandler implements StreamHandler for WebSocket streaming.
// WebSocket streams start/stop on-demand via GET /stream/ws, so StartStream
// and StopStream are no-ops. This registration exists purely for protocol
// discovery (so /api/cameras/{id}/protocols returns the correct list).
type WSStreamHandler struct{}

func (h *WSStreamHandler) Name() string { return "wasm" }

func (h *WSStreamHandler) CanHandle(codec model.Format) bool {
	return codec == model.FormatH264 || codec == model.FormatH265
}

func (h *WSStreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	return nil // WebSocket streams start on-demand via GET /stream/ws
}

func (h *WSStreamHandler) StopStream(camID string) error {
	return nil // WebSocket streams stop when client disconnects
}

// --- FMP4StreamHandler ---

// FMP4StreamHandler implements StreamHandler for HTTP fMP4 streaming.
// fMP4 streams are served on-demand via GET /stream.m4s, proxied to the
// lalmax media engine's HTTP fMP4 endpoint. StartStream/StopStream are no-ops.
type FMP4StreamHandler struct{}

func (h *FMP4StreamHandler) Name() string { return "fmp4" }

func (h *FMP4StreamHandler) CanHandle(codec model.Format) bool {
	return codec == model.FormatH264 || codec == model.FormatH265
}

func (h *FMP4StreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	return nil // fMP4 streams start on-demand via GET /stream.m4s
}

func (h *FMP4StreamHandler) StopStream(camID string) error {
	return nil // fMP4 streams stop when client disconnects
}

// StaticStreamHandler is a capability-only protocol registration used when
// the actual media I/O is owned by media.Engine instead of legacy managers.
type StaticStreamHandler struct {
	Protocol string
	Codecs   []model.Format
}

func (h *StaticStreamHandler) Name() string { return h.Protocol }

func (h *StaticStreamHandler) CanHandle(codec model.Format) bool {
	for _, supported := range h.Codecs {
		if supported == codec {
			return true
		}
	}
	return false
}

func (h *StaticStreamHandler) StartStream(camID string, rec model.Recorder, opts StreamStartOptions) error {
	return nil
}

func (h *StaticStreamHandler) StopStream(camID string) error {
	return nil
}

type ConditionalStaticStreamHandler struct {
	StaticStreamHandler
	Available bool
	Reason    string
}

func (h *ConditionalStaticStreamHandler) CanHandle(codec model.Format) bool {
	return h.Available && h.StaticStreamHandler.CanHandle(codec)
}

func (h *ConditionalStaticStreamHandler) SupportedCodec(codec model.Format) bool {
	return h.StaticStreamHandler.CanHandle(codec)
}

func (h *ConditionalStaticStreamHandler) UnavailabilityReason(_ model.Format) string {
	return h.Reason
}
