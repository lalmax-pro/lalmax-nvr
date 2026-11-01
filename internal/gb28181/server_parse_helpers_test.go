package gb28181

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveHost(t *testing.T) {
	require.Empty(t, resolveHost(""))
	require.Equal(t, "192.0.2.10", resolveHost("192.0.2.10"))
	require.Equal(t, "127.0.0.1", resolveHost("localhost"))
	require.Equal(t, "not-a-real-host.invalid", resolveHost("not-a-real-host.invalid"))
}

func TestParseChannelIDFromSDP(t *testing.T) {
	sdp := "v=0\r\no=34020000002000000001 0 0 IN IP4 192.0.2.10\r\ns=Play\r\n"
	require.Equal(t, "34020000002000000001", parseChannelIDFromSDP(sdp))
	require.Equal(t, "34020000001320000001", parseChannelIDFromSDP("o=34020000001320000001 0 0 IN IP4 10.0.0.1\n"))
	require.Empty(t, parseChannelIDFromSDP("o=camera 0 0 IN IP4 10.0.0.1\r\n"))
	require.Empty(t, parseChannelIDFromSDP("v=0\r\ns=Play\r\n"))
	require.Empty(t, parseChannelIDFromSDP("o=123 0 0 IN IP4 10.0.0.1\r\n"))
}
