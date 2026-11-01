package onvif

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPTZNotAvailable(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string { return "" })
	client := serviceClient(t, server, false, false, false, false)
	svc := NewPTZService(client)
	ctx := context.Background()

	_, err := svc.GetStatus(ctx, "p")
	require.ErrorContains(t, err, "PTZ service not available")
	require.ErrorContains(t, svc.ContinuousMove(ctx, "p", PTZVelocity{}), "PTZ service not available")
	require.ErrorContains(t, svc.Stop(ctx, "p", true, true), "PTZ service not available")
	require.ErrorContains(t, svc.AbsoluteMove(ctx, "p", PTZPosition{}), "PTZ service not available")
	require.ErrorContains(t, svc.RelativeMove(ctx, "p", PTZPosition{}), "PTZ service not available")
	_, err = svc.GetPresets(ctx, "p")
	require.ErrorContains(t, err, "PTZ service not available")
	require.ErrorContains(t, svc.GotoPreset(ctx, "p", "pre"), "PTZ service not available")
	_, err = svc.SetPreset(ctx, "p", "Door")
	require.ErrorContains(t, err, "PTZ service not available")
	require.ErrorContains(t, svc.RemovePreset(ctx, "p", "pre"), "PTZ service not available")
	require.ErrorContains(t, svc.GotoHomePosition(ctx, "p"), "PTZ service not available")
	require.ErrorContains(t, svc.SetHomePosition(ctx, "p"), "PTZ service not available")
}

func TestPTZGetStatus(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		require.Contains(t, body, "GetStatus")
		require.Contains(t, body, "Profile_1")
		return soapOK(`<GetStatusResponse>
			<Status>
				<Position>
					<PanTilt x="0.5" y="-0.25"/>
					<Zoom x="0.1"/>
				</Position>
				<MoveStatus>
					<PanTilt>MOVING</PanTilt>
					<Zoom>IDLE</Zoom>
				</MoveStatus>
				<Error></Error>
			</Status>
		</GetStatusResponse>`)
	})
	client := serviceClient(t, server, false, false, true, false)
	status, err := NewPTZService(client).GetStatus(context.Background(), "Profile_1")
	require.NoError(t, err)
	require.Equal(t, 0.5, status.Position.PanTilt.X)
	require.Equal(t, -0.25, status.Position.PanTilt.Y)
	require.Equal(t, 0.1, status.Position.Zoom.X)
	require.True(t, status.Moving)
	require.Empty(t, status.Error)
}

func TestPTZGetStatusIdleZoomMoving(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetStatusResponse>
			<Status>
				<Position>
					<PanTilt x="0" y="0"/>
					<Zoom x="1"/>
				</Position>
				<MoveStatus>
					<PanTilt>IDLE</PanTilt>
					<Zoom>MOVING</Zoom>
				</MoveStatus>
				<Error>stall</Error>
			</Status>
		</GetStatusResponse>`)
	})
	client := serviceClient(t, server, false, false, true, false)
	status, err := NewPTZService(client).GetStatus(context.Background(), "p")
	require.NoError(t, err)
	require.True(t, status.Moving)
	require.Equal(t, "stall", status.Error)
}

func TestPTZGetStatusSOAPAndParseError(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "denied")
	})
	client := serviceClient(t, server, false, false, true, false)
	_, err := NewPTZService(client).GetStatus(context.Background(), "p")
	require.ErrorContains(t, err, "GetStatus failed")

	parseServer := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetStatusResponse><`)
	})
	parseClient := serviceClient(t, parseServer, false, false, true, false)
	_, err = NewPTZService(parseClient).GetStatus(context.Background(), "p")
	require.Error(t, err)
}

func TestPTZContinuousMoveStopAbsoluteRelative(t *testing.T) {
	var seen []string
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "ContinuousMove"):
			seen = append(seen, "ContinuousMove")
			require.Contains(t, body, `x="0.200000"`)
			require.Contains(t, body, `y="-0.100000"`)
			return soapOK(`<ContinuousMoveResponse/>`)
		case strings.Contains(body, "<Stop "):
			seen = append(seen, "Stop")
			require.Contains(t, body, "<PanTilt>true</PanTilt>")
			require.Contains(t, body, "<Zoom>false</Zoom>")
			return soapOK(`<StopResponse/>`)
		case strings.Contains(body, "AbsoluteMove"):
			seen = append(seen, "AbsoluteMove")
			return soapOK(`<AbsoluteMoveResponse/>`)
		case strings.Contains(body, "RelativeMove"):
			seen = append(seen, "RelativeMove")
			return soapOK(`<RelativeMoveResponse/>`)
		default:
			return ""
		}
	})
	client := serviceClient(t, server, false, false, true, false)
	svc := NewPTZService(client)
	ctx := context.Background()

	require.NoError(t, svc.ContinuousMove(ctx, "p", PTZVelocity{
		PanTilt: Vector2D{X: 0.2, Y: -0.1},
		Zoom:    Vector1D{X: 0.05},
	}))
	require.NoError(t, svc.Stop(ctx, "p", true, false))
	require.NoError(t, svc.AbsoluteMove(ctx, "p", PTZPosition{
		PanTilt: Vector2D{X: 0.4, Y: 0.1},
		Zoom:    Vector1D{X: 0.2},
	}))
	require.NoError(t, svc.RelativeMove(ctx, "p", PTZPosition{
		PanTilt: Vector2D{X: -0.1, Y: 0.1},
		Zoom:    Vector1D{X: 0},
	}))
	require.Equal(t, []string{"ContinuousMove", "Stop", "AbsoluteMove", "RelativeMove"}, seen)
}

func TestPTZMoveSOAPErrors(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "busy")
	})
	client := serviceClient(t, server, false, false, true, false)
	svc := NewPTZService(client)
	ctx := context.Background()
	require.ErrorContains(t, svc.ContinuousMove(ctx, "p", PTZVelocity{}), "ContinuousMove failed")
	require.ErrorContains(t, svc.Stop(ctx, "p", true, true), "Stop failed")
	require.ErrorContains(t, svc.AbsoluteMove(ctx, "p", PTZPosition{}), "AbsoluteMove failed")
	require.ErrorContains(t, svc.RelativeMove(ctx, "p", PTZPosition{}), "RelativeMove failed")
}

func TestPTZPresets(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetPresets"):
			return soapOK(`<GetPresetsResponse>
				<Preset token="preset1">
					<Name>Door</Name>
					<PTZPosition>
						<PanTilt x="0.1" y="0.2"/>
						<Zoom x="0.3"/>
					</PTZPosition>
				</Preset>
			</GetPresetsResponse>`)
		case strings.Contains(body, "SetPreset"):
			require.Contains(t, body, "<PresetName>Window</PresetName>")
			return soapOK(`<SetPresetResponse><PresetToken>preset-new</PresetToken></SetPresetResponse>`)
		case strings.Contains(body, "GotoPreset"):
			require.Contains(t, body, "<PresetToken>preset1</PresetToken>")
			return soapOK(`<GotoPresetResponse/>`)
		case strings.Contains(body, "RemovePreset"):
			return soapOK(`<RemovePresetResponse/>`)
		case strings.Contains(body, "GotoHomePosition"):
			return soapOK(`<GotoHomePositionResponse/>`)
		case strings.Contains(body, "SetHomePosition"):
			return soapOK(`<SetHomePositionResponse/>`)
		default:
			return ""
		}
	})
	client := serviceClient(t, server, false, false, true, false)
	svc := NewPTZService(client)
	ctx := context.Background()

	presets, err := svc.GetPresets(ctx, "p")
	require.NoError(t, err)
	require.Len(t, presets, 1)
	require.Equal(t, "preset1", presets[0].Token)
	require.Equal(t, "Door", presets[0].Name)
	require.Equal(t, 0.1, presets[0].Position.PanTilt.X)
	require.Equal(t, 0.2, presets[0].Position.PanTilt.Y)
	require.Equal(t, 0.3, presets[0].Position.Zoom.X)

	token, err := svc.SetPreset(ctx, "p", "Window")
	require.NoError(t, err)
	require.Equal(t, "preset-new", token)
	require.NoError(t, svc.GotoPreset(ctx, "p", "preset1"))
	require.NoError(t, svc.RemovePreset(ctx, "p", "preset1"))
	require.NoError(t, svc.GotoHomePosition(ctx, "p"))
	require.NoError(t, svc.SetHomePosition(ctx, "p"))
}

func TestPTZPresetSOAPAndParseErrors(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Sender", "no")
	})
	client := serviceClient(t, server, false, false, true, false)
	svc := NewPTZService(client)
	ctx := context.Background()
	_, err := svc.GetPresets(ctx, "p")
	require.ErrorContains(t, err, "GetPresets failed")
	require.ErrorContains(t, svc.GotoPreset(ctx, "p", "x"), "GotoPreset failed")
	_, err = svc.SetPreset(ctx, "p", "n")
	require.ErrorContains(t, err, "SetPreset failed")
	require.ErrorContains(t, svc.RemovePreset(ctx, "p", "x"), "RemovePreset failed")
	require.ErrorContains(t, svc.GotoHomePosition(ctx, "p"), "GotoHomePosition failed")
	require.ErrorContains(t, svc.SetHomePosition(ctx, "p"), "SetHomePosition failed")

	parseServer := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetPresetsResponse><`)
	})
	parseClient := serviceClient(t, parseServer, false, false, true, false)
	_, err = NewPTZService(parseClient).GetPresets(ctx, "p")
	require.Error(t, err)

	setParse := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<SetPresetResponse><`)
	})
	_, err = NewPTZService(serviceClient(t, setParse, false, false, true, false)).SetPreset(ctx, "p", "n")
	require.Error(t, err)
}

func TestPTZNodesConfigurationsAux(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetNodes"):
			return soapOK(`<GetNodesResponse>
				<PTZNode token="node1">
					<Name>PTZ</Name>
					<FixedHomePosition>true</FixedHomePosition>
					<GeoMove>false</GeoMove>
					<MaximumNumberOfPresets>8</MaximumNumberOfPresets>
					<HomeSupported>true</HomeSupported>
				</PTZNode>
			</GetNodesResponse>`)
		case strings.Contains(body, "GetConfigurations"):
			return soapOK(`<GetConfigurationsResponse>
				<PTZConfiguration token="cfg1">
					<Name>Default</Name>
					<UseCount>1</UseCount>
					<NodeToken>node1</NodeToken>
					<DefaultAbsolutePantTiltPositionSpace>abs-pt</DefaultAbsolutePantTiltPositionSpace>
					<DefaultAbsoluteZoomPositionSpace>abs-z</DefaultAbsoluteZoomPositionSpace>
					<DefaultContinuousPanTiltVelocitySpace>cont-pt</DefaultContinuousPanTiltVelocitySpace>
					<DefaultContinuousZoomVelocitySpace>cont-z</DefaultContinuousZoomVelocitySpace>
					<DefaultPTZTimeout>PT5S</DefaultPTZTimeout>
				</PTZConfiguration>
			</GetConfigurationsResponse>`)
		case strings.Contains(body, "GetConfigurationOptions"):
			return soapOK(`<GetConfigurationOptionsResponse>
				<Options>
					<Spaces>
						<AbsolutePanTiltPositionSpace>
							<URI>abs-pt</URI>
							<XRange><Min>-1</Min><Max>1</Max></XRange>
							<YRange><Min>-1</Min><Max>1</Max></YRange>
						</AbsolutePanTiltPositionSpace>
						<AbsoluteZoomPositionSpace>
							<URI>abs-z</URI>
							<XRange><Min>0</Min><Max>1</Max></XRange>
						</AbsoluteZoomPositionSpace>
						<ContinuousPanTiltVelocitySpace>
							<URI>cont-pt</URI>
							<XRange><Min>-1</Min><Max>1</Max></XRange>
							<YRange><Min>-1</Min><Max>1</Max></YRange>
						</ContinuousPanTiltVelocitySpace>
						<ContinuousZoomVelocitySpace>
							<URI>cont-z</URI>
							<XRange><Min>-1</Min><Max>1</Max></XRange>
						</ContinuousZoomVelocitySpace>
					</Spaces>
					<PTZTimeout><Min>PT1S</Min><Max>PT60S</Max></PTZTimeout>
				</Options>
			</GetConfigurationOptionsResponse>`)
		case strings.Contains(body, "PTZSendAuxiliaryCommand"):
			return soapOK(`<PTZSendAuxiliaryCommandResponse><AuxiliaryData>ok</AuxiliaryData></PTZSendAuxiliaryCommandResponse>`)
		default:
			return ""
		}
	})
	client := serviceClient(t, server, false, false, true, false)
	svc := NewPTZService(client)
	ctx := context.Background()

	nodes, err := svc.GetPTZNodes(ctx)
	require.NoError(t, err)
	require.Equal(t, "node1", nodes[0].Token)
	require.Equal(t, "PTZ", nodes[0].Name)
	require.True(t, nodes[0].FixedHomePosition)
	require.Equal(t, 8, nodes[0].MaximumNumberOfPresets)
	require.True(t, nodes[0].HomeSupported)

	cfgs, err := svc.GetPTZConfigurations(ctx)
	require.NoError(t, err)
	require.Equal(t, "cfg1", cfgs[0].Token)
	require.Equal(t, "node1", cfgs[0].NodeToken)
	require.Equal(t, "PT5S", cfgs[0].DefaultPTZTimeout)

	opts, err := svc.GetPTZConfigurationOptions(ctx, "cfg1")
	require.NoError(t, err)
	require.Equal(t, "abs-pt", opts.Spaces.AbsolutePanTiltPositionSpace[0].URI)
	require.Equal(t, -1.0, opts.Spaces.AbsolutePanTiltPositionSpace[0].XRange.Min)
	require.Equal(t, "abs-z", opts.Spaces.AbsoluteZoomPositionSpace[0].URI)
	require.Equal(t, "cont-pt", opts.Spaces.ContinuousPanTiltVelocitySpace[0].URI)
	require.Equal(t, "cont-z", opts.Spaces.ContinuousZoomVelocitySpace[0].URI)

	aux, err := svc.PTZSendAuxiliaryCommand(ctx, "p", "IROn")
	require.NoError(t, err)
	require.Equal(t, "ok", aux)
}

func TestPTZConfigUnavailableAndErrors(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string { return "" })
	none := NewPTZService(serviceClient(t, server, false, false, false, false))
	ctx := context.Background()
	_, err := none.GetPTZNodes(ctx)
	require.ErrorContains(t, err, "PTZ service not available")
	_, err = none.GetPTZConfigurations(ctx)
	require.ErrorContains(t, err, "PTZ service not available")
	_, err = none.GetPTZConfigurationOptions(ctx, "c")
	require.ErrorContains(t, err, "PTZ service not available")
	_, err = none.PTZSendAuxiliaryCommand(ctx, "p", "x")
	require.ErrorContains(t, err, "PTZ service not available")

	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "fail")
	})
	svc := NewPTZService(serviceClient(t, fault, false, false, true, false))
	_, err = svc.GetPTZNodes(ctx)
	require.ErrorContains(t, err, "GetPTZNodes failed")
	_, err = svc.GetPTZConfigurations(ctx)
	require.ErrorContains(t, err, "GetPTZConfigurations failed")
	_, err = svc.GetPTZConfigurationOptions(ctx, "c")
	require.ErrorContains(t, err, "GetPTZConfigurationOptions failed")
	_, err = svc.PTZSendAuxiliaryCommand(ctx, "p", "x")
	require.ErrorContains(t, err, "PTZSendAuxiliaryCommand failed")

	bad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetNodesResponse><`)
	})
	_, err = NewPTZService(serviceClient(t, bad, false, false, true, false)).GetPTZNodes(ctx)
	require.Error(t, err)
}
