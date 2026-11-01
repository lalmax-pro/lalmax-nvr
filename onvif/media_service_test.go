package onvif

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

// serviceClient wires discovered endpoints to the httptest server so SOAP
// calls stay local (no real cameras / internet).
func serviceClient(t *testing.T, server *httptest.Server, media, media2, ptz, imaging bool) *Client {
	t.Helper()
	client := newTestClient(t, server)
	eps := &ServiceEndpoints{}
	if media {
		eps.Media = mustParseURL(t, server.URL+"/onvif/media")
	}
	if media2 {
		eps.Media2 = mustParseURL(t, server.URL+"/onvif/media2")
	}
	if ptz {
		eps.PTZ = mustParseURL(t, server.URL+"/onvif/ptz")
	}
	if imaging {
		eps.Imaging = mustParseURL(t, server.URL+"/onvif/imaging")
	}
	client.endpoints = eps
	return client
}

func media1ProfilesXML() string {
	return soapOK(`<GetProfilesResponse>
		<Profiles token="Profile_1">
			<Name>MainStream</Name>
			<VideoSourceConfiguration token="vs0"/>
			<VideoEncoderConfiguration token="ve0">
				<Encoding>H264</Encoding>
				<Resolution><Width>1920</Width><Height>1080</Height></Resolution>
				<RateControl>
					<FrameRateLimit>25</FrameRateLimit>
					<BitrateLimit>4096</BitrateLimit>
				</RateControl>
			</VideoEncoderConfiguration>
			<AudioSourceConfiguration token="as0"/>
			<AudioEncoderConfiguration token="ae0"/>
		</Profiles>
	</GetProfilesResponse>`)
}

func media2ProfilesXML() string {
	return soapOK(`<GetProfilesResponse>
		<Profiles token="Profile_T">
			<Name>ProfileT</Name>
			<Configurations>
				<VideoSource token="vs1"/>
				<VideoEncoder token="ve1">
					<Encoding>H265</Encoding>
					<Resolution><Width>3840</Width><Height>2160</Height></Resolution>
					<RateControl>
						<FrameRateLimit>30</FrameRateLimit>
						<BitrateLimit>8192</BitrateLimit>
					</RateControl>
				</VideoEncoder>
				<AudioSource token="as1"/>
				<AudioEncoder token="ae1"/>
			</Configurations>
		</Profiles>
	</GetProfilesResponse>`)
}

func TestGetProfilesNoMediaEndpoint(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string { return "" })
	client := serviceClient(t, server, false, false, false, false)
	_, err := NewMediaService(client).GetProfiles(context.Background())
	require.ErrorContains(t, err, "no media service available")
}

func TestGetProfilesMedia2(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		if strings.Contains(body, "GetProfiles") && strings.Contains(body, "ver20/media") {
			return media2ProfilesXML()
		}
		return ""
	})
	client := serviceClient(t, server, true, true, false, false)
	profiles, err := NewMediaService(client).GetProfiles(context.Background())
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	require.Equal(t, "Profile_T", profiles[0].Token)
	require.Equal(t, "ProfileT", profiles[0].Name)
	require.Equal(t, "vs1", profiles[0].VideoSource)
	require.Equal(t, "ve1", profiles[0].VideoEncoder)
	require.Equal(t, "as1", profiles[0].AudioSource)
	require.Equal(t, "ae1", profiles[0].AudioEncoder)
	require.Equal(t, "H265", profiles[0].Encoding)
	require.Equal(t, 3840, profiles[0].Resolution.Width)
	require.Equal(t, 2160, profiles[0].Resolution.Height)
	require.Equal(t, 30, profiles[0].Framerate)
	require.Equal(t, 8192, profiles[0].Bitrate)
}

func TestGetProfilesMedia1Only(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		if strings.Contains(body, "GetProfiles") {
			return media1ProfilesXML()
		}
		return ""
	})
	client := serviceClient(t, server, true, false, false, false)
	profiles, err := NewMediaService(client).GetProfiles(context.Background())
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	require.Equal(t, "Profile_1", profiles[0].Token)
	require.Equal(t, "MainStream", profiles[0].Name)
	require.Equal(t, "vs0", profiles[0].VideoSource)
	require.Equal(t, "ve0", profiles[0].VideoEncoder)
	require.Equal(t, "as0", profiles[0].AudioSource)
	require.Equal(t, "ae0", profiles[0].AudioEncoder)
	require.Equal(t, "H264", profiles[0].Encoding)
	require.Equal(t, 1920, profiles[0].Resolution.Width)
	require.Equal(t, 1080, profiles[0].Resolution.Height)
	require.Equal(t, 25, profiles[0].Framerate)
	require.Equal(t, 4096, profiles[0].Bitrate)
}

func TestGetProfilesMedia2EmptyFallsBackToMedia1(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "ver20/media") && strings.Contains(body, "GetProfiles"):
			return soapOK(`<GetProfilesResponse></GetProfilesResponse>`)
		case strings.Contains(body, "GetProfiles"):
			return media1ProfilesXML()
		default:
			return ""
		}
	})
	client := serviceClient(t, server, true, true, false, false)
	profiles, err := NewMediaService(client).GetProfiles(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Profile_1", profiles[0].Token)
}

func TestGetProfilesMedia2FaultFallsBackToMedia1(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "ver20/media") && strings.Contains(body, "GetProfiles"):
			return soapFaultXML("s:Receiver", "media2 down")
		case strings.Contains(body, "GetProfiles"):
			return media1ProfilesXML()
		default:
			return ""
		}
	})
	client := serviceClient(t, server, true, true, false, false)
	profiles, err := NewMediaService(client).GetProfiles(context.Background())
	require.NoError(t, err)
	require.Equal(t, "MainStream", profiles[0].Name)
}

func TestGetProfilesMedia2FailNoMedia1(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "nope")
	})
	client := serviceClient(t, server, false, true, false, false)
	_, err := NewMediaService(client).GetProfiles(context.Background())
	require.ErrorContains(t, err, "no media service available")
}

func TestGetProfilesMedia1SOAPError(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "denied")
	})
	client := serviceClient(t, server, true, false, false, false)
	_, err := NewMediaService(client).GetProfiles(context.Background())
	require.ErrorContains(t, err, "GetProfiles failed")
}

func TestGetProfilesParseErrors(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetProfilesResponse><`)
	})
	client := serviceClient(t, server, true, true, false, false)
	_, err := NewMediaService(client).GetProfiles(context.Background())
	require.Error(t, err)
}

func TestGetStreamURIMedia2(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		if strings.Contains(body, "GetStreamUri") && strings.Contains(body, "ver20/media") {
			return soapOK(`<GetStreamUriResponse><Uri>rtsp://127.0.0.1/media2</Uri></GetStreamUriResponse>`)
		}
		return ""
	})
	client := serviceClient(t, server, true, true, false, false)
	uri, err := NewMediaService(client).GetStreamURI(context.Background(), "Profile_T")
	require.NoError(t, err)
	require.Equal(t, "rtsp://127.0.0.1/media2", uri)
}

func TestGetStreamURIMedia1Fallback(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "ver20/media") && strings.Contains(body, "GetStreamUri"):
			return soapFaultXML("s:Receiver", "no media2")
		case strings.Contains(body, "GetStreamUri"):
			return soapOK(`<GetStreamUriResponse><MediaUri><Uri>rtsp://127.0.0.1/stream1</Uri></MediaUri></GetStreamUriResponse>`)
		default:
			return ""
		}
	})
	client := serviceClient(t, server, true, true, false, false)
	uri, err := NewMediaService(client).GetStreamURI(context.Background(), "Profile_1")
	require.NoError(t, err)
	require.Equal(t, "rtsp://127.0.0.1/stream1", uri)
}

func TestGetStreamURIMedia1Only(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		if strings.Contains(body, "GetStreamUri") {
			return soapOK(`<GetStreamUriResponse><MediaUri><Uri>rtsp://127.0.0.1/only1</Uri></MediaUri></GetStreamUriResponse>`)
		}
		return ""
	})
	client := serviceClient(t, server, true, false, false, false)
	uri, err := NewMediaService(client).GetStreamURI(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, "rtsp://127.0.0.1/only1", uri)
}

func TestGetStreamURINoMedia(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string { return "" })
	client := serviceClient(t, server, false, false, false, false)
	_, err := NewMediaService(client).GetStreamURI(context.Background(), "p")
	require.ErrorContains(t, err, "no media service available")
}

func TestGetStreamURIMedia2FailNoMedia1(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "down")
	})
	client := serviceClient(t, server, false, true, false, false)
	_, err := NewMediaService(client).GetStreamURI(context.Background(), "p")
	require.ErrorContains(t, err, "no media service available")
}

func TestGetStreamURIMedia1SOAPAndParseError(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Sender", "bad")
	})
	client := serviceClient(t, server, true, false, false, false)
	_, err := NewMediaService(client).GetStreamURI(context.Background(), "p")
	require.ErrorContains(t, err, "GetStreamUri failed")

	parseServer := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetStreamUriResponse><`)
	})
	parseClient := serviceClient(t, parseServer, true, false, false, false)
	_, err = NewMediaService(parseClient).GetStreamURI(context.Background(), "p")
	require.Error(t, err)
}

func TestGetStreamURIMedia2ParseErrorFallsBack(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "ver20/media"):
			return soapOK(`<GetStreamUriResponse><`)
		default:
			return soapOK(`<GetStreamUriResponse><MediaUri><Uri>rtsp://127.0.0.1/fb</Uri></MediaUri></GetStreamUriResponse>`)
		}
	})
	client := serviceClient(t, server, true, true, false, false)
	uri, err := NewMediaService(client).GetStreamURI(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, "rtsp://127.0.0.1/fb", uri)
}

func TestGetSnapshotUriMedia2(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		if strings.Contains(body, "GetSnapshotUri") && strings.Contains(body, "ver20/media") {
			return soapOK(`<GetSnapshotUriResponse><Uri>http://127.0.0.1/snap2.jpg</Uri></GetSnapshotUriResponse>`)
		}
		return ""
	})
	client := serviceClient(t, server, true, true, false, false)
	uri, err := NewMediaService(client).GetSnapshotUri(context.Background(), "Profile_T")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1/snap2.jpg", uri)
}

func TestGetSnapshotUriMedia2EmptyFallsBackToMedia1(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "ver20/media") && strings.Contains(body, "GetSnapshotUri"):
			return soapOK(`<GetSnapshotUriResponse><Uri></Uri></GetSnapshotUriResponse>`)
		case strings.Contains(body, "GetSnapshotUri"):
			return soapOK(`<GetSnapshotUriResponse><MediaUri><Uri>http://127.0.0.1/snap1.jpg</Uri></MediaUri></GetSnapshotUriResponse>`)
		default:
			return ""
		}
	})
	client := serviceClient(t, server, true, true, false, false)
	uri, err := NewMediaService(client).GetSnapshotUri(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1/snap1.jpg", uri)
}

func TestGetSnapshotUriMedia2FaultFallsBackToMedia1(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "ver20/media"):
			return soapFaultXML("s:Receiver", "no snap")
		default:
			return soapOK(`<GetSnapshotUriResponse><MediaUri><Uri>http://127.0.0.1/fb.jpg</Uri></MediaUri></GetSnapshotUriResponse>`)
		}
	})
	client := serviceClient(t, server, true, true, false, false)
	uri, err := NewMediaService(client).GetSnapshotUri(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1/fb.jpg", uri)
}

func TestGetSnapshotUriMedia1Only(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetSnapshotUriResponse><MediaUri><Uri>http://127.0.0.1/m1.jpg</Uri></MediaUri></GetSnapshotUriResponse>`)
	})
	client := serviceClient(t, server, true, false, false, false)
	uri, err := NewMediaService(client).GetSnapshotUri(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1/m1.jpg", uri)
}

func TestGetSnapshotUriNoMedia(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string { return "" })
	client := serviceClient(t, server, false, false, false, false)
	_, err := NewMediaService(client).GetSnapshotUri(context.Background(), "p")
	require.ErrorContains(t, err, "Media service not available")
}

func TestGetSnapshotUriBothEmpty(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		if strings.Contains(body, "ver20/media") {
			return soapOK(`<GetSnapshotUriResponse><Uri></Uri></GetSnapshotUriResponse>`)
		}
		return soapOK(`<GetSnapshotUriResponse><MediaUri><Uri></Uri></MediaUri></GetSnapshotUriResponse>`)
	})
	client := serviceClient(t, server, true, true, false, false)
	_, err := NewMediaService(client).GetSnapshotUri(context.Background(), "p")
	require.ErrorContains(t, err, "empty snapshot uri")
}

func TestGetSnapshotUriMedia2FailNoMedia1(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "snap down")
	})
	client := serviceClient(t, server, false, true, false, false)
	_, err := NewMediaService(client).GetSnapshotUri(context.Background(), "p")
	require.ErrorContains(t, err, "GetSnapshotUri (Media2) failed")
}

func TestGetSnapshotUriParseErrors(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetSnapshotUriResponse><`)
	})
	client := serviceClient(t, server, true, false, false, false)
	_, err := NewMediaService(client).GetSnapshotUri(context.Background(), "p")
	require.Error(t, err)

	m2 := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetSnapshotUriResponse><`)
	})
	c2 := serviceClient(t, m2, false, true, false, false)
	_, err = NewMediaService(c2).GetSnapshotUri(context.Background(), "p")
	require.Error(t, err)
}
