package xiaomi

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecorderCloudCacheRefreshAndWakeup(t *testing.T) {
	var wakeups, vendors atomic.Int32
	_, client := covNewCloudServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/home/rpc/1":
			wakeups.Add(1)
			covCloudEncrypted(w, r, covCloudSsecurity, `{"code":0,"result":{}}`)
		case "/app/v2/device/miss_get_vendor":
			vendors.Add(1)
			covCloudEncrypted(w, r, covCloudSsecurity, `{"code":0,"result":{"vendor":{"vendor":4},"public_key":"key","sign":"sign"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	cache := newRecorderCloudCache()
	var logins, lists int
	cache.login = func(context.Context, string, string, string) (*CloudSession, error) {
		logins++
		return covCloudSession(t, client), nil
	}
	cache.list = func(context.Context, *CloudSession) ([]CloudDevice, error) {
		lists++
		return []CloudDevice{{DID: "1", Model: "loock.cateye.v02", IP: "192.0.2.1"}}, nil
	}
	cfg := XiaomiCloudConfig{UserID: "user", Token: "token", Region: "cn"}
	for i := 0; i < 2; i++ {
		_, err := cache.resolve(context.Background(), cfg, "1", "")
		require.NoError(t, err)
	}
	require.Equal(t, 1, logins)
	require.Equal(t, 1, lists)
	require.EqualValues(t, 2, wakeups.Load())
	require.EqualValues(t, 2, vendors.Load())
	cache.devicesUntil = time.Now().Add(-time.Second)
	_, err := cache.resolve(context.Background(), cfg, "1", "")
	require.NoError(t, err)
	require.Equal(t, 1, logins)
	require.Equal(t, 2, lists)
	cfg.Token = "new-token"
	_, err = cache.resolve(context.Background(), cfg, "1", "")
	require.NoError(t, err)
	require.Equal(t, 2, logins)
	require.Equal(t, 3, lists)
}

func TestRecorderCloudCacheWaitHonorsCancellation(t *testing.T) {
	cache := newRecorderCloudCache()
	cache.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cache.resolve(ctx, XiaomiCloudConfig{}, "1", "")
	require.ErrorIs(t, err, context.Canceled)
	<-cache.gate
}
