package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
)

var probeLogger = slog.Default().With("component", "rtsp-probe")

// RTSPProbeConfig holds configuration for RTSP encoding probe.
type RTSPProbeConfig struct {
	RTSPURL       string
	RTSPTransport string
	Username      string
	Password      string
	Timeout       time.Duration // default 10s
}

// ProbeResult holds the result of RTSP encoding probe.
type ProbeResult struct {
	HasH264  bool
	HasH265  bool
	HasMJPEG bool
	HasAudio bool
	// Detected encoding priority: H265 > H264 > MJPEG
	DetectedEncoding string
}

// ProbeRTSPEncoding probes an RTSP stream to detect available video/audio encodings.
// Returns ProbeResult with detected encoding information.
func ProbeRTSPEncoding(ctx context.Context, cfg RTSPProbeConfig) (*ProbeResult, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}

	u, err := base.ParseURL(cfg.RTSPURL)
	if err != nil {
		return nil, fmt.Errorf("invalid RTSP URL: %w", err)
	}

	// Inject credentials from config if URL doesn't have them.
	if u.User == nil && cfg.Username != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}

	client := &gortsplib.Client{
		Scheme:       u.Scheme,
		Host:         u.Host,
		Protocol:     rtspTransportProtocol(cfg.RTSPTransport),
		ReadTimeout:  cfg.Timeout,
		WriteTimeout: cfg.Timeout,
	}

	if err := client.Start(); err != nil {
		return nil, fmt.Errorf("client start: %w", err)
	}
	defer client.Close()

	desc, _, err := client.Describe(u)
	if err != nil {
		return nil, fmt.Errorf("DESCRIBE: %w", err)
	}

	result := &ProbeResult{}

	// Check for H264
	var h264Forma *format.H264
	if medi := desc.FindFormat(&h264Forma); medi != nil {
		result.HasH264 = true
		probeLogger.Debug("found H264 format", "url", cfg.RTSPURL)
	}

	// Check for H265
	var h265Forma *format.H265
	if medi := desc.FindFormat(&h265Forma); medi != nil {
		result.HasH265 = true
		probeLogger.Debug("found H265 format", "url", cfg.RTSPURL)
	}

	// Check for MJPEG
	var mjpegForma *format.MJPEG
	if medi := desc.FindFormat(&mjpegForma); medi != nil {
		result.HasMJPEG = true
		probeLogger.Debug("found MJPEG format", "url", cfg.RTSPURL)
	}

	// Check for audio (AAC or G.711)
	var aacForma *format.MPEG4Audio
	if medi := desc.FindFormat(&aacForma); medi != nil {
		result.HasAudio = true
		probeLogger.Debug("found AAC audio format", "url", cfg.RTSPURL)
	}
	var g711Forma *format.G711
	if medi := desc.FindFormat(&g711Forma); medi != nil {
		result.HasAudio = true
		probeLogger.Debug("found G.711 audio format", "url", cfg.RTSPURL)
	}

	// Determine detected encoding with priority: H265 > H264 > MJPEG
	if result.HasH265 {
		result.DetectedEncoding = "h265"
	} else if result.HasH264 {
		result.DetectedEncoding = "h264"
	} else if result.HasMJPEG {
		result.DetectedEncoding = "mjpeg"
	}

	probeLogger.Info("RTSP encoding probe completed",
		"url", cfg.RTSPURL,
		"h264", result.HasH264,
		"h265", result.HasH265,
		"mjpeg", result.HasMJPEG,
		"audio", result.HasAudio,
		"detected", result.DetectedEncoding)

	return result, nil
}
