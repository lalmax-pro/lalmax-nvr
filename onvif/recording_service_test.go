package onvif

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func leftoverRecordingClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client := newTestClient(t, server)
	client.endpoints = &ServiceEndpoints{
		Recording: mustParseURL(t, server.URL+"/onvif/recording"),
		Search:    mustParseURL(t, server.URL+"/onvif/search"),
		Replay:    mustParseURL(t, server.URL+"/onvif/replay"),
		Events:    mustParseURL(t, server.URL+"/onvif/events"),
	}
	return client
}

func TestGetRecordingsEmptyAndErrors(t *testing.T) {
	empty := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetRecordingsResponse></GetRecordingsResponse>`)
	})
	recs, err := NewRecordingService(leftoverRecordingClient(t, empty)).GetRecordings(context.Background())
	require.NoError(t, err)
	require.Empty(t, recs)

	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "denied")
	})
	_, err = NewRecordingService(leftoverRecordingClient(t, fault)).GetRecordings(context.Background())
	require.ErrorContains(t, err, "GetRecordings failed")

	bad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetRecordingsResponse><`)
	})
	_, err = NewRecordingService(leftoverRecordingClient(t, bad)).GetRecordings(context.Background())
	require.Error(t, err)
}

func TestGetRecordingDetailsWithSegments(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		require.Contains(t, body, "GetRecordingInformation")
		require.Contains(t, body, "<RecordingToken>rec-1</RecordingToken>")
		return soapOK(`<GetRecordingInformationResponse>
			<RecordingInformation>
				<RecordingToken>rec-1</RecordingToken>
				<Source>
					<SourceId>src-1</SourceId>
					<Name>Cam1</Name>
					<Location>Lobby</Location>
					<Description>front door</Description>
				</Source>
				<Content><Name>Main</Name></Content>
				<Tracks>
					<Track>
						<TrackToken>trk-v</TrackToken>
						<Configuration>
							<TrackType>Video</TrackType>
							<Description>H264</Description>
						</Configuration>
						<Segment>
							<SegmentToken>seg-1</SegmentToken>
							<Start>2024-06-15T12:00:00Z</Start>
							<End>2024-06-15T13:00:00Z</End>
							<Duration>PT3600S</Duration>
							<Size>12345</Size>
						</Segment>
						<Segment>
							<SegmentToken>seg-bad</SegmentToken>
							<Start>not-a-time</Start>
							<End>also-bad</End>
							<Size>1</Size>
						</Segment>
					</Track>
				</Tracks>
			</RecordingInformation>
		</GetRecordingInformationResponse>`)
	})
	rec, err := NewRecordingService(leftoverRecordingClient(t, server)).GetRecordingDetails(context.Background(), "rec-1")
	require.NoError(t, err)
	require.Equal(t, "rec-1", rec.Token)
	require.Equal(t, "Main", rec.Name)
	require.Equal(t, "src-1", rec.Source.SourceID)
	require.Len(t, rec.Tracks, 1)
	require.Len(t, rec.Tracks[0].Segments, 2)
	require.Equal(t, "seg-1", rec.Tracks[0].Segments[0].Token)
	require.Equal(t, "rec-1", rec.Tracks[0].Segments[0].RecordingToken)
	require.Equal(t, int64(12345), rec.Tracks[0].Segments[0].Size)
	require.True(t, rec.Tracks[0].Segments[0].StartTime.Equal(time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)))
	require.True(t, rec.Tracks[0].Segments[1].StartTime.IsZero())
}

func TestGetRecordingDetailsNoContentAndErrors(t *testing.T) {
	empty := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetRecordingInformationResponse>
			<RecordingInformation>
				<RecordingToken>rec-empty</RecordingToken>
				<Source><SourceId>s</SourceId><Name>n</Name></Source>
			</RecordingInformation>
		</GetRecordingInformationResponse>`)
	})
	rec, err := NewRecordingService(leftoverRecordingClient(t, empty)).GetRecordingDetails(context.Background(), "rec-empty")
	require.NoError(t, err)
	require.Empty(t, rec.Name)
	require.Empty(t, rec.Tracks)

	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "missing")
	})
	_, err = NewRecordingService(leftoverRecordingClient(t, fault)).GetRecordingDetails(context.Background(), "x")
	require.ErrorContains(t, err, "GetRecordingInformation failed")

	bad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetRecordingInformationResponse><`)
	})
	_, err = NewRecordingService(leftoverRecordingClient(t, bad)).GetRecordingDetails(context.Background(), "x")
	require.Error(t, err)
}

func TestSearchRecordingsFindAndResults(t *testing.T) {
	var seenFind, seenResults bool
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "FindRecordings"):
			seenFind = true
			require.Contains(t, body, "<tt:From>2024-06-15T00:00:00Z</tt:From>")
			require.Contains(t, body, "<tt:Until>2024-06-15T23:59:59Z</tt:Until>")
			require.Contains(t, body, "<MaxResults>10</MaxResults>")
			return soapOK(`<FindRecordingsResponse><SearchToken>st-1</SearchToken></FindRecordingsResponse>`)
		case strings.Contains(body, "GetRecordingSearchResults"):
			seenResults = true
			require.Contains(t, body, "<SearchToken>st-1</SearchToken>")
			return soapOK(`<GetRecordingSearchResultsResponse>
				<ResultList>
					<SearchState>MoreResults</SearchState>
					<Recording>
						<RecordingToken>rec-1</RecordingToken>
						<Track>
							<TrackToken>trk-v</TrackToken>
							<Segment>
								<SegmentToken>seg-1</SegmentToken>
								<Start>2024-06-15T12:00:00Z</Start>
								<End>2024-06-15T13:00:00Z</End>
								<FileURL>/files/seg-1.mp4</FileURL>
								<Duration>PT3600S</Duration>
								<Size>99</Size>
							</Segment>
							<Segment>
								<SegmentToken>seg-bad</SegmentToken>
								<Start>bad</Start>
								<End>bad</End>
							</Segment>
						</Track>
					</Recording>
				</ResultList>
			</GetRecordingSearchResultsResponse>`)
		default:
			return ""
		}
	})
	result, err := NewRecordingService(leftoverRecordingClient(t, server)).SearchRecordings(context.Background(), SearchFilter{
		StartTime:  time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2024, 6, 15, 23, 59, 59, 0, time.UTC),
		MaxResults: 10,
	})
	require.NoError(t, err)
	require.True(t, seenFind)
	require.True(t, seenResults)
	require.Equal(t, "st-1", result.SearchToken)
	require.Equal(t, "MoreResults", result.SearchState)
	require.True(t, result.HasMore)
	require.Len(t, result.Segments, 2)
	require.Equal(t, "seg-1", result.Segments[0].Token)
	require.Equal(t, "/files/seg-1.mp4", result.Segments[0].FilePath)
	require.True(t, result.Segments[1].StartTime.IsZero())
}

func TestSearchRecordingsContinueTokenAndEmptyFind(t *testing.T) {
	cont := newONVIFDeviceServer(t, func(body string) string {
		require.NotContains(t, body, "FindRecordings")
		require.Contains(t, body, "GetRecordingSearchResults")
		require.Contains(t, body, "<SearchToken>keep</SearchToken>")
		require.Contains(t, body, "<MaxResults>100</MaxResults>")
		return soapOK(`<GetRecordingSearchResultsResponse>
			<ResultList><SearchState>Completed</SearchState></ResultList>
		</GetRecordingSearchResultsResponse>`)
	})
	result, err := NewRecordingService(leftoverRecordingClient(t, cont)).SearchRecordings(context.Background(), SearchFilter{
		SearchToken: "keep",
	})
	require.NoError(t, err)
	require.False(t, result.HasMore)
	require.Equal(t, "Completed", result.SearchState)
	require.Empty(t, result.Segments)

	emptyFind := newONVIFDeviceServer(t, func(body string) string {
		require.Contains(t, body, "FindRecordings")
		require.NotContains(t, body, "tt:IncludedTimes")
		return soapOK(`<FindRecordingsResponse><SearchToken></SearchToken></FindRecordingsResponse>`)
	})
	result, err = NewRecordingService(leftoverRecordingClient(t, emptyFind)).SearchRecordings(context.Background(), SearchFilter{})
	require.NoError(t, err)
	require.Empty(t, result.Segments)
	require.Empty(t, result.SearchToken)
}

func TestSearchRecordingsErrors(t *testing.T) {
	findFault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "no search")
	})
	_, err := NewRecordingService(leftoverRecordingClient(t, findFault)).SearchRecordings(context.Background(), SearchFilter{})
	require.ErrorContains(t, err, "FindRecordings failed")

	findBad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<FindRecordingsResponse><`)
	})
	_, err = NewRecordingService(leftoverRecordingClient(t, findBad)).SearchRecordings(context.Background(), SearchFilter{})
	require.Error(t, err)

	resultsFault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "no results")
	})
	_, err = NewRecordingService(leftoverRecordingClient(t, resultsFault)).SearchRecordings(context.Background(), SearchFilter{SearchToken: "st"})
	require.ErrorContains(t, err, "GetRecordingSearchResults failed")

	resultsBad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetRecordingSearchResultsResponse><`)
	})
	_, err = NewRecordingService(leftoverRecordingClient(t, resultsBad)).SearchRecordings(context.Background(), SearchFilter{SearchToken: "st"})
	require.Error(t, err)
}

func TestEndSearch(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		require.Contains(t, body, "EndSearch")
		require.Contains(t, body, "<SearchToken>st-1</SearchToken>")
		return soapOK(`<EndSearchResponse/>`)
	})
	require.NoError(t, NewRecordingService(leftoverRecordingClient(t, server)).EndSearch(context.Background(), "st-1"))

	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "gone")
	})
	require.ErrorContains(t, NewRecordingService(leftoverRecordingClient(t, fault)).EndSearch(context.Background(), "st"), "EndSearch failed")
}

func TestDeleteAndSetRecordingJobMode(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "DeleteRecordingJob"):
			require.Contains(t, body, "<JobToken>job-new</JobToken>")
			return soapOK(`<DeleteRecordingJobResponse/>`)
		case strings.Contains(body, "SetRecordingJobMode"):
			require.Contains(t, body, "<JobToken>job-new</JobToken>")
			require.Contains(t, body, "<Mode>Idle</Mode>")
			return soapOK(`<SetRecordingJobModeResponse/>`)
		default:
			return ""
		}
	})
	svc := NewRecordingService(leftoverRecordingClient(t, server))
	ctx := context.Background()
	require.NoError(t, svc.SetRecordingJobMode(ctx, "job-new", "Idle"))
	require.NoError(t, svc.DeleteRecordingJob(ctx, "job-new"))
}

func TestGetRecordingJobStateAndConfiguration(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetRecordingJobState"):
			return soapOK(`<GetRecordingJobStateResponse>
				<State>
					<RecordingToken>rec-1</RecordingToken>
					<Mode>Active</Mode>
					<Priority>3</Priority>
				</State>
			</GetRecordingJobStateResponse>`)
		case strings.Contains(body, "GetRecordingJobConfiguration"):
			return soapOK(`<GetRecordingJobConfigurationResponse>
				<JobToken>job-1</JobToken>
				<JobConfiguration>
					<RecordingToken>rec-1</RecordingToken>
					<Mode>Idle</Mode>
					<Priority>2</Priority>
				</JobConfiguration>
			</GetRecordingJobConfigurationResponse>`)
		default:
			return ""
		}
	})
	svc := NewRecordingService(leftoverRecordingClient(t, server))
	ctx := context.Background()
	state, err := svc.GetRecordingJobState(ctx, "job-1")
	require.NoError(t, err)
	require.Equal(t, "rec-1", state["recording"])
	require.Equal(t, "Active", state["mode"])
	require.Equal(t, 3, state["priority"])

	cfg, err := svc.GetRecordingJobConfiguration(ctx, "job-1")
	require.NoError(t, err)
	require.Equal(t, "job-1", cfg.Token)
	require.Equal(t, "Idle", cfg.Mode)
	require.Equal(t, 2, cfg.Priority)
}

func TestGetRecordingOptionsSummaryAndCapabilities(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetRecordingOptions"):
			return soapOK(`<GetRecordingOptionsResponse>
				<Options>
					<Job><Mode>Active</Mode><Priority>1</Priority></Job>
					<Job><Mode>Idle</Mode><Priority>0</Priority></Job>
				</Options>
			</GetRecordingOptionsResponse>`)
		case strings.Contains(body, "ver10/recording/wsdl") && strings.Contains(body, "GetServiceCapabilities"):
			return soapOK(`<GetServiceCapabilitiesResponse>
				<Capabilities DynamicRecordings="true" DynamicTracks="false" Encoding="true" MaxRate="10" MaxTotalRate="20" MaxRecording="4"/>
			</GetServiceCapabilitiesResponse>`)
		case strings.Contains(body, "ver10/search/wsdl") && strings.Contains(body, "GetServiceCapabilities"):
			return soapOK(`<GetServiceCapabilitiesResponse>
				<Capabilities MetadataSearch="true"/>
			</GetServiceCapabilitiesResponse>`)
		case strings.Contains(body, "GetRecordingSummary"):
			return soapOK(`<GetRecordingSummaryResponse>
				<Summary>
					<DataFrom>2024-01-01T00:00:00Z</DataFrom>
					<DataUntil>2024-06-15T00:00:00Z</DataUntil>
					<NumberRecordings>7</NumberRecordings>
				</Summary>
			</GetRecordingSummaryResponse>`)
		case strings.Contains(body, "ver10/search/wsdl") && strings.Contains(body, "GetRecordingInformation"):
			return soapOK(`<GetRecordingInformationResponse>
				<RecordingInformation>
					<RecordingToken>rec-1</RecordingToken>
					<Source>
						<SourceId>src-1</SourceId>
						<Name>Cam1</Name>
						<Location>Lobby</Location>
						<Description>front</Description>
					</Source>
					<EarliestRecording>2024-01-01T00:00:00Z</EarliestRecording>
					<LatestRecording>2024-06-15T00:00:00Z</LatestRecording>
					<Content><Name>Main</Name></Content>
				</RecordingInformation>
			</GetRecordingInformationResponse>`)
		default:
			return ""
		}
	})
	svc := NewRecordingService(leftoverRecordingClient(t, server))
	ctx := context.Background()

	opts, err := svc.GetRecordingOptions(ctx, "rec-1")
	require.NoError(t, err)
	jobs, ok := opts["jobs"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, jobs, 2)
	require.Equal(t, "Active", jobs[0]["mode"])

	caps, err := svc.GetRecordingServiceCapabilities(ctx)
	require.NoError(t, err)
	require.True(t, caps["dynamic_recordings"])
	require.False(t, caps["dynamic_tracks"])
	require.True(t, caps["encoding"])

	searchCaps, err := svc.GetSearchServiceCapabilities(ctx)
	require.NoError(t, err)
	require.True(t, searchCaps["metadata_search"])

	summary, err := svc.GetRecordingSummary(ctx)
	require.NoError(t, err)
	require.Equal(t, "2024-01-01T00:00:00Z", summary["data_from"])
	require.Equal(t, 7, summary["number_recordings"])

	info, err := svc.GetRecordingInformation(ctx, "rec-1")
	require.NoError(t, err)
	require.Equal(t, "rec-1", info["token"])
	require.Equal(t, "src-1", info["source_id"])
	require.Equal(t, "Cam1", info["source_name"])
	require.Equal(t, "Main", info["content_name"])
}

func TestRecordingJobLeftoverSOAPAndParseErrors(t *testing.T) {
	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "denied")
	})
	svc := NewRecordingService(leftoverRecordingClient(t, fault))
	ctx := context.Background()
	_, err := svc.GetRecordingJobs(ctx)
	require.ErrorContains(t, err, "GetRecordingJobs failed")
	_, err = svc.CreateRecordingJob(ctx, "r", "Active", 1)
	require.ErrorContains(t, err, "CreateRecordingJob failed")
	require.ErrorContains(t, svc.DeleteRecordingJob(ctx, "j"), "DeleteRecordingJob failed")
	_, err = svc.GetRecordingJobState(ctx, "j")
	require.ErrorContains(t, err, "GetRecordingJobState failed")
	require.ErrorContains(t, svc.SetRecordingJobMode(ctx, "j", "Idle"), "SetRecordingJobMode failed")
	_, err = svc.GetRecordingJobConfiguration(ctx, "j")
	require.ErrorContains(t, err, "GetRecordingJobConfiguration failed")
	_, err = svc.GetRecordingOptions(ctx, "r")
	require.ErrorContains(t, err, "GetRecordingOptions failed")
	_, err = svc.GetRecordingServiceCapabilities(ctx)
	require.ErrorContains(t, err, "GetRecordingServiceCapabilities failed")
	_, err = svc.GetSearchServiceCapabilities(ctx)
	require.ErrorContains(t, err, "GetSearchServiceCapabilities failed")
	_, err = svc.GetRecordingSummary(ctx)
	require.ErrorContains(t, err, "GetRecordingSummary failed")
	_, err = svc.GetRecordingInformation(ctx, "r")
	require.ErrorContains(t, err, "GetRecordingInformation failed")

	bad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetRecordingJobsResponse><`)
	})
	_, err = NewRecordingService(leftoverRecordingClient(t, bad)).GetRecordingJobs(ctx)
	require.Error(t, err)

	parseCases := []string{
		"CreateRecordingJob",
		"GetRecordingJobState",
		"GetRecordingJobConfiguration",
		"GetRecordingOptions",
		"GetRecordingServiceCapabilities",
		"GetSearchServiceCapabilities",
		"GetRecordingSummary",
		"GetRecordingInformation",
	}
	for _, name := range parseCases {
		name := name
		t.Run("parse_"+name, func(t *testing.T) {
			srv := newONVIFDeviceServer(t, func(body string) string {
				return soapOK(`<` + name + `Response><`)
			})
			s := NewRecordingService(leftoverRecordingClient(t, srv))
			var callErr error
			switch name {
			case "CreateRecordingJob":
				_, callErr = s.CreateRecordingJob(ctx, "r", "Active", 1)
			case "GetRecordingJobState":
				_, callErr = s.GetRecordingJobState(ctx, "j")
			case "GetRecordingJobConfiguration":
				_, callErr = s.GetRecordingJobConfiguration(ctx, "j")
			case "GetRecordingOptions":
				_, callErr = s.GetRecordingOptions(ctx, "r")
			case "GetRecordingServiceCapabilities":
				_, callErr = s.GetRecordingServiceCapabilities(ctx)
			case "GetSearchServiceCapabilities":
				_, callErr = s.GetSearchServiceCapabilities(ctx)
			case "GetRecordingSummary":
				_, callErr = s.GetRecordingSummary(ctx)
			case "GetRecordingInformation":
				_, callErr = s.GetRecordingInformation(ctx, "r")
			}
			require.Error(t, callErr)
		})
	}
}

func TestGetReplayURIErrors(t *testing.T) {
	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "no replay")
	})
	_, err := NewReplayService(leftoverRecordingClient(t, fault)).GetReplayURI(context.Background(), "rec")
	require.ErrorContains(t, err, "GetReplayURI failed")

	bad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetReplayUriResponse><`)
	})
	_, err = NewReplayService(leftoverRecordingClient(t, bad)).GetReplayURI(context.Background(), "rec")
	require.Error(t, err)
}
