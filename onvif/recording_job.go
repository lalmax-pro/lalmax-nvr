package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
)

// Recording Job Management operations.

// RecordingJob represents a recording job.
type RecordingJob struct {
	Token      string              `json:"token"`
	Recording  string              `json:"recording"`
	Mode       string              `json:"mode"` // Active, Idle, etc.
	Priority   int                 `json:"priority"`
	Source     *RecordingJobSource `json:"source,omitempty"`
}

// RecordingJobSource represents the source of a recording job.
type RecordingJobSource struct {
	SourceToken   string `json:"source_token"`
	AutoCreate    bool   `json:"auto_create"`
	Tracks        []RecordingJobTrack `json:"tracks"`
}

// RecordingJobTrack represents a track in a recording job.
type RecordingJobTrack struct {
	SourceTag string `json:"source_tag"`
	Destination string `json:"destination"`
}

// GetRecordingJobs retrieves all recording jobs.
func (s *RecordingService) GetRecordingJobs(ctx context.Context) ([]RecordingJob, error) {
	endpoint := s.client.endpoints.Search.String()
	body := `<GetRecordingJobs xmlns="http://www.onvif.org/ver10/recording/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetRecordingJobs failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetRecordingJobsResponse"`
		JobItem []struct {
			JobToken    string `xml:"JobToken"`
			JobConfig   struct {
				RecordingToken string `xml:"RecordingToken"`
				Mode           string `xml:"Mode"`
				Priority       int    `xml:"Priority"`
			} `xml:"JobConfiguration"`
		} `xml:"JobItem"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	jobs := make([]RecordingJob, 0, len(result.JobItem))
	for _, j := range result.JobItem {
		jobs = append(jobs, RecordingJob{
			Token:     j.JobToken,
			Recording: j.JobConfig.RecordingToken,
			Mode:      j.JobConfig.Mode,
			Priority:  j.JobConfig.Priority,
		})
	}
	return jobs, nil
}

// CreateRecordingJob creates a new recording job.
func (s *RecordingService) CreateRecordingJob(ctx context.Context, recordingToken, mode string, priority int) (*RecordingJob, error) {
	endpoint := s.client.endpoints.Search.String()
	body := fmt.Sprintf(`<CreateRecordingJob xmlns="http://www.onvif.org/ver10/recording/wsdl">
  <JobConfiguration>
    <RecordingToken>%s</RecordingToken>
    <Mode>%s</Mode>
    <Priority>%d</Priority>
  </JobConfiguration>
</CreateRecordingJob>`, recordingToken, mode, priority)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: CreateRecordingJob failed: %w", err)
	}

	var result struct {
		XMLName  xml.Name `xml:"CreateRecordingJobResponse"`
		JobToken string   `xml:"JobToken"`
		JobConfig struct {
			RecordingToken string `xml:"RecordingToken"`
			Mode           string `xml:"Mode"`
			Priority       int    `xml:"Priority"`
		} `xml:"JobConfiguration"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return &RecordingJob{
		Token:     result.JobToken,
		Recording: result.JobConfig.RecordingToken,
		Mode:      result.JobConfig.Mode,
		Priority:  result.JobConfig.Priority,
	}, nil
}

// DeleteRecordingJob deletes a recording job.
func (s *RecordingService) DeleteRecordingJob(ctx context.Context, jobToken string) error {
	endpoint := s.client.endpoints.Search.String()
	body := fmt.Sprintf(`<DeleteRecordingJob xmlns="http://www.onvif.org/ver10/recording/wsdl">
  <JobToken>%s</JobToken>
</DeleteRecordingJob>`, jobToken)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: DeleteRecordingJob failed: %w", err)
	}
	return nil
}

// GetRecordingJobState retrieves the state of a recording job.
func (s *RecordingService) GetRecordingJobState(ctx context.Context, jobToken string) (map[string]interface{}, error) {
	endpoint := s.client.endpoints.Search.String()
	body := fmt.Sprintf(`<GetRecordingJobState xmlns="http://www.onvif.org/ver10/recording/wsdl">
  <JobToken>%s</JobToken>
</GetRecordingJobState>`, jobToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetRecordingJobState failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetRecordingJobStateResponse"`
		State   struct {
			RecordingToken string `xml:"RecordingToken"`
			Mode           string `xml:"Mode"`
			Priority       int    `xml:"Priority"`
		} `xml:"State"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"recording": result.State.RecordingToken,
		"mode":      result.State.Mode,
		"priority":  result.State.Priority,
	}, nil
}

// SetRecordingJobMode sets the mode of a recording job (Active/Idle/Pause).
func (s *RecordingService) SetRecordingJobMode(ctx context.Context, jobToken, mode string) error {
	endpoint := s.client.endpoints.Search.String()
	body := fmt.Sprintf(`<SetRecordingJobMode xmlns="http://www.onvif.org/ver10/recording/wsdl">
  <JobToken>%s</JobToken>
  <Mode>%s</Mode>
</SetRecordingJobMode>`, jobToken, mode)

	_, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return fmt.Errorf("onvif: SetRecordingJobMode failed: %w", err)
	}
	return nil
}

// GetRecordingJobConfiguration retrieves the configuration of a recording job.
func (s *RecordingService) GetRecordingJobConfiguration(ctx context.Context, jobToken string) (*RecordingJob, error) {
	endpoint := s.client.endpoints.Search.String()
	body := fmt.Sprintf(`<GetRecordingJobConfiguration xmlns="http://www.onvif.org/ver10/recording/wsdl">
  <JobToken>%s</JobToken>
</GetRecordingJobConfiguration>`, jobToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetRecordingJobConfiguration failed: %w", err)
	}

	var result struct {
		XMLName  xml.Name `xml:"GetRecordingJobConfigurationResponse"`
		JobToken string   `xml:"JobToken"`
		JobConfig struct {
			RecordingToken string `xml:"RecordingToken"`
			Mode           string `xml:"Mode"`
			Priority       int    `xml:"Priority"`
		} `xml:"JobConfiguration"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return &RecordingJob{
		Token:     result.JobToken,
		Recording: result.JobConfig.RecordingToken,
		Mode:      result.JobConfig.Mode,
		Priority:  result.JobConfig.Priority,
	}, nil
}

// GetRecordingOptions retrieves recording options for a recording.
func (s *RecordingService) GetRecordingOptions(ctx context.Context, recordingToken string) (map[string]interface{}, error) {
	endpoint := s.client.endpoints.Search.String()
	body := fmt.Sprintf(`<GetRecordingOptions xmlns="http://www.onvif.org/ver10/recording/wsdl">
  <RecordingToken>%s</RecordingToken>
</GetRecordingOptions>`, recordingToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetRecordingOptions failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetRecordingOptionsResponse"`
		Options struct {
			Job []struct {
				Mode     string `xml:"Mode"`
				Priority int    `xml:"Priority"`
			} `xml:"Job"`
		} `xml:"Options"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	jobs := make([]map[string]interface{}, 0, len(result.Options.Job))
	for _, j := range result.Options.Job {
		jobs = append(jobs, map[string]interface{}{
			"mode":     j.Mode,
			"priority": j.Priority,
		})
	}

	return map[string]interface{}{
		"jobs": jobs,
	}, nil
}

// GetRecordingServiceCapabilities retrieves recording service capabilities.
func (s *RecordingService) GetRecordingServiceCapabilities(ctx context.Context) (map[string]bool, error) {
	if s.client.endpoints.Recording == nil {
		return nil, fmt.Errorf("onvif: Recording service not available")
	}
	endpoint := s.client.endpoints.Recording.String()
	body := `<GetServiceCapabilities xmlns="http://www.onvif.org/ver10/recording/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetRecordingServiceCapabilities failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetServiceCapabilitiesResponse"`
		Caps    struct {
			DynamicRecordings bool   `xml:"DynamicRecordings,attr"`
			DynamicTracks     bool   `xml:"DynamicTracks,attr"`
			Encoding          string `xml:"Encoding,attr"`
			SegmentExport     bool   `xml:"SegmentExport,attr"`
		} `xml:"Capabilities"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return map[string]bool{
		"dynamic_recordings": result.Caps.DynamicRecordings,
		"dynamic_tracks":     result.Caps.DynamicTracks,
		"encoding":           strings.TrimSpace(result.Caps.Encoding) != "" && result.Caps.Encoding != "false",
		"segment_export":     result.Caps.SegmentExport,
	}, nil
}

// --- Search Service Capabilities ---

// GetSearchServiceCapabilities retrieves search service capabilities.
func (s *RecordingService) GetSearchServiceCapabilities(ctx context.Context) (map[string]bool, error) {
	endpoint := s.client.endpoints.Search.String()
	body := `<GetServiceCapabilities xmlns="http://www.onvif.org/ver10/search/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetSearchServiceCapabilities failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetServiceCapabilitiesResponse"`
		Caps    struct {
			MetadataSearch bool `xml:"MetadataSearch,attr"`
		} `xml:"Capabilities"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return map[string]bool{
		"metadata_search": result.Caps.MetadataSearch,
	}, nil
}

// GetRecordingSummary retrieves the recording summary from the search service.
func (s *RecordingService) GetRecordingSummary(ctx context.Context) (map[string]interface{}, error) {
	endpoint := s.client.endpoints.Search.String()
	body := `<GetRecordingSummary xmlns="http://www.onvif.org/ver10/search/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetRecordingSummary failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetRecordingSummaryResponse"`
		Summary struct {
			DataFrom  string `xml:"DataFrom"`
			DataUntil string `xml:"DataUntil"`
			NumberRecordings int `xml:"NumberRecordings"`
		} `xml:"Summary"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"data_from":        result.Summary.DataFrom,
		"data_until":       result.Summary.DataUntil,
		"number_recordings": result.Summary.NumberRecordings,
	}, nil
}

// GetRecordingInformation retrieves recording information for a specific recording.
func (s *RecordingService) GetRecordingInformation(ctx context.Context, recordingToken string) (map[string]interface{}, error) {
	endpoint := s.client.endpoints.Search.String()
	body := fmt.Sprintf(`<GetRecordingInformation xmlns="http://www.onvif.org/ver10/search/wsdl">
  <RecordingToken>%s</RecordingToken>
</GetRecordingInformation>`, recordingToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{ServiceURL: endpoint, Body: body})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetRecordingInformation failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetRecordingInformationResponse"`
		RecordingInfo struct {
			RecordingToken string `xml:"RecordingToken"`
			Source         struct {
				SourceID    string `xml:"SourceId"`
				Name        string `xml:"Name"`
				Location    string `xml:"Location"`
				Description string `xml:"Description"`
			} `xml:"Source"`
			EarliestRecording string `xml:"EarliestRecording"`
			LatestRecording   string `xml:"LatestRecording"`
			Content           struct {
				Name string `xml:"Name"`
			} `xml:"Content"`
		} `xml:"RecordingInformation"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"token":              result.RecordingInfo.RecordingToken,
		"source_id":          result.RecordingInfo.Source.SourceID,
		"source_name":        result.RecordingInfo.Source.Name,
		"earliest_recording": result.RecordingInfo.EarliestRecording,
		"latest_recording":   result.RecordingInfo.LatestRecording,
		"content_name":       result.RecordingInfo.Content.Name,
	}, nil
}
