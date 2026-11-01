package onvif

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetVideoSourcesAndConfigurations(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetVideoSourceConfigurations"):
			return soapOK(`<GetVideoSourceConfigurationsResponse>
				<Configurations token="vsc0">
					<Name>VS</Name>
					<UseCount>1</UseCount>
					<SourceToken>vs0</SourceToken>
					<Bounds x="0" y="0" width="1920" height="1080"/>
				</Configurations>
			</GetVideoSourceConfigurationsResponse>`)
		case strings.Contains(body, "GetVideoSources"):
			return soapOK(`<GetVideoSourcesResponse>
				<VideoSources token="vs0">
					<Framerate>25</Framerate>
					<Resolution><Width>1920</Width><Height>1080</Height></Resolution>
				</VideoSources>
			</GetVideoSourcesResponse>`)
		default:
			return ""
		}
	})
	svc := NewMediaService(serviceClient(t, server, true, false, false, false))
	ctx := context.Background()
	sources, err := svc.GetVideoSources(ctx)
	require.NoError(t, err)
	require.Equal(t, "vs0", sources[0].Token)
	require.Equal(t, 25.0, sources[0].Framerate)
	require.Equal(t, 1920, sources[0].Resolution.Width)

	cfgs, err := svc.GetVideoSourceConfigurations(ctx)
	require.NoError(t, err)
	require.Equal(t, "vsc0", cfgs[0].Token)
	require.Equal(t, "vs0", cfgs[0].SourceToken)
	require.Equal(t, 1920, cfgs[0].Bounds.Width)
}

func TestGetSetVideoEncoderConfigurations(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "SetVideoEncoderConfiguration"):
			require.Contains(t, body, `token="ve0"`)
			require.Contains(t, body, "<Encoding>H264</Encoding>")
			require.Contains(t, body, "<FrameRateLimit>25</FrameRateLimit>")
			return soapOK(`<SetVideoEncoderConfigurationResponse/>`)
		case strings.Contains(body, "GetVideoEncoderConfigurationOptions"):
			require.Contains(t, body, "<ProfileToken>Profile_1</ProfileToken>")
			return soapOK(`<GetVideoEncoderConfigurationOptionsResponse>
				<Options>
					<QualityRange><Min>1</Min><Max>10</Max></QualityRange>
					<ResolutionsAvailable><Width>1920</Width><Height>1080</Height></ResolutionsAvailable>
					<ResolutionsAvailable><Width>1280</Width><Height>720</Height></ResolutionsAvailable>
					<FrameRateRange><Min>1</Min><Max>30</Max></FrameRateRange>
					<EncodingIntervalRange><Min>1</Min><Max>5</Max></EncodingIntervalRange>
				</Options>
			</GetVideoEncoderConfigurationOptionsResponse>`)
		case strings.Contains(body, "GetVideoEncoderConfigurations"):
			return soapOK(`<GetVideoEncoderConfigurationsResponse>
				<Configurations token="ve0">
					<Name>Main</Name>
					<UseCount>1</UseCount>
					<Encoding>H264</Encoding>
					<Resolution><Width>1920</Width><Height>1080</Height></Resolution>
					<Quality>5</Quality>
					<RateControl>
						<FrameRateLimit>25</FrameRateLimit>
						<BitrateLimit>4096</BitrateLimit>
					</RateControl>
					<H264>
						<GovLength>50</GovLength>
						<H264Profile>High</H264Profile>
					</H264>
				</Configurations>
			</GetVideoEncoderConfigurationsResponse>`)
		default:
			return ""
		}
	})
	svc := NewMediaService(serviceClient(t, server, true, false, false, false))
	ctx := context.Background()
	cfgs, err := svc.GetVideoEncoderConfigurations(ctx)
	require.NoError(t, err)
	require.Equal(t, "ve0", cfgs[0].Token)
	require.Equal(t, "H264", cfgs[0].Encoding)
	require.Equal(t, 5, cfgs[0].Quality)
	require.Equal(t, 25, cfgs[0].FramerateLimit)
	require.Equal(t, 50, cfgs[0].GovLength)
	require.Equal(t, "High", cfgs[0].Profile)

	opts, err := svc.GetVideoEncoderConfigurationOptions(ctx, "Profile_1")
	require.NoError(t, err)
	require.Equal(t, Range{Min: 1, Max: 10}, opts["quality_range"])
	require.Equal(t, []Resolution{{Width: 1920, Height: 1080}, {Width: 1280, Height: 720}}, opts["resolutions"])
	require.NoError(t, svc.SetVideoEncoderConfiguration(ctx, cfgs[0]))
}

func TestGetVideoEncoderConfigurationOptionsNoToken(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		require.NotContains(t, body, "ProfileToken")
		return soapOK(`<GetVideoEncoderConfigurationOptionsResponse>
			<Options>
				<QualityRange><Min>0</Min><Max>1</Max></QualityRange>
			</Options>
		</GetVideoEncoderConfigurationOptionsResponse>`)
	})
	opts, err := NewMediaService(serviceClient(t, server, true, false, false, false)).GetVideoEncoderConfigurationOptions(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, Range{Min: 0, Max: 1}, opts["quality_range"])
}

func TestAudioSourcesEncodersAndOutputs(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetAudioSourceConfigurations"):
			return soapOK(`<GetAudioSourceConfigurationsResponse>
				<Configurations token="asc0">
					<Name>Mic</Name>
					<UseCount>1</UseCount>
					<SourceToken>as0</SourceToken>
				</Configurations>
			</GetAudioSourceConfigurationsResponse>`)
		case strings.Contains(body, "GetAudioSources"):
			return soapOK(`<GetAudioSourcesResponse><AudioSources token="as0"/></GetAudioSourcesResponse>`)
		case strings.Contains(body, "GetAudioEncoderConfigurationOptions"):
			return soapOK(`<GetAudioEncoderConfigurationOptionsResponse>
				<Options>
					<Encoding>G711</Encoding>
					<BitrateList>64</BitrateList>
					<BitrateList>128</BitrateList>
					<SampleRateList>8</SampleRateList>
				</Options>
			</GetAudioEncoderConfigurationOptionsResponse>`)
		case strings.Contains(body, "GetAudioEncoderConfigurations"):
			return soapOK(`<GetAudioEncoderConfigurationsResponse>
				<Configurations token="ae0">
					<Name>Audio</Name>
					<UseCount>1</UseCount>
					<Encoding>G711</Encoding>
					<BitrateList>64</BitrateList>
					<SampleRateList>8</SampleRateList>
				</Configurations>
			</GetAudioEncoderConfigurationsResponse>`)
		case strings.Contains(body, "GetAudioOutputConfigurations"):
			return soapOK(`<GetAudioOutputConfigurationsResponse>
				<Configurations token="aoc0">
					<Name>Speaker</Name>
					<UseCount>1</UseCount>
					<OutputToken>ao0</OutputToken>
				</Configurations>
			</GetAudioOutputConfigurationsResponse>`)
		case strings.Contains(body, "GetAudioOutputs"):
			return soapOK(`<GetAudioOutputsResponse><AudioOutputs token="ao0"/></GetAudioOutputsResponse>`)
		default:
			return ""
		}
	})
	svc := NewMediaService(serviceClient(t, server, true, false, false, false))
	ctx := context.Background()

	sources, err := svc.GetAudioSources(ctx)
	require.NoError(t, err)
	require.Equal(t, "as0", sources[0].Token)
	asc, err := svc.GetAudioSourceConfigurations(ctx)
	require.NoError(t, err)
	require.Equal(t, "asc0", asc[0].Token)

	aec, err := svc.GetAudioEncoderConfigurations(ctx)
	require.NoError(t, err)
	require.Equal(t, "G711", aec[0].Encoding)
	require.Equal(t, 64, aec[0].Bitrate)
	opts, err := svc.GetAudioEncoderConfigurationOptions(ctx)
	require.NoError(t, err)
	require.Equal(t, "G711", opts["encoding"])
	require.Equal(t, []int{64, 128}, opts["bitrates"])

	outs, err := svc.GetAudioOutputs(ctx)
	require.NoError(t, err)
	require.Equal(t, "ao0", outs[0].Token)
	aoc, err := svc.GetAudioOutputConfigurations(ctx)
	require.NoError(t, err)
	require.Equal(t, "Speaker", aoc[0].Name)
}

func TestCreateDeleteProfileAndConfigs(t *testing.T) {
	var ops []string
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "CreateProfile"):
			ops = append(ops, "CreateProfile")
			require.Contains(t, body, "<Name>Custom</Name>")
			return soapOK(`<CreateProfileResponse><Profile token="p-new"><Name>Custom</Name></Profile></CreateProfileResponse>`)
		case strings.Contains(body, "DeleteProfile"):
			ops = append(ops, "DeleteProfile")
			require.Contains(t, body, "<ProfileToken>p-new</ProfileToken>")
			return soapOK(`<DeleteProfileResponse/>`)
		case strings.Contains(body, "AddVideoSourceConfiguration"):
			ops = append(ops, "AddVideoSourceConfiguration")
			return soapOK(`<AddVideoSourceConfigurationResponse/>`)
		case strings.Contains(body, "AddVideoEncoderConfiguration"):
			ops = append(ops, "AddVideoEncoderConfiguration")
			return soapOK(`<AddVideoEncoderConfigurationResponse/>`)
		case strings.Contains(body, "AddAudioSourceConfiguration"):
			ops = append(ops, "AddAudioSourceConfiguration")
			return soapOK(`<AddAudioSourceConfigurationResponse/>`)
		case strings.Contains(body, "AddAudioEncoderConfiguration"):
			ops = append(ops, "AddAudioEncoderConfiguration")
			return soapOK(`<AddAudioEncoderConfigurationResponse/>`)
		case strings.Contains(body, "RemoveVideoSourceConfiguration"):
			ops = append(ops, "RemoveVideoSourceConfiguration")
			return soapOK(`<RemoveVideoSourceConfigurationResponse/>`)
		case strings.Contains(body, "RemoveVideoEncoderConfiguration"):
			ops = append(ops, "RemoveVideoEncoderConfiguration")
			return soapOK(`<RemoveVideoEncoderConfigurationResponse/>`)
		case strings.Contains(body, "RemoveAudioSourceConfiguration"):
			ops = append(ops, "RemoveAudioSourceConfiguration")
			return soapOK(`<RemoveAudioSourceConfigurationResponse/>`)
		case strings.Contains(body, "RemoveAudioEncoderConfiguration"):
			ops = append(ops, "RemoveAudioEncoderConfiguration")
			return soapOK(`<RemoveAudioEncoderConfigurationResponse/>`)
		case strings.Contains(body, "SetSynchronizationPoint"):
			ops = append(ops, "SetSynchronizationPoint")
			require.Contains(t, body, "<ProfileToken>p-new</ProfileToken>")
			return soapOK(`<SetSynchronizationPointResponse/>`)
		default:
			return ""
		}
	})
	svc := NewMediaService(serviceClient(t, server, true, false, false, false))
	ctx := context.Background()
	profile, err := svc.CreateProfile(ctx, "Custom")
	require.NoError(t, err)
	require.Equal(t, "p-new", profile.Token)
	require.Equal(t, "Custom", profile.Name)
	require.NoError(t, svc.AddVideoSourceConfiguration(ctx, "p-new", "vsc0"))
	require.NoError(t, svc.AddVideoEncoderConfiguration(ctx, "p-new", "ve0"))
	require.NoError(t, svc.AddAudioSourceConfiguration(ctx, "p-new", "asc0"))
	require.NoError(t, svc.AddAudioEncoderConfiguration(ctx, "p-new", "ae0"))
	require.NoError(t, svc.SetSynchronizationPoint(ctx, "p-new"))
	require.NoError(t, svc.RemoveVideoSourceConfiguration(ctx, "p-new", "vsc0"))
	require.NoError(t, svc.RemoveVideoEncoderConfiguration(ctx, "p-new", "ve0"))
	require.NoError(t, svc.RemoveAudioSourceConfiguration(ctx, "p-new", "asc0"))
	require.NoError(t, svc.RemoveAudioEncoderConfiguration(ctx, "p-new", "ae0"))
	require.NoError(t, svc.DeleteProfile(ctx, "p-new"))
	require.Equal(t, []string{
		"CreateProfile",
		"AddVideoSourceConfiguration",
		"AddVideoEncoderConfiguration",
		"AddAudioSourceConfiguration",
		"AddAudioEncoderConfiguration",
		"SetSynchronizationPoint",
		"RemoveVideoSourceConfiguration",
		"RemoveVideoEncoderConfiguration",
		"RemoveAudioSourceConfiguration",
		"RemoveAudioEncoderConfiguration",
		"DeleteProfile",
	}, ops)
}

func TestGetMediaServiceCapabilities(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetServiceCapabilitiesResponse>
			<Capabilities SnapshotUri="true" Rotation="false" VideoSourceMode="true" OSD="true" TemporaryOSDText="false" EXICompression="true"/>
		</GetServiceCapabilitiesResponse>`)
	})
	caps, err := NewMediaService(serviceClient(t, server, true, false, false, false)).GetMediaServiceCapabilities(context.Background())
	require.NoError(t, err)
	require.True(t, caps["snapshot_uri"])
	require.False(t, caps["rotation"])
	require.True(t, caps["osd"])
	require.True(t, caps["exi_compression"])
}

func TestMediaConfigSOAPAndParseErrors(t *testing.T) {
	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "denied")
	})
	svc := NewMediaService(serviceClient(t, fault, true, false, false, false))
	ctx := context.Background()
	_, err := svc.GetVideoSources(ctx)
	require.ErrorContains(t, err, "GetVideoSources failed")
	_, err = svc.GetVideoSourceConfigurations(ctx)
	require.ErrorContains(t, err, "GetVideoSourceConfigurations failed")
	_, err = svc.GetVideoEncoderConfigurations(ctx)
	require.ErrorContains(t, err, "GetVideoEncoderConfigurations failed")
	_, err = svc.GetVideoEncoderConfigurationOptions(ctx, "")
	require.ErrorContains(t, err, "GetVideoEncoderConfigurationOptions failed")
	require.ErrorContains(t, svc.SetVideoEncoderConfiguration(ctx, VideoEncoderConfiguration{}), "SetVideoEncoderConfiguration failed")
	_, err = svc.GetAudioSources(ctx)
	require.ErrorContains(t, err, "GetAudioSources failed")
	_, err = svc.GetAudioSourceConfigurations(ctx)
	require.ErrorContains(t, err, "GetAudioSourceConfigurations failed")
	_, err = svc.GetAudioEncoderConfigurations(ctx)
	require.ErrorContains(t, err, "GetAudioEncoderConfigurations failed")
	_, err = svc.GetAudioEncoderConfigurationOptions(ctx)
	require.ErrorContains(t, err, "GetAudioEncoderConfigurationOptions failed")
	_, err = svc.GetAudioOutputs(ctx)
	require.ErrorContains(t, err, "GetAudioOutputs failed")
	_, err = svc.GetAudioOutputConfigurations(ctx)
	require.ErrorContains(t, err, "GetAudioOutputConfigurations failed")
	_, err = svc.CreateProfile(ctx, "x")
	require.ErrorContains(t, err, "CreateProfile failed")
	require.ErrorContains(t, svc.DeleteProfile(ctx, "x"), "DeleteProfile failed")
	require.Error(t, svc.AddVideoSourceConfiguration(ctx, "p", "c"))
	require.Error(t, svc.RemoveVideoEncoderConfiguration(ctx, "p", "c"))
	require.Error(t, svc.SetSynchronizationPoint(ctx, "p"))
	_, err = svc.GetMediaServiceCapabilities(ctx)
	require.ErrorContains(t, err, "GetMediaServiceCapabilities failed")

	parseCalls := map[string]func(*MediaService) error{
		"GetVideoSources": func(s *MediaService) error { _, err := s.GetVideoSources(ctx); return err },
		"GetVideoSourceConfigurations": func(s *MediaService) error {
			_, err := s.GetVideoSourceConfigurations(ctx)
			return err
		},
		"GetVideoEncoderConfigurations": func(s *MediaService) error {
			_, err := s.GetVideoEncoderConfigurations(ctx)
			return err
		},
		"GetVideoEncoderConfigurationOptions": func(s *MediaService) error {
			_, err := s.GetVideoEncoderConfigurationOptions(ctx, "")
			return err
		},
		"GetAudioSources": func(s *MediaService) error { _, err := s.GetAudioSources(ctx); return err },
		"GetAudioSourceConfigurations": func(s *MediaService) error {
			_, err := s.GetAudioSourceConfigurations(ctx)
			return err
		},
		"GetAudioEncoderConfigurations": func(s *MediaService) error {
			_, err := s.GetAudioEncoderConfigurations(ctx)
			return err
		},
		"GetAudioEncoderConfigurationOptions": func(s *MediaService) error {
			_, err := s.GetAudioEncoderConfigurationOptions(ctx)
			return err
		},
		"GetAudioOutputs": func(s *MediaService) error { _, err := s.GetAudioOutputs(ctx); return err },
		"GetAudioOutputConfigurations": func(s *MediaService) error {
			_, err := s.GetAudioOutputConfigurations(ctx)
			return err
		},
		"CreateProfile": func(s *MediaService) error { _, err := s.CreateProfile(ctx, "x"); return err },
		"GetMediaServiceCapabilities": func(s *MediaService) error {
			_, err := s.GetMediaServiceCapabilities(ctx)
			return err
		},
	}
	for name, call := range parseCalls {
		name, call := name, call
		t.Run("parse_"+name, func(t *testing.T) {
			srv := newONVIFDeviceServer(t, func(body string) string {
				return soapOK(`<` + name + `Response><`)
			})
			require.Error(t, call(NewMediaService(serviceClient(t, srv, true, false, false, false))))
		})
	}
}
