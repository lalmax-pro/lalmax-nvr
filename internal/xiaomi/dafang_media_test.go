// SPDX-License-Identifier: MIT

package xiaomi

import (
	"encoding/hex"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi/tutk"
	"github.com/stretchr/testify/require"
)

func TestDafangAndXiaofangMISSStartMedia(t *testing.T) {
	for _, model := range []string{ModelDafang, ModelXiaofang} {
		for _, tc := range []struct {
			quality                string
			audio                  bool
			wantQuality, wantAudio byte
		}{
			{"hd", true, 0, 1}, {"sd", false, 1, 0}, {"auto", true, 0, 1},
		} {
			t.Run(model+"/"+tc.quality, func(t *testing.T) {
				client, conn := newTestMISSClient()
				client.model = model
				require.NoError(t, client.StartMedia("0", tc.quality, tc.audio))
				require.Len(t, conn.writtenCmds, 2)
				for i, want := range [][]byte{tutk.ICAM(0xff07d5, tc.wantQuality), tutk.ICAM(0xff07d8, 1, tc.wantAudio)} {
					require.Equal(t, uint32(missCmdEncoded), conn.writtenCmds[i].cmd)
					decoded, err := Decode(conn.writtenCmds[i].data, client.key)
					require.NoError(t, err)
					require.Equal(t, []byte{0x7f, 0xff, 0xff, 0xff}, decoded[:4])
					raw, err := hex.DecodeString(string(decoded[4:]))
					require.NoError(t, err)
					require.Equal(t, want, raw)
				}
				require.Error(t, client.StartMedia("1", "hd"))
				require.Error(t, client.StartMedia("0", "3"))
				require.Len(t, conn.writtenCmds, 2)
			})
		}
	}
}
