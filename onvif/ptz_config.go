package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
)

// PTZ Node and Configuration queries.

// PTZNode represents a PTZ node.
type PTZNode struct {
	Token               string `json:"token"`
	Name                string `json:"name"`
	FixedHomePosition   bool   `json:"fixed_home_position"`
	GeoMove             bool   `json:"geo_move"`
	MaximumNumberOfPresets int `json:"max_presets"`
	HomeSupported       bool   `json:"home_supported"`
}

// PTZConfiguration represents a PTZ configuration.
type PTZConfiguration struct {
	Token               string `json:"token"`
	Name                string `json:"name"`
	UseCount            int    `json:"use_count"`
	NodeToken           string `json:"node_token"`
	DefaultAbsolutePantTiltPositionSpace string `json:"default_absolute_pantilt_space,omitempty"`
	DefaultAbsoluteZoomPositionSpace     string `json:"default_absolute_zoom_space,omitempty"`
	DefaultRelativePanTiltTranslationSpace string `json:"default_relative_pantilt_space,omitempty"`
	DefaultRelativeZoomTranslationSpace   string `json:"default_relative_zoom_space,omitempty"`
	DefaultContinuousPanTiltVelocitySpace string `json:"default_continuous_pantilt_space,omitempty"`
	DefaultContinuousZoomVelocitySpace   string `json:"default_continuous_zoom_space,omitempty"`
	DefaultPTZSpeed     *PTZSpeed `json:"default_ptz_speed,omitempty"`
	DefaultPTZTimeout   string    `json:"default_ptz_timeout,omitempty"`
}

// PTZSpeed represents PTZ speed.
type PTZSpeed struct {
	PanTilt Vector2D `json:"pan_tilt"`
	Zoom    Vector1D `json:"zoom"`
}

// PTZConfigurationOptions represents PTZ configuration options.
type PTZConfigurationOptions struct {
	Spaces PTZSpaces `json:"spaces"`
	PTZTimeout Range `json:"ptz_timeout"`
}

// PTZSpaces represents PTZ spaces.
type PTZSpaces struct {
	AbsolutePanTiltPositionSpace []PTZSpace `json:"absolute_pantilt_space,omitempty"`
	AbsoluteZoomPositionSpace    []PTZSpace `json:"absolute_zoom_space,omitempty"`
	RelativePanTiltTranslationSpace []PTZSpace `json:"relative_pantilt_space,omitempty"`
	RelativeZoomTranslationSpace    []PTZSpace `json:"relative_zoom_space,omitempty"`
	ContinuousPanTiltVelocitySpace  []PTZSpace `json:"continuous_pantilt_space,omitempty"`
	ContinuousZoomVelocitySpace     []PTZSpace `json:"continuous_zoom_space,omitempty"`
}

// PTZSpace represents a PTZ space.
type PTZSpace struct {
	URI    string     `json:"uri"`
	XRange Range      `json:"x_range"`
	YRange Range      `json:"y_range,omitempty"`
}

// GetPTZNodes retrieves PTZ nodes from the device.
func (s *PTZService) GetPTZNodes(ctx context.Context) ([]PTZNode, error) {
	if s.client.endpoints.PTZ == nil {
		return nil, fmt.Errorf("onvif: PTZ service not available")
	}

	body := `<GetNodes xmlns="http://www.onvif.org/ver20/ptz/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetPTZNodes failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetNodesResponse"`
		PTZNode []struct {
			Token               string `xml:"token,attr"`
			Name                string `xml:"Name"`
			FixedHomePosition   bool   `xml:"FixedHomePosition"`
			GeoMove             bool   `xml:"GeoMove"`
			MaximumNumberOfPresets int `xml:"MaximumNumberOfPresets"`
			HomeSupported       bool   `xml:"HomeSupported"`
		} `xml:"PTZNode"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	nodes := make([]PTZNode, 0, len(result.PTZNode))
	for _, n := range result.PTZNode {
		nodes = append(nodes, PTZNode{
			Token:               n.Token,
			Name:                n.Name,
			FixedHomePosition:   n.FixedHomePosition,
			GeoMove:             n.GeoMove,
			MaximumNumberOfPresets: n.MaximumNumberOfPresets,
			HomeSupported:       n.HomeSupported,
		})
	}
	return nodes, nil
}

// GetPTZConfigurations retrieves PTZ configurations.
func (s *PTZService) GetPTZConfigurations(ctx context.Context) ([]PTZConfiguration, error) {
	if s.client.endpoints.PTZ == nil {
		return nil, fmt.Errorf("onvif: PTZ service not available")
	}

	body := `<GetConfigurations xmlns="http://www.onvif.org/ver20/ptz/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetPTZConfigurations failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetConfigurationsResponse"`
		PTZConfiguration []struct {
			Token    string `xml:"token,attr"`
			Name     string `xml:"Name"`
			UseCount int    `xml:"UseCount"`
			NodeToken string `xml:"NodeToken"`
			DefaultAbsolutePantTiltPositionSpace string `xml:"DefaultAbsolutePantTiltPositionSpace"`
			DefaultAbsoluteZoomPositionSpace     string `xml:"DefaultAbsoluteZoomPositionSpace"`
			DefaultContinuousPanTiltVelocitySpace string `xml:"DefaultContinuousPanTiltVelocitySpace"`
			DefaultContinuousZoomVelocitySpace   string `xml:"DefaultContinuousZoomVelocitySpace"`
			DefaultPTZTimeout string `xml:"DefaultPTZTimeout"`
		} `xml:"PTZConfiguration"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	configs := make([]PTZConfiguration, 0, len(result.PTZConfiguration))
	for _, c := range result.PTZConfiguration {
		configs = append(configs, PTZConfiguration{
			Token:               c.Token,
			Name:                c.Name,
			UseCount:            c.UseCount,
			NodeToken:           c.NodeToken,
			DefaultAbsolutePantTiltPositionSpace: c.DefaultAbsolutePantTiltPositionSpace,
			DefaultAbsoluteZoomPositionSpace:     c.DefaultAbsoluteZoomPositionSpace,
			DefaultContinuousPanTiltVelocitySpace: c.DefaultContinuousPanTiltVelocitySpace,
			DefaultContinuousZoomVelocitySpace:   c.DefaultContinuousZoomVelocitySpace,
			DefaultPTZTimeout:   c.DefaultPTZTimeout,
		})
	}
	return configs, nil
}

// GetPTZConfigurationOptions retrieves PTZ configuration options for a configuration.
func (s *PTZService) GetPTZConfigurationOptions(ctx context.Context, configToken string) (*PTZConfigurationOptions, error) {
	if s.client.endpoints.PTZ == nil {
		return nil, fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<GetConfigurationOptions xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ConfigurationToken>%s</ConfigurationToken>
</GetConfigurationOptions>`, configToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetPTZConfigurationOptions failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetConfigurationOptionsResponse"`
		Options struct {
			Spaces struct {
				AbsolutePanTiltPositionSpace []struct {
					URI    string `xml:"URI"`
					XRange struct {
						Min float64 `xml:"Min"`
						Max float64 `xml:"Max"`
					} `xml:"XRange"`
					YRange struct {
						Min float64 `xml:"Min"`
						Max float64 `xml:"Max"`
					} `xml:"YRange"`
				} `xml:"AbsolutePanTiltPositionSpace"`
				AbsoluteZoomPositionSpace []struct {
					URI    string `xml:"URI"`
					XRange struct {
						Min float64 `xml:"Min"`
						Max float64 `xml:"Max"`
					} `xml:"XRange"`
				} `xml:"AbsoluteZoomPositionSpace"`
				ContinuousPanTiltVelocitySpace []struct {
					URI    string `xml:"URI"`
					XRange struct {
						Min float64 `xml:"Min"`
						Max float64 `xml:"Max"`
					} `xml:"XRange"`
					YRange struct {
						Min float64 `xml:"Min"`
						Max float64 `xml:"Max"`
					} `xml:"YRange"`
				} `xml:"ContinuousPanTiltVelocitySpace"`
				ContinuousZoomVelocitySpace []struct {
					URI    string `xml:"URI"`
					XRange struct {
						Min float64 `xml:"Min"`
						Max float64 `xml:"Max"`
					} `xml:"XRange"`
				} `xml:"ContinuousZoomVelocitySpace"`
			} `xml:"Spaces"`
			PTZTimeout struct {
				Min string `xml:"Min"`
				Max string `xml:"Max"`
			} `xml:"PTZTimeout"`
		} `xml:"Options"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	options := &PTZConfigurationOptions{}

	for _, s := range result.Options.Spaces.AbsolutePanTiltPositionSpace {
		options.Spaces.AbsolutePanTiltPositionSpace = append(options.Spaces.AbsolutePanTiltPositionSpace, PTZSpace{
			URI:    s.URI,
			XRange: Range{Min: s.XRange.Min, Max: s.XRange.Max},
			YRange: Range{Min: s.YRange.Min, Max: s.YRange.Max},
		})
	}
	for _, s := range result.Options.Spaces.AbsoluteZoomPositionSpace {
		options.Spaces.AbsoluteZoomPositionSpace = append(options.Spaces.AbsoluteZoomPositionSpace, PTZSpace{
			URI:    s.URI,
			XRange: Range{Min: s.XRange.Min, Max: s.XRange.Max},
		})
	}
	for _, s := range result.Options.Spaces.ContinuousPanTiltVelocitySpace {
		options.Spaces.ContinuousPanTiltVelocitySpace = append(options.Spaces.ContinuousPanTiltVelocitySpace, PTZSpace{
			URI:    s.URI,
			XRange: Range{Min: s.XRange.Min, Max: s.XRange.Max},
			YRange: Range{Min: s.YRange.Min, Max: s.YRange.Max},
		})
	}
	for _, s := range result.Options.Spaces.ContinuousZoomVelocitySpace {
		options.Spaces.ContinuousZoomVelocitySpace = append(options.Spaces.ContinuousZoomVelocitySpace, PTZSpace{
			URI:    s.URI,
			XRange: Range{Min: s.XRange.Min, Max: s.XRange.Max},
		})
	}

	return options, nil
}

// PTZSendAuxiliaryCommand sends an auxiliary command to the PTZ device.
func (s *PTZService) PTZSendAuxiliaryCommand(ctx context.Context, profileToken, auxiliaryData string) (string, error) {
	if s.client.endpoints.PTZ == nil {
		return "", fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<PTZSendAuxiliaryCommand xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
  <AuxiliaryData>%s</AuxiliaryData>
</PTZSendAuxiliaryCommand>`, profileToken, auxiliaryData)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return "", fmt.Errorf("onvif: PTZSendAuxiliaryCommand failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"PTZSendAuxiliaryCommandResponse"`
		AuxiliaryData string `xml:"AuxiliaryData"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return "", err
	}

	return result.AuxiliaryData, nil
}
