package transcode

import (
	"context"
	"encoding/json"
	"net/url"
	"time"
)

// ProbeInputDimensions probes only a trusted, resolved RTSP endpoint. Raw tool
// errors/output are never propagated: they may contain source credentials.
func ProbeInputDimensions(ctx context.Context, tool, input string, runner ProbeRunner) (int, int, error) {
	u, err := url.Parse(input)
	if err != nil || u.Scheme != "rtsp" || u.Hostname() == "" {
		return 0, 0, invalid("RTSP input required for dimension probe")
	}
	if runner == nil {
		return 0, 0, invalid("probe runner is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	data, err := runner.Run(ctx, tool, "-v", "error", "-rtsp_transport", "tcp", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "json", input)
	if err != nil {
		return 0, 0, &Error{Code: "source_probe_failed", Message: "source dimensions unavailable; check RTSP access and decoder metadata"}
	}
	return parseInputDimensions(data)
}
func parseInputDimensions(data []byte) (int, int, error) {
	var result struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	if json.Unmarshal(data, &result) != nil || len(result.Streams) != 1 {
		return 0, 0, invalid("source must provide one selected video stream")
	}
	v := result.Streams[0]
	if v.Width < 2 || v.Height < 2 || v.Width > 16384 || v.Height > 16384 {
		return 0, 0, invalid("source dimensions are outside supported limits")
	}
	return v.Width, v.Height, nil
}
