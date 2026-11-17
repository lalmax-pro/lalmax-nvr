package onvif

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	media1Namespace = "http://www.onvif.org/ver10/media/wsdl"
	media2Namespace = "http://www.onvif.org/ver20/media/wsdl"
	schemaNamespace = "http://www.onvif.org/ver10/schema"
)

// MediaService handles media operations.
type MediaService struct {
	client *Client
}

// NewMediaService creates a new media service.
func NewMediaService(client *Client) *MediaService {
	return &MediaService{client: client}
}

// GetProfiles retrieves media profiles.
func (s *MediaService) GetProfiles(ctx context.Context) ([]MediaProfile, error) {
	var media2Err error
	if s.client.endpoints.Media2 != nil {
		profiles, err := s.getMedia2Profiles(ctx)
		if err == nil && (len(profiles) > 0 || s.client.endpoints.Media == nil) {
			return profiles, nil
		}
		media2Err = err
	}
	if s.client.endpoints.Media != nil && ctx.Err() == nil {
		profiles, err := s.getMedia1Profiles(ctx)
		if err == nil {
			return profiles, nil
		}
		return nil, errors.Join(media2Err, err)
	}
	if media2Err != nil {
		return nil, media2Err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("onvif: no media service available")
}

// getMedia1Profiles retrieves profiles using Media1 service.
func (s *MediaService) getMedia1Profiles(ctx context.Context) ([]MediaProfile, error) {
	body := `<GetProfiles xmlns="http://www.onvif.org/ver10/media/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Media.String(),
		Action:     media1Namespace + "/GetProfiles",
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetProfiles failed: %w", err)
	}

	var result struct {
		XMLName  xml.Name `xml:"GetProfilesResponse"`
		Profiles []struct {
			Token                    string `xml:"token,attr"`
			Name                     string `xml:"Name"`
			VideoSourceConfiguration struct {
				Token       string `xml:"token,attr"`
				SourceToken string `xml:"SourceToken"`
			} `xml:"VideoSourceConfiguration"`
			VideoEncoderConfiguration struct {
				Token      string `xml:"token,attr"`
				Encoding   string `xml:"Encoding"`
				Resolution struct {
					Width  int `xml:"Width"`
					Height int `xml:"Height"`
				} `xml:"Resolution"`
				RateControl struct {
					FrameRateLimit float64 `xml:"FrameRateLimit"`
					BitrateLimit   int     `xml:"BitrateLimit"`
				} `xml:"RateControl"`
			} `xml:"VideoEncoderConfiguration"`
			AudioSourceConfiguration struct {
				Token string `xml:"token,attr"`
			} `xml:"AudioSourceConfiguration"`
			AudioEncoderConfiguration struct {
				Token string `xml:"token,attr"`
			} `xml:"AudioEncoderConfiguration"`
		} `xml:"Profiles"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	profiles := make([]MediaProfile, 0, len(result.Profiles))
	for _, p := range result.Profiles {
		profile := MediaProfile{
			Token:            p.Token,
			Name:             p.Name,
			VideoSource:      p.VideoSourceConfiguration.Token,
			VideoSourceToken: p.VideoSourceConfiguration.SourceToken,
			VideoEncoder:     p.VideoEncoderConfiguration.Token,
			AudioSource:      p.AudioSourceConfiguration.Token,
			AudioEncoder:     p.AudioEncoderConfiguration.Token,
			Encoding:         p.VideoEncoderConfiguration.Encoding,
			Resolution: Resolution{
				Width:  p.VideoEncoderConfiguration.Resolution.Width,
				Height: p.VideoEncoderConfiguration.Resolution.Height,
			},
			Framerate: p.VideoEncoderConfiguration.RateControl.FrameRateLimit,
			Bitrate:   p.VideoEncoderConfiguration.RateControl.BitrateLimit,
		}
		profiles = append(profiles, profile)
	}

	return profiles, nil
}

// getMedia2Profiles retrieves profiles using Media2 service (Profile T).
//
// NOTE: The Media2 (ver20) schema differs from Media1: each profile's
// configurations are nested under a <Configurations> wrapper and the encoder
// element is named <VideoEncoder> (not <VideoEncoderConfiguration>). Parsing
// must follow Profiles > Configurations > VideoEncoder > Encoding, otherwise
// Encoding/Resolution come back empty for Profile T devices.
func (s *MediaService) getMedia2Profiles(ctx context.Context) ([]MediaProfile, error) {
	body := `<GetProfiles xmlns="http://www.onvif.org/ver20/media/wsdl">
  <Type>All</Type>
</GetProfiles>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Media2.String(),
		Action:     media2Namespace + "/GetProfiles",
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetProfiles (Media2) failed: %w", err)
	}

	var result struct {
		XMLName  xml.Name `xml:"GetProfilesResponse"`
		Profiles []struct {
			Token          string `xml:"token,attr"`
			Name           string `xml:"Name"`
			Configurations struct {
				VideoSource struct {
					Token       string `xml:"token,attr"`
					SourceToken string `xml:"SourceToken"`
				} `xml:"VideoSource"`
				VideoEncoder struct {
					Token      string `xml:"token,attr"`
					Encoding   string `xml:"Encoding"`
					Resolution struct {
						Width  int `xml:"Width"`
						Height int `xml:"Height"`
					} `xml:"Resolution"`
					RateControl struct {
						FrameRateLimit float64 `xml:"FrameRateLimit"`
						BitrateLimit   int     `xml:"BitrateLimit"`
					} `xml:"RateControl"`
				} `xml:"VideoEncoder"`
				AudioSource struct {
					Token string `xml:"token,attr"`
				} `xml:"AudioSource"`
				AudioEncoder struct {
					Token string `xml:"token,attr"`
				} `xml:"AudioEncoder"`
			} `xml:"Configurations"`
		} `xml:"Profiles"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	profiles := make([]MediaProfile, 0, len(result.Profiles))
	for _, p := range result.Profiles {
		cfg := p.Configurations
		profile := MediaProfile{
			Token:            p.Token,
			Name:             p.Name,
			VideoSource:      cfg.VideoSource.Token,
			VideoSourceToken: cfg.VideoSource.SourceToken,
			VideoEncoder:     cfg.VideoEncoder.Token,
			AudioSource:      cfg.AudioSource.Token,
			AudioEncoder:     cfg.AudioEncoder.Token,
			Encoding:         cfg.VideoEncoder.Encoding,
			Resolution: Resolution{
				Width:  cfg.VideoEncoder.Resolution.Width,
				Height: cfg.VideoEncoder.Resolution.Height,
			},
			Framerate: cfg.VideoEncoder.RateControl.FrameRateLimit,
			Bitrate:   cfg.VideoEncoder.RateControl.BitrateLimit,
		}
		profiles = append(profiles, profile)
	}

	return profiles, nil
}

// StreamTransport specifies the ONVIF transport used to obtain a stream URI.
type StreamTransport string

const (
	StreamOverTCP  StreamTransport = "RTSP"
	StreamOverUDP  StreamTransport = "RtspUnicast"
	StreamOverHTTP StreamTransport = "RtspOverHttp"
)

// GetStreamURI retrieves an RTSP stream URI, preferring TCP and falling back to UDP.
func (s *MediaService) GetStreamURI(ctx context.Context, profileToken string) (string, error) {
	uri, err := s.GetStreamURIForTransport(ctx, profileToken, StreamOverTCP)
	if err == nil || ctx.Err() != nil || !streamTransportUnsupported(err) {
		return uri, err
	}
	return s.GetStreamURIForTransport(ctx, profileToken, StreamOverUDP)
}

func streamTransportUnsupported(err error) bool {
	var fault *SOAPFault
	return operationUnsupported(err) || (errors.As(err, &fault) &&
		(fault.hasSubcode("InvalidStreamSetup") || fault.hasSubcode("StreamConflict") || fault.hasSubcode("InvalidProtocol")))
}

// GetStreamURIForTransport retrieves a stream URI for a specific transport.
func (s *MediaService) GetStreamURIForTransport(ctx context.Context, profileToken string, transport StreamTransport) (string, error) {
	var media1Protocol string
	switch transport {
	case StreamOverTCP:
		media1Protocol = "RTSP"
	case StreamOverUDP:
		media1Protocol = "UDP"
	case StreamOverHTTP:
		media1Protocol = "HTTP"
	default:
		return "", fmt.Errorf("onvif: unsupported stream transport %q", transport)
	}

	var media2Err error
	if s.client.endpoints.Media2 != nil {
		body := fmt.Sprintf(`<GetStreamUri xmlns="%s"><Protocol>%s</Protocol><ProfileToken>%s</ProfileToken></GetStreamUri>`,
			media2Namespace, transport, escapeXML(profileToken))
		uri, err := s.getStreamURI(ctx, s.client.endpoints.Media2, media2Namespace, body)
		if err == nil {
			return uri, nil
		}
		media2Err = err
	}
	if s.client.endpoints.Media != nil && ctx.Err() == nil {
		body := fmt.Sprintf(`<GetStreamUri xmlns="%s" xmlns:tt="%s">
          <StreamSetup><tt:Stream>RTP-Unicast</tt:Stream><tt:Transport><tt:Protocol>%s</tt:Protocol></tt:Transport></StreamSetup>
          <ProfileToken>%s</ProfileToken>
        </GetStreamUri>`, media1Namespace, schemaNamespace, media1Protocol, escapeXML(profileToken))
		uri, err := s.getStreamURI(ctx, s.client.endpoints.Media, media1Namespace, body)
		if err == nil {
			return uri, nil
		}
		return "", errors.Join(media2Err, err)
	}
	if media2Err != nil {
		return "", media2Err
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return "", fmt.Errorf("onvif: no media service available")
}

func (s *MediaService) getStreamURI(ctx context.Context, endpoint *url.URL, namespace, body string) (string, error) {
	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: endpoint.String(),
		Action:     namespace + "/GetStreamUri",
		Body:       body,
	})
	if err != nil {
		return "", fmt.Errorf("onvif: GetStreamUri failed: %w", err)
	}
	var result struct {
		XMLName  xml.Name `xml:"GetStreamUriResponse"`
		URI      string   `xml:"Uri"`
		MediaURI string   `xml:"MediaUri>Uri"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return "", err
	}
	uri := strings.TrimSpace(result.URI)
	if uri == "" {
		uri = strings.TrimSpace(result.MediaURI)
	}
	if uri == "" {
		return "", fmt.Errorf("onvif: empty stream URI")
	}
	return uri, nil
}
