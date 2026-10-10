package gb28181

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

type MessageDeviceListResponse struct {
	XMLName  struct{}      `xml:"Response"`
	CmdType  string        `xml:"CmdType"`
	SN       int           `xml:"SN"`
	DeviceID string        `xml:"DeviceID"`
	SumNum   int           `xml:"SumNum"`
	Item     []ChannelsXML `xml:"DeviceList>Item"`
}

type MessageDeviceListResponse2022 struct {
	XMLName  struct{}          `xml:"Response"`
	CmdType  string            `xml:"CmdType"`
	SN       int               `xml:"SN"`
	DeviceID string            `xml:"DeviceID"`
	SumNum   int               `xml:"SumNum"`
	Item     []ChannelsXML2022 `xml:"DeviceList>Item"`
}

type catalogDecoder interface {
	Decode(deviceID string, body []byte, domain string, dev *Device) (int, []Channel, bool, error)
	Version() GBVersion
}

type catalogDecoder2016 struct{}
type catalogDecoder2022 struct{}

func (catalogDecoder2016) Version() GBVersion { return GBVersion2016 }
func (catalogDecoder2022) Version() GBVersion { return GBVersion2022 }

func (catalogDecoder2016) Decode(deviceID string, body []byte, domain string, dev *Device) (int, []Channel, bool, error) {
	var msg MessageDeviceListResponse
	if err := xmlUnmarshal(body, &msg); err != nil {
		return 0, nil, false, err
	}
	return msg.SumNum, catalogItems2016ToChannels(deviceID, domain, dev, msg.Item), false, nil
}

func (catalogDecoder2022) Decode(deviceID string, body []byte, domain string, dev *Device) (int, []Channel, bool, error) {
	var msg MessageDeviceListResponse2022
	if err := xmlUnmarshal(body, &msg); err != nil {
		return 0, nil, false, err
	}
	channels := make([]Channel, 0, len(msg.Item))
	supports2022 := false
	for _, item := range msg.Item {
		channel := catalogItem2022ToChannel(deviceID, domain, dev, item)
		if channelHas2022Fields(channel) {
			supports2022 = true
		}
		channels = append(channels, channel)
	}
	return msg.SumNum, channels, supports2022, nil
}

func (g *GB28181API) handleCatalogResponse(deviceID string, body []byte) {
	dev, ok := g.store.Load(deviceID)
	if !ok {
		return
	}
	domain := dev.region
	if domain == "" {
		domain = g.cfg.GetDomain()
	}

	decoder := g.catalogDecoderForDevice(dev)
	sumNum, dbChannels, supports2022, err := decoder.Decode(deviceID, body, domain, dev)
	if err != nil {
		slog.Error("catalog xml decode error", "device_id", deviceID, "gb_version", decoder.Version(), "error", err)
		return
	}
	if sumNum <= 0 {
		return
	}

	// Ensure device row exists before saving channels
	if err := g.store.SaveDevice(deviceID, dev); err != nil {
		slog.Error("failed to save device before catalog", "device_id", deviceID, "error", err)
	}

	newChannelMap := make(map[string]bool)
	for _, ch := range dbChannels {
		newChannelMap[ch.ChannelID] = true
	}

	// 内存全量替换
	dev.Channels = sync.Map{}
	for i := range dbChannels {
		dev.Channels.Store(dbChannels[i].ChannelID, &dbChannels[i])
	}

	// 获取现有通道用于失踪计数
	existingChannels := g.getExistingChannelIDs(deviceID)

	// Persist channels to database
	if err := g.store.SaveChannels(deviceID, dbChannels); err != nil {
		slog.Error("failed to save channels to DB", "device_id", deviceID, "error", err)
	}

	// 更新失踪计数
	for _, chID := range existingChannels {
		if !newChannelMap[chID] {
			if err := g.store.db.IncrementMissingCount(context.Background(), deviceID, chID); err != nil {
				slog.Error("failed to increment missing count",
					"device_id", deviceID,
					"channel_id", chID,
					"error", err)
			}
		}
	}

	if decoder.Version() == GBVersion2022 || supports2022 {
		dev.GBVersion = GBVersion2022
		slog.Info("device detected as GB28181-2022", "device_id", deviceID)
	} else if dev.GBVersion == GBVersionUnknown {
		dev.GBVersion = GBVersion2016
		slog.Info("device detected as GB28181-2016", "device_id", deviceID)
	}
	if err := g.store.SaveDevice(deviceID, dev); err != nil {
		slog.Error("failed to save device version after catalog", "device_id", deviceID, "gb_version", dev.GBVersion, "error", err)
	}

	// 广播通道更新事件
	if g.hub != nil {
		g.hub.Broadcast(Event{
			Type: EventChannelUpdate,
			Data: map[string]interface{}{
				"device_id":     deviceID,
				"channel_count": len(dbChannels),
				"gb_version":    string(dev.GBVersion),
			},
		})
	}

	slog.Info("catalog updated", "device_id", deviceID, "channels", len(dbChannels), "gb_version", dev.GBVersion)
}

func (g *GB28181API) configuredGBVersion() GBVersion {
	if g.cfg == nil {
		return GBVersion2016
	}
	return g.cfg.GBVersion()
}

func (g *GB28181API) catalogDecoderForDevice(dev *Device) catalogDecoder {
	version := dev.GBVersion
	if version == "" || version == GBVersionUnknown {
		version = g.configuredGBVersion()
	}
	if version == GBVersion2022 {
		return catalogDecoder2022{}
	}
	return catalogDecoder2016{}
}

func catalogItems2016ToChannels(deviceID, domain string, dev *Device, items []ChannelsXML) []Channel {
	channels := make([]Channel, 0, len(items))
	for _, item := range items {
		channels = append(channels, catalogItem2016ToChannel(deviceID, domain, dev, item))
	}
	return channels
}

func catalogItem2016ToChannel(deviceID, domain string, dev *Device, item ChannelsXML) Channel {
	item.ChannelID = item.DeviceID
	item.DeviceID = deviceID
	channel := Channel{
		ChannelID:          item.ChannelID,
		Name:               item.Name,
		device:             dev,
		Manufacturer:       item.Manufacturer,
		Model:              item.Model,
		Owner:              item.Owner,
		CivilCode:          item.CivilCode,
		Block:              item.Block,
		Address:            item.Address,
		Parental:           item.Parental,
		ParentID:           item.ParentID,
		SafetyWay:          item.SafetyWay,
		RegisterWay:        item.RegisterWay,
		CertNum:            item.CertNum,
		Certifiable:        item.Certifiable,
		ErrCode:            item.ErrCode,
		EndTime:            item.EndTime,
		Secrecy:            item.Secrecy,
		IPAddress:          item.IPAddress,
		Port:               item.Port,
		Password:           item.Password,
		Status:             item.Status,
		Longitude:          item.Longitude,
		Latitude:           item.Latitude,
		PTZType:            item.Info.PTZType,
		PositionType:       item.Info.PositionType,
		RoomType:           item.Info.RoomType,
		UseType:            item.Info.UseType,
		SupplyLightType:    item.Info.SupplyLightType,
		DirectionType:      item.Info.DirectionType,
		Resolution:         item.Info.Resolution,
		DownloadSpeed:      item.Info.DownloadSpeed,
		SVCSpaceSupportMod: item.Info.SVCSpaceSupportMod,
		SVCTimeSupportMode: item.Info.SVCTimeSupportMode,
		BusinessGroupID:    item.Info.BusinessGroupID,
	}
	channel.init(domain)
	return channel
}

func catalogItem2022ToChannel(deviceID, domain string, dev *Device, item ChannelsXML2022) Channel {
	channel := catalogItem2016ToChannel(deviceID, domain, dev, item.ChannelsXML)
	channel.PTZType = item.Info.PTZType
	channel.PositionType = item.Info.PositionType
	channel.RoomType = item.Info.RoomType
	channel.UseType = item.Info.UseType
	channel.SupplyLightType = item.Info.SupplyLightType
	channel.DirectionType = item.Info.DirectionType
	channel.Resolution = item.Info.Resolution
	channel.BusinessGroupID = item.Info.BusinessGroupID
	channel.DownloadSpeed = item.Info.DownloadSpeed
	channel.SVCSpaceSupportMod = item.Info.SVCSpaceSupportMod
	channel.SVCTimeSupportMode = item.Info.SVCTimeSupportMode
	channel.SecurityLevelCode = item.Info.SecurityLevelCode
	channel.StreamNumberList = item.Info.StreamNumberList
	channel.SSVCRatioSupportList = item.Info.SSVCRatioSupportList
	channel.MobileDeviceType = item.Info.MobileDeviceType
	channel.HorizontalFieldAngle = item.Info.HorizontalFieldAngle
	channel.VerticalFieldAngle = item.Info.VerticalFieldAngle
	channel.MaxViewDistance = item.Info.MaxViewDistance
	channel.GrassrootsCode = item.Info.GrassrootsCode
	channel.PoType = item.Info.PoType
	channel.PoCommonName = item.Info.PoCommonName
	channel.Mac = item.Info.Mac
	channel.FunctionType = item.Info.FunctionType
	channel.EncodeType = item.Info.EncodeType
	channel.InstallTime = item.Info.InstallTime
	channel.ManagementUnit = item.Info.ManagementUnit
	channel.ContactInfo = item.Info.ContactInfo
	channel.RecordSaveDays = item.Info.RecordSaveDays
	channel.IndustrialClassification = item.Info.IndustrialClassification
	return channel
}

func channelHas2022Fields(ch Channel) bool {
	return ch.SecurityLevelCode != "" || ch.StreamNumberList != "" ||
		ch.SSVCRatioSupportList != "" || ch.MobileDeviceType > 0 ||
		ch.HorizontalFieldAngle > 0 || ch.VerticalFieldAngle > 0 ||
		ch.MaxViewDistance > 0 || ch.GrassrootsCode != "" ||
		ch.PoType > 0 || ch.PoCommonName != "" || ch.Mac != "" ||
		ch.FunctionType != "" || ch.EncodeType != "" ||
		ch.InstallTime != "" || ch.ManagementUnit != "" ||
		ch.ContactInfo != "" || ch.RecordSaveDays > 0 ||
		ch.IndustrialClassification != ""
}

// getExistingChannelIDs returns existing channel IDs for a device.
func (g *GB28181API) getExistingChannelIDs(deviceID string) []string {
	ctx := context.Background()
	channels, err := g.store.db.ListGB28181Channels(ctx, deviceID)
	if err != nil {
		slog.Error("failed to list existing channels", "device_id", deviceID, "error", err)
		return nil
	}

	var channelIDs []string
	for _, ch := range channels {
		channelIDs = append(channelIDs, ch.ChannelID)
	}
	return channelIDs
}

func (g *GB28181API) QueryCatalog(deviceID string) error {
	slog.Debug("QueryCatalog", "device_id", deviceID)
	dev, ok := g.store.Load(deviceID)
	if !ok || !dev.IsOnline {
		return ErrDeviceOffline
	}

	xmlBody := catalogXML(deviceID)
	return g.sendMessage(deviceID, dev, xmlBody)
}

func catalogXML(deviceID string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Query>
<CmdType>Catalog</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
</Query>`, randInt(100000, 999999), deviceID))
}
