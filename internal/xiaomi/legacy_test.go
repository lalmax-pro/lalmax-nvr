// SPDX-License-Identifier: MIT

package xiaomi

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/url"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi/tutk"
	"github.com/stretchr/testify/require"
)

func TestLegacyMediaControl(t *testing.T) {
	for _, tc := range []struct {
		model, quality string
		wantCmds       []uint32
		last           []byte
	}{
		{legacyMijia, "hd", []uint32{0x300, 0x1ff, 0x320}, []byte(`{"videoquality":3}`)},
		{legacyXiaobai, "sd", []uint32{0x300, 0x320, 0x1ff}, []byte(`{}`)},
		{legacyAqara, "hd", []uint32{0x1ff, 0x605, 0x704}, []byte(`{}`)},
	} {
		t.Run(tc.model, func(t *testing.T) {
			conn := &mockMISSConn{}
			client := &MISSClient{Conn: conn, model: tc.model, legacy: true}
			require.NoError(t, client.StartMedia("", tc.quality, true))
			require.Len(t, conn.writtenCmds, len(tc.wantCmds))
			for i, cmd := range tc.wantCmds {
				require.Equal(t, cmd, conn.writtenCmds[i].cmd)
			}
			require.Equal(t, tc.last, conn.writtenCmds[len(conn.writtenCmds)-1].data)
			if tc.model == legacyXiaobai {
				require.Equal(t, []byte{0, 0, 0, 0, 4, 0, 0, 0}, conn.writtenCmds[1].data)
			}
			require.Error(t, client.StartMedia("1", "hd"))
		})
	}
}

func TestLegacyMediaValidationAndCodec(t *testing.T) {
	client := &MISSClient{legacy: true, model: legacyXiaobai}
	_, err := client.readLegacyPacket([]byte{1}, nil)
	require.Error(t, err)
	for _, tc := range []struct {
		in   byte
		want uint32
	}{{tutk.CodecH264, missCodecH264}, {tutk.CodecH265, missCodecH265}, {1, missCodecPCMA}, {tutk.CodecPCML, missCodecPCM}} {
		hdr := make([]byte, 20)
		hdr[0] = tc.in
		packet, err := client.readLegacyPacket(hdr, []byte{1, 2, 3})
		require.NoError(t, err)
		require.Equal(t, tc.want, packet.CodecID)
		require.Equal(t, []byte{1, 2, 3}, packet.Payload)
	}
	client.key = bytes.Repeat([]byte{1}, 32)
	_, err = client.readLegacyPacket(make([]byte, 20), []byte{1})
	require.Error(t, err)
}

func TestLegacyVideoEncryptionBounds(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	_, err := decodeLegacyVideo([]byte{1, 2}, key)
	require.Error(t, err)
	frame := make([]byte, 25)
	frame[8] = 1
	binary.LittleEndian.PutUint32(frame[9:], 2)
	binary.LittleEndian.PutUint32(frame[13:], 99)
	_, err = decodeLegacyVideo(frame, key)
	require.Error(t, err)
	plain := []byte{0, 0, 0, 1, 0x26}
	decoded, err := decodeLegacyVideo(plain, key)
	require.NoError(t, err)
	require.Equal(t, plain, decoded)
}

func TestLegacyChallengeValidation(t *testing.T) {
	conn := &mockMISSConn{}
	require.ErrorContains(t, legacyChallengeLogin(conn, "short"), "too short")
	require.Empty(t, conn.writtenCmds)
	require.ErrorContains(t, legacyChallengeLogin(conn, "0123456789abcdef"), "truncated")
}

func TestLegacyCloudCredentials(t *testing.T) {
	_, client := covNewCloudServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/app/device/devicepass", r.URL.Path)
		covCloudEncrypted(w, r, covCloudSsecurity, `{"code":0,"message":"ok","result":{"p2p_id":"p2p-id","password":"pass"}}`)
	}))
	cloud := &Cloud{ctx: context.Background(), client: client, region: "cn", ssecurity: covCloudSsecurity}
	raw, err := resolveLegacyWithCloud(cloud, "192.0.2.1", "123", legacyXiaobai, []byte{1}, []byte{2})
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	require.Equal(t, "legacy", u.Query().Get("vendor"))
	require.Equal(t, "p2p-id", u.Query().Get("uid"))
	require.Equal(t, "pass", u.Query().Get("password"))
}

func TestDecodeRejectsShortEncryptedPacket(t *testing.T) {
	for n := 0; n < 8; n++ {
		_, err := Decode(make([]byte, n), make([]byte, 32))
		require.Error(t, err)
	}
}

func TestLegacyResolutionSelectionAndFallback(t *testing.T) {
	for _, tc := range []struct {
		model             string
		wantVendorRequest bool
	}{{legacyXiaobai, false}, {ModelDafang, true}} {
		t.Run(tc.model, func(t *testing.T) {
			var paths []string
			_, client := covNewCloudServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case "/app/v2/device/miss_get_vendor":
					covCloudEncrypted(w, r, covCloudSsecurity, `{"code":-1,"message":"no available vendor support"}`)
				case "/app/device/devicepass":
					covCloudEncrypted(w, r, covCloudSsecurity, `{"code":0,"result":{"p2p_id":"uid","password":"0123456789abcdef"}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			session := &CloudSession{Region: "cn", client: client, ssecurity: covCloudSsecurity}
			raw, err := resolveMISSWithSession(context.Background(), session, "1", tc.model, []CloudDevice{{DID: "1", IP: "127.0.0.1", Model: tc.model}})
			require.NoError(t, err)
			parsed, err := url.Parse(raw)
			require.NoError(t, err)
			require.Equal(t, "legacy", parsed.Query().Get("vendor"))
			if tc.wantVendorRequest {
				require.Equal(t, []string{"/app/v2/device/miss_get_vendor", "/app/device/devicepass"}, paths)
			} else {
				require.Equal(t, []string{"/app/device/devicepass"}, paths)
			}
		})
	}
}
