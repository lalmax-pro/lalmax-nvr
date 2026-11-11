// SPDX-License-Identifier: MIT

package xiaomi

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi/tutk"
	"github.com/stretchr/testify/require"
)

func TestDafangMISSCommandsAndStop(t *testing.T) {
	client, conn := newTestMISSClient()
	client.model = ModelDafang
	for _, tc := range []struct {
		dir     string
		speed   int
		h, v, s byte
	}{
		{"left", 5, 1, 0, 5}, {"right", 6, 2, 0, 6}, {"up", 9, 0, 2, 9}, {"down", 100, 0, 1, 9}, {"stop", 0, 0, 0, 5},
	} {
		require.NoError(t, client.MotorControlContext(context.Background(), tc.dir, tc.speed))
		cmd, data, ok := conn.lastWrittenCmd()
		require.True(t, ok)
		require.Equal(t, uint32(missCmdEncoded), cmd)
		decoded, err := Decode(data, client.key)
		require.NoError(t, err)
		require.Equal(t, []byte{0x7f, 0xff, 0xff, 0xff}, decoded[:4])
		raw, err := hex.DecodeString(string(decoded[4:]))
		require.NoError(t, err)
		require.Equal(t, tutk.ICAM(0xff2404, tc.h, tc.v, tc.s), raw)
	}
	client.legacy = true
	require.ErrorContains(t, client.MotorControlContext(context.Background(), "left", 5), "legacy PTZ")
}

func TestDafangMotorLeaseSendsStop(t *testing.T) {
	client, conn := newTestMISSClient()
	client.model = ModelDafang
	r := NewXiaomiRecorder(XiaomiRecorderConfig{}, nil)
	r.motorLease = 20 * time.Millisecond
	r.attachMotorClient(client)
	defer r.detachMotorClient(client)
	require.NoError(t, r.MotorControl(context.Background(), "left", 5))
	require.Eventually(t, func() bool { conn.mu.Lock(); defer conn.mu.Unlock(); return len(conn.writtenCmds) >= 2 }, time.Second, 5*time.Millisecond)
	_, data, ok := conn.lastWrittenCmd()
	require.True(t, ok)
	decoded, err := Decode(data, client.key)
	require.NoError(t, err)
	raw, err := hex.DecodeString(string(decoded[4:]))
	require.NoError(t, err)
	require.Equal(t, tutk.ICAM(0xff2404, 0, 0, 5), raw)
}

func TestDafangCapabilityDistinguishesLegacyFirmware(t *testing.T) {
	client, _ := newTestMISSClient()
	client.model = ModelDafang
	r := NewXiaomiRecorder(XiaomiRecorderConfig{}, nil)
	r.attachMotorClient(client)
	require.Equal(t, "continuous", r.MotorProfile().Control)
	r.detachMotorClient(client)
	client.legacy = true
	r.attachMotorClient(client)
	require.Equal(t, "unknown", r.MotorProfile().Control)
	r.detachMotorClient(client)
}
