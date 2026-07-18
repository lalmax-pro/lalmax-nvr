package gb28181

import (
	"log/slog"
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

	g.handleKeepalive(deviceID, source, status)

	tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
}

func (g *GB28181API) handleKeepalive(deviceID, source, status string) {
	g.store.LoadOrStore(deviceID, &Device{
		DeviceID:  deviceID,
		source:    source,
		GBVersion: g.configuredGBVersion(),
	})

	dev, _ := g.store.Load(deviceID)
	wasOnline := dev != nil && dev.IsOnline
	isOnline := status == "OK" || status == "ON"

	g.store.Change(deviceID, func(d *Device) {
		d.LastKeepaliveAt = time.Now()
		d.IsOnline = isOnline
		d.Address = source
	})

	if err := g.store.UpdateDeviceStatus(deviceID, isOnline, source); err != nil {
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
