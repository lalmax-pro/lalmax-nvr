package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

// covChmod changes permissions and always restores them so t.TempDir cleanup works.
func covChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.Chmod(path, mode))
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

// TestCovManagerCreation covers constructor failures and camera dir helpers.
func TestCovManagerCreation(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "not-a-dir")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o644))

	_, err := NewManager(filePath)
	require.ErrorContains(t, err, "failed to create root directory")

	m, err := NewManager(filepath.Join(dir, "root"))
	require.NoError(t, err)

	// A camera directory that collides with a regular file cannot be created.
	cameraID := "cam/blocked"
	require.NoError(t, os.WriteFile(m.CameraDirPath(cameraID), []byte("x"), 0o644))
	require.Error(t, m.EnsureCameraDir(cameraID))
	_, _, err = m.CreateSegment(cameraID, "h264")
	require.ErrorContains(t, err, "failed to create camera dir")

	// Non-portable IDs are encoded and decoded again.
	require.NotEqual(t, cameraID, recordingDiskName(cameraID))
	roundTrip, ok := originalRecordingID(recordingDiskName(cameraID))
	require.True(t, ok)
	require.Equal(t, "cam/blocked", roundTrip)
	decoded, ok := originalRecordingID("id~zz")
	require.False(t, ok)
	require.Empty(t, decoded)
	require.Equal(t, "id~69647e3030", recordingDiskName("id~00"), "the reserved prefix is encoded again")
	require.Equal(t, "id~434f4e", recordingDiskName("CON"), "Windows device names are encoded")
}

// TestCovManagerSegmentFormats covers every supported segment format.
func TestCovManagerSegmentFormats(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	for _, format := range []string{"h264", "h265", "aac", "g711", "opus", "H264"} {
		tempPath, finalPath, err := m.CreateSegment("cam-format", format)
		require.NoError(t, err, format)
		info, err := os.Stat(tempPath)
		require.NoError(t, err)
		require.False(t, info.IsDir(), "media formats use a temp file")
		require.Contains(t, finalPath, m.CameraRecordingPrefix("cam-format"))
		require.True(t, len(filepath.Base(finalPath)) > 0)
		require.NoError(t, m.CloseSegment(tempPath, finalPath))
		_, err = os.Stat(finalPath)
		require.NoError(t, err)
	}

	_, _, err = m.CreateSegment("cam-format", "vp9")
	require.ErrorContains(t, err, "unsupported format")
}

// TestCovManagerCloseSegmentErrors covers the atomic-rename failure paths.
func TestCovManagerCloseSegmentErrors(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	err = m.CloseSegment(filepath.Join(dir, "missing.tmp"), filepath.Join(dir, "missing.mp4"))
	require.ErrorContains(t, err, "temp path not found")

	// A file cannot be renamed onto a non-empty directory.
	tempFile := filepath.Join(dir, "seg.tmp")
	require.NoError(t, os.WriteFile(tempFile, []byte("data"), 0o644))
	blockingDir := filepath.Join(dir, "blocked.mp4")
	require.NoError(t, os.MkdirAll(filepath.Join(blockingDir, "child"), 0o755))
	require.Error(t, m.CloseSegment(tempFile, blockingDir))

	// Directory segments rename like file segments.
	tempDirSegment, finalDirSegment, err := m.CreateSegment("cam-mjpeg", "mjpeg")
	require.NoError(t, err)
	require.NoError(t, m.CloseSegment(tempDirSegment, finalDirSegment))
	info, err := os.Stat(finalDirSegment)
	require.NoError(t, err)
	require.True(t, info.IsDir())

	// Unreadable temp directories cannot be opened for fsync.
	tempDir2, _, err := m.CreateSegment("cam-mjpeg", "mjpeg")
	require.NoError(t, err)
	covChmod(t, tempDir2, 0o000)
	require.Error(t, m.CloseSegment(tempDir2, filepath.Join(dir, "unreadable-final")))
}

// TestCovManagerWriteFrameErrors covers write failures for files and frames.
func TestCovManagerWriteFrameErrors(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	_, err = m.WriteFrame(filepath.Join(dir, "missing.tmp"), []byte("x"))
	require.ErrorContains(t, err, "temp path not accessible")

	// Read-only temp file: opening it for append fails.
	tempPath, _, err := m.CreateSegment("cam-write", "h264")
	require.NoError(t, err)
	covChmod(t, tempPath, 0o400)
	_, err = m.WriteFrame(tempPath, []byte("payload"))
	require.ErrorContains(t, err, "failed to open temp file")

	// MJPEG frames land as files inside the segment directory.
	tempDirSegment, _, err := m.CreateSegment("cam-frames", "mjpeg")
	require.NoError(t, err)
	n, err := m.WriteFrame(tempDirSegment, []byte("jpeg"))
	require.NoError(t, err)
	require.Zero(t, n, "frame writes report no byte count")
	frames, err := os.ReadDir(tempDirSegment)
	require.NoError(t, err)
	require.Len(t, frames, 1)
	require.Contains(t, frames[0].Name(), ".jpg")

	// A read-only segment directory cannot accept new frames.
	readOnlySegment, _, err := m.CreateSegment("cam-frames-ro", "mjpeg")
	require.NoError(t, err)
	covChmod(t, readOnlySegment, 0o555)
	_, err = m.WriteFrame(readOnlySegment, []byte("jpeg"))
	require.ErrorContains(t, err, "failed to write JPEG frame")
}

// TestCovManagerDirectoryErrors covers listing, deletion and cleanup failures.
func TestCovManagerDirectoryErrors(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	require.NoError(t, err)

	_, err = m.ListFiles("cam-missing")
	require.ErrorContains(t, err, "cannot read camera dir")
	_, err = m.ListCameraDirEntries("cam-missing")
	require.ErrorContains(t, err, "cannot read camera dir")

	_, err = m.GetFileSize(filepath.Join(dir, "missing.mp4"))
	require.ErrorContains(t, err, "cannot stat")

	// Removal fails when the parent directory is not writable.
	require.NoError(t, m.EnsureCameraDir("cam-locked"))
	require.NoError(t, os.WriteFile(filepath.Join(m.CameraDirPath("cam-locked"), "a.mp4"), []byte("x"), 0o644))
	covChmod(t, dir, 0o555)
	require.Error(t, m.DeleteCameraDir("cam-locked"))
	covChmod(t, dir, 0o755)
	require.NoError(t, m.DeleteCameraDir("cam-locked"))
	_, err = os.Stat(m.CameraDirPath("cam-locked"))
	require.True(t, os.IsNotExist(err))

	// Cleanup removes temp files/dirs and skips unreadable subtrees.
	require.NoError(t, m.EnsureCameraDir("cam-temp"))
	cameraDir := m.CameraDirPath("cam-temp")
	require.NoError(t, os.WriteFile(filepath.Join(cameraDir, "orphan.tmp"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(cameraDir, "orphan.tmp.d", "nested"), 0o755))
	tmpDir := filepath.Join(cameraDir, "frames.tmp")
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "child"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cameraDir, "keep.mp4"), []byte("keep"), 0o644))

	locked := filepath.Join(cameraDir, "locked")
	require.NoError(t, os.MkdirAll(locked, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(locked, "inner.tmp"), []byte("x"), 0o644))
	covChmod(t, locked, 0o000)

	require.NoError(t, m.CleanupTempFiles())
	_, err = os.Stat(filepath.Join(cameraDir, "orphan.tmp"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(tmpDir)
	require.True(t, os.IsNotExist(err), "temp directories are removed recursively")
	_, err = os.Stat(filepath.Join(cameraDir, "keep.mp4"))
	require.NoError(t, err, "real segments survive cleanup")
	_, err = os.Stat(filepath.Join(locked, "inner.tmp"))
	require.Error(t, err, "the unreadable subtree is skipped, not failed")
	require.False(t, os.IsNotExist(err), "walking never entered the unreadable directory")
}

// TestCovManagerDiskUsage covers disk usage reporting and metrics.
func TestCovManagerDiskUsage(t *testing.T) {
	dir := t.TempDir()
	collector := metrics.NewMetrics()
	m, err := NewManager(dir, collector)
	require.NoError(t, err)

	total, used, err := m.GetDiskUsage()
	require.NoError(t, err)
	require.Positive(t, total)
	require.GreaterOrEqual(t, used, int64(0))

	families, err := collector.Registry.Gather()
	require.NoError(t, err)
	found := false
	for _, family := range families {
		if family.GetName() == "nvr_storage_used_bytes" {
			found = true
			require.Len(t, family.GetMetric(), 1)
			require.Equal(t, float64(used), family.GetMetric()[0].GetGauge().GetValue())
		}
	}
	require.True(t, found, "storage usage is exported")

	// A missing root directory cannot report filesystem stats.
	missing := filepath.Join(dir, "gone")
	m2, err := NewManager(missing)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(missing))
	require.False(t, m2.IsAvailable())
	_, _, err = m2.GetDiskUsage()
	require.ErrorContains(t, err, "failed to stat filesystem")
}

// TestCovManagerReconcileErrors covers reconciliation edge cases.
func TestCovManagerReconcileErrors(t *testing.T) {
	ctx := context.Background()

	// Missing root directory.
	dir := t.TempDir()
	m, err := NewManager(filepath.Join(dir, "root"))
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "root")))
	_, err = m.ReconcileOrphanedFiles(ctx, nil, nil)
	require.Error(t, err)

	// Unreadable and malformed camera directories are skipped.
	m, err = NewManager(filepath.Join(dir, "walk"))
	require.NoError(t, err)
	root := m.RootDir()
	good := filepath.Join(root, "cam-ok")
	require.NoError(t, os.MkdirAll(good, 0o755))
	locked := filepath.Join(root, "cam-locked")
	require.NoError(t, os.MkdirAll(locked, 0o755))
	covChmod(t, locked, 0o000)

	// Malformed names: too few parts, mismatched camera id, bad timestamp, and a
	// non-mp4 file.
	for _, name := range []string{
		"cam-ok_20240101.mp4",
		"other-cam_20240101_120000_1.mp4",
		"cam-ok_notadate_120000_2.mp4",
		"cam-ok_20240101_120000_3.txt",
		"cam-ok_20240101_120000_4.tmp",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(good, name), []byte("data"), 0o644))
	}
	// A directory with an extension is skipped, an empty frame dir is skipped.
	require.NoError(t, os.MkdirAll(filepath.Join(good, "segment.tmp.d"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(good, "cam-ok_20240101_120000_5"), 0o755))

	total, err := m.ReconcileOrphanedFiles(ctx, nil, map[string]bool{"cam-ok": true, "cam-locked": true})
	require.NoError(t, err)
	require.Zero(t, total, "only malformed entries were present")

	// A valid orphan needs a working database.
	valid := filepath.Join(good, "cam-ok_20240101_120000_6.mp4")
	require.NoError(t, os.WriteFile(valid, []byte("data"), 0o644))
	db := newTestDB(t)
	require.NoError(t, db.Close())
	_, err = m.ReconcileOrphanedFiles(ctx, db, map[string]bool{"cam-ok": true})
	require.ErrorContains(t, err, "query existing recordings")

	// With a healthy database the orphan is registered once.
	db = newTestDB(t)
	total, err = m.ReconcileOrphanedFiles(ctx, db, map[string]bool{"cam-ok": true})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	rec, err := db.GetRecording(ctx, "6")
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, "cam-ok", rec.CameraID)
	require.Equal(t, model.FormatH264, rec.Format)
	require.Equal(t, int64(len("data")), rec.FileSize)
	require.True(t, rec.StartedAt.Equal(time.Date(2024, 1, 1, 12, 0, 0, 0, time.Local)))

	total, err = m.ReconcileOrphanedFiles(ctx, db, map[string]bool{"cam-ok": true})
	require.NoError(t, err)
	require.Zero(t, total, "reconciliation is idempotent")
}
