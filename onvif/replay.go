package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
)

// ReplayService handles replay operations.
type ReplayService struct {
	client *Client
}

// NewReplayService creates a new replay service.
func NewReplayService(client *Client) *ReplayService {
	return &ReplayService{client: client}
}

// GetReplayURI retrieves the replay URI for a recording.
func (s *ReplayService) GetReplayURI(ctx context.Context, recordingToken string) (string, error) {
	body := fmt.Sprintf(`<GetReplayUri xmlns="http://www.onvif.org/ver10/replay/wsdl">
  <StreamSetup>
    <Stream xmlns="http://www.onvif.org/ver10/schema">RTP-Unicast</Stream>
    <Transport xmlns="http://www.onvif.org/ver10/schema">
      <Protocol>RTSP</Protocol>
    </Transport>
  </StreamSetup>
  <RecordingToken>%s</RecordingToken>
</GetReplayUri>`, recordingToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Replay.String(),
		Body:       body,
	})
	if err != nil {
		return "", fmt.Errorf("onvif: GetReplayURI failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetReplayUriResponse"`
		Uri     string   `xml:"Uri"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return "", err
	}

	return result.Uri, nil
}
