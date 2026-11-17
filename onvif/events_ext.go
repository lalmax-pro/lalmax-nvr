package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
)

// Events service extended operations.

// GetEventProperties retrieves event properties (topic set).
func (e *EventsService) GetEventProperties(ctx context.Context) (map[string]interface{}, error) {
	if e.client.endpoints.Events == nil {
		return nil, fmt.Errorf("onvif: Events service not available")
	}

	body := `<GetEventProperties xmlns="http://www.onvif.org/ver10/events/wsdl"/>`

	resp, err := e.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: e.client.endpoints.Events.String(),
		Action:     eventNamespace + "/EventPortType/GetEventPropertiesRequest",
		addressing: true,
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetEventProperties failed: %w", err)
	}

	var result struct {
		XMLName  xml.Name `xml:"GetEventPropertiesResponse"`
		TopicSet struct {
			Topics []struct {
				Name string `xml:"name,attr"`
			} `xml:",any"`
		} `xml:"TopicSet"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	topics := make([]string, 0)
	for _, t := range result.TopicSet.Topics {
		if t.Name != "" {
			topics = append(topics, t.Name)
		}
	}

	return map[string]interface{}{
		"topics": topics,
	}, nil
}

// GetEventServiceCapabilities retrieves event service capabilities.
func (e *EventsService) GetEventServiceCapabilities(ctx context.Context) (map[string]bool, error) {
	if e.client.endpoints.Events == nil {
		return nil, fmt.Errorf("onvif: Events service not available")
	}

	body := `<GetServiceCapabilities xmlns="http://www.onvif.org/ver10/events/wsdl"/>`

	resp, err := e.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL: e.client.endpoints.Events.String(),
		Action:     eventNamespace + "/EventPortType/GetServiceCapabilitiesRequest",
		addressing: true,
		Body:       body,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: GetEventServiceCapabilities failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"GetServiceCapabilitiesResponse"`
		Caps    struct {
			WSSubscriptionPolicySupport                   bool `xml:"WSSubscriptionPolicySupport,attr"`
			WSPullPointSupport                            bool `xml:"WSPullPointSupport,attr"`
			WSPausableSubscriptionManagerInterfaceSupport bool `xml:"WSPausableSubscriptionManagerInterfaceSupport,attr"`
			MaxNotificationProducers                      int  `xml:"MaxNotificationProducers,attr"`
			MaxPullPoints                                 int  `xml:"MaxPullPoints,attr"`
			PersistentNotificationStorage                 bool `xml:"PersistentNotificationStorage,attr"`
		} `xml:"Capabilities"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	return map[string]bool{
		"ws_subscription_policy":          result.Caps.WSSubscriptionPolicySupport,
		"ws_pull_point_support":           result.Caps.WSPullPointSupport,
		"ws_pausable_subscription":        result.Caps.WSPausableSubscriptionManagerInterfaceSupport,
		"persistent_notification_storage": result.Caps.PersistentNotificationStorage,
	}, nil
}
