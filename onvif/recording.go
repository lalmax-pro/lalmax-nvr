package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RecordingService handles recording operations.
type RecordingService struct {
	client *Client
}

// NewRecordingService creates a new recording service.
func NewRecordingService(client *Client) *RecordingService {
	return &RecordingService{client: client}
}

// GetRecordings retrieves all recordings from the device.
func (s *RecordingService) GetRecordings(ctx context.Context) ([]Recording, error) {
	body := `<GetRecordings xmlns="http://www.onvif.org/ver10/recording/wsdl"/>`

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Recording.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetRecordings failed: %w", err)
	}

	var result struct {
		XMLName       xml.Name `xml:"GetRecordingsResponse"`
		RecordingItem []struct {
			RecordingToken string `xml:"RecordingToken"`
			Configuration  struct {
				Source struct {
					SourceID    string `xml:"SourceId"`
					Name        string `xml:"Name"`
					Location    string `xml:"Location"`
					Description string `xml:"Description"`
				} `xml:"Source"`
				Content []struct {
					Name string `xml:"Name"`
				} `xml:"Content"`
				MaximumRetentionTime string `xml:"MaximumRetentionTime"`
			} `xml:"Configuration"`
			Tracks struct {
				Track []struct {
					TrackToken    string `xml:"TrackToken"`
					Configuration struct {
						TrackType   string `xml:"TrackType"`
						Description string `xml:"Description"`
					} `xml:"Configuration"`
				} `xml:"Track"`
			} `xml:"Tracks"`
		} `xml:"RecordingItem"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	recordings := make([]Recording, 0, len(result.RecordingItem))
	for _, item := range result.RecordingItem {
		rec := Recording{
			Token:       item.RecordingToken,
			Description: item.Configuration.Source.Description,
			Source: RecordingSource{
				SourceID:    item.Configuration.Source.SourceID,
				Name:        item.Configuration.Source.Name,
				Location:    item.Configuration.Source.Location,
				Description: item.Configuration.Source.Description,
			},
			Status: "active",
		}

		// Extract name from content
		if len(item.Configuration.Content) > 0 {
			rec.Name = item.Configuration.Content[0].Name
		}

		// Extract tracks
		for _, t := range item.Tracks.Track {
			rec.Tracks = append(rec.Tracks, Track{
				Token:       t.TrackToken,
				TrackType:   t.Configuration.TrackType,
				Description: t.Configuration.Description,
			})
		}

		recordings = append(recordings, rec)
	}

	return recordings, nil
}

// GetRecordingDetails retrieves detailed recording information including segments.
func (s *RecordingService) GetRecordingDetails(ctx context.Context, recordingToken string) (*Recording, error) {
	body := fmt.Sprintf(`<GetRecordingInformation xmlns="http://www.onvif.org/ver10/recording/wsdl">
  <RecordingToken>%s</RecordingToken>
</GetRecordingInformation>`, recordingToken)

	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Recording.String(),
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetRecordingInformation failed: %w", err)
	}

	var result struct {
		XMLName              xml.Name `xml:"GetRecordingInformationResponse"`
		RecordingInformation struct {
			RecordingToken string `xml:"RecordingToken"`
			Source         struct {
				SourceID    string `xml:"SourceId"`
				Name        string `xml:"Name"`
				Location    string `xml:"Location"`
				Description string `xml:"Description"`
			} `xml:"Source"`
			Content []struct {
				Name string `xml:"Name"`
			} `xml:"Content"`
			Tracks struct {
				Track []struct {
					TrackToken    string `xml:"TrackToken"`
					Configuration struct {
						TrackType   string `xml:"TrackType"`
						Description string `xml:"Description"`
					} `xml:"Configuration"`
					Segment []struct {
						SegmentToken string `xml:"SegmentToken"`
						Start        string `xml:"Start"`
						End          string `xml:"End"`
						Duration     string `xml:"Duration"`
						Size         int64  `xml:"Size"`
					} `xml:"Segment"`
				} `xml:"Track"`
			} `xml:"Tracks"`
		} `xml:"RecordingInformation"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	info := result.RecordingInformation
	rec := &Recording{
		Token:       info.RecordingToken,
		Description: info.Source.Description,
		Source: RecordingSource{
			SourceID:    info.Source.SourceID,
			Name:        info.Source.Name,
			Location:    info.Source.Location,
			Description: info.Source.Description,
		},
		Status: "active",
	}

	// Extract name from content
	if len(info.Content) > 0 {
		rec.Name = info.Content[0].Name
	}

	// Extract tracks and segments
	for _, t := range info.Tracks.Track {
		track := Track{
			Token:       t.TrackToken,
			TrackType:   t.Configuration.TrackType,
			Description: t.Configuration.Description,
		}

		for _, seg := range t.Segment {
			segment := RecordingSegment{
				Token:          seg.SegmentToken,
				RecordingToken: info.RecordingToken,
				Size:           seg.Size,
			}

			if startTime, err := time.Parse(time.RFC3339, seg.Start); err == nil {
				segment.StartTime = startTime
			}
			if endTime, err := time.Parse(time.RFC3339, seg.End); err == nil {
				segment.EndTime = endTime
			}

			track.Segments = append(track.Segments, segment)
		}

		rec.Tracks = append(rec.Tracks, track)
	}

	return rec, nil
}

const searchNamespace = "http://www.onvif.org/ver10/search/wsdl"

// SearchRecordings starts or continues a recording search. Pass the same time
// and recording filters with the search token when requesting subsequent pages.
func (s *RecordingService) SearchRecordings(ctx context.Context, filter SearchFilter) (*SearchResult, error) {
	if s.client.endpoints.Search == nil {
		return nil, fmt.Errorf("onvif: Search service not available")
	}
	if filter.MaxResults <= 0 {
		filter.MaxResults = 100
	}
	if !filter.StartTime.IsZero() && !filter.EndTime.IsZero() && filter.EndTime.Before(filter.StartTime) {
		return nil, fmt.Errorf("onvif: recording search end time precedes start time")
	}
	if filter.SearchToken != "" {
		return s.getSearchResults(ctx, filter)
	}
	token, err := s.findRecordings(ctx, filter)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return &SearchResult{Segments: []RecordingSegment{}}, nil
	}
	filter.SearchToken = token
	result, err := s.getSearchResults(ctx, filter)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = s.EndSearch(cleanupCtx, token)
	}
	return result, err
}

func (s *RecordingService) findRecordings(ctx context.Context, filter SearchFilter) (string, error) {
	var scope, maxMatches string
	if filter.RecordingToken != "" {
		scope = "<tt:IncludedRecordings>" + escapeXML(filter.RecordingToken) + "</tt:IncludedRecordings>"
	}
	// FindRecordings does not take a time range. Filter the device's reported
	// bounds locally, so a match limit must not truncate that search prematurely.
	if filter.StartTime.IsZero() && filter.EndTime.IsZero() {
		maxMatches = "<MaxMatches>" + strconv.Itoa(filter.MaxResults) + "</MaxMatches>"
	}
	body := fmt.Sprintf(`<FindRecordings xmlns="%s" xmlns:tt="%s">
      <Scope>%s</Scope>%s<KeepAliveTime>PT10S</KeepAliveTime>
    </FindRecordings>`, searchNamespace, schemaNamespace, scope, maxMatches)
	resp, err := s.sendSearch(ctx, "FindRecordings", body)
	if err != nil {
		return "", err
	}
	var result struct {
		XMLName     xml.Name `xml:"FindRecordingsResponse"`
		SearchToken string   `xml:"SearchToken"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return "", err
	}
	return strings.TrimSpace(result.SearchToken), nil
}

func (s *RecordingService) getSearchResults(ctx context.Context, filter SearchFilter) (*SearchResult, error) {
	body := fmt.Sprintf(`<GetRecordingSearchResults xmlns="%s">
      <SearchToken>%s</SearchToken><MinResults>1</MinResults><MaxResults>%d</MaxResults><WaitTime>PT5S</WaitTime>
    </GetRecordingSearchResults>`, searchNamespace, escapeXML(filter.SearchToken), filter.MaxResults)
	resp, err := s.sendSearch(ctx, "GetRecordingSearchResults", body)
	if err != nil {
		return nil, err
	}

	var result struct {
		XMLName    xml.Name `xml:"GetRecordingSearchResultsResponse"`
		ResultList *struct {
			SearchState string `xml:"SearchState"`
			Recordings  []struct {
				Token    string `xml:"RecordingToken"`
				Earliest string `xml:"EarliestRecording"`
				Latest   string `xml:"LatestRecording"`
			} `xml:"RecordingInformation"`
			LegacyRecordings []struct {
				Token    string `xml:"RecordingToken"`
				Segments []struct {
					Token   string `xml:"SegmentToken"`
					Start   string `xml:"Start"`
					End     string `xml:"End"`
					FileURL string `xml:"FileURL"`
					Size    int64  `xml:"Size"`
				} `xml:"Track>Segment"`
			} `xml:"Recording"`
		} `xml:"ResultList"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}
	if result.ResultList == nil {
		return nil, fmt.Errorf("onvif: GetRecordingSearchResults response missing ResultList")
	}

	state := strings.TrimSpace(result.ResultList.SearchState)
	page := &SearchResult{
		SearchToken: filter.SearchToken,
		SearchState: state,
		HasMore:     state == "Searching" || state == "Queued" || state == "MoreResults",
		Segments:    make([]RecordingSegment, 0),
	}
	add := func(segment RecordingSegment, start, end string) {
		if filter.RecordingToken != "" && filter.RecordingToken != segment.RecordingToken {
			return
		}
		segment.StartTime, _ = time.Parse(time.RFC3339Nano, start)
		segment.EndTime, _ = time.Parse(time.RFC3339Nano, end)
		if segment.StartTime.IsZero() || segment.EndTime.IsZero() || segment.EndTime.Before(segment.StartTime) {
			return
		}
		if (!filter.StartTime.IsZero() && segment.EndTime.Before(filter.StartTime)) ||
			(!filter.EndTime.IsZero() && segment.StartTime.After(filter.EndTime)) {
			return
		}
		segment.Duration = int64(segment.EndTime.Sub(segment.StartTime) / time.Second)
		page.Segments = append(page.Segments, segment)
	}
	for _, recording := range result.ResultList.Recordings {
		if recording.Token != "" {
			add(RecordingSegment{
				Token: recording.Token, RecordingToken: recording.Token, RecordingBounds: true,
			}, recording.Earliest, recording.Latest)
		}
	}
	// Some older devices return individual file segments as a vendor extension.
	for _, recording := range result.ResultList.LegacyRecordings {
		for _, segment := range recording.Segments {
			add(RecordingSegment{
				Token: segment.Token, RecordingToken: recording.Token, FilePath: segment.FileURL, Size: segment.Size,
			}, segment.Start, segment.End)
		}
	}
	return page, nil
}

// EndSearch ends a search session.
func (s *RecordingService) EndSearch(ctx context.Context, searchToken string) error {
	body := fmt.Sprintf(`<EndSearch xmlns="%s"><SearchToken>%s</SearchToken></EndSearch>`,
		searchNamespace, escapeXML(searchToken))
	_, err := s.sendSearch(ctx, "EndSearch", body)
	return err
}

func (s *RecordingService) sendSearch(ctx context.Context, operation, body string) (*SOAPResponse, error) {
	if s.client.endpoints.Search == nil {
		return nil, fmt.Errorf("onvif: Search service not available")
	}
	resp, err := s.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: s.client.endpoints.Search.String(),
		Action:     searchNamespace + "/" + operation,
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: %s failed: %w", operation, err)
	}
	return resp, nil
}

// ListRecordedSegments retrieves the recorded segments when the device
// advertises the Recording Control SegmentExport capability.
func (s *RecordingService) ListRecordedSegments(ctx context.Context, token string, from, until time.Time, limit int) ([]RecordingSegment, error) {
	const namespace = "http://www.onvif.org/ver10/recording/wsdl"
	if s.client.endpoints.Recording == nil {
		return nil, fmt.Errorf("onvif: Recording service not available")
	}
	if token == "" || from.IsZero() || until.IsZero() || until.Before(from) {
		return nil, fmt.Errorf("onvif: invalid recording token or segment time range")
	}
	if limit <= 0 {
		limit = 100
	}
	segments := make([]RecordingSegment, 0)
	for {
		body := fmt.Sprintf(`<ListRecordedSegments xmlns="%s" xmlns:tt="%s">
			<Time><tt:From>%s</tt:From><tt:Until>%s</tt:Until></Time>
			<RecordingToken>%s</RecordingToken><MaxResults>%d</MaxResults>
		</ListRecordedSegments>`, namespace, schemaNamespace, from.UTC().Format(time.RFC3339Nano),
			until.UTC().Format(time.RFC3339Nano), escapeXML(token), limit-len(segments))
		response, err := s.client.soap.SendContext(ctx, &SOAPRequest{
			ServiceURL: s.client.endpoints.Recording.String(),
			Action:     namespace + "/ListRecordedSegments",
			Body:       body,
		})
		if err != nil {
			return nil, fmt.Errorf("onvif: ListRecordedSegments failed: %w", err)
		}
		var page struct {
			XMLName  xml.Name `xml:"ListRecordedSegmentsResponse"`
			Segments []struct {
				Start time.Time `xml:"StartTime"`
				End   time.Time `xml:"EndTime"`
			} `xml:"Segment"`
			HasMore bool `xml:"HasMoreResults"`
		}
		if err := ParseResponseBody(response.Body, &page); err != nil {
			return nil, err
		}
		cursor := from
		for _, segment := range page.Segments {
			if segment.Start.IsZero() || !segment.End.After(segment.Start) || !segment.End.After(cursor) || segment.Start.After(until) {
				continue
			}
			segments = append(segments, RecordingSegment{
				Token:          token + "@" + segment.Start.UTC().Format(time.RFC3339Nano),
				RecordingToken: token, StartTime: segment.Start, EndTime: segment.End,
				Duration: int64(segment.End.Sub(segment.Start) / time.Second),
			})
			cursor = segment.End
			if len(segments) == limit {
				return segments, nil
			}
		}
		if !page.HasMore || !cursor.Before(until) {
			return segments, nil
		}
		if !cursor.After(from) {
			return nil, fmt.Errorf("onvif: recorded segment pagination did not advance")
		}
		from = cursor
	}
}
