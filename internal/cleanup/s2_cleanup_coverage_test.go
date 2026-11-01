package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// s2FakeRepo is a scriptable storage.CleanupRepository. Nil function fields
// return zero values so each test can override only the call it needs.
type s2FakeRepo struct {
	countRecordings         func(context.Context) (int, error)
	listCameras             func(context.Context) ([]storage.CameraRow, error)
	listExpired             func(context.Context, string, int) ([]model.Recording, error)
	listOldest              func(context.Context, int) ([]model.Recording, error)
	deleteRecording         func(context.Context, string) error
	listArchivedCameras     func(context.Context) ([]storage.CameraRow, error)
	listExpiredArchived     func(context.Context, string, int) ([]model.Recording, error)
	countRecordingsCamera   func(context.Context, string) (int, error)
	deleteCamera            func(context.Context, string) error
	deleteHealthEvents      func(context.Context, time.Time) (int64, error)
	listRecordingPaths      func(context.Context, string) (map[string]bool, error)
	listPendingMJPEG        func(context.Context, string) ([]model.Recording, error)
	setMergeStatus          func(context.Context, []string, string) error
	repairZeroDuration      func(context.Context) ([]model.Recording, error)
	updateRecordingDuration func(context.Context, string, float64, time.Time) error
}

func (f *s2FakeRepo) CountRecordings(ctx context.Context) (int, error) {
	if f.countRecordings != nil {
		return f.countRecordings(ctx)
	}
	return 0, nil
}

func (f *s2FakeRepo) ListCameras(ctx context.Context) ([]storage.CameraRow, error) {
	if f.listCameras != nil {
		return f.listCameras(ctx)
	}
	return nil, nil
}

func (f *s2FakeRepo) ListExpiredRecordingsByCamera(ctx context.Context, cameraID string, days int) ([]model.Recording, error) {
	if f.listExpired != nil {
		return f.listExpired(ctx, cameraID, days)
	}
	return nil, nil
}

func (f *s2FakeRepo) ListOldestRecordings(ctx context.Context, limit int) ([]model.Recording, error) {
	if f.listOldest != nil {
		return f.listOldest(ctx, limit)
	}
	return nil, nil
}

func (f *s2FakeRepo) DeleteRecording(ctx context.Context, id string) error {
	if f.deleteRecording != nil {
		return f.deleteRecording(ctx, id)
	}
	return nil
}

func (f *s2FakeRepo) ListArchivedCameras(ctx context.Context) ([]storage.CameraRow, error) {
	if f.listArchivedCameras != nil {
		return f.listArchivedCameras(ctx)
	}
	return nil, nil
}

func (f *s2FakeRepo) ListExpiredArchivedRecordingsByCamera(ctx context.Context, cameraID string, days int) ([]model.Recording, error) {
	if f.listExpiredArchived != nil {
		return f.listExpiredArchived(ctx, cameraID, days)
	}
	return nil, nil
}

func (f *s2FakeRepo) CountRecordingsByCamera(ctx context.Context, cameraID string) (int, error) {
	if f.countRecordingsCamera != nil {
		return f.countRecordingsCamera(ctx, cameraID)
	}
	return 0, nil
}

func (f *s2FakeRepo) DeleteCamera(ctx context.Context, cameraID string) error {
	if f.deleteCamera != nil {
		return f.deleteCamera(ctx, cameraID)
	}
	return nil
}

func (f *s2FakeRepo) DeleteHealthEventsBefore(ctx context.Context, before time.Time) (int64, error) {
	if f.deleteHealthEvents != nil {
		return f.deleteHealthEvents(ctx, before)
	}
	return 0, nil
}

func (f *s2FakeRepo) ListRecordingPathsByCamera(ctx context.Context, cameraID string) (map[string]bool, error) {
	if f.listRecordingPaths != nil {
		return f.listRecordingPaths(ctx, cameraID)
	}
	return nil, nil
}

func (f *s2FakeRepo) ListPendingMJPEGRecordings(ctx context.Context, cameraID string) ([]model.Recording, error) {
	if f.listPendingMJPEG != nil {
		return f.listPendingMJPEG(ctx, cameraID)
	}
	return nil, nil
}

func (f *s2FakeRepo) SetMergeStatus(ctx context.Context, ids []string, status string) error {
	if f.setMergeStatus != nil {
		return f.setMergeStatus(ctx, ids, status)
	}
	return nil
}

func (f *s2FakeRepo) RepairZeroDurationRecordings(ctx context.Context) ([]model.Recording, error) {
	if f.repairZeroDuration != nil {
		return f.repairZeroDuration(ctx)
	}
	return nil, nil
}

func (f *s2FakeRepo) UpdateRecordingDuration(ctx context.Context, id string, duration float64, endedAt time.Time) error {
	if f.updateRecordingDuration != nil {
		return f.updateRecordingDuration(ctx, id, duration, endedAt)
	}
	return nil
}

func s2Manager(t *testing.T, repo storage.CleanupRepository, store *storage.Manager, cfg config.CleanupConfig, m *metrics.Metrics) *CleanupManager {
	t.Helper()
	var opts []*metrics.Metrics
	if m != nil {
		opts = append(opts, m)
	}
	cm, err := NewCleanupManager(repo, store, cfg, opts...)
	require.NoError(t, err)
	return cm
}

func s2Store(t *testing.T) *storage.Manager {
	t.Helper()
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	return store
}

// s2WriteFile creates a file (and parents) with the given age.
func s2WriteFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o644))
	if age > 0 {
		ts := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(path, ts, ts))
	}
}

func TestS2NewCleanupManagerVariants(t *testing.T) {
	store := s2Store(t)
	repo := &s2FakeRepo{}

	m := metrics.NewMetrics()
	cm := s2Manager(t, repo, store, defaultCleanupConfig(), m)
	require.Same(t, m, cm.metrics)

	_, err := NewCleanupManager(repo, store, config.CleanupConfig{CheckInterval: "not-a-duration"})
	require.Error(t, err)

	cm, err = NewCleanupManager(repo, store, config.CleanupConfig{CheckInterval: "0s", RetentionDays: 1})
	require.NoError(t, err)
	require.Equal(t, time.Hour, cm.interval)
}

func TestS2RunPasses(t *testing.T) {
	store := s2Store(t)
	cfg := defaultCleanupConfig()
	cfg.DiskThresholdPercent = 0
	repo := &s2FakeRepo{
		listCameras: func(context.Context) ([]storage.CameraRow, error) {
			return nil, errors.New("camera listing exploded")
		},
		countRecordings: func(context.Context) (int, error) {
			return 0, errors.New("count exploded")
		},
		deleteHealthEvents: func(context.Context, time.Time) (int64, error) {
			return 0, errors.New("health delete exploded")
		},
		deleteCamera: func(context.Context, string) error { return nil },
	}
	cm := s2Manager(t, repo, store, cfg, metrics.NewMetrics())
	cm.SetHealthConfig(true, time.Hour)

	// An already-cancelled context still runs one (failing) pass and returns.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		cm.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	// The periodic branch also runs when the ticker fires.
	cfg.CheckInterval = "2ms"
	cm = s2Manager(t, repo, store, cfg, metrics.NewMetrics())
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done = make(chan struct{})
	go func() {
		cm.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after context timeout")
	}
}

func TestS2RunOnceAggregatesProblems(t *testing.T) {
	store := s2Store(t)
	require.NoError(t, os.RemoveAll(store.RootDir()))

	repo := &s2FakeRepo{
		listCameras: func(context.Context) ([]storage.CameraRow, error) {
			return nil, errors.New("cannot list cameras")
		},
		countRecordings: func(context.Context) (int, error) {
			return 0, errors.New("cannot count")
		},
	}
	cm := s2Manager(t, repo, store, defaultCleanupConfig(), metrics.NewMetrics())
	cm.SetHealthConfig(true, 0) // retention <= 0 short-circuits health cleanup

	err := cm.RunOnce(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "recording count")
	require.Contains(t, err.Error(), "time-based cleanup")
	require.Contains(t, err.Error(), "disk-threshold cleanup")

	// Recording count gauge is refreshed on success (restore the storage root).
	require.NoError(t, os.MkdirAll(store.RootDir(), 0o755))
	repo.countRecordings = func(context.Context) (int, error) { return 7, nil }
	repo.listCameras = func(context.Context) ([]storage.CameraRow, error) { return nil, nil }
	require.NoError(t, cm.RunOnce(context.Background()))

	// Health retention cleanup error is swallowed but logged, and a positive
	// retention goes through the delete call.
	repo.deleteHealthEvents = func(context.Context, time.Time) (int64, error) {
		return 0, errors.New("health store unavailable")
	}
	cm.SetHealthConfig(true, time.Hour)
	require.NoError(t, cm.RunOnce(context.Background()))
}

func TestS2TimeBasedCleanupBranches(t *testing.T) {
	store := s2Store(t)
	ctx := context.Background()

	// Global retention of zero skips every camera.
	repo := &s2FakeRepo{
		listCameras: func(context.Context) ([]storage.CameraRow, error) {
			return []storage.CameraRow{{ID: "cam1"}, {ID: "cam2", RetentionDays: -1}}, nil
		},
	}
	cm := s2Manager(t, repo, store, config.CleanupConfig{CheckInterval: "1h"}, nil)
	require.NoError(t, cm.timeBasedCleanup(ctx))

	// Listing expired recordings for one camera fails without aborting the pass.
	repo.listExpired = func(_ context.Context, cameraID string, _ int) ([]model.Recording, error) {
		if cameraID == "cam1" {
			return nil, errors.New("listing exploded")
		}
		return nil, nil
	}
	cm = s2Manager(t, repo, store, defaultCleanupConfig(), nil)
	err := cm.timeBasedCleanup(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "list expired recordings for camera cam1")

	// Successful deletion increments the retention metric.
	file := filepath.Join(store.RootDir(), "cam1", "old.mp4")
	s2WriteFile(t, file, 48*time.Hour)
	repo.listExpired = func(_ context.Context, cameraID string, _ int) ([]model.Recording, error) {
		return []model.Recording{{ID: "rec-old", CameraID: cameraID, FilePath: file}}, nil
	}
	cm = s2Manager(t, repo, store, defaultCleanupConfig(), metrics.NewMetrics())
	require.NoError(t, cm.timeBasedCleanup(ctx))
	_, statErr := os.Stat(file)
	require.True(t, os.IsNotExist(statErr))
}

func TestS2DiskThresholdCleanupBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("disk usage error", func(t *testing.T) {
		store := s2Store(t)
		require.NoError(t, os.RemoveAll(store.RootDir()))
		cm := s2Manager(t, &s2FakeRepo{}, store, defaultCleanupConfig(), nil)
		require.Error(t, cm.diskThresholdCleanup(ctx))
	})

	t.Run("list oldest error", func(t *testing.T) {
		store := s2Store(t)
		repo := &s2FakeRepo{listOldest: func(context.Context, int) ([]model.Recording, error) {
			return nil, errors.New("oldest query exploded")
		}}
		cfg := defaultCleanupConfig()
		cfg.DiskThresholdPercent = 0
		cm := s2Manager(t, repo, store, cfg, nil)
		err := cm.diskThresholdCleanup(ctx)
		require.ErrorContains(t, err, "list oldest recordings")
	})

	t.Run("context cancelled mid batch", func(t *testing.T) {
		store := s2Store(t)
		var cancel context.CancelFunc
		innerCtx, cancelFn := context.WithCancel(ctx)
		cancel = cancelFn
		repo := &s2FakeRepo{listOldest: func(context.Context, int) ([]model.Recording, error) {
			cancel() // cancel between the loop guards
			return []model.Recording{{ID: "a"}, {ID: "b"}}, nil
		}}
		cfg := defaultCleanupConfig()
		cfg.DiskThresholdPercent = 0
		cm := s2Manager(t, repo, store, cfg, nil)
		require.ErrorIs(t, cm.diskThresholdCleanup(innerCtx), context.Canceled)
	})

	t.Run("undeletable batch stops early", func(t *testing.T) {
		store := s2Store(t)
		repo := &s2FakeRepo{
			listOldest: func(_ context.Context, limit int) ([]model.Recording, error) {
				return []model.Recording{{ID: "blocked"}}, nil
			},
			deleteRecording: func(context.Context, string) error { return errors.New("cannot delete") },
		}
		cfg := defaultCleanupConfig()
		cfg.DiskThresholdPercent = 0
		cm := s2Manager(t, repo, store, cfg, nil)
		err := cm.diskThresholdCleanup(ctx)
		require.ErrorContains(t, err, "cannot delete")
		require.Equal(t, 1, strings.Count(err.Error(), "cannot delete"), "failed recordings are not retried in the same pass")
	})

	t.Run("successful delete updates metrics", func(t *testing.T) {
		store := s2Store(t)
		file := filepath.Join(store.RootDir(), "cam1", "disk.mp4")
		s2WriteFile(t, file, 48*time.Hour)
		calls := 0
		repo := &s2FakeRepo{
			listOldest: func(context.Context, int) ([]model.Recording, error) {
				calls++
				if calls == 1 {
					return []model.Recording{{ID: "disk-rec", FilePath: file}}, nil
				}
				return nil, nil
			},
			deleteRecording: func(context.Context, string) error { return nil },
		}
		cfg := defaultCleanupConfig()
		cfg.DiskThresholdPercent = 0
		cm := s2Manager(t, repo, store, cfg, metrics.NewMetrics())
		require.NoError(t, cm.diskThresholdCleanup(ctx))
		_, statErr := os.Stat(file)
		require.True(t, os.IsNotExist(statErr))
	})

	t.Run("recheck disk usage error", func(t *testing.T) {
		store := s2Store(t)
		file := filepath.Join(store.RootDir(), "cam1", "recheck.mp4")
		s2WriteFile(t, file, 48*time.Hour)
		calls := 0
		repo := &s2FakeRepo{
			listOldest: func(context.Context, int) ([]model.Recording, error) {
				calls++
				if calls == 1 {
					return []model.Recording{{ID: "recheck", FilePath: file}}, nil
				}
				return nil, nil
			},
			deleteRecording: func(context.Context, string) error {
				// Break the filesystem between the first and second usage check.
				return os.RemoveAll(store.RootDir())
			},
		}
		cfg := defaultCleanupConfig()
		cfg.DiskThresholdPercent = 0
		cm := s2Manager(t, repo, store, cfg, nil)
		err := cm.diskThresholdCleanup(ctx)
		require.ErrorContains(t, err, "recheck disk usage")
	})
}

func TestS2ArchivedRetentionCleanup(t *testing.T) {
	ctx := context.Background()

	t.Run("list archived cameras error", func(t *testing.T) {
		repo := &s2FakeRepo{listArchivedCameras: func(context.Context) ([]storage.CameraRow, error) {
			return nil, errors.New("archive listing exploded")
		}}
		cm := s2Manager(t, repo, s2Store(t), defaultCleanupConfig(), nil)
		cm.archivedRetentionCleanup(ctx) // must not panic
	})

	t.Run("zero retention keeps archive", func(t *testing.T) {
		listed := 0
		repo := &s2FakeRepo{
			listArchivedCameras: func(context.Context) ([]storage.CameraRow, error) {
				return []storage.CameraRow{{ID: "arch1", ArchiveRetentionDays: 0}}, nil
			},
			listExpiredArchived: func(context.Context, string, int) ([]model.Recording, error) {
				listed++
				return nil, nil
			},
		}
		cm := s2Manager(t, repo, s2Store(t), defaultCleanupConfig(), nil)
		cm.archivedRetentionCleanup(ctx)
		require.Equal(t, 0, listed)
	})

	t.Run("errors are isolated per camera", func(t *testing.T) {
		repo := &s2FakeRepo{
			listArchivedCameras: func(context.Context) ([]storage.CameraRow, error) {
				return []storage.CameraRow{
					{ID: "arch1", ArchiveRetentionDays: 3},
					{ID: "arch2", ArchiveRetentionDays: 3},
				}, nil
			},
			listExpiredArchived: func(_ context.Context, cameraID string, _ int) ([]model.Recording, error) {
				switch cameraID {
				case "arch1":
					return nil, errors.New("expired query exploded")
				default:
					return []model.Recording{{ID: "gone"}}, nil
				}
			},
			deleteRecording: func(_ context.Context, id string) error {
				return errors.New("cannot delete " + id)
			},
			countRecordingsCamera: func(_ context.Context, cameraID string) (int, error) {
				return 0, errors.New("count exploded")
			},
		}
		cm := s2Manager(t, repo, s2Store(t), defaultCleanupConfig(), nil)
		cm.archivedRetentionCleanup(ctx)
	})

	t.Run("empty archive group is removed", func(t *testing.T) {
		store := s2Store(t)
		require.NoError(t, store.EnsureCameraDir("arch1"))
		dir := store.CameraDirPath("arch1")
		file := filepath.Join(dir, "archived.mp4")
		s2WriteFile(t, file, 48*time.Hour)
		deletedCameras := []string{}
		repo := &s2FakeRepo{
			listArchivedCameras: func(context.Context) ([]storage.CameraRow, error) {
				return []storage.CameraRow{{ID: "arch1", ArchiveRetentionDays: 1}}, nil
			},
			listExpiredArchived: func(context.Context, string, int) ([]model.Recording, error) {
				return []model.Recording{{ID: "arch-rec", CameraID: "arch1", FilePath: file}}, nil
			},
			countRecordingsCamera: func(context.Context, string) (int, error) { return 0, nil },
			deleteCamera: func(_ context.Context, cameraID string) error {
				deletedCameras = append(deletedCameras, cameraID)
				return nil
			},
		}
		cm := s2Manager(t, repo, store, defaultCleanupConfig(), metrics.NewMetrics())
		cm.archivedRetentionCleanup(ctx)
		require.Equal(t, []string{"arch1"}, deletedCameras)
		_, statErr := os.Stat(dir)
		require.True(t, os.IsNotExist(statErr))
	})

	t.Run("camera delete failure keeps group", func(t *testing.T) {
		store := s2Store(t)
		repo := &s2FakeRepo{
			listArchivedCameras: func(context.Context) ([]storage.CameraRow, error) {
				return []storage.CameraRow{{ID: "arch1", ArchiveRetentionDays: 1}}, nil
			},
			countRecordingsCamera: func(context.Context, string) (int, error) { return 0, nil },
			deleteCamera: func(context.Context, string) error {
				return errors.New("camera delete exploded")
			},
		}
		cm := s2Manager(t, repo, store, defaultCleanupConfig(), nil)
		cm.archivedRetentionCleanup(ctx) // must not panic
	})

	t.Run("camera directory removal failure is tolerated", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("directory permissions behave differently on Windows")
		}
		store := s2Store(t)
		require.NoError(t, store.EnsureCameraDir("arch1"))
		dir := store.CameraDirPath("arch1")
		child := filepath.Join(dir, "locked")
		require.NoError(t, os.MkdirAll(child, 0o755))
		s2WriteFile(t, filepath.Join(child, "f"), 0)
		require.NoError(t, os.Chmod(child, 0o500))
		t.Cleanup(func() { _ = os.Chmod(child, 0o700) })

		repo := &s2FakeRepo{
			listArchivedCameras: func(context.Context) ([]storage.CameraRow, error) {
				return []storage.CameraRow{{ID: "arch1", ArchiveRetentionDays: 1}}, nil
			},
			countRecordingsCamera: func(context.Context, string) (int, error) { return 0, nil },
		}
		cm := s2Manager(t, repo, store, defaultCleanupConfig(), nil)
		cm.archivedRetentionCleanup(ctx) // logs and continues
	})
}

func TestS2OrphanCleanupBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("no cameras", func(t *testing.T) {
		cm := s2Manager(t, &s2FakeRepo{}, s2Store(t), defaultCleanupConfig(), nil)
		cm.orphanFileCleanup(ctx)
	})

	t.Run("camera budget limits a pass", func(t *testing.T) {
		store := s2Store(t)
		cameras := make([]storage.CameraRow, 0, orphanCameraBudgetPerPass+4)
		for i := 0; i < orphanCameraBudgetPerPass+4; i++ {
			cameras = append(cameras, storage.CameraRow{ID: "cam" + string(rune('a'+i%26)) + string(rune('0'+i/26))})
		}
		scanned := 0
		repo := &s2FakeRepo{
			listCameras: func(context.Context) ([]storage.CameraRow, error) { return cameras, nil },
			listRecordingPaths: func(context.Context, string) (map[string]bool, error) {
				scanned++
				return nil, nil
			},
		}
		cm := s2Manager(t, repo, store, defaultCleanupConfig(), nil)
		cm.orphanFileCleanup(ctx)
		require.Equal(t, orphanCameraBudgetPerPass, scanned)
		require.NotEmpty(t, cm.orphanLastCameraID)

		// A second pass starts from the round-robin cursor.
		cm.orphanFileCleanup(ctx)
		require.Equal(t, 2*orphanCameraBudgetPerPass, scanned)
	})

	t.Run("cancelled context stops scanning", func(t *testing.T) {
		store := s2Store(t)
		repo := &s2FakeRepo{listCameras: func(context.Context) ([]storage.CameraRow, error) {
			return []storage.CameraRow{{ID: "cam1"}}, nil
		}}
		cm := s2Manager(t, repo, store, defaultCleanupConfig(), nil)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		cm.orphanFileCleanup(cancelled)
	})

	t.Run("recording path listing error", func(t *testing.T) {
		repo := &s2FakeRepo{listRecordingPaths: func(context.Context, string) (map[string]bool, error) {
			return nil, errors.New("path listing exploded")
		}}
		cm := s2Manager(t, repo, s2Store(t), defaultCleanupConfig(), nil)
		require.Equal(t, 0, cm.cleanOrphansForCamera(ctx, "cam1"))
	})

	t.Run("orphan directories and files are removed", func(t *testing.T) {
		store := s2Store(t)
		cameraID := "cam1"
		require.NoError(t, store.EnsureCameraDir(cameraID))
		dir := store.CameraDirPath(cameraID)
		prefix := store.CameraRecordingPrefix(cameraID)

		orphanFile := filepath.Join(dir, prefix+"old.mp4")
		s2WriteFile(t, orphanFile, 2*time.Hour)
		recentFile := filepath.Join(dir, prefix+"recent.mp4")
		s2WriteFile(t, recentFile, 0)
		registered := filepath.Join(dir, prefix+"registered.mp4")
		s2WriteFile(t, registered, 2*time.Hour)
		unrelated := filepath.Join(dir, "other-camera.mp4")
		s2WriteFile(t, unrelated, 2*time.Hour)
		orphanDir := filepath.Join(dir, prefix+"20260101_000000.tmp")
		require.NoError(t, os.MkdirAll(orphanDir, 0o755))
		ts := time.Now().Add(-2 * time.Hour)
		require.NoError(t, os.Chtimes(orphanDir, ts, ts))
		keptDir := filepath.Join(dir, "junk-dir")
		require.NoError(t, os.MkdirAll(keptDir, 0o755))
		require.NoError(t, os.Chtimes(keptDir, ts, ts))

		repo := &s2FakeRepo{
			listRecordingPaths: func(context.Context, string) (map[string]bool, error) {
				return map[string]bool{filepath.Base(registered): true}, nil
			},
		}
		cm := s2Manager(t, repo, store, defaultCleanupConfig(), metrics.NewMetrics())
		require.Equal(t, 2, cm.cleanOrphansForCamera(ctx, cameraID))

		for _, removed := range []string{orphanFile, orphanDir} {
			_, statErr := os.Stat(removed)
			require.True(t, os.IsNotExist(statErr), "%s should be reclaimed", removed)
		}
		for _, kept := range []string{recentFile, registered, unrelated, keptDir} {
			_, statErr := os.Stat(kept)
			require.NoError(t, statErr, "%s should survive", kept)
		}
	})

	t.Run("removal failures are logged and skipped", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("directory permissions behave differently on Windows")
		}
		store := s2Store(t)
		cameraID := "cam1"
		require.NoError(t, store.EnsureCameraDir(cameraID))
		dir := store.CameraDirPath(cameraID)
		prefix := store.CameraRecordingPrefix(cameraID)
		orphanFile := filepath.Join(dir, prefix+"locked.mp4")
		s2WriteFile(t, orphanFile, 2*time.Hour)
		orphanDir := filepath.Join(dir, prefix+"locked.tmp")
		require.NoError(t, os.MkdirAll(orphanDir, 0o755))
		ts := time.Now().Add(-2 * time.Hour)
		require.NoError(t, os.Chtimes(orphanDir, ts, ts))

		// A read-only camera directory makes unlink/remove-all fail.
		require.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		cm := s2Manager(t, &s2FakeRepo{}, store, defaultCleanupConfig(), metrics.NewMetrics())
		require.Equal(t, 0, cm.cleanOrphansForCamera(ctx, cameraID))
	})
}

func TestS2StaleRecordCleanup(t *testing.T) {
	ctx := context.Background()

	t.Run("pending listing error", func(t *testing.T) {
		repo := &s2FakeRepo{listPendingMJPEG: func(context.Context, string) ([]model.Recording, error) {
			return nil, errors.New("pending query exploded")
		}}
		cm := s2Manager(t, repo, s2Store(t), defaultCleanupConfig(), nil)
		require.Equal(t, 0, cm.fixStaleMJPEGRecords(ctx, "cam1"))
	})

	t.Run("no stale records", func(t *testing.T) {
		existing := filepath.Join(t.TempDir(), "present.mp4")
		s2WriteFile(t, existing, 0)
		repo := &s2FakeRepo{listPendingMJPEG: func(context.Context, string) ([]model.Recording, error) {
			return []model.Recording{{ID: "present", FilePath: existing}}, nil
		}}
		cm := s2Manager(t, repo, s2Store(t), defaultCleanupConfig(), nil)
		require.Equal(t, 0, cm.fixStaleMJPEGRecords(ctx, "cam1"))
	})

	t.Run("merge status update error", func(t *testing.T) {
		repo := &s2FakeRepo{
			listPendingMJPEG: func(context.Context, string) ([]model.Recording, error) {
				return []model.Recording{{ID: "missing", FilePath: filepath.Join(t.TempDir(), "absent.mp4")}}, nil
			},
			setMergeStatus: func(context.Context, []string, string) error {
				return errors.New("status update exploded")
			},
		}
		cm := s2Manager(t, repo, s2Store(t), defaultCleanupConfig(), nil)
		require.Equal(t, 0, cm.fixStaleMJPEGRecords(ctx, "cam1"))
	})

	t.Run("stale records are marked failed", func(t *testing.T) {
		var marked []string
		var status string
		repo := &s2FakeRepo{
			listCameras: func(context.Context) ([]storage.CameraRow, error) {
				return []storage.CameraRow{{ID: "cam1"}}, nil
			},
			listPendingMJPEG: func(context.Context, string) ([]model.Recording, error) {
				return []model.Recording{{ID: "missing-1", FilePath: filepath.Join(t.TempDir(), "a.mp4")}}, nil
			},
			setMergeStatus: func(_ context.Context, ids []string, s string) error {
				marked = append(marked, ids...)
				status = s
				return nil
			},
		}
		cm := s2Manager(t, repo, s2Store(t), defaultCleanupConfig(), nil)
		cm.staleRecordCleanup(ctx)
		require.Equal(t, []string{"missing-1"}, marked)
		require.Equal(t, model.MergeStatusFailed, status)
	})
}

// s2Script writes an executable shell script and returns its path.
func s2Script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffprobe")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	return path
}

func TestS2RepairZeroDurationRecordings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffprobe uses a POSIX shell script")
	}
	ctx := context.Background()
	store := s2Store(t)

	t.Run("missing ffprobe disables repair", func(t *testing.T) {
		cm := s2Manager(t, &s2FakeRepo{}, store, defaultCleanupConfig(), nil)
		cm.SetFFprobePath(filepath.Join(t.TempDir(), "nope"))
		cm.repairZeroDurationRecordings(ctx)
		require.Empty(t, cm.ffprobePath, "unavailable ffprobe must not be retried")
	})

	t.Run("query error", func(t *testing.T) {
		repo := &s2FakeRepo{repairZeroDuration: func(context.Context) ([]model.Recording, error) {
			return nil, errors.New("repair query exploded")
		}}
		cm := s2Manager(t, repo, store, defaultCleanupConfig(), nil)
		cm.SetFFprobePath(s2Script(t, "echo 1"))
		cm.repairZeroDurationRecordings(ctx)
	})

	t.Run("no candidates", func(t *testing.T) {
		cm := s2Manager(t, &s2FakeRepo{}, store, defaultCleanupConfig(), nil)
		cm.SetFFprobePath(s2Script(t, "echo 1"))
		cm.repairZeroDurationRecordings(ctx)
	})

	t.Run("records are repaired or skipped", func(t *testing.T) {
		dir := t.TempDir()
		good := filepath.Join(dir, "good.mp4")
		s2WriteFile(t, good, 0)
		failing := filepath.Join(dir, "failing.mp4")
		s2WriteFile(t, failing, 0)
		garbage := filepath.Join(dir, "garbage.mp4")
		s2WriteFile(t, garbage, 0)
		updateErr := filepath.Join(dir, "update-error.mp4")
		s2WriteFile(t, updateErr, 0)

		// The script's behaviour depends on the probed file name.
		script := s2Script(t, `case "$7" in
  *failing.mp4) exit 1 ;;
  *garbage.mp4) echo not-a-number ;;
  *update-error.mp4) echo 5.5 ;;
  *) echo 32.400000 ;;
esac`)

		var updated []string
		repo := &s2FakeRepo{
			repairZeroDuration: func(context.Context) ([]model.Recording, error) {
				start := time.Now().Add(-time.Hour)
				return []model.Recording{
					{ID: "good", CameraID: "cam1", FilePath: good, StartedAt: start},
					{ID: "missing", CameraID: "cam1", FilePath: filepath.Join(dir, "gone.mp4"), StartedAt: start},
					{ID: "failing", CameraID: "cam1", FilePath: failing, StartedAt: start},
					{ID: "garbage", CameraID: "cam1", FilePath: garbage, StartedAt: start},
					{ID: "update-error", CameraID: "cam1", FilePath: updateErr, StartedAt: start},
				}, nil
			},
			updateRecordingDuration: func(_ context.Context, id string, _ float64, _ time.Time) error {
				if id == "update-error" {
					return errors.New("update exploded")
				}
				updated = append(updated, id)
				return nil
			},
		}
		cm := s2Manager(t, repo, store, defaultCleanupConfig(), nil)
		cm.SetFFprobePath(script)
		cm.repairZeroDurationRecordings(ctx)
		require.Equal(t, []string{"good"}, updated)
	})
}
