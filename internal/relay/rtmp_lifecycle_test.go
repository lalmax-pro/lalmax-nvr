package relay

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/rtmp"
	"github.com/stretchr/testify/require"
)

func TestRTMPClientDisposedBeforeConnect(t *testing.T) {
	for _, sessionType := range []base.SessionType{base.SessionTypeRtmpPush, base.SessionTypeRtmpPull} {
		session := rtmp.NewClientSession(sessionType)
		require.ErrorIs(t, session.Dispose(), base.ErrSessionNotStarted)
		require.ErrorIs(t, session.Do("rtmp://127.0.0.1:1/live/test"), context.Canceled)
		require.NoError(t, session.Dispose())
	}
}

func TestRTMPClientDisposeDuringHandshake(t *testing.T) {
	for _, scheme := range []string{"rtmp", "rtmps"} {
		t.Run(scheme, func(t *testing.T) {
			listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			require.NoError(t, err)
			t.Cleanup(func() { _ = listener.Close() })
			require.NoError(t, listener.SetDeadline(time.Now().Add(3*time.Second)))

			session := rtmp.NewClientSession(base.SessionTypeRtmpPush, func(option *rtmp.ClientSessionOption) {
				option.DoTimeoutMs = 5000
			})
			t.Cleanup(func() { _ = session.Dispose() })
			done := make(chan error, 1)
			go func() { done <- session.Do(scheme + "://" + listener.Addr().String() + "/live/test") }()

			conn, err := listener.AcceptTCP()
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
			// The first byte proves the TCP dial completed and the RTMP or TLS
			// handshake is waiting for the peer before disposal starts.
			_, err = io.ReadFull(conn, make([]byte, 1))
			require.NoError(t, err)
			_ = session.Dispose()

			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(time.Second):
				t.Fatal("Dispose did not interrupt the connection handshake")
			}
			_, err = io.Copy(io.Discard, conn)
			require.NoError(t, err, "the peer must observe the socket closing")
		})
	}
}
