package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
)

// ImagingService handles imaging operations.
type ImagingService struct {
	client *Client
}

// NewImagingService creates a new imaging service.
func NewImagingService(client *Client) *ImagingService {
	return &ImagingService{client: client}
}

// GetImagingSettings retrieves imaging settings for a video source.
func (s *ImagingService) GetImagingSettings(ctx context.Context, videoSourceToken string) (*ImagingSettings, error) {
	if s.client.endpoints.Imaging == nil {
		return nil, fmt.Errorf("onvif: Imaging service not available")
	}

	body := fmt.Sprintf(`<GetImagingSettings xmlns="http://www.onvif.org/ver20/imaging/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
</GetImagingSettings>`, escapeXML(videoSourceToken))

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Imaging.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetImagingSettings failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetImagingSettingsResponse"`
		ImagingSettings struct {
			Brightness    float64 `xml:"Brightness"`
			Contrast      float64 `xml:"Contrast"`
			ColorSaturation float64 `xml:"ColorSaturation"`
			Sharpness     float64 `xml:"Sharpness"`
			WhiteBalance  struct {
				Mode string `xml:"Mode"`
			} `xml:"WhiteBalance"`
			Focus struct {
				Mode string `xml:"Mode"`
			} `xml:"Focus"`
			Exposure struct {
				Mode string `xml:"Mode"`
			} `xml:"Exposure"`
		} `xml:"ImagingSettings"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	settings := &ImagingSettings{
		Brightness:      result.ImagingSettings.Brightness,
		Contrast:        result.ImagingSettings.Contrast,
		ColorSaturation: result.ImagingSettings.ColorSaturation,
		Sharpness:       result.ImagingSettings.Sharpness,
		WhiteBalance:    result.ImagingSettings.WhiteBalance.Mode,
		FocusMode:       result.ImagingSettings.Focus.Mode,
		ExposureMode:    result.ImagingSettings.Exposure.Mode,
	}

	return settings, nil
}

// SetImagingSettings updates imaging settings.
func (s *ImagingService) SetImagingSettings(ctx context.Context, videoSourceToken string, settings ImagingSettings) error {
	if s.client.endpoints.Imaging == nil {
		return fmt.Errorf("onvif: Imaging service not available")
	}

	body := fmt.Sprintf(`<SetImagingSettings xmlns="http://www.onvif.org/ver20/imaging/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
  <ImagingSettings>
    <Brightness>%f</Brightness>
    <Contrast>%f</Contrast>
    <ColorSaturation>%f</ColorSaturation>
    <Sharpness>%f</Sharpness>
    <WhiteBalance>
      <Mode>%s</Mode>
    </WhiteBalance>
    <Focus>
      <Mode>%s</Mode>
    </Focus>
    <Exposure>
      <Mode>%s</Mode>
    </Exposure>
  </ImagingSettings>
</SetImagingSettings>`, escapeXML(videoSourceToken),
		settings.Brightness,
		settings.Contrast,
		settings.ColorSaturation,
		settings.Sharpness,
		settings.WhiteBalance,
		settings.FocusMode,
		settings.ExposureMode,
	)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Imaging.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: SetImagingSettings failed: %w", err)
	}

	return nil
}

// GetOptions retrieves imaging options for a video source.
func (s *ImagingService) GetOptions(ctx context.Context, videoSourceToken string) (*ImagingOptions, error) {
	if s.client.endpoints.Imaging == nil {
		return nil, fmt.Errorf("onvif: Imaging service not available")
	}

	body := fmt.Sprintf(`<GetOptions xmlns="http://www.onvif.org/ver20/imaging/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
</GetOptions>`, escapeXML(videoSourceToken))

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Imaging.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetOptions failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetOptionsResponse"`
		ImagingOptions struct {
			Brightness struct {
				Min float64 `xml:"Min"`
				Max float64 `xml:"Max"`
			} `xml:"Brightness"`
			Contrast struct {
				Min float64 `xml:"Min"`
				Max float64 `xml:"Max"`
			} `xml:"Contrast"`
			ColorSaturation struct {
				Min float64 `xml:"Min"`
				Max float64 `xml:"Max"`
			} `xml:"ColorSaturation"`
			Sharpness struct {
				Min float64 `xml:"Min"`
				Max float64 `xml:"Max"`
			} `xml:"Sharpness"`
		} `xml:"ImagingOptions"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	options := &ImagingOptions{
		Brightness: &Range{
			Min: result.ImagingOptions.Brightness.Min,
			Max: result.ImagingOptions.Brightness.Max,
		},
		Contrast: &Range{
			Min: result.ImagingOptions.Contrast.Min,
			Max: result.ImagingOptions.Contrast.Max,
		},
		Saturation: &Range{
			Min: result.ImagingOptions.ColorSaturation.Min,
			Max: result.ImagingOptions.ColorSaturation.Max,
		},
		Sharpness: &Range{
			Min: result.ImagingOptions.Sharpness.Min,
			Max: result.ImagingOptions.Sharpness.Max,
		},
	}

	return options, nil
}

// GetMoveOptions retrieves move options for a video source.
func (s *ImagingService) GetMoveOptions(ctx context.Context, videoSourceToken string) (map[string]interface{}, error) {
	if s.client.endpoints.Imaging == nil {
		return nil, fmt.Errorf("onvif: Imaging service not available")
	}

	body := fmt.Sprintf(`<GetMoveOptions xmlns="http://www.onvif.org/ver20/imaging/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
</GetMoveOptions>`, videoSourceToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Imaging.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetMoveOptions failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetMoveOptionsResponse"`
		MoveOptions struct {
			Absolute struct {
				Position struct {
					Min float64 `xml:"Min"`
					Max float64 `xml:"Max"`
				} `xml:"Position"`
				Speed struct {
					Min float64 `xml:"Min"`
					Max float64 `xml:"Max"`
				} `xml:"Speed"`
			} `xml:"Absolute"`
			Relative struct {
				Min float64 `xml:"Min"`
				Max float64 `xml:"Max"`
			} `xml:"Relative"`
			Continuous struct {
				Speed struct {
					Min float64 `xml:"Min"`
					Max float64 `xml:"Max"`
				} `xml:"Speed"`
			} `xml:"Continuous"`
		} `xml:"MoveOptions"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	options := map[string]interface{}{
		"absolute": map[string]interface{}{
			"position": map[string]interface{}{
				"min": result.MoveOptions.Absolute.Position.Min,
				"max": result.MoveOptions.Absolute.Position.Max,
			},
			"speed": map[string]interface{}{
				"min": result.MoveOptions.Absolute.Speed.Min,
				"max": result.MoveOptions.Absolute.Speed.Max,
			},
		},
		"relative": map[string]interface{}{
			"min": result.MoveOptions.Relative.Min,
			"max": result.MoveOptions.Relative.Max,
		},
		"continuous": map[string]interface{}{
			"speed": map[string]interface{}{
				"min": result.MoveOptions.Continuous.Speed.Min,
				"max": result.MoveOptions.Continuous.Speed.Max,
			},
		},
	}

	return options, nil
}

// Move performs a focus move operation.
func (s *ImagingService) Move(ctx context.Context, videoSourceToken string, focus float64) error {
	if s.client.endpoints.Imaging == nil {
		return fmt.Errorf("onvif: Imaging service not available")
	}

	body := fmt.Sprintf(`<Move xmlns="http://www.onvif.org/ver20/imaging/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
  <Focus>
    <Absolute>
      <Position>%f</Position>
      <Speed>1.0</Speed>
    </Absolute>
  </Focus>
</Move>`, videoSourceToken, focus)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Imaging.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: Move failed: %w", err)
	}

	return nil
}

// Stop stops imaging movement.
func (s *ImagingService) Stop(ctx context.Context, videoSourceToken string) error {
	if s.client.endpoints.Imaging == nil {
		return fmt.Errorf("onvif: Imaging service not available")
	}

	body := fmt.Sprintf(`<Stop xmlns="http://www.onvif.org/ver20/imaging/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
</Stop>`, videoSourceToken)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Imaging.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: Stop failed: %w", err)
	}

	return nil
}
