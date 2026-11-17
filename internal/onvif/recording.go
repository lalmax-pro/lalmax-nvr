package onvif

import (
	"context"
	"fmt"
	"time"

	onviflib "github.com/lalmax-pro/lalmax-nvr/onvif"
)

// Recording represents a recording on an ONVIF device.
type Recording struct {
	Token       string          `json:"token"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Source      RecordingSource `json:"source"`
	StartTime   time.Time       `json:"start_time"`
	EndTime     time.Time       `json:"end_time"`
	Status      string          `json:"status"`
}

// RecordingSource represents the source of a recording.
type RecordingSource struct {
	SourceID    string `json:"source_id"`
	Name        string `json:"name"`
	Location    string `json:"location"`
	Description string `json:"description"`
}

// RecordingSegment represents a segment of a recording.
type RecordingSegment struct {
	Token           string    `json:"token"`
	RecordingToken  string    `json:"recording_token"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	FilePath        string    `json:"file_path"`
	Duration        int64     `json:"duration"`
	Size            int64     `json:"size"`
	RecordingBounds bool      `json:"recording_bounds,omitempty"`
}

// SearchRequest represents a search request for recordings.
type SearchRequest struct {
	RecordingToken string
	StartTime      time.Time
	EndTime        time.Time
	MaxResults     int
}

// GetRecordings retrieves all recordings from an ONVIF device.
func (c *Client) GetRecordings(ctx context.Context) ([]Recording, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	recSvc := c.client.RecordingService()
	recordings, err := recSvc.GetRecordings(ctx)
	if err != nil {
		return nil, fmt.Errorf("get recordings: %w", err)
	}

	result := make([]Recording, 0, len(recordings))
	for _, rec := range recordings {
		result = append(result, Recording{
			Token:       rec.Token,
			Name:        rec.Name,
			Description: rec.Description,
			Source: RecordingSource{
				SourceID:    rec.Source.SourceID,
				Name:        rec.Source.Name,
				Location:    rec.Source.Location,
				Description: rec.Source.Description,
			},
			StartTime: rec.StartTime,
			EndTime:   rec.EndTime,
			Status:    rec.Status,
		})
	}

	return result, nil
}

// SearchRecordings searches for recording segments on an ONVIF device.
func (c *Client) SearchRecordings(ctx context.Context, req SearchRequest) ([]RecordingSegment, error) {
	if !c.ready {
		return nil, fmt.Errorf("onvif client not connected, call Connect() first")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}

	service := c.client.RecordingService()
	filter := onviflib.SearchFilter{
		RecordingToken: req.RecordingToken,
		StartTime:      req.StartTime,
		EndTime:        req.EndTime,
		MaxResults:     maxResults,
	}
	segments := make([]RecordingSegment, 0)
	defer func() {
		if filter.SearchToken != "" {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			_ = service.EndSearch(cleanupCtx, filter.SearchToken)
		}
	}()

	for {
		filter.MaxResults = maxResults - len(segments)
		page, err := service.SearchRecordings(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("search recordings: %w", err)
		}
		filter.SearchToken = page.SearchToken
		for _, segment := range page.Segments {
			segments = append(segments, RecordingSegment(segment))
			if len(segments) == maxResults {
				return listRecordedSegments(ctx, service, segments, req, maxResults)
			}
		}
		if !page.HasMore || filter.SearchToken == "" {
			return listRecordedSegments(ctx, service, segments, req, maxResults)
		}
		if len(page.Segments) == 0 {
			// A device may return a page before the search finds a match.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}

func listRecordedSegments(ctx context.Context, service *onviflib.RecordingService, matches []RecordingSegment, request SearchRequest, limit int) ([]RecordingSegment, error) {
	result := make([]RecordingSegment, 0, len(matches))
	var checked, supported bool
	for _, match := range matches {
		if match.RecordingBounds && !checked {
			capabilities, err := service.GetRecordingServiceCapabilities(ctx)
			supported = err == nil && capabilities["segment_export"]
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			checked = true
		}
		if !match.RecordingBounds || !supported {
			result = append(result, match)
		} else {
			from, until := match.StartTime, match.EndTime
			if request.StartTime.After(from) {
				from = request.StartTime
			}
			if !request.EndTime.IsZero() && request.EndTime.Before(until) {
				until = request.EndTime
			}
			segments, err := service.ListRecordedSegments(ctx, match.RecordingToken, from, until, limit-len(result))
			if err != nil {
				return nil, err
			}
			for _, segment := range segments {
				result = append(result, RecordingSegment(segment))
			}
		}
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

// GetReplayURI retrieves the replay URI for a recording.
func (c *Client) GetReplayURI(ctx context.Context, recordingToken string) (string, error) {
	if !c.ready {
		return "", fmt.Errorf("onvif client not connected, call Connect() first")
	}

	replaySvc := c.client.ReplayService()
	uri, err := replaySvc.GetReplayURI(ctx, recordingToken)
	if err != nil {
		return "", fmt.Errorf("get replay URI: %w", err)
	}

	return uri, nil
}
