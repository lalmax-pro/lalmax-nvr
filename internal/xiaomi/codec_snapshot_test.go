package xiaomi

import (
	"sync"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCodecParamsReturnsIndependentSnapshot(t *testing.T) {
	r := NewXiaomiRecorder(XiaomiRecorderConfig{}, nil)
	r.codec = model.FormatH265
	var ts uint64
	for _, nalu := range [][]byte{{32 << 1, 1}, {33 << 1, 2}, {34 << 1, 3}} {
		r.processNALU(nalu, 0, &ts)
	}
	codec, sps, pps, vps := r.CodecParams()
	require.Equal(t, model.FormatH265, codec)
	sps[1], pps[1], vps[1] = 9, 9, 9
	_, nextSPS, nextPPS, nextVPS := r.CodecParams()
	require.Equal(t, []byte{33 << 1, 2}, nextSPS)
	require.Equal(t, []byte{34 << 1, 3}, nextPPS)
	require.Equal(t, []byte{32 << 1, 1}, nextVPS)
	r.resetCodec()
	codec, nextSPS, nextPPS, nextVPS = r.CodecParams()
	require.Empty(t, codec)
	require.Nil(t, nextSPS)
	require.Nil(t, nextPPS)
	require.Nil(t, nextVPS)
	require.Equal(t, []byte{33 << 1, 9}, sps, "reconnect must not mutate a returned snapshot")
}

func TestCodecParamsConcurrentStreamUpdates(t *testing.T) {
	for _, codec := range []model.Format{model.FormatH264, model.FormatH265} {
		t.Run(string(codec), func(t *testing.T) {
			r := NewXiaomiRecorder(XiaomiRecorderConfig{}, nil)
			r.codec = codec
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				var ts uint64
				for i := 0; i < 1000; i++ {
					if codec == model.FormatH264 {
						r.processNALU([]byte{7, byte(i)}, 0, &ts)
						r.processNALU([]byte{8, byte(i)}, 0, &ts)
					} else {
						for _, typ := range []byte{32, 33, 34} {
							r.processNALU([]byte{typ << 1, byte(i)}, 0, &ts)
						}
					}
				}
			}()
			go func() {
				defer wg.Done()
				for i := 0; i < 1000; i++ {
					_, sps, pps, vps := r.CodecParams()
					for _, data := range [][]byte{sps, pps, vps} {
						if len(data) > 1 {
							data[1] = 0
						}
					}
				}
			}()
			wg.Wait()
		})
	}
}

func TestClearPendingLoginsRejectsOldChallenges(t *testing.T) {
	id := storePendingCloud(&Cloud{})
	ClearPendingLogins()
	require.Nil(t, loadPendingCloud(id))
	_, err := LoginWithCaptcha(id, "1234")
	require.ErrorContains(t, err, "invalid or expired")
	_, err = LoginWithVerify(id, "1234")
	require.ErrorContains(t, err, "invalid or expired")
}
