package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
)

// MediaService (snapshot part) - GetSnapshotUri

// GetSnapshotUri retrieves the snapshot URI for a profile.
// Profile T devices expose the snapshot on Media2; Media1 is the fallback.
// A substream profile often has no snapshot, so an empty URI is an error and
// the caller can try another profile.
func (s *MediaService) GetSnapshotUri(ctx context.Context, profileToken string) (string, error) {
	var lastErr error
	if s.client.endpoints.Media2 != nil {
		uri, err := s.getMedia2SnapshotURI(ctx, profileToken)
		if err == nil && uri != "" {
			return uri, nil
		}
		lastErr = err
	}
	if s.client.endpoints.Media != nil {
		uri, err := s.getMedia1SnapshotURI(ctx, profileToken)
		if err == nil && uri != "" {
			return uri, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("onvif: Media service not available")
}

func (s *MediaService) getMedia2SnapshotURI(ctx context.Context, profileToken string) (string, error) {
	body := fmt.Sprintf(`<GetSnapshotUri xmlns="http://www.onvif.org/ver20/media/wsdl">
  <ProfileToken>%s</ProfileToken>
</GetSnapshotUri>`, profileToken)

	resp, err := s.client.soap.Send(&SOAPRequest{
		ServiceURL: s.client.endpoints.Media2.String(),
		Body:       body,
	})
	if err != nil {
		return "", fmt.Errorf("onvif: GetSnapshotUri (Media2) failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetSnapshotUriResponse"`
		Uri     string   `xml:"Uri"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return "", err
	}
	if result.Uri == "" {
		return "", fmt.Errorf("onvif: empty snapshot uri")
	}
	return result.Uri, nil
}

func (s *MediaService) getMedia1SnapshotURI(ctx context.Context, profileToken string) (string, error) {
	body := fmt.Sprintf(`<GetSnapshotUri xmlns="http://www.onvif.org/ver10/media/wsdl">
  <ProfileToken>%s</ProfileToken>
</GetSnapshotUri>`, profileToken)

	resp, err := s.client.soap.Send(&SOAPRequest{
		ServiceURL: s.client.endpoints.Media.String(),
		Body:       body,
	})
	if err != nil {
		return "", fmt.Errorf("onvif: GetSnapshotUri failed: %w", err)
	}

	var result struct {
		XMLName  xml.Name `xml:"GetSnapshotUriResponse"`
		MediaUri struct {
			Uri string `xml:"Uri"`
		} `xml:"MediaUri"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return "", err
	}
	if result.MediaUri.Uri == "" {
		return "", fmt.Errorf("onvif: empty snapshot uri")
	}
	return result.MediaUri.Uri, nil
}
