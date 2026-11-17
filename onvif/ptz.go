package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
)

// PTZService handles PTZ operations.
type PTZService struct {
	client *Client
}

// NewPTZService creates a new PTZ service.
func NewPTZService(client *Client) *PTZService {
	return &PTZService{client: client}
}

// GetStatus retrieves PTZ status for a profile.
func (s *PTZService) GetStatus(ctx context.Context, profileToken string) (*PTZStatus, error) {
	if s.client.endpoints.PTZ == nil {
		return nil, fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<GetStatus xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
</GetStatus>`, profileToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetStatus failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetStatusResponse"`
		Status  struct {
			Position struct {
				PanTilt struct {
					X float64 `xml:"x,attr"`
					Y float64 `xml:"y,attr"`
				} `xml:"PanTilt"`
				Zoom struct {
					X float64 `xml:"x,attr"`
				} `xml:"Zoom"`
			} `xml:"Position"`
			MoveStatus struct {
				PanTilt string `xml:"PanTilt"`
				Zoom    string `xml:"Zoom"`
			} `xml:"MoveStatus"`
			Error string `xml:"Error"`
		} `xml:"Status"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	status := &PTZStatus{
		Position: PTZPosition{
			PanTilt: Vector2D{
				X: result.Status.Position.PanTilt.X,
				Y: result.Status.Position.PanTilt.Y,
			},
			Zoom: Vector1D{
				X: result.Status.Position.Zoom.X,
			},
		},
		Moving: result.Status.MoveStatus.PanTilt == "MOVING" || result.Status.MoveStatus.Zoom == "MOVING",
		Error:  result.Status.Error,
	}

	return status, nil
}

// ContinuousMove starts continuous PTZ movement.
func (s *PTZService) ContinuousMove(ctx context.Context, profileToken string, velocity PTZVelocity) error {
	if s.client.endpoints.PTZ == nil {
		return fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<ContinuousMove xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
  <Velocity>
    <PanTilt x="%f" y="%f" xmlns="http://www.onvif.org/ver10/schema"/>
    <Zoom x="%f" xmlns="http://www.onvif.org/ver10/schema"/>
  </Velocity>
</ContinuousMove>`, profileToken, velocity.PanTilt.X, velocity.PanTilt.Y, velocity.Zoom.X)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: ContinuousMove failed: %w", err)
	}

	return nil
}

// Stop stops PTZ movement.
func (s *PTZService) Stop(ctx context.Context, profileToken string, stopPanTilt, stopZoom bool) error {
	if s.client.endpoints.PTZ == nil {
		return fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<Stop xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
  <PanTilt>%t</PanTilt>
  <Zoom>%t</Zoom>
</Stop>`, profileToken, stopPanTilt, stopZoom)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: Stop failed: %w", err)
	}

	return nil
}

// AbsoluteMove moves to an absolute position.
func (s *PTZService) AbsoluteMove(ctx context.Context, profileToken string, position PTZPosition) error {
	if s.client.endpoints.PTZ == nil {
		return fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<AbsoluteMove xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
  <Position>
    <PanTilt x="%f" y="%f" xmlns="http://www.onvif.org/ver10/schema"/>
    <Zoom x="%f" xmlns="http://www.onvif.org/ver10/schema"/>
  </Position>
</AbsoluteMove>`, profileToken, position.PanTilt.X, position.PanTilt.Y, position.Zoom.X)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: AbsoluteMove failed: %w", err)
	}

	return nil
}

// RelativeMove moves relative to current position.
func (s *PTZService) RelativeMove(ctx context.Context, profileToken string, displacement PTZPosition) error {
	if s.client.endpoints.PTZ == nil {
		return fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<RelativeMove xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
  <Translation>
    <PanTilt x="%f" y="%f" xmlns="http://www.onvif.org/ver10/schema"/>
    <Zoom x="%f" xmlns="http://www.onvif.org/ver10/schema"/>
  </Translation>
</RelativeMove>`, profileToken, displacement.PanTilt.X, displacement.PanTilt.Y, displacement.Zoom.X)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: RelativeMove failed: %w", err)
	}

	return nil
}

// GetPresets retrieves PTZ presets.
func (s *PTZService) GetPresets(ctx context.Context, profileToken string) ([]PTZPreset, error) {
	if s.client.endpoints.PTZ == nil {
		return nil, fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<GetPresets xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
</GetPresets>`, profileToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetPresets failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetPresetsResponse"`
		Presets []struct {
			Token    string `xml:"token,attr"`
			Name     string `xml:"Name"`
			Position struct {
				PanTilt struct {
					X float64 `xml:"x,attr"`
					Y float64 `xml:"y,attr"`
				} `xml:"PanTilt"`
				Zoom struct {
					X float64 `xml:"x,attr"`
				} `xml:"Zoom"`
			} `xml:"PTZPosition"`
		} `xml:"Preset"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	presets := make([]PTZPreset, 0, len(result.Presets))
	for _, p := range result.Presets {
		presets = append(presets, PTZPreset{
			Token: p.Token,
			Name:  p.Name,
			Position: PTZPosition{
				PanTilt: Vector2D{
					X: p.Position.PanTilt.X,
					Y: p.Position.PanTilt.Y,
				},
				Zoom: Vector1D{
					X: p.Position.Zoom.X,
				},
			},
		})
	}

	return presets, nil
}

// GotoPreset moves to a preset.
func (s *PTZService) GotoPreset(ctx context.Context, profileToken, presetToken string) error {
	if s.client.endpoints.PTZ == nil {
		return fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<GotoPreset xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
  <PresetToken>%s</PresetToken>
</GotoPreset>`, profileToken, presetToken)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: GotoPreset failed: %w", err)
	}

	return nil
}

// SetPreset creates or updates a preset.
func (s *PTZService) SetPreset(ctx context.Context, profileToken, presetName string) (string, error) {
	if s.client.endpoints.PTZ == nil {
		return "", fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<SetPreset xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
  <PresetName>%s</PresetName>
</SetPreset>`, profileToken, presetName)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return "", fmt.Errorf("onvif: SetPreset failed: %w", err)
	}

	var result struct {
		XMLName     xml.Name `xml:"SetPresetResponse"`
		PresetToken string   `xml:"PresetToken"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return "", err
	}

	return result.PresetToken, nil
}

// RemovePreset removes a preset.
func (s *PTZService) RemovePreset(ctx context.Context, profileToken, presetToken string) error {
	if s.client.endpoints.PTZ == nil {
		return fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<RemovePreset xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
  <PresetToken>%s</PresetToken>
</RemovePreset>`, profileToken, presetToken)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: RemovePreset failed: %w", err)
	}

	return nil
}

// GotoHomePosition moves to the home position.
func (s *PTZService) GotoHomePosition(ctx context.Context, profileToken string) error {
	if s.client.endpoints.PTZ == nil {
		return fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<GotoHomePosition xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
</GotoHomePosition>`, profileToken)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: GotoHomePosition failed: %w", err)
	}

	return nil
}

// SetHomePosition sets the current position as home.
func (s *PTZService) SetHomePosition(ctx context.Context, profileToken string) error {
	if s.client.endpoints.PTZ == nil {
		return fmt.Errorf("onvif: PTZ service not available")
	}

	body := fmt.Sprintf(`<SetHomePosition xmlns="http://www.onvif.org/ver20/ptz/wsdl">
  <ProfileToken>%s</ProfileToken>
</SetHomePosition>`, profileToken)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.PTZ.String(),
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("onvif: SetHomePosition failed: %w", err)
	}

	return nil
}
