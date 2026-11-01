package onvif

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func recordingEventsClient(t *testing.T, handler func(body string) string) *Client {
	t.Helper()
	server := newONVIFDeviceServer(t, handler)
	client := newTestClient(t, server)
	client.endpoints = &ServiceEndpoints{
		Recording: mustParseURL(t, server.URL+"/onvif/recording"),
		Search:    mustParseURL(t, server.URL+"/onvif/search"),
		Replay:    mustParseURL(t, server.URL+"/onvif/replay"),
		Events:    mustParseURL(t, server.URL+"/onvif/events"),
	}
	return client
}

func TestGetRecordingsAndReplayURI(t *testing.T) {
	client := recordingEventsClient(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetRecordings"):
			return soapOK(`<GetRecordingsResponse>
				<RecordingItem>
					<RecordingToken>rec1</RecordingToken>
					<Configuration>
						<Source><SourceId>src</SourceId><Name>Cam</Name><Location>Hall</Location><Description>desc</Description></Source>
						<Content><Name>Main</Name></Content>
					</Configuration>
					<Tracks>
						<Track>
							<TrackToken>v1</TrackToken>
							<Configuration><TrackType>Video</TrackType><Description>main</Description></Configuration>
						</Track>
					</Tracks>
				</RecordingItem>
			</GetRecordingsResponse>`)
		case strings.Contains(body, "GetReplayUri"):
			return soapOK(`<GetReplayUriResponse><Uri>rtsp://127.0.0.1/replay</Uri></GetReplayUriResponse>`)
		default:
			return ""
		}
	})
	recs, err := NewRecordingService(client).GetRecordings(context.Background())
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "rec1", recs[0].Token)
	require.Equal(t, "Main", recs[0].Name)
	require.Equal(t, "src", recs[0].Source.SourceID)
	require.Len(t, recs[0].Tracks, 1)
	require.Equal(t, "Video", recs[0].Tracks[0].TrackType)

	uri, err := NewReplayService(client).GetReplayURI(context.Background(), "rec1")
	require.NoError(t, err)
	require.Equal(t, "rtsp://127.0.0.1/replay", uri)
}

func TestGetRecordingJobsAndCreateJob(t *testing.T) {
	client := recordingEventsClient(t, func(body string) string {
		switch {
		case strings.Contains(body, "GetRecordingJobs"):
			return soapOK(`<GetRecordingJobsResponse>
				<JobItem>
					<JobToken>job1</JobToken>
					<JobConfiguration><RecordingToken>rec1</RecordingToken><Mode>Active</Mode><Priority>1</Priority></JobConfiguration>
				</JobItem>
			</GetRecordingJobsResponse>`)
		case strings.Contains(body, "CreateRecordingJob"):
			return soapOK(`<CreateRecordingJobResponse>
				<JobToken>job2</JobToken>
				<JobConfiguration><RecordingToken>rec1</RecordingToken><Mode>Active</Mode><Priority>2</Priority></JobConfiguration>
			</CreateRecordingJobResponse>`)
		default:
			return ""
		}
	})
	jobs, err := NewRecordingService(client).GetRecordingJobs(context.Background())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "job1", jobs[0].Token)
	require.Equal(t, "rec1", jobs[0].Recording)
	require.Equal(t, "Active", jobs[0].Mode)

	created, err := NewRecordingService(client).CreateRecordingJob(context.Background(), "rec1", "Active", 2)
	require.NoError(t, err)
	require.Equal(t, "job2", created.Token)
	require.Equal(t, "rec1", created.Recording)
	require.Equal(t, 2, created.Priority)
}

func TestEventsPullPointAndMessages(t *testing.T) {
	client := recordingEventsClient(t, func(body string) string {
		switch {
		case strings.Contains(body, "CreatePullPointSubscription"):
			return soapOK(`<CreatePullPointSubscriptionResponse>
				<SubscriptionReference><Address>http://127.0.0.1/sub</Address></SubscriptionReference>
			</CreatePullPointSubscriptionResponse>`)
		case strings.Contains(body, "PullMessages"):
			return soapOK(`<PullMessagesResponse>
				<NotificationMessage>
					<Topic>tns1:VideoSource/MotionAlarm</Topic>
					<Message>
						<Message UtcTime="2026-01-02T03:04:05Z">
							<Source><SimpleItem Name="VideoSource" Value="vs0"/></Source>
							<Data><SimpleItem Name="State" Value="true"/></Data>
						</Message>
					</Message>
				</NotificationMessage>
			</PullMessagesResponse>`)
		case strings.Contains(body, "Renew"), strings.Contains(body, "Unsubscribe"):
			return soapOK(`<Response/>`)
		case strings.Contains(body, "GetEventProperties"):
			return soapOK(`<GetEventPropertiesResponse><TopicSet><t name="Motion"/></TopicSet></GetEventPropertiesResponse>`)
		case strings.Contains(body, "GetServiceCapabilities"):
			return soapOK(`<GetServiceCapabilitiesResponse>
				<Capabilities WSPullPointSupport="true" WSSubscriptionPolicySupport="false" PersistentNotificationStorage="true"/>
			</GetServiceCapabilitiesResponse>`)
		default:
			return ""
		}
	})
	svc := NewEventsService(client)
	sub, err := svc.CreatePullPointSubscription(context.Background())
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1/sub", sub.Reference)

	events, err := svc.PullMessages(context.Background(), client.endpoints.Events.String(), time.Second, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "tns1:VideoSource/MotionAlarm", events[0].Topic)
	require.Equal(t, "vs0", events[0].Source["VideoSource"])
	require.Equal(t, "true", events[0].Data["State"])
	require.False(t, events[0].Timestamp.IsZero())

	require.NoError(t, svc.Renew(context.Background(), client.endpoints.Events.String(), time.Minute))
	require.NoError(t, svc.Unsubscribe(context.Background(), client.endpoints.Events.String()))

	props, err := svc.GetEventProperties(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"Motion"}, props["topics"])
	caps, err := svc.GetEventServiceCapabilities(context.Background())
	require.NoError(t, err)
	require.True(t, caps["ws_pull_point_support"])
	require.True(t, caps["persistent_notification_storage"])
	require.False(t, caps["ws_subscription_policy"])

	none := NewEventsService(&Client{endpoints: &ServiceEndpoints{}})
	_, err = none.CreatePullPointSubscription(context.Background())
	require.ErrorContains(t, err, "Events service not available")
	_, err = none.GetEventProperties(context.Background())
	require.ErrorContains(t, err, "Events service not available")
}
