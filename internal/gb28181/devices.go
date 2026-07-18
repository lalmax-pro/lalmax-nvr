package gb28181

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

type Device struct {
	DeviceID string
	Channels sync.Map

	registerWithKeepaliveMutex sync.Mutex
	playMutex                  sync.Mutex

	IsOnline bool
	Address  string
	Password string
	region   string

	conn   sip.Connection
	source string

	LastKeepaliveAt time.Time
	LastRegisterAt  time.Time

	keepaliveInterval uint16
	keepaliveTimeout  uint16

	// 设备信息
	Manufacturer string
	Model        string
	Firmware     string
	GBVersion    GBVersion // GB28181标准版本 (2016/2022)
}

func (d *Device) Conn() sip.Connection {
	return d.conn
}

func (d *Device) Source() string {
	return d.source
}

func (d *Device) GetChannel(channelID string) (*Channel, bool) {
	v, ok := d.Channels.Load(channelID)
	if !ok {
		return nil, false
	}
	return v.(*Channel), true
}

type Channel struct {
	ChannelID string
	Name      string
	uriStr    string
	device    *Device

	// 2016标准字段
	Manufacturer    string  // 设备厂商
	Model           string  // 设备型号
	Owner           string  // 设备归属
	CivilCode       string  // 行政区域
	Block           string  // 警区
	Address         string  // 安装地址
	Parental        int     // 是否有子设备
	ParentID        string  // 父节点ID
	SafetyWay       int     // 信令安全模式
	RegisterWay     int     // 注册方式
	CertNum         string  // 证书序列号
	Certifiable     int     // 证书有效标识
	ErrCode         int     // 无效原因码
	EndTime         string  // 证书终止有效期
	Secrecy         int     // 保密属性
	IPAddress       string  // 设备IP地址
	Port            int     // 设备端口
	Password        string  // 设备口令
	Status          string  // 设备状态
	Longitude       float64 // 经度
	Latitude        float64 // 纬度
	PTZType         int     // 摄像机结构类型
	PositionType    int     // 摄像机位置类型
	RoomType        int     // 室内室外属性
	UseType         int     // 用途属性
	SupplyLightType int     // 补光属性
	DirectionType   int     // 监视方位属性
	Resolution      string  // 支持的分辨率

	// 2022标准新增字段
	SecurityLevelCode        string  // 安全能力等级代码
	StreamNumberList         string  // 码流编号列表
	DownloadSpeed            string  // 下载倍速
	SVCSpaceSupportMod       int     // 空域编码能力
	SVCTimeSupportMode       int     // 时域编码能力
	SSVCRatioSupportList     string  // SSVC增强层比例能力
	MobileDeviceType         int     // 移动设备类型
	HorizontalFieldAngle     float64 // 水平视场角
	VerticalFieldAngle       float64 // 垂直视场角
	MaxViewDistance          float64 // 可视距离
	GrassrootsCode           string  // 基层组织编码
	PoType                   int     // 监控点位类型
	PoCommonName             string  // 点位俗称
	Mac                      string  // MAC地址
	FunctionType             string  // 卡口功能类型
	EncodeType               string  // 视频编码格式
	InstallTime              string  // 安装使用时间
	ManagementUnit           string  // 管理单位名称
	ContactInfo              string  // 联系方式
	RecordSaveDays           int     // 录像保存天数
	IndustrialClassification string  // 行业分类代码
	BusinessGroupID          string  // 业务分组ID
}

func (c *Channel) Conn() sip.Connection {
	return c.device.conn
}

func (c *Channel) Source() string {
	return c.device.source
}

func (c *Channel) init(domain string) {
	c.uriStr = fmt.Sprintf("sip:%s@%s", c.ChannelID, domain)
}

// DeviceStore manages GB28181 devices with both memory cache and database persistence.
type DeviceStore struct {
	devices sync.Map
	db      *storage.DB
	hub     *WSHub
}

func NewDeviceStore(db *storage.DB, hub *WSHub) *DeviceStore {
	return &DeviceStore{
		db:  db,
		hub: hub,
	}
}

// GetDB returns the underlying database.
func (s *DeviceStore) GetDB() *storage.DB {
	return s.db
}

// LoadFromDB loads all devices from database into memory.
func (s *DeviceStore) LoadFromDB() error {
	ctx := context.Background()
	dbDevices, err := s.db.ListGB28181Devices(ctx)
	if err != nil {
		return err
	}

	for _, dbDev := range dbDevices {
		dev := &Device{
			DeviceID:  dbDev.DeviceID,
			IsOnline:  false, // Will be updated when device registers/heartbeats
			Address:   dbDev.Address,
			GBVersion: NormalizeGBVersion(dbDev.GBVersion),
		}
		if dbDev.LastKeepaliveAt != nil {
			dev.LastKeepaliveAt = *dbDev.LastKeepaliveAt
		}
		if dbDev.LastRegisterAt != nil {
			dev.LastRegisterAt = *dbDev.LastRegisterAt
		}

		// Load channels
		channels, err := s.db.ListGB28181Channels(ctx, dbDev.DeviceID)
		if err != nil {
			slog.Warn("Failed to load channels for device", "device_id", dbDev.DeviceID, "error", err)
		} else {
			for _, ch := range channels {
				channel := &Channel{
					ChannelID: ch.ChannelID,
					Name:      ch.Name,
					device:    dev,
				}
				dev.Channels.Store(ch.ChannelID, channel)
			}
		}

		s.devices.Store(dbDev.DeviceID, dev)
	}

	slog.Info("Loaded GB28181 devices from database", "count", len(dbDevices))
	return nil
}

func (s *DeviceStore) LoadOrStore(deviceID string, value *Device) {
	if value.GBVersion == "" {
		value.GBVersion = GBVersionUnknown
	}
	existing, loaded := s.devices.LoadOrStore(deviceID, value)
	if loaded {
		dev := existing.(*Device)
		if value.source != "" {
			dev.source = value.source
		}
		if dev.GBVersion == "" || dev.GBVersion == GBVersionUnknown {
			dev.GBVersion = value.GBVersion
		}
	}
}

func (s *DeviceStore) Load(deviceID string) (*Device, bool) {
	v, ok := s.devices.Load(deviceID)
	if !ok {
		return nil, false
	}
	return v.(*Device), true
}

func (s *DeviceStore) Store(deviceID string, value *Device) {
	s.devices.Store(deviceID, value)
}

func (s *DeviceStore) GetChannel(deviceID, channelID string) (*Channel, bool) {
	dev, ok := s.Load(deviceID)
	if !ok {
		return nil, false
	}
	return dev.GetChannel(channelID)
}

func (s *DeviceStore) Change(deviceID string, changeFn func(*Device)) error {
	v, ok := s.devices.Load(deviceID)
	if !ok {
		return ErrDeviceNotExist
	}
	dev := v.(*Device)
	dev.registerWithKeepaliveMutex.Lock()
	defer dev.registerWithKeepaliveMutex.Unlock()
	changeFn(dev)
	return nil
}

func (s *DeviceStore) RangeDevices(fn func(key string, value *Device) bool) {
	s.devices.Range(func(key, value any) bool {
		return fn(key.(string), value.(*Device))
	})
}

// SaveDevice persists device info to database.
func (s *DeviceStore) SaveDevice(deviceID string, dev *Device) error {
	ctx := context.Background()
	dbDev := &storage.GB28181DeviceRow{
		DeviceID:  deviceID,
		IsOnline:  dev.IsOnline,
		Address:   dev.Address,
		GBVersion: string(dev.GBVersion),
	}
	if !dev.LastKeepaliveAt.IsZero() {
		dbDev.LastKeepaliveAt = &dev.LastKeepaliveAt
	}
	if !dev.LastRegisterAt.IsZero() {
		dbDev.LastRegisterAt = &dev.LastRegisterAt
	}
	return s.db.UpsertGB28181Device(ctx, dbDev)
}

// UpdateDeviceStatus updates device online status in database.
func (s *DeviceStore) UpdateDeviceStatus(deviceID string, isOnline bool, address string) error {
	ctx := context.Background()
	if err := s.db.UpdateGB28181DeviceStatus(ctx, deviceID, isOnline, address); err != nil {
		return err
	}

	// 广播事件
	if s.hub != nil {
		eventType := EventDeviceOffline
		if isOnline {
			eventType = EventDeviceOnline
		}
		s.hub.Broadcast(Event{
			Type: eventType,
			Data: map[string]interface{}{
				"device_id": deviceID,
				"is_online": isOnline,
			},
		})
	}

	return nil
}

// UpdateDeviceRegistration updates device registration info in database.
func (s *DeviceStore) UpdateDeviceRegistration(deviceID string, address string) error {
	ctx := context.Background()
	return s.db.UpdateGB28181DeviceRegistration(ctx, deviceID, address)
}

// UpdateDeviceOnlineStatus sets device offline in database.
func (s *DeviceStore) UpdateDeviceOnlineStatus(deviceID string, isOnline bool) error {
	ctx := context.Background()
	return s.db.UpdateGB28181DeviceOnlineStatus(ctx, deviceID, isOnline)
}

func (s *DeviceStore) recordOperation(deviceID string, isOnline bool, address string) {
	if s.db == nil {
		return
	}
	action := "device.offline"
	message := "GB28181 device went offline"
	if isOnline {
		action = "device.online"
		message = "GB28181 device came online"
	}
	metadata, _ := json.Marshal(map[string]any{"address": address, "protocol": "gb28181"})
	if _, err := s.db.InsertOperationLog(context.Background(), model.OperationLog{
		ActorType:  "system",
		Action:     action,
		Resource:   "device",
		ResourceID: deviceID,
		Status:     "success",
		Message:    message,
		Metadata:   string(metadata),
	}); err != nil {
		slog.Warn("failed to write device operation log", "device_id", deviceID, "action", action, "error", err)
	}
}

// SaveChannels persists channels to database using incremental update.
func (s *DeviceStore) SaveChannels(deviceID string, channels []Channel) error {
	ctx := context.Background()
	dbChannels := make([]storage.GB28181ChannelRow, len(channels))
	for i, ch := range channels {
		dbChannels[i] = storage.GB28181ChannelRow{
			DeviceID:  deviceID,
			ChannelID: ch.ChannelID,
			Name:      ch.Name,

			// 2016标准字段
			Manufacturer:    ch.Manufacturer,
			Model:           ch.Model,
			Owner:           ch.Owner,
			CivilCode:       ch.CivilCode,
			Block:           ch.Block,
			Address:         ch.Address,
			Parental:        ch.Parental,
			ParentID:        ch.ParentID,
			SafetyWay:       ch.SafetyWay,
			RegisterWay:     ch.RegisterWay,
			CertNum:         ch.CertNum,
			Certifiable:     ch.Certifiable,
			ErrCode:         ch.ErrCode,
			EndTime:         ch.EndTime,
			Secrecy:         ch.Secrecy,
			IPAddress:       ch.IPAddress,
			Port:            ch.Port,
			Password:        ch.Password,
			Longitude:       ch.Longitude,
			Latitude:        ch.Latitude,
			PTZType:         ch.PTZType,
			PositionType:    ch.PositionType,
			RoomType:        ch.RoomType,
			UseType:         ch.UseType,
			SupplyLightType: ch.SupplyLightType,
			DirectionType:   ch.DirectionType,
			Resolution:      ch.Resolution,

			// 2022标准新增字段
			SecurityLevelCode:        ch.SecurityLevelCode,
			StreamNumberList:         ch.StreamNumberList,
			DownloadSpeed:            ch.DownloadSpeed,
			SVCSpaceSupportMod:       ch.SVCSpaceSupportMod,
			SVCTimeSupportMode:       ch.SVCTimeSupportMode,
			SSVCRatioSupportList:     ch.SSVCRatioSupportList,
			MobileDeviceType:         ch.MobileDeviceType,
			HorizontalFieldAngle:     ch.HorizontalFieldAngle,
			VerticalFieldAngle:       ch.VerticalFieldAngle,
			MaxViewDistance:          ch.MaxViewDistance,
			GrassrootsCode:           ch.GrassrootsCode,
			PoType:                   ch.PoType,
			PoCommonName:             ch.PoCommonName,
			Mac:                      ch.Mac,
			FunctionType:             ch.FunctionType,
			EncodeType:               ch.EncodeType,
			InstallTime:              ch.InstallTime,
			ManagementUnit:           ch.ManagementUnit,
			ContactInfo:              ch.ContactInfo,
			RecordSaveDays:           ch.RecordSaveDays,
			IndustrialClassification: ch.IndustrialClassification,
			BusinessGroupID:          ch.BusinessGroupID,
		}
	}
	return s.db.BatchUpsertChannels(ctx, deviceID, dbChannels)
}

// SaveDeviceInfo persists device info (manufacturer, model, firmware) to database.
func (s *DeviceStore) SaveDeviceInfo(deviceID, manufacturer, model, firmware string) error {
	ctx := context.Background()
	dbDev := &storage.GB28181DeviceRow{
		DeviceID:     deviceID,
		Manufacturer: manufacturer,
		Model:        model,
		Firmware:     firmware,
	}
	return s.db.UpsertGB28181Device(ctx, dbDev)
}

// DeleteDevice removes a device from memory and database.
func (s *DeviceStore) DeleteDevice(deviceID string) error {
	s.devices.Delete(deviceID)
	ctx := context.Background()
	return s.db.DeleteGB28181Device(ctx, deviceID)
}

func filterUnknowDevices(deviceID string) error {
	if len(deviceID) < 18 {
		return fmt.Errorf("device id too short")
	}
	if len(deviceID) > 20 {
		return fmt.Errorf("device id too long")
	}
	for _, ch := range deviceID {
		if ch < '0' || ch > '9' {
			return fmt.Errorf("device id must be all numbers")
		}
	}
	return nil
}

type ChannelsXML struct {
	DeviceID     string  `xml:"DeviceID"`
	ChannelID    string  `xml:"-"`
	Name         string  `xml:"Name"`
	Manufacturer string  `xml:"Manufacturer"`
	Model        string  `xml:"Model"`
	Owner        string  `xml:"Owner"`
	CivilCode    string  `xml:"CivilCode"`
	Block        string  `xml:"Block"`
	Address      string  `xml:"Address"`
	Parental     int     `xml:"Parental"`
	ParentID     string  `xml:"ParentID"`
	SafetyWay    int     `xml:"SafetyWay"`
	RegisterWay  int     `xml:"RegisterWay"`
	CertNum      string  `xml:"CertNum"`
	Certifiable  int     `xml:"Certifiable"`
	ErrCode      int     `xml:"ErrCode"`
	EndTime      string  `xml:"EndTime"`
	Secrecy      int     `xml:"Secrecy"`
	IPAddress    string  `xml:"IPAddress"`
	Port         int     `xml:"Port"`
	Password     string  `xml:"Password"`
	Status       string  `xml:"Status"`
	Longitude    float64 `xml:"Longitude"`
	Latitude     float64 `xml:"Latitude"`

	// Info子元素
	Info struct {
		PTZType            int    `xml:"PTZType"`
		PositionType       int    `xml:"PositionType"`
		RoomType           int    `xml:"RoomType"`
		UseType            int    `xml:"UseType"`
		SupplyLightType    int    `xml:"SupplyLightType"`
		DirectionType      int    `xml:"DirectionType"`
		Resolution         string `xml:"Resolution"`
		BusinessGroupID    string `xml:"BusinessGroupID"`
		DownloadSpeed      string `xml:"DownloadSpeed"`
		SVCSpaceSupportMod int    `xml:"SVCSpaceSupportMode"`
		SVCTimeSupportMode int    `xml:"SVCTimeSupportMode"`
	} `xml:"Info"`
}

type ChannelsXML2022 struct {
	ChannelsXML
	Info struct {
		PTZType                  int     `xml:"PTZType"`
		PositionType             int     `xml:"PositionType"`
		RoomType                 int     `xml:"RoomType"`
		UseType                  int     `xml:"UseType"`
		SupplyLightType          int     `xml:"SupplyLightType"`
		DirectionType            int     `xml:"DirectionType"`
		Resolution               string  `xml:"Resolution"`
		BusinessGroupID          string  `xml:"BusinessGroupID"`
		DownloadSpeed            string  `xml:"DownloadSpeed"`
		SVCSpaceSupportMod       int     `xml:"SVCSpaceSupportMode"`
		SVCTimeSupportMode       int     `xml:"SVCTimeSupportMode"`
		SecurityLevelCode        string  `xml:"SecurityLevelCode"`
		StreamNumberList         string  `xml:"StreamNumberList"`
		SSVCRatioSupportList     string  `xml:"SSVCRatioSupportList"`
		MobileDeviceType         int     `xml:"MobileDeviceType"`
		HorizontalFieldAngle     float64 `xml:"HorizontalFieldAngle"`
		VerticalFieldAngle       float64 `xml:"VerticalFieldAngle"`
		MaxViewDistance          float64 `xml:"MaxViewDistance"`
		GrassrootsCode           string  `xml:"GrassrootsCode"`
		PoType                   int     `xml:"PoType"`
		PoCommonName             string  `xml:"PoCommonName"`
		Mac                      string  `xml:"Mac"`
		FunctionType             string  `xml:"FunctionType"`
		EncodeType               string  `xml:"EncodeType"`
		InstallTime              string  `xml:"InstallTime"`
		ManagementUnit           string  `xml:"ManagementUnit"`
		ContactInfo              string  `xml:"ContactInfo"`
		RecordSaveDays           int     `xml:"RecordSaveDays"`
		IndustrialClassification string  `xml:"IndustrialClassification"`
	} `xml:"Info"`
}

type MessageNotify struct {
	CmdType  string `xml:"CmdType"`
	SN       int    `xml:"SN"`
	DeviceID string `xml:"DeviceID"`
	Status   string `xml:"Status"`
	Info     string `xml:"Info"`
}
