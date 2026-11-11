package camera

// cam3* tests exercise the ONVIF-facing branches of the camera manager against
// the loopback fake device defined in cam3_helpers_test.go.

import (
	"context"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCam3ONVIFClientControlsAndAccessors(t *testing.T) {
	ctx := context.Background()
	dev := cam3NewFakeONVIF(t, cam3DefaultDevice())
	mgr, _ := cam3NewManager(t,
		cam3ONVIFCamera("cam3-onvif-sub", dev.URL(), "sub"),
		cam3ONVIFCamera("cam3-onvif-main", dev.URL(), "main"),
		cam3ONVIFCamera("cam3-onvif-default", dev.URL(), ""),
	)

	client, err := mgr.GetONVIFClient(ctx, "cam3-onvif-sub")
	require.NoError(t, err)
	require.NotNil(t, client)
	cached, err := mgr.GetONVIFClient(ctx, "cam3-onvif-sub")
	require.NoError(t, err)
	require.Same(t, client, cached, "the ONVIF client must be cached per camera")

	ptz, err := mgr.GetONVIFPTZController(ctx, "cam3-onvif-sub")
	require.NoError(t, err)
	require.NotNil(t, ptz)

	imaging, err := mgr.GetImagingController(ctx, "cam3-onvif-main")
	require.NoError(t, err)
	require.NotNil(t, imaging)

	deviceMgr, err := mgr.GetDeviceManager(ctx, "cam3-onvif-main")
	require.NoError(t, err)
	require.NotNil(t, deviceMgr)

	// The preferred sub-stream profile has no snapshot URI, so the provider
	// falls back to the main profile.
	provider, err := mgr.GetSnapshotProvider(ctx, "cam3-onvif-sub")
	require.NoError(t, err)
	require.NotNil(t, provider)
	uri, err := provider.GetSnapshotUri(ctx)
	require.NoError(t, err)
	require.Equal(t, dev.BaseURL()+"/snapshot.jpg", uri)

	streamURL, err := mgr.resolveONVIFStreamURL(ctx, *mgr.GetCameraConfig("cam3-onvif-main"))
	require.NoError(t, err)
	require.Equal(t, "rtsp://admin:secret@127.0.0.1:1/stream/main", streamURL)

	// Without a profile token the first device profile is used.
	streamURL, err = mgr.resolveONVIFStreamURL(ctx, *mgr.GetCameraConfig("cam3-onvif-default"))
	require.NoError(t, err)
	require.Equal(t, "rtsp://admin:secret@127.0.0.1:1/stream/sub", streamURL)

	// An empty URI from the device is a hard error.
	emptyDev := cam3NewFakeONVIF(t, &cam3FakeONVIF{
		Profiles:   []cam3Profile{{Token: "main", Name: "MainStream", Encoding: "h264", VideoSource: "vsrc"}},
		StreamURIs: map[string]string{"main": ""},
	})
	emptyMgr, _ := cam3NewManager(t, cam3ONVIFCamera("cam3-onvif-empty-uri", emptyDev.URL(), "main"))
	_, err = emptyMgr.resolveONVIFStreamURL(ctx, *emptyMgr.GetCameraConfig("cam3-onvif-empty-uri"))
	require.ErrorContains(t, err, "empty stream URI")

	// Imaging falls back to the profile token when the profile carries no
	// video source token.
	noSourceDev := cam3NewFakeONVIF(t, &cam3FakeONVIF{
		Profiles: []cam3Profile{{Token: "main", Name: "MainStream", Encoding: "h264"}},
	})
	noSourceMgr, _ := cam3NewManager(t, cam3ONVIFCamera("cam3-onvif-nosrc", noSourceDev.URL(), "main"))
	ctrl, err := noSourceMgr.GetImagingController(ctx, "cam3-onvif-nosrc")
	require.NoError(t, err)
	require.NotNil(t, ctrl)
}

func TestCam3ONVIFProfilesAndEncodingProbes(t *testing.T) {
	ctx := context.Background()
	dev := cam3NewFakeONVIF(t, &cam3FakeONVIF{
		Profiles: []cam3Profile{
			{Token: "main", Name: "MainStream", Encoding: "h264", VideoSource: "vsrc-main"},
			{Token: "noenc", Name: "NoEncoder", VideoSource: "vsrc-noenc"},
		},
		Encoders: []cam3Encoder{
			{Token: "noenc", Encoding: "H264"},
			{Token: "00000", Encoding: "H265"},
			{Token: "1", Encoding: "H264"},
		},
	})
	mgr, _ := cam3NewManager(t, cam3ONVIFCamera("cam3-probe", dev.URL(), "noenc"))
	mgr.SetMediaEngine(&stubMediaEngine{})

	cam := *mgr.GetCameraConfig("cam3-probe")

	profiles, err := mgr.loadONVIFProfiles(ctx, cam)
	require.NoError(t, err)
	require.Len(t, profiles, 2)

	// A profile without encoder information is resolved through
	// GetVideoEncoderConfigurations.
	prepared, err := mgr.prepareCameraForStart(ctx, cam)
	require.NoError(t, err)
	require.Equal(t, "h264", prepared.Encoding)
	require.Equal(t, "H264", prepared.StreamEncoding)

	require.Equal(t, "h265", mgr.probeEncodingFromVideoEncoderConfigs(ctx, cam, "MediaProfile00000"))
	// A token without the Dahua prefix falls back to the first numeric suffix.
	require.Equal(t, "h264", mgr.probeEncodingFromVideoEncoderConfigs(ctx, cam, "cam1"))
	// No matching configuration and more than one candidate is a failed probe.
	require.Empty(t, mgr.probeEncodingFromVideoEncoderConfigs(ctx, cam, "MediaProfile999"))

	// The endpoint falls back to the camera URL when no ONVIF endpoint is set.
	urlOnly := cam
	urlOnly.ONVIFEndpoint = ""
	profiles, err = mgr.loadONVIFProfiles(ctx, urlOnly)
	require.NoError(t, err)
	require.Len(t, profiles, 2)

	named := cam
	named.ProfileToken = "main"
	require.Equal(t, "MainStream", mgr.getONVIFProfileName(ctx, named))

	unknown := cam
	unknown.ProfileToken = "does-not-exist"
	require.Equal(t, "MainStream", mgr.getONVIFProfileName(ctx, unknown))

	noEndpoint := cam
	noEndpoint.ONVIFEndpoint = ""
	noEndpoint.URL = ""
	require.Empty(t, mgr.getONVIFProfileName(ctx, noEndpoint))

	// A single encoder configuration is used even when the token cannot be
	// derived from the profile token.
	singleDev := cam3NewFakeONVIF(t, &cam3FakeONVIF{
		Profiles: []cam3Profile{{Token: "main", Name: "MainStream", Encoding: "h264", VideoSource: "vsrc"}},
		Encoders: []cam3Encoder{{Token: "only", Encoding: "H265"}},
	})
	singleMgr, _ := cam3NewManager(t, cam3ONVIFCamera("cam3-single-enc", singleDev.URL(), "main"))
	singleCam := *singleMgr.GetCameraConfig("cam3-single-enc")
	require.Equal(t, "h265", singleMgr.probeEncodingFromVideoEncoderConfigs(ctx, singleCam, "zzz"))

	// A device that rejects GetVideoEncoderConfigurations yields no encoding.
	failEncDev := cam3NewFakeONVIF(t, &cam3FakeONVIF{
		Profiles:     []cam3Profile{{Token: "main", Name: "MainStream", Encoding: "h264", VideoSource: "vsrc"}},
		FailEncoders: true,
	})
	failEncMgr, _ := cam3NewManager(t, cam3ONVIFCamera("cam3-failencoders", failEncDev.URL(), "main"))
	failEncCam := *failEncMgr.GetCameraConfig("cam3-failencoders")
	require.Empty(t, failEncMgr.probeEncodingFromVideoEncoderConfigs(ctx, failEncCam, "main"))
}

func TestCam3ONVIFEmptyProfilesAndMissingSnapshotURI(t *testing.T) {
	ctx := context.Background()

	emptyDev := cam3NewFakeONVIF(t, &cam3FakeONVIF{NoProfiles: true})
	emptyMgr, _ := cam3NewManager(t, cam3ONVIFCamera("cam3-empty-profiles", emptyDev.URL(), ""))
	_, err := emptyMgr.GetImagingController(ctx, "cam3-empty-profiles")
	require.ErrorContains(t, err, "no media profiles")
	_, err = emptyMgr.GetONVIFPTZController(ctx, "cam3-empty-profiles")
	require.ErrorContains(t, err, "no media profiles")
	_, err = emptyMgr.GetSnapshotProvider(ctx, "cam3-empty-profiles")
	require.ErrorContains(t, err, "no media profiles")
	_, err = emptyMgr.resolveONVIFStreamURL(ctx, *emptyMgr.GetCameraConfig("cam3-empty-profiles"))
	require.ErrorContains(t, err, "no media profiles")
	profiles, err := emptyMgr.loadONVIFProfiles(ctx, *emptyMgr.GetCameraConfig("cam3-empty-profiles"))
	require.NoError(t, err)
	require.Empty(t, profiles)

	// Profiles exist but none exposes a snapshot URI: the last device error is
	// returned to the caller.
	noSnapDev := cam3NewFakeONVIF(t, &cam3FakeONVIF{
		Profiles: []cam3Profile{
			{Token: "sub", Name: "SubStream", Encoding: "h264", VideoSource: "vsrc-sub"},
			{Token: "main", Name: "MainStream", Encoding: "h264", VideoSource: "vsrc-main"},
		},
	})
	noSnapMgr, _ := cam3NewManager(t, cam3ONVIFCamera("cam3-nosnap", noSnapDev.URL(), "sub"))
	_, err = noSnapMgr.GetSnapshotProvider(ctx, "cam3-nosnap")
	require.Error(t, err)

	// Profiles without tokens produce no candidate at all.
	noTokenDev := cam3NewFakeONVIF(t, &cam3FakeONVIF{
		Profiles: []cam3Profile{{Token: "", Name: ""}},
	})
	noTokenMgr, _ := cam3NewManager(t, cam3ONVIFCamera("cam3-notoken", noTokenDev.URL(), ""))
	_, err = noTokenMgr.GetSnapshotProvider(ctx, "cam3-notoken")
	require.ErrorContains(t, err, "no snapshot uri")

	// A device that rejects GetProfiles surfaces the load error.
	failDev := cam3NewFakeONVIF(t, &cam3FakeONVIF{FailProfiles: true})
	failMgr, _ := cam3NewManager(t, cam3ONVIFCamera("cam3-failprofiles", failDev.URL(), ""))
	failCam := *failMgr.GetCameraConfig("cam3-failprofiles")
	_, err = failMgr.loadONVIFProfiles(ctx, failCam)
	require.ErrorContains(t, err, "get profiles")
	require.Empty(t, failMgr.getONVIFProfileName(ctx, failCam))
	_, err = failMgr.resolveONVIFStreamURL(ctx, failCam)
	require.ErrorContains(t, err, "get profiles")

	// An unreachable endpoint fails at connect time.
	badMgr, _ := cam3NewManager(t, cam3ONVIFCamera("cam3-badendpoint", "http://127.0.0.1:1/onvif/device_service", ""))
	_, err = badMgr.loadONVIFProfiles(ctx, *badMgr.GetCameraConfig("cam3-badendpoint"))
	require.ErrorContains(t, err, "connect")
}

func TestCam3ONVIFStableIDSubStreamEventsAndTasks(t *testing.T) {
	ctx := context.Background()
	dev := cam3NewFakeONVIF(t, cam3DefaultDevice())
	mgr, _, _, _ := newTestManager(t)

	id, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:            "cam3-stable",
		Name:          "Stable",
		Protocol:      "onvif",
		Encoding:      "h264",
		ONVIFEndpoint: dev.URL(),
		URL:           dev.URL(),
		Username:      "admin",
		Password:      "secret",
	})
	require.NoError(t, err)
	cam := mgr.GetCameraConfig(id)
	require.NotNil(t, cam)
	require.Empty(t, cam.StableID)

	mgr.ensureStableID(ctx, *cam)
	learned := mgr.GetCameraConfig(id)
	require.Equal(t, "CAM3-SERIAL", learned.StableID)
	require.Equal(t, "CAM3-SERIAL", camIDSerial(mgr, id))

	// A second pass short-circuits: the stable id and serial are known.
	mgr.ensureStableID(ctx, *learned)

	// The sub-stream URL comes from the ONVIF device.
	mgr.SetMediaEngine(&stubMediaEngine{})
	subToken := "main"
	_, err = mgr.UpdateCamera(ctx, id, CameraUpdate{SubProfileToken: &subToken})
	require.NoError(t, err)
	require.NoError(t, mgr.EnsureSubStream(ctx, id))

	// An unreachable device fails stable-id learning and sub-stream resolution.
	badID, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:              "cam3-unreachable",
		Name:            "Unreachable",
		Protocol:        "onvif",
		Encoding:        "h264",
		ONVIFEndpoint:   "http://127.0.0.1:1/onvif/device_service",
		URL:             "http://127.0.0.1:1/onvif/device_service",
		SubProfileToken: "sub",
	})
	require.NoError(t, err)
	bad := mgr.GetCameraConfig(badID)
	require.NotNil(t, bad)
	require.Empty(t, mgr.getONVIFProfileName(ctx, *bad))
	mgr.ensureStableID(ctx, *bad)
	require.Empty(t, mgr.GetCameraConfig(badID).StableID)
	require.Error(t, mgr.EnsureSubStream(ctx, badID))

	// A device that cannot answer GetStreamUri fails sub-stream resolution too.
	noStreamDev := cam3NewFakeONVIF(t, &cam3FakeONVIF{
		Profiles:     []cam3Profile{{Token: "main", Name: "MainStream", Encoding: "h264", VideoSource: "vsrc"}},
		FailStreamUI: true,
	})
	noStreamID, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:              "cam3-nostreamuri",
		Name:            "NoStreamURI",
		Protocol:        "onvif",
		Encoding:        "h264",
		ONVIFEndpoint:   noStreamDev.URL(),
		URL:             noStreamDev.URL(),
		SubProfileToken: "main",
	})
	require.NoError(t, err)
	require.Error(t, mgr.EnsureSubStream(ctx, noStreamID))
	_, err = mgr.resolveONVIFStreamURL(ctx, *mgr.GetCameraConfig(noStreamID))
	require.ErrorContains(t, err, "get stream URI")

	// Event subscription and motion subscription share the cached client.
	require.NoError(t, mgr.SubscribeONVIFEvents(ctx, id, func(onvif.ONVIFEvent) {}))
	require.NoError(t, mgr.UnsubscribeONVIFEvents(ctx, id))

	mgr.SetRecordingModeSource(func(string) (string, bool) { return storage.RecordingModeEvent, true })
	mgr.subscribeMotionIfNeeded(ctx, id)
	require.NoError(t, mgr.UnsubscribeONVIFEvents(ctx, id))

	// A failing subscribe is logged and swallowed.
	mgr.subscribeMotionIfNeeded(ctx, badID)

	// Event-mode activity goes through the event manager.
	em := recorder.NewEventManager(time.Minute, time.Hour, nil, nil, func(string) bool { return true })
	mgr.SetEventManager(em)
	t.Cleanup(em.Stop)
	mgr.HandleActivity(id, "onvif")
	require.True(t, em.IsActive(id))

	// Refreshing the profile token persists the device profile name.
	mainToken := "main"
	updated, err := mgr.UpdateCamera(ctx, id, CameraUpdate{ProfileToken: &mainToken})
	require.NoError(t, err)
	require.Equal(t, "main", updated.ProfileToken)

	// A device whose event service fails surfaces the subscribe error.
	failEventsDev := cam3NewFakeONVIF(t, &cam3FakeONVIF{
		Profiles:   []cam3Profile{{Token: "main", Name: "MainStream", Encoding: "h264", VideoSource: "vsrc"}},
		FailEvents: true,
	})
	failEventsID, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:            "cam3-failevents",
		Name:          "FailEvents",
		Protocol:      "onvif",
		Encoding:      "h264",
		ONVIFEndpoint: failEventsDev.URL(),
		URL:           failEventsDev.URL(),
	})
	require.NoError(t, err)
	require.Error(t, mgr.SubscribeONVIFEvents(ctx, failEventsID, func(onvif.ONVIFEvent) {}))

	// A device that reports no serial number leaves the stable id unset.
	noSerialDev := cam3NewFakeONVIF(t, &cam3FakeONVIF{
		Profiles: []cam3Profile{{Token: "main", Name: "MainStream", Encoding: "h264", VideoSource: "vsrc"}},
		Serial:   "   ",
	})
	noSerialID, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:            "cam3-noserial",
		Name:          "NoSerial",
		Protocol:      "onvif",
		Encoding:      "h264",
		ONVIFEndpoint: noSerialDev.URL(),
		URL:           noSerialDev.URL(),
	})
	require.NoError(t, err)
	noSerialCam := mgr.GetCameraConfig(noSerialID)
	require.NotNil(t, noSerialCam)
	mgr.ensureStableID(ctx, *noSerialCam)
	require.Empty(t, mgr.GetCameraConfig(noSerialID).StableID)

	// Activating a camera whose recorder cannot start still persists the
	// activation state.
	actID, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "cam3-activate",
		Name:     "Activate",
		Protocol: "http",
		Encoding: "h264",
		URL:      "http://127.0.0.1:1/none",
	})
	require.NoError(t, err)
	require.NoError(t, mgr.ActivateCamera(ctx, actID, "user", "pass"))
	require.True(t, mgr.GetCameraConfig(actID).Enabled)

	// Task-backed recording pauses a camera that the plan says must not record.
	tasks := recorder.NewTaskManager(nil, nil, nil, nil, nil, time.Minute)
	mgr.SetRecordTasks(tasks)
	mgr.SetRecordingModeSource(func(string) (string, bool) { return storage.RecordingModeOff, true })
	taskCam := config.CameraConfig{ID: "cam3-task", StreamID: "cam3-task", Protocol: "rtsp", Encoding: "h264", Enabled: true}
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, taskCam)
	mgr.afterRecorderStart(taskCam)
	require.True(t, mgr.pausedRecorders[taskCam.ID])

	// Adaptive triggers reach the task manager when the camera has one.
	mgr.TriggerAdaptive(taskCam.ID, time.Second)
	mgr.TriggerAdaptive("cam3-missing-task", time.Second)
}
