package recorder

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVoIPAudioRecordingAndCleanup(t *testing.T) {
	for _, codec := range []string{"opus", "g711"} {
		t.Run(codec, func(t *testing.T) {
			store, err := storage.NewManager(t.TempDir())
			require.NoError(t, err)
			w := NewGroupWriter(store, nil, nil, nil, time.Hour)
			g := &fakeGroup{}
			id := "voip_alice_audio"
			w.groups = singleGroup{name: id, group: g}
			w.AttachVoIP(id, true)
			for i := 0; i < 150; i++ {
				payload := append([]byte{0xdf}, 0xf8, 0xff, 0xfe)
				if codec == "g711" {
					payload = append([]byte{0x72}, bytes.Repeat([]byte{0xd5}, 160)...)
				}
				g.push(base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio, TimestampAbs: uint32(i * 20), MsgLen: uint32(len(payload))}, Payload: payload})
			}
			w.DetachVoIP(id)
			w.Close()
			files, err := filepath.Glob(filepath.Join(store.RootDir(), id, "*.mp4"))
			require.NoError(t, err)
			require.Len(t, files, 1)
			if codec == "opus" {
				data, err := os.ReadFile(files[0])
				require.NoError(t, err)
				require.Contains(t, string(data), "sgpd")
				require.Contains(t, string(data), "sbgp")
				require.Contains(t, string(data), "roll")
				require.Contains(t, string(data), "elst")
				i := bytes.Index(data, []byte("dOps"))
				require.GreaterOrEqual(t, i, 0)
				require.Equal(t, uint16(3840), binary.BigEndian.Uint16(data[i+6:i+8]))
			}
			if probe, err := exec.LookPath("ffprobe"); err == nil {
				output, err := exec.Command(probe, "-v", "error", "-show_entries", "stream=codec_name,sample_rate,channels,duration", "-of", "json", files[0]).CombinedOutput()
				require.NoError(t, err, string(output))
				var report struct {
					Streams []struct {
						Codec    string `json:"codec_name"`
						Duration string `json:"duration"`
					}
				}
				require.NoError(t, json.Unmarshal(output, &report))
				require.Len(t, report.Streams, 1)
				if codec == "opus" {
					require.Equal(t, "opus", report.Streams[0].Codec)
				} else {
					require.Equal(t, "pcm_alaw", report.Streams[0].Codec)
				}
				require.True(t, strings.HasPrefix(report.Streams[0].Duration, "3."), string(output))
			}
			if ffmpeg, err := exec.LookPath("ffmpeg"); err == nil {
				output, err := exec.Command(ffmpeg, "-v", "error", "-i", files[0], "-f", "null", "-").CombinedOutput()
				require.NoError(t, err, string(output))
				output, err = exec.Command(ffmpeg, "-v", "error", "-ss", "1.5", "-i", files[0], "-t", "1", "-f", "null", "-").CombinedOutput()
				require.NoError(t, err, string(output))
			}
		})
	}
}
