package onvif

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// --- Video Source Configuration ---

// VideoSourceConfiguration represents a video source configuration.
type VideoSourceConfiguration struct {
	Token       string `xml:"token,attr" json:"token"`
	Name        string `json:"name"`
	UseCount    int    `json:"use_count"`
	SourceToken string `json:"source_token"`
	Bounds      Rectangle `json:"bounds"`
}

// Rectangle represents a rectangular area.
type Rectangle struct {
	X      int `xml:"x,attr" json:"x"`
	Y      int `xml:"y,attr" json:"y"`
	Width  int `xml:"width,attr" json:"width"`
	Height int `xml:"height,attr" json:"height"`
}

// GetVideoSources retrieves video sources from the device.
func (s *MediaService) GetVideoSources(ctx context.Context) ([]VideoSource, error) {
	endpoint := s.mediaEndpoint()
	body := `<GetVideoSources xmlns="http://www.onvif.org/ver10/media/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetVideoSources failed: %w", err)
	}

	var result struct {
		XMLName     xml.Name `xml:"GetVideoSourcesResponse"`
		VideoSources []struct {
			Token       string `xml:"token,attr"`
			Framerate   float64 `xml:"Framerate"`
			Resolution  struct {
				Width  int `xml:"Width"`
				Height int `xml:"Height"`
			} `xml:"Resolution"`
		} `xml:"VideoSources"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	sources := make([]VideoSource, 0, len(result.VideoSources))
	for _, vs := range result.VideoSources {
		sources = append(sources, VideoSource{
			Token:     vs.Token,
			Framerate: vs.Framerate,
			Resolution: Resolution{Width: vs.Resolution.Width, Height: vs.Resolution.Height},
		})
	}
	return sources, nil
}

// VideoSource represents a video source.
type VideoSource struct {
	Token      string     `json:"token"`
	Framerate  float64    `json:"framerate"`
	Resolution Resolution `json:"resolution"`
}

// GetVideoSourceConfigurations retrieves video source configurations.
func (s *MediaService) GetVideoSourceConfigurations(ctx context.Context) ([]VideoSourceConfiguration, error) {
	resp, _, err := s.readMediaConfiguration(ctx, "GetVideoSourceConfigurations", "")
	if err != nil {
		return nil, fmt.Errorf("onvif: GetVideoSourceConfigurations failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetVideoSourceConfigurationsResponse"`
		Configs []struct {
			Token       string `xml:"token,attr"`
			Name        string `xml:"Name"`
			UseCount    int    `xml:"UseCount"`
			SourceToken string `xml:"SourceToken"`
			Bounds      struct {
				X      int `xml:"x,attr"`
				Y      int `xml:"y,attr"`
				Width  int `xml:"width,attr"`
				Height int `xml:"height,attr"`
			} `xml:"Bounds"`
		} `xml:"Configurations"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	configs := make([]VideoSourceConfiguration, 0, len(result.Configs))
	for _, c := range result.Configs {
		configs = append(configs, VideoSourceConfiguration{
			Token:       c.Token,
			Name:        c.Name,
			UseCount:    c.UseCount,
			SourceToken: c.SourceToken,
			Bounds:      Rectangle{X: c.Bounds.X, Y: c.Bounds.Y, Width: c.Bounds.Width, Height: c.Bounds.Height},
		})
	}
	return configs, nil
}

// --- Video Encoder Configuration ---

// VideoEncoderConfiguration represents a video encoder configuration.
type VideoEncoderConfiguration struct {
	Token          string     `json:"token"`
	Name           string     `json:"name"`
	UseCount       int        `json:"use_count"`
	Encoding       string     `json:"encoding"`
	Resolution     Resolution `json:"resolution"`
	Quality        float64    `json:"quality"`
	FramerateLimit float64    `json:"framerate_limit"`
	BitrateLimit   int        `json:"bitrate_limit"`
	GovLength      int        `json:"gov_length"`
	Profile        string     `json:"profile"`
}

// GetVideoEncoderConfigurations retrieves video encoder configurations.
func (s *MediaService) GetVideoEncoderConfigurations(ctx context.Context) ([]VideoEncoderConfiguration, error) {
	resp, _, err := s.readMediaConfiguration(ctx, "GetVideoEncoderConfigurations", "")
	if err != nil {
		return nil, fmt.Errorf("onvif: GetVideoEncoderConfigurations failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetVideoEncoderConfigurationsResponse"`
		Configs []struct {
			Token      string `xml:"token,attr"`
			Name       string `xml:"Name"`
			UseCount   int    `xml:"UseCount"`
			Encoding   string `xml:"Encoding"`
			Resolution struct {
				Width  int `xml:"Width"`
				Height int `xml:"Height"`
			} `xml:"Resolution"`
			Quality     float64 `xml:"Quality"`
			RateControl struct {
				FrameRateLimit float64 `xml:"FrameRateLimit"`
				BitrateLimit   int     `xml:"BitrateLimit"`
			} `xml:"RateControl"`
			GovLength int    `xml:"GovLength,attr"`
			Profile   string `xml:"Profile,attr"`
			H264      struct {
				GovLength int    `xml:"GovLength"`
				Profile   string `xml:"H264Profile"`
			} `xml:"H264"`
		} `xml:"Configurations"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	configs := make([]VideoEncoderConfiguration, 0, len(result.Configs))
	for _, c := range result.Configs {
		if c.GovLength == 0 {
			c.GovLength = c.H264.GovLength
		}
		if c.Profile == "" {
			c.Profile = c.H264.Profile
		}
		configs = append(configs, VideoEncoderConfiguration{
			Token:          c.Token,
			Name:           c.Name,
			UseCount:       c.UseCount,
			Encoding:       c.Encoding,
			Resolution:     Resolution{Width: c.Resolution.Width, Height: c.Resolution.Height},
			Quality:        c.Quality,
			FramerateLimit: c.RateControl.FrameRateLimit,
			BitrateLimit:   c.RateControl.BitrateLimit,
			GovLength:      c.GovLength,
			Profile:        c.Profile,
		})
	}
	return configs, nil
}

// GetVideoEncoderConfigurationOptions retrieves options for video encoder configuration.
func (s *MediaService) GetVideoEncoderConfigurationOptions(ctx context.Context, profileToken string) (map[string]interface{}, error) {
	var token string
	if profileToken != "" {
		token = "<ProfileToken>" + escapeXML(profileToken) + "</ProfileToken>"
	}
	resp, namespace, err := s.readMediaConfiguration(ctx, "GetVideoEncoderConfigurationOptions", token)
	if err != nil {
		return nil, err
	}
	if namespace == media2Namespace {
		return parseMedia2EncoderOptions(resp.Body)
	}

	var result struct {
		XMLName xml.Name `xml:"GetVideoEncoderConfigurationOptionsResponse"`
		Options struct {
			QualityRange struct {
				Min int `xml:"Min"`
				Max int `xml:"Max"`
			} `xml:"QualityRange"`
			ResolutionsAvailable []struct {
				Width  int `xml:"Width"`
				Height int `xml:"Height"`
			} `xml:"ResolutionsAvailable"`
			FrameRateRange struct {
				Min int `xml:"Min"`
				Max int `xml:"Max"`
			} `xml:"FrameRateRange"`
			EncodingIntervalRange struct {
				Min int `xml:"Min"`
				Max int `xml:"Max"`
			} `xml:"EncodingIntervalRange"`
		} `xml:"Options"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	resolutions := make([]Resolution, 0, len(result.Options.ResolutionsAvailable))
	for _, r := range result.Options.ResolutionsAvailable {
		resolutions = append(resolutions, Resolution{Width: r.Width, Height: r.Height})
	}

	return map[string]interface{}{
		"quality_range":     Range{Min: float64(result.Options.QualityRange.Min), Max: float64(result.Options.QualityRange.Max)},
		"resolutions":       resolutions,
		"framerate_range":   Range{Min: float64(result.Options.FrameRateRange.Min), Max: float64(result.Options.FrameRateRange.Max)},
		"encoding_interval": Range{Min: float64(result.Options.EncodingIntervalRange.Min), Max: float64(result.Options.EncodingIntervalRange.Max)},
	}, nil
}

// SetVideoEncoderConfiguration updates a video encoder configuration.
func (s *MediaService) SetVideoEncoderConfiguration(ctx context.Context, config VideoEncoderConfiguration) error {
	if endpoint := s.client.endpoints.Media2; endpoint != nil {
		err := s.setMedia2EncoderConfiguration(ctx, endpoint, config)
		if err == nil || s.client.endpoints.Media == nil || !operationUnsupported(err) {
			return err
		}
	}
	endpoint := s.mediaEndpoint()
	body := fmt.Sprintf(`<SetVideoEncoderConfiguration xmlns="http://www.onvif.org/ver10/media/wsdl">
  <Configuration token="%s">
    <Name>%s</Name>
    <UseCount>%d</UseCount>
    <Encoding>%s</Encoding>
    <Resolution>
      <Width>%d</Width>
      <Height>%d</Height>
    </Resolution>
    <Quality>%g</Quality>
    <RateControl>
      <FrameRateLimit>%d</FrameRateLimit>
      <BitrateLimit>%d</BitrateLimit>
    </RateControl>
  </Configuration>
  <ForcePersistence>true</ForcePersistence>
</SetVideoEncoderConfiguration>`,
		escapeXML(config.Token), escapeXML(config.Name), config.UseCount, escapeXML(config.Encoding),
		config.Resolution.Width, config.Resolution.Height, config.Quality,
		int(config.FramerateLimit), config.BitrateLimit)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: SetVideoEncoderConfiguration failed: %w", err)
	}
	return nil
}

// --- Audio Source Configuration ---

// AudioSourceConfiguration represents an audio source configuration.
type AudioSourceConfiguration struct {
	Token       string `json:"token"`
	Name        string `json:"name"`
	UseCount    int    `json:"use_count"`
	SourceToken string `json:"source_token"`
}

// GetAudioSources retrieves audio sources.
func (s *MediaService) GetAudioSources(ctx context.Context) ([]AudioSource, error) {
	endpoint := s.mediaEndpoint()
	body := `<GetAudioSources xmlns="http://www.onvif.org/ver10/media/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetAudioSources failed: %w", err)
	}

	var result struct {
		XMLName      xml.Name `xml:"GetAudioSourcesResponse"`
		AudioSources []struct {
			Token string `xml:"token,attr"`
		} `xml:"AudioSources"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	sources := make([]AudioSource, 0, len(result.AudioSources))
	for _, as := range result.AudioSources {
		sources = append(sources, AudioSource{Token: as.Token})
	}
	return sources, nil
}

// AudioSource represents an audio source.
type AudioSource struct {
	Token string `json:"token"`
}

// GetAudioSourceConfigurations retrieves audio source configurations.
func (s *MediaService) GetAudioSourceConfigurations(ctx context.Context) ([]AudioSourceConfiguration, error) {
	endpoint := s.mediaEndpoint()
	body := `<GetAudioSourceConfigurations xmlns="http://www.onvif.org/ver10/media/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetAudioSourceConfigurations failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetAudioSourceConfigurationsResponse"`
		Configs []struct {
			Token       string `xml:"token,attr"`
			Name        string `xml:"Name"`
			UseCount    int    `xml:"UseCount"`
			SourceToken string `xml:"SourceToken"`
		} `xml:"Configurations"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	configs := make([]AudioSourceConfiguration, 0, len(result.Configs))
	for _, c := range result.Configs {
		configs = append(configs, AudioSourceConfiguration{
			Token: c.Token, Name: c.Name, UseCount: c.UseCount, SourceToken: c.SourceToken,
		})
	}
	return configs, nil
}

// --- Audio Encoder Configuration ---

// AudioEncoderConfiguration represents an audio encoder configuration.
type AudioEncoderConfiguration struct {
	Token       string `json:"token"`
	Name        string `json:"name"`
	UseCount    int    `json:"use_count"`
	Encoding    string `json:"encoding"`
	Bitrate     int    `json:"bitrate"`
	SampleRate  int    `json:"sample_rate"`
}

// GetAudioEncoderConfigurations retrieves audio encoder configurations.
func (s *MediaService) GetAudioEncoderConfigurations(ctx context.Context) ([]AudioEncoderConfiguration, error) {
	endpoint := s.mediaEndpoint()
	body := `<GetAudioEncoderConfigurations xmlns="http://www.onvif.org/ver10/media/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetAudioEncoderConfigurations failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetAudioEncoderConfigurationsResponse"`
		Configs []struct {
			Token      string `xml:"token,attr"`
			Name       string `xml:"Name"`
			UseCount   int    `xml:"UseCount"`
			Encoding   string `xml:"Encoding"`
			Bitrate    int    `xml:"BitrateList"`
			SampleRate int    `xml:"SampleRateList"`
		} `xml:"Configurations"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	configs := make([]AudioEncoderConfiguration, 0, len(result.Configs))
	for _, c := range result.Configs {
		configs = append(configs, AudioEncoderConfiguration{
			Token: c.Token, Name: c.Name, UseCount: c.UseCount,
			Encoding: c.Encoding, Bitrate: c.Bitrate, SampleRate: c.SampleRate,
		})
	}
	return configs, nil
}

// GetAudioEncoderConfigurationOptions retrieves audio encoder configuration options.
func (s *MediaService) GetAudioEncoderConfigurationOptions(ctx context.Context) (map[string]interface{}, error) {
	endpoint := s.mediaEndpoint()
	body := `<GetAudioEncoderConfigurationOptions xmlns="http://www.onvif.org/ver10/media/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetAudioEncoderConfigurationOptions failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetAudioEncoderConfigurationOptionsResponse"`
		Options struct {
			Encoding       string   `xml:"Encoding"`
			BitrateList    []int    `xml:"BitrateList"`
			SampleRateList []int    `xml:"SampleRateList"`
		} `xml:"Options"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"encoding":     result.Options.Encoding,
		"bitrates":     result.Options.BitrateList,
		"sample_rates": result.Options.SampleRateList,
	}, nil
}

// --- Audio Output Configuration ---

// AudioOutputConfiguration represents an audio output configuration.
type AudioOutputConfiguration struct {
	Token        string `json:"token"`
	Name         string `json:"name"`
	UseCount     int    `json:"use_count"`
	OutputToken  string `json:"output_token"`
}

// GetAudioOutputs retrieves audio outputs.
func (s *MediaService) GetAudioOutputs(ctx context.Context) ([]AudioOutput, error) {
	endpoint := s.mediaEndpoint()
	body := `<GetAudioOutputs xmlns="http://www.onvif.org/ver10/media/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetAudioOutputs failed: %w", err)
	}

	var result struct {
		XMLName     xml.Name `xml:"GetAudioOutputsResponse"`
		AudioOutputs []struct {
			Token string `xml:"token,attr"`
		} `xml:"AudioOutputs"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	outputs := make([]AudioOutput, 0, len(result.AudioOutputs))
	for _, ao := range result.AudioOutputs {
		outputs = append(outputs, AudioOutput{Token: ao.Token})
	}
	return outputs, nil
}

// AudioOutput represents an audio output.
type AudioOutput struct {
	Token string `json:"token"`
}

// GetAudioOutputConfigurations retrieves audio output configurations.
func (s *MediaService) GetAudioOutputConfigurations(ctx context.Context) ([]AudioOutputConfiguration, error) {
	endpoint := s.mediaEndpoint()
	body := `<GetAudioOutputConfigurations xmlns="http://www.onvif.org/ver10/media/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetAudioOutputConfigurations failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetAudioOutputConfigurationsResponse"`
		Configs []struct {
			Token       string `xml:"token,attr"`
			Name        string `xml:"Name"`
			UseCount    int    `xml:"UseCount"`
			OutputToken string `xml:"OutputToken"`
		} `xml:"Configurations"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	configs := make([]AudioOutputConfiguration, 0, len(result.Configs))
	for _, c := range result.Configs {
		configs = append(configs, AudioOutputConfiguration{
			Token: c.Token, Name: c.Name, UseCount: c.UseCount, OutputToken: c.OutputToken,
		})
	}
	return configs, nil
}

// --- Profile CRUD ---

// CreateProfile creates a new media profile.
func (s *MediaService) CreateProfile(ctx context.Context, name string) (*MediaProfile, error) {
	endpoint := s.mediaEndpoint()
	body := fmt.Sprintf(`<CreateProfile xmlns="http://www.onvif.org/ver10/media/wsdl">
  <Name>%s</Name>
</CreateProfile>`, name)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: CreateProfile failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"CreateProfileResponse"`
		Profile struct {
			Token string `xml:"token,attr"`
			Name  string `xml:"Name"`
		} `xml:"Profile"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return &MediaProfile{Token: result.Profile.Token, Name: result.Profile.Name}, nil
}

// DeleteProfile deletes a media profile.
func (s *MediaService) DeleteProfile(ctx context.Context, profileToken string) error {
	endpoint := s.mediaEndpoint()
	body := fmt.Sprintf(`<DeleteProfile xmlns="http://www.onvif.org/ver10/media/wsdl">
  <ProfileToken>%s</ProfileToken>
</DeleteProfile>`, profileToken)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: DeleteProfile failed: %w", err)
	}
	return nil
}

// --- Add/Remove Configuration to Profile ---

// AddVideoSourceConfiguration adds a video source configuration to a profile.
func (s *MediaService) AddVideoSourceConfiguration(ctx context.Context, profileToken, configToken string) error {
	return s.addConfigToProfile(ctx, profileToken, configToken, "AddVideoSourceConfiguration")
}

// AddVideoEncoderConfiguration adds a video encoder configuration to a profile.
func (s *MediaService) AddVideoEncoderConfiguration(ctx context.Context, profileToken, configToken string) error {
	return s.addConfigToProfile(ctx, profileToken, configToken, "AddVideoEncoderConfiguration")
}

// AddAudioSourceConfiguration adds an audio source configuration to a profile.
func (s *MediaService) AddAudioSourceConfiguration(ctx context.Context, profileToken, configToken string) error {
	return s.addConfigToProfile(ctx, profileToken, configToken, "AddAudioSourceConfiguration")
}

// AddAudioEncoderConfiguration adds an audio encoder configuration to a profile.
func (s *MediaService) AddAudioEncoderConfiguration(ctx context.Context, profileToken, configToken string) error {
	return s.addConfigToProfile(ctx, profileToken, configToken, "AddAudioEncoderConfiguration")
}

// RemoveVideoSourceConfiguration removes a video source configuration from a profile.
func (s *MediaService) RemoveVideoSourceConfiguration(ctx context.Context, profileToken, configToken string) error {
	return s.removeConfigFromProfile(ctx, profileToken, configToken, "RemoveVideoSourceConfiguration")
}

// RemoveVideoEncoderConfiguration removes a video encoder configuration from a profile.
func (s *MediaService) RemoveVideoEncoderConfiguration(ctx context.Context, profileToken, configToken string) error {
	return s.removeConfigFromProfile(ctx, profileToken, configToken, "RemoveVideoEncoderConfiguration")
}

// RemoveAudioSourceConfiguration removes an audio source configuration from a profile.
func (s *MediaService) RemoveAudioSourceConfiguration(ctx context.Context, profileToken, configToken string) error {
	return s.removeConfigFromProfile(ctx, profileToken, configToken, "RemoveAudioSourceConfiguration")
}

// RemoveAudioEncoderConfiguration removes an audio encoder configuration from a profile.
func (s *MediaService) RemoveAudioEncoderConfiguration(ctx context.Context, profileToken, configToken string) error {
	return s.removeConfigFromProfile(ctx, profileToken, configToken, "RemoveAudioEncoderConfiguration")
}

func (s *MediaService) addConfigToProfile(ctx context.Context, profileToken, configToken, operation string) error {
	endpoint := s.mediaEndpoint()
	body := fmt.Sprintf(`<%s xmlns="http://www.onvif.org/ver10/media/wsdl">
  <ProfileToken>%s</ProfileToken>
  <ConfigurationToken>%s</ConfigurationToken>
</%s>`, operation, profileToken, configToken, operation)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	return err
}

func (s *MediaService) removeConfigFromProfile(ctx context.Context, profileToken, configToken, operation string) error {
	endpoint := s.mediaEndpoint()
	body := fmt.Sprintf(`<%s xmlns="http://www.onvif.org/ver10/media/wsdl">
  <ProfileToken>%s</ProfileToken>
  <ConfigurationToken>%s</ConfigurationToken>
</%s>`, operation, profileToken, configToken, operation)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	return err
}

// --- SetSynchronizationPoint ---

// SetSynchronizationPoint sets a synchronization point for a profile.
func (s *MediaService) SetSynchronizationPoint(ctx context.Context, profileToken string) error {
	endpoint := s.mediaEndpoint()
	body := fmt.Sprintf(`<SetSynchronizationPoint xmlns="http://www.onvif.org/ver10/media/wsdl">
  <ProfileToken>%s</ProfileToken>
</SetSynchronizationPoint>`, profileToken)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	return err
}

// --- Media Service Capabilities ---

// GetMediaServiceCapabilities retrieves media service capabilities.
func (s *MediaService) GetMediaServiceCapabilities(ctx context.Context) (map[string]bool, error) {
	resp, _, err := s.readMediaConfiguration(ctx, "GetServiceCapabilities", "")
	if err != nil {
		return nil, fmt.Errorf("onvif: GetMediaServiceCapabilities failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetServiceCapabilitiesResponse"`
		Caps    struct {
			SnapshotUri      bool `xml:"SnapshotUri,attr"`
			Rotation         bool `xml:"Rotation,attr"`
			VideoSourceMode  bool `xml:"VideoSourceMode,attr"`
			OSD              bool `xml:"OSD,attr"`
			TemporaryOSDText bool `xml:"TemporaryOSDText,attr"`
			EXICompression   bool `xml:"EXICompression,attr"`
		} `xml:"Capabilities"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return map[string]bool{
		"snapshot_uri":       result.Caps.SnapshotUri,
		"rotation":           result.Caps.Rotation,
		"video_source_mode":  result.Caps.VideoSourceMode,
		"osd":                result.Caps.OSD,
		"temporary_osd_text": result.Caps.TemporaryOSDText,
		"exi_compression":    result.Caps.EXICompression,
	}, nil
}

func (s *MediaService) readMediaConfiguration(ctx context.Context, operation, params string) (*SOAPResponse, string, error) {
	var media2Err error
	if endpoint := s.client.endpoints.Media2; endpoint != nil {
		response, err := s.sendMediaConfiguration(ctx, endpoint.String(), media2Namespace, operation, params)
		if err == nil {
			return response, media2Namespace, nil
		}
		media2Err = err
		if s.client.endpoints.Media == nil || ctx.Err() != nil {
			return nil, "", err
		}
	}
	response, err := s.sendMediaConfiguration(ctx, s.mediaEndpoint(), media1Namespace, operation, params)
	if err != nil {
		return nil, "", errors.Join(media2Err, err)
	}
	return response, media1Namespace, nil
}

func (s *MediaService) sendMediaConfiguration(ctx context.Context, endpoint, namespace, operation, params string) (*SOAPResponse, error) {
	body := fmt.Sprintf(`<%s xmlns="%s">%s</%s>`, operation, namespace, params, operation)
	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: endpoint, Action: namespace + "/" + operation, Body: body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: %s failed: %w", operation, err)
	}
	return resp, nil
}

func (s *MediaService) setMedia2EncoderConfiguration(ctx context.Context, endpoint *url.URL, config VideoEncoderConfiguration) error {
	var attributes, rateControl string
	if config.GovLength > 0 {
		attributes += fmt.Sprintf(` GovLength="%d"`, config.GovLength)
	}
	if config.Profile != "" {
		attributes += ` Profile="` + escapeXML(config.Profile) + `"`
	}
	if config.FramerateLimit > 0 || config.BitrateLimit > 0 {
		rateControl = fmt.Sprintf(`<tt:RateControl><tt:FrameRateLimit>%g</tt:FrameRateLimit><tt:BitrateLimit>%d</tt:BitrateLimit></tt:RateControl>`,
			config.FramerateLimit, config.BitrateLimit)
	}
	params := fmt.Sprintf(`<Configuration xmlns:tt="%s" token="%s"%s>
      <tt:Name>%s</tt:Name><tt:UseCount>%d</tt:UseCount><tt:Encoding>%s</tt:Encoding>
      <tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution>
      %s<tt:Quality>%g</tt:Quality>
    </Configuration>`, schemaNamespace, escapeXML(config.Token), attributes, escapeXML(config.Name), config.UseCount,
		escapeXML(config.Encoding), config.Resolution.Width, config.Resolution.Height, rateControl, config.Quality)
	resp, err := s.sendMediaConfiguration(ctx, endpoint.String(), media2Namespace, "SetVideoEncoderConfiguration", params)
	if err != nil {
		return err
	}
	var result struct {
		XMLName xml.Name `xml:"SetVideoEncoderConfigurationResponse"`
	}
	return ParseResponseBody(resp.Body, &result)
}

func parseMedia2EncoderOptions(body []byte) (map[string]interface{}, error) {
	var result struct {
		XMLName xml.Name `xml:"GetVideoEncoderConfigurationOptionsResponse"`
		Options []struct {
			Encoding    string       `xml:"Encoding"`
			Quality     Range        `xml:"QualityRange"`
			Resolutions []Resolution `xml:"ResolutionsAvailable"`
			Bitrate     Range        `xml:"BitrateRange"`
			FrameRates  string       `xml:"FrameRatesSupported,attr"`
		} `xml:"Options"`
	}
	if err := ParseResponseBody(body, &result); err != nil {
		return nil, err
	}
	options := make([]map[string]interface{}, 0, len(result.Options))
	for _, value := range result.Options {
		var frames []float64
		for _, token := range strings.Fields(value.FrameRates) {
			if rate, err := strconv.ParseFloat(token, 64); err == nil {
				frames = append(frames, rate)
			}
		}
		item := map[string]interface{}{
			"encoding": value.Encoding, "quality_range": value.Quality,
			"resolutions": value.Resolutions, "bitrate_range": value.Bitrate, "frame_rates": frames,
		}
		if len(frames) > 0 {
			rateRange := Range{Min: frames[0], Max: frames[0]}
			for _, rate := range frames[1:] {
				rateRange.Min, rateRange.Max = min(rateRange.Min, rate), max(rateRange.Max, rate)
			}
			item["framerate_range"] = rateRange
		}
		options = append(options, item)
	}
	output := map[string]interface{}{"options": options}
	if len(options) == 1 {
		for key, value := range options[0] {
			output[key] = value
		}
	}
	return output, nil
}

func (s *MediaService) mediaEndpoint() string {
	if s.client.endpoints.Media != nil {
		return s.client.endpoints.Media.String()
	}
	return s.client.endpoint
}
