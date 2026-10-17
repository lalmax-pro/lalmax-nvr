package gb28181

import (
	"testing"
	"time"
)

func TestHistoricalTCPRouteRecovery(t *testing.T) {
	const id = "32010000001327000002"
	store := &DeviceStore{}
	dev := &Device{DeviceID: id, Address: "49.87.193.182:8315", LastRegisterAt: time.Now().Add(-time.Hour)}
	store.Store(id, dev)
	g := &GB28181API{store: store}
	g.restoreHistoricalTCPRoute(id, "49.87.193.182:8355", "TCP")
	if dev.Transport != "TCP" || dev.Address != "49.87.193.182:8355" {
		t.Fatalf("route not restored: %q %q", dev.Transport, dev.Address)
	}
}
func TestHistoricalTCPRouteRecoveryRejectsUnmatchedIPAndUDP(t *testing.T) {
	const id = "32010000001327000001"
	store := &DeviceStore{}
	dev := &Device{DeviceID: id, Address: "49.87.193.182:8314", LastRegisterAt: time.Now()}
	store.Store(id, dev)
	g := &GB28181API{store: store}
	g.restoreHistoricalTCPRoute(id, "198.51.100.99:9000", "TCP")
	g.restoreHistoricalTCPRoute(id, "49.87.193.182:9000", "UDP")
	if dev.Transport != "" || dev.Address != "49.87.193.182:8314" {
		t.Fatalf("unexpected route change: %q %q", dev.Transport, dev.Address)
	}
}
func TestHistoricalTCPRouteRequiresRegistrationHistory(t *testing.T) {
	const id = "32010000001327000001"
	store := &DeviceStore{}
	dev := &Device{DeviceID: id, Address: "49.87.193.182:8314"}
	store.Store(id, dev)
	g := &GB28181API{store: store}
	g.restoreHistoricalTCPRoute(id, "49.87.193.182:8356", "TCP")
	if dev.Transport != "" {
		t.Fatalf("unexpected transport: %q", dev.Transport)
	}
}
