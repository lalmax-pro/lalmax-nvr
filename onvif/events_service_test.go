package onvif

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEventsServiceCapabilitiesNotAvailable(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string { return "" })
	client := serviceClient(t, server, false, false, false, false)
	_, err := NewEventsService(client).GetEventServiceCapabilities(context.Background())
	require.ErrorContains(t, err, "Events service not available")
}

func TestCreatePullPointSubscriptionErrors(t *testing.T) {
	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "no events")
	})
	_, err := NewEventsService(leftoverRecordingClient(t, fault)).CreatePullPointSubscription(context.Background())
	require.ErrorContains(t, err, "CreatePullPointSubscription failed")

	bad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<CreatePullPointSubscriptionResponse><`)
	})
	_, err = NewEventsService(leftoverRecordingClient(t, bad)).CreatePullPointSubscription(context.Background())
	require.Error(t, err)
}

func TestPullMessagesDefaultLimitAndInvalidTime(t *testing.T) {
	server := newONVIFDeviceServer(t, func(body string) string {
		require.Contains(t, body, "<MessageLimit>100</MessageLimit>")
		return soapOK(`<PullMessagesResponse>
			<NotificationMessage>
				<Topic>t</Topic>
				<Message>
					<Message UtcTime="not-rfc3339">
						<Source><SimpleItem Name="k" Value="v"/></Source>
					</Message>
				</Message>
			</NotificationMessage>
		</PullMessagesResponse>`)
	})
	client := leftoverRecordingClient(t, server)
	events, err := NewEventsService(client).PullMessages(context.Background(), client.endpoints.Events.String(), time.Second, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.True(t, events[0].Timestamp.IsZero())
	require.Equal(t, "v", events[0].Source["k"])
}

func TestPullMessagesErrors(t *testing.T) {
	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "timeout")
	})
	_, err := NewEventsService(leftoverRecordingClient(t, fault)).PullMessages(context.Background(), "http://sub", time.Second, 1)
	require.ErrorContains(t, err, "PullMessages failed")

	bad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<PullMessagesResponse><`)
	})
	_, err = NewEventsService(leftoverRecordingClient(t, bad)).PullMessages(context.Background(), "http://sub", time.Second, 1)
	require.Error(t, err)
}

func TestRenewAndUnsubscribeErrors(t *testing.T) {
	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "gone")
	})
	svc := NewEventsService(leftoverRecordingClient(t, fault))
	ctx := context.Background()
	require.ErrorContains(t, svc.Renew(ctx, "http://sub", time.Second), "Renew failed")
	require.ErrorContains(t, svc.Unsubscribe(ctx, "http://sub"), "Unsubscribe failed")
}

func TestEventsExtSOAPAndParseErrors(t *testing.T) {
	fault := newONVIFDeviceServer(t, func(body string) string {
		return soapFaultXML("s:Receiver", "denied")
	})
	svc := NewEventsService(leftoverRecordingClient(t, fault))
	ctx := context.Background()
	_, err := svc.GetEventProperties(ctx)
	require.ErrorContains(t, err, "GetEventProperties failed")
	_, err = svc.GetEventServiceCapabilities(ctx)
	require.ErrorContains(t, err, "GetEventServiceCapabilities failed")

	bad := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetEventPropertiesResponse><`)
	})
	_, err = NewEventsService(leftoverRecordingClient(t, bad)).GetEventProperties(ctx)
	require.Error(t, err)

	badCaps := newONVIFDeviceServer(t, func(body string) string {
		return soapOK(`<GetServiceCapabilitiesResponse><`)
	})
	_, err = NewEventsService(leftoverRecordingClient(t, badCaps)).GetEventServiceCapabilities(ctx)
	require.Error(t, err)
}
