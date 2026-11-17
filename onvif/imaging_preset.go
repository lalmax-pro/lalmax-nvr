package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
)

// Imaging presets and status operations.

// ImagingPreset represents an imaging preset.
type ImagingPreset struct {
	Token       string `json:"token"`
	Name        string `json:"name"`
	Type        string `json:"type"`
}

// GetCurrentImagingPreset retrieves the current imaging preset.
func (s *ImagingService) GetCurrentImagingPreset(ctx context.Context, videoSourceToken string) (*ImagingPreset, error) {
	if s.client.endpoints.Imaging == nil {
		return nil, fmt.Errorf("onvif: Imaging service not available")
	}

	body := fmt.Sprintf(`<GetCurrentPreset xmlns="http://www.onvif.org/ver20/imaging/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
</GetCurrentPreset>`, videoSourceToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Imaging.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetCurrentImagingPreset failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetCurrentPresetResponse"`
		Preset  struct {
			Token string `xml:"token,attr"`
			Name  string `xml:"Name"`
			Type  string `xml:"Type"`
		} `xml:"Preset"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return &ImagingPreset{
		Token: result.Preset.Token,
		Name:  result.Preset.Name,
		Type:  result.Preset.Type,
	}, nil
}

// SetCurrentImagingPreset sets the current imaging preset.
func (s *ImagingService) SetCurrentImagingPreset(ctx context.Context, videoSourceToken, presetToken string) error {
	if s.client.endpoints.Imaging == nil {
		return fmt.Errorf("onvif: Imaging service not available")
	}

	body := fmt.Sprintf(`<SetCurrentPreset xmlns="http://www.onvif.org/ver20/imaging/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
  <PresetToken>%s</PresetToken>
</SetCurrentPreset>`, videoSourceToken, presetToken)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Imaging.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: SetCurrentImagingPreset failed: %w", err)
	}
	return nil
}

// GetImagingPresets retrieves all available imaging presets.
func (s *ImagingService) GetImagingPresets(ctx context.Context, videoSourceToken string) ([]ImagingPreset, error) {
	if s.client.endpoints.Imaging == nil {
		return nil, fmt.Errorf("onvif: Imaging service not available")
	}

	body := fmt.Sprintf(`<GetPresets xmlns="http://www.onvif.org/ver20/imaging/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
</GetPresets>`, videoSourceToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Imaging.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetImagingPresets failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetPresetsResponse"`
		Presets []struct {
			Token string `xml:"token,attr"`
			Name  string `xml:"Name"`
			Type  string `xml:"Type"`
		} `xml:"Preset"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	presets := make([]ImagingPreset, 0, len(result.Presets))
	for _, p := range result.Presets {
		presets = append(presets, ImagingPreset{
			Token: p.Token,
			Name:  p.Name,
			Type:  p.Type,
		})
	}
	return presets, nil
}

// GetImagingStatus retrieves the current imaging status (focus movement status).
func (s *ImagingService) GetImagingStatus(ctx context.Context, videoSourceToken string) (map[string]interface{}, error) {
	if s.client.endpoints.Imaging == nil {
		return nil, fmt.Errorf("onvif: Imaging service not available")
	}

	body := fmt.Sprintf(`<GetStatus xmlns="http://www.onvif.org/ver20/imaging/wsdl">
  <VideoSourceToken>%s</VideoSourceToken>
</GetStatus>`, videoSourceToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Imaging.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetImagingStatus failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetStatusResponse"`
		Status  struct {
			FocusStatus20 struct {
				Position  float64 `xml:"Position"`
				MoveStatus string  `xml:"MoveStatus"`
				Error     string  `xml:"Error"`
			} `xml:"FocusStatus20"`
		} `xml:"Status"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"position":    result.Status.FocusStatus20.Position,
		"move_status": result.Status.FocusStatus20.MoveStatus,
		"error":       result.Status.FocusStatus20.Error,
	}, nil
}

// GetImagingServiceCapabilities retrieves imaging service capabilities.
func (s *ImagingService) GetImagingServiceCapabilities(ctx context.Context) (map[string]bool, error) {
	if s.client.endpoints.Imaging == nil {
		return nil, fmt.Errorf("onvif: Imaging service not available")
	}

	body := `<GetServiceCapabilities xmlns="http://www.onvif.org/ver20/imaging/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Imaging.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetImagingServiceCapabilities failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetServiceCapabilitiesResponse"`
		Caps    struct {
			ImageStabilization bool `xml:"ImageStabilization,attr"`
			Preset             bool `xml:"Preset,attr"`
		} `xml:"Capabilities"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return map[string]bool{
		"image_stabilization": result.Caps.ImageStabilization,
		"preset":              result.Caps.Preset,
	}, nil
}
