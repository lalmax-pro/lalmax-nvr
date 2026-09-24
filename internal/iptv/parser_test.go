package iptv

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseM3U_Attributes(t *testing.T) {
	raw := `#EXTM3U
#EXTINF:-1 tvg-id="cctv1" tvg-name="CCTV-1" tvg-logo="http://logo/1.png" group-title="央视",CCTV-1 综合
https://example.com/live/cctv1/index.m3u8
#EXTINF:-1,News
https://example.com/news.m3u8
`
	got, err := ParseM3U(strings.NewReader(raw))
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "cctv1", got[0].ExternalID)
	require.Equal(t, "CCTV-1 综合", got[0].Name)
	require.Equal(t, "央视", got[0].GroupName)
	require.Equal(t, "https://example.com/live/cctv1/index.m3u8", got[0].SourceURL)
	require.Equal(t, "News", got[1].Name)
}

func TestParseM3U_EXTVLCUserAgent(t *testing.T) {
	raw := `#EXTM3U
#EXTINF:-1 tvg-id="OzoneTV.hu@SD" http-user-agent="Mozilla/5.0" group-title="Science",Ozone TV
#EXTVLCOPT:http-user-agent=Mozilla/5.0 (Windows NT 10.0; Win64; x64)
http://88.212.15.19/live/ozone/index.m3u8
`
	got, err := ParseM3U(strings.NewReader(raw))
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "OzoneTV.hu@SD", got[0].ExternalID)
	require.Equal(t, "Mozilla/5.0 (Windows NT 10.0; Win64; x64)", got[0].Headers["User-Agent"])
}

func TestValidatePublicURL_BlocksLoopback(t *testing.T) {
	require.Error(t, validatePublicURL("http://127.0.0.1/live.m3u8"))
	require.Error(t, validatePublicURL("rtsp://example.com/a"))
	require.NoError(t, validatePublicURL("https://example.com/live/index.m3u8"))
}
