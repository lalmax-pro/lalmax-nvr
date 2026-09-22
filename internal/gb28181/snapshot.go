package gb28181

import (
	"encoding/xml"
	"fmt"
	"log/slog"
)

// SnapshotRequest 图像抓拍请求
type SnapshotRequest struct {
	XMLName  xml.Name `xml:"Control"`
	CmdType  string   `xml:"CmdType"`
	SN       int      `xml:"SN"`
	DeviceID string   `xml:"DeviceID"`
	Info     struct {
		ControlPriority int `xml:"ControlPriority"`
	} `xml:"Info"`
}

// DeviceConfigRequest 设备配置请求
type DeviceConfigRequest struct {
	XMLName  xml.Name `xml:"Control"`
	CmdType  string   `xml:"CmdType"`
	SN       int      `xml:"SN"`
	DeviceID string   `xml:"DeviceID"`
	Info     struct {
		ControlPriority int `xml:"ControlPriority"`
	} `xml:"Info"`
}

// DeviceResetRequest 设备复位请求
type DeviceResetRequest struct {
	XMLName  xml.Name `xml:"Control"`
	CmdType  string   `xml:"CmdType"`
	SN       int      `xml:"SN"`
	DeviceID string   `xml:"DeviceID"`
	Info     struct {
		ControlPriority int `xml:"ControlPriority"`
	} `xml:"Info"`
}

// RecordControlRequest 录像控制请求
type RecordControlRequest struct {
	XMLName   xml.Name `xml:"Control"`
	CmdType   string   `xml:"CmdType"`
	SN        int      `xml:"SN"`
	DeviceID  string   `xml:"DeviceID"`
	RecordCmd string   `xml:"RecordCmd"`
	Info      struct {
		ControlPriority int `xml:"ControlPriority"`
	} `xml:"Info"`
}

// HomePositionRequest 看守位控制请求
type HomePositionRequest struct {
	XMLName      xml.Name `xml:"Control"`
	CmdType      string   `xml:"CmdType"`
	SN           int      `xml:"SN"`
	DeviceID     string   `xml:"DeviceID"`
	HomePosition struct {
		Enabled     int `xml:"Enabled"`
		ResetTime   int `xml:"ResetTime"`
		PresetIndex int `xml:"PresetIndex"`
	} `xml:"HomePosition"`
}

// Snapshot 发送图像抓拍命令
func (g *GB28181API) Snapshot(deviceID, channelID string) error {
	dev, ok := g.store.Load(deviceID)
	if !ok {
		return fmt.Errorf("device not found: %s", deviceID)
	}
	if !dev.IsOnline {
		return ErrDeviceOffline
	}

	req := SnapshotRequest{
		CmdType:  "DeviceControl",
		SN:       randInt(100000, 999999),
		DeviceID: channelID,
	}
	req.Info.ControlPriority = 5

	b, _ := xml.Marshal(req)
	xmlBody := append([]byte(`<?xml version="1.0" encoding="GB2312"?>`+"\n"), b...)

	if err := g.sendMessage(channelID, dev, xmlBody); err != nil {
		return fmt.Errorf("send snapshot command failed: %w", err)
	}

	slog.Info("snapshot command sent", "device_id", deviceID, "channel_id", channelID)
	return nil
}

// DeviceReset 发送设备复位命令
func (g *GB28181API) DeviceReset(deviceID, channelID string) error {
	dev, ok := g.store.Load(deviceID)
	if !ok {
		return fmt.Errorf("device not found: %s", deviceID)
	}
	if !dev.IsOnline {
		return ErrDeviceOffline
	}

	req := DeviceResetRequest{
		CmdType:  "DeviceControl",
		SN:       randInt(100000, 999999),
		DeviceID: channelID,
	}
	req.Info.ControlPriority = 5

	b, _ := xml.Marshal(req)
	xmlBody := append([]byte(`<?xml version="1.0" encoding="GB2312"?>`+"\n"), b...)

	if err := g.sendMessage(channelID, dev, xmlBody); err != nil {
		return fmt.Errorf("send device reset command failed: %w", err)
	}

	slog.Info("device reset command sent", "device_id", deviceID, "channel_id", channelID)
	return nil
}

// RecordControl 发送录像控制命令
// recordCmd: "record" 开始录像, "stop" 停止录像
func (g *GB28181API) RecordControl(deviceID, channelID, recordCmd string) error {
	dev, ok := g.store.Load(deviceID)
	if !ok {
		return fmt.Errorf("device not found: %s", deviceID)
	}
	if !dev.IsOnline {
		return ErrDeviceOffline
	}

	req := RecordControlRequest{
		CmdType:   "DeviceControl",
		SN:        randInt(100000, 999999),
		DeviceID:  channelID,
		RecordCmd: recordCmd,
	}
	req.Info.ControlPriority = 5

	b, _ := xml.Marshal(req)
	xmlBody := append([]byte(`<?xml version="1.0" encoding="GB2312"?>`+"\n"), b...)

	if err := g.sendMessage(channelID, dev, xmlBody); err != nil {
		return fmt.Errorf("send record control command failed: %w", err)
	}

	slog.Info("record control command sent", "device_id", deviceID, "channel_id", channelID, "cmd", recordCmd)
	return nil
}

// SetHomePosition 设置看守位
// enabled: 0=禁用, 1=启用
// resetTime: 自动归位时间(秒)
// presetIndex: 预置位编号(1-255)
func (g *GB28181API) SetHomePosition(deviceID, channelID string, enabled, resetTime, presetIndex int) error {
	dev, ok := g.store.Load(deviceID)
	if !ok {
		return fmt.Errorf("device not found: %s", deviceID)
	}
	if !dev.IsOnline {
		return ErrDeviceOffline
	}

	req := HomePositionRequest{
		CmdType:  "DeviceControl",
		SN:       randInt(100000, 999999),
		DeviceID: channelID,
	}
	req.HomePosition.Enabled = enabled
	req.HomePosition.ResetTime = resetTime
	req.HomePosition.PresetIndex = presetIndex

	b, _ := xml.Marshal(req)
	xmlBody := append([]byte(`<?xml version="1.0" encoding="GB2312"?>`+"\n"), b...)

	if err := g.sendMessage(channelID, dev, xmlBody); err != nil {
		return fmt.Errorf("send home position command failed: %w", err)
	}

	slog.Info("home position command sent",
		"device_id", deviceID,
		"channel_id", channelID,
		"enabled", enabled,
		"reset_time", resetTime,
		"preset_index", presetIndex)
	return nil
}

// DeviceConfigQuery 查询设备配置
func (g *GB28181API) DeviceConfigQuery(deviceID, channelID string) error {
	dev, ok := g.store.Load(deviceID)
	if !ok {
		return fmt.Errorf("device not found: %s", deviceID)
	}
	if !dev.IsOnline {
		return ErrDeviceOffline
	}

	xmlBody := []byte(fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Query>
<CmdType>ConfigDownload</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
</Query>`, randInt(100000, 999999), channelID))

	if err := g.sendMessage(channelID, dev, xmlBody); err != nil {
		return fmt.Errorf("send config query failed: %w", err)
	}

	slog.Info("config query sent", "device_id", deviceID, "channel_id", channelID)
	return nil
}

// DeviceStatusQuery 查询设备状态
func (g *GB28181API) DeviceStatusQuery(deviceID, channelID string) error {
	dev, ok := g.store.Load(deviceID)
	if !ok {
		return fmt.Errorf("device not found: %s", deviceID)
	}
	if !dev.IsOnline {
		return ErrDeviceOffline
	}

	xmlBody := []byte(fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Query>
<CmdType>DeviceStatus</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
</Query>`, randInt(100000, 999999), channelID))

	if err := g.sendMessage(channelID, dev, xmlBody); err != nil {
		return fmt.Errorf("send device status query failed: %w", err)
	}

	slog.Info("device status query sent", "device_id", deviceID, "channel_id", channelID)
	return nil
}

// DeviceInfoQuery 查询设备信息
func (g *GB28181API) DeviceInfoQuery(deviceID, channelID string) error {
	dev, ok := g.store.Load(deviceID)
	if !ok {
		return fmt.Errorf("device not found: %s", deviceID)
	}
	if !dev.IsOnline {
		return ErrDeviceOffline
	}

	xmlBody := []byte(fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Query>
<CmdType>DeviceInfo</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
</Query>`, randInt(100000, 999999), channelID))

	if err := g.sendMessage(channelID, dev, xmlBody); err != nil {
		return fmt.Errorf("send device info query failed: %w", err)
	}

	slog.Info("device info query sent", "device_id", deviceID, "channel_id", channelID)
	return nil
}
