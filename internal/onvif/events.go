package onvif

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	onviflib "github.com/lalmax-pro/lalmax-nvr/onvif"
)

var eventLogger = slog.Default().With("component", "onvif-events")

const DefaultPollInterval = 5 * time.Second
const DefaultPullTimeout = 30 * time.Second
const DefaultMessageLimit = 10
const DefaultSubscriptionDuration = 24 * time.Hour
const SubscriptionRenewBefore = 1 * time.Hour

type EventCallback func(event ONVIFEvent)

type EventSubscriberOption func(*EventSubscriberImpl)

func WithEventCallback(cb EventCallback) EventSubscriberOption {
	return func(es *EventSubscriberImpl) {
		es.eventCallback = cb
	}
}

func WithPollInterval(d time.Duration) EventSubscriberOption {
	return func(es *EventSubscriberImpl) {
		es.pollInterval = d
	}
}

func WithPullTimeout(d time.Duration) EventSubscriberOption {
	return func(es *EventSubscriberImpl) {
		es.pullTimeout = d
	}
}

func WithSubscriptionDuration(d time.Duration) EventSubscriberOption {
	return func(es *EventSubscriberImpl) {
		es.subDuration = d
	}
}

// EventSubscriberImpl implements EventSubscriber using the standalone onvif library.
type EventSubscriberImpl struct {
	client *onviflib.Client

	mu            sync.Mutex
	subscriptions map[string]*pullPointSubscription
	stopCh        map[string]chan struct{}

	eventCallback EventCallback
	pollInterval  time.Duration
	pullTimeout   time.Duration
	messageLimit  int
	subDuration   time.Duration
}

type pullPointSubscription struct {
	subscriptionRef string
	terminationTime time.Time
	active          bool
}

func NewEventSubscriberImpl(client *onviflib.Client, opts ...EventSubscriberOption) *EventSubscriberImpl {
	es := &EventSubscriberImpl{
		client:        client,
		subscriptions: make(map[string]*pullPointSubscription),
		stopCh:        make(map[string]chan struct{}),
		pollInterval:  DefaultPollInterval,
		pullTimeout:   DefaultPullTimeout,
		messageLimit:  DefaultMessageLimit,
		subDuration:   DefaultSubscriptionDuration,
	}
	for _, opt := range opts {
		opt(es)
	}
	return es
}

func (e *EventSubscriberImpl) Subscribe(ctx context.Context, cameraID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.subscriptions[cameraID]; exists {
		return nil
	}

	events := e.client.EventsService()
	sub, err := events.CreatePullPointSubscription(ctx)
	if err != nil {
		return fmt.Errorf("onvif: create PullPoint subscription for camera %q: %w", cameraID, err)
	}

	ps := &pullPointSubscription{
		subscriptionRef: sub.Reference,
		terminationTime: time.Now().Add(e.subDuration),
		active:          true,
	}
	e.subscriptions[cameraID] = ps

	stopCh := make(chan struct{})
	e.stopCh[cameraID] = stopCh

	eventLogger.Info("created PullPoint subscription",
		"camera_id", cameraID,
		"subscription_ref", sub.Reference)

	go e.publishEvents(context.Background(), cameraID, ps, stopCh)

	return nil
}

func (e *EventSubscriberImpl) Unsubscribe(ctx context.Context, cameraID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	ps, exists := e.subscriptions[cameraID]
	if !exists {
		return nil
	}

	if stopCh, ok := e.stopCh[cameraID]; ok {
		close(stopCh)
		delete(e.stopCh, cameraID)
	}

	ps.active = false

	events := e.client.EventsService()
	if err := events.Unsubscribe(ctx, ps.subscriptionRef); err != nil {
		eventLogger.Warn("failed to unsubscribe from PullPoint",
			"camera_id", cameraID,
			"error", err)
	}

	delete(e.subscriptions, cameraID)
	return nil
}

func (e *EventSubscriberImpl) GetEventMessages(_ context.Context) ([]ONVIFEvent, error) {
	return nil, nil
}

func (e *EventSubscriberImpl) IsSubscribed(cameraID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	ps, exists := e.subscriptions[cameraID]
	return exists && ps.active
}

func (e *EventSubscriberImpl) StopAll(ctx context.Context) {
	e.mu.Lock()
	cameraIDs := make([]string, 0, len(e.subscriptions))
	for id := range e.subscriptions {
		cameraIDs = append(cameraIDs, id)
	}
	e.mu.Unlock()

	for _, id := range cameraIDs {
		_ = e.Unsubscribe(ctx, id)
	}
}

func (e *EventSubscriberImpl) SetEventCallback(cb EventCallback) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.eventCallback = cb
}

func (e *EventSubscriberImpl) publishEvents(ctx context.Context, cameraID string, ps *pullPointSubscription, stopCh <-chan struct{}) {
	ticker := time.NewTicker(e.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !e.pollAndPublish(ctx, cameraID, ps) {
				return
			}
		}
	}
}

func (e *EventSubscriberImpl) pollAndPublish(ctx context.Context, cameraID string, ps *pullPointSubscription) bool {
	if time.Until(ps.terminationTime) < SubscriptionRenewBefore {
		events := e.client.EventsService()
		if err := events.Renew(ctx, ps.subscriptionRef, e.subDuration); err != nil {
			eventLogger.Warn("subscription renewal failed, stopping polling",
				"camera_id", cameraID, "error", err)
			ps.active = false
			return false
		}
		ps.terminationTime = time.Now().Add(e.subDuration)
	}

	events := e.client.EventsService()
	messages, err := events.PullMessages(ctx, ps.subscriptionRef, e.pullTimeout, e.messageLimit)
	if err != nil {
		eventLogger.Warn("PullMessages failed",
			"camera_id", cameraID,
			"error", err)
		return true
	}

	e.mu.Lock()
	callback := e.eventCallback
	e.mu.Unlock()

	for _, msg := range messages {
		event := ONVIFEvent{
			Topic:     msg.Topic,
			Timestamp: msg.Timestamp,
			Data:      msg.Data,
			CameraID:  cameraID,
		}

		if event.Topic == "" {
			continue
		}

		if callback != nil {
			callback(event)
		}
	}

	return true
}

var _ EventSubscriber = (*EventSubscriberImpl)(nil)
