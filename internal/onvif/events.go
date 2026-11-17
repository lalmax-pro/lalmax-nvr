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
const DefaultSubscriptionDuration = time.Minute
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

	eventCallback EventCallback
	pollInterval  time.Duration
	pullTimeout   time.Duration
	messageLimit  int
	subDuration   time.Duration
}

type pullPointSubscription struct {
	subscription *onviflib.PullPointSubscription
	cancel       context.CancelFunc
}

func NewEventSubscriberImpl(client *onviflib.Client, opts ...EventSubscriberOption) *EventSubscriberImpl {
	es := &EventSubscriberImpl{
		client:        client,
		subscriptions: make(map[string]*pullPointSubscription),
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

	sub, err := e.createSubscription(ctx, cameraID)
	if err != nil {
		return err
	}
	// The caller's context covers creation. The subscription lives until Unsubscribe.
	pollCtx, cancel := context.WithCancel(context.Background())
	ps := &pullPointSubscription{subscription: sub, cancel: cancel}
	e.subscriptions[cameraID] = ps
	go e.publishEvents(pollCtx, cameraID, ps)
	return nil
}

func (e *EventSubscriberImpl) createSubscription(ctx context.Context, cameraID string) (*onviflib.PullPointSubscription, error) {
	events := e.client.EventsService()
	sub, err := events.CreatePullPointSubscriptionWithLease(ctx, e.subDuration)
	if err != nil {
		return nil, fmt.Errorf("onvif: create PullPoint subscription for camera %q: %w", cameraID, err)
	}
	if err := sub.SetSynchronizationPoint(ctx); err != nil && ctx.Err() == nil {
		// Keep polling older cameras which do not implement synchronization.
		eventLogger.Debug("PullPoint synchronization failed", "camera_id", cameraID, "error", err)
	}
	if err := ctx.Err(); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = sub.Unsubscribe(cleanupCtx)
		return nil, err
	}
	return sub, nil
}

func (e *EventSubscriberImpl) Unsubscribe(ctx context.Context, cameraID string) error {
	e.mu.Lock()
	ps, exists := e.subscriptions[cameraID]
	if !exists {
		e.mu.Unlock()
		return nil
	}
	delete(e.subscriptions, cameraID)
	ps.cancel()
	sub := ps.subscription
	e.mu.Unlock()

	if sub != nil {
		if err := sub.Unsubscribe(ctx); err != nil {
			eventLogger.Warn("failed to unsubscribe from PullPoint", "camera_id", cameraID, "error", err)
		}
	}
	return nil
}

func (e *EventSubscriberImpl) GetEventMessages(_ context.Context) ([]ONVIFEvent, error) {
	return nil, nil
}

func (e *EventSubscriberImpl) IsSubscribed(cameraID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	ps, exists := e.subscriptions[cameraID]
	return exists && ps.subscription != nil
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

func (e *EventSubscriberImpl) publishEvents(ctx context.Context, cameraID string, ps *pullPointSubscription) {
	interval := e.pollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.pollAndPublish(ctx, cameraID, ps, interval)
		}
	}
}

func (e *EventSubscriberImpl) pollAndPublish(ctx context.Context, cameraID string, ps *pullPointSubscription, interval time.Duration) {
	e.mu.Lock()
	sub := ps.subscription
	e.mu.Unlock()

	if sub == nil {
		replacement, err := e.createSubscription(ctx, cameraID)
		if err != nil {
			if ctx.Err() == nil {
				eventLogger.Warn("PullPoint resubscription failed", "camera_id", cameraID, "error", err)
			}
			return
		}
		e.mu.Lock()
		if ctx.Err() != nil {
			e.mu.Unlock()
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = replacement.Unsubscribe(cleanupCtx)
			return
		}
		ps.subscription = replacement
		sub = replacement
		e.mu.Unlock()
	}

	remaining := time.Until(sub.ExpiresAt())
	if remaining <= interval {
		if err := sub.Renew(ctx, e.subDuration); err != nil && (remaining <= 0 || onviflib.IsSubscriptionExpired(err)) {
			e.mu.Lock()
			ps.subscription = nil
			e.mu.Unlock()
			return
		}
	}
	messages, err := sub.PullMessages(ctx, e.pullTimeout, e.messageLimit)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if onviflib.IsSubscriptionExpired(err) || time.Until(sub.ExpiresAt()) <= 0 {
			e.mu.Lock()
			ps.subscription = nil
			e.mu.Unlock()
		}
		eventLogger.Warn("PullMessages failed", "camera_id", cameraID, "error", err)
		return
	}

	e.mu.Lock()
	callback := e.eventCallback
	e.mu.Unlock()
	for _, msg := range messages {
		if ctx.Err() != nil {
			return
		}
		if callback != nil && msg.Topic != "" {
			callback(ONVIFEvent{Topic: msg.Topic, Timestamp: msg.Timestamp, Data: msg.Data, CameraID: cameraID})
		}
	}
}

var _ EventSubscriber = (*EventSubscriberImpl)(nil)
