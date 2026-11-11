package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCovCameraCRUD covers camera insert/update/read/metadata paths.
func TestCovCameraCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Front Door", "rtsp", "h264", "rtsp://cam1/stream", "user", "secret", true,
		"http://cam1/onvif", "profile-1", "h264"))
	require.NoError(t, db.UpsertCamera(ctx, "cam-2", "Back Yard", "gb28181", "h265", "rtsp://cam2/stream", "", "", false,
		"", "", "", "udp"))

	got, err := db.GetCamera(ctx, "cam-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "Front Door", got.Name)
	require.Equal(t, "rtsp", got.Protocol)
	require.Equal(t, "h264", got.Encoding)
	require.Equal(t, "tcp", got.RTSPTransport, "default transport")
	require.True(t, got.Enabled)
	require.True(t, got.HasPassword)
	require.Equal(t, "http://cam1/onvif", got.ONVIFEndpoint)
	require.Equal(t, "profile-1", got.ProfileToken)
	require.Equal(t, "active", got.ActivationState)
	require.False(t, got.Archived)

	second, err := db.GetCamera(ctx, "cam-2")
	require.NoError(t, err)
	require.Equal(t, "udp", second.RTSPTransport)
	require.False(t, second.Enabled)
	require.False(t, second.HasPassword)

	// Upsert updates in place.
	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Front Door HD", "rtsp", "h265", "rtsp://cam1/main", "", "", true, "http://cam1/onvif2", "profile-2", "h265"))
	updated, err := db.GetCamera(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, "Front Door HD", updated.Name)
	require.Equal(t, "h265", updated.Encoding)
	require.Equal(t, "profile-2", updated.ProfileToken)
	require.False(t, updated.HasPassword, "empty password clears has_password")

	missing, err := db.GetCamera(ctx, "cam-missing")
	require.NoError(t, err)
	require.Nil(t, missing)

	require.NoError(t, db.UpdateCameraProfileName(ctx, "cam-1", "Main Profile"))
	updated, err = db.GetCamera(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, "Main Profile", updated.ProfileName)

	require.NoError(t, db.UpdateCameraMetadata(ctx, "cam-1", "desc", "lobby", "brand", "model-x", "SN123", 14))
	updated, err = db.GetCamera(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, "desc", updated.Description)
	require.Equal(t, "lobby", updated.Location)
	require.Equal(t, "brand", updated.Brand)
	require.Equal(t, "model-x", updated.Model)
	require.Equal(t, "SN123", updated.SerialNumber)
	require.Equal(t, 14, updated.RetentionDays)

	// Activation and identity updates.
	require.NoError(t, db.UpdateCameraActivation(ctx, "cam-1", "pending_activation"))
	updated, err = db.GetCamera(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, "pending_activation", updated.ActivationState)
	require.NoError(t, db.UpdateCameraActivation(ctx, "cam-1", ""))
	updated, err = db.GetCamera(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, "active", updated.ActivationState)

	require.NoError(t, db.UpdateCameraStableID(ctx, "cam-1", "stable-1"))
	updated, err = db.GetCamera(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, "stable-1", updated.StableID)

	// Listings.
	cameras, err := db.ListCameras(ctx)
	require.NoError(t, err)
	require.Len(t, cameras, 2)
	archived, err := db.ListArchivedCameras(ctx)
	require.NoError(t, err)
	require.Empty(t, archived)

	// Archive a camera with a timestamp and check both listings.
	archivedAt := covTestTime(time.Hour)
	_, err = db.execContext(ctx, `UPDATE cameras SET archived=1, archived_at=? WHERE id='cam-2'`, timeToDB(archivedAt))
	require.NoError(t, err)
	cameras, err = db.ListCameras(ctx)
	require.NoError(t, err)
	require.Len(t, cameras, 1)
	archived, err = db.ListArchivedCameras(ctx)
	require.NoError(t, err)
	require.Len(t, archived, 1)
	require.Equal(t, "cam-2", archived[0].ID)
	require.True(t, archived[0].Archived)
	require.NotNil(t, archived[0].ArchivedAt)
	require.True(t, archived[0].ArchivedAt.Equal(archivedAt))

	require.ErrorIs(t, db.DeleteCamera(ctx, "cam-missing"), sql.ErrNoRows)
	require.NoError(t, db.DeleteCamera(ctx, "cam-2"))
	gone, err := db.GetCamera(ctx, "cam-2")
	require.NoError(t, err)
	require.Nil(t, gone)
}

// TestCovUpdateDefaultGB28181CameraName covers the default-name guard.
func TestCovUpdateDefaultGB28181CameraName(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.UpsertCamera(ctx, "gb-1", "GB28181 ch-1", "gb28181", "h264", "rtsp://gb1", "", "", true, "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-1", "GB28181 ch-9", "rtsp", "h264", "rtsp://r1", "", "", true, "", "", ""))

	// Empty inputs are ignored.
	require.NoError(t, db.UpdateDefaultGB28181CameraName(ctx, "", "ch-1", "New"))
	require.NoError(t, db.UpdateDefaultGB28181CameraName(ctx, "gb-1", "", "New"))
	require.NoError(t, db.UpdateDefaultGB28181CameraName(ctx, "gb-1", "ch-1", "   "))

	// Only the generated default name is replaced.
	require.NoError(t, db.UpdateDefaultGB28181CameraName(ctx, "gb-1", "ch-1", "  Lobby  "))
	cam, err := db.GetCamera(ctx, "gb-1")
	require.NoError(t, err)
	require.Equal(t, "Lobby", cam.Name)

	// A second rename no longer matches the default and is a no-op.
	require.NoError(t, db.UpdateDefaultGB28181CameraName(ctx, "gb-1", "ch-1", "Other"))
	cam, err = db.GetCamera(ctx, "gb-1")
	require.NoError(t, err)
	require.Equal(t, "Lobby", cam.Name)

	// Non-GB28181 cameras are never renamed.
	require.NoError(t, db.UpdateDefaultGB28181CameraName(ctx, "rtsp-1", "ch-9", "Renamed"))
	cam, err = db.GetCamera(ctx, "rtsp-1")
	require.NoError(t, err)
	require.Equal(t, "GB28181 ch-9", cam.Name)
}

// TestCovCameraStreamBindings covers stream→camera bindings and extras updates.
func TestCovCameraStreamBindings(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Cam", "rtsp", "h264", "rtsp://cam1", "", "", true, "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "cam-2", "Cam 2", "rtsp", "h264", "rtsp://cam2", "", "", true, "", "", ""))

	require.NoError(t, db.SetCameraStream(ctx, "cam-1", "stream-a"))
	binding, err := db.GetStreamBinding(ctx, "stream-a")
	require.NoError(t, err)
	require.NotNil(t, binding)
	require.Equal(t, "cam-1", binding.CameraID)
	require.False(t, binding.CreatedAt.IsZero())

	cam, err := db.GetCamera(ctx, "cam-1")
	require.NoError(t, err)
	require.NotNil(t, cam)
	// extras_json carries the stream id and is not surfaced on CameraRow.
	var extrasJSON string
	require.NoError(t, db.queryRowContext(ctx, `SELECT extras_json FROM cameras WHERE id='cam-1'`).Scan(&extrasJSON))
	require.Contains(t, extrasJSON, "stream-a")

	// Re-pointing the camera removes the previous binding.
	require.NoError(t, db.SetCameraStream(ctx, "cam-1", "stream-b"))
	oldBinding, err := db.GetStreamBinding(ctx, "stream-a")
	require.NoError(t, err)
	require.Nil(t, oldBinding)
	binding, err = db.GetBindingByCameraID(ctx, "cam-1")
	require.NoError(t, err)
	require.NotNil(t, binding)
	require.Equal(t, "stream-b", binding.StreamID)

	// Unknown camera fails while reading extras.
	require.Error(t, db.SetCameraStream(ctx, "cam-missing", "stream-c"))

	// Corrupt extras JSON is surfaced.
	_, err = db.execContext(ctx, `UPDATE cameras SET extras_json='{not-json' WHERE id='cam-2'`)
	require.NoError(t, err)
	require.Error(t, db.SetCameraStream(ctx, "cam-2", "stream-d"))

	// Direct bind API.
	require.NoError(t, db.BindStreamToCamera(ctx, "stream-e", "cam-2"))
	binding, err = db.GetStreamBinding(ctx, "stream-e")
	require.NoError(t, err)
	require.NotNil(t, binding)
	require.Equal(t, "cam-2", binding.CameraID)

	// Upsert moves the binding to another camera.
	require.NoError(t, db.BindStreamToCamera(ctx, "stream-e", "cam-1"))
	binding, err = db.GetStreamBinding(ctx, "stream-e")
	require.NoError(t, err)
	require.Equal(t, "cam-1", binding.CameraID)

	bindings, err := db.ListStreamBindings(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, bindings)

	missingBinding, err := db.GetStreamBinding(ctx, "stream-none")
	require.NoError(t, err)
	require.Nil(t, missingBinding)
	missingByCamera, err := db.GetBindingByCameraID(ctx, "cam-missing")
	require.NoError(t, err)
	require.Nil(t, missingByCamera)

	require.NoError(t, db.UnbindStreamFromCamera(ctx, "stream-e"))
	require.ErrorIs(t, db.UnbindStreamFromCamera(ctx, "stream-e"), sql.ErrNoRows)
}

// TestCovFindCameraByIdentity covers endpoint/serial discovery helpers.
func TestCovFindCameraByIdentity(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Cam", "rtsp", "h264", "rtsp://cam1/main", "", "", true,
		"http://10.0.0.5/onvif", "", ""))
	require.NoError(t, db.UpdateCameraMetadata(ctx, "cam-1", "", "", "", "", "SN-1", 0))
	require.NoError(t, db.UpdateCameraStableID(ctx, "cam-1", "stable-1"))

	byEndpoint, err := db.FindCameraByEndpoint(ctx, "http://10.0.0.5/onvif")
	require.NoError(t, err)
	require.NotNil(t, byEndpoint)
	require.Equal(t, "cam-1", byEndpoint.ID)

	byURL, err := db.FindCameraByEndpoint(ctx, "  rtsp://cam1/main  ")
	require.NoError(t, err)
	require.NotNil(t, byURL)
	require.Equal(t, "cam-1", byURL.ID)

	emptyEndpoint, err := db.FindCameraByEndpoint(ctx, "   ")
	require.NoError(t, err)
	require.Nil(t, emptyEndpoint)

	noEndpoint, err := db.FindCameraByEndpoint(ctx, "http://unknown/onvif")
	require.NoError(t, err)
	require.Nil(t, noEndpoint)

	bySerial, err := db.FindCameraBySerial(ctx, "SN-1")
	require.NoError(t, err)
	require.NotNil(t, bySerial)
	require.Equal(t, "cam-1", bySerial.ID)

	byStable, err := db.FindCameraBySerial(ctx, "stable-1")
	require.NoError(t, err)
	require.NotNil(t, byStable)
	require.Equal(t, "cam-1", byStable.ID)

	emptySerial, err := db.FindCameraBySerial(ctx, "")
	require.NoError(t, err)
	require.Nil(t, emptySerial)

	noSerial, err := db.FindCameraBySerial(ctx, "SN-missing")
	require.NoError(t, err)
	require.Nil(t, noSerial)
}

// TestCovCameraMigrationsIdempotent covers the activation/rolling migrations.
func TestCovCameraMigrationsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.migrateActivationIdentity(ctx))
	require.NoError(t, db.migrateMergeRolling(ctx))
	// Second run keeps working (columns already present).
	require.NoError(t, db.migrateActivationIdentity(ctx))
	require.NoError(t, db.migrateMergeRolling(ctx))
}

// TestCovCreatedStreams covers created-stream slots and naming.
func TestCovCreatedStreams(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Defaults: app name, input mode and timestamp.
	require.NoError(t, db.InsertCreatedStream(ctx, CreatedStream{StreamID: "live/push-1", Name: "Push One"}))
	stream, err := db.GetCreatedStream(ctx, "live/push-1")
	require.NoError(t, err)
	require.NotNil(t, stream)
	require.Equal(t, "live", stream.AppName)
	require.Equal(t, CreatedStreamPush, stream.InputMode)
	require.False(t, stream.CreatedAt.IsZero())

	// Pull slots keep their source URL.
	require.NoError(t, db.InsertCreatedStream(ctx, CreatedStream{
		StreamID: "live/pull-1", Name: "Pull One", AppName: "live", InputMode: CreatedStreamPull,
		SourceURL: "rtsp://source/1", CreatedAt: covTestTime(0),
	}))
	pull, err := db.GetCreatedStream(ctx, "live/pull-1")
	require.NoError(t, err)
	require.NotNil(t, pull)
	require.Equal(t, CreatedStreamPull, pull.InputMode)
	require.Equal(t, "rtsp://source/1", pull.SourceURL)
	require.True(t, pull.CreatedAt.Equal(covTestTime(0)))

	missing, err := db.GetCreatedStream(ctx, "live/nope")
	require.NoError(t, err)
	require.Nil(t, missing)

	// A duplicate stream id violates the primary key.
	require.Error(t, db.InsertCreatedStream(ctx, CreatedStream{StreamID: "live/push-1", Name: "dup"}))

	list, err := db.ListCreatedStreams(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)

	// Renaming an existing row preserves input mode and source URL.
	require.NoError(t, db.SetCreatedStreamName(ctx, "live/pull-1", " Pull Renamed "))
	renamed, err := db.GetCreatedStream(ctx, "live/pull-1")
	require.NoError(t, err)
	require.Equal(t, "Pull Renamed", renamed.Name)
	require.Equal(t, CreatedStreamPull, renamed.InputMode)
	require.Equal(t, "rtsp://source/1", renamed.SourceURL)

	// An unknown stream gets a push slot with the stream ID as fallback name.
	require.NoError(t, db.SetCreatedStreamName(ctx, " live/new ", "   "))
	fallback, err := db.GetCreatedStream(ctx, "live/new")
	require.NoError(t, err)
	require.NotNil(t, fallback)
	require.Equal(t, "live/new", fallback.Name)
	require.Equal(t, CreatedStreamPush, fallback.InputMode)

	require.ErrorContains(t, db.SetCreatedStreamName(ctx, "   ", "name"), "stream_id is required")

	deleted, err := db.DeleteCreatedStream(ctx, "live/new")
	require.NoError(t, err)
	require.True(t, deleted)
	deleted, err = db.DeleteCreatedStream(ctx, "live/new")
	require.NoError(t, err)
	require.False(t, deleted)

	require.False(t, IsUniqueViolation(nil))
	require.False(t, IsUniqueViolation(context.Canceled))
	require.True(t, IsUniqueViolation(errUniqueTest))
}

var errUniqueTest = covUniqueError{}

type covUniqueError struct{}

func (covUniqueError) Error() string { return "UNIQUE constraint failed: created_streams.stream_id" }

// TestCovCameraClosedDBErrorPaths asserts every camera helper propagates errors.
func TestCovCameraClosedDBErrorPaths(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Close())

	require.Error(t, db.UpsertCamera(ctx, "c", "n", "p", "e", "u", "", "", true, "", "", ""))
	_, err := db.ListCameras(ctx)
	require.Error(t, err)
	_, err = db.ListArchivedCameras(ctx)
	require.Error(t, err)
	_, err = db.GetCamera(ctx, "c")
	require.Error(t, err)
	require.Error(t, db.DeleteCamera(ctx, "c"))
	require.Error(t, db.UpdateCameraMetadata(ctx, "c", "", "", "", "", "", 0))
	require.Error(t, db.UpdateCameraProfileName(ctx, "c", "p"))
	require.Error(t, db.SetCameraStream(ctx, "c", "s"))
	require.Error(t, db.BindStreamToCamera(ctx, "s", "c"))
	require.Error(t, db.UnbindStreamFromCamera(ctx, "s"))
	_, err = db.GetStreamBinding(ctx, "s")
	require.Error(t, err)
	_, err = db.ListStreamBindings(ctx)
	require.Error(t, err)
	_, err = db.GetBindingByCameraID(ctx, "c")
	require.Error(t, err)
	require.Error(t, db.UpdateCameraActivation(ctx, "c", "active"))
	require.Error(t, db.UpdateCameraStableID(ctx, "c", "s"))
	_, err = db.FindCameraByEndpoint(ctx, "endpoint")
	require.Error(t, err)
	_, err = db.FindCameraBySerial(ctx, "serial")
	require.Error(t, err)
	require.Error(t, db.migrateActivationIdentity(ctx))
	require.Error(t, db.migrateMergeRolling(ctx))

	require.Error(t, db.InsertCreatedStream(ctx, CreatedStream{StreamID: "s"}))
	_, err = db.GetCreatedStream(ctx, "s")
	require.Error(t, err)
	require.Error(t, db.SetCreatedStreamName(ctx, "s", "n"))
	_, err = db.ListCreatedStreams(ctx)
	require.Error(t, err)
	_, err = db.DeleteCreatedStream(ctx, "s")
	require.Error(t, err)
	require.Error(t, db.UpdateDefaultGB28181CameraName(ctx, "c", "ch", "n"))
	require.Error(t, db.UpsertCamera(ctx, "c", "n", "p", "e", "u", "", "", true, "", "", ""))
}
