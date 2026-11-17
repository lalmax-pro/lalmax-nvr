package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
)

// OSD (On-Screen Display) operations for Media service.

// OSDConfiguration represents an OSD configuration.
type OSDConfiguration struct {
	Token       string        `json:"token"`
	VideoSource string        `json:"video_source"`
	Type        string        `json:"type"` // Text, Image, Extended
	Position    OSDPosition   `json:"position"`
	TextConfig  *OSDTextConfig `json:"text_config,omitempty"`
	ImageConfig *OSDImageConfig `json:"image_config,omitempty"`
}

// OSDPosition represents the position of an OSD element.
type OSDPosition struct {
	Type      string  `json:"type"` // UpperLeft, UpperRight, LowerLeft, LowerRight, Custom
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
}

// OSDTextConfig represents OSD text configuration.
type OSDTextConfig struct {
	Type        string `json:"type"` // Plain, Date, Time, DateAndTime
	DateFormat  string `json:"date_format,omitempty"`
	TimeFormat  string `json:"time_format,omitempty"`
	FontSize    int    `json:"font_size,omitempty"`
	FontColor   string `json:"font_color,omitempty"`
	BackColor   string `json:"back_color,omitempty"`
	PlainText   string `json:"plain_text,omitempty"`
}

// OSDImageConfig represents OSD image configuration.
type OSDImageConfig struct {
	ImgPath string `json:"img_path"`
}

// GetOSDs retrieves all OSD configurations for a video source.
func (s *MediaService) GetOSDs(ctx context.Context, videoSourceToken string) ([]OSDConfiguration, error) {
	endpoint := s.mediaEndpoint()
	body := fmt.Sprintf(`<GetOSDs xmlns="http://www.onvif.org/ver10/media/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
</GetOSDs>`, videoSourceToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetOSDs failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetOSDsResponse"`
		OSDs    []struct {
			Token       string `xml:"token,attr"`
			VideoSource string `xml:"VideoSourceConfigurationToken"`
			Type        string `xml:"Type"`
			Position    struct {
				Type string `xml:"Type"`
				X    float64 `xml:"x,attr"`
				Y    float64 `xml:"y,attr"`
			} `xml:"Position"`
			TextConfig *struct {
				Type       string `xml:"Type"`
				DateFormat string `xml:"DateFormat"`
				TimeFormat string `xml:"TimeFormat"`
				FontSize   int    `xml:"FontSize"`
				FontColor  struct {
					X float64 `xml:"X"`
					Y float64 `xml:"Y"`
					Z float64 `xml:"Z"`
				} `xml:"FontColor"`
				PlainText string `xml:"PlainText"`
			} `xml:"TextString"`
			ImageConfig *struct {
				ImgPath string `xml:"ImgPath"`
			} `xml:"Image"`
		} `xml:"OSDs"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	osds := make([]OSDConfiguration, 0, len(result.OSDs))
	for _, o := range result.OSDs {
		osd := OSDConfiguration{
			Token:       o.Token,
			VideoSource: o.VideoSource,
			Type:        o.Type,
			Position: OSDPosition{
				Type: o.Position.Type,
				X:    o.Position.X,
				Y:    o.Position.Y,
			},
		}
		if o.TextConfig != nil {
			osd.TextConfig = &OSDTextConfig{
				Type:       o.TextConfig.Type,
				DateFormat: o.TextConfig.DateFormat,
				TimeFormat: o.TextConfig.TimeFormat,
				FontSize:   o.TextConfig.FontSize,
				PlainText:  o.TextConfig.PlainText,
			}
		}
		if o.ImageConfig != nil {
			osd.ImageConfig = &OSDImageConfig{ImgPath: o.ImageConfig.ImgPath}
		}
		osds = append(osds, osd)
	}
	return osds, nil
}

// GetOSDOptions retrieves OSD options for a video source.
func (s *MediaService) GetOSDOptions(ctx context.Context, videoSourceToken string) (map[string]interface{}, error) {
	endpoint := s.mediaEndpoint()
	body := fmt.Sprintf(`<GetOSDOptions xmlns="http://www.onvif.org/ver10/media/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
</GetOSDOptions>`, videoSourceToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetOSDOptions failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetOSDOptionsResponse"`
		Options struct {
			PositionOptions []struct {
				Type string `xml:"Type"`
			} `xml:"PositionOption"`
			TextOption *struct {
				Type         []string `xml:"Type"`
				FontSizeRange struct {
					Min int `xml:"Min"`
					Max int `xml:"Max"`
				} `xml:"FontSizeRange"`
				DateFormat []string `xml:"DateFormat"`
				TimeFormat []string `xml:"TimeFormat"`
			} `xml:"TextOption"`
			ImageOption *struct {
				ImagePath []string `xml:"ImagePath"`
			} `xml:"ImageOption"`
		} `xml:"Options"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	positionTypes := make([]string, 0, len(result.Options.PositionOptions))
	for _, p := range result.Options.PositionOptions {
		positionTypes = append(positionTypes, p.Type)
	}

	options := map[string]interface{}{
		"position_types": positionTypes,
	}

	if result.Options.TextOption != nil {
		options["text_types"] = result.Options.TextOption.Type
		options["font_size_range"] = Range{
			Min: float64(result.Options.TextOption.FontSizeRange.Min),
			Max: float64(result.Options.TextOption.FontSizeRange.Max),
		}
		options["date_formats"] = result.Options.TextOption.DateFormat
		options["time_formats"] = result.Options.TextOption.TimeFormat
	}

	if result.Options.ImageOption != nil {
		options["image_paths"] = result.Options.ImageOption.ImagePath
	}

	return options, nil
}

// CreateOSD creates a new OSD configuration.
func (s *MediaService) CreateOSD(ctx context.Context, videoSourceToken string, osd OSDConfiguration) (*OSDConfiguration, error) {
	endpoint := s.mediaEndpoint()

	textXML := ""
	if osd.TextConfig != nil {
		textXML = fmt.Sprintf(`<TextString>
  <Type>%s</Type>
  <PlainText>%s</PlainText>
  <FontSize>%d</FontSize>
</TextString>`, osd.TextConfig.Type, osd.TextConfig.PlainText, osd.TextConfig.FontSize)
	}

	body := fmt.Sprintf(`<CreateOSD xmlns="http://www.onvif.org/ver10/media/wsdl">
  <OSD>
    <VideoSourceConfigurationToken>%s</VideoSourceConfigurationToken>
    <Type>%s</Type>
    <Position>
      <Type>%s</Type>
    </Position>
    %s
  </OSD>
</CreateOSD>`, videoSourceToken, osd.Type, osd.Position.Type, textXML)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: CreateOSD failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"CreateOSDResponse"`
		Token   string   `xml:"OSDToken"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	osd.Token = result.Token
	return &osd, nil
}

// SetOSD updates an existing OSD configuration.
func (s *MediaService) SetOSD(ctx context.Context, osd OSDConfiguration) error {
	endpoint := s.mediaEndpoint()

	textXML := ""
	if osd.TextConfig != nil {
		textXML = fmt.Sprintf(`<TextString>
  <Type>%s</Type>
  <PlainText>%s</PlainText>
  <FontSize>%d</FontSize>
</TextString>`, osd.TextConfig.Type, osd.TextConfig.PlainText, osd.TextConfig.FontSize)
	}

	body := fmt.Sprintf(`<SetOSD xmlns="http://www.onvif.org/ver10/media/wsdl">
  <OSD token="%s">
    <Type>%s</Type>
    <Position>
      <Type>%s</Type>
    </Position>
    %s
  </OSD>
</SetOSD>`, osd.Token, osd.Type, osd.Position.Type, textXML)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: SetOSD failed: %w", err)
	}
	return nil
}

// DeleteOSD deletes an OSD configuration.
func (s *MediaService) DeleteOSD(ctx context.Context, osdToken string) error {
	endpoint := s.mediaEndpoint()
	body := fmt.Sprintf(`<DeleteOSD xmlns="http://www.onvif.org/ver10/media/wsdl">
  <OSDToken>%s</OSDToken>
</DeleteOSD>`, osdToken)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: DeleteOSD failed: %w", err)
	}
	return nil
}
