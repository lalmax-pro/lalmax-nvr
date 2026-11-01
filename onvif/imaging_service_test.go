package onvif

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImagingNotAvailable(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string { return "" })
	client := serviceClient(t, server, false, false, false, false)
	svc := NewImagingService(client)
	ctx := context.Background()

	_, err := svc.GetImagingSettings(ctx, "vs")
	require.ErrorContains(t, err, "Imaging service not available")
	require.ErrorContains(t, svc.SetImagingSettings(ctx, "vs", ImagingSettings{}), "Imaging service not available")
	_, err = svc.GetOptions(ctx, "vs")
	require.ErrorContains(t, err, "Imaging service not available")
	_, err = svc.GetMoveOptions(ctx, "vs")
	require.ErrorContains(t, err, "Imaging service not available")
	require.ErrorContains(t, svc.Move(ctx, "vs", 0.5), "Imaging service not available")
	require.ErrorContains(t, svc.Stop(ctx, "vs"), "Imaging service not available")
	_, err = svc.GetCurrentImagingPreset(ctx, "vs")
	require.ErrorContains(t, err, "Imaging service not available")
	require.ErrorContains(t, svc.SetCurrentImagingPreset(ctx, "vs", "p"), "Imaging service not available")
	_, err = svc.GetImagingPresets(ctx, "vs")
	require.ErrorContains(t, err, "Imaging service not available")
	_, err = svc.GetImagingStatus(ctx, "vs")
	require.ErrorContains(t, err, "Imaging service not available")
	_, err = svc.GetImagingServiceCapabilities(ctx)
	require.ErrorContains(t, err, "Imaging service not available")
}

func TestImagingSettingsAndOptions(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetImagingSettings"):
			return soapOK(`<GetImagingSettingsResponse>
				<ImagingSettings>
					<Brightness>50</Brightness>
					<Contrast>40</Contrast>
					<ColorSaturation>30</ColorSaturation>
					<Sharpness>20</Sharpness>
					<WhiteBalance><Mode>AUTO</Mode></WhiteBalance>
					<Focus><Mode>AUTO</Mode></Focus>
					<Exposure><Mode>MANUAL</Mode></Exposure>
				</ImagingSettings>
			</GetImagingSettingsResponse>`)
		case strings.Contains(body, "SetImagingSettings"):
			require.Contains(t, body, "<Brightness>55.000000</Brightness>")
			return soapOK(`<SetImagingSettingsResponse/>`)
		case strings.Contains(body, "GetOptions"):
			return soapOK(`<GetOptionsResponse>
				<ImagingOptions>
					<Brightness><Min>0</Min><Max>100</Max></Brightness>
					<Contrast><Min>1</Min><Max>99</Max></Contrast>
					<ColorSaturation><Min>2</Min><Max>98</Max></ColorSaturation>
					<Sharpness><Min>3</Min><Max>97</Max></Sharpness>
				</ImagingOptions>
			</GetOptionsResponse>`)
		default:
			return ""
		}
	})
	client := serviceClient(t, server, false, false, false, true)
	svc := NewImagingService(client)
	ctx := context.Background()

	settings, err := svc.GetImagingSettings(ctx, "vs0")
	require.NoError(t, err)
	require.Equal(t, 50.0, settings.Brightness)
	require.Equal(t, 40.0, settings.Contrast)
	require.Equal(t, 30.0, settings.ColorSaturation)
	require.Equal(t, 20.0, settings.Sharpness)
	require.Equal(t, "AUTO", settings.WhiteBalance)
	require.Equal(t, "AUTO", settings.FocusMode)
	require.Equal(t, "MANUAL", settings.ExposureMode)

	require.NoError(t, svc.SetImagingSettings(ctx, "vs0", ImagingSettings{
		Brightness: 55, Contrast: 40, ColorSaturation: 30, Sharpness: 20,
		WhiteBalance: "AUTO", FocusMode: "AUTO", ExposureMode: "MANUAL",
	}))

	opts, err := svc.GetOptions(ctx, "vs0")
	require.NoError(t, err)
	require.Equal(t, 0.0, opts.Brightness.Min)
	require.Equal(t, 100.0, opts.Brightness.Max)
	require.Equal(t, 1.0, opts.Contrast.Min)
	require.Equal(t, 2.0, opts.Saturation.Min)
	require.Equal(t, 3.0, opts.Sharpness.Min)
	require.Equal(t, 97.0, opts.Sharpness.Max)
}

func TestImagingMoveOptionsMoveStop(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetMoveOptions"):
			return soapOK(`<GetMoveOptionsResponse>
				<MoveOptions>
					<Absolute>
						<Position><Min>0</Min><Max>1</Max></Position>
						<Speed><Min>0.1</Min><Max>1</Max></Speed>
					</Absolute>
					<Relative><Min>-1</Min><Max>1</Max></Relative>
					<Continuous>
						<Speed><Min>-1</Min><Max>1</Max></Speed>
					</Continuous>
				</MoveOptions>
			</GetMoveOptionsResponse>`)
		case strings.Contains(body, "<Move "):
			require.Contains(t, body, "<Position>0.400000</Position>")
			return soapOK(`<MoveResponse/>`)
		case strings.Contains(body, "<Stop "):
			return soapOK(`<StopResponse/>`)
		default:
			return ""
		}
	})
	client := serviceClient(t, server, false, false, false, true)
	svc := NewImagingService(client)
	ctx := context.Background()

	opts, err := svc.GetMoveOptions(ctx, "vs0")
	require.NoError(t, err)
	abs := opts["absolute"].(map[string]interface{})
	pos := abs["position"].(map[string]interface{})
	require.Equal(t, 0.0, pos["min"])
	require.Equal(t, 1.0, pos["max"])
	rel := opts["relative"].(map[string]interface{})
	require.Equal(t, -1.0, rel["min"])
	cont := opts["continuous"].(map[string]interface{})
	spd := cont["speed"].(map[string]interface{})
	require.Equal(t, -1.0, spd["min"])

	require.NoError(t, svc.Move(ctx, "vs0", 0.4))
	require.NoError(t, svc.Stop(ctx, "vs0"))
}

func TestImagingSOAPErrors(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "img fail")
	})
	client := serviceClient(t, server, false, false, false, true)
	svc := NewImagingService(client)
	ctx := context.Background()
	_, err := svc.GetImagingSettings(ctx, "vs")
	require.ErrorContains(t, err, "GetImagingSettings failed")
	require.ErrorContains(t, svc.SetImagingSettings(ctx, "vs", ImagingSettings{}), "SetImagingSettings failed")
	_, err = svc.GetOptions(ctx, "vs")
	require.ErrorContains(t, err, "GetOptions failed")
	_, err = svc.GetMoveOptions(ctx, "vs")
	require.ErrorContains(t, err, "GetMoveOptions failed")
	require.ErrorContains(t, svc.Move(ctx, "vs", 1), "Move failed")
	require.ErrorContains(t, svc.Stop(ctx, "vs"), "Stop failed")

	bad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetImagingSettingsResponse><`)
	})
	_, err = NewImagingService(serviceClient(t, bad, false, false, false, true)).GetImagingSettings(ctx, "vs")
	require.Error(t, err)
}

func TestImagingPresetsAndStatus(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetCurrentPreset"):
			return soapOK(`<GetCurrentPresetResponse>
				<Preset token="night"><Name>Night</Name><Type>Night</Type></Preset>
			</GetCurrentPresetResponse>`)
		case strings.Contains(body, "SetCurrentPreset"):
			require.Contains(t, body, "<PresetToken>day</PresetToken>")
			return soapOK(`<SetCurrentPresetResponse/>`)
		case strings.Contains(body, "GetPresets"):
			return soapOK(`<GetPresetsResponse>
				<Preset token="day"><Name>Day</Name><Type>Day</Type></Preset>
				<Preset token="night"><Name>Night</Name><Type>Night</Type></Preset>
			</GetPresetsResponse>`)
		case strings.Contains(body, "GetStatus"):
			return soapOK(`<GetStatusResponse>
				<Status>
					<FocusStatus20>
						<Position>0.55</Position>
						<MoveStatus>IDLE</MoveStatus>
						<Error></Error>
					</FocusStatus20>
				</Status>
			</GetStatusResponse>`)
		case strings.Contains(body, "GetServiceCapabilities"):
			return soapOK(`<GetServiceCapabilitiesResponse>
				<Capabilities ImageStabilization="true" Preset="true"/>
			</GetServiceCapabilitiesResponse>`)
		default:
			return ""
		}
	})
	client := serviceClient(t, server, false, false, false, true)
	svc := NewImagingService(client)
	ctx := context.Background()

	cur, err := svc.GetCurrentImagingPreset(ctx, "vs0")
	require.NoError(t, err)
	require.Equal(t, "night", cur.Token)
	require.Equal(t, "Night", cur.Name)
	require.Equal(t, "Night", cur.Type)

	require.NoError(t, svc.SetCurrentImagingPreset(ctx, "vs0", "day"))

	presets, err := svc.GetImagingPresets(ctx, "vs0")
	require.NoError(t, err)
	require.Len(t, presets, 2)
	require.Equal(t, "day", presets[0].Token)

	st, err := svc.GetImagingStatus(ctx, "vs0")
	require.NoError(t, err)
	require.Equal(t, 0.55, st["position"])
	require.Equal(t, "IDLE", st["move_status"])

	caps, err := svc.GetImagingServiceCapabilities(ctx)
	require.NoError(t, err)
	require.True(t, caps["image_stabilization"])
	require.True(t, caps["preset"])
}

func TestImagingPresetSOAPErrors(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "no")
	})
	client := serviceClient(t, server, false, false, false, true)
	svc := NewImagingService(client)
	ctx := context.Background()
	_, err := svc.GetCurrentImagingPreset(ctx, "vs")
	require.ErrorContains(t, err, "GetCurrentImagingPreset failed")
	require.ErrorContains(t, svc.SetCurrentImagingPreset(ctx, "vs", "p"), "SetCurrentImagingPreset failed")
	_, err = svc.GetImagingPresets(ctx, "vs")
	require.ErrorContains(t, err, "GetImagingPresets failed")
	_, err = svc.GetImagingStatus(ctx, "vs")
	require.ErrorContains(t, err, "GetImagingStatus failed")
	_, err = svc.GetImagingServiceCapabilities(ctx)
	require.ErrorContains(t, err, "GetImagingServiceCapabilities failed")

	bad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetCurrentPresetResponse><`)
	})
	_, err = NewImagingService(serviceClient(t, bad, false, false, false, true)).GetCurrentImagingPreset(ctx, "vs")
	require.Error(t, err)
}
