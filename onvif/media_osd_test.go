package onvif

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetOSDs(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		require.Contains(t, body, "GetOSDs")
		require.Contains(t, body, "<VideoSourceToken>vs0</VideoSourceToken>")
		return soapOK(`<GetOSDsResponse>
			<OSDs token="osd-text">
				<VideoSourceConfigurationToken>vs0</VideoSourceConfigurationToken>
				<Type>Text</Type>
				<Position x="0.1" y="0.2"><Type>UpperLeft</Type></Position>
				<TextString>
					<Type>Plain</Type>
					<DateFormat>yyyy-MM-dd</DateFormat>
					<TimeFormat>HH:mm:ss</TimeFormat>
					<FontSize>16</FontSize>
					<PlainText>CAM1</PlainText>
				</TextString>
			</OSDs>
			<OSDs token="osd-img">
				<VideoSourceConfigurationToken>vs0</VideoSourceConfigurationToken>
				<Type>Image</Type>
				<Position><Type>LowerRight</Type></Position>
				<Image><ImgPath>/logo.png</ImgPath></Image>
			</OSDs>
		</GetOSDsResponse>`)
	})
	osds, err := NewMediaService(serviceClient(t, server, true, false, false, false)).GetOSDs(context.Background(), "vs0")
	require.NoError(t, err)
	require.Len(t, osds, 2)
	require.Equal(t, "osd-text", osds[0].Token)
	require.Equal(t, "Text", osds[0].Type)
	require.Equal(t, "UpperLeft", osds[0].Position.Type)
	require.Equal(t, 0.1, osds[0].Position.X)
	require.NotNil(t, osds[0].TextConfig)
	require.Equal(t, "CAM1", osds[0].TextConfig.PlainText)
	require.Equal(t, 16, osds[0].TextConfig.FontSize)
	require.Equal(t, "osd-img", osds[1].Token)
	require.NotNil(t, osds[1].ImageConfig)
	require.Equal(t, "/logo.png", osds[1].ImageConfig.ImgPath)
}

func TestGetOSDOptions(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetOSDOptionsResponse>
			<Options>
				<PositionOption><Type>UpperLeft</Type></PositionOption>
				<PositionOption><Type>Custom</Type></PositionOption>
				<TextOption>
					<Type>Plain</Type>
					<Type>Date</Type>
					<FontSizeRange><Min>8</Min><Max>64</Max></FontSizeRange>
					<DateFormat>yyyy-MM-dd</DateFormat>
					<TimeFormat>HH:mm:ss</TimeFormat>
				</TextOption>
				<ImageOption>
					<ImagePath>/logo.png</ImagePath>
				</ImageOption>
			</Options>
		</GetOSDOptionsResponse>`)
	})
	opts, err := NewMediaService(serviceClient(t, server, true, false, false, false)).GetOSDOptions(context.Background(), "vs0")
	require.NoError(t, err)
	require.Equal(t, []string{"UpperLeft", "Custom"}, opts["position_types"])
	require.Equal(t, []string{"Plain", "Date"}, opts["text_types"])
	require.Equal(t, Range{Min: 8, Max: 64}, opts["font_size_range"])
	require.Equal(t, []string{"/logo.png"}, opts["image_paths"])
}

func TestGetOSDOptionsMinimal(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetOSDOptionsResponse><Options></Options></GetOSDOptionsResponse>`)
	})
	opts, err := NewMediaService(serviceClient(t, server, true, false, false, false)).GetOSDOptions(context.Background(), "vs0")
	require.NoError(t, err)
	require.Empty(t, opts["position_types"])
	require.Nil(t, opts["text_types"])
	require.Nil(t, opts["image_paths"])
}

func TestCreateSetDeleteOSD(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "CreateOSD"):
			require.Contains(t, body, "<Type>Text</Type>")
			require.Contains(t, body, "<PlainText>HELLO</PlainText>")
			require.Contains(t, body, "<FontSize>12</FontSize>")
			return soapOK(`<CreateOSDResponse><OSDToken>osd-new</OSDToken></CreateOSDResponse>`)
		case strings.Contains(body, "SetOSD"):
			require.Contains(t, body, `token="osd-new"`)
			require.Contains(t, body, "<PlainText>UPDATED</PlainText>")
			return soapOK(`<SetOSDResponse/>`)
		case strings.Contains(body, "DeleteOSD"):
			require.Contains(t, body, "<OSDToken>osd-new</OSDToken>")
			return soapOK(`<DeleteOSDResponse/>`)
		default:
			return ""
		}
	})
	svc := NewMediaService(serviceClient(t, server, true, false, false, false))
	ctx := context.Background()
	created, err := svc.CreateOSD(ctx, "vs0", OSDConfiguration{
		Type:     "Text",
		Position: OSDPosition{Type: "UpperLeft"},
		TextConfig: &OSDTextConfig{
			Type:      "Plain",
			PlainText: "HELLO",
			FontSize:  12,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "osd-new", created.Token)
	created.TextConfig.PlainText = "UPDATED"
	require.NoError(t, svc.SetOSD(ctx, *created))
	require.NoError(t, svc.DeleteOSD(ctx, "osd-new"))
}

func TestCreateSetOSDWithoutText(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		require.NotContains(t, body, "TextString")
		if strings.Contains(body, "CreateOSD") {
			return soapOK(`<CreateOSDResponse><OSDToken>osd-img</OSDToken></CreateOSDResponse>`)
		}
		return soapOK(`<SetOSDResponse/>`)
	})
	svc := NewMediaService(serviceClient(t, server, true, false, false, false))
	ctx := context.Background()
	created, err := svc.CreateOSD(ctx, "vs0", OSDConfiguration{
		Type:     "Image",
		Position: OSDPosition{Type: "LowerRight"},
	})
	require.NoError(t, err)
	require.Equal(t, "osd-img", created.Token)
	require.NoError(t, svc.SetOSD(ctx, OSDConfiguration{Token: "osd-img", Type: "Image", Position: OSDPosition{Type: "LowerRight"}}))
}

func TestOSDErrors(t *testing.T) {
	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "denied")
	})
	svc := NewMediaService(serviceClient(t, fault, true, false, false, false))
	ctx := context.Background()
	_, err := svc.GetOSDs(ctx, "vs0")
	require.ErrorContains(t, err, "GetOSDs failed")
	_, err = svc.GetOSDOptions(ctx, "vs0")
	require.ErrorContains(t, err, "GetOSDOptions failed")
	_, err = svc.CreateOSD(ctx, "vs0", OSDConfiguration{})
	require.ErrorContains(t, err, "CreateOSD failed")
	require.ErrorContains(t, svc.SetOSD(ctx, OSDConfiguration{Token: "x"}), "SetOSD failed")
	require.ErrorContains(t, svc.DeleteOSD(ctx, "x"), "DeleteOSD failed")

	parseCalls := map[string]func(*MediaService) error{
		"GetOSDs":       func(s *MediaService) error { _, err := s.GetOSDs(ctx, "vs0"); return err },
		"GetOSDOptions": func(s *MediaService) error { _, err := s.GetOSDOptions(ctx, "vs0"); return err },
		"CreateOSD":     func(s *MediaService) error { _, err := s.CreateOSD(ctx, "vs0", OSDConfiguration{}); return err },
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
