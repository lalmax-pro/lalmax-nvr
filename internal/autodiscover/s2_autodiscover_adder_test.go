package autodiscover

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// s2DiscoveryRepo is a scriptable storage.CameraDiscoveryRepository.
type s2DiscoveryRepo struct {
	bySerial      *storage.CameraRow
	bySerialErr   error
	byEndpoint    *storage.CameraRow
	byEndpointErr error
	metadata      []string
	stableIDs     []string
	activations   []string
	updateErr     error
}

func (r *s2DiscoveryRepo) FindCameraBySerial(context.Context, string) (*storage.CameraRow, error) {
	return r.bySerial, r.bySerialErr
}

func (r *s2DiscoveryRepo) FindCameraByEndpoint(context.Context, string) (*storage.CameraRow, error) {
	return r.byEndpoint, r.byEndpointErr
}

func (r *s2DiscoveryRepo) UpdateCameraMetadata(_ context.Context, id, _, _, brand, model, serial string, _ int) error {
	r.metadata = append(r.metadata, id+"|"+brand+"|"+model+"|"+serial)
	return r.updateErr
}

func (r *s2DiscoveryRepo) UpdateCameraActivation(_ context.Context, id, state string) error {
	r.activations = append(r.activations, id+"|"+state)
	return r.updateErr
}

func (r *s2DiscoveryRepo) UpdateCameraStableID(_ context.Context, id, stableID string) error {
	r.stableIDs = append(r.stableIDs, id+"|"+stableID)
	return r.updateErr
}

// s2FailingEnroller always fails to enroll.
type s2FailingEnroller struct{ err error }

func (e s2FailingEnroller) AddCamera(context.Context, config.CameraConfig) (string, error) {
	return "", e.err
}
func (e s2FailingEnroller) UpdateONVIFEndpoint(context.Context, string, string) error { return nil }
func (e s2FailingEnroller) RestartRecorder(context.Context, string) error             { return nil }

func s2AdderConfig() config.AutoDiscoverConfig {
	return config.AutoDiscoverConfig{Enabled: s2Bool(true), ListenForHello: s2Bool(false)}
}

func s2DiscoveredDevice() onvif.DiscoveredDevice {
	return onvif.DiscoveredDevice{
		Endpoint: "http://192.168.1.50/onvif/device_service",
		Name:     "Front Door",
		UUID:     "uuid-1234",
	}
}

func s2InfoFunc(t *testing.T) DeviceInfoFunc {
	t.Helper()
	return func(_ context.Context, endpoint, username, password string) (*onvif.DeviceInfo, error) {
		require.Equal(t, "http://192.168.1.50/onvif/device_service", endpoint)
		require.Empty(t, username)
		require.Empty(t, password)
		return &onvif.DeviceInfo{Manufacturer: "Acme", Model: "Cam 4K", SerialNumber: "SN-42"}, nil
	}
}

func TestS2AdderEnrollsWithDatabaseAndBus(t *testing.T) {
	enroller := &fakeEnroller{}
	repo := &s2DiscoveryRepo{}
	bus := event.NewEventBus(4)
	events := make(chan event.Event, 4)
	require.NoError(t, bus.Subscribe(TopicCameraAdded, events, 4))

	adder := NewAdder(s2AdderConfig(), enroller, repo, bus, s2InfoFunc(t))
	adder.HandleDiscovered(context.Background(), s2DiscoveredDevice())

	require.Len(t, enroller.added, 1)
	cam := enroller.added[0]
	require.Equal(t, "Front Door", cam.Name)
	require.Equal(t, "onvif", cam.Protocol)
	require.Equal(t, "SN-42", cam.StableID)
	require.Equal(t, string(config.ActivationActive), cam.ActivationState)
	require.True(t, cam.Enabled)

	// Device metadata, stable id and activation are persisted.
	require.Equal(t, []string{"cam-1|Acme|Cam 4K|SN-42"}, repo.metadata)
	require.Equal(t, []string{"cam-1|SN-42"}, repo.stableIDs)
	require.Equal(t, []string{"cam-1|" + string(config.ActivationActive)}, repo.activations)

	// The camera.added product event is published.
	select {
	case ev := <-events:
		payload, ok := ev.Data.(map[string]any)
		require.True(t, ok)
		require.Equal(t, "cam-1", payload["camera_id"])
		require.Equal(t, "auto", payload["source"])
	case <-time.After(time.Second):
		t.Fatal("camera.added event was not published")
	}
}

func TestS2AdderSkipsWhenDatabaseErrorsOrFindsCamera(t *testing.T) {
	// A serial lookup failure falls through to the endpoint lookup.
	repo := &s2DiscoveryRepo{bySerialErr: errors.New("lookup exploded")}
	enroller := &fakeEnroller{}
	adder := NewAdder(s2AdderConfig(), enroller, repo, nil, s2InfoFunc(t))
	adder.HandleDiscovered(context.Background(), s2DiscoveredDevice())
	require.Len(t, enroller.added, 1)
	require.Equal(t, []string{"cam-1|Acme|Cam 4K|SN-42"}, repo.metadata)

	// An already known endpoint is ignored.
	repo = &s2DiscoveryRepo{byEndpoint: &storage.CameraRow{ID: "known"}}
	enroller = &fakeEnroller{}
	adder = NewAdder(s2AdderConfig(), enroller, repo, nil, s2InfoFunc(t))
	adder.HandleDiscovered(context.Background(), s2DiscoveredDevice())
	require.Empty(t, enroller.added)

	// An unknown endpoint with an endpoint lookup error still enrolls.
	repo = &s2DiscoveryRepo{byEndpointErr: errors.New("lookup exploded")}
	enroller = &fakeEnroller{}
	adder = NewAdder(s2AdderConfig(), enroller, repo, nil, s2InfoFunc(t))
	adder.HandleDiscovered(context.Background(), s2DiscoveredDevice())
	require.Len(t, enroller.added, 1)
}

func TestS2AdderHandlesEnrollmentFailureAndEmptyEndpoint(t *testing.T) {
	// Empty endpoints never reach the enroller.
	enroller := &fakeEnroller{}
	adder := NewAdder(s2AdderConfig(), enroller, nil, nil, s2InfoFunc(t))
	adder.HandleDiscovered(context.Background(), onvif.DiscoveredDevice{Endpoint: "   "})
	require.Empty(t, enroller.added)

	// Enrollment errors are logged and do not touch the database.
	repo := &s2DiscoveryRepo{}
	failing := s2FailingEnroller{err: errors.New("enroll exploded")}
	adder = NewAdder(s2AdderConfig(), failing, repo, nil, s2InfoFunc(t))
	adder.HandleDiscovered(context.Background(), s2DiscoveredDevice())
	require.Empty(t, repo.metadata)
	require.Empty(t, repo.activations)
}

func TestS2AdderPendingWhenProfilesUnavailable(t *testing.T) {
	// With credentials configured and an unenrichable device the camera is
	// enrolled as pending with no credentials stored.
	enroller := &fakeEnroller{}
	cfg := s2AdderConfig()
	cfg.DefaultUsername = "admin"
	cfg.DefaultPassword = "secret"
	infoFn := func(context.Context, string, string, string) (*onvif.DeviceInfo, error) {
		return nil, errors.New("device unreachable")
	}
	adder := NewAdder(cfg, enroller, nil, nil, infoFn)
	adder.HandleDiscovered(context.Background(), onvif.DiscoveredDevice{
		Endpoint: "http://192.168.1.60/onvif/device_service", UUID: "uuid-pending",
	})

	require.Len(t, enroller.added, 1)
	cam := enroller.added[0]
	require.Equal(t, string(config.ActivationPending), cam.ActivationState)
	require.Empty(t, cam.Username)
	require.Empty(t, cam.Password)
	require.True(t, cam.Enabled)
}

// s2RenameEnroller lets the roaming tests inject endpoint/restart failures.
type s2RenameEnroller struct {
	updateErr  error
	restartErr error
	updated    []string
	restarted  []string
}

func (e *s2RenameEnroller) AddCamera(context.Context, config.CameraConfig) (string, error) {
	return "cam-new", nil
}

func (e *s2RenameEnroller) UpdateONVIFEndpoint(_ context.Context, cameraID, endpoint string) error {
	e.updated = append(e.updated, cameraID+"="+endpoint)
	return e.updateErr
}

func (e *s2RenameEnroller) RestartRecorder(_ context.Context, cameraID string) error {
	e.restarted = append(e.restarted, cameraID)
	return e.restartErr
}

func TestS2AdderHandleExistingRoaming(t *testing.T) {
	ctx := context.Background()
	endpoint := "http://192.168.1.50/onvif/device_service"

	// Archived cameras are ignored.
	archived := &s2DiscoveryRepo{bySerial: &storage.CameraRow{ID: "cam-arch", Archived: true}}
	enroller := &s2RenameEnroller{}
	adder := NewAdder(s2AdderConfig(), enroller, archived, nil, s2InfoFunc(t))
	adder.HandleDiscovered(ctx, s2DiscoveredDevice())
	require.Empty(t, enroller.updated)

	// A camera already on the endpoint is left alone.
	sameEndpoint := &s2DiscoveryRepo{bySerial: &storage.CameraRow{ID: "cam-same", ONVIFEndpoint: endpoint}}
	enroller = &s2RenameEnroller{}
	adder = NewAdder(s2AdderConfig(), enroller, sameEndpoint, nil, s2InfoFunc(t))
	adder.HandleDiscovered(ctx, s2DiscoveredDevice())
	require.Empty(t, enroller.updated)

	// An empty stored endpoint falls back to comparing the URL.
	urlOnly := &s2DiscoveryRepo{bySerial: &storage.CameraRow{ID: "cam-url", URL: endpoint + "/"}}
	enroller = &s2RenameEnroller{}
	adder = NewAdder(s2AdderConfig(), enroller, urlOnly, nil, s2InfoFunc(t))
	adder.HandleDiscovered(ctx, s2DiscoveredDevice())
	require.Empty(t, enroller.updated)

	// A changed endpoint updates the camera and restarts its recorder.
	roamed := &s2DiscoveryRepo{bySerial: &storage.CameraRow{ID: "cam-roam", ONVIFEndpoint: "http://10.0.0.9/onvif/device_service"}}
	enroller = &s2RenameEnroller{}
	adder = NewAdder(s2AdderConfig(), enroller, roamed, nil, s2InfoFunc(t))
	adder.HandleDiscovered(ctx, s2DiscoveredDevice())
	require.Equal(t, []string{"cam-roam=" + endpoint}, enroller.updated)
	require.Equal(t, []string{"cam-roam"}, enroller.restarted)

	// Endpoint update failures stop the roam without restarting.
	roamed = &s2DiscoveryRepo{bySerial: &storage.CameraRow{ID: "cam-roam", ONVIFEndpoint: "http://10.0.0.9/onvif/device_service"}}
	enroller = &s2RenameEnroller{updateErr: errors.New("update exploded")}
	adder = NewAdder(s2AdderConfig(), enroller, roamed, nil, s2InfoFunc(t))
	adder.HandleDiscovered(ctx, s2DiscoveredDevice())
	require.Empty(t, enroller.restarted)

	// Restart failures are logged but the update is kept.
	roamed = &s2DiscoveryRepo{bySerial: &storage.CameraRow{ID: "cam-roam", ONVIFEndpoint: "http://10.0.0.9/onvif/device_service"}}
	enroller = &s2RenameEnroller{restartErr: errors.New("restart exploded")}
	adder = NewAdder(s2AdderConfig(), enroller, roamed, nil, s2InfoFunc(t))
	adder.HandleDiscovered(ctx, s2DiscoveredDevice())
	require.Equal(t, []string{"cam-roam"}, enroller.restarted)
}
