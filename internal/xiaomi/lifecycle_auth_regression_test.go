// SPDX-License-Identifier: MIT

package xiaomi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestRecorderFailedStartRollsBackAndAllowsRestart(t *testing.T) {
	t.Helper()
	m := metrics.NewMetrics()
	engine := &covMediaEngine{addErr: errors.New("lal unavailable")}
	r := NewXiaomiRecorder(XiaomiRecorderConfig{MediaEngine: engine}, nil, m)
	require.Error(t, r.Start(context.Background()))
	require.Equal(t, model.StatusStopped, r.Status())
	require.Nil(t, r.cancel)
	require.Nil(t, r.pubSession)
	require.Zero(t, testutil.ToFloat64(m.ActiveRecordings))
	stopped := make(chan struct{})
	go func() { _ = r.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked after failed Start")
	}
	engine.addErr = nil
	engine.pub = &covPubSession{}
	require.NoError(t, r.Start(context.Background()))
	require.NoError(t, r.Stop())
	require.Zero(t, testutil.ToFloat64(m.ActiveRecordings))
	require.Equal(t, 1, engine.delCalls)
	// Another complete start/stop cycle cannot reuse the old done channel.
	require.NoError(t, r.Start(context.Background()))
	require.NoError(t, r.Stop())
	require.Equal(t, 2, engine.delCalls)
	require.Zero(t, testutil.ToFloat64(m.ActiveRecordings))
}

func TestRecorderConcurrentStopsCleanUpExactlyOnce(t *testing.T) {
	t.Helper()
	m := metrics.NewMetrics()
	engine := &covMediaEngine{pub: &covPubSession{}}
	r := NewXiaomiRecorder(XiaomiRecorderConfig{MediaEngine: engine}, nil, m)
	require.NoError(t, r.Start(context.Background()))
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- r.Stop() }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Equal(t, 1, engine.delCalls)
	require.Zero(t, testutil.ToFloat64(m.ActiveRecordings))
	require.Equal(t, model.StatusStopped, r.Status())
}

func TestWrongOTPAllowsRetryWithSameSession(t *testing.T) {
	t.Helper()
	state := &covCloudServer{ssecurity: covCloudSsecurity}
	_, client := covNewCloudServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity/auth/verifyPhone" && r.URL.Query().Get("ticket") != "correct" {
			fmt.Fprint(w, `&&&START&&&{"code":7001,"message":"invalid verification code"}`)
			return
		}
		state.ServeHTTP(w, r)
	}))
	c := &Cloud{client: client, region: "cn", auth: map[string]string{"flag": "4", "identity_session": "test", "password": "secret"}}
	id := storePendingCloud(c)
	t.Cleanup(func() { deletePendingCloud(id) })
	_, err := LoginWithVerifyContext(context.Background(), id, "wrong")
	require.ErrorContains(t, err, "verification failed")
	require.Same(t, c, loadPendingCloud(id))
	session, err := LoginWithVerifyContext(context.Background(), id, "correct")
	require.NoError(t, err)
	require.Equal(t, "user-1", session.UserID)
	require.Nil(t, loadPendingCloud(id))
	require.Nil(t, c.auth, "password must be released after successful login")
}

func TestCaptchaContinuationUsesFreshRequestContext(t *testing.T) {
	t.Helper()
	state := &covCloudServer{ssecurity: covCloudSsecurity, auth2Mode: "captcha"}
	covInstallDefaultTransport(t, state)
	ctx, cancel := context.WithCancel(context.Background())
	_, id, err := SignInWithCaptchaContext(ctx, "user@example.com", "secret", "cn")
	require.Error(t, err)
	require.NotEmpty(t, id)
	t.Cleanup(func() { deletePendingCloud(id) })
	cancel() // The initial HTTP request has ended before the user solves captcha.
	state.setAuth2Mode("ok")
	session, err := LoginWithCaptchaContext(context.Background(), id, "1234")
	require.NoError(t, err)
	require.Equal(t, "user-1", session.UserID)
}

func TestPendingLoginExpires(t *testing.T) {
	t.Helper()
	id := storePendingCloud(&Cloud{})
	t.Cleanup(func() { deletePendingCloud(id) })
	pendingCloudsMu.Lock()
	pendingClouds[id].expiresAt = time.Now().Add(-time.Second)
	pendingCloudsMu.Unlock()
	require.Nil(t, loadPendingCloud(id))
	_, err := LoginWithVerify(id, "1234")
	require.ErrorContains(t, err, "expired")
	pendingCloudsMu.Lock()
	_, exists := pendingClouds[id]
	pendingCloudsMu.Unlock()
	require.False(t, exists)
}

func TestCloudResolutionCancellationAtEveryHTTPStage(t *testing.T) {
	t.Helper()
	for _, path := range []string{"/pass/serviceLogin", "/pass/callback/first", "/app/v2/home/device_list_page", "/app/v2/device/miss_get_vendor"} {
		t.Run(path, func(t *testing.T) {
			t.Helper()
			started := make(chan struct{}, 1)
			state := &covCloudServer{ssecurity: covCloudSsecurity}
			state.apiHandler = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/app/v2/home/device_list_page" {
					covCloudEncrypted(w, r, state.ssecurity, `{"code":0,"result":{"list":[{"did":"1","model":"chuangmi.camera.046c04","localip":"192.168.1.10"}]}}`)
					return true
				}
				return false
			}
			covInstallDefaultTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == path {
					_, _ = io.Copy(io.Discard, r.Body)
					started <- struct{}{}
					<-r.Context().Done()
					return
				}
				state.ServeHTTP(w, r)
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := ResolveMISSURLContext(ctx, XiaomiCloudConfig{UserID: "user", Token: "token", Region: "cn"}, "1", "")
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("cloud stage not reached")
			}
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("cloud request ignored cancellation")
			}
		})
	}
}

func TestRecorderStopCancelsCloudLookup(t *testing.T) {
	t.Helper()
	started := make(chan struct{}, 1)
	covInstallDefaultTransport(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; <-r.Context().Done() }))
	engine := &covMediaEngine{pub: &covPubSession{}}
	r := NewXiaomiRecorder(XiaomiRecorderConfig{MediaEngine: engine, CloudCfg: XiaomiCloudConfig{UserID: "user", Token: "token"}}, nil)
	require.NoError(t, r.Start(context.Background()))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cloud lookup not started")
	}
	done := make(chan error, 1)
	go func() { done <- r.Stop() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Stop waited for the cloud HTTP timeout")
	}
	require.Equal(t, 1, engine.delCalls)
}

func TestCS2HandshakeHonorsCancellation(t *testing.T) {
	t.Helper()
	// A silent local UDP endpoint avoids ICMP failures and forces handshake wait.
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = CS2DialContext(ctx, server.LocalAddr().String(), "udp", time.Second)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
