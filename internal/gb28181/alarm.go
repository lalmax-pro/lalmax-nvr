package gb28181

import (
	"context"
	"log/slog"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// AlarmMessage represents a parsed alarm notification from a device.
type AlarmMessage struct {
	CmdType     string `xml:"CmdType"`
	SN          int    `xml:"SN"`
	DeviceID    string `xml:"DeviceID"`
	AlarmType   string `xml:"AlarmType"`
	AlarmTime   string `xml:"AlarmTime"`
	Priority    int    `xml:"Priority"`
	Method      string `xml:"Method"`
	Description string `xml:"Description"`
}

// AlarmManager handles alarm subscriptions and notifications.
type AlarmManager struct {
	store  *storage.DB
	client *sipgo.Client
	cfg    *Config
}

// NewAlarmManager creates a new alarm manager.
func NewAlarmManager(client *sipgo.Client, cfg *Config, store *storage.DB) *AlarmManager {
	return &AlarmManager{
		store:  store,
		client: client,
		cfg:    cfg,
	}
}

// HandleAlarm processes an alarm message from a device.
func (am *AlarmManager) HandleAlarm(deviceID string, body []byte) {
	var msg AlarmMessage
	if err := xmlUnmarshal(body, &msg); err != nil {
		slog.Error("[Alarm] xml decode error", "device_id", deviceID, "error", err)
		return
	}

	slog.Info("[Alarm] received",
		"device_id", deviceID,
		"channel_id", msg.DeviceID,
		"alarm_type", msg.AlarmType,
		"priority", msg.Priority,
		"time", msg.AlarmTime,
	)

	// Parse alarm time
	alarmTime := time.Now()
	if msg.AlarmTime != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04:05", msg.AlarmTime, time.Local); err == nil {
			alarmTime = t
		}
	}

	// Store alarm
	alarmRow := &storage.AlarmRow{
		DeviceID:    deviceID,
		ChannelID:   msg.DeviceID,
		AlarmType:   msg.AlarmType,
		AlarmTime:   alarmTime,
		Priority:    msg.Priority,
		Method:      msg.Method,
		Description: msg.Description,
	}

	if _, err := am.store.CreateAlarm(context.Background(), alarmRow); err != nil {
		slog.Error("[Alarm] failed to save alarm", "device_id", deviceID, "error", err)
	}
}

// SubscribeAlarm sends an alarm subscription request to a device.
func (am *AlarmManager) SubscribeAlarm(deviceID string, store *DeviceStore) error {
	dev, ok := store.Load(deviceID)
	if !ok || !dev.IsOnline {
		return ErrDeviceOffline
	}

	alarmCmd := alarmSubscribeXML(deviceID, am.cfg.ID)
	return am.sendMessage(deviceID, dev, alarmCmd)
}

func (am *AlarmManager) sendMessage(targetID string, dev *Device, body []byte) error {
	uri := sip.Uri{
		Scheme: "sip",
		User:   targetID,
		Host:   getHost(dev.Address),
		Port:   getPort(dev.Address),
	}

	req := sip.NewRequest(sip.MESSAGE, uri)
	req.SetBody(body)
	req.AppendHeader(sip.NewHeader("Content-Type", "Application/MANSCDP+xml"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := am.client.Do(ctx, req)
	return err
}

func alarmSubscribeXML(deviceID, platformID string) []byte {
	return []byte(`<?xml version="1.0" encoding="GB2312"?>
<Query>
<CmdType>Alarm</CmdType>
<SN>` + itoa(randInt(100000, 999999)) + `</SN>
<DeviceID>` + deviceID + `</DeviceID>
<StartAlarmPriority>0</StartAlarmPriority>
<EndAlarmPriority>0</EndAlarmPriority>
<AlarmMethod>0</AlarmMethod>
</Query>`)
}

func itoa(i int) string {
	return string(rune('0' + i%10))
}
