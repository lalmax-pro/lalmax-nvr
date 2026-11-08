//go:build livekit_browser

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

// This uses a headless browser with synthetic media and muted audio. It checks
// the actual NVR authorization, embedded SFU, Group, recording plan and file.
func TestNVRLiveKitRecordingBrowser(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg, cfgPath := testConfig(t)
	isolateEmbeddedMedia(t, cfg)
	cfg.Server.Listen = freeTCPAddr(t)
	cfg.Auth.Username, cfg.Auth.Password = "admin", "test-password"
	cfg.JT808.Enabled, cfg.MQTT.Enabled = false, false
	disabled := false
	cfg.FTP.Enabled, cfg.DLNA.Enabled, cfg.RTMP.Enabled = &disabled, &disabled, &disabled
	cfg.SRT.Enabled, cfg.WHIP.Enabled = &disabled, &disabled
	port := freeTCPPort(t)
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	require.NoError(t, udp.Close())
	const key, secret = "testkey", "testsecret-testsecret-testsecret-123456"
	livekitConfig := fmt.Sprintf("development: false\nport: %d\nbind_addresses: [127.0.0.1]\nkeys:\n  %s: %s\nrtc:\n  node_ip: 127.0.0.1\n  enable_loopback_candidate: true\n  ips:\n    includes: [127.0.0.1/32]\n  tcp_port: 0\n  port_range_start: 0\n  port_range_end: 0\n  udp_port: %d\n", port, key, secret, udpPort)
	path := filepath.Join(t.TempDir(), "livekit.yaml")
	require.NoError(t, os.WriteFile(path, []byte(livekitConfig), 0o600))
	cfg.SFU = config.SFUConfig{Enabled: true, Backend: "livekit", LiveKit: config.SFULiveKitConfig{ConfigFile: path, APIKey: key, PublicURL: fmt.Sprintf("http://127.0.0.1:%d", port)}}
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Start() }()
	waitForTCP(t, cfg.Server.Listen)
	t.Cleanup(func() {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(35 * time.Second):
			t.Error("app did not stop")
		}
		signal.Reset(syscall.SIGTERM)
	})
	require.Eventually(t, func() bool { return app.liveKitRuntime.server.IsRunning() }, 10*time.Second, 20*time.Millisecond)
	_, source, _, _ := goruntime.Caller(0)
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(filepath.Dir(source), "../../tools/sfu-meet/browser-e2e.cjs"), "http://"+cfg.Server.Listen)
	command.Env = append(os.Environ(), "LK_BRIDGE_TEST=1", "LK_RECORD_TEST=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	match := regexp.MustCompile(`RECORDED_STREAM=(sfu_[0-9a-f]+)`).FindStringSubmatch(string(output))
	require.Len(t, match, 2, "%s", output)
	var files []string
	require.Eventually(t, func() bool {
		files, err = app.store.ListFiles(match[1])
		return err == nil && len(files) > 0
	}, 10*time.Second, 100*time.Millisecond, "recording did not appear")
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", files[0]).CombinedOutput()
	require.NoError(t, err, "%s", probe)
	require.Contains(t, string(probe), "h264")
	require.Contains(t, string(probe), "opus")
	decoded, err := exec.Command("ffmpeg", "-v", "error", "-xerror", "-i", files[0], "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-").CombinedOutput()
	require.NoError(t, err, "%s", decoded)
	t.Log(strings.TrimSpace(string(output)))
}
