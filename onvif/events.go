package onvif

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	eventNamespace           = "http://www.onvif.org/ver10/events/wsdl"
	pullPointAction          = eventNamespace + "/PullPointSubscription/"
	subscriptionAction       = "http://docs.oasis-open.org/wsn/bw-2/SubscriptionManager/"
	defaultSubscriptionLease = time.Minute
)

// EventsService handles event operations.
type EventsService struct {
	client *Client
}

func NewEventsService(client *Client) *EventsService {
	return &EventsService{client: client}
}

// PullPointSubscription identifies a device subscription and its negotiated lease.
type PullPointSubscription struct {
	Reference           string
	Timeout             time.Duration // Initial lease.
	service             *EventsService
	referenceParameters referenceParameters
	mu                  sync.Mutex
	expires             time.Time
}

type subscriptionTimes struct {
	CurrentTime     string `xml:"CurrentTime"`
	TerminationTime string `xml:"TerminationTime"`
}

func (sub *PullPointSubscription) updateLease(times subscriptionTimes) {
	current, currentErr := time.Parse(time.RFC3339Nano, times.CurrentTime)
	until, untilErr := time.Parse(time.RFC3339Nano, times.TerminationTime)
	if currentErr == nil && untilErr == nil {
		sub.mu.Lock()
		sub.expires = time.Now().Add(until.Sub(current))
		sub.mu.Unlock()
	}
}

// ExpiresAt returns the local deadline for the most recently reported device lease.
func (sub *PullPointSubscription) ExpiresAt() time.Time {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	return sub.expires
}

type referenceParameters string

func (p *referenceParameters) UnmarshalXML(decoder *xml.Decoder, _ xml.StartElement) error {
	var buffer strings.Builder
	encoder := xml.NewEncoder(&buffer)
	depth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch t := token.(type) {
		case xml.StartElement:
			attrs := make([]xml.Attr, 0, len(t.Attr)+1)
			for _, attr := range t.Attr {
				if attr.Name.Local == "xmlns" && attr.Name.Space == "" {
					continue // The encoder declares the resolved element namespace.
				}
				if attr.Name.Space == "xmlns" {
					attr.Name = xml.Name{Local: "xmlns:" + attr.Name.Local}
				}
				if attr.Name != (xml.Name{Space: addressingNamespace, Local: "IsReferenceParameter"}) {
					attrs = append(attrs, attr)
				}
			}
			if depth == 0 {
				attrs = append(attrs, xml.Attr{Name: xml.Name{Space: addressingNamespace, Local: "IsReferenceParameter"}, Value: "true"})
			}
			t.Attr = attrs
			token = t
			depth++
		case xml.EndElement:
			if depth == 0 {
				if err := encoder.Flush(); err != nil {
					return err
				}
				*p = referenceParameters(buffer.String())
				return nil
			}
			depth--
		}
		if err := encoder.EncodeToken(token); err != nil {
			return err
		}
	}
}

func durationXML(duration time.Duration) string {
	return "PT" + strconv.FormatFloat(duration.Seconds(), 'f', -1, 64) + "S"
}

// CreatePullPointSubscription creates a subscription with a one-minute initial lease.
func (s *EventsService) CreatePullPointSubscription(ctx context.Context) (*PullPointSubscription, error) {
	return s.CreatePullPointSubscriptionWithLease(ctx, defaultSubscriptionLease)
}

// CreatePullPointSubscriptionWithLease requests the given initial lease.
func (s *EventsService) CreatePullPointSubscriptionWithLease(ctx context.Context, lease time.Duration) (*PullPointSubscription, error) {
	if s.client.endpoints.Events == nil {
		return nil, fmt.Errorf("onvif: Events service not available")
	}
	if lease <= 0 {
		lease = defaultSubscriptionLease
	}
	address, err := resolveONVIFEndpoint(s.client.soap.origin, s.client.endpoints.Events.String())
	if err != nil {
		return nil, fmt.Errorf("onvif: CreatePullPointSubscription failed: %w", err)
	}
	request := &SOAPRequest{
		ServiceURL: address.String(),
		Action:     eventNamespace + "/EventPortType/CreatePullPointSubscriptionRequest",
		Body: fmt.Sprintf(`<CreatePullPointSubscription xmlns="%s"><InitialTerminationTime>%s</InitialTerminationTime></CreatePullPointSubscription>`,
			eventNamespace, durationXML(lease)),
		addressing: true,
	}
	resp, err := s.client.soap.SendContext(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("onvif: CreatePullPointSubscription failed: %w", err)
	}

	var result struct {
		XMLName xml.Name `xml:"CreatePullPointSubscriptionResponse"`
		subscriptionTimes
		SubscriptionReference struct {
			Address    string              `xml:"Address"`
			Parameters referenceParameters `xml:"ReferenceParameters"`
		} `xml:"SubscriptionReference"`
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}
	endpoint, err := resolveONVIFEndpoint(address, result.SubscriptionReference.Address)
	if err != nil {
		return nil, fmt.Errorf("onvif: invalid PullPoint subscription reference: %w", err)
	}
	sub := &PullPointSubscription{
		Reference:           endpoint.String(),
		Timeout:             lease,
		service:             s,
		referenceParameters: result.SubscriptionReference.Parameters,
		expires:             time.Now().Add(lease),
	}
	sub.updateLease(result.subscriptionTimes)
	sub.Timeout = time.Until(sub.expires).Round(time.Millisecond)
	return sub, nil
}

// PullMessages pulls messages for an address-only subscription.
func (s *EventsService) PullMessages(ctx context.Context, subscriptionRef string, timeout time.Duration, messageLimit int) ([]Event, error) {
	sub := PullPointSubscription{Reference: subscriptionRef, service: s}
	return sub.PullMessages(ctx, timeout, messageLimit)
}

// PullMessages pulls notifications with this subscription's full endpoint reference.
func (sub *PullPointSubscription) PullMessages(ctx context.Context, timeout time.Duration, messageLimit int) ([]Event, error) {
	if timeout <= 0 {
		timeout = time.Second
	}
	if messageLimit <= 0 {
		messageLimit = 100
	}
	body := fmt.Sprintf(`<PullMessages xmlns="%s"><Timeout>%s</Timeout><MessageLimit>%d</MessageLimit></PullMessages>`,
		eventNamespace, durationXML(timeout), messageLimit)
	resp, err := sub.send(ctx, "PullMessages", pullPointAction, body, timeout)
	if err != nil {
		return nil, err
	}

	var result struct {
		XMLName xml.Name `xml:"PullMessagesResponse"`
		subscriptionTimes
		NotificationMessage []struct {
			Topic   string `xml:"Topic"`
			Message struct {
				Message struct {
					Source struct {
						SimpleItem []struct {
							Name  string `xml:"Name,attr"`
							Value string `xml:"Value,attr"`
						} `xml:"SimpleItem"`
					} `xml:"Source"`
					Data struct {
						SimpleItem []struct {
							Name  string `xml:"Name,attr"`
							Value string `xml:"Value,attr"`
						} `xml:"SimpleItem"`
					} `xml:"Data"`
					UtcTime string `xml:"UtcTime,attr"`
				} `xml:"Message"`
			} `xml:"Message"`
		} `xml:"NotificationMessage"`
	}

	if err := ParseResponseBody(resp.Body, &result); err != nil {
		return nil, err
	}

	sub.updateLease(result.subscriptionTimes)
	events := make([]Event, 0, len(result.NotificationMessage))
	for _, msg := range result.NotificationMessage {
		event := Event{
			Topic:  strings.TrimSpace(msg.Topic),
			Source: make(map[string]interface{}),
			Data:   make(map[string]interface{}),
		}

		// Parse timestamp
		if msg.Message.Message.UtcTime != "" {
			if t, err := time.Parse(time.RFC3339, msg.Message.Message.UtcTime); err == nil {
				event.Timestamp = t
			}
		}

		// Parse source
		for _, item := range msg.Message.Message.Source.SimpleItem {
			event.Source[item.Name] = item.Value
		}

		// Parse data
		for _, item := range msg.Message.Message.Data.SimpleItem {
			event.Data[item.Name] = item.Value
		}

		events = append(events, event)
	}

	return events, nil
}

// Renew renews an address-only subscription.
func (s *EventsService) Renew(ctx context.Context, subscriptionRef string, terminationTime time.Duration) error {
	sub := PullPointSubscription{Reference: subscriptionRef, service: s}
	return sub.Renew(ctx, terminationTime)
}

// Renew extends this subscription and updates its negotiated deadline.
func (sub *PullPointSubscription) Renew(ctx context.Context, lease time.Duration) error {
	body := fmt.Sprintf(`<Renew xmlns="http://docs.oasis-open.org/wsn/b-2"><TerminationTime>%s</TerminationTime></Renew>`, durationXML(lease))
	resp, err := sub.send(ctx, "Renew", subscriptionAction, body, 0)
	if err != nil {
		return err
	}
	var result struct {
		XMLName xml.Name
		subscriptionTimes
	}
	if err := ParseResponseBody(resp.Body, &result); err != nil {
		if len(strings.TrimSpace(string(resp.Body))) != 0 {
			return err
		}
	} else if result.XMLName.Local != "RenewResponse" && result.XMLName.Local != "Response" {
		return fmt.Errorf("onvif: unexpected Renew response %s", result.XMLName.Local)
	}
	sub.mu.Lock()
	sub.expires = time.Now().Add(lease) // Older devices may omit the timestamps.
	sub.mu.Unlock()
	sub.updateLease(result.subscriptionTimes)
	return nil
}

// SetSynchronizationPoint requests the device's current property states.
func (sub *PullPointSubscription) SetSynchronizationPoint(ctx context.Context) error {
	_, err := sub.send(ctx, "SetSynchronizationPoint", pullPointAction,
		`<SetSynchronizationPoint xmlns="http://www.onvif.org/ver10/events/wsdl"/>`, 0)
	return err
}

// Unsubscribe unsubscribes from an address-only subscription.
func (s *EventsService) Unsubscribe(ctx context.Context, subscriptionRef string) error {
	sub := PullPointSubscription{Reference: subscriptionRef, service: s}
	return sub.Unsubscribe(ctx)
}

// Unsubscribe terminates this subscription.
func (sub *PullPointSubscription) Unsubscribe(ctx context.Context) error {
	_, err := sub.send(ctx, "Unsubscribe", subscriptionAction, `<Unsubscribe xmlns="http://docs.oasis-open.org/wsn/b-2"/>`, 0)
	return err
}

func (sub *PullPointSubscription) send(ctx context.Context, operation, actionBase, body string, pollTimeout time.Duration) (*SOAPResponse, error) {
	resp, err := sub.service.client.soap.SendContext(ctx, &SOAPRequest{
		ServiceURL:          sub.Reference,
		Action:              actionBase + operation + "Request",
		Body:                body,
		addressing:          true,
		referenceParameters: string(sub.referenceParameters),
		pollTimeout:         pollTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("onvif: %s failed: %w", operation, err)
	}
	return resp, nil
}

// IsSubscriptionExpired reports whether the device no longer recognizes the subscription.
func IsSubscriptionExpired(err error) bool {
	var fault *SOAPFault
	var status *soapHTTPError
	return (errors.As(err, &fault) && (fault.hasSubcode("ResourceUnknown") || fault.hasSubcode("ResourceUnknownFault") ||
		strings.HasSuffix(fault.Code, ":ResourceUnknownFault"))) ||
		(errors.As(err, &status) && (status.status == http.StatusGone || status.status == http.StatusNotFound))
}
