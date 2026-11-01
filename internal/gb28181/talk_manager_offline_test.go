package gb28181

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStartTalkRejectsOfflineMissingChannelAndCleansFailedStart(t *testing.T) {
	store := NewDeviceStore(nil, NewWSHub())
	tm := NewTalkManager(nil, &Config{ID: "34020000002000000001", MediaIP: "127.0.0.1"}, store)

	_, err := tm.StartTalk("missing", "ch", TransportUDP, store)
	require.ErrorIs(t, err, ErrDeviceOffline)

	dev := &Device{DeviceID: "34020000001320000001", IsOnline: true, Address: "192.0.2.20:5060"}
	store.Store(dev.DeviceID, dev)
	_, err = tm.StartTalk(dev.DeviceID, "ch", TransportUDP, store)
	require.ErrorIs(t, err, ErrChannelNotExist)

	require.NoError(t, tm.StopTalk("missing", "ch"))
	_, ok := tm.GetSession(dev.DeviceID, "ch")
	require.False(t, ok)
	tm.RemoveSession("unused")
}

func TestWSHubBroadcastDropsWhenQueueFull(t *testing.T) {
	h := NewWSHub()
	for i := 0; i < 256; i++ {
		h.Broadcast(Event{Type: EventDeviceOnline})
	}
	done := make(chan struct{})
	go func() {
		h.Broadcast(Event{Type: EventChannelUpdate})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Broadcast blocked when the event queue was full")
	}
}
