package onvif

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func standardDevice(t *testing.T, handler func(string, string) (int, string)) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		var status int
		var body string
		switch soapOperation(string(payload)) {
		case "GetServices":
			body = `<GetServicesResponse xmlns="http://www.onvif.org/ver10/device/wsdl">
				<Service><Namespace>http://www.onvif.org/ver20/media/wsdl</Namespace><XAddr>/media</XAddr><Version><Major>26</Major><Minor>6</Minor></Version></Service>
				<Service><Namespace>http://www.onvif.org/ver20/imaging/wsdl</Namespace><XAddr>/imaging</XAddr><Version><Major>22</Major><Minor>6</Minor></Version></Service>
				<Service><Namespace>http://www.onvif.org/ver10/search/wsdl</Namespace><XAddr>/search</XAddr><Version><Major>26</Major><Minor>6</Minor></Version></Service>
				<Service><Namespace>http://www.onvif.org/ver10/recording/wsdl</Namespace><XAddr>/recording</XAddr><Version><Major>26</Major><Minor>6</Minor></Version></Service>
				<Service><Namespace>http://www.onvif.org/ver10/events/wsdl</Namespace><XAddr>/events</XAddr><Version><Major>26</Major><Minor>6</Minor></Version></Service>
			</GetServicesResponse>`
			status = http.StatusOK
		default:
			status, body = handler(r.URL.Path, string(payload))
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>%s</s:Body></s:Envelope>`, body)
	}))
	t.Cleanup(server.Close)
	client := NewClient(server.URL+"/device", "camera", "secret")
	require.NoError(t, client.Connect(t.Context()))
	return client
}

func TestONVIF2606RecordingAdapterReadsPagesAndExactSegments(t *testing.T) {
	var searches, lists, ended atomic.Int32
	client := standardDevice(t, func(path, body string) (int, string) {
		switch {
		case strings.Contains(body, "<FindRecordings"):
			assert.Equal(t, "/search", path)
			return http.StatusOK, `<FindRecordingsResponse xmlns="http://www.onvif.org/ver10/search/wsdl"><SearchToken>search-1</SearchToken></FindRecordingsResponse>`
		case strings.Contains(body, "<GetRecordingSearchResults"):
			state, start := "Searching", "2026-11-16"
			if searches.Add(1) == 2 {
				state, start = "Completed", "2026-11-17"
			}
			return http.StatusOK, fmt.Sprintf(`<GetRecordingSearchResultsResponse xmlns="http://www.onvif.org/ver10/search/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
				<ResultList><tt:SearchState>%s</tt:SearchState><tt:RecordingInformation>
					<tt:RecordingToken>rec-1</tt:RecordingToken><tt:Source><tt:SourceId>urn:camera:1</tt:SourceId>
					<tt:Name>Camera</tt:Name><tt:Location>Lab</tt:Location><tt:Description>Video</tt:Description><tt:Address>http://camera</tt:Address></tt:Source>
					<tt:EarliestRecording>%sT00:00:00Z</tt:EarliestRecording><tt:LatestRecording>%sT03:00:00Z</tt:LatestRecording>
					<tt:Content>Video</tt:Content><tt:RecordingStatus>Recording</tt:RecordingStatus>
				</tt:RecordingInformation></ResultList></GetRecordingSearchResultsResponse>`, state, start, start)
		case strings.Contains(body, "<EndSearch"):
			ended.Add(1)
			return http.StatusOK, `<EndSearchResponse xmlns="http://www.onvif.org/ver10/search/wsdl"><Endpoint>2026-11-17T03:00:00Z</Endpoint></EndSearchResponse>`
		case path == "/recording" && strings.Contains(body, "<GetServiceCapabilities"):
			return http.StatusOK, `<GetServiceCapabilitiesResponse xmlns="http://www.onvif.org/ver10/recording/wsdl"><Capabilities SegmentExport="true" Encoding="H264 H265"/></GetServiceCapabilitiesResponse>`
		case path == "/recording" && strings.Contains(body, "<ListRecordedSegments"):
			assert.Contains(t, body, "<RecordingToken>rec-1</RecordingToken>")
			assert.Contains(t, body, "<tt:Until>2026-11-17T02:30:00Z</tt:Until>")
			from, start, end, more := "00:30:00", "00:45:00", "01:00:00", "true"
			if lists.Add(1) == 2 {
				from, start, end, more = "01:00:00", "02:00:00", "02:15:00", "false"
			}
			assert.Contains(t, body, "<tt:From>2026-11-17T"+from+"Z</tt:From>")
			return http.StatusOK, fmt.Sprintf(`<ListRecordedSegmentsResponse xmlns="http://www.onvif.org/ver10/recording/wsdl"><Segment>
				<StartTime>2026-11-17T%sZ</StartTime><EndTime>2026-11-17T%sZ</EndTime></Segment><HasMoreResults>%s</HasMoreResults></ListRecordedSegmentsResponse>`,
				start, end, more)
		default:
			return http.StatusNotImplemented, ""
		}
	})
	segments, err := client.SearchRecordings(t.Context(), SearchRequest{
		StartTime:  time.Date(2026, 11, 17, 0, 30, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 11, 17, 2, 30, 0, 0, time.UTC),
		MaxResults: 10,
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, searches.Load())
	require.EqualValues(t, 2, lists.Load())
	require.EqualValues(t, 1, ended.Load())
	require.Len(t, segments, 2)
	require.False(t, segments[0].RecordingBounds)
	require.Equal(t, "rec-1", segments[1].RecordingToken)
	require.Equal(t, time.Hour, segments[1].StartTime.Sub(segments[0].EndTime))
}

func TestONVIF2606SubscriberRecoversFromExpiredReference(t *testing.T) {
	var created, synced, unsubscribed atomic.Int32
	client := standardDevice(t, func(path, body string) (int, string) {
		switch {
		case path == "/events" && strings.Contains(body, "<CreatePullPointSubscription"):
			id := created.Add(1)
			return http.StatusOK, fmt.Sprintf(`<tev:CreatePullPointSubscriptionResponse xmlns:tev="http://www.onvif.org/ver10/events/wsdl"
				xmlns:wsa="http://www.w3.org/2005/08/addressing" xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2">
				<tev:SubscriptionReference><wsa:Address>/sub/%d</wsa:Address></tev:SubscriptionReference>
				<wsnt:CurrentTime>2026-11-17T00:00:00Z</wsnt:CurrentTime><wsnt:TerminationTime>2026-11-17T00:01:00Z</wsnt:TerminationTime>
			</tev:CreatePullPointSubscriptionResponse>`, id)
		case strings.Contains(body, "<SetSynchronizationPoint"):
			synced.Add(1)
			return http.StatusOK, `<SetSynchronizationPointResponse xmlns="http://www.onvif.org/ver10/events/wsdl"/>`
		case path == "/sub/1" && strings.Contains(body, "<PullMessages"):
			return http.StatusInternalServerError, `<s:Fault xmlns:b="http://docs.oasis-open.org/wsrf/r-2">
				<s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>b:ResourceUnknownFault</s:Value></s:Subcode></s:Code>
			</s:Fault>`
		case path == "/sub/2" && strings.Contains(body, "<PullMessages"):
			return http.StatusOK, `<tev:PullMessagesResponse xmlns:tev="http://www.onvif.org/ver10/events/wsdl" xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2" xmlns:tt="http://www.onvif.org/ver10/schema">
				<tev:CurrentTime>2026-11-17T00:00:00Z</tev:CurrentTime><tev:TerminationTime>2026-11-17T00:01:00Z</tev:TerminationTime>
				<wsnt:NotificationMessage><wsnt:Topic>tns1:VideoSource/MotionAlarm</wsnt:Topic><wsnt:Message>
					<tt:Message UtcTime="2026-11-17T00:00:00Z"><tt:Data><tt:SimpleItem Name="State" Value="true"/></tt:Data></tt:Message>
				</wsnt:Message></wsnt:NotificationMessage></tev:PullMessagesResponse>`
		case path == "/sub/2" && strings.Contains(body, "<Unsubscribe"):
			unsubscribed.Add(1)
			return http.StatusOK, `<UnsubscribeResponse xmlns="http://docs.oasis-open.org/wsn/b-2"/>`
		default:
			return http.StatusNotImplemented, ""
		}
	})
	got := make(chan ONVIFEvent, 1)
	subscriber := client.NewEventSubscriber(WithPollInterval(20*time.Millisecond), WithEventCallback(func(event ONVIFEvent) {
		select {
		case got <- event:
		default:
		}
	}))
	require.NoError(t, subscriber.Subscribe(t.Context(), "camera"))
	t.Cleanup(func() { _ = subscriber.Unsubscribe(t.Context(), "camera") })
	select {
	case event := <-got:
		require.Equal(t, "true", event.Data["State"])
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber did not recover")
	}
	require.EqualValues(t, 2, created.Load())
	require.EqualValues(t, 2, synced.Load())
	require.NoError(t, subscriber.Unsubscribe(t.Context(), "camera"))
	require.EqualValues(t, 1, unsubscribed.Load())
}

func TestONVIF2606ImagingUsesPhysicalSourceToken(t *testing.T) {
	client := standardDevice(t, func(path, body string) (int, string) {
		switch {
		case path == "/media" && strings.Contains(body, "<GetProfiles"):
			return http.StatusOK, `<GetProfilesResponse xmlns="http://www.onvif.org/ver20/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
				<Profiles token="profile"><Name>Main</Name><Configurations><VideoSource token="config"><tt:Name>Sensor config</tt:Name>
				<tt:UseCount>1</tt:UseCount><tt:SourceToken>sensor</tt:SourceToken><tt:Bounds x="0" y="0" width="1920" height="1080"/></VideoSource></Configurations></Profiles></GetProfilesResponse>`
		case path == "/imaging" && strings.Contains(body, "<GetImagingSettings"):
			assert.Contains(t, body, "<VideoSourceToken>sensor</VideoSourceToken>")
			return http.StatusOK, `<GetImagingSettingsResponse xmlns="http://www.onvif.org/ver20/imaging/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
				<ImagingSettings><tt:Brightness>40</tt:Brightness></ImagingSettings></GetImagingSettingsResponse>`
		default:
			return http.StatusNotImplemented, ""
		}
	})
	capabilities, err := client.GetCapabilitiesDetailed(t.Context())
	require.NoError(t, err)
	require.True(t, capabilities.Streaming)
	require.True(t, capabilities.Imaging)
	profiles, err := client.GetProfiles(t.Context())
	require.NoError(t, err)
	require.Equal(t, "config", profiles[0].VideoSource)
	controller := client.NewImagingController(client.ResolveVideoSourceToken(t.Context(), profiles[0]))
	settings, err := controller.GetImagingSettings(t.Context())
	require.NoError(t, err)
	require.Equal(t, 40.0, settings.Brightness)
}
