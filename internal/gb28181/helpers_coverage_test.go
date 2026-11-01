package gb28181

import (
	"encoding/hex"
	"encoding/xml"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeGBCode(t *testing.T) {
	require.Nil(t, DecodeGBCode("short"))
	require.Nil(t, DecodeGBCode("3402000000215000000a"))
	code := DecodeGBCode("34020000002150000001")
	require.Equal(t, &GBCode{
		CenterCode: "34020000", IndustryCode: "00", TypeCode: "215", NetCode: "0", SN: "000001",
	}, code)
	require.True(t, IsBusinessGroup("34020000002150000001"))
	require.False(t, IsBusinessGroup("34020000002160000001"))
	require.True(t, IsVirtualOrg("34020000002160000001"))
	require.False(t, IsVirtualOrg("not-a-code"))
}

func TestPTZCommandBuilders(t *testing.T) {
	require.Empty(t, BuildContinuousMove("diagonal", 0.5))
	for _, direction := range []string{"up", "down", "left", "right", "upleft", "upright", "downleft", "downright", "zoomin", "zoomout"} {
		cmd := BuildContinuousMove(direction, 0.5)
		require.Len(t, cmd, 16, direction)
		raw, err := hex.DecodeString(cmd)
		require.NoError(t, err)
		require.Equal(t, byte(0xA5), raw[0])
		sum := byte(0)
		for _, b := range raw[:7] {
			sum += b
		}
		require.Equal(t, sum, raw[7], direction)
	}
	stop := BuildStop()
	require.Len(t, stop, 16)

	pos := BuildSetPositionCmd(-10, 200, 0)
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_SET_POSITION, 0, 255, 0), pos)
	overflow := BuildSetPositionCmd(720, 45, 200)
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_SET_POSITION, 255, 128, 255>>4), overflow)
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_QUERY_POSITION, 0, 0, 0), BuildQueryPositionCmd())
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_SET_PRESET, 1, 1, 0), BuildSetPresetCmd(0))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_CALL_PRESET, 1, 255, 0), BuildCallPresetCmd(300))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_DELETE_PRESET, 1, 7, 0), BuildDeletePresetCmd(7))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_CRUISE_ADD_POINT, 0, 1, 0), BuildCruiseAddPointCmd(-1, 0))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_CRUISE_DELETE, 255, 255, 0), BuildCruiseDeletePointCmd(300, 400))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_CRUISE_START, 3, 0, 0), BuildCruiseStartCmd(3))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_CRUISE_STOP, 3, 0, 0), BuildCruiseStopCmd(3))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_SCAN_SET_LEFT, 2, 1, 0), BuildScanSetLeftCmd(2))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_SCAN_SET_RIGHT, 2, 2, 0), BuildScanSetRightCmd(2))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_SCAN_START, 2, 0, 0), BuildScanStartCmd(2))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_SCAN_STOP, 2, 0, 0), BuildScanStopCmd(2))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_CRUISE_SPEED, 1, 0xFF, 0x0F), BuildCruiseSpeedCmd(1, 5000))
	require.Equal(t, BuildFrontEndCmdString(PTZ_CMD_SCAN_SET_SPEED, 4, 1, 0), BuildScanSetSpeedCmd(4, 0))
}

func TestStreamHelpers(t *testing.T) {
	require.Equal(t, "dev:ch", StreamID("dev", "ch"))
	require.Equal(t, "play:dev:ch", playStreamKey("dev", "ch"))
	mgr := &streamsManager{}
	require.False(t, mgr.isPlaying("dev", "ch"))
	require.False(t, mgr.IsStreamPlaying("dev:ch"))
	stream := &Streams{DeviceID: "dev", ChannelID: "ch", SSRC: "ssrc"}
	mgr.storeStream(playStreamKey("dev", "ch"), stream)
	require.True(t, mgr.isPlaying("dev", "ch"))
	require.True(t, mgr.IsStreamPlaying("dev:ch"))
	got, ok := mgr.deleteStream(playStreamKey("dev", "ch"))
	require.True(t, ok)
	require.Equal(t, stream, got)
	_, ok = mgr.deleteStream(playStreamKey("dev", "ch"))
	require.False(t, ok)
	ssrc := mgr.getSSRC("3402000000")
	require.Regexp(t, `^020000[0-9]{4}$`, ssrc)
}

func TestXMLUnmarshalUTF8AndRetry(t *testing.T) {
	var got struct {
		XMLName xml.Name `xml:"Notify"`
		CmdType string   `xml:"CmdType"`
	}
	require.NoError(t, xmlUnmarshal([]byte(`<?xml version="1.0"?><Notify><CmdType>Keepalive</CmdType></Notify>`), &got))
	require.Equal(t, "Keepalive", got.CmdType)
	require.Error(t, xmlUnmarshal([]byte("<not-xml"), &got))
	require.InDelta(t, 5, randInt(5, 5), 0)
	require.Equal(t, int64(7), randInt64(7, 7))
}
