package gb28181

import (
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
)

func (g *GB28181API) handlerNotify(req *sip.Request, tx sip.ServerTransaction) {
	deviceID := req.From().Address.User
	source := req.Source()

	slog.Debug("[SIP] NOTIFY received",
		"device_id", deviceID,
		"source", source,
		"call_id", req.CallID().Value(),
	)

	var msg MessageNotify
	body := req.Body()
	status := "OK"
	if len(body) > 0 {
		slog.Debug("[SIP] NOTIFY body", "device_id", deviceID, "body", string(body))
		if err := xmlUnmarshal(body, &msg); err != nil {
			slog.Error("[SIP] NOTIFY xml decode error", "device_id", deviceID, "error", err, "body", string(body))
			tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
			return
		}
		if msg.Status != "" {
			status = msg.Status
		}
	}

	g.handleKeepaliveFromRequest(deviceID, source, status, req.Transport())

	tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
}

// handleKeepaliveFromRequest restores a TCP signaling route for a previously
// registered device after restart, using a restricted source-IP compatibility
// policy. A SIP MESSAGE itself is NOT authentication; this must only be enabled
// for deployments that trust their device network/source IP.
func (g *GB28181API) handleKeepaliveFromRequest(deviceID, source, status, transport string) {
	g.restoreHistoricalTCPRoute(deviceID, source, transport)
	g.handleKeepalive(deviceID, source, status)
}

func (g *GB28181API) restoreHistoricalTCPRoute(deviceID, source, transport string) {
	if !strings.EqualFold(transport, "TCP") || filterUnknowDevices(deviceID) != nil {
		return
	}
	dev, ok := g.store.Load(deviceID)
	if !ok || dev == nil || !dev.LastRegisterAt.After(time.Time{}) || dev.Transport != "" {
		return
	}
	oldHost, _, oldErr := net.SplitHostPort(dev.Address)
	newHost, _, newErr := net.SplitHostPort(source)
	if oldErr != nil || newErr != nil || net.ParseIP(oldHost) == nil || oldHost != newHost {
		return
	}
	// Rebind only a known, previously registered device. The 20-digit ID and
	// source IP are compatibility constraints, not cryptographic proof.
	if err := g.store.Change(deviceID, func(d *Device) {
		if d.Transport == "" && d.LastRegisterAt.After(time.Time{}) {
			d.Transport = "TCP"
			d.Address = source
		}
	}); err != nil {
		return
	}
	slog.Warn("[SIP] historical TCP route restored from matching source IP (compatibility trust)",
		"device_id", deviceID, "source", source)
}

func (g *GB28181API) handleKeepalive(deviceID, source, status string) {
	// Never allow an unauthenticated keepalive to enroll a new device.
	dev, known := g.store.Load(deviceID)
	if !known || dev == nil {
		return
	}
	// Limit MESSAGE/NOTIFY heartbeats to the previously registered source IP.
	// NAT source port changes are tolerated, not arbitrary source IP changes.
	if !dev.LastRegisterAt.IsZero() && dev.Address != "" {
		oldHost, _, oldErr := net.SplitHostPort(dev.Address)
		newHost, _, newErr := net.SplitHostPort(source)
		if oldErr != nil || newErr != nil || oldHost != newHost {
			return
		}
	}
	wasOnline := dev.IsOnline
	isOnline := status == "OK" || status == "ON"

	g.store.Change(deviceID, func(d *Device) {
		d.LastKeepaliveAt = time.Now()
		d.IsOnline = isOnline
		// The historical route recovery above may update Address only when
		// source IP matched the stored authenticated-registration address.
		// Otherwise keep existing routed address if transport is already known.
		if d.Address == "" {
			d.Address = source
		}
	})

	if err := g.store.UpdateDeviceStatus(deviceID, isOnline, dev.Address); err != nil {
		slog.Error("failed to update device status in DB", "device_id", deviceID, "error", err)
	}
	if wasOnline != isOnline {
		g.store.recordOperation(deviceID, isOnline, source)
	}

	slog.Debug("[SIP] Keepalive processed", "device_id", deviceID, "status", status, "source", source)

	if dev == nil {
		return
	}
	hasChannels := false
	dev.Channels.Range(func(key, value any) bool {
		hasChannels = true
		return false
	})
	if !hasChannels {
		slog.Info("[SIP] Device has no channels, querying catalog", "device_id", deviceID)
		go g.sendDeviceInfoQuery(deviceID)
		go g.QueryCatalog(deviceID)
	}
}
