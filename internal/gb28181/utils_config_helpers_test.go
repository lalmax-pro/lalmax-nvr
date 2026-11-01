package gb28181

import (
	"errors"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestPortInt(t *testing.T) {
	require.Equal(t, 5060, portInt("5060"))
	require.Equal(t, 0, portInt("abc"))
	require.Equal(t, 80, portInt("80x"))
	require.Equal(t, 0, portInt(""))
}

func TestGetHostAndPort(t *testing.T) {
	require.Equal(t, "192.0.2.10", getHost("192.0.2.10:5060"))
	require.Equal(t, 5060, getPort("192.0.2.10:5060"))
	require.Equal(t, "192.0.2.10", getHost("192.0.2.10"))
	require.Equal(t, 5060, getPort("192.0.2.10"))
	require.Equal(t, 5060, getPort("192.0.2.10:0"))
	require.Equal(t, "2001:db8::1", getHost("[2001:db8::1]:5061"))
	require.Equal(t, 5061, getPort("[2001:db8::1]:5061"))
}

func TestXMLUnmarshalGB18030Retry(t *testing.T) {
	utf8XML := `<?xml version="1.0"?><Notify><CmdType>Keepalive</CmdType><Info>在线</Info></Notify>`
	gb, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(utf8XML))
	require.NoError(t, err)
	require.False(t, utf8.Valid(gb))

	var msg MessageNotify
	require.NoError(t, xmlUnmarshal(gb, &msg))
	require.Equal(t, "Keepalive", msg.CmdType)
	require.Equal(t, "在线", msg.Info)

	var retry MessageNotify
	require.Error(t, xmlUnmarshalRetry(gb, &retry), "retry without an encoding declaration cannot decode raw GB18030")
	require.Error(t, xmlUnmarshalRetry([]byte("<not-xml"), &retry))
}

func TestFilterUnknownDevices(t *testing.T) {
	require.ErrorContains(t, filterUnknowDevices("123"), "too short")
	require.ErrorContains(t, filterUnknowDevices("123456789012345678901"), "too long")
	require.ErrorContains(t, filterUnknowDevices("3402000000111000000a"), "all numbers")
	require.NoError(t, filterUnknowDevices("340200000011100000"))
	require.NoError(t, filterUnknowDevices("34020000001110000001"))
}

func TestConfigValidateAndApplyDefaultsGaps(t *testing.T) {
	disabled := &Config{Enabled: false, ID: "bad"}
	require.NoError(t, disabled.Validate())

	base := func() *Config {
		return &Config{
			Enabled:  true,
			ID:       "34020000002000000001",
			Port:     5060,
			MediaIP:  "192.0.2.10",
			Password: "secret",
		}
	}

	cfg := base()
	cfg.ID = "123"
	require.ErrorContains(t, cfg.Validate(), "10-20 digits")

	cfg = base()
	cfg.ID = "123456789012345678901"
	require.ErrorContains(t, cfg.Validate(), "10-20 digits")

	cfg = base()
	cfg.ID = "34020000002ABCDEF001"
	require.ErrorContains(t, cfg.Validate(), "only digits")

	cfg = base()
	cfg.Port = 0
	require.ErrorContains(t, cfg.Validate(), "1-65535")

	cfg = base()
	cfg.Port = 65536
	require.ErrorContains(t, cfg.Validate(), "1-65535")

	cfg = base()
	cfg.Password = ""
	require.ErrorContains(t, cfg.Validate(), "password is required")

	cfg = base()
	cfg.Password = "#"
	require.ErrorContains(t, cfg.Validate(), "password is required")

	cfg = base()
	cfg.StandardVersion = "1999"
	require.ErrorContains(t, cfg.Validate(), "standard_version")

	cfg = base()
	cfg.MediaIP = "not-an-ip"
	require.NoError(t, cfg.Validate())
	require.NotEqual(t, "not-an-ip", cfg.MediaIP, "invalid media_ip is replaced by auto-detection")

	ok := base()
	require.NoError(t, ok.Validate())
	require.Equal(t, "3402000000", ok.GetDomain())
	require.Equal(t, GBVersion2016, ok.GBVersion())

	short := &Config{ID: "34020000"}
	require.Equal(t, "34020000", short.GetDomain())

	defaults := &Config{}
	defaults.ApplyDefaults()
	require.Equal(t, 5060, defaults.Port)
	require.Equal(t, string(GBVersion2016), defaults.StandardVersion)

	require.Equal(t, GBVersion2016, NormalizeGBVersion(""))
	require.Equal(t, GBVersion2016, NormalizeGBVersion("GB/T28181-2016"))
	require.Equal(t, GBVersion2016, NormalizeGBVersion("GB28181-2016"))
	require.Equal(t, GBVersion2022, NormalizeGBVersion("2022"))
	require.Equal(t, GBVersion2022, NormalizeGBVersion("GB/T28181-2022"))
	require.Equal(t, GBVersion2022, NormalizeGBVersion("GB28181-2022"))
	require.Equal(t, GBVersionUnknown, NormalizeGBVersion("v3"))
}

func TestPackageErrors(t *testing.T) {
	require.Equal(t, "xml decode error", ErrXMLDecode.Error())
	require.Equal(t, "device not exist", ErrDeviceNotExist.Error())
	require.Equal(t, "channel not exist", ErrChannelNotExist.Error())
	require.Equal(t, "device offline", ErrDeviceOffline.Error())
	require.Equal(t, "device is online", ErrDeviceOnline.Error())
	require.Equal(t, "device still has an active GB28181 stream", ErrDeviceHasActiveStream.Error())
	require.Equal(t, "device keepalive source IP mismatch", ErrDeviceSourceMismatch.Error())
	require.Equal(t, "channel offline", ErrChannelOffline.Error())
	require.True(t, errors.Is(ErrDeviceOffline, ErrDeviceOffline))
}

func TestDeviceChannelHelpers(t *testing.T) {
	dev := &Device{DeviceID: "dev", source: "192.0.2.1:5060"}
	ch, ok := dev.GetChannel("missing")
	require.False(t, ok)
	require.Nil(t, ch)

	channel := &Channel{ChannelID: "ch1", device: dev}
	channel.init("3402000000")
	dev.Channels.Store("ch1", channel)

	got, ok := dev.GetChannel("ch1")
	require.True(t, ok)
	require.Equal(t, "sip:ch1@3402000000", got.uriStr)
	require.Equal(t, "192.0.2.1:5060", got.Source())
	require.Equal(t, "192.0.2.1:5060", dev.Source())
	require.Nil(t, got.Conn())
	require.Nil(t, dev.Conn())
}

func TestStreamsManagerLoadStream(t *testing.T) {
	mgr := &streamsManager{}
	_, ok := mgr.loadStream("missing")
	require.False(t, ok)
	stream := &Streams{DeviceID: "d", ChannelID: "c"}
	mgr.storeStream("k", stream)
	got, ok := mgr.loadStream("k")
	require.True(t, ok)
	require.Equal(t, stream, got)
}

func TestNewGB28181APIWiresDependencies(t *testing.T) {
	cfg := &Config{ID: "34020000002000000001"}
	store := NewDeviceStore(nil, NewWSHub())
	api := NewGB28181API(cfg, store, nil, nil)
	require.Equal(t, cfg, api.cfg)
	require.Equal(t, store, api.store)
	require.Equal(t, globalStreams, api.streams)
	require.Equal(t, store.hub, api.hub)
	require.NotNil(t, api.digestNonces)
}

func TestBroadcastAndTalkKeys(t *testing.T) {
	require.Equal(t, "dev_ch", broadcastKey("dev", "ch"))
	bm := NewBroadcastManager(nil, &Config{}, nil)
	require.NotNil(t, bm)
	_, ok := bm.GetSession("dev", "ch")
	require.False(t, ok)
	require.NoError(t, bm.StopBroadcast("dev", "ch"))
}

func TestIsAdvertisableMediaIP(t *testing.T) {
	require.False(t, isAdvertisableMediaIP(""))
	require.False(t, isAdvertisableMediaIP("0.0.0.0"))
	require.False(t, isAdvertisableMediaIP("127.0.0.1"))
	require.False(t, isAdvertisableMediaIP("::1"))
	require.True(t, isAdvertisableMediaIP("192.0.2.10"))
	require.True(t, isAdvertisableMediaIP("192.0.2.10:5060"))
	require.False(t, isAdvertisableMediaIP("not-an-ip"))
}

func TestSplitLinesAndFields(t *testing.T) {
	require.Equal(t, []string{"a", "b", "c"}, splitLines("a\r\nb\nc"))
	require.Equal(t, []string{"a", "b"}, splitLines("a\nb"))
	require.Empty(t, splitLines(""))
	require.Equal(t, []string{"IN", "IP4", "1.2.3.4"}, splitFields("IN IP4 1.2.3.4"))
	require.Equal(t, []string{"a", "b"}, splitFields("a\tb"))
	require.Empty(t, splitFields("   "))
}
