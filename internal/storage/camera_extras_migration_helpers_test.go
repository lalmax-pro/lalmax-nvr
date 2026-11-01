package storage

import (
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCameraExtrasRoundTrip(t *testing.T) {
	audio := true
	cam := config.CameraConfig{
		Protocol:             "rtsp",
		Encoding:             "H264",
		AudioEnabled:         true,
		SubStreamURL:         "rtsp://127.0.0.1/sub",
		SnapshotURL:          "http://127.0.0.1/snap.jpg",
		SampleInterval:       5,
		HLSMaxFPS:            15,
		FrameWatchdogTimeout: "8s",
		PullRetryNum:         3,
		DID:                  "did-1",
		Vendor:               "stub",
		SourceType:           "rtsp",
		StreamID:             "cam1",
		SubProfileToken:      "sub",
		SubnetHints:          []string{"192.0.2.0/24"},
		Longitude:            121.4,
		Latitude:             31.2,
	}
	raw, err := marshalCameraExtras(cam)
	require.NoError(t, err)
	require.True(t, extrasHasAudioEnabled(raw))

	extras, err := unmarshalCameraExtras(raw)
	require.NoError(t, err)
	require.NotNil(t, extras.AudioEnabled)
	require.Equal(t, audio, *extras.AudioEnabled)

	var out config.CameraConfig
	applyExtrasToCamera(&out, extras)
	require.Equal(t, cam.SubStreamURL, out.SubStreamURL)
	require.Equal(t, cam.SnapshotURL, out.SnapshotURL)
	require.Equal(t, cam.StreamID, out.StreamID)
	require.Equal(t, cam.Longitude, out.Longitude)
	require.Equal(t, cam.Latitude, out.Latitude)
	require.True(t, out.AudioEnabled)

	empty, err := unmarshalCameraExtras("")
	require.NoError(t, err)
	require.Equal(t, cameraExtras{}, empty)
	_, err = unmarshalCameraExtras("{")
	require.Error(t, err)
	require.False(t, extrasHasAudioEnabled(""))
	require.False(t, extrasHasAudioEnabled("{"))
	require.False(t, extrasHasAudioEnabled(`{"name":"cam"}`))
}

func TestMergeConfigFromRow(t *testing.T) {
	require.Nil(t, mergeConfigFromRow(CameraRow{}))
	enabled := true
	interval := "1h"
	window := "6h"
	batch := 8
	age := "30s"
	minSeg := 4
	rolling := true
	debounce := "5s"
	cfg := mergeConfigFromRow(CameraRow{
		MergeEnabled:            &enabled,
		MergeCheckInterval:      &interval,
		MergeWindowSize:         &window,
		MergeBatchLimit:         &batch,
		MergeMinSegmentAge:      &age,
		MergeMinSegmentsToMerge: &minSeg,
		MergeRollingEnabled:     &rolling,
		MergeRollingDebounce:    &debounce,
	})
	require.NotNil(t, cfg)
	require.True(t, cfg.Enabled)
	require.Equal(t, "1h", cfg.CheckInterval)
	require.Equal(t, "6h", cfg.WindowSize)
	require.Equal(t, 8, cfg.BatchLimit)
	require.Equal(t, "30s", cfg.MinSegmentAge)
	require.Equal(t, 4, cfg.MinSegmentsToMerge)
	require.NotNil(t, cfg.RollingEnabled)
	require.True(t, *cfg.RollingEnabled)
	require.Equal(t, "5s", cfg.RollingDebounce)
}

func TestMigrationQuoteAndIdentity(t *testing.T) {
	quoted, err := migrationQuoteIdentifier("cameras", DriverSQLite)
	require.NoError(t, err)
	require.Equal(t, `"cameras"`, quoted)
	quoted, err = migrationQuoteIdentifier("cameras", DriverMySQL)
	require.NoError(t, err)
	require.Equal(t, "`cameras`", quoted)
	_, err = migrationQuoteIdentifier("cam-eras", DriverSQLite)
	require.ErrorContains(t, err, "unsafe SQL identifier")
	_, err = migrationQuoteIdentifier("1cameras", DriverPostgres)
	require.Error(t, err)

	id := migrationIdentity("file.db", DriverSQLite)
	require.Len(t, id, 64)
	require.NotEqual(t, id, migrationIdentity("file.db", DriverMySQL))
}

func TestSQLiteBool(t *testing.T) {
	require.Equal(t, true, sqliteBool(int64(7)))
	require.Equal(t, false, sqliteBool(int64(0)))
	require.Equal(t, true, sqliteBool(1))
	require.Equal(t, false, sqliteBool(0))
	require.Equal(t, true, sqliteBool([]byte("1")))
	require.Equal(t, true, sqliteBool("TRUE"))
	require.Equal(t, false, sqliteBool("no"))
	require.Equal(t, true, sqliteBool(true))
}
