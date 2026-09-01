package autodiscover

import (
	"context"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

type fakeEnroller struct {
	added   []config.CameraConfig
	updated []string
}

func (f *fakeEnroller) AddCamera(_ context.Context, cam config.CameraConfig) (string, error) {
	if cam.ID == "" {
		cam.ID = "cam-1"
	}
	f.added = append(f.added, cam)
	return cam.ID, nil
}

func (f *fakeEnroller) UpdateONVIFEndpoint(_ context.Context, cameraID, endpoint string) error {
	f.updated = append(f.updated, cameraID+"="+endpoint)
	return nil
}

func (f *fakeEnroller) RestartRecorder(context.Context, string) error { return nil }

func TestAdderPendingWhenAuthFails(t *testing.T) {
	enroller := &fakeEnroller{}
	adder := NewAdder(config.AutoDiscoverConfig{}, enroller, nil, nil, func(context.Context, string, string, string) (*onvif.DeviceInfo, error) {
		return nil, context.DeadlineExceeded
	})
	adder.HandleDiscovered(context.Background(), onvif.DiscoveredDevice{
		UUID:     "urn:uuid:abc",
		Name:     "Front",
		Endpoint: "http://192.168.1.50/onvif/device_service",
	})
	if len(enroller.added) != 1 {
		t.Fatalf("expected 1 camera, got %d", len(enroller.added))
	}
	if enroller.added[0].ActivationState != config.ActivationPending {
		t.Fatalf("activation=%q", enroller.added[0].ActivationState)
	}
}

func TestAdderActiveWhenDeviceInfoOK(t *testing.T) {
	enroller := &fakeEnroller{}
	adder := NewAdder(config.AutoDiscoverConfig{}, enroller, nil, nil, func(context.Context, string, string, string) (*onvif.DeviceInfo, error) {
		return &onvif.DeviceInfo{SerialNumber: "SN-1", Manufacturer: "Acme", Model: "Cam"}, nil
	})
	adder.HandleDiscovered(context.Background(), onvif.DiscoveredDevice{
		UUID:     "urn:uuid:abc",
		Endpoint: "http://192.168.1.50/onvif/device_service",
	})
	if len(enroller.added) != 1 {
		t.Fatalf("expected 1 camera, got %d", len(enroller.added))
	}
	cam := enroller.added[0]
	if cam.ActivationState != config.ActivationActive {
		t.Fatalf("activation=%q", cam.ActivationState)
	}
	if cam.StableID != "SN-1" {
		t.Fatalf("stable_id=%q", cam.StableID)
	}
}

func TestAdderDedupesSameEndpoint(t *testing.T) {
	enroller := &fakeEnroller{}
	adder := NewAdder(config.AutoDiscoverConfig{}, enroller, nil, nil, func(context.Context, string, string, string) (*onvif.DeviceInfo, error) {
		return &onvif.DeviceInfo{SerialNumber: "SN-1"}, nil
	})
	d := onvif.DiscoveredDevice{UUID: "u", Endpoint: "http://10.0.0.8/onvif/device_service"}
	adder.HandleDiscovered(context.Background(), d)
	adder.HandleDiscovered(context.Background(), d)
	if len(enroller.added) != 1 {
		t.Fatalf("expected dedupe, got %d adds", len(enroller.added))
	}
}

func TestAdderRoamsExistingSerial(t *testing.T) {
	db, err := storage.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := db.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCamera(ctx, "cam-old", "Cam", "onvif", "h264", "http://10.0.0.1/onvif/device_service", "", "", true, "http://10.0.0.1/onvif/device_service", "", "H264"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateCameraStableID(ctx, "cam-old", "SN-9"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateCameraMetadata(ctx, "cam-old", "", "", "", "", "SN-9", 0); err != nil {
		t.Fatal(err)
	}

	enroller := &fakeEnroller{}
	adder := NewAdder(config.AutoDiscoverConfig{}, enroller, db, nil, func(context.Context, string, string, string) (*onvif.DeviceInfo, error) {
		return &onvif.DeviceInfo{SerialNumber: "SN-9"}, nil
	})
	adder.HandleDiscovered(ctx, onvif.DiscoveredDevice{UUID: "u", Endpoint: "http://10.0.0.9/onvif/device_service"})
	if len(enroller.added) != 0 {
		t.Fatalf("should update existing, not add: %+v", enroller.added)
	}
	if len(enroller.updated) != 1 || !strings.Contains(enroller.updated[0], "10.0.0.9") {
		t.Fatalf("expected endpoint update, got %v", enroller.updated)
	}
}
