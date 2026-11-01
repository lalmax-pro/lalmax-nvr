package gb28181

import (
	"encoding/xml"
	"testing"

	"github.com/stretchr/testify/require"
)

const catalog2016Fixture = `<?xml version="1.0" encoding="GB2312"?>
<Response>
<CmdType>Catalog</CmdType>
<SN>42</SN>
<DeviceID>34020000002000000001</DeviceID>
<SumNum>2</SumNum>
<DeviceList Num="2">
<Item>
<DeviceID>34020000001320000001</DeviceID>
<Name>Camera 1</Name>
<Manufacturer>Hikvision</Manufacturer>
<Model>DS-2CD</Model>
<Owner>OwnerA</Owner>
<CivilCode>340200</CivilCode>
<Block>Block1</Block>
<Address>Street 1</Address>
<Parental>0</Parental>
<ParentID>34020000002160000001</ParentID>
<SafetyWay>0</SafetyWay>
<RegisterWay>1</RegisterWay>
<CertNum>CERT-1</CertNum>
<Certifiable>1</Certifiable>
<ErrCode>0</ErrCode>
<EndTime>2030-01-01T00:00:00</EndTime>
<Secrecy>0</Secrecy>
<IPAddress>192.0.2.10</IPAddress>
<Port>5060</Port>
<Password>pwd</Password>
<Status>ON</Status>
<Longitude>117.28</Longitude>
<Latitude>31.86</Latitude>
<Info>
<PTZType>1</PTZType>
<PositionType>2</PositionType>
<RoomType>1</RoomType>
<UseType>3</UseType>
<SupplyLightType>1</SupplyLightType>
<DirectionType>1</DirectionType>
<Resolution>1920*1080</Resolution>
<BusinessGroupID>34020000002150000001</BusinessGroupID>
<DownloadSpeed>4</DownloadSpeed>
<SVCSpaceSupportMode>1</SVCSpaceSupportMode>
<SVCTimeSupportMode>1</SVCTimeSupportMode>
</Info>
</Item>
<Item>
<DeviceID>34020000001320000002</DeviceID>
<Name>Camera 2</Name>
<Status>OFF</Status>
</Item>
</DeviceList>
</Response>`

const catalog2022Fixture = `<?xml version="1.0" encoding="GB2312"?>
<Response>
<CmdType>Catalog</CmdType>
<SN>7</SN>
<DeviceID>34020000002000000001</DeviceID>
<SumNum>2</SumNum>
<DeviceList Num="2">
<Item>
<DeviceID>34020000001320000001</DeviceID>
<Name>Gate Cam</Name>
<Manufacturer>Vendor</Manufacturer>
<Info>
<PTZType>2</PTZType>
<PositionType>1</PositionType>
<RoomType>2</RoomType>
<UseType>1</UseType>
<SupplyLightType>2</SupplyLightType>
<DirectionType>3</DirectionType>
<Resolution>1280*720</Resolution>
<BusinessGroupID>34020000002150000001</BusinessGroupID>
<DownloadSpeed>8</DownloadSpeed>
<SVCSpaceSupportMode>2</SVCSpaceSupportMode>
<SVCTimeSupportMode>3</SVCTimeSupportMode>
<SecurityLevelCode>3</SecurityLevelCode>
<StreamNumberList>1/2</StreamNumberList>
<SSVCRatioSupportList>1:1</SSVCRatioSupportList>
<MobileDeviceType>1</MobileDeviceType>
<HorizontalFieldAngle>90.5</HorizontalFieldAngle>
<VerticalFieldAngle>45.5</VerticalFieldAngle>
<MaxViewDistance>120.25</MaxViewDistance>
<GrassrootsCode>340200001</GrassrootsCode>
<PoType>2</PoType>
<PoCommonName>Gate</PoCommonName>
<Mac>aa:bb:cc:dd:ee:ff</Mac>
<FunctionType>1</FunctionType>
<EncodeType>H.264</EncodeType>
<InstallTime>2024-01-02T03:04:05</InstallTime>
<ManagementUnit>UnitA</ManagementUnit>
<ContactInfo>10086</ContactInfo>
<RecordSaveDays>30</RecordSaveDays>
<IndustrialClassification>I</IndustrialClassification>
</Info>
</Item>
<Item>
<DeviceID>34020000001320000002</DeviceID>
<Name>Legacy Cam</Name>
</Item>
</DeviceList>
</Response>`

func TestCatalogDecoder2016MapsStandardFields(t *testing.T) {
	dev := &Device{DeviceID: "34020000002000000001"}
	sumNum, channels, supports2022, err := catalogDecoder2016{}.Decode(
		"34020000002000000001", []byte(catalog2016Fixture), "3402000000", dev)
	require.NoError(t, err)
	require.False(t, supports2022)
	require.Equal(t, 2, sumNum)
	require.Len(t, channels, 2)

	ch := channels[0]
	require.Equal(t, "34020000001320000001", ch.ChannelID)
	require.Equal(t, "Camera 1", ch.Name)
	require.Equal(t, "Hikvision", ch.Manufacturer)
	require.Equal(t, "DS-2CD", ch.Model)
	require.Equal(t, "OwnerA", ch.Owner)
	require.Equal(t, "340200", ch.CivilCode)
	require.Equal(t, "Block1", ch.Block)
	require.Equal(t, "Street 1", ch.Address)
	require.Equal(t, 0, ch.Parental)
	require.Equal(t, "34020000002160000001", ch.ParentID)
	require.Equal(t, 1, ch.RegisterWay)
	require.Equal(t, "CERT-1", ch.CertNum)
	require.Equal(t, "192.0.2.10", ch.IPAddress)
	require.Equal(t, 5060, ch.Port)
	require.Equal(t, "ON", ch.Status)
	require.InDelta(t, 117.28, ch.Longitude, 0.001)
	require.InDelta(t, 31.86, ch.Latitude, 0.001)
	require.Equal(t, 1, ch.PTZType)
	require.Equal(t, 2, ch.PositionType)
	require.Equal(t, "1920*1080", ch.Resolution)
	require.Equal(t, "34020000002150000001", ch.BusinessGroupID)
	require.Equal(t, "4", ch.DownloadSpeed)
	require.Equal(t, "sip:34020000001320000001@3402000000", ch.uriStr)
	require.Same(t, dev, ch.device)
	require.Equal(t, "Camera 2", channels[1].Name)
	require.Equal(t, "OFF", channels[1].Status)
}

func TestCatalogDecoder2022MapsExtendedAndLegacyItems(t *testing.T) {
	sumNum, channels, supports2022, err := catalogDecoder2022{}.Decode(
		"34020000002000000001", []byte(catalog2022Fixture), "3402000000", &Device{})
	require.NoError(t, err)
	require.True(t, supports2022)
	require.Equal(t, 2, sumNum)
	require.Len(t, channels, 2)

	ch := channels[0]
	require.Equal(t, "Gate Cam", ch.Name)
	require.Equal(t, 2, ch.PTZType)
	require.Equal(t, "3", ch.SecurityLevelCode)
	require.Equal(t, "1/2", ch.StreamNumberList)
	require.Equal(t, "1:1", ch.SSVCRatioSupportList)
	require.Equal(t, 1, ch.MobileDeviceType)
	require.InDelta(t, 90.5, ch.HorizontalFieldAngle, 0.001)
	require.InDelta(t, 45.5, ch.VerticalFieldAngle, 0.001)
	require.InDelta(t, 120.25, ch.MaxViewDistance, 0.001)
	require.Equal(t, "340200001", ch.GrassrootsCode)
	require.Equal(t, 2, ch.PoType)
	require.Equal(t, "Gate", ch.PoCommonName)
	require.Equal(t, "aa:bb:cc:dd:ee:ff", ch.Mac)
	require.Equal(t, "1", ch.FunctionType)
	require.Equal(t, "H.264", ch.EncodeType)
	require.Equal(t, "2024-01-02T03:04:05", ch.InstallTime)
	require.Equal(t, "UnitA", ch.ManagementUnit)
	require.Equal(t, "10086", ch.ContactInfo)
	require.Equal(t, 30, ch.RecordSaveDays)
	require.Equal(t, "I", ch.IndustrialClassification)
	require.False(t, channelHas2022Fields(channels[1]))
}

func TestCatalogDecoderRejectsInvalidXML(t *testing.T) {
	_, _, _, err := catalogDecoder2016{}.Decode("dev", []byte("<not-xml"), "domain", &Device{})
	require.Error(t, err)
	_, _, _, err = catalogDecoder2022{}.Decode("dev", []byte("<Response>"), "domain", &Device{})
	require.Error(t, err)
}

func TestCatalogDecoderEmptyList(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><Response><CmdType>Catalog</CmdType><SN>1</SN><DeviceID>d</DeviceID><SumNum>0</SumNum><DeviceList Num="0"></DeviceList></Response>`)
	sumNum, channels, supports2022, err := catalogDecoder2016{}.Decode("d", body, "domain", &Device{})
	require.NoError(t, err)
	require.Equal(t, 0, sumNum)
	require.Empty(t, channels)
	require.False(t, supports2022)
}

func TestCatalogDecoderVersions(t *testing.T) {
	require.Equal(t, GBVersion2016, catalogDecoder2016{}.Version())
	require.Equal(t, GBVersion2022, catalogDecoder2022{}.Version())
}

func TestCatalogXMLQueryBuilder(t *testing.T) {
	body := catalogXML("34020000002000000001")
	var msg struct {
		XMLName  xml.Name `xml:"Query"`
		CmdType  string   `xml:"CmdType"`
		SN       int      `xml:"SN"`
		DeviceID string   `xml:"DeviceID"`
	}
	require.NoError(t, xmlUnmarshal(body, &msg))
	require.Equal(t, "Catalog", msg.CmdType)
	require.Equal(t, "34020000002000000001", msg.DeviceID)
	require.GreaterOrEqual(t, msg.SN, 100000)
	require.LessOrEqual(t, msg.SN, 999999)
}

func TestCatalogDecoderForDeviceAndConfiguredVersion(t *testing.T) {
	nilAPI := &GB28181API{}
	require.Equal(t, GBVersion2016, nilAPI.configuredGBVersion())
	require.IsType(t, catalogDecoder2016{}, nilAPI.catalogDecoderForDevice(&Device{GBVersion: GBVersionUnknown}))

	api := &GB28181API{cfg: &Config{StandardVersion: "2022"}}
	require.Equal(t, GBVersion2022, api.configuredGBVersion())
	require.IsType(t, catalogDecoder2022{}, api.catalogDecoderForDevice(&Device{}))
	require.IsType(t, catalogDecoder2022{}, api.catalogDecoderForDevice(&Device{GBVersion: GBVersion2022}))
	require.IsType(t, catalogDecoder2016{}, api.catalogDecoderForDevice(&Device{GBVersion: GBVersion2016}))
}

func TestChannelHas2022Fields(t *testing.T) {
	require.False(t, channelHas2022Fields(Channel{}))
	cases := []Channel{
		{SecurityLevelCode: "1"},
		{StreamNumberList: "1"},
		{SSVCRatioSupportList: "1:1"},
		{MobileDeviceType: 1},
		{HorizontalFieldAngle: 1},
		{VerticalFieldAngle: 1},
		{MaxViewDistance: 1},
		{GrassrootsCode: "g"},
		{PoType: 1},
		{PoCommonName: "n"},
		{Mac: "aa"},
		{FunctionType: "f"},
		{EncodeType: "H.265"},
		{InstallTime: "t"},
		{ManagementUnit: "u"},
		{ContactInfo: "c"},
		{RecordSaveDays: 7},
		{IndustrialClassification: "I"},
	}
	for _, ch := range cases {
		require.True(t, channelHas2022Fields(ch), "%+v", ch)
	}
}

func TestQueryCatalogWithoutLiveDevice(t *testing.T) {
	api := &GB28181API{store: NewDeviceStore(nil, nil)}
	require.ErrorIs(t, api.QueryCatalog("missing"), ErrDeviceOffline)

	api.store.Store("34020000001110000001", &Device{DeviceID: "34020000001110000001", IsOnline: false})
	require.ErrorIs(t, api.QueryCatalog("34020000001110000001"), ErrDeviceOffline)
}

func TestDeviceControlHelpersWithoutLiveDevice(t *testing.T) {
	api := &GB28181API{store: NewDeviceStore(nil, nil)}
	missing := "missing-device"
	require.ErrorContains(t, api.Snapshot(missing, "ch"), "device not found")
	require.ErrorContains(t, api.DeviceReset(missing, "ch"), "device not found")
	require.ErrorContains(t, api.RecordControl(missing, "ch", "record"), "device not found")
	require.ErrorContains(t, api.SetHomePosition(missing, "ch", 1, 30, 1), "device not found")
	require.ErrorContains(t, api.DeviceConfigQuery(missing, "ch"), "device not found")
	require.ErrorContains(t, api.DeviceStatusQuery(missing, "ch"), "device not found")
	require.ErrorContains(t, api.DeviceInfoQuery(missing, "ch"), "device not found")
	require.ErrorContains(t, api.PTZControl(missing, "ch", "A50F"), "device not found")
	require.ErrorContains(t, api.QueryPTZPosition(missing, "ch"), "device not found")
	require.ErrorContains(t, api.SetPreset(missing, "ch", 1), "device not found")

	offlineID := "34020000001110000001"
	api.store.Store(offlineID, &Device{DeviceID: offlineID, IsOnline: false})
	require.ErrorIs(t, api.Snapshot(offlineID, "ch"), ErrDeviceOffline)
	require.ErrorIs(t, api.DeviceReset(offlineID, "ch"), ErrDeviceOffline)
	require.ErrorIs(t, api.RecordControl(offlineID, "ch", "stop"), ErrDeviceOffline)
	require.ErrorIs(t, api.SetHomePosition(offlineID, "ch", 0, 0, 1), ErrDeviceOffline)
	require.ErrorIs(t, api.DeviceConfigQuery(offlineID, "ch"), ErrDeviceOffline)
	require.ErrorIs(t, api.DeviceStatusQuery(offlineID, "ch"), ErrDeviceOffline)
	require.ErrorIs(t, api.DeviceInfoQuery(offlineID, "ch"), ErrDeviceOffline)
	require.ErrorIs(t, api.PTZControl(offlineID, "ch", "A50F"), ErrDeviceOffline)
	require.ErrorIs(t, api.QueryPTZPosition(offlineID, "ch"), ErrDeviceOffline)
}

func TestPTZPositionControlValidatesRangeWithoutSIP(t *testing.T) {
	id := "34020000001110000001"
	api := &GB28181API{store: NewDeviceStore(nil, nil)}
	api.store.Store(id, &Device{DeviceID: id, IsOnline: true})

	require.ErrorContains(t, api.PTZPositionControl(id, "ch", -1, 0, 1), "horizontal angle")
	require.ErrorContains(t, api.PTZPositionControl(id, "ch", 361, 0, 1), "horizontal angle")
	require.ErrorContains(t, api.PTZPositionControl(id, "ch", 0, -1, 1), "vertical angle")
	require.ErrorContains(t, api.PTZPositionControl(id, "ch", 0, 91, 1), "vertical angle")
	require.ErrorContains(t, api.PTZPositionControl(id, "ch", 0, 0, 0), "zoom level")
	require.ErrorContains(t, api.PTZPositionControl(id, "ch", 0, 0, 101), "zoom level")
}
