package gb28181

import (
	"errors"
	"fmt"
	"log/slog"
)

// GBVersion GB28181标准版本
type GBVersion string

const (
	GBVersion2016    GBVersion = "2016"
	GBVersion2022    GBVersion = "2022"
	GBVersionUnknown GBVersion = "unknown"
)

type MessageDeviceInfoResponse struct {
	CmdType      string `xml:"CmdType"`
	SN           int    `xml:"SN"`
	DeviceID     string `xml:"DeviceID"`
	DeviceName   string `xml:"DeviceName"`
	Manufacturer string `xml:"Manufacturer"`
	Model        string `xml:"Model"`
	Firmware     string `xml:"Firmware"`
	Result       string `xml:"Result"`
}

func (g *GB28181API) handleDeviceInfoResponse(deviceID string, body []byte) {
	var msg MessageDeviceInfoResponse
	if err := xmlUnmarshal(body, &msg); err != nil {
		slog.Error("device info xml decode error", "device_id", deviceID, "error", err)
		return
	}

	version := g.configuredGBVersion()

	slog.Info("device info received",
		"device_id", deviceID,
		"manufacturer", msg.Manufacturer,
		"model", msg.Model,
		"firmware", msg.Firmware,
		"name", msg.DeviceName,
		"gb_version", version,
	)

	// A late response cannot enroll a removed device. Protect both the in-memory
	// update and persistence with the same device lock used by offline deletion.
	err := g.store.withExistingDevice(deviceID, func(dev *Device) error {
		dev.Manufacturer = msg.Manufacturer
		dev.Model = msg.Model
		dev.Firmware = msg.Firmware
		if dev.GBVersion == GBVersionUnknown || dev.GBVersion == "" {
			dev.GBVersion = version
		}
		return g.store.SaveDeviceInfo(deviceID, msg.Manufacturer, msg.Model, msg.Firmware)
	})
	if err != nil && !errors.Is(err, ErrDeviceNotExist) {
		slog.Error("failed to save device info to DB", "device_id", deviceID, "error", err)
	}
}

func (g *GB28181API) sendDeviceInfoQuery(deviceID string) {
	// Snapshot the connection fields under the device lock: REGISTER/login and
	// logout mutate them concurrently while this goroutine runs.
	var snap messagingSnapshot
	err := g.store.withExistingDevice(deviceID, func(dev *Device) error {
		// The store already holds the device lock for this callback.
		snap = messagingSnapshot{online: dev.IsOnline, address: dev.Address, transport: dev.Transport}
		return nil
	})
	if err != nil || !snap.online {
		return
	}

	xmlBody := deviceInfoXML(deviceID)
	if err := g.sendMessage(deviceID, &Device{Address: snap.address, Transport: snap.transport}, xmlBody); err != nil {
		slog.Error("send DeviceInfo query failed", "device_id", deviceID, "error", err)
	}
}

func deviceInfoXML(deviceID string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Query>
<CmdType>DeviceInfo</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
</Query>`, randInt(100000, 999999), deviceID))
}
