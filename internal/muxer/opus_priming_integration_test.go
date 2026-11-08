package muxer

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This opt-in test generates and decodes files only; it never opens an audio device.
func TestOpusPrimingPreservesSourceStart(t *testing.T) {
	if os.Getenv("SFU_DECODE_RECORDING_TEST") != "1" {
		t.Skip("set SFU_DECODE_RECORDING_TEST=1 to use FFmpeg and FFprobe")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "source.opus")
	filter := `aevalsrc=if(between(t\,0.02\,0.04)+between(t\,0.5\,0.52)\,0.5*sin(2*PI*1000*t)\,0):s=48000:d=1`
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", filter, "-ac", "2", "-c:a", "libopus", "-frame_duration", "20", "-b:a", "64k", "-y", input)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	cmd = exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_packets", "-show_data", "-show_entries", "packet=data", "-of", "json", input)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	var source struct {
		Packets []struct {
			Data string `json:"data"`
		} `json:"packets"`
	}
	require.NoError(t, json.Unmarshal(out, &source))
	require.Greater(t, len(source.Packets), 25)
	var packets [][]byte
	for _, packet := range source.Packets {
		var data []byte
		for _, line := range strings.Split(strings.TrimSpace(packet.Data), "\n") {
			parts := strings.SplitN(line, ":", 2)
			require.Len(t, parts, 2)
			hexPart := strings.SplitN(parts[1], "  ", 2)[0]
			chunk, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(hexPart), " ", ""))
			require.NoError(t, err)
			data = append(data, chunk...)
		}
		packets = append(packets, data)
	}
	for _, tc := range []struct {
		name string
		gap  time.Duration
	}{{"continuous", 0}, {"long_gap", 2400 * time.Millisecond}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "recorded.mp4")
			m := NewMP4Muxer(path)
			audio, err := m.AddAudioTrack("opus", nil)
			require.NoError(t, err)
			for i, data := range packets {
				pts := time.Duration(i) * 20 * time.Millisecond
				if i >= 15 {
					pts += tc.gap
				}
				unfilled, err := m.WriteOpusSampleWithGapFill(audio, data, pts, 20*time.Millisecond)
				require.NoError(t, err)
				require.Zero(t, unfilled)
			}
			require.NoError(t, m.Close())
			cmd := exec.Command("ffmpeg", "-v", "error", "-i", path, "-map", "0:a:0", "-f", "f32le", "-acodec", "pcm_f32le", "-")
			out, err := cmd.Output()
			require.NoError(t, err)
			const rate, channels = 48000, 2
			windowPeak := func(start, end time.Duration) float64 {
				from := int(start.Seconds()*rate) * channels * 4
				to := int(end.Seconds()*rate) * channels * 4
				require.GreaterOrEqual(t, len(out), to)
				var peak float64
				for i := from; i < to; i += 4 {
					value := math.Abs(float64(math.Float32frombits(binary.LittleEndian.Uint32(out[i:]))))
					if value > peak {
						peak = value
					}
				}
				return peak
			}
			require.Less(t, windowPeak(0, 10*time.Millisecond), 0.05, "source silence before the first signal must remain")
			require.Greater(t, windowPeak(20*time.Millisecond, 60*time.Millisecond), 0.05, "first signal must survive Opus priming")
			if tc.gap > 0 {
				require.Less(t, windowPeak(time.Second, 1100*time.Millisecond), 0.05, "gap must decode as silence")
			}
			require.Greater(t, windowPeak(500*time.Millisecond+tc.gap, 540*time.Millisecond+tc.gap), 0.05, "later signal must stay on the source timeline")
		})
	}
}
